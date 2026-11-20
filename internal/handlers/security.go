package handlers

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// NewIPLimiter creates an IPLimiter allowing up to rpm requests per minute per
// client (ClientKey), with an initial burst.
func NewIPLimiter(rpm, burst int) *IPLimiter {
	if rpm <= 0 {
		rpm = 30
	}
	if burst <= 0 {
		burst = rpm
	}
	return &IPLimiter{
		rpm:     rpm,
		burst:   burst,
		maxKeys: limiterMaxKeys,
		now:     time.Now,
		cur:     map[string]*rate.Limiter{},
		prev:    map[string]*rate.Limiter{},
	}
}

// limiterTTL sets how long an idle key keeps its bucket: buckets live in two
// generations that rotate every limiterTTL, so a key unused for between one and
// two TTLs is forgotten (by then its bucket would have refilled anyway).
const limiterTTL = 10 * time.Minute

// limiterMaxKeys caps the keys in one generation; reaching it rotates early, so
// memory stays bounded (two generations) however many keys arrive.
const limiterMaxKeys = 50000

// IPLimiter is a per-client token bucket limiter. It is single-instance
// (documented), suitable for limiting public unauthenticated endpoints against
// brute-force and scraping abuse. Every operation is O(1): idle keys are
// dropped by rotating generations rather than by scanning the map.
type IPLimiter struct {
	rpm     int
	burst   int
	maxKeys int
	now     func() time.Time

	mu      sync.Mutex
	cur     map[string]*rate.Limiter
	prev    map[string]*rate.Limiter
	rotated time.Time
}

// allow reports whether the key may proceed now.
func (l *IPLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.rotated.IsZero() {
		l.rotated = now
	}
	switch elapsed := now.Sub(l.rotated); {
	case elapsed >= 2*limiterTTL:
		l.cur, l.prev, l.rotated = map[string]*rate.Limiter{}, map[string]*rate.Limiter{}, now
	case elapsed >= limiterTTL:
		l.rotateLocked(now)
	}
	lim, ok := l.cur[key]
	if !ok {
		if lim, ok = l.prev[key]; ok {
			delete(l.prev, key)
		} else {
			lim = rate.NewLimiter(rate.Limit(float64(l.rpm)/60.0), l.burst)
		}
		l.cur[key] = lim
		if len(l.cur) >= l.maxKeys {
			l.rotateLocked(now)
		}
	}
	return lim.Allow()
}

// rotateLocked starts a new generation; keys only in the previous one are
// dropped.
func (l *IPLimiter) rotateLocked(now time.Time) {
	l.prev, l.cur, l.rotated = l.cur, map[string]*rate.Limiter{}, now
}

// size and has are for tests.
func (l *IPLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.cur) + len(l.prev)
}

func (l *IPLimiter) has(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, inCur := l.cur[key]
	_, inPrev := l.prev[key]
	return inCur || inPrev
}

// Handler rejects requests over the per-client rate limit with 429.
func (l *IPLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(ClientKey(r)) {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// KeyedHandler returns a middleware that rate limits by a per-request key (for
// example "client|packID"), so an abusive client is throttled per resource
// instead of globally. Idle keys are evicted like the client entries.
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

// trustProxyHeadersAlways is set by TRUST_PROXY_HEADERS=always for deployments
// where the proxy directly in front of the service has a public address: the
// peer is then always treated as our proxy. Beyond it, only private or loopback
// addresses are treated as our own hops.
var trustProxyHeadersAlways = strings.EqualFold(os.Getenv("TRUST_PROXY_HEADERS"), "always")

// ClientIP returns the client's IP address. The API sits behind a CDN
// (Cloudflare by default) and Traefik. It finds the edge address — the first address outside our own
// network that the request came from — by starting at the peer and, while that
// is one of our hops (private or loopback, or the peer itself with
// TRUST_PROXY_HEADERS=always), moving to the X-Forwarded-For entry that hop
// added, right to left (entries further left were sent by the client). An
// entry that isn't an IP address stops the walk. The CDN's client-address
// header (Cloudflare's CF-Connecting-IP unless ConfigureCDN says otherwise) is
// then used if the edge is one of the CDN's addresses, or if the request only
// passed through private hops (e.g. a Cloudflare Tunnel or an ingress proxy in
// front of Traefik — this assumes the origin is reachable only through the
// CDN); a request whose edge is any other public address is keyed by that
// address, so a client reaching the origin directly can't choose its own.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	edge := net.ParseIP(host)
	if edge == nil {
		return host
	}
	next := forwardedFromRight(r)
	for hop := trustProxyHeadersAlways || privateHop(edge); hop; hop = privateHop(edge) {
		ip, ok := next()
		if !ok || ip == nil {
			break
		}
		edge = ip
	}
	if cdnClientIPHeader != "" && (isCDN(edge) || privateHop(edge)) {
		if ip := parseIP(r.Header.Get(cdnClientIPHeader)); ip != nil {
			return ip.String()
		}
	}
	return edge.String()
}

// ClientKey is the rate-limit key for a request's client: its IPv4 address, or
// its IPv6 /64 (one subscriber's allocation), so a client can't get fresh
// buckets by rotating addresses within its own prefix.
func ClientKey(r *http.Request) string {
	client := ClientIP(r)
	ip := net.ParseIP(client)
	if ip == nil {
		return client
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return (&net.IPNet{IP: ip.Mask(net.CIDRMask(64, 128)), Mask: net.CIDRMask(64, 128)}).String()
}

// forwardedFromRight returns a function yielding the X-Forwarded-For entries
// from the last one backwards, across header lines, parsing only as far as the
// caller reads; an entry that isn't an IP address yields nil, and ok is false
// once there are none left.
func forwardedFromRight(r *http.Request) func() (ip net.IP, ok bool) {
	lines := r.Header.Values("X-Forwarded-For")
	rest := ""
	return func() (net.IP, bool) {
		for rest == "" {
			if len(lines) == 0 {
				return nil, false
			}
			rest, lines = lines[len(lines)-1], lines[:len(lines)-1]
		}
		entry := rest
		if i := strings.LastIndexByte(rest, ','); i >= 0 {
			entry, rest = rest[i+1:], rest[:i]
		} else {
			rest = ""
		}
		return parseIP(entry), true
	}
}

// parseIP parses an address, also accepting brackets or a port ("1.2.3.4:80",
// "[2001:db8::1]", "[2001:db8::1]:80"), or returns nil.
func parseIP(s string) net.IP {
	s = strings.TrimSpace(s)
	if ip := net.ParseIP(s); ip != nil {
		return ip
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"))
}

// privateHop reports whether an address is one of our own hops: a private or
// loopback address (Traefik, a tunnel, an internal load balancer).
func privateHop(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate()
}

// cloudflareRanges are Cloudflare's published edge ranges
// (https://www.cloudflare.com/ips-v4 and /ips-v6, as of 2026-10-14), the
// default CDN. They change rarely; Cloudflare announces changes in advance.
var cloudflareRanges = mustParseCIDRs(
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
)

// The CDN in front of the service: its edge ranges and the header in which it
// passes the client's address. Set once at startup by ConfigureCDN; Cloudflare
// by default.
var (
	cdnRanges         = cloudflareRanges
	cdnClientIPHeader = "CF-Connecting-IP"
)

// ConfigureCDN sets the CDN whose client-address header ClientIP trusts.
// With both values empty it is Cloudflare (its published ranges and
// CF-Connecting-IP). For another CDN both are needed: ranges, a comma-separated
// list of the CDN's edge CIDRs, and header, one the CDN sets on every request
// (overwriting any value from the client) — with only one of them, the header
// a client can send through the other CDN would be trusted. ranges "none"
// means no CDN (DNS only), so no client-address header is trusted. It is
// called once at startup, before serving.
func ConfigureCDN(ranges, header string) error {
	ranges = strings.TrimSpace(ranges)
	header = strings.TrimSpace(header)
	switch {
	case strings.EqualFold(ranges, "none"):
		if header != "" {
			return fmt.Errorf("CDN_CLIENT_IP_HEADER is set but CDN_IP_RANGES is \"none\": with no CDN no header is trusted")
		}
		cdnRanges, cdnClientIPHeader = nil, ""
		return nil
	case ranges == "" && header == "":
		cdnRanges, cdnClientIPHeader = cloudflareRanges, "CF-Connecting-IP"
		return nil
	case ranges == "" || header == "":
		return fmt.Errorf("set both CDN_IP_RANGES and CDN_CLIENT_IP_HEADER for a CDN other than Cloudflare (or neither)")
	}
	switch http.CanonicalHeaderKey(header) {
	case "X-Forwarded-For", "Forwarded":
		return fmt.Errorf("CDN_CLIENT_IP_HEADER can't be %s: clients set it; use the header the CDN overwrites with the client's address", header)
	}
	parsed, err := parseCIDRs(strings.Split(ranges, ","))
	if err != nil {
		return fmt.Errorf("CDN_IP_RANGES: %w", err)
	}
	if len(parsed) == 0 {
		return fmt.Errorf("CDN_IP_RANGES has no ranges")
	}
	cdnRanges, cdnClientIPHeader = parsed, header
	return nil
}

// parseCIDRs parses CIDRs, skipping blank items (e.g. a trailing comma).
func parseCIDRs(cidrs []string) ([]*net.IPNet, error) {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	out, err := parseCIDRs(cidrs)
	if err != nil {
		panic(err)
	}
	return out
}

func isCDN(ip net.IP) bool {
	for _, n := range cdnRanges {
		if n.Contains(ip) {
			return true
		}
	}
	return false
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
		// HSTS: TLS terminates at the edge (Cloudflare/Traefik), so instruct the
		// browser to only use HTTPS for this host from now on.
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}
