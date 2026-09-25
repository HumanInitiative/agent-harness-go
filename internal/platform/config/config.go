// Package config loads harness configuration from environment variables.
//
// It is the only code in the harness that reads the environment, and the
// only place defaults are defined. Load validates everything up front so a
// misconfigured deployment fails at startup with a clear message instead of
// misbehaving under traffic.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// minAPIKeyLength rejects short, guessable keys. `openssl rand -hex 32`
// produces 64 characters.
const minAPIKeyLength = 32

// Config holds every setting the harness needs to start.
type Config struct {
	// HTTP server.
	HTTPAddr        string
	RequestTimeout  time.Duration // upper bound on one request, model call included
	ShutdownTimeout time.Duration // must be >= RequestTimeout
	MaxRequestBytes int64
	SwaggerEnabled  bool

	// Access control.
	APIKeys            []string // accepted X-API-Key values; secret
	RateLimitPerMinute int      // sustained requests per minute per API key
	RateLimitBurst     int      // short bursts allowed above the sustained rate

	// Logging.
	LogLevel  slog.Level
	LogFormat string // "json" or "text"

	// Model.
	GeminiAPIKey string // secret
	GenkitModel  string

	// Agent behaviour.
	SystemPrompt    string
	MaxToolTurns    int
	MaxMessageChars int
	HistoryWindow   int

	// In-memory conversation store.
	MaxConversations int
	ConversationTTL  time.Duration
}

// LogValue implements slog.LogValuer so a Config can be logged without ever
// printing a secret.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("http_addr", c.HTTPAddr),
		slog.Duration("request_timeout", c.RequestTimeout),
		slog.Duration("shutdown_timeout", c.ShutdownTimeout),
		slog.Int64("max_request_bytes", c.MaxRequestBytes),
		slog.Bool("swagger_enabled", c.SwaggerEnabled),
		slog.Int("api_keys", len(c.APIKeys)),
		slog.Int("rate_limit_per_minute", c.RateLimitPerMinute),
		slog.Int("rate_limit_burst", c.RateLimitBurst),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("log_format", c.LogFormat),
		slog.String("genkit_model", c.GenkitModel),
		slog.Int("max_tool_turns", c.MaxToolTurns),
		slog.Int("max_message_chars", c.MaxMessageChars),
		slog.Int("history_window", c.HistoryWindow),
		slog.Int("max_conversations", c.MaxConversations),
		slog.Duration("conversation_ttl", c.ConversationTTL),
	)
}

// Load reads Config from the process environment.
func Load() (Config, error) {
	return LoadFrom(os.Getenv)
}

// LoadFrom reads Config through getenv, applying defaults for unset
// variables and returning every validation problem at once.
func LoadFrom(getenv func(string) string) (Config, error) {
	r := reader{getenv: getenv}

	cfg := Config{
		HTTPAddr:        r.str("HTTP_ADDR", ":8080"),
		RequestTimeout:  r.seconds("REQUEST_TIMEOUT_SECONDS", 60),
		ShutdownTimeout: r.seconds("SHUTDOWN_TIMEOUT_SECONDS", 70),
		MaxRequestBytes: int64(r.positiveInt("MAX_REQUEST_BYTES", 64*1024)),
		SwaggerEnabled:  r.boolean("SWAGGER_ENABLED", false),

		APIKeys:            r.list("API_KEYS"),
		RateLimitPerMinute: r.positiveInt("RATE_LIMIT_PER_MINUTE", 30),
		RateLimitBurst:     r.positiveInt("RATE_LIMIT_BURST", 10),

		LogLevel:  r.level("LOG_LEVEL", slog.LevelInfo),
		LogFormat: r.oneOf("LOG_FORMAT", "json", "json", "text"),

		GeminiAPIKey: r.firstOf("GEMINI_API_KEY", "GOOGLE_API_KEY"),
		GenkitModel:  r.str("GENKIT_MODEL", "googleai/gemini-flash-latest"),

		SystemPrompt:    r.str("SYSTEM_PROMPT", defaultSystemPrompt),
		MaxToolTurns:    r.positiveInt("MAX_TOOL_TURNS", 4),
		MaxMessageChars: r.positiveInt("MAX_MESSAGE_CHARS", 8000),
		HistoryWindow:   r.nonNegativeInt("HISTORY_WINDOW_MESSAGES", 20),

		MaxConversations: r.positiveInt("MAX_CONVERSATIONS", 10000),
		ConversationTTL:  r.minutes("CONVERSATION_TTL_MINUTES", 24*60),
	}

	if cfg.GeminiAPIKey == "" {
		r.fail("GEMINI_API_KEY is required")
	}
	if len(cfg.APIKeys) == 0 {
		r.fail("API_KEYS is required (comma-separated; generate one with `openssl rand -hex 32`)")
	}
	for i, k := range cfg.APIKeys {
		if len(k) < minAPIKeyLength {
			r.fail(fmt.Sprintf("API_KEYS entry %d is %d characters; the minimum is %d", i+1, len(k), minAPIKeyLength))
		}
	}
	if cfg.ShutdownTimeout < cfg.RequestTimeout {
		r.fail(fmt.Sprintf("SHUTDOWN_TIMEOUT_SECONDS (%s) must be >= REQUEST_TIMEOUT_SECONDS (%s), "+
			"otherwise shutdown cuts off requests that are still within their allowed time",
			cfg.ShutdownTimeout, cfg.RequestTimeout))
	}

	if err := errors.Join(r.errs...); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

const defaultSystemPrompt = "You are the Human Initiative AI assistant. Be concise and accurate. " +
	"Use the available tools whenever they give a more reliable answer than reasoning alone. " +
	"If a tool reports an error, correct the input and retry once, or explain the problem to the user."

// reader collects parse errors instead of stopping at the first one, so an
// operator fixes every mistake in one pass.
type reader struct {
	getenv func(string) string
	errs   []error
}

func (r *reader) fail(msg string) { r.errs = append(r.errs, errors.New(msg)) }

func (r *reader) str(key, def string) string {
	if v := strings.TrimSpace(r.getenv(key)); v != "" {
		return v
	}
	return def
}

func (r *reader) firstOf(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(r.getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func (r *reader) list(key string) []string {
	var out []string
	for _, part := range strings.Split(r.getenv(key), ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (r *reader) int(key string, def int, valid func(int) bool, rule string) int {
	raw := strings.TrimSpace(r.getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || !valid(n) {
		r.fail(fmt.Sprintf("%s must be %s, got %q", key, rule, raw))
		return def
	}
	return n
}

func (r *reader) positiveInt(key string, def int) int {
	return r.int(key, def, func(n int) bool { return n > 0 }, "a positive integer")
}

func (r *reader) nonNegativeInt(key string, def int) int {
	return r.int(key, def, func(n int) bool { return n >= 0 }, "a non-negative integer")
}

func (r *reader) seconds(key string, def int) time.Duration {
	return time.Duration(r.positiveInt(key, def)) * time.Second
}

func (r *reader) minutes(key string, def int) time.Duration {
	return time.Duration(r.positiveInt(key, def)) * time.Minute
}

func (r *reader) boolean(key string, def bool) bool {
	raw := strings.TrimSpace(r.getenv(key))
	if raw == "" {
		return def
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		r.fail(fmt.Sprintf("%s must be true or false, got %q", key, raw))
		return def
	}
	return b
}

func (r *reader) oneOf(key, def string, allowed ...string) string {
	v := strings.ToLower(r.str(key, def))
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	r.fail(fmt.Sprintf("%s must be one of %s, got %q", key, strings.Join(allowed, ", "), v))
	return def
}

func (r *reader) level(key string, def slog.Level) slog.Level {
	raw := strings.TrimSpace(r.getenv(key))
	if raw == "" {
		return def
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(raw)); err != nil {
		r.fail(fmt.Sprintf("%s must be one of debug, info, warn, error, got %q", key, raw))
		return def
	}
	return lvl
}
