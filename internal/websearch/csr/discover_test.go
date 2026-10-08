package csr

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

const discoveryYAML = `
discovery:
  queries:
    - "program CSR {fokus} {wilayah} {tahun}"
    - "call for proposal CSR {tahun}"
    - "CSR sektor {sektor}"
  results_per_query: 5
  queries_per_run: 2
`

func TestDiscoveryConfig_ExpandsAndValidates(t *testing.T) {
	cfg, err := LoadDiscoveryConfig(strings.NewReader(discoveryYAML))
	if err != nil {
		t.Fatal(err)
	}
	ip := InstitutionProfile{FocusAreas: []string{"pendidikan", "kesehatan"}, Regions: []string{"Banten"}}
	got := cfg.ExpandQueries(ip, t0)
	want := []string{"program CSR pendidikan Banten 2026", "program CSR kesehatan Banten 2026", "call for proposal CSR 2026"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got) // {sektor} has no values: that template is skipped
	}
	if cfg.PagesPerRun != 30 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if _, err := LoadDiscoveryConfig(strings.NewReader("discovery:\n  queries: [\"CSR {kota}\"]\n")); err == nil {
		t.Fatal("unknown placeholder must be rejected")
	}
}

type discoveryFixture struct {
	store *Store
	web   *fakeWeb
	llm   *scriptedExtractor
	d     *Discoverer
}

func newDiscoveryFixture(t *testing.T, replies ...string) discoveryFixture {
	t.Helper()
	s := newTestStore(t)
	web := newFakeWeb()
	llm := &scriptedExtractor{replies: replies}
	cfg, err := LoadDiscoveryConfig(strings.NewReader(discoveryYAML))
	if err != nil {
		t.Fatal(err)
	}
	ip := InstitutionProfile{FocusAreas: []string{"pendidikan"}, Regions: []string{"Banten"}}
	d := NewDiscoverer(s, web, web, NewSignalExtractor(llm, quietLogger()), cfg, ip, func() time.Time { return t0 }, quietLogger())
	return discoveryFixture{store: s, web: web, llm: llm, d: d}
}

const newsArticle = "PT Contoh Tambang Tbk menyalurkan program beasiswa pendidikan kepada 200 penerima manfaat di Banten. " +
	"Program CSR ini merupakan bagian dari tanggung jawab sosial perusahaan dan pemberdayaan masyarakat. " +
	"Bank BRI juga memberikan bantuan beasiswa dan pelatihan UMKM bagi warga desa binaan di Serang. " +
	"Pemerintah Provinsi Banten mengapresiasi program tanggung jawab sosial tersebut."

// TestAcceptance_DiscoveryAddsUnverifiedCompaniesForTheCrawl is the Phase 3
// acceptance test: search results become verified signals and new
// companies that the regular crawl picks up, never verified automatically.
func TestAcceptance_DiscoveryAddsUnverifiedCompaniesForTheCrawl(t *testing.T) {
	reply := `{"signals": [
	  {"company": "PT Contoh Tambang Tbk", "program": "beasiswa pendidikan di Banten", "excerpt": "PT Contoh Tambang Tbk menyalurkan program beasiswa pendidikan kepada 200 penerima manfaat di Banten."},
	  {"company": "Bank BRI", "program": "beasiswa dan pelatihan UMKM", "excerpt": "Bank BRI juga memberikan bantuan beasiswa dan pelatihan UMKM bagi warga desa binaan di Serang."},
	  {"company": "PT Karangan Tbk", "program": "air bersih", "excerpt": "PT Karangan Tbk membangun sumur air bersih di Lebak."},
	  {"company": "PT Lain Sekali", "program": "beasiswa", "excerpt": "Bank BRI juga memberikan bantuan beasiswa dan pelatihan UMKM bagi warga desa binaan di Serang."}
	]}`
	f := newDiscoveryFixture(t, reply)
	ctx := context.Background()
	seedID := mustInsert(t, f.store, Company{Name: "PT Bank Rakyat Indonesia (Persero) Tbk", Domain: "bri.co.id"})

	f.web.search["program CSR pendidikan Banten 2026"] = []websearch.Result{
		{URL: "https://berita.example/csr-banten?utm_source=x"},
		{URL: "https://berita.example/saham-naik"},
		{URL: "https://www.facebook.com/somepage"},
	}
	f.web.pages["https://berita.example/csr-banten"] = websearch.Page{Title: "Perusahaan salurkan CSR di Banten", Content: newsArticle}
	f.web.pages["https://berita.example/saham-naik"] = websearch.Page{Title: "Saham naik", Content: strings.Repeat("Harga saham perusahaan naik hari ini. ", 20)}

	report, err := f.d.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Queries != 2 || report.PagesRead != 2 || report.PagesToModel != 1 || f.llm.calls != 1 {
		t.Fatalf("expected two queries, two pages read and only the CSR article sent to the model: %+v calls=%d", report, f.llm.calls)
	}
	// Kept: the mining company (new) and Bank BRI (new, but resembling the
	// seeded BRI). Dropped: an excerpt not on the page, and an excerpt that
	// does not name its company.
	if report.Signals != 2 || report.NewCompanies != 2 || report.Flagged != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	for _, c := range f.web.fetched {
		if strings.Contains(c, "facebook.com") {
			t.Fatal("social media must not be read")
		}
	}

	mining, err := f.store.CompanyByNormalizedName(ctx, NormalizeName("PT Contoh Tambang Tbk"))
	if err != nil {
		t.Fatal(err)
	}
	if mining.Source != SourceSignal || mining.Status != StatusNew || mining.ReviewNote != "" {
		t.Fatalf("a discovered company is new and from a signal: %+v", mining)
	}
	sigs, _ := f.store.Signals(ctx, mining.ID)
	if len(sigs) != 1 || sigs[0].URL != "https://berita.example/csr-banten" || sigs[0].Host != "berita.example" ||
		sigs[0].Query != "program CSR pendidikan Banten 2026" || !strings.Contains(sigs[0].Excerpt, "200 penerima manfaat") {
		t.Fatalf("signal not recorded with its source: %+v", sigs)
	}
	bri, _ := f.store.CompanyByNormalizedName(ctx, NormalizeName("Bank BRI"))
	if !strings.Contains(bri.ReviewNote, "PT Bank Rakyat Indonesia (Persero) Tbk") || !strings.Contains(bri.ReviewNote, "(id 1)") {
		t.Fatalf("a similar name must be flagged for review, not merged: %+v", bri)
	}
	if seed, _ := f.store.Company(ctx, seedID); seed.ReviewNote != "" || seed.Source != SourceSeed {
		t.Fatalf("the seeded company must be left alone: %+v", seed)
	}

	// The regular crawl picks the new companies up: they are due now.
	due, _ := f.store.CompaniesDue(ctx, t0, 10)
	if len(due) != 3 {
		t.Fatalf("new companies must be due for the crawl: %d due", len(due))
	}

	// The next run moves on to the other queries and never rereads a page.
	f.web.search["call for proposal CSR 2026"] = []websearch.Result{{URL: "https://berita.example/csr-banten"}}
	second, err := f.d.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.Queries != 2 || second.PagesRead != 0 || f.llm.calls != 1 {
		t.Fatalf("a read page must not be read again: %+v calls=%d", second, f.llm.calls)
	}

	// Nothing is ever verified automatically.
	all, _ := f.store.ListCompanies(ctx, "", 50)
	for _, c := range all {
		if c.Status == StatusVerified {
			t.Fatalf("discovery must never verify a company: %+v", c)
		}
	}
}

func TestDiscovery_SearchBlockedStopsTheRunAndModelOutageKeepsThePage(t *testing.T) {
	f := newDiscoveryFixture(t, `{"signals": []}`)
	ctx := context.Background()
	f.web.search["program CSR pendidikan Banten 2026"] = []websearch.Result{{URL: "https://berita.example/csr-banten"}}
	f.web.pages["https://berita.example/csr-banten"] = websearch.Page{Title: "CSR", Content: newsArticle}
	f.llm.err = errors.New("model unavailable")

	report, err := f.d.RunOnce(ctx)
	if err != nil || len(report.Errors) != 1 {
		t.Fatalf("expected one page error: %+v, %v", report, err)
	}
	if read, _ := f.store.PageRead(ctx, "https://berita.example/csr-banten"); read {
		t.Fatal("a page the model could not read must be tried again later")
	}
}

func TestSimilarNames(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Bank BRI", "PT Bank Rakyat Indonesia (Persero) Tbk", true},
		{"PT Indofood Sukses Makmur Tbk", "PT Indofood CBP Sukses Makmur Tbk", true},
		{"Astra International", "PT Astra Agro Lestari Tbk", true},
		{"PLN", "PT Perusahaan Listrik Negara (Persero)", true},
		{"PT Contoh Tambang Tbk", "PT Bank Rakyat Indonesia (Persero) Tbk", false},
		{"Bank Mandiri", "Bank Central Asia", false},
	}
	for _, c := range cases {
		if got := similarNames(c.a, c.b); got != c.want {
			t.Errorf("similarNames(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestShippedDiscoveryConfigLoads(t *testing.T) {
	f, err := os.Open("../../../config/discovery.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := LoadDiscoveryConfig(f)
	if err != nil {
		t.Fatalf("config/discovery.yaml: %v", err)
	}
	ip := InstitutionProfile{FocusAreas: []string{"pendidikan", "kesehatan"}, Regions: []string{"Banten", "Jawa Barat"}}
	if n := len(cfg.ExpandQueries(ip, t0)); n < 10 {
		t.Fatalf("expected the templates to expand into many queries, got %d", n)
	}
}
