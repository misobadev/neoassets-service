package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"neoassets/internal/apperr"
	"neoassets/internal/models"
	"neoassets/internal/repository"
	"neoassets/internal/testdb"
	"neoassets/pkg/r2/r2test"
)

func isKind(err error, kind apperr.Kind) bool { return err != nil && apperr.KindOf(err) == kind }

// stagedUpload puts an upload where a presign for this pack and name would.
func stagedUpload(bucket *r2test.Memory, packID, fileName string, data []byte) string {
	key := reviewObjectKey(packID, fileName)
	bucket.Put(key, "image/png", data)
	return key
}

func newIntegrityPack(t *testing.T, s *Service, bucket *r2test.Memory, owner uuid.UUID) (*models.Submission, string) {
	t.Helper()
	name := "Pack " + uuid.NewString()[:8]
	packID := s.PackIDFromName(name)
	key := stagedUpload(bucket, packID, "snes.png", []byte("first version"))
	sub, err := s.CreateSubmission(context.Background(), models.CreateSubmissionRequest{
		Name: name, Author: "A", DonationURL: "https://ko-fi.com/a",
		Files: []models.SubmissionFileInput{{Kind: models.KindBackground, ObjectKey: key, FileName: "snes.png", SystemID: "snes", MimeType: "image/png", Size: 13}},
	}, owner)
	if err != nil {
		t.Fatal(err)
	}
	return sub, key
}

func fileKeys(t *testing.T, repo *repository.Repository, id uuid.UUID) []string {
	t.Helper()
	files, err := repo.ListFiles(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, len(files))
	for i, f := range files {
		keys[i] = f.ObjectKey
	}
	return keys
}

// Registering an upload moves it to a key no presign targets, so a second PUT
// with the presigned URL (which stays valid for a while) can't change what the
// reviewer sees.
func TestRegisteredUploadsAreFrozen(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	sub, staged := newIntegrityPack(t, s, bucket, owner.ID)

	keys := fileKeys(t, repo, sub.ID)
	if len(keys) != 1 || keys[0] == staged || !isReviewObjectKey(keys[0]) {
		t.Fatalf("registered keys %v (staged %s)", keys, staged)
	}
	if _, ok, _ := bucket.ObjectSize(t.Context(), staged); ok {
		t.Fatal("the staged upload was left in place")
	}
	// A later PUT to the presigned key lands beside the registered file.
	bucket.Put(staged, "image/png", []byte("swapped after review started"))
	data, err := bucket.DownloadObject(t.Context(), keys[0], 1<<20)
	if err != nil || string(data) != "first version" {
		t.Fatalf("registered file = %q, %v", data, err)
	}
	// A frozen key can't be registered by a client.
	err = s.validatePackObjectKey(sub.PackID, models.SubmissionFileInput{Kind: models.KindBackground, ObjectKey: keys[0], FileName: "snes.png"})
	if !isKind(err, apperr.KindForbidden) {
		t.Fatalf("frozen key accepted from a client: %v", err)
	}
}

// Only keys a presign hands out for this pack and file name, or the exact
// canonical key, are accepted.
func TestPackObjectKeysAreStrict(t *testing.T) {
	s := NewService(nil, nil, "", "", nil)
	ok := func(key, kind, name string) error {
		return s.validatePackObjectKey("p", models.SubmissionFileInput{Kind: kind, ObjectKey: key, FileName: name})
	}
	for _, c := range []struct{ key, kind, name string }{
		{"packs/p/review/0a1b2c3d/snes.png", models.KindBackground, "snes.png"},
		{"packs/p/backgrounds/snes.webp", models.KindBackground, "snes.png"},
		{"packs/p/preview.webp", models.KindPreview, "shot.png"},
	} {
		if err := ok(c.key, c.kind, c.name); err != nil {
			t.Errorf("%s refused: %v", c.key, err)
		}
	}
	for _, c := range []struct{ key, kind, name string }{
		{"packs/q/review/0a1b2c3d/snes.png", models.KindBackground, "snes.png"},                         // another pack
		{"packs/p/review/0a1b2c3d/other.png", models.KindBackground, "snes.png"},                        // another name
		{"packs/p/review/../../q/review/0a1b2c3d/snes.png", models.KindBackground, "snes.png"},          // dot segments
		{"packs/p/review/0a1b2c3d0a1b2c3d0a1b2c3d0a1b2c3d/snes.png", models.KindBackground, "snes.png"}, // frozen
		{"packs/p/uploads/x/snes.png", models.KindBackground, "snes.png"},
		{"packs/p/backgrounds/n64.webp", models.KindBackground, "snes.png"},
		{"media/games/x.webp", models.KindPreview, "x.webp"},
	} {
		if err := ok(c.key, c.kind, c.name); err == nil {
			t.Errorf("%s accepted", c.key)
		}
	}
}

// File names can't climb out of their folder or carry control characters, at
// presign and at registration; a background's or logo's name must be a system
// id.
func TestFileNamesAreChecked(t *testing.T) {
	s := NewService(nil, nil, "", "", nil)
	for _, name := range []string{"../../../../media/games/x.webp", "a/b.png", `a\b.png`, "x\x00.png", "..", "",
		"Preview 100%.png", "a.png?x.png", "a.png#x.png", "a%2f..%2fb.png", "evil\u202egnp.png", "zero\u200bwidth.png", strings.Repeat("a", 256) + ".png"} {
		if err := s.ValidateUploadRequest(models.UploadRequest{Kind: models.KindPreview, FileName: name, MimeType: "image/png", Size: 10}); err == nil {
			t.Errorf("presign accepted %q", name)
		}
		if err := s.validateSubmissionFile(models.SubmissionFileInput{Kind: models.KindPreview, ObjectKey: "k", FileName: name}); err == nil {
			t.Errorf("registration accepted %q", name)
		}
	}
	for _, name := range []string{"..png", "sn es.png", "snes.backup.png"} {
		if err := s.validateSubmissionFile(models.SubmissionFileInput{Kind: models.KindBackground, ObjectKey: "k", FileName: name}); err == nil {
			t.Errorf("background %q accepted", name)
		}
	}
	for _, name := range []string{"Preview 100.png", "c64+.png", "Pokémon (Japan) [!].png", "東方.webp"} {
		if err := s.validateSubmissionFile(models.SubmissionFileInput{Kind: models.KindPreview, ObjectKey: "k", FileName: name}); err != nil {
			t.Errorf("an ordinary preview name %q refused: %v", name, err)
		}
	}
}

// A submission in review takes no new uploads.
func TestNoUploadsWhileInReview(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	sub, _ := newIntegrityPack(t, s, bucket, owner.ID)
	_, err := s.GetUploadURL(context.Background(), sub.ID, owner.ID, models.UploadRequest{Kind: models.KindBackground, FileName: "snes.png", MimeType: "image/png", Size: 10})
	if !isKind(err, apperr.KindConflict) {
		t.Fatalf("upload signed for a submission in review: %v", err)
	}
}

// Trashing a submission in review works, and deletes only its own staged
// files: never the live pack's files or another submission's.
func TestTrashInReviewDeletesOnlyItsOwnFiles(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	contributor := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)

	// The owner's pack is live; its canonical file and an in-progress upload
	// of the owner's must survive the contributor's trash.
	packID := approvedPack(t, repo, owner.ID, reviewer.ID, "https://ko-fi.com/owner")
	canonical := "packs/" + packID + "/backgrounds/snes.webp"
	bucket.Put(canonical, "image/webp", []byte("live"))
	ownerUpload := stagedUpload(bucket, packID, "n64.png", []byte("owner's next upload"))

	contribution, err := s.CreateSubmission(context.Background(), models.CreateSubmissionRequest{
		Contribution: true, PackID: packID, Name: "Original Name", Author: "Original Author", Description: "more art",
		Files: []models.SubmissionFileInput{
			{Kind: models.KindBackground, ObjectKey: canonical, FileName: "snes.png", SystemID: "snes", MimeType: "image/png", Size: 4},
			{Kind: models.KindBackground, ObjectKey: stagedUpload(bucket, packID, "gba.png", []byte("new")), FileName: "gba.png", SystemID: "gba", MimeType: "image/png", Size: 3},
		},
	}, contributor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if contribution.Status != models.StatusPending {
		t.Fatalf("status %q", contribution.Status)
	}
	var frozen string
	for _, k := range fileKeys(t, repo, contribution.ID) {
		if k != canonical {
			frozen = k
		}
	}

	trashed, err := s.Trash(context.Background(), contribution.ID, contributor.ID)
	if err != nil || trashed.Status != models.StatusTrashed {
		t.Fatalf("trash in review: %v %+v", err, trashed)
	}
	for _, k := range []string{canonical, ownerUpload} {
		if _, ok, _ := bucket.ObjectSize(t.Context(), k); !ok {
			t.Errorf("%s was deleted", k)
		}
	}
	if _, ok, _ := bucket.ObjectSize(t.Context(), frozen); ok {
		t.Errorf("the trashed submission's own upload %s was kept", frozen)
	}
	if keys := fileKeys(t, repo, contribution.ID); len(keys) != 0 {
		t.Errorf("file rows left: %v", keys)
	}
}

// A refused trash (an approved submission) deletes nothing.
func TestRefusedTrashDeletesNothing(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	sub, _ := newIntegrityPack(t, s, bucket, owner.ID)
	keys := fileKeys(t, repo, sub.ID)
	if _, err := repo.SetSubmissionStatus(t.Context(), sub.ID, models.StatusApproved, reviewer.ID, "1.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Trash(context.Background(), sub.ID, owner.ID); err == nil {
		t.Fatal("an approved submission was trashed")
	}
	if _, ok, _ := bucket.ObjectSize(t.Context(), keys[0]); !ok || len(fileKeys(t, repo, sub.ID)) != 1 {
		t.Fatal("a refused trash deleted files")
	}
}

// A new pack without files is refused before it enters the review queue.
func TestSubmitWithoutFilesStaysOutOfReview(t *testing.T) {
	s, repo := contentTestService(t, r2test.NewMemory())
	owner := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	packID := "pack-" + uuid.NewString()[:8]
	id, err := repo.CreateSubmission(t.Context(), packID, "P "+packID, "A", "", "", owner.ID.String(), false, &owner.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetSubmissionStatus(t.Context(), id, models.StatusRejected, reviewer.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitForReview(context.Background(), id, owner.ID); !isKind(err, apperr.KindInvalid) {
		t.Fatalf("submit without files: %v", err)
	}
	if sub, _ := repo.GetSubmission(t.Context(), id); sub.Status != models.StatusRejected {
		t.Fatalf("status after a refused submit: %q", sub.Status)
	}
}

// Deleting a submission keeps the objects another live revision of the pack
// still serves.
func TestDeleteSubmissionKeepsTheLivePacksFiles(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	packID := "pack-" + uuid.NewString()[:8]
	canonical := "packs/" + packID + "/backgrounds/snes.webp"
	only := "packs/" + packID + "/backgrounds/gba.webp"
	bucket.Put(canonical, "image/webp", []byte("shared"))
	bucket.Put(only, "image/webp", []byte("old revision only"))

	revision := func(keys ...string) uuid.UUID {
		id, err := repo.CreateSubmission(t.Context(), packID, "P", "A", "", "", owner.ID.String(), false, &owner.ID, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range keys {
			name := strings.TrimSuffix(k[strings.LastIndex(k, "/")+1:], ".webp") + ".webp"
			if err := repo.AddFileReason(t.Context(), id, k, name, strings.TrimSuffix(name, ".webp"), models.KindBackground, "image/webp", 1, ""); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := repo.SetSubmissionStatus(t.Context(), id, models.StatusApproved, reviewer.ID, "1.0"); err != nil {
			t.Fatal(err)
		}
		return id
	}
	old := revision(canonical, only)
	revision(canonical)

	if err := s.DeleteSubmission(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := bucket.ObjectSize(t.Context(), canonical); !ok {
		t.Error("the live pack's shared file was deleted")
	}
	if _, ok, _ := bucket.ObjectSize(t.Context(), only); ok {
		t.Error("the deleted revision's own file was kept")
	}
}

// Approving a contribution that only keeps the live pack's files publishes
// nothing, and so awards no XP.
func TestKeptFilesEarnNoXP(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	contributor := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	packID := approvedPack(t, repo, owner.ID, reviewer.ID, "https://ko-fi.com/owner")
	canonical := "packs/" + packID + "/backgrounds/snes.webp"
	bucket.Put(canonical, "image/webp", []byte("live"))

	sub, err := s.CreateSubmission(context.Background(), models.CreateSubmissionRequest{
		Contribution: true, PackID: packID, Name: "Original Name", Author: "Original Author", Description: "only a description",
		Files: []models.SubmissionFileInput{{Kind: models.KindBackground, ObjectKey: canonical, FileName: "snes.png", SystemID: "snes", MimeType: "image/png", Size: 4}},
	}, contributor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(context.Background(), sub.ID, reviewer.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if u, _ := repo.GetUserByID(t.Context(), contributor.ID); u.XP != 0 {
		t.Fatalf("XP for files only kept from the live pack: %d", u.XP)
	}
}

// Metadata uploads: only the exact staging key a presign handed the user for
// this kind and name is accepted, and registering moves it into the
// submission's own namespace.
func TestMetadataUploadKeysAreStrictAndFrozen(t *testing.T) {
	user := uuid.New()
	good := stagingMediaKey(user, models.MediaCover, "cover.png")
	if !isOwnUploadKey(good, user, models.MediaCover, "cover.png") {
		t.Fatalf("own key refused: %s", good)
	}
	for _, c := range []struct{ key, kind, name string }{
		{good, models.MediaLogo, "cover.png"},
		{good, models.MediaCover, "other.png"},
		{stagingMediaKey(uuid.New(), models.MediaCover, "cover.png"), models.MediaCover, "cover.png"},
		{"media/staging/" + user.String() + "/../../games/x/cover/cover.png", models.MediaCover, "cover.png"},
		{"media/staging/" + user.String() + "/not-a-uuid/cover/cover.png", models.MediaCover, "cover.png"},
		{"media/staging/" + user.String() + "/" + uuid.NewString() + "/cover/x/cover.png", models.MediaCover, "cover.png"},
	} {
		if isOwnUploadKey(c.key, user, c.kind, c.name) {
			t.Errorf("accepted %s as %s/%s", c.key, c.kind, c.name)
		}
	}

	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	u := contentTestUser(t, repo, models.RoleUser)
	staged := stagingMediaKey(u.ID, models.MediaCover, "cover.png")
	bucket.Put(staged, "image/png", []byte("cover"))
	frozen, err := s.freezeMediaUpload(t.Context(), u.ID, models.MetadataUploadRequest{Kind: models.MediaCover, ObjectKey: staged, FileName: "cover.png"})
	if err != nil {
		t.Fatal(err)
	}
	// The copy is the user's own (cleaned up with the submission) but can't be
	// registered again.
	if frozen == staged || !isOwnStagingKey(frozen, u.ID) || isOwnUploadKey(frozen, u.ID, models.MediaCover, "cover.png") {
		t.Fatalf("frozen key %s", frozen)
	}
}

// A trash that arrives while the submission is being reviewed is refused and
// deletes nothing.
func TestTrashDuringReviewDeletesNothing(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	sub, _ := newIntegrityPack(t, s, bucket, owner.ID)
	keys := fileKeys(t, repo, sub.ID)

	err := repo.WithReviewLock(t.Context(), sub.ID, func() error {
		_, err := s.Trash(context.Background(), sub.ID, owner.ID)
		return err
	})
	if !isKind(err, apperr.KindConflict) {
		t.Fatalf("trash during a review: %v", err)
	}
	if _, ok, _ := bucket.ObjectSize(t.Context(), keys[0]); !ok || len(fileKeys(t, repo, sub.ID)) != 1 {
		t.Fatal("a trash refused during a review deleted files")
	}
	if got, _ := repo.GetSubmission(t.Context(), sub.ID); got.Status != models.StatusPending {
		t.Fatalf("status %q", got.Status)
	}
}

// Creating a metadata submission freezes its uploads (the staged key is gone,
// the row points at the copy), and a refused create leaves no copies behind.
func TestMetadataCreateFreezesUploads(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	u := contentTestUser(t, repo, models.RoleUser)
	systemID := "sys" + uuid.NewString()[:8]
	if _, err := testdb.Open(t, "services_upload").Exec(`INSERT INTO metadata_systems (id, name) VALUES ($1, $1)`, systemID); err != nil {
		t.Fatal(err)
	}
	staged := stagingMediaKey(u.ID, models.MediaFanart, "fanart.png")
	bucket.Put(staged, "image/png", []byte("fanart"))
	sub, err := s.CreateMetadataSubmission(context.Background(), u.ID, models.MetadataSubmissionRequest{
		SystemID: &systemID, Payload: map[string]any{"description": "new art"},
		Files: []models.MetadataUploadRequest{{Kind: models.MediaFanart, ObjectKey: staged, FileName: "fanart.png", MimeType: "image/png", Size: 6}},
	})
	if err != nil {
		t.Fatal(err)
	}
	files, err := repo.ListMetadataSubmissionFiles(t.Context(), sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ObjectKey == staged || !isOwnStagingKey(files[0].ObjectKey, u.ID) {
		t.Fatalf("registered files %+v", files)
	}
	if _, ok, _ := bucket.ObjectSize(t.Context(), staged); ok {
		t.Fatal("the staged upload was left in place")
	}

	// A second file that fails validation: nothing is copied or written.
	before := len(bucket.Keys())
	good := stagingMediaKey(u.ID, models.MediaCover, "cover.png")
	bucket.Put(good, "image/png", []byte("cover"))
	_, err = s.CreateMetadataSubmission(context.Background(), u.ID, models.MetadataSubmissionRequest{
		SystemID: &systemID, Payload: map[string]any{"description": "again"},
		Files: []models.MetadataUploadRequest{
			{Kind: models.MediaCover, ObjectKey: good, FileName: "cover.png", MimeType: "image/png", Size: 5},
			{Kind: models.MediaLogo, ObjectKey: "media/games/x/logo.png", FileName: "logo.png", MimeType: "image/png", Size: 5},
		},
	})
	if err == nil {
		t.Fatal("a create with a foreign key was accepted")
	}
	if after := len(bucket.Keys()); after != before+1 {
		t.Fatalf("objects: %d before, %d after (want only the new staged upload)", before, after)
	}
}

// The per-submission upload routes don't sign for a submission in review.
func TestPerSubmissionUploadsRefusedInReview(t *testing.T) {
	s, repo := contentTestService(t, r2test.NewMemory())
	u := contentTestUser(t, repo, models.RoleUser)
	systemID := "sys" + uuid.NewString()[:8]
	if _, err := testdb.Open(t, "services_upload").Exec(`INSERT INTO metadata_systems (id, name) VALUES ($1, $1)`, systemID); err != nil {
		t.Fatal(err)
	}
	metaID, err := repo.CreateMetadataSubmission(t.Context(), nil, &systemID, u.ID, "edit", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.MetadataUploadURL(context.Background(), metaID, u.ID, models.MetadataUploadRequest{Kind: models.MediaFanart, FileName: "f.png", MimeType: "image/png", Size: 10})
	if !isKind(err, apperr.KindConflict) {
		t.Fatalf("metadata upload signed for a submission in review: %v", err)
	}
}

// Adding files to a rejected pack freezes them, and the per-submission route
// registers a presigned key only on a draft, which submitting freezes.
func TestAddFilesAndSubmitFreezeUploads(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	sub, _ := newIntegrityPack(t, s, bucket, owner.ID)
	if _, err := repo.SetSubmissionStatus(t.Context(), sub.ID, models.StatusRejected, reviewer.ID, ""); err != nil {
		t.Fatal(err)
	}

	staged := stagedUpload(bucket, sub.PackID, "n64.png", []byte("n64"))
	if _, err := s.AddSubmissionFiles(context.Background(), sub.ID, owner.ID, []models.SubmissionFileInput{
		// The system id comes from the file name, whatever the client sends.
		{Kind: models.KindBackground, ObjectKey: staged, FileName: "n64.png", SystemID: "snes", MimeType: "image/png", Size: 3},
	}); err != nil {
		t.Fatal(err)
	}
	added, err := repo.ListFiles(t.Context(), sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range added {
		if f.FileName == "n64.png" && f.SystemID != "n64" {
			t.Fatalf("system id %q stored for n64.png", f.SystemID)
		}
	}
	for _, k := range fileKeys(t, repo, sub.ID) {
		if k == staged || reviewKeyRegex.MatchString(k) {
			t.Fatalf("an added file kept its presigned key: %v", fileKeys(t, repo, sub.ID))
		}
	}
	// Re-saving what the client got back is accepted and changes nothing.
	keys := fileKeys(t, repo, sub.ID)
	if _, err := s.AddSubmissionFiles(context.Background(), sub.ID, owner.ID, []models.SubmissionFileInput{
		{Kind: models.KindBackground, ObjectKey: keys[0], FileName: "snes.png", SystemID: "snes", MimeType: "image/png", Size: 13},
	}); err != nil {
		t.Fatalf("re-saving a registered file: %v", err)
	}
	// Removing by the presigned key the client still holds removes the file.
	if err := s.RemoveSubmissionFile(context.Background(), sub.ID, owner.ID, staged); err != nil {
		t.Fatal(err)
	}
	if n := len(fileKeys(t, repo, sub.ID)); n != len(keys)-1 {
		t.Fatalf("files after removing by the presigned key: %d, want %d", n, len(keys)-1)
	}

	// A draft's per-submission upload is frozen when it is submitted.
	if _, err := testdb.Open(t, "services_upload").Exec(`UPDATE submissions SET status = 'created' WHERE id = $1`, sub.ID); err != nil {
		t.Fatal(err)
	}
	up, err := s.GetUploadURL(context.Background(), sub.ID, owner.ID, models.UploadRequest{Kind: models.KindBackground, FileName: "gba.png", MimeType: "image/png", Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	bucket.Put(up.ObjectKey, "image/png", []byte("gba"))
	if _, err := s.SubmitForReview(context.Background(), sub.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	for _, k := range fileKeys(t, repo, sub.ID) {
		if reviewKeyRegex.MatchString(k) {
			t.Fatalf("a file entered review at its presigned key %s", k)
		}
	}
	bucket.Put(up.ObjectKey, "image/png", []byte("swapped"))
	for _, k := range fileKeys(t, repo, sub.ID) {
		if data, _ := bucket.DownloadObject(t.Context(), k, 1<<20); string(data) == "swapped" {
			t.Fatal("the presigned URL replaced a file in review")
		}
	}
}

// A staged upload another submission row already references can't be taken.
func TestStagedKeyOfAnotherSubmissionIsRefused(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	other := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	packID := approvedPack(t, repo, owner.ID, reviewer.ID, "https://ko-fi.com/owner")
	key := stagedUpload(bucket, packID, "n64.png", []byte("owner's"))
	ownerSub, err := repo.CreateSubmission(t.Context(), packID, "P", "A", "", "", owner.ID.String(), false, &owner.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddFileReason(t.Context(), ownerSub, key, "n64.png", "n64", models.KindBackground, "image/png", 7, ""); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateSubmission(context.Background(), models.CreateSubmissionRequest{
		Contribution: true, PackID: packID, Name: "Original Name", Author: "Original Author",
		Files: []models.SubmissionFileInput{{Kind: models.KindBackground, ObjectKey: key, FileName: "n64.png", SystemID: "n64", MimeType: "image/png", Size: 7}},
	}, other.ID)
	if !isKind(err, apperr.KindForbidden) {
		t.Fatalf("another submission's staged upload taken: %v", err)
	}
	if _, ok, _ := bucket.ObjectSize(t.Context(), key); !ok {
		t.Fatal("the other submission's upload was deleted")
	}
}

// Approval XP counts published files only: not a row whose upload never
// arrived.
func TestXPSkipsFilesThatNeverArrived(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	packID := "pack-" + uuid.NewString()[:8]
	id, err := repo.CreateSubmission(t.Context(), packID, "P "+packID, "A", "", "", owner.ID.String(), false, &owner.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddFileReason(t.Context(), id, frozenReviewKey(packID, "theme.json"), "theme.json", "", models.KindTheme, "application/json", 2, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(context.Background(), id, reviewer.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if u, _ := repo.GetUserByID(t.Context(), owner.ID); u.XP != 0 {
		t.Fatalf("XP for a file that was never uploaded: %d", u.XP)
	}
}

// Deleting a revision doesn't keep files only a pending contribution
// references.
func TestDeleteSubmissionIgnoresPendingReferences(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	packID := "pack-" + uuid.NewString()[:8]
	canonical := "packs/" + packID + "/backgrounds/snes.webp"
	bucket.Put(canonical, "image/webp", []byte("only in this revision"))
	rev, err := repo.CreateSubmission(t.Context(), packID, "P", "A", "", "", owner.ID.String(), false, &owner.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddFileReason(t.Context(), rev, canonical, "snes.webp", "snes", models.KindBackground, "image/webp", 1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetSubmissionStatus(t.Context(), rev, models.StatusApproved, reviewer.ID, "1.0"); err != nil {
		t.Fatal(err)
	}
	pending, err := repo.CreateSubmission(t.Context(), packID, "P", "A", "", "", owner.ID.String(), false, &owner.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddFileReason(t.Context(), pending, canonical, "snes.webp", "snes", models.KindBackground, "image/webp", 1, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSubmission(context.Background(), rev); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := bucket.ObjectSize(t.Context(), canonical); ok {
		t.Fatal("a pending contribution's reference kept a deleted revision's file")
	}
}

// Public URLs escape each key segment, so a key always names its object.
func TestPublicURLEscapesKeys(t *testing.T) {
	s := &Service{publicBase: "https://cdn.test"}
	if got := s.PublicURL("packs/p/review/0a1b2c3d/a b#1.png"); got != "https://cdn.test/packs/p/review/0a1b2c3d/a%20b%231.png" {
		t.Fatalf("PublicURL = %q", got)
	}
}

// Metadata upload names are cleaned, not refused (they never reach the
// published key), and the cleaned key is still recognised as the upload's.
func TestMetadataNamesAreCleaned(t *testing.T) {
	for in, want := range map[string]string{
		"cover.png":                       "cover.png",
		"Box art #2.png":                  "Box art _2.png",
		"100% done?.png":                  "100_ done_.png",
		"Pokémon.webp":                    "Pokémon.webp",
		strings.Repeat("a", 256) + ".png": "cover.png",
	} {
		if got := mediaKeyName(models.MediaCover, in); got != want {
			t.Errorf("mediaKeyName(%q) = %q, want %q", in, got, want)
		}
	}
	user := uuid.New()
	key := stagingMediaKey(user, models.MediaCover, "Box art #2.png")
	if !isOwnUploadKey(key, user, models.MediaCover, "Box art #2.png") {
		t.Fatalf("cleaned key %s not recognised", key)
	}
}

// failingDeletes is a bucket whose bulk deletes fail.
type failingDeletes struct{ *r2test.Memory }

func (failingDeletes) DeleteObjects(context.Context, []string) error {
	return errors.New("storage unavailable")
}

// If a trashed submission's files can't be deleted, its rows stay, so the
// objects keep a reference.
func TestTrashKeepsRowsWhenDeletesFail(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	sub, _ := newIntegrityPack(t, s, bucket, owner.ID)
	s.r2 = failingDeletes{bucket}
	if _, err := s.Trash(context.Background(), sub.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(fileKeys(t, repo, sub.ID)); n != 1 {
		t.Fatalf("file rows after a failed delete: %d, want 1", n)
	}
}

// Files can't be removed from a submission in review.
func TestRemoveRefusedInReview(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	sub, _ := newIntegrityPack(t, s, bucket, owner.ID)
	keys := fileKeys(t, repo, sub.ID)
	if err := s.RemoveSubmissionFile(context.Background(), sub.ID, owner.ID, keys[0]); !isKind(err, apperr.KindConflict) {
		t.Fatalf("remove in review: %v", err)
	}
	if len(fileKeys(t, repo, sub.ID)) != 1 {
		t.Fatal("a file was removed from a submission in review")
	}
}

// Deleting a submission waits for no review: one under review is refused and
// keeps its files.
func TestDeleteSubmissionDuringReviewIsRefused(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	sub, _ := newIntegrityPack(t, s, bucket, owner.ID)
	keys := fileKeys(t, repo, sub.ID)
	err := repo.WithReviewLock(t.Context(), sub.ID, func() error {
		return s.DeleteSubmission(context.Background(), sub.ID)
	})
	if !isKind(err, apperr.KindConflict) {
		t.Fatalf("delete during a review: %v", err)
	}
	if _, ok, _ := bucket.ObjectSize(t.Context(), keys[0]); !ok {
		t.Fatal("files deleted during a review")
	}
}

// Cancelling a metadata submission under review is refused and deletes
// nothing.
func TestCancelMetadataDuringReviewIsRefused(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	u := contentTestUser(t, repo, models.RoleUser)
	systemID := "sys" + uuid.NewString()[:8]
	if _, err := testdb.Open(t, "services_upload").Exec(`INSERT INTO metadata_systems (id, name) VALUES ($1, $1)`, systemID); err != nil {
		t.Fatal(err)
	}
	staged := stagingMediaKey(u.ID, models.MediaFanart, "fanart.png")
	bucket.Put(staged, "image/png", []byte("fanart"))
	sub, err := s.CreateMetadataSubmission(context.Background(), u.ID, models.MetadataSubmissionRequest{
		SystemID: &systemID, Payload: map[string]any{"description": "x"},
		Files: []models.MetadataUploadRequest{{Kind: models.MediaFanart, ObjectKey: staged, FileName: "fanart.png", MimeType: "image/png", Size: 6}},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = repo.WithReviewLock(t.Context(), sub.ID, func() error {
		return s.CancelMetadataSubmission(context.Background(), sub.ID, u.ID)
	})
	if !isKind(err, apperr.KindConflict) {
		t.Fatalf("cancel during a review: %v", err)
	}
	if _, err := repo.GetMetadataSubmissionForUser(t.Context(), sub.ID, u.ID); err != nil {
		t.Fatalf("submission gone after a refused cancel: %v", err)
	}
}

// Replacing a file (same name) on a rejected pack deletes the replaced upload.
func TestReplacedFileIsDeleted(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	sub, _ := newIntegrityPack(t, s, bucket, owner.ID)
	old := fileKeys(t, repo, sub.ID)[0]
	if _, err := repo.SetSubmissionStatus(t.Context(), sub.ID, models.StatusRejected, reviewer.ID, ""); err != nil {
		t.Fatal(err)
	}
	staged := stagedUpload(bucket, sub.PackID, "snes.png", []byte("second version"))
	if _, err := s.AddSubmissionFiles(context.Background(), sub.ID, owner.ID, []models.SubmissionFileInput{
		{Kind: models.KindBackground, ObjectKey: staged, FileName: "snes.png", SystemID: "snes", MimeType: "image/png", Size: 14},
	}); err != nil {
		t.Fatal(err)
	}
	if keys := fileKeys(t, repo, sub.ID); len(keys) != 1 || keys[0] == old {
		t.Fatalf("files after replacing: %v", keys)
	}
	if _, ok, _ := bucket.ObjectSize(t.Context(), old); ok {
		t.Fatal("the replaced upload was kept")
	}
}

// Submitting a draft drops a file whose upload never arrived, instead of
// being blocked by it.
func TestSubmitDropsUploadsThatNeverArrived(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	sub, _ := newIntegrityPack(t, s, bucket, owner.ID)
	if _, err := testdb.Open(t, "services_upload").Exec(`UPDATE submissions SET status = 'created' WHERE id = $1`, sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUploadURL(context.Background(), sub.ID, owner.ID, models.UploadRequest{Kind: models.KindBackground, FileName: "gba.png", MimeType: "image/png", Size: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitForReview(context.Background(), sub.ID, owner.ID); err != nil {
		t.Fatalf("submit with an upload that never arrived: %v", err)
	}
	if keys := fileKeys(t, repo, sub.ID); len(keys) != 1 {
		t.Fatalf("files after submit: %v", keys)
	}
}

// A stale presigned key that matches two frozen files (a background and a
// logo of the same name) removes neither and says so.
func TestRemoveByAmbiguousPresignedKeyRemovesNothing(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	sub, _ := newIntegrityPack(t, s, bucket, owner.ID)
	if _, err := repo.SetSubmissionStatus(t.Context(), sub.ID, models.StatusRejected, reviewer.ID, ""); err != nil {
		t.Fatal(err)
	}
	logo := stagedUpload(bucket, sub.PackID, "snes.png", []byte("logo"))
	if _, err := s.AddSubmissionFiles(context.Background(), sub.ID, owner.ID, []models.SubmissionFileInput{
		{Kind: models.KindLogo, ObjectKey: logo, FileName: "snes.png", SystemID: "snes", MimeType: "image/png", Size: 4},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveSubmissionFile(context.Background(), sub.ID, owner.ID, logo); !isKind(err, apperr.KindConflict) {
		t.Fatalf("ambiguous remove: %v, want a conflict", err)
	}
	if n := len(fileKeys(t, repo, sub.ID)); n != 2 {
		t.Fatalf("files after an ambiguous remove: %d, want 2", n)
	}
}

// hookBucket runs a callback once, in the middle of a signing or a copy, to
// interleave two requests deterministically.
type hookBucket struct {
	*r2test.Memory
	onSign, onCopy func()
}

func (h *hookBucket) GenerateSignedUploadURL(ctx context.Context, key, contentType string, ttl time.Duration) (string, error) {
	if f := h.onSign; f != nil {
		h.onSign = nil
		f()
	}
	return h.Memory.GenerateSignedUploadURL(ctx, key, contentType, ttl)
}

func (h *hookBucket) CopyObject(ctx context.Context, src, dst string) error {
	if f := h.onCopy; f != nil {
		h.onCopy = nil
		f()
	}
	return h.Memory.CopyObject(ctx, src, dst)
}

func draftIntegrityPack(t *testing.T, s *Service, bucket *r2test.Memory, owner uuid.UUID) *models.Submission {
	t.Helper()
	sub, _ := newIntegrityPack(t, s, bucket, owner)
	if _, err := testdb.Open(t, "services_upload").Exec(`UPDATE submissions SET status = 'created' WHERE id = $1`, sub.ID); err != nil {
		t.Fatal(err)
	}
	return sub
}

func presignedKeys(keys []string) []string {
	var out []string
	for _, k := range keys {
		if reviewKeyRegex.MatchString(k) {
			out = append(out, k)
		}
	}
	return out
}

// A submit that lands while an upload URL is being signed wins: the upload
// request is refused (no URL), and the pending submission holds no presigned
// key.
func TestUploadRequestRacingASubmitIsRefused(t *testing.T) {
	bucket := r2test.NewMemory()
	hooks := &hookBucket{Memory: bucket}
	s, repo := contentTestService(t, hooks)
	owner := contentTestUser(t, repo, models.RoleUser)
	sub := draftIntegrityPack(t, s, bucket, owner.ID)

	var submitErr error
	hooks.onSign = func() { _, submitErr = s.SubmitForReview(context.Background(), sub.ID, owner.ID) }
	up, err := s.GetUploadURL(context.Background(), sub.ID, owner.ID, models.UploadRequest{Kind: models.KindTheme, FileName: "theme.json", MimeType: "application/json", Size: 2})
	if submitErr != nil {
		t.Fatalf("submit: %v", submitErr)
	}
	if !isKind(err, apperr.KindConflict) || up != nil {
		t.Fatalf("upload request after the submit: %v %+v", err, up)
	}
	if keys := presignedKeys(fileKeys(t, repo, sub.ID)); len(keys) != 0 {
		t.Fatalf("pending submission holds presigned keys %v", keys)
	}
}

// An upload registered while a submit is copying files makes the submit
// refuse (nothing changes, no copies left behind); the retry freezes it.
func TestSubmitRacingAnUploadIsRefused(t *testing.T) {
	bucket := r2test.NewMemory()
	hooks := &hookBucket{Memory: bucket}
	s, repo := contentTestService(t, hooks)
	owner := contentTestUser(t, repo, models.RoleUser)
	sub := draftIntegrityPack(t, s, bucket, owner.ID)
	up, err := s.GetUploadURL(context.Background(), sub.ID, owner.ID, models.UploadRequest{Kind: models.KindBackground, FileName: "gba.png", MimeType: "image/png", Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	bucket.Put(up.ObjectKey, "image/png", []byte("gba"))
	before := len(bucket.Keys())

	var late *models.UploadResponse
	hooks.onCopy = func() {
		late, err = s.GetUploadURL(context.Background(), sub.ID, owner.ID, models.UploadRequest{Kind: models.KindTheme, FileName: "theme.json", MimeType: "application/json", Size: 2})
	}
	if _, serr := s.SubmitForReview(context.Background(), sub.ID, owner.ID); !isKind(serr, apperr.KindConflict) {
		t.Fatalf("submit racing an upload: %v", serr)
	}
	if err != nil || late == nil {
		t.Fatalf("the racing upload request: %v", err)
	}
	if got, _ := repo.GetSubmission(t.Context(), sub.ID); got.Status != models.StatusCreated {
		t.Fatalf("status after a refused submit: %q", got.Status)
	}
	if after := len(bucket.Keys()); after != before {
		t.Fatalf("objects: %d before, %d after the refused submit", before, after)
	}

	bucket.Put(late.ObjectKey, "application/json", []byte(`{}`))
	if _, err := s.SubmitForReview(context.Background(), sub.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	if keys := presignedKeys(fileKeys(t, repo, sub.ID)); len(keys) != 0 {
		t.Fatalf("pending submission holds presigned keys %v", keys)
	}
}

// A draft whose only file never arrived has no files to submit.
func TestSubmitWithOnlyAMissingUploadIsRefused(t *testing.T) {
	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	sub := draftIntegrityPack(t, s, bucket, owner.ID)
	for _, k := range fileKeys(t, repo, sub.ID) {
		if err := s.RemoveSubmissionFile(context.Background(), sub.ID, owner.ID, k); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.GetUploadURL(context.Background(), sub.ID, owner.ID, models.UploadRequest{Kind: models.KindBackground, FileName: "gba.png", MimeType: "image/png", Size: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitForReview(context.Background(), sub.ID, owner.ID); !isKind(err, apperr.KindInvalid) {
		t.Fatalf("submit with only a missing upload: %v", err)
	}
	if got, _ := repo.GetSubmission(t.Context(), sub.ID); got.Status != models.StatusCreated {
		t.Fatalf("status %q", got.Status)
	}
}

// A missing upload removed while a submit is copying the others doesn't count
// twice: the submit still goes ahead with the file that arrived.
func TestSubmitCountsARemovedMissingUploadOnce(t *testing.T) {
	bucket := r2test.NewMemory()
	hooks := &hookBucket{Memory: bucket}
	s, repo := contentTestService(t, hooks)
	owner := contentTestUser(t, repo, models.RoleUser)
	sub := draftIntegrityPack(t, s, bucket, owner.ID)
	for _, k := range fileKeys(t, repo, sub.ID) {
		if err := s.RemoveSubmissionFile(context.Background(), sub.ID, owner.ID, k); err != nil {
			t.Fatal(err)
		}
	}
	gone, err := s.GetUploadURL(context.Background(), sub.ID, owner.ID, models.UploadRequest{Kind: models.KindBackground, FileName: "gba.png", MimeType: "image/png", Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	arrived, err := s.GetUploadURL(context.Background(), sub.ID, owner.ID, models.UploadRequest{Kind: models.KindBackground, FileName: "snes.png", MimeType: "image/png", Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	bucket.Put(arrived.ObjectKey, "image/png", []byte("snes"))
	hooks.onCopy = func() {
		if err := s.RemoveSubmissionFile(context.Background(), sub.ID, owner.ID, gone.ObjectKey); err != nil {
			t.Error(err)
		}
	}
	if _, err := s.SubmitForReview(context.Background(), sub.ID, owner.ID); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if keys := fileKeys(t, repo, sub.ID); len(keys) != 1 || reviewKeyRegex.MatchString(keys[0]) {
		t.Fatalf("files after submit: %v", keys)
	}
}
