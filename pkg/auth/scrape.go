package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Scrape subject kinds.
const (
	ScrapeKindUser  = "user"
	ScrapeKindGuest = "guest"
)

// ScrapeSubject is the authenticated identity behind a scraping API request: a
// developer application, plus an optional registered user (guest when absent).
type ScrapeSubject struct {
	Kind       string
	UserID     uuid.UUID
	Username   string
	ClientID   string
	ClientDBID uuid.UUID
	ClientName string
	// ClientOwnerID is the registered user that owns the developer app.
	ClientOwnerID uuid.UUID
	// ClientKey is the caller's network address (IPv4, or IPv6 /64). A guest
	// has no account, so its threads, rate and daily quota are keyed by it:
	// each person using a guest-capable app gets their own allowance.
	ClientKey    string
	SoftwareName string
	Threads      int
	DailyLimit   int
	// RateLimit is the subject's requests per minute and RateRemaining the
	// requests it can make right now without waiting (the bucket's tokens).
	RateLimit     int
	RateRemaining int
	// Debug is true when the request carried a valid developer debug password.
	Debug          bool
	ForceUsed      *int
	ForceRateLimit bool
}

// IsGuest reports whether the subject has no user credential.
func (s *ScrapeSubject) IsGuest() bool { return s.Kind == ScrapeKindGuest }

// ScrapeCredentials is the raw credential and debug input of a scrape request.
// ForceUsed and ForceThreads are debug overrides applied only when DebugPassword
// is valid.
type ScrapeCredentials struct {
	ClientID       string
	ClientSecret   string
	UserCredential string
	SoftwareName   string
	DebugPassword  string
	ForceUsed      *int
	ForceThreads   *int
	ForceRateLimit bool
	// ClientKey identifies the caller's network address (an IPv4 address or
	// an IPv6 /64). It keys the pre-authentication limiter, so one developer
	// app shipped to many users is not capped as a whole.
	ClientKey string
}

// ScrapeAuthenticator resolves the credential layers of a scrape request into a
// subject. It is implemented by the scraping service so this package stays free
// of database dependencies.
type ScrapeAuthenticator interface {
	AuthenticateScrape(ctx context.Context, creds ScrapeCredentials) (*ScrapeSubject, error)
}

// AuthError carries the HTTP status a credential failure should map to.
type AuthError struct {
	Status  int
	Message string
	// RetryAfter, when set, is sent as the Retry-After header (whole seconds,
	// rounded up) so a rate-limited client knows exactly how long to wait.
	RetryAfter time.Duration
}

func (e *AuthError) Error() string { return e.Message }

type scrapeContextKey string

const scrapeSubjectKey scrapeContextKey = "scrape_subject"

// WithScrapeSubject stores the resolved scrape subject in the request context.
func WithScrapeSubject(ctx context.Context, s *ScrapeSubject) context.Context {
	return context.WithValue(ctx, scrapeSubjectKey, s)
}

// ScrapeSubjectFromContext returns the scrape subject stored in the context.
func ScrapeSubjectFromContext(ctx context.Context) (*ScrapeSubject, bool) {
	s, ok := ctx.Value(scrapeSubjectKey).(*ScrapeSubject)
	return s, ok
}

// ScrapeMiddleware validates developer app credentials (always) and an optional
// user credential, then stores the resolved subject in the request context. The
// debug password is only accepted via the X-Debug-Password header: accepting it
// as a query parameter would leak it into access logs, proxies and history.
//
// clientKey returns the caller's address key (nil leaves it empty).
func ScrapeMiddleware(a ScrapeAuthenticator, clientKey func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			debugPassword := r.Header.Get("X-Debug-Password")
			softwareName := q.Get("softname")
			if softwareName == "" {
				softwareName = r.Header.Get("X-Software-Name")
			}
			creds := ScrapeCredentials{
				ClientID:       r.Header.Get("X-Client-Id"),
				ClientSecret:   r.Header.Get("X-Client-Secret"),
				UserCredential: bearer(r),
				SoftwareName:   softwareName,
				DebugPassword:  debugPassword,
				ForceUsed:      queryInt(q, "forcerequestok"),
				ForceThreads:   queryInt(q, "forcethreads"),
				ForceRateLimit: q.Get("forceratelimit") == "1",
			}
			if clientKey != nil {
				creds.ClientKey = clientKey(r)
			}

			subject, err := a.AuthenticateScrape(r.Context(), creds)
			if err != nil {
				status := http.StatusUnauthorized
				message := "unauthorized"
				var ae *AuthError
				if errors.As(err, &ae) {
					status = ae.Status
					message = ae.Message
				}
				if ae != nil && ae.RetryAfter > 0 {
					w.Header().Set("Retry-After", strconv.Itoa(RetryAfterSeconds(ae.RetryAfter)))
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
				return
			}
			next.ServeHTTP(w, r.WithContext(WithScrapeSubject(r.Context(), subject)))
		})
	}
}

// queryInt parses an optional integer query parameter, returning nil when it is
// absent or invalid.
func queryInt(q url.Values, key string) *int {
	raw := q.Get(key)
	if raw == "" {
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return &n
}

// RetryAfterSeconds rounds a wait up to whole seconds (at least 1), the unit
// of the Retry-After header.
func RetryAfterSeconds(d time.Duration) int {
	s := int((d + time.Second - 1) / time.Second)
	if s < 1 {
		s = 1
	}
	return s
}
