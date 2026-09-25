package httpapi

import "encoding/json"

// ChatRequest is the payload for POST /api/v1/chat. Unknown fields are
// rejected.
type ChatRequest struct {
	// ConversationID continues an existing conversation. Omit it to start a
	// new one; the response carries the generated ID back. 1-128 characters
	// of letters, digits, '-' or '_'.
	ConversationID string `json:"conversation_id,omitempty" example:"3f9c9b1e-2f0a-4a3e-9b1a-6e4b6f0c1a2b"`
	// Message is the user's message to the assistant. Required; limited to
	// MAX_MESSAGE_CHARS characters.
	Message string `json:"message" example:"What is 12 times 7?"`
}

// ToolCallDTO is one tool invocation the agent made while producing its
// reply.
type ToolCallDTO struct {
	// Name is the tool's identifier, e.g. "calculator".
	Name string `json:"name" example:"calculator"`
	// Arguments is the JSON object the model supplied to the tool.
	Arguments json.RawMessage `json:"arguments" swaggertype:"object"`
	// Result is the tool's output when the call succeeded.
	Result string `json:"result,omitempty" example:"84"`
	// Error is the tool's failure message when the call failed. A failed
	// tool call does not fail the request: the model is told about the
	// failure and decides how to proceed.
	Error string `json:"error,omitempty"`
	// DurationMS is how long the tool took to run.
	DurationMS int64 `json:"duration_ms" example:"12"`
}

// ChatResponse is the successful response body for POST /api/v1/chat.
type ChatResponse struct {
	// ConversationID identifies the conversation; send it back on the next
	// request to continue.
	ConversationID string `json:"conversation_id" example:"3f9c9b1e-2f0a-4a3e-9b1a-6e4b6f0c1a2b"`
	// Reply is the assistant's final answer.
	Reply string `json:"reply" example:"12 times 7 is 84."`
	// ToolCalls lists every tool the agent invoked, in order. Empty when it
	// answered directly.
	ToolCalls []ToolCallDTO `json:"tool_calls"`
}

// ErrorResponse is the body of every non-2xx response.
type ErrorResponse struct {
	// Code is a stable, machine-readable error identifier. Clients should
	// branch on Code, never on Error.
	Code string `json:"code" example:"invalid_request" enums:"invalid_request,unauthorized,not_found,method_not_allowed,conversation_busy,request_too_large,rate_limited,tool_turn_limit,internal_error,model_unavailable,timeout,client_closed_request"`
	// Error is a human-readable explanation. Its wording may change.
	Error string `json:"error" example:"invalid input: message must not be empty"`
	// RequestID identifies the request in the server logs; include it when
	// reporting a problem.
	RequestID string `json:"request_id,omitempty" example:"5b1f6c9e-7a0d-4c7e-8f5e-2d9a1c3b4e6f"`
}

// StatusResponse is the body returned by the health endpoints.
type StatusResponse struct {
	Status string `json:"status" example:"ok"`
}
