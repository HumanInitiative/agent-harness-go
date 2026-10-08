package csr

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLock_OneHolderAtATime(t *testing.T) {
	ctx := context.Background()
	now := t0
	path := filepath.Join(t.TempDir(), "csr.db")
	open := func() *Store {
		s, err := OpenStore(ctx, path, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	// Two stores on one file stand for two processes (a scheduled and a
	// manual crawl).
	a, b := open(), open()

	if err := a.AcquireLock(ctx, "crawl", "scheduled", time.Hour); err != nil {
		t.Fatal(err)
	}
	err := b.AcquireLock(ctx, "crawl", "manual", time.Hour)
	var held *LockHeldError
	if !errors.Is(err, ErrLocked) || !errors.As(err, &held) || held.Holder != "scheduled" || !held.ExpiresAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("second crawl must be refused with the holder named, got %v", err)
	}

	// Renewing keeps it; the lock expires only after the renewed time.
	now = t0.Add(50 * time.Minute)
	if err := a.RenewLock(ctx, "crawl", "scheduled", time.Hour); err != nil {
		t.Fatal(err)
	}
	now = t0.Add(80 * time.Minute)
	if err := b.AcquireLock(ctx, "crawl", "manual", time.Hour); !errors.Is(err, ErrLocked) {
		t.Fatalf("a renewed lock must still be held, got %v", err)
	}

	// A holder that stopped renewing (crashed) loses the lock to the next.
	now = t0.Add(3 * time.Hour)
	if err := b.AcquireLock(ctx, "crawl", "manual", time.Hour); err != nil {
		t.Fatalf("an expired lock must be taken over: %v", err)
	}
	if err := a.RenewLock(ctx, "crawl", "scheduled", time.Hour); !errors.Is(err, ErrLockLost) {
		t.Fatalf("the old holder must learn it lost the lock, got %v", err)
	}
	if err := a.ReleaseLock(ctx, "crawl", "scheduled"); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireLock(ctx, "crawl", "scheduled", time.Hour); !errors.Is(err, ErrLocked) {
		t.Fatal("releasing someone else's lock must not free it")
	}
	if err := b.ReleaseLock(ctx, "crawl", "manual"); err != nil {
		t.Fatal(err)
	}
	if err := a.AcquireLock(ctx, "crawl", "scheduled", time.Hour); err != nil {
		t.Fatalf("a released lock is free: %v", err)
	}
}
