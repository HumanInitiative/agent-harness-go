package csr

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// InstitutionProfile describes the institution looking for funding, so each
// company can be scored by how well its CSR work fits.
type InstitutionProfile struct {
	FocusAreas   []string `yaml:"focus_areas"`
	Regions      []string `yaml:"regions"`
	ProgramTypes []string `yaml:"program_types"`
	// MinConfidence is the extraction confidence below which a profile is
	// shown with a warning rather than trusted.
	MinConfidence float64 `yaml:"min_confidence"`
}

// LoadInstitutionProfile reads the YAML format:
//
//	institution_profile:
//	  focus_areas: [pendidikan, kesehatan]
//	  regions: [Jawa Barat]
//	  program_types: [beasiswa]
//	  min_confidence: 0.6
func LoadInstitutionProfile(r io.Reader) (InstitutionProfile, error) {
	var doc struct {
		Profile InstitutionProfile `yaml:"institution_profile"`
	}
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return InstitutionProfile{}, fmt.Errorf("csr: read institution profile: %w", err)
	}
	p := doc.Profile
	if len(p.FocusAreas) == 0 {
		return InstitutionProfile{}, errors.New("csr: institution profile needs at least one focus area")
	}
	if p.MinConfidence < 0 || p.MinConfidence > 1 {
		return InstitutionProfile{}, fmt.Errorf("csr: min_confidence %v is outside 0..1", p.MinConfidence)
	}
	return p, nil
}

// focusGroups map the many ways pages describe a field to one concept, so
// "beasiswa" on a company page matches "pendidikan" in the institution
// profile. The first entry names the group.
var focusGroups = [][]string{
	{"pendidikan", "education", "beasiswa", "scholarship", "sekolah", "school", "literasi", "literacy", "guru", "teacher"},
	{"kesehatan", "health", "gizi", "nutrition", "stunting", "medis", "medical", "posyandu", "puskesmas"},
	{"pemberdayaan ekonomi", "ekonomi", "economic", "umkm", "msme", "ukm", "kewirausahaan", "entrepreneur", "livelihood", "usaha mikro", "koperasi"},
	{"lingkungan", "environment", "penghijauan", "reboisasi", "mangrove", "konservasi", "conservation", "sampah", "waste", "iklim", "climate"},
	{"kebencanaan", "bencana", "disaster", "tanggap darurat", "kemanusiaan", "humanitarian", "relief"},
	{"air bersih", "sanitasi", "sanitation", "water", "wash"},
	{"infrastruktur", "infrastructure", "fasilitas umum", "rumah ibadah"},
	{"pelatihan", "training", "keterampilan", "skill", "vokasi", "vocational"},
	{"bantuan sosial", "bansos", "santunan", "sembako", "charity", "donasi", "donation"},
}

// regionAliases maps common abbreviations to province names.
var regionAliases = map[string]string{
	"jabar": "jawa barat", "jateng": "jawa tengah", "jatim": "jawa timur", "jakarta": "dki jakarta",
	"diy": "di yogyakarta", "yogyakarta": "di yogyakarta", "jogja": "di yogyakarta",
	"sumut": "sumatera utara", "sumbar": "sumatera barat", "sumsel": "sumatera selatan",
	"kaltim": "kalimantan timur", "kalbar": "kalimantan barat", "kalsel": "kalimantan selatan",
	"sulsel": "sulawesi selatan", "ntb": "nusa tenggara barat", "ntt": "nusa tenggara timur",
}

var nationwide = []string{"nasional", "national", "nationwide", "seluruh indonesia", "indonesia"}

// concept reduces a focus or program term to its group name, or to itself.
func concept(term string) string {
	c, _ := focusGroup(term)
	return c
}

// focusGroup reduces a term to its group name and says whether it belongs to
// a known group. An unknown term stays itself and only matches a company
// that uses the same words.
func focusGroup(term string) (string, bool) {
	hay := words(term)
	for _, g := range focusGroups {
		for _, syn := range g {
			if contains(hay, syn) {
				return g[0], true
			}
		}
	}
	return strings.TrimSpace(hay), false
}

// FocusGroup is a field of work the scoring understands, with the other
// words that count as the same field.
type FocusGroup struct {
	Name     string
	Synonyms []string
}

// FocusGroups lists the fields of work the scoring understands.
func FocusGroups() []FocusGroup {
	out := make([]FocusGroup, 0, len(focusGroups))
	for _, g := range focusGroups {
		out = append(out, FocusGroup{Name: g[0], Synonyms: append([]string(nil), g[1:]...)})
	}
	return out
}

// Kinds of term a program is described by.
const (
	TermFocus  = "focus"
	TermRegion = "region"
	TermType   = "type"
)

// TermCount says how well one term of the institution profile is covered by
// the indexed companies, so a typo or a field nobody in the index works in
// shows up instead of silently giving an empty shortlist.
type TermCount struct {
	Kind string
	Term string
	// Group is the known field the term maps to, for focus areas and
	// program types; empty when Recognized is false.
	Group string
	// Recognized is false for a focus area or program type that is not a
	// known field: it then only matches companies that use the same words.
	// Regions are free text and always count as recognized.
	Recognized bool
	// Companies is how many of the given prospects work in this field, in
	// this region, or offer this type of program.
	Companies int
}

// Coverage counts, for every term of the profile, the prospects whose
// extracted profile matches it. Prospects without a profile match nothing.
func (ip InstitutionProfile) Coverage(prospects []Prospect) []TermCount {
	var out []TermCount
	count := func(kind, term string, matches func(*Profile) bool) {
		group, known := focusGroup(term)
		tc := TermCount{Kind: kind, Term: term, Recognized: true}
		if kind != TermRegion {
			tc.Recognized = known
			if known {
				tc.Group = group
			}
		}
		for _, p := range prospects {
			if p.Profile != nil && matches(p.Profile) {
				tc.Companies++
			}
		}
		out = append(out, tc)
	}
	for _, t := range ip.FocusAreas {
		c := concept(t)
		count(TermFocus, t, func(p *Profile) bool {
			return anyClaimConcept(p.FocusAreas, c) || anyClaimConcept(p.ProgramTypes, c)
		})
	}
	for _, t := range ip.Regions {
		count(TermRegion, t, func(p *Profile) bool {
			for _, cl := range p.Regions {
				if regionMatches(cl.Value, t) || isNationwide(cl.Value) {
					return true
				}
			}
			return false
		})
	}
	for _, t := range ip.ProgramTypes {
		c := concept(t)
		count(TermType, t, func(p *Profile) bool { return anyClaimConcept(p.ProgramTypes, c) })
	}
	return out
}

func anyClaimConcept(claims []Claim, c string) bool {
	for _, cl := range claims {
		if concept(cl.Value) == c {
			return true
		}
	}
	return false
}

// normalizeRegion lowercases a region and expands abbreviations word by
// word, so "Kabupaten Bandung, Jabar" becomes "kabupaten bandung jawa barat"
// and "Jakarta Selatan" becomes "dki jakarta selatan".
func normalizeRegion(r string) string {
	tokens := strings.Fields(words(r))
	for i, t := range tokens {
		if alias, ok := regionAliases[t]; ok {
			if i > 0 && strings.HasSuffix(alias, " "+t) && tokens[i-1]+" "+t == alias {
				continue // already the full name, e.g. "dki jakarta"
			}
			tokens[i] = alias
		}
	}
	return strings.Join(tokens, " ")
}

func regionMatches(companyRegion, wanted string) bool {
	c, w := words(normalizeRegion(companyRegion)), normalizeRegion(wanted)
	return w != "" && contains(c, w)
}

func isNationwide(region string) bool {
	hay := words(region)
	for _, n := range nationwide {
		if strings.TrimSpace(hay) == n || contains(hay, "seluruh indonesia") {
			return true
		}
	}
	return false
}

// Match is an explainable fit score.
type Match struct {
	// Score is 0..100.
	Score int
	// Reasons lists, in Indonesian for the people reading results, why the
	// score is what it is — including what lowers trust in it.
	Reasons []string
	// LowConfidence is set when the extraction confidence is below the
	// institution's minimum.
	LowConfidence bool
	// FocusMatch is set when at least one of the institution's focus areas
	// is among the company's. A company can score on region or on having a
	// proposal channel alone; callers that need a relevant company (a
	// shortlist for one program) require this too.
	FocusMatch bool
}

// Score rates how well a company's CSR profile fits the institution. The
// score is a sum of explained parts; nothing contributes without a reason.
func (ip InstitutionProfile) Score(c Company, p *Profile) Match {
	if p == nil {
		return Match{Reasons: []string{"belum ada profil CSR yang terekstraksi dari halaman perusahaan"}}
	}
	var m Match
	add := func(points int, reason string) {
		m.Score += points
		m.Reasons = append(m.Reasons, reason)
	}

	// Focus areas (program types count too: "beasiswa" is education work).
	companyConcepts := map[string][]string{}
	for _, cl := range append(append([]Claim{}, p.FocusAreas...), p.ProgramTypes...) {
		k := concept(cl.Value)
		companyConcepts[k] = append(companyConcepts[k], cl.Value)
	}
	var matched []string
	for _, f := range ip.FocusAreas {
		if vals, ok := companyConcepts[concept(f)]; ok {
			matched = append(matched, fmt.Sprintf("%s (perusahaan: %s)", f, strings.Join(dedupe(vals), ", ")))
		}
	}
	if len(matched) > 0 {
		m.FocusMatch = true
		add(min(45, 25+10*(len(matched)-1)), "bidang fokus cocok: "+strings.Join(matched, "; "))
	}

	// Regions: proven by the pages beats the seed's general label.
	var regionHits []string
	nation := false
	for _, cl := range p.Regions {
		if isNationwide(cl.Value) {
			nation = true
		}
		for _, w := range ip.Regions {
			if regionMatches(cl.Value, w) {
				regionHits = append(regionHits, w)
			}
		}
	}
	switch {
	case len(regionHits) > 0:
		add(20, "wilayah program cocok: "+strings.Join(dedupe(regionHits), ", "))
	case nation:
		add(15, "program disebut berskala nasional")
	case isNationwide(c.Region) || anyRegionMatches(c.Region, ip.Regions):
		add(5, fmt.Sprintf("wilayah dari data awal (%s), belum terbukti di halaman CSR", c.Region))
	}

	// Program types.
	var typeHits []string
	for _, t := range ip.ProgramTypes {
		for _, cl := range p.ProgramTypes {
			if concept(cl.Value) == concept(t) {
				typeHits = append(typeHits, t)
				break
			}
		}
	}
	if len(typeHits) > 0 {
		add(10, "jenis program cocok: "+strings.Join(typeHits, ", "))
	}

	if p.SeekingPartners != nil && p.SeekingPartners.Value == "true" {
		add(15, "halaman menyebut perusahaan membuka kemitraan atau menerima proposal")
	}
	if p.ProposalChannel != nil {
		add(10, "tersedia kanal resmi pengajuan proposal: "+p.ProposalChannel.Value)
	}

	m.Score = min(m.Score, 100)
	if len(m.Reasons) == 0 {
		m.Reasons = append(m.Reasons, "tidak ada kecocokan dengan bidang fokus, wilayah, atau jenis program lembaga")
	}
	if p.ModelConfidence < ip.MinConfidence {
		m.LowConfidence = true
		m.Reasons = append(m.Reasons, fmt.Sprintf("perhatian: keyakinan ekstraksi %.2f di bawah ambang %.2f", p.ModelConfidence, ip.MinConfidence))
	}
	return m
}

// scorePrograms adjusts a profile's score by the company's programs: an
// active program in the institution's fields or regions is the strongest
// sign of a current opportunity, and a company whose recorded programs
// have all ended is a weaker prospect than its profile alone suggests.
func (ip InstitutionProfile) scorePrograms(m *Match, active []Program, total int) {
	focus := map[string]bool{}
	for _, f := range append(append([]string{}, ip.FocusAreas...), ip.ProgramTypes...) {
		focus[concept(f)] = true
	}
	var relevant []string
	for _, p := range active {
		fits := false
		for _, v := range append(append([]string{}, p.FocusAreas...), p.ProgramTypes...) {
			fits = fits || focus[concept(v)]
		}
		for _, r := range p.Regions {
			fits = fits || anyRegionMatches(r, ip.Regions)
		}
		if fits {
			relevant = append(relevant, p.Name)
		}
	}
	switch {
	case len(relevant) > 0:
		m.Score = min(m.Score+10, 100)
		m.Reasons = append(m.Reasons, "program aktif yang relevan: "+strings.Join(dedupe(relevant), ", "))
	case total > 0 && len(active) == 0:
		m.Score = max(m.Score-10, 0)
		m.Reasons = append(m.Reasons, "semua program yang tercatat sudah berakhir atau tidak terlihat lagi di halaman sumber")
	}
}

func anyRegionMatches(region string, wanted []string) bool {
	for _, w := range wanted {
		if regionMatches(region, w) {
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if k := strings.ToLower(s); !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
