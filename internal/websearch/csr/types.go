// Package csr discovers companies that may fund social programs (CSR/TJSL)
// and keeps an evidence-backed local index of them: which company, which of
// its pages describe CSR work (the "routing record"), what the pages say
// (extracted into a profile where every claim cites evidence), and how well
// each company fits the institution's own profile.
//
// Like its parent package websearch, it imports nothing from the harness:
// fetching, searching and LLM extraction come in through small interfaces.
package csr

import (
	"context"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

// Where a company record came from.
const (
	SourceSeed     = "seed"
	SourceSignal   = "signal"
	SourceOnDemand = "on_demand"
)

// Review status of a company record. Automated code never sets
// StatusVerified: that is a human decision.
const (
	StatusNew      = "new"
	StatusVerified = "verified"
	StatusExcluded = "excluded"
)

// How much the company's domain has been confirmed.
const (
	// DomainUnverified: supplied (e.g. by the seed file) but not checked yet.
	DomainUnverified = "unverified"
	// DomainVerified: the site answers and is confirmed to be this company's.
	DomainVerified = "verified"
	// DomainCandidate: a plausible domain that still needs human review.
	DomainCandidate = "candidate"
	// DomainInvalid: the domain does not resolve at all.
	DomainInvalid = "invalid"
)

// PageKind says what a CSR-related page is.
type PageKind string

const (
	KindCSRProgram PageKind = "csr_program"
	KindReport     PageKind = "report"
	KindNews       PageKind = "news"
	KindFoundation PageKind = "foundation"
)

// How a page was discovered. Recorded so the route can be trusted (or
// distrusted) accordingly and so discovery itself can be evaluated.
const (
	ViaHomepage = "homepage"
	ViaSitemap  = "sitemap"
	ViaSearch   = "search"
	ViaProbe    = "probe"
	ViaManual   = "manual"
)

// Page state in the routing record.
const (
	// PageActive: discovered automatically and currently reachable.
	PageActive = "active"
	// PagePinned: set by a person; never replaced by automatic discovery.
	PagePinned = "pinned"
	// PageGone: returned 404/410; kept to avoid rediscovering it.
	PageGone = "gone"
	// PageBlocked: the site blocks us (WAF, 403, robots.txt).
	PageBlocked = "blocked"
	// PageRejected: a person marked it as not a CSR page.
	PageRejected = "rejected"
)

// Domain access outcomes, remembered per domain so a blocked site is not
// hammered on every crawl.
const (
	AccessOK               = "ok"
	AccessBlocked          = "blocked"
	AccessJSRendered       = "js_rendered"
	AccessForbidden        = "forbidden"
	AccessUnreachable      = "unreachable"
	AccessRobotsDisallowed = "robots_disallowed"
)

// Company is one prospect.
type Company struct {
	ID             int64
	Name           string
	NameNormalized string
	Domain         string
	DomainStatus   string
	// CSRURL is the best known CSR program page, if any.
	CSRURL        string
	Sector        string
	Region        string
	Source        string
	Status        string
	Confidence    float64
	LastCrawledAt time.Time
	NextCrawlAt   time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Page is one entry of the routing record: a URL known to hold CSR-related
// content for a company, how it was found, and when to look at it again.
type Page struct {
	ID            int64
	CompanyID     int64
	URL           string
	Kind          PageKind
	DiscoveredVia string
	// Score is how strongly the link text and URL indicate CSR content.
	Score         float64
	State         string
	HTTPStatus    int
	ContentHash   string
	LastCheckedAt time.Time
	NextCheckAt   time.Time
	Failures      int
}

// DomainAccess records how reachable a domain was at its last check.
type DomainAccess struct {
	Domain    string
	Status    string
	Detail    string
	CheckedAt time.Time
}

// Evidence is a verbatim excerpt from a page that supports a claim.
type Evidence struct {
	ID          int64
	CompanyID   int64
	URL         string
	Title       string
	Excerpt     string
	Kind        PageKind
	FetchedAt   time.Time
	PublishedAt time.Time
}

// Claim is one extracted fact together with the evidence that supports it.
// A claim without evidence is never stored.
type Claim struct {
	Value       string  `json:"value"`
	EvidenceIDs []int64 `json:"evidence_ids"`
}

// Profile is the structured summary of a company's CSR activity.
type Profile struct {
	CompanyID       int64
	FocusAreas      []Claim
	Regions         []Claim
	ProgramTypes    []Claim
	KnownPartners   []Claim
	ProposalChannel *Claim
	// SeekingPartners is nil when the pages do not say.
	SeekingPartners *Claim
	Notes           string
	ExtractedAt     time.Time
	ModelConfidence float64
}

// Fetcher downloads pages; satisfied by *websearch.Fetcher. For automated
// crawling it must enforce robots.txt.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string) (websearch.Page, error)
	// Sitemaps returns the Sitemap: URLs from the robots.txt of rawURL's host.
	Sitemaps(ctx context.Context, rawURL string) ([]string, error)
}

// Searcher runs a web search; satisfied by *websearch.Router.
type Searcher interface {
	Search(ctx context.Context, query string, limit int) ([]websearch.Result, error)
}

// Extractor turns content into JSON matching a schema. It is implemented by
// the harness's model adapter; this package never knows which LLM is used.
type Extractor interface {
	// ExtractJSON returns JSON intended to match schema. Callers must still
	// validate it: an LLM's output is untrusted input.
	ExtractJSON(ctx context.Context, instruction, content, schema string) ([]byte, error)
}
