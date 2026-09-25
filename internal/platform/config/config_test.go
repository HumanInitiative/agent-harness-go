package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/platform/config"
)

var validKey = strings.Repeat("k", 64)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func requiredOnly() map[string]string {
	return map[string]string{"GEMINI_API_KEY": "gemini-secret", "API_KEYS": validKey}
}

func TestLoadFrom_AppliesDefaults(t *testing.T) {
	cfg, err := config.LoadFrom(env(requiredOnly()))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	checks := map[string]bool{
		"HTTPAddr":         cfg.HTTPAddr == ":8080",
		"RequestTimeout":   cfg.RequestTimeout == 60*time.Second,
		"ShutdownTimeout":  cfg.ShutdownTimeout == 70*time.Second,
		"SwaggerEnabled":   !cfg.SwaggerEnabled,
		"LogLevel":         cfg.LogLevel == slog.LevelInfo,
		"LogFormat":        cfg.LogFormat == "json",
		"MaxToolTurns":     cfg.MaxToolTurns == 4,
		"HistoryWindow":    cfg.HistoryWindow == 20,
		"ConversationTTL":  cfg.ConversationTTL == 24*time.Hour,
		"RateLimit":        cfg.RateLimitPerMinute == 30 && cfg.RateLimitBurst == 10,
		"MaxRequestBytes":  cfg.MaxRequestBytes == 64*1024,
		"SystemPrompt set": cfg.SystemPrompt != "",
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("default for %s not applied: %+v", name, cfg)
		}
	}
}

func TestLoadFrom_ParsesOverrides(t *testing.T) {
	vars := requiredOnly()
	vars["API_KEYS"] = validKey + " , " + strings.Repeat("z", 40)
	vars["LOG_LEVEL"] = "debug"
	vars["LOG_FORMAT"] = "TEXT"
	vars["SWAGGER_ENABLED"] = "true"
	vars["HISTORY_WINDOW_MESSAGES"] = "0"

	cfg, err := config.LoadFrom(env(vars))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if len(cfg.APIKeys) != 2 || cfg.APIKeys[0] != validKey {
		t.Errorf("API_KEYS not split and trimmed: %q", cfg.APIKeys)
	}
	if cfg.LogLevel != slog.LevelDebug || cfg.LogFormat != "text" || !cfg.SwaggerEnabled || cfg.HistoryWindow != 0 {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadFrom_FallsBackToGoogleAPIKey(t *testing.T) {
	cfg, err := config.LoadFrom(env(map[string]string{"GOOGLE_API_KEY": "g", "API_KEYS": validKey}))
	if err != nil || cfg.GeminiAPIKey != "g" {
		t.Fatalf("expected GOOGLE_API_KEY fallback, got %q, %v", cfg.GeminiAPIKey, err)
	}
}

func TestLoadFrom_RejectsInvalidConfiguration(t *testing.T) {
	cases := map[string]map[string]string{
		"missing gemini key": {"API_KEYS": validKey},
		"missing api keys":   {"GEMINI_API_KEY": "g"},
		"short api key":      {"GEMINI_API_KEY": "g", "API_KEYS": "short"},
		"non-numeric int":    merge(requiredOnly(), "MAX_TOOL_TURNS", "four"),
		"zero tool turns":    merge(requiredOnly(), "MAX_TOOL_TURNS", "0"),
		"negative history":   merge(requiredOnly(), "HISTORY_WINDOW_MESSAGES", "-1"),
		"bad log level":      merge(requiredOnly(), "LOG_LEVEL", "verbose"),
		"bad log format":     merge(requiredOnly(), "LOG_FORMAT", "xml"),
		"bad bool":           merge(requiredOnly(), "SWAGGER_ENABLED", "yes please"),
		"shutdown shorter than request": merge(merge(requiredOnly(),
			"REQUEST_TIMEOUT_SECONDS", "60"), "SHUTDOWN_TIMEOUT_SECONDS", "30"),
	}
	for name, vars := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := config.LoadFrom(env(vars)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestLoadFrom_ReportsEveryProblemAtOnce(t *testing.T) {
	_, err := config.LoadFrom(env(map[string]string{"MAX_TOOL_TURNS": "x", "LOG_FORMAT": "xml"}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"GEMINI_API_KEY", "API_KEYS", "MAX_TOOL_TURNS", "LOG_FORMAT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestConfig_LogValueNeverContainsSecrets(t *testing.T) {
	cfg, err := config.LoadFrom(env(requiredOnly()))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	rendered := cfg.LogValue().String()
	for _, secret := range []string{validKey, "gemini-secret"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("secret leaked into log value: %s", rendered)
		}
	}
}

func merge(base map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out[key] = value
	return out
}
