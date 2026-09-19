package a2a

import (
	"context"
	"encoding/json"
	"errors"
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

func TestDiscoverSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != AgentCardPath {
			http.NotFound(w, r)
			return
		}
		requireVersionHeader(t, r)
		jsonHandler(w, `{"name":"koura","description":"test agent","url":"http://example.invalid/a2a/v1","version":"1.0.0","protocolVersion":"1.0"}`)
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
}

func TestDiscoverValidatesMinimumFields(t *testing.T) {
	cases := map[string]string{
		"missing name":    `{"url":"http://example.invalid/","version":"1.0"}`,
		"missing url":     `{"name":"x","version":"1.0"}`,
		"missing version": `{"name":"x","url":"http://example.invalid/"}`,
		"bad url":         `{"name":"x","url":"::not-a-url","version":"1.0"}`,
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
	t.Run("card version mismatch", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			jsonHandler(w, `{"name":"x","url":"http://example.invalid/","version":"1.0","protocolVersion":"2.0"}`)
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
			_, _ = w.Write([]byte(`{"name":"x","url":"http://example.invalid/","version":"1.0"}`))
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

func rpcSuccess(taskOrMsg string) string {
	return `{"jsonrpc":"2.0","id":"1","result":` + taskOrMsg + `}`
}

func TestSendImmediateMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireVersionHeader(t, r)
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Fatalf("bad auth header: %q", got)
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Method != "message/send" || req.JSONRPC != "2.0" {
			t.Fatalf("unexpected request: %+v", req)
		}
		jsonHandler(w, rpcSuccess(`{"kind":"message","role":"agent","messageId":"m1","contextId":"c1","parts":[{"kind":"text","text":"hello"}]}`))
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
	// Unknown fields must survive in Raw.
	if !strings.Contains(string(res.MessageRaw), "hello") {
		t.Fatalf("raw not preserved: %s", res.MessageRaw)
	}
}

func TestSendWorkingThenWaitCompleted(t *testing.T) {
	var polls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "message/send":
			jsonHandler(w, rpcSuccess(`{"kind":"task","id":"t1","contextId":"c1","status":{"state":"working"},"extraUnknown":"keep"}`))
		case "tasks/get":
			n := atomic.AddInt64(&polls, 1)
			if n == 1 {
				jsonHandler(w, rpcSuccess(`{"kind":"task","id":"t1","contextId":"c1","status":{"state":"working"}}`))
			} else {
				jsonHandler(w, rpcSuccess(`{"kind":"task","id":"t1","contextId":"c1","status":{"state":"completed"},"extraUnknown":"keep"}`))
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
	if res.Kind != "task" || res.Task.Status.State != "working" {
		t.Fatalf("unexpected send result: %+v", res.Task)
	}
	task, raw, err := c.Wait(ctx, testToken, "t1")
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if task.Status.State != "completed" {
		t.Fatalf("want completed, got %q", task.Status.State)
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
		jsonHandler(w, rpcSuccess(`{"kind":"task","id":"t9","contextId":"c9","status":{"state":"rejected"}}`))
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
	if task == nil || task.Status.State != "rejected" {
		t.Fatalf("want rejected task payload, got %+v", task)
	}
}

func TestSendRejectedTaskReturnsPayloadAndError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonHandler(w, rpcSuccess(`{"kind":"task","id":"t2","contextId":"c2","status":{"state":"failed"}}`))
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
	t.Run("unknown task state", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			jsonHandler(w, rpcSuccess(`{"kind":"task","id":"t1","contextId":"c1","status":{"state":"teleporting"}}`))
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
		if strings.HasSuffix(r.URL.Path, AgentCardPath) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"` + big + `","url":"http://example.invalid/","version":"1.0"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(rpcSuccess(`{"kind":"task","id":"t1","contextId":"c1","status":{"state":"working"},"pad":"` + big + `"}`)))
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
		jsonHandler(w, `{"name":"slow","url":"http://example.invalid/","version":"1.0"}`)
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
		jsonHandler(w, rpcSuccess(`{"kind":"task","id":"t1","contextId":"c1","status":{"state":"working"}}`))
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
