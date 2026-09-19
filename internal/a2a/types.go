package a2a

import (
	"encoding/json"
	"net/url"
	"strings"
)

// ProtocolVersion is the only A2A protocol version this slice supports.
const ProtocolVersion = "1.0"

// A2A 1.0 TaskState enum values (ProtoJSON SCREAMING_SNAKE names). Per
// spec section 4.1.3 these are the only states a conformant peer emits.
const (
	StateUnspecified   = "TASK_STATE_UNSPECIFIED"
	StateSubmitted     = "TASK_STATE_SUBMITTED"
	StateWorking       = "TASK_STATE_WORKING"
	StateInputRequired = "TASK_STATE_INPUT_REQUIRED"
	StateAuthRequired  = "TASK_STATE_AUTH_REQUIRED"
	StateCompleted     = "TASK_STATE_COMPLETED"
	StateFailed        = "TASK_STATE_FAILED"
	StateRejected      = "TASK_STATE_REJECTED"
	StateCanceled      = "TASK_STATE_CANCELED"
)

// A2A 1.0 Role enum values (ProtoJSON SCREAMING_SNAKE names).
const (
	RoleUser  = "ROLE_USER"
	RoleAgent = "ROLE_AGENT"
)

// JSON-RPC method names are PascalCase per A2A 1.0 spec section 9.1.
const (
	MethodSendMessage = "SendMessage"
	MethodGetTask     = "GetTask"
)

// AgentInterface is one entry of an A2A 1.0 Agent Card's supportedInterfaces
// array. Clients select the first JSONRPC entry they support.
type AgentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	ProtocolVersion string `json:"protocolVersion"`
	Tenant          string `json:"tenant,omitempty"`
}

// AgentCard is the narrow validated subset of /.well-known/agent-card.json.
// A2A 1.0 relocates the endpoint URL and protocol version into
// supportedInterfaces; there is no top-level url or protocolVersion.
// Unknown fields are ignored on decode (forward-compatible).
type AgentCard struct {
	Name                string           `json:"name"`
	Description         string           `json:"description,omitempty"`
	Version             string           `json:"version"`
	SupportedInterfaces []AgentInterface `json:"supportedInterfaces,omitempty"`
}

// Interface selects the first JSONRPC interface that advertises protocol
// version 1.0. It only inspects fields the slice needs; unknown fields and
// other bindings are ignored.
func (c *AgentCard) Interface() (*AgentInterface, error) {
	if c == nil {
		return nil, &ProtocolError{Msg: "missing agent card"}
	}
	var sawJSONRPC bool
	for i := range c.SupportedInterfaces {
		it := &c.SupportedInterfaces[i]
		if !strings.EqualFold(strings.TrimSpace(it.ProtocolBinding), "jsonrpc") {
			continue
		}
		sawJSONRPC = true
		if strings.TrimSpace(it.ProtocolVersion) != ProtocolVersion {
			continue
		}
		u, err := url.Parse(strings.TrimSpace(it.URL))
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, &ProtocolError{Msg: "agent card has invalid JSONRPC interface url"}
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, &ProtocolError{Msg: "agent card JSONRPC interface url scheme must be http or https"}
		}
		return it, nil
	}
	if !sawJSONRPC {
		return nil, &ProtocolError{Msg: "agent card has no JSONRPC interface"}
	}
	return nil, &ProtocolError{Msg: "agent card has no JSONRPC interface for protocolVersion " + ProtocolVersion}
}

// TextPart is the only part kind this slice produces. A2A 1.0 removed the
// `kind` discriminator; the member name (`text`) identifies the type.
type TextPart struct {
	Text string `json:"text"`
}

// Message is the narrow A2A 1.0 message subset used by send/wait.
// A2A 1.0 removed the `kind` discriminator and uses ROLE_* enum values.
type Message struct {
	Role      string     `json:"role"`
	MessageID string     `json:"messageId"`
	ContextID string     `json:"contextId,omitempty"`
	TaskID    string     `json:"taskId,omitempty"`
	Parts     []TextPart `json:"parts"`
}

// TaskStatus is the narrow task status subset.
type TaskStatus struct {
	State   string   `json:"state"`
	Message *Message `json:"message,omitempty"`
}

// Task is the narrow A2A 1.0 task subset. A2A 1.0 removed the `kind`
// discriminator. Unknown fields are ignored on decode; callers that need
// stable passthrough should keep the accompanying Raw.
type Task struct {
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

// IsInterrupted reports whether state is a recognized non-terminal state the
// slice can surface (submitted/working/input-required/auth-required).
func IsInterrupted(state string) bool {
	switch state {
	case StateSubmitted, StateWorking, StateInputRequired, StateAuthRequired:
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

// rpcRequest is the JSON-RPC 2.0 envelope this client sends. The ID is a
// string so responses can be correlated exactly.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcResponse is the JSON-RPC 2.0 envelope this client accepts. ID must
// exactly echo the request ID (checked by doJSON).
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
