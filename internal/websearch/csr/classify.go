package csr

import (
	"net/url"
	"strings"
	"unicode"
)

// MinLinkScore is the score a link needs to be recorded as a CSR route.
// Calibrated on the homepages of the seed companies: it keeps pages such as
// "Keberlanjutan" or "ESG > Social" and drops generic "Social" links.
const MinLinkScore = 3

// term is a weighted phrase. A trailing "*" makes it a prefix ("sustainab*"
// matches "sustainability" and "sustainable"); otherwise whole words only.
type term struct {
	phrase string
	weight float64
}

// csrTerms were chosen from real CSR navigation on Indonesian corporate
// sites; the negative terms come from false positives seen on the same
// sites ("Media Sosial", "Tanggung Jawab Dewan Komisaris", "Tanggung Jawab
// Produk", "Bidang Usaha ... TJSL", "Supply Chain Due Diligence").
var csrTerms = []term{
	// Unambiguous CSR vocabulary.
	{"csr", 5}, {"tjsl", 5}, {"pkbl", 5},
	{"tanggung jawab sosial", 5}, {"corporate social responsibility", 5}, {"social responsibility", 4},
	{"community development", 4}, {"comdev", 4}, {"pengembangan masyarakat", 4}, {"pemberdayaan masyarakat", 4},
	{"kemasyarakatan", 3}, {"filantropi", 3}, {"philanthrop*", 3},
	{"shared value", 3}, {"share value", 3}, {"nilai sosial", 3},
	{"peduli", 3}, {"bakti", 3}, {"yayasan", 3}, {"foundation", 3},
	{"keberlanjutan", 3}, {"sustainab*", 3}, {"sustainb*", 3}, {"berkelanjutan", 2},
	{"beasiswa", 2}, {"scholarship*", 2}, {"donasi", 2}, {"kemitraan", 2}, {"community", 2},
	{"esg", 2}, {"social", 1}, {"sosial", 1}, {"lingkungan", 1}, {"environment*", 1},
	{"masyarakat", 1}, {"program", 1},
	{"laporan keberlanjutan", 3}, {"sustainability report*", 3}, {"laporan tjsl", 3},
	{"laporan tahunan", 1}, {"annual report*", 1},

	// Pages that merely share words with CSR.
	{"media sosial", -8}, {"social media", -8}, {"sosial media", -8},
	{"komisaris", -4}, {"direksi", -4}, {"governance", -4}, {"tata kelola", -4}, {"gcg", -4},
	{"whistleblow*", -4}, {"kode etik", -4}, {"komite", -3},
	{"karir", -6}, {"career*", -6}, {"lowongan", -6}, {"rekrutmen", -6},
	{"login", -6}, {"register", -6}, {"privasi", -6}, {"privacy", -6}, {"cookie*", -6},
	{"tender", -4}, {"pengadaan", -4}, {"procurement", -4},
	{"supply chain", -3}, {"due diligence", -3}, {"saham", -3}, {"stock", -3},
	{"produk", -2}, {"product*", -2}, {"usaha", -2}, {"bisnis", -2}, {"safety", -2},
	{"risk", -2}, {"risiko", -2}, {"investor", -1},
}

var reportTerms = []string{"laporan keberlanjutan", "sustainability report*", "laporan tjsl", "laporan tahunan", "annual report*", "report*", "laporan"}

var foundationTerms = []string{"yayasan", "foundation"}

var newsTerms = []string{"news", "berita", "artikel", "article*", "siaran pers", "press*", "publikasi", "blog", "media"}

// LinkScore is how strongly a link looks like a CSR page, and which kind.
type LinkScore struct {
	Score float64
	Kind  PageKind
}

// ScoreLink scores a link from its visible text and its URL (host and path).
// Each term counts once, wherever it appears.
func ScoreLink(text, rawURL string) LinkScore {
	var host, path string
	if u, err := url.Parse(rawURL); err == nil {
		host = u.Hostname()
		path = u.Path + " " + u.RawQuery
		if unescaped, err := url.PathUnescape(u.Path); err == nil {
			path = unescaped + " " + u.RawQuery
		}
	}
	hay := words(text + " " + subdomainLabels(host) + " " + path)
	pathHay := words(path)

	var score float64
	for _, t := range csrTerms {
		if contains(hay, t.phrase) {
			score += t.weight
		}
	}
	// Prefer the Indonesian version when a site links both languages.
	if contains(pathHay, "en") || strings.Contains(strings.ToLower(path), "lang=en") {
		score -= 0.5
	}

	kind := KindCSRProgram
	switch {
	case strings.HasSuffix(strings.ToLower(pathOnly(rawURL)), ".pdf") || containsAny(hay, reportTerms):
		kind = KindReport
	case containsAny(hay, foundationTerms):
		kind = KindFoundation
	case containsAny(pathHay, newsTerms):
		kind = KindNews
	}
	return LinkScore{Score: score, Kind: kind}
}

// words lowercases s and turns every non-alphanumeric run into one space,
// padded with spaces so whole-word matching can use " word ".
func words(s string) string {
	var b strings.Builder
	b.WriteByte(' ')
	space := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	if !space {
		b.WriteByte(' ')
	}
	return b.String()
}

func contains(hay, phrase string) bool {
	if strings.HasSuffix(phrase, "*") {
		return strings.Contains(hay, " "+strings.TrimSuffix(phrase, "*"))
	}
	return strings.Contains(hay, " "+phrase+" ")
}

func containsAny(hay string, phrases []string) bool {
	for _, p := range phrases {
		if contains(hay, p) {
			return true
		}
	}
	return false
}

// subdomainLabels returns the labels left of the registrable part, so
// "tjsl.kai.id" contributes "tjsl" while "www" is ignored.
func subdomainLabels(host string) string {
	labels := strings.Split(strings.ToLower(host), ".")
	if len(labels) <= 2 {
		return ""
	}
	var out []string
	for _, l := range labels[:len(labels)-2] {
		if l != "www" {
			out = append(out, l)
		}
	}
	return strings.Join(out, " ")
}

func pathOnly(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		return u.Path
	}
	return rawURL
}
