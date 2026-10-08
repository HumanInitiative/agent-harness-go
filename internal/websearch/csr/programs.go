package csr

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// programStatus decides a program's status from its dates and how many
// consecutive re-extractions missed it. Dates win: a program whose end
// date or deadline has passed is expired however often it is still seen
// (reports keep describing last year's programs).
func programStatus(p Program, misses int, now time.Time) string {
	switch {
	case p.ended(now):
		return ProgramExpired
	case misses >= 2:
		return ProgramInactive
	case misses == 1:
		return ProgramStale
	default:
		return ProgramActive
	}
}

// savePrograms records the programs of one extraction and advances the
// lifecycle of programs its pages no longer mention. A program counts as
// missed only when every page it was read from was re-read (read holds
// canonical URLs).
func savePrograms(ctx context.Context, q queryer, companyID int64, fresh []Program, read map[string]bool, now time.Time) error {
	existing, err := loadPrograms(ctx, q, companyID)
	if err != nil {
		return err
	}
	byKey := map[string]Program{}
	for _, p := range existing {
		byKey[programKey(p)] = p
	}

	seen := map[string]bool{}
	for _, p := range fresh {
		key := programKey(p)
		if seen[key] {
			continue
		}
		seen[key] = true
		if old, ok := byKey[key]; ok {
			// A later read that omits a date does not erase one already
			// verified.
			p.PeriodStart = firstDate(p.PeriodStart, old.PeriodStart)
			p.PeriodEnd = firstDate(p.PeriodEnd, old.PeriodEnd)
			p.ProposalDeadline = firstDate(p.ProposalDeadline, old.ProposalDeadline)
		}
		p.Status = programStatus(p, 0, now)
		if err := upsertProgram(ctx, q, companyID, key, p, now); err != nil {
			return err
		}
	}

	for key, old := range byKey {
		if seen[key] || old.Status == ProgramInactive || !allRead(old.SourceURLs, read) {
			continue
		}
		misses := old.Misses + 1
		_, err := q.ExecContext(ctx, `UPDATE csr_programs SET misses = ?, status = ?, updated_at = ? WHERE id = ?`,
			misses, programStatus(old, misses, now), ts(now), old.ID)
		if err != nil {
			return fmt.Errorf("csr: update program lifecycle: %w", err)
		}
	}
	return nil
}

func upsertProgram(ctx context.Context, q queryer, companyID int64, key string, p Program, now time.Time) error {
	focus, regions, types := jsonList(p.FocusAreas), jsonList(p.Regions), jsonList(p.ProgramTypes)
	evidence, _ := json.Marshal(p.EvidenceIDs)
	sources := jsonList(p.SourceURLs)
	_, err := q.ExecContext(ctx, `INSERT INTO csr_programs (company_id, name_key, name, description, focus_areas, regions,
		program_types, period_start, period_end, proposal_deadline, status, misses, evidence_ids, source_urls,
		first_seen_at, last_seen_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, 0, ?, ?, ?, ?, ?)
		ON CONFLICT (company_id, name_key) DO UPDATE SET name = excluded.name, description = excluded.description,
			focus_areas = excluded.focus_areas, regions = excluded.regions, program_types = excluded.program_types,
			period_start = excluded.period_start, period_end = excluded.period_end,
			proposal_deadline = excluded.proposal_deadline, status = excluded.status, misses = 0,
			evidence_ids = excluded.evidence_ids, source_urls = excluded.source_urls,
			last_seen_at = excluded.last_seen_at, updated_at = excluded.updated_at`,
		companyID, key, p.Name, p.Description, focus, regions, types,
		string(p.PeriodStart), string(p.PeriodEnd), string(p.ProposalDeadline), p.Status,
		string(evidence), sources, ts(now), ts(now), ts(now))
	if err != nil {
		return fmt.Errorf("csr: save program %q: %w", p.Name, err)
	}
	return nil
}

func firstDate(a, b PartialDate) PartialDate {
	if a != "" {
		return a
	}
	return b
}

func allRead(urls []string, read map[string]bool) bool {
	if len(urls) == 0 {
		return false
	}
	for _, u := range urls {
		if !read[u] {
			return false
		}
	}
	return true
}

func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

const programColumns = `id, company_id, name, description, focus_areas, regions, program_types,
	coalesce(period_start, ''), coalesce(period_end, ''), coalesce(proposal_deadline, ''), status, misses,
	evidence_ids, source_urls, first_seen_at, last_seen_at`

func loadPrograms(ctx context.Context, q queryer, companyID int64) ([]Program, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+programColumns+` FROM csr_programs WHERE company_id = ?
		ORDER BY coalesce(period_start, '') DESC, id`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Program
	for rows.Next() {
		p, err := scanProgram(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanProgram(row interface{ Scan(...any) error }) (Program, error) {
	var p Program
	var focus, regions, types, evidence, sources string
	var start, end, deadline string
	var first, last sql.NullString
	if err := row.Scan(&p.ID, &p.CompanyID, &p.Name, &p.Description, &focus, &regions, &types,
		&start, &end, &deadline, &p.Status, &p.Misses, &evidence, &sources, &first, &last); err != nil {
		return Program{}, err
	}
	p.PeriodStart, p.PeriodEnd, p.ProposalDeadline = PartialDate(start), PartialDate(end), PartialDate(deadline)
	p.FirstSeenAt, p.LastSeenAt = parseTS(first), parseTS(last)
	for _, f := range []struct {
		raw string
		dst any
	}{{focus, &p.FocusAreas}, {regions, &p.Regions}, {types, &p.ProgramTypes}, {evidence, &p.EvidenceIDs}, {sources, &p.SourceURLs}} {
		if err := json.Unmarshal([]byte(f.raw), f.dst); err != nil {
			return Program{}, fmt.Errorf("csr: decode program %d: %w", p.ID, err)
		}
	}
	return p, nil
}

// Programs returns every program recorded for a company, whatever its
// status, newest period first. Status is as stored; use EffectiveStatus
// for the status today.
func (s *Store) Programs(ctx context.Context, companyID int64) ([]Program, error) {
	return loadPrograms(ctx, s.db, companyID)
}

// SearchPrograms runs a full-text search over program names, descriptions,
// focus areas, regions and types, and returns matching program IDs, best
// match first.
func (s *Store) SearchPrograms(ctx context.Context, query string, limit int) ([]int64, error) {
	return s.searchFTS(ctx, query, `SELECT rowid FROM csr_programs_fts WHERE csr_programs_fts MATCH ? ORDER BY rank LIMIT ?`, limit)
}

// ExpirePrograms stores the expired status of programs whose end date or
// proposal deadline has passed, and returns how many changed. Answers do
// not depend on it (they use EffectiveStatus); it keeps the stored status
// honest for anyone reading the database directly.
func (s *Store) ExpirePrograms(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+programColumns+` FROM csr_programs
		WHERE status != 'expired' AND (period_end IS NOT NULL OR proposal_deadline IS NOT NULL)`)
	if err != nil {
		return 0, err
	}
	var ended []int64
	for rows.Next() {
		p, err := scanProgram(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		if p.ended(now) {
			ended = append(ended, p.ID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ended {
		if _, err := s.db.ExecContext(ctx, `UPDATE csr_programs SET status = 'expired', updated_at = ? WHERE id = ?`, ts(now), id); err != nil {
			return 0, err
		}
	}
	return len(ended), nil
}
