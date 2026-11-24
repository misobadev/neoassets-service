package services

import (
	"testing"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/internal/repository"
	"neoassets/internal/testdb"
	"neoassets/pkg/auth"
)

// The pre-authentication limiter is per app and per caller address: one
// developer app used by many people (a frontend) is not capped as a whole,
// while one address hammering it with bogus user keys still is.
func TestPreCheckIsPerAppAndAddress(t *testing.T) {
	repo := repository.NewRepository(testdb.Open(t, "services_precheck"))
	ctx := t.Context()
	owner, err := repo.CreateUser(ctx, &models.User{Username: "p" + uuid.NewString()[:8], Email: uuid.NewString()[:8] + "@example.com", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	clientID, secret := "cid-"+uuid.NewString(), "secret-"+uuid.NewString()
	if _, err := repo.CreateDeveloperApp(ctx, owner.ID, "Frontend", "", "", clientID, auth.HashToken(secret), ""); err != nil {
		t.Fatal(err)
	}
	// rpmPerThread 1: the pre-check allows 16 RPM with a burst of 16.
	s := NewScrapeService(repo, nil, "jwt", 2, 1, false, false)
	creds := func(key string) auth.ScrapeCredentials {
		return auth.ScrapeCredentials{ClientID: clientID, ClientSecret: secret, UserCredential: "ns_live_bogus", ClientKey: key}
	}

	refusedAt := -1
	for i := 0; i < 40; i++ {
		_, err := s.AuthenticateScrape(ctx, creds("203.0.113.1"))
		var ae *auth.AuthError
		if asAuthError(err, &ae) && ae.Status == 429 {
			refusedAt = i
			break
		}
	}
	if refusedAt != 16 {
		t.Fatalf("one address was refused at request %d, want 16 (the pre-check burst)", refusedAt)
	}
	// Another person using the same app is not affected.
	_, err = s.AuthenticateScrape(ctx, creds("198.51.100.7"))
	var ae *auth.AuthError
	if !asAuthError(err, &ae) || ae.Status != 401 {
		t.Fatalf("another address: err = %v, want the bogus key's 401, not a rate limit", err)
	}
}

func asAuthError(err error, target **auth.AuthError) bool {
	ae, ok := err.(*auth.AuthError)
	if ok {
		*target = ae
	}
	return ok
}
