// Package prospecting is the pilot 2 use case: from one Human Initiative
// program to a shortlist of companies that may fund it, with the reasons,
// the evidence and a draft approach email for each.
//
// Which companies, and why, comes from the evidence index and its explainable
// score. The model only words the emails, one call per company, and each
// email is checked against the evidence it cites before it is accepted;
// what the checks cannot confirm becomes a note for the reviewer.
package prospecting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/outreach"
	"github.com/HumanInitiative/agent-harness-go/internal/platform/caller"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

var (
	// ErrInvalidInput means the program cannot be matched as given.
	ErrInvalidInput = errors.New("invalid input")
	// ErrNoProspects means no indexed company fits the program. The index
	// may be empty or not yet crawled for this field.
	ErrNoProspects = errors.New("no company in the index fits this program")
	// ErrUnknownFormat means no renderer exists for the requested format.
	ErrUnknownFormat = errors.New("unknown shortlist format")
)

// Config bounds one run.
type Config struct {
	// DefaultLimit and MaxLimit are how many companies a shortlist holds.
	DefaultLimit int
	MaxLimit     int
	// Concurrency is how many emails are drafted at once.
	Concurrency int
	// Attempts is how many times the model may try one email.
	Attempts int
	// MaxEvidence and MaxExcerptChars bound what is sent to the model per
	// company.
	MaxEvidence     int
	MaxExcerptChars int
	// Price of model tokens, in rupiah per million. Zero means unknown, and
	// the trace then carries no cost estimate.
	InputRupiahPerMillion  float64
	OutputRupiahPerMillion float64
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// DefaultConfig is what main starts from.
func DefaultConfig() Config {
	return Config{DefaultLimit: 10, MaxLimit: 25, Concurrency: 4, Attempts: 2, MaxEvidence: 6, MaxExcerptChars: 500}
}

// Service builds shortlists.
type Service struct {
	prospects outbound.ProspectSource
	model     outbound.ModelPort
	renderers map[string]outbound.DraftRenderer
	cfg       Config
	log       *slog.Logger
}

// NewService wires the use case. renderers may be empty (then only the
// structured result is available).
func NewService(prospects outbound.ProspectSource, model outbound.ModelPort, renderers []outbound.DraftRenderer, cfg Config, log *slog.Logger) (*Service, error) {
	if prospects == nil || model == nil {
		return nil, errors.New("prospecting: prospect source and model are required")
	}
	if cfg.DefaultLimit < 1 || cfg.MaxLimit < cfg.DefaultLimit || cfg.Concurrency < 1 || cfg.Attempts < 1 ||
		cfg.MaxEvidence < 1 || cfg.MaxExcerptChars < 1 {
		return nil, errors.New("prospecting: invalid Config")
	}
	if cfg.InputRupiahPerMillion < 0 || cfg.OutputRupiahPerMillion < 0 {
		return nil, errors.New("prospecting: token prices must not be negative")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	byFormat := map[string]outbound.DraftRenderer{}
	for _, r := range renderers {
		if _, dup := byFormat[r.Format()]; dup {
			return nil, fmt.Errorf("prospecting: two renderers for format %q", r.Format())
		}
		byFormat[r.Format()] = r
	}
	return &Service{prospects: prospects, model: model, renderers: byFormat, cfg: cfg, log: log}, nil
}

// Input selects the shortlist.
type Input struct {
	Program outreach.Program
	// Limit is the number of companies; 0 takes the default.
	Limit int
	// Emails asks for a draft email per company. Without it the shortlist
	// costs no model call at all.
	Emails bool
}

// Run builds the shortlist.
func (s *Service) Run(ctx context.Context, in Input) (outreach.Shortlist, error) {
	start := s.cfg.Now()
	program := in.Program.Clean()
	if err := program.Validate(); err != nil {
		return outreach.Shortlist{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	limit := in.Limit
	switch {
	case limit < 0 || limit > s.cfg.MaxLimit:
		return outreach.Shortlist{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidInput, s.cfg.MaxLimit)
	case limit == 0:
		limit = s.cfg.DefaultLimit
	}

	found, err := s.prospects.Prospects(ctx, program, limit)
	if err != nil {
		return outreach.Shortlist{}, fmt.Errorf("find prospects: %w", err)
	}
	if len(found.Candidates) == 0 {
		return outreach.Shortlist{}, ErrNoProspects
	}

	list := outreach.Shortlist{Program: program, GeneratedAt: start}
	for _, c := range found.Candidates {
		list.Entries = append(list.Entries, outreach.Entry{Candidate: c, Notes: candidateNotes(c)})
	}
	list.Trace = outreach.Trace{StartedAt: start, Candidates: found.Fitting}

	if in.Emails {
		if err := s.draftEmails(ctx, &list); err != nil {
			return outreach.Shortlist{}, err
		}
	}

	list.Trace.DurationMS = s.cfg.Now().Sub(start).Milliseconds()
	for _, e := range list.Trace.Emails {
		list.Trace.InputTokens += e.InputTokens
		list.Trace.OutputTokens += e.OutputTokens
	}
	if s.cfg.InputRupiahPerMillion > 0 || s.cfg.OutputRupiahPerMillion > 0 {
		cost := float64(list.Trace.InputTokens)/1e6*s.cfg.InputRupiahPerMillion +
			float64(list.Trace.OutputTokens)/1e6*s.cfg.OutputRupiahPerMillion
		list.Trace.EstimatedCostRupiah = &cost
	}

	drafted := 0
	for _, e := range list.Entries {
		if e.Email != nil {
			drafted++
		}
	}
	// The audit trail: who asked, for what, and what it cost. No email text.
	s.log.InfoContext(ctx, "shortlist built",
		"key_id", caller.KeyID(ctx),
		"program", program.Name,
		"fitting", found.Fitting,
		"shortlisted", len(list.Entries),
		"emails_drafted", drafted,
		"input_tokens", list.Trace.InputTokens,
		"output_tokens", list.Trace.OutputTokens,
		"duration_ms", list.Trace.DurationMS)
	return list, nil
}

// draftEmails drafts one email per entry, a few at a time. A company whose
// email cannot be produced keeps its place with a note; the run fails only
// when the model is unavailable or the caller has gone, because then no
// other email can succeed either.
func (s *Service) draftEmails(ctx context.Context, list *outreach.Shortlist) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		fatalErr error
		sem      = make(chan struct{}, s.cfg.Concurrency)
		traces   = make([]outreach.EmailTrace, len(list.Entries))
	)
	for i := range list.Entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			email, notes, trace, err := s.draftEmail(ctx, list.Program, list.Entries[i].Candidate)
			mu.Lock()
			defer mu.Unlock()
			traces[i] = trace
			if err != nil {
				if isFatal(err) {
					if fatalErr == nil {
						fatalErr = err
						cancel()
					}
					return
				}
				list.Entries[i].Notes = append(list.Entries[i].Notes, "Draf email gagal dibuat: "+err.Error())
				return
			}
			list.Entries[i].Email = &email
			list.Entries[i].Notes = append(list.Entries[i].Notes, notes...)
		}()
	}
	wg.Wait()
	if fatalErr != nil {
		return fatalErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, t := range traces {
		if t.Company != "" {
			list.Trace.Emails = append(list.Trace.Emails, t)
		}
	}
	return nil
}

func isFatal(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, outbound.ErrModelUnavailable)
}

// candidateNotes are what a person should know about a company before
// acting on it, from the index alone.
func candidateNotes(c outreach.Candidate) []string {
	var notes []string
	if !c.Verified {
		notes = append(notes, "Perusahaan belum diverifikasi orang (status baru). Pastikan datanya benar sebelum menghubungi.")
	}
	if c.Stale {
		reason := c.StaleReason
		if reason == "" {
			reason = "tidak ada halaman yang mengonfirmasi datanya belakangan ini"
		}
		notes = append(notes, "Data sudah lama: "+reason+". Cek ulang halaman CSR perusahaan sebelum menghubungi.")
	}
	if c.LowConfidence {
		notes = append(notes, "Keyakinan ekstraksi profil rendah. Baca halaman sumbernya sendiri.")
	}
	if c.ProposalChannel == "" {
		notes = append(notes, "Kanal pengajuan proposal tidak diketahui; cari tahu cara menghubungi perusahaan ini.")
	}
	if c.SeekingPartners == "false" {
		notes = append(notes, "Halaman perusahaan menyebut mereka tidak sedang membuka kemitraan.")
	}
	if len(c.Programs) == 0 {
		notes = append(notes, "Belum ada program CSR aktif yang tercatat untuk perusahaan ini.")
	}
	return notes
}

// Rendered is a shortlist turned into a file.
type Rendered struct {
	Data        []byte
	ContentType string
	Extension   string
}

// Render turns a shortlist into the requested format.
func (s *Service) Render(list outreach.Shortlist, format string) (Rendered, error) {
	r, ok := s.renderers[format]
	if !ok {
		return Rendered{}, fmt.Errorf("%w: %q", ErrUnknownFormat, format)
	}
	data, err := r.Render(list.Draft())
	if err != nil {
		return Rendered{}, fmt.Errorf("render %s: %w", format, err)
	}
	return Rendered{Data: data, ContentType: r.ContentType(), Extension: r.Extension()}, nil
}

// Formats lists the formats Render accepts, sorted.
func (s *Service) Formats() []string {
	out := make([]string, 0, len(s.renderers))
	for f := range s.renderers {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
