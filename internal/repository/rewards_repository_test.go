package repository

import (
	"testing"

	"neoassets/internal/models"
	"neoassets/internal/progression"
)

func TestThreadsForUser(t *testing.T) {
	// Level 1 base = 4 threads.
	if got := ThreadsForUser(models.RoleUser, models.DonorNone, 0); got != 4 {
		t.Errorf("free level 1 = %d, want 4", got)
	}
	// Additive donor bonus: 4 + 2 / 4 + 4.
	if got := ThreadsForUser(models.RoleUser, models.DonorSupporter, 0); got != 6 {
		t.Errorf("supporter level 1 = %d, want 6", got)
	}
	if got := ThreadsForUser(models.RoleUser, models.DonorMonthlySupporter, 0); got != 8 {
		t.Errorf("monthly supporter level 1 = %d, want 8", got)
	}
	// Level 65 base = 10 threads; monthly +4 -> 14.
	if got := ThreadsForUser(models.RoleUser, models.DonorMonthlySupporter, progression.XPForLevel(65)); got != 14 {
		t.Errorf("monthly supporter level 65 = %d, want 14", got)
	}
	// Hard cap at 16 even with the donor bonus.
	if got := ThreadsForUser(models.RoleUser, models.DonorMonthlySupporter, progression.XPForLevel(100)); got != 16 {
		t.Errorf("monthly supporter level 100 = %d, want 16 (capped)", got)
	}
	// Admins are always maxed.
	if got := ThreadsForUser(models.RoleAdmin, models.DonorNone, 0); got != AdminThreads {
		t.Errorf("admin = %d, want %d", got, AdminThreads)
	}
}

func TestApplyXPBonus(t *testing.T) {
	cases := []struct{ base, pct, want int }{
		{100, 0, 100},
		{100, 25, 125},
		{100, 50, 150},
		{40, 25, 50},
		{10, 25, 13}, // 12.5 rounds to 13
		{5, 50, 8},   // 7.5 rounds to 8
	}
	for _, c := range cases {
		if got := ApplyXPBonus(c.base, c.pct); got != c.want {
			t.Errorf("ApplyXPBonus(%d, %d) = %d, want %d", c.base, c.pct, got, c.want)
		}
	}
}

func TestDonorBonusAndXP(t *testing.T) {
	if DonorBonusThreads(models.DonorSupporter) != 2 || DonorBonusThreads(models.DonorMonthlySupporter) != 4 {
		t.Error("unexpected donor bonus threads")
	}
	if DonorXPBonusPct(models.DonorSupporter) != 25 || DonorXPBonusPct(models.DonorMonthlySupporter) != 50 {
		t.Error("unexpected donor XP bonus")
	}
	if DonorBonusThreads(models.DonorNone) != 0 || DonorXPBonusPct(models.DonorNone) != 0 {
		t.Error("none donor should grant nothing")
	}
}
