package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/application/reporting"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

const (
	reportFormatJSON = "json"
	reportDateLayout = "2006-01-02"
)

// ReportHandler exposes reporting.Service over HTTP.
type ReportHandler struct {
	service *reporting.Service
	logger  *slog.Logger
}

// NewReportHandler builds a ReportHandler around the reporting use case.
func NewReportHandler(service *reporting.Service, logger *slog.Logger) *ReportHandler {
	return &ReportHandler{service: service, logger: logger}
}

// ServeHTTP handles POST /api/v1/reports.
//
//	@Summary		Draft a donation report
//	@Description	Builds a draft donation report for one program and period. Figures come from the donation
//	@Description	data; the narrative sections are written by the model from the program's activity
//	@Description	documents and cite them. The draft always needs human review: notes lists what to check.
//	@Description	With format "docx" the response is the Word file instead of JSON.
//	@Tags			reports
//	@Accept			json
//	@Produce		json
//	@Produce		application/vnd.openxmlformats-officedocument.wordprocessingml.document
//	@Security		ApiKeyAuth
//	@Param			request	body		ReportRequest	true	"Report request"
//	@Success		200		{object}	ReportResponse	"format json"
//	@Success		200		{file}		file			"format docx"
//	@Failure		400		{object}	ErrorResponse	"invalid_request: malformed body, bad date, unknown format"
//	@Failure		401		{object}	ErrorResponse	"unauthorized: missing or invalid X-API-Key"
//	@Failure		404		{object}	ErrorResponse	"program_not_found or document_not_found"
//	@Failure		413		{object}	ErrorResponse	"request_too_large"
//	@Failure		422		{object}	ErrorResponse	"no_documents: the program has no readable activity documents"
//	@Failure		429		{object}	ErrorResponse	"rate_limited: see Retry-After header"
//	@Failure		500		{object}	ErrorResponse	"internal_error"
//	@Failure		502		{object}	ErrorResponse	"narrative_failed: the model gave no usable narrative"
//	@Failure		503		{object}	ErrorResponse	"model_unavailable: see Retry-After header"
//	@Failure		504		{object}	ErrorResponse	"timeout"
//	@Router			/api/v1/reports [post]
func (h *ReportHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req ReportRequest
	if !decodeJSON(w, r, &req, `{"program_id": "...", "from": "2026-01-01", "to": "2026-03-31"}`) {
		return
	}

	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = reportFormatJSON
	}
	// Refuse an unknown format before spending a model call on the draft.
	if format != reportFormatJSON && !slices.Contains(h.service.Formats(), format) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"format must be one of: "+strings.Join(append([]string{reportFormatJSON}, h.service.Formats()...), ", "))
		return
	}
	from, err := time.Parse(reportDateLayout, req.From)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "from must be a date like 2026-01-01")
		return
	}
	to, err := time.Parse(reportDateLayout, req.To)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, "to must be a date like 2026-03-31")
		return
	}

	draft, err := h.service.Generate(r.Context(), reporting.Input{
		ProgramID: req.ProgramID, From: from, To: to, Documents: req.Documents,
	})
	if err != nil {
		h.writeReportError(w, r, err)
		return
	}

	if format == reportFormatJSON {
		notes, sources := draft.Notes, draft.Sources
		if notes == nil {
			notes = []string{}
		}
		writeJSON(w, http.StatusOK, ReportResponse{Markdown: draft.Markdown(), Notes: notes, Sources: sources})
		return
	}

	file, err := h.service.Render(draft, format)
	if err != nil {
		h.writeReportError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", file.ContentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+reportFileName(req.ProgramID, req.From, req.To, file.Extension)+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(file.Data) // the status is sent; a write error is a gone client
}

func (h *ReportHandler) writeReportError(w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()
	switch {
	case errors.Is(err, reporting.ErrInvalidInput), errors.Is(err, reporting.ErrUnknownFormat):
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())

	case errors.Is(err, outbound.ErrProgramNotFound):
		writeError(w, r, http.StatusNotFound, codeProgramNotFound, "no program with that id")

	case errors.Is(err, outbound.ErrDocumentNotFound):
		writeError(w, r, http.StatusNotFound, codeDocumentNotFound, err.Error())

	case errors.Is(err, reporting.ErrNoDocuments):
		writeError(w, r, http.StatusUnprocessableEntity, codeNoDocuments,
			"the program has no readable activity documents to write the report from")

	case writeCommonError(h.logger, w, r, err):

	case errors.Is(err, reporting.ErrNarrativeRejected):
		h.logger.WarnContext(ctx, "report narrative rejected", "error", err)
		writeError(w, r, http.StatusBadGateway, codeNarrativeFailed,
			"the model did not produce a usable narrative; try again")

	default:
		h.logger.ErrorContext(ctx, "report failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "internal server error")
	}
}

// reportFileName builds a safe download name. The program id came from the
// request, so anything outside letters, digits, '-' and '_' is dropped.
func reportFileName(programID, from, to, ext string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return -1
	}, programID)
	if clean == "" {
		clean = "program"
	}
	return "report-" + clean + "-" + from + "-" + to + "." + ext
}
