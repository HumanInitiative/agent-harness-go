// Package genkitmodel implements outbound.ModelPort on top of Genkit
// (github.com/firebase/genkit/go), Google's Go framework for building
// LLM-backed applications. This is the only package in the whole harness
// allowed to import Genkit — everything above outbound.ModelPort talks in
// the harness's own domain/port vocabulary and has no idea which SDK is
// underneath.
//
// Model selection is a single Genkit model reference string (e.g.
// "googleai/gemini-flash-latest"), so switching model versions is a config
// change, not a code change.
package genkitmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/status"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/googlegenai"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/conversation"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

// Config configures the Genkit-backed model adapter.
type Config struct {
	// Model is the Genkit model reference to generate with, e.g.
	// "googleai/gemini-flash-latest". Required.
	Model string
	// APIKey is the Gemini API key. Required. It is passed explicitly
	// (rather than letting the plugin read the environment itself) so a
	// missing key fails startup with a clear error, and so
	// internal/platform/config stays the only code that reads the
	// environment.
	APIKey string
}

// Adapter implements outbound.ModelPort using Genkit.
type Adapter struct {
	g      *genkit.Genkit
	model  string
	logger *slog.Logger
}

// New initializes Genkit with the Google AI (Gemini) plugin and returns a
// ready Adapter.
func New(ctx context.Context, cfg Config, logger *slog.Logger) (*Adapter, error) {
	if cfg.Model == "" {
		return nil, errors.New("genkitmodel: Config.Model must not be empty")
	}
	if cfg.APIKey == "" {
		return nil, errors.New("genkitmodel: Config.APIKey must not be empty")
	}
	g := genkit.Init(ctx, genkit.WithPlugins(&googlegenai.GoogleAI{APIKey: cfg.APIKey}))
	return newAdapter(g, cfg.Model, logger), nil
}

// newAdapter builds an Adapter around an already-initialized Genkit
// instance. Tests use it to plug in a fake model instead of Gemini.
func newAdapter(g *genkit.Genkit, model string, logger *slog.Logger) *Adapter {
	return &Adapter{g: g, model: model, logger: logger}
}

// Generate implements outbound.ModelPort. It translates the harness's own
// message/tool vocabulary into Genkit calls, runs Genkit's bounded
// tool-calling loop (ai.WithMaxTurns), and translates the result — or the
// failure — back.
func (a *Adapter) Generate(ctx context.Context, req outbound.GenerateRequest) (outbound.GenerateResponse, error) {
	if req.MaxTurns < 1 {
		// Genkit silently substitutes 50 for 0; refuse instead, so a
		// misconfiguration cannot quietly allow 50 rounds of tool calls.
		return outbound.GenerateResponse{}, fmt.Errorf("genkitmodel: MaxTurns must be >= 1, got %d", req.MaxTurns)
	}
	if err := ctx.Err(); err != nil {
		// Genkit does not check this before calling the provider; don't pay
		// for a model call nobody is waiting for.
		return outbound.GenerateResponse{}, fmt.Errorf("genkitmodel: generate: %w", err)
	}

	recorder := &toolCallRecorder{}
	toolRefs := make([]ai.ToolRef, 0, len(req.Tools))
	for _, handler := range req.Tools {
		toolRefs = append(toolRefs, a.adaptTool(handler, recorder))
	}

	opts := []ai.GenerateOption{
		ai.WithModelName(a.model),
		ai.WithMessages(toGenkitMessages(req.Messages)...),
		ai.WithMaxTurns(req.MaxTurns),
	}
	if req.SystemPrompt != "" {
		opts = append(opts, ai.WithSystem(req.SystemPrompt))
	}
	if len(toolRefs) > 0 {
		opts = append(opts, ai.WithTools(toolRefs...))
	}

	resp, err := genkit.Generate(ctx, a.g, opts...)
	if err != nil {
		return outbound.GenerateResponse{}, classifyError(ctx, err)
	}

	return outbound.GenerateResponse{
		Output:    resp.Text(),
		ToolCalls: recorder.records(),
	}, nil
}

// classifyError maps a Genkit failure onto the error contract documented
// on outbound.ModelPort.
func classifyError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		// Our own caller gave up (client disconnected or request timeout):
		// report that, not whatever the SDK made of the cancellation.
		return fmt.Errorf("genkitmodel: generate: %w", ctxErr)
	}
	if errors.Is(err, ai.ErrMaxTurnsExceeded) {
		return fmt.Errorf("genkitmodel: %w: %w", outbound.ErrToolTurnLimit, err)
	}
	switch status.Of(err) {
	case status.ResourceExhausted, status.Unavailable, status.DeadlineExceeded:
		retryAfter, _ := googlegenai.RetryDelay(err)
		return &outbound.ModelUnavailableError{RetryAfter: retryAfter, Cause: err}
	}
	return fmt.Errorf("genkitmodel: generate: %w", err)
}

// toGenkitMessages maps conversation messages to Genkit messages, in order.
// The system prompt is passed separately via ai.WithSystem.
func toGenkitMessages(messages []conversation.Message) []*ai.Message {
	out := make([]*ai.Message, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case conversation.RoleUser:
			out = append(out, ai.NewUserTextMessage(m.Content))
		case conversation.RoleAssistant:
			out = append(out, ai.NewModelTextMessage(m.Content))
		}
	}
	return out
}

// adaptTool wraps one outbound.ToolHandler as a Genkit tool, created fresh
// for this Generate call (ai.NewTool needs no global registration).
//
// Two properties matter for production:
//   - A failing tool does not fail the turn. Genkit aborts the whole
//     generation when a tool returns an error, so instead the failure is
//     returned to the model as the tool's result; the model can then retry
//     with corrected arguments or explain the problem to the user.
//   - A panicking tool cannot crash the process. Genkit runs tool calls on
//     their own goroutines, out of reach of the HTTP layer's panic
//     recovery, so the panic is recovered here and reported as a failure.
func (a *Adapter) adaptTool(handler outbound.ToolHandler, recorder *toolCallRecorder) ai.ToolRef {
	name := handler.Name()
	// The input type must be `any`: Genkit rejects (panics on) any other
	// type parameter when a custom schema is supplied via WithInputSchema.
	execute := func(toolCtx *ai.ToolContext, input any) (string, error) {
		args, err := json.Marshal(input)
		if err != nil {
			args = []byte("{}")
		}

		start := time.Now()
		result, execErr := runTool(toolCtx, handler, args)
		record := outbound.ToolCallRecord{
			Name:       name,
			Arguments:  args,
			Result:     result,
			DurationMS: time.Since(start).Milliseconds(),
		}

		// Arguments and results are deliberately not logged: they can carry
		// personal data. They are returned to the API caller instead.
		var panicked *toolPanic
		switch {
		case errors.As(execErr, &panicked):
			record.Result = ""
			record.Error = execErr.Error()
			a.logger.ErrorContext(toolCtx, "tool call panicked",
				"tool", name, "duration_ms", record.DurationMS,
				"panic", fmt.Sprint(panicked.value), "stack", string(panicked.stack))
		case execErr != nil:
			record.Result = ""
			record.Error = execErr.Error()
			a.logger.WarnContext(toolCtx, "tool call failed",
				"tool", name, "duration_ms", record.DurationMS, "error", execErr)
		default:
			a.logger.InfoContext(toolCtx, "tool call succeeded",
				"tool", name, "duration_ms", record.DurationMS)
		}
		recorder.add(record)

		if execErr != nil {
			return toolErrorResult(execErr), nil
		}
		return result, nil
	}

	return ai.NewTool(name, handler.Description(), execute, ai.WithInputSchema(handler.InputSchema()))
}

// toolPanic is a recovered tool panic. Its Error() deliberately omits the
// stack trace, which goes to the logs only — the model and the API caller
// need to know the call failed, not how the harness is built.
type toolPanic struct {
	value any
	stack []byte
}

func (p *toolPanic) Error() string { return fmt.Sprintf("tool panicked: %v", p.value) }

// runTool executes handler, converting a panic into a *toolPanic error.
func runTool(ctx context.Context, handler outbound.ToolHandler, args json.RawMessage) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			result, err = "", &toolPanic{value: r, stack: debug.Stack()}
		}
	}()
	return handler.Execute(ctx, args)
}

// toolErrorResult formats a tool failure as a small JSON object the model
// can read.
func toolErrorResult(err error) string {
	const maxLen = 500
	msg := err.Error()
	if len(msg) > maxLen {
		msg = msg[:maxLen]
		// Back off to a rune boundary so a multi-byte character is never
		// cut in half.
		for len(msg) > 0 && !utf8.ValidString(msg) {
			msg = msg[:len(msg)-1]
		}
	}
	encoded, _ := json.Marshal(map[string]string{"error": msg})
	return string(encoded)
}

// toolCallRecorder collects ToolCallRecords during one Generate call. Genkit
// runs the tool calls of a single model turn concurrently, so every access
// is mutex-guarded.
type toolCallRecorder struct {
	mu      sync.Mutex
	entries []outbound.ToolCallRecord
}

func (r *toolCallRecorder) add(rec outbound.ToolCallRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, rec)
}

func (r *toolCallRecorder) records() []outbound.ToolCallRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]outbound.ToolCallRecord(nil), r.entries...)
}
