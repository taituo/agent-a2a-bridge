package a2a

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// AgentCardPath is the official A2A 1.0 discovery path.
	AgentCardPath = "/.well-known/agent-card.json"
	// VersionHeader is sent on every A2A call and validated when present.
	VersionHeader = "A2A-Version"
	// JSONRPCVersion is the only envelope version accepted.
	JSONRPCVersion = "2.0"

	// DefaultMaxBodyBytes bounds every response body (discovery + JSON-RPC).
	DefaultMaxBodyBytes = 1 << 20 // 1 MiB
	// DefaultTimeout bounds any single HTTP call when the caller has no deadline.
	DefaultTimeout = 15 * time.Second
	// DefaultPollInterval is the initial wait delay; it doubles up to MaxPollInterval.
	DefaultPollInterval = 200 * time.Millisecond
	// DefaultMaxPollInterval caps the bounded backoff.
	DefaultMaxPollInterval = 2 * time.Second
)

// Client is an A2A 1.0 JSON-RPC client. BaseURL is the agent endpoint
// (the `url` from its Agent Card, e.g. http://host:9900 or
// http://host:18789/a2a/v1). Zero values are filled by NewClient.
type Client struct {
	BaseURL         string
	HTTP            *http.Client
	MaxBodyBytes    int64
	PollInterval    time.Duration
	MaxPollInterval time.Duration
}

// NewClient returns a Client with sane defaults over the stdlib transport.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL:         strings.TrimRight(baseURL, "/"),
		HTTP:            &http.Client{Timeout: DefaultTimeout},
		MaxBodyBytes:    DefaultMaxBodyBytes,
		PollInterval:    DefaultPollInterval,
		MaxPollInterval: DefaultMaxPollInterval,
	}
}

func (c *Client) maxBody() int64 {
	if c.MaxBodyBytes > 0 {
		return c.MaxBodyBytes
	}
	return DefaultMaxBodyBytes
}

func (c *Client) pollInterval() time.Duration {
	if c.PollInterval > 0 {
		return c.PollInterval
	}
	return DefaultPollInterval
}

func (c *Client) maxPollInterval() time.Duration {
	if c.MaxPollInterval > 0 {
		return c.MaxPollInterval
	}
	return DefaultMaxPollInterval
}

// discoveryURL resolves baseURL to the official card path. If baseURL
// already ends with the card path it is used as-is.
func discoveryURL(baseURL string) (string, error) {
	trimmed := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(trimmed, AgentCardPath) {
		if _, err := url.ParseRequestURI(trimmed); err != nil {
			return "", &UsageError{Msg: "invalid discovery url: " + err.Error()}
		}
		return trimmed, nil
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", &UsageError{Msg: "invalid url (want scheme://host[:port][/prefix])"}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", &UsageError{Msg: "invalid url scheme (want http or https)"}
	}
	return trimmed + AgentCardPath, nil
}

// newID returns a hex request/message identifier.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// checkVersionHeader rejects a response that explicitly declares a
// non-1.0 A2A version. A missing header is accepted.
func checkVersionHeader(h http.Header) error {
	if v := strings.TrimSpace(h.Get(VersionHeader)); v != "" && v != ProtocolVersion {
		return &ProtocolError{Msg: "unsupported " + VersionHeader + " " + v + " (want 1.0)"}
	}
	return nil
}

// checkContentType requires an application/json content type.
func checkContentType(h http.Header) error {
	ct := strings.TrimSpace(h.Get("Content-Type"))
	if ct == "" {
		return &ProtocolError{Msg: "missing Content-Type (want application/json)"}
	}
	media := strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	if media != "application/json" {
		return &ProtocolError{Msg: "unexpected Content-Type " + ct + " (want application/json)"}
	}
	return nil
}

// readBounded reads at most max+1 bytes so oversize bodies are detectable.
func readBounded(r io.Reader, max int64) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, &TransportError{Msg: "read response body: " + err.Error()}
	}
	if int64(len(buf)) > max {
		return nil, &TransportError{Msg: fmt.Sprintf("response body exceeds %d bytes", max), Oversize: true}
	}
	return buf, nil
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var uerr interface{ Timeout() bool }
	if errors.As(err, &uerr) && uerr.Timeout() {
		return true
	}
	return false
}

// doJSON POSTs a JSON-RPC envelope and decodes the response envelope.
// It enforces version header, content type, body limit, and auth mapping.
func (c *Client) doJSON(ctx context.Context, token string, reqBody rpcRequest) (*rpcResponse, http.Header, error) {
	if strings.TrimSpace(token) == "" {
		return nil, nil, &AuthError{Msg: "missing bearer token"}
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, &UsageError{Msg: "encode request: " + err.Error()}
	}
	if _, err := url.Parse(c.BaseURL); err != nil || c.BaseURL == "" {
		return nil, nil, &UsageError{Msg: "invalid url (want scheme://host[:port][/prefix])"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL, bytes.NewReader(raw))
	if err != nil {
		return nil, nil, &UsageError{Msg: "build request: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(VersionHeader, ProtocolVersion)
	req.Header.Set("Authorization", "Bearer "+token)

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if isTimeout(err) || ctx.Err() != nil {
			return nil, nil, &TransportError{Msg: "request failed: " + err.Error(), Timeout: true}
		}
		return nil, nil, &TransportError{Msg: "request failed: " + err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, nil, &AuthError{Status: resp.StatusCode, Msg: "peer rejected credentials"}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, nil, &ProtocolError{Msg: fmt.Sprintf("unexpected HTTP status %d", resp.StatusCode)}
	}
	if err := checkVersionHeader(resp.Header); err != nil {
		return nil, nil, err
	}
	if err := checkContentType(resp.Header); err != nil {
		return nil, nil, err
	}
	body, err := readBounded(resp.Body, c.maxBody())
	if err != nil {
		return nil, nil, err
	}
	var env rpcResponse
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&env); err != nil {
		return nil, nil, &ProtocolError{Msg: "malformed JSON-RPC response: " + err.Error()}
	}
	if env.JSONRPC != JSONRPCVersion {
		return nil, nil, &ProtocolError{Msg: "unsupported jsonrpc version " + env.JSONRPC + " (want 2.0)"}
	}
	if env.Error != nil {
		msg := strings.TrimSpace(env.Error.Message)
		if msg == "" {
			msg = "unknown JSON-RPC error"
		}
		return nil, nil, &ProtocolError{Msg: fmt.Sprintf("peer JSON-RPC error %d: %s", env.Error.Code, msg)}
	}
	if len(env.Result) == 0 {
		return nil, nil, &ProtocolError{Msg: "missing result in JSON-RPC response"}
	}
	return &env, resp.Header, nil
}

// Discover fetches and validates the Agent Card at the official path.
func (c *Client) Discover(ctx context.Context) (*AgentCard, error) {
	target, err := discoveryURL(c.BaseURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, &UsageError{Msg: "build request: " + err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set(VersionHeader, ProtocolVersion)

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if isTimeout(err) || ctx.Err() != nil {
			return nil, &TransportError{Msg: "discover failed: " + err.Error(), Timeout: true}
		}
		return nil, &TransportError{Msg: "discover failed: " + err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &AuthError{Status: resp.StatusCode, Msg: "peer rejected discovery request"}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &ProtocolError{Msg: fmt.Sprintf("unexpected HTTP status %d", resp.StatusCode)}
	}
	if err := checkVersionHeader(resp.Header); err != nil {
		return nil, err
	}
	if err := checkContentType(resp.Header); err != nil {
		return nil, err
	}
	body, err := readBounded(resp.Body, c.maxBody())
	if err != nil {
		return nil, err
	}
	var card AgentCard
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&card); err != nil {
		return nil, &ProtocolError{Msg: "malformed agent card: " + err.Error()}
	}
	if err := ValidateCard(&card); err != nil {
		return nil, err
	}
	return &card, nil
}

// ValidateCard enforces the minimum fields needed for a call.
func ValidateCard(card *AgentCard) error {
	if card == nil {
		return &ProtocolError{Msg: "missing agent card"}
	}
	if strings.TrimSpace(card.Name) == "" {
		return &ProtocolError{Msg: "agent card missing name"}
	}
	if strings.TrimSpace(card.URL) == "" {
		return &ProtocolError{Msg: "agent card missing url"}
	}
	u, err := url.Parse(card.URL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return &ProtocolError{Msg: "agent card has invalid url"}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return &ProtocolError{Msg: "agent card url scheme must be http or https"}
	}
	if strings.TrimSpace(card.Version) == "" {
		return &ProtocolError{Msg: "agent card missing version"}
	}
	if strings.TrimSpace(card.ProtocolVersion) != "" && card.ProtocolVersion != ProtocolVersion {
		return &ProtocolError{Msg: "unsupported agent card protocolVersion " + card.ProtocolVersion + " (want 1.0)"}
	}
	return nil
}

type sendMessageParams struct {
	Message Message `json:"message"`
}

// SendMessage sends an authenticated A2A 1.0 message/send request.
// contextID may be empty (a fresh one is generated for idempotency scoping).
// A terminal-failure Task is returned alongside a *TaskFailedError so the
// caller can still emit the task payload.
func (c *Client) SendMessage(ctx context.Context, token, text, contextID string) (*SendResult, error) {
	if strings.TrimSpace(text) == "" {
		return nil, &UsageError{Msg: "message text must not be empty"}
	}
	if contextID == "" {
		contextID = newID()
	}
	msg := Message{
		Kind:      "message",
		Role:      "user",
		MessageID: newID(),
		ContextID: contextID,
		Parts:     []TextPart{{Kind: "text", Text: text}},
	}
	params, err := json.Marshal(sendMessageParams{Message: msg})
	if err != nil {
		return nil, &UsageError{Msg: "encode message: " + err.Error()}
	}
	env, _, err := c.doJSON(ctx, token, rpcRequest{
		JSONRPC: JSONRPCVersion,
		ID:      newID(),
		Method:  "message/send",
		Params:  params,
	})
	if err != nil {
		return nil, err
	}
	return parseSendResult(env.Result)
}

// parseSendResult accepts either {"kind":"task",...} or {"kind":"message",...}.
func parseSendResult(raw json.RawMessage) (*SendResult, error) {
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, &ProtocolError{Msg: "malformed message/send result: " + err.Error()}
	}
	switch probe.Kind {
	case "task":
		var task Task
		if err := json.Unmarshal(raw, &task); err != nil {
			return nil, &ProtocolError{Msg: "malformed task result: " + err.Error()}
		}
		if strings.TrimSpace(task.ID) == "" {
			return nil, &ProtocolError{Msg: "task result missing id"}
		}
		if strings.TrimSpace(task.ContextID) == "" {
			return nil, &ProtocolError{Msg: "task result missing contextId"}
		}
		if strings.TrimSpace(task.Status.State) == "" {
			return nil, &ProtocolError{Msg: "task result missing status.state"}
		}
		res := &SendResult{Kind: "task", Task: &task, TaskRaw: append(json.RawMessage(nil), raw...)}
		if IsFailure(task.Status.State) {
			return res, &TaskFailedError{State: task.Status.State, TaskID: task.ID}
		}
		if !IsTerminal(task.Status.State) && task.Status.State != StateSubmitted && task.Status.State != StateWorking && task.Status.State != StateInputRequired {
			return nil, &ProtocolError{Msg: "unknown task state " + task.Status.State}
		}
		return res, nil
	case "message":
		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, &ProtocolError{Msg: "malformed message result: " + err.Error()}
		}
		if strings.TrimSpace(msg.MessageID) == "" && len(msg.Parts) == 0 {
			return nil, &ProtocolError{Msg: "message result missing messageId/parts"}
		}
		return &SendResult{Kind: "message", Message: &msg, MessageRaw: append(json.RawMessage(nil), raw...)}, nil
	default:
		return nil, &ProtocolError{Msg: "message/send result kind must be task or message"}
	}
}

type getTaskParams struct {
	ID string `json:"id"`
}

// GetTask fetches one task snapshot via tasks/get. Raw preserves unknown
// fields for stable output; a terminal-failure state also returns
// *TaskFailedError alongside the task.
func (c *Client) GetTask(ctx context.Context, token, taskID string) (*Task, json.RawMessage, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, nil, &UsageError{Msg: "task id must not be empty"}
	}
	params, err := json.Marshal(getTaskParams{ID: taskID})
	if err != nil {
		return nil, nil, &UsageError{Msg: "encode tasks/get params: " + err.Error()}
	}
	env, _, err := c.doJSON(ctx, token, rpcRequest{
		JSONRPC: JSONRPCVersion,
		ID:      newID(),
		Method:  "tasks/get",
		Params:  params,
	})
	if err != nil {
		return nil, nil, err
	}
	var task Task
	if err := json.Unmarshal(env.Result, &task); err != nil {
		return nil, nil, &ProtocolError{Msg: "malformed tasks/get result: " + err.Error()}
	}
	if strings.TrimSpace(task.ID) == "" {
		return nil, nil, &ProtocolError{Msg: "tasks/get result missing id"}
	}
	if strings.TrimSpace(task.Status.State) == "" {
		return nil, nil, &ProtocolError{Msg: "tasks/get result missing status.state"}
	}
	raw := append(json.RawMessage(nil), env.Result...)
	if IsFailure(task.Status.State) {
		return &task, raw, &TaskFailedError{State: task.Status.State, TaskID: task.ID}
	}
	if !IsTerminal(task.Status.State) && task.Status.State != StateSubmitted && task.Status.State != StateWorking && task.Status.State != StateInputRequired {
		return nil, nil, &ProtocolError{Msg: "unknown task state " + task.Status.State}
	}
	return &task, raw, nil
}

// Wait polls tasks/get with bounded backoff until a terminal state or the
// context deadline. It returns the final task and its raw payload; terminal
// failure states return *TaskFailedError alongside the task.
func (c *Client) Wait(ctx context.Context, token, taskID string) (*Task, json.RawMessage, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, nil, &UsageError{Msg: "task id must not be empty"}
	}
	backoff := c.pollInterval()
	maxBackoff := c.maxPollInterval()
	for {
		task, raw, err := c.GetTask(ctx, token, taskID)
		var taskFailed *TaskFailedError
		if errors.As(err, &taskFailed) {
			return task, raw, err
		}
		if err != nil {
			return nil, nil, err
		}
		if IsTerminal(task.Status.State) {
			return task, raw, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, &TransportError{Msg: "wait deadline exceeded for task " + taskID, Timeout: true}
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
