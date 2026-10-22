package bootstrap

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportapi"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportdata"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportdocs"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportdocx"
	"github.com/HumanInitiative/agent-harness-go/internal/application/reporting"
	"github.com/HumanInitiative/agent-harness-go/internal/platform/config"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch"
)

// ReportStack is the donation report feature, wired.
type ReportStack struct {
	Service *reporting.Service
	// Data is the figures source, also given to the get_report_figures tool.
	Data outbound.ReportDataSource
	// Source says where the data comes from, for the startup log.
	Source string
}

// NewReportStack wires the donation report use case. With cfg.APIURL, the
// figures and the activity documents come from the donation data API; without
// it, from built-in sample figures and the folder cfg.DocsDir. PDFs are read
// through pdftotext when it is installed. Output is DOCX.
func NewReportStack(cfg config.ReportConfig, model outbound.ModelPort, log *slog.Logger) (*ReportStack, error) {
	var pdf *websearch.PDFToText
	if p, err := websearch.NewPDFToText(time.Minute, 4<<20); err != nil {
		log.Warn("pdftotext not found: PDF activity documents will not be readable (install poppler-utils)")
	} else {
		pdf = p
	}
	// A typed nil must not become a non-nil interface.
	var pdfExtractor reportdocs.PDFTextExtractor
	if pdf != nil {
		pdfExtractor = pdf
	}

	var (
		data    outbound.ReportDataSource
		docs    outbound.ActivityDocuments
		catalog outbound.ProgramCatalog
		source  string
	)
	if cfg.APIURL != "" {
		client, err := reportapi.New(reportapi.Config{BaseURL: cfg.APIURL, APIKey: cfg.APIKey, Timeout: cfg.APITimeout}, pdfExtractor, log)
		if err != nil {
			return nil, fmt.Errorf("bootstrap: report API: %w", err)
		}
		data, docs, catalog, source = client, client, client, "donation data API "+cfg.APIURL
	} else {
		dir, err := reportdocs.NewDir(cfg.DocsDir, pdfExtractor, log)
		if err != nil {
			return nil, fmt.Errorf("bootstrap: report documents: %w", err)
		}
		sample := reportdata.NewSample(nil)
		data, docs, catalog, source = sample, dir, sample, "built-in SAMPLE figures and folder "+cfg.DocsDir
	}

	svc, err := reporting.NewService(data, docs, model, []outbound.DraftRenderer{reportdocx.New()}, reporting.DefaultConfig(), log)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: report service: %w", err)
	}
	return &ReportStack{Service: svc.WithPrograms(catalog), Data: data, Source: source}, nil
}
