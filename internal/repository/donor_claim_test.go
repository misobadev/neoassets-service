package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"neoassets/internal/models"
)

var (
	claimDBOnce sync.Once
	claimDB     *sql.DB
	claimDBErr  error
)

// claimTestRepo returns a Repository on the database named by
// NEOASSETS_TEST_DATABASE_URL, migrated from scratch once per run. Its public
// schema is dropped first, so the database's name must contain "test". Without
// the variable the test is skipped.
func claimTestRepo(t *testing.T) *Repository {
	t.Helper()
	url := os.Getenv("NEOASSETS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("NEOASSETS_TEST_DATABASE_URL not set")
	}
	claimDBOnce.Do(func() { claimDB, claimDBErr = migrateClaimTestDB(url) })
	if claimDBErr != nil {
		t.Fatal(claimDBErr)
	}
	return NewRepository(claimDB)
}

func migrateClaimTestDB(url string) (*sql.DB, error) {
	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}
	var name string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		return nil, err
	}
	if !strings.Contains(strings.ToLower(name), "test") {
		return nil, fmt.Errorf("refusing to reset database %s: its name must contain \"test\"", name)
	}
	if _, err := db.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		return nil, err
	}
	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		return nil, err
	}
	m, err := migrate.NewWithDatabaseInstance("file://../../migrations", "postgres", driver)
	if err != nil {
		return nil, err
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return nil, err
	}
	return db, nil
}

func claimTestUser(t *testing.T, r *Repository) uuid.UUID {
	t.Helper()
	u, err := r.CreateUser(&models.User{
		Username:     "c" + uuid.NewString()[:8],
		Email:        uuid.NewString()[:8] + "@example.com",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// A claim allows a fixed number of checks; a new code resets the count.
func TestDonorClaimAttemptsAreLimited(t *testing.T) {
	r := claimTestRepo(t)
	id := claimTestUser(t, r)
	expires := time.Now().Add(15 * time.Minute)

	if ok, err := r.UpsertDonorClaim(id, "donor@example.com", "hash", expires, 0); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	for i := 1; i <= 5; i++ {
		if _, _, _, ok, err := r.UseDonorClaimAttempt(id, 5); err != nil || !ok {
			t.Fatalf("attempt %d refused: err=%v", i, err)
		}
	}
	if _, _, _, ok, err := r.UseDonorClaimAttempt(id, 5); err != nil || ok {
		t.Fatalf("a sixth attempt was allowed: ok=%v err=%v", ok, err)
	}

	if ok, err := r.UpsertDonorClaim(id, "donor@example.com", "hash2", expires, 0); err != nil || !ok {
		t.Fatalf("new code: ok=%v err=%v", ok, err)
	}
	email, hash, _, ok, err := r.UseDonorClaimAttempt(id, 5)
	if err != nil || !ok || email != "donor@example.com" || hash != "hash2" {
		t.Fatalf("after a new code: ok=%v err=%v email=%q hash=%q", ok, err, email, hash)
	}
}

// The limit holds when many checks run at once.
func TestDonorClaimAttemptsHoldUnderConcurrency(t *testing.T) {
	r := claimTestRepo(t)
	id := claimTestUser(t, r)
	if ok, err := r.UpsertDonorClaim(id, "donor@example.com", "hash", time.Now().Add(time.Hour), 0); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, _, ok, err := r.UseDonorClaimAttempt(id, 5); err == nil && ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 5 {
		t.Fatalf("%d of 25 concurrent checks allowed, want 5", allowed)
	}
}

func TestDonorClaimCooldown(t *testing.T) {
	r := claimTestRepo(t)
	id := claimTestUser(t, r)
	expires := time.Now().Add(15 * time.Minute)

	if ok, err := r.UpsertDonorClaim(id, "donor@example.com", "hash", expires, time.Minute); err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	if ok, err := r.UpsertDonorClaim(id, "other@example.com", "hash2", expires, time.Minute); err != nil || ok {
		t.Fatalf("a second code within the cooldown was stored: ok=%v err=%v", ok, err)
	}
	email, hash, _, ok, err := r.UseDonorClaimAttempt(id, 5)
	if err != nil || !ok || email != "donor@example.com" || hash != "hash" {
		t.Fatalf("claim replaced within the cooldown: ok=%v err=%v email=%q hash=%q", ok, err, email, hash)
	}
}

func TestUseDonorClaimAttemptWithoutAClaim(t *testing.T) {
	r := claimTestRepo(t)
	if _, _, _, ok, err := r.UseDonorClaimAttempt(claimTestUser(t, r), 5); err != nil || ok {
		t.Fatalf("no claim: ok=%v err=%v", ok, err)
	}
}
