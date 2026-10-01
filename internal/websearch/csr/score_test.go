package csr

import (
	"strings"
	"testing"
)

const profileYAML = `
institution_profile:
  focus_areas: [pendidikan, pemberdayaan ekonomi, kesehatan]
  regions: [Jawa Barat, DKI Jakarta, Banten]
  program_types: [pelatihan, beasiswa, bantuan sosial]
  min_confidence: 0.6
`

func loadProfile(t *testing.T) InstitutionProfile {
	t.Helper()
	ip, err := LoadInstitutionProfile(strings.NewReader(profileYAML))
	if err != nil {
		t.Fatalf("LoadInstitutionProfile: %v", err)
	}
	return ip
}

func claimsOf(values ...string) []Claim {
	var out []Claim
	for _, v := range values {
		out = append(out, Claim{Value: v, EvidenceIDs: []int64{1}})
	}
	return out
}

func TestLoadInstitutionProfile(t *testing.T) {
	ip := loadProfile(t)
	if len(ip.FocusAreas) != 3 || ip.MinConfidence != 0.6 || ip.Regions[0] != "Jawa Barat" {
		t.Fatalf("unexpected profile: %+v", ip)
	}
	for name, doc := range map[string]string{
		"no focus":       "institution_profile:\n  regions: [Banten]\n",
		"bad confidence": "institution_profile:\n  focus_areas: [x]\n  min_confidence: 2\n",
		"unknown field":  "institution_profile:\n  focus_areas: [x]\n  budget: 5\n",
	} {
		if _, err := LoadInstitutionProfile(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestScore_StrongMatchIsExplained(t *testing.T) {
	ip := loadProfile(t)
	p := &Profile{
		FocusAreas:      claimsOf("Pendidikan anak", "UMKM binaan"),
		Regions:         claimsOf("Kabupaten Bandung, Jabar"),
		ProgramTypes:    claimsOf("beasiswa", "pelatihan wirausaha"),
		SeekingPartners: &Claim{Value: "true", EvidenceIDs: []int64{1}},
		ProposalChannel: &Claim{Value: "csr@contoh.co.id", EvidenceIDs: []int64{1}},
		ModelConfidence: 0.85,
	}
	m := ip.Score(Company{Region: "Nasional"}, p)

	// focus: pendidikan + pemberdayaan ekonomi (2 matches) = 35; region 20;
	// program types 10; seeking 15; channel 10 => 90.
	if m.Score != 90 || m.LowConfidence {
		t.Fatalf("score = %d, reasons:\n%s", m.Score, strings.Join(m.Reasons, "\n"))
	}
	all := strings.Join(m.Reasons, "\n")
	for _, want := range []string{"pendidikan (perusahaan: Pendidikan anak", "pemberdayaan ekonomi (perusahaan: UMKM binaan", "wilayah program cocok: Jawa Barat",
		"jenis program cocok: pelatihan, beasiswa", "membuka kemitraan", "csr@contoh.co.id"} {
		if !strings.Contains(all, want) {
			t.Errorf("reasons should mention %q:\n%s", want, all)
		}
	}
}

func TestScore_RegionEvidenceBeatsSeedLabel(t *testing.T) {
	ip := loadProfile(t)
	nationwideProfile := &Profile{FocusAreas: claimsOf("kesehatan"), Regions: claimsOf("Seluruh Indonesia"), ModelConfidence: 0.9}
	seedOnly := &Profile{FocusAreas: claimsOf("kesehatan"), ModelConfidence: 0.9}

	n := ip.Score(Company{Region: "Kalimantan"}, nationwideProfile)
	s := ip.Score(Company{Region: "Nasional"}, seedOnly)
	if n.Score != 40 || s.Score != 30 {
		t.Fatalf("nationwide-on-page=%d (want 40), seed-label-only=%d (want 30)", n.Score, s.Score)
	}
	if !strings.Contains(strings.Join(s.Reasons, " "), "belum terbukti") {
		t.Fatalf("a seed-only region must be flagged as unproven: %v", s.Reasons)
	}
}

func TestScore_NoMatchLowConfidenceAndMissingProfile(t *testing.T) {
	ip := loadProfile(t)
	m := ip.Score(Company{Region: "Papua"}, &Profile{FocusAreas: claimsOf("olahraga"), ModelConfidence: 0.3})
	if m.Score != 0 || !m.LowConfidence || len(m.Reasons) != 2 {
		t.Fatalf("unexpected match: %+v", m)
	}
	if m := ip.Score(Company{}, nil); m.Score != 0 || !strings.Contains(m.Reasons[0], "belum ada profil") {
		t.Fatalf("missing profile: %+v", m)
	}
}

func TestConceptAndRegionNormalization(t *testing.T) {
	cases := map[string]string{
		"Beasiswa Prestasi": "pendidikan", "Stunting": "kesehatan", "pengembangan UMKM": "pemberdayaan ekonomi",
		"Penanaman mangrove": "lingkungan", "Tanggap darurat bencana": "kebencanaan", "olahraga": "olahraga",
	}
	for in, want := range cases {
		if got := concept(in); got != want {
			t.Errorf("concept(%q) = %q, want %q", in, got, want)
		}
	}
	for _, tc := range [][2]string{
		{"Kota Bandung, Jabar", "Jawa Barat"},
		{"Jakarta Selatan", "DKI Jakarta"},
		{"DKI Jakarta", "DKI Jakarta"},
		{"DKI Jakarta", "Jakarta"},
		{"Yogyakarta", "DI Yogyakarta"},
	} {
		if !regionMatches(tc[0], tc[1]) {
			t.Errorf("regionMatches(%q, %q) = false, want true", tc[0], tc[1])
		}
	}
	if regionMatches("Jawa Timur", "Jawa Barat") {
		t.Error("different provinces must not match")
	}
}
