package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func clientIPRequest(peer, xff, cf string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = peer
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	if cf != "" {
		r.Header.Set("CF-Connecting-IP", cf)
	}
	return r
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name, peer, xff, cf, want string
	}{
		// Behind a local proxy (Traefik), the address it saw is the entry it
		// appended last; entries before it came from the client.
		{"local proxy: the proxy's entry, not the client's", "10.0.1.5:4000", "6.6.6.6, 203.0.113.9", "", "203.0.113.9"},
		{"local proxy: single entry", "10.0.1.5:4000", "203.0.113.9", "", "203.0.113.9"},
		// Cloudflare's header is used only when the request reached the proxy
		// from Cloudflare.
		{"via Cloudflare", "10.0.1.5:4000", "162.158.10.1", "198.51.100.7", "198.51.100.7"},
		{"via Cloudflare (IPv6 edge)", "10.0.1.5:4000", "2606:4700:10::1", "198.51.100.7", "198.51.100.7"},
		{"Cloudflare header from a non-Cloudflare address", "10.0.1.5:4000", "203.0.113.9", "198.51.100.7", "203.0.113.9"},
		// A request that only passed through our own hops (a Cloudflare Tunnel,
		// an ingress proxy in front of Traefik) keeps Cloudflare's header.
		{"only private hops (tunnel)", "10.0.1.5:4000", "172.18.0.7", "198.51.100.7", "198.51.100.7"},
		{"only private hops, no forwarded entry", "10.0.1.5:4000", "", "198.51.100.7", "198.51.100.7"},
		{"private hops then Cloudflare", "10.0.1.5:4000", "6.6.6.6, 162.158.10.1, 10.0.0.2", "198.51.100.7", "198.51.100.7"},
		{"private hops then another public address", "10.0.1.5:4000", "162.158.10.1, 203.0.113.9, 10.0.0.2", "198.51.100.7", "203.0.113.9"},
		{"several header lines", "10.0.1.5:4000", "6.6.6.6", "", "203.0.113.9"},
		{"a private hop in its own header line", "10.0.1.5:4000", "198.51.100.4", "", "198.51.100.4"},
		{"empty entries", "10.0.1.5:4000", "6.6.6.6, ,203.0.113.9", "", "203.0.113.9"},
		{"bracketed IPv6 entry", "10.0.1.5:4000", "[2001:db8::7]", "", "2001:db8::7"},
		{"unparsable entry after private hops", "10.0.1.5:4000", "198.18.0.1, junk, 10.0.0.2", "198.51.100.7", "198.51.100.7"},
		{"private IPv4-mapped peer", "[::ffff:10.0.1.5]:4000", "203.0.113.9", "", "203.0.113.9"},
		{"entry with a port", "10.0.1.5:4000", "203.0.113.9:5678", "", "203.0.113.9"},
		{"IPv4-mapped peer", "[::ffff:203.0.113.9]:5000", "6.6.6.6", "198.51.100.7", "203.0.113.9"},
		// Without a local proxy, the peer is the edge.
		{"Cloudflare directly", "104.16.1.1:443", "", "198.51.100.7", "198.51.100.7"},
		{"public peer: headers ignored", "203.0.113.9:5000", "6.6.6.6", "198.51.100.7", "203.0.113.9"},
		// Values must be addresses.
		{"forwarded entry not an address", "10.0.1.5:4000", "6.6.6.6, not-an-ip", "", "10.0.1.5"},
		{"Cloudflare header not an address", "10.0.1.5:4000", "162.158.10.1", "not-an-ip", "162.158.10.1"},
		{"canonical form", "10.0.1.5:4000", "2001:DB8:0:0::1", "", "2001:db8::1"},
	}
	// Further X-Forwarded-For header lines, by case.
	extraLines := map[string][]string{
		"several header lines":                 {"203.0.113.9"},
		"a private hop in its own header line": {"10.0.0.2"},
	}
	for _, c := range cases {
		r := clientIPRequest(c.peer, c.xff, c.cf)
		for _, line := range extraLines[c.name] {
			r.Header.Add("X-Forwarded-For", line)
		}
		if got := ClientIP(r); got != c.want {
			t.Errorf("%s: ClientIP = %q, want %q", c.name, got, c.want)
		}
	}
}

// TRUST_PROXY_HEADERS=always treats the public peer as our proxy, and only
// that peer: entries further left and Cloudflare's header from a
// non-Cloudflare client are still ignored.
func TestClientIPTrustingAPublicProxy(t *testing.T) {
	trustProxyHeadersAlways = true
	defer func() { trustProxyHeadersAlways = false }()
	cases := []struct{ xff, cf, want string }{
		{"198.51.100.7", "", "198.51.100.7"},
		{"6.6.6.6, 198.51.100.7", "", "198.51.100.7"},
		{"198.51.100.7", "6.6.6.6", "198.51.100.7"},
		{"162.158.10.1", "198.51.100.7", "198.51.100.7"},
	}
	for _, c := range cases {
		if got := ClientIP(clientIPRequest("203.0.113.1:443", c.xff, c.cf)); got != c.want {
			t.Errorf("XFF %q, CF %q: ClientIP = %q, want %q", c.xff, c.cf, got, c.want)
		}
	}
}

func TestClientKeyGroupsIPv6By64(t *testing.T) {
	key := func(addr string) string { return ClientKey(clientIPRequest("10.0.1.5:4000", addr, "")) }
	if a, b := key("2001:db8:1:2:aaaa::1"), key("2001:db8:1:2:bbbb::2"); a != b {
		t.Fatalf("same /64 gave keys %q and %q", a, b)
	}
	if a, b := key("2001:db8:1:2::1"), key("2001:db8:1:3::1"); a == b {
		t.Fatalf("different /64s share key %q", a)
	}
	if got := key("203.0.113.9"); got != "203.0.113.9" {
		t.Fatalf("IPv4 key = %q", got)
	}
	if got := key("::ffff:203.0.113.9"); got != "203.0.113.9" {
		t.Fatalf("IPv4-mapped key = %q", got)
	}
}

// Addresses in one IPv6 /64 share a bucket.
func TestLimiterLimitsAnIPv6Prefix(t *testing.T) {
	l := NewIPLimiter(1, 1)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := l.Handler(ok)
	codes := []int{}
	for _, addr := range []string{"2001:db8:1:2::1", "2001:db8:1:2::2"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, clientIPRequest("10.0.1.5:4000", addr, ""))
		codes = append(codes, w.Code)
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v, want [200 429]", codes)
	}
}

// The limiter's memory is bounded however many keys arrive, without scanning
// every entry on each request.
func TestLimiterMemoryIsBounded(t *testing.T) {
	l := NewIPLimiter(60, 1)
	l.maxKeys = 100
	for i := 0; i < 1000; i++ {
		l.allow(fmt.Sprintf("k%d", i))
	}
	if n := l.size(); n > 2*l.maxKeys {
		t.Fatalf("%d keys held, want at most %d", n, 2*l.maxKeys)
	}
}

// A limited key stays limited when its generation rotates, and an idle key is
// forgotten after the TTL.
func TestLimiterKeepsActiveKeysAcrossRotation(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewIPLimiter(1, 1)
	l.now = func() time.Time { return now }
	l.maxKeys = 10
	if !l.allow("a") || l.allow("a") {
		t.Fatal("burst of 1 not applied")
	}
	for i := 0; i < l.maxKeys; i++ { // fills a generation: rotates
		l.allow(fmt.Sprintf("k%d", i))
	}
	if l.allow("a") {
		t.Fatal("a limited key was reset by a rotation")
	}
	if !l.has("k9") {
		t.Fatal("a recent key was dropped")
	}
	// One TTL later the generations shift: a key used since survives the next
	// shift, one that wasn't is dropped.
	now = now.Add(limiterTTL)
	l.allow("k9")
	now = now.Add(limiterTTL)
	l.allow("other")
	if !l.has("k9") || l.has("k8") {
		t.Fatalf("after two shifts: k9 kept %v (want true), k8 kept %v (want false)", l.has("k9"), l.has("k8"))
	}
	now = now.Add(2 * limiterTTL)
	l.allow("x")
	if l.has("k9") {
		t.Fatal("a key idle for two TTLs was kept")
	}
}

// useCDN configures the CDN for one test and restores the default afterwards.
func useCDN(t *testing.T, ranges, header string) {
	t.Helper()
	if err := ConfigureCDN(ranges, header); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ConfigureCDN("", ""); err != nil {
			t.Fatal(err)
		}
	})
}

// Another CDN in front of Traefik: its ranges and header replace Cloudflare's.
func TestClientIPBehindAnotherCDN(t *testing.T) {
	useCDN(t, "151.101.0.0/16, 2a04:4e40::/32", "Fastly-Client-IP")
	cases := []struct {
		name, xff, header, value, want string
	}{
		{"the CDN's header from the CDN", "151.101.1.1", "Fastly-Client-IP", "198.51.100.7", "198.51.100.7"},
		{"the CDN's header from its IPv6 edge", "2a04:4e40::9", "Fastly-Client-IP", "198.51.100.7", "198.51.100.7"},
		{"the CDN's header from elsewhere", "203.0.113.9", "Fastly-Client-IP", "198.51.100.7", "203.0.113.9"},
		{"Cloudflare's header no longer trusted", "162.158.10.1", "CF-Connecting-IP", "198.51.100.7", "162.158.10.1"},
		{"the CDN's header through private hops", "172.18.0.7", "Fastly-Client-IP", "198.51.100.7", "198.51.100.7"},
	}
	for _, c := range cases {
		r := clientIPRequest("10.0.1.5:4000", c.xff, "")
		r.Header.Set(c.header, c.value)
		if got := ClientIP(r); got != c.want {
			t.Errorf("%s: ClientIP = %q, want %q", c.name, got, c.want)
		}
	}
}

// No CDN (DNS only): no client-address header is trusted, from anywhere.
func TestClientIPWithoutACDN(t *testing.T) {
	useCDN(t, "none", "")
	if got := ClientIP(clientIPRequest("10.0.1.5:4000", "162.158.10.1", "198.51.100.7")); got != "162.158.10.1" {
		t.Errorf("from a Cloudflare address: ClientIP = %q", got)
	}
	if got := ClientIP(clientIPRequest("10.0.1.5:4000", "172.18.0.7", "198.51.100.7")); got != "172.18.0.7" {
		t.Errorf("through private hops: ClientIP = %q", got)
	}
	if got := ClientIP(clientIPRequest("10.0.1.5:4000", "203.0.113.9", "")); got != "203.0.113.9" {
		t.Errorf("direct client: ClientIP = %q", got)
	}
}

func TestConfigureCDN(t *testing.T) {
	t.Cleanup(func() { _ = ConfigureCDN("", "") })
	refused := []struct{ name, ranges, header string }{
		{"invalid range", "151.101.0.0/16, not-a-range", "Fastly-Client-IP"},
		{"ranges without a header", "151.101.0.0/16", ""},
		{"a header without ranges", "", "Fastly-Client-IP"},
		{"a header with none", "none", "Fastly-Client-IP"},
		{"X-Forwarded-For as the header", "151.101.0.0/16", "x-forwarded-for"},
		{"Forwarded as the header", "151.101.0.0/16", "Forwarded"},
		{"no ranges at all", " , ", "Fastly-Client-IP"},
	}
	for _, c := range refused {
		if err := ConfigureCDN(c.ranges, c.header); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	if err := ConfigureCDN("", ""); err != nil || cdnClientIPHeader != "CF-Connecting-IP" || len(cdnRanges) != len(cloudflareRanges) {
		t.Fatalf("defaults: %v, header %q, %d ranges", err, cdnClientIPHeader, len(cdnRanges))
	}
	if err := ConfigureCDN("151.101.0.0/16, 2a04:4e40::/32,", "Fastly-Client-IP"); err != nil || len(cdnRanges) != 2 {
		t.Fatalf("a trailing comma: %v, %d ranges", err, len(cdnRanges))
	}
	if err := ConfigureCDN("NONE", ""); err != nil || cdnClientIPHeader != "" || len(cdnRanges) != 0 {
		t.Fatalf("none: %v, header %q, %d ranges", err, cdnClientIPHeader, len(cdnRanges))
	}
}
