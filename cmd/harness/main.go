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
	"github.com/HumanInitiative/agent-harness-go/internal/bootstrap"
	"github.com/HumanInitiative/agent-harness-go/internal/platform/config"
	"github.com/HumanInitiative/agent-harness-go/internal/platform/logger"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
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
	// One set of web counters for everything the harness fetches and
	// searches, logged periodically.
	webMetrics := websearch.NewMetrics()
	if cfg.Web.Enabled {
		web, err := bootstrap.NewWebStack(cfg.Web, bootstrap.WebStackOptions{
			RespectRobots: cfg.Web.RespectRobotsOnFetch, Metrics: webMetrics,
		}, log)
		if err != nil {
			return fmt.Errorf("initialize web tools: %w", err)
		}
		bootstrap.LogMetricsEvery(ctx, webMetrics, cfg.Web.MetricsLogInterval, log)
		log.Info("web tools enabled", "providers", cfg.Web.Providers,
			"respect_robots_on_fetch", cfg.Web.RespectRobotsOnFetch, "pdf", web.PDF)
		registeredTools = append(registeredTools,
			tools.NewWebSearchTool(web.Router, nil),
			tools.NewWebFetchTool(web.Fetcher),
		)
	}
	if cfg.CSR.ToolsEnabled {
		profile, err := bootstrap.LoadInstitutionProfile(cfg.CSR)
		if err != nil {
			return fmt.Errorf("initialize CSR tools: %w", err)
		}
		store, err := bootstrap.OpenCSRStore(ctx, cfg.CSR)
		if err != nil {
			return fmt.Errorf("initialize CSR tools: %w", err)
		}
		defer store.Close()
		index := csr.NewIndex(store, profile, csr.IndexOptions{StaleAfter: cfg.CSR.StaleAfter})
		checkCompany := tools.NewCheckCompanyTool(index, nil)
		if cfg.CSR.OnDemandEnabled {
			onDemand, err := bootstrap.NewOnDemand(cfg.Web, cfg.CSR, store, index, model, webMetrics, log)
			if err != nil {
				return fmt.Errorf("initialize CSR on-demand lookups: %w", err)
			}
			checkCompany.WithLookup(onDemand)
		}
		log.Info("CSR tools enabled", "db", cfg.CSR.DBPath, "on_demand", cfg.CSR.OnDemandEnabled)
		registeredTools = append(registeredTools,
			tools.NewFindCSRProspectsTool(index, nil),
			checkCompany,
			tools.NewListNewCompaniesTool(store, nil),
		)
		if len(cfg.CSR.ReviewerKeyIDs) > 0 {
			registeredTools = append(registeredTools, tools.NewSetCompanyStatusTool(store, cfg.CSR.ReviewerKeyIDs, log))
		}
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
