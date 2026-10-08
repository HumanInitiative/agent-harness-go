package csr

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// legalTokens are parts of Indonesian company names that say nothing about
// which company it is: "PT Bank Rakyat Indonesia (Persero) Tbk" and "Bank
// Rakyat Indonesia" are the same company.
var legalTokens = map[string]bool{
	"pt": true, "tbk": true, "persero": true, "perseroan": true, "terbatas": true,
	"cv": true, "inc": true, "ltd": true, "corp": true, "co": true,
}

// genericTokens appear in many company names, so matching on them alone
// (e.g. a domain containing "indonesia") proves nothing.
var genericTokens = map[string]bool{
	"bank": true, "indonesia": true, "indonesian": true, "group": true, "grup": true,
	"international": true, "internasional": true, "the": true, "and": true, "dan": true,
	"perusahaan": true, "industri": true, "industry": true, "nusantara": true,
	"energi": true, "energy": true, "mineral": true, "sejahtera": true, "sukses": true,
	"makmur": true, "jaya": true, "abadi": true, "global": true,
	"tbk": true, "holding": true, "resources": true, "corporation": true, "company": true,
	// Shared by many unrelated companies: "syariah" matched a different
	// bank's domain during a live crawl.
	"syariah": true, "motor": true,
}

// NormalizeName reduces a company name to a comparison key: lowercase, no
// punctuation, no legal-form words. Used for de-duplication.
func NormalizeName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	var kept []string
	for _, tok := range strings.Fields(b.String()) {
		if !legalTokens[tok] {
			kept = append(kept, tok)
		}
	}
	return strings.Join(kept, " ")
}

// BrandTokens returns the strings a company's own domain is likely to
// contain: its distinctive words and the acronym of its name. "Bank Rakyat
// Indonesia" yields "rakyat" and "bri"; "Perusahaan Listrik Negara" yields
// "listrik", "negara" and "pln".
func BrandTokens(name string) []string {
	words := strings.Fields(NormalizeName(name))
	var tokens []string
	seen := map[string]bool{}
	add := func(t string) {
		if len(t) >= 3 && !seen[t] {
			seen[t] = true
			tokens = append(tokens, t)
		}
	}
	for _, w := range words {
		if !genericTokens[w] && len(w) >= 4 {
			add(w)
		}
	}
	if len(words) >= 2 {
		var acronym strings.Builder
		for _, w := range words {
			acronym.WriteByte(w[0])
		}
		add(acronym.String())
	}
	return tokens
}

// NormalizeDomain turns "https://www.Example.co.id/path" or "example.co.id"
// into "example.co.id". It returns "" for anything that is not a plausible
// host name.
func NormalizeDomain(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(u.Hostname(), "www.")
	if !domainPattern.MatchString(host) {
		return ""
	}
	return host
}

var domainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// HostKey is a host without "www." and in lowercase, for comparisons.
func HostKey(host string) string {
	return strings.TrimPrefix(strings.ToLower(host), "www.")
}

// OnDomain reports whether host belongs to domain itself or a subdomain of it.
func OnDomain(host, domain string) bool {
	host, domain = HostKey(host), HostKey(domain)
	return domain != "" && (host == domain || strings.HasSuffix(host, "."+domain))
}

// trackingParams are query parameters that change the URL but not the page.
var trackingParams = []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "fbclid", "gclid"}

// CanonicalURL normalizes a URL so the same page is stored once: lowercase
// scheme and host, no default port, no fragment, no tracking parameters. It
// returns "" for non-http(s) URLs.
//
// The path is kept exactly, trailing slash included: servers may treat
// "/csr/" and "/csr" as different resources (one seed company's "/csr/"
// works while "/csr" is a 404).
func CanonicalURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && !(u.Scheme == "http" && port == "80") && !(u.Scheme == "https" && port == "443") {
		host += ":" + port
	}
	u.Host = host
	u.Fragment = ""
	u.RawFragment = ""
	if u.RawQuery != "" {
		q := u.Query()
		for _, p := range trackingParams {
			q.Del(p)
		}
		u.RawQuery = q.Encode()
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

// DocumentKey identifies a document regardless of how its URL was written:
// with or without "www.", over http or https, with or without tracking
// parameters. A "#page=N" fragment is kept, since evidence from a report
// cites its page. Used to store each route and each excerpt once, whichever
// source (seed crawl, discovery, on-demand lookup) found it. It returns ""
// for non-http(s) URLs.
func DocumentKey(raw string) string {
	canonical := CanonicalURL(raw)
	if canonical == "" {
		return ""
	}
	u, _ := url.Parse(canonical)
	u.Scheme = "https"
	u.Host = HostKey(u.Host)
	if orig, err := url.Parse(strings.TrimSpace(raw)); err == nil && strings.HasPrefix(orig.Fragment, "page=") {
		u.Fragment = orig.Fragment
	}
	return u.String()
}
