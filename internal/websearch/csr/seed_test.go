package csr

import (
	"context"
	"strings"
	"testing"
)

const seedCSV = utf8BOM + "Name,Sector,Region,Domain\n" +
	"PT Contoh Energi Tbk,Energi,Nasional,contoh-energi.co.id\n" +
	"PT Contoh Perbankan Tbk,perbankan,Nasional,\n" +
	"Contoh Energi,energi,Jawa Barat,\n" + // duplicate of row 1 after normalization
	",perkebunan,Sumatera,\n" + // no name
	"PT Contoh Tambang,tambang,Kalimantan,not a domain\n"

func TestReadSeedCSV_HeaderInAnyCaseAndOrder(t *testing.T) {
	rows, problems, err := ReadSeedCSV(strings.NewReader("domain,NAME\nexample.co.id,PT Contoh\n"))
	if err != nil || len(problems) != 0 || len(rows) != 1 || rows[0].Name != "PT Contoh" || rows[0].Domain != "example.co.id" {
		t.Fatalf("got %+v %v %v", rows, problems, err)
	}
	if _, _, err := ReadSeedCSV(strings.NewReader("company,sector\nX,Y\n")); err == nil {
		t.Fatal("a CSV without a name column must be rejected")
	}
}

func TestImportSeed_ReportsEveryRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rows, problems, err := ReadSeedCSV(strings.NewReader(seedCSV))
	if err != nil || len(problems) != 0 {
		t.Fatalf("read: %v %v", problems, err)
	}

	report, err := ImportSeed(ctx, s, rows)
	if err != nil {
		t.Fatalf("ImportSeed: %v", err)
	}
	if report.Inserted != 3 || len(report.Problems) != 3 {
		t.Fatalf("unexpected report: %+v", report)
	}
	for _, want := range []string{"duplicate of row 1", "missing company name", "invalid domain"} {
		if !strings.Contains(strings.Join(report.Problems, "\n"), want) {
			t.Errorf("report should mention %q: %v", want, report.Problems)
		}
	}

	c, err := s.CompanyByNormalizedName(ctx, "contoh energi")
	if err != nil || c.Domain != "contoh-energi.co.id" || c.DomainStatus != DomainUnverified ||
		c.Sector != "energi" || c.Source != SourceSeed || c.Status != StatusNew {
		t.Fatalf("seed company stored wrong: %+v %v", c, err)
	}
	tambang, _ := s.CompanyByNormalizedName(ctx, "contoh tambang")
	if tambang.Domain != "" {
		t.Fatalf("an invalid domain must not be stored: %+v", tambang)
	}

	// Re-importing the same file is harmless.
	again, err := ImportSeed(ctx, s, rows)
	if err != nil || again.Inserted != 0 || again.Unchanged != 3 {
		t.Fatalf("re-import should change nothing: %+v %v", again, err)
	}
}

func TestReadSeedJSON(t *testing.T) {
	rows, err := ReadSeedJSON(strings.NewReader(`[{"name":"PT Contoh","sector":"energi","domain":"contoh.co.id"}]`))
	if err != nil || len(rows) != 1 || rows[0].Domain != "contoh.co.id" {
		t.Fatalf("got %+v %v", rows, err)
	}
	if _, err := ReadSeedJSON(strings.NewReader(`[{"name":"X","ceo":"someone"}]`)); err == nil {
		t.Fatal("unknown fields must be rejected")
	}
}
