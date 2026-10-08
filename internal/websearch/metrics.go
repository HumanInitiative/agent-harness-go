package websearch

import (
	"fmt"
	"sort"
	"strings"
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

// Changed returns the counters that differ from prev (a previous
// Snapshot), with their current values, and the current snapshot to pass
// next time.
func (m *Metrics) Changed(prev map[string]int64) (changed, now map[string]int64) {
	now = m.Snapshot()
	changed = map[string]int64{}
	for k, v := range now {
		if prev[k] != v {
			changed[k] = v
		}
	}
	return changed, now
}

// Summary sums counters into the few numbers that tell whether crawling is
// healthy: fetches that worked, failed, were blocked (WAF, 403, 429) or
// confirmed unchanged, and searches that worked or failed across providers.
func (m *Metrics) Summary() MetricsSummary {
	return Summarize(m.Snapshot())
}

// Summarize is Summary over a snapshot, or over the difference of two
// snapshots (see Since) to summarize one run.
func Summarize(counters map[string]int64) MetricsSummary {
	var s MetricsSummary
	for k, v := range counters {
		switch {
		case k == "fetch.success":
			s.FetchOK = v
		case k == "fetch.failure":
			s.FetchFailed = v
		case k == "fetch.blocked":
			s.FetchBlocked = v
		case k == "fetch.not_modified":
			s.FetchNotModified = v
		case k == "fetch.robots_disallowed":
			s.RobotsDisallowed = v
		case strings.HasPrefix(k, "search.") && strings.HasSuffix(k, ".success"):
			s.SearchOK += v
		case strings.HasPrefix(k, "search.") && strings.HasSuffix(k, ".failure"):
			s.SearchFailed += v
		}
	}
	return s
}

// Since returns how much each counter grew since an earlier snapshot.
func (m *Metrics) Since(earlier map[string]int64) map[string]int64 {
	out := map[string]int64{}
	for k, v := range m.Snapshot() {
		if d := v - earlier[k]; d != 0 {
			out[k] = d
		}
	}
	return out
}

// MetricsSummary is the result of Summary.
type MetricsSummary struct {
	FetchOK, FetchFailed, FetchBlocked, FetchNotModified, RobotsDisallowed int64
	SearchOK, SearchFailed                                                 int64
}

func (s MetricsSummary) String() string {
	return fmt.Sprintf("fetch ok %d, failed %d (blocked %d), unchanged %d, robots-disallowed %d | search ok %d, failed %d",
		s.FetchOK, s.FetchFailed, s.FetchBlocked, s.FetchNotModified, s.RobotsDisallowed, s.SearchOK, s.SearchFailed)
}
