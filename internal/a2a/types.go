package a2a

import "encoding/json"

// ProtocolVersion is the only A2A protocol version this slice supports.
const ProtocolVersion = "1.0"

// Terminal task states per TASK.md: wait stops on these.
const (
	StateSubmitted     = "submitted"
	StateWorking       = "working"
	StateInputRequired = "input-required"
	StateCompleted     = "completed"
	StateFailed        = "failed"
	StateRejected      = "rejected"
	StateCanceled      = "canceled"
)

// AgentCard is the narrow validated subset of /.well-known/agent-card.json.
// Unknown fields are ignored on decode (forward-compatible); discover output
// re-emits only the validated fields below.
type AgentCard struct {
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	URL             string `json:"url"`
	Version         string `json:"version"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
}

// TextPart is the only part kind this slice produces.
type TextPart struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Message is the narrow A2A message subset used by send/wait.
type Message struct {
	Kind      string     `json:"kind,omitempty"`
	Role      string     `json:"role"`
	MessageID string     `json:"messageId"`
	ContextID string     `json:"contextId,omitempty"`
	Parts     []TextPart `json:"parts"`
}

// TaskStatus is the narrow task status subset.
type TaskStatus struct {
	State   string   `json:"state"`
	Message *Message `json:"message,omitempty"`
}

// Task is the narrow task subset. Unknown fields are ignored on decode;
// callers that need stable passthrough should keep the accompanying Raw.
type Task struct {
	Kind      string     `json:"kind,omitempty"`
	ID        string     `json:"id"`
	ContextID string     `json:"contextId"`
	Status    TaskStatus `json:"status"`
}

// IsTerminal reports whether state stops polling.
func IsTerminal(state string) bool {
	switch state {
	case StateCompleted, StateFailed, StateRejected, StateCanceled:
		return true
	default:
		return false
	}
}

// IsFailure reports whether a terminal state must map to the
// remote-task-failure exit code.
func IsFailure(state string) bool {
	switch state {
	case StateFailed, StateRejected, StateCanceled:
		return true
	default:
		return false
	}
}

// rpcRequest is the JSON-RPC 2.0 envelope this client sends.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcResponse is the JSON-RPC 2.0 envelope this client accepts.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// SendResult holds either an immediate Message or a Task.
// Raw preserves unknown response fields for stable CLI output.
type SendResult struct {
	Kind       string // "task" or "message"
	Task       *Task
	TaskRaw    json.RawMessage
	Message    *Message
	MessageRaw json.RawMessage
}
