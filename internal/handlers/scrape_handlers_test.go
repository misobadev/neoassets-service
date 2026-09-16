package handlers

import (
	"net/http/httptest"
	"testing"
)

func TestParseScrapeGameQuerySelectors(t *testing.T) {
	cases := []struct {
		url     string
		wantErr bool
	}{
		// system_id is required.
		{"/api/v1/scrape/games", true},
		{"/api/v1/scrape/games?crc=abc123", true},
		{"/api/v1/scrape/games?name=mario", true},
		// system_id alone is not a selector.
		{"/api/v1/scrape/games?system_id=snes", true},
		// game_id is no longer accepted as a selector.
		{"/api/v1/scrape/games?system_id=snes&game_id=11111111-1111-1111-1111-111111111111", true},
		// Valid selectors scoped to a system.
		{"/api/v1/scrape/games?system_id=snes&crc=abc123", false},
		{"/api/v1/scrape/games?system_id=snes&md5=deadbeef", false},
		{"/api/v1/scrape/games?system_id=snes&sha1=deadbeef", false},
		{"/api/v1/scrape/games?system_id=snes&sha256=deadbeef", false},
		{"/api/v1/scrape/games?system_id=snes&name=chrono%20trigger", false},
		// Hash + name together (hash wins, name is the fallback).
		{"/api/v1/scrape/games?system_id=snes&crc=abc123&name=chrono", false},
		// Optional type filter on a name search.
		{"/api/v1/scrape/games?system_id=snes&name=mario&type=base", false},
		{"/api/v1/scrape/games?system_id=snes&name=mario&type=hack", false},
		{"/api/v1/scrape/games?system_id=snes&name=mario&type=homebrew", false},
		{"/api/v1/scrape/games?system_id=snes&name=mario&type=invalid", true},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", tc.url, nil)
		if _, err := parseScrapeGameQuery(req); (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr = %v", tc.url, err, tc.wantErr)
		}
	}
}

func TestParseScrapeGameQueryMedia(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/scrape/games?system_id=snes&name=mario&media=cover,%20video%20,logo", nil)
	q, err := parseScrapeGameQuery(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.SystemID != "snes" {
		t.Errorf("system_id = %q, want snes", q.SystemID)
	}
	want := []string{"cover", "video", "logo"}
	if len(q.Media) != len(want) {
		t.Fatalf("media = %v, want %v", q.Media, want)
	}
	for i := range want {
		if q.Media[i] != want[i] {
			t.Errorf("media[%d] = %q, want %q", i, q.Media[i], want[i])
		}
	}
}
