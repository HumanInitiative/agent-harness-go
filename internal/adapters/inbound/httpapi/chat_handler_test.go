package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/inbound/httpapi"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

func TestChat_Success(t *testing.T) {
	model := fakeModel{generate: func(context.Context, outbound.GenerateRequest) (outbound.GenerateResponse, error) {
		return outbound.GenerateResponse{
			Output: "12 times 7 is 84.",
			ToolCalls: []outbound.ToolCallRecord{
				{Name: "calculator", Arguments: json.RawMessage(`{"expression":"12*7"}`), Result: "84", DurationMS: 3},
			},
		}, nil
	}}
	srv := newTestServer(t, model)

	rec := srv.chat(`{"message": "What is 12 times 7?"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp httpapi.ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Reply != "12 times 7 is 84." || resp.ConversationID == "" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "calculator" || resp.ToolCalls[0].Result != "84" {
		t.Fatalf("unexpected tool calls: %+v", resp.ToolCalls)
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("missing X-Request-Id response header")
	}
}

func TestChat_ToolCallsIsAnEmptyArrayNotNull(t *testing.T) {
	rec := newTestServer(t, replyWith("hi")).chat(`{"message": "hello"}`)
	if !strings.Contains(rec.Body.String(), `"tool_calls":[]`) {
		t.Fatalf("expected tool_calls to be [], got %s", rec.Body.String())
	}
}

func TestChat_RejectsBadRequestBodies(t *testing.T) {
	srv := newTestServer(t, replyWith("unused"))
	cases := map[string]string{
		"not json":             `not json`,
		"unknown field":        `{"message": "hi", "admin": true}`,
		"trailing data":        `{"message": "hi"} {"message": "again"}`,
		"wrong type":           `{"message": 42}`,
		"empty message":        `{"message": "   "}`,
		"message over limit":   `{"message": "` + strings.Repeat("x", 201) + `"}`,
		"invalid conversation": `{"message": "hi", "conversation_id": "../etc/passwd"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			expectError(t, srv.chat(body), http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestChat_RejectsOversizedBody(t *testing.T) {
	srv := newTestServer(t, replyWith("unused"), func(c *httpapi.RouterConfig) { c.MaxRequestBytes = 64 })
	body := `{"message": "` + strings.Repeat("x", 100) + `"}`
	expectError(t, srv.chat(body), http.StatusRequestEntityTooLarge, "request_too_large")
}

func TestChat_MapsModelUnavailableTo503WithRetryAfter(t *testing.T) {
	cases := []struct {
		name       string
		retryAfter time.Duration
		want       string
	}{
		{"provider suggested a delay", 12 * time.Second, "12"},
		{"provider gave no delay", 0, "5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t, failWith(&outbound.ModelUnavailableError{RetryAfter: tc.retryAfter, Cause: errors.New("429")}))
			rec := srv.chat(`{"message": "hi"}`)
			expectError(t, rec, http.StatusServiceUnavailable, "model_unavailable")
			if got := rec.Header().Get("Retry-After"); got != tc.want {
				t.Fatalf("Retry-After = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChat_MapsToolTurnLimit(t *testing.T) {
	srv := newTestServer(t, failWith(outbound.ErrToolTurnLimit))
	expectError(t, srv.chat(`{"message": "hi"}`), http.StatusInternalServerError, "tool_turn_limit")
}

func TestChat_TimeoutReturns504Once(t *testing.T) {
	model := fakeModel{generate: func(ctx context.Context, _ outbound.GenerateRequest) (outbound.GenerateResponse, error) {
		<-ctx.Done()
		return outbound.GenerateResponse{}, ctx.Err()
	}}
	srv := newTestServer(t, model, func(c *httpapi.RouterConfig) { c.RequestTimeout = 20 * time.Millisecond })

	rec := srv.chat(`{"message": "hi"}`)
	expectError(t, rec, http.StatusGatewayTimeout, "timeout")
	// A single JSON object: the response was written exactly once.
	if strings.Count(rec.Body.String(), `"code"`) != 1 {
		t.Fatalf("response written more than once: %s", rec.Body.String())
	}
}

func TestChat_UnexpectedErrorDoesNotLeakDetails(t *testing.T) {
	srv := newTestServer(t, failWith(errors.New("dial tcp 10.0.0.5:5432: secret internal detail")))
	body := expectError(t, srv.chat(`{"message": "hi"}`), http.StatusInternalServerError, "internal_error")
	if strings.Contains(body.Error, "10.0.0.5") {
		t.Fatalf("internal error detail leaked to client: %q", body.Error)
	}
}

func TestChat_ConcurrentTurnOnSameConversationGets409(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	model := fakeModel{generate: func(context.Context, outbound.GenerateRequest) (outbound.GenerateResponse, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return outbound.GenerateResponse{Output: "done"}, nil
	}}
	srv := newTestServer(t, model)

	done := make(chan int, 1)
	go func() { done <- srv.chat(`{"message": "first", "conversation_id": "conv-1"}`).Code }()
	<-started

	expectError(t, srv.chat(`{"message": "second", "conversation_id": "conv-1"}`), http.StatusConflict, "conversation_busy")

	close(release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("first turn status = %d", code)
	}
}

func TestChat_PanicInsideRequestBecomes500(t *testing.T) {
	model := fakeModel{generate: func(context.Context, outbound.GenerateRequest) (outbound.GenerateResponse, error) {
		panic("unexpected nil pointer somewhere")
	}}
	srv := newTestServer(t, model)

	expectError(t, srv.chat(`{"message": "hi"}`), http.StatusInternalServerError, "internal_error")

	found := false
	for _, line := range srv.logs.lines(t) {
		if line["msg"] == "panic while handling request" && line["stack"] != nil && line["request_id"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("panic was not logged with a stack trace and request_id")
	}
}
