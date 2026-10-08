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
	CheckCompany(ctx context.Context, name string, includeInactive bool) ([]csr.Prospect, error)
}

const (
	maxFilterChars        = 100
	maxProspectQueryChars = 200
	defaultProspects      = 5
	maxProspects          = 20
	maxExcerptInOutput    = 240
	maxCheckedCompanies   = 3
	// Programs listed per company: enough to judge, short enough for many
	// companies in one answer.
	programsPerProspect = 5
	programsPerCompany  = 10
)

// dateLayout is how dates appear in tool output.
const dateLayout = "2006-01-02"

// indexFirstGuidance tells the model where CSR answers come from. It ends
// both tools' descriptions.
const indexFirstGuidance = " Answer CSR prospect questions from this index first, and always state each company's " +
	"\"Data per\" date and the evidence fetch dates. Use web_search or web_fetch only when the index has no answer, " +
	"and then say clearly that the information is not yet verified in the CSR index."

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
	return "Lists companies whose CSR/TJSL programs fit the institution's focus, from a local index that a daily " +
		"crawl of company websites and reports keeps current. Each result has a fit score with reasons, the " +
		"extraction confidence, its human review status, dates (when the data was extracted and last confirmed), " +
		"its programs with their status and periods, and evidence URLs with excerpts. Only active programs are " +
		"shown unless include_inactive is true. Always cite the evidence URLs, say when a company has not been " +
		"verified by a person yet, and warn when its data is marked stale." + indexFirstGuidance
}

func (t *FindCSRProspectsTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sector": map[string]any{"type": "string", "description": "Optional sector filter, e.g. perbankan, energi, tambang."},
			"region": map[string]any{"type": "string", "description": "Optional region filter, e.g. Jawa Barat."},
			"focus":  map[string]any{"type": "string", "description": "Optional program focus, e.g. pendidikan, kesehatan."},
			"query": map[string]any{"type": "string", "description": "Optional free text matched against program names, " +
				"descriptions and regions and against evidence, e.g. \"beasiswa Banten\" or \"air bersih\"."},
			"include_inactive": map[string]any{"type": "boolean", "description": "Also show expired, stale and inactive " +
				"programs, e.g. to see a company's history. Defaults to false."},
			"limit": map[string]any{"type": "integer", "description": fmt.Sprintf("Number of companies, 1-%d. Defaults to %d.", maxProspects, defaultProspects)},
		},
	}
}

type findProspectsArgs struct {
	Sector          string `json:"sector"`
	Region          string `json:"region"`
	Focus           string `json:"focus"`
	Query           string `json:"query"`
	IncludeInactive bool   `json:"include_inactive"`
	Limit           int    `json:"limit"`
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
	if utf8.RuneCountInString(in.Query) > maxProspectQueryChars {
		return "", fmt.Errorf("find_csr_prospects: query must be at most %d characters", maxProspectQueryChars)
	}
	if in.Limit <= 0 {
		in.Limit = defaultProspects
	}
	in.Limit = min(in.Limit, maxProspects)

	prospects, err := t.index.FindProspects(ctx, csr.ProspectFilter{
		Sector: strings.TrimSpace(in.Sector), Region: strings.TrimSpace(in.Region), Focus: strings.TrimSpace(in.Focus),
		Query: strings.TrimSpace(in.Query), IncludeInactive: in.IncludeInactive, Limit: in.Limit,
	})
	if err != nil {
		return "", fmt.Errorf("find_csr_prospects: %w", err)
	}
	if len(prospects) == 0 {
		hint := ""
		if !in.IncludeInactive {
			hint = " Retrying with include_inactive=true also searches expired and inactive programs."
		}
		return "No companies in the local CSR index match these filters yet. The index only contains companies " +
			"that have been crawled and have evidence-backed CSR profiles." + hint + " If you search the web instead, " +
			"say that those findings are not yet verified in the CSR index.", nil
	}

	var sb strings.Builder
	for i, p := range prospects {
		writeProspect(&sb, i+1, p, false, programsPerProspect)
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
	return "Looks up one company in the local CSR index: its CSR pages, extracted CSR profile and programs with " +
		"evidence and dates, and fit with the institution. Only active programs are shown unless include_inactive " +
		"is true. Only the local index is searched; for companies not in it, web_search and web_fetch may be used." +
		indexFirstGuidance
}

func (t *CheckCompanyTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "Company name, e.g. \"Bank Rakyat Indonesia\"."},
			"include_inactive": map[string]any{"type": "boolean", "description": "Also show expired, stale and inactive " +
				"programs. Defaults to false."},
		},
		"required": []string{"name"},
	}
}

func (t *CheckCompanyTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Name            string `json:"name"`
		IncludeInactive bool   `json:"include_inactive"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("check_company: invalid arguments: %w", err)
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > maxFilterChars {
		return "", fmt.Errorf("check_company: name must be 1-%d characters", maxFilterChars)
	}

	prospects, err := t.index.CheckCompany(ctx, in.Name, in.IncludeInactive)
	if err != nil {
		return "", fmt.Errorf("check_company: %w", err)
	}
	if len(prospects) == 0 {
		return fmt.Sprintf("%q is not in the local CSR index. You may look it up with web_search and web_fetch, "+
			"but say that those findings are not yet verified in the CSR index.", in.Name), nil
	}
	if len(prospects) > maxCheckedCompanies {
		prospects = prospects[:maxCheckedCompanies]
	}
	var sb strings.Builder
	for i, p := range prospects {
		writeProspect(&sb, i+1, p, true, programsPerCompany)
	}
	return websearch.Wrap("csr_index", t.now(), sb.String()), nil
}

// writeProspect renders one company with its claims and programs numbered
// against a per-company evidence list, so every statement can be traced to
// a URL and a date.
func writeProspect(sb *strings.Builder, n int, p csr.Prospect, withRoutes bool, maxPrograms int) {
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
	if p.Profile != nil {
		fmt.Fprintf(sb, "   Data per: %s (terakhir dibaca dari sumber) | sumber terakhir dicek: %s\n",
			date(p.Freshness.ExtractedAt), date(p.Freshness.CheckedAt))
	}
	if p.Freshness.Stale {
		fmt.Fprintf(sb, "   PERHATIAN: data mungkin sudah usang — %s. Jangan sajikan sebagai informasi terkini.\n", p.Freshness.Reason)
	}
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
		writePrograms(sb, p, maxPrograms, ref)
		if len(cited) > 0 {
			sb.WriteString("   Bukti:\n")
			for i, e := range cited {
				fmt.Fprintf(sb, "   [%d] %s (diambil %s) — \"%s\"\n", i+1, e.URL, date(e.FetchedAt), shorten(e.Excerpt, maxExcerptInOutput))
			}
		}
	}
	if withRoutes {
		var routes []string
		for _, r := range p.Routes {
			if r.State == csr.PageActive || r.State == csr.PagePinned {
				routes = append(routes, fmt.Sprintf("%s (%s, dicek %s)", r.URL, r.Kind, date(r.LastCheckedAt)))
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

// programStatusLabel translates a program status for the people reading
// answers.
var programStatusLabel = map[string]string{
	csr.ProgramActive:   "aktif",
	csr.ProgramExpired:  "sudah berakhir",
	csr.ProgramStale:    "tidak terlihat di pembacaan terakhir",
	csr.ProgramInactive: "tidak aktif lagi",
}

func writePrograms(sb *strings.Builder, p csr.Prospect, max int, ref func([]int64) string) {
	if len(p.Programs) > 0 {
		sb.WriteString("   Program:\n")
	}
	for i, prog := range p.Programs {
		if i == max {
			fmt.Fprintf(sb, "   … dan %d program lain (gunakan check_company untuk daftar lengkap)\n", len(p.Programs)-max)
			break
		}
		facts := []string{programStatusLabel[prog.Status]}
		if period := programPeriod(prog); period != "" {
			facts = append(facts, "periode "+period)
		}
		if prog.ProposalDeadline != "" {
			facts = append(facts, "batas proposal "+string(prog.ProposalDeadline))
		}
		fmt.Fprintf(sb, "   - %s [%s] %s", prog.Name, strings.Join(facts, "; "), ref(prog.EvidenceIDs))
		var details []string
		if prog.Description != "" {
			details = append(details, strings.TrimSuffix(prog.Description, "."))
		}
		if len(prog.FocusAreas) > 0 {
			details = append(details, "fokus: "+strings.Join(prog.FocusAreas, ", "))
		}
		if len(prog.Regions) > 0 {
			details = append(details, "wilayah: "+strings.Join(prog.Regions, ", "))
		}
		if len(details) > 0 {
			sb.WriteString(" — " + shorten(strings.Join(details, "; "), maxExcerptInOutput))
		}
		sb.WriteString("\n")
	}
	if p.HiddenPrograms > 0 {
		fmt.Fprintf(sb, "   (%d program yang sudah berakhir atau tidak aktif disembunyikan; gunakan include_inactive=true untuk melihatnya)\n", p.HiddenPrograms)
	}
}

func programPeriod(p csr.Program) string {
	switch {
	case p.PeriodStart == "" && p.PeriodEnd == "":
		return ""
	case p.PeriodEnd == "" || p.PeriodStart == p.PeriodEnd:
		if p.PeriodStart == "" {
			return "s.d. " + string(p.PeriodEnd)
		}
		return string(p.PeriodStart)
	case p.PeriodStart == "":
		return "s.d. " + string(p.PeriodEnd)
	}
	return string(p.PeriodStart) + " s.d. " + string(p.PeriodEnd)
}

func date(t time.Time) string {
	if t.IsZero() {
		return "belum pernah"
	}
	return t.UTC().Format(dateLayout)
}

func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
