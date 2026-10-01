package websearch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// DuckDuckGoHTMLURL is the endpoint of DuckDuckGo's no-JavaScript results page.
const DuckDuckGoHTMLURL = "https://html.duckduckgo.com/html/"

// DuckDuckGo scrapes DuckDuckGo's HTML results page. It needs no key or
// infrastructure, which makes it a useful fallback, but it is the fragile
// option: the markup can change and DuckDuckGo serves an anti-bot challenge
// to clients it considers automated (reported as ErrBlocked). Prefer SearXNG
// for regular use.
type DuckDuckGo struct {
	endpoint  string
	client    *http.Client
	userAgent string
	region    string
}

// NewDuckDuckGo builds the provider. endpoint is normally DuckDuckGoHTMLURL;
// region (e.g. "id-id") biases results toward a country and language.
func NewDuckDuckGo(endpoint string, client *http.Client, userAgent, region string) *DuckDuckGo {
	return &DuckDuckGo{endpoint: endpoint, client: client, userAgent: userAgent, region: region}
}

// Name implements Provider.
func (d *DuckDuckGo) Name() string { return "duckduckgo" }

// Search implements Provider.
func (d *DuckDuckGo) Search(ctx context.Context, query string, limit int) ([]Result, error) {
	form := url.Values{"q": {query}, "b": {""}}
	if d.region != "" {
		form.Set("kl", d.region)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", d.userAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("duckduckgo: %w", err)
	}
	defer resp.Body.Close()

	body, _, err := readLimited(resp.Body, 2<<20)
	if err != nil {
		return nil, fmt.Errorf("duckduckgo: read response: %w", err)
	}
	// DuckDuckGo answers suspected bots with 202 or 403 and a challenge page.
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: duckduckgo returned HTTP %d (anti-bot challenge)", ErrBlocked, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &StatusError{URL: d.endpoint, StatusCode: resp.StatusCode}
	}

	results, err := parseDuckDuckGo(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for i := range results {
		results[i].Source = d.Name()
	}
	return cleanResults(results, limit), nil
}

// parseDuckDuckGo extracts organic results from the HTML results page,
// skipping ads, and detects the anti-bot challenge page.
func parseDuckDuckGo(r io.Reader) ([]Result, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("duckduckgo: parse results: %w", err)
	}
	if isDuckDuckGoChallenge(doc) {
		return nil, fmt.Errorf("%w: duckduckgo served an anti-bot challenge", ErrBlocked)
	}

	var results []Result
	walk(doc, func(n *html.Node) bool {
		if n.Type != html.ElementNode || n.DataAtom != atom.Div || !hasClass(n, "result") {
			return true
		}
		if hasClass(n, "result--ad") {
			return false
		}
		var res Result
		walk(n, func(c *html.Node) bool {
			if c.Type != html.ElementNode {
				return true
			}
			switch {
			case c.DataAtom == atom.A && hasClass(c, "result__a"):
				res.Title = collapse(textOf(c))
				res.URL = unwrapDuckDuckGoLink(attr(c, "href"))
				return false
			case hasClass(c, "result__snippet"):
				res.Snippet = collapse(textOf(c))
				return false
			}
			return true
		})
		if res.URL != "" {
			results = append(results, res)
		}
		return false
	})
	return results, nil
}

// unwrapDuckDuckGoLink turns DuckDuckGo's click-tracking redirect
// (//duckduckgo.com/l/?uddg=<encoded target>&rut=...) into the target URL.
func unwrapDuckDuckGoLink(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if target := u.Query().Get("uddg"); target != "" {
		return target
	}
	if u.Scheme == "" && strings.HasPrefix(href, "//") {
		u.Scheme = "https"
	}
	return u.String()
}

func isDuckDuckGoChallenge(doc *html.Node) bool {
	challenge := false
	walk(doc, func(n *html.Node) bool {
		if challenge {
			return false
		}
		if n.Type == html.ElementNode &&
			(attr(n, "id") == "challenge-form" || hasClass(n, "anomaly-modal__modal") || hasClass(n, "anomaly-modal")) {
			challenge = true
			return false
		}
		return true
	})
	return challenge
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}
