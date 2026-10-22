package csrprospects

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/outreach"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

var now = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func claim(v string, ev ...int64) csr.Claim { return csr.Claim{Value: v, EvidenceIDs: ev} }

// seed adds a company with an extracted profile; routed companies also have a
// page that was just checked, so their data counts as current.
func seed(t *testing.T, s *csr.Store, name string, routed bool, ex csr.Extraction) int64 {
	t.Helper()
	ctx := context.Background()
	id, _, err := s.UpsertCompany(ctx, csr.Company{Name: name, Source: csr.SourceSeed, Region: "Nasional"})
	if err != nil {
		t.Fatal(err)
	}
	ex.HasCSRContent = true
	if err := s.SaveExtraction(ctx, id, ex); err != nil {
		t.Fatal(err)
	}
	if routed {
		pid, err := s.UpsertPage(ctx, csr.Page{CompanyID: id, URL: "https://" + strings.ReplaceAll(strings.ToLower(name), " ", "") + ".co.id/csr", Kind: csr.KindCSRProgram, DiscoveredVia: csr.ViaHomepage, Score: 5})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.RecordPageCheck(ctx, pid, 200, csr.PageActive, "h", 0, now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func education(url string, extra ...csr.Claim) csr.Extraction {
	seeking := claim("true", 1)
	return csr.Extraction{
		Evidence: []csr.Evidence{
			{URL: url, Title: "Program TJSL", Excerpt: "Beasiswa Terang 2026 untuk siswa di Jawa Barat", Kind: csr.KindCSRProgram, FetchedAt: now, PublishedAt: now.AddDate(0, -1, 0)},
			{URL: url, Title: "Program TJSL", Excerpt: "Proposal dikirim ke csr@contoh.co.id", Kind: csr.KindCSRProgram, FetchedAt: now},
		},
		Profile: csr.Profile{
			FocusAreas: []csr.Claim{claim("pendidikan", 0)}, Regions: append([]csr.Claim{claim("Jawa Barat", 0)}, extra...),
			ProposalChannel: &csr.Claim{Value: "csr@contoh.co.id", EvidenceIDs: []int64{1}}, SeekingPartners: &seeking,
			ModelConfidence: 0.8,
		},
		Programs: []csr.Program{{Name: "Beasiswa Terang 2026", PeriodEnd: "2026-12", EvidenceIDs: []int64{0}}},
		ReadURLs: []string{url},
	}
}

func program() outreach.Program {
	return outreach.Program{Name: "Beasiswa Anak Desa", FocusAreas: []string{"pendidikan"}, Regions: []string{"Jawa Barat"}, ProgramTypes: []string{"beasiswa"}}
}

func newSource(t *testing.T) (*Source, *csr.Store) {
	t.Helper()
	store, err := csr.OpenStore(context.Background(), ":memory:", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return New(store, 0.6, csr.IndexOptions{Now: func() time.Time { return now }}), store
}

func TestProspects_ScoresAgainstTheProgram(t *testing.T) {
	src, store := newSource(t)
	ctx := context.Background()

	a := seed(t, store, "PT Contoh Energi", true, education("https://contohenergi.co.id/csr"))
	if err := store.SetStatus(ctx, a, csr.StatusVerified); err != nil {
		t.Fatal(err)
	}
	// Health only: does not fit a program about education.
	seed(t, store, "PT Sehat Sentosa", true, csr.Extraction{
		Evidence: []csr.Evidence{{URL: "https://sehat.co.id/csr", Excerpt: "Pemeriksaan kesehatan gratis", Kind: csr.KindCSRProgram, FetchedAt: now}},
		Profile:  csr.Profile{FocusAreas: []csr.Claim{claim("kesehatan", 0)}, Regions: []csr.Claim{claim("Jawa Barat", 0)}, ModelConfidence: 0.9},
		ReadURLs: []string{"https://sehat.co.id/csr"},
	})
	// Fits, but excluded by a person.
	c := seed(t, store, "PT Dikeluarkan", true, education("https://dikeluarkan.co.id/csr"))
	if err := store.SetStatus(ctx, c, csr.StatusExcluded); err != nil {
		t.Fatal(err)
	}
	// Fits, but its pages were never confirmed: stale, so ranked last.
	seed(t, store, "PT Lama Sekali", false, education("https://lama.co.id/csr"))

	got, err := src.Prospects(ctx, program(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.Fitting != 2 || len(got.Candidates) != 2 {
		t.Fatalf("fitting = %d, candidates = %d; want 2 and 2 (health-only and excluded left out)", got.Fitting, len(got.Candidates))
	}
	first, second := got.Candidates[0], got.Candidates[1]
	if first.Name != "PT Contoh Energi" || second.Name != "PT Lama Sekali" {
		t.Fatalf("order = %s, %s; a current company comes before a stale one", first.Name, second.Name)
	}

	if first.Score <= 0 || !first.Verified || first.Stale || first.LowConfidence {
		t.Errorf("first: %+v", first)
	}
	if !strings.Contains(strings.Join(first.Reasons, " "), "bidang fokus cocok") {
		t.Errorf("reasons: %v", first.Reasons)
	}
	if first.ProposalChannel != "csr@contoh.co.id" || first.SeekingPartners != "true" {
		t.Errorf("channel = %q, seeking = %q", first.ProposalChannel, first.SeekingPartners)
	}
	if len(first.Programs) != 1 || first.Programs[0].Name != "Beasiswa Terang 2026" || first.Programs[0].PeriodEnd != "2026-12" {
		t.Errorf("programs: %+v", first.Programs)
	}
	if len(first.Evidence) != 2 || first.Evidence[0].ID >= first.Evidence[1].ID {
		t.Fatalf("evidence must be listed by id: %+v", first.Evidence)
	}
	if e := first.Evidence[0]; !e.DatePublished || !e.Date.Equal(now.AddDate(0, -1, 0)) {
		t.Errorf("a published date is preferred: %+v", e)
	}
	if e := first.Evidence[1]; e.DatePublished || !e.Date.Equal(now) {
		t.Errorf("without a publish date the fetch date is used, and said so: %+v", e)
	}
	if first.ConfirmedAt.IsZero() {
		t.Error("ConfirmedAt must be set")
	}
	if !second.Stale || second.Verified || second.StaleReason == "" {
		t.Errorf("second: %+v", second)
	}
}

func TestProspects_LimitCutsAfterRankingButCountsEveryFit(t *testing.T) {
	src, store := newSource(t)
	seed(t, store, "PT Satu", true, education("https://satu.co.id/csr"))
	seed(t, store, "PT Dua", true, education("https://dua.co.id/csr"))
	seed(t, store, "PT Tiga", true, education("https://tiga.co.id/csr"))
	got, err := src.Prospects(context.Background(), program(), 2)
	if err != nil || got.Fitting != 3 || len(got.Candidates) != 2 {
		t.Fatalf("fitting = %d, candidates = %d, err = %v", got.Fitting, len(got.Candidates), err)
	}
}

func TestProspects_EmptyIndexAndBadLimit(t *testing.T) {
	src, _ := newSource(t)
	if got, err := src.Prospects(context.Background(), program(), 5); err != nil || got.Fitting != 0 || len(got.Candidates) != 0 {
		t.Fatalf("an empty index is an empty result: %+v, %v", got, err)
	}
	if _, err := src.Prospects(context.Background(), program(), 0); err == nil {
		t.Fatal("a limit below 1 must be refused")
	}
}

func TestProspects_ProgramRegionsChangeTheScore(t *testing.T) {
	src, store := newSource(t)
	seed(t, store, "PT Contoh Energi", true, education("https://contohenergi.co.id/csr"))
	inRegion, _ := src.Prospects(context.Background(), program(), 1)
	p := program()
	p.Regions = []string{"Papua"}
	elsewhere, _ := src.Prospects(context.Background(), p, 1)
	if inRegion.Candidates[0].Score <= elsewhere.Candidates[0].Score {
		t.Errorf("scores must follow the program's regions: %d vs %d", inRegion.Candidates[0].Score, elsewhere.Candidates[0].Score)
	}
}

func TestProspects_TermCoverageSpansTheWholeIndex(t *testing.T) {
	src, store := newSource(t)
	seed(t, store, "PT Contoh Energi", true, education("https://contohenergi.co.id/csr"))
	seed(t, store, "PT Sehat Sentosa", true, csr.Extraction{
		Evidence: []csr.Evidence{{URL: "https://sehat.co.id/csr", Excerpt: "Pemeriksaan kesehatan gratis", Kind: csr.KindCSRProgram, FetchedAt: now}},
		Profile:  csr.Profile{FocusAreas: []csr.Claim{claim("kesehatan", 0)}, Regions: []csr.Claim{claim("Seluruh Indonesia", 0)}, ModelConfidence: 0.9},
		ReadURLs: []string{"https://sehat.co.id/csr"},
	})
	p := program()
	p.FocusAreas = []string{"pendidikan", "olahraga"}
	p.Regions = []string{"Jawa Barat", "Papua"}
	got, err := src.Prospects(context.Background(), p, 1)
	if err != nil {
		t.Fatal(err)
	}
	byTerm := map[string]outreach.TermCoverage{}
	for _, tc := range got.Terms {
		byTerm[tc.Kind+":"+tc.Term] = tc
	}
	// The cut to one company does not hide the other from the coverage.
	if c := byTerm["focus:pendidikan"]; !c.Recognized || c.Group != "pendidikan" || c.Companies != 1 {
		t.Errorf("pendidikan: %+v", c)
	}
	if c := byTerm["focus:olahraga"]; c.Recognized || c.Companies != 0 {
		t.Errorf("olahraga: %+v", c)
	}
	if c := byTerm["region:Jawa Barat"]; c.Companies != 2 { // one by name, one nationwide
		t.Errorf("Jawa Barat: %+v", c)
	}
	if c := byTerm["region:Papua"]; c.Companies != 1 {
		t.Errorf("Papua (only the nationwide company): %+v", c)
	}
	if c := byTerm["type:beasiswa"]; !c.Recognized || c.Group != "pendidikan" {
		t.Errorf("beasiswa: %+v", c)
	}
}

func TestFocusGroups(t *testing.T) {
	src, _ := newSource(t)
	groups := src.FocusGroups()
	if len(groups) < 5 || groups[0].Name != "pendidikan" || len(groups[0].Synonyms) == 0 {
		t.Errorf("groups = %+v", groups)
	}
}
