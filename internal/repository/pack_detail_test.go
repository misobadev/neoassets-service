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

// A PNG background approved before the file name followed the published key is
// served under the WebP name of its canonical object, not as snes.png.
func TestGetPackDetailServesThePublishedFileName(t *testing.T) {
	r := NewRepository(testdb.Open(t, "repository_pack_detail"))
	ctx := t.Context()
	pack := "pack-" + uuid.NewString()[:8]

	id, err := r.CreateSubmission(ctx, pack, "Pack", "Author", "", "", "", false, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	add := func(key, name, system, kind string) {
		t.Helper()
		if err := r.AddFile(ctx, id, key, name, system, kind, "image/webp", 1); err != nil {
			t.Fatal(err)
		}
	}
	add("packs/"+pack+"/backgrounds/snes.webp", "snes.png", "snes", models.KindBackground)
	add("packs/"+pack+"/logos/gba.webp", "gba.jpg", "gba", models.KindLogo)
	add("packs/"+pack+"/backgrounds/all.gif", "all.gif", "all", models.KindBackground)
	if _, err := r.db.ExecContext(ctx, `UPDATE submissions SET status = 'approved' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	d, err := r.GetPackDetail(ctx, pack)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range d.Files {
		got[f.SystemID] = f.FileName
	}
	want := map[string]string{"snes": "snes.webp", "gba": "gba.webp", "all": "all.gif"}
	for system, name := range want {
		if got[system] != name {
			t.Errorf("%s file_name = %q, want %q", system, got[system], name)
		}
	}
}

func TestPublishedFileName(t *testing.T) {
	for _, tc := range []struct {
		kind, key, name, want string
	}{
		{models.KindBackground, "packs/p/backgrounds/snes.webp", "snes.png", "snes.webp"},
		{models.KindLogo, "packs/p/logos/gba.webp", "gba.jpg", "gba.webp"},
		{models.KindBackground, "packs/p/backgrounds/all.gif", "all.gif", "all.gif"},
		// Not at its canonical key: keep the stored name.
		{models.KindBackground, "packs/p/review/abcd1234/snes.png", "snes.png", "snes.png"},
		{models.KindBackground, "packs/other/backgrounds/snes.webp", "snes.png", "snes.png"},
		// Only backgrounds and logos are renamed.
		{models.KindTheme, "packs/p/theme.json", "theme.json", "theme.json"},
		{models.KindPreview, "packs/p/preview.webp", "preview.png", "preview.png"},
	} {
		f := models.PackFile{Kind: tc.kind, ObjectKey: tc.key, FileName: tc.name}
		if got := publishedFileName("p", f); got != tc.want {
			t.Errorf("publishedFileName(%s, %s, %s) = %q, want %q", tc.kind, tc.key, tc.name, got, tc.want)
		}
	}
}
