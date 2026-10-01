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
	hay := words(term)
	for _, g := range focusGroups {
		for _, syn := range g {
			if contains(hay, syn) {
				return g[0]
			}
		}
	}
	return strings.TrimSpace(hay)
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
