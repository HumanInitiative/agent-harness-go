package outreach

import (
	"fmt"
	"strings"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
)

const dateLayout = "2006-01-02"

// Draft lays the shortlist out as a document (Markdown or Word, through the
// report renderers). The facts come from the index and the numbers from the
// run; only the email wording is the model's, and it is shown with the
// evidence it cites.
func (s Shortlist) Draft() report.Draft {
	var d report.Draft
	add := func(b ...report.Block) { d.Blocks = append(d.Blocks, b...) }
	para := func(text string) report.Block { return report.Block{Kind: report.BlockParagraph, Text: text} }
	h := func(level int, text string) report.Block {
		return report.Block{Kind: report.BlockHeading, Level: level, Text: text}
	}

	add(
		h(1, "Calon mitra CSR untuk: "+s.Program.Name),
		para("Dibuat "+s.GeneratedAt.Format(dateLayout)+". "+
			"DRAFT. Disusun dengan bantuan AI dari indeks CSR. Perusahaan berstatus \"belum diverifikasi\" "+
			"belum dikonfirmasi orang, dan setiap email perlu dibaca serta disesuaikan sebelum dikirim."),
	)
	if len(s.Warnings) > 0 {
		add(report.Block{Kind: report.BlockParagraph, Bold: true, Text: "Perhatikan kriteria program:"},
			report.Block{Kind: report.BlockBullets, Items: s.Warnings})
	}
	if len(s.Entries) == 0 {
		add(para("Tidak ada perusahaan di indeks yang cocok dengan program ini."))
		return d
	}

	rows := make([][]string, 0, len(s.Entries))
	for i, e := range s.Entries {
		rows = append(rows, []string{fmt.Sprintf("%d. %s", i+1, e.Candidate.Name), fmt.Sprint(e.Candidate.Score), confirmed(e.Candidate)})
	}
	add(h(2, "Ringkasan"), report.Block{Kind: report.BlockTable, Header: []string{"Perusahaan", "Skor", "Data dikonfirmasi"}, Rows: rows})

	for i, e := range s.Entries {
		c := e.Candidate
		add(h(2, fmt.Sprintf("%d. %s (skor %d)", i+1, c.Name, c.Score)))
		status := "belum diverifikasi orang"
		if c.Verified {
			status = "diverifikasi orang"
		}
		add(para("Status: " + status + ". Data terakhir dikonfirmasi: " + confirmed(c) + "."))
		if len(e.Notes) > 0 {
			add(report.Block{Kind: report.BlockParagraph, Bold: true, Text: "Periksa sebelum bertindak:"},
				report.Block{Kind: report.BlockBullets, Items: e.Notes})
		}
		if len(c.Reasons) > 0 {
			add(report.Block{Kind: report.BlockParagraph, Bold: true, Text: "Mengapa cocok:"},
				report.Block{Kind: report.BlockBullets, Items: c.Reasons})
		}
		if progs := programLines(c); len(progs) > 0 {
			add(report.Block{Kind: report.BlockParagraph, Bold: true, Text: "Program CSR yang tercatat:"},
				report.Block{Kind: report.BlockBullets, Items: progs})
		}
		if c.ProposalChannel != "" {
			add(para("Kanal pengajuan proposal: " + c.ProposalChannel))
		}
		if e.Email == nil {
			add(para("Draf email tidak tersedia untuk perusahaan ini."))
			continue
		}
		add(report.Block{Kind: report.BlockParagraph, Bold: true, Text: "Draf email"},
			para("Subjek: "+e.Email.Subject), para(e.Email.Body))
		if basis := evidenceLines(c, e.Email.Cited); len(basis) > 0 {
			add(report.Block{Kind: report.BlockParagraph, Bold: true, Text: "Dasar informasi di email:"},
				report.Block{Kind: report.BlockBullets, Items: basis})
		}
	}

	add(h(2, "Jejak proses"), report.Block{Kind: report.BlockBullets, Items: s.Trace.summary(len(s.Entries))})
	return d
}

func confirmed(c Candidate) string {
	if c.ConfirmedAt.IsZero() {
		return "tidak diketahui"
	}
	out := c.ConfirmedAt.Format(dateLayout)
	if c.Stale {
		out += " (sudah lama)"
	}
	return out
}

func programLines(c Candidate) []string {
	var out []string
	for _, p := range c.Programs {
		line := p.Name
		if p.PeriodStart != "" || p.PeriodEnd != "" {
			line += " (" + strings.Trim(p.PeriodStart+" - "+p.PeriodEnd, " -") + ")"
		}
		if p.ProposalDeadline != "" {
			line += ", batas proposal " + p.ProposalDeadline
		}
		out = append(out, line)
	}
	if c.HiddenPrograms > 0 {
		out = append(out, fmt.Sprintf("dan %d program lain yang sudah berakhir atau tidak terlihat lagi", c.HiddenPrograms))
	}
	return out
}

func evidenceLines(c Candidate, ids []int64) []string {
	var out []string
	for _, id := range ids {
		ev, ok := c.EvidenceByID(id)
		if !ok {
			continue
		}
		when := ev.Date.Format(dateLayout)
		if !ev.DatePublished {
			when = "diambil " + when
		}
		out = append(out, fmt.Sprintf("%s (%s), %s: \"%s\"", firstNonEmpty(ev.Title, ev.URL), when, ev.URL, snippet(ev.Excerpt, 220)))
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func snippet(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "..."
	}
	return s
}

func (t Trace) summary(shortlisted int) []string {
	out := []string{fmt.Sprintf("%d perusahaan dinilai, %d masuk daftar.", t.Candidates, shortlisted)}
	out = append(out, fmt.Sprintf("Pemakaian model: %d token masuk, %d token keluar.", t.InputTokens, t.OutputTokens))
	if t.EstimatedCostRupiah != nil {
		out = append(out, "Perkiraan biaya: "+report.FormatRupiah(int64(*t.EstimatedCostRupiah+0.5))+".")
	}
	out = append(out, fmt.Sprintf("Lama proses: %.1f detik.", float64(t.DurationMS)/1000))
	return out
}
