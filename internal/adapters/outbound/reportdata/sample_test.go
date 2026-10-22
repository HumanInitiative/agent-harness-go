package reportdata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

func TestSample_FiguresAreValid(t *testing.T) {
	req := report.Request{
		ProgramID: SampleProgramID,
		Period: report.Period{
			From: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			To:   time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		},
	}
	fig, err := NewSample(nil).Figures(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := fig.Validate(); err != nil {
		t.Fatalf("sample figures must satisfy the port contract: %v", err)
	}
	if fig.Period != req.Period {
		t.Error("figures must carry the requested period")
	}
}

func TestSample_UnknownProgram(t *testing.T) {
	req := report.Request{
		ProgramID: "other",
		Period:    report.Period{From: time.Now(), To: time.Now()},
	}
	if _, err := NewSample(nil).Figures(context.Background(), req); !errors.Is(err, outbound.ErrProgramNotFound) {
		t.Fatalf("err = %v, want ErrProgramNotFound", err)
	}
}
