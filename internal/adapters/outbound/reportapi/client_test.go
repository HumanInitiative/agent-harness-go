package reportapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
	"github.com/HumanInitiative/agent-harness-go/internal/mockapi"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func q1(program string) report.Request {
	return report.Request{ProgramID: program, Period: report.Period{From: day("2026-01-01"), To: day("2026-03-31")}}
}

func newClient(t *testing.T, h http.Handler, key string) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{BaseURL: srv.URL + "/", APIKey: key, Timeout: 5 * time.Second}, nil, quiet())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mock(t *testing.T, key string) http.Handler {
	t.Helper()
	m, err := mockapi.New(mockapi.Options{APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFigures_AgainstTheMockAPI(t *testing.T) {
	c := newClient(t, mock(t, "k"), "k")
	f, err := c.Figures(context.Background(), q1("pangan-jakarta"))
	if err != nil {
		t.Fatalf("Figures: %v", err)
	}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	if f.Program.Name != "Bantuan Pangan Jakarta" || f.Period != q1("x").Period {
		t.Errorf("program/period: %+v", f)
	}
	if f.TotalReceived() != 240_000_000 || f.TotalDisbursed() != 221_000_000 || f.TotalBeneficiaries() != 3350 {
		t.Errorf("totals: %d %d %d", f.TotalReceived(), f.TotalDisbursed(), f.TotalBeneficiaries())
	}
}

func TestFigures_Errors(t *testing.T) {
	c := newClient(t, mock(t, "k"), "k")
	if _, err := c.Figures(context.Background(), q1("ghost")); !errors.Is(err, outbound.ErrProgramNotFound) {
		t.Errorf("unknown program: %v", err)
	}
	if _, err := c.Figures(context.Background(), report.Request{}); err == nil {
		t.Error("invalid request must fail before any call")
	}

	wrongKey := newClient(t, mock(t, "k"), "nope")
	if _, err := wrongKey.Figures(context.Background(), q1("pangan-jakarta")); err == nil || !strings.Contains(err.Error(), "rejected the API key") {
		t.Errorf("wrong key: %v", err)
	}
}

func TestFigures_DistrustsTheAPI(t *testing.T) {
	respond := func(body string, status int) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		})
	}
	cases := map[string]http.Handler{
		"server error":    respond(`boom`, 500),
		"not json":        respond(`<html>`, 200),
		"bad as_of":       respond(`{"program":{"id":"p","name":"P"},"as_of":"yesterday"}`, 200),
		"no program":      respond(`{"as_of":"2026-07-01T08:00:00Z"}`, 200),
		"negative amount": respond(`{"program":{"id":"p","name":"P"},"as_of":"2026-07-01T08:00:00Z","received":[{"label":"x","rupiah":-5}]}`, 200),
		"redirect":        http.RedirectHandler("http://example.invalid/steal", http.StatusFound),
		"huge response":   respond(strings.Repeat("x", maxJSONBytes+10), 200),
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := newClient(t, h, "k").Figures(context.Background(), q1("p")); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestFigures_SendsTheKeyAndTheDates(t *testing.T) {
	var mu sync.Mutex
	var gotKey, gotQuery, gotPath string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotKey, gotQuery, gotPath = r.Header.Get("X-API-Key"), r.URL.RawQuery, r.URL.EscapedPath()
		mu.Unlock()
		http.NotFound(w, r)
	})
	_, _ = newClient(t, h, "the-key").Figures(context.Background(), q1("a b/c"))
	mu.Lock()
	defer mu.Unlock()
	if gotKey != "the-key" || gotQuery != "from=2026-01-01&to=2026-03-31" || gotPath != "/v1/programs/a%20b%2Fc/report-data" {
		t.Errorf("key=%q query=%q path=%q", gotKey, gotQuery, gotPath)
	}
}

func TestList_AgainstTheMockAPI(t *testing.T) {
	c := newClient(t, mock(t, ""), "")
	docs, err := c.List(context.Background(), "pangan-jakarta", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := map[string]string{}
	var names []string
	for _, d := range docs {
		names = append(names, d.Name)
		text[d.Name] = d.Text
	}
	if strings.Join(names, ",") != "catatan-lapangan-maret-2026.docx,evaluasi-triwulan-ii-2026.txt,laporan-kegiatan-q1-2026.md" {
		t.Fatalf("names = %v", names)
	}
	if !strings.Contains(text["catatan-lapangan-maret-2026.docx"], "520 keluarga dan 780 anak") {
		t.Errorf("docx text not extracted: %q", text["catatan-lapangan-maret-2026.docx"])
	}
	if !strings.Contains(text["laporan-kegiatan-q1-2026.md"], "1.320 keluarga") ||
		!strings.Contains(text["evaluasi-triwulan-ii-2026.txt"], "Tanah Tinggi") {
		t.Error("text and markdown documents must come through")
	}
}

func TestList_NamedAndMissing(t *testing.T) {
	c := newClient(t, mock(t, ""), "")
	docs, err := c.List(context.Background(), "pangan-jakarta", []string{"evaluasi-triwulan-ii-2026.txt"})
	if err != nil || len(docs) != 1 {
		t.Fatalf("docs=%v err=%v", docs, err)
	}
	if _, err := c.List(context.Background(), "pangan-jakarta", []string{"none.txt"}); !errors.Is(err, outbound.ErrDocumentNotFound) {
		t.Errorf("missing named document: %v", err)
	}
	if _, err := c.List(context.Background(), "pangan-jakarta", []string{"../x.txt"}); !errors.Is(err, outbound.ErrDocumentNotFound) {
		t.Errorf("a path is never an available document: %v", err)
	}
	if docs, err := c.List(context.Background(), "ghost", nil); err != nil || len(docs) != 0 {
		t.Errorf("unknown program lists nothing: %v %v", docs, err)
	}
	if _, err := c.List(context.Background(), "ghost", []string{"a.txt"}); !errors.Is(err, outbound.ErrDocumentNotFound) {
		t.Errorf("named document of an unknown program: %v", err)
	}
}

func TestList_OneBrokenDocumentDoesNotSinkTheRest(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/programs/p/documents":
			_, _ = io.WriteString(w, `[{"name":"ok.txt"},{"name":"gone.txt"},{"name":"rusak.docx"},{"name":"foto.png"},{"name":"../evil.txt"}]`)
		case "/v1/programs/p/documents/ok.txt":
			_, _ = io.WriteString(w, "isi")
		case "/v1/programs/p/documents/rusak.docx":
			_, _ = io.WriteString(w, "not a zip")
		default:
			http.NotFound(w, r)
		}
	})
	docs, err := newClient(t, h, "").List(context.Background(), "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	text := map[string]string{}
	for _, d := range docs {
		text[d.Name] = d.Text
	}
	if len(docs) != 3 || text["ok.txt"] != "isi" || text["gone.txt"] != "" || text["rusak.docx"] != "" {
		t.Errorf("docs = %+v (unsupported and path names must not be listed)", docs)
	}
}

func TestList_UnreachableAPI(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c, _ := New(Config{BaseURL: url, Timeout: time.Second}, nil, quiet())
	if _, err := c.List(context.Background(), "p", nil); err == nil {
		t.Error("an unreachable API must be an error, not an empty list")
	}
	if _, err := c.Figures(context.Background(), q1("p")); err == nil {
		t.Error("an unreachable API must be an error")
	}
}

func TestNew_Validation(t *testing.T) {
	for _, base := range []string{"", "donations.example.org", "ftp://x", "http://"} {
		if _, err := New(Config{BaseURL: base, Timeout: time.Second}, nil, quiet()); err == nil {
			t.Errorf("base URL %q must be refused", base)
		}
	}
	if _, err := New(Config{BaseURL: "http://x"}, nil, quiet()); err == nil {
		t.Error("a zero timeout must be refused")
	}
}

func TestPrograms(t *testing.T) {
	c := newClient(t, mock(t, ""), "")
	got, err := c.Programs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "pangan-jakarta" || got[0].Name != "Bantuan Pangan Jakarta" {
		t.Errorf("programs = %+v", got)
	}

	for name, body := range map[string]string{"not json": "<html>", "no name": `[{"id":"x","name":" "}]`} {
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
		if _, err := newClient(t, h, "").Programs(context.Background()); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if _, err := newClient(t, mock(t, "k"), "wrong").Programs(context.Background()); err == nil || !strings.Contains(err.Error(), "rejected the API key") {
		t.Errorf("wrong key: %v", err)
	}
}
