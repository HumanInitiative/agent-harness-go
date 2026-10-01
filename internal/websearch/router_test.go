package websearch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type scriptedProvider struct {
	name string
	mu   sync.Mutex
	// replies are consumed one per call; the last one repeats.
	replies []func(ctx context.Context) ([]Result, error)
	calls   int
}

func (p *scriptedProvider) Name() string { return p.name }

func (p *scriptedProvider) Search(ctx context.Context, _ string, _ int) ([]Result, error) {
	p.mu.Lock()
	i := min(p.calls, len(p.replies)-1)
	p.calls++
	reply := p.replies[i]
	p.mu.Unlock()
	return reply(ctx)
}

func (p *scriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func ok(urls ...string) func(context.Context) ([]Result, error) {
	return func(context.Context) ([]Result, error) {
		var rs []Result
		for _, u := range urls {
			rs = append(rs, Result{Title: u, URL: u})
		}
		return rs, nil
	}
}

func fail(err error) func(context.Context) ([]Result, error) {
	return func(context.Context) ([]Result, error) { return nil, err }
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestRouter(t *testing.T, clock *fakeClock, providers ...Provider) *Router {
	t.Helper()
	r, err := NewRouter(providers, RouterOptions{Now: clock.now, Cooldown: time.Minute, FailureThreshold: 2, CacheTTL: time.Millisecond},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r
}

func TestRouter_UsesFirstProviderThatAnswers(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	primary := &scriptedProvider{name: "primary", replies: []func(context.Context) ([]Result, error){ok("https://a.example")}}
	backup := &scriptedProvider{name: "backup", replies: []func(context.Context) ([]Result, error){ok("https://b.example")}}
	r := newTestRouter(t, clock, primary, backup)

	results, err := r.Search(context.Background(), "q", 5)
	if err != nil || len(results) != 1 || results[0].URL != "https://a.example" {
		t.Fatalf("got %+v, %v", results, err)
	}
	if backup.callCount() != 0 {
		t.Fatal("backup should not be called when primary answers")
	}
}

func TestRouter_FailsOverOnErrorAndOnEmpty(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	broken := &scriptedProvider{name: "broken", replies: []func(context.Context) ([]Result, error){fail(ErrBlocked)}}
	empty := &scriptedProvider{name: "empty", replies: []func(context.Context) ([]Result, error){ok()}}
	good := &scriptedProvider{name: "good", replies: []func(context.Context) ([]Result, error){ok("https://c.example")}}
	r := newTestRouter(t, clock, broken, empty, good)

	results, err := r.Search(context.Background(), "q", 5)
	if err != nil || len(results) != 1 || results[0].URL != "https://c.example" {
		t.Fatalf("got %+v, %v", results, err)
	}
}

func TestRouter_FailsOverOnProviderTimeout(t *testing.T) {
	slow := &scriptedProvider{name: "slow", replies: []func(context.Context) ([]Result, error){
		func(ctx context.Context) ([]Result, error) { <-ctx.Done(); return nil, ctx.Err() },
	}}
	good := &scriptedProvider{name: "good", replies: []func(context.Context) ([]Result, error){ok("https://fast.example")}}
	r, _ := NewRouter([]Provider{slow, good}, RouterOptions{ProviderTimeout: 20 * time.Millisecond},
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	results, err := r.Search(context.Background(), "q", 5)
	if err != nil || len(results) != 1 {
		t.Fatalf("expected failover after the slow provider timed out, got %+v, %v", results, err)
	}
}

func TestRouter_AllEmptyIsNotAnError(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	a := &scriptedProvider{name: "a", replies: []func(context.Context) ([]Result, error){ok()}}
	r := newTestRouter(t, clock, a)
	results, err := r.Search(context.Background(), "q", 5)
	if err != nil || results == nil || len(results) != 0 {
		t.Fatalf("expected empty non-nil results, got %+v, %v", results, err)
	}
}

func TestRouter_AllFailingReturnsErrNoProvider(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	a := &scriptedProvider{name: "a", replies: []func(context.Context) ([]Result, error){fail(ErrBlocked)}}
	b := &scriptedProvider{name: "b", replies: []func(context.Context) ([]Result, error){fail(ErrProviderMisconfigured)}}
	r := newTestRouter(t, clock, a, b)

	_, err := r.Search(context.Background(), "q", 5)
	if !errors.Is(err, ErrNoProvider) || !errors.Is(err, ErrBlocked) || !errors.Is(err, ErrProviderMisconfigured) {
		t.Fatalf("expected ErrNoProvider wrapping both causes, got %v", err)
	}
}

func TestRouter_CircuitBreakerSkipsThenRetries(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	flaky := &scriptedProvider{name: "flaky", replies: []func(context.Context) ([]Result, error){
		fail(ErrBlocked), fail(ErrBlocked), ok("https://recovered.example"),
	}}
	backup := &scriptedProvider{name: "backup", replies: []func(context.Context) ([]Result, error){ok("https://backup.example")}}
	r := newTestRouter(t, clock, flaky, backup)
	search := func() string {
		clock.advance(time.Second) // expire the result cache between searches
		rs, err := r.Search(context.Background(), "q", 5)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		return rs[0].URL
	}

	search()
	search() // second consecutive failure opens the circuit
	if flaky.callCount() != 2 {
		t.Fatalf("flaky called %d times, want 2", flaky.callCount())
	}
	search()
	if flaky.callCount() != 2 {
		t.Fatal("open circuit should skip the flaky provider")
	}

	clock.advance(2 * time.Minute) // past the cooldown
	if got := search(); got != "https://recovered.example" {
		t.Fatalf("after cooldown the provider should be retried, got %s", got)
	}
}

func TestRouter_CachesResults(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	a := &scriptedProvider{name: "a", replies: []func(context.Context) ([]Result, error){ok("https://a.example")}}
	r, _ := NewRouter([]Provider{a}, RouterOptions{Now: clock.now, CacheTTL: time.Hour},
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	for i := 0; i < 3; i++ {
		if _, err := r.Search(context.Background(), "  Program CSR  ", 5); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Search(context.Background(), "program csr", 5); err != nil {
		t.Fatal(err)
	}
	if a.callCount() != 1 {
		t.Fatalf("provider called %d times, want 1 (cache hit, case-insensitive)", a.callCount())
	}
}

func TestRouter_PacesUpstreamQueriesButNotCacheHits(t *testing.T) {
	a := &scriptedProvider{name: "a", replies: []func(context.Context) ([]Result, error){ok("https://a.example")}}
	r, _ := NewRouter([]Provider{a}, RouterOptions{MinInterval: 50 * time.Millisecond, CacheTTL: time.Hour},
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	start := time.Now()
	for _, q := range []string{"q1", "q2", "q3", "q1", "q2"} { // last two are cache hits
		if _, err := r.Search(context.Background(), q, 5); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	if a.callCount() != 3 {
		t.Fatalf("expected 3 upstream queries, got %d", a.callCount())
	}
	// 3 upstream queries need at least 2 gaps of 50ms; cache hits add none.
	if elapsed < 100*time.Millisecond || elapsed > 400*time.Millisecond {
		t.Fatalf("pacing off: %v for 3 upstream queries at 50ms spacing", elapsed)
	}

	// Pacing gives up when the caller does.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, _ = r.Search(context.Background(), "q4", 5) // consume the token
	if _, err := r.Search(ctx, "q5", 5); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the caller's deadline while waiting for pacing, got %v", err)
	}
}

func TestRouter_StopsWhenCallerCancels(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	a := &scriptedProvider{name: "a", replies: []func(context.Context) ([]Result, error){ok("https://a.example")}}
	r := newTestRouter(t, clock, a)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Search(ctx, "q", 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestRouter_RejectsEmptyQueryAndNoProviders(t *testing.T) {
	if _, err := NewRouter(nil, RouterOptions{}, slog.New(slog.NewTextHandler(io.Discard, nil))); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("expected ErrNoProvider, got %v", err)
	}
	clock := &fakeClock{t: time.Unix(0, 0)}
	r := newTestRouter(t, clock, &scriptedProvider{name: "a", replies: []func(context.Context) ([]Result, error){ok()}})
	if _, err := r.Search(context.Background(), "   ", 5); err == nil {
		t.Fatal("expected error for empty query")
	}
}
