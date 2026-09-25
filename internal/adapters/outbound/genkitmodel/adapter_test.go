package genkitmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/status"
	"github.com/firebase/genkit/go/genkit"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/conversation"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

const fakeModelName = "test/scripted"

// scriptedModel is a Genkit model whose behaviour each test scripts. It
// runs inside Genkit's real generate loop, so these tests exercise the
// actual tool-calling machinery — only the LLM itself is faked.
type scriptedModel struct {
	mu       sync.Mutex
	requests []*ai.ModelRequest
	respond  func(req *ai.ModelRequest) (*ai.ModelResponse, error)
}

func (m *scriptedModel) lastRequest() *ai.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[len(m.requests)-1]
}

func newTestAdapter(t *testing.T, respond func(*ai.ModelRequest) (*ai.ModelResponse, error)) (*Adapter, *scriptedModel) {
	t.Helper()
	model := &scriptedModel{respond: respond}
	g := genkit.Init(context.Background())
	genkit.DefineModel(g, fakeModelName,
		&ai.ModelOptions{Supports: &ai.ModelSupports{Tools: true, Multiturn: true, SystemRole: true}},
		func(ctx context.Context, req *ai.ModelRequest, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			model.mu.Lock()
			model.requests = append(model.requests, req)
			model.mu.Unlock()
			resp, err := model.respond(req)
			// Like a real provider client, give up once the caller has.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return resp, err
		})
	return newAdapter(g, fakeModelName, slog.New(slog.NewTextHandler(io.Discard, nil))), model
}

func textResponse(text string) *ai.ModelResponse {
	return &ai.ModelResponse{Message: ai.NewModelTextMessage(text), FinishReason: ai.FinishReasonStop}
}

func toolRequest(name string, input map[string]any) *ai.ModelResponse {
	return &ai.ModelResponse{
		Message:      ai.NewModelMessage(ai.NewToolRequestPart(&ai.ToolRequest{Name: name, Ref: "call-1", Input: input})),
		FinishReason: ai.FinishReasonStop,
	}
}

// toolOutput returns the output of the tool response in the request's last
// message, if that message is a tool response.
func toolOutput(req *ai.ModelRequest) (string, bool) {
	last := req.Messages[len(req.Messages)-1]
	if last.Role != ai.RoleTool {
		return "", false
	}
	for _, p := range last.Content {
		if p.IsToolResponse() {
			return fmt.Sprint(p.ToolResponse.Output), true
		}
	}
	return "", false
}

// callToolOnceThenAnswer requests toolName once, then answers with whatever
// the tool returned.
func callToolOnceThenAnswer(toolName string, input map[string]any) func(*ai.ModelRequest) (*ai.ModelResponse, error) {
	return func(req *ai.ModelRequest) (*ai.ModelResponse, error) {
		if out, ok := toolOutput(req); ok {
			return textResponse("tool said: " + out), nil
		}
		return toolRequest(toolName, input), nil
	}
}

type stubTool struct {
	name    string
	execute func(args json.RawMessage) (string, error)
}

func (s stubTool) Name() string                { return s.name }
func (s stubTool) Description() string         { return "stub tool for tests" }
func (s stubTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (s stubTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	return s.execute(args)
}

func userMessage(text string) []conversation.Message {
	return []conversation.Message{{Role: conversation.RoleUser, Content: text}}
}

func TestGenerate_RunsToolAndReturnsFinalAnswer(t *testing.T) {
	adapter, _ := newTestAdapter(t, callToolOnceThenAnswer("calc", map[string]any{"expression": "6*7"}))
	var gotArgs json.RawMessage
	tool := stubTool{name: "calc", execute: func(args json.RawMessage) (string, error) {
		gotArgs = args
		return "42", nil
	}}

	resp, err := adapter.Generate(context.Background(), outbound.GenerateRequest{
		Messages: userMessage("what is 6*7?"),
		Tools:    []outbound.ToolHandler{tool},
		MaxTurns: 3,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Output != "tool said: 42" {
		t.Fatalf("Output = %q", resp.Output)
	}
	if !strings.Contains(string(gotArgs), `"expression":"6*7"`) {
		t.Fatalf("tool received unexpected args: %s", gotArgs)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "calc" || resp.ToolCalls[0].Result != "42" || resp.ToolCalls[0].Error != "" {
		t.Fatalf("unexpected tool call records: %+v", resp.ToolCalls)
	}
}

func TestGenerate_ToolErrorIsReportedToModelInsteadOfFailingTheTurn(t *testing.T) {
	adapter, model := newTestAdapter(t, callToolOnceThenAnswer("calc", map[string]any{}))
	tool := stubTool{name: "calc", execute: func(json.RawMessage) (string, error) {
		return "", errors.New("expression must not be empty")
	}}

	resp, err := adapter.Generate(context.Background(), outbound.GenerateRequest{
		Messages: userMessage("calc something"),
		Tools:    []outbound.ToolHandler{tool},
		MaxTurns: 3,
	})
	if err != nil {
		t.Fatalf("a failing tool must not fail the turn, got %v", err)
	}

	out, ok := toolOutput(model.lastRequest())
	if !ok || !strings.Contains(out, "expression must not be empty") {
		t.Fatalf("model should have been shown the tool error, saw %q", out)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Error != "expression must not be empty" || resp.ToolCalls[0].Result != "" {
		t.Fatalf("unexpected tool call records: %+v", resp.ToolCalls)
	}
}

func TestGenerate_ToolPanicIsRecoveredWithoutLeakingStack(t *testing.T) {
	adapter, model := newTestAdapter(t, callToolOnceThenAnswer("boom", map[string]any{}))
	tool := stubTool{name: "boom", execute: func(json.RawMessage) (string, error) {
		panic("something broke")
	}}

	resp, err := adapter.Generate(context.Background(), outbound.GenerateRequest{
		Messages: userMessage("trigger"),
		Tools:    []outbound.ToolHandler{tool},
		MaxTurns: 3,
	})
	if err != nil {
		t.Fatalf("a panicking tool must not fail the turn, got %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Error != "tool panicked: something broke" {
		t.Fatalf("unexpected tool call records: %+v", resp.ToolCalls)
	}
	out, _ := toolOutput(model.lastRequest())
	if strings.Contains(out, "goroutine") || strings.Contains(out, ".go:") {
		t.Fatalf("stack trace leaked to the model: %q", out)
	}
}

func TestGenerate_ToolTurnLimitIsClassified(t *testing.T) {
	adapter, _ := newTestAdapter(t, func(*ai.ModelRequest) (*ai.ModelResponse, error) {
		return toolRequest("calc", map[string]any{}), nil // never stops asking
	})
	tool := stubTool{name: "calc", execute: func(json.RawMessage) (string, error) { return "1", nil }}

	_, err := adapter.Generate(context.Background(), outbound.GenerateRequest{
		Messages: userMessage("loop forever"),
		Tools:    []outbound.ToolHandler{tool},
		MaxTurns: 2,
	})
	if !errors.Is(err, outbound.ErrToolTurnLimit) {
		t.Fatalf("expected ErrToolTurnLimit, got %v", err)
	}
}

func TestGenerate_ProviderOverloadIsClassifiedAsUnavailable(t *testing.T) {
	for _, sentinel := range []*status.Sentinel{status.ErrUnavailable, status.ErrResourceExhausted} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			adapter, _ := newTestAdapter(t, func(*ai.ModelRequest) (*ai.ModelResponse, error) {
				return nil, status.Errorf(sentinel, "provider says no")
			})

			_, err := adapter.Generate(context.Background(), outbound.GenerateRequest{
				Messages: userMessage("hi"),
				MaxTurns: 1,
			})
			if !errors.Is(err, outbound.ErrModelUnavailable) {
				t.Fatalf("expected ErrModelUnavailable, got %v", err)
			}
		})
	}
}

func TestGenerate_OtherProviderErrorsAreNotMisclassified(t *testing.T) {
	adapter, _ := newTestAdapter(t, func(*ai.ModelRequest) (*ai.ModelResponse, error) {
		return nil, status.Errorf(status.ErrInvalidArgument, "bad request")
	})

	_, err := adapter.Generate(context.Background(), outbound.GenerateRequest{Messages: userMessage("hi"), MaxTurns: 1})
	if err == nil || errors.Is(err, outbound.ErrModelUnavailable) || errors.Is(err, outbound.ErrToolTurnLimit) {
		t.Fatalf("expected a plain error, got %v", err)
	}
}

func TestGenerate_CallerCancellationIsPreserved(t *testing.T) {
	adapter, _ := newTestAdapter(t, func(*ai.ModelRequest) (*ai.ModelResponse, error) {
		return textResponse("unreachable"), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := adapter.Generate(ctx, outbound.GenerateRequest{Messages: userMessage("hi"), MaxTurns: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled in chain, got %v", err)
	}
}

func TestGenerate_DeadlineDuringGenerationIsPreserved(t *testing.T) {
	adapter, _ := newTestAdapter(t, func(*ai.ModelRequest) (*ai.ModelResponse, error) {
		time.Sleep(50 * time.Millisecond) // a slow provider
		return textResponse("too late"), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := adapter.Generate(ctx, outbound.GenerateRequest{Messages: userMessage("hi"), MaxTurns: 1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded in chain, got %v", err)
	}
	if errors.Is(err, outbound.ErrModelUnavailable) {
		t.Fatalf("our own timeout must not be reported as a provider outage: %v", err)
	}
}

func TestGenerate_MapsMessagesSystemPromptAndTools(t *testing.T) {
	adapter, model := newTestAdapter(t, func(*ai.ModelRequest) (*ai.ModelResponse, error) {
		return textResponse("ok"), nil
	})
	tools := []outbound.ToolHandler{
		stubTool{name: "alpha", execute: func(json.RawMessage) (string, error) { return "", nil }},
		stubTool{name: "beta", execute: func(json.RawMessage) (string, error) { return "", nil }},
	}

	_, err := adapter.Generate(context.Background(), outbound.GenerateRequest{
		SystemPrompt: "be terse",
		Messages: []conversation.Message{
			{Role: conversation.RoleUser, Content: "q1"},
			{Role: conversation.RoleAssistant, Content: "a1"},
			{Role: conversation.RoleUser, Content: "q2"},
		},
		Tools:    tools,
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	req := model.lastRequest()
	type turn struct {
		role ai.Role
		text string
	}
	want := []turn{{ai.RoleSystem, "be terse"}, {ai.RoleUser, "q1"}, {ai.RoleModel, "a1"}, {ai.RoleUser, "q2"}}
	if len(req.Messages) != len(want) {
		t.Fatalf("got %d messages, want %d", len(req.Messages), len(want))
	}
	for i, w := range want {
		if req.Messages[i].Role != w.role || req.Messages[i].Text() != w.text {
			t.Errorf("message %d = (%s, %q), want (%s, %q)", i, req.Messages[i].Role, req.Messages[i].Text(), w.role, w.text)
		}
	}

	// Genkit does not guarantee tool order, so compare as a set.
	var offered []string
	for _, def := range req.Tools {
		offered = append(offered, def.Name)
	}
	sort.Strings(offered)
	if strings.Join(offered, ",") != "alpha,beta" {
		t.Fatalf("tools offered to model = %v, want [alpha beta]", offered)
	}
}

func TestGenerate_RejectsNonPositiveMaxTurns(t *testing.T) {
	adapter, _ := newTestAdapter(t, func(*ai.ModelRequest) (*ai.ModelResponse, error) {
		return textResponse("ok"), nil
	})
	if _, err := adapter.Generate(context.Background(), outbound.GenerateRequest{Messages: userMessage("hi")}); err == nil {
		t.Fatal("expected an error for MaxTurns = 0")
	}
}

func TestNew_RequiresModelAndAPIKey(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(context.Background(), Config{APIKey: "k"}, logger); err == nil {
		t.Error("expected an error for a missing model")
	}
	if _, err := New(context.Background(), Config{Model: "googleai/x"}, logger); err == nil {
		t.Error("expected an error for a missing API key")
	}
}
