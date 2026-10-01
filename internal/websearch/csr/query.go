package csr

import (
	"context"
	"errors"
	"sort"
	"strings"
)

// Prospect is a company with everything needed to judge and cite it.
type Prospect struct {
	Company Company
	// Profile is nil when nothing has been extracted yet.
	Profile *Profile
	// Evidence holds every excerpt the profile's claims cite, by ID.
	Evidence map[int64]Evidence
	Routes   []Page
	Match    Match
}

// ProspectFilter narrows FindProspects. Empty fields mean "any".
type ProspectFilter struct {
	Sector string
	Region string
	Focus  string
	Limit  int
}

// FindProspects returns companies whose CSR profile fits the filter, best
// match first. Only companies with an evidence-backed profile are
// considered: without evidence there is nothing to show.
func FindProspects(ctx context.Context, store *Store, ip InstitutionProfile, f ProspectFilter) ([]Prospect, error) {
	if f.Limit <= 0 {
		f.Limit = 10
	}
	companies, err := store.ProfiledCompanies(ctx, f.Sector, 1000)
	if err != nil {
		return nil, err
	}

	var out []Prospect
	for _, c := range companies {
		p, err := loadProspect(ctx, store, ip, c)
		if err != nil {
			return nil, err
		}
		if f.Region != "" && !prospectInRegion(p, f.Region) {
			continue
		}
		if f.Focus != "" && !prospectHasFocus(p, f.Focus) {
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Match.LowConfidence != out[j].Match.LowConfidence {
			return !out[i].Match.LowConfidence
		}
		if out[i].Match.Score != out[j].Match.Score {
			return out[i].Match.Score > out[j].Match.Score
		}
		return out[i].Company.Confidence > out[j].Company.Confidence
	})
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// CheckCompany looks a company up by name in the local index (exact
// normalized name first, then partial matches). It returns no prospects,
// not an error, when the company is unknown.
func CheckCompany(ctx context.Context, store *Store, ip InstitutionProfile, name string) ([]Prospect, error) {
	var companies []Company
	if c, err := store.CompanyByNormalizedName(ctx, NormalizeName(name)); err == nil {
		companies = []Company{c}
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	} else if companies, err = store.FindCompanies(ctx, name, 3); err != nil {
		return nil, err
	}
	out := make([]Prospect, 0, len(companies))
	for _, c := range companies {
		p, err := loadProspect(ctx, store, ip, c)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func loadProspect(ctx context.Context, store *Store, ip InstitutionProfile, c Company) (Prospect, error) {
	p := Prospect{Company: c, Evidence: map[int64]Evidence{}}
	routes, err := store.Pages(ctx, c.ID)
	if err != nil {
		return p, err
	}
	p.Routes = routes

	profile, err := store.Profile(ctx, c.ID)
	if errors.Is(err, ErrNotFound) {
		p.Match = ip.Score(c, nil)
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p.Profile = &profile
	var ids []int64
	for _, cl := range allClaims(profile) {
		ids = append(ids, cl.EvidenceIDs...)
	}
	evidence, err := store.Evidence(ctx, ids)
	if err != nil {
		return p, err
	}
	for _, e := range evidence {
		p.Evidence[e.ID] = e
	}
	p.Match = ip.Score(c, &profile)
	return p, nil
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
	return regionMatches(p.Company.Region, region) || isNationwide(p.Company.Region)
}

func prospectHasFocus(p Prospect, focus string) bool {
	if p.Profile == nil {
		return false
	}
	want := concept(focus)
	for _, cl := range append(append([]Claim{}, p.Profile.FocusAreas...), p.Profile.ProgramTypes...) {
		if concept(cl.Value) == want || strings.Contains(strings.ToLower(cl.Value), strings.ToLower(focus)) {
			return true
		}
	}
	return false
}

// Index answers prospect questions for one institution profile; it is what
// the harness tools talk to.
type Index struct {
	store   *Store
	profile InstitutionProfile
}

// NewIndex builds an Index over store, scoring against profile.
func NewIndex(store *Store, profile InstitutionProfile) *Index {
	return &Index{store: store, profile: profile}
}

// FindProspects runs FindProspects against the index.
func (i *Index) FindProspects(ctx context.Context, f ProspectFilter) ([]Prospect, error) {
	return FindProspects(ctx, i.store, i.profile, f)
}

// CheckCompany runs CheckCompany against the index.
func (i *Index) CheckCompany(ctx context.Context, name string) ([]Prospect, error) {
	return CheckCompany(ctx, i.store, i.profile, name)
}
