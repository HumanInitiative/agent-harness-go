package redisstore

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/HumanInitiative/agent-harness-go/internal/domain/conversation"
)

func newRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1, DialTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

func user(s string) conversation.Message {
	return conversation.Message{Role: conversation.RoleUser, Content: s}
}

func assistant(s string) conversation.Message {
	return conversation.Message{Role: conversation.RoleAssistant, Content: s}
}

func newStore(t *testing.T, rdb redis.Cmdable, max int, ttl time.Duration) *ConversationStore {
	t.Helper()
	s, err := NewConversationStore(rdb, ConversationOptions{MaxMessagesPerConversation: max, TTL: ttl, KeyPrefix: "t:conv:"})
	if err != nil {
		t.Fatalf("NewConversationStore: %v", err)
	}
	return s
}

func TestConversationStoreRoundTrip(t *testing.T) {
	_, rdb := newRedis(t)
	s := newStore(t, rdb, 10, time.Hour)
	ctx := context.Background()

	if got, err := s.History(ctx, "c1"); err != nil || len(got) != 0 {
		t.Fatalf("unknown conversation = %v, %v; want empty, nil", got, err)
	}

	want := []conversation.Message{user("halo \"dunia\"\n"), assistant("hai")}
	if err := s.Append(ctx, "c1", want...); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := s.History(ctx, "c1")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("History = %v, want %v", got, want)
	}

	if other, _ := s.History(ctx, "c2"); len(other) != 0 {
		t.Errorf("another conversation saw %v", other)
	}
}

func TestConversationStoreKeepsOnlyTheNewestMessages(t *testing.T) {
	_, rdb := newRedis(t)
	s := newStore(t, rdb, 3, time.Hour)
	ctx := context.Background()

	for _, m := range []string{"1", "2", "3", "4", "5"} {
		if err := s.Append(ctx, "c", user(m)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, _ := s.History(ctx, "c")
	want := []conversation.Message{user("3"), user("4"), user("5")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("History = %v, want %v", got, want)
	}
}

func TestConversationStoreExpiresWhenIdleAndRenewsOnAppend(t *testing.T) {
	mr, rdb := newRedis(t)
	s := newStore(t, rdb, 10, time.Hour)
	ctx := context.Background()

	_ = s.Append(ctx, "c", user("a"))
	mr.FastForward(40 * time.Minute)
	_ = s.Append(ctx, "c", user("b")) // renews the TTL
	mr.FastForward(40 * time.Minute)
	if got, _ := s.History(ctx, "c"); len(got) != 2 {
		t.Fatalf("History after renewed TTL = %v, want 2 messages", got)
	}
	mr.FastForward(40 * time.Minute)
	if got, _ := s.History(ctx, "c"); len(got) != 0 {
		t.Errorf("History after idle TTL = %v, want none", got)
	}
}

func TestConversationStoreRejectsBadOptions(t *testing.T) {
	_, rdb := newRedis(t)
	if _, err := NewConversationStore(rdb, ConversationOptions{MaxMessagesPerConversation: 0, TTL: time.Hour}); err == nil {
		t.Error("zero MaxMessagesPerConversation accepted")
	}
	if _, err := NewConversationStore(rdb, ConversationOptions{MaxMessagesPerConversation: 1}); err == nil {
		t.Error("zero TTL accepted")
	}
}

func TestConversationStoreSurfacesRedisErrors(t *testing.T) {
	mr, rdb := newRedis(t)
	s := newStore(t, rdb, 10, time.Hour)
	mr.Close()

	if _, err := s.History(context.Background(), "c"); err == nil {
		t.Error("History succeeded with Redis down")
	}
	if err := s.Append(context.Background(), "c", user("a")); err == nil {
		t.Error("Append succeeded with Redis down")
	}
}

func newGuard(t *testing.T, rdb redis.Cmdable, ttl time.Duration) *TurnGuard {
	t.Helper()
	g, err := NewTurnGuard(rdb, TurnGuardOptions{TTL: ttl, KeyPrefix: "t:turn:"})
	if err != nil {
		t.Fatalf("NewTurnGuard: %v", err)
	}
	return g
}

func TestTurnGuardIsSharedBetweenInstances(t *testing.T) {
	_, rdb := newRedis(t)
	a, b := newGuard(t, rdb, time.Minute), newGuard(t, rdb, time.Minute) // two instances, one Redis
	ctx := context.Background()

	release, ok, err := a.TryAcquire(ctx, "c")
	if err != nil || !ok {
		t.Fatalf("first TryAcquire = %v, %v; want acquired", ok, err)
	}
	if _, ok, _ := b.TryAcquire(ctx, "c"); ok {
		t.Fatal("a second instance claimed a turn that is running")
	}
	if _, ok, _ := b.TryAcquire(ctx, "other"); !ok {
		t.Fatal("a different conversation was refused")
	}

	release()
	if _, ok, _ := b.TryAcquire(ctx, "c"); !ok {
		t.Fatal("conversation still busy after release")
	}
}

func TestTurnGuardReleasesEvenWhenTheRequestWasCanceled(t *testing.T) {
	_, rdb := newRedis(t)
	g := newGuard(t, rdb, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	release, ok, _ := g.TryAcquire(ctx, "c")
	if !ok {
		t.Fatal("not acquired")
	}
	cancel()
	release()

	if _, ok, _ := g.TryAcquire(context.Background(), "c"); !ok {
		t.Error("claim was not released after the request context was canceled")
	}
}

func TestTurnGuardClaimExpiresIfTheHolderDies(t *testing.T) {
	mr, rdb := newRedis(t)
	g := newGuard(t, rdb, time.Minute)
	ctx := context.Background()

	if _, ok, _ := g.TryAcquire(ctx, "c"); !ok {
		t.Fatal("not acquired")
	}
	mr.FastForward(2 * time.Minute) // the holder never released
	if _, ok, _ := g.TryAcquire(ctx, "c"); !ok {
		t.Error("an abandoned claim never expired")
	}
}

func TestTurnGuardStaleReleaseDoesNotFreeANewHolder(t *testing.T) {
	mr, rdb := newRedis(t)
	g := newGuard(t, rdb, time.Minute)
	ctx := context.Background()

	staleRelease, _, _ := g.TryAcquire(ctx, "c")
	mr.FastForward(2 * time.Minute) // first claim expires...
	if _, ok, _ := g.TryAcquire(ctx, "c"); !ok {
		t.Fatal("second holder could not claim") // ...and another turn takes over
	}

	staleRelease() // the slow first turn finally ends
	if _, ok, _ := g.TryAcquire(ctx, "c"); ok {
		t.Error("a stale release freed the new holder's claim")
	}
}

func TestTurnGuardSurfacesRedisErrors(t *testing.T) {
	mr, rdb := newRedis(t)
	g := newGuard(t, rdb, time.Minute)
	mr.Close()

	if _, _, err := g.TryAcquire(context.Background(), "c"); err == nil {
		t.Error("TryAcquire succeeded with Redis down")
	}
}

func TestTurnGuardRejectsBadOptions(t *testing.T) {
	_, rdb := newRedis(t)
	if _, err := NewTurnGuard(rdb, TurnGuardOptions{}); err == nil {
		t.Error("zero TTL accepted")
	}
}

func TestConnectDoesNotLeakThePasswordInErrors(t *testing.T) {
	_, err := Connect(context.Background(), "http://:s3cret@host")
	if err == nil {
		t.Fatal("a non-redis URL was accepted")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaks the password: %v", err)
	}
}

func TestConnectChecksTheServerAnswers(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	client, err := Connect(context.Background(), "redis://"+addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_ = client.Close()

	mr.Close()
	if _, err := Connect(context.Background(), "redis://"+addr); err == nil {
		t.Error("Connect succeeded against a dead server")
	}
}
