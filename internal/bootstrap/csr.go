package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/HumanInitiative/agent-harness-go/internal/platform/config"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

// OpenCSRStore opens (creating if needed) the CSR index database.
func OpenCSRStore(ctx context.Context, cfg config.CSRConfig) (*csr.Store, error) {
	if dir := filepath.Dir(cfg.DBPath); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("bootstrap: create %s: %w", dir, err)
		}
	}
	return csr.OpenStore(ctx, cfg.DBPath, nil)
}

// LoadInstitutionProfile reads the institution profile the CSR index scores
// companies against.
func LoadInstitutionProfile(cfg config.CSRConfig) (csr.InstitutionProfile, error) {
	f, err := os.Open(cfg.InstitutionProfilePath)
	if err != nil {
		return csr.InstitutionProfile{}, fmt.Errorf("bootstrap: open institution profile (CSR_INSTITUTION_PROFILE): %w", err)
	}
	defer f.Close()
	return csr.LoadInstitutionProfile(f)
}

// LoadDiscoveryConfig reads the open-discovery queries.
func LoadDiscoveryConfig(cfg config.CSRConfig) (csr.DiscoveryConfig, error) {
	f, err := os.Open(cfg.DiscoveryConfigPath)
	if err != nil {
		return csr.DiscoveryConfig{}, fmt.Errorf("bootstrap: open discovery config (CSR_DISCOVERY_CONFIG): %w", err)
	}
	defer f.Close()
	return csr.LoadDiscoveryConfig(f)
}

// NewOnDemand builds on-demand lookups for check_company: a one-company
// crawler with its own robots.txt-respecting web stack, so lookups are as
// polite to company sites as the scheduled crawl.
func NewOnDemand(web config.WebConfig, cfg config.CSRConfig, store *csr.Store, index *csr.Index,
	extractor csr.Extractor, log *slog.Logger) (*csr.OnDemand, error) {
	stack, err := NewWebStack(web, WebStackOptions{RespectRobots: true}, log)
	if err != nil {
		return nil, err
	}
	resolver := csr.NewResolver(store, stack.Fetcher, stack.Router, csr.ResolveOptions{}, log)
	crawler := csr.NewCrawler(store, resolver, stack.Fetcher, csr.NewProfileExtractor(extractor, log),
		csr.CrawlOptions{Workers: 1, PagesPerCompany: 3}, log)
	return csr.NewOnDemand(store, crawler, index, csr.OnDemandOptions{Budget: cfg.OnDemandTimeout, PerHour: cfg.OnDemandPerHour}, log), nil
}
