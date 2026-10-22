package httpapi

import "encoding/json"

// ChatRequest is the payload for POST /api/v1/chat. Unknown fields are
// rejected.
type ChatRequest struct {
	// ConversationID continues an existing conversation. Omit it to start a
	// new one; the response carries the generated ID back. 1-128 characters
	// of letters, digits, '-' or '_'.
	ConversationID string `json:"conversation_id,omitempty" example:"3f9c9b1e-2f0a-4a3e-9b1a-6e4b6f0c1a2b"`
	// Message is the user's message to the assistant. Required; limited to
	// MAX_MESSAGE_CHARS characters.
	Message string `json:"message" example:"What is 12 times 7?"`
}

// ToolCallDTO is one tool invocation the agent made while producing its
// reply.
type ToolCallDTO struct {
	// Name is the tool's identifier, e.g. "calculator".
	Name string `json:"name" example:"calculator"`
	// Arguments is the JSON object the model supplied to the tool.
	Arguments json.RawMessage `json:"arguments" swaggertype:"object"`
	// Result is the tool's output when the call succeeded.
	Result string `json:"result,omitempty" example:"84"`
	// Error is the tool's failure message when the call failed. A failed
	// tool call does not fail the request: the model is told about the
	// failure and decides how to proceed.
	Error string `json:"error,omitempty"`
	// DurationMS is how long the tool took to run.
	DurationMS int64 `json:"duration_ms" example:"12"`
}

// ChatResponse is the successful response body for POST /api/v1/chat.
type ChatResponse struct {
	// ConversationID identifies the conversation; send it back on the next
	// request to continue.
	ConversationID string `json:"conversation_id" example:"3f9c9b1e-2f0a-4a3e-9b1a-6e4b6f0c1a2b"`
	// Reply is the assistant's final answer.
	Reply string `json:"reply" example:"12 times 7 is 84."`
	// ToolCalls lists every tool the agent invoked, in order. Empty when it
	// answered directly.
	ToolCalls []ToolCallDTO `json:"tool_calls"`
}

// ErrorResponse is the body of every non-2xx response.
type ErrorResponse struct {
	// Code is a stable, machine-readable error identifier. Clients should
	// branch on Code, never on Error.
	Code string `json:"code" example:"invalid_request" enums:"invalid_request,unauthorized,not_found,method_not_allowed,conversation_busy,request_too_large,rate_limited,tool_turn_limit,internal_error,model_unavailable,timeout,client_closed_request,program_not_found,document_not_found,no_documents,narrative_failed,no_prospects"`
	// Error is a human-readable explanation. Its wording may change.
	Error string `json:"error" example:"invalid input: message must not be empty"`
	// RequestID identifies the request in the server logs; include it when
	// reporting a problem.
	RequestID string `json:"request_id,omitempty" example:"5b1f6c9e-7a0d-4c7e-8f5e-2d9a1c3b4e6f"`
}

// StatusResponse is the body returned by the health endpoints.
type StatusResponse struct {
	Status string `json:"status" example:"ok"`
}

// ReportRequest is the payload for POST /api/v1/reports. Unknown fields
// are rejected.
type ReportRequest struct {
	// ProgramID is the program the report is about.
	ProgramID string `json:"program_id" example:"sample-program"`
	// From and To are the first and last day of the period, YYYY-MM-DD,
	// inclusive.
	From string `json:"from" example:"2026-01-01"`
	To   string `json:"to" example:"2026-03-31"`
	// Documents restricts the report to these activity documents (file
	// names). Omit it to use every document of the program.
	Documents []string `json:"documents,omitempty" example:"laporan-q1.pdf"`
	// Format is "json" (default: the draft as Markdown in the response) or
	// "docx" (the draft as a Word file).
	Format string `json:"format,omitempty" example:"json" enums:"json,docx"`
}

// ReportResponse is the response body of POST /api/v1/reports for the json
// format.
type ReportResponse struct {
	// Markdown is the draft report.
	Markdown string `json:"markdown"`
	// Notes are things the reviewer must check before the draft goes out,
	// for example a number the activity documents do not contain. The same
	// notes are written into the draft.
	Notes []string `json:"notes"`
	// Sources are the activity documents the narrative sections cite.
	Sources []string `json:"sources"`
}

// ProgramDTO is one program a report can be made for.
type ProgramDTO struct {
	// ID is what ReportRequest.program_id takes.
	ID string `json:"id" example:"pangan-jakarta"`
	// Name is the program's name, for people.
	Name string `json:"name" example:"Bantuan Pangan Jakarta"`
}

// OutreachProgramDTO is the Human Initiative program to find funders for.
type OutreachProgramDTO struct {
	// Name of the program. Required.
	Name string `json:"name" example:"Beasiswa Anak Desa"`
	// Description helps the model word the email; at most 3000 characters.
	Description string `json:"description,omitempty" example:"Beasiswa untuk 120 anak di desa terpencil Jawa Barat."`
	// FocusAreas are the fields the program works in. At least one is
	// required: companies are scored on it.
	FocusAreas []string `json:"focus_areas" example:"pendidikan"`
	// Regions where the program works. Empty means anywhere.
	Regions []string `json:"regions,omitempty" example:"Jawa Barat"`
	// ProgramTypes are the kinds of support needed.
	ProgramTypes []string `json:"program_types,omitempty" example:"beasiswa"`
}

// OutreachRequest is the payload for POST /api/v1/outreach. Unknown fields
// are rejected.
type OutreachRequest struct {
	Program OutreachProgramDTO `json:"program"`
	// Limit is the number of companies, 1-25. Defaults to 10.
	Limit int `json:"limit,omitempty" example:"10"`
	// Emails asks for a draft email per company (the default). Set false
	// for the shortlist alone, which costs no model call.
	Emails *bool `json:"emails,omitempty" example:"true"`
	// Format is "json" (default) or "docx" (the shortlist as a Word file).
	Format string `json:"format,omitempty" example:"json" enums:"json,docx"`
}

// OutreachEvidenceDTO is an excerpt an email's claims rest on.
type OutreachEvidenceDTO struct {
	ID      int64  `json:"id" example:"11"`
	Title   string `json:"title,omitempty" example:"Program TJSL"`
	URL     string `json:"url" example:"https://contoh.co.id/csr"`
	Date    string `json:"date" example:"2026-09-01"`
	Excerpt string `json:"excerpt"`
	// DatePublished is true when Date is the publication date, false when
	// it is the date the page was retrieved.
	DatePublished bool `json:"date_published"`
}

// OutreachEmailDTO is a draft approach email.
type OutreachEmailDTO struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// Basis is the evidence the email cites.
	Basis []OutreachEvidenceDTO `json:"basis"`
}

// OutreachProgramLineDTO is a CSR program the company runs.
type OutreachProgramLineDTO struct {
	Name             string `json:"name"`
	PeriodStart      string `json:"period_start,omitempty"`
	PeriodEnd        string `json:"period_end,omitempty"`
	ProposalDeadline string `json:"proposal_deadline,omitempty"`
	Status           string `json:"status"`
}

// OutreachCompanyDTO is one company of the shortlist.
type OutreachCompanyDTO struct {
	ID     int64  `json:"id"`
	Name   string `json:"name" example:"PT Contoh Energi"`
	Domain string `json:"domain,omitempty"`
	Sector string `json:"sector,omitempty"`
	Region string `json:"region,omitempty"`
	// Verified is true only when a person confirmed the company record.
	Verified bool `json:"verified"`
}

// OutreachEntryDTO is one line of the shortlist.
type OutreachEntryDTO struct {
	Company OutreachCompanyDTO `json:"company"`
	// Score is 0-100; Reasons say why.
	Score   int      `json:"score" example:"82"`
	Reasons []string `json:"reasons"`
	// ConfirmedAt is when the company's data was last confirmed current.
	ConfirmedAt     string                   `json:"confirmed_at,omitempty" example:"2026-09-01"`
	Stale           bool                     `json:"stale"`
	Programs        []OutreachProgramLineDTO `json:"programs"`
	ProposalChannel string                   `json:"proposal_channel,omitempty"`
	// Notes are what the reviewer must check before acting on this entry.
	Notes []string `json:"notes"`
	// Email is null when none was asked for or drafting failed (Notes say
	// which).
	Email *OutreachEmailDTO `json:"email"`
}

// OutreachEmailTraceDTO records how one email was drafted.
type OutreachEmailTraceDTO struct {
	Company      string `json:"company"`
	Attempts     int    `json:"attempts"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	DurationMS   int64  `json:"duration_ms"`
	Outcome      string `json:"outcome"`
}

// OutreachTraceDTO records the run, for cost and quality tracking.
type OutreachTraceDTO struct {
	// Fitting is how many companies fit the program before the cut.
	Fitting      int `json:"fitting"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// EstimatedCostRupiah is present only when token prices are configured.
	EstimatedCostRupiah *float64                `json:"estimated_cost_rupiah,omitempty"`
	DurationMS          int64                   `json:"duration_ms"`
	Emails              []OutreachEmailTraceDTO `json:"emails"`
}

// OutreachResponse is the response body of POST /api/v1/outreach for the
// json format.
type OutreachResponse struct {
	Program     OutreachProgramDTO `json:"program"`
	GeneratedAt string             `json:"generated_at" example:"2026-10-22"`
	Entries     []OutreachEntryDTO `json:"entries"`
	Trace       OutreachTraceDTO   `json:"trace"`
}
