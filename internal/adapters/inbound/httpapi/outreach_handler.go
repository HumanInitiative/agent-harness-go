package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/HumanInitiative/agent-harness-go/internal/application/prospecting"
	"github.com/HumanInitiative/agent-harness-go/internal/domain/outreach"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
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
	if !decodeJSON(w, r, &req, `{"program": {"name": "...", "focus_areas": ["pendidikan"]}} or {"program_id": "..."}`) {
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

	in := prospecting.Input{ProgramID: req.ProgramID, Limit: req.Limit, Emails: req.Emails == nil || *req.Emails}
	if p := req.Program; p != nil {
		in.Program = outreach.Program{
			Name: p.Name, Description: p.Description, FocusAreas: p.FocusAreas, Regions: p.Regions, ProgramTypes: p.ProgramTypes,
		}
	}
	list, err := h.service.Run(r.Context(), in)
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

	case errors.Is(err, outbound.ErrProgramNotFound):
		writeError(w, r, http.StatusNotFound, codeProgramNotFound, "no program with that id in the catalog")

	case errors.Is(err, prospecting.ErrNoProspects):
		// The error carries what is wrong with the criteria, which is the
		// reason a person needs to fix the request.
		writeError(w, r, http.StatusUnprocessableEntity, codeNoProspects, err.Error())

	case writeCommonError(h.logger, w, r, err):

	default:
		h.logger.ErrorContext(r.Context(), "outreach failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, codeInternal, "internal server error")
	}
}

// vocabularyNote tells a person what the program's criteria may contain.
const vocabularyNote = "Bidang fokus dan jenis dukungan dicocokkan lewat kelompok di atas: tulis nama kelompok atau salah satu kata di bawahnya. " +
	"Istilah lain hanya cocok dengan perusahaan yang memakai kata yang sama persis. " +
	"Wilayah berupa teks bebas (nama provinsi atau kabupaten; singkatan umum seperti Jabar dikenali), dan perusahaan berskala nasional juga dihitung cocok."

// Catalog handles GET /api/v1/outreach/programs.
//
//	@Summary		List the Human Initiative programs that can be shortlisted for
//	@Description	The programs in the catalog file (OUTREACH_PROGRAMS_FILE). Send one's id as program_id
//	@Description	instead of describing the program again.
//	@Tags			outreach
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Success		200	{array}		OutreachProgramDTO
//	@Failure		401	{object}	ErrorResponse	"unauthorized: missing or invalid X-API-Key"
//	@Failure		429	{object}	ErrorResponse	"rate_limited: see Retry-After header"
//	@Router			/api/v1/outreach/programs [get]
func (h *OutreachHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	programs, err := h.service.Catalog(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := make([]OutreachProgramDTO, 0, len(programs))
	for _, p := range programs {
		out = append(out, programDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// Vocabulary handles GET /api/v1/outreach/vocabulary.
//
//	@Summary		List the fields of work a program's criteria may use
//	@Description	The focus areas the scoring understands and the words that count as each. Terms outside
//	@Description	them still work, but only match companies that use exactly the same words.
//	@Tags			outreach
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Success		200	{object}	OutreachVocabularyResponse
//	@Failure		401	{object}	ErrorResponse	"unauthorized: missing or invalid X-API-Key"
//	@Failure		429	{object}	ErrorResponse	"rate_limited: see Retry-After header"
//	@Router			/api/v1/outreach/vocabulary [get]
func (h *OutreachHandler) Vocabulary(w http.ResponseWriter, r *http.Request) {
	groups := h.service.FocusGroups()
	out := OutreachVocabularyResponse{FocusAreas: make([]FocusGroupDTO, 0, len(groups)), Note: vocabularyNote}
	for _, g := range groups {
		out.FocusAreas = append(out.FocusAreas, FocusGroupDTO{Name: g.Name, AlsoRecognized: nonNil(g.Synonyms)})
	}
	writeJSON(w, http.StatusOK, out)
}

func programDTO(p outreach.Program) OutreachProgramDTO {
	return OutreachProgramDTO{
		ID: p.ID, Name: p.Name, Description: p.Description, FocusAreas: nonNil(p.FocusAreas),
		Regions: p.Regions, ProgramTypes: p.ProgramTypes,
	}
}

func toOutreachResponse(l outreach.Shortlist) OutreachResponse {
	out := OutreachResponse{
		Program:      programDTO(l.Program),
		Warnings:     nonNil(l.Warnings),
		TermCoverage: make([]TermCoverageDTO, 0, len(l.Terms)),
		GeneratedAt:  l.GeneratedAt.Format(reportDateLayout),
		Entries:      make([]OutreachEntryDTO, 0, len(l.Entries)),
		Trace: OutreachTraceDTO{
			Fitting: l.Trace.Candidates, InputTokens: l.Trace.InputTokens, OutputTokens: l.Trace.OutputTokens,
			EstimatedCostRupiah: l.Trace.EstimatedCostRupiah, DurationMS: l.Trace.DurationMS,
			Emails: make([]OutreachEmailTraceDTO, 0, len(l.Trace.Emails)),
		},
	}
	for _, t := range l.Terms {
		out.TermCoverage = append(out.TermCoverage, TermCoverageDTO{
			Kind: t.Kind, Term: t.Term, Group: t.Group, Recognized: t.Recognized, Companies: t.Companies,
		})
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
