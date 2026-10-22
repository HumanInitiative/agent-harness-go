package outbound

import (
	"context"
	"errors"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
)

// ErrProgramNotFound means the donation data has no program with that id.
var ErrProgramNotFound = errors.New("program not found")

// ReportDataSource supplies the figures of a donation report. The real
// adapter calls the donation API; the application and the tools never see
// how.
type ReportDataSource interface {
	// Figures returns the figures of one program over a period. An unknown
	// program returns ErrProgramNotFound. The result must pass
	// report.Figures.Validate.
	Figures(ctx context.Context, req report.Request) (report.Figures, error)
}

// ErrDocumentNotFound means a requested activity document does not exist
// for the program.
var ErrDocumentNotFound = errors.New("activity document not found")

// ActivityDocuments supplies the activity reports a program's narrative is
// written from. How documents get there (a folder, an upload store, a
// document system) is the adapter's business.
type ActivityDocuments interface {
	// List returns the text of the program's documents. A non-empty names
	// restricts the result to those files, and an unknown name returns
	// ErrDocumentNotFound. An unknown program, or one with no documents,
	// returns an empty slice. Names are plain file names, never paths.
	List(ctx context.Context, programID string, names []string) ([]report.Document, error)
}

// DraftRenderer turns a finished draft into a file of one format.
type DraftRenderer interface {
	// Format is the short name clients ask for, e.g. "docx".
	Format() string
	// ContentType is the media type of what Render returns.
	ContentType() string
	// Extension is the file name extension, without the dot.
	Extension() string
	Render(d report.Draft) ([]byte, error)
}
