package websearch

import (
	"sort"
	"sync"
)

// Metrics is a set of named in-memory counters (provider successes and
// failures, cache hits, fetch outcomes, ...). The harness can expose a
// Snapshot however it likes; this package stays free of any metrics
// library. A nil *Metrics is valid and records nothing.
type Metrics struct {
	mu       sync.Mutex
	counters map[string]int64
}

// NewMetrics returns an empty counter set.
func NewMetrics() *Metrics {
	return &Metrics{counters: make(map[string]int64)}
}

// Inc adds one to the named counter.
func (m *Metrics) Inc(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.counters[name]++
	m.mu.Unlock()
}

// Snapshot returns a copy of every counter.
func (m *Metrics) Snapshot() map[string]int64 {
	if m == nil {
		return map[string]int64{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int64, len(m.counters))
	for k, v := range m.counters {
		out[k] = v
	}
	return out
}

// Names returns the counter names in sorted order.
func (m *Metrics) Names() []string {
	snap := m.Snapshot()
	names := make([]string, 0, len(snap))
	for k := range snap {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
