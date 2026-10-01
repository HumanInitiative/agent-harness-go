package csr

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// SeedRow is one company in a seed file. Only Name is required.
type SeedRow struct {
	Name   string `json:"name"`
	Sector string `json:"sector"`
	Region string `json:"region"`
	Domain string `json:"domain"`
}

// ImportReport summarizes a seed import, including every row that was not
// used and why, so nothing is dropped silently.
type ImportReport struct {
	Inserted  int
	Updated   int
	Unchanged int
	Problems  []string
}

// utf8BOM is the byte-order mark spreadsheet tools often prepend to CSVs;
// left in place it would turn the first header into "\ufeffname".
const utf8BOM = "\xef\xbb\xbf"

// maxSeedRows bounds an import; seed lists are curated by hand.
const maxSeedRows = 10000

// ReadSeedCSV parses a seed CSV with a header row containing at least
// "name" (and optionally sector, region, domain), in any order and case.
func ReadSeedCSV(r io.Reader) ([]SeedRow, []string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("csr: read seed header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, utf8BOM)))] = i
	}
	if _, ok := col["name"]; !ok {
		return nil, nil, errors.New(`csr: seed CSV needs a "name" column`)
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}

	var rows []SeedRow
	var problems []string
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("line %d: %v", line, err))
			continue
		}
		if len(rows) >= maxSeedRows {
			return nil, nil, fmt.Errorf("csr: seed has more than %d rows", maxSeedRows)
		}
		rows = append(rows, SeedRow{Name: get(rec, "name"), Sector: get(rec, "sector"), Region: get(rec, "region"), Domain: get(rec, "domain")})
	}
	return rows, problems, nil
}

// ReadSeedJSON parses a JSON array of seed rows.
func ReadSeedJSON(r io.Reader) ([]SeedRow, error) {
	dec := json.NewDecoder(io.LimitReader(r, 16<<20))
	dec.DisallowUnknownFields()
	var rows []SeedRow
	if err := dec.Decode(&rows); err != nil {
		return nil, fmt.Errorf("csr: read seed JSON: %w", err)
	}
	if len(rows) > maxSeedRows {
		return nil, fmt.Errorf("csr: seed has more than %d rows", maxSeedRows)
	}
	return rows, nil
}

// ImportSeed upserts seed rows as companies with source "seed" and status
// "new". Domains are stored as unverified: a seed list (especially one an
// LLM helped write) can contain wrong or nonexistent domains, so the
// resolver confirms them before they are trusted.
func ImportSeed(ctx context.Context, store *Store, rows []SeedRow) (ImportReport, error) {
	var report ImportReport
	seen := map[string]int{}
	for i, row := range rows {
		label := fmt.Sprintf("row %d (%q)", i+1, row.Name)
		key := NormalizeName(row.Name)
		if key == "" {
			report.Problems = append(report.Problems, label+": missing company name")
			continue
		}
		if first, dup := seen[key]; dup {
			report.Problems = append(report.Problems, fmt.Sprintf("%s: duplicate of row %d", label, first))
			continue
		}
		seen[key] = i + 1
		if row.Domain != "" && NormalizeDomain(row.Domain) == "" {
			report.Problems = append(report.Problems, fmt.Sprintf("%s: ignoring invalid domain %q", label, row.Domain))
			row.Domain = ""
		}

		_, res, err := store.UpsertCompany(ctx, Company{
			Name: row.Name, Sector: strings.ToLower(row.Sector), Region: row.Region, Domain: row.Domain, Source: SourceSeed,
		})
		if err != nil {
			return report, fmt.Errorf("csr: import %s: %w", label, err)
		}
		switch res {
		case Inserted:
			report.Inserted++
		case Updated:
			report.Updated++
		default:
			report.Unchanged++
		}
	}
	return report, nil
}
