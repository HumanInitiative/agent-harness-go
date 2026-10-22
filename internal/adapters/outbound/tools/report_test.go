package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportdata"
	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
)

func sampleTool() *ReportFiguresTool {
	fixed := time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC)
	return NewReportFiguresTool(reportdata.NewSample(func() time.Time { return fixed }))
}

func TestReportFiguresTool_Execute(t *testing.T) {
	got, err := sampleTool().Execute(context.Background(),
		[]byte(`{"program_id":"sample-program","from":"2026-01-01","to":"2026-03-31"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	want := map[string]float64{
		"total_received_rupiah":  250_000_000,
		"total_disbursed_rupiah": 150_000_000,
		"balance_rupiah":         100_000_000,
		"total_beneficiaries":    860,
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %v, want %v", k, out[k], v)
		}
	}
	if out["data_as_of"] != "2026-04-02T09:00:00Z" {
		t.Errorf("data_as_of = %v", out["data_as_of"])
	}
}

func TestReportFiguresTool_Execute_Rejects(t *testing.T) {
	cases := map[string]string{
		"malformed":       `not json`,
		"bad date":        `{"program_id":"sample-program","from":"01/01/2026","to":"2026-03-31"}`,
		"reversed period": `{"program_id":"sample-program","from":"2026-04-01","to":"2026-03-31"}`,
		"no program":      `{"program_id":"","from":"2026-01-01","to":"2026-03-31"}`,
		"unknown program": `{"program_id":"nope","from":"2026-01-01","to":"2026-03-31"}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := sampleTool().Execute(context.Background(), []byte(args)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

type badSource struct{}

func (badSource) Figures(context.Context, report.Request) (report.Figures, error) {
	return report.Figures{}, nil
}

type failingSource struct{}

func (failingSource) Figures(context.Context, report.Request) (report.Figures, error) {
	return report.Figures{}, errors.New("upstream down")
}

func TestReportFiguresTool_Execute_DistrustsTheSource(t *testing.T) {
	args := []byte(`{"program_id":"x","from":"2026-01-01","to":"2026-03-31"}`)
	if _, err := NewReportFiguresTool(badSource{}).Execute(context.Background(), args); err == nil ||
		!strings.Contains(err.Error(), "unusable") {
		t.Fatalf("invalid figures must be refused, got %v", err)
	}
	if _, err := NewReportFiguresTool(failingSource{}).Execute(context.Background(), args); err == nil {
		t.Fatal("source errors must surface")
	}
}
