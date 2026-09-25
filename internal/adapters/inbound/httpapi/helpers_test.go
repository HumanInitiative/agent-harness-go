package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/inbound/httpapi"
	"github.com/HumanInitiative/agent-harness-go/internal/application/agent"
	"github.com/HumanInitiative/agent-harness-go/internal/domain/conversation"
	"github.com/HumanInitiative/agent-harness-go/internal/platform/logger"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

var (
	testKey      = strings.Repeat("a", 64)
	otherTestKey = strings.Repeat("b", 64)
)

// fakeModel delegates to generate, so each test scripts the model's
// behaviour — including blocking until the request context ends.
type fakeModel struct {
	generate func(ctx context.Context, req outbound.GenerateRequest) (outbound.GenerateResponse, error)
}

func (f fakeModel) Generate(ctx context.Context, req outbound.GenerateRequest) (outbound.GenerateResponse, error) {
	return f.generate(ctx, req)
}

func replyWith(text string) fakeModel {
	return fakeModel{generate: func(context.Context, outbound.GenerateRequest) (outbound.GenerateResponse, error) {
		return outbound.GenerateResponse{Output: text}, nil
	}}
}

func failWith(err error) fakeModel {
	return fakeModel{generate: func(context.Context, outbound.GenerateRequest) (outbound.GenerateResponse, error) {
		return outbound.GenerateResponse{}, err
	}}
}

type fakeStore struct {
	mu   sync.Mutex
	data map[string][]conversation.Message
}

func (f *fakeStore) History(_ context.Context, id string) ([]conversation.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]conversation.Message(nil), f.data[id]...), nil
}

func (f *fakeStore) Append(_ context.Context, id string, m ...conversation.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[id] = append(f.data[id], m...)
	return nil
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, raw := range bytes.Split(bytes.TrimSpace(b.buf.Bytes()), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatalf("log line is not JSON: %s", raw)
		}
		out = append(out, line)
	}
	return out
}

type testServer struct {
	handler http.Handler
	logs    *syncBuffer
}

func defaultRouterConfig() httpapi.RouterConfig {
	return httpapi.RouterConfig{
		RequestTimeout:     5 * time.Second,
		MaxRequestBytes:    1024,
		APIKeys:            []string{testKey, otherTestKey},
		RateLimitPerMinute: 1000,
		RateLimitBurst:     1000,
	}
}

func newTestServer(t *testing.T, model outbound.ModelPort, mutate ...func(*httpapi.RouterConfig)) testServer {
	t.Helper()
	logs := &syncBuffer{}
	log := logger.New(logs, slog.LevelDebug, "json")

	svc, err := agent.NewService(model, &fakeStore{data: map[string][]conversation.Message{}}, nil,
		agent.Config{MaxToolTurns: 2, HistoryWindow: 10, MaxMessageChars: 200}, log)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	cfg := defaultRouterConfig()
	for _, m := range mutate {
		m(&cfg)
	}
	handler, err := httpapi.NewRouter(httpapi.NewChatHandler(svc, log), log, cfg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return testServer{handler: handler, logs: logs}
}

// do sends a request; key "" sends no X-API-Key header.
func (s testServer) do(method, path, key, body string, headers ...string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func (s testServer) chat(body string) *httptest.ResponseRecorder {
	return s.do(http.MethodPost, "/api/v1/chat", testKey, body)
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) httpapi.ErrorResponse {
	t.Helper()
	var body httpapi.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v: %s", err, rec.Body.String())
	}
	return body
}

func expectError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) httpapi.ErrorResponse {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, status, rec.Body.String())
	}
	body := decodeError(t, rec)
	if body.Code != code {
		t.Fatalf("code = %q, want %q; body: %s", body.Code, code, rec.Body.String())
	}
	if body.RequestID == "" {
		t.Fatalf("error body has no request_id: %s", rec.Body.String())
	}
	return body
}
