package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// SearXNG queries a self-hosted SearXNG instance through its JSON API
// (GET {base}/search?q=...&format=json). It is the preferred provider: one
// instance aggregates many engines, and running our own avoids scraping.
//
// The instance is operator-configured and usually lives on a private
// network, so this provider deliberately uses a plain client rather than
// the SSRF-guarded one — the URL never comes from a model or a web page.
type SearXNG struct {
	base      *url.URL
	client    *http.Client
	userAgent string
	language  string
}

// NewSearXNG builds a provider for the instance at baseURL. language (e.g.
// "id") biases results; empty lets the instance decide.
func NewSearXNG(baseURL string, client *http.Client, userAgent, language string) (*SearXNG, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: SearXNG URL %q must be an absolute http(s) URL", ErrProviderMisconfigured, baseURL)
	}
	return &SearXNG{base: u, client: client, userAgent: userAgent, language: language}, nil
}

// Name implements Provider.
func (s *SearXNG) Name() string { return "searxng" }

type searxngResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
	// UnresponsiveEngines lists [engine, reason] pairs for engines that
	// failed this query (rate limited, CAPTCHA, timeout, ...).
	UnresponsiveEngines [][]string `json:"unresponsive_engines"`
}

// Search implements Provider.
func (s *SearXNG) Search(ctx context.Context, query string, limit int) ([]Result, error) {
	endpoint := *s.base
	endpoint.Path += "/search"
	q := url.Values{"q": {query}, "format": {"json"}, "safesearch": {"1"}}
	if s.language != "" {
		q.Set("language", s.language)
	}
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", s.userAgent)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: searxng returned 403 — its JSON API is disabled; add \"json\" to search.formats in the instance's settings.yml",
			ErrProviderMisconfigured)
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: searxng rate limited the request (its limiter may need to allow this client)", ErrBlocked)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, &StatusError{URL: endpoint.Redacted(), StatusCode: resp.StatusCode}
	}

	var body searxngResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&body); err != nil {
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			return nil, fmt.Errorf("%w: searxng did not return JSON (is format=json enabled?): %v", ErrProviderMisconfigured, err)
		}
		return nil, fmt.Errorf("searxng: decode response: %w", err)
	}

	// No results while engines failed is not "nothing found": the upstream
	// engines are rate limiting or blocking the instance. Reporting it as an
	// error lets the router's circuit breaker and the logs show it, instead
	// of every later search quietly returning nothing.
	if len(body.Results) == 0 && len(body.UnresponsiveEngines) > 0 {
		var failed []string
		for _, e := range body.UnresponsiveEngines {
			failed = append(failed, strings.Join(e, ": "))
		}
		return nil, fmt.Errorf("%w: searxng returned no results and its engines failed (%s)", ErrBlocked, strings.Join(failed, "; "))
	}

	results := make([]Result, 0, len(body.Results))
	for _, r := range body.Results {
		results = append(results, Result{Title: r.Title, URL: r.URL, Snippet: r.Content, Source: s.Name()})
	}
	return cleanResults(results, limit), nil
}
