package services

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/pkg/r2"
)

// fakeR2 records copies and deletes against an in-memory bucket. Only the
// calls the reject path makes are implemented; anything else panics via the
// nil embedded interface.
type fakeR2 struct {
	r2.Client
	objects map[string]bool
	copies  []string
	deletes []string
}

func newFakeR2(keys ...string) *fakeR2 {
	f := &fakeR2{objects: map[string]bool{}}
	for _, k := range keys {
		f.objects[k] = true
	}
	return f
}

func (f *fakeR2) CopyObject(_ context.Context, src, dst string) error {
	if !f.objects[src] {
		return fmt.Errorf("NoSuchKey: %s", src)
	}
	f.copies = append(f.copies, src)
	f.objects[dst] = true
	return nil
}

func (f *fakeR2) DeleteObject(_ context.Context, key string) error {
	f.deletes = append(f.deletes, key)
	delete(f.objects, key)
	return nil
}

func TestOwnedSubmissionUpload(t *testing.T) {
	user := uuid.New()
	sub := &models.MetadataSubmission{ID: uuid.New(), UserID: user}
	own := "media/staging/" + user.String() + "/cover.webp"
	subKey := "media/submissions/" + sub.ID.String() + "/cover/cover.webp"

	cases := []struct {
		name string
		file models.MetadataSubmissionFile
		want bool
	}{
		{"own staging upload", models.MetadataSubmissionFile{ObjectKey: own}, true},
		{"submission upload", models.MetadataSubmissionFile{ObjectKey: subKey}, true},
		{"another user's staging", models.MetadataSubmissionFile{ObjectKey: "media/staging/" + uuid.NewString() + "/x.webp"}, false},
		{"another submission's upload", models.MetadataSubmissionFile{ObjectKey: "media/submissions/" + uuid.NewString() + "/cover/x.webp"}, false},
		{"published media", models.MetadataSubmissionFile{ObjectKey: "media/games/nes/abc.webp"}, false},
		{"pack file", models.MetadataSubmissionFile{ObjectKey: "packs/p1/theme.json"}, false},
		{"delete row, even with an own key", models.MetadataSubmissionFile{ObjectKey: own, IsDelete: true}, false},
		{"move row", models.MetadataSubmissionFile{ObjectKey: "media/games/nes/abc.webp", IsMove: true}, false},
	}
	for _, c := range cases {
		if got := ownedSubmissionUpload(c.file, sub); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// A rejected submission must only move what it uploaded: move and delete rows
// name existing objects (move rows the game's published media), which must stay
// where the catalog expects them.
func TestPreserveRejectedSubmissionFilesOnlyMovesOwnUploads(t *testing.T) {
	user := uuid.New()
	sub := &models.MetadataSubmission{ID: uuid.New(), UserID: user}
	own := "media/staging/" + user.String() + "/cover.webp"
	subKey := "media/submissions/" + sub.ID.String() + "/logo/logo.webp"
	published := "media/games/nes/abc.webp"
	pack := "packs/p1/theme.json"

	bucket := newFakeR2(own, subKey, published, pack)
	files := []models.MetadataSubmissionFile{
		{ID: uuid.New(), ObjectKey: own},
		{ID: uuid.New(), ObjectKey: subKey},
		{ID: uuid.New(), ObjectKey: pack, IsDelete: true},
		{ID: uuid.New(), ObjectKey: published, IsMove: true},
	}
	updated := map[uuid.UUID]string{}
	setKey := func(id uuid.UUID, key string) error {
		updated[id] = key
		return nil
	}

	if err := preserveRejectedSubmissionFiles(context.Background(), bucket, sub, files, setKey); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !bucket.objects[published] || !bucket.objects[pack] {
		t.Fatalf("published objects were moved: copies=%v deletes=%v", bucket.copies, bucket.deletes)
	}
	if len(bucket.deletes) != 2 || bucket.objects[own] || bucket.objects[subKey] {
		t.Fatalf("own uploads not preserved: deletes=%v", bucket.deletes)
	}
	if updated[files[0].ID] != "rejected/"+own || updated[files[1].ID] != "rejected/"+subKey {
		t.Fatalf("own uploads not re-keyed: %v", updated)
	}
	if _, ok := updated[files[2].ID]; ok {
		t.Errorf("delete row was re-keyed")
	}
	if _, ok := updated[files[3].ID]; ok {
		t.Errorf("move row was re-keyed")
	}
}

// A delete row whose key names no object (or is empty) must not make the
// rejection fail.
func TestPreserveRejectedSubmissionFilesIgnoresDeleteRowKeys(t *testing.T) {
	sub := &models.MetadataSubmission{ID: uuid.New(), UserID: uuid.New()}
	files := []models.MetadataSubmissionFile{
		{ID: uuid.New(), ObjectKey: "", IsDelete: true},
		{ID: uuid.New(), ObjectKey: "does/not/exist.webp", IsDelete: true},
	}
	bucket := newFakeR2()

	err := preserveRejectedSubmissionFiles(context.Background(), bucket, sub, files, func(uuid.UUID, string) error { return nil })
	if err != nil {
		t.Fatalf("reject failed on a delete row: %v", err)
	}
	if len(bucket.copies)+len(bucket.deletes) != 0 {
		t.Fatalf("touched the bucket: copies=%v deletes=%v", bucket.copies, bucket.deletes)
	}
}
