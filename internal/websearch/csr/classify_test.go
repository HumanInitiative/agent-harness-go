package csr

import "testing"

// These cases are real links found on the homepages of seed companies
// (survey of 2026-10-01). "keep" means the link should become a route.
func TestScoreLink_RealHomepageLinks(t *testing.T) {
	cases := []struct {
		text, url string
		keep      bool
		kind      PageKind
	}{
		// CSR program pages.
		{"TJSL", "https://pelindo.co.id/page/tjsl", true, KindCSRProgram},
		{"TJSL", "https://tjsl.kai.id", true, KindCSRProgram}, // signal is in the subdomain
		{"Program TJSL", "https://www.kimiafarma.co.id/id/program-tjsl", true, KindCSRProgram},
		{"Tanggung Jawab Sosial Perusahaan", "https://www.prudential.co.id/id/about-us/csr/", true, KindCSRProgram},
		{"Tanggung Jawab Sosial", "https://mayoraindah.co.id/landing/Tanggung-Jawab-Sosial-12", true, KindCSRProgram},
		{"Pengembangan Sosial dan Kemasyarakatan", "https://mayoraindah.co.id/content/Pengembangan-Sosial-dan-Kemasyarakatan-7", true, KindCSRProgram},
		{"Community Development Programs", "http://www.kpc.co.id/sustainability/community-development-programs/", true, KindCSRProgram},
		{"ESG", "https://www.alamtri.com/pages/view/csr.html", true, KindCSRProgram},
		{"Keberlanjutan", "https://www.kai.id/keberlanjutan", true, KindCSRProgram},
		{"Penciptaan Nilai Sosial", "https://www.sidomuncul.co.id:443/creating_share_value.html", true, KindCSRProgram},
		{"Esg", "https://timah.com/blog/sustainability/sustainbility-strategy.html", true, KindNews}, // under /blog/
		{"Social", "https://www.indikaenergy.co.id/esg/social/", true, KindCSRProgram},
		{"Program CSR", "https://sig.id/program-csr", true, KindCSRProgram},

		// Reports.
		{"Laporan Keberlanjutan", "https://www.kimiafarma.co.id/id/sustainability-report", true, KindReport},
		{"", "https://mind.id/temp/TJSL-Report-MIND-ID-2025.pdf", true, KindReport},
		{"Laporan Tahunan & Keberlanjutan", "https://krakatausteel.com/viewcontent/129", true, KindReport},
		{"Laporan TJSL", "https://ptpp.co.id/id/keberlanjutan/laporan-tjsl", true, KindReport},

		// Foundations.
		{"YAYASAN KARYA BAKTI UT", "https://www.unitedtractors.com/yayasan/yayasan-karya-bakti-ut/", true, KindFoundation},
		{"Yayasan", "https://www.smart-tbk.com/tentang/yayasan/", true, KindFoundation},

		// CSR news.
		{"Tumbuh Bersama Keluarga Pengemudi Melalui Program Beasiswa Bluebird Peduli", "https://www.bluebirdgroup.com/news/tumbuh-bersama-program-beasiswa-bluebird-peduli?lang=en", true, KindNews},

		// False positives seen on the same sites.
		{"Lihat Semua Media Sosial", "https://www.bca.co.id/id/tentang-bca/media-riset/Social-Media", false, ""},
		{"Media Sosial", "https://pnm.co.id/publikasi/media-sosial", false, ""},
		{"Tugas dan Tanggung Jawab Dewan Komisaris", "https://www.indocement.co.id/id/governance/board-of-commissioners-charter/tugas-dan-tanggung-jawab-dewan-komisaris", false, ""},
		{"Komite ESG", "https://www.indocement.co.id/id/company/management/komite-esg", false, ""},
		{"Tanggung Jawab Produk", "https://mayoraindah.co.id/content/Tanggung-Jawab-Produk-9", false, ""},
		{"Bidang Usaha Jasa Manajemen PNM", "https://www.pnm.co.id/bisnis/jasa-manajemen-tjsl", false, ""},
		{"Governance", "https://chandra-asri.com/en/sustainability/governance", false, ""},
		{"Sustainability and Risk Management Governance", "https://www.medcoenergi.com/en/our-sustainability/our-approach/sustainability-and-risk-management-governance", false, ""},
		{"Safety Health & Security", "http://www.kpc.co.id/sustainability/safety-health-security/", false, ""},
		{"Smart Operation & Product Stewardship", "https://timah.com/blog/sustainability/smart-operation-product-stewardship.html", false, ""},
		{"WMI Supply Chain Due Diligence Policy 2025", "https://www.harumenergy.com/cfind/source/files/sustainability/wmi-due-diligence-policy.pdf", false, ""},
		{"Karir", "https://example.co.id/karir", false, ""},
		{"Social", "https://www.alamtri.com/pages/read/9/66/Sosial", false, ""}, // a lone generic word is too weak
	}
	for _, tc := range cases {
		got := ScoreLink(tc.text, tc.url)
		keep := got.Score >= MinLinkScore
		if keep != tc.keep {
			t.Errorf("%q %s: score %.1f, keep=%v, want keep=%v", tc.text, tc.url, got.Score, keep, tc.keep)
			continue
		}
		if tc.keep && got.Kind != tc.kind {
			t.Errorf("%q %s: kind %s, want %s", tc.text, tc.url, got.Kind, tc.kind)
		}
	}
}

func TestScoreLink_PrefersIndonesianVersion(t *testing.T) {
	id := ScoreLink("Keberlanjutan", "https://example.co.id/id/keberlanjutan")
	en := ScoreLink("Sustainability", "https://example.co.id/en/sustainability")
	if id.Score <= en.Score {
		t.Fatalf("Indonesian page should score higher: id=%.1f en=%.1f", id.Score, en.Score)
	}
}

func TestScoreLink_WholeWordsOnly(t *testing.T) {
	// "csr" inside another word, "esg" inside "esgrima": not CSR signals.
	if s := ScoreLink("csrf token", "https://example.com/security/csrf"); s.Score >= MinLinkScore {
		t.Fatalf("csrf matched as csr: %.1f", s.Score)
	}
}
