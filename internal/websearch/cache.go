package websearch

import (
	"container/list"
	"sync"
	"time"
)

// Cache is a size-bounded, in-memory TTL cache. When full, the least
// recently used entry is evicted, so memory stays bounded no matter how
// many distinct queries or URLs are seen.
type Cache[V any] struct {
	ttl     time.Duration
	max     int
	now     func() time.Time
	metrics *Metrics
	name    string

	mu    sync.Mutex
	items map[string]*list.Element
	order *list.List // front = most recently used
}

type cacheEntry[V any] struct {
	key     string
	value   V
	expires time.Time
}

// NewCache returns a cache holding up to max entries for ttl each. now may
// be nil (time.Now); metrics may be nil. name labels the cache's hit/miss
// counters.
func NewCache[V any](name string, ttl time.Duration, max int, now func() time.Time, metrics *Metrics) *Cache[V] {
	if now == nil {
		now = time.Now
	}
	if max < 1 {
		max = 1
	}
	return &Cache[V]{
		ttl: ttl, max: max, now: now, metrics: metrics, name: name,
		items: make(map[string]*list.Element), order: list.New(),
	}
}

// Get returns the cached value for key if present and not expired.
func (c *Cache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var zero V
	elem, ok := c.items[key]
	if !ok {
		c.metrics.Inc("cache." + c.name + ".miss")
		return zero, false
	}
	e := elem.Value.(*cacheEntry[V])
	if c.now().After(e.expires) {
		c.order.Remove(elem)
		delete(c.items, key)
		c.metrics.Inc("cache." + c.name + ".miss")
		return zero, false
	}
	c.order.MoveToFront(elem)
	c.metrics.Inc("cache." + c.name + ".hit")
	return e.value, true
}

// Set stores value under key, evicting the least recently used entry if the
// cache is full.
func (c *Cache[V]) Set(key string, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	expires := c.now().Add(c.ttl)
	if elem, ok := c.items[key]; ok {
		e := elem.Value.(*cacheEntry[V])
		e.value, e.expires = value, expires
		c.order.MoveToFront(elem)
		return
	}
	for c.order.Len() >= c.max {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*cacheEntry[V]).key)
	}
	c.items[key] = c.order.PushFront(&cacheEntry[V]{key: key, value: value, expires: expires})
}
