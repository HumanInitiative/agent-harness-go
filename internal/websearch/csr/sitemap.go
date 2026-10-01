package csr

import (
	"encoding/xml"
	"sort"
	"strings"
)

// maxSitemapURLs bounds how many URLs are read from one sitemap.
const maxSitemapURLs = 20000

type sitemapDoc struct {
	XMLName xml.Name
	URLs    []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

// parseSitemap reads a sitemap (<urlset>) or sitemap index (<sitemapindex>)
// and returns page URLs or child sitemap URLs respectively.
func parseSitemap(doc string) (pages, children []string, ok bool) {
	var s sitemapDoc
	dec := xml.NewDecoder(strings.NewReader(doc))
	dec.Strict = false
	if err := dec.Decode(&s); err != nil {
		return nil, nil, false
	}
	for i, u := range s.URLs {
		if i >= maxSitemapURLs {
			break
		}
		if loc := strings.TrimSpace(u.Loc); loc != "" {
			pages = append(pages, loc)
		}
	}
	for i, sm := range s.Sitemaps {
		if i >= maxSitemapURLs {
			break
		}
		if loc := strings.TrimSpace(sm.Loc); loc != "" {
			children = append(children, loc)
		}
	}
	return pages, children, s.XMLName.Local == "urlset" || s.XMLName.Local == "sitemapindex"
}

// rankChildSitemaps orders the children of a sitemap index so the ones most
// likely to list CSR pages ("page", "csr", "sustainability" sitemaps) are
// read first; post and product sitemaps of large sites come last.
func rankChildSitemaps(children []string) []string {
	score := func(u string) float64 {
		s := ScoreLink("", u).Score
		l := strings.ToLower(u)
		if strings.Contains(l, "page") {
			s += 2
		}
		if strings.Contains(l, "product") || strings.Contains(l, "produk") || strings.Contains(l, "post") {
			s -= 2
		}
		return s
	}
	out := append([]string(nil), children...)
	sort.SliceStable(out, func(i, j int) bool { return score(out[i]) > score(out[j]) })
	return out
}
