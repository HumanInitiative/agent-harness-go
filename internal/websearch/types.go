// Package websearch provides web search and page fetching for AI agents,
// without paid API keys: search through interchangeable providers (SearXNG,
// DuckDuckGo HTML) with failover, and fetching guarded against SSRF, sized
// and rate limited, with page content reduced to light markdown.
//
// The package deliberately imports nothing from the rest of the harness, so
// it can later move into its own module or sit behind an MCP server with
// only a few lines of glue. Everything with side effects (HTTP clients,
// clocks, PDF extraction) is injected, so all of it is testable offline.
//
// Web content is untrusted data. Callers that hand it to a model must wrap
// it with Wrap first.
package websearch

import (
	"errors"
	"fmt"
	"time"
)

// Result is one web search hit.
type Result struct {
	Title   string
	URL     string
	Snippet string
	// Source names the provider that returned the hit.
	Source string
}

// Link is a hyperlink found on a fetched page, resolved to an absolute URL.
type Link struct {
	Text string
	URL  string
}

// Page is a fetched document reduced to readable text.
type Page struct {
	// URL is the address that was requested; FinalURL is where redirects
	// ended up.
	URL      string
	FinalURL string
	Title    string
	// Kind is "html", "text", "xml" or "pdf". For "xml" (e.g. sitemaps),
	// Content is the raw document.
	Kind string
	// Content is the extracted text (light markdown for HTML). It is not
	// truncated to any token budget; use Truncate for that. For PDFs, pages
	// are separated by PageBreak.
	Content string
	// Links lists every hyperlink on the page, navigation and footer
	// included (which Content omits), so callers can look for specific
	// sections such as a CSR page.
	Links []Link
	// Bytes is how many body bytes were read; BodyTruncated reports that
	// the body hit the size limit and only its beginning was used (never
	// for PDFs: an oversized PDF is ErrTooLarge).
	Bytes         int
	BodyTruncated bool
	FetchedAt     time.Time
}

// Sentinel errors callers can branch on with errors.Is.
var (
	// ErrBlocked means a search provider or website refused to serve us
	// (CAPTCHA, WAF bot challenge, or rate limiting).
	ErrBlocked = errors.New("websearch: blocked by the remote service")
	// ErrProviderMisconfigured means a provider cannot work as configured
	// (e.g. SearXNG with its JSON API disabled). Retrying will not help.
	ErrProviderMisconfigured = errors.New("websearch: provider misconfigured")
	// ErrNoProvider means every search provider failed or was skipped.
	ErrNoProvider = errors.New("websearch: no search provider available")
	// ErrSSRF means the URL points somewhere this package refuses to
	// connect to (private network, loopback, metadata service, ...).
	ErrSSRF = errors.New("websearch: destination not allowed")
	// ErrInvalidURL means the URL is malformed or uses a forbidden form
	// (non-http scheme, embedded credentials, disallowed port).
	ErrInvalidURL = errors.New("websearch: invalid URL")
	// ErrTooLarge means a response exceeded the size limit and could not be
	// used partially (e.g. a truncated PDF).
	ErrTooLarge = errors.New("websearch: response too large")
	// ErrUnsupportedContent means the content type is not html, text, xml
	// or pdf.
	ErrUnsupportedContent = errors.New("websearch: unsupported content type")
	// ErrNotAllowedByRobots means robots.txt disallows the URL.
	ErrNotAllowedByRobots = errors.New("websearch: disallowed by robots.txt")
	// ErrPDFUnavailable means a PDF was fetched but no PDF extractor is
	// configured (pdftotext is not installed).
	ErrPDFUnavailable = errors.New("websearch: pdf extraction unavailable")
)

// StatusError reports a non-2xx HTTP response.
type StatusError struct {
	URL        string
	StatusCode int
	// RetryAfter is the server's Retry-After hint, when it sent one.
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("websearch: %s returned HTTP %d", e.URL, e.StatusCode)
}

// Temporary reports whether retrying later may succeed (429 and 5xx).
func (e *StatusError) Temporary() bool {
	return e.StatusCode == 429 || e.StatusCode >= 500
}
