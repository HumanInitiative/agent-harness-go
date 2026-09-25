package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/HumanInitiative/agent-harness-go/internal/application/agent"
	"github.com/HumanInitiative/agent-harness-go/internal/domain/conversation"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

// fakeModel returns a fixed reply and records the request it received. If
// block is non-nil, Generate waits on it — used to hold a turn "in flight".
type fakeModel struct {
	mu       sync.Mutex
	response outbound.GenerateResponse
	err      error
	lastReq  outbound.GenerateRequest
	started  chan struct{}
	block    chan struct{}
}

func (f *fakeModel) Generate(_ context.Context, req outbound.GenerateRequest) (outbound.GenerateResponse, error) {
	f.mu.Lock()
	f.lastReq = req
	f.mu.Unlock()
	if f.started != nil {
		close(f.started)
	}
	if f.block != nil {
		<-f.block
	}
	if f.err != nil {
		return outbound.GenerateResponse{}, f.err
	}
	return f.response, nil
}

func (f *fakeModel) request() outbound.GenerateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastReq
}

type fakeStore struct {
	mu   sync.Mutex
	data map[string][]conversation.Message
}

func newFakeStore() *fakeStore { return &fakeStore{data: map[string][]conversation.Message{}} }

func (f *fakeStore) History(_ context.Context, id string) ([]conversation.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]conversation.Message(nil), f.data[id]...), nil
}

func (f *fakeStore) Append(_ context.Context, id string, messages ...conversation.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[id] = append(f.data[id], messages...)
	return nil
}

type namedTool struct{ name string }

func (t namedTool) Name() string                { return t.name }
func (t namedTool) Description() string         { return "test tool" }
func (t namedTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (t namedTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

func validConfig() agent.Config {
	return agent.Config{MaxToolTurns: 3, HistoryWindow: 20, MaxMessageChars: 100}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newService(t *testing.T, model outbound.ModelPort, store outbound.ConversationStore, cfg agent.Config) *agent.Service {
	t.Helper()
	svc, err := agent.NewService(model, store, nil, cfg, discardLogger())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestNewService_RejectsInvalidConfig(t *testing.T) {
	cases := map[string]agent.Config{
		"zero tool turns":     {MaxToolTurns: 0, HistoryWindow: 1, MaxMessageChars: 1},
		"negative history":    {MaxToolTurns: 1, HistoryWindow: -1, MaxMessageChars: 1},
		"zero message length": {MaxToolTurns: 1, HistoryWindow: 1, MaxMessageChars: 0},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := agent.NewService(&fakeModel{}, newFakeStore(), nil, cfg, discardLogger()); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestNewService_RejectsDuplicateToolNames(t *testing.T) {
	tools := []outbound.ToolHandler{namedTool{"calculator"}, namedTool{"calculator"}}
	if _, err := agent.NewService(&fakeModel{}, newFakeStore(), tools, validConfig(), discardLogger()); err == nil {
		t.Fatal("expected an error for duplicate tool names")
	}
}

func TestChat_GeneratesConversationIDWhenNoneGiven(t *testing.T) {
	svc := newService(t, &fakeModel{response: outbound.GenerateResponse{Output: "hi"}}, newFakeStore(), validConfig())

	out, err := svc.Chat(context.Background(), agent.ChatInput{Message: "hello"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out.ConversationID == "" || out.Reply != "hi" {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestChat_PersistsTurnAndReusesConversationID(t *testing.T) {
	store := newFakeStore()
	svc := newService(t, &fakeModel{response: outbound.GenerateResponse{Output: "answer"}}, store, validConfig())

	out, err := svc.Chat(context.Background(), agent.ChatInput{ConversationID: "conv_1", Message: "question"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if out.ConversationID != "conv_1" {
		t.Fatalf("conversation ID not reused: %q", out.ConversationID)
	}

	history, _ := store.History(context.Background(), "conv_1")
	want := []conversation.Message{
		{Role: conversation.RoleUser, Content: "question"},
		{Role: conversation.RoleAssistant, Content: "answer"},
	}
	if len(history) != len(want) || history[0] != want[0] || history[1] != want[1] {
		t.Fatalf("history = %+v, want %+v", history, want)
	}
}

func TestChat_SendsHistoryThenNewMessageAndConfiguredLimits(t *testing.T) {
	store := newFakeStore()
	_ = store.Append(context.Background(), "c",
		conversation.Message{Role: conversation.RoleUser, Content: "first"},
		conversation.Message{Role: conversation.RoleAssistant, Content: "first reply"},
	)
	model := &fakeModel{response: outbound.GenerateResponse{Output: "ok"}}
	cfg := validConfig()
	cfg.SystemPrompt = "be helpful"
	svc := newService(t, model, store, cfg)

	if _, err := svc.Chat(context.Background(), agent.ChatInput{ConversationID: "c", Message: "second"}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	req := model.request()
	if len(req.Messages) != 3 || req.Messages[2].Content != "second" {
		t.Fatalf("unexpected messages sent to model: %+v", req.Messages)
	}
	if req.MaxTurns != cfg.MaxToolTurns || req.SystemPrompt != "be helpful" {
		t.Fatalf("config not forwarded: MaxTurns=%d SystemPrompt=%q", req.MaxTurns, req.SystemPrompt)
	}
}

func TestChat_HistoryWindowKeepsRecentMessagesAndStartsWithUser(t *testing.T) {
	store := newFakeStore()
	for _, m := range []conversation.Message{
		{Role: conversation.RoleUser, Content: "u1"},
		{Role: conversation.RoleAssistant, Content: "a1"},
		{Role: conversation.RoleUser, Content: "u2"},
		{Role: conversation.RoleAssistant, Content: "a2"},
	} {
		_ = store.Append(context.Background(), "c", m)
	}
	model := &fakeModel{response: outbound.GenerateResponse{Output: "ok"}}
	cfg := validConfig()
	cfg.HistoryWindow = 3 // last 3 = a1, u2, a2 -> leading assistant dropped -> u2, a2
	svc := newService(t, model, store, cfg)

	if _, err := svc.Chat(context.Background(), agent.ChatInput{ConversationID: "c", Message: "u3"}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	got := model.request().Messages
	wantContents := []string{"u2", "a2", "u3"}
	if len(got) != len(wantContents) {
		t.Fatalf("got %d messages, want %d: %+v", len(got), len(wantContents), got)
	}
	for i, want := range wantContents {
		if got[i].Content != want {
			t.Errorf("message %d = %q, want %q", i, got[i].Content, want)
		}
	}
}

func TestChat_RejectsInvalidInput(t *testing.T) {
	svc := newService(t, &fakeModel{}, newFakeStore(), validConfig())

	cases := map[string]agent.ChatInput{
		"empty message":         {Message: ""},
		"whitespace message":    {Message: "   \n\t"},
		"message too long":      {Message: strings.Repeat("x", 101)},
		"bad conversation id":   {ConversationID: "has spaces", Message: "hi"},
		"too long conversation": {ConversationID: strings.Repeat("a", 129), Message: "hi"},
		"control chars in conv": {ConversationID: "abc\x00", Message: "hi"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Chat(context.Background(), in)
			if !errors.Is(err, agent.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}

func TestChat_MessageLimitCountsCharactersNotBytes(t *testing.T) {
	svc := newService(t, &fakeModel{response: outbound.GenerateResponse{Output: "ok"}}, newFakeStore(), validConfig())

	// 100 multi-byte characters is within a 100-character limit even though
	// it is far more than 100 bytes.
	if _, err := svc.Chat(context.Background(), agent.ChatInput{Message: strings.Repeat("é", 100)}); err != nil {
		t.Fatalf("expected 100 characters to be accepted, got %v", err)
	}
}

func TestChat_RejectsConcurrentTurnOnSameConversation(t *testing.T) {
	model := &fakeModel{
		response: outbound.GenerateResponse{Output: "ok"},
		started:  make(chan struct{}),
		block:    make(chan struct{}),
	}
	svc := newService(t, model, newFakeStore(), validConfig())

	done := make(chan error, 1)
	go func() {
		_, err := svc.Chat(context.Background(), agent.ChatInput{ConversationID: "c", Message: "first"})
		done <- err
	}()
	<-model.started

	_, err := svc.Chat(context.Background(), agent.ChatInput{ConversationID: "c", Message: "second"})
	if !errors.Is(err, agent.ErrConversationBusy) {
		t.Fatalf("expected ErrConversationBusy, got %v", err)
	}

	close(model.block)
	if err := <-done; err != nil {
		t.Fatalf("first turn failed: %v", err)
	}

	// Once the first turn finishes, the conversation accepts turns again.
	model.started, model.block = nil, nil
	if _, err := svc.Chat(context.Background(), agent.ChatInput{ConversationID: "c", Message: "third"}); err != nil {
		t.Fatalf("expected conversation to be free again, got %v", err)
	}
}

func TestChat_PropagatesModelErrorWithoutPersisting(t *testing.T) {
	store := newFakeStore()
	svc := newService(t, &fakeModel{err: outbound.ErrModelUnavailable}, store, validConfig())

	_, err := svc.Chat(context.Background(), agent.ChatInput{ConversationID: "c", Message: "hi"})
	if !errors.Is(err, outbound.ErrModelUnavailable) {
		t.Fatalf("expected ErrModelUnavailable in chain, got %v", err)
	}
	if history, _ := store.History(context.Background(), "c"); len(history) != 0 {
		t.Fatalf("a failed turn must not be persisted, got %+v", history)
	}
}
