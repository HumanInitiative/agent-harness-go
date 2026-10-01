package csr

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

// fakeWeb serves canned pages, errors, sitemaps and search results, and
// records every fetch so tests can assert what was (not) requested.
type fakeWeb struct {
	mu       sync.Mutex
	pages    map[string]websearch.Page
	errs     map[string]error
	sitemaps map[string][]string
	search   map[string][]websearch.Result
	fetched  []string
	searched []string
}

func newFakeWeb() *fakeWeb {
	return &fakeWeb{pages: map[string]websearch.Page{}, errs: map[string]error{}, sitemaps: map[string][]string{}, search: map[string][]websearch.Result{}}
}

func (f *fakeWeb) Fetch(_ context.Context, rawURL string) (websearch.Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched = append(f.fetched, rawURL)
	if err, ok := f.errs[rawURL]; ok {
		return websearch.Page{}, err
	}
	if p, ok := f.pages[rawURL]; ok {
		if p.FinalURL == "" {
			p.FinalURL = rawURL
		}
		if p.Kind == "" {
			p.Kind = "html"
		}
		return p, nil
	}
	return websearch.Page{}, &websearch.StatusError{URL: rawURL, StatusCode: 404}
}

func (f *fakeWeb) Sitemaps(_ context.Context, rawURL string) ([]string, error) {
	u, _ := url.Parse(rawURL)
	return f.sitemaps[u.Scheme+"://"+u.Host], nil
}

func (f *fakeWeb) Search(_ context.Context, q string, _ int) ([]websearch.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searched = append(f.searched, q)
	for prefix, rs := range f.search {
		if strings.HasPrefix(q, prefix) {
			return rs, nil
		}
	}
	return nil, nil
}

func dnsNotFound(host string) error {
	return &url.Error{Op: "Get", URL: "https://" + host, Err: &net.OpError{Op: "dial", Err: &net.DNSError{Name: host, Err: "no such host", IsNotFound: true}}}
}

func homepage(links ...websearch.Link) websearch.Page {
	return websearch.Page{Title: "Beranda", Content: strings.Repeat("Konten beranda perusahaan. ", 30), Links: links}
}

func link(text, u string) websearch.Link { return websearch.Link{Text: text, URL: u} }

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func routes(t *testing.T, s *Store, companyID int64) map[string]string {
	t.Helper()
	pages, err := s.Pages(context.Background(), companyID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range pages {
		out[p.URL] = string(p.Kind) + "/" + p.DiscoveredVia
	}
	return out
}

func TestResolve_HomepageLinksBecomeRoutes(t *testing.T) {
	s := newTestStore(t)
	id := mustInsert(t, s, Company{Name: "PT Contoh Energi Tbk", Domain: "contohenergi.co.id"})
	web := newFakeWeb()
	web.pages["https://contohenergi.co.id/"] = homepage(
		link("TJSL", "https://contohenergi.co.id/tjsl"),
		link("Laporan Keberlanjutan", "https://contohenergi.co.id/laporan/sr-2025.pdf"),
		link("Yayasan Contoh Energi", "https://yayasan-contohenergi.org/"),
		link("Media Sosial", "https://contohenergi.co.id/media-sosial"),
		link("CSR kami di Instagram", "https://instagram.com/contohenergi"),
		link("Karir", "https://contohenergi.co.id/karir"),
	)
	r := NewResolver(s, web, nil, ResolveOptions{}, quietLogger())
	c, _ := s.Company(context.Background(), id)

	res, err := r.Resolve(context.Background(), c)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.DomainStatus != DomainVerified || res.Access != AccessOK {
		t.Fatalf("expected a verified, reachable domain: %+v", res)
	}
	want := map[string]string{
		"https://contohenergi.co.id/tjsl":                "csr_program/homepage",
		"https://contohenergi.co.id/laporan/sr-2025.pdf": "report/homepage",
		"https://yayasan-contohenergi.org/":              "foundation/homepage", // brand-carrying site, off-domain
	}
	got := routes(t, s, id)
	if fmt.Sprint(sortedKeys(got)) != fmt.Sprint(sortedKeys(want)) {
		t.Fatalf("routes = %v, want %v", got, want)
	}
	for u, w := range want {
		if got[u] != w {
			t.Errorf("%s: %s, want %s", u, got[u], w)
		}
	}
	stored, _ := s.Company(context.Background(), id)
	if stored.DomainStatus != DomainVerified {
		t.Fatalf("domain status not persisted: %+v", stored)
	}
	if a, _ := s.DomainAccess(context.Background(), "contohenergi.co.id"); a.Status != AccessOK {
		t.Fatalf("domain access not recorded: %+v", a)
	}
}

// Mirrors BRI: the site serves a WAF challenge, yet search finds its TJSL
// pages and a CSR page on its separate investor-relations domain.
func TestResolve_BlockedSiteFallsBackToSearch(t *testing.T) {
	s := newTestStore(t)
	id := mustInsert(t, s, Company{Name: "PT Bank Rakyat Contoh (Persero) Tbk", Domain: "brc.co.id"})
	web := newFakeWeb()
	web.errs["https://brc.co.id/"] = fmt.Errorf("%w: brc.co.id served a bot challenge (Imperva)", websearch.ErrBlocked)
	web.search["site:brc.co.id"] = []websearch.Result{
		{Title: "BRC Peduli - TJSL", URL: "https://brc.co.id/web/guest/id/tjsl", Snippet: "Program tanggung jawab sosial"},
	}
	web.search[`"Bank Rakyat Contoh"`] = []websearch.Result{
		{Title: "ESG: Corporate Social Responsibility - ir-brc.com", URL: "https://www.ir-brc.com/esg/corporate_csr.html"},
		{Title: "BRC salurkan CSR untuk sekolah", URL: "https://www.kompas.com/brc-csr-sekolah"},
		{Title: "BRC CSR", URL: "https://www.instagram.com/p/xyz"},
	}
	r := NewResolver(s, web, web, ResolveOptions{}, quietLogger())
	c, _ := s.Company(context.Background(), id)

	res, err := r.Resolve(context.Background(), c)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Access != AccessBlocked || res.DomainStatus != DomainVerified {
		t.Fatalf("blocked site with a brand domain should still be verified: %+v", res)
	}
	got := routes(t, s, id)
	if got["https://brc.co.id/web/guest/id/tjsl"] != "csr_program/search" || got["https://www.ir-brc.com/esg/corporate_csr.html"] != "csr_program/search" {
		t.Fatalf("expected the TJSL page and the IR site page: %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("news portals and social networks must not become routes: %v", got)
	}
	for _, u := range web.fetched {
		if strings.Contains(u, "sitemap") || strings.HasSuffix(u, "/csr") {
			t.Errorf("a blocked site must not be hammered with sitemap or probe requests: fetched %s", u)
		}
	}
}

func TestResolve_TriesWWWWhenApexDoesNotResolve(t *testing.T) {
	s := newTestStore(t)
	id := mustInsert(t, s, Company{Name: "PT Bank Muamalat Contoh", Domain: "muamalatcontoh.co.id"})
	web := newFakeWeb()
	web.errs["https://muamalatcontoh.co.id/"] = dnsNotFound("muamalatcontoh.co.id")
	web.pages["https://www.muamalatcontoh.co.id/"] = homepage(link("CSR", "https://www.muamalatcontoh.co.id/csr"))
	r := NewResolver(s, web, nil, ResolveOptions{}, quietLogger())
	c, _ := s.Company(context.Background(), id)

	res, err := r.Resolve(context.Background(), c)
	if err != nil || res.Access != AccessOK || res.DomainStatus != DomainVerified {
		t.Fatalf("got %+v, %v", res, err)
	}
	if routes(t, s, id)["https://www.muamalatcontoh.co.id/csr"] == "" {
		t.Fatal("route on the www host not recorded")
	}
}

func TestResolve_NonexistentDomainIsMarkedInvalid(t *testing.T) {
	s := newTestStore(t)
	id := mustInsert(t, s, Company{Name: "PT Star Contoh Geothermal", Domain: "starcontohgeothermal.com"})
	web := newFakeWeb()
	web.errs["https://starcontohgeothermal.com/"] = dnsNotFound("starcontohgeothermal.com")
	web.errs["https://www.starcontohgeothermal.com/"] = dnsNotFound("www.starcontohgeothermal.com")
	r := NewResolver(s, web, web, ResolveOptions{}, quietLogger())
	c, _ := s.Company(context.Background(), id)

	res, err := r.Resolve(context.Background(), c)
	if err != nil || res.DomainStatus != DomainInvalid {
		t.Fatalf("got %+v, %v", res, err)
	}
	stored, _ := s.Company(context.Background(), id)
	if stored.DomainStatus != DomainInvalid {
		t.Fatal("invalid domain not persisted")
	}
	if len(web.searched) != 0 {
		t.Fatal("no route search should run for a domain that does not exist")
	}

	// Invalid domains are not retried.
	web.fetched = nil
	if _, err := r.Resolve(context.Background(), stored); err != nil || len(web.fetched) != 0 {
		t.Fatalf("invalid domain fetched again: %v", web.fetched)
	}
}

func TestResolve_FindsDomainForCompanyWithoutOne(t *testing.T) {
	s := newTestStore(t)
	id := mustInsert(t, s, Company{Name: "PT Vale Contoh Tbk"})
	web := newFakeWeb()
	web.search[`"Vale Contoh" situs resmi`] = []websearch.Result{
		{Title: "Vale Contoh - Wikipedia", URL: "https://id.wikipedia.org/wiki/Vale_Contoh"},
		{Title: "Vale Contoh | LinkedIn", URL: "https://www.linkedin.com/company/vale-contoh"},
		{Title: "PT Vale Contoh Tbk - Situs Resmi", URL: "https://www.valecontoh.com/"},
	}
	home := homepage(link("Keberlanjutan", "https://valecontoh.com/keberlanjutan"))
	home.Title = "PT Vale Contoh Tbk"
	web.pages["https://valecontoh.com/"] = home
	r := NewResolver(s, web, web, ResolveOptions{}, quietLogger())
	c, _ := s.Company(context.Background(), id)

	res, err := r.Resolve(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Domain != "valecontoh.com" {
		t.Fatalf("expected the official site, not Wikipedia or LinkedIn: %+v", res)
	}
	// The homepage names the company, which confirms the found domain.
	if res.DomainStatus != DomainVerified {
		t.Fatalf("expected the found domain to be confirmed: %+v", res)
	}
	if routes(t, s, id)["https://valecontoh.com/keberlanjutan"] == "" {
		t.Fatal("route not recorded after finding the domain")
	}
}

// Mirrors live findings: a search-found domain that merely shares a word
// with the company (a city government site, a sister company in the same
// group) must not be verified automatically.
func TestResolve_SearchFoundDomainNeedsTheHomepageToNameTheCompany(t *testing.T) {
	s := newTestStore(t)
	id := mustInsert(t, s, Company{Name: "PT Astra Contoh Motor"})
	web := newFakeWeb()
	web.search[`"Astra Contoh Motor" situs resmi`] = []websearch.Result{
		{Title: "Pemerintah Kota Astra", URL: "https://astra.kotacontoh.go.id/"},
		{Title: "Astra Group", URL: "https://www.astra-group.co.id/"},
	}
	web.pages["https://astra-group.co.id/"] = homepage() // a sister company; never names "Contoh"
	r := NewResolver(s, web, web, ResolveOptions{}, quietLogger())
	c, _ := s.Company(context.Background(), id)

	res, err := r.Resolve(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Domain != "astra-group.co.id" {
		t.Fatalf("government domains must never be chosen: %+v", res)
	}
	if res.DomainStatus != DomainCandidate {
		t.Fatalf("a domain that only shares a brand word must stay a candidate for review: %+v", res)
	}
}

func TestResolve_OffDomainRoutesFromSearchRankBelowOwnPages(t *testing.T) {
	s := newTestStore(t)
	id := mustInsert(t, s, Company{Name: "PT Bank Syariah Contoh Tbk", Domain: "bsc.co.id"})
	web := newFakeWeb()
	web.errs["https://bsc.co.id/"] = fmt.Errorf("%w: Imperva", websearch.ErrBlocked)
	web.search[`"Bank Syariah Contoh"`] = []websearch.Result{
		// A different bank whose domain contains the generic word "syariah".
		{Title: "Tanggung Jawab Sosial Perusahaan", URL: "https://www.lainsyariah.co.id/tanggung-jawab-sosial-perusahaan"},
		{Title: "CSR", URL: "https://www.bsc.co.id/csr"},
	}
	r := NewResolver(s, web, web, ResolveOptions{}, quietLogger())
	c, _ := s.Company(context.Background(), id)
	if _, err := r.Resolve(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	got := routes(t, s, id)
	if _, wrong := got["https://www.lainsyariah.co.id/tanggung-jawab-sosial-perusahaan"]; wrong || got["https://www.bsc.co.id/csr"] == "" {
		t.Fatalf("\"syariah\" must not make another bank's site a route: %v", got)
	}
}

func TestResolve_UnconfirmedDomainStaysUnverified(t *testing.T) {
	s := newTestStore(t)
	// The domain carries no brand token and its homepage never names the company.
	id := mustInsert(t, s, Company{Name: "PT Global Digital Niaga Tbk", Domain: "shop-example.com"})
	web := newFakeWeb()
	web.pages["https://shop-example.com/"] = homepage()
	r := NewResolver(s, web, nil, ResolveOptions{}, quietLogger())
	c, _ := s.Company(context.Background(), id)

	res, err := r.Resolve(context.Background(), c)
	if err != nil || res.DomainStatus != DomainUnverified {
		t.Fatalf("got %+v, %v", res, err)
	}
}

func TestResolve_SitemapIndexAndJSRenderedSite(t *testing.T) {
	s := newTestStore(t)
	id := mustInsert(t, s, Company{Name: "PT Kalbe Contoh Tbk", Domain: "kalbecontoh.co.id"})
	web := newFakeWeb()
	// A JavaScript-only homepage: no text, no links.
	web.pages["https://kalbecontoh.co.id/"] = websearch.Page{Title: "Kalbe Contoh Portal"}
	web.sitemaps["https://kalbecontoh.co.id"] = []string{"https://kalbecontoh.co.id/sitemap-index.xml"}
	web.pages["https://kalbecontoh.co.id/sitemap-index.xml"] = websearch.Page{Kind: "xml", Content: `<sitemapindex>
		<sitemap><loc>https://kalbecontoh.co.id/sitemap-products.xml</loc></sitemap>
		<sitemap><loc>https://kalbecontoh.co.id/sitemap-pages.xml</loc></sitemap></sitemapindex>`}
	web.pages["https://kalbecontoh.co.id/sitemap-pages.xml"] = websearch.Page{Kind: "xml", Content: `<urlset>
		<url><loc>https://kalbecontoh.co.id/id/tentang-kami</loc></url>
		<url><loc>https://kalbecontoh.co.id/id/tanggung-jawab-sosial</loc></url>
		<url><loc>https://kalbecontoh.co.id/id/laporan-keberlanjutan</loc></url></urlset>`}
	r := NewResolver(s, web, nil, ResolveOptions{MaxSitemapDocs: 2}, quietLogger())
	c, _ := s.Company(context.Background(), id)

	res, err := r.Resolve(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Access != AccessJSRendered {
		t.Fatalf("expected js_rendered: %+v", res)
	}
	got := routes(t, s, id)
	if got["https://kalbecontoh.co.id/id/tanggung-jawab-sosial"] != "csr_program/sitemap" ||
		got["https://kalbecontoh.co.id/id/laporan-keberlanjutan"] != "report/sitemap" || len(got) != 2 {
		t.Fatalf("unexpected routes: %v", got)
	}
	for _, u := range web.fetched {
		if strings.Contains(u, "products") {
			t.Fatal("the pages sitemap should be read before the products sitemap (MaxSitemapDocs=2)")
		}
	}
}

func TestResolve_ProbesOnlyAsLastResort(t *testing.T) {
	s := newTestStore(t)
	id := mustInsert(t, s, Company{Name: "PT Pindad Contoh", Domain: "pindadcontoh.com"})
	web := newFakeWeb()
	web.pages["https://pindadcontoh.com/"] = homepage(link("Produk", "https://pindadcontoh.com/produk"))
	web.pages["https://pindadcontoh.com/tjsl"] = websearch.Page{Title: "TJSL - Pindad Contoh", Content: "Program tanggung jawab sosial dan lingkungan."}
	r := NewResolver(s, web, nil, ResolveOptions{}, quietLogger())
	c, _ := s.Company(context.Background(), id)

	if _, err := r.Resolve(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if routes(t, s, id)["https://pindadcontoh.com/tjsl"] != "csr_program/probe" {
		t.Fatalf("probe should have found /tjsl: %v", routes(t, s, id))
	}
}

func TestResolve_LimitsRoutesPerKind(t *testing.T) {
	s := newTestStore(t)
	id := mustInsert(t, s, Company{Name: "PT Berita Contoh", Domain: "beritacontoh.co.id"})
	var links []websearch.Link
	for i := 0; i < 10; i++ {
		links = append(links, link(fmt.Sprintf("Berita CSR %d", i), fmt.Sprintf("https://beritacontoh.co.id/berita/csr-%d", i)))
	}
	links = append(links, link("Program CSR", "https://beritacontoh.co.id/program-csr"))
	web := newFakeWeb()
	web.pages["https://beritacontoh.co.id/"] = homepage(links...)
	r := NewResolver(s, web, nil, ResolveOptions{}, quietLogger())
	c, _ := s.Company(context.Background(), id)

	if _, err := r.Resolve(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	got := routes(t, s, id)
	news := 0
	for _, v := range got {
		if strings.HasPrefix(v, "news/") {
			news++
		}
	}
	if news != perKindLimit[KindNews] || got["https://beritacontoh.co.id/program-csr"] == "" {
		t.Fatalf("expected %d news routes plus the program page: %v", perKindLimit[KindNews], got)
	}
}

func TestDisplayName(t *testing.T) {
	cases := map[string]string{
		"PT Bank Rakyat Indonesia (Persero) Tbk": "Bank Rakyat Indonesia",
		"PT. Kalbe Farma Tbk.":                   "Kalbe Farma",
		"Pertamina":                              "Pertamina",
	}
	for in, want := range cases {
		if got := DisplayName(in); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
