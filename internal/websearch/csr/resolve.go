package csr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

// ResolveOptions bounds how much work discovery does per company.
type ResolveOptions struct {
	// MaxPages is the most routes recorded per company. Default 8.
	MaxPages int
	// MaxSitemapDocs is how many sitemap files are read per company. Default 4.
	MaxSitemapDocs int
	// SearchResults is how many search results each query asks for. Default 8.
	SearchResults int
	// ProbePaths are tried only when nothing else found a route.
	ProbePaths []string
}

// perKindLimit keeps a company's routes varied: one site with forty CSR news
// posts should not crowd out its program page and report.
var perKindLimit = map[PageKind]int{KindCSRProgram: 3, KindReport: 2, KindFoundation: 2, KindNews: 2}

var defaultProbePaths = []string{"/csr", "/tjsl", "/tanggung-jawab-sosial", "/keberlanjutan", "/sustainability", "/id/csr", "/id/tjsl"}

// Resolver confirms a company's domain and finds its CSR pages, recording
// them in the routing record (company_pages). Strategies run cheapest and
// most trustworthy first: the homepage's own links, the sitemap, a web
// search, and only then guessing common paths. The survey of the seed
// companies showed CSR paths vary too much for guessing alone (e.g.
// "/pages/view/csr.html", "/id/berita-csr", "/keberlanjutan-2/...").
type Resolver struct {
	store    *Store
	fetcher  Fetcher
	searcher Searcher // may be nil: search-based steps are then skipped
	opts     ResolveOptions
	log      *slog.Logger
}

// NewResolver builds a Resolver. fetcher must enforce robots.txt.
func NewResolver(store *Store, fetcher Fetcher, searcher Searcher, opts ResolveOptions, log *slog.Logger) *Resolver {
	if opts.MaxPages <= 0 {
		opts.MaxPages = 8
	}
	if opts.MaxSitemapDocs <= 0 {
		opts.MaxSitemapDocs = 4
	}
	if opts.SearchResults <= 0 {
		opts.SearchResults = 8
	}
	if opts.ProbePaths == nil {
		opts.ProbePaths = defaultProbePaths
	}
	return &Resolver{store: store, fetcher: fetcher, searcher: searcher, opts: opts, log: log}
}

// ResolveResult reports what discovery found for one company.
type ResolveResult struct {
	Domain       string
	DomainStatus string
	Access       string
	AccessDetail string
	// Found counts recorded routes by how they were discovered.
	Found map[string]int
	Pages []Page
}

type candidate struct {
	url   string
	kind  PageKind
	via   string
	score float64
}

// Resolve confirms c's domain, finds its CSR routes and records both.
func (d *Resolver) Resolve(ctx context.Context, c Company) (ResolveResult, error) {
	res := ResolveResult{Domain: c.Domain, DomainStatus: c.DomainStatus, Found: map[string]int{}}
	brands := BrandTokens(c.Name)

	if res.Domain == "" {
		domain, err := d.findDomain(ctx, c, brands)
		if err != nil {
			return res, err
		}
		if domain == "" {
			return res, nil // nothing to go on yet; a later crawl retries
		}
		res.Domain, res.DomainStatus = domain, DomainCandidate
		if err := d.store.SetDomain(ctx, c.ID, domain, DomainCandidate); err != nil {
			return res, err
		}
	}
	if res.DomainStatus == DomainInvalid {
		return res, nil
	}

	home := d.fetchHome(ctx, res.Domain)
	res.Access, res.AccessDetail = home.access, home.detail
	if err := d.store.SetDomainAccess(ctx, DomainAccess{Domain: res.Domain, Status: home.access, Detail: home.detail}); err != nil {
		return res, err
	}
	if home.dnsNotFound {
		res.DomainStatus = DomainInvalid
		return res, d.store.SetDomain(ctx, c.ID, res.Domain, DomainInvalid)
	}
	confirmed := false
	switch res.DomainStatus {
	case DomainUnverified: // supplied by the seed list
		confirmed = hostHasBrand(res.Domain, brands) || namesCompany(home, c.Name)
	case DomainCandidate: // found by search: its host matching the brand is how it was picked
		confirmed = namesCompany(home, c.Name)
	}
	if confirmed {
		res.DomainStatus = DomainVerified
		if err := d.store.SetDomain(ctx, c.ID, res.Domain, DomainVerified); err != nil {
			return res, err
		}
	}
	if note, err := domainAmbiguity(ctx, d.store, c, res.Domain); err != nil {
		return res, err
	} else if note != "" {
		if err := d.store.AddReviewNote(ctx, c.ID, note); err != nil {
			return res, err
		}
	}

	cands := map[string]candidate{}
	add := func(rawURL, text, via string) {
		canonical := CanonicalURL(rawURL)
		if canonical == "" {
			return
		}
		u, _ := url.Parse(canonical)
		if !acceptHost(u.Hostname(), res.Domain, brands) {
			return
		}
		ls := ScoreLink(text, canonical)
		if via != ViaHomepage && !OnDomain(u.Hostname(), res.Domain) {
			// A brand-carrying site found by search or sitemap can be the
			// company's own (investor relations, foundation) or a sister
			// company's in a group sharing the brand; prefer the company's
			// own pages. A link from the company's own homepage needs no
			// such discount: the company itself points to it.
			ls.Score *= offDomainWeight
		}
		if ls.Score < MinLinkScore {
			return
		}
		if prev, ok := cands[canonical]; !ok || ls.Score > prev.score {
			cands[canonical] = candidate{url: canonical, kind: ls.Kind, via: via, score: ls.Score}
		}
	}

	if home.page != nil {
		for _, l := range home.page.Links {
			add(l.URL, l.Text, ViaHomepage)
		}
	}
	if home.access == AccessOK || home.access == AccessJSRendered {
		d.fromSitemaps(ctx, home.origin, add)
	}
	if d.searcher != nil && countKind(cands, KindCSRProgram) < 2 {
		if err := d.fromSearch(ctx, c, res.Domain, add); err != nil {
			return res, err
		}
	}
	if len(cands) == 0 && home.access == AccessOK {
		d.fromProbes(ctx, home.origin, add)
	}

	for _, cd := range selectCandidates(cands, d.opts.MaxPages) {
		id, err := d.store.UpsertPage(ctx, Page{CompanyID: c.ID, URL: cd.url, Kind: cd.kind, DiscoveredVia: cd.via, Score: cd.score})
		if err != nil {
			return res, err
		}
		res.Found[cd.via]++
		res.Pages = append(res.Pages, Page{ID: id, CompanyID: c.ID, URL: cd.url, Kind: cd.kind, DiscoveredVia: cd.via, Score: cd.score, State: PageActive})
	}
	d.log.InfoContext(ctx, "csr resolve finished", "company_id", c.ID, "domain", res.Domain,
		"domain_status", res.DomainStatus, "access", res.Access, "found", res.Found)
	return res, nil
}

// domainAmbiguity explains why a domain may belong to the company's parent,
// subsidiary or sister company rather than the company itself, or returns
// "". Seen live: Indofood CBP resolved to indofood.com (the group). Such
// domains are kept, as they often are the best source available, but
// flagged for a person.
//
// A foreign parent's global site (Vale Indonesia on vale.com) is not
// detected: on the seed list, a rule for "X Indonesia" on a non-.id domain
// flagged only Indonesian companies that simply use .com (AlamTri,
// London Sumatra).
func domainAmbiguity(ctx context.Context, store *Store, c Company, domain string) (string, error) {
	domain = HostKey(domain)
	if domain == "" {
		return "", nil
	}
	others, err := store.queryCompanies(ctx, `SELECT `+companyColumns+` FROM companies WHERE id != ? ORDER BY id LIMIT 20000`, c.ID)
	if err != nil {
		return "", err
	}
	var shared []string
	for _, o := range others {
		if HostKey(o.Domain) == domain {
			shared = append(shared, fmt.Sprintf("%s (id %d)", o.Name, o.ID))
		}
	}
	if len(shared) > 0 {
		return fmt.Sprintf("domain %s juga dipakai %s: mungkin situs induk atau grup", domain, strings.Join(shared, ", ")), nil
	}

	// The domain is only a brand that other indexed companies also carry:
	// a group brand ("indofood" for Indofood CBP and Indofood Sukses Makmur).
	label := strings.ReplaceAll(strings.Split(domain, ".")[0], "-", "")
	mine := distinctiveWords(c.Name)
	if len(mine) >= 2 && mine[label] {
		var group []string
		for _, o := range others {
			if distinctiveWords(o.Name)[label] {
				group = append(group, fmt.Sprintf("%s (id %d)", o.Name, o.ID))
			}
		}
		if len(group) > 0 {
			return fmt.Sprintf("domain %s hanya memuat merek %q yang juga dipakai %s: mungkin situs grup, bukan "+
				"situs perusahaan ini sendiri", domain, label, strings.Join(group, ", ")), nil
		}
	}

	return "", nil
}

type homeResult struct {
	page        *websearch.Page
	origin      string
	access      string
	detail      string
	dnsNotFound bool
}

// fetchHome fetches the homepage, trying "www." when the bare domain does
// not resolve (one seed company is only reachable that way).
func (d *Resolver) fetchHome(ctx context.Context, domain string) homeResult {
	var last homeResult
	for i, host := range []string{domain, "www." + domain} {
		origin := "https://" + host
		page, err := d.fetcher.Fetch(ctx, origin+"/")
		if err == nil {
			r := homeResult{page: &page, origin: originOf(page.FinalURL, origin), access: AccessOK}
			if len(page.Content) < 300 && len(page.Links) < 5 {
				r.access, r.detail = AccessJSRendered, "page has almost no text without JavaScript"
			}
			return r
		}
		last = homeResult{origin: origin}
		last.access, last.detail = classifyFetchError(err)
		last.dnsNotFound = isDNSNotFound(err)
		if !last.dnsNotFound || i == 1 {
			return last
		}
	}
	return last
}

func originOf(finalURL, fallback string) string {
	if u, err := url.Parse(finalURL); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return fallback
}

// namesCompany reports whether the homepage names the company: every
// distinctive word of its name appears in the title or text. A site that
// merely shares a brand word ("Sarana" for a city government site, "Astra"
// for a sister company) does not pass.
func namesCompany(home homeResult, name string) bool {
	if home.page == nil {
		return false
	}
	text := home.page.Title + " " + home.page.Content
	if len(text) > 8000 {
		text = text[:8000]
	}
	hay := words(text)
	required := 0
	for _, w := range strings.Fields(NormalizeName(name)) {
		if genericTokens[w] || len(w) < 3 {
			continue
		}
		required++
		if !contains(hay, w) {
			return false
		}
	}
	return required > 0
}

func (d *Resolver) fromSitemaps(ctx context.Context, origin string, add func(rawURL, text, via string)) {
	queue, _ := d.fetcher.Sitemaps(ctx, origin+"/")
	queue = append(queue, origin+"/sitemap.xml", origin+"/sitemap_index.xml")
	seen := map[string]bool{}
	for read := 0; len(queue) > 0 && read < d.opts.MaxSitemapDocs; {
		next := queue[0]
		queue = queue[1:]
		if seen[next] {
			continue
		}
		seen[next] = true
		read++
		page, err := d.fetcher.Fetch(ctx, next)
		if err != nil || page.Kind != "xml" {
			continue
		}
		pages, children, ok := parseSitemap(page.Content)
		if !ok {
			continue
		}
		for _, p := range pages {
			add(p, "", ViaSitemap)
		}
		queue = append(rankChildSitemaps(children), queue...)
	}
}

func (d *Resolver) fromSearch(ctx context.Context, c Company, domain string, add func(rawURL, text, via string)) error {
	name := DisplayName(c.Name)
	queries := []string{
		fmt.Sprintf(`site:%s CSR OR TJSL OR "tanggung jawab sosial" OR keberlanjutan`, domain),
		fmt.Sprintf(`"%s" program CSR TJSL`, name),
	}
	for _, q := range queries {
		results, err := d.searcher.Search(ctx, q, d.opts.SearchResults)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A failed search only means fewer routes this time.
			d.log.WarnContext(ctx, "csr route search failed", "company_id", c.ID, "error", err)
			continue
		}
		for _, r := range results {
			add(r.URL, r.Title+" "+r.Snippet, ViaSearch)
		}
	}
	return nil
}

func (d *Resolver) fromProbes(ctx context.Context, origin string, add func(rawURL, text, via string)) {
	hits := 0
	for _, p := range d.opts.ProbePaths {
		if hits >= 2 {
			return
		}
		page, err := d.fetcher.Fetch(ctx, origin+p)
		if err != nil || page.Kind != "html" {
			continue
		}
		text := page.Title + " " + firstChars(page.Content, 500)
		if ScoreLink(text, page.FinalURL).Score >= MinLinkScore {
			add(page.FinalURL, text, ViaProbe)
			hits++
		}
	}
}

// findDomain looks for a company's official site when the seed had none.
// The result is only a candidate: a person, or a later homepage check that
// names the company, confirms it.
func (d *Resolver) findDomain(ctx context.Context, c Company, brands []string) (string, error) {
	if d.searcher == nil {
		return "", nil
	}
	results, err := d.searcher.Search(ctx, fmt.Sprintf(`"%s" situs resmi`, DisplayName(c.Name)), d.opts.SearchResults)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		d.log.WarnContext(ctx, "csr domain search failed", "company_id", c.ID, "error", err)
		return "", nil
	}
	nameWords := strings.Fields(NormalizeName(c.Name))
	best, bestScore := "", 0.0
	for _, r := range results {
		u, err := url.Parse(r.URL)
		if err != nil {
			continue
		}
		host := HostKey(u.Hostname())
		if host == "" || blockedHost(host) {
			continue
		}
		score := 0.0
		if hostHasBrand(host, brands) {
			score += 3
		}
		// Prefer the host matching more of the name: valeindonesia.com
		// over vale.com for "Vale Indonesia".
		label := strings.ReplaceAll(strings.Split(host, ".")[0], "-", "")
		for _, w := range nameWords {
			if len(w) >= 3 && strings.Contains(label, w) {
				score += 0.5
			}
		}
		title := words(r.Title)
		matched := 0
		for _, w := range nameWords {
			if contains(title, w) {
				matched++
			}
		}
		if len(nameWords) > 0 && matched == len(nameWords) {
			score += 2
		}
		if u.Path == "" || u.Path == "/" {
			score++
		}
		if score > bestScore {
			best, bestScore = host, score
		}
	}
	if bestScore < 3 {
		return "", nil
	}
	return best, nil
}

// acceptHost decides whether a page on host can be one of the company's own
// CSR routes: on its domain, or on a site carrying its brand (an investor
// relations or foundation site such as ir-bri.com), but never a social
// network, news portal or directory.
func acceptHost(host, domain string, brands []string) bool {
	if OnDomain(host, domain) {
		return true
	}
	if blockedHost(host) {
		return false
	}
	return hostHasBrand(host, brands)
}

// hostHasBrand reports whether a host's name (TLD aside) carries one of the
// company's brand tokens, as a whole label part or, for longer tokens,
// inside a label ("unitedtractors" contains "tractors").
func hostHasBrand(host string, brands []string) bool {
	labels := strings.Split(HostKey(host), ".")
	if len(labels) > 1 {
		labels = labels[:len(labels)-1]
		if n := len(labels); n > 1 && (labels[n-1] == "co" || labels[n-1] == "or" || labels[n-1] == "go" || labels[n-1] == "ac") {
			labels = labels[:n-1] // second-level suffixes such as .co.id
		}
	}
	for _, label := range labels {
		parts := strings.Split(label, "-")
		for _, b := range brands {
			for _, p := range parts {
				if p == b {
					return true
				}
			}
			if len(b) >= 5 && strings.Contains(label, b) {
				return true
			}
		}
	}
	return false
}

// blockedHosts are never a company's own CSR route: social networks,
// encyclopedias, news portals, directories and document hosts.
var blockedHosts = []string{
	"facebook.com", "instagram.com", "twitter.com", "x.com", "linkedin.com", "youtube.com", "tiktok.com",
	"wikipedia.org", "wikimedia.org", "google.com", "bing.com", "duckduckgo.com",
	"detik.com", "kompas.com", "cnbcindonesia.com", "cnnindonesia.com", "bisnis.com", "kontan.co.id",
	"liputan6.com", "tempo.co", "antaranews.com", "tribunnews.com", "okezone.com", "kumparan.com",
	"idntimes.com", "suara.com", "republika.co.id", "sindonews.com", "merdeka.com", "viva.co.id",
	"jpnn.com", "investor.id", "katadata.co.id", "bloomberg.com", "reuters.com",
	"idx.co.id", "jobstreet.co.id", "glassdoor.com", "indeed.com", "crunchbase.com", "dnb.com",
	"scribd.com", "issuu.com", "slideshare.net", "medium.com",
}

// blockedSuffixes are second-level domains no company uses: government,
// military, academic and school sites (a live crawl matched a city
// government site on a shared word).
var blockedSuffixes = []string{".go.id", ".mil.id", ".ac.id", ".sch.id", ".desa.id", ".gov"}

// offDomainWeight scales the score of routes found outside the company's
// own domain.
const offDomainWeight = 0.6

func blockedHost(host string) bool {
	host = HostKey(host)
	for _, suffix := range blockedSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	for _, b := range blockedHosts {
		if host == b || strings.HasSuffix(host, "."+b) {
			return true
		}
	}
	return false
}

func selectCandidates(cands map[string]candidate, max int) []candidate {
	all := make([]candidate, 0, len(cands))
	for _, c := range cands {
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].url < all[j].url
	})
	perKind := map[PageKind]int{}
	var out []candidate
	for _, c := range all {
		if len(out) >= max {
			break
		}
		if perKind[c.kind] >= perKindLimit[c.kind] {
			continue
		}
		perKind[c.kind]++
		out = append(out, c)
	}
	return out
}

func countKind(cands map[string]candidate, kind PageKind) int {
	n := 0
	for _, c := range cands {
		if c.kind == kind {
			n++
		}
	}
	return n
}

// classifyFetchError maps a fetch failure to a domain access status.
func classifyFetchError(err error) (string, string) {
	var status *websearch.StatusError
	switch {
	case errors.Is(err, websearch.ErrBlocked):
		return AccessBlocked, err.Error()
	case errors.Is(err, websearch.ErrNotAllowedByRobots):
		return AccessRobotsDisallowed, "robots.txt disallows crawling"
	case errors.As(err, &status) && (status.StatusCode == 401 || status.StatusCode == 403):
		return AccessForbidden, fmt.Sprintf("HTTP %d", status.StatusCode)
	default:
		return AccessUnreachable, err.Error()
	}
}

func isDNSNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}

// DisplayName drops legal-form words for use in search queries:
// "PT Bank Rakyat Indonesia (Persero) Tbk" -> "Bank Rakyat Indonesia".
func DisplayName(name string) string {
	var kept []string
	for _, tok := range strings.Fields(name) {
		bare := strings.ToLower(strings.Trim(tok, ".,()"))
		if !legalTokens[bare] {
			kept = append(kept, strings.Trim(tok, ","))
		}
	}
	return strings.TrimRight(strings.Join(kept, " "), ".")
}

func firstChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
