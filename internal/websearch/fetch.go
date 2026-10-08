package websearch

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html/charset"
)

// FetcherOptions configures a Fetcher. UserAgent is required; zero values
// elsewhere take the defaults noted on each field.
type FetcherOptions struct {
	// UserAgent identifies us to websites and should include a contact URL,
	// e.g. "HumanInitiativeBot/1.0 (+https://example.org/bot)".
	UserAgent string
	// Timeout bounds one whole request, body included. Default 15s.
	Timeout time.Duration
	// MaxBodyBytes caps how much of an HTML, text or XML response is read.
	// Default 5 MiB.
	MaxBodyBytes int64
	// MaxPDFBytes caps a PDF download. PDFs are streamed to a temporary file
	// rather than held in memory, so this can be far larger than
	// MaxBodyBytes: annual and sustainability reports are often 50-150 MB.
	// Default MaxBodyBytes.
	MaxPDFBytes int64
	// PDFTimeout replaces Timeout once a response turns out to be a PDF, to
	// allow for downloading a large report. Default Timeout.
	PDFTimeout time.Duration
	// MaxRedirects caps redirect hops; each hop is re-validated. Default 5.
	MaxRedirects int
	// RespectRobots enforces robots.txt. When false (agent-initiated
	// fetches), robots.txt is still checked and a violation is logged, but
	// the fetch proceeds. Automated crawling must always set it to true.
	RespectRobots bool
	// Guard decides which destinations are reachable.
	Guard Guard
	// DomainRatePerSecond spaces out requests per domain. Default 1.
	DomainRatePerSecond float64
	// CacheTTL and CacheSize configure the page cache. Defaults 30m, 500.
	CacheTTL  time.Duration
	CacheSize int
	// PDF extracts text from PDFs; nil means PDFs return ErrPDFUnavailable.
	PDF PDFExtractor
	// Now returns the current time; nil means time.Now.
	Now     func() time.Time
	Metrics *Metrics
}

// Fetcher downloads web pages safely and reduces them to text. It is safe
// for concurrent use.
type Fetcher struct {
	opts    FetcherOptions
	client  *http.Client
	robots  *RobotsChecker
	limiter *DomainLimiter
	cache   *Cache[Page]
	log     *slog.Logger
}

// NewFetcher builds a Fetcher whose HTTP client can only ever connect to
// public addresses (see Guard).
func NewFetcher(opts FetcherOptions, log *slog.Logger) (*Fetcher, error) {
	if strings.TrimSpace(opts.UserAgent) == "" {
		return nil, errors.New("websearch: FetcherOptions.UserAgent is required")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Second
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 5 << 20
	}
	if opts.MaxPDFBytes <= 0 {
		opts.MaxPDFBytes = opts.MaxBodyBytes
	}
	if opts.PDFTimeout <= 0 {
		opts.PDFTimeout = opts.Timeout
	}
	if opts.MaxRedirects <= 0 {
		opts.MaxRedirects = 5
	}
	if opts.DomainRatePerSecond <= 0 {
		opts.DomainRatePerSecond = 1
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = 30 * time.Minute
	}
	if opts.CacheSize <= 0 {
		opts.CacheSize = 500
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	f := &Fetcher{
		opts:    opts,
		limiter: NewDomainLimiter(opts.DomainRatePerSecond, opts.Now),
		cache:   NewCache[Page]("page", opts.CacheTTL, opts.CacheSize, opts.Now, opts.Metrics),
		log:     log,
	}
	// No client-wide Timeout: it would also cap PDF downloads. Each request
	// gets a deadline through its context instead (see fetch).
	f.client = &http.Client{
		Transport: &http.Transport{
			// No proxy: a proxy would make the dial-time IP check meaningless.
			Proxy:                 nil,
			DialContext:           opts.Guard.DialContext(opts.Timeout),
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: opts.Timeout,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       60 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= opts.MaxRedirects {
				return fmt.Errorf("websearch: stopped after %d redirects", opts.MaxRedirects)
			}
			return opts.Guard.CheckURL(req.URL)
		},
	}
	f.robots = NewRobotsChecker(productToken(opts.UserAgent), 24*time.Hour, opts.Now, f.fetchRobots, opts.Metrics)
	return f, nil
}

// productToken is the part of a User-Agent robots.txt groups match against:
// "HumanInitiativeBot/1.0 (+url)" -> "HumanInitiativeBot".
func productToken(userAgent string) string {
	token, _, _ := strings.Cut(strings.TrimSpace(userAgent), "/")
	token, _, _ = strings.Cut(token, " ")
	return token
}

// Fetch downloads rawURL and extracts its text. Results are cached by URL.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (Page, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return Page{}, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	u.Fragment = ""
	if err := f.opts.Guard.CheckURL(u); err != nil {
		f.opts.Metrics.Inc("fetch.rejected")
		return Page{}, err
	}
	key := u.String()
	if page, ok := f.cache.Get(key); ok {
		return page, nil
	}

	allowed, err := f.robots.Allowed(ctx, u)
	if err != nil {
		f.opts.Metrics.Inc("fetch.failure")
		f.log.InfoContext(ctx, "web fetch failed", "url", key, "error", err)
		return Page{}, err
	}
	if !allowed {
		f.opts.Metrics.Inc("fetch.robots_disallowed")
		if f.opts.RespectRobots {
			return Page{}, fmt.Errorf("%w: %s", ErrNotAllowedByRobots, key)
		}
		f.log.WarnContext(ctx, "robots.txt disallows this URL; fetching anyway because the fetch is agent-initiated",
			"url", key)
	}

	start := f.opts.Now()
	page, err := f.fetch(ctx, u)
	duration := f.opts.Now().Sub(start)
	if err != nil {
		f.opts.Metrics.Inc("fetch.failure")
		f.log.InfoContext(ctx, "web fetch failed", "url", key, "duration_ms", duration.Milliseconds(), "error", err)
		return Page{}, err
	}
	f.opts.Metrics.Inc("fetch.success")
	// Only the URL, outcome and size are logged, never page content.
	f.log.InfoContext(ctx, "web fetch succeeded", "url", key, "final_url", page.FinalURL,
		"kind", page.Kind, "bytes", page.Bytes, "body_truncated", page.BodyTruncated,
		"duration_ms", duration.Milliseconds())
	f.cache.Set(key, page)
	return page, nil
}

func (f *Fetcher) fetch(ctx context.Context, u *url.URL) (Page, error) {
	if err := f.limiter.Wait(ctx, u.Hostname()); err != nil {
		return Page{}, err
	}
	// The deadline starts at Timeout and is extended to PDFTimeout once the
	// response is known to be a PDF.
	reqCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	deadline := time.AfterFunc(f.opts.Timeout, func() {
		cancel(fmt.Errorf("websearch: fetch %s timed out after %s", u, f.opts.Timeout))
	})
	defer deadline.Stop()

	page, err := f.fetchWithin(reqCtx, u, func() {
		deadline.Reset(f.opts.PDFTimeout)
	})
	if err != nil && ctx.Err() == nil {
		if cause := context.Cause(reqCtx); cause != nil && !errors.Is(cause, context.Canceled) {
			return Page{}, cause // report the timeout, not a bare "context canceled"
		}
	}
	return page, err
}

func (f *Fetcher) fetchWithin(ctx context.Context, u *url.URL, extendForPDF func()) (Page, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{}, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	req.Header.Set("User-Agent", f.opts.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,application/pdf;q=0.8")
	req.Header.Set("Accept-Language", "id,en;q=0.8")

	resp, err := f.clientWithFreshCookies().Do(req)
	if err != nil {
		return Page{}, fmt.Errorf("websearch: fetch %s: %w", u, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return Page{}, &StatusError{URL: u.String(), StatusCode: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), f.opts.Now())}
	}

	contentType := resp.Header.Get("Content-Type")
	if kind, _ := classify(contentType, nil); kind == "pdf" {
		extendForPDF() // before reading anything: a large PDF is slow from the first byte
	}
	// Sniffing needs only the first 512 bytes, so the content type is known
	// before deciding how (and how much) to read.
	body := bufio.NewReaderSize(resp.Body, 512)
	head, err := body.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return Page{}, fmt.Errorf("websearch: read %s: %w", u, err)
	}
	kind, err := classify(contentType, head)
	if err != nil {
		return Page{}, err
	}

	final := resp.Request.URL
	page := Page{URL: u.String(), FinalURL: final.String(), Kind: kind, FetchedAt: f.opts.Now()}
	if kind == "pdf" {
		extendForPDF()
		text, n, err := f.readPDF(ctx, body, resp.ContentLength)
		if err != nil {
			return Page{}, err
		}
		page.Bytes = int(n)
		// Not TrimSpace: it would also strip the page breaks of leading
		// pages without text (image-only covers), shifting every page
		// number that follows.
		page.Content = strings.Trim(text, " \t\r\n")
		return page, nil
	}

	raw, truncated, err := readLimited(body, f.opts.MaxBodyBytes)
	if err != nil {
		return Page{}, fmt.Errorf("websearch: read %s: %w", u, err)
	}
	page.Bytes, page.BodyTruncated = len(raw), truncated

	switch kind {
	case "html":
		if vendor := botWall(raw); vendor != "" {
			return Page{}, fmt.Errorf("%w: %s served a bot challenge (%s) instead of the page", ErrBlocked, u.Hostname(), vendor)
		}
		r, err := charset.NewReader(bytes.NewReader(raw), contentType)
		if err != nil {
			return Page{}, fmt.Errorf("websearch: decode %s: %w", u, err)
		}
		page.Title, page.Content, page.Links, err = extractHTML(r, final)
		if err != nil {
			return Page{}, fmt.Errorf("websearch: parse %s: %w", u, err)
		}
	case "text", "xml":
		r, err := charset.NewReader(bytes.NewReader(raw), contentType)
		if err != nil {
			return Page{}, fmt.Errorf("websearch: decode %s: %w", u, err)
		}
		text, err := io.ReadAll(r)
		if err != nil {
			return Page{}, fmt.Errorf("websearch: decode %s: %w", u, err)
		}
		page.Content = strings.TrimSpace(string(text))
	}
	return page, nil
}

// readPDF streams a PDF body to a temporary file (never into memory) and
// extracts its text. It refuses early when the server announces a size
// over the limit, and a PDF cut off at the limit is an error: half a PDF
// is unreadable, unlike half an HTML page.
func (f *Fetcher) readPDF(ctx context.Context, body io.Reader, contentLength int64) (string, int64, error) {
	if f.opts.PDF == nil {
		return "", 0, ErrPDFUnavailable
	}
	if contentLength > f.opts.MaxPDFBytes {
		return "", 0, fmt.Errorf("%w: PDF is %d bytes, the limit is %d", ErrTooLarge, contentLength, f.opts.MaxPDFBytes)
	}
	tmp, err := os.CreateTemp("", "websearch-*.pdf")
	if err != nil {
		return "", 0, fmt.Errorf("websearch: pdf temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(body, f.opts.MaxPDFBytes+1))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", n, fmt.Errorf("websearch: download pdf: %w", err)
	}
	if n > f.opts.MaxPDFBytes {
		return "", n, fmt.Errorf("%w: PDF larger than %d bytes", ErrTooLarge, f.opts.MaxPDFBytes)
	}
	text, err := f.opts.PDF.ExtractText(ctx, tmp.Name())
	return text, n, err
}

// clientWithFreshCookies returns the guarded client with an empty cookie jar
// for one fetch. Some sites (WAFs, consent and language redirects) set a
// cookie and redirect to the same URL; without a jar that becomes a redirect
// loop. A fresh jar per fetch carries no state between fetches or sites.
func (f *Fetcher) clientWithFreshCookies() *http.Client {
	jar, _ := cookiejar.New(nil) // never fails with nil options
	c := *f.client
	c.Jar = jar
	return &c
}

// Sitemaps returns the sitemap URLs declared (Sitemap: lines) in the
// robots.txt of rawURL's host.
func (f *Fetcher) Sitemaps(ctx context.Context, rawURL string) ([]string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	if err := f.opts.Guard.CheckURL(u); err != nil {
		return nil, err
	}
	return f.robots.Sitemaps(ctx, u)
}

// fetchRobots retrieves a robots.txt with the same guarded client.
func (f *Fetcher) fetchRobots(ctx context.Context, robotsURL string) (int, []byte, error) {
	u, err := url.Parse(robotsURL)
	if err != nil {
		return 0, nil, err
	}
	if err := f.limiter.Wait(ctx, u.Hostname()); err != nil {
		return 0, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, f.opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", f.opts.UserAgent)
	resp, err := f.clientWithFreshCookies().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _, err := readLimited(resp.Body, maxRobotsBytes)
	return resp.StatusCode, body, err
}

// botWallSignatures identify the challenge pages common web application
// firewalls serve to clients without JavaScript. Without this check such a
// page would look like a successful fetch of an empty page.
var botWallSignatures = []struct{ marker, vendor string }{
	{"_Incapsula_Resource", "Imperva"},
	{"Incapsula incident ID", "Imperva"},
	{"challenges.cloudflare.com", "Cloudflare"},
	{"cf_chl_opt", "Cloudflare"},
	{"cf-browser-verification", "Cloudflare"},
	{"sucuri_cloudproxy_js", "Sucuri"},
	{"/_Incapsula_", "Imperva"},
	{"awswaf", "AWS WAF"},
}

// botWall returns the WAF vendor whose challenge page body is, or "". Only
// the start of the body is inspected: challenge pages are small, and a real
// page merely mentioning one of these strings deep in its content must not
// be misclassified.
func botWall(body []byte) string {
	head := body
	if len(head) > 16<<10 {
		head = head[:16<<10]
	}
	for _, sig := range botWallSignatures {
		if bytes.Contains(head, []byte(sig.marker)) {
			return sig.vendor
		}
	}
	return ""
}

// readLimited reads at most max bytes and reports whether there was more.
func readLimited(r io.Reader, max int64) ([]byte, bool, error) {
	body, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(body)) > max {
		return body[:max], true, nil
	}
	return body, false, nil
}

// classify maps a Content-Type (or, when missing or generic, the sniffed
// body) to "html", "text", "xml" (sitemaps) or "pdf".
func classify(contentType string, body []byte) (string, error) {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "" || mediaType == "application/octet-stream" {
		mediaType, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	switch mediaType {
	case "text/html", "application/xhtml+xml":
		return "html", nil
	case "text/plain":
		return "text", nil
	case "application/xml", "text/xml":
		return "xml", nil
	case "application/pdf", "application/x-pdf":
		return "pdf", nil
	}
	return "", fmt.Errorf("%w: %s", ErrUnsupportedContent, mediaType)
}

// parseRetryAfter understands both forms of Retry-After: seconds or a date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}
