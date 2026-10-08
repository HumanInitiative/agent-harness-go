package websearch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetrics_SummaryChangedAndSince(t *testing.T) {
	m := NewMetrics()
	for _, name := range []string{"fetch.success", "fetch.success", "fetch.failure", "fetch.blocked",
		"fetch.not_modified", "search.searxng.success", "search.duckduckgo.failure", "cache.page.hit"} {
		m.Inc(name)
	}
	want := "fetch ok 2, failed 1 (blocked 1), unchanged 1, robots-disallowed 0 | search ok 1, failed 1"
	if got := m.Summary().String(); got != want {
		t.Fatalf("summary = %q", got)
	}

	before := m.Snapshot()
	changed, now := m.Changed(nil)
	if len(changed) != 7 || now["fetch.success"] != 2 {
		t.Fatalf("everything changed since nothing: %v", changed)
	}
	if changed, _ := m.Changed(now); len(changed) != 0 {
		t.Fatalf("nothing changed since now: %v", changed)
	}
	m.Inc("fetch.success")
	if run := m.Since(before); len(run) != 1 || run["fetch.success"] != 1 || Summarize(run).FetchOK != 1 {
		t.Fatalf("one run's counters: %v", run)
	}
	var none *Metrics
	if none.Summary() != (MetricsSummary{}) {
		t.Fatal("a nil Metrics summarizes to zero")
	}
}

func TestFetch_CountsBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			http.NotFound(w, r)
		case "/waf":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<html><script src="https://challenges.cloudflare.com/x.js"></script></html>`)
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	f, _ := testFetcher(t, srv)
	if _, err := f.Fetch(context.Background(), srv.URL+"/waf"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("expected ErrBlocked, got %v", err)
	}
	_, _ = f.Fetch(context.Background(), srv.URL+"/forbidden")
	_, _ = f.Fetch(context.Background(), srv.URL+"/missing")
	s := f.opts.Metrics.Summary()
	if s.FetchFailed != 3 || s.FetchBlocked != 2 || !strings.Contains(s.String(), "blocked 2") {
		t.Fatalf("a WAF page and a 403 are blocks, a 404 is not: %+v", s)
	}
}
