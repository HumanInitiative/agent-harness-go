package websearch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RouterOptions configures a Router. Zero values take the noted defaults.
type RouterOptions struct {
	// ProviderTimeout bounds one provider attempt. Default 10s.
	ProviderTimeout time.Duration
	// FailureThreshold consecutive failures open a provider's circuit.
	// Default 3.
	FailureThreshold int
	// Cooldown is how long an open circuit skips the provider before
	// letting one attempt through again. Default 1m.
	Cooldown time.Duration
	// CacheTTL and CacheSize configure the result cache. Defaults 30m, 500.
	CacheTTL  time.Duration
	CacheSize int
	// MaxResults caps the limit callers may ask for. Default 10.
	MaxResults int
	// MinInterval spaces out queries sent to providers (cache hits are
	// free). Search engines rate-limit by IP: a batch crawl sending
	// hundreds of queries in a few minutes got every upstream engine of a
	// SearXNG instance suspended. Zero means no pacing, which suits
	// interactive use.
	MinInterval time.Duration
	// Now returns the current time; nil means time.Now.
	Now     func() time.Time
	Metrics *Metrics
}

// Router searches through an ordered list of providers. It moves on to the
// next provider on error, timeout or an empty result, and temporarily skips
// a provider that keeps failing (a simple circuit breaker), so one broken
// backend costs a few failed attempts, not a timeout on every search.
type Router struct {
	providers []Provider
	opts      RouterOptions
	cache     *Cache[[]Result]
	log       *slog.Logger
	pace      *rate.Limiter // nil when MinInterval is zero

	mu       sync.Mutex
	breakers map[string]*breaker
}

type breaker struct {
	failures  int
	openUntil time.Time
}

// NewRouter builds a Router over providers, tried in the given order.
func NewRouter(providers []Provider, opts RouterOptions, log *slog.Logger) (*Router, error) {
	if len(providers) == 0 {
		return nil, fmt.Errorf("%w: at least one provider is required", ErrNoProvider)
	}
	if opts.ProviderTimeout <= 0 {
		opts.ProviderTimeout = 10 * time.Second
	}
	if opts.FailureThreshold <= 0 {
		opts.FailureThreshold = 3
	}
	if opts.Cooldown <= 0 {
		opts.Cooldown = time.Minute
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = 30 * time.Minute
	}
	if opts.CacheSize <= 0 {
		opts.CacheSize = 500
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = 10
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	r := &Router{
		providers: providers,
		opts:      opts,
		cache:     NewCache[[]Result]("search", opts.CacheTTL, opts.CacheSize, opts.Now, opts.Metrics),
		log:       log,
		breakers:  make(map[string]*breaker, len(providers)),
	}
	for _, p := range providers {
		r.breakers[p.Name()] = &breaker{}
	}
	if opts.MinInterval > 0 {
		r.pace = rate.NewLimiter(rate.Every(opts.MinInterval), 1)
	}
	return r, nil
}

// Search returns up to limit results from the first provider that produces
// any. It returns an empty slice (not an error) when every reachable
// provider worked but found nothing, and an error wrapping ErrNoProvider
// when none could be used at all.
func (r *Router) Search(ctx context.Context, query string, limit int) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("websearch: query must not be empty")
	}
	limit = max(1, min(limit, r.opts.MaxResults))

	key := strings.ToLower(query) + "|" + strconv.Itoa(limit)
	if cached, ok := r.cache.Get(key); ok {
		return cached, nil
	}

	var errs []error
	sawEmpty := false
	for _, p := range r.providers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := p.Name()
		if !r.allow(name) {
			r.log.DebugContext(ctx, "search provider skipped: circuit open", "provider", name)
			errs = append(errs, fmt.Errorf("%s: circuit open", name))
			continue
		}

		if r.pace != nil {
			if err := r.pace.Wait(ctx); err != nil {
				return nil, err
			}
		}
		start := r.opts.Now()
		attemptCtx, cancel := context.WithTimeout(ctx, r.opts.ProviderTimeout)
		results, err := p.Search(attemptCtx, query, limit)
		cancel()
		duration := r.opts.Now().Sub(start).Milliseconds()

		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err() // the caller gave up; not the provider's fault
			}
			r.recordFailure(name)
			r.opts.Metrics.Inc("search." + name + ".failure")
			r.log.WarnContext(ctx, "search provider failed, failing over",
				"provider", name, "duration_ms", duration, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}

		r.recordSuccess(name)
		if len(results) == 0 {
			sawEmpty = true
			r.opts.Metrics.Inc("search." + name + ".empty")
			r.log.InfoContext(ctx, "search provider returned no results, trying the next one",
				"provider", name, "duration_ms", duration)
			continue
		}

		r.opts.Metrics.Inc("search." + name + ".success")
		r.log.InfoContext(ctx, "search succeeded", "provider", name, "results", len(results), "duration_ms", duration)
		r.cache.Set(key, results)
		return results, nil
	}

	if sawEmpty {
		return []Result{}, nil
	}
	return nil, fmt.Errorf("%w: %w", ErrNoProvider, errors.Join(errs...))
}

// allow reports whether the provider's circuit lets a request through. Once
// the cooldown passes, requests flow again; a failure re-opens it at once
// because the failure count is still at the threshold.
func (r *Router) allow(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.opts.Now().Before(r.breakers[name].openUntil)
}

func (r *Router) recordFailure(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.breakers[name]
	b.failures++
	if b.failures >= r.opts.FailureThreshold {
		b.openUntil = r.opts.Now().Add(r.opts.Cooldown)
	}
}

func (r *Router) recordSuccess(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.breakers[name] = &breaker{}
}
