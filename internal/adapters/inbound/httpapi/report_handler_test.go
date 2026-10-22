package httpapi_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/inbound/httpapi"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportapi"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportdata"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportdocs"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportdocx"
	"github.com/HumanInitiative/agent-harness-go/internal/application/reporting"
	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
	"github.com/HumanInitiative/agent-harness-go/internal/mockapi"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

type reportDocs struct {
	docs []report.Document
	err  error
}

func (d reportDocs) List(context.Context, string, []string) ([]report.Document, error) {
	return d.docs, d.err
}

const reportAnswer = `{"activities": {"text": "Dibagikan 1.200 paket.", "sources": ["laporan-q1.pdf"]},
 "challenges": {"text": "Banjir.", "sources": ["laporan-q1.pdf"]}}`

var reportDoc = report.Document{Name: "laporan-q1.pdf", Text: "Dibagikan 1.200 paket. Banjir."}

func reportServer(t *testing.T, docs reportDocs, model outbound.ModelPort) testServer {
	t.Helper()
	return newTestServer(t, replyWith("unused"), func(cfg *httpapi.RouterConfig) {
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		svc, err := reporting.NewService(reportdata.NewSample(nil), docs, model,
			[]outbound.DraftRenderer{reportdocx.New()}, reporting.DefaultConfig(), log)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Reports = httpapi.NewReportHandler(svc, log)
	})
}

const reportBody = `{"program_id":"sample-program","from":"2026-01-01","to":"2026-03-31"}`

func (s testServer) report(body string) *httptest.ResponseRecorder {
	return s.do(http.MethodPost, "/api/v1/reports", testKey, body)
}

func TestReports_NotMountedUnlessConfigured(t *testing.T) {
	expectError(t, newTestServer(t, replyWith("x")).report(reportBody), http.StatusNotFound, "not_found")
}

func TestReports_RequireAnAPIKey(t *testing.T) {
	s := reportServer(t, reportDocs{docs: []report.Document{reportDoc}}, replyWith(reportAnswer))
	expectError(t, s.do(http.MethodPost, "/api/v1/reports", "", reportBody), http.StatusUnauthorized, "unauthorized")
}

func TestReports_JSON(t *testing.T) {
	s := reportServer(t, reportDocs{docs: []report.Document{reportDoc}}, replyWith(reportAnswer))
	rec := s.report(reportBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got httpapi.ReportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Markdown, "| Funds received | Rp 250.000.000 |") || !strings.Contains(got.Markdown, "Dibagikan 1.200 paket.") {
		t.Errorf("markdown:\n%s", got.Markdown)
	}
	if got.Notes == nil || len(got.Notes) != 0 || len(got.Sources) != 1 || got.Sources[0] != "laporan-q1.pdf" {
		t.Errorf("notes = %#v, sources = %#v", got.Notes, got.Sources)
	}
	if !strings.Contains(rec.Body.String(), `"notes":[]`) {
		t.Errorf("notes must be an empty list, not null: %s", rec.Body.String())
	}
}

func TestReports_DOCX(t *testing.T) {
	s := reportServer(t, reportDocs{docs: []report.Document{reportDoc}}, replyWith(reportAnswer))
	rec := s.report(`{"program_id":"sample-program","from":"2026-01-01","to":"2026-03-31","format":"DOCX"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != reportdocx.ContentType {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="report-sample-program-2026-01-01-2026-03-31.docx"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if _, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len())); err != nil {
		t.Errorf("body is not a docx (zip): %v", err)
	}
}

func TestReports_ClientErrors(t *testing.T) {
	model := &countingModel{answer: reportAnswer}
	s := reportServer(t, reportDocs{docs: []report.Document{reportDoc}}, model)
	cases := map[string]struct {
		body   string
		status int
		code   string
	}{
		"malformed":      {`not json`, 400, "invalid_request"},
		"unknown field":  {`{"program_id":"p","from":"2026-01-01","to":"2026-01-02","x":1}`, 400, "invalid_request"},
		"bad from":       {`{"program_id":"sample-program","from":"1 Jan","to":"2026-03-31"}`, 400, "invalid_request"},
		"bad to":         {`{"program_id":"sample-program","from":"2026-01-01","to":""}`, 400, "invalid_request"},
		"reversed":       {`{"program_id":"sample-program","from":"2026-04-01","to":"2026-03-31"}`, 400, "invalid_request"},
		"unknown format": {`{"program_id":"sample-program","from":"2026-01-01","to":"2026-03-31","format":"odt"}`, 400, "invalid_request"},
		"no program":     {`{"program_id":"","from":"2026-01-01","to":"2026-03-31"}`, 400, "invalid_request"},
		"unknown prog":   {`{"program_id":"ghost","from":"2026-01-01","to":"2026-03-31"}`, 404, "program_not_found"},
		"path as doc":    {`{"program_id":"sample-program","from":"2026-01-01","to":"2026-03-31","documents":["../x"]}`, 400, "invalid_request"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			expectError(t, s.report(c.body), c.status, c.code)
		})
	}
	if model.calls != 0 {
		t.Errorf("the model was called %d times for requests that cannot be served", model.calls)
	}
}

func TestReports_DocumentProblems(t *testing.T) {
	s := reportServer(t, reportDocs{err: outbound.ErrDocumentNotFound}, replyWith(reportAnswer))
	expectError(t, s.report(reportBody), http.StatusNotFound, "document_not_found")

	s = reportServer(t, reportDocs{}, replyWith(reportAnswer))
	expectError(t, s.report(reportBody), http.StatusUnprocessableEntity, "no_documents")
}

func TestReports_ModelProblems(t *testing.T) {
	docs := reportDocs{docs: []report.Document{reportDoc}}

	s := reportServer(t, docs, replyWith("I will not do that"))
	expectError(t, s.report(reportBody), http.StatusBadGateway, "narrative_failed")

	s = reportServer(t, docs, failWith(&outbound.ModelUnavailableError{}))
	rec := s.report(reportBody)
	expectError(t, rec, http.StatusServiceUnavailable, "model_unavailable")
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After missing")
	}
}

type countingModel struct {
	answer string
	calls  int
}

func (m *countingModel) Generate(context.Context, outbound.GenerateRequest) (outbound.GenerateResponse, error) {
	m.calls++
	return outbound.GenerateResponse{Output: m.answer}, nil
}

// TestReports_EndToEndWithRealAdapters runs the whole flow with only the
// model faked: a folder of activity documents in, a readable .docx out.
func TestReports_EndToEndWithRealAdapters(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sample-program")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "laporan-q1.txt"),
		[]byte("Dibagikan 1.200 paket. Hubungi relawan@example.org. Banjir."), 0o644); err != nil {
		t.Fatal(err)
	}
	model := &countingModel{answer: strings.ReplaceAll(reportAnswer, "laporan-q1.pdf", "laporan-q1.txt")}

	s := newTestServer(t, replyWith("unused"), func(cfg *httpapi.RouterConfig) {
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		docs, err := reportdocs.NewDir(root, nil, log)
		if err != nil {
			t.Fatal(err)
		}
		svc, err := reporting.NewService(reportdata.NewSample(nil), docs, model,
			[]outbound.DraftRenderer{reportdocx.New()}, reporting.DefaultConfig(), log)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Reports = httpapi.NewReportHandler(svc, log)
	})
	rec := s.report(`{"program_id":"sample-program","from":"2026-01-01","to":"2026-03-31","format":"docx"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var doc string
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			doc = string(b)
		}
	}
	for _, want := range []string{"Rp 250.000.000", "Dibagikan 1.200 paket.", "laporan-q1.txt"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docx lacks %q", want)
		}
	}
	if model.calls != 1 {
		t.Errorf("model calls = %d", model.calls)
	}
}

type recordingModel struct {
	answer string
	prompt string
}

func (m *recordingModel) Generate(_ context.Context, req outbound.GenerateRequest) (outbound.GenerateResponse, error) {
	m.prompt = req.Messages[0].Content
	return outbound.GenerateResponse{Output: m.answer}, nil
}

// TestReports_EndToEndOverHTTPWithTheMockAPI runs the whole flow against the
// mock donation API over real HTTP: figures and documents are fetched, the
// model (faked) writes from them, and the checks find nothing to flag.
func TestReports_EndToEndOverHTTPWithTheMockAPI(t *testing.T) {
	mock, err := mockapi.New(mockapi.Options{APIKey: "mock-key"})
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(mock)
	t.Cleanup(api.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client, err := reportapi.New(reportapi.Config{BaseURL: api.URL, APIKey: "mock-key", Timeout: 5 * time.Second}, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	model := &recordingModel{answer: `{
	  "activities": {"text": "Program menjangkau 1.320 keluarga dan 2.030 anak di 12 titik distribusi; pada Maret 520 keluarga dan 780 anak menerima paket.",
	                 "sources": ["laporan-kegiatan-q1-2026.md", "catatan-lapangan-maret-2026.docx"]},
	  "challenges": {"text": "Banjir pada Februari menunda distribusi di Rawa Badak selama empat hari.",
	                 "sources": ["laporan-kegiatan-q1-2026.md"]}}`}
	svc, err := reporting.NewService(client, client, model, []outbound.DraftRenderer{reportdocx.New()}, reporting.DefaultConfig(), log)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, replyWith("unused"), func(cfg *httpapi.RouterConfig) {
		cfg.Reports = httpapi.NewReportHandler(svc, log)
	})

	rec := s.report(`{"program_id":"pangan-jakarta","from":"2026-01-01","to":"2026-03-31"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got httpapi.ReportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Donation report: Bantuan Pangan Jakarta",
		"| Funds received | Rp 240.000.000 |",
		"| Funds disbursed | Rp 221.000.000 |",
		"| Balance | Rp 19.000.000 |",
		"| Keluarga | 1.320 |",
		"| Anak | 2.030 |",
		"Banjir pada Februari",
	} {
		if !strings.Contains(got.Markdown, want) {
			t.Errorf("draft lacks %q\n%s", want, got.Markdown)
		}
	}
	if len(got.Notes) != 0 {
		t.Errorf("numbers all come from the figures and documents; notes = %v", got.Notes)
	}
	// The docx field notes were downloaded and read, the contact details masked.
	if !strings.Contains(model.prompt, "520 keluarga dan 780 anak") {
		t.Error("the .docx document did not reach the model")
	}
	if strings.Contains(model.prompt, "sari.wulandari@example.org") || strings.Contains(model.prompt, "0812-5550-1234") {
		t.Error("contact details reached the model")
	}

	// Another period, another figures; and an unknown program is a 404.
	feb := s.report(`{"program_id":"pangan-jakarta","from":"2026-02-01","to":"2026-02-28"}`)
	if feb.Code != 200 || !strings.Contains(feb.Body.String(), "Rp 55.000.000") {
		t.Errorf("February: %d %s", feb.Code, feb.Body.String())
	}
	expectError(t, s.report(`{"program_id":"ghost","from":"2026-01-01","to":"2026-03-31"}`), http.StatusNotFound, "program_not_found")
}
