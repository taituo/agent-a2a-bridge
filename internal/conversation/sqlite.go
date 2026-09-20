package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore is a local, append-only conversation store.
type SQLiteStore struct{ db *sql.DB }

// OpenSQLite opens path, enables integrity-oriented pragmas, and migrates the
// schema. The caller owns Close.
func OpenSQLite(path string) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("sqlite path is required")
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(path)), "file:") {
		return nil, errors.New("sqlite URI paths are not supported; use a filesystem path")
	}
	if err := secureCreate(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &SQLiteStore{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure sqlite: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// OpenSQLiteReadOnly opens an existing store without creating or migrating it.
func OpenSQLiteReadOnly(path string) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("sqlite path is required")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("open existing sqlite: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite read-only: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func secureCreate(path string) error {
	if path == ":memory:" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("create sqlite: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close sqlite: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure sqlite: %w", err)
	}
	return nil
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
	const schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS conversations (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  participants_json TEXT NOT NULL DEFAULT '[]',
  project_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  conversation_id TEXT NOT NULL REFERENCES conversations(id),
  parent_id TEXT NOT NULL DEFAULT '',
  sender TEXT NOT NULL,
  recipient TEXT NOT NULL,
  body TEXT NOT NULL,
  source TEXT NOT NULL,
  context_id TEXT NOT NULL DEFAULT '',
  task_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_conversation_sequence
  ON messages(conversation_id, sequence);
CREATE TABLE IF NOT EXISTS events (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  conversation_id TEXT NOT NULL REFERENCES conversations(id),
  type TEXT NOT NULL,
  causation_id TEXT NOT NULL DEFAULT '',
  correlation_id TEXT NOT NULL DEFAULT '',
  payload_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_conversation_sequence
  ON events(conversation_id, sequence);`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}
	return nil
}

func timestamp(t time.Time) string {
	if t.IsZero() {
		t = time.Now().UTC()
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (s *SQLiteStore) EnsureConversation(ctx context.Context, c Conversation) error {
	if strings.TrimSpace(c.ID) == "" {
		return errors.New("conversation id is required")
	}
	participants, err := json.Marshal(c.Participants)
	if err != nil {
		return fmt.Errorf("encode participants: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO conversations
 (id,title,participants_json,project_id,created_at) VALUES(?,?,?,?,?)
 ON CONFLICT(id) DO NOTHING`, c.ID, Redact(c.Title), string(participants), c.ProjectID, timestamp(c.CreatedAt))
	if err != nil {
		return fmt.Errorf("ensure conversation: %w", err)
	}
	return nil
}

// Record atomically appends a related group of transcript and audit rows.
func (s *SQLiteStore) Record(ctx context.Context, c Conversation, messages []Message, events []Event) error {
	if strings.TrimSpace(c.ID) == "" {
		return errors.New("conversation id is required")
	}
	participants, err := json.Marshal(c.Participants)
	if err != nil {
		return fmt.Errorf("encode participants: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin record: %w", err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO conversations (id,title,participants_json,project_id,created_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, c.ID, Redact(c.Title), string(participants), c.ProjectID, timestamp(c.CreatedAt)); err != nil {
		return fmt.Errorf("record conversation: %w", err)
	}
	for _, m := range messages {
		if strings.TrimSpace(m.ID) == "" || m.ConversationID != c.ID {
			return errors.New("message id required and conversation id must match batch")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO messages (id,conversation_id,parent_id,sender,recipient,body,source,context_id,task_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, m.ID, m.ConversationID, m.ParentID, m.Sender, m.Recipient, Redact(m.Body), m.Source, m.ContextID, m.TaskID, timestamp(m.CreatedAt)); err != nil {
			return fmt.Errorf("record message: %w", err)
		}
	}
	for _, e := range events {
		if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.Type) == "" || e.ConversationID != c.ID {
			return errors.New("event id/type required and conversation id must match batch")
		}
		payload := RedactJSON(e.Payload)
		if len(e.Payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO events (id,conversation_id,type,causation_id,correlation_id,payload_json,created_at) VALUES(?,?,?,?,?,?,?)`, e.ID, e.ConversationID, e.Type, e.CausationID, e.CorrelationID, string(payload), timestamp(e.CreatedAt)); err != nil {
			return fmt.Errorf("record event: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit record: %w", err)
	}
	return nil
}

func (s *SQLiteStore) AppendMessage(ctx context.Context, m Message) error {
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.ConversationID) == "" {
		return errors.New("message id and conversation id are required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO messages
 (id,conversation_id,parent_id,sender,recipient,body,source,context_id,task_id,created_at)
 VALUES(?,?,?,?,?,?,?,?,?,?)`, m.ID, m.ConversationID, m.ParentID, m.Sender, m.Recipient, Redact(m.Body), m.Source, m.ContextID, m.TaskID, timestamp(m.CreatedAt))
	if err != nil {
		return fmt.Errorf("append message: %w", err)
	}
	return nil
}

func (s *SQLiteStore) AppendEvent(ctx context.Context, e Event) error {
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.ConversationID) == "" || strings.TrimSpace(e.Type) == "" {
		return errors.New("event id, conversation id, and type are required")
	}
	payload := RedactJSON(e.Payload)
	if len(e.Payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO events
 (id,conversation_id,type,causation_id,correlation_id,payload_json,created_at)
	 VALUES(?,?,?,?,?,?,?)`, e.ID, e.ConversationID, e.Type, e.CausationID, e.CorrelationID, string(payload), timestamp(e.CreatedAt))
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

func boundedLimit(n int) int {
	if n <= 0 {
		return 100
	}
	if n > 1000 {
		return 1000
	}
	return n
}

func (s *SQLiteStore) ListConversations(ctx context.Context, limit int) ([]Conversation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,participants_json,project_id,created_at FROM conversations ORDER BY created_at DESC LIMIT ?`, boundedLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		var c Conversation
		var p, ts string
		if err := rows.Scan(&c.ID, &c.Title, &p, &c.ProjectID, &ts); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(p), &c.Participants)
		c.CreatedAt, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListMessages(ctx context.Context, conversationID string, limit int) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,conversation_id,parent_id,sender,recipient,body,source,context_id,task_id,created_at FROM messages WHERE conversation_id=? ORDER BY sequence ASC LIMIT ?`, conversationID, boundedLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var ts string
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.ParentID, &m.Sender, &m.Recipient, &m.Body, &m.Source, &m.ContextID, &m.TaskID, &ts); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListEvents(ctx context.Context, conversationID string, limit int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,conversation_id,type,causation_id,correlation_id,payload_json,created_at FROM events WHERE conversation_id=? ORDER BY sequence ASC LIMIT ?`, conversationID, boundedLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var p, ts string
		if err := rows.Scan(&e.ID, &e.ConversationID, &e.Type, &e.CausationID, &e.CorrelationID, &p, &ts); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(p)
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Close() error { return s.db.Close() }
