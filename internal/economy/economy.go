// Package economy holds the rewards and scraping economy: thread counts,
// donor bonuses, the daily quota per thread and the XP awarded per approved
// contribution. It is a plain value built once at startup from the
// configuration and passed to the repository (see repository.WithEconomy), so
// there is no mutable package state.
package economy

import (
	"math"

	"neoassets/internal/models"
	"neoassets/internal/progression"
)

// Points is the XP awarded per approved contribution type.
type Points struct {
	TextMetadata  int
	ImageMetadata int
	VideoMetadata int
	SAPImage      int
	// NewGame is an extra bonus for approving a brand-new game on top of the
	// field/media XP it already earns.
	NewGame int
}

// Config is the economy. Build it with Default().Override(...).
type Config struct {
	// AdminThreads is the thread count granted to admin accounts.
	AdminThreads int
	// GuestThreads is the thread count for anonymous scraping (developer
	// credentials without a user credential).
	GuestThreads int
	// SupporterBonusThreads is the additive thread bonus of a one-time donation.
	SupporterBonusThreads int
	// MonthlySupporterBonusThreads is the additive thread bonus of a monthly
	// donation.
	MonthlySupporterBonusThreads int
	// SupporterXPBonusPct is the XP bonus (percent) of a one-time donation.
	SupporterXPBonusPct int
	// MonthlySupporterXPBonusPct is the XP bonus (percent) of a monthly donation.
	MonthlySupporterXPBonusPct int
	// DailyGamesPerThread is the daily game scrape quota granted per thread.
	// The effective daily limit is threads * DailyGamesPerThread.
	DailyGamesPerThread int
	// RPMPerThread is the per-minute request budget granted per thread. The
	// effective rate limit is threads * RPMPerThread.
	RPMPerThread int
	// DonorGraceDays is the inactivity window after which an auto-managed
	// monthly supporter with no fresh subscription payment is downgraded.
	DonorGraceDays int
	// Points is the XP per approved contribution type.
	Points Points
}

// Default returns the built-in economy.
func Default() Config {
	return Config{
		AdminThreads:                 20,
		GuestThreads:                 1,
		SupporterBonusThreads:        2,
		MonthlySupporterBonusThreads: 4,
		SupporterXPBonusPct:          25,
		MonthlySupporterXPBonusPct:   50,
		DailyGamesPerThread:          1000,
		RPMPerThread:                 100,
		DonorGraceDays:               40,
		Points: Points{
			TextMetadata:  10,
			ImageMetadata: 50,
			VideoMetadata: 100,
			SAPImage:      20,
			NewGame:       100,
		},
	}
}

// Override returns c with every positive value of o applied; zero or negative
// values in o keep c's value (so an unset environment variable keeps the
// default).
func (c Config) Override(o Config) Config {
	pick := func(cur, v int) int {
		if v > 0 {
			return v
		}
		return cur
	}
	c.AdminThreads = pick(c.AdminThreads, o.AdminThreads)
	c.GuestThreads = pick(c.GuestThreads, o.GuestThreads)
	c.SupporterBonusThreads = pick(c.SupporterBonusThreads, o.SupporterBonusThreads)
	c.MonthlySupporterBonusThreads = pick(c.MonthlySupporterBonusThreads, o.MonthlySupporterBonusThreads)
	c.SupporterXPBonusPct = pick(c.SupporterXPBonusPct, o.SupporterXPBonusPct)
	c.MonthlySupporterXPBonusPct = pick(c.MonthlySupporterXPBonusPct, o.MonthlySupporterXPBonusPct)
	c.DailyGamesPerThread = pick(c.DailyGamesPerThread, o.DailyGamesPerThread)
	c.RPMPerThread = pick(c.RPMPerThread, o.RPMPerThread)
	c.DonorGraceDays = pick(c.DonorGraceDays, o.DonorGraceDays)
	c.Points.TextMetadata = pick(c.Points.TextMetadata, o.Points.TextMetadata)
	c.Points.ImageMetadata = pick(c.Points.ImageMetadata, o.Points.ImageMetadata)
	c.Points.VideoMetadata = pick(c.Points.VideoMetadata, o.Points.VideoMetadata)
	c.Points.SAPImage = pick(c.Points.SAPImage, o.Points.SAPImage)
	c.Points.NewGame = pick(c.Points.NewGame, o.Points.NewGame)
	return c
}

// DonorBonusThreads returns the additive thread bonus of a donor status.
func (c Config) DonorBonusThreads(status string) int {
	switch status {
	case models.DonorSupporter:
		return c.SupporterBonusThreads
	case models.DonorMonthlySupporter:
		return c.MonthlySupporterBonusThreads
	default:
		return 0
	}
}

// DonorXPBonusPct returns the XP bonus (percent) of a donor status.
func (c Config) DonorXPBonusPct(status string) int {
	switch status {
	case models.DonorSupporter:
		return c.SupporterXPBonusPct
	case models.DonorMonthlySupporter:
		return c.MonthlySupporterXPBonusPct
	default:
		return 0
	}
}

// ThreadsForUser returns the effective scraping threads: admins are maxed,
// everyone else gets their level-rank base threads plus the donor bonus, capped
// at the hard maximum (donations never grant XP or rank, only bonus threads).
func (c Config) ThreadsForUser(role, donorStatus string, xp int) int {
	if role == models.RoleAdmin {
		return c.AdminThreads
	}
	base := progression.ThreadsForLevel(progression.LevelForXP(xp))
	total := base + c.DonorBonusThreads(donorStatus)
	if max := progression.MaxThreads(); total > max {
		total = max
	}
	return total
}

// ApplyXPBonus returns base XP increased by pct percent (rounded).
func ApplyXPBonus(baseXP, pct int) int {
	return int(math.Round(float64(baseXP) * float64(100+pct) / 100))
}
