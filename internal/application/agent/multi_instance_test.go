package agent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/HumanInitiative/agent-harness-go/internal/adapters/outbound/redisstore"
	"github.com/HumanInitiative/agent-harness-go/internal/application/agent"
	"github.com/HumanInitiative/agent-harness-go/internal/ports/outbound"
)

// newSharedInstance builds a Service the way a second harness instance would:
// its own process state, but conversations and turn claims in the shared Redis.
func newSharedInstance(t *testing.T, addr string, model outbound.ModelPort) *agent.Service {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })

	store, err := redisstore.NewConversationStore(rdb, redisstore.ConversationOptions{
		MaxMessagesPerConversation: 22, TTL: time.Hour, KeyPrefix: "t:conv:",
	})
	if err != nil {
		t.Fatal(err)
	}
	guard, err := redisstore.NewTurnGuard(rdb, redisstore.TurnGuardOptions{TTL: time.Minute, KeyPrefix: "t:turn:"})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := agent.NewService(model, store, guard, nil, validConfig(), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestMultiInstance_FollowUpOnAnotherInstanceKeepsContext(t *testing.T) {
	mr := miniredis.RunT(t)
	first := newSharedInstance(t, mr.Addr(), &fakeModel{response: outbound.GenerateResponse{Output: "halo Budi"}})
	secondModel := &fakeModel{response: outbound.GenerateResponse{Output: "Budi"}}
	second := newSharedInstance(t, mr.Addr(), secondModel)

	if _, err := first.Chat(context.Background(), agent.ChatInput{ConversationID: "c", Message: "namaku Budi"}); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if _, err := second.Chat(context.Background(), agent.ChatInput{ConversationID: "c", Message: "siapa aku?"}); err != nil {
		t.Fatalf("second turn: %v", err)
	}

	// The model of instance two saw instance one's exchange plus the new message.
	got := secondModel.request().Messages
	if len(got) != 3 || got[0].Content != "namaku Budi" || got[1].Content != "halo Budi" || got[2].Content != "siapa aku?" {
		t.Errorf("second instance sent %v to the model; want the first instance's turn as context", got)
	}
}

func TestMultiInstance_RejectsAConcurrentTurnRunningOnAnotherInstance(t *testing.T) {
	mr := miniredis.RunT(t)
	slow := &fakeModel{
		response: outbound.GenerateResponse{Output: "ok"},
		started:  make(chan struct{}),
		block:    make(chan struct{}),
	}
	first := newSharedInstance(t, mr.Addr(), slow)
	second := newSharedInstance(t, mr.Addr(), &fakeModel{response: outbound.GenerateResponse{Output: "ok"}})

	done := make(chan error, 1)
	go func() {
		_, err := first.Chat(context.Background(), agent.ChatInput{ConversationID: "c", Message: "one"})
		done <- err
	}()
	<-slow.started

	_, err := second.Chat(context.Background(), agent.ChatInput{ConversationID: "c", Message: "two"})
	if !errors.Is(err, agent.ErrConversationBusy) {
		t.Fatalf("second instance: got %v, want ErrConversationBusy", err)
	}

	close(slow.block)
	if err := <-done; err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if _, err := second.Chat(context.Background(), agent.ChatInput{ConversationID: "c", Message: "two"}); err != nil {
		t.Errorf("after the first turn ended, the second instance still failed: %v", err)
	}
}
