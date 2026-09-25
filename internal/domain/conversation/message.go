// Package conversation holds the pure, framework-agnostic vocabulary of a
// chat conversation: a message and who authored it.
//
// This package must never import Genkit, an HTTP framework, or any adapter
// package — it is the language the rest of the harness speaks internally,
// independent of which model SDK or transport happens to be plugged in.
package conversation

// Role identifies who authored a Message.
type Role string

const (
	// RoleUser is a message from the human (or calling system) driving the
	// conversation.
	RoleUser Role = "user"
	// RoleAssistant is a message produced by the model.
	RoleAssistant Role = "assistant"
)

// Message is one turn of a conversation. It carries only the final text of
// a turn — how the model arrived at that text (which tools it called) is
// reported separately via outbound.ToolCallRecord and is not stored as
// history.
type Message struct {
	Role    Role
	Content string
}
