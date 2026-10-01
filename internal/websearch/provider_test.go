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

const searxngFixture = `{
  "query": "program csr pendidikan",
  "results": [
    {"title": "Program CSR  Pendidikan", "url": "https://contoh.co.id/csr", "content": "Beasiswa untuk pelajar.", "engine": "google"},
    {"title": "Duplicate", "url": "https://contoh.co.id/csr", "content": "dup"},
    {"title": "Not web", "url": "javascript:alert(1)", "content": "x"},
    {"title": "Laporan", "url": "https://contoh.co.id/laporan.pdf", "content": "Laporan keberlanjutan"}
  ],
  "unresponsive_engines": []
}`

func TestSearXNG_ParsesAndCleansResults(t *testing.T) {
	var gotQuery, gotFormat string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotFormat = r.URL.Query().Get("q"), r.URL.Query().Get("format")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, searxngFixture)
	}))
	defer srv.Close()

	p, err := NewSearXNG(srv.URL+"/", srv.Client(), "test/1", "id")
	if err != nil {
		t.Fatalf("NewSearXNG: %v", err)
	}
	results, err := p.Search(context.Background(), "program csr pendidikan", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotQuery != "program csr pendidikan" || gotFormat != "json" {
		t.Errorf("request query=%q format=%q", gotQuery, gotFormat)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 cleaned results (duplicate and non-http dropped), got %+v", results)
	}
	if results[0].Title != "Program CSR Pendidikan" || results[0].Source != "searxng" || results[0].Snippet != "Beasiswa untuk pelajar." {
		t.Errorf("unexpected first result: %+v", results[0])
	}
}

func TestSearXNG_ErrorsAreActionable(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr error
		mention string
	}{
		{"json api disabled", http.StatusForbidden, "Forbidden", ErrProviderMisconfigured, "search.formats"},
		{"rate limited", http.StatusTooManyRequests, "", ErrBlocked, ""},
		{"html instead of json", http.StatusOK, "<html>not json</html>", ErrProviderMisconfigured, "format=json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			p, _ := NewSearXNG(srv.URL, srv.Client(), "test/1", "")
			_, err := p.Search(context.Background(), "q", 5)
			if !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), tc.mention) {
				t.Fatalf("got %v, want %v mentioning %q", err, tc.wantErr, tc.mention)
			}
		})
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(502) }))
	defer srv.Close()
	p, _ := NewSearXNG(srv.URL, srv.Client(), "test/1", "")
	var se *StatusError
	if _, err := p.Search(context.Background(), "q", 5); !errors.As(err, &se) || se.StatusCode != 502 {
		t.Fatalf("expected StatusError 502, got %v", err)
	}
}

func TestSearXNG_AllEnginesFailingIsBlockedNotEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results": [], "unresponsive_engines": [["brave", "Suspended: too many requests"], ["google cse", "too many requests"]]}`)
	}))
	defer srv.Close()
	p, _ := NewSearXNG(srv.URL, srv.Client(), "test/1", "")
	_, err := p.Search(context.Background(), "q", 5)
	if !errors.Is(err, ErrBlocked) || !strings.Contains(err.Error(), "brave: Suspended: too many requests") {
		t.Fatalf("expected ErrBlocked naming the failed engines, got %v", err)
	}

	// Results despite some failed engines are a success.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results": [{"title":"t","url":"https://a.example","content":"c"}], "unresponsive_engines": [["brave", "too many requests"]]}`)
	}))
	defer ok.Close()
	p, _ = NewSearXNG(ok.URL, ok.Client(), "test/1", "")
	if rs, err := p.Search(context.Background(), "q", 5); err != nil || len(rs) != 1 {
		t.Fatalf("partial engine failure should still return results: %v %v", rs, err)
	}
}

func TestNewSearXNG_RejectsBadURL(t *testing.T) {
	for _, u := range []string{"", "not a url", "ftp://x"} {
		if _, err := NewSearXNG(u, http.DefaultClient, "x", ""); !errors.Is(err, ErrProviderMisconfigured) {
			t.Errorf("%q: expected ErrProviderMisconfigured, got %v", u, err)
		}
	}
}

// ddgFixture mirrors the structure of html.duckduckgo.com/html/ results,
// including an ad and the click-tracking redirect links.
const ddgFixture = `<html><body>
<div class="serp__results">
  <div class="result results_links results_links_deep result--ad">
    <div class="links_main links_deep result__body">
      <h2 class="result__title"><a class="result__a" href="https://duckduckgo.com/y.js?ad_provider=x">Iklan Sponsor</a></h2>
      <a class="result__snippet" href="#">Buy now</a>
    </div>
  </div>
  <div class="result results_links results_links_deep web-result ">
    <div class="links_main links_deep result__body">
      <h2 class="result__title">
        <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fwww.contoh.co.id%2Fkeberlanjutan&amp;rut=abc">Keberlanjutan <b>PT Contoh</b></a>
      </h2>
      <div class="result__extras"><a class="result__url" href="#">www.contoh.co.id/keberlanjutan</a></div>
      <a class="result__snippet" href="#">Program <b>CSR</b> bidang pendidikan dan lingkungan.</a>
    </div>
  </div>
  <div class="result results_links results_links_deep web-result ">
    <div class="links_main links_deep result__body">
      <h2 class="result__title"><a class="result__a" href="https://direct.example.org/page">Direct Link</a></h2>
      <div class="result__snippet">Snippet in a div.</div>
    </div>
  </div>
</div>
</body></html>`

func TestParseDuckDuckGo(t *testing.T) {
	results, err := parseDuckDuckGo(strings.NewReader(ddgFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 organic results (ad skipped), got %+v", results)
	}
	want := Result{
		Title:   "Keberlanjutan PT Contoh",
		URL:     "https://www.contoh.co.id/keberlanjutan",
		Snippet: "Program CSR bidang pendidikan dan lingkungan.",
	}
	if results[0] != want {
		t.Errorf("first result:\n got %+v\nwant %+v", results[0], want)
	}
	if results[1].URL != "https://direct.example.org/page" || results[1].Snippet != "Snippet in a div." {
		t.Errorf("second result: %+v", results[1])
	}
}

func TestDuckDuckGo_DetectsBlocking(t *testing.T) {
	challenge := `<html><body><form id="challenge-form" action="/anomaly"></form></body></html>`
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"challenge page with 200", 200, challenge},
		{"202 anomaly", 202, "<html></html>"},
		{"403", 403, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			_, err := NewDuckDuckGo(srv.URL, srv.Client(), "test/1", "id-id").Search(context.Background(), "q", 5)
			if !errors.Is(err, ErrBlocked) {
				t.Fatalf("expected ErrBlocked, got %v", err)
			}
		})
	}
}

func TestDuckDuckGo_SendsQueryAndRegion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Method != http.MethodPost || r.Form.Get("q") != "csr bank" || r.Form.Get("kl") != "id-id" {
			t.Errorf("unexpected request: %s %v", r.Method, r.Form)
		}
		_, _ = io.WriteString(w, ddgFixture)
	}))
	defer srv.Close()
	results, err := NewDuckDuckGo(srv.URL, srv.Client(), "test/1", "id-id").Search(context.Background(), "csr bank", 1)
	if err != nil || len(results) != 1 || results[0].Source != "duckduckgo" {
		t.Fatalf("got %+v, %v", results, err)
	}
}
