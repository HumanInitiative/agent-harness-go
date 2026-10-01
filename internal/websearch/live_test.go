//go:build live

// Live checks against the real internet. They are excluded from normal test
// runs and CI because they depend on third-party sites; run them by hand to
// confirm the parsers still match real markup:
//
//	go test -tags live -run Live -v ./internal/websearch/
//
// Optional: LIVE_SEARXNG_URL=http://localhost:8888 also checks SearXNG, and
// LIVE_FETCH_URL overrides the page fetched.
package websearch

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const liveUserAgent = "HumanInitiativeBot/1.0 (+https://github.com/HumanInitiative/agent-harness-go)"

func liveLogger() *slog.Logger { return slog.New(slog.NewTextHandler(os.Stderr, nil)) }

func TestLive_DuckDuckGo(t *testing.T) {
	ddg := NewDuckDuckGo(DuckDuckGoHTMLURL, &http.Client{Timeout: 15 * time.Second}, liveUserAgent, "id-id")
	results, err := ddg.Search(context.Background(), "program CSR Bank Rakyat Indonesia", 5)
	if err != nil {
		t.Fatalf("DuckDuckGo: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results: the result markup may have changed")
	}
	for _, r := range results {
		t.Logf("%s\n    %s\n    %.100s", r.Title, r.URL, r.Snippet)
	}
}

func TestLive_SearXNG(t *testing.T) {
	base := os.Getenv("LIVE_SEARXNG_URL")
	if base == "" {
		t.Skip("set LIVE_SEARXNG_URL to check a SearXNG instance")
	}
	p, err := NewSearXNG(base, &http.Client{Timeout: 15 * time.Second}, liveUserAgent, "id")
	if err != nil {
		t.Fatal(err)
	}
	results, err := p.Search(context.Background(), "program CSR Bank Rakyat Indonesia", 5)
	if err != nil || len(results) == 0 {
		t.Fatalf("SearXNG: %d results, %v", len(results), err)
	}
}

func TestLive_FetchCompanySite(t *testing.T) {
	target := os.Getenv("LIVE_FETCH_URL")
	if target == "" {
		target = "https://bri.co.id/"
	}
	f, err := NewFetcher(FetcherOptions{UserAgent: liveUserAgent, RespectRobots: true}, liveLogger())
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.Fetch(context.Background(), target)
	if err != nil {
		t.Fatalf("fetch %s: %v", target, err)
	}
	t.Logf("final=%s kind=%s bytes=%d truncated=%v title=%q links=%d content=%d chars",
		page.FinalURL, page.Kind, page.Bytes, page.BodyTruncated, page.Title, len(page.Links), len(page.Content))

	keywords := []string{"csr", "tjsl", "keberlanjutan", "sustainab", "tanggung jawab", "esg", "sosial"}
	for _, l := range page.Links {
		hay := strings.ToLower(l.Text + " " + l.URL)
		for _, k := range keywords {
			if strings.Contains(hay, k) {
				t.Logf("CSR-like link: %q -> %s", l.Text, l.URL)
				break
			}
		}
	}
}
