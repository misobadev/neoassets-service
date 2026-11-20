package repository

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/internal/testdb"
)

func TestWithReviewLockRefusesAConcurrentReview(t *testing.T) {
	r := NewRepository(testdb.Open(t, "repository_review"))
	ctx := context.Background()
	id := uuid.New()

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error)
	go func() {
		done <- r.WithReviewLock(ctx, id, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	if err := r.WithReviewLock(ctx, id, func() error { return nil }); !errors.Is(err, ErrReviewInProgress) {
		t.Fatalf("second review while the first holds the lock: got %v, want ErrReviewInProgress", err)
	}
	// A different submission is not blocked.
	if err := r.WithReviewLock(ctx, uuid.New(), func() error { return nil }); err != nil {
		t.Fatalf("other submission: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Released: the same submission can be reviewed again.
	if err := r.WithReviewLock(ctx, id, func() error { return nil }); err != nil {
		t.Fatalf("after release: %v", err)
	}
}

func TestWithReviewLockReturnsTheCallbackError(t *testing.T) {
	r := NewRepository(testdb.Open(t, "repository_review"))
	want := errors.New("boom")
	if err := r.WithReviewLock(context.Background(), uuid.New(), func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

// Overlapping awards for one submission credit the user exactly once.
func TestAwardXPOncePerSubmissionUnderConcurrency(t *testing.T) {
	r := NewRepository(testdb.Open(t, "repository_review"))
	u, err := r.CreateUser(t.Context(), &models.User{
		Username:     "xp" + uuid.NewString()[:8],
		Email:        uuid.NewString()[:8] + "@example.com",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatal(err)
	}
	sub := uuid.New()

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- r.AwardXP(t.Context(), u.ID, 50, "approved_metadata", &sub)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := r.GetUserByID(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.XP != 50 {
		t.Fatalf("xp = %d, want 50 (credited once)", got.XP)
	}
	var rows int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM xp_ledger WHERE submission_id = $1`, sub).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("ledger rows = %d, want 1", rows)
	}

	// Awards without a submission are not deduplicated.
	if err := r.AwardXP(t.Context(), u.ID, 5, "manual", nil); err != nil {
		t.Fatal(err)
	}
	if err := r.AwardXP(t.Context(), u.ID, 5, "manual", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.GetUserByID(t.Context(), u.ID); got.XP != 60 {
		t.Fatalf("xp = %d, want 60", got.XP)
	}
}
