package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const testBearer = "synthetic-test-token-abc123"

func getenvFor(env, val string) func(string) string {
	return func(k string) string {
		if k == env {
			return val
		}
		return ""
	}
}

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

func TestDiscoverExitOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"hura","version":"0.21.3","supportedInterfaces":[{"url":"http://example.invalid/a2a/v1","protocolBinding":"JSONRPC","protocolVersion":"1.0"}]}`))
	}))
	defer srv.Close()
	var out, errBuf bytes.Buffer
	code := Run([]string{"discover", "--url", srv.URL}, &out, &errBuf, getenvFor("X", ""))
	if code != ExitOK {
		t.Fatalf("want 0, got %d (%s)", code, errBuf.String())
	}
	var decoded map[string]string
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if decoded["name"] != "hura" || decoded["url"] != "http://example.invalid/a2a/v1" || decoded["protocolVersion"] != "1.0" {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestTokenEnvRequiredAndNeverPrinted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(rpcEnvelope(decodeID(r), `{"message":{"role":"ROLE_AGENT","messageId":"m1","parts":[{"text":"ok"}]}}`)))
	}))
	defer srv.Close()

	// Missing env -> usage.
	var out, errBuf bytes.Buffer
	code := Run([]string{"send", "--url", srv.URL, "--token-env", "MISSING_ENV", "--message", "hi"}, &out, &errBuf, getenvFor("OTHER", "x"))
	if code != ExitUsage {
		t.Fatalf("want %d, got %d", ExitUsage, code)
	}

	// Auth failure must not echo the token.
	srv401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv401.Close()
	out.Reset()
	errBuf.Reset()
	code = Run([]string{"send", "--url", srv401.URL, "--token-env", "A2A_TOKEN", "--message", "hi"}, &out, &errBuf, getenvFor("A2A_TOKEN", testBearer))
	if code != ExitAuth {
		t.Fatalf("want %d, got %d (%s)", ExitAuth, code, errBuf.String())
	}
	if strings.Contains(out.String(), testBearer) || strings.Contains(errBuf.String(), testBearer) {
		t.Fatal("bearer token leaked into output")
	}
}

func TestProtocolErrorMapsToExit3(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","error":{"code":-32601,"message":"Method not found"}}`))
	}))
	defer srv.Close()
	var out, errBuf bytes.Buffer
	code := Run([]string{"send", "--url", srv.URL, "--token-env", "T", "--message", "hi"}, &out, &errBuf, getenvFor("T", testBearer))
	if code != ExitProtocol {
		t.Fatalf("want %d, got %d (%s)", ExitProtocol, code, errBuf.String())
	}
}

func TestSendAndWaitExitCodes(t *testing.T) {
	var polls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "SendMessage":
			_, _ = w.Write([]byte(rpcEnvelope(req.ID, `{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}}`)))
		case "GetTask":
			if atomic.AddInt64(&polls, 1) == 1 {
				_, _ = w.Write([]byte(rpcEnvelope(req.ID, `{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}`)))
			} else {
				_, _ = w.Write([]byte(rpcEnvelope(req.ID, `{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_REJECTED"}}`)))
			}
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer srv.Close()

	var out, errBuf bytes.Buffer
	code := Run([]string{"send", "--url", srv.URL, "--token-env", "T", "--message", "hi"}, &out, &errBuf, getenvFor("T", testBearer))
	if code != ExitOK {
		t.Fatalf("send want 0, got %d (%s)", code, errBuf.String())
	}
	if strings.Contains(out.String(), testBearer) {
		t.Fatal("token leaked into send output")
	}
	out.Reset()
	errBuf.Reset()
	code = Run([]string{"wait", "--url", srv.URL, "--token-env", "T", "--task", "t1", "--timeout", "5s"}, &out, &errBuf, getenvFor("T", testBearer))
	if code != ExitTaskFailed {
		t.Fatalf("wait want %d, got %d (%s)", ExitTaskFailed, code, errBuf.String())
	}
	var decoded struct {
		Task map[string]any `json:"task"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("wait output not JSON: %v", err)
	}
}

func TestSendWaitTenantFlags(t *testing.T) {
	const tenant = "cli-tenant"
	var sendTenant, getTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		got := ""
		if raw, ok := req.Params["tenant"]; ok {
			_ = json.Unmarshal(raw, &got)
		}
		switch req.Method {
		case "SendMessage":
			sendTenant = got
			_, _ = w.Write([]byte(rpcEnvelope(req.ID, `{"task":{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_WORKING"}}}`)))
		case "GetTask":
			getTenant = got
			_, _ = w.Write([]byte(rpcEnvelope(req.ID, `{"id":"t1","contextId":"c1","status":{"state":"TASK_STATE_COMPLETED"}}`)))
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer srv.Close()

	var out, errBuf bytes.Buffer
	if code := Run([]string{"send", "--url", srv.URL, "--token-env", "T", "--tenant", tenant, "--message", "hi"}, &out, &errBuf, getenvFor("T", testBearer)); code != ExitOK {
		t.Fatalf("send want 0, got %d (%s)", code, errBuf.String())
	}
	if sendTenant != tenant {
		t.Fatalf("SendMessage tenant = %q, want %q", sendTenant, tenant)
	}
	out.Reset()
	errBuf.Reset()
	if code := Run([]string{"wait", "--url", srv.URL, "--token-env", "T", "--tenant", tenant, "--task", "t1", "--timeout", "5s"}, &out, &errBuf, getenvFor("T", testBearer)); code != ExitOK {
		t.Fatalf("wait want 0, got %d (%s)", code, errBuf.String())
	}
	if getTenant != tenant {
		t.Fatalf("GetTask tenant = %q, want %q", getTenant, tenant)
	}
}
