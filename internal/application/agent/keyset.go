package agent

import "sync"

// keySet is a concurrency-safe set used to mark conversations that have a
// turn in progress. It only coordinates goroutines within this process —
// enough while conversations themselves live in process memory. A
// multi-instance deployment needs a shared lock (e.g. in Redis or Postgres)
// alongside its shared ConversationStore.
type keySet struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func newKeySet() *keySet {
	return &keySet{keys: make(map[string]struct{})}
}

// tryAdd adds key and reports true, or reports false if key is already
// present.
func (s *keySet) tryAdd(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.keys[key]; ok {
		return false
	}
	s.keys[key] = struct{}{}
	return true
}

func (s *keySet) remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, key)
}
