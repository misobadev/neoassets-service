package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"neoassets/internal/models"
)

func addDonationEvent(t *testing.T, r *Repository, userID uuid.UUID, kind string, active bool, occurred *time.Time) {
	t.Helper()
	_, err := r.db.ExecContext(t.Context(),
		`INSERT INTO donation_events (platform, external_id, email, kind, user_id, active, occurred_at)
		 VALUES ('kofi', $1, 'donor@example.com', $2, $3, $4, $5)`,
		uuid.NewString(), kind, userID, active, occurred)
	if err != nil {
		t.Fatal(err)
	}
}

// A monthly supporter whose subscription lapses keeps the supporter status
// for good; someone who never paid stays none.
func TestRecomputeDonorStatusKeepsPaidSubscribersAsSupporters(t *testing.T) {
	r := claimTestRepo(t)
	ctx := t.Context()

	now := time.Now()
	monthly := claimTestUser(t, r)
	addDonationEvent(t, r, monthly, models.DonationKindSubscription, true, &now)
	if got, err := r.RecomputeDonorStatus(ctx, monthly); err != nil || got != models.DonorMonthlySupporter {
		t.Fatalf("active subscription: %q, %v", got, err)
	}

	lapsed := claimTestUser(t, r)
	old := now.AddDate(0, 0, -90)
	addDonationEvent(t, r, lapsed, models.DonationKindSubscription, true, &old)
	if got, err := r.RecomputeDonorStatus(ctx, lapsed); err != nil || got != models.DonorSupporter {
		t.Fatalf("lapsed subscription: %q, %v, want supporter", got, err)
	}

	ended := claimTestUser(t, r)
	addDonationEvent(t, r, ended, models.DonationKindSubscription, false, &old)
	if got, err := r.RecomputeDonorStatus(ctx, ended); err != nil || got != models.DonorSupporter {
		t.Fatalf("ended membership that was charged: %q, %v, want supporter", got, err)
	}

	neverCharged := claimTestUser(t, r)
	addDonationEvent(t, r, neverCharged, models.DonationKindSubscription, false, nil)
	if got, err := r.RecomputeDonorStatus(ctx, neverCharged); err != nil || got != models.DonorNone {
		t.Fatalf("never charged: %q, %v, want none", got, err)
	}
}
