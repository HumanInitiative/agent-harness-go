//go:build live

// Live check against a real annual report. Excluded from normal test runs
// and CI because it downloads a ~85 MB PDF from a third-party site and
// needs pdftotext; run it by hand to confirm large reports still stream,
// convert and yield program pages:
//
//	go test -tags live -run Live -v ./internal/websearch/csr/
//
// LIVE_REPORT_URL overrides the report.
package csr

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

const defaultLiveReport = "https://mind.id/temp/TJSL-Report-MIND-ID-2025.pdf"

func TestLive_LargeReportYieldsProgramPages(t *testing.T) {
	url := os.Getenv("LIVE_REPORT_URL")
	if url == "" {
		url = defaultLiveReport
	}
	pdf, err := websearch.NewPDFToText(2*time.Minute, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := websearch.NewFetcher(websearch.FetcherOptions{
		UserAgent:     "HumanInitiativeBot/1.0 (+https://github.com/HumanInitiative/agent-harness-go)",
		RespectRobots: true,
		MaxPDFBytes:   150 << 20,
		PDFTimeout:    5 * time.Minute,
		PDF:           pdf,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	page, err := fetcher.Fetch(context.Background(), url)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	pages := strings.Count(page.Content, websearch.PageBreak) + 1
	t.Logf("%s: %d bytes, %d pages, ~%d tokens, fetched and converted in %s",
		url, page.Bytes, pages, websearch.EstimateTokens(page.Content), time.Since(start).Round(time.Millisecond))

	selected := SelectPages(page.Content, defaultTokensPerDocument)
	if len(selected) == 0 {
		t.Fatal("no CSR pages selected from a CSR report")
	}
	tokens := 0
	for _, p := range selected {
		tokens += websearch.EstimateTokens(p.Text)
		first := strings.Join(strings.Fields(p.Text), " ")
		t.Logf("  page %d (score %d): %.100s", p.Number, p.Score, first)
	}
	t.Logf("selected %d pages, ~%d tokens", len(selected), tokens)
	if tokens > defaultTokensPerDocument {
		t.Fatalf("selection over budget: %d tokens", tokens)
	}
}
