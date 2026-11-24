package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// The daily counter stops at the limit: refused calls report the limit and
// don't keep writing to the row.
func TestScrapeUsageStopsAtTheLimit(t *testing.T) {
	r, db := loadTestRepo(t)
	ctx := t.Context()
	day := time.Now().UTC()
	subject := uuid.NewString()

	for want := 1; want <= 3; want++ {
		got, ok, err := r.IncrementScrapeUsage(ctx, day, "user", subject, 3)
		if err != nil || !ok || got != want {
			t.Fatalf("call %d: got (%d, %v, %v), want (%d, true, nil)", want, got, ok, err, want)
		}
	}
	for i := 0; i < 2; i++ {
		got, ok, err := r.IncrementScrapeUsage(ctx, day, "user", subject, 3)
		if err != nil || ok || got != 3 {
			t.Fatalf("over the limit: got (%d, %v, %v), want (3, false, nil)", got, ok, err)
		}
	}
	var stored int
	if err := db.QueryRow(`SELECT count FROM scrape_usage WHERE subject_id = $1`, subject).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 3 {
		t.Fatalf("stored count %d, want 3", stored)
	}
	// A raised limit (a level-up) lets the account continue.
	if got, ok, _ := r.IncrementScrapeUsage(ctx, day, "user", subject, 4); !ok || got != 4 {
		t.Fatalf("after raising the limit: got (%d, %v)", got, ok)
	}
}
