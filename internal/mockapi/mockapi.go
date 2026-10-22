// Package mockapi is a stand-in for Human Initiative's donation data API,
// until the real endpoints exist. It serves two things the report feature
// reads over HTTP: donation figures for a program and period, and the
// program's activity documents. The data is invented, but period filtering
// and aggregation are real, so the report flow can be exercised end to end.
//
// The contract is in docs/donation-data-api.md. The real API only has to
// match it (or the adapter in internal/adapters/outbound/reportapi is
// adjusted to the real one).
package mockapi

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/reportdocx"
	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
)

//go:embed fixtures
var fixtures embed.FS

const dateLayout = "2006-01-02"

// Options configure a Server.
type Options struct {
	// APIKey, when set, must be sent in the X-API-Key header.
	APIKey string
	// Now stamps "as_of" on figures. Nil means time.Now.
	Now func() time.Time
}

// Server implements http.Handler.
type Server struct {
	opts     Options
	programs []program
	docs     map[string][]document // by program id, sorted by name
	mux      *http.ServeMux
}

type program struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Transactions  []transaction `json:"transactions"`
	Beneficiaries []beneficiary `json:"beneficiaries"`
}

type transaction struct {
	Date   string `json:"date"`
	Kind   string `json:"kind"` // "received" or "disbursed"
	Label  string `json:"label"`
	Rupiah int64  `json:"rupiah"`
}

type beneficiary struct {
	Date   string `json:"date"`
	Group  string `json:"group"`
	People int    `json:"people"`
}

type document struct {
	name        string
	contentType string
	data        []byte
}

// New loads the built-in fixtures.
func New(opts Options) (*Server, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	raw, err := fixtures.ReadFile("fixtures/programs.json")
	if err != nil {
		return nil, err
	}
	s := &Server{opts: opts, docs: map[string][]document{}, mux: http.NewServeMux()}
	if err := json.Unmarshal(raw, &s.programs); err != nil {
		return nil, fmt.Errorf("mockapi: fixtures/programs.json: %w", err)
	}
	for _, p := range s.programs {
		docs, err := loadDocuments(p)
		if err != nil {
			return nil, err
		}
		s.docs[p.ID] = docs
	}

	s.mux.HandleFunc("GET /v1/programs", s.listPrograms)
	s.mux.HandleFunc("GET /v1/programs/{id}/report-data", s.reportData)
	s.mux.HandleFunc("GET /v1/programs/{id}/documents", s.listDocuments)
	s.mux.HandleFunc("GET /v1/programs/{id}/documents/{name}", s.getDocument)
	return s, nil
}

// ServeHTTP checks the API key, then routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.opts.APIKey != "" &&
		subtle.ConstantTimeCompare([]byte(r.Header.Get("X-API-Key")), []byte(s.opts.APIKey)) != 1 {
		writeError(w, http.StatusUnauthorized, "missing or invalid X-API-Key")
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) listPrograms(w http.ResponseWriter, _ *http.Request) {
	type item struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := make([]item, 0, len(s.programs))
	for _, p := range s.programs {
		out = append(out, item{ID: p.ID, Name: p.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) find(id string) (program, bool) {
	for _, p := range s.programs {
		if p.ID == id {
			return p, true
		}
	}
	return program{}, false
}

type line struct {
	Label  string `json:"label"`
	Rupiah int64  `json:"rupiah"`
}

type groupLine struct {
	Group  string `json:"group"`
	People int    `json:"people"`
}

type reportDataResponse struct {
	Program struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"program"`
	Period struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"period"`
	AsOf          string      `json:"as_of"`
	Received      []line      `json:"received"`
	Disbursed     []line      `json:"disbursed"`
	Beneficiaries []groupLine `json:"beneficiaries"`
}

func (s *Server) reportData(w http.ResponseWriter, r *http.Request) {
	p, ok := s.find(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such program")
		return
	}
	from, err := time.Parse(dateLayout, r.URL.Query().Get("from"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "from must be a date like 2026-01-01")
		return
	}
	to, err := time.Parse(dateLayout, r.URL.Query().Get("to"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "to must be a date like 2026-03-31")
		return
	}
	if to.Before(from) {
		writeError(w, http.StatusBadRequest, "to is before from")
		return
	}
	in := func(date string) bool { return date >= from.Format(dateLayout) && date <= to.Format(dateLayout) }

	var resp reportDataResponse
	resp.Program.ID, resp.Program.Name = p.ID, p.Name
	resp.Period.From, resp.Period.To = from.Format(dateLayout), to.Format(dateLayout)
	resp.AsOf = s.opts.Now().UTC().Format(time.RFC3339)

	received, disbursed := map[string]int64{}, map[string]int64{}
	for _, t := range p.Transactions {
		if !in(t.Date) {
			continue
		}
		if t.Kind == "received" {
			received[t.Label] += t.Rupiah
		} else {
			disbursed[t.Label] += t.Rupiah
		}
	}
	people := map[string]int{}
	for _, b := range p.Beneficiaries {
		if in(b.Date) {
			people[b.Group] += b.People
		}
	}
	resp.Received, resp.Disbursed = lines(received), lines(disbursed)
	resp.Beneficiaries = make([]groupLine, 0, len(people))
	for _, g := range sortedKeys(people) {
		resp.Beneficiaries = append(resp.Beneficiaries, groupLine{Group: g, People: people[g]})
	}
	writeJSON(w, http.StatusOK, resp)
}

func lines(m map[string]int64) []line {
	out := make([]line, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, line{Label: k, Rupiah: m[k]})
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (s *Server) listDocuments(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.find(id); !ok {
		writeError(w, http.StatusNotFound, "no such program")
		return
	}
	type item struct {
		Name        string `json:"name"`
		ContentType string `json:"content_type"`
		Size        int    `json:"size"`
	}
	out := make([]item, 0, len(s.docs[id]))
	for _, d := range s.docs[id] {
		out = append(out, item{Name: d.name, ContentType: d.contentType, Size: len(d.data)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getDocument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.find(id); !ok {
		writeError(w, http.StatusNotFound, "no such program")
		return
	}
	name := r.PathValue("name")
	for _, d := range s.docs[id] {
		if d.name == name {
			w.Header().Set("Content-Type", d.contentType)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = w.Write(d.data)
			return
		}
	}
	writeError(w, http.StatusNotFound, "no such document")
}

// loadDocuments reads the program's embedded files and adds a generated
// .docx, so every format the report reads is served.
func loadDocuments(p program) ([]document, error) {
	dir := path.Join("fixtures/documents", p.ID)
	entries, err := fs.ReadDir(fixtures, dir)
	if err != nil {
		return nil, fmt.Errorf("mockapi: %s: %w", dir, err)
	}
	var docs []document
	for _, e := range entries {
		data, err := fixtures.ReadFile(path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		docs = append(docs, document{name: e.Name(), contentType: contentTypeOf(e.Name()), data: data})
	}
	if p.ID == "pangan-jakarta" {
		data, err := fieldNotesDocx()
		if err != nil {
			return nil, err
		}
		docs = append(docs, document{name: "catatan-lapangan-maret-2026.docx", contentType: reportdocx.ContentType, data: data})
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].name < docs[j].name })
	return docs, nil
}

// fieldNotesDocx is a short Word document of field notes for March, in the
// same numbers as the Q1 report.
func fieldNotesDocx() ([]byte, error) {
	para := func(text string) report.Block { return report.Block{Kind: report.BlockParagraph, Text: text} }
	d := report.Draft{Blocks: []report.Block{
		{Kind: report.BlockHeading, Level: 1, Text: "Catatan Lapangan Maret 2026 - Bantuan Pangan Jakarta"},
		para("Distribusi Maret dilakukan lebih awal menjelang Ramadan. Di 12 titik, relawan membagikan paket kepada 520 keluarga dan 780 anak, jumlah terbanyak pada triwulan pertama."),
		para("Antrean di titik Pejaten Timur sempat panjang pada pagi pertama. Relawan membagi warga ke dua jalur berdasarkan nomor RW, dan pembagian selesai sebelum tengah hari."),
		para("Seorang ketua RW di Cengkareng Barat meminta jadwal distribusi dikirim lima hari sebelumnya agar warga lanjut usia bisa diantar keluarganya."),
	}}
	return reportdocx.New().Render(d)
}

func contentTypeOf(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return reportdocx.ContentType
	}
	return "application/octet-stream"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
