package bootstrap

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/csrprospects"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportdocx"
	"github.com/HumanInitiative/agent-harness-go/internal/application/prospecting"
	"github.com/HumanInitiative/agent-harness-go/internal/platform/config"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

// NewOutreachService wires the funder shortlist over the CSR index: companies
// are scored against the requested program, emails are drafted by model and
// the result can be rendered as DOCX.
func NewOutreachService(cfg config.OutreachConfig, store *csr.Store, minConfidence float64, staleAfter time.Duration,
	model outbound.ModelPort, log *slog.Logger) (*prospecting.Service, error) {
	pc := prospecting.DefaultConfig()
	pc.InputRupiahPerMillion = cfg.InputRupiahPerMillion
	pc.OutputRupiahPerMillion = cfg.OutputRupiahPerMillion
	source := csrprospects.New(store, minConfidence, csr.IndexOptions{StaleAfter: staleAfter})
	svc, err := prospecting.NewService(source, model, []outbound.DraftRenderer{reportdocx.New()}, pc, log)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: outreach service: %w", err)
	}
	return svc, nil
}
