package websearch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakePDF struct{ text string }

func (f fakePDF) ExtractText(context.Context, []byte) (string, error) { return f.text, nil }

// testFetcher builds a Fetcher allowed to reach the given httptest server.
// Everything else (robots, size limits, redirects) behaves as in production.
func testFetcher(t *testing.T, srv *httptest.Server, mutate ...func(*FetcherOptions)) (*Fetcher, *bytes.Buffer) {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	opts := FetcherOptions{
		UserAgent:           "HumanInitiativeBot/1.0 (+https://example.org/bot)",
		Guard:               Guard{AllowPrivateNetworks: true, AllowedPorts: []int{port}},
		DomainRatePerSecond: 1000,
		RespectRobots:       true,
		Metrics:             NewMetrics(),
	}
	for _, m := range mutate {
		m(&opts)
	}
	var logs bytes.Buffer
	f, err := NewFetcher(opts, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("NewFetcher: %v", err)
	}
	return f, &logs
}

func TestFetch_HTMLPageWithRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/old-csr", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/csr", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/csr", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, csrPageFixture)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f, logs := testFetcher(t, srv)

	page, err := f.Fetch(context.Background(), srv.URL+"/old-csr#section")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if page.Kind != "html" || page.FinalURL != srv.URL+"/csr" || page.URL != srv.URL+"/old-csr" {
		t.Fatalf("unexpected page metadata: %+v", page)
	}
	if !strings.Contains(page.Content, "# Program TJSL 2026") || page.Title == "" || len(page.Links) == 0 {
		t.Fatalf("content not extracted: title=%q links=%d content=%q", page.Title, len(page.Links), page.Content)
	}
	if strings.Contains(logs.String(), "Program TJSL") {
		t.Fatal("page content must never be logged")
	}
}

func TestFetch_CachesByURL(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "hello")
	}))
	defer srv.Close()
	f, _ := testFetcher(t, srv)

	for i := 0; i < 3; i++ {
		if _, err := f.Fetch(context.Background(), srv.URL+"/page"); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("server hit %d times, want 1", hits.Load())
	}
}

// TestFetch_CookieRedirect mirrors WAF-fronted sites (e.g. Imperva) that set
// a cookie and redirect to the same URL, looping forever without a jar.
func TestFetch_CookieRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		if _, err := r.Cookie("session"); err != nil {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "1", Path: "/"})
			http.Redirect(w, r, r.URL.Path, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "content behind a cookie redirect")
	}))
	defer srv.Close()
	f, _ := testFetcher(t, srv)

	page, err := f.Fetch(context.Background(), srv.URL+"/home")
	if err != nil || page.Content != "content behind a cookie redirect" {
		t.Fatalf("got %q, %v", page.Content, err)
	}
	// The next fetch starts with an empty jar: no state carries over.
	if _, err := f.Fetch(context.Background(), srv.URL+"/other"); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
}

func TestFetch_DetectsBotWalls(t *testing.T) {
	pages := map[string]string{
		"/imperva":    `<html><head><script src="/_Incapsula_Resource?SWJIYLWA=x"></script></head><body><iframe src="/_Incapsula_Resource?CWUDNSAI=23"></iframe></body></html>`,
		"/cloudflare": `<html><head><title>Just a moment...</title></head><body><script src="https://challenges.cloudflare.com/turnstile/v0/api.js"></script></body></html>`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	f, _ := testFetcher(t, srv)

	for path, vendor := range map[string]string{"/imperva": "Imperva", "/cloudflare": "Cloudflare"} {
		_, err := f.Fetch(context.Background(), srv.URL+path)
		if !errors.Is(err, ErrBlocked) || !strings.Contains(err.Error(), vendor) {
			t.Errorf("%s: expected ErrBlocked naming %s, got %v", path, vendor, err)
		}
	}
}

func TestBotWall_IgnoresMentionsDeepInRealPages(t *testing.T) {
	page := "<html><body>" + strings.Repeat("<p>Konten asli.</p>", 2000) + "<p>We moved off Incapsula incident ID handling.</p></body></html>"
	if vendor := botWall([]byte(page)); vendor != "" {
		t.Fatalf("real page misclassified as a %s challenge", vendor)
	}
}

func TestFetch_RedirectLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/r"))
		http.Redirect(w, r, fmt.Sprintf("/r%d", n+1), http.StatusFound)
	}))
	defer srv.Close()
	f, _ := testFetcher(t, srv)

	if _, err := f.Fetch(context.Background(), srv.URL+"/r0"); err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("expected redirect limit error, got %v", err)
	}
}

func TestFetch_RedirectToForbiddenDestinationIsRevalidated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "http://169.254.169.254:8080/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()
	f, _ := testFetcher(t, srv)

	_, err := f.Fetch(context.Background(), srv.URL+"/go")
	if !errors.Is(err, ErrInvalidURL) && !errors.Is(err, ErrSSRF) {
		t.Fatalf("redirect hop must be re-validated, got %v", err)
	}
}

func TestFetch_RejectsPrivateAddressesInProduction(t *testing.T) {
	f, err := NewFetcher(FetcherOptions{UserAgent: "x/1"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewFetcher: %v", err)
	}
	for _, target := range []string{"http://127.0.0.1/", "http://169.254.169.254/latest/meta-data/", "http://[::1]/", "file:///etc/passwd"} {
		if _, err := f.Fetch(context.Background(), target); !errors.Is(err, ErrSSRF) && !errors.Is(err, ErrInvalidURL) {
			t.Errorf("%s: expected rejection, got %v", target, err)
		}
	}
}

func TestFetch_BodyLimit(t *testing.T) {
	big := "<html><body><p>" + strings.Repeat("CSR ", 5000) + "</p></body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, big)
		case "/report.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = io.WriteString(w, "%PDF-1.7 "+strings.Repeat("x", 5000))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f, _ := testFetcher(t, srv, func(o *FetcherOptions) { o.MaxBodyBytes = 1000; o.PDF = fakePDF{"text"} })

	page, err := f.Fetch(context.Background(), srv.URL+"/page")
	if err != nil {
		t.Fatalf("oversized HTML should be used partially, got %v", err)
	}
	if !page.BodyTruncated || page.Bytes != 1000 {
		t.Fatalf("expected a truncated 1000-byte body, got truncated=%v bytes=%d", page.BodyTruncated, page.Bytes)
	}

	if _, err := f.Fetch(context.Background(), srv.URL+"/report.pdf"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized PDF should be ErrTooLarge, got %v", err)
	}
}

func TestFetch_ContentTypes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/image.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
		case "/no-type":
			w.Header()["Content-Type"] = nil // let the client see no type at all
			_, _ = io.WriteString(w, "<!doctype html><html><body><p>sniffed html</p></body></html>")
		case "/latin1":
			w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
			_, _ = w.Write([]byte("<html><body><p>Caf\xe9 donasi</p></body></html>"))
		case "/report.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = io.WriteString(w, "%PDF-1.7 fake")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	f, _ := testFetcher(t, srv)
	if _, err := f.Fetch(context.Background(), srv.URL+"/image.png"); !errors.Is(err, ErrUnsupportedContent) {
		t.Errorf("image: expected ErrUnsupportedContent, got %v", err)
	}
	if page, err := f.Fetch(context.Background(), srv.URL+"/no-type"); err != nil || page.Kind != "html" || !strings.Contains(page.Content, "sniffed html") {
		t.Errorf("sniffing failed: %+v, %v", page, err)
	}
	if page, err := f.Fetch(context.Background(), srv.URL+"/latin1"); err != nil || !strings.Contains(page.Content, "Café donasi") {
		t.Errorf("charset decoding failed: %q, %v", page.Content, err)
	}
	if _, err := f.Fetch(context.Background(), srv.URL+"/report.pdf"); !errors.Is(err, ErrPDFUnavailable) {
		t.Errorf("PDF without extractor: expected ErrPDFUnavailable, got %v", err)
	}

	withPDF, _ := testFetcher(t, srv, func(o *FetcherOptions) { o.PDF = fakePDF{"Laporan Keberlanjutan 2025"} })
	if page, err := withPDF.Fetch(context.Background(), srv.URL+"/report.pdf"); err != nil || page.Kind != "pdf" || page.Content != "Laporan Keberlanjutan 2025" {
		t.Errorf("PDF extraction failed: %+v, %v", page, err)
	}
}

func TestFetch_StatusErrorCarriesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	f, _ := testFetcher(t, srv)

	_, err := f.Fetch(context.Background(), srv.URL+"/busy")
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 429 || se.RetryAfter != 7*time.Second || !se.Temporary() {
		t.Fatalf("expected a temporary 429 StatusError with Retry-After 7s, got %v", err)
	}
}

func TestFetch_Robots(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = io.WriteString(w, "User-agent: *\nDisallow: /private\n")
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "secret-ish")
	}))
	defer srv.Close()

	strict, _ := testFetcher(t, srv)
	if _, err := strict.Fetch(context.Background(), srv.URL+"/private/page"); !errors.Is(err, ErrNotAllowedByRobots) {
		t.Fatalf("expected ErrNotAllowedByRobots, got %v", err)
	}
	if _, err := strict.Fetch(context.Background(), srv.URL+"/public"); err != nil {
		t.Fatalf("allowed path failed: %v", err)
	}

	lenient, logs := testFetcher(t, srv, func(o *FetcherOptions) { o.RespectRobots = false })
	if _, err := lenient.Fetch(context.Background(), srv.URL+"/private/page"); err != nil {
		t.Fatalf("agent-initiated fetch should proceed, got %v", err)
	}
	if !strings.Contains(logs.String(), "robots.txt disallows") {
		t.Fatal("a robots.txt violation must still be logged")
	}
}

func TestPDFToText_RunsBinary(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "pdftotext")
	// A stand-in for pdftotext: prints a fixed text to stdout ("-").
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'Program CSR dari PDF'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := NewPDFToTextAt(script, 5*time.Second, 1<<20).ExtractText(context.Background(), []byte("%PDF"))
	if err != nil || strings.TrimSpace(got) != "Program CSR dari PDF" {
		t.Fatalf("got %q, %v", got, err)
	}

	capped, err := NewPDFToTextAt(script, 5*time.Second, 7).ExtractText(context.Background(), []byte("%PDF"))
	if err != nil || capped != "Program" {
		t.Fatalf("output cap not applied: %q, %v", capped, err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if d := parseRetryAfter("120", now); d != 2*time.Minute {
		t.Errorf("seconds form: %v", d)
	}
	if d := parseRetryAfter(now.Add(30*time.Second).Format(http.TimeFormat), now); d != 30*time.Second {
		t.Errorf("date form: %v", d)
	}
	if d := parseRetryAfter("soon", now); d != 0 {
		t.Errorf("garbage: %v", d)
	}
}

func TestProductToken(t *testing.T) {
	if got := productToken("HumanInitiativeBot/1.0 (+https://x)"); got != "HumanInitiativeBot" {
		t.Errorf("got %q", got)
	}
}
