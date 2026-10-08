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
	{"community development", 3}, {"social investment", 3}, {"pengembangan masyarakat", 3},
	{"social responsibility", 3},
	// Indonesian terms come with their English counterparts: many reports
	// are bilingual, and some are published in English only.
	{"pemberdayaan", 2}, {"empower", 2}, {"stunting", 2}, {"umkm", 2}, {"msme", 2}, {"bantuan", 2},
	{"donasi", 2}, {"donation", 2}, {"santunan", 2}, {"charity", 2}, {"posyandu", 2}, {"pelatihan", 2},
	{"realisasi", 2}, {"penyaluran", 2}, {"kemitraan", 2}, {"yayasan", 2}, {"foundation", 2},
	{"bencana", 2}, {"disaster", 2}, {"air bersih", 2}, {"clean water", 2}, {"binaan", 2},
	{"livelihood", 2}, {"community engagement", 2}, {"proposal", 2},
	{"pendidikan", 1}, {"education", 1}, {"kesehatan", 1}, {"health", 1}, {"masyarakat", 1},
	{"communit", 1}, {"desa", 1}, {"village", 1}, {"warga", 1}, {"siswa", 1}, {"student", 1}, {"petani", 1},
	{"farmer", 1}, {"program", 1},

	// Pages reports are full of that are not about CSR programs. Employee
	// training, customer service and supplier pages share the program
	// vocabulary ("pelatihan", "pemberdayaan"), so they need their own.
	{"daftar isi", -20}, {"table of contents", -20}, {"pemangku kepentingan yang terhormat", -4},
	{"dear stakeholders", -4}, {"laporan keuangan", -4}, {"financial statements", -4}, {"neraca", -4},
	{"auditor", -3}, {"kode etik", -3}, {"code of conduct", -3}, {"komisaris", -3}, {"jam pelatihan", -3},
	{"training hours", -3}, {"direksi", -2}, {"gas rumah kaca", -2}, {"greenhouse gas", -2},
	{"indeks gri", -2}, {"gri index", -2}, {"pojk", -2}, {"pengungkapan", -2}, {"disclosure", -2},
	{"hak asasi manusia", -2}, {"human rights", -2}, {"sumber daya manusia", -2}, {"human resource", -2},
	{"keselamatan kerja", -2}, {"occupational", -2}, {"pengadaan", -2}, {"procurement", -2},
	{"pemasok", -2}, {"supplier", -2}, {"emisi", -1}, {"karyawan", -1}, {"employee", -1}, {"pelanggan", -1},
	{"customer", -1}, {"nasabah", -1}, {"sertifikasi", -1}, {"certification", -1},
}

var (
	moneyPattern = regexp.MustCompile(`(?i)\b(rp|idr)\s?[\d.,]+`)
	yearPattern  = regexp.MustCompile(`\b20\d\d\b`)
)

const (
	// minPageScore keeps pages that merely mention "program" or
	// "masyarakat" out of the selection.
	minPageScore = 20
	// minPageChars skips covers, dividers and photo pages.
	minPageChars = 200
	// maxTokensPerPage stops one dense page from using the whole budget.
	maxTokensPerPage = 2000
	// maxTermRepeats caps how often one term counts on a page.
	maxTermRepeats = 3
	// minOwnScore is what a page must score on its own before it can
	// inherit from a neighbour, so a table of contents or a divider next
	// to a program page stays out.
	minOwnScore = 8
	// neighbourShare is the percentage of the stronger neighbouring page's
	// score a page inherits.
	neighbourShare = 30
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
	pages := stripRunningHeaders(strings.Split(content, websearch.PageBreak))
	scores := make([]int, len(pages))
	for i, text := range pages {
		pages[i] = strings.TrimSpace(text)
		if len(pages[i]) >= minPageChars {
			scores[i] = max(scorePage(pages[i]), 0)
		}
	}
	var candidates []DocPage
	for i, text := range pages {
		if len(text) < minPageChars {
			continue
		}
		// Reports describe programs in runs of consecutive pages, and a
		// page deep in such a run often names only its own program ("Desa
		// Wisata Limbongan ...") without the generic CSR vocabulary. Half
		// of the stronger neighbour's score carries over.
		neighbour := 0
		if i > 0 {
			neighbour = scores[i-1]
		}
		if i+1 < len(scores) {
			neighbour = max(neighbour, scores[i+1])
		}
		if s := scores[i] + neighbour*neighbourShare/100; scores[i] >= minOwnScore && s >= minPageScore {
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

// stripRunningHeaders removes lines repeated on many pages: running
// headers, footers and navigation bars ("Sambutan Presiden Direktur ·
// Ikhtisar Kinerja · ..."). They say nothing about the page, blur the
// difference between pages when scoring, and would cost tokens on every
// page sent to the model.
func stripRunningHeaders(pages []string) []string {
	if len(pages) < 5 {
		return pages
	}
	seen := map[string]int{}
	for _, p := range pages {
		lines := map[string]bool{}
		for _, l := range strings.Split(p, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines[l] = true
			}
		}
		for l := range lines {
			seen[l]++
		}
	}
	threshold := max(3, len(pages)/5)
	out := make([]string, len(pages))
	for i, p := range pages {
		var kept []string
		for _, l := range strings.Split(p, "\n") {
			if t := strings.TrimSpace(l); t == "" || seen[t] < threshold {
				kept = append(kept, l)
			}
		}
		out[i] = strings.Join(kept, "\n")
	}
	return out
}

func scorePage(text string) int {
	low := " " + strings.Join(strings.Fields(strings.ToLower(text)), " ") + " "
	score := 0
	for _, t := range pageTerms {
		// Repetition counts, up to a cap: a program page keeps returning
		// to its subject, a summary page names many subjects once each.
		if n := strings.Count(low, t.term); n > 0 {
			score += t.weight * min(n, maxTermRepeats)
		}
	}
	score += min(len(moneyPattern.FindAllStringIndex(low, 4)), 3)
	if yearPattern.MatchString(low) {
		score++
	}
	return score
}
