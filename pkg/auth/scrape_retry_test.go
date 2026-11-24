package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type refuseAuth struct{ err error }

func (a refuseAuth) AuthenticateScrape(context.Context, ScrapeCredentials) (*ScrapeSubject, error) {
	return nil, a.err
}

func TestScrapeMiddlewareSendsRetryAfter(t *testing.T) {
	mw := ScrapeMiddleware(refuseAuth{&AuthError{Status: 429, Message: "rate limit exceeded", RetryAfter: 1200 * time.Millisecond}}, nil)
	rec := httptest.NewRecorder()
	mw(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/scrape/games", nil))
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("status %d Retry-After %q, want 429 and 2 (1.2 s rounded up)", rec.Code, rec.Header().Get("Retry-After"))
	}

	mw = ScrapeMiddleware(refuseAuth{&AuthError{Status: 401, Message: "unauthorized"}}, nil)
	rec = httptest.NewRecorder()
	mw(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/scrape/games", nil))
	if rec.Header().Get("Retry-After") != "" {
		t.Fatal("a credential error must not carry Retry-After")
	}
}

func TestRetryAfterSecondsRoundsUp(t *testing.T) {
	for d, want := range map[time.Duration]int{0: 1, 10 * time.Millisecond: 1, time.Second: 1, 1001 * time.Millisecond: 2, 59 * time.Second: 59} {
		if got := RetryAfterSeconds(d); got != want {
			t.Errorf("RetryAfterSeconds(%v) = %d, want %d", d, got, want)
		}
	}
}
