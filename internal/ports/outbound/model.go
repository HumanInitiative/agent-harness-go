// Package outbound declares every interface the application layer depends
// on but does not implement — the "driven" side of the hexagon. Concrete
// implementations live under internal/adapters/outbound and are wired
// together only in cmd/harness/main.go (the composition root).
//
// Nothing in this package imports Genkit, an HTTP client, or any other
// concrete SDK: that is what keeps internal/application free to change
// adapters (a different model provider, a different persistence layer)
// without touching business logic.
package outbound

import (
	"context"
	"encoding/json"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/conversation"
)

// GenerateRequest is what the application layer asks a ModelPort to do for
// one conversational turn.
type GenerateRequest struct {
	// SystemPrompt sets the assistant's persona/instructions for this call.
	SystemPrompt string
	// Messages is the conversation to answer, oldest first, ending with the
	// user's latest message.
	Messages []conversation.Message
	// Tools lists every tool the model may call during this turn. An empty
	// slice means plain text generation with no tool calling.
	Tools []ToolHandler
	// MaxTurns bounds the respond<->act loop: how many rounds of tool calls
	// the model may make before it must produce a final answer. Must be at
	// least 1; implementations reject anything lower instead of silently
	// substituting a default, so the one source of truth for this limit is
	// the caller's configuration.
	MaxTurns int
}

// ToolCallRecord is an audit trail entry for one tool invocation the model
// made while producing a response.
type ToolCallRecord struct {
	Name      string
	Arguments json.RawMessage
	// Result is the tool's output when it succeeded; empty otherwise.
	Result string
	// Error is the tool's failure message when it failed; empty otherwise.
	// A failed tool call does not fail the turn: the failure is reported
	// back to the model so it can recover (retry with corrected arguments,
	// or explain the problem to the user).
	Error      string
	DurationMS int64
}

// GenerateResponse is the outcome of one ModelPort.Generate call.
type GenerateResponse struct {
	// Output is the model's final natural-language answer, after every tool
	// call has been resolved.
	Output string
	// ToolCalls records every tool invocation made while producing Output.
	ToolCalls []ToolCallRecord
}

// ModelPort is the boundary to whatever LLM SDK actually runs generation.
//
// Implementations must classify failures so callers can react correctly:
//   - context.Canceled / context.DeadlineExceeded from the caller's own
//     context stay in the error chain unchanged;
//   - provider overload or outage is reported as *ModelUnavailableError
//     (matches ErrModelUnavailable);
//   - exhausting GenerateRequest.MaxTurns is reported as ErrToolTurnLimit.
type ModelPort interface {
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
}
