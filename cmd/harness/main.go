// Command harness runs the Human Initiative AI agent harness: an HTTP
// service that lets a Genkit-backed LLM answer questions and call
// registered tools to do so.
//
// This is the composition root — the one place that knows every concrete
// adapter and wires them together behind the ports
// internal/application/agent depends on. See the "Architecture" section of
// README.md for the layer breakdown.
//
//	@title						Human Initiative Agent Harness API
//	@version					1.0
//	@description				AI agent harness with tool calling, built with Go and Genkit.
//	@contact.name				Human Initiative Engineering
//	@BasePath					/
//	@securityDefinitions.apikey	ApiKeyAuth
//	@in							header
//	@name						X-API-Key
//	@description				API key issued by the Human Initiative engineering team.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // embed timezone data so get_current_time works on any base image

	_ "github.com/HumanInitiative/agent-harness-go/api/swagger" // registers the generated OpenAPI spec
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/inbound/httpapi"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/genkitmodel"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/memory"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/tools"
	"github.com/HumanInitiative/agent-harness-go/internal/application/agent"
	"github.com/HumanInitiative/agent-harness-go/internal/platform/config"
	"github.com/HumanInitiative/agent-harness-go/internal/platform/logger"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet (e.g. config failed to load), so
		// report on stderr directly.
		fmt.Fprintln(os.Stderr, "harness: fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logger.New(os.Stdout, cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(log)

	// Canceled on SIGINT/SIGTERM; that is the signal to start shutting down.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	model, err := genkitmodel.New(ctx, genkitmodel.Config{
		Model:  cfg.GenkitModel,
		APIKey: cfg.GeminiAPIKey,
	}, log)
	if err != nil {
		return fmt.Errorf("initialize model adapter: %w", err)
	}

	conversations, err := memory.New(memory.Options{
		MaxConversations:           cfg.MaxConversations,
		MaxMessagesPerConversation: cfg.HistoryWindow + 2, // the window plus the turn being added
		TTL:                        cfg.ConversationTTL,
	})
	if err != nil {
		return fmt.Errorf("initialize conversation store: %w", err)
	}

	registeredTools := []outbound.ToolHandler{
		tools.NewCurrentTimeTool(),
		tools.NewCalculatorTool(),
	}
	if cfg.Web.Enabled {
		webTools, err := buildWebTools(cfg.Web, log)
		if err != nil {
			return fmt.Errorf("initialize web tools: %w", err)
		}
		registeredTools = append(registeredTools, webTools...)
	}

	service, err := agent.NewService(model, conversations, registeredTools, agent.Config{
		SystemPrompt:    cfg.SystemPrompt,
		MaxToolTurns:    cfg.MaxToolTurns,
		HistoryWindow:   cfg.HistoryWindow,
		MaxMessageChars: cfg.MaxMessageChars,
	}, log)
	if err != nil {
		return fmt.Errorf("initialize agent service: %w", err)
	}

	router, err := httpapi.NewRouter(httpapi.NewChatHandler(service, log), log, httpapi.RouterConfig{
		RequestTimeout:     cfg.RequestTimeout,
		MaxRequestBytes:    cfg.MaxRequestBytes,
		SwaggerEnabled:     cfg.SwaggerEnabled,
		APIKeys:            cfg.APIKeys,
		RateLimitPerMinute: cfg.RateLimitPerMinute,
		RateLimitBurst:     cfg.RateLimitBurst,
	})
	if err != nil {
		return fmt.Errorf("initialize router: %w", err)
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		// Bodies are capped at MaxRequestBytes, so reading one should take
		// seconds at most; this stops slow-body (slowloris) connections.
		ReadTimeout: 15 * time.Second,
		// Must outlive the request timeout, or the connection would be cut
		// before the handler can send its 504.
		WriteTimeout:   cfg.RequestTimeout + 10*time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 32 << 10,
		// Route net/http's own errors (TLS handshakes, bad requests) through
		// the structured logger instead of the standard library's stderr log.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	log.Info("starting harness", "config", cfg, "tools_registered", len(registeredTools))

	serveErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil

	case <-ctx.Done():
		// Stop listening for signals: a second Ctrl+C now kills the process
		// immediately instead of being swallowed.
		stop()
		log.Info("shutdown signal received, draining in-flight requests", "timeout", cfg.ShutdownTimeout)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		log.Info("shutdown complete")
		return nil
	}
}

// buildWebTools wires the websearch library into the web_search and
// web_fetch tools.
func buildWebTools(cfg config.WebConfig, log *slog.Logger) ([]outbound.ToolHandler, error) {
	metrics := websearch.NewMetrics()

	// PDF reading is optional: without pdftotext the fetcher reports PDFs as
	// unreadable instead of the harness refusing to start.
	var pdf websearch.PDFExtractor
	if p, err := websearch.NewPDFToText(30*time.Second, 2<<20); err != nil {
		log.Warn("PDF reading disabled", "reason", err)
	} else {
		pdf = p
	}

	fetcher, err := websearch.NewFetcher(websearch.FetcherOptions{
		UserAgent:           cfg.UserAgent,
		Timeout:             cfg.FetchTimeout,
		MaxBodyBytes:        cfg.MaxBodyBytes,
		RespectRobots:       cfg.RespectRobotsOnFetch,
		Guard:               websearch.Guard{},
		DomainRatePerSecond: cfg.DomainRatePerSecond,
		CacheTTL:            cfg.CacheTTL,
		PDF:                 pdf,
		Metrics:             metrics,
	}, log)
	if err != nil {
		return nil, err
	}

	// Search providers talk to fixed, operator-chosen endpoints, so they use
	// a plain client; the SSRF-guarded client is for model-chosen URLs.
	searchClient := &http.Client{Timeout: cfg.FetchTimeout}
	providers := make([]websearch.Provider, 0, len(cfg.Providers))
	for _, name := range cfg.Providers {
		switch name {
		case "searxng":
			p, err := websearch.NewSearXNG(cfg.SearXNGURL, searchClient, cfg.UserAgent, cfg.SearchLanguage)
			if err != nil {
				return nil, err
			}
			providers = append(providers, p)
		case "duckduckgo":
			// DuckDuckGo's region code for Indonesia in Indonesian is "id-id";
			// other languages get no regional bias.
			region := ""
			if cfg.SearchLanguage == "id" {
				region = "id-id"
			}
			providers = append(providers, websearch.NewDuckDuckGo(websearch.DuckDuckGoHTMLURL, searchClient, cfg.UserAgent, region))
		}
	}
	router, err := websearch.NewRouter(providers, websearch.RouterOptions{
		ProviderTimeout: cfg.FetchTimeout,
		CacheTTL:        cfg.CacheTTL,
		Metrics:         metrics,
	}, log)
	if err != nil {
		return nil, err
	}

	log.Info("web tools enabled", "providers", cfg.Providers, "respect_robots_on_fetch", cfg.RespectRobotsOnFetch,
		"pdf", pdf != nil)
	return []outbound.ToolHandler{
		tools.NewWebSearchTool(router, nil),
		tools.NewWebFetchTool(fetcher),
	}, nil
}
