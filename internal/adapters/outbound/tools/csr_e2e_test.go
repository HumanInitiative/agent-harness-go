package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

// noNetwork fails any HTTP request made through the default transport for
// the rest of the test.
type noNetwork struct{ t *testing.T }

func (n noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.t.Errorf("unexpected web request to %s: CSR answers must come from the index", r.URL)
	return nil, errors.New("network disabled in this test")
}

// TestCSRIndex_EndToEnd asks a prospect question against a real index, as
// a crawl leaves it, and checks the answer cites evidence URLs with dates
// and current programs only, without any web request.
func TestCSRIndex_EndToEnd(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = noNetwork{t}
	t.Cleanup(func() { http.DefaultTransport = original })

	ctx := context.Background()
	crawledAt := time.Date(2026, 9, 20, 2, 0, 0, 0, time.UTC)
	store, err := csr.OpenStore(ctx, ":memory:", func() time.Time { return crawledAt })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// What a crawl writes: a company, a checked route and an extraction.
	id, _, err := store.UpsertCompany(ctx, csr.Company{Name: "PT Contoh Energi Tbk", Domain: "contohenergi.co.id", Source: csr.SourceSeed})
	if err != nil {
		t.Fatal(err)
	}
	report := "https://contohenergi.co.id/laporan-2025.pdf"
	pageID, err := store.UpsertPage(ctx, csr.Page{CompanyID: id, URL: report, Kind: csr.KindReport, DiscoveredVia: csr.ViaHomepage, Score: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordPageCheck(ctx, pageID, 200, csr.PageActive, "hash", 0, crawledAt.Add(90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ex := csr.Extraction{
		HasCSRContent: true,
		Evidence: []csr.Evidence{
			{URL: report + "#page=42", Excerpt: "Beasiswa Prestasi 2026 untuk siswa di Banten, pendaftaran hingga 30 November 2026", Kind: csr.KindReport, FetchedAt: crawledAt},
			{URL: report + "#page=40", Excerpt: "Beasiswa Prestasi 2024 telah disalurkan kepada 300 siswa di Banten", Kind: csr.KindReport, FetchedAt: crawledAt},
		},
		Profile: csr.Profile{
			FocusAreas:      []csr.Claim{{Value: "pendidikan", EvidenceIDs: []int64{0}}},
			Regions:         []csr.Claim{{Value: "Banten", EvidenceIDs: []int64{0}}},
			ModelConfidence: 0.8,
		},
		Programs: []csr.Program{
			{Name: "Beasiswa Prestasi 2026", FocusAreas: []string{"pendidikan"}, Regions: []string{"Banten"},
				PeriodStart: "2026", ProposalDeadline: "2026-11-30", EvidenceIDs: []int64{0}},
			{Name: "Beasiswa Prestasi 2024", FocusAreas: []string{"pendidikan"}, Regions: []string{"Banten"},
				PeriodStart: "2024", PeriodEnd: "2024", EvidenceIDs: []int64{1}},
		},
		ReadURLs: []string{report},
	}
	if err := store.SaveExtraction(ctx, id, ex); err != nil {
		t.Fatal(err)
	}

	institution, err := csr.LoadInstitutionProfile(strings.NewReader("institution_profile:\n  focus_areas: [pendidikan]\n  regions: [Banten]\n  min_confidence: 0.6\n"))
	if err != nil {
		t.Fatal(err)
	}
	asked := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	index := csr.NewIndex(store, institution, csr.IndexOptions{Now: func() time.Time { return asked }})
	tool := NewFindCSRProspectsTool(index, func() time.Time { return asked })

	out, err := tool.Execute(ctx, json.RawMessage(`{"query":"beasiswa Banten"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"1. PT Contoh Energi Tbk",
		"Data per: 2026-09-20 (terakhir dibaca dari sumber) | sumber terakhir dicek: 2026-09-20",
		"- Beasiswa Prestasi 2026 [aktif; periode 2026; batas proposal 2026-11-30]",
		"program aktif yang relevan: Beasiswa Prestasi 2026",
		"(1 program yang sudah berakhir atau tidak aktif disembunyikan",
		"https://contohenergi.co.id/laporan-2025.pdf#page=42 (diambil 2026-09-20)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("answer missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "Beasiswa Prestasi 2024") || strings.Contains(out, "PERHATIAN") {
		t.Fatalf("an expired program must be hidden by default and fresh data not flagged:\n%s", out)
	}

	out, err = tool.Execute(ctx, json.RawMessage(`{"query":"beasiswa Banten","include_inactive":true}`))
	if err != nil || !strings.Contains(out, "- Beasiswa Prestasi 2024 [sudah berakhir; periode 2024]") {
		t.Fatalf("include_inactive must show the expired program: %v\n%s", err, out)
	}

	// Half a year later without a successful check, the same data is
	// labelled stale instead of presented as current.
	later := csr.NewIndex(store, institution, csr.IndexOptions{Now: func() time.Time { return asked.AddDate(0, 6, 0) }})
	out, _ = NewCheckCompanyTool(later, nil).Execute(ctx, json.RawMessage(`{"name":"Contoh Energi"}`))
	if !strings.Contains(out, "PERHATIAN: data mungkin sudah usang") {
		t.Fatalf("old data must be labelled stale:\n%s", out)
	}
}
