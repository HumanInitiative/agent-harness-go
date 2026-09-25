package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/HumanInitiative/agent-harness-go/internal/platform/logger"
)

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log output is not JSON: %v: %s", err, buf.String())
	}
	return line
}

func TestContextAttrsAppearOnEveryContextLogCall(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(&buf, slog.LevelInfo, "json")

	ctx := logger.WithAttrs(context.Background(), slog.String("request_id", "req-1"))
	ctx = logger.WithAttrs(ctx, slog.String("api_key_id", "abc"))
	log.InfoContext(ctx, "hello", "extra", 1)

	line := decode(t, &buf)
	if line["request_id"] != "req-1" || line["api_key_id"] != "abc" || line["extra"] != float64(1) {
		t.Fatalf("context attributes missing: %v", line)
	}
}

func TestWithAttrsDoesNotMutateParentContext(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(&buf, slog.LevelInfo, "json")

	parent := logger.WithAttrs(context.Background(), slog.String("a", "1"))
	_ = logger.WithAttrs(parent, slog.String("b", "2"))
	log.InfoContext(parent, "parent only")

	line := decode(t, &buf)
	if _, leaked := line["b"]; leaked {
		t.Fatalf("child attribute leaked into parent context: %v", line)
	}
}

func TestLevelIsRespected(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(&buf, slog.LevelWarn, "json")
	log.Info("dropped")
	if buf.Len() != 0 {
		t.Fatalf("info line written at warn level: %s", buf.String())
	}
}
