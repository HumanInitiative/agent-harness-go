//go:build live

// Live checks against real reports. Excluded from normal test runs and CI
// because they download PDFs (one of ~85 MB, about 200 MB in all) from
// third-party sites and need pdftotext. Run them by hand after changing
// the page-selection rules:
//
//	go test -tags live -run Live -v -timeout 30m ./internal/websearch/csr/
//
// LIVE_REPORT_URL overrides the report of the large-report check.
package csr

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

const defaultLiveReport = "https://mind.id/temp/TJSL-Report-MIND-ID-2025.pdf"

func liveFetcher(t *testing.T) *websearch.Fetcher {
	t.Helper()
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
	return fetcher
}

func TestLive_LargeReportYieldsProgramPages(t *testing.T) {
	url := os.Getenv("LIVE_REPORT_URL")
	if url == "" {
		url = defaultLiveReport
	}
	fetcher := liveFetcher(t)

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

func pageRange(from, to int) []int {
	var out []int
	for n := from; n <= to; n++ {
		out = append(out, n)
	}
	return out
}

// selectionBenchmark lists real reports with the pages a person marked as
// describing community or CSR programs (2026-10-08). Bilingual, Indonesian
// and English-only reports from banking, food, insurance and mining.
var selectionBenchmark = []struct {
	name, url string
	program   []int
}{
	{"Asuransi Astra SR 2024", "https://www.asuransiastra.com/wp-content/uploads/2025/04/SUSTAINABILITY-REPORT-2024.pdf",
		append(append([]int{13, 14, 15, 16}, pageRange(38, 40)...), pageRange(56, 67)...)},
	{"BCA SR 2024", "https://www.bca.co.id/-/media/Feature/Report/File/S8/Laporan-Keberlanjutan/2025/20250212-BCA-SR-2024-INA.pdf",
		append([]int{9, 15}, pageRange(114, 130)...)},
	{"CPIN SR 2024", "https://cp.co.id/wp-content/uploads/2025/04/Sustainability-Report-CPIN-2024.pdf",
		append([]int{14}, pageRange(56, 66)...)},
	{"Merdeka Gold SR 2025", "https://merdekagoldresources.com/wp-content/uploads/2026/04/Sustainability-Report-EMAS-2025.pdf",
		append([]int{5}, pageRange(61, 68)...)},
	{"RAIN SR 2025", "https://raintbk.com/wp-content/uploads/2026/04/Sustainability-Report-Rain-Tbk-KKGI-2025.pdf",
		pageRange(74, 82)},
	{"Bank Mandiri TJSL 2024", "https://www.bankmandiri.co.id/documents/20143/390966416/Laporan+TJSL.pdf/70a6ac8a-c0c7-8744-a001-b0e8634b5be9?t=1746668708005",
		pageRange(3, 22)},
	{"Medco SR 2024 (English)", "https://www.medcoenergi.com/uploads/sreports/2024/MEDC_SR2024_ENG_0705.pdf",
		append([]int{20, 21}, pageRange(128, 149)...)},
	{"BNI SR 2024 (English)", "https://www.banktrack.org/download/csr_report_2024_24/srbni2024eng.pdf",
		append([]int{13}, pageRange(90, 107)...)},
}

// TestLive_PageSelectionPrecision measures which share of the selected
// pages are program pages. Measured on 2026-10-08: 0.90 on average with
// these rules, 0.67 with the first version (presence-only scoring, no
// header removal, no neighbour carry-over, Indonesian-heavy vocabulary).
func TestLive_PageSelectionPrecision(t *testing.T) {
	fetcher := liveFetcher(t)
	var sum float64
	measured := 0
	for _, r := range selectionBenchmark {
		page, err := fetcher.Fetch(context.Background(), r.url)
		if err != nil {
			t.Logf("%s: skipped, %v", r.name, err) // reports move; one missing one is not a failure
			continue
		}
		program := map[int]bool{}
		for _, n := range r.program {
			program[n] = true
		}
		selected := SelectPages(page.Content, defaultTokensPerDocument)
		hits := 0
		var marks []string
		for _, p := range selected {
			mark := "-"
			if program[p.Number] {
				hits++
				mark = "+"
			}
			marks = append(marks, fmt.Sprintf("%s%d", mark, p.Number))
		}
		precision := float64(hits) / float64(max(len(selected), 1))
		sum += precision
		measured++
		t.Logf("%-26s precision %.2f  %s", r.name, precision, strings.Join(marks, " "))
	}
	if measured < len(selectionBenchmark)/2 {
		t.Fatalf("only %d of %d reports could be fetched", measured, len(selectionBenchmark))
	}
	mean := sum / float64(measured)
	t.Logf("mean precision %.2f over %d reports", mean, measured)
	if mean < 0.8 {
		t.Fatalf("page selection precision dropped to %.2f (was 0.90)", mean)
	}
}
