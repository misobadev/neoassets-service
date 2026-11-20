package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"neoassets/internal/models"
	"neoassets/internal/progression"
)

// GetRewards returns the XP progression and unlocked limits for a user.
func (r *Repository) GetRewards(ctx context.Context, userID uuid.UUID) (*models.Rewards, error) {
	u, err := r.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	level := progression.LevelForXP(u.XP)
	rank := progression.RankForLevel(level)
	threads := r.econ.ThreadsForUser(u.Role, u.DonorStatus, u.XP)

	// Progress toward the next level keeps working past MaxLevel; the level is
	// unbounded, only the rank/thread benefits stop at the top band.
	levelXP := progression.XPForLevel(level)
	nextLevelXP := progression.XPForLevel(level + 1)
	progress := 100
	if span := nextLevelXP - levelXP; span > 0 {
		progress = int(float64(u.XP-levelXP) / float64(span) * 100)
	}

	return &models.Rewards{
		XP:                u.XP,
		Level:             level,
		Rank:              rank,
		Threads:           threads,
		DonorStatus:       u.DonorStatus,
		DonorBonusThreads: r.econ.DonorBonusThreads(u.DonorStatus),
		XPBonusPct:        r.econ.DonorXPBonusPct(u.DonorStatus),
		DailyGames:        threads * r.econ.DailyGamesPerThread,
		LevelXP:           levelXP,
		NextLevelXP:       nextLevelXP,
		ProgressPct:       progress,
	}, nil
}

// HasXPLedgerEntry reports whether a submission has already been credited, so
// awarding stays idempotent per submission (a re-approval cannot double-credit).
func (r *Repository) HasXPLedgerEntry(ctx context.Context, submissionID uuid.UUID) (bool, error) {
	var exists bool
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM xp_ledger WHERE submission_id = $1)`, submissionID).Scan(&exists); err != nil {
		return false, fmt.Errorf("failed to check xp ledger: %w", err)
	}
	return exists, nil
}

// SubmissionPointsByIDs returns the total XP awarded per submission id (the sum
// of the ledger deltas), keyed by submission id. Used to show contributors how
// many points each approved contribution earned.
func (r *Repository) SubmissionPointsByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]int, error) {
	out := map[uuid.UUID]int{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT submission_id, SUM(delta) FROM xp_ledger
		 WHERE submission_id = ANY($1) GROUP BY submission_id`, pq.Array(ids),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to sum submission points: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var total int
		if err := rows.Scan(&id, &total); err != nil {
			return nil, err
		}
		out[id] = total
	}
	return out, rows.Err()
}

// UserAwardedXPBySource returns the lifetime XP a user earned from approved
// metadata contributions and from approved system art pack contributions.
func (r *Repository) UserAwardedXPBySource(ctx context.Context, userID uuid.UUID) (metadataXP, sapXP int, err error) {
	if err = r.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(x.delta), 0)
		   FROM xp_ledger x
		   JOIN metadata_submissions ms ON ms.id = x.submission_id
		  WHERE x.user_id = $1 AND x.delta > 0 AND ms.status = 'approved'`,
		userID,
	).Scan(&metadataXP); err != nil {
		return 0, 0, fmt.Errorf("failed to sum metadata xp: %w", err)
	}
	if err = r.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(x.delta), 0)
		   FROM xp_ledger x
		   JOIN submissions s ON s.id = x.submission_id
		  WHERE x.user_id = $1 AND x.delta > 0 AND s.status = 'approved'`,
		userID,
	).Scan(&sapXP); err != nil {
		return 0, 0, fmt.Errorf("failed to sum sap xp: %w", err)
	}
	return metadataXP, sapXP, nil
}

// AwardXP credits a user's XP and records the reason in the ledger.
func (r *Repository) AwardXP(ctx context.Context, userID uuid.UUID, delta int, reason string, submissionID *uuid.UUID) error {
	if delta == 0 {
		return nil
	}
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// An award tied to a submission is granted once. The transaction-level
	// advisory lock serializes concurrent awards for the same submission, so
	// the existence check and the insert can't interleave (a re-approval or two
	// overlapping approvals never double-credit).
	if submissionID != nil {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "xp:"+submissionID.String()); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM xp_ledger WHERE submission_id = $1)`, *submissionID).Scan(&exists); err != nil {
			return fmt.Errorf("failed to check xp ledger: %w", err)
		}
		if exists {
			return nil
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET xp = xp + $1, updated_at = NOW() WHERE id = $2`, delta, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO xp_ledger (user_id, delta, reason, submission_id) VALUES ($1, $2, $3, $4)`,
		userID, delta, reason, submissionID,
	); err != nil {
		return err
	}
	return tx.Commit()
}
