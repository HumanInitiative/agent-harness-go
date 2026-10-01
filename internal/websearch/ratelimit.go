package websearch

import (
	"context"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// maxTrackedDomains bounds the limiter map; the least recently used domain
// is forgotten beyond it (its next request simply starts a fresh bucket).
const maxTrackedDomains = 10000

// DomainLimiter spaces out requests to the same domain so we never hammer a
// company's website, whoever triggers the request. Unlike the API rate
// limiter it waits instead of rejecting: a polite crawler slows down.
type DomainLimiter struct {
	limit rate.Limit
	now   func() time.Time

	mu      sync.Mutex
	domains map[string]*domainBucket
}

type domainBucket struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// NewDomainLimiter allows perSecond requests per second to each domain, with
// no bursting. now may be nil (time.Now).
func NewDomainLimiter(perSecond float64, now func() time.Time) *DomainLimiter {
	if now == nil {
		now = time.Now
	}
	return &DomainLimiter{limit: rate.Limit(perSecond), now: now, domains: make(map[string]*domainBucket)}
}

// Wait blocks until a request to host is allowed or ctx ends.
func (l *DomainLimiter) Wait(ctx context.Context, host string) error {
	return l.bucket(domainKey(host)).Wait(ctx)
}

func (l *DomainLimiter) bucket(key string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if b, ok := l.domains[key]; ok {
		b.lastUsed = now
		return b.limiter
	}
	if len(l.domains) >= maxTrackedDomains {
		l.evictOldest()
	}
	b := &domainBucket{limiter: rate.NewLimiter(l.limit, 1), lastUsed: now}
	l.domains[key] = b
	return b.limiter
}

func (l *DomainLimiter) evictOldest() {
	var oldestKey string
	var oldest time.Time
	for k, b := range l.domains {
		if oldestKey == "" || b.lastUsed.Before(oldest) {
			oldestKey, oldest = k, b.lastUsed
		}
	}
	delete(l.domains, oldestKey)
}

// domainKey groups "www.example.com" with "example.com": they are the same
// site for politeness purposes.
func domainKey(host string) string {
	return strings.TrimPrefix(strings.ToLower(host), "www.")
}
