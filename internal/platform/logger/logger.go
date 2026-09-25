// Package logger builds the process-wide structured logger (log/slog, JSON
// in production, text for local development) and lets request-scoped
// attributes travel through context.Context.
//
// Any layer can attach attributes to a context with WithAttrs; every
// *Context logging call made with that context (InfoContext, ErrorContext,
// ...) then includes them automatically. That is how a request_id set once
// by the HTTP middleware shows up on the log lines of the application
// service and of every tool call, without any of them knowing about HTTP.
package logger

import (
	"context"
	"io"
	"log/slog"
)

// New builds a logger writing to w at the given level, as "json" or "text".
// Values are validated by internal/platform/config; any format other than
// "text" produces JSON.
func New(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}

	var base slog.Handler
	if format == "text" {
		base = slog.NewTextHandler(w, opts)
	} else {
		base = slog.NewJSONHandler(w, opts)
	}
	return slog.New(contextHandler{Handler: base})
}

type ctxKey struct{}

// WithAttrs returns a copy of ctx carrying attrs in addition to any
// attributes it already carries.
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	existing, _ := ctx.Value(ctxKey{}).([]slog.Attr)
	merged := make([]slog.Attr, 0, len(existing)+len(attrs))
	merged = append(merged, existing...)
	merged = append(merged, attrs...)
	return context.WithValue(ctx, ctxKey{}, merged)
}

// contextHandler adds the attributes stored by WithAttrs to every record.
type contextHandler struct {
	slog.Handler
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if attrs, ok := ctx.Value(ctxKey{}).([]slog.Attr); ok {
		r.AddAttrs(attrs...)
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{Handler: h.Handler.WithGroup(name)}
}
