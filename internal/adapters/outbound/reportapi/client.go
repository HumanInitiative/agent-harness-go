// Package reportapi is the HTTP client for Human Initiative's donation data
// API (docs/donation-data-api.md): donation figures for a program and
// period, and the program's activity documents. One Client implements both
// outbound.ReportDataSource and outbound.ActivityDocuments.
//
// Until the real API exists, internal/mockapi serves the same contract.
package reportapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/doctext"
	"github.com/HumanInitiative/agent-harness-go/internal/domain/report"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

const (
	dateLayout = "2006-01-02"
	// maxJSONBytes bounds a JSON response; figures and document lists are
	// small.
	maxJSONBytes = 4 << 20
	// maxDocuments bounds how many documents of one program are downloaded.
	maxDocuments = 50
)

// Config locates the API.
type Config struct {
	// BaseURL is the API root, e.g. https://donations.example.org (no
	// trailing path).
	BaseURL string
	// APIKey is sent as X-API-Key when set; secret.
	APIKey string
	// Timeout bounds each HTTP call.
	Timeout time.Duration
}

// Client talks to the donation data API.
type Client struct {
	base string
	key  string
	http *http.Client
	text doctext.Extractor
	log  *slog.Logger
}

// New builds a Client. pdf may be nil (PDF documents are then listed
// without text).
func New(cfg Config, pdf doctext.PDFTextExtractor, log *slog.Logger) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("reportapi: base URL %q must be an http(s) URL", cfg.BaseURL)
	}
	if cfg.Timeout <= 0 {
		return nil, errors.New("reportapi: timeout must be positive")
	}
	return &Client{
		base: u.String(),
		key:  cfg.APIKey,
		http: &http.Client{
			Timeout: cfg.Timeout,
			// Never follow a redirect: the API key would travel with it.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		text: doctext.Extractor{PDF: pdf},
		log:  log,
	}, nil
}

type figuresJSON struct {
	Program struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"program"`
	AsOf     string `json:"as_of"`
	Received []struct {
		Label  string `json:"label"`
		Rupiah int64  `json:"rupiah"`
	} `json:"received"`
	Disbursed []struct {
		Label  string `json:"label"`
		Rupiah int64  `json:"rupiah"`
	} `json:"disbursed"`
	Beneficiaries []struct {
		Group  string `json:"group"`
		People int    `json:"people"`
	} `json:"beneficiaries"`
}

// Figures implements outbound.ReportDataSource.
func (c *Client) Figures(ctx context.Context, req report.Request) (report.Figures, error) {
	if err := req.Validate(); err != nil {
		return report.Figures{}, fmt.Errorf("report data: %w", err)
	}
	q := url.Values{"from": {req.Period.From.Format(dateLayout)}, "to": {req.Period.To.Format(dateLayout)}}
	body, status, err := c.get(ctx, c.programURL(req.ProgramID, "report-data")+"?"+q.Encode(), maxJSONBytes)
	if err != nil {
		return report.Figures{}, fmt.Errorf("report data: %w", err)
	}
	switch {
	case status == http.StatusNotFound:
		return report.Figures{}, outbound.ErrProgramNotFound
	case status != http.StatusOK:
		return report.Figures{}, statusError("report data", status, body)
	}

	var in figuresJSON
	if err := json.Unmarshal(body, &in); err != nil {
		return report.Figures{}, fmt.Errorf("report data: response is not the expected JSON: %w", err)
	}
	asOf, err := time.Parse(time.RFC3339, in.AsOf)
	if err != nil {
		return report.Figures{}, fmt.Errorf("report data: as_of %q is not an RFC 3339 time", in.AsOf)
	}
	f := report.Figures{
		Program: report.Program{ID: in.Program.ID, Name: in.Program.Name},
		Period:  req.Period,
		AsOf:    asOf,
	}
	for _, l := range in.Received {
		f.Received = append(f.Received, report.Amount{Label: l.Label, Rupiah: l.Rupiah})
	}
	for _, l := range in.Disbursed {
		f.Disbursed = append(f.Disbursed, report.Amount{Label: l.Label, Rupiah: l.Rupiah})
	}
	for _, l := range in.Beneficiaries {
		f.Beneficiaries = append(f.Beneficiaries, report.Count{Label: l.Group, People: l.People})
	}
	if err := f.Validate(); err != nil {
		return report.Figures{}, fmt.Errorf("report data: the API returned unusable figures: %w", err)
	}
	return f, nil
}

type documentJSON struct {
	Name string `json:"name"`
}

// List implements outbound.ActivityDocuments: it lists the program's
// documents, downloads them and extracts their text.
func (c *Client) List(ctx context.Context, programID string, names []string) ([]report.Document, error) {
	body, status, err := c.get(ctx, c.programURL(programID, "documents"), maxJSONBytes)
	if err != nil {
		return nil, fmt.Errorf("activity documents: %w", err)
	}
	switch {
	case status == http.StatusNotFound:
		if len(names) > 0 {
			return nil, fmt.Errorf("%w: %s", outbound.ErrDocumentNotFound, names[0])
		}
		return nil, nil
	case status != http.StatusOK:
		return nil, statusError("activity documents", status, body)
	}
	var listed []documentJSON
	if err := json.Unmarshal(body, &listed); err != nil {
		return nil, fmt.Errorf("activity documents: response is not the expected JSON: %w", err)
	}

	available := map[string]bool{}
	var all []string
	for _, d := range listed {
		if plainName(d.Name) && doctext.Supported(d.Name) && !available[d.Name] {
			available[d.Name] = true
			all = append(all, d.Name)
		}
	}
	wanted := all
	if len(names) > 0 {
		wanted = nil
		for _, n := range names {
			if !available[n] {
				return nil, fmt.Errorf("%w: %s", outbound.ErrDocumentNotFound, n)
			}
			wanted = append(wanted, n)
		}
	}
	if len(wanted) > maxDocuments {
		c.log.WarnContext(ctx, "program has more documents than one report reads", "program", programID,
			"documents", len(wanted), "read", maxDocuments)
		wanted = wanted[:maxDocuments]
	}

	docs := make([]report.Document, 0, len(wanted))
	for _, name := range wanted {
		doc := report.Document{Name: name}
		text, err := c.download(ctx, programID, name)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// One bad document must not sink the report; it is listed
			// without text and the service tells the reviewer.
			c.log.WarnContext(ctx, "activity document unreadable", "program", programID, "file", name, "error", err)
		}
		doc.Text = strings.ToValidUTF8(text, "")
		docs = append(docs, doc)
	}
	return docs, nil
}

// download fetches one document into a temporary file and reads its text.
func (c *Client) download(ctx context.Context, programID, name string) (string, error) {
	body, status, err := c.get(ctx, c.programURL(programID, "documents/"+url.PathEscape(name)), doctext.MaxFileBytes)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", statusError("document "+name, status, body)
	}
	dir, err := os.MkdirTemp("", "report-doc-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "document"+strings.ToLower(filepath.Ext(name)))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", err
	}
	return c.text.Text(ctx, path)
}

func (c *Client) programURL(programID, rest string) string {
	return c.base + "/v1/programs/" + url.PathEscape(programID) + "/" + rest
}

// get returns the body (at most limit bytes) and status of a GET.
func (c *Client) get(ctx context.Context, rawURL string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json, */*")
	if c.key != "" {
		req.Header.Set("X-API-Key", c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("donation API unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, 0, fmt.Errorf("read response: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, 0, fmt.Errorf("response is larger than %d bytes", limit)
	}
	return body, resp.StatusCode, nil
}

func statusError(what string, status int, body []byte) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("%s: the donation API rejected the API key (HTTP %d)", what, status)
	}
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	return fmt.Errorf("%s: the donation API answered HTTP %d: %s", what, status, snippet)
}

func plainName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`+"\x00")
}
