package services

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/internal/repository"
	"neoassets/internal/testdb"
	"neoassets/pkg/auth"
)

// Only a credential that does not exist is a 401. Any other lookup failure
// is a 503 with Retry-After, so a client keeps a valid key instead of
// treating it as invalid.
func TestCredentialLookupError(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"missing", sql.ErrNoRows, http.StatusUnauthorized},
		{"wrapped missing", errors.Join(errors.New("scan"), sql.ErrNoRows), http.StatusUnauthorized},
		{"database error", errors.New("connection reset"), http.StatusServiceUnavailable},
		{"timeout", context.DeadlineExceeded, http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		var ae *auth.AuthError
		if !asAuthError(credentialLookupError(ctx, c.err, "invalid"), &ae) {
			t.Fatalf("%s: not an AuthError", c.name)
		}
		if ae.Status != c.status {
			t.Errorf("%s: status %d, want %d", c.name, ae.Status, c.status)
		}
		if c.status == http.StatusServiceUnavailable && ae.RetryAfter <= 0 {
			t.Errorf("%s: no Retry-After on a 503", c.name)
		}
		if c.status == http.StatusUnauthorized && ae.Message != "invalid" {
			t.Errorf("%s: message %q, want the invalid message", c.name, ae.Message)
		}
	}
}

func TestAuthenticateScrapeLookupFailures(t *testing.T) {
	repo := repository.NewRepository(testdb.Open(t, "services_credlookup"))
	ctx := t.Context()
	owner, err := repo.CreateUser(ctx, &models.User{Username: "c" + uuid.NewString()[:8], Email: uuid.NewString()[:8] + "@example.com", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	clientID, secret := "cid-"+uuid.NewString(), "secret-"+uuid.NewString()
	if _, err := repo.CreateDeveloperApp(ctx, owner.ID, "App", "", "", clientID, auth.HashToken(secret), ""); err != nil {
		t.Fatal(err)
	}
	s := NewScrapeService(repo, nil, "jwt", 2, 60, false, false)
	status := func(ctx context.Context, creds auth.ScrapeCredentials) int {
		_, err := s.AuthenticateScrape(ctx, creds)
		var ae *auth.AuthError
		if !asAuthError(err, &ae) {
			t.Fatalf("err = %v, want an AuthError", err)
		}
		return ae.Status
	}

	if got := status(ctx, auth.ScrapeCredentials{ClientID: "cid-unknown", ClientSecret: secret}); got != http.StatusUnauthorized {
		t.Errorf("unknown app: status %d, want 401", got)
	}
	if got := status(ctx, auth.ScrapeCredentials{ClientID: clientID, ClientSecret: secret, UserCredential: auth.APIKeyPrefix + "unknown-key"}); got != http.StatusUnauthorized {
		t.Errorf("unknown user key: status %d, want 401", got)
	}
	// A lookup that fails (here, a canceled request) is not an invalid key.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got := status(canceled, auth.ScrapeCredentials{ClientID: clientID, ClientSecret: secret}); got != http.StatusServiceUnavailable {
		t.Errorf("failed app lookup: status %d, want 503", got)
	}
}
