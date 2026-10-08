package csr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

// DiscoveryConfig configures open discovery: which searches to run to find
// companies that fund programs but are not in the seed list yet.
type DiscoveryConfig struct {
	// Queries are templates. Placeholders: {tahun} (this year), {fokus}
	// (each institution focus area), {wilayah} (each institution region)
	// and {sektor} (each of Sectors). A template expands into one query per
	// combination; a template whose placeholder has no values is skipped.
	Queries []string `yaml:"queries"`
	Sectors []string `yaml:"sectors"`
	// ResultsPerQuery is how many search results each query reads. Default 8.
	ResultsPerQuery int `yaml:"results_per_query"`
	// QueriesPerRun bounds one run; successive runs rotate through the
	// expanded queries, least recently run first. Default 10.
	QueriesPerRun int `yaml:"queries_per_run"`
	// PagesPerRun bounds how many new pages one run reads. Default 30.
	PagesPerRun int `yaml:"pages_per_run"`
}

// LoadDiscoveryConfig reads the YAML format:
//
//	discovery:
//	  queries: ["program CSR {fokus} {wilayah} {tahun}"]
//	  sectors: [perbankan, tambang]
func LoadDiscoveryConfig(r io.Reader) (DiscoveryConfig, error) {
	var doc struct {
		Discovery DiscoveryConfig `yaml:"discovery"`
	}
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return DiscoveryConfig{}, fmt.Errorf("csr: read discovery config: %w", err)
	}
	cfg := doc.Discovery
	if len(cfg.Queries) == 0 {
		return DiscoveryConfig{}, errors.New("csr: discovery config needs at least one query")
	}
	for _, q := range cfg.Queries {
		for _, ph := range placeholders(q) {
			if _, ok := knownPlaceholders[ph]; !ok {
				return DiscoveryConfig{}, fmt.Errorf("csr: query %q uses unknown placeholder {%s}", q, ph)
			}
		}
	}
	return cfg.withDefaults(), nil
}

func (c DiscoveryConfig) withDefaults() DiscoveryConfig {
	if c.ResultsPerQuery <= 0 {
		c.ResultsPerQuery = 8
	}
	if c.QueriesPerRun <= 0 {
		c.QueriesPerRun = 10
	}
	if c.PagesPerRun <= 0 {
		c.PagesPerRun = 30
	}
	return c
}

var knownPlaceholders = map[string]struct{}{"tahun": {}, "fokus": {}, "wilayah": {}, "sektor": {}}

func placeholders(template string) []string {
	var out []string
	for rest := template; ; {
		open := strings.Index(rest, "{")
		if open < 0 {
			return out
		}
		end := strings.Index(rest[open:], "}")
		if end < 0 {
			return out
		}
		out = append(out, rest[open+1:open+end])
		rest = rest[open+end+1:]
	}
}

// ExpandQueries fills the templates with this year and the institution's
// focus areas and regions, so discovery looks for what the institution can
// actually propose.
func (c DiscoveryConfig) ExpandQueries(ip InstitutionProfile, now time.Time) []string {
	values := map[string][]string{
		"tahun":   {strconv.Itoa(now.Year())},
		"fokus":   ip.FocusAreas,
		"wilayah": ip.Regions,
		"sektor":  c.Sectors,
	}
	seen := map[string]bool{}
	var out []string
	for _, template := range c.Queries {
		partial := []string{template}
		for _, ph := range placeholders(template) {
			var next []string
			for _, q := range partial {
				for _, v := range values[ph] {
					next = append(next, strings.ReplaceAll(q, "{"+ph+"}", v))
				}
			}
			partial = next
		}
		for _, q := range partial {
			q = strings.Join(strings.Fields(q), " ")
			if q != "" && !seen[q] {
				seen[q] = true
				out = append(out, q)
			}
		}
	}
	return out
}

// Discoverer runs open discovery: search → read result pages → find the
// companies they say fund programs → add new ones to the index as
// source "signal", status "new", due for the regular crawl to resolve their
// sites and build profiles. Nothing it adds is ever marked verified.
type Discoverer struct {
	store     *Store
	searcher  Searcher
	fetcher   Fetcher
	extractor *SignalExtractor
	cfg       DiscoveryConfig
	profile   InstitutionProfile
	now       func() time.Time
	log       *slog.Logger
}

// NewDiscoverer builds a Discoverer. fetcher must enforce robots.txt.
func NewDiscoverer(store *Store, searcher Searcher, fetcher Fetcher, extractor *SignalExtractor, cfg DiscoveryConfig,
	profile InstitutionProfile, now func() time.Time, log *slog.Logger) *Discoverer {
	if now == nil {
		now = time.Now
	}
	return &Discoverer{store: store, searcher: searcher, fetcher: fetcher, extractor: extractor,
		cfg: cfg.withDefaults(), profile: profile, now: now, log: log}
}

// DiscoveryReport summarizes a discovery run.
type DiscoveryReport struct {
	Queries int
	Results int
	// PagesRead counts pages fetched; PagesToModel those that looked like
	// CSR content and went to the model (the rest cost nothing).
	PagesRead    int
	PagesToModel int
	Signals      int
	NewCompanies int
	// Flagged counts new companies whose name resembles one already in
	// the index; they carry a review note.
	Flagged int
	Errors  []string
}

// minSignalPageScore is the page score (see scorePage) below which a
// search result is not worth a model call.
const minSignalPageScore = 12

// RunOnce runs the next batch of queries.
func (d *Discoverer) RunOnce(ctx context.Context) (DiscoveryReport, error) {
	var report DiscoveryReport
	queries, err := d.store.LeastRecentQueries(ctx, d.cfg.ExpandQueries(d.profile, d.now()), d.cfg.QueriesPerRun)
	if err != nil {
		return report, err
	}
	seen := map[string]bool{}
	for _, q := range queries {
		if report.PagesRead >= d.cfg.PagesPerRun {
			break
		}
		results, err := d.searcher.Search(ctx, q, d.cfg.ResultsPerQuery)
		if err != nil {
			if ctx.Err() != nil {
				return report, ctx.Err()
			}
			report.Errors = append(report.Errors, fmt.Sprintf("search %q: %v", q, err))
			if errors.Is(err, websearch.ErrNoProvider) || errors.Is(err, websearch.ErrBlocked) {
				break // the next query would fail the same way
			}
			continue
		}
		report.Queries++
		report.Results += len(results)
		found := 0
		for _, r := range results {
			if report.PagesRead >= d.cfg.PagesPerRun {
				break
			}
			canonical := CanonicalURL(r.URL)
			if canonical == "" || seen[canonical] || skipForSignals(canonical) {
				continue
			}
			seen[canonical] = true
			if read, err := d.store.PageRead(ctx, canonical); err != nil {
				return report, err
			} else if read {
				continue
			}
			n, err := d.readPage(ctx, q, canonical, &report)
			if err != nil {
				if ctx.Err() != nil {
					return report, ctx.Err()
				}
				report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", canonical, err))
				continue
			}
			found += n
		}
		if err := d.store.RecordQueryRun(ctx, q, len(results), found); err != nil {
			return report, err
		}
	}
	d.log.InfoContext(ctx, "csr discovery finished", "queries", report.Queries, "pages_read", report.PagesRead,
		"pages_to_model", report.PagesToModel, "signals", report.Signals, "new_companies", report.NewCompanies)
	return report, nil
}

// readPage reads one result page and records what it says. It returns the
// number of new signals.
func (d *Discoverer) readPage(ctx context.Context, query, rawURL string, report *DiscoveryReport) (int, error) {
	page, err := d.fetcher.Fetch(ctx, rawURL)
	report.PagesRead++
	if err != nil {
		// Unreachable pages are not retried by later runs either: search
		// results churn, and there is always another page.
		return 0, errors.Join(err, d.store.RecordPageRead(ctx, rawURL, 0))
	}
	if page.Kind == "xml" || scorePage(page.Title+"\n"+page.Content) < minSignalPageScore {
		return 0, d.store.RecordPageRead(ctx, rawURL, 0)
	}
	report.PagesToModel++
	companies, err := d.extractor.Extract(ctx, page)
	if errors.Is(err, ErrExtractionFailed) {
		return 0, d.store.RecordPageRead(ctx, rawURL, 0)
	}
	if err != nil {
		return 0, err // model unavailable: leave the page for a later run
	}

	host := ""
	if u, err := url.Parse(page.FinalURL); err == nil {
		host = u.Hostname()
	}
	added := 0
	for _, fc := range companies {
		id, res, err := d.store.UpsertCompany(ctx, Company{Name: fc.Name, Source: SourceSignal})
		if err != nil {
			return added, err
		}
		if res == Inserted {
			report.NewCompanies++
			similar, err := d.store.SimilarCompanies(ctx, fc.Name, 3)
			if err != nil {
				return added, err
			}
			if len(similar) > 0 {
				var names []string
				for _, c := range similar {
					names = append(names, fmt.Sprintf("%s (id %d)", c.Name, c.ID))
				}
				note := "nama mirip dengan " + strings.Join(names, ", ") +
					": mungkin perusahaan yang sama, induk, anak atau sesama grup; periksa sebelum menggabungkan atau memverifikasi"
				if err := d.store.SetReviewNote(ctx, id, note); err != nil {
					return added, err
				}
				report.Flagged++
			}
		}
		isNew, err := d.store.AddSignal(ctx, Signal{CompanyID: id, URL: CanonicalURL(page.FinalURL), Host: host,
			Title: page.Title, Excerpt: fc.Excerpt, Program: fc.Program, Query: query})
		if err != nil {
			return added, err
		}
		if isNew {
			added++
			report.Signals++
		}
	}
	return added, d.store.RecordPageRead(ctx, rawURL, added)
}
