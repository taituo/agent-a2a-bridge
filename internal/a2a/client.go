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
// (the selected interface `url` from its Agent Card, e.g.
// http://host:9900 or http://host:18789/a2a/v1). Zero values are filled by
// NewClient.
type Client struct {
	BaseURL         string
	HTTP            *http.Client
	MaxBodyBytes    int64
	PollInterval    time.Duration
	MaxPollInterval time.Duration
	// Tenant is the routing identifier from the selected Agent Card
	// AgentInterface. A2A 1.0 requires it in every request message when the
	// interface declares one; it is omitted when empty. Use
	// AgentInterface.Tenant to set it.
	Tenant string
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

// discoveryURL resolves baseURL to the official Agent Card well-known URI at
// the URL origin root. Per A2A 1.0 / RFC 8615 the card always lives at
// scheme://host[:port]/.well-known/agent-card.json, so any endpoint path such
// as /a2a/v1 is discarded rather than appended to. An input that is already
// the card URL resolves to the same URL. Query and fragment components are
// validated by url.Parse and then dropped, never carried into the result.
func discoveryURL(baseURL string) (string, error) {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		return "", &UsageError{Msg: "invalid url (want scheme://host[:port])"}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", &UsageError{Msg: "invalid url (want scheme://host[:port])"}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", &UsageError{Msg: "invalid url scheme (want http or https)"}
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: AgentCardPath}).String(), nil
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

// checkRPCID verifies the JSON-RPC response id exactly echoes the request id.
// The client always sends a string id, so a missing, null, numeric, or
// different id is rejected.
func checkRPCID(sent string, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return &ProtocolError{Msg: "JSON-RPC response missing id"}
	}
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		return &ProtocolError{Msg: "JSON-RPC response id is not a string matching the request id"}
	}
	if got != sent {
		return &ProtocolError{Msg: fmt.Sprintf("JSON-RPC response id %q does not match request id %q", got, sent)}
	}
	return nil
}

// ensureNoTrailingJSON rejects a body that carries a second JSON value or
// trailing garbage after the first decoded value. A clean end of input is the
// only accepted outcome.
func ensureNoTrailingJSON(dec *json.Decoder) error {
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return &ProtocolError{Msg: "trailing data after JSON value"}
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
// It enforces version header, content type, body limit, response-ID
// correlation, and auth mapping.
func (c *Client) doJSON(ctx context.Context, token string, reqBody rpcRequest) (*rpcResponse, http.Header, error) {
	if strings.TrimSpace(token) == "" {
		return nil, nil, &AuthError{Msg: "missing bearer token"}
	}
	if strings.TrimSpace(reqBody.ID) == "" {
		reqBody.ID = newID()
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
	if err := ensureNoTrailingJSON(dec); err != nil {
		return nil, nil, err
	}
	if env.JSONRPC != JSONRPCVersion {
		return nil, nil, &ProtocolError{Msg: "unsupported jsonrpc version " + env.JSONRPC + " (want 2.0)"}
	}
	if err := checkRPCID(reqBody.ID, env.ID); err != nil {
		return nil, nil, err
	}
	if env.Error != nil && len(env.Result) > 0 {
		return nil, nil, &ProtocolError{Msg: "JSON-RPC response must not contain both result and error"}
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
	if err := ensureNoTrailingJSON(dec); err != nil {
		return nil, err
	}
	if err := ValidateCard(&card); err != nil {
		return nil, err
	}
	return &card, nil
}

// ValidateCard enforces a deliberately narrow operational subset of the A2A
// 1.0 Agent Card: name, version, and at least one JSONRPC interface at
// protocolVersion 1.0 with an http/https URL. It intentionally does NOT
// validate every field the A2A 1.0 schema marks required (for example
// description, capabilities, defaultInputModes, defaultOutputModes, and
// skills); this client only needs enough metadata to place a call. Callers
// that require full card validation must do it separately. Unknown fields are
// ignored for forward compatibility.
func ValidateCard(card *AgentCard) error {
	if card == nil {
		return &ProtocolError{Msg: "missing agent card"}
	}
	if strings.TrimSpace(card.Name) == "" {
		return &ProtocolError{Msg: "agent card missing name"}
	}
	if strings.TrimSpace(card.Version) == "" {
		return &ProtocolError{Msg: "agent card missing version"}
	}
	_, err := card.Interface()
	return err
}

// sendMessageConfiguration requests a non-blocking SendMessage so the client
// receives a task ID immediately and can poll with GetTask / wait.
type sendMessageConfiguration struct {
	ReturnImmediately bool `json:"returnImmediately"`
}

type sendMessageParams struct {
	Message       Message                  `json:"message"`
	Configuration sendMessageConfiguration `json:"configuration"`
	Tenant        string                   `json:"tenant,omitempty"`
}

// SendMessage sends an authenticated A2A 1.0 SendMessage request. contextID
// may be empty (a fresh one is generated for idempotency scoping). It requests
// returnImmediately so a Task response carries an ID the caller can poll.
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
		Role:      RoleUser,
		MessageID: newID(),
		ContextID: contextID,
		Parts:     []TextPart{{Text: text}},
	}
	params, err := json.Marshal(sendMessageParams{
		Message:       msg,
		Configuration: sendMessageConfiguration{ReturnImmediately: true},
		Tenant:        c.Tenant,
	})
	if err != nil {
		return nil, &UsageError{Msg: "encode message: " + err.Error()}
	}
	env, _, err := c.doJSON(ctx, token, rpcRequest{
		JSONRPC: JSONRPCVersion,
		ID:      newID(),
		Method:  MethodSendMessage,
		Params:  params,
	})
	if err != nil {
		return nil, err
	}
	return parseSendResult(env.Result)
}

// parseSendResult accepts the A2A 1.0 SendMessageResponse wrapper, which must
// contain exactly one of `task` or `message` (no `kind` discriminator).
func parseSendResult(raw json.RawMessage) (*SendResult, error) {
	var env struct {
		Task    json.RawMessage `json:"task"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &ProtocolError{Msg: "malformed SendMessage result: " + err.Error()}
	}
	hasTask := len(env.Task) > 0 && string(env.Task) != "null"
	hasMessage := len(env.Message) > 0 && string(env.Message) != "null"
	if hasTask == hasMessage {
		return nil, &ProtocolError{Msg: "SendMessage result must contain exactly one of task or message"}
	}
	if hasTask {
		var task Task
		if err := json.Unmarshal(env.Task, &task); err != nil {
			return nil, &ProtocolError{Msg: "malformed task result: " + err.Error()}
		}
		if err := validateTask(&task); err != nil {
			return nil, err
		}
		res := &SendResult{Kind: "task", Task: &task, TaskRaw: append(json.RawMessage(nil), env.Task...)}
		if IsFailure(task.Status.State) {
			return res, &TaskFailedError{State: task.Status.State, TaskID: task.ID}
		}
		if !IsTerminal(task.Status.State) && !IsInterrupted(task.Status.State) {
			return nil, &ProtocolError{Msg: "unknown task state " + task.Status.State}
		}
		return res, nil
	}
	var msg Message
	if err := json.Unmarshal(env.Message, &msg); err != nil {
		return nil, &ProtocolError{Msg: "malformed message result: " + err.Error()}
	}
	if strings.TrimSpace(msg.MessageID) == "" || len(msg.Parts) == 0 {
		return nil, &ProtocolError{Msg: "message result missing messageId/parts"}
	}
	return &SendResult{Kind: "message", Message: &msg, MessageRaw: append(json.RawMessage(nil), env.Message...)}, nil
}

// validateTask enforces the minimum A2A 1.0 task fields for a call.
func validateTask(task *Task) error {
	if strings.TrimSpace(task.ID) == "" {
		return &ProtocolError{Msg: "task result missing id"}
	}
	if strings.TrimSpace(task.ContextID) == "" {
		return &ProtocolError{Msg: "task result missing contextId"}
	}
	if strings.TrimSpace(task.Status.State) == "" {
		return &ProtocolError{Msg: "task result missing status.state"}
	}
	return nil
}

type getTaskParams struct {
	ID     string `json:"id"`
	Tenant string `json:"tenant,omitempty"`
}

// GetTask fetches one task snapshot via GetTask. Raw preserves unknown
// fields for stable output; a terminal-failure state also returns
// *TaskFailedError alongside the task.
func (c *Client) GetTask(ctx context.Context, token, taskID string) (*Task, json.RawMessage, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, nil, &UsageError{Msg: "task id must not be empty"}
	}
	params, err := json.Marshal(getTaskParams{ID: taskID, Tenant: c.Tenant})
	if err != nil {
		return nil, nil, &UsageError{Msg: "encode GetTask params: " + err.Error()}
	}
	env, _, err := c.doJSON(ctx, token, rpcRequest{
		JSONRPC: JSONRPCVersion,
		ID:      newID(),
		Method:  MethodGetTask,
		Params:  params,
	})
	if err != nil {
		return nil, nil, err
	}
	var task Task
	if err := json.Unmarshal(env.Result, &task); err != nil {
		return nil, nil, &ProtocolError{Msg: "malformed GetTask result: " + err.Error()}
	}
	if err := validateTask(&task); err != nil {
		return nil, nil, err
	}
	raw := append(json.RawMessage(nil), env.Result...)
	if IsFailure(task.Status.State) {
		return &task, raw, &TaskFailedError{State: task.Status.State, TaskID: task.ID}
	}
	if !IsTerminal(task.Status.State) && !IsInterrupted(task.Status.State) {
		return nil, nil, &ProtocolError{Msg: "unknown task state " + task.Status.State}
	}
	return &task, raw, nil
}

// Wait polls GetTask with bounded backoff until a terminal state or the
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
