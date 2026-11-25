package services

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"neoassets/internal/models"
	"neoassets/pkg/auth"
)

// Live scraping status (open threads, requests per minute, active scrapers),
// kept in memory for the public status panel. Like the rate limiters it is
// per instance.

const (
	// DefaultGuestOpenThreads is how many scrape requests all guests together
	// may have open (in progress or waiting for a slot) at once.
	DefaultGuestOpenThreads = 512
	// DefaultUserOpenThreads is the same cap for all registered users.
	DefaultUserOpenThreads = 8192
	// activeScraperWindow is how recently a scraper must have made a request to
	// count as active.
	activeScraperWindow = 5 * time.Minute
	// statusCacheTTL is how long a computed status is served before it is
	// rebuilt, so the public endpoint never walks the scraper map per request.
	statusCacheTTL = 5 * time.Second
)

// liveStats counts what the status panel shows. The zero value is ready.
type liveStats struct {
	openGuests atomic.Int64
	openUsers  atomic.Int64

	mu sync.Mutex
	// seconds holds the requests of the last 60 seconds, one bucket per
	// second; stamps holds the unix second each bucket belongs to.
	seconds [60]int64
	stamps  [60]int64
	// seen maps a scraper (its admission key) to its last request and kind.
	seen map[string]scraperSeen

	cacheMu  sync.Mutex
	cached   models.ScrapeStatus
	cachedAt time.Time
}

type scraperSeen struct {
	at    time.Time
	guest bool
}

// record counts one admitted request for the requests-per-minute figure and
// marks the scraper as active.
func (l *liveStats) record(key string, guest bool, now time.Time) {
	sec := now.Unix()
	i := sec % 60
	l.mu.Lock()
	if l.stamps[i] != sec {
		l.stamps[i] = sec
		l.seconds[i] = 0
	}
	l.seconds[i]++
	if l.seen == nil {
		l.seen = map[string]scraperSeen{}
	}
	l.seen[key] = scraperSeen{at: now, guest: guest}
	l.mu.Unlock()
}

// snapshot returns the requests of the last minute and the active scrapers by
// kind, dropping scrapers idle for longer than the window.
func (l *liveStats) snapshot(now time.Time) (perMinute int64, users, guests int) {
	sec := now.Unix()
	cutoff := now.Add(-activeScraperWindow)
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.seconds {
		if sec-l.stamps[i] < 60 {
			perMinute += l.seconds[i]
		}
	}
	for k, s := range l.seen {
		if s.at.Before(cutoff) {
			delete(l.seen, k)
			continue
		}
		if s.guest {
			guests++
		} else {
			users++
		}
	}
	return perMinute, users, guests
}

// SetOpenThreadCaps sets how many scrape requests all guests and all
// registered users may have open at once (0 uses the defaults, a negative
// value disables that cap).
func (s *ScrapeService) SetOpenThreadCaps(guests, users int) {
	if guests == 0 {
		guests = DefaultGuestOpenThreads
	}
	if users == 0 {
		users = DefaultUserOpenThreads
	}
	s.guestOpenCap = guests
	s.userOpenCap = users
}

// acquireOpen takes one of the open-thread slots shared by every subject of
// the same kind, waiting briefly. It returns ErrServerBusy when they are all
// taken.
func (s *ScrapeService) acquireOpen(ctx context.Context, subject *auth.ScrapeSubject) (func(), error) {
	key, limit, counter := "open:users", s.userOpenCap, &s.live.openUsers
	if subject.IsGuest() {
		key, limit, counter = "open:guests", s.guestOpenCap, &s.live.openGuests
	}
	release := func() {}
	if limit > 0 {
		r, ok := s.openSlots.acquire(ctx, key, limit, admissionWait)
		if !ok {
			return nil, ErrServerBusy
		}
		release = r
	}
	counter.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			counter.Add(-1)
			release()
		})
	}, nil
}

// Status returns the live scraping status, rebuilt at most every few seconds.
func (s *ScrapeService) Status() models.ScrapeStatus {
	now := time.Now()
	s.live.cacheMu.Lock()
	defer s.live.cacheMu.Unlock()
	if !s.live.cachedAt.IsZero() && now.Sub(s.live.cachedAt) < statusCacheTTL {
		return s.live.cached
	}
	perMinute, users, guests := s.live.snapshot(now)
	st := models.ScrapeStatus{RequestsPerMinute: perMinute}
	st.OpenThreads.Users = s.live.openUsers.Load()
	st.OpenThreads.UsersMax = s.userOpenCap
	st.OpenThreads.Guests = s.live.openGuests.Load()
	st.OpenThreads.GuestsMax = s.guestOpenCap
	st.Scrapers.Users = users
	st.Scrapers.Guests = guests
	st.UpdatedAt = now.UTC()
	s.live.cached = st
	s.live.cachedAt = now
	return st
}
