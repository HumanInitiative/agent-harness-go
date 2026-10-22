package report

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Document is the text of one activity report (docx, pdf, ...), the source
// the narrative sections are written from.
type Document struct {
	// Name is the file name, which is how the draft cites the document.
	Name string
	Text string
	// Truncated is set when Text is only the start of a longer document.
	Truncated bool
}

var (
	idNumberPattern = regexp.MustCompile(`\b\d{16}\b`)
	emailPattern    = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	phonePattern    = regexp.MustCompile(`(?:\+62|\b0)8[\d \-]{8,13}\d`)
	numberPattern   = regexp.MustCompile(`\d(?:[\d.,]*\d)?`)
)

// MaskPersonalData replaces national ID numbers, email addresses and
// mobile phone numbers with placeholders and reports how many it replaced.
// Names cannot be found reliably, so this is a safety net for contact
// details, not an anonymiser: it runs before document text goes to the
// model.
func MaskPersonalData(text string) (string, int) {
	n := 0
	mask := func(re *regexp.Regexp, placeholder string) {
		text = re.ReplaceAllStringFunc(text, func(string) string {
			n++
			return placeholder
		})
	}
	mask(idNumberPattern, "[id-number]")
	mask(emailPattern, "[email]")
	mask(phonePattern, "[phone]")
	return text, n
}

// NumbersIn returns the numbers written in text, normalised by dropping
// thousands and decimal separators so "1.250.000" and "1,250,000" compare
// equal. The result is the set of distinct numbers.
func NumbersIn(text string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range numberPattern.FindAllString(text, -1) {
		out[normalizeNumber(tok)] = true
	}
	return out
}

func normalizeNumber(tok string) string {
	tok = strings.NewReplacer(".", "", ",", "").Replace(tok)
	return strings.TrimLeft(tok, "0") + zeroIfEmpty(tok)
}

// zeroIfEmpty keeps "0" from normalising to the empty string.
func zeroIfEmpty(tok string) string {
	if strings.Trim(tok, "0") == "" {
		return "0"
	}
	return ""
}

// UnsupportedNumbers returns the numbers in text that appear in none of the
// known sets, sorted. A narrative number no document or figure contains is
// something the model made up or got wrong.
func UnsupportedNumbers(text string, known ...map[string]bool) []string {
	var out []string
outer:
	for n := range NumbersIn(text) {
		for _, set := range known {
			if set[n] {
				continue outer
			}
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// KnownNumbers is every number the figures justify in a narrative: the
// amounts and counts, the totals and balance, the same amounts written in
// millions or billions when they divide exactly ("Rp 250 juta"), and the
// day, month and year of the period.
func (f Figures) KnownNumbers() map[string]bool {
	out := map[string]bool{}
	addInt := func(v int64) {
		if v < 0 {
			v = -v
		}
		out[strconv.FormatInt(v, 10)] = true
		for _, unit := range []int64{1_000_000, 1_000_000_000} {
			if v >= unit && v%unit == 0 {
				out[strconv.FormatInt(v/unit, 10)] = true
			}
		}
	}
	for _, a := range f.Received {
		addInt(a.Rupiah)
	}
	for _, a := range f.Disbursed {
		addInt(a.Rupiah)
	}
	for _, c := range f.Beneficiaries {
		addInt(int64(c.People))
	}
	addInt(f.TotalReceived())
	addInt(f.TotalDisbursed())
	addInt(f.Balance())
	addInt(int64(f.TotalBeneficiaries()))
	for _, t := range []struct{ y, m, d int }{
		{f.Period.From.Year(), int(f.Period.From.Month()), f.Period.From.Day()},
		{f.Period.To.Year(), int(f.Period.To.Month()), f.Period.To.Day()},
	} {
		addInt(int64(t.y))
		addInt(int64(t.m))
		addInt(int64(t.d))
	}
	return out
}
