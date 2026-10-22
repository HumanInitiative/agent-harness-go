// Package reportdocx renders a report draft as a Word document (.docx).
//
// A .docx is a zip of XML parts. A report draft needs only headings,
// paragraphs, a few tables and bullets, so the parts are written directly
// with the standard library rather than pulling in a document library.
package reportdocx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
)

// ContentType is the media type of a .docx file.
const ContentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// Renderer implements outbound.DraftRenderer for .docx.
type Renderer struct{}

// New returns the DOCX renderer.
func New() Renderer { return Renderer{} }

func (Renderer) Format() string      { return "docx" }
func (Renderer) ContentType() string { return ContentType }
func (Renderer) Extension() string   { return "docx" }

// Render writes the draft as a .docx file.
func (Renderer) Render(d report.Draft) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", rootRelsXML},
		{"word/_rels/document.xml.rels", documentRelsXML},
		{"word/styles.xml", stylesXML},
		{"word/document.xml", documentXML(d)},
	}
	for _, p := range parts {
		w, err := zw.Create(p.name)
		if err != nil {
			return nil, fmt.Errorf("docx: create %s: %w", p.name, err)
		}
		if _, err := w.Write([]byte(p.body)); err != nil {
			return nil, fmt.Errorf("docx: write %s: %w", p.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("docx: close archive: %w", err)
	}
	return buf.Bytes(), nil
}

func documentXML(d report.Draft) string {
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, blk := range d.Blocks {
		switch blk.Kind {
		case report.BlockHeading:
			style := "Heading1"
			if blk.Level >= 2 {
				style = "Heading2"
			}
			b.WriteString(`<w:p><w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>` + run(blk.Text, false) + `</w:p>`)
		case report.BlockParagraph:
			b.WriteString(`<w:p>` + run(blk.Text, blk.Bold) + `</w:p>`)
		case report.BlockBullets:
			for _, item := range blk.Items {
				b.WriteString(`<w:p><w:pPr><w:ind w:left="360" w:hanging="240"/></w:pPr>` + run("• "+item, false) + `</w:p>`)
			}
		case report.BlockTable:
			writeTable(&b, blk)
		}
	}
	// A4, 2 cm margins.
	b.WriteString(`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134" w:header="708" w:footer="708" w:gutter="0"/></w:sectPr>`)
	b.WriteString(`</w:body></w:document>`)
	return b.String()
}

func writeTable(b *strings.Builder, blk report.Block) {
	b.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="5000" w:type="pct"/><w:tblBorders>`)
	for _, side := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
		b.WriteString(`<w:` + side + ` w:val="single" w:sz="4" w:space="0" w:color="999999"/>`)
	}
	b.WriteString(`</w:tblBorders></w:tblPr>`)
	writeRow(b, blk.Header, true, true)
	for i, row := range blk.Rows {
		writeRow(b, row, false, blk.TotalRow && i == len(blk.Rows)-1)
	}
	b.WriteString(`</w:tbl><w:p/>`)
}

func writeRow(b *strings.Builder, cells []string, header, bold bool) {
	b.WriteString(`<w:tr>`)
	for i, c := range cells {
		b.WriteString(`<w:tc><w:tcPr>`)
		if header {
			b.WriteString(`<w:shd w:val="clear" w:color="auto" w:fill="E7E6E6"/>`)
		}
		b.WriteString(`</w:tcPr><w:p><w:pPr>`)
		if i > 0 {
			b.WriteString(`<w:jc w:val="right"/>`)
		}
		b.WriteString(`</w:pPr>` + run(c, bold) + `</w:p></w:tc>`)
	}
	b.WriteString(`</w:tr>`)
}

// run writes text as one run; a newline becomes a line break.
func run(text string, bold bool) string {
	var b strings.Builder
	b.WriteString(`<w:r>`)
	if bold {
		b.WriteString(`<w:rPr><w:b/></w:rPr>`)
	}
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			b.WriteString(`<w:br/>`)
		}
		b.WriteString(`<w:t xml:space="preserve">` + escape(line) + `</w:t>`)
	}
	b.WriteString(`</w:r>`)
	return b.String()
}

// escape makes text safe for XML, dropping the control characters XML 1.0
// cannot hold at all.
func escape(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' || r == 0x7f || r == 0xFFFE || r == 0xFFFF {
			return -1
		}
		return r
	}, s)
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s)) // writing to a bytes.Buffer cannot fail
	return b.String()
}

const contentTypesXML = xml.Header + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
	`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
	`<Default Extension="xml" ContentType="application/xml"/>` +
	`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
	`<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>` +
	`</Types>`

const rootRelsXML = xml.Header + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
	`</Relationships>`

const documentRelsXML = xml.Header + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
	`</Relationships>`

const stylesXML = xml.Header + `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
	`<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:cs="Calibri"/><w:sz w:val="22"/></w:rPr></w:rPrDefault>` +
	`<w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="264" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>` +
	`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/>` +
	`<w:pPr><w:keepNext/><w:spacing w:before="240" w:after="160"/><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:b/><w:sz w:val="36"/></w:rPr></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/>` +
	`<w:pPr><w:keepNext/><w:spacing w:before="240" w:after="120"/><w:outlineLvl w:val="1"/></w:pPr><w:rPr><w:b/><w:sz w:val="28"/></w:rPr></w:style>` +
	`</w:styles>`
