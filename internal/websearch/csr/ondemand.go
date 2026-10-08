package csr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// ErrLookupLimit means the hourly budget of on-demand lookups is used up.
var ErrLookupLimit = errors.New("csr: on-demand lookup limit reached")

// ErrNotACompanyName means a name cannot identify a company (too short,
// only generic words such as "Bank Indonesia Tbk").
var ErrNotACompanyName = errors.New("csr: not a usable company name")

// OnDemandOptions configures on-demand lookups. Zero values take the
// defaults noted on each field.
type OnDemandOptions struct {
	// Budget bounds one lookup; what does not finish is completed by the
	// next scheduled crawl. It must leave the model time to answer within
	// the request deadline. Default 40s.
	Budget time.Duration
	// PerHour caps lookups, each of which costs searches, page fetches and
	// a model call. Default 20.
	PerHour int
}

// OnDemand looks up a company that is not in the index yet: it records
// the company (source "on_demand", status "new"), finds its site and CSR
// pages, and extracts a profile, within a time budget.
type OnDemand struct {
	store   *Store
	crawler *Crawler
	index   *Index
	limiter *rate.Limiter
	opts    OnDemandOptions
	log     *slog.Logger
}

// NewOnDemand builds an OnDemand over a crawler whose fetcher enforces
// robots.txt.
func NewOnDemand(store *Store, crawler *Crawler, index *Index, opts OnDemandOptions, log *slog.Logger) *OnDemand {
	if opts.Budget <= 0 {
		opts.Budget = 40 * time.Second
	}
	if opts.PerHour <= 0 {
		opts.PerHour = 20
	}
	limiter := rate.NewLimiter(rate.Every(time.Hour/time.Duration(opts.PerHour)), opts.PerHour)
	return &OnDemand{store: store, crawler: crawler, index: index, limiter: limiter, opts: opts, log: log}
}

// LookupResult is what an on-demand lookup found.
type LookupResult struct {
	Prospects []Prospect
	// Complete is false when the lookup ran out of time; the company stays
	// due and the next scheduled crawl finishes it.
	Complete bool
	// Started is false when another lookup or crawl already recorded the
	// company, so nothing new was started.
	Started bool
}

// LookUp records and crawls a company that the index does not know.
func (o *OnDemand) LookUp(ctx context.Context, name string) (LookupResult, error) {
	name = strings.TrimSpace(name)
	if len([]rune(name)) < 3 || len([]rune(name)) > maxSignalNameLen ||
		(len(distinctiveWords(name)) == 0 && len(acronyms(name)) == 0) {
		return LookupResult{}, fmt.Errorf("%w: %q", ErrNotACompanyName, name)
	}
	if !o.limiter.Allow() {
		return LookupResult{}, ErrLookupLimit
	}

	id, res, err := o.store.UpsertCompany(ctx, Company{Name: name, Source: SourceOnDemand})
	if err != nil {
		return LookupResult{}, err
	}
	result := LookupResult{Started: res == Inserted}
	if result.Started {
		similar, err := o.store.SimilarCompanies(ctx, name, 3)
		if err != nil {
			return result, err
		}
		if len(similar) > 0 {
			var names []string
			for _, c := range similar {
				names = append(names, fmt.Sprintf("%s (id %d)", c.Name, c.ID))
			}
			if err := o.store.SetReviewNote(ctx, id, "dicari langsung atas permintaan; nama mirip dengan "+
				strings.Join(names, ", ")+": mungkin perusahaan yang sama atau sesama grup"); err != nil {
				return result, err
			}
		}

		company, err := o.store.Company(ctx, id)
		if err != nil {
			return result, err
		}
		budget, cancel := context.WithTimeout(ctx, o.opts.Budget)
		start := time.Now()
		_, crawlErr := o.crawler.CrawlCompany(budget, company)
		cancel()
		switch {
		case crawlErr == nil:
			result.Complete = true
		case ctx.Err() != nil:
			return result, ctx.Err() // the caller gave up
		case budget.Err() != nil:
			o.log.InfoContext(ctx, "csr on-demand lookup ran out of time; the scheduled crawl finishes it",
				"company_id", id, "budget", o.opts.Budget.String())
		default:
			o.log.WarnContext(ctx, "csr on-demand lookup failed", "company_id", id, "error", crawlErr)
		}
		o.log.InfoContext(ctx, "csr on-demand lookup", "company_id", id, "complete", result.Complete,
			"duration_ms", time.Since(start).Milliseconds())
	}

	// Read back with a fresh context: the lookup's may have expired.
	prospects, err := o.index.CheckCompany(context.WithoutCancel(ctx), name, false)
	if err != nil {
		return result, err
	}
	result.Prospects = prospects
	return result, nil
}
