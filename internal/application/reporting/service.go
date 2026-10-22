// Package reporting is the donation report use case: read the figures from
// the donation data, read the program's activity documents, have the model
// write the narrative sections from them, check the result, and assemble
// the draft.
//
// The model never touches a figure. It receives the totals only so it does
// not contradict them, and every number it writes is checked against the
// figures and the documents afterwards.
package reporting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
	"github.com/HumanInitiative/agent-harness-go/internal/platform/caller"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

var (
	// ErrInvalidInput means the request is unusable as given (no program,
	// bad period, unsafe document name).
	ErrInvalidInput = errors.New("invalid input")
	// ErrNoDocuments means the program has no activity documents, so there
	// is nothing to write the narrative from. Drafting without them would
	// mean inventing it.
	ErrNoDocuments = errors.New("no activity documents for this program")
	// ErrNarrativeRejected means the model did not produce a usable
	// narrative within the allowed attempts.
	ErrNarrativeRejected = errors.New("the model did not produce a usable narrative")
	// ErrUnknownFormat means no renderer exists for the requested format.
	ErrUnknownFormat = errors.New("unknown report format")
)

// Config bounds what is sent to the model.
type Config struct {
	// MaxDocuments is how many documents one report may use.
	MaxDocuments int
	// MaxCharsPerDocument and MaxTotalChars cut long documents, in
	// characters, so one report cannot cost without limit. A cut document
	// is flagged to the reviewer.
	MaxCharsPerDocument int
	MaxTotalChars       int
	// Attempts is how many times the model may try to give a usable answer.
	Attempts int
}

// DefaultConfig is what main uses.
func DefaultConfig() Config {
	return Config{MaxDocuments: 10, MaxCharsPerDocument: 30_000, MaxTotalChars: 120_000, Attempts: 2}
}

// Service drafts donation reports.
type Service struct {
	data      outbound.ReportDataSource
	docs      outbound.ActivityDocuments
	model     outbound.ModelPort
	catalog   outbound.ProgramCatalog // optional
	renderers map[string]outbound.DraftRenderer
	cfg       Config
	log       *slog.Logger
}

// NewService wires the use case. renderers may be empty (drafts are then
// only available as Markdown).
func NewService(data outbound.ReportDataSource, docs outbound.ActivityDocuments, model outbound.ModelPort,
	renderers []outbound.DraftRenderer, cfg Config, log *slog.Logger) (*Service, error) {
	if data == nil || docs == nil || model == nil {
		return nil, errors.New("reporting: data source, documents and model are required")
	}
	if cfg.MaxDocuments < 1 || cfg.MaxCharsPerDocument < 1 || cfg.MaxTotalChars < 1 || cfg.Attempts < 1 {
		return nil, errors.New("reporting: every Config limit must be positive")
	}
	byFormat := map[string]outbound.DraftRenderer{}
	for _, r := range renderers {
		if _, dup := byFormat[r.Format()]; dup {
			return nil, fmt.Errorf("reporting: two renderers for format %q", r.Format())
		}
		byFormat[r.Format()] = r
	}
	return &Service{data: data, docs: docs, model: model, renderers: byFormat, cfg: cfg, log: log}, nil
}

// WithPrograms gives the service a catalog to list programs from. Without
// one, Programs returns an empty list.
func (s *Service) WithPrograms(catalog outbound.ProgramCatalog) *Service {
	s.catalog = catalog
	return s
}

// Programs lists the programs a report can be made for.
func (s *Service) Programs(ctx context.Context) ([]report.Program, error) {
	if s.catalog == nil {
		return []report.Program{}, nil
	}
	programs, err := s.catalog.Programs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list programs: %w", err)
	}
	return programs, nil
}

// Input selects the report.
type Input struct {
	ProgramID string
	From, To  time.Time
	// Documents restricts the report to these file names. Empty means every
	// document of the program.
	Documents []string
}

// Generate builds the draft for one program and period.
func (s *Service) Generate(ctx context.Context, in Input) (report.Draft, error) {
	req := report.Request{ProgramID: in.ProgramID, Period: report.Period{From: in.From, To: in.To}}
	if err := req.Validate(); err != nil {
		return report.Draft{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	for _, name := range in.Documents {
		if !plainFileName(name) {
			return report.Draft{}, fmt.Errorf("%w: document %q is not a plain file name", ErrInvalidInput, name)
		}
	}

	figures, err := s.data.Figures(ctx, req)
	if err != nil {
		return report.Draft{}, fmt.Errorf("read figures: %w", err)
	}
	if err := figures.Validate(); err != nil {
		return report.Draft{}, fmt.Errorf("read figures: data source returned unusable figures: %w", err)
	}

	docs, err := s.docs.List(ctx, in.ProgramID, in.Documents)
	if err != nil {
		return report.Draft{}, fmt.Errorf("read activity documents: %w", err)
	}
	docs, notes := s.prepare(docs)
	if len(docs) == 0 {
		return report.Draft{}, ErrNoDocuments
	}

	narratives, err := s.writeNarratives(ctx, figures, docs)
	if err != nil {
		return report.Draft{}, err
	}
	notes = append(notes, checkNumbers(figures, docs, narratives)...)

	draft, err := report.Assemble(figures, narratives, notes)
	if err != nil {
		return report.Draft{}, fmt.Errorf("%w: %v", ErrNarrativeRejected, err)
	}
	// The audit trail of drafts: who asked for what, and what it was built
	// from. Never the text of the report or of the documents.
	s.log.InfoContext(ctx, "report drafted",
		"key_id", caller.KeyID(ctx),
		"program", in.ProgramID,
		"from", in.From.Format("2006-01-02"),
		"to", in.To.Format("2006-01-02"),
		"documents_read", documentNames(docs),
		"documents_cited", draft.Sources,
		"review_notes", len(notes))
	return draft, nil
}

// Rendered is a draft turned into a file.
type Rendered struct {
	Data        []byte
	ContentType string
	Extension   string
}

// Render turns a draft into the requested format.
func (s *Service) Render(d report.Draft, format string) (Rendered, error) {
	r, ok := s.renderers[format]
	if !ok {
		return Rendered{}, fmt.Errorf("%w: %q", ErrUnknownFormat, format)
	}
	data, err := r.Render(d)
	if err != nil {
		return Rendered{}, fmt.Errorf("render %s: %w", format, err)
	}
	return Rendered{Data: data, ContentType: r.ContentType(), Extension: r.Extension()}, nil
}

// Formats lists the formats Render accepts, sorted.
func (s *Service) Formats() []string {
	out := make([]string, 0, len(s.renderers))
	for f := range s.renderers {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// prepare masks personal data, drops empty documents, and applies the size
// limits, returning review notes for anything it changed or skipped.
func (s *Service) prepare(docs []report.Document) ([]report.Document, []string) {
	var notes []string
	var out []report.Document
	total := 0
	for _, d := range docs {
		text, _ := report.MaskPersonalData(strings.TrimSpace(d.Text))
		if text == "" {
			notes = append(notes, fmt.Sprintf("%s has no readable text (a scanned PDF?) and was not used.", d.Name))
			continue
		}
		if len(out) >= s.cfg.MaxDocuments {
			notes = append(notes, fmt.Sprintf("%s was not used: a report uses at most %d documents.", d.Name, s.cfg.MaxDocuments))
			continue
		}
		room := min(s.cfg.MaxCharsPerDocument, s.cfg.MaxTotalChars-total)
		if room <= 0 {
			notes = append(notes, fmt.Sprintf("%s was not used: the size limit of the report was reached.", d.Name))
			continue
		}
		runes := []rune(text)
		if len(runes) > room {
			text, d.Truncated = string(runes[:room]), true
		}
		if d.Truncated {
			notes = append(notes, fmt.Sprintf("%s is long and only its first part was read.", d.Name))
		}
		d.Text = text
		total += len([]rune(text))
		out = append(out, d)
	}
	return out, notes
}

func documentNames(docs []report.Document) []string {
	names := make([]string, len(docs))
	for i, d := range docs {
		names[i] = d.Name
	}
	return names
}

// checkNumbers flags every number in a narrative that neither the figures
// nor the cited documents contain.
func checkNumbers(f report.Figures, docs []report.Document, narratives []report.Narrative) []string {
	byName := map[string]map[string]bool{}
	for _, d := range docs {
		byName[d.Name] = report.NumbersIn(d.Text)
	}
	known := f.KnownNumbers()
	var notes []string
	for _, n := range narratives {
		sets := []map[string]bool{known}
		for _, src := range n.Sources {
			if set, ok := byName[strings.TrimSpace(src)]; ok {
				sets = append(sets, set)
			}
		}
		if bad := report.UnsupportedNumbers(n.Text, sets...); len(bad) > 0 {
			notes = append(notes, fmt.Sprintf("In the %q section, these numbers are in neither the figures nor the cited documents: %s. Check them.",
				n.Section, strings.Join(bad, ", ")))
		}
	}
	return notes
}

// plainFileName reports whether name is a file name and not a path.
func plainFileName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`+"\x00")
}
