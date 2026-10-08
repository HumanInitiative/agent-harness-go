package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HumanInitiative/agent-harness-go/internal/platform/caller"
)

// Tools authorize by the caller's key fingerprint, so the middleware must
// put exactly KeyFingerprint(key) into the request context.
func TestAPIKeyAuth_PutsTheKeyFingerprintInTheContext(t *testing.T) {
	key := strings.Repeat("r", 64)
	auth, err := NewAPIKeyAuth([]string{strings.Repeat("o", 64), key})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	h := auth.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = caller.KeyID(r.Context()) }))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat", nil)
	req.Header.Set(apiKeyHeader, key)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got == "" || got != KeyFingerprint(key) || len(got) != 12 {
		t.Fatalf("context key id = %q, want %q", got, KeyFingerprint(key))
	}
}
