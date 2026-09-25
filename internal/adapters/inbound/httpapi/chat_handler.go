package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/application/agent"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

// defaultRetryAfter is suggested to clients when the model provider is
// unavailable but did not say how long to wait.
const defaultRetryAfter = 5 * time.Second

// ChatHandler exposes agent.Service over HTTP. It only decodes the request,
// calls the use case, and maps the outcome to a response; every rule about
// what a valid chat turn is lives in internal/application/agent.
type ChatHandler struct {
	service *agent.Service
	logger  *slog.Logger
}

// NewChatHandler builds a ChatHandler around the given agent.Service.
func NewChatHandler(service *agent.Service, logger *slog.Logger) *ChatHandler {
	return &ChatHandler{service: service, logger: logger}
}

// ServeHTTP handles POST /api/v1/chat.
//
//	@Summary		Send a message to the agent
//	@Description	Sends a user message to the AI agent. The agent may call registered tools before
//	@Description	replying; every call is listed in tool_calls. Pass conversation_id to continue a
//	@Description	conversation, omit it to start a new one. Turns of one conversation must be sent
//	@Description	one at a time.
//	@Tags			agent
//	@Accept			json
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Param			request	body		ChatRequest	true	"Chat request"
//	@Success		200		{object}	ChatResponse
//	@Failure		400		{object}	ErrorResponse	"invalid_request: malformed body, empty or oversized message, bad conversation_id"
//	@Failure		401		{object}	ErrorResponse	"unauthorized: missing or invalid X-API-Key"
//	@Failure		409		{object}	ErrorResponse	"conversation_busy: another turn of this conversation is still running"
//	@Failure		413		{object}	ErrorResponse	"request_too_large: body exceeds MAX_REQUEST_BYTES"
//	@Failure		429		{object}	ErrorResponse	"rate_limited: see Retry-After header"
//	@Failure		500		{object}	ErrorResponse	"internal_error or tool_turn_limit"
//	@Failure		503		{object}	ErrorResponse	"model_unavailable: provider overloaded; see Retry-After header"
//	@Failure		504		{object}	ErrorResponse	"timeout: the reply took longer than REQUEST_TIMEOUT_SECONDS"
//	@Router			/api/v1/chat [post]
func (h *ChatHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := h.decode(w, r)
	if !ok {
		return
	}

	out, err := h.service.Chat(r.Context(), agent.ChatInput{
		ConversationID: req.ConversationID,
		Message:        req.Message,
	})
	if err != nil {
		h.writeChatError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, ChatResponse{
		ConversationID: out.ConversationID,
		Reply:          out.Reply,
		ToolCalls:      toToolCallDTOs(out.ToolCalls),
	})
}

// decode reads exactly one JSON object with no unknown fields. It writes the
// error response itself and reports false when the body is unusable.
func (h *ChatHandler) decode(w http.ResponseWriter, r *http.Request) (ChatRequest, bool) {
	var req ChatRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	err := dec.Decode(&req)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("unexpected data after the JSON object")
	}
	if err == nil {
		return req, true
	}

	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, r, http.StatusRequestEntityTooLarge, codeRequestTooLarge,
			"request body exceeds "+strconv.FormatInt(tooLarge.Limit, 10)+" bytes")
		return ChatRequest{}, false
	}
	writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
		`request body must be a single JSON object like {"message": "..."}: `+err.Error())
	return ChatRequest{}, false
}

// writeChatError maps a use-case failure to an HTTP response. Each error is
// logged exactly once, here, at a level that reflects whose problem it is.
func (h *ChatHandler) writeChatError(w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()
	var unavailable *outbound.ModelUnavailableError

	switch {
	case errors.Is(err, agent.ErrInvalidInput):
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())

	case errors.Is(err, agent.ErrConversationBusy):
		writeError(w, r, http.StatusConflict, codeConversationBusy,
			"another turn of this conversation is still running; wait for it to finish")

	case errors.Is(err, context.Canceled) && ctx.Err() != nil:
		h.logger.InfoContext(ctx, "client disconnected before the reply was ready")
		writeError(w, r, statusClientClosedRequest, codeClientClosed, "client closed request")

	case errors.Is(err, context.DeadlineExceeded):
		h.logger.WarnContext(ctx, "request timed out", "error", err)
		writeError(w, r, http.StatusGatewayTimeout, codeTimeout, "the agent did not reply in time")

	case errors.As(err, &unavailable):
		retryAfter := unavailable.RetryAfter
		if retryAfter <= 0 {
			retryAfter = defaultRetryAfter
		}
		h.logger.WarnContext(ctx, "model provider unavailable", "error", err, "retry_after", retryAfter)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
		writeError(w, r, http.StatusServiceUnavailable, codeModelUnavailable,
			"the model provider is temporarily unavailable, retry later")

	case errors.Is(err, outbound.ErrToolTurnLimit):
		h.logger.WarnContext(ctx, "agent exhausted its tool-call limit", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeToolTurnLimit,
			"the agent could not finish within its tool-call limit; try a simpler or more specific request")

	default:
		h.logger.ErrorContext(ctx, "chat failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "internal server error")
	}
}

func toToolCallDTOs(records []outbound.ToolCallRecord) []ToolCallDTO {
	dtos := make([]ToolCallDTO, len(records))
	for i, rec := range records {
		dtos[i] = ToolCallDTO{
			Name:       rec.Name,
			Arguments:  rec.Arguments,
			Result:     rec.Result,
			Error:      rec.Error,
			DurationMS: rec.DurationMS,
		}
	}
	return dtos
}
