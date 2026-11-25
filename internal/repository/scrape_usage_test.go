package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// The daily usage counter has no limit: every call counts and is stored.
func TestScrapeUsageCountsWithoutLimit(t *testing.T) {
	r, db := loadTestRepo(t)
	ctx := t.Context()
	day := time.Now().UTC()
	subject := uuid.NewString()

	for want := 1; want <= 5; want++ {
		got, err := r.IncrementScrapeUsage(ctx, day, "user", subject)
		if err != nil || got != want {
			t.Fatalf("call %d: got (%d, %v), want (%d, nil)", want, got, err, want)
		}
	}
	var stored int
	if err := db.QueryRow(`SELECT count FROM scrape_usage WHERE subject_id = $1`, subject).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 5 {
		t.Fatalf("stored count %d, want 5", stored)
	}
	if got, err := r.GetScrapeUsage(ctx, day, "user", subject); err != nil || got != 5 {
		t.Fatalf("GetScrapeUsage = (%d, %v), want 5", got, err)
	}
}
