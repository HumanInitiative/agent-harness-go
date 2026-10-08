package csr

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type crawlFixture struct {
	store     *Store
	web       *fakeWeb
	extractor *scriptedExtractor
	crawler   *Crawler
	clock     *clock
}

func newCrawlFixture(t *testing.T, opts CrawlOptions, replies ...string) crawlFixture {
	t.Helper()
	clk := &clock{t: t0}
	s, err := OpenStore(context.Background(), ":memory:", clk.now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	web := newFakeWeb()
	fx := &scriptedExtractor{replies: replies}
	opts.Now = clk.now
	resolver := NewResolver(s, web, web, ResolveOptions{}, quietLogger())
	var pe *ProfileExtractor
	if len(replies) > 0 {
		pe = NewProfileExtractor(fx, quietLogger())
	}
	return crawlFixture{store: s, web: web, extractor: fx, crawler: NewCrawler(s, resolver, web, pe, opts, quietLogger()), clock: clk}
}

// csrPage builds a fake CSR program page whose text supports goodExtraction.
func csrPage() websearch.Page {
	return websearch.Page{Title: extractSources[0].Title, Content: extractSources[0].Content, FetchedAt: t0}
}

// TestAcceptance_SeedToProspectsWithEvidence is the Phase 2 acceptance test:
// import a sample seed, crawl it, and find_csr_prospects returns companies
// with evidence URLs.
func TestAcceptance_SeedToProspectsWithEvidence(t *testing.T) {
	f := newCrawlFixture(t, CrawlOptions{}, goodExtraction)
	ctx := context.Background()

	rows, _, err := ReadSeedCSV(strings.NewReader("name,sector,region,domain\n" +
		"PT Contoh Energi Tbk,energi,Nasional,contohenergi.co.id\n" +
		"PT Contoh Perbankan Tbk,perbankan,Nasional,contohbank.co.id\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSeed(ctx, f.store, rows); err != nil {
		t.Fatal(err)
	}

	f.web.pages["https://contohenergi.co.id/"] = homepage(link("TJSL", "https://contohenergi.co.id/tjsl"))
	f.web.pages["https://contohenergi.co.id/tjsl"] = csrPage()
	f.web.errs["https://contohbank.co.id/"] = fmt.Errorf("%w: Imperva", websearch.ErrBlocked) // nothing to read

	report, err := f.crawler.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if report.Companies != 2 || report.Extracted != 1 || len(report.Errors) != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}

	ip := testInstitution(t)
	index := NewIndex(f.store, ip, IndexOptions{Now: f.clock.now})
	prospects, err := index.FindProspects(ctx, ProspectFilter{Focus: "pendidikan", Region: "Jawa Barat"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prospects) != 1 || prospects[0].Company.Name != "PT Contoh Energi Tbk" {
		t.Fatalf("expected the energy company only: %+v", prospects)
	}
	p := prospects[0]
	if p.Match.Score == 0 || len(p.Match.Reasons) == 0 {
		t.Fatalf("expected an explained score: %+v", p.Match)
	}
	for _, claim := range p.Profile.FocusAreas {
		for _, id := range claim.EvidenceIDs {
			e, ok := p.Evidence[id]
			if !ok || e.URL != "https://contohenergi.co.id/tjsl" || e.Excerpt == "" {
				t.Fatalf("claim %q must resolve to an evidence URL and excerpt: %+v", claim.Value, e)
			}
		}
	}
	if p.Company.CSRURL != "https://contohenergi.co.id/tjsl" {
		t.Fatalf("company CSR URL not recorded: %q", p.Company.CSRURL)
	}

	// Nothing is ever verified automatically.
	all, _ := f.store.ListCompanies(ctx, "", 10)
	for _, c := range all {
		if c.Status != StatusNew {
			t.Fatalf("crawling must never change review status: %+v", c)
		}
	}
}

func TestCrawl_UnchangedContentIsNotReExtracted(t *testing.T) {
	f := newCrawlFixture(t, CrawlOptions{}, goodExtraction)
	ctx := context.Background()
	mustInsert(t, f.store, Company{Name: "PT Contoh Energi Tbk", Domain: "contohenergi.co.id"})
	f.web.pages["https://contohenergi.co.id/"] = homepage(link("TJSL", "https://contohenergi.co.id/tjsl"))
	f.web.pages["https://contohenergi.co.id/tjsl"] = csrPage()

	if _, err := f.crawler.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// Nothing is due again until the schedule passes.
	if r, _ := f.crawler.RunOnce(ctx); r.Companies != 0 {
		t.Fatalf("company crawled again before it was due: %+v", r)
	}

	f.clock.advance(8 * day) // past the 7-day recheck of program pages
	r, err := f.crawler.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.PagesFetched != 1 || r.PagesChanged != 0 || r.Resolved != 0 {
		t.Fatalf("expected a route check without rediscovery or change: %+v", r)
	}
	if f.extractor.calls != 1 {
		t.Fatalf("unchanged content must not reach the LLM again: %d calls", f.extractor.calls)
	}

	// The page changes: extraction runs again.
	changed := csrPage()
	changed.Content += "\n\nTahun ini program diperluas ke Jawa Tengah."
	f.web.pages["https://contohenergi.co.id/tjsl"] = changed
	f.clock.advance(8 * day)
	if r, _ := f.crawler.RunOnce(ctx); r.PagesChanged != 1 || f.extractor.calls != 2 {
		t.Fatalf("changed content should be re-extracted: %+v calls=%d", r, f.extractor.calls)
	}
}

func TestCrawl_GoneRouteTriggersRediscovery(t *testing.T) {
	f := newCrawlFixture(t, CrawlOptions{SkipExtraction: true}, goodExtraction)
	ctx := context.Background()
	id := mustInsert(t, f.store, Company{Name: "PT Contoh Energi Tbk", Domain: "contohenergi.co.id"})
	f.web.pages["https://contohenergi.co.id/"] = homepage(link("TJSL", "https://contohenergi.co.id/tjsl"))
	f.web.pages["https://contohenergi.co.id/tjsl"] = csrPage()
	if _, err := f.crawler.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// The site is redesigned: the old route is gone, the homepage links a new one.
	delete(f.web.pages, "https://contohenergi.co.id/tjsl")
	f.web.pages["https://contohenergi.co.id/"] = homepage(link("Program CSR", "https://contohenergi.co.id/program-csr"))
	f.web.pages["https://contohenergi.co.id/program-csr"] = csrPage()

	f.clock.advance(8 * day)
	r, err := f.crawler.RunOnce(ctx) // finds the 404 and rediscovers in the same run
	if err != nil {
		t.Fatal(err)
	}
	if r.Resolved != 1 || r.RoutesFound != 1 {
		t.Fatalf("expected immediate rediscovery once no route is usable: %+v", r)
	}
	c, _ := f.store.Company(ctx, id)
	if !c.NextCrawlAt.Equal(f.clock.now().Add(day)) {
		t.Fatalf("new routes should be checked on the next run, a day later: %v", c.NextCrawlAt)
	}
	states := map[string]string{}
	pages, _ := f.store.Pages(ctx, id)
	for _, p := range pages {
		states[p.URL] = p.State
	}
	if states["https://contohenergi.co.id/tjsl"] != PageGone || states["https://contohenergi.co.id/program-csr"] != PageActive {
		t.Fatalf("unexpected route states: %v", states)
	}
	if f.extractor.calls != 0 {
		t.Fatal("SkipExtraction must not call the LLM")
	}
}

func TestCrawl_BlockedRouteAndFailureBackoff(t *testing.T) {
	f := newCrawlFixture(t, CrawlOptions{SkipExtraction: true}, goodExtraction)
	ctx := context.Background()
	id := mustInsert(t, f.store, Company{Name: "PT Contoh Energi Tbk", Domain: "contohenergi.co.id"})
	blocked, _ := f.store.UpsertPage(ctx, Page{CompanyID: id, URL: "https://contohenergi.co.id/tjsl", Kind: KindCSRProgram, DiscoveredVia: ViaManual, Score: 5})
	flaky, _ := f.store.UpsertPage(ctx, Page{CompanyID: id, URL: "https://contohenergi.co.id/csr", Kind: KindCSRProgram, DiscoveredVia: ViaManual, Score: 4})
	f.web.errs["https://contohenergi.co.id/tjsl"] = fmt.Errorf("%w: Cloudflare", websearch.ErrBlocked)
	f.web.errs["https://contohenergi.co.id/csr"] = &websearch.StatusError{StatusCode: 503}

	if _, err := f.crawler.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	pages, _ := f.store.Pages(ctx, id)
	byID := map[int64]Page{}
	for _, p := range pages {
		byID[p.ID] = p
	}
	if byID[blocked].State != PageBlocked || !byID[blocked].NextCheckAt.Equal(t0.Add(blockedRetryAfter)) {
		t.Fatalf("blocked route: %+v", byID[blocked])
	}
	if byID[flaky].State != PageActive || byID[flaky].Failures != 1 || byID[flaky].HTTPStatus != 503 ||
		!byID[flaky].NextCheckAt.Equal(t0.Add(day)) {
		t.Fatalf("temporary failure should back off, not deactivate: %+v", byID[flaky])
	}
	if backoff(1) != day || backoff(3) != 4*day || backoff(20) != maxFailureBackoff {
		t.Fatalf("backoff schedule wrong: %v %v %v", backoff(1), backoff(3), backoff(20))
	}
}

func TestCrawl_BlockedRouteIsRetriedAfterItsRetryTime(t *testing.T) {
	f := newCrawlFixture(t, CrawlOptions{SkipExtraction: true}, goodExtraction)
	ctx := context.Background()
	id := mustInsert(t, f.store, Company{Name: "PT Contoh Energi Tbk", Domain: "contohenergi.co.id"})
	page, _ := f.store.UpsertPage(ctx, Page{CompanyID: id, URL: "https://contohenergi.co.id/tjsl", Kind: KindCSRProgram, DiscoveredVia: ViaManual, Score: 5})
	other, _ := f.store.UpsertPage(ctx, Page{CompanyID: id, URL: "https://contohenergi.co.id/csr", Kind: KindCSRProgram, DiscoveredVia: ViaManual, Score: 4})
	f.web.errs["https://contohenergi.co.id/tjsl"] = fmt.Errorf("%w: WAF", websearch.ErrBlocked)
	f.web.pages["https://contohenergi.co.id/csr"] = csrPage()
	if _, err := f.crawler.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// The WAF block lifts; once the retry time passes the route is checked
	// again (even though the company still has another usable route).
	delete(f.web.errs, "https://contohenergi.co.id/tjsl")
	f.web.pages["https://contohenergi.co.id/tjsl"] = csrPage()
	f.clock.advance(blockedRetryAfter + day)
	if _, err := f.crawler.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	pages, _ := f.store.Pages(ctx, id)
	states := map[int64]string{}
	for _, p := range pages {
		states[p.ID] = p.State
	}
	if states[page] != PageActive || states[other] != PageActive {
		t.Fatalf("a lifted block should make the route active again: %v", states)
	}
}

func TestCrawl_InvalidDomainIsScheduledFarOut(t *testing.T) {
	f := newCrawlFixture(t, CrawlOptions{}, goodExtraction)
	ctx := context.Background()
	id := mustInsert(t, f.store, Company{Name: "PT Star Contoh", Domain: "starcontoh.com"})
	f.web.errs["https://starcontoh.com/"] = dnsNotFound("starcontoh.com")
	f.web.errs["https://www.starcontoh.com/"] = dnsNotFound("www.starcontoh.com")

	if _, err := f.crawler.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := f.store.Company(ctx, id)
	if c.DomainStatus != DomainInvalid || !c.NextCrawlAt.Equal(t0.Add(invalidRetryAfter)) {
		t.Fatalf("got %+v", c)
	}
}

func TestCrawl_FailedExtractionStoresNothing(t *testing.T) {
	f := newCrawlFixture(t, CrawlOptions{}, "not json")
	ctx := context.Background()
	id := mustInsert(t, f.store, Company{Name: "PT Contoh Energi Tbk", Domain: "contohenergi.co.id"})
	f.web.pages["https://contohenergi.co.id/"] = homepage(link("TJSL", "https://contohenergi.co.id/tjsl"))
	f.web.pages["https://contohenergi.co.id/tjsl"] = csrPage()

	r, err := f.crawler.RunOnce(ctx)
	if err != nil || r.ExtractFailed != 1 || r.Extracted != 0 {
		t.Fatalf("got %+v, %v", r, err)
	}
	if _, err := f.store.Profile(ctx, id); err == nil {
		t.Fatal("a failed extraction must not leave a profile")
	}
}

func TestCrawl_RunSizeAndTimeLimitsReportLeftovers(t *testing.T) {
	f := newCrawlFixture(t, CrawlOptions{CompaniesPerRun: 2, Workers: 1})
	ctx := context.Background()
	for _, name := range []string{"PT Satu", "PT Dua", "PT Tiga"} {
		mustInsert(t, f.store, Company{Name: name}) // no domain: nothing to fetch
	}
	r, err := f.crawler.RunOnce(ctx)
	if err != nil || r.Companies != 2 || r.StillDue != 1 {
		t.Fatalf("a run of 2 out of 3 due companies must report 1 left over: %+v, %v", r, err)
	}

	// Each look at the clock costs an hour, so processing one company uses
	// up the two-hour limit and the run starts no other.
	g := newCrawlFixture(t, CrawlOptions{MaxRunDuration: 2 * time.Hour, Workers: 1})
	for _, name := range []string{"PT Satu", "PT Dua", "PT Tiga"} {
		mustInsert(t, g.store, Company{Name: name})
	}
	g.crawler.opts.Now = func() time.Time { g.clock.advance(time.Hour); return g.clock.now() }
	r, err = g.crawler.RunOnce(ctx)
	if err != nil || r.Companies != 1 || r.StillDue != 2 {
		t.Fatalf("the time limit must stop new companies: %+v, %v", r, err)
	}
}

func TestCheckCompany_FindsByNameVariants(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mustInsert(t, s, Company{Name: "PT Bank Rakyat Indonesia (Persero) Tbk"})
	index := NewIndex(s, testInstitution(t), IndexOptions{})

	for _, q := range []string{"bank rakyat indonesia", "PT BANK RAKYAT INDONESIA TBK", "rakyat"} {
		got, err := index.CheckCompany(ctx, q, false)
		if err != nil || len(got) != 1 || got[0].Profile != nil || !strings.Contains(got[0].Match.Reasons[0], "belum ada profil") {
			t.Errorf("CheckCompany(%q) = %+v, %v", q, got, err)
		}
	}
	if got, _ := index.CheckCompany(ctx, "Perusahaan Tidak Ada", false); len(got) != 0 {
		t.Fatalf("unknown company should return nothing: %+v", got)
	}
}
