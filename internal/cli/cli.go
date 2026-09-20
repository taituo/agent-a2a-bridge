package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/agent-a2a-bridge/internal/a2a"
	"github.com/agent-a2a-bridge/internal/conversation"
)

const (
	// ExitOK is success (including wait reaching completed).
	ExitOK = 0
	// ExitUsage covers bad flags, missing env, and bad URLs.
	ExitUsage = 1
	// ExitAuth covers 401/403 and missing bearer values.
	ExitAuth = 2
	// ExitProtocol covers version, content-type, envelope, and field errors.
	ExitProtocol = 3
	// ExitTaskFailed covers terminal failed/rejected/canceled tasks.
	ExitTaskFailed = 4
	// ExitTransport covers network errors, deadlines, and oversized bodies.
	ExitTransport = 5
	// ExitStore covers durable transcript failures.
	ExitStore = 6
)

// exitCode maps typed library errors to stable exit codes.
func exitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var usage *a2a.UsageError
	if errors.As(err, &usage) {
		return ExitUsage
	}
	var auth *a2a.AuthError
	if errors.As(err, &auth) {
		return ExitAuth
	}
	var proto *a2a.ProtocolError
	if errors.As(err, &proto) {
		return ExitProtocol
	}
	var failed *a2a.TaskFailedError
	if errors.As(err, &failed) {
		return ExitTaskFailed
	}
	var transport *a2a.TransportError
	if errors.As(err, &transport) {
		return ExitTransport
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ExitTransport
	}
	return ExitTransport
}

// Run dispatches a2actl subcommands. getenv resolves bearer tokens;
// tokens are never printed.
func Run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: a2actl <discover|send|wait|conversations|messages|events> [flags]")
		return ExitUsage
	}
	switch args[0] {
	case "discover":
		return runDiscover(args[1:], stdout, stderr)
	case "send":
		return runSend(args[1:], stdout, stderr, getenv)
	case "wait":
		return runWait(args[1:], stdout, stderr, getenv)
	case "conversations":
		return runConversations(args[1:], stdout, stderr)
	case "messages":
		return runMessages(args[1:], stdout, stderr)
	case "events":
		return runEvents(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return ExitUsage
	}
}

func parseTimeout(s string) (time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, &a2a.UsageError{Msg: "invalid --timeout: " + err.Error()}
	}
	if d <= 0 {
		return 0, &a2a.UsageError{Msg: "--timeout must be positive"}
	}
	return d, nil
}

// bearerFromEnv resolves the token strictly from the named environment
// variable. The value never appears in errors or output.
func bearerFromEnv(envName string, getenv func(string) string) (string, error) {
	if strings.TrimSpace(envName) == "" {
		return "", &a2a.UsageError{Msg: "--token-env is required"}
	}
	if getenv == nil {
		return "", &a2a.UsageError{Msg: "environment lookup unavailable"}
	}
	if v := getenv(envName); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), nil
	}
	return "", &a2a.UsageError{Msg: "environment variable " + envName + " is not set or empty"}
}

func emitJSON(stdout io.Writer, v any) error {
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return &a2a.ProtocolError{Msg: "encode output: " + err.Error()}
	}
	buf = append(buf, '\n')
	_, err = stdout.Write(buf)
	return err
}

func runDiscover(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "agent base or endpoint URL (card fetched at origin /.well-known/agent-card.json)")
	timeoutFlag := fs.String("timeout", "15s", "overall deadline (e.g. 10s, 1m)")
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if strings.TrimSpace(*urlFlag) == "" {
		fmt.Fprintln(stderr, "discover: --url is required")
		return ExitUsage
	}
	timeout, err := parseTimeout(*timeoutFlag)
	if err != nil {
		fmt.Fprintln(stderr, "discover:", err.Error())
		return exitCode(err)
	}
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client := a2a.NewClient(*urlFlag)
	card, err := client.Discover(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "discover:", err.Error())
		return exitCode(err)
	}
	iface, err := card.Interface()
	if err != nil {
		fmt.Fprintln(stderr, "discover:", err.Error())
		return exitCode(err)
	}
	out := map[string]string{
		"name":            card.Name,
		"url":             iface.URL,
		"version":         card.Version,
		"protocol":        a2a.ProtocolVersion,
		"protocolBinding": iface.ProtocolBinding,
		"protocolVersion": iface.ProtocolVersion,
	}
	if card.Description != "" {
		out["description"] = card.Description
	}
	if iface.Tenant != "" {
		out["tenant"] = iface.Tenant
	}
	if err := emitJSON(stdout, out); err != nil {
		fmt.Fprintln(stderr, "discover:", err.Error())
		return exitCode(err)
	}
	return ExitOK
}

func runSend(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "agent JSON-RPC endpoint (url from agent card)")
	tokenEnv := fs.String("token-env", "", "environment variable holding the bearer token")
	tenant := fs.String("tenant", "", "optional tenant from the selected agent card interface")
	message := fs.String("message", "", "message text to send")
	contextID := fs.String("context", "", "optional A2A context ID (generated when empty)")
	storePath := fs.String("store", "", "optional SQLite conversation store path")
	sender := fs.String("sender", "human", "durable transcript sender identity")
	recipient := fs.String("recipient", "peer", "durable transcript recipient identity")
	source := fs.String("source", "cli", "durable transcript source")
	timeoutFlag := fs.String("timeout", "30s", "overall deadline (e.g. 30s, 1m)")
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if strings.TrimSpace(*urlFlag) == "" {
		fmt.Fprintln(stderr, "send: --url is required")
		return ExitUsage
	}
	if strings.TrimSpace(*message) == "" {
		fmt.Fprintln(stderr, "send: --message is required")
		return ExitUsage
	}
	token, err := bearerFromEnv(*tokenEnv, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "send:", err.Error())
		return exitCode(err)
	}
	timeout, err := parseTimeout(*timeoutFlag)
	if err != nil {
		fmt.Fprintln(stderr, "send:", err.Error())
		return exitCode(err)
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if strings.TrimSpace(*contextID) == "" {
		*contextID = a2a.NewID()
	}
	var store conversation.Store
	requestMessageID := a2a.NewID()
	if strings.TrimSpace(*storePath) != "" {
		store, err = conversation.OpenSQLite(*storePath)
		if err != nil {
			fmt.Fprintln(stderr, "send: store:", err)
			return ExitStore
		}
		defer store.Close()
		storeCtx, storeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer storeCancel()
		if err := store.Record(storeCtx,
			conversation.Conversation{ID: *contextID, Participants: []string{*sender, *recipient}},
			[]conversation.Message{{ID: requestMessageID, ConversationID: *contextID, Sender: *sender, Recipient: *recipient, Body: *message, Source: *source, ContextID: *contextID}},
			[]conversation.Event{{ID: a2a.NewID(), ConversationID: *contextID, Type: "send_started", CausationID: requestMessageID, CorrelationID: *contextID}},
		); err != nil {
			fmt.Fprintln(stderr, "send: store:", err)
			return ExitStore
		}
	}

	client := a2a.NewClient(*urlFlag)
	client.Tenant = strings.TrimSpace(*tenant)
	res, err := client.SendMessageWithID(ctx, token, *message, *contextID, requestMessageID)
	var storeErr error
	if store != nil {
		storeCtx, storeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		storeErr = recordSendResult(storeCtx, store, *contextID, requestMessageID, *sender, *recipient, *source, res, err)
		storeCancel()
		if storeErr != nil {
			fmt.Fprintln(stderr, "send: store:", storeErr)
		}
	}
	// TaskFailedError still carries an emittable payload; print first.
	if res != nil {
		var payload any
		if res.Kind == "task" {
			payload = map[string]any{"type": "task", "task": json.RawMessage(res.TaskRaw)}
		} else {
			payload = map[string]any{"type": "message", "message": json.RawMessage(res.MessageRaw)}
		}
		if werr := emitJSON(stdout, payload); werr != nil {
			fmt.Fprintln(stderr, "send:", werr.Error())
			return exitCode(werr)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "send:", err.Error())
		return exitCode(err)
	}
	if storeErr != nil {
		return ExitStore
	}
	return ExitOK
}

func runWait(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "agent JSON-RPC endpoint (url from agent card)")
	tokenEnv := fs.String("token-env", "", "environment variable holding the bearer token")
	tenant := fs.String("tenant", "", "optional tenant from the selected agent card interface")
	taskID := fs.String("task", "", "task ID to poll")
	storePath := fs.String("store", "", "optional SQLite conversation store path")
	conversationID := fs.String("conversation", "", "conversation ID used for durable wait events")
	sender := fs.String("sender", "human", "durable transcript response recipient")
	recipient := fs.String("recipient", "peer", "durable transcript response sender")
	source := fs.String("source", "cli", "durable transcript source")
	timeoutFlag := fs.String("timeout", "60s", "overall deadline (e.g. 60s, 5m)")
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if strings.TrimSpace(*urlFlag) == "" {
		fmt.Fprintln(stderr, "wait: --url is required")
		return ExitUsage
	}
	if strings.TrimSpace(*taskID) == "" {
		fmt.Fprintln(stderr, "wait: --task is required")
		return ExitUsage
	}
	if (strings.TrimSpace(*storePath) == "") != (strings.TrimSpace(*conversationID) == "") {
		fmt.Fprintln(stderr, "wait: --store and --conversation must be supplied together")
		return ExitUsage
	}
	token, err := bearerFromEnv(*tokenEnv, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "wait:", err.Error())
		return exitCode(err)
	}
	timeout, err := parseTimeout(*timeoutFlag)
	if err != nil {
		fmt.Fprintln(stderr, "wait:", err.Error())
		return exitCode(err)
	}
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client := a2a.NewClient(*urlFlag)
	client.Tenant = strings.TrimSpace(*tenant)
	task, raw, err := client.Wait(ctx, token, *taskID)
	var storeErr error
	if strings.TrimSpace(*storePath) != "" && strings.TrimSpace(*conversationID) != "" {
		store, openErr := conversation.OpenSQLite(*storePath)
		if openErr != nil {
			fmt.Fprintln(stderr, "wait: store:", openErr)
			return ExitStore
		}
		defer store.Close()
		storeCtx, storeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer storeCancel()
		payload, _ := json.Marshal(map[string]any{"taskId": *taskID, "state": func() string {
			if task != nil {
				return task.Status.State
			}
			return ""
		}(), "error": safeError(err)})
		var messages []conversation.Message
		if task != nil && task.Status.Message != nil {
			m := task.Status.Message
			messages = append(messages, conversation.Message{ID: m.MessageID, ConversationID: *conversationID, Sender: *recipient, Recipient: *sender, Body: messageText(m), Source: *source, ContextID: m.ContextID, TaskID: task.ID})
		}
		storeErr = store.Record(storeCtx, conversation.Conversation{ID: *conversationID, Participants: []string{*sender, *recipient}}, messages, []conversation.Event{{ID: a2a.NewID(), ConversationID: *conversationID, Type: "wait_finished", CorrelationID: *conversationID, Payload: payload}})
		if storeErr != nil {
			fmt.Fprintln(stderr, "wait: store:", storeErr)
		}
	}
	if task != nil && len(raw) > 0 {
		if werr := emitJSON(stdout, map[string]any{"task": json.RawMessage(raw)}); werr != nil {
			fmt.Fprintln(stderr, "wait:", werr.Error())
			return exitCode(werr)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "wait:", err.Error())
		return exitCode(err)
	}
	if storeErr != nil {
		return ExitStore
	}
	return ExitOK
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	return conversation.Redact(err.Error())
}

func messageText(m *a2a.Message) string {
	if m == nil {
		return ""
	}
	var parts []string
	for _, p := range m.Parts {
		parts = append(parts, p.Text)
	}
	return strings.Join(parts, "\n")
}

func recordSendResult(ctx context.Context, store conversation.Store, conversationID, causationID, sender, recipient, source string, res *a2a.SendResult, sendErr error) error {
	typeName := "send_completed"
	payload := map[string]any{"error": safeError(sendErr)}
	if sendErr != nil {
		typeName = "send_failed"
	}
	var messages []conversation.Message
	if res != nil && res.Task != nil {
		payload["taskId"] = res.Task.ID
		payload["state"] = res.Task.Status.State
		if res.Task.Status.Message != nil {
			m := res.Task.Status.Message
			messages = append(messages, conversation.Message{ID: m.MessageID, ConversationID: conversationID, ParentID: causationID, Sender: recipient, Recipient: sender, Body: messageText(m), Source: source, ContextID: m.ContextID, TaskID: res.Task.ID})
		}
	}
	if res != nil && res.Message != nil {
		m := res.Message
		messages = append(messages, conversation.Message{ID: m.MessageID, ConversationID: conversationID, ParentID: causationID, Sender: recipient, Recipient: sender, Body: messageText(m), Source: source, ContextID: m.ContextID, TaskID: m.TaskID})
	}
	raw, _ := json.Marshal(payload)
	return store.Record(ctx, conversation.Conversation{ID: conversationID, Participants: []string{sender, recipient}}, messages, []conversation.Event{{ID: a2a.NewID(), ConversationID: conversationID, Type: typeName, CausationID: causationID, CorrelationID: conversationID, Payload: raw}})
}

func openReadStore(path string, stderr io.Writer) (*conversation.SQLiteStore, int) {
	if strings.TrimSpace(path) == "" {
		fmt.Fprintln(stderr, "--store is required")
		return nil, ExitUsage
	}
	s, err := conversation.OpenSQLiteReadOnly(path)
	if err != nil {
		fmt.Fprintln(stderr, "store:", err)
		return nil, ExitStore
	}
	return s, ExitOK
}

func readFlags(name string, args []string, stderr io.Writer, needConversation bool) (string, string, int, int) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	path := fs.String("store", "", "SQLite conversation store path")
	id := fs.String("conversation", "", "conversation ID")
	limit := fs.Int("limit", 100, "maximum rows (1-1000)")
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return "", "", 0, ExitUsage
	}
	if needConversation && strings.TrimSpace(*id) == "" {
		fmt.Fprintln(stderr, name+": --conversation is required")
		return "", "", 0, ExitUsage
	}
	return *path, *id, *limit, ExitOK
}

func runConversations(args []string, stdout, stderr io.Writer) int {
	path, _, limit, code := readFlags("conversations", args, stderr, false)
	if code != 0 {
		return code
	}
	s, code := openReadStore(path, stderr)
	if code != 0 {
		return code
	}
	defer s.Close()
	rows, err := s.ListConversations(context.Background(), limit)
	if err != nil {
		fmt.Fprintln(stderr, "conversations:", err)
		return ExitStore
	}
	if err := emitJSON(stdout, rows); err != nil {
		return ExitStore
	}
	return ExitOK
}
func runMessages(args []string, stdout, stderr io.Writer) int {
	path, id, limit, code := readFlags("messages", args, stderr, true)
	if code != 0 {
		return code
	}
	s, code := openReadStore(path, stderr)
	if code != 0 {
		return code
	}
	defer s.Close()
	rows, err := s.ListMessages(context.Background(), id, limit)
	if err != nil {
		fmt.Fprintln(stderr, "messages:", err)
		return ExitStore
	}
	if err := emitJSON(stdout, rows); err != nil {
		return ExitStore
	}
	return ExitOK
}
func runEvents(args []string, stdout, stderr io.Writer) int {
	path, id, limit, code := readFlags("events", args, stderr, true)
	if code != 0 {
		return code
	}
	s, code := openReadStore(path, stderr)
	if code != 0 {
		return code
	}
	defer s.Close()
	rows, err := s.ListEvents(context.Background(), id, limit)
	if err != nil {
		fmt.Fprintln(stderr, "events:", err)
		return ExitStore
	}
	if err := emitJSON(stdout, rows); err != nil {
		return ExitStore
	}
	return ExitOK
}
