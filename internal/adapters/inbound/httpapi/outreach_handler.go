package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/HumanInitiative/agent-harness-go/internal/application/prospecting"
	"github.com/HumanInitiative/agent-harness-go/internal/domain/outreach"
)

// OutreachHandler exposes prospecting.Service over HTTP.
type OutreachHandler struct {
	service *prospecting.Service
	logger  *slog.Logger
}

// NewOutreachHandler builds an OutreachHandler around the use case.
func NewOutreachHandler(service *prospecting.Service, logger *slog.Logger) *OutreachHandler {
	return &OutreachHandler{service: service, logger: logger}
}

// ServeHTTP handles POST /api/v1/outreach.
//
//	@Summary		Shortlist companies that may fund a program, with draft approach emails
//	@Description	Scores the companies in the CSR index against one Human Initiative program and returns the
//	@Description	best fits with the reasons and the evidence behind them. Unless emails is false, each company
//	@Description	also gets a draft approach email written by the model, which may only claim what cited
//	@Description	evidence says; whatever the checks cannot confirm is listed in the entry's notes. Every draft
//	@Description	needs human review. With format "docx" the response is the Word file instead of JSON.
//	@Tags			outreach
//	@Accept			json
//	@Produce		json
//	@Produce		application/vnd.openxmlformats-officedocument.wordprocessingml.document
//	@Security		ApiKeyAuth
//	@Param			request	body		OutreachRequest		true	"Outreach request"
//	@Success		200		{object}	OutreachResponse	"format json"
//	@Success		200		{file}		file				"format docx"
//	@Failure		400		{object}	ErrorResponse		"invalid_request: malformed body, missing focus area, bad limit or format"
//	@Failure		401		{object}	ErrorResponse		"unauthorized: missing or invalid X-API-Key"
//	@Failure		413		{object}	ErrorResponse		"request_too_large"
//	@Failure		422		{object}	ErrorResponse		"no_prospects: no indexed company fits the program"
//	@Failure		429		{object}	ErrorResponse		"rate_limited: see Retry-After header"
//	@Failure		500		{object}	ErrorResponse		"internal_error"
//	@Failure		503		{object}	ErrorResponse		"model_unavailable: see Retry-After header"
//	@Failure		504		{object}	ErrorResponse		"timeout"
//	@Router			/api/v1/outreach [post]
func (h *OutreachHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req OutreachRequest
	if !decodeJSON(w, r, &req, `{"program": {"name": "...", "focus_areas": ["pendidikan"]}}`) {
		return
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = reportFormatJSON
	}
	// Refuse an unknown format before spending model calls on the emails.
	if format != reportFormatJSON && !slices.Contains(h.service.Formats(), format) {
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest,
			"format must be one of: "+strings.Join(append([]string{reportFormatJSON}, h.service.Formats()...), ", "))
		return
	}

	list, err := h.service.Run(r.Context(), prospecting.Input{
		Program: outreach.Program{
			Name: req.Program.Name, Description: req.Program.Description,
			FocusAreas: req.Program.FocusAreas, Regions: req.Program.Regions, ProgramTypes: req.Program.ProgramTypes,
		},
		Limit:  req.Limit,
		Emails: req.Emails == nil || *req.Emails,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if format == reportFormatJSON {
		writeJSON(w, http.StatusOK, toOutreachResponse(list))
		return
	}
	file, err := h.service.Render(list, format)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", file.ContentType)
	w.Header().Set("Content-Disposition", `attachment; filename="calon-mitra-csr-`+list.GeneratedAt.Format(reportDateLayout)+"."+file.Extension+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(file.Data) // the status is sent; a write error is a gone client
}

func (h *OutreachHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, prospecting.ErrInvalidInput), errors.Is(err, prospecting.ErrUnknownFormat):
		writeError(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())

	case errors.Is(err, prospecting.ErrNoProspects):
		writeError(w, r, http.StatusUnprocessableEntity, codeNoProspects,
			"no company in the CSR index fits this program; the index may not cover its field yet")

	case writeCommonError(h.logger, w, r, err):

	default:
		h.logger.ErrorContext(r.Context(), "outreach failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "internal server error")
	}
}

func toOutreachResponse(l outreach.Shortlist) OutreachResponse {
	out := OutreachResponse{
		Program: OutreachProgramDTO{
			Name: l.Program.Name, Description: l.Program.Description, FocusAreas: nonNil(l.Program.FocusAreas),
			Regions: l.Program.Regions, ProgramTypes: l.Program.ProgramTypes,
		},
		GeneratedAt: l.GeneratedAt.Format(reportDateLayout),
		Entries:     make([]OutreachEntryDTO, 0, len(l.Entries)),
		Trace: OutreachTraceDTO{
			Fitting: l.Trace.Candidates, InputTokens: l.Trace.InputTokens, OutputTokens: l.Trace.OutputTokens,
			EstimatedCostRupiah: l.Trace.EstimatedCostRupiah, DurationMS: l.Trace.DurationMS,
			Emails: make([]OutreachEmailTraceDTO, 0, len(l.Trace.Emails)),
		},
	}
	for _, t := range l.Trace.Emails {
		out.Trace.Emails = append(out.Trace.Emails, OutreachEmailTraceDTO{
			Company: t.Company, Attempts: t.Attempts, InputTokens: t.InputTokens, OutputTokens: t.OutputTokens,
			DurationMS: t.DurationMS, Outcome: t.Outcome,
		})
	}
	for _, e := range l.Entries {
		c := e.Candidate
		entry := OutreachEntryDTO{
			Company: OutreachCompanyDTO{ID: c.CompanyID, Name: c.Name, Domain: c.Domain, Sector: c.Sector, Region: c.Region, Verified: c.Verified},
			Score:   c.Score, Reasons: nonNil(c.Reasons), Stale: c.Stale, ProposalChannel: c.ProposalChannel,
			Notes: nonNil(e.Notes), Programs: make([]OutreachProgramLineDTO, 0, len(c.Programs)),
		}
		if !c.ConfirmedAt.IsZero() {
			entry.ConfirmedAt = c.ConfirmedAt.Format(reportDateLayout)
		}
		for _, p := range c.Programs {
			entry.Programs = append(entry.Programs, OutreachProgramLineDTO{
				Name: p.Name, PeriodStart: p.PeriodStart, PeriodEnd: p.PeriodEnd, ProposalDeadline: p.ProposalDeadline, Status: p.Status,
			})
		}
		if e.Email != nil {
			email := &OutreachEmailDTO{Subject: e.Email.Subject, Body: e.Email.Body, Basis: []OutreachEvidenceDTO{}}
			for _, id := range e.Email.Cited {
				if ev, ok := c.EvidenceByID(id); ok {
					email.Basis = append(email.Basis, OutreachEvidenceDTO{
						ID: ev.ID, Title: ev.Title, URL: ev.URL, Date: ev.Date.Format(reportDateLayout),
						Excerpt: ev.Excerpt, DatePublished: ev.DatePublished,
					})
				}
			}
			entry.Email = email
		}
		out.Entries = append(out.Entries, entry)
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
