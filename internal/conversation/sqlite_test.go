package conversation

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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
	if err := s.AppendEvent(ctx, Event{ID: "e1", ConversationID: "c1", Type: "sent", Payload: json.RawMessage(`{"authorization":"Bearer abc123"}`)}); err != nil {
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
	if len(events) != 1 || strings.Contains(string(events[0].Payload), "abc123") {
		t.Fatalf("event was not redacted: %s", events[0].Payload)
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
