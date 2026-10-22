// Package redisstore implements outbound.ConversationStore and
// outbound.TurnGuard on Redis, so several harness instances can share
// conversations and agree on which turn is running.
//
// Both are bounded without any sweeping of our own:
//   - a conversation is a Redis list trimmed to MaxMessagesPerConversation
//     and given a TTL that every append renews (idle conversations vanish);
//   - a turn claim carries its own expiry, so an instance that dies mid-turn
//     cannot lock a conversation for longer than that.
//
// There is deliberately no cap on the number of conversations (the memory
// adapter has one): bound it on the server with maxmemory and an eviction
// policy such as allkeys-lru. Conversations are not durable either way.
package redisstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Connect parses a redis:// or rediss:// URL, opens a client and checks the
// server answers.
func Connect(ctx context.Context, url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		// The error text echoes parts of the URL, which can hold a password.
		return nil, errors.New("redisstore: REDIS_URL is not a valid redis:// or rediss:// URL")
	}
	// A dead Redis must fail a request in seconds, not hold it for the
	// client's defaults (several 5s dials with retries) of a 60s budget.
	opts.DialTimeout = 2 * time.Second
	opts.ReadTimeout = 2 * time.Second
	opts.WriteTimeout = 2 * time.Second
	opts.MaxRetries = 1
	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redisstore: ping: %w", err)
	}
	return client, nil
}

// releaseTimeout bounds the release of a turn claim, which must happen even
// when the request's own context is already canceled.
const releaseTimeout = 3 * time.Second
