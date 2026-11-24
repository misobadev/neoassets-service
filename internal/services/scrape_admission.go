package services

import (
	"context"
	"errors"
	"sync"
	"time"

	"neoassets/pkg/auth"
)

// Scrape admission control. A subject's threads are its concurrent requests:
// a scraper with 4 threads gets at most 4 requests in flight at once. On top of
// that, game lookups (the only scrape route that does real database work) share
// one global in-flight cap so a few heavy accounts can never queue enough work
// to starve each other or the website, which uses the same connection pool.

const (
	// DefaultScrapeMaxInFlight is the global cap on concurrent game lookups.
	// Measured on two cores, 16 in flight keeps p50 around 20 ms while leaving
	// pool connections (DB_MAX_OPEN_CONNS, default 20) for the website.
	DefaultScrapeMaxInFlight = 16
	// admissionWait is how long a request waits for a free slot before it is
	// refused. Short enough that a refused client retries quickly, long enough
	// to absorb the jitter of a client that fires all its threads at once.
	admissionWait = 2 * time.Second
)

var (
	// ErrTooManyConcurrent is returned when a subject already has as many
	// requests in flight as it has threads.
	ErrTooManyConcurrent = errors.New("too many concurrent requests for this account")
	// ErrServerBusy is returned when the global in-flight cap is reached.
	ErrServerBusy = errors.New("server busy")
)

// slotPool hands out per-key semaphores whose size is the key's limit. Entries
// are created on demand and removed when nobody holds or waits for them, so
// the map only holds active subjects.
type slotPool struct {
	mu      sync.Mutex
	entries map[string]*slotEntry
}

type slotEntry struct {
	slots chan struct{}
	refs  int // holders plus waiters
}

// acquire takes one of key's limit slots, waiting up to wait. The returned
// release must be called exactly once when ok is true. When a key's limit
// changes (a level-up or a donation), new requests use a semaphore of the new
// size and requests already in flight release into the old one.
func (p *slotPool) acquire(ctx context.Context, key string, limit int, wait time.Duration) (release func(), ok bool) {
	if limit < 1 {
		limit = 1
	}
	p.mu.Lock()
	if p.entries == nil {
		p.entries = map[string]*slotEntry{}
	}
	e := p.entries[key]
	if e == nil || cap(e.slots) != limit {
		e = &slotEntry{slots: make(chan struct{}, limit)}
		p.entries[key] = e
	}
	e.refs++
	p.mu.Unlock()

	drop := func() {
		p.mu.Lock()
		e.refs--
		if e.refs == 0 && p.entries[key] == e {
			delete(p.entries, key)
		}
		p.mu.Unlock()
	}

	if !take(ctx, e.slots, wait) {
		drop()
		return nil, false
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-e.slots
			drop()
		})
	}, true
}

// take puts a token in slots, waiting up to wait (or until ctx is done).
func take(ctx context.Context, slots chan struct{}, wait time.Duration) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
	}
	if wait <= 0 {
		return false
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case slots <- struct{}{}:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// admissionKey identifies whose threads a request uses: the user for a
// registered account, the developer app's owner for a guest (so creating more
// apps does not add threads, the same rule as the daily quota).
func admissionKey(subject *auth.ScrapeSubject) string {
	if subject.IsGuest() {
		return "owner:" + subject.ClientOwnerID.String()
	}
	return "user:" + subject.UserID.String()
}

// AcquireSubject takes one of the subject's thread slots, waiting briefly for
// one to free up. It returns ErrTooManyConcurrent when all are busy.
func (s *ScrapeService) AcquireSubject(ctx context.Context, subject *auth.ScrapeSubject) (func(), error) {
	release, ok := s.subjectSlots.acquire(ctx, admissionKey(subject), subject.Threads, admissionWait)
	if !ok {
		return nil, ErrTooManyConcurrent
	}
	return release, nil
}

// AcquireGlobal takes a slot of the global game-lookup cap, waiting briefly.
// It returns ErrServerBusy when the server is saturated. A cap below 1
// disables it.
func (s *ScrapeService) AcquireGlobal(ctx context.Context) (func(), error) {
	if s.maxInFlight < 1 {
		return func() {}, nil
	}
	release, ok := s.globalSlots.acquire(ctx, "global", s.maxInFlight, admissionWait)
	if !ok {
		return nil, ErrServerBusy
	}
	return release, nil
}

// SetMaxInFlight sets the global cap on concurrent game lookups (0 uses
// DefaultScrapeMaxInFlight, a negative value disables the cap).
func (s *ScrapeService) SetMaxInFlight(n int) {
	if n == 0 {
		n = DefaultScrapeMaxInFlight
	}
	s.maxInFlight = n
}
