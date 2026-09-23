package services

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/pkg/auth"
)

func TestCleanGameName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Super Mario Bros. (USA).sfc", "Super Mario Bros."},
		{"Sonic The Hedgehog (Europe) [!].md", "Sonic The Hedgehog"},
		{"Chrono Trigger.sfc", "Chrono Trigger"},
		{"Legend of Zelda, The (USA) (Rev 1).nes", "Legend of Zelda, The"},
		{"Dr. Mario", "Dr. Mario"},
		{"Mario Party 2", "Mario Party 2"},
		{"  spaced   name  ", "spaced name"},
		{"", ""},
		{"Game [hack]", "Game"},
	}
	for _, tc := range cases {
		if got := cleanGameName(tc.in); got != tc.want {
			t.Errorf("cleanGameName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildQuotaClampsRemaining(t *testing.T) {
	q := buildQuota(100, 130)
	if q.Remaining != 0 {
		t.Errorf("remaining = %d, want 0", q.Remaining)
	}
	if q.Used != 130 {
		t.Errorf("used = %d, want 130", q.Used)
	}
	if q.DailyLimit != 100 {
		t.Errorf("daily_limit = %d, want 100", q.DailyLimit)
	}
	if !q.ResetsAt.After(time.Now().UTC()) {
		t.Error("resets_at should be in the future")
	}
}

func TestUsageKey(t *testing.T) {
	userID := uuid.New()
	user := &auth.ScrapeSubject{Kind: auth.ScrapeKindUser, UserID: userID}
	kind, id := usageKey(user)
	if kind != "user" || id != userID.String() {
		t.Errorf("usageKey(user) = (%q, %q)", kind, id)
	}

	ownerID := uuid.New()
	guest := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientID: "nsapp_abc", ClientOwnerID: ownerID}
	kind, id = usageKey(guest)
	if kind != "guest" || id != ownerID.String() {
		t.Errorf("usageKey(guest) = (%q, %q), want guest keyed by owner %q", kind, id, ownerID.String())
	}
}

func TestRateLimiterBurstThenBlock(t *testing.T) {
	s := &ScrapeService{rpmPerThread: 100, limiters: map[string]*limiterEntry{}}

	if !s.limiterFor("app-1", 2).Allow() || !s.limiterFor("app-1", 2).Allow() {
		t.Fatal("expected the initial burst to be allowed")
	}
	if s.limiterFor("app-1", 2).Allow() {
		t.Error("expected the limiter to block after the burst is exhausted")
	}

	// A different client has its own bucket.
	if !s.limiterFor("app-2", 2).Allow() {
		t.Error("expected an independent bucket for a different client")
	}
}

func TestAllowSubjectUsesThreadsAndSubjectKey(t *testing.T) {
	s := &ScrapeService{rpmPerThread: 100, limiters: map[string]*limiterEntry{}}

	// A guest is keyed by the developer app and gets the guest thread count.
	guest := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientID: "nsapp_abc", Threads: 2}
	for i := 0; i < 200; i++ {
		if !s.allowSubject(guest) {
			t.Fatalf("guest request %d should be allowed within the burst", i)
		}
	}
	if s.allowSubject(guest) {
		t.Error("expected the guest limiter to block after the burst is exhausted")
	}

	// A user gets threads * rpmPerThread and its own bucket.
	userID := uuid.New()
	user := &auth.ScrapeSubject{Kind: auth.ScrapeKindUser, ClientID: "nsapp_abc", UserID: userID, Threads: 16}
	if !s.allowSubject(user) {
		t.Error("expected the user bucket to be independent from the guest bucket")
	}
}

func TestToScrapeGameBuildsPublicURLs(t *testing.T) {
	year := 1994
	s := &ScrapeService{meta: &Service{publicBase: "https://cdn.example.com"}}
	detail := &models.GameDetail{
		Game: models.Game{
			ID:          uuid.New(),
			SystemID:    "snes",
			SystemName:  "Super Nintendo",
			Name:        "Chrono Trigger",
			ReleaseYear: &year,
		},
		Roms: []models.Rom{{Name: "chrono.sfc", CRC: "abc", SHA1: "def"}},
		Media: []models.Media{
			{Kind: "cover", ObjectKey: "media/games/snes/1.webp", Mime: "image/webp", Size: 10},
			{Kind: "video", ObjectKey: "media/games/snes/1.webm", Mime: "video/webm", Size: 20},
		},
	}

	got := s.toScrapeGame(detail, models.ScrapeGameQuery{})
	if len(got.Media) != 2 {
		t.Fatalf("media len = %d, want 2", len(got.Media))
	}
	if got.Media[0].URL != "https://cdn.example.com/media/games/snes/1.webp" {
		t.Errorf("media url = %q", got.Media[0].URL)
	}
	// ROMs are omitted by default; only the count is returned.
	if got.RomCount != 1 || len(got.Roms) != 0 {
		t.Errorf("roms should be omitted by default, got count=%d roms=%+v", got.RomCount, got.Roms)
	}

	withRoms := s.toScrapeGame(detail, models.ScrapeGameQuery{IncludeRoms: true})
	if len(withRoms.Roms) != 1 || withRoms.Roms[0].CRC != "abc" {
		t.Errorf("roms = %+v", withRoms.Roms)
	}

	filtered := s.toScrapeGame(detail, models.ScrapeGameQuery{Media: []string{"cover"}})
	if len(filtered.Media) != 1 || filtered.Media[0].Kind != "cover" {
		t.Errorf("media filter failed: %+v", filtered.Media)
	}
}

func TestBestMatch(t *testing.T) {
	games := []models.Game{
		{Name: "A Super Mario World: And The 8 Lost Worlds", Type: "hack"},
		{Name: "Super Mario World", Type: "base", Rating: 9},
		{Name: "Super Mario World 2: Yoshi's Island", Type: "base", Rating: 9},
	}
	got := bestMatch(games, "Super Mario World")
	if got == nil || got.Name != "Super Mario World" {
		t.Fatalf("bestMatch = %+v, want exact match", got)
	}
	if bestMatch(nil, "x") != nil {
		t.Error("bestMatch(nil) should be nil")
	}
}

func TestBestMatchPrefersBase(t *testing.T) {
	games := []models.Game{
		{Name: "Mario World Deluxe", Type: "hack", Rating: 5},
		{Name: "Mario World", Type: "base", Rating: 5},
	}
	got := bestMatch(games, "mario world")
	if got == nil || got.Name != "Mario World" {
		t.Fatalf("bestMatch = %+v, want the base game", got)
	}
}

func TestBestMatchHashPrefersBase(t *testing.T) {
	games := []models.Game{
		{Name: "Grand Poo World", Type: "hack"},
		{Name: "Super Mario World", Type: "base"},
	}
	got := bestMatch(games, "")
	if got == nil || got.Name != "Super Mario World" {
		t.Fatalf("bestMatch = %+v, want the base game", got)
	}
}
