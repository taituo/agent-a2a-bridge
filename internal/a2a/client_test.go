package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "test-bearer-token"

func jsonHandler(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func requireVersionHeader(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get(VersionHeader); got != ProtocolVersion {
		t.Fatalf("missing/invalid %s header: %q", VersionHeader, got)
	}
}

// rpcEnvelope builds a JSON-RPC 2.0 response that echoes the request id.
func rpcEnvelope(id json.RawMessage, result string) string {
	if len(id) == 0 {
		id = json.RawMessage(`"1"`)
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, id, result)
}

func decodeID(r *http.Request) json.RawMessage {
	var req struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	return req.ID
}

const validInterface = `{"url":"http://example.invalid/a2a/v1","protocolBinding":"JSONRPC","protocolVersion":"1.0"}`

func cardJSON(interfaces string) string {
	return `{"name":"koura","description":"test agent","version":"1.0.0","supportedInterfaces":[` + interfaces + `]}`
}

func TestDiscoverSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != AgentCardPath {
			http.NotFound(w, r)
			return
		}
		requireVersionHeader(t, r)
		jsonHandler(w, cardJSON(validInterface))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	card, err := c.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if card.Name != "koura" || card.Version != "1.0.0" {
		t.Fatalf("unexpected card: %+v", card)
	}
	iface, err := card.Interface()
	if err != nil {
		t.Fatalf("Interface: %v", err)
	}
	if iface.URL != "http://example.invalid/a2a/v1" || iface.ProtocolVersion != "1.0" {
		t.Fatalf("unexpected interface: %+v", iface)
	}
}

func TestDiscoverInterfaceSelection(t *testing.T) {
	cases := map[string]struct {
		interfaces string
		wantURL    string
	}{
		"skips non-jsonrpc first": {
			interfaces: `{"url":"http://example.invalid/grpc","protocolBinding":"GRPC","protocolVersion":"1.0"},` + validInterface,
			wantURL:    "http://example.invalid/a2a/v1",
		},
		"skips wrong-version jsonrpc": {
			interfaces: `{"url":"http://example.invalid/old","protocolBinding":"JSONRPC","protocolVersion":"0.3"},` + validInterface,
			wantURL:    "http://example.invalid/a2a/v1",
		},
		"case-insensitive binding": {
			interfaces: `{"url":"http://example.invalid/a2a/v1","protocolBinding":"jsonrpc","protocolVersion":"1.0"}`,
			wantURL:    "http://example.invalid/a2a/v1",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				jsonHandler(w, cardJSON(tc.interfaces))
			}))
			defer srv.Close()
			c := NewClient(srv.URL)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			card, err := c.Discover(ctx)
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			iface, err := card.Interface()
			if err != nil {
				t.Fatalf("Interface: %v", err)
			}
			if iface.URL != tc.wantURL {
				t.Fatalf("want %s, got %s", tc.wantURL, iface.URL)
			}
		})
	}
}

func TestDiscoverValidatesMinimumFields(t *testing.T) {
	cases := map[string]string{
		"missing name":           `{"version":"1.0.0","supportedInterfaces":[` + validInterface + `]}`,
		"missing version":        `{"name":"x","supportedInterfaces":[` + validInterface + `]}`,
		"no interfaces":          `{"name":"x","version":"1.0.0"}`,
		"no jsonrpc interface":   `{"name":"x","version":"1.0.0","supportedInterfaces":[{"url":"http://example.invalid/grpc","protocolBinding":"GRPC","protocolVersion":"1.0"}]}`,
		"wrong protocol version": `{"name":"x","version":"1.0.0","supportedInterfaces":[{"url":"http://example.invalid/a2a","protocolBinding":"JSONRPC","protocolVersion":"0.3"}]}`,
		"invalid interface url":  `{"name":"x","version":"1.0.0","supportedInterfaces":[{"url":"::not-a-url","protocolBinding":"JSONRPC","protocolVersion":"1.0"}]}`,
		"unsupported url scheme": `{"name":"x","version":"1.0.0","supportedInterfaces":[{"url":"ftp://example.invalid/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				jsonHandler(w, body)
			}))
			defer srv.Close()
			c := NewClient(srv.URL)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := c.Discover(ctx); err == nil {
				t.Fatal("want protocol error, got nil")
			} else {
				var perr *ProtocolError
				if !errors.As(err, &perr) {
					t.Fatalf("want *ProtocolError, got %T (%v)", err, err)
				}
			}
		})
	}
}

func TestProtocolVersionFailure(t *testing.T) {
	t.Run("card interface version mismatch", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			jsonHandler(w, cardJSON(`{"url":"http://example.invalid/a2a","protocolBinding":"JSONRPC","protocolVersion":"2.0"}`))
		}))
		defer srv.Close()
		c := NewClient(srv.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := c.Discover(ctx)
		var perr *ProtocolError
		if !errors.As(err, &perr) {
			t.Fatalf("want *ProtocolError, got %T (%v)", err, err)
		}
	})
	t.Run("response header mismatch", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set(VersionHeader, "2.0")
			_, _ = w.Write([]byte(cardJSON(validInterface)))
		}))
		defer srv.Close()
		c := NewClient(srv.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := c.Discover(ctx)
		var perr *ProtocolError
		if !errors.As(err, &perr) {
			t.Fatalf("want *ProtocolError, got %T (%v)", err, err)
		}
	})
}

func TestSendImmediateMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireVersionHeader(t, r)
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Fatalf("bad auth header: %q", got)
		}
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != MethodSendMessage || req.JSONRPC != JSONRPCVersion {
			t.Fatalf("unexpected request: %+v", req)
		}
		if strings.Contains(string(req.Params), `"kind"`) {
			t.Fatalf("A2A 1.0 request must not contain a kind discriminator: %s", req.Params)
		}
		var sp sendMessageParams
		if err := json.Unmarshal(req.Params, &sp); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		if !sp.Configuration.ReturnImmediately {
			t.Fatal("SendMessage must request returnImmediately for send-to-wait")
		}
		if sp.Message.Role != RoleUser {
			t.Fatalf("want role %s, got %q", RoleUser, sp.Message.Role)
		}
		if len(sp.Message.Parts) != 1 || sp.Message.Parts[0].Text != "hello" {
			t.Fatalf("unexpected parts: %+v", sp.Message.Parts)
		}
		if sp.Message.MessageID == "" || sp.Message.ContextID == "" {
			t.Fatalf("missing ids: %+v", sp.Message)
		}
		jsonHandler(w, rpcEnvelope(req.ID, `{"message":{"role":"ROLE_AGENT","messageId":"m1","contextId":"c1","parts":[{"text":"hello"}]}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.SendMessage(ctx, testToken, "hello", "")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if res.Kind != "message" || res.Message == nil {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !strings.Contains(string(res.MessageRaw), "hello") {
		t.Fatalf("raw not preserved: %s", res.MessageRaw)
	}
}

func TestSendWorkingThenWaitCompleted(t *testing.T) {
	var polls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case MethodSendMessage:
			jsonHandler(w, rpcEnvelope(req.ID, `{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"},"extraUnknown":"keep"}}`))
		case MethodGetTask:
			n := atomic.AddInt64(&polls, 1)
			if n == 1 {
				jsonHandler(w, rpcEnvelope(req.ID, `{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}`))
			} else {
				jsonHandler(w, rpcEnvelope(req.ID, `{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_COMPLETED"},"extraUnknown":"keep"}`))
			}
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.PollInterval = 5 * time.Millisecond
	c.MaxPollInterval = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.SendMessage(ctx, testToken, "hi", "c1")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if res.Kind != "task" || res.Task.Status.State != StateWorking {
		t.Fatalf("unexpected send result: %+v", res.Task)
	}
	task, raw, err := c.Wait(ctx, testToken, "t1")
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if task.Status.State != StateCompleted {
		t.Fatalf("want %s, got %q", StateCompleted, task.Status.State)
	}
	if !strings.Contains(string(raw), "keep") {
		t.Fatalf("raw unknown fields not preserved: %s", raw)
	}
	if atomic.LoadInt64(&polls) < 2 {
		t.Fatalf("want >=2 polls, got %d", polls)
	}
}

func TestWaitRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonHandler(w, rpcEnvelope(decodeID(r), `{"id":"t9","contextId":"c9","status":{"state":"TASK_STATE_REJECTED"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task, _, err := c.Wait(ctx, testToken, "t9")
	var failed *TaskFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("want *TaskFailedError, got %T (%v)", err, err)
	}
	if task == nil || task.Status.State != StateRejected {
		t.Fatalf("want rejected task payload, got %+v", task)
	}
}

func TestSendRejectedTaskReturnsPayloadAndError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonHandler(w, rpcEnvelope(decodeID(r), `{"task":{"id":"t2","contextId":"c2","status":{"state":"TASK_STATE_FAILED"}}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.SendMessage(ctx, testToken, "hi", "")
	var failed *TaskFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("want *TaskFailedError, got %T (%v)", err, err)
	}
	if res == nil || res.Task == nil {
		t.Fatal("want task payload alongside failure")
	}
}

func TestAuthRequiredIsRecognized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonHandler(w, rpcEnvelope(decodeID(r), `{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_AUTH_REQUIRED"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	task, raw, err := c.GetTask(ctx, testToken, "t1")
	if err != nil {
		t.Fatalf("GetTask must accept interrupted AUTH_REQUIRED, got %v", err)
	}
	if task.Status.State != StateAuthRequired || len(raw) == 0 {
		t.Fatalf("unexpected task: %+v raw=%s", task, raw)
	}
}

func TestSendResultWrapperValidation(t *testing.T) {
	cases := map[string]string{
		"neither":     `{}`,
		"both":        `{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}},"message":{"role":"ROLE_AGENT","messageId":"m1","parts":[{"text":"x"}]}}`,
		"legacy kind": `{"kind":"task","id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}`,
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseSendResult(json.RawMessage(result))
			var perr *ProtocolError
			if !errors.As(err, &perr) {
				t.Fatalf("want *ProtocolError, got %T (%v)", err, err)
			}
		})
	}
}

func TestResponseIDCorrelation(t *testing.T) {
	cases := map[string]struct {
		envelope string
	}{
		"mismatched id": {`{"jsonrpc":"2.0","id":"999","result":{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}}}`},
		"missing id":    {`{"jsonrpc":"2.0","result":{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}}}`},
		"null id":       {`{"jsonrpc":"2.0","id":null,"result":{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}}}`},
		"numeric id":    {`{"jsonrpc":"2.0","id":1,"result":{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}}}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				jsonHandler(w, tc.envelope)
			}))
			defer srv.Close()
			c := NewClient(srv.URL)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := c.SendMessage(ctx, testToken, "hi", "")
			var perr *ProtocolError
			if !errors.As(err, &perr) {
				t.Fatalf("want *ProtocolError, got %T (%v)", err, err)
			}
		})
	}
}

func TestMalformedPayload(t *testing.T) {
	t.Run("invalid json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			jsonHandler(w, `{"jsonrpc": broken`)
		}))
		defer srv.Close()
		c := NewClient(srv.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := c.SendMessage(ctx, testToken, "hi", "")
		var perr *ProtocolError
		if !errors.As(err, &perr) {
			t.Fatalf("want *ProtocolError, got %T (%v)", err, err)
		}
	})
	t.Run("wrong content type", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html>nope</html>`))
		}))
		defer srv.Close()
		c := NewClient(srv.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := c.Discover(ctx)
		var perr *ProtocolError
		if !errors.As(err, &perr) {
			t.Fatalf("want *ProtocolError, got %T (%v)", err, err)
		}
	})
	t.Run("unsupported jsonrpc version", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			jsonHandler(w, `{"jsonrpc":"1.0","id":"1","result":{}}`)
		}))
		defer srv.Close()
		c := NewClient(srv.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := c.SendMessage(ctx, testToken, "hi", "")
		var perr *ProtocolError
		if !errors.As(err, &perr) {
			t.Fatalf("want *ProtocolError, got %T (%v)", err, err)
		}
	})
	t.Run("jsonrpc error object", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			jsonHandler(w, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"Method not found"}}`, decodeID(r)))
		}))
		defer srv.Close()
		c := NewClient(srv.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := c.SendMessage(ctx, testToken, "hi", "")
		var perr *ProtocolError
		if !errors.As(err, &perr) {
			t.Fatalf("want *ProtocolError, got %T (%v)", err, err)
		}
	})
	t.Run("unknown task state", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			jsonHandler(w, rpcEnvelope(decodeID(r), `{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_TELEPORTING"}}`))
		}))
		defer srv.Close()
		c := NewClient(srv.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, err := c.GetTask(ctx, testToken, "t1")
		var perr *ProtocolError
		if !errors.As(err, &perr) {
			t.Fatalf("want *ProtocolError, got %T (%v)", err, err)
		}
	})
	t.Run("task missing contextId", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			jsonHandler(w, rpcEnvelope(decodeID(r), `{"id":"t1","status":{"state":"TASK_STATE_WORKING"}}`))
		}))
		defer srv.Close()
		c := NewClient(srv.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, err := c.GetTask(ctx, testToken, "t1")
		var perr *ProtocolError
		if !errors.As(err, &perr) {
			t.Fatalf("want *ProtocolError, got %T (%v)", err, err)
		}
	})
}

func TestOversizedResponse(t *testing.T) {
	big := strings.Repeat("x", 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, AgentCardPath) {
			_, _ = w.Write([]byte(cardJSON(`{"url":"http://example.invalid/` + big + `","protocolBinding":"JSONRPC","protocolVersion":"1.0"}`)))
			return
		}
		_, _ = w.Write([]byte(rpcEnvelope(decodeID(r), `{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"},"pad":"`+big+`"}}`)))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.MaxBodyBytes = 128
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.Discover(ctx)
	var terr *TransportError
	if !errors.As(err, &terr) || !terr.Oversize {
		t.Fatalf("want oversize *TransportError, got %T (%v)", err, err)
	}
	_, err = c.SendMessage(ctx, testToken, "hi", "")
	if !errors.As(err, &terr) || !terr.Oversize {
		t.Fatalf("want oversize *TransportError, got %T (%v)", err, err)
	}
}

func TestAuthenticationFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Discover(ctx); err == nil {
		t.Fatal("want auth error, got nil")
	} else {
		var aerr *AuthError
		if !errors.As(err, &aerr) {
			t.Fatalf("want *AuthError, got %T (%v)", err, err)
		}
	}
	if _, err := c.SendMessage(ctx, "wrong", "hi", ""); err == nil {
		t.Fatal("want auth error, got nil")
	} else {
		var aerr *AuthError
		if !errors.As(err, &aerr) {
			t.Fatalf("want *AuthError, got %T (%v)", err, err)
		}
	}
	if _, err := c.SendMessage(ctx, "", "hi", ""); err == nil {
		t.Fatal("want auth error for empty token, got nil")
	} else {
		var aerr *AuthError
		if !errors.As(err, &aerr) {
			t.Fatalf("want *AuthError, got %T (%v)", err, err)
		}
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		jsonHandler(w, cardJSON(validInterface))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.Discover(ctx)
	var terr *TransportError
	if !errors.As(err, &terr) || !terr.Timeout {
		t.Fatalf("want timeout *TransportError, got %T (%v)", err, err)
	}
}

func TestWaitDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonHandler(w, rpcEnvelope(decodeID(r), `{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.PollInterval = 5 * time.Millisecond
	c.MaxPollInterval = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, _, err := c.Wait(ctx, testToken, "t1")
	var terr *TransportError
	if !errors.As(err, &terr) || !terr.Timeout {
		t.Fatalf("want deadline *TransportError, got %T (%v)", err, err)
	}
}

func TestStateEnumsAreA2A10(t *testing.T) {
	terminal := []string{StateCompleted, StateFailed, StateRejected, StateCanceled}
	for _, s := range terminal {
		if !IsTerminal(s) {
			t.Fatalf("%s must be terminal", s)
		}
	}
	interrupted := []string{StateSubmitted, StateWorking, StateInputRequired, StateAuthRequired}
	for _, s := range interrupted {
		if IsTerminal(s) {
			t.Fatalf("%s must not be terminal", s)
		}
		if !IsInterrupted(s) {
			t.Fatalf("%s must be recognized as interrupted", s)
		}
	}
	// Legacy lowercase states must not be accepted by a 1.0 client.
	if IsTerminal("completed") || IsInterrupted("working") {
		t.Fatal("legacy lowercase states must not be recognized")
	}
	if !IsFailure(StateFailed) || !IsFailure(StateRejected) || !IsFailure(StateCanceled) {
		t.Fatal("failed/rejected/canceled must map to failure")
	}
	if IsFailure(StateCompleted) || IsFailure(StateAuthRequired) {
		t.Fatal("completed/auth-required must not map to failure")
	}
}

func TestWireMessageHasNoLegacyKind(t *testing.T) {
	b, err := json.Marshal(Message{Role: RoleUser, MessageID: "m1", Parts: []TextPart{{Text: "x"}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "kind") {
		t.Fatalf("A2A 1.0 message must not carry kind: %s", b)
	}
	var part map[string]any
	if err := json.Unmarshal([]byte(`{"text":"x"}`), &part); err != nil {
		t.Fatalf("unmarshal part: %v", err)
	}
	if _, ok := part["kind"]; ok {
		t.Fatal("text part must not carry kind")
	}
}
