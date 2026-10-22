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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/inbound/httpapi"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/csrprospects"
	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportdocx"
	"github.com/HumanInitiative/agent-harness-go/internal/application/prospecting"
	"github.com/HumanInitiative/agent-harness-go/internal/domain/outreach"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

var outreachDay = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

type outreachSource struct {
	candidates []outreach.Candidate
}

func (s outreachSource) Prospects(context.Context, outreach.Program, int) (outbound.Prospects, error) {
	return outbound.Prospects{Candidates: s.candidates, Fitting: len(s.candidates) + 3}, nil
}

type outreachModel struct {
	calls  atomic.Int32
	output string
	err    error
}

func (m *outreachModel) Generate(context.Context, outbound.GenerateRequest) (outbound.GenerateResponse, error) {
	m.calls.Add(1)
	return outbound.GenerateResponse{Output: m.output, Usage: outbound.Usage{InputTokens: 900, OutputTokens: 150}}, m.err
}

const outreachAnswer = `{"subject": "Kemitraan beasiswa", "body": "Yth. Tim CSR, kami melihat Beasiswa Terang 2026 untuk 300 siswa. Program kami membantu 120 anak.", "cited_evidence": [11]}`

func outreachCandidate() outreach.Candidate {
	return outreach.Candidate{
		CompanyID: 7, Name: "PT Contoh Energi", Domain: "contohenergi.co.id", Verified: true, Score: 82,
		Reasons: []string{"bidang fokus cocok: pendidikan"}, ConfirmedAt: outreachDay, ProposalChannel: "csr@contohenergi.co.id",
		Programs: []outreach.CompanyProgram{{Name: "Beasiswa Terang 2026", PeriodEnd: "2026-12", Status: "active", EvidenceIDs: []int64{11}}},
		Evidence: []outreach.Evidence{{ID: 11, URL: "https://contohenergi.co.id/csr", Title: "Program TJSL", Date: outreachDay, DatePublished: true,
			Excerpt: "Beasiswa Terang 2026 untuk 300 siswa di Jawa Barat."}},
	}
}

func outreachServer(t *testing.T, source outbound.ProspectSource, model outbound.ModelPort) testServer {
	t.Helper()
	return newTestServer(t, replyWith("unused"), func(cfg *httpapi.RouterConfig) {
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		pc := prospecting.DefaultConfig()
		pc.InputRupiahPerMillion, pc.OutputRupiahPerMillion = 2000, 10000
		svc, err := prospecting.NewService(source, model, []outbound.DraftRenderer{reportdocx.New()}, pc, log)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Outreach = httpapi.NewOutreachHandler(svc, log)
	})
}

const outreachBody = `{"program":{"name":"Beasiswa Anak Desa","description":"Beasiswa untuk 120 anak.","focus_areas":["pendidikan"],"regions":["Jawa Barat"]}}`

func (s testServer) outreach(body string) *httptest.ResponseRecorder {
	return s.do(http.MethodPost, "/api/v1/outreach", testKey, body)
}

func TestOutreach_NotMountedAndNeedsAKey(t *testing.T) {
	expectError(t, newTestServer(t, replyWith("x")).outreach(outreachBody), http.StatusNotFound, "not_found")
	s := outreachServer(t, outreachSource{candidates: []outreach.Candidate{outreachCandidate()}}, &outreachModel{output: outreachAnswer})
	expectError(t, s.do(http.MethodPost, "/api/v1/outreach", "", outreachBody), http.StatusUnauthorized, "unauthorized")
}

func TestOutreach_JSON(t *testing.T) {
	model := &outreachModel{output: outreachAnswer}
	s := outreachServer(t, outreachSource{candidates: []outreach.Candidate{outreachCandidate()}}, model)
	rec := s.outreach(outreachBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got httpapi.OutreachResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Program.Name != "Beasiswa Anak Desa" || len(got.Entries) != 1 {
		t.Fatalf("response: %+v", got)
	}
	e := got.Entries[0]
	if e.Company.Name != "PT Contoh Energi" || !e.Company.Verified || e.Score != 82 || e.ConfirmedAt != "2026-09-01" ||
		e.ProposalChannel != "csr@contohenergi.co.id" || len(e.Programs) != 1 || e.Programs[0].Name != "Beasiswa Terang 2026" {
		t.Errorf("entry: %+v", e)
	}
	if e.Email == nil || e.Email.Subject != "Kemitraan beasiswa" || len(e.Email.Basis) != 1 ||
		e.Email.Basis[0].ID != 11 || e.Email.Basis[0].URL != "https://contohenergi.co.id/csr" || !e.Email.Basis[0].DatePublished {
		t.Errorf("email: %+v", e.Email)
	}
	if e.Notes == nil || len(e.Notes) != 0 {
		t.Errorf("notes must be an empty list: %#v", e.Notes)
	}
	tr := got.Trace
	if tr.Fitting != 4 || tr.InputTokens != 900 || tr.OutputTokens != 150 || tr.EstimatedCostRupiah == nil || len(tr.Emails) != 1 || tr.Emails[0].Outcome != "ok" {
		t.Errorf("trace: %+v", tr)
	}
	if model.calls.Load() != 1 {
		t.Errorf("model calls = %d, want 1", model.calls.Load())
	}
}

func TestOutreach_ShortlistAloneCostsNoModelCall(t *testing.T) {
	model := &outreachModel{output: outreachAnswer}
	s := outreachServer(t, outreachSource{candidates: []outreach.Candidate{outreachCandidate()}}, model)
	rec := s.outreach(`{"program":{"name":"P","focus_areas":["pendidikan"]},"emails":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"email":null`) || model.calls.Load() != 0 {
		t.Errorf("calls = %d, body: %s", model.calls.Load(), rec.Body.String())
	}
}

func TestOutreach_DOCX(t *testing.T) {
	s := outreachServer(t, outreachSource{candidates: []outreach.Candidate{outreachCandidate()}}, &outreachModel{output: outreachAnswer})
	rec := s.outreach(`{"program":{"name":"Beasiswa Anak Desa","focus_areas":["pendidikan"]},"format":"docx"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != reportdocx.ContentType ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), `attachment; filename="calon-mitra-csr-`) {
		t.Errorf("headers: %v", rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("not a docx: %v", err)
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
	for _, want := range []string{"Calon mitra CSR untuk: Beasiswa Anak Desa", "PT Contoh Energi", "Kemitraan beasiswa"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docx lacks %q", want)
		}
	}
}

func TestOutreach_ClientErrors(t *testing.T) {
	model := &outreachModel{output: outreachAnswer}
	s := outreachServer(t, outreachSource{candidates: []outreach.Candidate{outreachCandidate()}}, model)
	for name, body := range map[string]string{
		"malformed":      `nope`,
		"unknown field":  `{"program":{"name":"P","focus_areas":["x"]},"budget":5}`,
		"no name":        `{"program":{"name":"","focus_areas":["pendidikan"]}}`,
		"no focus":       `{"program":{"name":"P"}}`,
		"limit too high": `{"program":{"name":"P","focus_areas":["x"]},"limit":26}`,
		"negative limit": `{"program":{"name":"P","focus_areas":["x"]},"limit":-2}`,
		"unknown format": `{"program":{"name":"P","focus_areas":["x"]},"format":"odt"}`,
	} {
		t.Run(name, func(t *testing.T) { expectError(t, s.outreach(body), http.StatusBadRequest, "invalid_request") })
	}
	if model.calls.Load() != 0 {
		t.Errorf("the model was called %d times for requests that cannot be served", model.calls.Load())
	}
}

func TestOutreach_NoProspectsAndModelDown(t *testing.T) {
	expectError(t, outreachServer(t, outreachSource{}, &outreachModel{output: outreachAnswer}).outreach(outreachBody),
		http.StatusUnprocessableEntity, "no_prospects")

	down := &outreachModel{err: &outbound.ModelUnavailableError{}}
	rec := outreachServer(t, outreachSource{candidates: []outreach.Candidate{outreachCandidate()}}, down).outreach(outreachBody)
	expectError(t, rec, http.StatusServiceUnavailable, "model_unavailable")
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After missing")
	}
}

// TestOutreach_EndToEndWithARealIndex runs the whole chain with only the
// model faked: a real (in-memory) CSR index, the scoring against the
// requested program, the service, the checks and the HTTP layer.
func TestOutreach_EndToEndWithARealIndex(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	store, err := csr.OpenStore(ctx, ":memory:", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	add := func(name, focus, region, excerpt string) {
		id, _, err := store.UpsertCompany(ctx, csr.Company{Name: name, Source: csr.SourceSeed})
		if err != nil {
			t.Fatal(err)
		}
		url := "https://" + strings.ToLower(strings.Fields(name)[1]) + ".co.id/csr"
		err = store.SaveExtraction(ctx, id, csr.Extraction{
			HasCSRContent: true,
			Evidence:      []csr.Evidence{{URL: url, Title: "Program TJSL", Excerpt: excerpt, Kind: csr.KindCSRProgram, FetchedAt: now}},
			Profile: csr.Profile{
				FocusAreas: []csr.Claim{{Value: focus, EvidenceIDs: []int64{0}}}, Regions: []csr.Claim{{Value: region, EvidenceIDs: []int64{0}}},
				ModelConfidence: 0.8,
			},
			Programs: []csr.Program{{Name: "Program " + name, EvidenceIDs: []int64{0}}},
			ReadURLs: []string{url},
		})
		if err != nil {
			t.Fatal(err)
		}
		pid, err := store.UpsertPage(ctx, csr.Page{CompanyID: id, URL: url, Kind: csr.KindCSRProgram, DiscoveredVia: csr.ViaHomepage, Score: 5})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.RecordPageCheck(ctx, pid, 200, csr.PageActive, "h", 0, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	add("PT Satu Pendidikan", "pendidikan", "Jawa Barat", "Beasiswa pendidikan untuk 300 siswa di Jawa Barat")
	add("PT Dua Kesehatan", "kesehatan", "Jawa Barat", "Layanan kesehatan gratis")

	model := &outreachModel{output: `{"subject": "Kemitraan beasiswa", "body": "Yth. Tim CSR, kami melihat beasiswa untuk 300 siswa.", "cited_evidence": [1]}`}
	s := newTestServer(t, replyWith("unused"), func(cfg *httpapi.RouterConfig) {
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		svc, err := prospecting.NewService(csrprospects.New(store, 0.6, csr.IndexOptions{}), model, nil, prospecting.DefaultConfig(), log)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Outreach = httpapi.NewOutreachHandler(svc, log)
	})

	rec := s.outreach(outreachBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got httpapi.OutreachResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Company.Name != "PT Satu Pendidikan" {
		t.Fatalf("only the education company fits an education program: %+v", got.Entries)
	}
	e := got.Entries[0]
	if e.Score <= 0 || e.Email == nil || len(e.Email.Basis) != 1 || !strings.Contains(e.Email.Basis[0].Excerpt, "300 siswa") || e.Stale {
		t.Errorf("entry: %+v", e)
	}
	if !strings.Contains(strings.Join(e.Notes, " "), "belum diverifikasi") {
		t.Errorf("an unverified company must say so: %v", e.Notes)
	}
	if got.Trace.Fitting != 1 || got.Trace.InputTokens != 900 {
		t.Errorf("trace: %+v", got.Trace)
	}
}
