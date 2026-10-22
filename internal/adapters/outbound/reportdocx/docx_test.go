package reportdocx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
)

func draft(t *testing.T, texts ...string) report.Draft {
	t.Helper()
	day := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	text := "Dibagikan paket <pangan> & \"sembako\".\nBaris kedua.\x01"
	if len(texts) > 0 {
		text = texts[0]
	}
	d, err := report.Assemble(report.Figures{
		Program:   report.Program{ID: "p", Name: "Program <A> & B"},
		Period:    report.Period{From: day.AddDate(0, -3, 0), To: day},
		AsOf:      day,
		Received:  []report.Amount{{Label: "Donor", Rupiah: 1_250_000}},
		Disbursed: []report.Amount{{Label: "Pangan", Rupiah: 1_000_000}},
	}, []report.Narrative{
		{Section: report.SectionActivities, Text: text, Sources: []string{"a.pdf"}},
		{Section: report.SectionChallenges, Text: "Banjir.", Sources: []string{"a.pdf"}},
	}, []string{"Check 999."})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func unzip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestRender_ProducesWellFormedParts(t *testing.T) {
	data, err := New().Render(draft(t))
	if err != nil {
		t.Fatal(err)
	}
	parts := unzip(t, data)
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "word/_rels/document.xml.rels", "word/styles.xml", "word/document.xml"} {
		body, ok := parts[name]
		if !ok {
			t.Errorf("missing part %s", name)
			continue
		}
		dec := xml.NewDecoder(strings.NewReader(body))
		for {
			if _, err := dec.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Errorf("%s is not well-formed XML: %v", name, err)
				break
			}
		}
	}
}

func TestRender_Content(t *testing.T) {
	data, err := New().Render(draft(t))
	if err != nil {
		t.Fatal(err)
	}
	doc := unzip(t, data)["word/document.xml"]
	for _, want := range []string{
		`Donation report: Program &lt;A&gt; &amp; B`,
		`w:pStyle w:val="Heading1"`,
		`Rp 1.250.000`,
		`paket &lt;pangan&gt; &amp; &#34;sembako&#34;.`,
		`<w:br/>`,
		`Check 999.`,
		`w:jc w:val="right"`,
		`<w:b/>`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("document.xml lacks %q", want)
		}
	}
	if strings.Contains(doc, "\x01") {
		t.Error("control characters must be dropped")
	}
}

func TestRenderer_Metadata(t *testing.T) {
	r := New()
	if r.Format() != "docx" || r.Extension() != "docx" || r.ContentType() != ContentType {
		t.Errorf("metadata: %s %s %s", r.Format(), r.Extension(), r.ContentType())
	}
}
