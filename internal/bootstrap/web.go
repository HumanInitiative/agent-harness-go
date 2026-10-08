// Package bootstrap builds the components that more than one command needs
// (the harness server and csrctl), so their wiring is written once. It is
// part of the composition root: the only code besides cmd/ that knows both
// configuration and concrete adapters.
package bootstrap

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/platform/config"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

// WebStack is a configured fetcher and search router sharing one set of
// metrics.
type WebStack struct {
	Fetcher *websearch.Fetcher
	Router  *websearch.Router
	Metrics *websearch.Metrics
	// PDF reports whether PDF text extraction is available.
	PDF bool
}

// WebStackOptions adapts the web stack to how it is used.
type WebStackOptions struct {
	// RespectRobots must be true for automated crawling; agent-initiated
	// fetches follow WebConfig.RespectRobotsOnFetch.
	RespectRobots bool
	// DomainRatePerSecond overrides WebConfig.DomainRatePerSecond when > 0
	// (crawls go slower than interactive fetches).
	DomainRatePerSecond float64
	// SearchInterval spaces out upstream search queries; zero for none.
	SearchInterval time.Duration
	// MaxPDFBytes and PDFTimeout override WebConfig.MaxPDFBytes and
	// WebConfig.FetchTimeout for PDFs when > 0 (crawls read whole reports).
	MaxPDFBytes int64
	PDFTimeout  time.Duration
}

// NewWebStack builds the fetcher and search router from configuration.
func NewWebStack(cfg config.WebConfig, opts WebStackOptions, log *slog.Logger) (WebStack, error) {
	domainRate := cfg.DomainRatePerSecond
	if opts.DomainRatePerSecond > 0 {
		domainRate = opts.DomainRatePerSecond
	}
	maxPDF := cfg.MaxPDFBytes
	if opts.MaxPDFBytes > 0 {
		maxPDF = opts.MaxPDFBytes
	}
	metrics := websearch.NewMetrics()

	// PDF reading is optional: without pdftotext, PDFs are reported as
	// unreadable instead of the process refusing to start. A 380-page report
	// converts in about a second into ~1 MB of text; the limits leave room
	// for far larger ones.
	var pdf websearch.PDFExtractor
	if p, err := websearch.NewPDFToText(2*time.Minute, 16<<20); err != nil {
		log.Warn("PDF reading disabled", "reason", err)
	} else {
		pdf = p
	}

	fetcher, err := websearch.NewFetcher(websearch.FetcherOptions{
		UserAgent:           cfg.UserAgent,
		Timeout:             cfg.FetchTimeout,
		MaxBodyBytes:        cfg.MaxBodyBytes,
		MaxPDFBytes:         maxPDF,
		PDFTimeout:          opts.PDFTimeout,
		RespectRobots:       opts.RespectRobots,
		Guard:               websearch.Guard{},
		DomainRatePerSecond: domainRate,
		CacheTTL:            cfg.CacheTTL,
		PDF:                 pdf,
		Metrics:             metrics,
	}, log)
	if err != nil {
		return WebStack{}, err
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
				return WebStack{}, err
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
		default:
			return WebStack{}, fmt.Errorf("bootstrap: unknown search provider %q", name)
		}
	}
	router, err := websearch.NewRouter(providers, websearch.RouterOptions{
		ProviderTimeout: cfg.FetchTimeout,
		CacheTTL:        cfg.CacheTTL,
		MinInterval:     opts.SearchInterval,
		Metrics:         metrics,
	}, log)
	if err != nil {
		return WebStack{}, err
	}
	return WebStack{Fetcher: fetcher, Router: router, Metrics: metrics, PDF: pdf != nil}, nil
}
