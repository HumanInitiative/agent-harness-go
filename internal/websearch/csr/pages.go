package csr

import (
	"regexp"
	"sort"
	"strings"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

// DocPage is one page of a multi-page document (a PDF report) chosen for
// extraction.
type DocPage struct {
	// Number is the 1-based page number, so evidence can link to it
	// ("report.pdf#page=229").
	Number int
	Text   string
	Score  int
}

// pageTerms are weighted words that indicate a page describes CSR programs
// (positive) or something else a report is full of (negative). Matching is
// on lowercased text with whitespace collapsed. Measured on two real 264-
// and 380-page reports, the highest-scoring pages were the program pages,
// and the top 8 pages held 3-5% of the text.
var pageTerms = []struct {
	term   string
	weight int
}{
	{"tjsl", 3}, {"csr", 3}, {"tanggung jawab sosial", 3}, {"penerima manfaat", 3}, {"beneficiar", 3},
	{"beasiswa", 3}, {"scholarship", 3}, {"mitra binaan", 3}, {"bina lingkungan", 3},
	{"pemberdayaan", 2}, {"empowerment", 2}, {"stunting", 2}, {"umkm", 2}, {"bantuan", 2}, {"donasi", 2},
	{"santunan", 2}, {"posyandu", 2}, {"pelatihan", 2}, {"realisasi", 2}, {"penyaluran", 2},
	{"kemitraan", 2}, {"yayasan", 2}, {"foundation", 2}, {"bencana", 2}, {"air bersih", 2},
	{"community development", 3}, {"social investment", 3}, {"proposal", 2},
	{"pendidikan", 1}, {"education", 1}, {"kesehatan", 1}, {"health", 1}, {"masyarakat", 1},
	{"community", 1}, {"desa", 1}, {"program", 1},

	{"daftar isi", -6}, {"table of contents", -6}, {"laporan keuangan", -4}, {"financial statements", -4},
	{"neraca", -4}, {"auditor", -3}, {"kode etik", -3}, {"code of conduct", -3}, {"komisaris", -3},
	{"direksi", -2}, {"gas rumah kaca", -2}, {"greenhouse gas", -2}, {"indeks gri", -2}, {"gri index", -2},
	{"emisi", -1},
}

var (
	moneyPattern = regexp.MustCompile(`(?i)\b(rp|idr)\s?[\d.,]+`)
	yearPattern  = regexp.MustCompile(`\b20\d\d\b`)
)

const (
	// minPageScore keeps pages that merely mention "program" or
	// "masyarakat" out of the selection.
	minPageScore = 8
	// minPageChars skips covers, dividers and photo pages.
	minPageChars = 200
	// maxTokensPerPage stops one dense page from using the whole budget.
	maxTokensPerPage = 2000
)

// IsMultiPage reports whether content is a paginated document (a PDF
// converted by pdftotext), whose pages are separated by
// websearch.PageBreak.
func IsMultiPage(content string) bool {
	return strings.Contains(content, websearch.PageBreak)
}

// SelectPages picks the pages of a long document most likely to describe
// CSR programs, within maxTokens, and returns them in page order. It uses
// rules only: it runs on every report the crawler reads, and sending whole
// 150k-token reports to a model instead would cost far more for no gain.
// It returns nothing when no page looks like CSR content.
func SelectPages(content string, maxTokens int) []DocPage {
	var candidates []DocPage
	for i, text := range strings.Split(content, websearch.PageBreak) {
		text = strings.TrimSpace(text)
		if len(text) < minPageChars {
			continue
		}
		if s := scorePage(text); s >= minPageScore {
			candidates = append(candidates, DocPage{Number: i + 1, Text: text, Score: s})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })

	var out []DocPage
	used := 0
	for _, p := range candidates {
		p.Text, _ = websearch.Truncate(p.Text, maxTokensPerPage)
		cost := websearch.EstimateTokens(p.Text)
		if used+cost > maxTokens {
			continue // a smaller page further down may still fit
		}
		used += cost
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

func scorePage(text string) int {
	low := " " + strings.Join(strings.Fields(strings.ToLower(text)), " ") + " "
	score := 0
	for _, t := range pageTerms {
		if strings.Contains(low, t.term) {
			score += t.weight
		}
	}
	score += min(len(moneyPattern.FindAllStringIndex(low, 4)), 3)
	if yearPattern.MatchString(low) {
		score++
	}
	return score
}
