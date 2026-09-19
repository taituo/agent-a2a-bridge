package cli

import (
	"bytes"
	"encoding/json"
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

func TestDiscoverExitOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"hura","url":"http://example.invalid/","version":"0.21.3","protocolVersion":"1.0"}`))
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
	if decoded["name"] != "hura" {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestTokenEnvRequiredAndNeverPrinted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"kind":"message","role":"agent","messageId":"m1","parts":[{"kind":"text","text":"ok"}]}}`))
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

func TestSendAndWaitExitCodes(t *testing.T) {
	var polls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "message/send":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"kind":"task","id":"t1","contextId":"c1","status":{"state":"working"}}}`))
		case "tasks/get":
			if atomic.AddInt64(&polls, 1) == 1 {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"kind":"task","id":"t1","contextId":"c1","status":{"state":"working"}}}`))
			} else {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"kind":"task","id":"t1","contextId":"c1","status":{"state":"rejected"}}}`))
			}
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
