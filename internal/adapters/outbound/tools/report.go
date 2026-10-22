package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

type reportFiguresArgs struct {
	ProgramID string `json:"program_id"`
	From      string `json:"from"`
	To        string `json:"to"`
}

// ReportFiguresTool returns the numbers of a donation report. Report
// figures must come from here, never from the model's own arithmetic or
// memory.
type ReportFiguresTool struct {
	source outbound.ReportDataSource
}

// NewReportFiguresTool builds the tool over a data source.
func NewReportFiguresTool(source outbound.ReportDataSource) *ReportFiguresTool {
	return &ReportFiguresTool{source: source}
}

func (t *ReportFiguresTool) Name() string { return "get_report_figures" }

func (t *ReportFiguresTool) Description() string {
	return "Returns the figures of a donation report for one program and period: funds received and disbursed " +
		"(whole rupiah, by line), beneficiaries, their totals and the balance. Every number in a report must come " +
		"from this tool; never compute or recall figures yourself. Dates are YYYY-MM-DD and the period is inclusive."
}

func (t *ReportFiguresTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"program_id": map[string]any{"type": "string", "description": "Program id."},
			"from":       map[string]any{"type": "string", "description": "First day of the period, YYYY-MM-DD."},
			"to":         map[string]any{"type": "string", "description": "Last day of the period, YYYY-MM-DD."},
		},
		"required": []string{"program_id", "from", "to"},
	}
}

func (t *ReportFiguresTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in reportFiguresArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("get_report_figures: invalid arguments: %w", err)
	}
	from, err := time.Parse(dateLayout, in.From)
	if err != nil {
		return "", fmt.Errorf("get_report_figures: from must be YYYY-MM-DD: %w", err)
	}
	to, err := time.Parse(dateLayout, in.To)
	if err != nil {
		return "", fmt.Errorf("get_report_figures: to must be YYYY-MM-DD: %w", err)
	}
	req := report.Request{ProgramID: in.ProgramID, Period: report.Period{From: from, To: to}}
	if err := req.Validate(); err != nil {
		return "", fmt.Errorf("get_report_figures: %w", err)
	}

	fig, err := t.source.Figures(ctx, req)
	if errors.Is(err, outbound.ErrProgramNotFound) {
		return "", fmt.Errorf("get_report_figures: no program with id %q", in.ProgramID)
	}
	if err != nil {
		return "", fmt.Errorf("get_report_figures: %w", err)
	}
	if err := fig.Validate(); err != nil {
		return "", fmt.Errorf("get_report_figures: data source returned unusable figures: %w", err)
	}

	out := reportFiguresResult{
		Program:            fig.Program.Name,
		From:               in.From,
		To:                 in.To,
		DataAsOf:           fig.AsOf.Format(time.RFC3339),
		Received:           amounts(fig.Received),
		Disbursed:          amounts(fig.Disbursed),
		TotalReceived:      fig.TotalReceived(),
		TotalDisbursed:     fig.TotalDisbursed(),
		Balance:            fig.Balance(),
		TotalBeneficiaries: fig.TotalBeneficiaries(),
	}
	for _, c := range fig.Beneficiaries {
		out.Beneficiaries = append(out.Beneficiaries, countLine{Group: c.Label, People: c.People})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("get_report_figures: encode result: %w", err)
	}
	return string(b), nil
}

type amountLine struct {
	Label  string `json:"label"`
	Rupiah int64  `json:"rupiah"`
}

type countLine struct {
	Group  string `json:"group"`
	People int    `json:"people"`
}

type reportFiguresResult struct {
	Program            string       `json:"program"`
	From               string       `json:"from"`
	To                 string       `json:"to"`
	DataAsOf           string       `json:"data_as_of"`
	Received           []amountLine `json:"received"`
	Disbursed          []amountLine `json:"disbursed"`
	Beneficiaries      []countLine  `json:"beneficiaries"`
	TotalReceived      int64        `json:"total_received_rupiah"`
	TotalDisbursed     int64        `json:"total_disbursed_rupiah"`
	Balance            int64        `json:"balance_rupiah"`
	TotalBeneficiaries int          `json:"total_beneficiaries"`
}

func amounts(lines []report.Amount) []amountLine {
	out := make([]amountLine, 0, len(lines))
	for _, a := range lines {
		out = append(out, amountLine{Label: a.Label, Rupiah: a.Rupiah})
	}
	return out
}
