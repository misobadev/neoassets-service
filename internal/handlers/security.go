package handlers

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// NewIPLimiter creates an IPLimiter allowing up to rpm requests per minute per
// IP, with an initial burst.
func NewIPLimiter(rpm, burst int) *IPLimiter {
	if rpm <= 0 {
		rpm = 30
	}
	if burst <= 0 {
		burst = rpm
	}
	return &IPLimiter{rpm: rpm, burst: burst, limits: map[string]*rate.Limiter{}, last: map[string]time.Time{}}
}

// IPLimiter is a per-client-IP token bucket limiter with TTL eviction. It is
// single-instance (documented), suitable for limiting public unauthenticated
// endpoints against brute-force and scraping abuse.
type IPLimiter struct {
	rpm    int
	burst  int
	mu     sync.Mutex
	limits map[string]*rate.Limiter
	last   map[string]time.Time
}

// allow reports whether the IP may proceed now.
func (l *IPLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked()
	lim, ok := l.limits[ip]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(float64(l.rpm)/60.0), l.burst)
		l.limits[ip] = lim
	}
	l.last[ip] = time.Now()
	return lim.Allow()
}

// sweepLocked evicts entries that have not been used for 10 minutes, bounding
// memory even when many distinct IPs pass through.
func (l *IPLimiter) sweepLocked() {
	if len(l.limits) < 4096 {
		return
	}
	cutoff := time.Now().Add(-10 * time.Minute)
	for ip, last := range l.last {
		if last.Before(cutoff) {
			delete(l.limits, ip)
			delete(l.last, ip)
		}
	}
}

// Handler rejects requests over the per-IP rate limit with 429.
func (l *IPLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)
		if !l.allow(ip) {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// KeyedHandler returns a middleware that rate limits by a per-request key (for
// example "ip|packID"), so an abusive client is throttled per resource instead
// of globally. Idle keys are evicted like the IP entries.
func (l *IPLimiter) KeyedHandler(keyFn func(r *http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.allow(keyFn(r)) {
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP returns the real client IP. The API sits behind Cloudflare and
// Traefik, so RemoteAddr is a proxy address: prefer Cloudflare's
// CF-Connecting-IP, then the first X-Forwarded-For entry, and only fall back to
// RemoteAddr. The origin is firewalled to the proxy, so these headers are
// trusted; without this the per-IP limiters would key on the proxy address.
func ClientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// maxRequestBody caps the size of any request body (2 MiB), enough for JSON
// payloads while bounding memory against oversized submissions.
const maxRequestBody = 2 << 20

// LimitBody caps the request body size on every request.
func LimitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		}
		next.ServeHTTP(w, r)
	})
}

// SecurityHeaders applies the standard security headers to every response. A
// Content-Security-Policy is not set here because these responses are JSON; the
// SPA is served by the web nginx, which sets its own policy.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}