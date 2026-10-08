package csr

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const programPage = "https://contoh.co.id/csr"

// extractionWith builds an extraction from one page whose evidence item i
// supports programs[i].
func extractionWith(read []string, programs ...Program) Extraction {
	ex := Extraction{HasCSRContent: true, ReadURLs: read, Profile: Profile{ModelConfidence: 0.8}}
	for i := range programs {
		ex.Evidence = append(ex.Evidence, Evidence{URL: programPage, Excerpt: "Program " + programs[i].Name, Kind: KindCSRProgram, FetchedAt: t0})
		programs[i].EvidenceIDs = []int64{int64(i)}
	}
	ex.Programs = programs
	return ex
}

func programsByName(t *testing.T, s *Store, companyID int64) map[string]Program {
	t.Helper()
	list, err := s.Programs(context.Background(), companyID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Program{}
	for _, p := range list {
		out[p.Name] = p
	}
	return out
}

func TestPrograms_SameProgramInDifferentYearsIsTwoRecords(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id := mustInsert(t, s, Company{Name: "Contoh"})

	ex := extractionWith([]string{programPage},
		Program{Name: "Beasiswa 2023", PeriodStart: "2023", PeriodEnd: "2023"},
		Program{Name: "Beasiswa 2026", PeriodStart: "2026-01", PeriodEnd: "2026-12", ProposalDeadline: "2026-11-30"},
	)
	if err := s.SaveExtraction(ctx, id, ex); err != nil {
		t.Fatal(err)
	}
	got := programsByName(t, s, id)
	old, current := got["Beasiswa 2023"], got["Beasiswa 2026"]
	if len(got) != 2 || old.ID == current.ID {
		t.Fatalf("expected two programs, got %+v", got)
	}
	if old.Status != ProgramExpired || current.Status != ProgramActive {
		t.Fatalf("2023 must be expired and 2026 active on %s: %s / %s", t0.Format("2006-01-02"), old.Status, current.Status)
	}
	if current.ProposalDeadline != "2026-11-30" || len(current.EvidenceIDs) != 1 || current.EvidenceIDs[0] == old.EvidenceIDs[0] {
		t.Fatalf("each program keeps its own dates and evidence: %+v / %+v", old, current)
	}
	if !current.FirstSeenAt.Equal(t0) || !current.LastSeenAt.Equal(t0) || current.SourceURLs[0] != programPage {
		t.Fatalf("missing bookkeeping: %+v", current)
	}

	// The same name without a year in it is told apart by its start year.
	a := programKey(Program{Name: "Beasiswa Prestasi", PeriodStart: "2023"})
	b := programKey(Program{Name: "Beasiswa Prestasi", PeriodStart: "2026-02"})
	if a == b {
		t.Fatalf("keys must differ by start year: %q", a)
	}
}

func TestPrograms_Lifecycle(t *testing.T) {
	ctx := context.Background()
	now := t0
	s, err := OpenStore(ctx, ":memory:", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := mustInsert(t, s, Company{Name: "Contoh"})
	read := []string{programPage}
	save := func(programs ...Program) {
		t.Helper()
		if err := s.SaveExtraction(ctx, id, extractionWith(read, programs...)); err != nil {
			t.Fatal(err)
		}
	}
	status := func(name string) string { return programsByName(t, s, id)[name].Status }

	save(Program{Name: "Desa Sejahtera"}, Program{Name: "Beasiswa Prestasi"})
	if status("Desa Sejahtera") != ProgramActive {
		t.Fatal("a new program starts active")
	}

	// Missing once: stale. Missing twice: inactive. Never deleted.
	now = t0.Add(7 * day)
	save(Program{Name: "Beasiswa Prestasi"})
	if got := status("Desa Sejahtera"); got != ProgramStale {
		t.Fatalf("one miss: got %s", got)
	}
	now = t0.Add(14 * day)
	save(Program{Name: "Beasiswa Prestasi"})
	if got := status("Desa Sejahtera"); got != ProgramInactive {
		t.Fatalf("two misses: got %s", got)
	}

	// It reappears: active again, misses reset, first sighting kept.
	now = t0.Add(21 * day)
	save(Program{Name: "Desa Sejahtera"}, Program{Name: "Beasiswa Prestasi"})
	p := programsByName(t, s, id)["Desa Sejahtera"]
	if p.Status != ProgramActive || p.Misses != 0 || !p.FirstSeenAt.Equal(t0) || !p.LastSeenAt.Equal(now) {
		t.Fatalf("reappeared program: %+v", p)
	}

	// A re-read of other pages is no evidence the program is gone.
	read = []string{"https://contoh.co.id/berita"}
	save()
	if got := status("Desa Sejahtera"); got != ProgramActive {
		t.Fatalf("program from an unread page must be left alone, got %s", got)
	}
	read = []string{programPage}

	// Dates: a deadline that passes expires the program, both when read
	// (EffectiveStatus) and when stored (ExpirePrograms), with no model.
	save(Program{Name: "Desa Sejahtera"}, Program{Name: "Beasiswa Prestasi", ProposalDeadline: "2026-11"})
	p = programsByName(t, s, id)["Beasiswa Prestasi"]
	if p.Status != ProgramActive {
		t.Fatalf("deadline not passed yet: %s", p.Status)
	}
	later := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	if p.EffectiveStatus(time.Date(2026, 11, 30, 23, 0, 0, 0, time.UTC)) != ProgramActive || p.EffectiveStatus(later) != ProgramExpired {
		t.Fatal("a month deadline lasts until the month's last day")
	}
	if n, err := s.ExpirePrograms(ctx, later); err != nil || n != 1 || status("Beasiswa Prestasi") != ProgramExpired {
		t.Fatalf("ExpirePrograms: %d %v %s", n, err, status("Beasiswa Prestasi"))
	}

	// A later read that omits the date does not un-expire it.
	now = later
	save(Program{Name: "Desa Sejahtera"}, Program{Name: "Beasiswa Prestasi"})
	if p := programsByName(t, s, id)["Beasiswa Prestasi"]; p.Status != ProgramExpired || p.ProposalDeadline != "2026-11" {
		t.Fatalf("verified dates are kept: %+v", p)
	}
}

func TestSaveExtraction_KeepsClaimsFromPagesNotReRead(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	id := mustInsert(t, s, Company{Name: "Contoh"})
	news := "https://contoh.co.id/berita"

	first := Extraction{
		Evidence: []Evidence{
			{URL: programPage, Excerpt: "Beasiswa untuk siswa di Banten", Kind: KindCSRProgram, FetchedAt: t0},
			{URL: news, Excerpt: "Bantuan bencana banjir di Bekasi", Kind: KindNews, FetchedAt: t0},
		},
		Profile: Profile{
			FocusAreas: []Claim{{Value: "pendidikan", EvidenceIDs: []int64{0}}, {Value: "kebencanaan", EvidenceIDs: []int64{1}}},
		},
		ReadURLs: []string{programPage, news},
	}
	if err := s.SaveExtraction(ctx, id, first); err != nil {
		t.Fatal(err)
	}
	// Only the news page changed and was re-read: it now reports a health
	// program. The education claim from the unchanged program page stays;
	// the old news claim is replaced.
	second := Extraction{
		Evidence: []Evidence{{URL: news + "#page=2", Excerpt: "Posyandu untuk balita di Bogor", Kind: KindNews, FetchedAt: t0}},
		Profile:  Profile{FocusAreas: []Claim{{Value: "kesehatan", EvidenceIDs: []int64{0}}}},
		ReadURLs: []string{news},
	}
	if err := s.SaveExtraction(ctx, id, second); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Profile(ctx, id)
	if got := strings.Join(claimValues(p.FocusAreas), ","); got != "kesehatan,pendidikan" {
		t.Fatalf("focus areas = %s", got)
	}
}

func TestStore_MigratesVersion1Database(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "csr.db")

	// A database as the previous release left it: schema v1 with data.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{schemaV1, "PRAGMA user_version = 1",
		`INSERT INTO companies (name, name_normalized, source, created_at, updated_at) VALUES ('PT Lama', 'lama', 'seed', 'x', 'x')`} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s, err := OpenStore(ctx, path, nil)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer s.Close()
	var version int
	_ = s.db.QueryRow("PRAGMA user_version").Scan(&version)
	if version != len(migrations) {
		t.Fatalf("schema version %d, want %d", version, len(migrations))
	}
	if c, err := s.CompanyByNormalizedName(ctx, "lama"); err != nil || c.Name != "PT Lama" {
		t.Fatalf("data lost in the upgrade: %+v %v", c, err)
	}
	if _, err := s.Programs(ctx, 1); err != nil {
		t.Fatalf("programs table missing: %v", err)
	}
}

func TestPartialDate(t *testing.T) {
	for in, want := range map[string]string{"2025": "2025-12-31", "2024-02": "2024-02-29", "2025-06-30": "2025-06-30"} {
		d, ok := ParsePartialDate(in)
		if !ok || d.LastDay() != want || d.Year() != in[:4] {
			t.Errorf("%s: %q %v last=%s", in, d, ok, d.LastDay())
		}
	}
	for _, bad := range []string{"", "Juni 2025", "2025-13", "1890", "2025/06/30", "30-06-2025"} {
		if _, ok := ParsePartialDate(bad); ok {
			t.Errorf("%q must be rejected", bad)
		}
	}
}
