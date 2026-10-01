//go:build live

package websearch

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLive_SeedSurvey fetches the homepage of every seed company that has a
// domain and reports how reachable the sites are and which CSR-like links
// their homepages expose. One homepage plus robots.txt per domain, a few
// domains at a time:
//
//	LIVE_SEED_CSV=../csr-seed-companies.csv go test -tags live -run Live_SeedSurvey -v -timeout 10m ./internal/websearch/
func TestLive_SeedSurvey(t *testing.T) {
	path := os.Getenv("LIVE_SEED_CSV")
	if path == "" {
		t.Skip("set LIVE_SEED_CSV to the seed company CSV")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	fetcher, err := NewFetcher(FetcherOptions{UserAgent: liveUserAgent, RespectRobots: true, Timeout: 20 * time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		domain, class, detail string
		csrLinks              []string
	}
	keywords := []string{"csr", "tjsl", "keberlanjutan", "sustainab", "tanggung-jawab", "tanggung jawab", "esg", "social", "sosial", "foundation", "yayasan", "peduli"}

	jobs := make(chan string)
	var mu sync.Mutex
	var results []outcome
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for domain := range jobs {
				o := outcome{domain: domain}
				page, err := fetcher.Fetch(context.Background(), "https://"+domain+"/")
				var se *StatusError
				switch {
				case errors.Is(err, ErrBlocked):
					o.class, o.detail = "waf_blocked", err.Error()
				case errors.Is(err, ErrNotAllowedByRobots):
					o.class = "robots_disallowed"
				case errors.As(err, &se):
					o.class, o.detail = "http_error", fmt.Sprint(se.StatusCode)
				case err != nil:
					o.class, o.detail = "network_error", err.Error()
				case len(page.Content) < 300 && len(page.Links) < 5:
					o.class, o.detail = "js_rendered", fmt.Sprintf("%d bytes, %d chars text", page.Bytes, len(page.Content))
				default:
					o.class = "ok"
					seen := map[string]bool{}
					for _, l := range page.Links {
						u, _ := url.Parse(l.URL)
						hay := strings.ToLower(l.Text + " " + u.Path)
						for _, k := range keywords {
							if strings.Contains(hay, k) && !seen[u.Path] {
								seen[u.Path] = true
								o.csrLinks = append(o.csrLinks, fmt.Sprintf("%q %s", l.Text, l.URL))
								break
							}
						}
					}
				}
				mu.Lock()
				results = append(results, o)
				mu.Unlock()
			}
		}()
	}
	for _, row := range rows[1:] {
		if d := strings.TrimSpace(row[3]); d != "" {
			jobs <- d
		}
	}
	close(jobs)
	wg.Wait()

	sort.Slice(results, func(i, j int) bool {
		if results[i].class != results[j].class {
			return results[i].class < results[j].class
		}
		return results[i].domain < results[j].domain
	})
	counts := map[string]int{}
	withCSRLink := 0
	for _, o := range results {
		counts[o.class]++
		if len(o.csrLinks) > 0 {
			withCSRLink++
		}
		line := fmt.Sprintf("%-18s %-28s %s", o.class, o.domain, o.detail)
		if len(o.csrLinks) > 0 {
			line += fmt.Sprintf(" csr_links=%d", len(o.csrLinks))
			for i, l := range o.csrLinks {
				if i == 3 {
					break
				}
				line += "\n      " + l
			}
		}
		t.Log(line)
	}
	t.Logf("SUMMARY total=%d %v ok_with_csr_link=%d", len(results), counts, withCSRLink)
}
