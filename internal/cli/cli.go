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
		fmt.Fprintln(stderr, "usage: a2actl <discover|send|wait> [flags]")
		return ExitUsage
	}
	switch args[0] {
	case "discover":
		return runDiscover(args[1:], stdout, stderr)
	case "send":
		return runSend(args[1:], stdout, stderr, getenv)
	case "wait":
		return runWait(args[1:], stdout, stderr, getenv)
	default:
		fmt.Fprintf(stderr, "unknown command %q (want discover|send|wait)\n", args[0])
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
	urlFlag := fs.String("url", "", "agent base URL (card fetched at /.well-known/agent-card.json)")
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
	out := map[string]string{
		"name":     card.Name,
		"url":      card.URL,
		"version":  card.Version,
		"protocol": a2a.ProtocolVersion,
	}
	if card.Description != "" {
		out["description"] = card.Description
	}
	if card.ProtocolVersion != "" {
		out["protocolVersion"] = card.ProtocolVersion
	} else {
		out["protocolVersion"] = a2a.ProtocolVersion
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
	message := fs.String("message", "", "message text to send")
	contextID := fs.String("context", "", "optional A2A context ID (generated when empty)")
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

	client := a2a.NewClient(*urlFlag)
	res, err := client.SendMessage(ctx, token, *message, *contextID)
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
	return ExitOK
}

func runWait(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	urlFlag := fs.String("url", "", "agent JSON-RPC endpoint (url from agent card)")
	tokenEnv := fs.String("token-env", "", "environment variable holding the bearer token")
	taskID := fs.String("task", "", "task ID to poll")
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
	task, raw, err := client.Wait(ctx, token, *taskID)
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
	return ExitOK
}
