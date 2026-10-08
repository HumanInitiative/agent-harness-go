package csr

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(context.Background(), ":memory:", func() time.Time { return t0 })
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustInsert(t *testing.T, s *Store, c Company) int64 {
	t.Helper()
	if c.Source == "" {
		c.Source = SourceSeed
	}
	id, _, err := s.UpsertCompany(context.Background(), c)
	if err != nil {
		t.Fatalf("UpsertCompany: %v", err)
	}
	return id
}

func TestStore_OpenFileAndReopenKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "csr.db")
	ctx := context.Background()
	s, err := OpenStore(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	mustInsert(t, s, Company{Name: "PT Contoh Energi Tbk"})
	s.Close()

	s, err = OpenStore(ctx, path, nil) // migration must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CompanyByNormalizedName(ctx, "contoh energi"); err != nil {
		t.Fatalf("data lost after reopen: %v", err)
	}
}

func TestStore_UpsertCompanyDedupesAndOnlyFillsGaps(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id, res, err := s.UpsertCompany(ctx, Company{Name: "PT Bank Contoh (Persero) Tbk", Sector: "perbankan", Source: SourceSeed})
	if err != nil || res != Inserted {
		t.Fatalf("insert: %v %v", res, err)
	}
	// Same company spelled differently: merged, gaps filled.
	id2, res, err := s.UpsertCompany(ctx, Company{Name: "Bank Contoh", Region: "Nasional", Domain: "https://www.contoh.co.id", Source: SourceSeed})
	if err != nil || res != Updated || id2 != id {
		t.Fatalf("merge: id=%d res=%v err=%v", id2, res, err)
	}
	// Existing values are never overwritten.
	_, res, _ = s.UpsertCompany(ctx, Company{Name: "Bank Contoh", Sector: "keuangan", Domain: "other.co.id", Source: SourceSeed})
	if res != Unchanged {
		t.Fatalf("expected Unchanged, got %v", res)
	}

	c, err := s.Company(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "PT Bank Contoh (Persero) Tbk" || c.Sector != "perbankan" || c.Region != "Nasional" ||
		c.Domain != "contoh.co.id" || c.DomainStatus != DomainUnverified || c.Status != StatusNew {
		t.Fatalf("unexpected company: %+v", c)
	}
	if _, _, err := s.UpsertCompany(ctx, Company{Name: "PT Tbk", Source: SourceSeed}); err == nil {
		t.Fatal("a name with nothing but legal words must be rejected")
	}
}

func TestStore_FindCompanies(t *testing.T) {
	s := newTestStore(t)
	mustInsert(t, s, Company{Name: "PT Bank Rakyat Indonesia (Persero) Tbk"})
	mustInsert(t, s, Company{Name: "PT Bank Rakyat Indonesia Agroniaga Tbk"})
	mustInsert(t, s, Company{Name: "PT Bank Mandiri (Persero) Tbk"})

	got, err := s.FindCompanies(context.Background(), "bank rakyat indonesia", 10)
	if err != nil || len(got) != 2 || got[0].NameNormalized != "bank rakyat indonesia" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got, _ := s.FindCompanies(context.Background(), "100%_'; DROP TABLE companies;--", 10); len(got) != 0 {
		t.Fatalf("hostile input should match nothing, got %+v", got)
	}
}

func TestStore_CompaniesDueAndMarkCrawled(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := mustInsert(t, s, Company{Name: "Alpha"})
	b := mustInsert(t, s, Company{Name: "Beta"})
	c := mustInsert(t, s, Company{Name: "Gamma"})
	if err := s.SetStatus(ctx, c, StatusExcluded); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCrawled(ctx, a, "https://alpha.example/csr", 0.8, t0.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	due, err := s.CompaniesDue(ctx, t0, 10)
	if err != nil || len(due) != 1 || due[0].ID != b {
		t.Fatalf("only Beta is due (Alpha scheduled later, Gamma excluded): %+v %v", due, err)
	}
	due, _ = s.CompaniesDue(ctx, t0.Add(72*time.Hour), 10)
	if len(due) != 2 {
		t.Fatalf("after the schedule passes Alpha is due too: %+v", due)
	}
	alpha, _ := s.Company(ctx, a)
	if alpha.CSRURL != "https://alpha.example/csr" || alpha.Confidence != 0.8 || !alpha.LastCrawledAt.Equal(t0) {
		t.Fatalf("crawl not recorded: %+v", alpha)
	}
	if err := s.SetStatus(ctx, a, "approved"); err == nil {
		t.Fatal("unknown status must be rejected")
	}
}

func TestStore_RoutingRecord(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id := mustInsert(t, s, Company{Name: "Contoh"})

	p1, err := s.UpsertPage(ctx, Page{CompanyID: id, URL: "https://WWW.contoh.co.id:443/csr#top", Kind: KindCSRProgram, DiscoveredVia: ViaHomepage, Score: 5})
	if err != nil {
		t.Fatal(err)
	}
	// Rediscovery of the same canonical URL: same row, score only goes up,
	// original discovery method kept.
	p2, _ := s.UpsertPage(ctx, Page{CompanyID: id, URL: "https://www.contoh.co.id/csr", Kind: KindCSRProgram, DiscoveredVia: ViaSearch, Score: 2})
	if p1 != p2 {
		t.Fatalf("same page stored twice: %d vs %d", p1, p2)
	}
	pages, _ := s.Pages(ctx, id)
	if len(pages) != 1 || pages[0].Score != 5 || pages[0].DiscoveredVia != ViaHomepage {
		t.Fatalf("unexpected route: %+v", pages)
	}

	// A person rejects the route; automatic updates must not revive it.
	if err := s.SetPageState(ctx, p1, PageRejected); err != nil {
		t.Fatal(err)
	}
	_ = s.RecordPageCheck(ctx, p1, 200, PageActive, "hash1", 0, t0.Add(time.Hour))
	_, _ = s.UpsertPage(ctx, Page{CompanyID: id, URL: "https://www.contoh.co.id/csr", Kind: KindNews, DiscoveredVia: ViaSitemap, Score: 9})
	pages, _ = s.Pages(ctx, id)
	if pages[0].State != PageRejected || pages[0].Kind != KindCSRProgram || pages[0].ContentHash != "hash1" || pages[0].HTTPStatus != 200 {
		t.Fatalf("manual decision overridden: %+v", pages[0])
	}
	if err := s.SetPageState(ctx, p1, PageGone); err == nil {
		t.Fatal("automatic-only states must not be settable manually")
	}

	// Pinned routes sort first regardless of score.
	pin, _ := s.UpsertPage(ctx, Page{CompanyID: id, URL: "https://contoh.co.id/tjsl", Kind: KindCSRProgram, DiscoveredVia: ViaManual, Score: 0, State: PagePinned})
	pages, _ = s.Pages(ctx, id)
	if pages[0].ID != pin {
		t.Fatalf("pinned route should come first: %+v", pages)
	}
	if _, err := s.UpsertPage(ctx, Page{CompanyID: id, URL: "javascript:alert(1)", Kind: KindCSRProgram, DiscoveredVia: ViaHomepage}); err == nil {
		t.Fatal("non-http URL must be rejected")
	}
}

func TestStore_DomainAccess(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.DomainAccess(ctx, "bri.co.id"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	_ = s.SetDomainAccess(ctx, DomainAccess{Domain: "www.BRI.co.id", Status: AccessBlocked, Detail: "Imperva"})
	_ = s.SetDomainAccess(ctx, DomainAccess{Domain: "bri.co.id", Status: AccessBlocked, Detail: "Imperva again"})
	a, err := s.DomainAccess(ctx, "bri.co.id")
	if err != nil || a.Status != AccessBlocked || a.Detail != "Imperva again" || !a.CheckedAt.Equal(t0) {
		t.Fatalf("got %+v, %v", a, err)
	}
}

func TestStore_SaveExtractionAndFTS(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id := mustInsert(t, s, Company{Name: "Contoh Energi"})
	other := mustInsert(t, s, Company{Name: "Contoh Bank"})

	evidence := []Evidence{
		{URL: "https://contoh.co.id/csr", Title: "Program TJSL", Excerpt: "Beasiswa pendidikan untuk siswa di Jawa Barat", Kind: KindCSRProgram, FetchedAt: t0},
		{URL: "https://contoh.co.id/csr", Title: "Program TJSL", Excerpt: "Proposal dikirim ke csr@contoh.co.id", Kind: KindCSRProgram, FetchedAt: t0},
	}
	seeking := Claim{Value: "true", EvidenceIDs: []int64{1}}
	profile := Profile{
		FocusAreas:      []Claim{{Value: "pendidikan", EvidenceIDs: []int64{0}}},
		Regions:         []Claim{{Value: "Jawa Barat", EvidenceIDs: []int64{0}}},
		ProposalChannel: &Claim{Value: "csr@contoh.co.id", EvidenceIDs: []int64{1}},
		SeekingPartners: &seeking,
		ModelConfidence: 0.75,
	}
	ex := Extraction{Evidence: evidence, Profile: profile, ReadURLs: []string{"https://contoh.co.id/csr"}}
	if err := s.SaveExtraction(ctx, id, ex); err != nil {
		t.Fatalf("SaveExtraction: %v", err)
	}
	// Saving again reuses identical evidence rows instead of duplicating.
	if err := s.SaveExtraction(ctx, id, ex); err != nil {
		t.Fatal(err)
	}

	got, err := s.Profile(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FocusAreas) != 1 || got.FocusAreas[0].Value != "pendidikan" || got.ProposalChannel == nil ||
		got.SeekingPartners == nil || got.ModelConfidence != 0.75 {
		t.Fatalf("unexpected profile: %+v", got)
	}
	ev, err := s.Evidence(ctx, append(got.FocusAreas[0].EvidenceIDs, got.ProposalChannel.EvidenceIDs...))
	if err != nil || len(ev) != 2 || ev[0].Excerpt != evidence[0].Excerpt || ev[1].Excerpt != evidence[1].Excerpt {
		t.Fatalf("claims must resolve to their evidence: %+v %v", ev, err)
	}
	var count int
	_ = s.db.QueryRow(`SELECT count(*) FROM evidence`).Scan(&count)
	if count != 2 {
		t.Fatalf("evidence duplicated: %d rows", count)
	}

	ids, err := s.SearchEvidence(ctx, "beasiswa", 10)
	if err != nil || len(ids) != 1 || ids[0] != got.FocusAreas[0].EvidenceIDs[0] {
		t.Fatalf("FTS search: %v %v", ids, err)
	}
	if ids, _ := s.SearchEvidence(ctx, `beasiswa" OR evil NEAR(`, 10); len(ids) != 1 {
		t.Fatalf("FTS syntax in user input must be neutralized, got %v", ids)
	}
	if _, err := s.Profile(ctx, other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for a company without profile, got %v", err)
	}
}

func TestStore_SaveExtractionRejectsUnsupportedClaimsAtomically(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id := mustInsert(t, s, Company{Name: "Contoh"})
	evidence := []Evidence{{URL: "https://contoh.co.id/csr", Excerpt: "teks", Kind: KindCSRProgram, FetchedAt: t0}}

	for name, p := range map[string]Profile{
		"no evidence":        {FocusAreas: []Claim{{Value: "pendidikan"}}},
		"index out of range": {FocusAreas: []Claim{{Value: "pendidikan", EvidenceIDs: []int64{5}}}},
	} {
		if err := s.SaveExtraction(ctx, id, Extraction{Evidence: evidence, Profile: p}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	var count int
	_ = s.db.QueryRow(`SELECT count(*) FROM evidence`).Scan(&count)
	if count != 0 {
		t.Fatalf("a failed save must not leave evidence behind, found %d rows", count)
	}
}

func TestStore_SameDocumentIsStoredOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id := mustInsert(t, s, Company{Name: "Contoh"})

	// Routes: the seed crawl found one spelling, discovery another.
	a, _ := s.UpsertPage(ctx, Page{CompanyID: id, URL: "https://contoh.co.id/csr", Kind: KindCSRProgram, DiscoveredVia: ViaHomepage, Score: 5})
	b, _ := s.UpsertPage(ctx, Page{CompanyID: id, URL: "http://www.contoh.co.id/csr?utm_source=x", Kind: KindCSRProgram, DiscoveredVia: ViaSearch, Score: 7})
	pages, _ := s.Pages(ctx, id)
	if a != b || len(pages) != 1 || pages[0].Score != 7 || pages[0].URL != "https://contoh.co.id/csr" {
		t.Fatalf("one route expected, with the better score: %+v", pages)
	}
	// A different path, or a different report page, is a different document.
	if DocumentKey("https://contoh.co.id/csr/") == DocumentKey("https://contoh.co.id/csr") ||
		DocumentKey("https://contoh.co.id/r.pdf#page=2") == DocumentKey("https://contoh.co.id/r.pdf#page=3") {
		t.Fatal("different documents must keep different keys")
	}

	// Evidence: the same excerpt reached through two spellings.
	excerpt := "Beasiswa pendidikan untuk siswa di Banten"
	for _, u := range []string{"https://contoh.co.id/csr", "https://www.contoh.co.id/csr"} {
		ex := Extraction{
			Evidence: []Evidence{{URL: u, Excerpt: excerpt, Kind: KindCSRProgram, FetchedAt: t0}},
			Profile:  Profile{FocusAreas: []Claim{{Value: "pendidikan", EvidenceIDs: []int64{0}}}},
			ReadURLs: []string{u},
		}
		if err := s.SaveExtraction(ctx, id, ex); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	_ = s.db.QueryRow(`SELECT count(*) FROM evidence`).Scan(&count)
	if count != 1 {
		t.Fatalf("the same excerpt of the same document must be stored once, got %d rows", count)
	}

	// Signals: one page, two spellings.
	for _, u := range []string{"https://berita.example/a", "http://www.berita.example/a"} {
		if _, err := s.AddSignal(ctx, Signal{CompanyID: id, URL: u, Host: "berita.example", Excerpt: excerpt}); err != nil {
			t.Fatal(err)
		}
	}
	if sigs, _ := s.Signals(ctx, id); len(sigs) != 1 {
		t.Fatalf("one signal expected, got %d", len(sigs))
	}
}
