// Package report holds the vocabulary of a donation report and the one rule
// that shapes it: figures are filled in by code from the donation data,
// never written by the model, and the model only writes the narrative
// sections, each citing the documents it drew from.
//
// This package must never import Genkit, an HTTP client, or any adapter
// package.
package report

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Request selects what a report is about.
type Request struct {
	ProgramID string
	Period    Period
}

// Validate reports why the request cannot be served, if it cannot.
func (r Request) Validate() error {
	if strings.TrimSpace(r.ProgramID) == "" {
		return errors.New("program id is required")
	}
	return r.Period.Validate()
}

// Period is an inclusive range of calendar days.
type Period struct {
	From time.Time
	To   time.Time
}

// Validate rejects an unset or reversed period.
func (p Period) Validate() error {
	if p.From.IsZero() || p.To.IsZero() {
		return errors.New("period needs both a start and an end date")
	}
	if p.To.Before(p.From) {
		return errors.New("period ends before it starts")
	}
	return nil
}

// Program identifies the program a report covers.
type Program struct {
	ID   string
	Name string
}

// Amount is money in whole rupiah under a label (a month, a source, a
// category). Whole rupiah avoids floating point in totals.
type Amount struct {
	Label  string
	Rupiah int64
}

// Count is a number of people under a label (a group or region).
type Count struct {
	Label  string
	People int
}

// Figures is everything numeric in a report, as the donation data reports
// it. Totals are derived here from the lines so a total can never disagree
// with the table above it.
type Figures struct {
	Program Program
	Period  Period
	// AsOf is when the data was read; the report states it.
	AsOf time.Time

	Received      []Amount
	Disbursed     []Amount
	Beneficiaries []Count
}

// TotalReceived is the sum of Received.
func (f Figures) TotalReceived() int64 { return sumAmounts(f.Received) }

// TotalDisbursed is the sum of Disbursed.
func (f Figures) TotalDisbursed() int64 { return sumAmounts(f.Disbursed) }

// Balance is what was received and not yet disbursed. It is negative when
// more was disbursed than received in the period (spending from earlier
// funds), which the report shows as is.
func (f Figures) Balance() int64 { return f.TotalReceived() - f.TotalDisbursed() }

// TotalBeneficiaries is the sum of Beneficiaries. The groups must not
// overlap; the data source is responsible for that.
func (f Figures) TotalBeneficiaries() int {
	total := 0
	for _, c := range f.Beneficiaries {
		total += c.People
	}
	return total
}

// Validate rejects figures that cannot be put in a report.
func (f Figures) Validate() error {
	if strings.TrimSpace(f.Program.ID) == "" || strings.TrimSpace(f.Program.Name) == "" {
		return errors.New("figures have no program")
	}
	if err := f.Period.Validate(); err != nil {
		return err
	}
	if f.AsOf.IsZero() {
		return errors.New("figures have no data timestamp")
	}
	for _, a := range append(append([]Amount{}, f.Received...), f.Disbursed...) {
		if strings.TrimSpace(a.Label) == "" {
			return errors.New("an amount has no label")
		}
		if a.Rupiah < 0 {
			return fmt.Errorf("amount %q is negative", a.Label)
		}
	}
	for _, c := range f.Beneficiaries {
		if strings.TrimSpace(c.Label) == "" {
			return errors.New("a beneficiary count has no label")
		}
		if c.People < 0 {
			return fmt.Errorf("beneficiary count %q is negative", c.Label)
		}
	}
	return nil
}

func sumAmounts(lines []Amount) int64 {
	var total int64
	for _, a := range lines {
		total += a.Rupiah
	}
	return total
}
