package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// search_norm is kept up to date by triggers on games and game_regions, so a
// rename, a new short name or a regional name is searchable right away (also
// for writers outside this service, like the catalog importer).
func TestSearchNormFollowsNamesAndRegions(t *testing.T) {
	r, db := loadTestRepo(t)
	ctx := context.Background()
	tag := strings.ToLower("q" + uuid.NewString()[:6])
	sys := seedSystem(t, db, "Plain "+tag, 1)

	find := func(q string) int {
		t.Helper()
		got, err := r.SearchGamesLight(ctx, q, []string{sys}, "", 25)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	if find("Plain "+tag) != 1 {
		t.Fatal("a new game is not searchable by name")
	}

	var id uuid.UUID
	if err := db.QueryRow(`UPDATE games SET name = $2, short_name = $3 WHERE system_id = $1 RETURNING id`,
		sys, "Pokémon - FireRed "+tag, "pkfr"+tag).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if find("pokemon firered "+tag) != 1 || find("pkfr"+tag) != 1 {
		t.Fatal("a renamed game is not searchable by its new name and short name")
	}
	if find("plain "+tag) != 0 {
		t.Fatal("the old name still matches after a rename")
	}

	if _, err := db.Exec(`INSERT INTO game_regions (game_id, region, name) VALUES ($1, 'jp', $2)`, id, "Poketto Monsutā "+tag); err != nil {
		t.Fatal(err)
	}
	if find("poketto monsuta "+tag) != 1 {
		t.Fatal("a regional name is not searchable")
	}
	// Tokens must all match, but each may come from a different name.
	if find("poketto firered "+tag) != 1 {
		t.Fatal("tokens from the regional and display names do not combine")
	}
	if _, err := db.Exec(`DELETE FROM game_regions WHERE game_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if find("poketto "+tag) != 0 {
		t.Fatal("a deleted regional name still matches")
	}
}

// A hash lookup over several systems is one query that returns what one lookup
// per system did, in the order of the systems given.
func TestHashLookupAcrossSystemsKeepsSystemOrder(t *testing.T) {
	r, db := loadTestRepo(t)
	ctx := context.Background()
	crc := strings.ToLower(uuid.NewString()[:8])
	a := seedSystem(t, db, "Zeta "+crc, 1)
	b := seedSystem(t, db, "Alpha "+crc, 1)
	other := seedSystem(t, db, "Other "+crc, 1)
	for _, sys := range []string{a, b, other} {
		if _, err := db.Exec(`INSERT INTO roms (game_id, name, crc) SELECT id, 'rom.bin', $2 FROM games WHERE system_id = $1`, sys, crc); err != nil {
			t.Fatal(err)
		}
	}

	got, err := r.LookupGameByHashInSystems(ctx, crc, "", "", "", []string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SystemID != a || got[1].SystemID != b {
		t.Fatalf("got %v, want the %s game then the %s game", gameNames(got), a, b)
	}
	one, err := r.LookupGameByHash(ctx, strings.ToUpper(crc), "", "", "", other)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].SystemID != other {
		t.Fatalf("single-system lookup got %v", gameNames(one))
	}
}

// A browse page picks its rows before decorating them; the page must hold the
// same games, in the same order, as walking the full sorted list.
func TestPagedBrowseKeepsOrderAcrossPages(t *testing.T) {
	r, db := loadTestRepo(t)
	ctx := context.Background()
	sys := seedSystem(t, db, "Page "+uuid.NewString()[:6], 23)
	if _, err := db.Exec(`UPDATE games SET rating = (random() * 5)::int WHERE system_id = $1`, sys); err != nil {
		t.Fatal(err)
	}
	for _, sort := range []string{"", "name_desc", "rating_desc", "scrapes", "completion_asc"} {
		all, total, err := r.ListGamesBySystems(ctx, []string{sys}, 100, 0, "", sort)
		if err != nil {
			t.Fatal(err)
		}
		if total != 23 || len(all) != 23 {
			t.Fatalf("%q: got %d of %d games, want 23", sort, len(all), total)
		}
		var paged []string
		for off := 0; off < 23; off += 5 {
			page, _, err := r.ListGamesBySystems(ctx, []string{sys}, 5, off, "", sort)
			if err != nil {
				t.Fatal(err)
			}
			paged = append(paged, gameNames(page)...)
		}
		if strings.Join(paged, ",") != strings.Join(gameNames(all), ",") {
			t.Fatalf("%q: pages %v\nfull list %v", sort, paged, gameNames(all))
		}
		found, _, err := r.SearchGames(ctx, "page", []string{sys}, "", sort, 5, 5)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(gameNames(found), ",") != strings.Join(gameNames(all[5:10]), ",") {
			t.Fatalf("%q: search page %v, want %v", sort, gameNames(found), gameNames(all[5:10]))
		}
	}
}
