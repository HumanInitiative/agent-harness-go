package report

import (
	"strconv"
	"strings"
	"time"
)

// FormatRupiah writes whole rupiah the Indonesian way: "Rp 1.250.000".
func FormatRupiah(v int64) string {
	if v < 0 {
		return "-Rp " + formatInt(-v)
	}
	return "Rp " + formatInt(v)
}

// formatInt groups digits in threes with a dot.
func formatInt(v int64) string {
	digits := strconv.FormatInt(v, 10)
	if len(digits) <= 3 {
		return digits
	}
	var b strings.Builder
	lead := len(digits) % 3
	if lead > 0 {
		b.WriteString(digits[:lead])
	}
	for i := lead; i < len(digits); i += 3 {
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

func formatDate(t time.Time) string { return t.Format("2006-01-02") }
