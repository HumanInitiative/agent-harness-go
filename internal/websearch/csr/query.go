package csr

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Prospect is a company with everything needed to judge and cite it.
type Prospect struct {
	Company Company
	// Profile is nil when nothing has been extracted yet.
	Profile *Profile
	// Programs are the company's programs to show, with Status set to the
	// status as of the query (see Program.EffectiveStatus): active ones
	// only, unless inactive ones were asked for. Programs matching a
	// free-text query come first.
	Programs []Program
	// HiddenPrograms counts expired, stale or inactive programs left out.
	HiddenPrograms int
	// Evidence holds every excerpt the profile's claims and the shown
	// programs cite, by ID.
	Evidence  map[int64]Evidence
	Routes    []Page
	Match     Match
	Freshness Freshness
}

// Freshness says how current a prospect's data is, so answers can carry
// dates and never present old data as current.
type Freshness struct {
	// ExtractedAt is when the model last read the company's pages.
	ExtractedAt time.Time
	// CheckedAt is the latest successful check of one of the company's
	// pages. Unchanged pages are not re-read by the model, so this is the
	// date the data was last confirmed.
	CheckedAt time.Time
	// Stale is set when no page confirmed the data recently, or every
	// recorded page is gone or blocked. Stale prospects stay visible,
	// labelled, and ranked last.
	Stale  bool
	Reason string
}

// ProspectFilter narrows FindProspects. Empty fields mean "any".
type ProspectFilter struct {
	Sector string
	Region string
	Focus  string
	// Query is free text matched against program names, descriptions,
	// focus areas and regions, and against evidence excerpts
	// ("beasiswa Banten").
	Query string
	// IncludeInactive also shows (and matches) expired, stale and inactive
	// programs.
	IncludeInactive bool
	Limit           int
}

// IndexOptions configures an Index. Zero values take the defaults noted on
// each field.
type IndexOptions struct {
	// StaleAfter is how long data stays current without any of its pages
	// being successfully checked. Default 120 days: longer than the 90-day
	// recheck interval of reports, so a company known only from its annual
	// report does not turn stale between two scheduled checks.
	StaleAfter time.Duration
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
}

// Index answers prospect questions for one institution profile from the
// local index only; it never fetches anything. It is what the harness
// tools talk to.
type Index struct {
	store   *Store
	profile InstitutionProfile
	opts    IndexOptions
}

// NewIndex builds an Index over store, scoring against profile.
func NewIndex(store *Store, profile InstitutionProfile, opts IndexOptions) *Index {
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = 120 * day
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Index{store: store, profile: profile, opts: opts}
}

// FindProspects returns companies whose CSR profile fits the filter, best
// match first. Only companies with an evidence-backed profile are
// considered: without evidence there is nothing to show.
func (i *Index) FindProspects(ctx context.Context, f ProspectFilter) ([]Prospect, error) {
	if f.Limit <= 0 {
		f.Limit = 10
	}
	companies, err := i.store.ProfiledCompanies(ctx, f.Sector, 1000)
	if err != nil {
		return nil, err
	}
	var q *textMatch
	if strings.TrimSpace(f.Query) != "" {
		if q, err = i.searchText(ctx, f.Query); err != nil {
			return nil, err
		}
	}

	var out []Prospect
	for _, c := range companies {
		p, err := i.load(ctx, c, f.IncludeInactive, q)
		if err != nil {
			return nil, err
		}
		if f.Region != "" && !prospectInRegion(p, f.Region) {
			continue
		}
		if f.Focus != "" && !prospectHasFocus(p, f.Focus) {
			continue
		}
		if q != nil && !q.matches(p) {
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(a, b int) bool {
		pa, pb := out[a], out[b]
		if pa.Freshness.Stale != pb.Freshness.Stale {
			return !pa.Freshness.Stale
		}
		if pa.Match.LowConfidence != pb.Match.LowConfidence {
			return !pa.Match.LowConfidence
		}
		if pa.Match.Score != pb.Match.Score {
			return pa.Match.Score > pb.Match.Score
		}
		return pa.Company.Confidence > pb.Company.Confidence
	})
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// CheckCompany looks a company up by name in the local index (exact
// normalized name first, then partial matches). It returns no prospects,
// not an error, when the company is unknown.
func (i *Index) CheckCompany(ctx context.Context, name string, includeInactive bool) ([]Prospect, error) {
	var companies []Company
	if c, err := i.store.CompanyByNormalizedName(ctx, NormalizeName(name)); err == nil {
		companies = []Company{c}
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	} else if companies, err = i.store.FindCompanies(ctx, name, 3); err != nil {
		return nil, err
	}
	out := make([]Prospect, 0, len(companies))
	for _, c := range companies {
		p, err := i.load(ctx, c, includeInactive, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (i *Index) load(ctx context.Context, c Company, includeInactive bool, q *textMatch) (Prospect, error) {
	now := i.opts.Now()
	p := Prospect{Company: c, Evidence: map[int64]Evidence{}}
	routes, err := i.store.Pages(ctx, c.ID)
	if err != nil {
		return p, err
	}
	p.Routes = routes

	profile, err := i.store.Profile(ctx, c.ID)
	if errors.Is(err, ErrNotFound) {
		p.Match = i.profile.Score(c, nil)
		p.Freshness = freshness(nil, routes, now, i.opts.StaleAfter)
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p.Profile = &profile
	p.Freshness = freshness(&profile, routes, now, i.opts.StaleAfter)

	programs, err := i.store.Programs(ctx, c.ID)
	if err != nil {
		return p, err
	}
	var active []Program
	for _, prog := range programs {
		prog.Status = prog.EffectiveStatus(now)
		if prog.Status == ProgramActive {
			active = append(active, prog)
		}
		if prog.Status == ProgramActive || includeInactive {
			p.Programs = append(p.Programs, prog)
		} else {
			p.HiddenPrograms++
		}
	}
	if q != nil {
		// Matching programs first, in their original order otherwise.
		sort.SliceStable(p.Programs, func(a, b int) bool {
			return q.programs[p.Programs[a].ID] && !q.programs[p.Programs[b].ID]
		})
	}

	var ids []int64
	for _, cl := range allClaims(profile) {
		ids = append(ids, cl.EvidenceIDs...)
	}
	for _, prog := range p.Programs {
		ids = append(ids, prog.EvidenceIDs...)
	}
	evidence, err := i.store.Evidence(ctx, ids)
	if err != nil {
		return p, err
	}
	for _, e := range evidence {
		p.Evidence[e.ID] = e
	}
	p.Match = i.profile.Score(c, &profile)
	i.profile.scorePrograms(&p.Match, active, len(programs))
	return p, nil
}

// freshness dates a prospect's data from its routes.
func freshness(profile *Profile, routes []Page, now time.Time, staleAfter time.Duration) Freshness {
	var f Freshness
	if profile != nil {
		f.ExtractedAt = profile.ExtractedAt
	}
	reachable := false
	for _, r := range routes {
		if !usable(r) {
			continue
		}
		reachable = true
		// 304: the server confirmed the page is unchanged.
		if (r.HTTPStatus == 200 || r.HTTPStatus == 304) && r.LastCheckedAt.After(f.CheckedAt) {
			f.CheckedAt = r.LastCheckedAt
		}
	}
	confirmed := f.CheckedAt
	if f.ExtractedAt.After(confirmed) {
		confirmed = f.ExtractedAt
	}
	switch {
	case profile == nil:
	case !reachable:
		f.Stale, f.Reason = true, "semua halaman CSR yang tercatat kini hilang atau memblokir akses"
	case now.Sub(confirmed) > staleAfter:
		f.Stale = true
		f.Reason = fmt.Sprintf("halaman sumber terakhir berhasil dicek %d hari lalu", int(now.Sub(confirmed)/day))
	}
	return f
}

// textMatch holds the result of a free-text query over the index.
type textMatch struct {
	programs map[int64]bool
	evidence map[int64]bool
}

func (i *Index) searchText(ctx context.Context, text string) (*textMatch, error) {
	m := &textMatch{programs: map[int64]bool{}, evidence: map[int64]bool{}}
	programIDs, err := i.store.SearchPrograms(ctx, text, 500)
	if err != nil {
		return nil, err
	}
	for _, id := range programIDs {
		m.programs[id] = true
	}
	evidenceIDs, err := i.store.SearchEvidence(ctx, text, 1000)
	if err != nil {
		return nil, err
	}
	for _, id := range evidenceIDs {
		m.evidence[id] = true
	}
	return m, nil
}

// matches reports whether one of the prospect's shown programs, or an
// excerpt its current profile cites, matched the query. Excerpts of
// programs that are hidden (expired, inactive) do not count, so an old
// program cannot make a company look current.
func (m *textMatch) matches(p Prospect) bool {
	for _, prog := range p.Programs {
		if m.programs[prog.ID] {
			return true
		}
	}
	if p.Profile != nil {
		for _, cl := range allClaims(*p.Profile) {
			for _, id := range cl.EvidenceIDs {
				if m.evidence[id] {
					return true
				}
			}
		}
	}
	return false
}

// allClaims lists every claim of a profile.
func allClaims(p Profile) []Claim {
	out := append(append(append(append([]Claim{}, p.FocusAreas...), p.Regions...), p.ProgramTypes...), p.KnownPartners...)
	if p.ProposalChannel != nil {
		out = append(out, *p.ProposalChannel)
	}
	if p.SeekingPartners != nil {
		out = append(out, *p.SeekingPartners)
	}
	return out
}

func prospectInRegion(p Prospect, region string) bool {
	if p.Profile != nil {
		for _, cl := range p.Profile.Regions {
			if regionMatches(cl.Value, region) || isNationwide(cl.Value) {
				return true
			}
		}
	}
	for _, prog := range p.Programs {
		for _, r := range prog.Regions {
			if regionMatches(r, region) || isNationwide(r) {
				return true
			}
		}
	}
	return regionMatches(p.Company.Region, region) || isNationwide(p.Company.Region)
}

func prospectHasFocus(p Prospect, focus string) bool {
	if p.Profile == nil {
		return false
	}
	values := claimValuesOf(append(append([]Claim{}, p.Profile.FocusAreas...), p.Profile.ProgramTypes...))
	for _, prog := range p.Programs {
		values = append(append(values, prog.FocusAreas...), prog.ProgramTypes...)
	}
	want := concept(focus)
	for _, v := range values {
		if concept(v) == want || strings.Contains(strings.ToLower(v), strings.ToLower(focus)) {
			return true
		}
	}
	return false
}

func claimValuesOf(claims []Claim) []string {
	out := make([]string, 0, len(claims))
	for _, c := range claims {
		out = append(out, c.Value)
	}
	return out
}
