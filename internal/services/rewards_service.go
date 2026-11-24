package services

import (
	"context"
	"github.com/google/uuid"

	"neoassets/internal/economy"
	"neoassets/internal/models"
	"neoassets/internal/progression"
)

// textMetadataKeys are the payload keys that count as one text metadata each.
var textMetadataKeys = []string{"name", "description", "region", "genre", "developer", "publisher", "release_year", "rating", "type"}

// Rewards returns a user's XP progression and the scraping limits it unlocks.
func (s *Service) Rewards(ctx context.Context, userID uuid.UUID) (*models.Rewards, error) {
	return s.repo.GetRewards(ctx, userID)
}

// awardXP credits a user's XP for an approved contribution, applying the donor
// XP bonus (supporter +25%, monthly supporter +50%). Donations never grant rank.
// Awards are idempotent per submission so a re-approval cannot double-credit.
func (s *Service) awardXP(ctx context.Context, userID uuid.UUID, baseXP int, reason string, submissionID *uuid.UUID) error {
	if submissionID != nil {
		exists, err := s.repo.HasXPLedgerEntry(ctx, *submissionID)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
	}
	user, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	econ := s.repo.Economy()
	pct := econ.DonorXPBonusPct(user.DonorStatus)
	delta := economy.ApplyXPBonus(baseXP, pct)
	return s.repo.AwardXP(ctx, userID, delta, reason, submissionID)
}

// PublicConfig returns the runtime rewards/scrape configuration so the web UI
// can render the current economy without hardcoded values.
func (s *Service) PublicConfig() models.PublicConfig {
	ranks := make([]models.RankConfig, 0, len(progression.Ranks))
	for _, r := range progression.Ranks {
		ranks = append(ranks, models.RankConfig{Rank: r.Key, MinLevel: r.MinLevel, MaxLevel: r.MaxLevel, Threads: r.Threads})
	}
	econ := s.repo.Economy()
	return models.PublicConfig{
		GuestThreads:        econ.GuestThreads,
		AdminThreads:        econ.AdminThreads,
		DailyGamesPerThread: econ.DailyGamesPerThread,
		RPMPerThread:        econ.RPMPerThread,
		Points: models.PointsConfig{
			TextMetadata:  econ.Points.TextMetadata,
			ImageMetadata: econ.Points.ImageMetadata,
			VideoMetadata: econ.Points.VideoMetadata,
			SAPImage:      econ.Points.SAPImage,
			NewGame:       econ.Points.NewGame,
		},
		Ranks: ranks,
		DonorTiers: []models.DonorTier{
			{Status: models.DonorSupporter, BonusThreads: econ.SupporterBonusThreads, XPBonusPct: econ.SupporterXPBonusPct},
			{Status: models.DonorMonthlySupporter, BonusThreads: econ.MonthlySupporterBonusThreads, XPBonusPct: econ.MonthlySupporterXPBonusPct},
		},
	}
}
