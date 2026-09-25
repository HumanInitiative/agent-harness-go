package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/memory"
	"github.com/HumanInitiative/agent-harness-go/internal/domain/conversation"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newStore(t *testing.T, opts memory.Options) (*memory.ConversationStore, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	opts.Now = clock.now
	if opts.MaxConversations == 0 {
		opts.MaxConversations = 100
	}
	if opts.MaxMessagesPerConversation == 0 {
		opts.MaxMessagesPerConversation = 100
	}
	if opts.TTL == 0 {
		opts.TTL = time.Hour
	}
	store, err := memory.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, clock
}

func msg(content string) conversation.Message {
	return conversation.Message{Role: conversation.RoleUser, Content: content}
}

func contents(t *testing.T, store *memory.ConversationStore, id string) []string {
	t.Helper()
	history, err := store.History(context.Background(), id)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	out := make([]string, len(history))
	for i, m := range history {
		out[i] = m.Content
	}
	return out
}

func TestNew_RejectsInvalidOptions(t *testing.T) {
	cases := map[string]memory.Options{
		"zero conversations": {MaxConversations: 0, MaxMessagesPerConversation: 1, TTL: time.Second},
		"zero messages":      {MaxConversations: 1, MaxMessagesPerConversation: 0, TTL: time.Second},
		"zero ttl":           {MaxConversations: 1, MaxMessagesPerConversation: 1, TTL: 0},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := memory.New(opts); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestHistory_UnknownConversationIsEmptyNotError(t *testing.T) {
	store, _ := newStore(t, memory.Options{})
	if got := contents(t, store, "missing"); len(got) != 0 {
		t.Fatalf("expected empty history, got %v", got)
	}
}

func TestAppend_RoundTripsInOrder(t *testing.T) {
	store, _ := newStore(t, memory.Options{})
	_ = store.Append(context.Background(), "c", msg("a"), msg("b"))
	_ = store.Append(context.Background(), "c", msg("c"))

	got := contents(t, store, "c")
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("got %v", got)
	}
}

func TestHistory_ReturnsCopy(t *testing.T) {
	store, _ := newStore(t, memory.Options{})
	_ = store.Append(context.Background(), "c", msg("original"))

	history, _ := store.History(context.Background(), "c")
	history[0].Content = "mutated"

	if got := contents(t, store, "c"); got[0] != "original" {
		t.Fatalf("internal state mutated through returned slice: %v", got)
	}
}

func TestAppend_KeepsOnlyNewestMessages(t *testing.T) {
	store, _ := newStore(t, memory.Options{MaxMessagesPerConversation: 2})
	_ = store.Append(context.Background(), "c", msg("1"), msg("2"), msg("3"))

	got := contents(t, store, "c")
	if len(got) != 2 || got[0] != "2" || got[1] != "3" {
		t.Fatalf("expected [2 3], got %v", got)
	}
}

func TestAppend_EvictsLeastRecentlyActiveConversation(t *testing.T) {
	store, _ := newStore(t, memory.Options{MaxConversations: 2})
	ctx := context.Background()
	_ = store.Append(ctx, "a", msg("a"))
	_ = store.Append(ctx, "b", msg("b"))
	_ = store.Append(ctx, "a", msg("a again")) // a is now more recent than b
	_ = store.Append(ctx, "c", msg("c"))       // must evict b, not a

	if store.Len() != 2 {
		t.Fatalf("expected 2 conversations, got %d", store.Len())
	}
	if len(contents(t, store, "b")) != 0 {
		t.Fatal("expected b to be evicted")
	}
	if len(contents(t, store, "a")) == 0 || len(contents(t, store, "c")) == 0 {
		t.Fatal("expected a and c to be retained")
	}
}

func TestTTL_ExpiredConversationIsGone(t *testing.T) {
	store, clock := newStore(t, memory.Options{TTL: time.Hour})
	_ = store.Append(context.Background(), "c", msg("hello"))

	clock.advance(59 * time.Minute)
	if len(contents(t, store, "c")) == 0 {
		t.Fatal("conversation expired too early")
	}

	clock.advance(2 * time.Minute)
	if got := contents(t, store, "c"); len(got) != 0 {
		t.Fatalf("expected expired conversation to be empty, got %v", got)
	}
	if store.Len() != 0 {
		t.Fatalf("expected expired conversation to be removed, Len=%d", store.Len())
	}
}

func TestTTL_ActivityExtendsLifetime(t *testing.T) {
	store, clock := newStore(t, memory.Options{TTL: time.Hour})
	_ = store.Append(context.Background(), "c", msg("1"))
	clock.advance(50 * time.Minute)
	_ = store.Append(context.Background(), "c", msg("2"))
	clock.advance(50 * time.Minute)

	if got := contents(t, store, "c"); len(got) != 2 {
		t.Fatalf("expected activity to keep conversation alive, got %v", got)
	}
}

func TestAppend_SweepsExpiredConversations(t *testing.T) {
	store, clock := newStore(t, memory.Options{TTL: time.Hour})
	_ = store.Append(context.Background(), "old", msg("x"))
	clock.advance(2 * time.Hour)
	_ = store.Append(context.Background(), "new", msg("y"))

	if store.Len() != 1 {
		t.Fatalf("expected the expired conversation to be swept, Len=%d", store.Len())
	}
}
