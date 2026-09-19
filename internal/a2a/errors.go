package a2a

import "fmt"

// UsageError is a caller-side configuration problem (bad flags, missing env,
// malformed URL). Maps to exit code 1.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return "usage: " + e.Msg }

// AuthError is a 401/403 from the peer or a missing bearer. Maps to exit 2.
type AuthError struct {
	Status int
	Msg    string
}

func (e *AuthError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("authentication failed (status %d): %s", e.Status, e.Msg)
	}
	return "authentication failed: " + e.Msg
}

// ProtocolError is a peer response that violates A2A 1.0 expectations:
// wrong content type, malformed JSON, bad envelope, version mismatch, or
// missing fields. Maps to exit code 3.
type ProtocolError struct{ Msg string }

func (e *ProtocolError) Error() string { return "protocol error: " + e.Msg }

// TaskFailedError marks a terminal task failure/rejection/cancellation.
// It still carries the task so callers can emit it. Maps to exit code 4.
type TaskFailedError struct {
	State  string
	TaskID string
}

func (e *TaskFailedError) Error() string {
	return fmt.Sprintf("task %q terminated with state %q", e.TaskID, e.State)
}

// TransportError is a network failure, deadline, or oversized body.
// Maps to exit code 5.
type TransportError struct {
	Msg      string
	Timeout  bool
	Oversize bool
}

func (e *TransportError) Error() string { return "transport error: " + e.Msg }
