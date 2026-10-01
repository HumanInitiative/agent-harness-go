package csr

import (
	"slices"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"PT Bank Rakyat Indonesia (Persero) Tbk": "bank rakyat indonesia",
		"PT. Bank Mandiri, Tbk.":                 "bank mandiri",
		"pt bank mandiri tbk":                    "bank mandiri",
		"PT Astra Agro Lestari Tbk":              "astra agro lestari",
		"PT Indah Kiat Pulp and Paper Tbk":       "indah kiat pulp and paper",
		"  PT  Kalbe   Farma Tbk ":               "kalbe farma",
	}
	for in, want := range cases {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
	// Distinct companies stay distinct: parent vs. subsidiary is not merged.
	if NormalizeName("PT Astra International Tbk") == NormalizeName("PT Astra Agro Lestari Tbk") {
		t.Error("different companies normalized to the same key")
	}
}

func TestBrandTokens(t *testing.T) {
	cases := map[string][]string{
		"PT Bank Rakyat Indonesia (Persero) Tbk":  {"rakyat", "bri"},
		"PT Perusahaan Listrik Negara (Persero)":  {"listrik", "negara", "pln"},
		"PT Telkom Indonesia (Persero) Tbk":       {"telkom", "ti"},
		"PT Sarana Multi Infrastruktur (Persero)": {"sarana", "multi", "infrastruktur", "smi"},
		"PT Kalbe Farma Tbk":                      {"kalbe", "farma", "kf"},
	}
	for name, want := range cases {
		got := BrandTokens(name)
		for _, w := range want {
			if len(w) >= 3 && !slices.Contains(got, w) {
				t.Errorf("BrandTokens(%q) = %v, missing %q", name, got, w)
			}
		}
		for _, g := range got {
			if g == "indonesia" || g == "bank" || len(g) < 3 {
				t.Errorf("BrandTokens(%q) contains non-distinctive token %q", name, g)
			}
		}
	}
}

func TestNormalizeDomain(t *testing.T) {
	cases := map[string]string{
		"bri.co.id":                    "bri.co.id",
		"https://www.BRI.co.id/id/csr": "bri.co.id",
		"http://pertamina.com:443":     "pertamina.com",
		"":                             "",
		"not a domain":                 "",
		"localhost":                    "",
		"-bad-.com":                    "",
	}
	for in, want := range cases {
		if got := NormalizeDomain(in); got != want {
			t.Errorf("NormalizeDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOnDomain(t *testing.T) {
	if !OnDomain("www.bri.co.id", "bri.co.id") || !OnDomain("ir.bri.co.id", "bri.co.id") {
		t.Error("expected apex and subdomains to match")
	}
	if OnDomain("notbri.co.id", "bri.co.id") || OnDomain("bri.co.id.evil.com", "bri.co.id") || OnDomain("x.com", "") {
		t.Error("unrelated hosts must not match")
	}
}

func TestCanonicalURL(t *testing.T) {
	cases := map[string]string{
		"https://WWW.Allianz.co.id:443/program/csr.html#top": "https://www.allianz.co.id/program/csr.html",
		"https://example.com/csr/?utm_source=x&page=2":       "https://example.com/csr/?page=2",
		"https://gudanggaramtbk.com/csr/":                    "https://gudanggaramtbk.com/csr/",
		"https://example.com":                                "https://example.com/",
		"https://example.com:8443/x":                         "https://example.com:8443/x",
		"mailto:csr@example.com":                             "",
		"javascript:void(0)":                                 "",
	}
	for in, want := range cases {
		if got := CanonicalURL(in); got != want {
			t.Errorf("CanonicalURL(%q) = %q, want %q", in, got, want)
		}
	}
}
