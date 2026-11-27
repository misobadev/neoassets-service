package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// An Android game is found by its package name, stored as the ROM name, only
// on an exact match and only within the requested systems.
func TestLookupGameByRomNameInSystems(t *testing.T) {
	r, db := loadTestRepo(t)
	ctx := context.Background()
	android := seedSystem(t, db, "App "+uuid.NewString()[:6], 1)
	other := seedSystem(t, db, "Other "+uuid.NewString()[:6], 1)
	pkg := "com.example.g" + uuid.NewString()[:8]

	var gameID uuid.UUID
	if err := db.QueryRow(`SELECT id FROM games WHERE system_id = $1`, android).Scan(&gameID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO roms (game_id, name, size, crc, md5, sha1) VALUES ($1, $2, 0, '', '', '')`, gameID, pkg); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		romName string
		systems []string
		want    int
	}{
		{"exact match", pkg, []string{android}, 1},
		{"any system", pkg, nil, 1},
		{"other system", pkg, []string{other}, 0},
		{"prefix is not a match", pkg[:len(pkg)-1], []string{android}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			games, err := r.LookupGameByRomNameInSystems(ctx, tc.romName, tc.systems)
			if err != nil {
				t.Fatal(err)
			}
			if len(games) != tc.want {
				t.Fatalf("got %d games %v, want %d", len(games), gameNames(games), tc.want)
			}
			if tc.want == 1 && games[0].ID != gameID {
				t.Fatalf("got game %s, want %s", games[0].ID, gameID)
			}
		})
	}
}
