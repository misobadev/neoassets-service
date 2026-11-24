package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"neoassets/internal/services"
	"neoassets/pkg/auth"
)

// admissionRequest is a scrape request whose subject has the given threads and
// whose context gives up after wait (so a refused request returns quickly).
func admissionRequest(t *testing.T, subject *auth.ScrapeSubject, wait time.Duration) *http.Request {
	t.Helper()
	ctx, cancel := context.WithTimeout(auth.WithScrapeSubject(context.Background(), subject), wait)
	t.Cleanup(cancel)
	return httptest.NewRequest(http.MethodGet, "/api/v1/scrape/games", nil).WithContext(ctx)
}

// blockingHandler holds its request until release is closed.
func blockingHandler(started chan<- struct{}, release <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	})
}

func TestScrapeAdmissionLimitsRequestsToThreads(t *testing.T) {
	svc := services.NewScrapeService(nil, nil, "secret", 2, 100, false, false)
	h := &Handler{scrapeSvc: svc}
	user := &auth.ScrapeSubject{Kind: auth.ScrapeKindUser, UserID: uuid.New(), Threads: 1}

	started, release := make(chan struct{}), make(chan struct{})
	mw := h.ScrapeAdmission(blockingHandler(started, release))
	done := make(chan *httptest.ResponseRecorder)
	go func() {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, admissionRequest(t, user, 5*time.Second))
		done <- rec
	}()
	<-started

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, admissionRequest(t, user, 50*time.Millisecond))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second concurrent request: status %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" || rec.Header().Get("X-Concurrency-Limit") != "1" {
		t.Fatalf("missing headers: %v", rec.Header())
	}

	close(release)
	if first := <-done; first.Code != http.StatusOK || first.Header().Get("X-Concurrency-Limit") != "1" {
		t.Fatalf("first request: status %d headers %v", first.Code, first.Header())
	}
}

func TestScrapeGlobalAdmissionReturns503WhenFull(t *testing.T) {
	svc := services.NewScrapeService(nil, nil, "secret", 2, 100, false, false)
	svc.SetMaxInFlight(1)
	h := &Handler{scrapeSvc: svc}
	a := &auth.ScrapeSubject{Kind: auth.ScrapeKindUser, UserID: uuid.New(), Threads: 4}
	b := &auth.ScrapeSubject{Kind: auth.ScrapeKindUser, UserID: uuid.New(), Threads: 4}

	started, release := make(chan struct{}), make(chan struct{})
	mw := h.ScrapeGlobalAdmission(blockingHandler(started, release))
	done := make(chan struct{})
	go func() {
		mw.ServeHTTP(httptest.NewRecorder(), admissionRequest(t, a, 5*time.Second))
		close(done)
	}()
	<-started

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, admissionRequest(t, b, 50*time.Millisecond))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("full server: status %d headers %v, want 503 with Retry-After", rec.Code, rec.Header())
	}
	close(release)
	<-done
}
