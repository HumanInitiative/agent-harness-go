package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

// Quirks make the demo model misbehave in one specific way, so that the
// checks and the retry in the pilots can be seen working.
const (
	quirkNone         = ""
	quirkInventNumber = "invent-number" // states a number found in no document or evidence
	quirkBadJSONOnce  = "bad-json-once" // first answer is not JSON
	quirkFakeEmail    = "fake-email"    // first email contains an invented address
)

var quirks = []string{quirkInventNumber, quirkBadJSONOnce, quirkFakeEmail}

// demoModel is a scripted outbound.ModelPort. It writes plausible text by
// quoting the documents and evidence it is given, which is enough to run the
// whole pilots without a Gemini key. Everything it writes starts with
// "[DEMO]" so that nobody takes it for real output.
type demoModel struct {
	quirk string
}

var (
	documentBlock = regexp.MustCompile(`(?s)<document name="([^"]+)">\n(.*?)\n</document>`)
	evidenceBlock = regexp.MustCompile(`(?s)\[(\d+)\] ([^\n]*?), (\S+) \((diterbitkan|diambil) (\d{4}-\d{2}-\d{2})\)\n"""(.*?)"""`)
)

func (m demoModel) Generate(_ context.Context, req outbound.GenerateRequest) (outbound.GenerateResponse, error) {
	first := req.Messages[0].Content
	firstAttempt := len(req.Messages) == 1

	var out string
	switch {
	case strings.Contains(first, "Write the two sections now."):
		out = m.narrative(first, firstAttempt)
	case strings.Contains(first, "PERUSAHAAN TUJUAN: "):
		out = m.email(first, firstAttempt)
	default:
		out = "[DEMO] Ini model palsu untuk latihan; ia tidak menjawab pertanyaan sungguhan."
	}

	var in int
	for _, msg := range req.Messages {
		in += len([]rune(msg.Content))
	}
	in += len([]rune(req.SystemPrompt))
	return outbound.GenerateResponse{
		Output: out,
		// Roughly four characters a token, the usual rule of thumb.
		Usage: outbound.Usage{InputTokens: in / 4, OutputTokens: len([]rune(out)) / 4},
	}, nil
}

type docSentence struct{ source, text string }

// narrative answers the report prompt: two sentences from the documents for
// each section, citing the documents they came from.
func (m demoModel) narrative(prompt string, firstAttempt bool) string {
	if m.quirk == quirkBadJSONOnce && firstAttempt {
		return "Maaf, saya belum bisa menyusun narasi untuk laporan ini."
	}
	var sentences []docSentence
	for _, d := range documentBlock.FindAllStringSubmatch(prompt, -1) {
		for _, s := range splitSentences(d[2]) {
			sentences = append(sentences, docSentence{source: d[1], text: s})
		}
	}
	activities := pick(sentences, []string{"membagikan", "menjangkau", "keluarga", "siswa", "distribusi"}, 2)
	challenges := pick(sentences, []string{"banjir", "kendala", "harga", "perlu", "usulan", "terlambat"}, 2)

	activityText := "[DEMO] " + joinSentences(activities)
	if m.quirk == quirkInventNumber {
		activityText += " Sebanyak 900 relawan terlibat sepanjang periode ini."
	}
	answer := map[string]any{
		"activities": map[string]any{"text": activityText, "sources": sources(activities)},
		"challenges": map[string]any{"text": "[DEMO] " + joinSentences(challenges), "sources": sources(challenges)},
	}
	b, _ := json.Marshal(answer)
	return string(b)
}

// email answers the outreach prompt with a short email built from the first
// piece of evidence it was shown.
func (m demoModel) email(prompt string, firstAttempt bool) string {
	if m.quirk == quirkBadJSONOnce && firstAttempt {
		return "Maaf, saya tidak bisa menulis email ini."
	}
	company := lineAfter(prompt, "PERUSAHAAN TUJUAN: ")
	program := lineAfter(prompt, "Nama: ")
	ev := evidenceBlock.FindStringSubmatch(prompt)
	if ev == nil {
		b, _ := json.Marshal(map[string]any{"subject": "Kemitraan program " + program, "body": "[DEMO] Tidak ada bukti yang ditampilkan.", "cited_evidence": []int64{}})
		return string(b)
	}
	id, _ := strconv.ParseInt(ev[1], 10, 64)
	claim := firstSentence(strings.Join(strings.Fields(ev[6]), " "))
	body := fmt.Sprintf("Yth. Tim CSR %s,\n\n[DEMO] Kami dari Human Initiative membaca di \"%s\" (%s %s) bahwa %s "+
		"Kami mengelola program \"%s\" dan ingin membuka percakapan tentang kemungkinan kemitraan.\n\n[Nama pengirim]\n[Jabatan]\n[Kontak]",
		company, ev[2], ev[4], ev[5], claim, program)
	switch {
	case m.quirk == quirkFakeEmail && firstAttempt:
		body += "\nHubungi langsung direktur@rahasia.example untuk percepatan."
	case m.quirk == quirkInventNumber:
		body += "\nKami berharap dukungan sebesar Rp 450 juta."
	}
	b, _ := json.Marshal(map[string]any{"subject": "Kemitraan program " + program, "body": body, "cited_evidence": []int64{id}})
	return string(b)
}

var sentenceEnd = regexp.MustCompile(`[.!?]\s+`)

// splitSentences cuts text into sentences of a useful length, dropping
// headings and masked placeholders.
func splitSentences(text string) []string {
	var out []string
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(para), "#-* "))
		if para == "" {
			continue
		}
		start := 0
		for _, loc := range sentenceEnd.FindAllStringIndex(para+" ", -1) {
			s := strings.TrimSpace(para[start:min(loc[1], len(para))])
			start = min(loc[1], len(para))
			if n := len([]rune(s)); n >= 40 && n <= 300 && !strings.Contains(s, "[email]") && !strings.Contains(s, "[phone]") {
				out = append(out, s)
			}
		}
	}
	return out
}

// pick returns up to n sentences, those containing a keyword first, then any,
// in document order and without repeats.
func pick(all []docSentence, keywords []string, n int) []docSentence {
	var out []docSentence
	used := map[int]bool{}
	take := func(match bool) {
		for i, s := range all {
			if len(out) == n {
				return
			}
			if used[i] {
				continue
			}
			hit := false
			for _, k := range keywords {
				hit = hit || strings.Contains(strings.ToLower(s.text), k)
			}
			if !match || hit {
				out = append(out, s)
				used[i] = true
			}
		}
	}
	take(true)
	take(false)
	return out
}

func joinSentences(ss []docSentence) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = strings.TrimRight(s.text, ".") + "."
	}
	return strings.Join(parts, " ")
}

func sources(ss []docSentence) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range ss {
		if !seen[s.source] {
			seen[s.source] = true
			out = append(out, s.source)
		}
	}
	return out
}

func lineAfter(text, marker string) string {
	i := strings.Index(text, marker)
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(text[i+len(marker):], "\n", 2)[0])
}

func firstSentence(s string) string {
	if loc := sentenceEnd.FindStringIndex(s + " "); loc != nil && loc[0] < len(s) {
		return s[:loc[0]+1]
	}
	return s
}
