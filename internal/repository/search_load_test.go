package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"neoassets/internal/models"
	"neoassets/internal/testdb"
)

func TestSearchTokensAreCapped(t *testing.T) {
	q := strings.Repeat("a b c d e f g h i j k l m n o p ", 6)
	toks := searchTokens(q)
	if len(toks) != maxSearchTokens {
		t.Fatalf("got %d tokens, want %d", len(toks), maxSearchTokens)
	}
	if got := searchTokens("Final Fantasy X"); strings.Join(got, " ") != "final fantasy x" {
		t.Fatalf("short queries keep every token, got %v", got)
	}
}

// loadTestRepo returns a Repository on a migrated test database (see testdb).
func loadTestRepo(t *testing.T) (*Repository, *sql.DB) {
	t.Helper()
	db := testdb.Open(t, "repository_search")
	return NewRepository(db), db
}

// seedSystem creates a system with n games named "<prefix> NN".
func seedSystem(t *testing.T, db *sql.DB, prefix string, n int) string {
	t.Helper()
	id := "s" + uuid.NewString()[:8]
	if _, err := db.Exec(`INSERT INTO metadata_systems (id, name) VALUES ($1, $1)`, id); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := db.Exec(`INSERT INTO games (system_id, name) VALUES ($1, $2)`, id, fmt.Sprintf("%s %02d", prefix, i)); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func gameNames(games []models.Game) []string {
	out := make([]string, len(games))
	for i, g := range games {
		out[i] = g.SystemID + "/" + g.Name
	}
	return out
}

// One query over several systems returns the same candidates, in the same
// order, as one search per system: up to perSystem per system, systems in the
// order given (the best-match tie-break depends on it).
func TestCandidateSearchAcrossSystemsKeepsPerSystemLimit(t *testing.T) {
	r, db := loadTestRepo(t)
	ctx := context.Background()
	prefix := "Fighter " + uuid.NewString()[:6]
	big := seedSystem(t, db, prefix, 30)
	small := seedSystem(t, db, prefix, 2)
	none := seedSystem(t, db, "Other "+uuid.NewString()[:6], 3)

	if _, err := db.Exec(`UPDATE games SET type = 'hack' WHERE system_id = $1 AND name LIKE '% 0_'`, big); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		systems []string
		gtype   string
		want    int
	}{
		{"several systems", []string{small, none, big}, "", 27},
		{"a system given twice", []string{big, small, big}, "", 52},
		{"a type filter", []string{small, big}, "hack", 10},
	}
	for _, c := range cases {
		got, err := r.SearchGamesLight(ctx, prefix, c.systems, c.gtype, 25)
		if err != nil {
			t.Fatal(err)
		}
		// What the resolver got before: one search per system, in order.
		var want []models.Game
		for _, id := range c.systems {
			one, err := r.SearchGamesLight(ctx, prefix, []string{id}, c.gtype, 25)
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, one...)
		}
		if len(got) != c.want || strings.Join(gameNames(got), ",") != strings.Join(gameNames(want), ",") {
			t.Fatalf("%s: %d candidates %v\nper system: %d candidates %v", c.name, len(got), gameNames(got), len(want), gameNames(want))
		}
	}
}

// A search stops when its request is cancelled instead of running on. (With an
// already-cancelled context the first statement returns at once, so this shows
// the context reaches the search; both statements take it.)
func TestSearchStopsWithItsRequest(t *testing.T) {
	r, db := loadTestRepo(t)
	seedSystem(t, db, "Cancel "+uuid.NewString()[:6], 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.SearchGames(ctx, "cancel", nil, "", "", 10, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchGames with a cancelled context: %v", err)
	}
	if _, err := r.SearchGamesLight(ctx, "cancel", nil, "", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchGamesLight with a cancelled context: %v", err)
	}
}

func testDeveloperApp(t *testing.T, r *Repository) *models.DeveloperApp {
	t.Helper()
	u, err := r.CreateUser(&models.User{Username: "d" + uuid.NewString()[:8], Email: uuid.NewString()[:8] + "@example.com", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	app, err := r.CreateDeveloperApp(u.ID, "My Scraper", "", "", "cid-"+uuid.NewString(), "hash", "")
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func softwareRows(t *testing.T, db *sql.DB, appID uuid.UUID) map[string]int64 {
	t.Helper()
	rows, err := db.Query(`SELECT name, api_calls FROM app_software_stats WHERE app_id = $1`, appID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var calls int64
		if err := rows.Scan(&name, &calls); err != nil {
			t.Fatal(err)
		}
		out[name] = calls
	}
	return out
}

func TestSoftwareNamesPerAppAreCapped(t *testing.T) {
	r, db := loadTestRepo(t)
	app := testDeveloperApp(t, r)
	for i := 0; i < maxSoftwareNamesPerApp; i++ {
		if err := r.RecordSoftwareOutcome(app.ID, fmt.Sprintf("v%d", i), app.Name, "ok", 1); err != nil {
			t.Fatal(err)
		}
	}
	// A further new name is counted under the app's own name.
	if err := r.RecordSoftwareOutcome(app.ID, "one-too-many", app.Name, "ok", 1); err != nil {
		t.Fatal(err)
	}
	// A name already recorded keeps counting under its own row.
	if err := r.RecordSoftwareOutcome(app.ID, "v0", app.Name, "ok", 3); err != nil {
		t.Fatal(err)
	}
	rows := softwareRows(t, db, app.ID)
	if _, ok := rows["one-too-many"]; ok {
		t.Fatal("a name past the cap got its own row")
	}
	if rows[app.Name] != 1 || rows["v0"] != 4 {
		t.Fatalf("app name counted %d (want 1), v0 counted %d (want 4)", rows[app.Name], rows["v0"])
	}
	if len(rows) > maxSoftwareNamesPerApp+1 {
		t.Fatalf("%d rows for one app", len(rows))
	}
}

// Refused requests are written in batches: one write adds the whole count.
func TestScrapeOutcomesAddACount(t *testing.T) {
	r, db := loadTestRepo(t)
	app := testDeveloperApp(t, r)
	if err := r.RecordScrapeOutcome(app.ID, "rate_limited", 5); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordSoftwareOutcome(app.ID, "", app.Name, "rate_limited", 5); err != nil {
		t.Fatal(err)
	}
	var calls, limited int64
	if err := db.QueryRow(`SELECT api_calls, rate_limited FROM developer_apps WHERE id = $1`, app.ID).Scan(&calls, &limited); err != nil {
		t.Fatal(err)
	}
	if calls != 5 || limited != 5 {
		t.Fatalf("app counters: api_calls %d, rate_limited %d; want 5, 5", calls, limited)
	}
	var swLimited int64
	if err := db.QueryRow(`SELECT rate_limited FROM app_software_stats WHERE app_id = $1 AND name = $2`, app.ID, app.Name).Scan(&swLimited); err != nil {
		t.Fatal(err)
	}
	if swLimited != 5 {
		t.Fatalf("software rate_limited = %d, want 5", swLimited)
	}
}
