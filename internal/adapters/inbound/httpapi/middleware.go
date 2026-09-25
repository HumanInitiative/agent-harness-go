package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/HumanInitiative/agent-harness-go/internal/platform/logger"
)

const requestIDHeader = "X-Request-Id"

// requestIDPattern limits which caller-supplied request IDs are trusted. An
// unbounded, arbitrary header value would otherwise flow straight into
// every log line of the request.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type requestIDKey struct{}

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// RequestID assigns every request an ID: the caller's X-Request-Id when it
// is well-formed (so a trace can span systems), a fresh UUID otherwise. The
// ID is echoed in the response header, included in error bodies, and
// attached to the logging context so every log line the request produces —
// in any layer — carries it.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !requestIDPattern.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set(requestIDHeader, id)

		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		ctx = logger.WithAttrs(ctx, slog.String("request_id", id))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestLogging logs one line per request once it completes. Server
// errors log at ERROR, client errors at WARN, everything else at INFO, so
// alerting can key off the level alone.
func RequestLogging(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			status := ww.Status()
			level := slog.LevelInfo
			switch {
			case status >= 500:
				level = slog.LevelError
			case status >= 400:
				level = slog.LevelWarn
			}
			log.Log(r.Context(), level, "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"remote_addr", r.RemoteAddr,
			)
		})
	}
}

// Recover turns a panic in any handler into a logged 500 instead of a
// dropped connection, and logs it through slog (with the request ID) rather
// than to stderr.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					// The standard library's deliberate "abort this
					// response" signal; let net/http handle it.
					panic(rec)
				}
				log.ErrorContext(r.Context(), "panic while handling request",
					"panic", rec, "stack", string(debug.Stack()))
				writeError(w, r, http.StatusInternalServerError, codeInternal, "internal server error")
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout bounds how long a request may run by putting a deadline on its
// context. Unlike chi's middleware.Timeout it writes nothing itself: the
// handler sees context.DeadlineExceeded from whatever it was waiting on and
// answers 504 through its normal error path, so a response is never written
// twice.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// LimitBody caps the request body at n bytes. Reads beyond the cap fail
// with *http.MaxBytesError, which handlers turn into 413.
func LimitBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}
