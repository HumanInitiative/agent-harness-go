package outbound

import (
	"context"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/outreach"
)

// Prospects is what a ProspectSource found for one program.
type Prospects struct {
	// Candidates are the best matches, best first, at most the limit asked.
	Candidates []outreach.Candidate
	// Fitting is how many companies fit the program before the cut.
	Fitting int
}

// ProspectSource finds the companies that may fund a program, from an
// evidence-backed index. It reads only what is already known: it never
// fetches pages, so it is fast and cheap.
type ProspectSource interface {
	// Prospects returns the companies whose CSR work fits the program: at
	// least one of its focus areas must match. An empty index or no fit is
	// an empty result, not an error.
	Prospects(ctx context.Context, program outreach.Program, limit int) (Prospects, error)
}
