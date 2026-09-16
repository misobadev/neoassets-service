package services

import (
	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/internal/progression"
	"neoassets/internal/repository"
)

// Points awarded for approved contributions. They are configurable at startup
// via ConfigurePoints so the economy can be tuned without code changes.
var (
	PointsTextMetadata  = 10
	PointsImageMetadata = 50
	PointsVideoMetadata = 100
	PointsSAPImage      = 20
	// PointsNewGame is an extra bonus for approving a brand-new game on top of
	// the field/media XP it already earns.
	PointsNewGame = 100
)

// ConfigurePoints overrides the points awarded per approved contribution type.
// Any non-positive argument keeps the current value.
func ConfigurePoints(textMetadata, imageMetadata, videoMetadata, sapImage, newGame int) {
	if textMetadata > 0 {
		PointsTextMetadata = textMetadata
	}
	if imageMetadata > 0 {
		PointsImageMetadata = imageMetadata
	}
	if videoMetadata > 0 {
		PointsVideoMetadata = videoMetadata
	}
	if sapImage > 0 {
		PointsSAPImage = sapImage
	}
	if newGame > 0 {
		PointsNewGame = newGame
	}
}

// textMetadataKeys are the payload keys that count as one text metadata each.
var textMetadataKeys = []string{"name", "description", "region", "genre", "developer", "publisher", "release_year", "rating", "type"}

// Rewards returns a user's XP progression and the scraping limits it unlocks.
func (s *Service) Rewards(userID uuid.UUID) (*models.Rewards, error) {
	return s.repo.GetRewards(userID)
}

// awardXP credits a user's XP for an approved contribution, applying the donor
// XP bonus (supporter +25%, monthly supporter +50%). Donations never grant rank.
// Awards are idempotent per submission so a re-approval cannot double-credit.
func (s *Service) awardXP(userID uuid.UUID, baseXP int, reason string, submissionID *uuid.UUID) error {
	if submissionID != nil {
		exists, err := s.repo.HasXPLedgerEntry(*submissionID)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
	}
	user, err := s.repo.GetUserByID(userID)
	if err != nil {
		return err
	}
	pct := repository.DonorXPBonusPct(user.DonorStatus)
	delta := repository.ApplyXPBonus(baseXP, pct)
	return s.repo.AwardXP(userID, delta, reason, submissionID)
}

// PublicConfig returns the runtime rewards/scrape configuration so the web UI
// can render the current economy without hardcoded values.
func (s *Service) PublicConfig() models.PublicConfig {
	ranks := make([]models.RankConfig, 0, len(progression.Ranks))
	for _, r := range progression.Ranks {
		ranks = append(ranks, models.RankConfig{Rank: r.Key, MinLevel: r.MinLevel, MaxLevel: r.MaxLevel, Threads: r.Threads})
	}
	return models.PublicConfig{
		GuestThreads:        repository.GuestThreads,
		AdminThreads:        repository.AdminThreads,
		DailyGamesPerThread: repository.DailyGamesPerThread,
		Points: models.PointsConfig{
			TextMetadata:  PointsTextMetadata,
			ImageMetadata: PointsImageMetadata,
			VideoMetadata: PointsVideoMetadata,
			SAPImage:      PointsSAPImage,
			NewGame:       PointsNewGame,
		},
		Ranks: ranks,
		DonorTiers: []models.DonorTier{
			{Status: models.DonorSupporter, BonusThreads: repository.SupporterBonusThreads, XPBonusPct: repository.SupporterXPBonusPct},
			{Status: models.DonorMonthlySupporter, BonusThreads: repository.MonthlySupporterBonusThreads, XPBonusPct: repository.MonthlySupporterXPBonusPct},
		},
	}
}
