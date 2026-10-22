package reportdocs

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportdocx"
	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

type fakePDF struct{ err error }

func (f fakePDF) ExtractText(_ context.Context, path string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "Halaman satu\fHalaman dua dari " + filepath.Base(path), nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDocx(t *testing.T, path, documentXML string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, _ := zw.Create("word/document.xml")
	_, _ = w.Write([]byte(documentXML))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

const wordDoc = `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
	`<w:p><w:r><w:t>Kegiatan Maret</w:t></w:r><w:r><w:t xml:space="preserve"> 2026</w:t></w:r></w:p>` +
	`<w:p><w:r><w:t>Dibagikan</w:t><w:tab/><w:t>1.200 paket</w:t></w:r></w:p></w:body></w:document>`

func TestList_ReadsEveryFormatSorted(t *testing.T) {
	root := t.TempDir()
	prog := filepath.Join(root, "prog-1")
	write(t, filepath.Join(prog, "b-catatan.txt"), "Catatan lapangan\n")
	write(t, filepath.Join(prog, "c-ringkasan.md"), "# Ringkasan")
	write(t, filepath.Join(prog, "a-laporan.pdf"), "%PDF-fake")
	writeDocx(t, filepath.Join(prog, "d-kegiatan.docx"), wordDoc)
	write(t, filepath.Join(prog, "~$d-kegiatan.docx"), "lock")
	write(t, filepath.Join(prog, ".DS_Store"), "x")
	write(t, filepath.Join(prog, "gambar.png"), "x")
	write(t, filepath.Join(root, "prog-2", "lain.txt"), "program lain")

	d, _ := NewDir(root, fakePDF{}, quiet())
	docs, err := d.List(context.Background(), "prog-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, doc := range docs {
		names = append(names, doc.Name)
	}
	if strings.Join(names, ",") != "a-laporan.pdf,b-catatan.txt,c-ringkasan.md,d-kegiatan.docx" {
		t.Fatalf("names = %v", names)
	}
	if !strings.Contains(docs[0].Text, "Halaman satu\nHalaman dua dari a-laporan.pdf") {
		t.Errorf("pdf text = %q", docs[0].Text)
	}
	if docs[3].Text != "Kegiatan Maret 2026\nDibagikan\t1.200 paket" {
		t.Errorf("docx text = %q", docs[3].Text)
	}
}

func TestList_NamedDocumentsOnly(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "p", "a.txt"), "A")
	write(t, filepath.Join(root, "p", "b.txt"), "B")
	d, _ := NewDir(root, nil, quiet())

	docs, err := d.List(context.Background(), "p", []string{"b.txt"})
	if err != nil || len(docs) != 1 || docs[0].Name != "b.txt" {
		t.Fatalf("docs = %+v, err = %v", docs, err)
	}
	if _, err := d.List(context.Background(), "p", []string{"nope.txt"}); !errors.Is(err, outbound.ErrDocumentNotFound) {
		t.Errorf("missing named file: %v", err)
	}
	if _, err := d.List(context.Background(), "ghost", []string{"a.txt"}); !errors.Is(err, outbound.ErrDocumentNotFound) {
		t.Errorf("named file in a missing program: %v", err)
	}
}

func TestList_UnknownOrUnsafeProgramHasNoDocuments(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "secret.txt"), "outside")
	write(t, filepath.Join(filepath.Dir(root), "outside-the-root.txt"), "x")
	d, _ := NewDir(root, nil, quiet())
	for _, id := range []string{"ghost", "..", "../" + filepath.Base(root), "a/b", ".hidden", ""} {
		docs, err := d.List(context.Background(), id, nil)
		if err != nil || len(docs) != 0 {
			t.Errorf("program %q: docs = %v, err = %v", id, docs, err)
		}
	}
	// A name that is a path must not escape either.
	write(t, filepath.Join(root, "p", "a.txt"), "A")
	if _, err := d.List(context.Background(), "p", []string{"../secret.txt"}); !errors.Is(err, outbound.ErrDocumentNotFound) {
		t.Errorf("path as name: %v", err)
	}
}

func TestList_UnreadableFileIsListedWithoutText(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "p", "scan.pdf"), "x")
	write(t, filepath.Join(root, "p", "rusak.docx"), "not a zip")
	write(t, filepath.Join(root, "p", "ok.txt"), "isi")

	for name, pdf := range map[string]PDFTextExtractor{"pdftotext fails": fakePDF{err: errors.New("boom")}, "no pdftotext": nil} {
		t.Run(name, func(t *testing.T) {
			d, _ := NewDir(root, pdf, quiet())
			docs, err := d.List(context.Background(), "p", nil)
			if err != nil {
				t.Fatalf("one bad file must not fail the listing: %v", err)
			}
			text := map[string]string{}
			for _, doc := range docs {
				text[doc.Name] = doc.Text
			}
			if len(docs) != 3 || text["ok.txt"] != "isi" || text["scan.pdf"] != "" || text["rusak.docx"] != "" {
				t.Errorf("docs = %+v", docs)
			}
		})
	}
}

func TestList_ReadsWhatTheRendererWrites(t *testing.T) {
	day := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	draft, err := report.Assemble(report.Figures{
		Program: report.Program{ID: "p", Name: "Program"}, Period: report.Period{From: day, To: day}, AsOf: day,
	}, []report.Narrative{
		{Section: report.SectionActivities, Text: "Isi kegiatan.", Sources: []string{"a"}},
		{Section: report.SectionChallenges, Text: "Isi tantangan.", Sources: []string{"a"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := reportdocx.New().Render(draft)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "p", "draf.docx"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	d, _ := NewDir(root, nil, quiet())
	docs, err := d.List(context.Background(), "p", nil)
	if err != nil || len(docs) != 1 {
		t.Fatalf("docs = %v, err = %v", docs, err)
	}
	for _, want := range []string{"Donation report: Program", "Isi kegiatan.", "Isi tantangan."} {
		if !strings.Contains(docs[0].Text, want) {
			t.Errorf("text lacks %q:\n%s", want, docs[0].Text)
		}
	}
}

func TestNewDir_RequiresRoot(t *testing.T) {
	if _, err := NewDir(" ", nil, quiet()); err == nil {
		t.Fatal("expected an error")
	}
}
