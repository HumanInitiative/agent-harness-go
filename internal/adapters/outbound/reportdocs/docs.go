// Package reportdocs reads a program's activity reports from a folder.
//
// Layout: one sub-folder per program id under the root, holding that
// program's reports (.pdf, .docx, .txt, .md). Putting a file in the folder
// is how a report becomes available to the agent; a document store or an
// upload endpoint can replace this adapter behind the same port later.
package reportdocs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/doctext"
	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

// programIDPattern is the shape of a folder name: no separators, no dots at
// the start, so an id can never point outside the root.
var programIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// PDFTextExtractor turns a PDF file into text; websearch.PDFToText fits.
type PDFTextExtractor = doctext.PDFTextExtractor

// Dir implements outbound.ActivityDocuments over a folder tree.
type Dir struct {
	root string
	text doctext.Extractor
	log  *slog.Logger
}

// NewDir reads documents under root. pdf may be nil: PDFs are then listed
// with no text, which the report flags to the reviewer.
func NewDir(root string, pdf PDFTextExtractor, log *slog.Logger) (*Dir, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("reportdocs: root folder is required")
	}
	return &Dir{root: root, text: doctext.Extractor{PDF: pdf}, log: log}, nil
}

// List implements outbound.ActivityDocuments.
func (d *Dir) List(ctx context.Context, programID string, names []string) ([]report.Document, error) {
	if !programIDPattern.MatchString(programID) {
		return nil, nil
	}
	dir := filepath.Join(d.root, programID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		if len(names) > 0 {
			return nil, fmt.Errorf("%w: %s", outbound.ErrDocumentNotFound, names[0])
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reportdocs: read %s: %w", dir, err)
	}

	available := map[string]bool{}
	for _, e := range entries {
		if e.Type().IsRegular() && doctext.Supported(e.Name()) {
			available[e.Name()] = true
		}
	}
	var wanted []string
	if len(names) == 0 {
		for n := range available {
			wanted = append(wanted, n)
		}
	} else {
		for _, n := range names {
			if !available[n] {
				return nil, fmt.Errorf("%w: %s", outbound.ErrDocumentNotFound, n)
			}
			wanted = append(wanted, n)
		}
	}
	sort.Strings(wanted)

	docs := make([]report.Document, 0, len(wanted))
	for _, name := range wanted {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		doc := report.Document{Name: name}
		text, err := d.text.Text(ctx, filepath.Join(dir, name))
		if err != nil {
			// One unreadable file must not sink the report; it is listed
			// without text and the service tells the reviewer.
			d.log.WarnContext(ctx, "activity document unreadable", "program", programID, "file", name, "error", err)
		}
		doc.Text = strings.ToValidUTF8(text, "")
		docs = append(docs, doc)
	}
	return docs, nil
}
