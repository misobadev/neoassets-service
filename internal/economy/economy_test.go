package economy

import (
	"testing"

	"neoassets/internal/models"
	"neoassets/internal/progression"
)

func TestThreadsForUser(t *testing.T) {
	d := Default()
	// Level 1 base = 4 threads.
	if got := d.ThreadsForUser(models.RoleUser, models.DonorNone, 0); got != 4 {
		t.Errorf("free level 1 = %d, want 4", got)
	}
	// Additive donor bonus: 4 + 2 / 4 + 4.
	if got := d.ThreadsForUser(models.RoleUser, models.DonorSupporter, 0); got != 6 {
		t.Errorf("supporter level 1 = %d, want 6", got)
	}
	if got := d.ThreadsForUser(models.RoleUser, models.DonorMonthlySupporter, 0); got != 8 {
		t.Errorf("monthly supporter level 1 = %d, want 8", got)
	}
	// Level 65 base = 10 threads; monthly +4 -> 14.
	if got := d.ThreadsForUser(models.RoleUser, models.DonorMonthlySupporter, progression.XPForLevel(65)); got != 14 {
		t.Errorf("monthly supporter level 65 = %d, want 14", got)
	}
	// Hard cap at 16 even with the donor bonus.
	if got := d.ThreadsForUser(models.RoleUser, models.DonorMonthlySupporter, progression.XPForLevel(100)); got != 16 {
		t.Errorf("monthly supporter level 100 = %d, want 16 (capped)", got)
	}
	// Admins are always maxed.
	if got := d.ThreadsForUser(models.RoleAdmin, models.DonorNone, 0); got != d.AdminThreads {
		t.Errorf("admin = %d, want %d", got, d.AdminThreads)
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
	d := Default()
	if d.DonorBonusThreads(models.DonorSupporter) != 2 || d.DonorBonusThreads(models.DonorMonthlySupporter) != 4 {
		t.Error("unexpected donor bonus threads")
	}
	if d.DonorXPBonusPct(models.DonorSupporter) != 25 || d.DonorXPBonusPct(models.DonorMonthlySupporter) != 50 {
		t.Error("unexpected donor XP bonus")
	}
	if d.DonorBonusThreads(models.DonorNone) != 0 || d.DonorXPBonusPct(models.DonorNone) != 0 {
		t.Error("none donor should grant nothing")
	}
}

func TestOverrideKeepsDefaultsForUnsetValues(t *testing.T) {
	c := Default().Override(Config{GuestThreads: 3, Points: Points{NewGame: 250}, DailyGamesPerThread: -1})
	if c.GuestThreads != 3 || c.Points.NewGame != 250 {
		t.Fatalf("overrides not applied: %+v", c)
	}
	if c.DailyGamesPerThread != 1000 || c.AdminThreads != 16 || c.Points.TextMetadata != 10 || c.DonorGraceDays != 40 {
		t.Fatalf("defaults not kept: %+v", c)
	}
}
