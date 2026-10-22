package redisstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// releaseScript deletes the claim only if we still own it, so a claim that
// expired and was taken by another instance is never released by mistake.
var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0`)

// TurnGuardOptions configures a TurnGuard. Every field is required.
type TurnGuardOptions struct {
	// TTL is how long a claim lives if its holder never releases it (the
	// instance died). It must exceed the longest a turn can run, i.e. the
	// request timeout, or two turns could overlap.
	TTL time.Duration
	// KeyPrefix namespaces the keys, e.g. "harness:turn:".
	KeyPrefix string
}

// TurnGuard is an outbound.TurnGuard on Redis (SET NX PX).
type TurnGuard struct {
	rdb  redis.Cmdable
	opts TurnGuardOptions
}

// NewTurnGuard returns a guard over rdb, or an error if opts are invalid.
func NewTurnGuard(rdb redis.Cmdable, opts TurnGuardOptions) (*TurnGuard, error) {
	if opts.TTL <= 0 {
		return nil, fmt.Errorf("redisstore: TTL must be positive, got %s", opts.TTL)
	}
	return &TurnGuard{rdb: rdb, opts: opts}, nil
}

// TryAcquire implements outbound.TurnGuard.
func (g *TurnGuard) TryAcquire(ctx context.Context, conversationID string) (func(), bool, error) {
	token, err := newToken()
	if err != nil {
		return nil, false, err
	}
	key := g.opts.KeyPrefix + conversationID

	ok, err := g.rdb.SetNX(ctx, key, token, g.opts.TTL).Result()
	if err != nil {
		return nil, false, fmt.Errorf("redisstore: claim turn: %w", err)
	}
	if !ok {
		return nil, false, nil
	}

	return func() {
		// The request's context may already be canceled (client went away,
		// timeout); the claim must still be released promptly.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
		defer cancel()
		// A failure here is harmless: the claim expires on its own.
		_ = releaseScript.Run(rctx, g.rdb, []string{key}, token).Err()
	}, true, nil
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("redisstore: token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
