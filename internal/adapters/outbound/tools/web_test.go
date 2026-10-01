package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

type fakeSearcher struct {
	results  []websearch.Result
	err      error
	gotQuery string
	gotLimit int
}

func (f *fakeSearcher) Search(_ context.Context, q string, limit int) ([]websearch.Result, error) {
	f.gotQuery, f.gotLimit = q, limit
	return f.results, f.err
}

type fakeFetcher struct {
	page websearch.Page
	err  error
}

func (f fakeFetcher) Fetch(context.Context, string) (websearch.Page, error) { return f.page, f.err }

var fixedNow = func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }

func TestWebSearchTool_FormatsAndWrapsResults(t *testing.T) {
	s := &fakeSearcher{results: []websearch.Result{
		{Title: "Program CSR", URL: "https://contoh.co.id/csr", Snippet: "Beasiswa", Source: "searxng"},
		{Title: "Laporan", URL: "https://contoh.co.id/laporan.pdf", Source: "searxng"},
	}}
	out, err := NewWebSearchTool(s, fixedNow).Execute(context.Background(), json.RawMessage(`{"query":"  csr pendidikan ","limit":50}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if s.gotQuery != "csr pendidikan" || s.gotLimit != maxSearchResults {
		t.Errorf("searcher got query=%q limit=%d", s.gotQuery, s.gotLimit)
	}
	for _, want := range []string{
		`<web_content source="web_search:searxng" fetched_at="2026-10-01T00:00:00Z" untrusted="true">`,
		"1. Program CSR\n   URL: https://contoh.co.id/csr\n   Beasiswa",
		"2. Laporan\n   URL: https://contoh.co.id/laporan.pdf",
		"</web_content>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestWebSearchTool_DefaultsAndValidation(t *testing.T) {
	s := &fakeSearcher{}
	tool := NewWebSearchTool(s, fixedNow)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"x"}`))
	if err != nil || s.gotLimit != defaultSearchResults || !strings.Contains(out, "No web results") {
		t.Fatalf("defaults/empty: out=%q err=%v limit=%d", out, err, s.gotLimit)
	}
	for name, args := range map[string]string{
		"empty query":  `{"query":"  "}`,
		"long query":   fmt.Sprintf(`{"query":"%s"}`, strings.Repeat("a", maxQueryChars+1)),
		"invalid json": `nope`,
	} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(args)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestWebSearchTool_ProviderOutageIsExplained(t *testing.T) {
	s := &fakeSearcher{err: fmt.Errorf("%w: all down", websearch.ErrNoProvider)}
	_, err := NewWebSearchTool(s, fixedNow).Execute(context.Background(), json.RawMessage(`{"query":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "temporarily unavailable") {
		t.Fatalf("got %v", err)
	}
}

func TestWebFetchTool_TruncatesAndWraps(t *testing.T) {
	page := websearch.Page{
		FinalURL:  "https://contoh.co.id/csr",
		Title:     "Program TJSL",
		Content:   strings.Repeat("Paragraf program CSR.\n\n", 500),
		FetchedAt: fixedNow(),
	}
	out, err := NewWebFetchTool(fakeFetcher{page: page}).Execute(context.Background(),
		json.RawMessage(`{"url":"https://contoh.co.id/csr","max_tokens":300}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, `<web_content source="https://contoh.co.id/csr"`) || !strings.Contains(out, "# Program TJSL") {
		t.Errorf("unexpected output start: %.120s", out)
	}
	if !strings.Contains(out, "[truncated: showing about") {
		t.Error("expected a truncation marker")
	}
	if websearch.EstimateTokens(out) > 400 {
		t.Errorf("output is %d tokens, expected about 300", websearch.EstimateTokens(out))
	}
}

func TestWebFetchTool_ExplainsEmptyAndOversizedPages(t *testing.T) {
	page := websearch.Page{FinalURL: "https://spa.example", FetchedAt: fixedNow(), BodyTruncated: true}
	out, err := NewWebFetchTool(fakeFetcher{page: page}).Execute(context.Background(), json.RawMessage(`{"url":"https://spa.example"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "rendered by JavaScript") || !strings.Contains(out, "size limit") {
		t.Fatalf("expected explanations, got %s", out)
	}
}

func TestWebFetchTool_ErrorMessagesTellTheModelWhatHappened(t *testing.T) {
	cases := map[error]string{
		websearch.ErrSSRF:                             "not allowed",
		websearch.ErrNotAllowedByRobots:               "robots.txt",
		websearch.ErrUnsupportedContent:               "unsupported content type",
		websearch.ErrPDFUnavailable:                   "PDF",
		&websearch.StatusError{StatusCode: 503}:       "temporary",
		&websearch.StatusError{StatusCode: 404}:       "HTTP 404",
		fmt.Errorf("x: %w", context.DeadlineExceeded): "too long",
	}
	for cause, want := range cases {
		_, err := NewWebFetchTool(fakeFetcher{err: cause}).Execute(context.Background(), json.RawMessage(`{"url":"https://x.example"}`))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: got %v, want mention of %q", cause, err, want)
		}
	}
	if _, err := NewWebFetchTool(fakeFetcher{}).Execute(context.Background(), json.RawMessage(`{"url":""}`)); err == nil {
		t.Error("expected an error for an empty url")
	}
}

// Both tools must satisfy the port the agent service consumes.
var (
	_ outbound.ToolHandler = (*WebSearchTool)(nil)
	_ outbound.ToolHandler = (*WebFetchTool)(nil)
)

func TestWebTools_SchemasRequireArguments(t *testing.T) {
	for _, tool := range []outbound.ToolHandler{NewWebSearchTool(&fakeSearcher{}, nil), NewWebFetchTool(fakeFetcher{})} {
		schema := tool.InputSchema()
		if schema["type"] != "object" || schema["required"] == nil {
			t.Errorf("%s: schema must be an object with required fields", tool.Name())
		}
	}
}
