package repository

import (
	"testing"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/internal/testdb"
)

// A contribution that replaces all.gif with all.webp shows only the new file:
// the older approved row for the same slot is not listed again.
func TestGetPackDetailListsAReplacedFileOnce(t *testing.T) {
	r := NewRepository(testdb.Open(t, "repository_pack_detail"))
	ctx := t.Context()
	pack := "pack-" + uuid.NewString()[:8]

	approved := func(contribution bool, files ...[2]string) {
		t.Helper()
		id, err := r.CreateSubmission(ctx, pack, "Pack", "Author", "", "", "", false, nil, contribution)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			key := "packs/" + pack + "/backgrounds/" + f[1]
			if err := r.AddFile(ctx, id, key, f[1], f[0], models.KindBackground, "image/webp", 1); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := r.db.ExecContext(ctx, `UPDATE submissions SET status = 'approved' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	approved(false, [2]string{"all", "all.gif"}, [2]string{"amiga", "amiga.webp"})
	approved(true, [2]string{"all", "all.webp"})

	d, err := r.GetPackDetail(ctx, pack)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range d.Files {
		got = append(got, f.FileName)
	}
	if len(got) != 2 || got[0] != "all.webp" || got[1] != "amiga.webp" {
		t.Fatalf("files = %v, want [all.webp amiga.webp]", got)
	}
}
