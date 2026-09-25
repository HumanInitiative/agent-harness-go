package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/inbound/httpapi"
)

func TestAuth_RejectsMissingAndInvalidKeys(t *testing.T) {
	srv := newTestServer(t, replyWith("hi"))

	expectError(t, srv.do(http.MethodPost, "/api/v1/chat", "", `{"message":"hi"}`), http.StatusUnauthorized, "unauthorized")
	expectError(t, srv.do(http.MethodPost, "/api/v1/chat", "wrong-key", `{"message":"hi"}`), http.StatusUnauthorized, "unauthorized")
	expectError(t, srv.do(http.MethodPost, "/api/v1/chat", testKey+"x", `{"message":"hi"}`), http.StatusUnauthorized, "unauthorized")
}

func TestAuth_AcceptsEveryConfiguredKeyForRotation(t *testing.T) {
	srv := newTestServer(t, replyWith("hi"))
	for _, key := range []string{testKey, otherTestKey} {
		if rec := srv.do(http.MethodPost, "/api/v1/chat", key, `{"message":"hi"}`); rec.Code != http.StatusOK {
			t.Fatalf("key rejected: status %d", rec.Code)
		}
	}
}

func TestAuth_LogsKeyFingerprintNeverTheKey(t *testing.T) {
	srv := newTestServer(t, replyWith("hi"))
	srv.chat(`{"message":"hi"}`)

	sawFingerprint := false
	for _, line := range srv.logs.lines(t) {
		for _, v := range line {
			if s, ok := v.(string); ok && strings.Contains(s, testKey) {
				t.Fatalf("API key leaked into logs: %v", line)
			}
		}
		if id, ok := line["api_key_id"].(string); ok && id != "" {
			sawFingerprint = true
		}
	}
	if !sawFingerprint {
		t.Fatal("expected api_key_id on authenticated log lines")
	}
}

func TestProbesAreOpenWithoutAPIKey(t *testing.T) {
	srv := newTestServer(t, replyWith("hi"))
	for _, path := range []string{"/healthz", "/readyz"} {
		if rec := srv.do(http.MethodGet, path, "", ""); rec.Code != http.StatusOK {
			t.Errorf("%s status = %d", path, rec.Code)
		}
	}
}

func TestRateLimit_IsPerKeyAndSetsRetryAfter(t *testing.T) {
	srv := newTestServer(t, replyWith("hi"), func(c *httpapi.RouterConfig) {
		c.RateLimitPerMinute = 1
		c.RateLimitBurst = 1
	})

	if rec := srv.do(http.MethodPost, "/api/v1/chat", testKey, `{"message":"hi"}`); rec.Code != http.StatusOK {
		t.Fatalf("first request status = %d", rec.Code)
	}
	rec := srv.do(http.MethodPost, "/api/v1/chat", testKey, `{"message":"hi"}`)
	expectError(t, rec, http.StatusTooManyRequests, "rate_limited")
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}

	// Another client has its own budget.
	if rec := srv.do(http.MethodPost, "/api/v1/chat", otherTestKey, `{"message":"hi"}`); rec.Code != http.StatusOK {
		t.Fatalf("other key was throttled by the first key's usage: status %d", rec.Code)
	}
}

func TestRequestID_HonoursWellFormedAndReplacesMalformed(t *testing.T) {
	srv := newTestServer(t, replyWith("hi"))

	rec := srv.do(http.MethodGet, "/healthz", "", "", "X-Request-Id", "trace-abc.123")
	if got := rec.Header().Get("X-Request-Id"); got != "trace-abc.123" {
		t.Fatalf("well-formed request ID not honoured: %q", got)
	}

	malicious := "evil\" injected=1 " + strings.Repeat("x", 200)
	rec = srv.do(http.MethodGet, "/healthz", "", "", "X-Request-Id", malicious)
	if got := rec.Header().Get("X-Request-Id"); got == malicious || got == "" {
		t.Fatalf("malformed request ID should be replaced, got %q", got)
	}
}

// TestLogCorrelation proves the audit finding is fixed: log lines written by
// the application layer (not just the HTTP access log) carry the request ID.
func TestLogCorrelation_EveryLayerLogsTheRequestID(t *testing.T) {
	srv := newTestServer(t, replyWith("hi"))
	srv.do(http.MethodPost, "/api/v1/chat", testKey, `{"message":"hi"}`, "X-Request-Id", "corr-1")

	seen := map[string]bool{}
	for _, line := range srv.logs.lines(t) {
		if line["request_id"] == "corr-1" {
			seen[line["msg"].(string)] = true
		}
	}
	for _, msg := range []string{"generating reply", "reply generated", "http request"} {
		if !seen[msg] {
			t.Errorf("log line %q does not carry the request ID; saw %v", msg, seen)
		}
	}
}

func TestSwagger_OnlyMountedWhenEnabled(t *testing.T) {
	off := newTestServer(t, replyWith("hi"))
	expectError(t, off.do(http.MethodGet, "/swagger/index.html", "", ""), http.StatusNotFound, "not_found")

	on := newTestServer(t, replyWith("hi"), func(c *httpapi.RouterConfig) { c.SwaggerEnabled = true })
	if rec := on.do(http.MethodGet, "/swagger/index.html", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("swagger UI status = %d", rec.Code)
	}
}

func TestUnknownRoutesAndMethodsReturnJSONErrors(t *testing.T) {
	srv := newTestServer(t, replyWith("hi"))
	expectError(t, srv.do(http.MethodGet, "/nope", "", ""), http.StatusNotFound, "not_found")
	expectError(t, srv.do(http.MethodDelete, "/healthz", "", ""), http.StatusMethodNotAllowed, "method_not_allowed")
}

func TestNewRouter_RejectsInvalidConfig(t *testing.T) {
	cases := map[string]func(*httpapi.RouterConfig){
		"no api keys":     func(c *httpapi.RouterConfig) { c.APIKeys = nil },
		"zero timeout":    func(c *httpapi.RouterConfig) { c.RequestTimeout = 0 },
		"zero body limit": func(c *httpapi.RouterConfig) { c.MaxRequestBytes = 0 },
		"zero rate limit": func(c *httpapi.RouterConfig) { c.RateLimitPerMinute = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := defaultRouterConfig()
			mutate(&cfg)
			if _, err := httpapi.NewRouter(nil, nil, cfg); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
