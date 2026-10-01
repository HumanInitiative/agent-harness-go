package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

// ProspectIndex is the part of csr.Index the CSR tools need.
type ProspectIndex interface {
	FindProspects(ctx context.Context, f csr.ProspectFilter) ([]csr.Prospect, error)
	CheckCompany(ctx context.Context, name string) ([]csr.Prospect, error)
}

const (
	maxFilterChars      = 100
	defaultProspects    = 5
	maxProspects        = 20
	maxExcerptInOutput  = 240
	maxCheckedCompanies = 3
)

// FindCSRProspectsTool lists companies whose CSR programs fit the
// institution, from the local index built by the crawler.
type FindCSRProspectsTool struct {
	index ProspectIndex
	now   func() time.Time
}

// NewFindCSRProspectsTool builds the tool. now may be nil (time.Now).
func NewFindCSRProspectsTool(index ProspectIndex, now func() time.Time) *FindCSRProspectsTool {
	if now == nil {
		now = time.Now
	}
	return &FindCSRProspectsTool{index: index, now: now}
}

func (t *FindCSRProspectsTool) Name() string { return "find_csr_prospects" }

func (t *FindCSRProspectsTool) Description() string {
	return "Lists companies whose CSR/TJSL programs fit the institution's focus, from a local index built " +
		"by crawling company websites. Each result has a fit score with reasons, the extraction confidence, " +
		"its human review status, and evidence URLs with excerpts. Always cite those evidence URLs, and say " +
		"when a company has not been verified by a person yet."
}

func (t *FindCSRProspectsTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sector": map[string]any{"type": "string", "description": "Optional sector filter, e.g. perbankan, energi, tambang."},
			"region": map[string]any{"type": "string", "description": "Optional region filter, e.g. Jawa Barat."},
			"focus":  map[string]any{"type": "string", "description": "Optional program focus, e.g. pendidikan, kesehatan."},
			"limit":  map[string]any{"type": "integer", "description": fmt.Sprintf("Number of companies, 1-%d. Defaults to %d.", maxProspects, defaultProspects)},
		},
	}
}

type findProspectsArgs struct {
	Sector string `json:"sector"`
	Region string `json:"region"`
	Focus  string `json:"focus"`
	Limit  int    `json:"limit"`
}

func (t *FindCSRProspectsTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in findProspectsArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return "", fmt.Errorf("find_csr_prospects: invalid arguments: %w", err)
		}
	}
	for name, v := range map[string]string{"sector": in.Sector, "region": in.Region, "focus": in.Focus} {
		if utf8.RuneCountInString(v) > maxFilterChars {
			return "", fmt.Errorf("find_csr_prospects: %s must be at most %d characters", name, maxFilterChars)
		}
	}
	if in.Limit <= 0 {
		in.Limit = defaultProspects
	}
	in.Limit = min(in.Limit, maxProspects)

	prospects, err := t.index.FindProspects(ctx, csr.ProspectFilter{
		Sector: strings.TrimSpace(in.Sector), Region: strings.TrimSpace(in.Region), Focus: strings.TrimSpace(in.Focus), Limit: in.Limit,
	})
	if err != nil {
		return "", fmt.Errorf("find_csr_prospects: %w", err)
	}
	if len(prospects) == 0 {
		return "No companies in the local CSR index match these filters yet. The index only contains companies " +
			"that have been crawled and have evidence-backed CSR profiles.", nil
	}

	var sb strings.Builder
	for i, p := range prospects {
		writeProspect(&sb, i+1, p, false)
	}
	return websearch.Wrap("csr_index", t.now(), sb.String()), nil
}

// CheckCompanyTool reports what the local index knows about one company.
type CheckCompanyTool struct {
	index ProspectIndex
	now   func() time.Time
}

// NewCheckCompanyTool builds the tool. now may be nil (time.Now).
func NewCheckCompanyTool(index ProspectIndex, now func() time.Time) *CheckCompanyTool {
	if now == nil {
		now = time.Now
	}
	return &CheckCompanyTool{index: index, now: now}
}

func (t *CheckCompanyTool) Name() string { return "check_company" }

func (t *CheckCompanyTool) Description() string {
	return "Looks up one company in the local CSR index: its CSR pages, extracted CSR profile with evidence, " +
		"and fit with the institution. Only the local index is searched; for companies not in it, use " +
		"web_search and web_fetch instead."
}

func (t *CheckCompanyTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "Company name, e.g. \"Bank Rakyat Indonesia\"."},
		},
		"required": []string{"name"},
	}
}

func (t *CheckCompanyTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("check_company: invalid arguments: %w", err)
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > maxFilterChars {
		return "", fmt.Errorf("check_company: name must be 1-%d characters", maxFilterChars)
	}

	prospects, err := t.index.CheckCompany(ctx, in.Name)
	if err != nil {
		return "", fmt.Errorf("check_company: %w", err)
	}
	if len(prospects) == 0 {
		return fmt.Sprintf("%q is not in the local CSR index. Use web_search and web_fetch to look it up directly.", in.Name), nil
	}
	if len(prospects) > maxCheckedCompanies {
		prospects = prospects[:maxCheckedCompanies]
	}
	var sb strings.Builder
	for i, p := range prospects {
		writeProspect(&sb, i+1, p, true)
	}
	return websearch.Wrap("csr_index", t.now(), sb.String()), nil
}

// writeProspect renders one company with its claims numbered against a
// per-company evidence list, so every statement can be traced to a URL.
func writeProspect(sb *strings.Builder, n int, p csr.Prospect, withRoutes bool) {
	c := p.Company
	fmt.Fprintf(sb, "%d. %s", n, c.Name)
	var meta []string
	if c.Sector != "" {
		meta = append(meta, "sektor "+c.Sector)
	}
	if c.Region != "" {
		meta = append(meta, "wilayah (data awal) "+c.Region)
	}
	if c.Domain != "" {
		meta = append(meta, "domain "+c.Domain+" ("+c.DomainStatus+")")
	}
	if len(meta) > 0 {
		sb.WriteString(" — " + strings.Join(meta, ", "))
	}
	sb.WriteString("\n")

	review := "belum diverifikasi manusia"
	switch c.Status {
	case csr.StatusVerified:
		review = "sudah diverifikasi manusia"
	case csr.StatusExcluded:
		review = "dikecualikan"
	}
	fmt.Fprintf(sb, "   Skor kecocokan: %d/100 | keyakinan ekstraksi: %.2f | status: %s\n", p.Match.Score, c.Confidence, review)
	fmt.Fprintf(sb, "   Alasan: %s\n", strings.Join(p.Match.Reasons, "; "))

	if p.Profile != nil {
		numbers := map[int64]int{}
		var cited []csr.Evidence
		ref := func(ids []int64) string {
			var refs []string
			for _, id := range ids {
				e, ok := p.Evidence[id]
				if !ok {
					continue
				}
				if _, seen := numbers[id]; !seen {
					cited = append(cited, e)
					numbers[id] = len(cited)
				}
				refs = append(refs, fmt.Sprintf("[%d]", numbers[id]))
			}
			return strings.Join(refs, "")
		}
		line := func(label string, claims []csr.Claim) {
			if len(claims) == 0 {
				return
			}
			var parts []string
			for _, cl := range claims {
				parts = append(parts, cl.Value+" "+ref(cl.EvidenceIDs))
			}
			fmt.Fprintf(sb, "   %s: %s\n", label, strings.Join(parts, ", "))
		}
		line("Fokus", p.Profile.FocusAreas)
		line("Wilayah program", p.Profile.Regions)
		line("Jenis program", p.Profile.ProgramTypes)
		line("Mitra", p.Profile.KnownPartners)
		if p.Profile.ProposalChannel != nil {
			fmt.Fprintf(sb, "   Kanal proposal: %s %s\n", p.Profile.ProposalChannel.Value, ref(p.Profile.ProposalChannel.EvidenceIDs))
		}
		if p.Profile.SeekingPartners != nil && p.Profile.SeekingPartners.Value == "true" {
			fmt.Fprintf(sb, "   Membuka kemitraan: ya %s\n", ref(p.Profile.SeekingPartners.EvidenceIDs))
		}
		if len(cited) > 0 {
			sb.WriteString("   Bukti:\n")
			for i, e := range cited {
				fmt.Fprintf(sb, "   [%d] %s — \"%s\"\n", i+1, e.URL, shorten(e.Excerpt, maxExcerptInOutput))
			}
		}
	}
	if withRoutes {
		var routes []string
		for _, r := range p.Routes {
			if r.State == csr.PageActive || r.State == csr.PagePinned {
				routes = append(routes, fmt.Sprintf("%s (%s)", r.URL, r.Kind))
			}
		}
		if len(routes) > 0 {
			fmt.Fprintf(sb, "   Halaman CSR yang tercatat: %s\n", strings.Join(routes, ", "))
		} else {
			sb.WriteString("   Belum ada halaman CSR yang tercatat untuk perusahaan ini.\n")
		}
	} else if c.CSRURL != "" {
		fmt.Fprintf(sb, "   Halaman CSR utama: %s\n", c.CSRURL)
	}
	sb.WriteString("\n")
}

func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
