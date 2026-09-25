// Package agent contains the harness's single use case: run one turn of a
// tool-calling conversation. It depends only on internal/domain and
// internal/ports — never on a concrete adapter — so it can be unit tested
// with fakes and stays correct no matter which model provider or storage
// backend is plugged in behind those ports.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/conversation"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

var (
	// ErrInvalidInput means the caller's input breaks a rule of this use
	// case (empty or oversized message, malformed conversation ID). The
	// wrapped message is safe to show to the caller.
	ErrInvalidInput = errors.New("invalid input")

	// ErrConversationBusy means another turn of the same conversation is
	// still running. Turns of one conversation are strictly sequential:
	// running two at once would let both read the same history and append
	// their messages interleaved, corrupting the context of every later
	// turn.
	ErrConversationBusy = errors.New("conversation has a turn in progress")
)

// conversationIDPattern accepts UUIDs (what this service generates) and any
// reasonable caller-chosen opaque ID, while rejecting anything that could
// cause trouble downstream (unbounded length, whitespace, control chars).
var conversationIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Config holds the policy Service enforces. Every field is required and
// validated by NewService; defaults live in one place only,
// internal/platform/config.
type Config struct {
	// SystemPrompt sets the assistant's persona and instructions.
	SystemPrompt string
	// MaxToolTurns bounds how many rounds of tool calls the model may make
	// in one turn before it must answer. Must be >= 1.
	MaxToolTurns int
	// HistoryWindow is the maximum number of prior messages sent to the
	// model as context. Bounding it keeps cost per turn flat and prevents a
	// long conversation from overflowing the model's context window.
	// 0 disables history entirely (every turn is independent).
	HistoryWindow int
	// MaxMessageChars caps the length of one user message, in characters.
	// Must be >= 1.
	MaxMessageChars int
}

// Service is the harness's core orchestrator: given a user message (and
// optionally an existing conversation to continue), it asks the model for a
// reply, lets the model call any registered tool as needed, and persists
// the turn.
type Service struct {
	model         outbound.ModelPort
	conversations outbound.ConversationStore
	tools         []outbound.ToolHandler
	cfg           Config
	logger        *slog.Logger
	inFlight      *keySet
}

// NewService wires a Service from its ports and validates its
// configuration. Every registered tool is available to every caller on
// every turn (this build has no per-user authorization).
func NewService(model outbound.ModelPort, conversations outbound.ConversationStore, tools []outbound.ToolHandler, cfg Config, logger *slog.Logger) (*Service, error) {
	if cfg.MaxToolTurns < 1 {
		return nil, fmt.Errorf("agent: MaxToolTurns must be >= 1, got %d", cfg.MaxToolTurns)
	}
	if cfg.HistoryWindow < 0 {
		return nil, fmt.Errorf("agent: HistoryWindow must be >= 0, got %d", cfg.HistoryWindow)
	}
	if cfg.MaxMessageChars < 1 {
		return nil, fmt.Errorf("agent: MaxMessageChars must be >= 1, got %d", cfg.MaxMessageChars)
	}

	seen := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		if _, dup := seen[t.Name()]; dup {
			return nil, fmt.Errorf("agent: duplicate tool name %q", t.Name())
		}
		seen[t.Name()] = struct{}{}
	}

	return &Service{
		model:         model,
		conversations: conversations,
		tools:         tools,
		cfg:           cfg,
		logger:        logger,
		inFlight:      newKeySet(),
	}, nil
}

// ChatInput is one incoming user turn.
type ChatInput struct {
	// ConversationID continues an existing conversation when set; a new one
	// is started when empty.
	ConversationID string
	// Message is the user's input for this turn.
	Message string
}

// ChatOutput is the result of one turn.
type ChatOutput struct {
	ConversationID string
	Reply          string
	ToolCalls      []outbound.ToolCallRecord
}

// Chat runs one turn: validate input, load recent history, ask the model for
// a reply with every registered tool available, persist the user's message
// and the reply, and return the reply plus a record of the tools used.
func (s *Service) Chat(ctx context.Context, in ChatInput) (ChatOutput, error) {
	if err := s.validate(in); err != nil {
		return ChatOutput{}, err
	}

	conversationID := in.ConversationID
	if conversationID == "" {
		conversationID = uuid.NewString()
	}

	if !s.inFlight.tryAdd(conversationID) {
		return ChatOutput{}, ErrConversationBusy
	}
	defer s.inFlight.remove(conversationID)

	history, err := s.conversations.History(ctx, conversationID)
	if err != nil {
		return ChatOutput{}, fmt.Errorf("agent: load history: %w", err)
	}
	history = recentWindow(history, s.cfg.HistoryWindow)

	userMessage := conversation.Message{Role: conversation.RoleUser, Content: in.Message}
	messages := make([]conversation.Message, 0, len(history)+1)
	messages = append(messages, history...)
	messages = append(messages, userMessage)

	// Message content is deliberately never logged: it may contain personal
	// data, and its length is enough to reason about cost and behaviour.
	s.logger.InfoContext(ctx, "generating reply",
		"conversation_id", conversationID,
		"history_messages", len(history),
		"message_chars", utf8.RuneCountInString(in.Message),
		"tools_available", len(s.tools),
	)

	resp, err := s.model.Generate(ctx, outbound.GenerateRequest{
		SystemPrompt: s.cfg.SystemPrompt,
		Messages:     messages,
		Tools:        s.tools,
		MaxTurns:     s.cfg.MaxToolTurns,
	})
	if err != nil {
		return ChatOutput{}, fmt.Errorf("agent: generate: %w", err)
	}

	assistantMessage := conversation.Message{Role: conversation.RoleAssistant, Content: resp.Output}
	if err := s.conversations.Append(ctx, conversationID, userMessage, assistantMessage); err != nil {
		// The caller still gets this turn's answer, but the next turn will
		// be missing context — log loudly rather than fail a request whose
		// expensive part already succeeded.
		s.logger.ErrorContext(ctx, "failed to persist conversation turn",
			"conversation_id", conversationID, "error", err)
	}

	s.logger.InfoContext(ctx, "reply generated",
		"conversation_id", conversationID,
		"tool_calls", len(resp.ToolCalls),
		"reply_chars", utf8.RuneCountInString(resp.Output),
	)

	return ChatOutput{
		ConversationID: conversationID,
		Reply:          resp.Output,
		ToolCalls:      resp.ToolCalls,
	}, nil
}

func (s *Service) validate(in ChatInput) error {
	if strings.TrimSpace(in.Message) == "" {
		return fmt.Errorf("%w: message must not be empty", ErrInvalidInput)
	}
	if n := utf8.RuneCountInString(in.Message); n > s.cfg.MaxMessageChars {
		return fmt.Errorf("%w: message is %d characters, the limit is %d", ErrInvalidInput, n, s.cfg.MaxMessageChars)
	}
	if in.ConversationID != "" && !conversationIDPattern.MatchString(in.ConversationID) {
		return fmt.Errorf("%w: conversation_id must be 1-128 characters of letters, digits, '-' or '_'", ErrInvalidInput)
	}
	return nil
}

// recentWindow returns at most the last n messages of history. If the cut
// would make the window start with an assistant message, that message is
// dropped too, so the model always sees a conversation that opens with a
// user turn — some providers reject anything else.
func recentWindow(history []conversation.Message, n int) []conversation.Message {
	if n <= 0 {
		return nil
	}
	if len(history) > n {
		history = history[len(history)-n:]
	}
	for len(history) > 0 && history[0].Role != conversation.RoleUser {
		history = history[1:]
	}
	return history
}
