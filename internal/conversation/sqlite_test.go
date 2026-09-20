package conversation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSQLiteAppendReadAndRedact(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.EnsureConversation(ctx, Conversation{ID: "c1", Participants: []string{"human", "hura"}}); err != nil {
		t.Fatal(err)
	}
	secret := "1234567890:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef"
	if err := s.AppendMessage(ctx, Message{ID: "m1", ConversationID: "c1", Sender: "human", Recipient: "hura", Source: "test", Body: "token=" + secret}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(ctx, Event{ID: "e1", ConversationID: "c1", Type: "sent", Payload: json.RawMessage(`{"authorization":"Bearer abc123","nested":{"api_key":"key-value","items":[{"Password":"pw-value"}]}}`)}); err != nil {
		t.Fatal(err)
	}
	messages, err := s.ListMessages(ctx, "c1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || strings.Contains(messages[0].Body, secret) || !strings.Contains(messages[0].Body, "[REDACTED]") {
		t.Fatalf("message was not redacted: %#v", messages)
	}
	events, err := s.ListEvents(ctx, "c1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || strings.Contains(string(events[0].Payload), "abc123") || strings.Contains(string(events[0].Payload), "key-value") || strings.Contains(string(events[0].Payload), "pw-value") {
		t.Fatalf("event was not redacted: %s", events[0].Payload)
	}
}

func TestSQLiteFileIsPrivateAndReadOnlyOpenDoesNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.db")
	if _, err := OpenSQLiteReadOnly(path); err == nil {
		t.Fatal("read-only open created missing database")
	}
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode=%o want 600", got)
	}
	r, err := OpenSQLiteReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
}

func TestRecordRollsBackAsOneUnit(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "atomic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	err = s.Record(ctx, Conversation{ID: "c1"}, []Message{{ID: "m1", ConversationID: "wrong", Sender: "a", Recipient: "b", Source: "test", Body: "x"}}, nil)
	if err == nil {
		t.Fatal("invalid batch unexpectedly succeeded")
	}
	rows, err := s.ListConversations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("partial conversation committed: %#v", rows)
	}
}

func TestConcurrentOpenExistingStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			x, err := OpenSQLite(path)
			if err == nil {
				err = x.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAppendOnlyRejectsDuplicateIDs(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	_ = s.EnsureConversation(ctx, Conversation{ID: "c1"})
	m := Message{ID: "m1", ConversationID: "c1", Sender: "a", Recipient: "b", Source: "test", Body: "one"}
	if err := s.AppendMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	m.Body = "two"
	if err := s.AppendMessage(ctx, m); err == nil {
		t.Fatal("duplicate immutable id unexpectedly accepted")
	}
}
