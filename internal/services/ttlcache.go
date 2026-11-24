package services

import (
	"context"
	"sync"
	"time"
)

// DefaultCatalogCacheTTL is how long catalog aggregates (systems, families,
// groups, languages, the dashboard) are served from memory. They only change
// on approvals (which clear the cache) or catalog imports, so a short TTL bounds
// how stale an import can look.
const DefaultCatalogCacheTTL = time.Minute

// ttlValue caches one value for a fixed time. Loads are serialized by the
// mutex, so a burst of requests on an expired value runs the query once
// instead of once per request. A zero ttl disables caching.
type ttlValue[T any] struct {
	mu  sync.Mutex
	val T
	at  time.Time
	ok  bool
}

// get returns the cached value while it is fresh, otherwise loads it. A load
// error is returned and nothing is cached.
func (c *ttlValue[T]) get(ctx context.Context, ttl time.Duration, load func(context.Context) (T, error)) (T, error) {
	if ttl <= 0 {
		return load(ctx)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok && time.Since(c.at) < ttl {
		return c.val, nil
	}
	v, err := load(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	c.val, c.at, c.ok = v, time.Now(), true
	return v, nil
}

// clear drops the cached value so the next get loads it again.
func (c *ttlValue[T]) clear() {
	c.mu.Lock()
	c.ok = false
	var zero T
	c.val = zero
	c.mu.Unlock()
}
