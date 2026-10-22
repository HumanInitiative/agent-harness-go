package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

// demoCompany describes one invented company of the demo index.
type demoCompany struct {
	name, sector, region  string
	focus, regions, types []string
	channel               string
	seeking               bool
	confidence            float64
	verified              bool
	// routed companies have a CSR page that was checked just now, so their
	// data is current; the others are stale.
	routed bool
	// age is how long ago the company's pages were read: everything about
	// it is dated that far back, so that its data looks as old as it is.
	age      time.Duration
	program  csr.Program
	title    string
	url      string
	excerpts []string
}

// offsetClock is the store's clock: the real time moved by an offset, so the
// seed can write records "as of" some time ago and the rest of the demo then
// runs on the real time.
type offsetClock struct{ offset time.Duration }

func (c *offsetClock) now() time.Time { return time.Now().Add(c.offset) }

var demoCompanies = []demoCompany{
	{
		name: "PT Terang Energi Tbk", sector: "energi", region: "Nasional",
		focus: []string{"pendidikan"}, regions: []string{"Jawa Barat", "Banten"}, types: []string{"beasiswa", "pelatihan"},
		channel: "csr@terangenergi.example", seeking: true, confidence: 0.85, verified: true, routed: true,
		program: csr.Program{Name: "Beasiswa Terang 2026", FocusAreas: []string{"pendidikan"}, Regions: []string{"Jawa Barat", "Banten"},
			ProgramTypes: []string{"beasiswa"}, PeriodStart: "2026", PeriodEnd: "2026-12", ProposalDeadline: "2026-12"},
		title: "Program TJSL Terang Energi", url: "https://terangenergi.example/csr/beasiswa",
		excerpts: []string{
			"Beasiswa Terang 2026 menyediakan 300 beasiswa untuk siswa SMA di Jawa Barat dan Banten. Proposal kemitraan diterima hingga Desember 2026.",
			"Proposal kemitraan dikirim ke csr@terangenergi.example.",
		},
	},
	{
		name: "PT Nusa Perbankan Tbk", sector: "perbankan", region: "Nasional",
		focus: []string{"pendidikan", "pemberdayaan ekonomi"}, regions: []string{"Seluruh Indonesia"}, types: []string{"pelatihan"},
		channel: "csr@nusaperbankan.example", seeking: true, confidence: 0.7, routed: true,
		program: csr.Program{Name: "Literasi Keuangan Desa", FocusAreas: []string{"pendidikan", "pemberdayaan ekonomi"},
			Regions: []string{"Seluruh Indonesia"}, ProgramTypes: []string{"pelatihan"}, PeriodStart: "2026", PeriodEnd: "2026-12"},
		title: "Tanggung Jawab Sosial Nusa Perbankan", url: "https://nusaperbankan.example/tjsl/literasi",
		excerpts: []string{
			"Literasi Keuangan Desa melatih 1.200 pelaku usaha mikro dan guru di 40 desa di seluruh Indonesia selama 2026.",
			"Proposal kemitraan dikirim ke csr@nusaperbankan.example.",
		},
	},
	{
		name: "PT Sehat Farma", sector: "kesehatan", region: "Jawa Barat",
		focus: []string{"kesehatan"}, regions: []string{"Jawa Barat"}, types: []string{"bantuan sosial"},
		confidence: 0.8, routed: true,
		program: csr.Program{Name: "Posyandu Sehat", FocusAreas: []string{"kesehatan"}, Regions: []string{"Jawa Barat"},
			ProgramTypes: []string{"bantuan sosial"}, PeriodStart: "2026", PeriodEnd: "2026-12"},
		title: "Posyandu Sehat", url: "https://sehatfarma.example/csr",
		excerpts: []string{"Posyandu Sehat memberikan pemeriksaan gizi gratis untuk balita di Jawa Barat sepanjang 2026."},
	},
	{
		name: "PT Tirta Jaya Utama", sector: "air minum", region: "Banten",
		focus: []string{"air bersih"}, regions: []string{"Banten"}, types: []string{"infrastruktur"},
		channel: "csr@tirtajaya.example", seeking: true, confidence: 0.75, routed: true,
		program: csr.Program{Name: "Air untuk Dusun", FocusAreas: []string{"air bersih"}, Regions: []string{"Banten"},
			ProgramTypes: []string{"infrastruktur"}, PeriodStart: "2026", PeriodEnd: "2026-12"},
		title: "Air untuk Dusun", url: "https://tirtajaya.example/csr/air-untuk-dusun",
		excerpts: []string{
			"Program Air untuk Dusun membangun 25 sumur dan jaringan air bersih di dusun yang kesulitan air di Banten pada 2026.",
			"Proposal kemitraan dikirim ke csr@tirtajaya.example.",
		},
	},
	{
		// Fits an education program, but nothing has confirmed its data
		// lately and its only program has ended: stale, so ranked last.
		name: "PT Lama Sekali Tbk", sector: "pertambangan", region: "Kalimantan",
		focus: []string{"pendidikan"}, regions: []string{"Jawa Barat"}, types: []string{"beasiswa"},
		confidence: 0.6, routed: false, age: 200 * 24 * time.Hour,
		program: csr.Program{Name: "Sekolah Tambang 2023", FocusAreas: []string{"pendidikan"}, Regions: []string{"Jawa Barat"},
			ProgramTypes: []string{"beasiswa"}, PeriodStart: "2023", PeriodEnd: "2023-12"},
		title: "Sekolah Tambang", url: "https://lamasekali.example/csr",
		excerpts: []string{"Sekolah Tambang 2023 membangun tiga ruang kelas dan memberi beasiswa di Jawa Barat."},
	},
	{
		name: "PT Kebun Sumatera", sector: "perkebunan", region: "Sumatera",
		focus: []string{"lingkungan"}, regions: []string{"Sumatera"}, types: []string{"penghijauan"},
		confidence: 0.8, routed: true,
		program: csr.Program{Name: "Mangrove Pesisir", FocusAreas: []string{"lingkungan"}, Regions: []string{"Sumatera"},
			ProgramTypes: []string{"penghijauan"}, PeriodStart: "2026", PeriodEnd: "2026-12"},
		title: "Mangrove Pesisir", url: "https://kebunsumatera.example/csr",
		excerpts: []string{"Program Mangrove Pesisir menanam 10.000 pohon mangrove di pesisir Sumatera pada 2026."},
	},
}

// seedDemoIndex fills an empty store with the invented companies. The same
// calls the crawler makes (UpsertCompany, SaveExtraction, UpsertPage,
// RecordPageCheck) are used, so the index looks like a real one.
func seedDemoIndex(ctx context.Context, store *csr.Store, clock *offsetClock) error {
	defer func() { clock.offset = 0 }()
	for _, c := range demoCompanies {
		clock.offset = -c.age
		now := clock.now()
		id, _, err := store.UpsertCompany(ctx, csr.Company{Name: c.name, Sector: c.sector, Region: c.region, Source: csr.SourceSeed})
		if err != nil {
			return fmt.Errorf("seed %s: %w", c.name, err)
		}

		fetched := now.AddDate(0, 0, -3)
		published := now.AddDate(0, -1, 0)
		evidence := make([]csr.Evidence, len(c.excerpts))
		for i, text := range c.excerpts {
			evidence[i] = csr.Evidence{URL: c.url, Title: c.title, Excerpt: text, Kind: csr.KindCSRProgram, FetchedAt: fetched}
			if i == 0 {
				evidence[i].PublishedAt = published
			}
		}
		claims := func(values []string) []csr.Claim {
			out := make([]csr.Claim, len(values))
			for i, v := range values {
				out[i] = csr.Claim{Value: v, EvidenceIDs: []int64{0}}
			}
			return out
		}
		profile := csr.Profile{
			FocusAreas: claims(c.focus), Regions: claims(c.regions), ProgramTypes: claims(c.types), ModelConfidence: c.confidence,
		}
		if c.channel != "" {
			profile.ProposalChannel = &csr.Claim{Value: c.channel, EvidenceIDs: []int64{int64(len(c.excerpts) - 1)}}
		}
		if c.seeking {
			profile.SeekingPartners = &csr.Claim{Value: "true", EvidenceIDs: []int64{0}}
		}
		program := c.program
		program.EvidenceIDs = []int64{0}
		if err := store.SaveExtraction(ctx, id, csr.Extraction{
			HasCSRContent: true, Evidence: evidence, Profile: profile, Programs: []csr.Program{program}, ReadURLs: []string{c.url},
		}); err != nil {
			return fmt.Errorf("seed %s: %w", c.name, err)
		}

		if c.routed {
			pageID, err := store.UpsertPage(ctx, csr.Page{CompanyID: id, URL: c.url, Kind: csr.KindCSRProgram, DiscoveredVia: csr.ViaHomepage, Score: 5})
			if err != nil {
				return fmt.Errorf("seed %s: %w", c.name, err)
			}
			if err := store.RecordPageCheck(ctx, pageID, 200, csr.PageActive, "demo", 0, now.Add(7*24*time.Hour)); err != nil {
				return fmt.Errorf("seed %s: %w", c.name, err)
			}
		}
		// What a finished crawl records: the best CSR page and how sure the
		// extraction was; the index ranks equal scores by it.
		if err := store.MarkCrawled(ctx, id, c.url, c.confidence, now.Add(7*24*time.Hour)); err != nil {
			return fmt.Errorf("seed %s: %w", c.name, err)
		}
		if c.verified {
			if err := store.SetStatus(ctx, id, csr.StatusVerified); err != nil {
				return fmt.Errorf("seed %s: %w", c.name, err)
			}
		}
	}
	return nil
}

// demoCompanyNames lists the invented companies, for the banner.
func demoCompanyNames() string {
	names := make([]string, len(demoCompanies))
	for i, c := range demoCompanies {
		names[i] = c.name
	}
	return strings.Join(names, ", ")
}
