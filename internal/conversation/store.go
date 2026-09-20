package conversation

import (
	"context"
	"encoding/json"
	"time"
)

// Conversation is the durable routing scope shared by human and agent turns.
type Conversation struct {
	ID           string    `json:"id"`
	Title        string    `json:"title,omitempty"`
	Participants []string  `json:"participants"`
	ProjectID    string    `json:"projectId,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Message is an immutable transcript entry.
type Message struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversationId"`
	ParentID       string    `json:"parentId,omitempty"`
	Sender         string    `json:"sender"`
	Recipient      string    `json:"recipient"`
	Body           string    `json:"body"`
	Source         string    `json:"source"`
	ContextID      string    `json:"contextId,omitempty"`
	TaskID         string    `json:"taskId,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

// Event is immutable audit metadata. Payload must not contain credentials.
type Event struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversationId"`
	Type           string          `json:"type"`
	CausationID    string          `json:"causationId,omitempty"`
	CorrelationID  string          `json:"correlationId,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// Store is append-only for transcript and audit data. Implementations expose
// reads but deliberately no update or delete operation.
type Store interface {
	EnsureConversation(context.Context, Conversation) error
	AppendMessage(context.Context, Message) error
	AppendEvent(context.Context, Event) error
	ListConversations(context.Context, int) ([]Conversation, error)
	ListMessages(context.Context, string, int) ([]Message, error)
	ListEvents(context.Context, string, int) ([]Event, error)
	Close() error
}
