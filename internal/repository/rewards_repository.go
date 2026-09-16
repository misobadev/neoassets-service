package repository

import (
	"fmt"
	"math"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"neoassets/internal/models"
	"neoassets/internal/progression"
)

var (
	// AdminThreads is the thread count granted to admin accounts.
	AdminThreads = 16
	// GuestThreads is the thread count for anonymous scraping (developer
	// credentials without a user credential).
	GuestThreads = 2
	// SupporterBonusThreads is the additive thread bonus of a one-time donation.
	SupporterBonusThreads = 2
	// MonthlySupporterBonusThreads is the additive thread bonus of a monthly
	// donation.
	MonthlySupporterBonusThreads = 4
	// SupporterXPBonusPct is the XP bonus (percent) of a one-time donation.
	SupporterXPBonusPct = 25
	// MonthlySupporterXPBonusPct is the XP bonus (percent) of a monthly donation.
	MonthlySupporterXPBonusPct = 50
	// DailyGamesPerThread is the daily game scrape quota granted per thread.
	// The effective daily limit is threads * DailyGamesPerThread.
	DailyGamesPerThread = 1000
)

// ConfigureThreads overrides the guest/admin/donor thread bonuses from
// environment values. Any non-positive argument keeps the current value. Call
// once at startup before services are built.
func ConfigureThreads(adminThreads, guestThreads, supporterBonusThreads, monthlySupporterBonusThreads int) {
	if adminThreads > 0 {
		AdminThreads = adminThreads
	}
	if guestThreads > 0 {
		GuestThreads = guestThreads
	}
	if supporterBonusThreads > 0 {
		SupporterBonusThreads = supporterBonusThreads
	}
	if monthlySupporterBonusThreads > 0 {
		MonthlySupporterBonusThreads = monthlySupporterBonusThreads
	}
}

// ConfigureRewards overrides the daily quota per thread and the donor XP
// bonuses (percent). A non-positive value keeps the current value.
func ConfigureRewards(dailyGamesPerThread, supporterXPBonusPct, monthlySupporterXPBonusPct int) {
	if dailyGamesPerThread > 0 {
		DailyGamesPerThread = dailyGamesPerThread
	}
	if supporterXPBonusPct > 0 {
		SupporterXPBonusPct = supporterXPBonusPct
	}
	if monthlySupporterXPBonusPct > 0 {
		MonthlySupporterXPBonusPct = monthlySupporterXPBonusPct
	}
}

// DonorBonusThreads returns the additive thread bonus of a donor status.
func DonorBonusThreads(status string) int {
	switch status {
	case models.DonorSupporter:
		return SupporterBonusThreads
	case models.DonorMonthlySupporter:
		return MonthlySupporterBonusThreads
	default:
		return 0
	}
}

// DonorXPBonusPct returns the XP bonus (percent) of a donor status.
func DonorXPBonusPct(status string) int {
	switch status {
	case models.DonorSupporter:
		return SupporterXPBonusPct
	case models.DonorMonthlySupporter:
		return MonthlySupporterXPBonusPct
	default:
		return 0
	}
}

// ApplyXPBonus returns base XP increased by pct percent (rounded).
func ApplyXPBonus(baseXP, pct int) int {
	return int(math.Round(float64(baseXP) * float64(100+pct) / 100))
}

// ThreadsForUser returns the effective scraping threads: admins are maxed,
// everyone else gets their level-rank base threads plus the donor bonus, capped
// at the hard maximum (donations never grant XP or rank, only bonus threads).
func ThreadsForUser(role, donorStatus string, xp int) int {
	if role == models.RoleAdmin {
		return AdminThreads
	}
	base := progression.ThreadsForLevel(progression.LevelForXP(xp))
	total := base + DonorBonusThreads(donorStatus)
	if max := progression.MaxThreads(); total > max {
		total = max
	}
	return total
}

// GetRewards returns the XP progression and unlocked limits for a user.
func (r *Repository) GetRewards(userID uuid.UUID) (*models.Rewards, error) {
	u, err := r.GetUserByID(userID)
	if err != nil {
		return nil, err
	}
	level := progression.LevelForXP(u.XP)
	rank := progression.RankForLevel(level)
	threads := ThreadsForUser(u.Role, u.DonorStatus, u.XP)

	levelXP := progression.XPForLevel(level)
	nextLevelXP := 0
	progress := 100
	if level < progression.MaxLevel {
		nextLevelXP = progression.XPForLevel(level + 1)
		span := nextLevelXP - levelXP
		if span > 0 {
			progress = int(float64(u.XP-levelXP) / float64(span) * 100)
		}
	}

	return &models.Rewards{
		XP:               u.XP,
		Level:            level,
		Rank:             rank,
		Threads:          threads,
		DonorStatus:      u.DonorStatus,
		DonorBonusThreads: DonorBonusThreads(u.DonorStatus),
		XPBonusPct:       DonorXPBonusPct(u.DonorStatus),
		DailyGames:       threads * DailyGamesPerThread,
		LevelXP:          levelXP,
		NextLevelXP:      nextLevelXP,
		ProgressPct:      progress,
	}, nil
}

// HasXPLedgerEntry reports whether a submission has already been credited, so
// awarding stays idempotent per submission (a re-approval cannot double-credit).
func (r *Repository) HasXPLedgerEntry(submissionID uuid.UUID) (bool, error) {
	var exists bool
	if err := r.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM xp_ledger WHERE submission_id = $1)`, submissionID).Scan(&exists); err != nil {
		return false, fmt.Errorf("failed to check xp ledger: %w", err)
	}
	return exists, nil
}

// SubmissionPointsByIDs returns the total XP awarded per submission id (the sum
// of the ledger deltas), keyed by submission id. Used to show contributors how
// many points each approved contribution earned.
func (r *Repository) SubmissionPointsByIDs(ids []uuid.UUID) (map[uuid.UUID]int, error) {
	out := map[uuid.UUID]int{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(
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
func (r *Repository) UserAwardedXPBySource(userID uuid.UUID) (metadataXP, sapXP int, err error) {
	if err = r.db.QueryRow(
		`SELECT COALESCE(SUM(x.delta), 0)
		   FROM xp_ledger x
		   JOIN metadata_submissions ms ON ms.id = x.submission_id
		  WHERE x.user_id = $1 AND x.delta > 0 AND ms.status = 'approved'`,
		userID,
	).Scan(&metadataXP); err != nil {
		return 0, 0, fmt.Errorf("failed to sum metadata xp: %w", err)
	}
	if err = r.db.QueryRow(
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
func (r *Repository) AwardXP(userID uuid.UUID, delta int, reason string, submissionID *uuid.UUID) error {
	if delta == 0 {
		return nil
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET xp = xp + $1, updated_at = NOW() WHERE id = $2`, delta, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO xp_ledger (user_id, delta, reason, submission_id) VALUES ($1, $2, $3, $4)`,
		userID, delta, reason, submissionID,
	); err != nil {
		return err
	}
	return tx.Commit()
}
