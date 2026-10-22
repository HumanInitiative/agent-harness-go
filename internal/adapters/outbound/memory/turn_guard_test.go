package memory

import (
	"context"
	"testing"
)

func TestTurnGuard(t *testing.T) {
	g := NewTurnGuard()
	ctx := context.Background()

	release, ok, err := g.TryAcquire(ctx, "a")
	if err != nil || !ok {
		t.Fatalf("first TryAcquire = ok %v, err %v; want acquired", ok, err)
	}
	if _, ok, _ := g.TryAcquire(ctx, "a"); ok {
		t.Fatal("second TryAcquire of the same conversation succeeded")
	}
	if _, ok, _ := g.TryAcquire(ctx, "b"); !ok {
		t.Fatal("a different conversation was refused")
	}

	release()
	release() // releasing twice must be harmless
	if _, ok, _ := g.TryAcquire(ctx, "a"); !ok {
		t.Fatal("conversation still busy after release")
	}
}

func TestTurnGuardStaleReleaseDoesNotFreeANewHolder(t *testing.T) {
	g := NewTurnGuard()
	ctx := context.Background()

	first, _, _ := g.TryAcquire(ctx, "a")
	first()
	_, ok, _ := g.TryAcquire(ctx, "a") // new holder
	if !ok {
		t.Fatal("expected to acquire after release")
	}
	first() // a second call of the old release must not free the new holder
	if _, ok, _ := g.TryAcquire(ctx, "a"); ok {
		t.Fatal("old release freed the new holder's turn")
	}
}
