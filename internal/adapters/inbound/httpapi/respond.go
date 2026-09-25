package httpapi

import (
	"encoding/json"
	"net/http"
)

// Stable error codes returned in ErrorResponse.Code. They are part of the
// API contract: add new ones freely, never rename or reuse existing ones.
const (
	codeInvalidRequest   = "invalid_request"
	codeUnauthorized     = "unauthorized"
	codeNotFound         = "not_found"
	codeMethodNotAllowed = "method_not_allowed"
	codeConversationBusy = "conversation_busy"
	codeRequestTooLarge  = "request_too_large"
	codeRateLimited      = "rate_limited"
	codeToolTurnLimit    = "tool_turn_limit"
	codeInternal         = "internal_error"
	codeModelUnavailable = "model_unavailable"
	codeTimeout          = "timeout"
	codeClientClosed     = "client_closed_request"
)

// statusClientClosedRequest is the de-facto (nginx) status for "the client
// went away before we answered". Nobody receives it; it exists so access
// logs distinguish abandoned requests from server failures.
const statusClientClosedRequest = 499

// writeJSON encodes v as the response body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	// If encoding fails the status line is already sent; there is nothing
	// left to recover, so the error is intentionally dropped.
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes an ErrorResponse, tagged with the request's ID.
func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{
		Code:      code,
		Error:     message,
		RequestID: requestIDFrom(r.Context()),
	})
}
