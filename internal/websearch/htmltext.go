package websearch

import (
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	// maxLinks caps how many links are kept per page; a mega-menu should
	// not blow up memory or downstream prompts.
	maxLinks = 500
	// minMainChars is how much text an <article>/<main> must hold to be
	// trusted as the page's main content; below it, an empty or decorative
	// <main> would hide the real content, so the whole <body> is used.
	minMainChars = 200
)

// skipped elements never contribute to Content: scripts and styling, page
// chrome (navigation, header, footer, sidebars) and interactive widgets.
var skipped = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true,
	atom.Nav: true, atom.Header: true, atom.Footer: true, atom.Aside: true,
	atom.Form: true, atom.Button: true, atom.Select: true, atom.Input: true,
	atom.Textarea: true, atom.Svg: true, atom.Canvas: true, atom.Iframe: true,
	atom.Object: true, atom.Embed: true, atom.Head: true,
}

var (
	spaceRun   = regexp.MustCompile(`[ \t\r\f\v\x{00a0}]+`)
	blankLines = regexp.MustCompile(`\n{3,}`)
)

// extractHTML parses an HTML document and returns its title, its main
// content as light markdown, and every link on the page.
func extractHTML(r io.Reader, base *url.URL) (title, content string, links []Link, err error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", "", nil, err
	}

	title = findTitle(doc)
	links = collectLinks(doc, base)

	root := pickMainRoot(doc)
	w := &mdWriter{base: base}
	w.walk(root)
	content = blankLines.ReplaceAllString(strings.TrimSpace(w.sb.String()), "\n\n")
	if title == "" {
		if h1 := findFirst(doc, atom.H1); h1 != nil {
			title = collapse(textOf(h1))
		}
	}
	return title, content, links, nil
}

func pickMainRoot(doc *html.Node) *html.Node {
	for _, a := range []atom.Atom{atom.Article, atom.Main} {
		if n := findFirst(doc, a); n != nil && len(collapse(textOf(n))) >= minMainChars {
			return n
		}
	}
	if n := findByAttr(doc, "role", "main"); n != nil && len(collapse(textOf(n))) >= minMainChars {
		return n
	}
	if body := findFirst(doc, atom.Body); body != nil {
		return body
	}
	return doc
}

func findTitle(doc *html.Node) string {
	if t := findFirst(doc, atom.Title); t != nil {
		if s := collapse(textOf(t)); s != "" {
			return s
		}
	}
	var og string
	walk(doc, func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.DataAtom == atom.Meta && attr(n, "property") == "og:title" {
			og = collapse(attr(n, "content"))
			return false
		}
		return true
	})
	return og
}

func collectLinks(doc *html.Node, base *url.URL) []Link {
	seen := map[string]bool{}
	var links []Link
	walk(doc, func(n *html.Node) bool {
		if len(links) >= maxLinks {
			return false
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.A {
			if u := resolveHref(base, attr(n, "href")); u != "" && !seen[u] {
				seen[u] = true
				text := collapse(textOf(n))
				if text == "" {
					text = collapse(attr(n, "title") + " " + attr(n, "aria-label"))
				}
				links = append(links, Link{Text: text, URL: u})
			}
		}
		return true
	})
	return links
}

// resolveHref turns an href into an absolute http(s) URL without fragment,
// or "" for anything else (mailto:, javascript:, tel:, malformed).
func resolveHref(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

// mdWriter renders a DOM subtree as light markdown: headings, paragraphs,
// lists, links, preformatted text and simple tables.
type mdWriter struct {
	sb       strings.Builder
	base     *url.URL
	inPre    int
	listNums []int // one entry per open list; -1 for unordered
}

func (w *mdWriter) block() { w.sb.WriteString("\n\n") }

func (w *mdWriter) walk(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		if w.inPre > 0 {
			w.sb.WriteString(n.Data)
		} else {
			w.sb.WriteString(spaceRun.ReplaceAllString(strings.ReplaceAll(n.Data, "\n", " "), " "))
		}
		return
	case html.ElementNode:
		if skipped[n.DataAtom] || hidden(n) {
			return
		}
	case html.DocumentNode:
	default:
		return
	}

	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		level := int(n.Data[1] - '0')
		if text := collapse(textOf(n)); text != "" {
			w.block()
			w.sb.WriteString(strings.Repeat("#", level) + " " + text)
			w.block()
		}
		return
	case atom.P, atom.Div, atom.Section, atom.Article, atom.Main, atom.Blockquote, atom.Figure:
		w.block()
		w.children(n)
		w.block()
		return
	case atom.Br:
		w.sb.WriteString("\n")
		return
	case atom.Ul, atom.Ol:
		start := -1
		if n.DataAtom == atom.Ol {
			start = 1
		}
		w.listNums = append(w.listNums, start)
		w.block()
		w.children(n)
		w.listNums = w.listNums[:len(w.listNums)-1]
		w.block()
		return
	case atom.Li:
		w.listItem(n)
		return
	case atom.A:
		text := collapse(textOf(n))
		if u := resolveHref(w.base, attr(n, "href")); u != "" && text != "" {
			w.sb.WriteString("[" + text + "](" + u + ")")
		} else {
			w.sb.WriteString(text)
		}
		return
	case atom.Pre:
		w.block()
		w.sb.WriteString("```\n")
		w.inPre++
		w.children(n)
		w.inPre--
		w.sb.WriteString("\n```")
		w.block()
		return
	case atom.Table:
		w.table(n)
		return
	case atom.Img:
		return
	}
	w.children(n)
}

func (w *mdWriter) children(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c)
	}
}

func (w *mdWriter) listItem(n *html.Node) {
	depth := len(w.listNums)
	marker := "- "
	if depth > 0 && w.listNums[depth-1] > 0 {
		marker = strconv.Itoa(w.listNums[depth-1]) + ". "
		w.listNums[depth-1]++
	}
	indent := ""
	if depth > 1 {
		indent = strings.Repeat("  ", depth-1)
	}
	inner := &mdWriter{base: w.base, listNums: w.listNums}
	inner.children(n)
	text := strings.TrimSpace(blankLines.ReplaceAllString(inner.sb.String(), "\n"))
	if text == "" {
		return
	}
	w.sb.WriteString("\n" + indent + marker + strings.ReplaceAll(text, "\n\n", "\n"))
}

func (w *mdWriter) table(n *html.Node) {
	var rows [][]string
	walk(n, func(c *html.Node) bool {
		if c.Type == html.ElementNode && c.DataAtom == atom.Tr {
			var cells []string
			for cell := c.FirstChild; cell != nil; cell = cell.NextSibling {
				if cell.Type == html.ElementNode && (cell.DataAtom == atom.Td || cell.DataAtom == atom.Th) {
					cells = append(cells, strings.ReplaceAll(collapse(textOf(cell)), "|", "\\|"))
				}
			}
			if len(cells) > 0 {
				rows = append(rows, cells)
			}
			return false
		}
		return true
	})
	if len(rows) == 0 {
		return
	}
	w.block()
	for i, row := range rows {
		w.sb.WriteString("| " + strings.Join(row, " | ") + " |\n")
		if i == 0 {
			sep := make([]string, len(row))
			for j := range sep {
				sep[j] = "---"
			}
			w.sb.WriteString("| " + strings.Join(sep, " | ") + " |\n")
		}
	}
	w.block()
}

// hidden reports elements a browser would not show.
func hidden(n *html.Node) bool {
	for _, a := range n.Attr {
		switch a.Key {
		case "hidden":
			return true
		case "aria-hidden":
			if a.Val == "true" {
				return true
			}
		case "style":
			s := strings.ReplaceAll(strings.ToLower(a.Val), " ", "")
			if strings.Contains(s, "display:none") || strings.Contains(s, "visibility:hidden") {
				return true
			}
		}
	}
	return false
}

func textOf(n *html.Node) string {
	var sb strings.Builder
	walk(n, func(c *html.Node) bool {
		if c.Type == html.ElementNode && (skipped[c.DataAtom] || hidden(c)) {
			return false
		}
		if c.Type == html.TextNode {
			sb.WriteString(c.Data)
			sb.WriteString(" ")
		}
		return true
	})
	return sb.String()
}

func collapse(s string) string {
	return strings.TrimSpace(spaceRun.ReplaceAllString(strings.ReplaceAll(s, "\n", " "), " "))
}

// walk visits n and its descendants depth-first; returning false from fn
// skips that node's children.
func walk(n *html.Node, fn func(*html.Node) bool) {
	if !fn(n) {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

func findFirst(n *html.Node, a atom.Atom) *html.Node {
	var found *html.Node
	walk(n, func(c *html.Node) bool {
		if found != nil {
			return false
		}
		if c.Type == html.ElementNode && c.DataAtom == a {
			found = c
			return false
		}
		return true
	})
	return found
}

func findByAttr(n *html.Node, key, val string) *html.Node {
	var found *html.Node
	walk(n, func(c *html.Node) bool {
		if found != nil {
			return false
		}
		if c.Type == html.ElementNode && attr(c, key) == val {
			found = c
			return false
		}
		return true
	})
	return found
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
