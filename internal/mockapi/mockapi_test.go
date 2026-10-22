package mockapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func serve(t *testing.T, opts Options) *Server {
	t.Helper()
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func get(s http.Handler, path string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func figures(t *testing.T, s http.Handler, path string) reportDataResponse {
	t.Helper()
	rec := get(s, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d: %s", path, rec.Code, rec.Body.String())
	}
	var out reportDataResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func sum(ls []line) (total int64) {
	for _, l := range ls {
		total += l.Rupiah
	}
	return
}

func TestReportData_FirstQuarter(t *testing.T) {
	now := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	s := serve(t, Options{Now: func() time.Time { return now }})
	out := figures(t, s, "/v1/programs/pangan-jakarta/report-data?from=2026-01-01&to=2026-03-31")

	if out.Program.Name != "Bantuan Pangan Jakarta" || out.AsOf != "2026-07-01T08:00:00Z" {
		t.Errorf("program/as_of: %+v", out)
	}
	if got := sum(out.Received); got != 240_000_000 {
		t.Errorf("received = %d", got)
	}
	if got := sum(out.Disbursed); got != 221_000_000 {
		t.Errorf("disbursed = %d", got)
	}
	people := map[string]int{}
	for _, b := range out.Beneficiaries {
		people[b.Group] = b.People
	}
	// The activity report for the quarter states the same two numbers.
	if people["Keluarga"] != 1320 || people["Anak"] != 2030 {
		t.Errorf("beneficiaries = %v", people)
	}
	if len(out.Received) != 3 || out.Received[0].Label != "Donatur individu" {
		t.Errorf("received lines (sorted by label): %+v", out.Received)
	}
}

func TestReportData_PeriodFiltering(t *testing.T) {
	s := serve(t, Options{})
	one := figures(t, s, "/v1/programs/pangan-jakarta/report-data?from=2026-02-01&to=2026-02-28")
	if got := sum(one.Received); got != 55_000_000 { // 40 individu + 15 daring
		t.Errorf("February received = %d", got)
	}
	// Day-level inclusivity: a transaction on the 15th is in [15th, 15th].
	day := figures(t, s, "/v1/programs/pangan-jakarta/report-data?from=2026-02-15&to=2026-02-15")
	if sum(day.Received) != 55_000_000 {
		t.Errorf("single day received = %d", sum(day.Received))
	}
	none := figures(t, s, "/v1/programs/pangan-jakarta/report-data?from=2027-01-01&to=2027-01-31")
	if len(none.Received) != 0 || len(none.Beneficiaries) != 0 {
		t.Errorf("an empty period must give empty lists: %+v", none)
	}
	if !strings.Contains(get(s, "/v1/programs/pangan-jakarta/report-data?from=2027-01-01&to=2027-01-31").Body.String(), `"received":[]`) {
		t.Error("empty lists must be [] not null")
	}
}

func TestReportData_Errors(t *testing.T) {
	s := serve(t, Options{})
	for path, want := range map[string]int{
		"/v1/programs/ghost/report-data?from=2026-01-01&to=2026-03-31":          404,
		"/v1/programs/pangan-jakarta/report-data?from=nope&to=2026-03-31":       400,
		"/v1/programs/pangan-jakarta/report-data?from=2026-01-01":               400,
		"/v1/programs/pangan-jakarta/report-data?from=2026-04-01&to=2026-03-31": 400,
		"/v1/nothing": 404,
	} {
		if got := get(s, path).Code; got != want {
			t.Errorf("%s: status %d, want %d", path, got, want)
		}
	}
}

func TestDocuments(t *testing.T) {
	s := serve(t, Options{})
	rec := get(s, "/v1/programs/pangan-jakarta/documents")
	var listed []struct {
		Name        string `json:"name"`
		ContentType string `json:"content_type"`
		Size        int    `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || rec.Code != 200 {
		t.Fatalf("list: %d %v", rec.Code, err)
	}
	names := make([]string, len(listed))
	for i, d := range listed {
		names[i] = d.Name
		if d.Size == 0 {
			t.Errorf("%s has no size", d.Name)
		}
	}
	want := "catatan-lapangan-maret-2026.docx,evaluasi-triwulan-ii-2026.txt,laporan-kegiatan-q1-2026.md"
	if strings.Join(names, ",") != want {
		t.Errorf("names = %v", names)
	}

	file := get(s, "/v1/programs/pangan-jakarta/documents/laporan-kegiatan-q1-2026.md")
	if file.Code != 200 || !strings.Contains(file.Body.String(), "1.320 keluarga") ||
		!strings.HasPrefix(file.Header().Get("Content-Type"), "text/markdown") {
		t.Errorf("md file: %d %s", file.Code, file.Header().Get("Content-Type"))
	}
	docx := get(s, "/v1/programs/pangan-jakarta/documents/catatan-lapangan-maret-2026.docx")
	if docx.Code != 200 || !strings.HasPrefix(docx.Body.String(), "PK") {
		t.Errorf("docx: %d", docx.Code)
	}
	for _, path := range []string{
		"/v1/programs/ghost/documents",
		"/v1/programs/pangan-jakarta/documents/none.pdf",
		"/v1/programs/sekolah-desa/documents/laporan-kegiatan-q1-2026.md", // another program's file
	} {
		if got := get(s, path).Code; got != 404 {
			t.Errorf("%s: %d", path, got)
		}
	}
}

func TestAPIKey(t *testing.T) {
	s := serve(t, Options{APIKey: "secret"})
	if get(s, "/v1/programs").Code != 401 || get(s, "/v1/programs", "X-API-Key", "wrong").Code != 401 {
		t.Error("a missing or wrong key must be refused")
	}
	if get(s, "/v1/programs", "X-API-Key", "secret").Code != 200 {
		t.Error("the right key must pass")
	}
	if get(serve(t, Options{}), "/v1/programs").Code != 200 {
		t.Error("no key configured means open")
	}
}

func TestListPrograms(t *testing.T) {
	rec := get(serve(t, Options{}), "/v1/programs")
	if !strings.Contains(rec.Body.String(), `"pangan-jakarta"`) || !strings.Contains(rec.Body.String(), `"sekolah-desa"`) {
		t.Errorf("programs: %s", rec.Body.String())
	}
}
