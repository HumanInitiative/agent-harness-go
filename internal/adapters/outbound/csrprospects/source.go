// Package csrprospects implements outbound.ProspectSource over the CSR index
// (internal/websearch/csr): it scores the indexed companies against one
// program instead of the institution-wide profile.
package csrprospects

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/outreach"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

// maxScanned bounds how many indexed companies one request scores.
const maxScanned = 1000

// Source finds prospects in a csr.Store.
type Source struct {
	store         *csr.Store
	minConfidence float64
	opts          csr.IndexOptions
}

// New builds a Source. minConfidence is the extraction confidence below
// which a profile is flagged (the institution profile's value); opts sets the
// staleness window and clock.
func New(store *csr.Store, minConfidence float64, opts csr.IndexOptions) *Source {
	return &Source{store: store, minConfidence: minConfidence, opts: opts}
}

// Prospects implements outbound.ProspectSource.
func (s *Source) Prospects(ctx context.Context, p outreach.Program, limit int) (outbound.Prospects, error) {
	if limit < 1 {
		return outbound.Prospects{}, fmt.Errorf("csrprospects: limit must be at least 1, got %d", limit)
	}
	index := csr.NewIndex(s.store, csr.InstitutionProfile{
		FocusAreas: p.FocusAreas, Regions: p.Regions, ProgramTypes: p.ProgramTypes, MinConfidence: s.minConfidence,
	}, s.opts)
	// The index ranks (current before stale, trusted before doubtful, then
	// score), so scoring everything and cutting afterwards keeps its order.
	found, err := index.FindProspects(ctx, csr.ProspectFilter{Limit: maxScanned})
	if err != nil {
		return outbound.Prospects{}, fmt.Errorf("csrprospects: %w", err)
	}
	var out outbound.Prospects
	for _, tc := range index.Profile().Coverage(found) {
		out.Terms = append(out.Terms, outreach.TermCoverage{
			Kind: tc.Kind, Term: tc.Term, Group: tc.Group, Recognized: tc.Recognized, Companies: tc.Companies,
		})
	}
	for _, f := range found {
		if !f.Match.FocusMatch {
			continue
		}
		out.Fitting++
		if len(out.Candidates) < limit {
			out.Candidates = append(out.Candidates, candidate(f))
		}
	}
	return out, nil
}

// FocusGroups implements outbound.ProspectSource.
func (s *Source) FocusGroups() []outreach.FocusGroup {
	groups := csr.FocusGroups()
	out := make([]outreach.FocusGroup, len(groups))
	for i, g := range groups {
		out[i] = outreach.FocusGroup{Name: g.Name, Synonyms: g.Synonyms}
	}
	return out
}

func candidate(f csr.Prospect) outreach.Candidate {
	c := outreach.Candidate{
		CompanyID: f.Company.ID, Name: f.Company.Name, Domain: f.Company.Domain,
		Sector: f.Company.Sector, Region: f.Company.Region,
		Verified: f.Company.Status == csr.StatusVerified,
		Score:    f.Match.Score, Reasons: f.Match.Reasons, LowConfidence: f.Match.LowConfidence,
		ConfirmedAt: latest(f.Freshness.CheckedAt, f.Freshness.ExtractedAt),
		Stale:       f.Freshness.Stale, StaleReason: f.Freshness.Reason,
		HiddenPrograms: f.HiddenPrograms,
	}
	if f.Profile != nil {
		if f.Profile.ProposalChannel != nil {
			c.ProposalChannel = f.Profile.ProposalChannel.Value
		}
		if f.Profile.SeekingPartners != nil {
			c.SeekingPartners = f.Profile.SeekingPartners.Value
		}
	}
	for _, prog := range f.Programs {
		c.Programs = append(c.Programs, outreach.CompanyProgram{
			Name: prog.Name, Description: prog.Description, FocusAreas: prog.FocusAreas, Regions: prog.Regions,
			PeriodStart: string(prog.PeriodStart), PeriodEnd: string(prog.PeriodEnd),
			ProposalDeadline: string(prog.ProposalDeadline), Status: prog.Status, EvidenceIDs: prog.EvidenceIDs,
		})
	}
	ids := make([]int64, 0, len(f.Evidence))
	for id := range f.Evidence {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		e := f.Evidence[id]
		ev := outreach.Evidence{ID: e.ID, URL: e.URL, Title: e.Title, Excerpt: e.Excerpt, Date: e.PublishedAt, DatePublished: !e.PublishedAt.IsZero()}
		if !ev.DatePublished {
			ev.Date = e.FetchedAt
		}
		c.Evidence = append(c.Evidence, ev)
	}
	return c
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
