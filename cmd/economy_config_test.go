package main

import (
	"testing"

	"neoassets/internal/economy"
)

// Environment values reach the economy; unset ones keep the defaults.
func TestEconomyFromConfig(t *testing.T) {
	t.Setenv("SCRAPE_GUEST_THREADS", "3")
	t.Setenv("POINTS_NEW_GAME", "250")
	t.Setenv("DONOR_SUBSCRIPTION_GRACE_DAYS", "45")
	got := economyFromConfig(loadConfig())

	want := economy.Default()
	want.GuestThreads = 3
	want.Points.NewGame = 250
	want.DonorGraceDays = 45
	if got != want {
		t.Fatalf("economy = %+v\nwant %+v", got, want)
	}
}
