package repository

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"neoassets/internal/models"
)

const donationEventColumns = `id, platform, external_id, email, from_name, amount_cents, currency, kind, tier_name, active, user_id, occurred_at, created_at`

// donorGraceDays is the inactivity window after which an auto-managed monthly
// supporter with no fresh subscription payment is downgraded. Configured at
// boot via ConfigureDonations.
var donorGraceDays = 40

// ConfigureDonations sets the subscription grace window in days. Zero keeps the
// default (40 days, a little over a monthly billing cycle).
func ConfigureDonations(graceDays int) {
	if graceDays > 0 {
		donorGraceDays = graceDays
	}
}

// UpsertDonationEvent inserts a payment, or refreshes it when the platform
// redelivers the same event. external_id is the platform transaction id, so
// repeated webhooks are idempotent. An already-linked user_id is preserved.
func (r *Repository) UpsertDonationEvent(ev *models.DonationEvent) (*models.DonationEvent, error) {
	var out models.DonationEvent
	err := r.db.QueryRow(`
		INSERT INTO donation_events
			(platform, external_id, email, from_name, amount_cents, currency, kind, tier_name, active, user_id, occurred_at, raw)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (platform, external_id) DO UPDATE SET
			email = COALESCE(NULLIF(EXCLUDED.email, ''), donation_events.email),
			from_name = COALESCE(NULLIF(EXCLUDED.from_name, ''), donation_events.from_name),
			amount_cents = COALESCE(NULLIF(EXCLUDED.amount_cents, 0), donation_events.amount_cents),
			currency = COALESCE(NULLIF(EXCLUDED.currency, ''), donation_events.currency),
			kind = EXCLUDED.kind,
			tier_name = COALESCE(NULLIF(EXCLUDED.tier_name, ''), donation_events.tier_name),
			active = EXCLUDED.active,
			occurred_at = COALESCE(EXCLUDED.occurred_at, donation_events.occurred_at),
			raw = EXCLUDED.raw,
			updated_at = NOW(),
			user_id = COALESCE(donation_events.user_id, EXCLUDED.user_id)
		RETURNING `+donationEventColumns,
		ev.Platform, ev.ExternalID, ev.Email, ev.FromName, ev.AmountCents, ev.Currency,
		ev.Kind, ev.TierName, ev.Active, ev.UserID, ev.OccurredAt, string(ev.Raw),
	).Scan(&out.ID, &out.Platform, &out.ExternalID, &out.Email, &out.FromName, &out.AmountCents,
		&out.Currency, &out.Kind, &out.TierName, &out.Active, &out.UserID, &out.OccurredAt, &out.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to upsert donation event: %w", err)
	}
	return &out, nil
}

// LinkDonationEventsByEmail attaches every unclaimed event with the given email
// to the user and reports how many rows were linked.
func (r *Repository) LinkDonationEventsByEmail(userID uuid.UUID, email string) (int64, error) {
	res, err := r.db.Exec(
		`UPDATE donation_events SET user_id = $1, updated_at = NOW()
		 WHERE LOWER(email) = LOWER($2) AND user_id IS NULL`,
		userID, email,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to link donation events: %w", err)
	}
	return res.RowsAffected()
}

// HasPendingDonationsForEmail reports whether an unclaimed donation exists for
// the email, used to decide whether a claim is worth starting.
func (r *Repository) HasPendingDonationsForEmail(email string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM donation_events WHERE LOWER(email) = LOWER($1) AND user_id IS NULL)`,
		email,
	).Scan(&exists)
	return exists, err
}

// CountDonationsByUser returns how many donation events are linked to the user.
func (r *Repository) CountDonationsByUser(userID uuid.UUID) (int, error) {
	var n int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM donation_events WHERE user_id = $1`, userID).Scan(&n)
	return n, err
}

// RecomputeDonorStatus derives the user's donor status from their linked
// donation events: an active subscription beats a one-time donation. A status
// set manually by an admin is never overwritten. Returns the resulting status.
func (r *Repository) RecomputeDonorStatus(userID uuid.UUID) (string, error) {
	var current, source string
	if err := r.db.QueryRow(`SELECT donor_status, donor_source FROM users WHERE id = $1`, userID).Scan(&current, &source); err != nil {
		return "", fmt.Errorf("failed to read donor status: %w", err)
	}
	if source == models.DonorSourceAdmin {
		return current, nil
	}

	var hasSubscription, hasOneTime bool
	if err := r.db.QueryRow(`
		SELECT
			EXISTS(SELECT 1 FROM donation_events
			       WHERE user_id = $1 AND kind = 'subscription' AND active
			         AND occurred_at >= NOW() - make_interval(days => $2)),
			EXISTS(SELECT 1 FROM donation_events WHERE user_id = $1 AND kind = 'one_time')
	`, userID, donorGraceDays).Scan(&hasSubscription, &hasOneTime); err != nil {
		return "", fmt.Errorf("failed to compute donor status: %w", err)
	}

	status := models.DonorNone
	if hasOneTime {
		status = models.DonorSupporter
	}
	if hasSubscription {
		status = models.DonorMonthlySupporter
	}
	if status == current {
		return status, nil
	}
	if _, err := r.db.Exec(
		`UPDATE users SET donor_status = $1, donor_source = 'auto', updated_at = NOW() WHERE id = $2`,
		status, userID,
	); err != nil {
		return "", fmt.Errorf("failed to update donor status: %w", err)
	}
	return status, nil
}

// StaleSubscriptionUserIDs returns auto-managed monthly supporters whose latest
// subscription payment is older than the grace window, so the background job
// can downgrade them (Ko-fi never notifies when a membership ends).
func (r *Repository) StaleSubscriptionUserIDs() ([]uuid.UUID, error) {
	rows, err := r.db.Query(`
		SELECT u.id FROM users u
		WHERE u.donor_source = 'auto'
		  AND u.donor_status = 'monthly_supporter'
		  AND EXISTS (SELECT 1 FROM donation_events e WHERE e.user_id = u.id AND e.kind = 'subscription' AND e.active)
		  AND NOT EXISTS (SELECT 1 FROM donation_events e
		                  WHERE e.user_id = u.id AND e.kind = 'subscription' AND e.active
		                    AND e.occurred_at >= NOW() - make_interval(days => $1))
	`, donorGraceDays)
	if err != nil {
		return nil, fmt.Errorf("failed to list stale subscriptions: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UpsertDonorClaim stores (or replaces) the pending claim for a user.
func (r *Repository) UpsertDonorClaim(userID uuid.UUID, email, tokenHash string, expiresAt time.Time) error {
	_, err := r.db.Exec(`
		INSERT INTO donor_claims (user_id, email, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE SET
			email = EXCLUDED.email, token_hash = EXCLUDED.token_hash,
			expires_at = EXCLUDED.expires_at, created_at = NOW()`,
		userID, email, tokenHash, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save donor claim: %w", err)
	}
	return nil
}

// GetDonorClaim returns the pending claim for a user, or sql.ErrNoRows.
func (r *Repository) GetDonorClaim(userID uuid.UUID) (email, tokenHash string, expiresAt time.Time, err error) {
	err = r.db.QueryRow(
		`SELECT email, token_hash, expires_at FROM donor_claims WHERE user_id = $1`, userID,
	).Scan(&email, &tokenHash, &expiresAt)
	if err == sql.ErrNoRows {
		return "", "", time.Time{}, sql.ErrNoRows
	}
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to read donor claim: %w", err)
	}
	return email, tokenHash, expiresAt, nil
}

// DeleteDonorClaim removes the pending claim for a user.
func (r *Repository) DeleteDonorClaim(userID uuid.UUID) error {
	_, err := r.db.Exec(`DELETE FROM donor_claims WHERE user_id = $1`, userID)
	return err
}
