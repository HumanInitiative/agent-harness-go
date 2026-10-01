package csr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

// CrawlOptions configures one crawl run. Zero values take the defaults
// noted on each field.
type CrawlOptions struct {
	// Workers is how many companies are processed concurrently. Default 4.
	// Requests to any one domain are still spaced by the fetcher.
	Workers int
	// CompaniesPerRun caps how many due companies one run handles. Default 25.
	CompaniesPerRun int
	// PagesPerCompany caps routes fetched per company per run. Default 4.
	PagesPerCompany int
	// SkipExtraction runs discovery and route checks without calling the
	// LLM (useful to review routes before spending model quota).
	SkipExtraction bool
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
}

// Recheck intervals by page kind: program pages change occasionally, news
// often, and reports about once a year.
var recheckAfter = map[PageKind]time.Duration{
	KindCSRProgram: 7 * 24 * time.Hour,
	KindFoundation: 14 * 24 * time.Hour,
	KindNews:       3 * 24 * time.Hour,
	KindReport:     90 * 24 * time.Hour,
}

const (
	day                = 24 * time.Hour
	blockedRetryAfter  = 14 * day
	goneRetryAfter     = 30 * day
	noRouteRetryAfter  = 7 * day
	invalidRetryAfter  = 90 * day
	maxFailureBackoff  = 30 * day
	firstFailureRetry  = day
	minCompanyInterval = day
)

// extractionOrder decides which routes the model reads first: program and
// foundation pages describe CSR work directly; news and long reports (which
// get truncated) come after.
var extractionOrder = map[PageKind]int{KindCSRProgram: 0, KindFoundation: 1, KindNews: 2, KindReport: 3}

// Crawler runs the scheduled crawl: for each due company it checks the
// recorded routes (rediscovering them only when none are usable), and when
// page content changed it re-extracts the company's profile.
type Crawler struct {
	store     *Store
	resolver  *Resolver
	fetcher   Fetcher
	extractor *ProfileExtractor // nil disables extraction
	opts      CrawlOptions
	log       *slog.Logger
}

// NewCrawler builds a Crawler. fetcher must enforce robots.txt.
func NewCrawler(store *Store, resolver *Resolver, fetcher Fetcher, extractor *ProfileExtractor, opts CrawlOptions, log *slog.Logger) *Crawler {
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.CompaniesPerRun <= 0 {
		opts.CompaniesPerRun = 25
	}
	if opts.PagesPerCompany <= 0 {
		opts.PagesPerCompany = 4
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Crawler{store: store, resolver: resolver, fetcher: fetcher, extractor: extractor, opts: opts, log: log}
}

// CrawlReport summarizes a run.
type CrawlReport struct {
	Companies     int
	Resolved      int
	RoutesFound   int
	PagesFetched  int
	PagesChanged  int
	Extracted     int
	ExtractFailed int
	Errors        []string
}

// RunOnce processes the companies that are due now. It is meant to be run
// by an external scheduler (cron, a Kubernetes CronJob) through csrctl,
// never by a timer inside the harness: with more than one harness instance
// that would crawl everything several times over.
func (c *Crawler) RunOnce(ctx context.Context) (CrawlReport, error) {
	due, err := c.store.CompaniesDue(ctx, c.opts.Now(), c.opts.CompaniesPerRun)
	if err != nil {
		return CrawlReport{}, err
	}
	var mu sync.Mutex
	report := CrawlReport{Companies: len(due)}
	jobs := make(chan Company)
	var wg sync.WaitGroup
	for w := 0; w < c.opts.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for company := range jobs {
				r, err := c.crawlCompany(ctx, company)
				mu.Lock()
				report.add(r)
				if err != nil {
					report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", company.Name, err))
				}
				mu.Unlock()
			}
		}()
	}
	for _, company := range due {
		select {
		case jobs <- company:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return report, err
	}
	return report, nil
}

func (r *CrawlReport) add(o CrawlReport) {
	r.Resolved += o.Resolved
	r.RoutesFound += o.RoutesFound
	r.PagesFetched += o.PagesFetched
	r.PagesChanged += o.PagesChanged
	r.Extracted += o.Extracted
	r.ExtractFailed += o.ExtractFailed
}

// CrawlCompany processes one company now, regardless of its schedule. Used
// by csrctl and by on-demand checks.
func (c *Crawler) CrawlCompany(ctx context.Context, company Company) (CrawlReport, error) {
	return c.crawlCompany(ctx, company)
}

func (c *Crawler) crawlCompany(ctx context.Context, company Company) (CrawlReport, error) {
	var report CrawlReport
	now := c.opts.Now()

	pages, err := c.store.Pages(ctx, company.ID)
	if err != nil {
		return report, err
	}
	resolved := false
	resolve := func() (bool, error) {
		res, err := c.resolver.Resolve(ctx, company)
		if err != nil {
			return false, fmt.Errorf("resolve: %w", err)
		}
		resolved = true
		report.Resolved++
		report.RoutesFound += len(res.Pages)
		if res.DomainStatus == DomainInvalid {
			return false, c.store.MarkCrawled(ctx, company.ID, "", company.Confidence, now.Add(invalidRetryAfter))
		}
		pages, err = c.store.Pages(ctx, company.ID)
		return err == nil, err
	}
	if !hasUsableRoute(pages) {
		if ok, err := resolve(); !ok {
			return report, err
		}
	}
	if !hasUsableRoute(pages) {
		return report, c.store.MarkCrawled(ctx, company.ID, "", company.Confidence, now.Add(noRouteRetryAfter))
	}

	_, profileErr := c.store.Profile(ctx, company.ID)
	needFullExtraction := errors.Is(profileErr, ErrNotFound)

	var changed, fresh []SourcePage
	fetched := 0
	for _, p := range orderForCheck(pages) {
		if fetched >= c.opts.PagesPerCompany {
			break
		}
		due := p.NextCheckAt.IsZero() || !p.NextCheckAt.After(now)
		if !due || !(usable(p) || retryable(p)) {
			continue
		}
		fetched++
		report.PagesFetched++
		src, isChanged, err := c.checkPage(ctx, p, now)
		if err != nil {
			c.log.InfoContext(ctx, "csr route check failed", "company_id", company.ID, "url", p.URL, "error", err)
			continue
		}
		fresh = append(fresh, src)
		if isChanged {
			report.PagesChanged++
			changed = append(changed, src)
		}
	}

	confidence := company.Confidence
	sources := changed
	if needFullExtraction {
		sources = fresh
	}
	if c.extractor != nil && !c.opts.SkipExtraction && len(sources) > 0 {
		sort.SliceStable(sources, func(i, j int) bool { return extractionOrder[sources[i].Kind] < extractionOrder[sources[j].Kind] })
		ex, err := c.extractor.Extract(ctx, company, sources)
		switch {
		case errors.Is(err, ErrExtractionFailed):
			report.ExtractFailed++
			c.log.WarnContext(ctx, "csr extraction failed", "company_id", company.ID, "error", err)
		case err != nil:
			return report, fmt.Errorf("extract: %w", err)
		case ex.HasCSRContent:
			if err := c.store.SaveExtraction(ctx, company.ID, ex.Evidence, ex.Profile); err != nil {
				return report, err
			}
			report.Extracted++
			confidence = ex.Profile.ModelConfidence
			if len(ex.Dropped) > 0 {
				c.log.InfoContext(ctx, "csr extraction dropped unverifiable claims", "company_id", company.ID, "dropped", len(ex.Dropped))
			}
		}
	}

	pages, err = c.store.Pages(ctx, company.ID)
	if err != nil {
		return report, err
	}
	next := nextCompanyCrawl(pages, now)
	if !hasUsableRoute(pages) && !resolved {
		// Every route just failed (e.g. a site redesign): look for new ones
		// now rather than leaving the company routeless for a week, and
		// check whatever is found on the next run.
		if ok, err := resolve(); !ok {
			return report, err
		}
		next = now.Add(minCompanyInterval)
	}
	return report, c.store.MarkCrawled(ctx, company.ID, bestProgramURL(pages), confidence, next)
}

// checkPage fetches one route and records the outcome. It returns the page
// as an extraction source and whether its content changed since last time.
func (c *Crawler) checkPage(ctx context.Context, p Page, now time.Time) (SourcePage, bool, error) {
	page, err := c.fetcher.Fetch(ctx, p.URL)
	if err != nil {
		state, failures, next, status := p.State, p.Failures+1, now.Add(backoff(p.Failures+1)), 0
		var se *websearch.StatusError
		switch {
		case errors.As(err, &se) && (se.StatusCode == 404 || se.StatusCode == 410):
			state, next, status = PageGone, now.Add(goneRetryAfter), se.StatusCode
		case errors.Is(err, websearch.ErrBlocked), errors.Is(err, websearch.ErrNotAllowedByRobots),
			errors.As(err, &se) && (se.StatusCode == 401 || se.StatusCode == 403):
			state, next = PageBlocked, now.Add(blockedRetryAfter)
			if se != nil {
				status = se.StatusCode
			}
		case errors.As(err, &se):
			status = se.StatusCode
		}
		if recErr := c.store.RecordPageCheck(ctx, p.ID, status, state, "", failures, next); recErr != nil {
			return SourcePage{}, false, recErr
		}
		return SourcePage{}, false, err
	}

	sum := sha256.Sum256([]byte(page.Title + "\n" + page.Content))
	hash := hex.EncodeToString(sum[:])
	interval := recheckAfter[p.Kind]
	if interval == 0 {
		interval = 7 * day
	}
	if err := c.store.RecordPageCheck(ctx, p.ID, 200, PageActive, hash, 0, now.Add(interval)); err != nil {
		return SourcePage{}, false, err
	}
	return SourcePage{URL: p.URL, Title: page.Title, Kind: p.Kind, Content: page.Content, FetchedAt: page.FetchedAt},
		hash != p.ContentHash, nil
}

// backoff grows the retry delay with consecutive failures: 1, 2, 4, ...
// days, capped at 30.
func backoff(failures int) time.Duration {
	d := firstFailureRetry
	for i := 1; i < failures && d < maxFailureBackoff; i++ {
		d *= 2
	}
	return min(d, maxFailureBackoff)
}

func usable(p Page) bool { return p.State == PageActive || p.State == PagePinned }

// retryable routes failed before but deserve another look once their retry
// time passes: WAF blocks lift, and a 404 can be a temporary site problem.
// A successful check makes them active again.
func retryable(p Page) bool { return p.State == PageGone || p.State == PageBlocked }

func hasUsableRoute(pages []Page) bool {
	for _, p := range pages {
		if usable(p) {
			return true
		}
	}
	return false
}

// orderForCheck puts pinned routes first, then by extraction priority and
// score.
func orderForCheck(pages []Page) []Page {
	out := append([]Page(nil), pages...)
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].State == PagePinned) != (out[j].State == PagePinned) {
			return out[i].State == PagePinned
		}
		if extractionOrder[out[i].Kind] != extractionOrder[out[j].Kind] {
			return extractionOrder[out[i].Kind] < extractionOrder[out[j].Kind]
		}
		return out[i].Score > out[j].Score
	})
	return out
}

func bestProgramURL(pages []Page) string {
	for _, p := range orderForCheck(pages) {
		if usable(p) && (p.Kind == KindCSRProgram || p.Kind == KindFoundation) {
			return p.URL
		}
	}
	return ""
}

// nextCompanyCrawl is when the company's earliest route is due again, but
// never sooner than a day from now.
func nextCompanyCrawl(pages []Page, now time.Time) time.Time {
	next := now.Add(noRouteRetryAfter)
	for _, p := range pages {
		if usable(p) && !p.NextCheckAt.IsZero() && p.NextCheckAt.Before(next) {
			next = p.NextCheckAt
		}
	}
	return maxTime(next, now.Add(minCompanyInterval))
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
