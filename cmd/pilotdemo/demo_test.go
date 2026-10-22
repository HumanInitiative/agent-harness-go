package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// start builds the demo on free ports and returns a helper for calling it.
func start(t *testing.T, quirk string) func(method, path, body string) (int, []byte) {
	t.Helper()
	d, err := build(context.Background(), options{
		addr: "127.0.0.1:0", mockAddr: "127.0.0.1:0",
		dbPath:       filepath.Join(t.TempDir(), "csr.db"),
		programsFile: "../../config/hi-programs.example.yaml",
		quirk:        quirk,
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(d.close)
	go func() { _ = d.server.Serve(d.listener) }()
	t.Cleanup(func() { _ = d.server.Close() })
	base := "http://" + d.listener.Addr().String()

	return func(method, path, body string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-API-Key", demoAPIKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
}

type reportResp struct {
	Markdown string   `json:"markdown"`
	Notes    []string `json:"notes"`
	Sources  []string `json:"sources"`
}

type outreachResp struct {
	Warnings     []string `json:"warnings"`
	TermCoverage []struct {
		Kind       string `json:"kind"`
		Term       string `json:"term"`
		Recognized bool   `json:"recognized"`
		Companies  int    `json:"companies"`
	} `json:"term_coverage"`
	Entries []struct {
		Company struct {
			Name     string `json:"name"`
			Verified bool   `json:"verified"`
		} `json:"company"`
		Score       int      `json:"score"`
		ConfirmedAt string   `json:"confirmed_at"`
		Stale       bool     `json:"stale"`
		Notes       []string `json:"notes"`
		Email       *struct {
			Subject string `json:"subject"`
			Body    string `json:"body"`
		} `json:"email"`
	} `json:"entries"`
	Trace struct {
		Fitting             int      `json:"fitting"`
		InputTokens         int      `json:"input_tokens"`
		EstimatedCostRupiah *float64 `json:"estimated_cost_rupiah"`
		Emails              []struct {
			Company  string `json:"company"`
			Attempts int    `json:"attempts"`
			Outcome  string `json:"outcome"`
		} `json:"emails"`
	} `json:"trace"`
}

const q1Report = `{"program_id":"pangan-jakarta","from":"2026-01-01","to":"2026-03-31"}`

func report(t *testing.T, call func(string, string, string) (int, []byte), body string) reportResp {
	t.Helper()
	status, b := call("POST", "/api/v1/reports", body)
	if status != 200 {
		t.Fatalf("status %d: %s", status, b)
	}
	var r reportResp
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func outreach(t *testing.T, call func(string, string, string) (int, []byte), body string) outreachResp {
	t.Helper()
	status, b := call("POST", "/api/v1/outreach", body)
	if status != 200 {
		t.Fatalf("status %d: %s", status, b)
	}
	var r outreachResp
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDemo_ReportRunsEndToEndWithoutNotes(t *testing.T) {
	call := start(t, "")
	r := report(t, call, q1Report)
	for _, want := range []string{"Rp 240.000.000", "Rp 221.000.000", "[DEMO]", "1.320"} {
		if !strings.Contains(r.Markdown, want) {
			t.Errorf("report lacks %q\n%s", want, r.Markdown)
		}
	}
	if len(r.Notes) != 0 || len(r.Sources) == 0 {
		t.Errorf("a quote-only fake model must pass the number check: notes = %v, sources = %v", r.Notes, r.Sources)
	}

	// Another period gives other figures: the donation API is aggregating.
	feb := report(t, call, `{"program_id":"pangan-jakarta","from":"2026-02-01","to":"2026-02-28"}`)
	if !strings.Contains(feb.Markdown, "Rp 55.000.000") {
		t.Errorf("February figures:\n%s", feb.Markdown)
	}

	status, b := call("POST", "/api/v1/reports", `{"program_id":"pangan-jakarta","from":"2026-01-01","to":"2026-03-31","format":"docx"}`)
	if _, err := zip.NewReader(bytes.NewReader(b), int64(len(b))); status != 200 || err != nil {
		t.Errorf("docx: %d %v", status, err)
	}
	if status, _ := call("POST", "/api/v1/reports", `{"program_id":"ghost","from":"2026-01-01","to":"2026-03-31"}`); status != 404 {
		t.Errorf("unknown program: %d", status)
	}
}

func TestDemo_ShortlistForAnEducationProgram(t *testing.T) {
	r := outreach(t, start(t, ""), `{"program_id":"beasiswa-anak-desa"}`)

	var names []string
	for _, e := range r.Entries {
		names = append(names, e.Company.Name)
	}
	want := "PT Terang Energi Tbk, PT Nusa Perbankan Tbk, PT Lama Sekali Tbk"
	if strings.Join(names, ", ") != want {
		t.Fatalf("shortlist = %v, want %s (health, water and plantation companies do not fit education)", names, want)
	}
	best, middle, stale := r.Entries[0], r.Entries[1], r.Entries[2]
	if best.Score <= middle.Score || !best.Company.Verified || len(best.Notes) != 0 {
		t.Errorf("best: %+v", best)
	}
	if middle.Company.Verified || !strings.Contains(strings.Join(middle.Notes, " "), "belum diverifikasi") {
		t.Errorf("middle: %+v", middle)
	}
	if !stale.Stale || !strings.Contains(strings.Join(stale.Notes, " "), "Data sudah lama") {
		t.Errorf("stale: %+v", stale)
	}
	// Its data must look as old as it is, not "confirmed today".
	if stale.ConfirmedAt == "" || stale.ConfirmedAt >= time.Now().UTC().AddDate(0, 0, -150).Format("2006-01-02") {
		t.Errorf("a stale company must be dated long ago, got %q", stale.ConfirmedAt)
	}
	if best.ConfirmedAt != time.Now().UTC().Format("2006-01-02") {
		t.Errorf("a current company was confirmed today, got %q", best.ConfirmedAt)
	}
	for _, e := range r.Entries {
		if e.Email == nil || !strings.Contains(e.Email.Body, "[DEMO]") || !strings.Contains(e.Email.Body, "[Nama pengirim]") {
			t.Errorf("%s: email = %+v", e.Company.Name, e.Email)
		}
	}
	if r.Trace.Fitting != 3 || r.Trace.InputTokens == 0 || r.Trace.EstimatedCostRupiah == nil || len(r.Trace.Emails) != 3 {
		t.Errorf("trace = %+v", r.Trace)
	}
	for _, e := range r.Trace.Emails {
		if e.Outcome != "ok" || e.Attempts != 1 {
			t.Errorf("trace = %+v", e)
		}
	}
	if r.Entries[0].Email != nil && len(best.Notes) == 0 && strings.Contains(r.Entries[0].Email.Body, "direktur@") {
		t.Error("no quirk, no invented address")
	}
}

func TestDemo_ShortlistForAWaterProgramAndBadCriteria(t *testing.T) {
	call := start(t, "")
	water := outreach(t, call, `{"program_id":"air-bersih-dusun","emails":false}`)
	if len(water.Entries) < 2 || water.Entries[0].Company.Name != "PT Tirta Jaya Utama" {
		t.Errorf("water shortlist: %+v", water.Entries)
	}
	if water.Entries[0].Email != nil {
		t.Error("emails=false must draft nothing")
	}

	typo := outreach(t, call, `{"program":{"name":"X","focus_areas":["pendidikan","olahraga"],"regions":["Papua"]},"emails":false}`)
	joined := strings.Join(typo.Warnings, " ")
	if !strings.Contains(joined, `"olahraga"`) {
		t.Errorf("warnings = %v", typo.Warnings)
	}
	// Papua is covered, but only by the one nationwide company: no warning,
	// and the coverage says how thin it is.
	if strings.Contains(joined, `"Papua"`) {
		t.Errorf("a nationwide company covers every region: %v", typo.Warnings)
	}
	for _, tc := range typo.TermCoverage {
		if tc.Term == "Papua" && tc.Companies != 1 {
			t.Errorf("Papua coverage = %+v, want exactly the nationwide company", tc)
		}
		if tc.Term == "olahraga" && (tc.Recognized || tc.Companies != 0) {
			t.Errorf("olahraga coverage = %+v", tc)
		}
	}
	status, b := call("POST", "/api/v1/outreach", `{"program":{"name":"X","focus_areas":["olahraga"]},"emails":false}`)
	if status != 422 || !strings.Contains(string(b), "no_prospects") || !strings.Contains(string(b), "olahraga") {
		t.Errorf("an unfit program must say why: %d %s", status, b)
	}
}

func TestDemo_QuirkInventNumber(t *testing.T) {
	call := start(t, quirkInventNumber)
	if r := report(t, call, q1Report); len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "900") {
		t.Errorf("report notes = %v, want the invented 900 flagged", r.Notes)
	}
	o := outreach(t, call, `{"program_id":"beasiswa-anak-desa"}`)
	for _, e := range o.Entries {
		if !strings.Contains(strings.Join(e.Notes, " "), "450") {
			t.Errorf("%s: notes = %v, want the invented 450 flagged", e.Company.Name, e.Notes)
		}
	}
}

func TestDemo_QuirkBadJSONOnceIsRetried(t *testing.T) {
	call := start(t, quirkBadJSONOnce)
	if r := report(t, call, q1Report); !strings.Contains(r.Markdown, "[DEMO]") {
		t.Errorf("the report must succeed on the second try:\n%s", r.Markdown)
	}
	o := outreach(t, call, `{"program_id":"beasiswa-anak-desa"}`)
	for _, e := range o.Trace.Emails {
		if e.Attempts != 2 || e.Outcome != "ok" {
			t.Errorf("trace = %+v, want 2 attempts and ok", e)
		}
	}
}

func TestDemo_QuirkFakeEmailIsRejectedAndRetried(t *testing.T) {
	o := outreach(t, start(t, quirkFakeEmail), `{"program_id":"beasiswa-anak-desa"}`)
	for _, e := range o.Trace.Emails {
		if e.Attempts != 2 || e.Outcome != "ok" {
			t.Errorf("trace = %+v, want 2 attempts and ok", e)
		}
	}
	for _, e := range o.Entries {
		if e.Email == nil || strings.Contains(e.Email.Body, "rahasia.example") {
			t.Errorf("%s: the invented address must not survive: %+v", e.Company.Name, e.Email)
		}
	}
}

func TestDemo_PageChatAndUnknownQuirk(t *testing.T) {
	call := start(t, "")
	if status, b := call("GET", "/reports", ""); status != 200 || !strings.Contains(string(b), "Draf Laporan Donasi") {
		t.Errorf("page: %d", status)
	}
	status, b := call("POST", "/api/v1/chat", `{"message":"halo"}`)
	if status != 200 || !strings.Contains(string(b), "[DEMO]") {
		t.Errorf("chat: %d %s", status, b)
	}
	if status, _ := call("GET", "/api/v1/outreach/vocabulary", ""); status != 200 {
		t.Errorf("vocabulary: %d", status)
	}

	_, err := build(context.Background(), options{
		addr: "127.0.0.1:0", mockAddr: "127.0.0.1:0", dbPath: filepath.Join(t.TempDir(), "x.db"),
		programsFile: "../../config/hi-programs.example.yaml", quirk: "nonsense",
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err == nil || !strings.Contains(err.Error(), "unknown -quirk") {
		t.Errorf("an unknown quirk must be refused: %v", err)
	}
}
