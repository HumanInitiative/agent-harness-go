package memory

import (
	"context"
	"sync"
)

// TurnGuard is an in-process outbound.TurnGuard. Like ConversationStore it
// only coordinates goroutines of this process, so it is not suitable for
// more than one running instance.
type TurnGuard struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

// NewTurnGuard returns a TurnGuard with no turn in progress.
func NewTurnGuard() *TurnGuard {
	return &TurnGuard{keys: make(map[string]struct{})}
}

// TryAcquire implements outbound.TurnGuard.
func (g *TurnGuard) TryAcquire(_ context.Context, conversationID string) (func(), bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, busy := g.keys[conversationID]; busy {
		return nil, false, nil
	}
	g.keys[conversationID] = struct{}{}

	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			delete(g.keys, conversationID)
		})
	}, true, nil
}
