package outreach_test

import (
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/outreach"
)

var day = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func program() outreach.Program {
	return outreach.Program{
		Name:        "Beasiswa Anak Desa",
		Description: "Beasiswa untuk 120 anak di desa terpencil Jawa Barat.",
		FocusAreas:  []string{"pendidikan"}, Regions: []string{"Jawa Barat"}, ProgramTypes: []string{"beasiswa"},
	}
}

func candidate() outreach.Candidate {
	return outreach.Candidate{
		CompanyID: 7, Name: "PT Contoh Energi", Domain: "contohenergi.co.id", Score: 82,
		Reasons:     []string{"fokus pendidikan sama dengan program"},
		ConfirmedAt: day, ProposalChannel: "csr@contohenergi.co.id",
		Programs: []outreach.CompanyProgram{{Name: "Beasiswa Terang 2026", PeriodStart: "2026", PeriodEnd: "2026-12", ProposalDeadline: "2026-10-31"}},
		Evidence: []outreach.Evidence{
			{ID: 11, URL: "https://contohenergi.co.id/csr", Title: "Program TJSL", Date: day, DatePublished: true,
				Excerpt: "Beasiswa Terang 2026 untuk 300 siswa di Jawa Barat. Proposal ke csr@contohenergi.co.id sebelum 31 Oktober."},
			{ID: 12, URL: "https://contohenergi.co.id/laporan", Excerpt: "Laporan keberlanjutan 2025."},
		},
	}
}

func TestProgramValidate(t *testing.T) {
	if err := program().Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*outreach.Program){
		"no name":   func(p *outreach.Program) { p.Name = " " },
		"no focus":  func(p *outreach.Program) { p.FocusAreas = []string{" ", ""} },
		"long name": func(p *outreach.Program) { p.Name = strings.Repeat("a", 201) },
		"long desc": func(p *outreach.Program) { p.Description = strings.Repeat("a", 3001) },
		"too many terms": func(p *outreach.Program) {
			p.Regions = make([]string, 13)
			for i := range p.Regions {
				p.Regions[i] = "x"
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := program()
			mutate(&p)
			if p.Validate() == nil {
				t.Fatal("expected an error")
			}
		})
	}
	c := outreach.Program{Name: " A ", FocusAreas: []string{" x ", ""}}.Clean()
	if c.Name != "A" || len(c.FocusAreas) != 1 || c.FocusAreas[0] != "x" {
		t.Errorf("Clean = %+v", c)
	}
}

func goodEmail() outreach.Email {
	return outreach.Email{
		Subject: "Kemitraan Beasiswa Anak Desa",
		Body:    "Kami melihat Beasiswa Terang 2026 untuk 300 siswa di Jawa Barat. Program kami membantu 120 anak. Proposal akan kami kirim ke csr@contohenergi.co.id.",
		Cited:   []int64{11},
	}
}

func TestVerifyEmail_AcceptsAGroundedEmail(t *testing.T) {
	problems, notes := outreach.VerifyEmail(goodEmail(), candidate(), program())
	if len(problems) != 0 || len(notes) != 0 {
		t.Fatalf("problems = %v, notes = %v", problems, notes)
	}
}

func TestVerifyEmail_Problems(t *testing.T) {
	cases := map[string]struct {
		mutate func(*outreach.Email)
		want   string
	}{
		"empty subject":    {func(e *outreach.Email) { e.Subject = "" }, "subject"},
		"empty body":       {func(e *outreach.Email) { e.Body = " " }, "body is empty"},
		"too long":         {func(e *outreach.Email) { e.Body = strings.Repeat("kata ", 700) }, "longer than"},
		"no citation":      {func(e *outreach.Email) { e.Cited = nil }, "no evidence is cited"},
		"foreign evidence": {func(e *outreach.Email) { e.Cited = []int64{11, 999} }, "evidence 999"},
		"invented email":   {func(e *outreach.Email) { e.Body += " Hubungi direktur@contohenergi.co.id." }, "direktur@contohenergi.co.id"},
		"invented link":    {func(e *outreach.Email) { e.Body += " Lihat https://contohenergi.co.id/rahasia." }, "https://contohenergi.co.id/rahasia"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := goodEmail()
			c.mutate(&e)
			problems, _ := outreach.VerifyEmail(e, candidate(), program())
			if len(problems) == 0 || !strings.Contains(strings.Join(problems, "|"), c.want) {
				t.Fatalf("problems = %v, want one mentioning %q", problems, c.want)
			}
		})
	}
}

func TestVerifyEmail_AcceptsTheDatesItIsAskedToState(t *testing.T) {
	e := goodEmail()
	e.Body += " Informasi ini kami baca di halaman yang terbit 2026-09-01 (per 1 September 2026)."
	if problems, notes := outreach.VerifyEmail(e, candidate(), program()); len(problems) != 0 || len(notes) != 0 {
		t.Fatalf("problems = %v, notes = %v: stating the evidence date must not be flagged", problems, notes)
	}
}

func TestVerifyEmail_AcceptsKnownLinksAndFlagsInventedNumbers(t *testing.T) {
	e := goodEmail()
	e.Body += " Informasi program ada di https://contohenergi.co.id/csr, dan kami berharap bantuan 450 juta."
	problems, notes := outreach.VerifyEmail(e, candidate(), program())
	if len(problems) != 0 {
		t.Fatalf("a link found in the evidence is fine: %v", problems)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "450") {
		t.Fatalf("notes = %v, want the invented 450 flagged", notes)
	}
	// The email's own date-like numbers that the evidence holds do not trigger.
	if strings.Contains(notes[0], "300") || strings.Contains(notes[0], "120") {
		t.Errorf("numbers from the evidence and the program must pass: %v", notes)
	}
}

func shortlist(email *outreach.Email, notes []string) outreach.Shortlist {
	cost := 1234.4
	return outreach.Shortlist{
		Program: program(), GeneratedAt: day,
		Entries: []outreach.Entry{{Candidate: candidate(), Email: email, Notes: notes}},
		Trace:   outreach.Trace{Candidates: 9, InputTokens: 5000, OutputTokens: 400, DurationMS: 3200, EstimatedCostRupiah: &cost},
	}
}

func TestShortlistDraft(t *testing.T) {
	e := goodEmail()
	out := shortlist(&e, []string{"Perusahaan belum diverifikasi."}).Draft().Markdown()
	for _, want := range []string{
		"# Calon mitra CSR untuk: Beasiswa Anak Desa",
		"| 1. PT Contoh Energi | 82 | 2026-09-01 |",
		"## 1. PT Contoh Energi (skor 82)",
		"Status: belum diverifikasi orang.",
		"**Periksa sebelum bertindak:**",
		"- Perusahaan belum diverifikasi.",
		"- fokus pendidikan sama dengan program",
		"- Beasiswa Terang 2026 (2026 - 2026-12), batas proposal 2026-10-31",
		"Kanal pengajuan proposal: csr@contohenergi.co.id",
		"Subjek: Kemitraan Beasiswa Anak Desa",
		"Program TJSL (2026-09-01), https://contohenergi.co.id/csr:",
		"## Jejak proses",
		"9 perusahaan dinilai, 1 masuk daftar.",
		"5000 token masuk, 400 token keluar",
		"Perkiraan biaya: Rp 1.234.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("draft lacks %q\n%s", want, out)
		}
	}
}

func TestShortlistDraft_WithoutEmailOrResults(t *testing.T) {
	out := shortlist(nil, nil).Draft().Markdown()
	if !strings.Contains(out, "Draf email tidak tersedia") || strings.Contains(out, "Draf email\n") {
		t.Errorf("a missing email must be said:\n%s", out)
	}
	empty := outreach.Shortlist{Program: program(), GeneratedAt: day}.Draft().Markdown()
	if !strings.Contains(empty, "Tidak ada perusahaan di indeks yang cocok") {
		t.Errorf("an empty shortlist must say so:\n%s", empty)
	}
	// No price configured: no cost line.
	s := shortlist(nil, nil)
	s.Trace.EstimatedCostRupiah = nil
	if strings.Contains(s.Draft().Markdown(), "Perkiraan biaya") {
		t.Error("no price configured, no estimate")
	}
}

func TestShortlistDraft_StaleAndVerified(t *testing.T) {
	s := shortlist(nil, nil)
	s.Entries[0].Candidate.Stale = true
	s.Entries[0].Candidate.Verified = true
	out := s.Draft().Markdown()
	if !strings.Contains(out, "2026-09-01 (sudah lama)") || !strings.Contains(out, "Status: diverifikasi orang.") {
		t.Errorf("stale/verified not shown:\n%s", out)
	}
}

func TestWarnings(t *testing.T) {
	groups := []outreach.FocusGroup{{Name: "pendidikan"}, {Name: "kesehatan"}}
	got := outreach.Warnings([]outreach.TermCoverage{
		{Kind: "focus", Term: "pendidikan", Recognized: true, Companies: 12},                   // fine
		{Kind: "focus", Term: "olahraga", Recognized: false, Companies: 0},                     // unknown, nobody
		{Kind: "type", Term: "pameran", Recognized: false, Companies: 3},                       // unknown, a few use the words
		{Kind: "focus", Term: "kesehatan", Group: "kesehatan", Recognized: true, Companies: 0}, // known, nobody works in it
		{Kind: "region", Term: "Papua", Recognized: true, Companies: 0},
		{Kind: "type", Term: "beasiswa", Group: "pendidikan", Recognized: true, Companies: 0},
		{Kind: "region", Term: "Jawa Barat", Recognized: true, Companies: 4},
	}, groups)
	want := []string{
		`Bidang "olahraga" bukan salah satu yang dikenali sistem (pendidikan, kesehatan). Ia hanya cocok dengan perusahaan yang memakai kata yang sama persis: belum ada perusahaan di indeks yang memakainya. Periksa ejaannya, atau pakai istilah yang dikenali.`,
		`Jenis dukungan "pameran" bukan salah satu yang dikenali sistem (pendidikan, kesehatan). Ia hanya cocok dengan perusahaan yang memakai kata yang sama persis: 3 perusahaan di indeks memakainya. Periksa ejaannya, atau pakai istilah yang dikenali.`,
		`Belum ada perusahaan di indeks yang bekerja di bidang "kesehatan".`,
		`Belum ada perusahaan di indeks yang menyebut wilayah "Papua" atau berskala nasional.`,
		`Belum ada perusahaan di indeks yang menjalankan program jenis "beasiswa".`,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d warnings, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("warning %d:\n got  %s\n want %s", i, got[i], want[i])
		}
	}
	if len(outreach.Warnings(nil, groups)) != 0 {
		t.Error("no terms, no warnings")
	}
}

func TestProgramID(t *testing.T) {
	for id, ok := range map[string]bool{"beasiswa-anak-desa": true, "a": true, "p2": true, "": false, "-x": false, "Beasiswa": false, "a b": false, "a/b": false, "../x": false} {
		if outreach.ValidProgramID(id) != ok {
			t.Errorf("ValidProgramID(%q) = %v, want %v", id, !ok, ok)
		}
	}
	if got := (outreach.Program{ID: " p1 ", Name: "A", FocusAreas: []string{"x"}}).Clean().ID; got != "p1" {
		t.Errorf("Clean must trim the id: %q", got)
	}
}

func TestShortlistDraft_ShowsCriteriaWarnings(t *testing.T) {
	s := shortlist(nil, nil)
	s.Warnings = []string{"Bidang \"olahraga\" bukan salah satu yang dikenali."}
	out := s.Draft().Markdown()
	if !strings.Contains(out, "**Perhatikan kriteria program:**") || !strings.Contains(out, "- Bidang \"olahraga\" bukan salah satu yang dikenali.") {
		t.Errorf("warnings missing:\n%s", out)
	}
	if strings.Contains(shortlist(nil, nil).Draft().Markdown(), "Perhatikan kriteria") {
		t.Error("no warnings, no section")
	}
	empty := outreach.Shortlist{Program: program(), GeneratedAt: day, Warnings: []string{"Papua kosong."}}.Draft().Markdown()
	if !strings.Contains(empty, "Papua kosong.") || !strings.Contains(empty, "Tidak ada perusahaan") {
		t.Errorf("an empty shortlist must still explain itself:\n%s", empty)
	}
}
