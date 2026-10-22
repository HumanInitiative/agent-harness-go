package report_test

import (
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func sampleFigures() report.Figures {
	return report.Figures{
		Program: report.Program{ID: "p1", Name: "Program Satu"},
		Period:  report.Period{From: day("2026-01-01"), To: day("2026-03-31")},
		AsOf:    day("2026-04-02"),
		Received: []report.Amount{
			{Label: "Individuals", Rupiah: 150_000_000},
			{Label: "Corporate", Rupiah: 100_000_000},
		},
		Disbursed:     []report.Amount{{Label: "Food", Rupiah: 120_000_000}},
		Beneficiaries: []report.Count{{Label: "Families", People: 320}, {Label: "Children", People: 540}},
	}
}

func goodNarratives() []report.Narrative {
	return []report.Narrative{
		{Section: report.SectionActivities, Text: "Distributed food packages.", Sources: []string{"laporan-q1.pdf"}},
		{Section: report.SectionChallenges, Text: "Roads were flooded in March.", Sources: []string{"laporan-q1.pdf", "catatan-lapangan.docx"}},
	}
}

func TestFiguresTotals(t *testing.T) {
	f := sampleFigures()
	if got := f.TotalReceived(); got != 250_000_000 {
		t.Errorf("TotalReceived = %d", got)
	}
	if got := f.TotalDisbursed(); got != 120_000_000 {
		t.Errorf("TotalDisbursed = %d", got)
	}
	if got := f.Balance(); got != 130_000_000 {
		t.Errorf("Balance = %d", got)
	}
	if got := f.TotalBeneficiaries(); got != 860 {
		t.Errorf("TotalBeneficiaries = %d", got)
	}
}

func TestFiguresValidate(t *testing.T) {
	cases := map[string]func(*report.Figures){
		"no program":       func(f *report.Figures) { f.Program.Name = "" },
		"reversed period":  func(f *report.Figures) { f.Period.To = day("2025-01-01") },
		"no timestamp":     func(f *report.Figures) { f.AsOf = time.Time{} },
		"negative amount":  func(f *report.Figures) { f.Received[0].Rupiah = -1 },
		"unlabelled":       func(f *report.Figures) { f.Disbursed[0].Label = " " },
		"negative people":  func(f *report.Figures) { f.Beneficiaries[0].People = -3 },
		"unlabelled group": func(f *report.Figures) { f.Beneficiaries[0].Label = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := sampleFigures()
			mutate(&f)
			if err := f.Validate(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if err := sampleFigures().Validate(); err != nil {
		t.Fatalf("valid figures rejected: %v", err)
	}
}

func TestFormatRupiah(t *testing.T) {
	cases := map[int64]string{
		0: "Rp 0", 999: "Rp 999", 1000: "Rp 1.000", 1_250_000: "Rp 1.250.000",
		250_000_000: "Rp 250.000.000", -5000: "-Rp 5.000",
	}
	for in, want := range cases {
		if got := report.FormatRupiah(in); got != want {
			t.Errorf("FormatRupiah(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestAssemble(t *testing.T) {
	draft, err := report.Assemble(sampleFigures(), goodNarratives(), nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	out := draft.Markdown()
	for _, want := range []string{
		"# Donation report: Program Satu",
		"Period: 2026-01-01 to 2026-03-31",
		"**DRAFT.",
		"Reviewed by:",
		"| Funds received | Rp 250.000.000 |",
		"| Balance | Rp 130.000.000 |",
		"| Beneficiaries | 860 |",
		"| **Total** | **Rp 120.000.000** |",
		"## 5. Activities and impact",
		"Distributed food packages.",
		"## 6. Challenges and next steps",
		"Data read on 2026-04-02.",
		"- laporan-q1.pdf\n- catatan-lapangan.docx\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("draft lacks %q\n%s", want, out)
		}
	}
	if strings.Count(out, "- laporan-q1.pdf") != 1 {
		t.Error("a source cited twice must be listed once in the appendix")
	}
	if len(draft.Sources) != 2 {
		t.Errorf("Sources = %v", draft.Sources)
	}
}

func TestAssembleRefusesIncompleteNarratives(t *testing.T) {
	cases := map[string][]report.Narrative{
		"missing slot": goodNarratives()[:1],
		"empty text": {
			{Section: report.SectionActivities, Text: "  ", Sources: []string{"a.pdf"}},
			goodNarratives()[1],
		},
		"no source": {
			{Section: report.SectionActivities, Text: "x", Sources: []string{" "}},
			goodNarratives()[1],
		},
		"unknown slot": append(goodNarratives(), report.Narrative{Section: "summary", Text: "x", Sources: []string{"a"}}),
		"duplicate":    append(goodNarratives(), goodNarratives()[0]),
	}
	for name, n := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := report.Assemble(sampleFigures(), n, nil); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestAssembleRefusesInvalidFigures(t *testing.T) {
	f := sampleFigures()
	f.Program.ID = ""
	if _, err := report.Assemble(f, goodNarratives(), nil); err == nil {
		t.Fatal("expected an error")
	}
}

func TestAssembleEmptyTables(t *testing.T) {
	f := sampleFigures()
	f.Received, f.Disbursed, f.Beneficiaries = nil, nil, nil
	draft, err := report.Assemble(f, goodNarratives(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := draft.Markdown()
	if !strings.Contains(out, "Nothing recorded for this period.") ||
		!strings.Contains(out, "No beneficiaries recorded") {
		t.Errorf("empty tables must say so:\n%s", out)
	}
}

func TestRequestValidate(t *testing.T) {
	ok := report.Request{ProgramID: "p", Period: report.Period{From: day("2026-01-01"), To: day("2026-01-31")}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.ProgramID = " "
	if bad.Validate() == nil {
		t.Error("blank program id must fail")
	}
	bad = ok
	bad.Period.From = time.Time{}
	if bad.Validate() == nil {
		t.Error("unset start must fail")
	}
}

func TestAssemble_ShowsReviewNotes(t *testing.T) {
	draft, err := report.Assemble(sampleFigures(), goodNarratives(), []string{"number 999 not found"})
	if err != nil {
		t.Fatal(err)
	}
	out := draft.Markdown()
	if !strings.Contains(out, "## Notes for the reviewer") || !strings.Contains(out, "- number 999 not found") {
		t.Errorf("notes missing:\n%s", out)
	}
	if len(draft.Notes) != 1 {
		t.Errorf("Notes = %v", draft.Notes)
	}
	clean, _ := report.Assemble(sampleFigures(), goodNarratives(), nil)
	if strings.Contains(clean.Markdown(), "Notes for the reviewer") {
		t.Error("no notes, no notes section")
	}
}

func TestMaskPersonalData(t *testing.T) {
	in := "Hubungi Budi di budi@example.org atau 0812-3456-7890 / +6281234567890, NIK 3171234567890123. Dana Rp 1.250.000."
	got, n := report.MaskPersonalData(in)
	if n != 4 {
		t.Errorf("masked %d items, want 4: %s", n, got)
	}
	for _, leaked := range []string{"budi@example.org", "3456-7890", "81234567890", "3171234567890123"} {
		if strings.Contains(got, leaked) {
			t.Errorf("%q leaked: %s", leaked, got)
		}
	}
	if !strings.Contains(got, "Rp 1.250.000") {
		t.Errorf("amounts must survive masking: %s", got)
	}
}

func TestNumbersAndUnsupportedNumbers(t *testing.T) {
	doc := report.NumbersIn("Dibagikan 1.200 paket kepada 45 keluarga pada 15 Maret 2026.")
	for _, want := range []string{"1200", "45", "15", "2026"} {
		if !doc[want] {
			t.Errorf("document numbers lack %s: %v", want, doc)
		}
	}
	figs := sampleFigures().KnownNumbers()
	// 250 juta is the total received; 130 juta the balance; March is month 3.
	got := report.UnsupportedNumbers("Terkumpul Rp 250 juta, 1.200 paket, 45 keluarga, 999 relawan, bulan 3, nol 0.", doc, figs)
	if len(got) != 2 || got[0] != "0" || got[1] != "999" {
		t.Errorf("UnsupportedNumbers = %v, want [0 999]", got)
	}
	if n := report.NumbersIn("007 dan 0"); !n["7"] || !n["0"] {
		t.Errorf("leading zeros: %v", n)
	}
}
