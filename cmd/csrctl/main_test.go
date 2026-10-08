package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/websearch/csr"
)

func TestNextDailyRun(t *testing.T) {
	jakarta, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ now, want string }{
		{"2026-10-08T01:00:00+07:00", "2026-10-08T02:00:00+07:00"}, // later today
		{"2026-10-08T02:00:00+07:00", "2026-10-09T02:00:00+07:00"}, // exactly now: tomorrow
		{"2026-10-08T23:30:00+07:00", "2026-10-09T02:00:00+07:00"},
		{"2026-10-07T20:00:00Z", "2026-10-09T02:00:00+07:00"}, // 03:00 WIB on the 8th
		{"2026-12-31T18:30:00Z", "2027-01-01T02:00:00+07:00"}, // 01:30 WIB on 1 January
	}
	for _, c := range cases {
		now, _ := time.Parse(time.RFC3339, c.now)
		want, _ := time.Parse(time.RFC3339, c.want)
		if got := nextDailyRun(now, 2, 0, jakarta); !got.Equal(want) {
			t.Errorf("now %s: got %s, want %s", c.now, got.Format(time.RFC3339), c.want)
		}
	}
}

func TestWithCrawlLock_SecondCrawlExitsImmediately(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "csr.db")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	open := func() *csr.Store {
		s, err := csr.OpenStore(ctx, path, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	first, second := open(), open()

	ran := false
	err := withCrawlLock(ctx, first, log, func(ctx context.Context) error {
		// A second process tries to crawl while the first one runs.
		err := withCrawlLock(ctx, second, log, func(context.Context) error {
			t.Fatal("the second crawl must not run")
			return nil
		})
		if !errors.Is(err, csr.ErrLocked) || !strings.Contains(err.Error(), "another crawl is already running") ||
			!strings.Contains(err.Error(), "pid") {
			t.Fatalf("expected a clear message naming the running crawl, got %v", err)
		}
		ran = true
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("first crawl: %v (ran=%v)", err, ran)
	}

	// Released on exit: the next crawl may start at once.
	if err := withCrawlLock(ctx, second, log, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("lock not released after the first crawl: %v", err)
	}
}
