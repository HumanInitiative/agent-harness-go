package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/platform/config"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

const programYAML = `
program:
  name: Beasiswa Anak Desa
  description: Beasiswa untuk 120 anak.
  focus_areas: [pendidikan]
  regions: [Jawa Barat]
  program_types: [beasiswa]
`

func writeProgram(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "program.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func shortlistStore(t *testing.T) *csr.Store {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	store, err := csr.OpenStore(ctx, ":memory:", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	id, _, err := store.UpsertCompany(ctx, csr.Company{Name: "PT Contoh Energi", Source: csr.SourceSeed})
	if err != nil {
		t.Fatal(err)
	}
	err = store.SaveExtraction(ctx, id, csr.Extraction{
		HasCSRContent: true,
		Evidence:      []csr.Evidence{{URL: "https://contoh.co.id/csr", Excerpt: "Beasiswa pendidikan di Jawa Barat", Kind: csr.KindCSRProgram, FetchedAt: now}},
		Profile: csr.Profile{
			FocusAreas: []csr.Claim{{Value: "pendidikan", EvidenceIDs: []int64{0}}}, Regions: []csr.Claim{{Value: "Jawa Barat", EvidenceIDs: []int64{0}}},
			ProposalChannel: &csr.Claim{Value: "csr@contoh.co.id", EvidenceIDs: []int64{0}}, ModelConfidence: 0.8,
		},
		Programs: []csr.Program{{Name: "Beasiswa Terang", EvidenceIDs: []int64{0}}},
		ReadURLs: []string{"https://contoh.co.id/csr"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestShortlistCommand_Table(t *testing.T) {
	var out bytes.Buffer
	err := cmdShortlist(context.Background(), config.CrawlerConfig{}, shortlistStore(t), []string{"-program", writeProgram(t, programYAML), "-limit", "5"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Beasiswa Anak Desa: 1 of 1 fitting companies", "PT Contoh Energi", "csr@contoh.co.id", "bidang fokus cocok"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

func TestShortlistCommand_CSVRatingSheet(t *testing.T) {
	var out bytes.Buffer
	err := cmdShortlist(context.Background(), config.CrawlerConfig{}, shortlistStore(t), []string{"-program", writeProgram(t, programYAML), "-csv"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&out).ReadAll()
	if err != nil {
		t.Fatalf("not valid CSV: %v\n%s", err, out.String())
	}
	if len(rows) != 2 || rows[0][2] != "company" || rows[0][9] != "relevant (y/n)" {
		t.Fatalf("rows = %v", rows)
	}
	row := rows[1]
	if row[0] != "1" || row[2] != "PT Contoh Energi" || row[6] != "csr@contoh.co.id" || row[8] != "Beasiswa Terang" || row[9] != "" || row[10] != "" {
		t.Errorf("row = %v: the rating columns must be left empty for a person", row)
	}
}

func TestShortlistCommand_Errors(t *testing.T) {
	store := shortlistStore(t)
	run := func(args ...string) error {
		return cmdShortlist(context.Background(), config.CrawlerConfig{}, store, args, &bytes.Buffer{})
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("no program file: %v", err)
	}
	if err := run("-program", filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("a missing file must be an error")
	}
	if err := run("-program", writeProgram(t, "program:\n  name: P\n")); err == nil || !strings.Contains(err.Error(), "focus area") {
		t.Errorf("a program without focus areas: %v", err)
	}
	if err := run("-program", writeProgram(t, "program:\n  name: P\n  focus_areas: [x]\n  budget: 5\n")); err == nil {
		t.Error("an unknown field must be refused")
	}
	if err := run("-program", writeProgram(t, programYAML), "-limit", "0"); err == nil {
		t.Error("limit 0 must be refused")
	}

	var out bytes.Buffer
	other := "program:\n  name: Olahraga\n  focus_areas: [olahraga]\n"
	if err := cmdShortlist(context.Background(), config.CrawlerConfig{}, store, []string{"-program", writeProgram(t, other)}, &out); err != nil ||
		!strings.Contains(out.String(), "No indexed company fits") {
		t.Errorf("no fit must say so, not fail: %v %q", err, out.String())
	}
	// ...and says why: the term is not one the scoring knows.
	if !strings.Contains(out.String(), "Check the program's criteria:") || !strings.Contains(out.String(), `Bidang "olahraga" bukan salah satu yang dikenali`) {
		t.Errorf("the criteria warning is missing: %q", out.String())
	}
}

func TestShortlistCommand_WarningsStayOutOfTheCSV(t *testing.T) {
	prog := writeProgram(t, "program:\n  name: P\n  focus_areas: [pendidikan, olahraga]\n")
	var out bytes.Buffer
	if err := cmdShortlist(context.Background(), config.CrawlerConfig{}, shortlistStore(t), []string{"-program", prog, "-csv"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "criteria") {
		t.Errorf("warnings leaked into the CSV:\n%s", out.String())
	}
	if _, err := csv.NewReader(&out).ReadAll(); err != nil {
		t.Errorf("not valid CSV: %v", err)
	}
	// A table, on the other hand, carries the warning above the rows.
	out.Reset()
	if err := cmdShortlist(context.Background(), config.CrawlerConfig{}, shortlistStore(t), []string{"-program", prog}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `Bidang "olahraga"`) || !strings.Contains(out.String(), "PT Contoh Energi") {
		t.Errorf("table:\n%s", out.String())
	}
}
