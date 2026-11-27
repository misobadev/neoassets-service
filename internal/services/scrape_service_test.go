package services

import (
	"context"
	"strings"
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

func TestUnlimitedQuota(t *testing.T) {
	q := unlimitedQuota(130)
	if !q.Unlimited || q.DailyLimit != nil {
		t.Errorf("quota = %+v, want unlimited with a null daily_limit", q)
	}
	if q.Used != 130 {
		t.Errorf("used = %d, want 130", q.Used)
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

	// Guests are keyed by their (hashed) address, not by the app's owner: two
	// people on the same app get separate quotas, one person on two apps one.
	ownerID := uuid.New()
	a := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientID: "nsapp_abc", ClientOwnerID: ownerID, ClientKey: "203.0.113.7"}
	b := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientID: "nsapp_abc", ClientOwnerID: ownerID, ClientKey: "198.51.100.9"}
	aOtherApp := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientID: "nsapp_xyz", ClientOwnerID: uuid.New(), ClientKey: "203.0.113.7"}
	kindA, idA := usageKey(a)
	_, idB := usageKey(b)
	_, idA2 := usageKey(aOtherApp)
	if kindA != "guest" || idA == idB || idA != idA2 {
		t.Errorf("guest keys: a=%q b=%q a-other-app=%q", idA, idB, idA2)
	}
	if strings.Contains(idA, "203.0.113.7") {
		t.Errorf("guest key %q must not store the raw address", idA)
	}
}

func TestRateLimiterBurstThenBlock(t *testing.T) {
	s := &ScrapeService{rpmPerThread: 100, limiters: map[string]*limiterEntry{}}

	if !s.limiterFor("app-1", 2, 2).Allow() || !s.limiterFor("app-1", 2, 2).Allow() {
		t.Fatal("expected the initial burst to be allowed")
	}
	if s.limiterFor("app-1", 2, 2).Allow() {
		t.Error("expected the limiter to block after the burst is exhausted")
	}

	// A different client has its own bucket.
	if !s.limiterFor("app-2", 2, 2).Allow() {
		t.Error("expected an independent bucket for a different client")
	}
}

func TestAllowSubjectUsesThreadsAndSubjectKey(t *testing.T) {
	s := &ScrapeService{rpmPerThread: 100, limiters: map[string]*limiterEntry{}}

	// A guest gets the guest thread count (2 threads -> 200 RPM) with a burst
	// of about five seconds (200/12 = 16), not the whole minute at once. It is
	// keyed by the guest's address: another guest of the same app has their own.
	owner := uuid.New()
	guest := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientID: "nsapp_abc", ClientOwnerID: owner, ClientKey: "203.0.113.7", Threads: 2}
	for i := 0; i < 16; i++ {
		if ok, _ := s.allowSubject(guest); !ok {
			t.Fatalf("guest request %d should be allowed within the burst", i)
		}
	}
	if ok, _ := s.allowSubject(guest); ok {
		t.Error("expected the guest limiter to block after the burst is exhausted")
	}
	sameGuestOtherApp := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientID: "nsapp_other", ClientOwnerID: uuid.New(), ClientKey: "203.0.113.7", Threads: 2}
	if ok, _ := s.allowSubject(sameGuestOtherApp); ok {
		t.Error("the same guest on another app must share their bucket")
	}
	otherGuest := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientID: "nsapp_abc", ClientOwnerID: owner, ClientKey: "198.51.100.9", Threads: 2}
	if ok, _ := s.allowSubject(otherGuest); !ok {
		t.Error("another guest of the same app must have their own bucket")
	}

	// A user gets threads * rpmPerThread and its own bucket.
	userID := uuid.New()
	user := &auth.ScrapeSubject{Kind: auth.ScrapeKindUser, ClientID: "nsapp_abc", UserID: userID, Threads: 16}
	if ok, _ := s.allowSubject(user); !ok {
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

	got := s.toScrapeGame(t.Context(), detail, models.ScrapeGameQuery{})
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

	withRoms := s.toScrapeGame(t.Context(), detail, models.ScrapeGameQuery{IncludeRoms: true})
	if len(withRoms.Roms) != 1 || withRoms.Roms[0].CRC != "abc" {
		t.Errorf("roms = %+v", withRoms.Roms)
	}

	filtered := s.toScrapeGame(t.Context(), detail, models.ScrapeGameQuery{Media: []string{"cover"}})
	if len(filtered.Media) != 1 || filtered.Media[0].Kind != "cover" {
		t.Errorf("media filter failed: %+v", filtered.Media)
	}
}

func TestToScrapeGameRegionalNamesAndMedia(t *testing.T) {
	s := &ScrapeService{meta: &Service{publicBase: "https://cdn.example.com"}}
	detail := &models.GameDetail{
		Game: models.Game{ID: uuid.New(), SystemID: "snes", Name: "Ninja Gaiden Trilogy"},
		Media: []models.Media{
			{Kind: "cover", ObjectKey: "media/games/snes/jp.webp", Mime: "image/webp", Region: "Japan"},
		},
		Regions: []models.GameRegion{
			{Region: "USA", Name: "Ninja Gaiden Trilogy"},
			{Region: "Europe"}, // release date only: no name to report
			{Region: "Japan", Name: "Ninja Ryuuken Den Tomoe"},
		},
	}

	got := s.toScrapeGame(t.Context(), detail, models.ScrapeGameQuery{})
	want := []models.ScrapeName{
		{Region: "USA", Name: "Ninja Gaiden Trilogy"},
		{Region: "Japan", Name: "Ninja Ryuuken Den Tomoe"},
	}
	if len(got.Names) != len(want) {
		t.Fatalf("names = %+v, want %+v", got.Names, want)
	}
	for i := range want {
		if got.Names[i] != want[i] {
			t.Errorf("names[%d] = %+v, want %+v", i, got.Names[i], want[i])
		}
	}
	if got.Media[0].Region != "Japan" {
		t.Errorf("media region = %q, want Japan", got.Media[0].Region)
	}

	// A game without regional names leaves the field out of the payload.
	bare := s.toScrapeGame(t.Context(), &models.GameDetail{Game: detail.Game}, models.ScrapeGameQuery{})
	if bare.Names != nil {
		t.Errorf("names = %+v, want nil", bare.Names)
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

// An arcade ROM file is named after its set: "asteroid.zip" must resolve to
// Asteroids even though Asteroids Deluxe rates higher and its title also
// starts with the query. The catalog's Asteroids carries a wrong set name
// ("ar_sprg"), so the ROM file decides.
func TestBestMatchPrefersExactRomFile(t *testing.T) {
	games := []models.Game{
		{Name: "Asteroids Deluxe", ShortName: "astdelux", RomNames: []string{"astdelux.zip"}, Type: "base", Rating: 9},
		{Name: "Asteroids", ShortName: "ar_sprg", RomNames: []string{"asteroid.zip"}, Type: "base", Rating: 7},
	}
	for file, want := range map[string]string{
		"asteroid.zip": "Asteroids",
		"astdelux.zip": "Asteroids Deluxe",
		"ASTEROID.ZIP": "Asteroids",
		"asteroid":     "Asteroids",
	} {
		if got := bestMatch(games, strings.ToLower(cleanGameName(file))); got == nil || got.Name != want {
			t.Errorf("bestMatch(%q) = %+v, want %s", file, got, want)
		}
	}
}

// With no ROM file of that name, a set name equal to the query still beats a
// title it only prefixes.
func TestBestMatchFallsBackToRomSet(t *testing.T) {
	games := []models.Game{
		{Name: "Asteroids Deluxe", ShortName: "astdelux", Type: "base", Rating: 9},
		{Name: "Asteroids", ShortName: "asteroid", Type: "base", Rating: 7},
	}
	if got := bestMatch(games, "asteroid"); got == nil || got.Name != "Asteroids" {
		t.Fatalf("bestMatch = %+v, want Asteroids", got)
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

// The scrape API takes the same user tokens as the website, so a token revoked
// by a password change must stop working there too.
func TestUserTokenCurrent(t *testing.T) {
	user := &models.User{TokenVersion: 2}
	if userTokenCurrent(&auth.UserClaims{TokenVersion: 1}, user) {
		t.Error("a token from before the password change was accepted")
	}
	if !userTokenCurrent(&auth.UserClaims{TokenVersion: 2}, user) {
		t.Error("a current token was rejected")
	}
}

func TestRateBurstIsAboutFiveSeconds(t *testing.T) {
	for _, c := range []struct{ rpm, threads, want int }{
		{200, 2, 16},    // guest
		{2000, 20, 166}, // legend
		{6, 4, 4},       // never below the threads
		{0, 0, 1},
	} {
		if got := rateBurst(c.rpm, c.threads); got != c.want {
			t.Errorf("rateBurst(%d, %d) = %d, want %d", c.rpm, c.threads, got, c.want)
		}
	}
}

// A refused request learns exactly how long to wait, and an allowed one how
// many requests it has left right now.
func TestAllowSubjectReportsWaitAndRemaining(t *testing.T) {
	s := &ScrapeService{rpmPerThread: 60, limiters: map[string]*limiterEntry{}}
	// 1 thread * 60 RPM: one token per second, burst max(60/12, 1) = 5.
	user := &auth.ScrapeSubject{Kind: auth.ScrapeKindUser, UserID: uuid.New(), Threads: 1}
	for i := 4; i >= 0; i-- {
		ok, _ := s.allowSubject(user)
		if !ok || user.RateLimit != 60 || user.RateRemaining != i {
			t.Fatalf("request %d: ok=%v limit=%d remaining=%d, want remaining %d", 5-i, ok, user.RateLimit, user.RateRemaining, i)
		}
	}
	ok, wait := s.allowSubject(user)
	if ok || wait <= 0 || wait > time.Second+50*time.Millisecond {
		t.Fatalf("empty bucket: ok=%v wait=%v, want refused with a wait of about 1 s", ok, wait)
	}
	// A refusal does not consume a token: waiting that long is enough.
	time.Sleep(wait)
	if ok, _ := s.allowSubject(user); !ok {
		t.Fatal("still refused after waiting Retry-After")
	}
}

// A guest thread runs at the lower guest rate, and a guest over that rate is
// held until its next token instead of being refused.
func TestGuestRateIsSlowerAndPaced(t *testing.T) {
	s := &ScrapeService{rpmPerThread: 100, guestRPMPerThread: 600, limiters: map[string]*limiterEntry{}}
	guest := &auth.ScrapeSubject{Kind: auth.ScrapeKindGuest, ClientKey: "203.0.113.7", Threads: 1}
	if got := s.subjectRPM(guest); got != 600 {
		t.Fatalf("guest rpm = %d, want threads * the guest rate", got)
	}
	user := &auth.ScrapeSubject{Kind: auth.ScrapeKindUser, UserID: uuid.New(), Threads: 2}
	if got := s.subjectRPM(user); got != 200 {
		t.Fatalf("user rpm = %d, want threads * rpmPerThread", got)
	}

	// Drain the burst, then the next request waits (~100 ms at 600 rpm)
	// instead of failing.
	ctx := context.Background()
	for {
		if ok, _, _ := take1(s.limiterFor("rpm:"+admissionKey(guest), 600, 1)); !ok {
			break
		}
	}
	if ok, _ := s.allowSubjectPaced(ctx, guest); !ok || guest.PaceDelay <= 0 {
		t.Fatalf("a guest just over its rate must be paced, not refused (delay %v)", guest.PaceDelay)
	}
	// Admission holds the paced request for its delay, counted as an open
	// thread meanwhile.
	start := time.Now()
	done := make(chan func())
	go func() {
		release, err := s.AcquireSubject(ctx, guest)
		if err != nil {
			t.Error(err)
		}
		done <- release
	}()
	time.Sleep(20 * time.Millisecond)
	if n := s.live.openGuests.Load(); n != 1 {
		t.Errorf("open guests while paced = %d, want 1", n)
	}
	release := <-done
	if time.Since(start) < 50*time.Millisecond {
		t.Error("expected the paced request to wait for its token")
	}
	if release != nil {
		release()
	}

	// A guest far over its rate (wait > guestPaceWait) is refused.
	slow := &ScrapeService{rpmPerThread: 100, guestRPMPerThread: 1, limiters: map[string]*limiterEntry{}}
	_, _ = slow.allowSubjectPaced(ctx, guest)
	if ok, wait := slow.allowSubjectPaced(ctx, guest); ok || wait <= guestPaceWait {
		t.Fatalf("far over the rate: ok=%v wait=%v, want a refusal with Retry-After", ok, wait)
	}
}

// "arkanoid" must not resolve to "Arkanoid: Revenge of Doh" (arknoid2) just
// because its title starts with the set name; only a ROM file, set name or
// exact title counts as a match for a ROM set query.
func TestStrongMatchRomSetQuery(t *testing.T) {
	sequel := &models.Game{Name: "Arkanoid: Revenge Of Doh", ShortName: "arknoid2"}
	if !isRomSetQuery("arkanoid") || isRomSetQuery("Metal Slug") {
		t.Fatal("isRomSetQuery misclassified a query")
	}
	if strongMatch(sequel, "arkanoid") {
		t.Error("a title prefix must not be a strong match")
	}
	for _, g := range []*models.Game{
		{Name: "Arkanoid", ShortName: "arkanoid"},
		{Name: "Arkanoid"},
		{Name: "Arkanoid (bootleg)", RomNames: []string{"arkanoid.zip"}},
	} {
		if !strongMatch(g, "arkanoid") {
			t.Errorf("strongMatch(%+v) = false, want true", g)
		}
	}
}
