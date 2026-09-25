package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"

	"github.com/HumanInitiative/agent-harness-go/internal/platform/logger"
)

const apiKeyHeader = "X-API-Key"

// APIKeyAuth authenticates requests by a static API key in the X-API-Key
// header. Several keys can be active at once, which is what makes rotation
// possible without downtime: add the new key, move clients over, remove
// the old one.
type APIKeyAuth struct {
	keys []apiKey
}

type apiKey struct {
	hash [sha256.Size]byte
	// id is a short fingerprint of the key. It is safe to log and to use as
	// a rate-limit bucket: it identifies which key was used without
	// revealing it.
	id string
}

type apiKeyIDKey struct{}

// apiKeyIDFrom returns the fingerprint of the key that authenticated the
// request, or "" if the request did not pass through APIKeyAuth.
func apiKeyIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(apiKeyIDKey{}).(string)
	return id
}

// NewAPIKeyAuth builds the middleware. It refuses to build with no keys,
// so the API can never start accidentally open.
func NewAPIKeyAuth(keys []string) (*APIKeyAuth, error) {
	if len(keys) == 0 {
		return nil, errors.New("httpapi: at least one API key is required")
	}
	a := &APIKeyAuth{keys: make([]apiKey, 0, len(keys))}
	for _, k := range keys {
		sum := sha256.Sum256([]byte(k))
		a.keys = append(a.keys, apiKey{hash: sum, id: hex.EncodeToString(sum[:])[:12]})
	}
	return a, nil
}

// Middleware rejects requests without a valid key with 401.
func (a *APIKeyAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented := r.Header.Get(apiKeyHeader)
		if presented == "" {
			writeError(w, r, http.StatusUnauthorized, codeUnauthorized, "missing "+apiKeyHeader+" header")
			return
		}

		id, ok := a.match(presented)
		if !ok {
			writeError(w, r, http.StatusUnauthorized, codeUnauthorized, "invalid API key")
			return
		}

		ctx := context.WithValue(r.Context(), apiKeyIDKey{}, id)
		ctx = logger.WithAttrs(ctx, slog.String("api_key_id", id))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// match compares hashes rather than raw keys, so every comparison has the
// same length, and uses a constant-time compare against every configured
// key without stopping early — how long a check takes reveals nothing about
// which key, or how much of one, was right.
func (a *APIKeyAuth) match(presented string) (string, bool) {
	sum := sha256.Sum256([]byte(presented))
	matched := ""
	for _, k := range a.keys {
		if subtle.ConstantTimeCompare(sum[:], k.hash[:]) == 1 {
			matched = k.id
		}
	}
	return matched, matched != ""
}
