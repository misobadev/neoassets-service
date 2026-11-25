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
	// DefaultScrapeGuestMaxInFlight is how many of those lookups all guests
	// together may hold at once, so anonymous traffic always leaves most of
	// the server to registered users.
	DefaultScrapeGuestMaxInFlight = 4
	// guestAdmissionWait is how long a guest waits for one of the guest
	// lookup slots. Guests may be slower, so they queue longer before a 503.
	guestAdmissionWait = 5 * time.Second
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
// registered account, the caller's network address for a guest (the same
// rule as the daily quota, so creating more apps does not add threads).
func admissionKey(subject *auth.ScrapeSubject) string {
	if subject.IsGuest() {
		return "guest:" + guestKey(subject)
	}
	return "user:" + subject.UserID.String()
}

// AcquireSubject takes one of the open-thread slots shared by every subject of
// the same kind (ErrServerBusy when those are full), holds a paced guest for
// its pace delay, and then takes one of the subject's own thread slots, waiting briefly for one to free up
// (ErrTooManyConcurrent when all are busy). An admitted request is counted in
// the live status.
func (s *ScrapeService) AcquireSubject(ctx context.Context, subject *auth.ScrapeSubject) (func(), error) {
	releaseOpen, err := s.acquireOpen(ctx, subject)
	if err != nil {
		if subject.PaceCancel != nil {
			subject.PaceCancel()
		}
		return nil, err
	}
	if subject.PaceDelay > 0 {
		t := time.NewTimer(subject.PaceDelay)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			if subject.PaceCancel != nil {
				subject.PaceCancel()
			}
			releaseOpen()
			return nil, ctx.Err()
		}
	}
	key := admissionKey(subject)
	release, ok := s.subjectSlots.acquire(ctx, key, subject.Threads, admissionWait)
	if !ok {
		releaseOpen()
		return nil, ErrTooManyConcurrent
	}
	s.live.record(key, subject.IsGuest(), time.Now())
	return func() {
		release()
		releaseOpen()
	}, nil
}

// AcquireGlobal takes a slot of the global game-lookup cap, waiting briefly.
// A guest (subject may be nil) first takes one of the slots all guests share.
// It returns ErrServerBusy when the server, or the guests' share, is
// saturated. A cap below 1 disables it.
func (s *ScrapeService) AcquireGlobal(ctx context.Context, subject *auth.ScrapeSubject) (func(), error) {
	releaseGuest := func() {}
	if subject != nil && subject.IsGuest() && s.guestInFlight > 0 {
		r, ok := s.guestSlots.acquire(ctx, "guests", s.guestInFlight, guestAdmissionWait)
		if !ok {
			return nil, ErrServerBusy
		}
		releaseGuest = r
	}
	if s.maxInFlight < 1 {
		return releaseGuest, nil
	}
	release, ok := s.globalSlots.acquire(ctx, "global", s.maxInFlight, admissionWait)
	if !ok {
		releaseGuest()
		return nil, ErrServerBusy
	}
	return func() {
		release()
		releaseGuest()
	}, nil
}

// SetGuestMaxInFlight sets how many game lookups all guests together may run
// at once (0 uses DefaultScrapeGuestMaxInFlight, a negative value disables
// the guest cap).
func (s *ScrapeService) SetGuestMaxInFlight(n int) {
	if n == 0 {
		n = DefaultScrapeGuestMaxInFlight
	}
	s.guestInFlight = n
}

// SetMaxInFlight sets the global cap on concurrent game lookups (0 uses
// DefaultScrapeMaxInFlight, a negative value disables the cap).
func (s *ScrapeService) SetMaxInFlight(n int) {
	if n == 0 {
		n = DefaultScrapeMaxInFlight
	}
	s.maxInFlight = n
}
