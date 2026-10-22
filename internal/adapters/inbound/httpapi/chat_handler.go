package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/HumanInitiative/agent-harness-go/internal/application/agent"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

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

// decode reads the chat request. It writes the error response itself and
// reports false when the body is unusable.
func (h *ChatHandler) decode(w http.ResponseWriter, r *http.Request) (ChatRequest, bool) {
	var req ChatRequest
	if !decodeJSON(w, r, &req, `{"message": "..."}`) {
		return ChatRequest{}, false
	}
	return req, true
}

// writeChatError maps a use-case failure to an HTTP response. Each error is
// logged exactly once, here, at a level that reflects whose problem it is.
func (h *ChatHandler) writeChatError(w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()

	switch {
	case errors.Is(err, agent.ErrInvalidInput):
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())

	case errors.Is(err, agent.ErrConversationBusy):
		writeError(w, r, http.StatusConflict, codeConversationBusy,
			"another turn of this conversation is still running; wait for it to finish")

	case writeCommonError(h.logger, w, r, err):

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
