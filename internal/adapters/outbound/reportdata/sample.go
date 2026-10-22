// Package reportdata holds adapters for outbound.ReportDataSource.
package reportdata

import (
	"context"
	"fmt"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

// SampleProgramID is the one program the sample source knows.
const SampleProgramID = "sample-program"

// Sample serves fixed, made-up figures so the report tools and template can
// be tried before the donation API contract is known. It is not a stand-in
// for real data: the numbers are invented and the same for every period.
type Sample struct {
	now func() time.Time
}

// NewSample builds the sample source. now may be nil (time.Now).
func NewSample(now func() time.Time) *Sample {
	if now == nil {
		now = time.Now
	}
	return &Sample{now: now}
}

// Figures returns the sample figures for SampleProgramID, labelled with the
// requested period.
func (s *Sample) Figures(_ context.Context, req report.Request) (report.Figures, error) {
	if err := req.Validate(); err != nil {
		return report.Figures{}, fmt.Errorf("sample report data: %w", err)
	}
	if req.ProgramID != SampleProgramID {
		return report.Figures{}, outbound.ErrProgramNotFound
	}
	return report.Figures{
		Program: report.Program{ID: SampleProgramID, Name: "Sample program (invented data)"},
		Period:  req.Period,
		AsOf:    s.now(),
		Received: []report.Amount{
			{Label: "Individual donors", Rupiah: 150_000_000},
			{Label: "Corporate donors", Rupiah: 100_000_000},
		},
		Disbursed: []report.Amount{
			{Label: "Food packages", Rupiah: 120_000_000},
			{Label: "Operations", Rupiah: 30_000_000},
		},
		Beneficiaries: []report.Count{
			{Label: "Families", People: 320},
			{Label: "Children", People: 540},
		},
	}, nil
}

// Programs lists the one program the sample source knows.
func (s *Sample) Programs(context.Context) ([]report.Program, error) {
	return []report.Program{{ID: SampleProgramID, Name: "Sample program (invented data)"}}, nil
}
