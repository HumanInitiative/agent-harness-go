// Package outreach holds the vocabulary of pilot 2: from one Human
// Initiative program to a shortlist of companies that may fund it, each with
// the reasons it fits, the evidence behind them, and a draft approach email.
//
// The rule that shapes it mirrors the donation reports: facts about a
// company come from the evidence index, never from the model. The model only
// words the email, and every claim in it must cite evidence the index holds;
// anything the checks cannot confirm becomes a note for the person who
// reviews the draft.
//
// This package must never import Genkit, an HTTP client, or any adapter.
package outreach

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	maxNameChars        = 200
	maxDescriptionChars = 3000
	maxTerms            = 12
)

// programIDPattern is the shape of a catalog id: lowercase words joined by
// dashes, so an id is safe in a URL and in a file.
var programIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidProgramID reports whether id can name a catalog program.
func ValidProgramID(id string) bool { return programIDPattern.MatchString(id) }

// Program is the Human Initiative program that needs funding. Its focus
// areas, regions and types are what companies are scored against.
type Program struct {
	// ID names the program in the catalog; empty for a program described
	// inline in a request.
	ID          string
	Name        string
	Description string
	// FocusAreas are the fields the program works in ("pendidikan").
	FocusAreas []string
	// Regions are where it works ("Jawa Barat"). Empty means anywhere.
	Regions []string
	// ProgramTypes are the kinds of support it needs ("beasiswa").
	ProgramTypes []string
}

// Validate reports why the program cannot be matched, if it cannot.
func (p Program) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("program name is required")
	}
	if len([]rune(p.Name)) > maxNameChars {
		return fmt.Errorf("program name is longer than %d characters", maxNameChars)
	}
	if len([]rune(p.Description)) > maxDescriptionChars {
		return fmt.Errorf("program description is longer than %d characters", maxDescriptionChars)
	}
	if len(cleanTerms(p.FocusAreas)) == 0 {
		return errors.New("at least one focus area is required: it is what companies are scored on")
	}
	for name, terms := range map[string][]string{"focus areas": p.FocusAreas, "regions": p.Regions, "program types": p.ProgramTypes} {
		if len(terms) > maxTerms {
			return fmt.Errorf("at most %d %s", maxTerms, name)
		}
	}
	return nil
}

// Clean returns the program with blank terms dropped and whitespace trimmed.
func (p Program) Clean() Program {
	return Program{
		ID:           strings.TrimSpace(p.ID),
		Name:         strings.TrimSpace(p.Name),
		Description:  strings.TrimSpace(p.Description),
		FocusAreas:   cleanTerms(p.FocusAreas),
		Regions:      cleanTerms(p.Regions),
		ProgramTypes: cleanTerms(p.ProgramTypes),
	}
}

func cleanTerms(in []string) []string {
	var out []string
	for _, t := range in {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Evidence is a verbatim excerpt from a company page, the only thing a claim
// in an email may rest on.
type Evidence struct {
	ID      int64
	URL     string
	Title   string
	Excerpt string
	// Date is when the page was published, or fetched when it does not say;
	// DatePublished tells which.
	Date          time.Time
	DatePublished bool
}

// CompanyProgram is a CSR program a company runs, as the index read it.
type CompanyProgram struct {
	Name        string
	Description string
	FocusAreas  []string
	Regions     []string
	// Dates are as precise as the pages state them ("2026", "2026-06").
	PeriodStart      string
	PeriodEnd        string
	ProposalDeadline string
	Status           string
	EvidenceIDs      []int64
}

// Candidate is one company that may fund the program, with everything
// needed to judge it and to cite it.
type Candidate struct {
	CompanyID int64
	Name      string
	Domain    string
	Sector    string
	Region    string
	// Verified is true only when a person confirmed the company record;
	// automated code never sets it.
	Verified bool

	// Score is 0..100 and Reasons say why, in Indonesian, including what
	// lowers trust in it.
	Score         int
	Reasons       []string
	LowConfidence bool

	// ConfirmedAt is when the company's data was last confirmed current.
	ConfirmedAt time.Time
	Stale       bool
	StaleReason string

	Programs       []CompanyProgram
	HiddenPrograms int
	// ProposalChannel is how the company says proposals are submitted, as
	// the page states it ("csr@..."); empty when unknown.
	ProposalChannel string
	// SeekingPartners is "true" or "false" when the pages say, else "".
	SeekingPartners string
	Evidence        []Evidence
}

// EvidenceByID finds one of the candidate's evidence excerpts.
func (c Candidate) EvidenceByID(id int64) (Evidence, bool) {
	for _, e := range c.Evidence {
		if e.ID == id {
			return e, true
		}
	}
	return Evidence{}, false
}

// Email is a draft approach email. It is a draft: a person reads and sends
// it, and the sender's own details are left as placeholders.
type Email struct {
	Subject string
	Body    string
	// Cited are the IDs of the evidence the email's claims about the
	// company rest on.
	Cited []int64
}

// Entry is one line of the shortlist.
type Entry struct {
	Candidate Candidate
	// Email is nil when drafting failed or was not asked for; Notes then
	// say which.
	Email *Email
	// Notes are what the reviewer must check before acting on this entry.
	Notes []string
}

// EmailTrace records how one email was drafted.
type EmailTrace struct {
	Company      string
	Attempts     int
	InputTokens  int
	OutputTokens int
	DurationMS   int64
	// Outcome is "ok", or why no email was produced.
	Outcome string
}

// Trace records one run, for cost accounting and for finding out why a run
// was slow or poor.
type Trace struct {
	StartedAt  time.Time
	DurationMS int64
	// Candidates is how many companies were scored before the cut.
	Candidates   int
	Emails       []EmailTrace
	InputTokens  int
	OutputTokens int
	// EstimatedCostRupiah is set only when token prices are configured.
	EstimatedCostRupiah *float64
}

// FocusGroup is a field of work the scoring understands, with the other
// words that count as the same field.
type FocusGroup struct {
	Name     string
	Synonyms []string
}

// Kinds of term a program is described by.
const (
	TermFocus  = "focus"
	TermRegion = "region"
	TermType   = "type"
)

// TermCoverage says how well one term of the program is covered by the
// companies in the index.
type TermCoverage struct {
	Kind string
	Term string
	// Group is the known field the term maps to; empty when not Recognized.
	Group string
	// Recognized is false for a focus area or program type the scoring does
	// not know: it then only matches companies using the same words.
	Recognized bool
	// Companies is how many indexed companies match the term.
	Companies int
}

// Shortlist is the result of a run.
type Shortlist struct {
	Program Program
	Entries []Entry
	// Terms and Warnings describe how the program's criteria fared against
	// the index: a term nobody matches is why a shortlist is short or empty.
	Terms       []TermCoverage
	Warnings    []string
	GeneratedAt time.Time
	Trace       Trace
}
