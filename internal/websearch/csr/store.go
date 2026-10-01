package csr

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver with FTS5
)

//go:embed schema.sql
var schemaV1 string

// ErrNotFound means the requested record does not exist.
var ErrNotFound = errors.New("csr: not found")

// Store is the SQLite-backed index of companies, routes, evidence and
// profiles. SQLite means one writer at a time: the crawler (csrctl) and the
// harness may share the file (WAL mode lets readers proceed), but this is a
// single-host design, like the rest of the harness today.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// OpenStore opens (creating if needed) the database at path and migrates it.
// Use ":memory:" for tests. now may be nil (time.Now).
func OpenStore(ctx context.Context, path string, now func() time.Time) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	if path == ":memory:" {
		dsn = "file::memory:?_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("csr: open %s: %w", path, err)
	}
	// One connection: SQLite serializes writes anyway, and an in-memory
	// database only exists within a single connection.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, now: now}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("csr: read schema version: %w", err)
	}
	if version >= 1 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schemaV1); err != nil {
		return fmt.Errorf("csr: apply schema v1: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
		return err
	}
	return tx.Commit()
}

// --- time helpers: stored as RFC 3339 text in UTC -------------------------

func ts(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTS(v sql.NullString) time.Time {
	if !v.Valid || v.String == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, v.String)
	return t
}

// --- companies --------------------------------------------------------------

const companyColumns = `id, name, name_normalized, coalesce(domain,''), domain_status, coalesce(csr_url,''),
	sector, region, source, status, confidence, last_crawled_at, next_crawl_at, created_at, updated_at`

func scanCompany(row interface{ Scan(...any) error }) (Company, error) {
	var c Company
	var lastCrawled, nextCrawl, created, updated sql.NullString
	err := row.Scan(&c.ID, &c.Name, &c.NameNormalized, &c.Domain, &c.DomainStatus, &c.CSRURL,
		&c.Sector, &c.Region, &c.Source, &c.Status, &c.Confidence, &lastCrawled, &nextCrawl, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Company{}, ErrNotFound
	}
	if err != nil {
		return Company{}, err
	}
	c.LastCrawledAt, c.NextCrawlAt = parseTS(lastCrawled), parseTS(nextCrawl)
	c.CreatedAt, c.UpdatedAt = parseTS(created), parseTS(updated)
	return c, nil
}

// UpsertResult says what UpsertCompany did.
type UpsertResult int

const (
	Inserted UpsertResult = iota
	Updated
	Unchanged
)

// UpsertCompany inserts c, or merges it into the existing company with the
// same normalized name. Merging only fills gaps: it never overwrites a
// sector, region or domain that is already set, never touches a verified
// domain, and never changes a company's review status.
func (s *Store) UpsertCompany(ctx context.Context, c Company) (int64, UpsertResult, error) {
	c.NameNormalized = NormalizeName(c.Name)
	if c.NameNormalized == "" {
		return 0, Unchanged, fmt.Errorf("csr: company name %q is empty after normalization", c.Name)
	}
	c.Domain = NormalizeDomain(c.Domain)
	now := ts(s.now())

	existing, err := s.CompanyByNormalizedName(ctx, c.NameNormalized)
	if errors.Is(err, ErrNotFound) {
		if c.Status == "" {
			c.Status = StatusNew
		}
		res, err := s.db.ExecContext(ctx, `INSERT INTO companies
			(name, name_normalized, domain, domain_status, sector, region, source, status, confidence, created_at, updated_at)
			VALUES (?, ?, nullif(?, ''), ?, ?, ?, ?, ?, ?, ?, ?)`,
			strings.TrimSpace(c.Name), c.NameNormalized, c.Domain, DomainUnverified, c.Sector, c.Region,
			c.Source, c.Status, c.Confidence, now, now)
		if err != nil {
			return 0, Unchanged, fmt.Errorf("csr: insert company: %w", err)
		}
		id, err := res.LastInsertId()
		return id, Inserted, err
	}
	if err != nil {
		return 0, Unchanged, err
	}

	changed := false
	if existing.Sector == "" && c.Sector != "" {
		existing.Sector, changed = c.Sector, true
	}
	if existing.Region == "" && c.Region != "" {
		existing.Region, changed = c.Region, true
	}
	if existing.Domain == "" && c.Domain != "" {
		existing.Domain, existing.DomainStatus, changed = c.Domain, DomainUnverified, true
	}
	if !changed {
		return existing.ID, Unchanged, nil
	}
	_, err = s.db.ExecContext(ctx, `UPDATE companies SET sector = ?, region = ?, domain = nullif(?, ''),
		domain_status = ?, updated_at = ? WHERE id = ?`,
		existing.Sector, existing.Region, existing.Domain, existing.DomainStatus, now, existing.ID)
	return existing.ID, Updated, err
}

// Company returns the company with the given id.
func (s *Store) Company(ctx context.Context, id int64) (Company, error) {
	return scanCompany(s.db.QueryRowContext(ctx, `SELECT `+companyColumns+` FROM companies WHERE id = ?`, id))
}

// CompanyByNormalizedName returns the company whose normalized name matches.
func (s *Store) CompanyByNormalizedName(ctx context.Context, normalized string) (Company, error) {
	return scanCompany(s.db.QueryRowContext(ctx, `SELECT `+companyColumns+` FROM companies WHERE name_normalized = ?`, normalized))
}

// FindCompanies returns companies whose normalized name contains every word
// of query, best (shortest) match first.
func (s *Store) FindCompanies(ctx context.Context, query string, limit int) ([]Company, error) {
	words := strings.Fields(NormalizeName(query))
	if len(words) == 0 {
		return nil, nil
	}
	var where []string
	var args []any
	for _, w := range words {
		where = append(where, "name_normalized LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(w)+"%")
	}
	args = append(args, limit)
	return s.queryCompanies(ctx, `SELECT `+companyColumns+` FROM companies WHERE `+strings.Join(where, " AND ")+
		` ORDER BY length(name_normalized), id LIMIT ?`, args...)
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// CompaniesDue returns companies whose next crawl is due (or never ran),
// excluding companies a person excluded, oldest first.
func (s *Store) CompaniesDue(ctx context.Context, now time.Time, limit int) ([]Company, error) {
	return s.queryCompanies(ctx, `SELECT `+companyColumns+` FROM companies
		WHERE status != 'excluded' AND (next_crawl_at IS NULL OR next_crawl_at <= ?)
		ORDER BY next_crawl_at IS NOT NULL, next_crawl_at, id LIMIT ?`, ts(now), limit)
}

// ListCompanies returns companies, optionally filtered by status.
func (s *Store) ListCompanies(ctx context.Context, status string, limit int) ([]Company, error) {
	if status == "" {
		return s.queryCompanies(ctx, `SELECT `+companyColumns+` FROM companies ORDER BY id LIMIT ?`, limit)
	}
	return s.queryCompanies(ctx, `SELECT `+companyColumns+` FROM companies WHERE status = ? ORDER BY id LIMIT ?`, status, limit)
}

func (s *Store) queryCompanies(ctx context.Context, query string, args ...any) ([]Company, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Company
	for rows.Next() {
		c, err := scanCompany(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ProfiledCompanies returns companies that have an extracted profile and
// are not excluded, optionally limited to one sector.
func (s *Store) ProfiledCompanies(ctx context.Context, sector string, limit int) ([]Company, error) {
	return s.queryCompanies(ctx, `SELECT `+companyColumns+` FROM companies
		WHERE id IN (SELECT company_id FROM csr_profile) AND status != 'excluded'
			AND (? = '' OR lower(sector) = lower(?))
		ORDER BY confidence DESC, id LIMIT ?`, sector, sector, limit)
}

// SetDomain records a domain and how far it has been confirmed.
func (s *Store) SetDomain(ctx context.Context, companyID int64, domain, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE companies SET domain = nullif(?, ''), domain_status = ?, updated_at = ? WHERE id = ?`,
		domain, status, ts(s.now()), companyID)
	return err
}

// MarkCrawled records the outcome of a crawl of the company.
func (s *Store) MarkCrawled(ctx context.Context, companyID int64, csrURL string, confidence float64, next time.Time) error {
	now := s.now()
	_, err := s.db.ExecContext(ctx, `UPDATE companies SET csr_url = coalesce(nullif(?, ''), csr_url), confidence = ?,
		last_crawled_at = ?, next_crawl_at = ?, updated_at = ? WHERE id = ?`,
		csrURL, confidence, ts(now), ts(next), ts(now), companyID)
	return err
}

// SetStatus changes a company's review status. Only people (through the
// review tools) call this with StatusVerified.
func (s *Store) SetStatus(ctx context.Context, companyID int64, status string) error {
	switch status {
	case StatusNew, StatusVerified, StatusExcluded:
	default:
		return fmt.Errorf("csr: invalid status %q", status)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE companies SET status = ?, updated_at = ? WHERE id = ?`, status, ts(s.now()), companyID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- routing record -----------------------------------------------------------

// UpsertPage records a discovered route. An existing route keeps its state
// when a person set it (pinned/rejected) and keeps its original discovery
// method; its score only ever goes up, so a weaker rediscovery cannot demote
// a strong route.
func (s *Store) UpsertPage(ctx context.Context, p Page) (int64, error) {
	p.URL = CanonicalURL(p.URL)
	if p.URL == "" {
		return 0, errors.New("csr: page URL is not a valid http(s) URL")
	}
	if p.State == "" {
		p.State = PageActive
	}
	now := ts(s.now())
	_, err := s.db.ExecContext(ctx, `INSERT INTO company_pages
		(company_id, url, kind, discovered_via, score, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (company_id, url) DO UPDATE SET
			score = max(score, excluded.score),
			kind = CASE WHEN state IN ('pinned','rejected') THEN kind ELSE excluded.kind END,
			updated_at = excluded.updated_at`,
		p.CompanyID, p.URL, string(p.Kind), p.DiscoveredVia, p.Score, p.State, now, now)
	if err != nil {
		return 0, fmt.Errorf("csr: upsert page: %w", err)
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM company_pages WHERE company_id = ? AND url = ?`, p.CompanyID, p.URL).Scan(&id)
	return id, err
}

// Pages returns a company's routes, best first.
func (s *Store) Pages(ctx context.Context, companyID int64) ([]Page, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, company_id, url, kind, discovered_via, score, state,
		coalesce(http_status, 0), coalesce(content_hash, ''), last_checked_at, next_check_at, failures
		FROM company_pages WHERE company_id = ? ORDER BY state = 'pinned' DESC, score DESC, id`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Page
	for rows.Next() {
		var p Page
		var kind string
		var last, next sql.NullString
		if err := rows.Scan(&p.ID, &p.CompanyID, &p.URL, &kind, &p.DiscoveredVia, &p.Score, &p.State,
			&p.HTTPStatus, &p.ContentHash, &last, &next, &p.Failures); err != nil {
			return nil, err
		}
		p.Kind, p.LastCheckedAt, p.NextCheckAt = PageKind(kind), parseTS(last), parseTS(next)
		out = append(out, p)
	}
	return out, rows.Err()
}

// RecordPageCheck stores the outcome of fetching a route.
func (s *Store) RecordPageCheck(ctx context.Context, pageID int64, httpStatus int, state, contentHash string, failures int, next time.Time) error {
	now := ts(s.now())
	_, err := s.db.ExecContext(ctx, `UPDATE company_pages SET http_status = nullif(?, 0),
		state = CASE WHEN state IN ('pinned','rejected') THEN state ELSE ? END,
		content_hash = coalesce(nullif(?, ''), content_hash), failures = ?,
		last_checked_at = ?, next_check_at = ?, updated_at = ? WHERE id = ?`,
		httpStatus, state, contentHash, failures, now, ts(next), now, pageID)
	return err
}

// SetPageState lets a person pin or reject a route (or restore it).
func (s *Store) SetPageState(ctx context.Context, pageID int64, state string) error {
	switch state {
	case PageActive, PagePinned, PageRejected:
	default:
		return fmt.Errorf("csr: state %q cannot be set manually", state)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE company_pages SET state = ?, updated_at = ? WHERE id = ?`, state, ts(s.now()), pageID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- domain access ------------------------------------------------------------

// SetDomainAccess remembers how reachable a domain was.
func (s *Store) SetDomainAccess(ctx context.Context, a DomainAccess) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO domain_access (domain, status, detail, checked_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (domain) DO UPDATE SET status = excluded.status, detail = excluded.detail, checked_at = excluded.checked_at`,
		HostKey(a.Domain), a.Status, truncateText(a.Detail, 300), ts(s.now()))
	return err
}

// DomainAccess returns the last recorded access outcome for domain.
func (s *Store) DomainAccess(ctx context.Context, domain string) (DomainAccess, error) {
	var a DomainAccess
	var checked sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT domain, status, detail, checked_at FROM domain_access WHERE domain = ?`,
		HostKey(domain)).Scan(&a.Domain, &a.Status, &a.Detail, &checked)
	if errors.Is(err, sql.ErrNoRows) {
		return DomainAccess{}, ErrNotFound
	}
	a.CheckedAt = parseTS(checked)
	return a, err
}

// --- evidence and profiles ------------------------------------------------------

// SaveExtraction atomically replaces a company's profile: it stores the
// evidence (reusing identical rows), rewrites every claim's evidence
// references into evidence row IDs, and saves the profile. Either
// everything is written or nothing is.
//
// In the profile passed in, Claim.EvidenceIDs are indexes into evidence
// (not yet row IDs); every claim must reference at least one.
func (s *Store) SaveExtraction(ctx context.Context, companyID int64, evidence []Evidence, profile Profile) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	ids := make([]int64, len(evidence))
	for i, e := range evidence {
		_, err := tx.ExecContext(ctx, `INSERT INTO evidence (company_id, url, title, excerpt, kind, fetched_at, published_at)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (company_id, url, excerpt) DO NOTHING`,
			companyID, e.URL, e.Title, e.Excerpt, string(e.Kind), ts(e.FetchedAt), ts(e.PublishedAt))
		if err != nil {
			return fmt.Errorf("csr: insert evidence: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT id FROM evidence WHERE company_id = ? AND url = ? AND excerpt = ?`,
			companyID, e.URL, e.Excerpt).Scan(&ids[i]); err != nil {
			return err
		}
	}

	remap := func(claims []Claim) ([]byte, error) {
		out := make([]Claim, len(claims))
		for i, c := range claims {
			out[i] = Claim{Value: c.Value}
			if len(c.EvidenceIDs) == 0 {
				return nil, fmt.Errorf("csr: claim %q has no evidence", c.Value)
			}
			for _, idx := range c.EvidenceIDs {
				if idx < 0 || int(idx) >= len(ids) {
					return nil, fmt.Errorf("csr: claim %q references evidence %d of %d", c.Value, idx, len(ids))
				}
				out[i].EvidenceIDs = append(out[i].EvidenceIDs, ids[idx])
			}
		}
		return json.Marshal(out)
	}
	single := func(c *Claim) (any, error) {
		if c == nil {
			return nil, nil
		}
		b, err := remap([]Claim{*c})
		if err != nil {
			return nil, err
		}
		return string(b[1 : len(b)-1]), nil // the single object inside the array
	}

	focus, err := remap(profile.FocusAreas)
	if err != nil {
		return err
	}
	regions, err := remap(profile.Regions)
	if err != nil {
		return err
	}
	types, err := remap(profile.ProgramTypes)
	if err != nil {
		return err
	}
	partners, err := remap(profile.KnownPartners)
	if err != nil {
		return err
	}
	channel, err := single(profile.ProposalChannel)
	if err != nil {
		return err
	}
	seeking, err := single(profile.SeekingPartners)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO csr_profile (company_id, focus_areas, regions, program_types, known_partners,
		proposal_channel, seeking_partners, notes, extracted_at, model_confidence) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (company_id) DO UPDATE SET focus_areas = excluded.focus_areas, regions = excluded.regions,
			program_types = excluded.program_types, known_partners = excluded.known_partners,
			proposal_channel = excluded.proposal_channel, seeking_partners = excluded.seeking_partners,
			notes = excluded.notes, extracted_at = excluded.extracted_at, model_confidence = excluded.model_confidence`,
		companyID, string(focus), string(regions), string(types), string(partners), channel, seeking,
		profile.Notes, ts(s.now()), profile.ModelConfidence)
	if err != nil {
		return fmt.Errorf("csr: save profile: %w", err)
	}
	return tx.Commit()
}

// Profile returns a company's profile.
func (s *Store) Profile(ctx context.Context, companyID int64) (Profile, error) {
	var p Profile
	var focus, regions, types, partners string
	var channel, seeking, extracted sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT company_id, focus_areas, regions, program_types, known_partners,
		proposal_channel, seeking_partners, notes, extracted_at, model_confidence FROM csr_profile WHERE company_id = ?`,
		companyID).Scan(&p.CompanyID, &focus, &regions, &types, &partners, &channel, &seeking, &p.Notes, &extracted, &p.ModelConfidence)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, err
	}
	p.ExtractedAt = parseTS(extracted)
	for _, f := range []struct {
		raw string
		dst *[]Claim
	}{{focus, &p.FocusAreas}, {regions, &p.Regions}, {types, &p.ProgramTypes}, {partners, &p.KnownPartners}} {
		if err := json.Unmarshal([]byte(f.raw), f.dst); err != nil {
			return Profile{}, fmt.Errorf("csr: decode profile: %w", err)
		}
	}
	for _, f := range []struct {
		raw sql.NullString
		dst **Claim
	}{{channel, &p.ProposalChannel}, {seeking, &p.SeekingPartners}} {
		if f.raw.Valid && f.raw.String != "" {
			var c Claim
			if err := json.Unmarshal([]byte(f.raw.String), &c); err != nil {
				return Profile{}, fmt.Errorf("csr: decode profile: %w", err)
			}
			*f.dst = &c
		}
	}
	return p, nil
}

// Evidence returns evidence rows by id, in the order requested.
func (s *Store) Evidence(ctx context.Context, ids []int64) ([]Evidence, error) {
	out := make([]Evidence, 0, len(ids))
	for _, id := range ids {
		var e Evidence
		var kind string
		var fetched, published sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT id, company_id, url, title, excerpt, kind, fetched_at, published_at
			FROM evidence WHERE id = ?`, id).Scan(&e.ID, &e.CompanyID, &e.URL, &e.Title, &e.Excerpt, &kind, &fetched, &published)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		e.Kind, e.FetchedAt, e.PublishedAt = PageKind(kind), parseTS(fetched), parseTS(published)
		out = append(out, e)
	}
	return out, nil
}

// SearchEvidence runs a full-text search over evidence titles and excerpts
// and returns the matching company IDs, best match first.
func (s *Store) SearchEvidence(ctx context.Context, query string, limit int) ([]int64, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.company_id FROM evidence_fts f JOIN evidence e ON e.id = f.rowid
		WHERE evidence_fts MATCH ? GROUP BY e.company_id ORDER BY min(f.rank) LIMIT ?`, match, limit)
	if err != nil {
		return nil, fmt.Errorf("csr: search evidence: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ftsQuery turns free text into a safe FTS5 query: every word becomes a
// quoted prefix term joined with OR, so user input can never inject FTS
// syntax.
func ftsQuery(text string) string {
	var terms []string
	for _, w := range strings.Fields(NormalizeName(text)) {
		if len(w) >= 3 {
			terms = append(terms, `"`+w+`"*`)
		}
	}
	return strings.Join(terms, " OR ")
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
