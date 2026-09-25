// Package httpapi is the harness's inbound HTTP adapter: it turns
// JSON-over-HTTP requests into calls against internal/application/agent and
// results back into JSON responses. It contains no business logic, so the
// same use case could be driven by another transport (a CLI, a queue
// consumer) without changing internal/application.
package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	httpSwagger "github.com/swaggo/http-swagger/v2"
)

// RouterConfig holds the transport-level settings of the HTTP API.
type RouterConfig struct {
	// RequestTimeout bounds one /api/v1 request, model call included.
	RequestTimeout time.Duration
	// MaxRequestBytes caps the size of a request body.
	MaxRequestBytes int64
	// SwaggerEnabled mounts the Swagger UI at /swagger/. Disable it in
	// production unless the API documentation is meant to be public.
	SwaggerEnabled bool
	// APIKeys are the accepted X-API-Key values. At least one is required.
	APIKeys []string
	// RateLimitPerMinute and RateLimitBurst configure the per-key limit.
	RateLimitPerMinute int
	RateLimitBurst     int
}

// NewRouter builds the complete HTTP router.
//
// Middleware order, outermost first:
//
//	RequestID -> RequestLogging -> Recover         (every route)
//	LimitBody -> APIKeyAuth -> RateLimiter -> Timeout   (/api/v1 only)
//
// RequestID comes first so every later log line carries the ID; logging
// wraps Recover so a recovered panic is still logged as a 500; auth runs
// before rate limiting because limits are counted per API key; the timeout
// starts last so time spent rejecting a request is not charged to it.
func NewRouter(chat *ChatHandler, log *slog.Logger, cfg RouterConfig) (http.Handler, error) {
	if cfg.RequestTimeout <= 0 {
		return nil, errors.New("httpapi: RequestTimeout must be positive")
	}
	if cfg.MaxRequestBytes <= 0 {
		return nil, errors.New("httpapi: MaxRequestBytes must be positive")
	}
	auth, err := NewAPIKeyAuth(cfg.APIKeys)
	if err != nil {
		return nil, err
	}
	limiter, err := NewRateLimiter(cfg.RateLimitPerMinute, cfg.RateLimitBurst)
	if err != nil {
		return nil, err
	}

	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(RequestLogging(log))
	r.Use(Recover(log))

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, codeNotFound, "no such endpoint")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed for this endpoint")
	})

	r.Get("/healthz", Healthz)
	r.Get("/readyz", Readyz)

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(LimitBody(cfg.MaxRequestBytes))
		api.Use(auth.Middleware)
		api.Use(limiter.Middleware)
		api.Use(Timeout(cfg.RequestTimeout))
		api.Method(http.MethodPost, "/chat", chat)
	})

	if cfg.SwaggerEnabled {
		// Generated from the @-annotations by `make swagger`.
		r.Get("/swagger/*", httpSwagger.WrapHandler)
	}

	return r, nil
}
