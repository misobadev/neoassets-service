package services

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"neoassets/internal/models"
	"neoassets/internal/repository"
	"neoassets/internal/testdb"
	"neoassets/pkg/r2"
)

// storeR2 is an in-memory bucket that records the Content-Type of every
// signed upload and every server-side write, so a test can assert which type
// an object would be served with. Unimplemented methods panic via the nil
// embedded interface.
type storeR2 struct {
	r2.Client
	objects map[string][]byte
	types   map[string]string
	signed  []string
	copies  []string
	deletes []string
}

func newStoreR2() *storeR2 {
	return &storeR2{objects: map[string][]byte{}, types: map[string]string{}}
}

func (f *storeR2) GenerateSignedUploadURL(_ context.Context, key, contentType string, _ time.Duration) (string, error) {
	f.signed = append(f.signed, contentType)
	return "https://upload.test/" + key, nil
}

func (f *storeR2) DownloadObject(_ context.Context, key string, maxSize int64) ([]byte, error) {
	data, ok := f.objects[key]
	if !ok {
		return nil, fmt.Errorf("NoSuchKey: %s", key)
	}
	if int64(len(data)) > maxSize {
		return nil, r2.ErrObjectTooLarge
	}
	return data, nil
}

func (f *storeR2) UploadFile(_ context.Context, key, contentType string, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.objects[key] = data
	f.types[key] = contentType
	return nil
}

func (f *storeR2) CopyObject(_ context.Context, src, dst string) error {
	f.copies = append(f.copies, src)
	f.objects[dst] = f.objects[src]
	f.types[dst] = f.types[src]
	return nil
}

func (f *storeR2) DeleteObject(_ context.Context, key string) error {
	f.deletes = append(f.deletes, key)
	delete(f.objects, key)
	delete(f.types, key)
	return nil
}

func (f *storeR2) ObjectSize(_ context.Context, key string) (int64, bool, error) {
	data, ok := f.objects[key]
	return int64(len(data)), ok, nil
}

func TestUploadContentTypeAllowList(t *testing.T) {
	cases := []struct {
		kind, declared, want string
		ok                   bool
	}{
		{models.KindBackground, "image/png", "image/png", true},
		{models.KindBackground, "image/jpeg", "image/jpeg", true},
		{models.KindPreview, "image/webp", "image/webp", true},
		{models.KindLogo, "image/gif", "image/gif", true},
		{models.KindBackground, "", "application/octet-stream", true},
		{models.KindBackground, "application/octet-stream", "application/octet-stream", true},
		{models.KindTheme, "application/json", "application/json", true},
		{models.KindTheme, "", "application/octet-stream", true},
		{models.MediaVideo, "video/mp4", "video/mp4", true},
		{models.MediaVideo, "video/x-matroska", "video/x-matroska", true},
		{models.MediaCover, "image/webp", "image/webp", true},

		{models.KindBackground, "text/html", "", false},
		{models.KindBackground, "image/svg+xml", "", false},
		{models.KindBackground, "image/png; charset=utf-8", "", false},
		{models.KindBackground, "IMAGE/PNG", "", false},
		{models.KindBackground, "video/mp4", "", false},
		{models.KindTheme, "text/html", "", false},
		{models.KindTheme, "image/png", "", false},
		{models.KindTheme, "text/plain", "", false},
		{models.MediaVideo, "text/html", "", false},
		{models.MediaVideo, "image/png", "", false},
		{models.MediaVideo, "video/", "", false},
		{models.MediaCover, "application/xhtml+xml", "", false},
	}
	for _, c := range cases {
		got, err := uploadContentType(c.kind, c.declared)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("uploadContentType(%q, %q) = %q, %v; want %q", c.kind, c.declared, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("uploadContentType(%q, %q) = %q; want an error", c.kind, c.declared, got)
		}
	}
}

func TestSubmissionUploadURLPreSignsOnlyAllowedTypes(t *testing.T) {
	bucket := newStoreR2()
	s := &Service{r2: bucket, imageExts: NewService(nil, nil, "", "", nil).imageExts, uploadTTL: time.Minute}
	req := models.SubmissionUploadURLRequest{Name: "My Pack", Kind: models.KindBackground, FileName: "snes.png", Size: 10}

	req.MimeType = "text/html"
	if _, err := s.SubmissionUploadURLPre(context.Background(), req); err == nil {
		t.Fatal("a text/html upload was signed")
	}
	if len(bucket.signed) != 0 {
		t.Fatalf("signed %v for a refused upload", bucket.signed)
	}

	req.MimeType = "image/png"
	if _, err := s.SubmissionUploadURLPre(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(bucket.signed) != 1 || bucket.signed[0] != "image/png" {
		t.Fatalf("signed %v, want [image/png]", bucket.signed)
	}
}

func TestThemeSizeIsCapped(t *testing.T) {
	s := NewService(nil, nil, "", "", nil)
	req := models.UploadRequest{Kind: models.KindTheme, FileName: "theme.json", MimeType: "application/json", Size: maxThemeSize + 1}
	if err := s.ValidateUploadRequest(req); err == nil {
		t.Fatal("an oversized theme was accepted at presign")
	}
	bucket := newStoreR2()
	bucket.objects["big"] = make([]byte, maxThemeSize+1)
	bucket.objects["ok"] = []byte(`{}`)
	s.r2 = bucket
	if err := s.ensureUploaded(context.Background(), "big", "theme.json"); err == nil {
		t.Fatal("an oversized uploaded theme was accepted")
	}
	if err := s.ensureUploaded(context.Background(), "ok", "theme.json"); err != nil {
		t.Fatal(err)
	}
	if got := maxUploadSize(models.KindTheme, "theme.json"); got != maxThemeSize {
		t.Fatalf("maxUploadSize(theme) = %d, want %d", got, maxThemeSize)
	}
}

func TestNormalizeDonationURL(t *testing.T) {
	ok := map[string]string{
		"":                             "",
		"https://ko-fi.com/someone":    "https://ko-fi.com/someone",
		"  https://ko-fi.com/someone ": "https://ko-fi.com/someone",
		"HTTPS://Ko-fi.com/someone":    "https://Ko-fi.com/someone",
		"http://ko-fi.com/someone":     "https://ko-fi.com/someone",
		"ko-fi.com/someone":            "https://ko-fi.com/someone",
		"www.patreon.com/someone":      "https://www.patreon.com/someone",
	}
	for in, want := range ok {
		got, err := normalizeDonationURL(in)
		if err != nil || got != want {
			t.Errorf("normalizeDonationURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"javascript:void(0)",
		"data:text/plain,hello",
		"mailto:someone@example.com",
		"ftp://example.com/x",
		"https://",
		"https://user@example.com/",
		"someone",
		"https://localhost/donate",
		"https://" + strings.Repeat("a", 512) + ".com",
	} {
		if got, err := normalizeDonationURL(in); err == nil {
			t.Errorf("normalizeDonationURL(%q) = %q; want an error", in, got)
		}
	}
}

// contentTestService returns a Service over a migrated test database (see
// testdb) and the given bucket.
func contentTestService(t *testing.T, bucket r2.Client) (*Service, *repository.Repository) {
	t.Helper()
	repo := repository.NewRepository(testdb.Open(t, "services_upload"))
	return NewService(repo, bucket, "https://cdn.test", "test-secret", nil), repo
}

func contentTestUser(t *testing.T, repo *repository.Repository, role string) *models.User {
	t.Helper()
	u, err := repo.CreateUser(t.Context(), &models.User{
		Username:      "u" + uuid.NewString()[:8],
		Email:         uuid.NewString()[:8] + "@example.com",
		PasswordHash:  "hash",
		EmailVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if role != models.RoleUser {
		if _, err := repo.SetUserRole(t.Context(), u.ID, role); err != nil {
			t.Fatal(err)
		}
	}
	return u
}

// approvedPack stores an approved pack owned by owner and returns its id.
func approvedPack(t *testing.T, repo *repository.Repository, owner, reviewer uuid.UUID, donationURL string) string {
	t.Helper()
	packID := "pack-" + uuid.NewString()[:8]
	id, err := repo.CreateSubmission(t.Context(), packID, "Original Name", "Original Author", "first", donationURL, owner.String(), false, &owner, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetSubmissionStatus(t.Context(), id, models.StatusApproved, reviewer, "1.0"); err != nil {
		t.Fatal(err)
	}
	return packID
}

func pendingSubmissionWithFile(t *testing.T, repo *repository.Repository, owner uuid.UUID, kind, fileName, declaredType string, data []byte, bucket *storeR2) (string, uuid.UUID) {
	t.Helper()
	packID := "pack-" + uuid.NewString()[:8]
	id, err := repo.CreateSubmission(t.Context(), packID, "Pack "+packID, "Author", "", "", owner.String(), false, &owner, false)
	if err != nil {
		t.Fatal(err)
	}
	key := reviewObjectKey(packID, fileName)
	bucket.objects[key] = data
	bucket.types[key] = declaredType
	if err := repo.AddFile(t.Context(), id, key, fileName, strings.TrimSuffix(fileName, getExt(fileName)), kind, declaredType, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	return packID, id
}

func TestApprovedThemeIsPublishedAsJSON(t *testing.T) {
	bucket := newStoreR2()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)

	// Registered under an old client with a type the allow-list now refuses.
	packID, id := pendingSubmissionWithFile(t, repo, owner.ID, models.KindTheme, "theme.json", "text/html", []byte(`{"name":"x"}`), bucket)
	if _, err := s.Approve(context.Background(), id, reviewer.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	canonical := "packs/" + packID + "/theme.json"
	if got := bucket.types[canonical]; got != "application/json" {
		t.Fatalf("theme published as %q, want application/json", got)
	}
	if len(bucket.copies) != 0 {
		t.Fatalf("theme was copied with the uploader's metadata: %v", bucket.copies)
	}
	files, err := repo.ListFiles(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].MimeType != "application/json" || files[0].ObjectKey != canonical {
		t.Fatalf("file row = %+v", files)
	}
}

func TestApproveRefusesAThemeThatIsNotJSON(t *testing.T) {
	bucket := newStoreR2()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)

	packID, id := pendingSubmissionWithFile(t, repo, owner.ID, models.KindTheme, "theme.json", "application/json", []byte(`<p>not json</p>`), bucket)
	if _, err := s.Approve(context.Background(), id, reviewer.ID, "", ""); err == nil {
		t.Fatal("a theme that is not JSON was approved")
	}
	if _, ok := bucket.objects["packs/"+packID+"/theme.json"]; ok {
		t.Fatal("an invalid theme was published")
	}
}

func TestApprovedGIFIsPublishedAsGIF(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	bucket := newStoreR2()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)

	img := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	packID, id := pendingSubmissionWithFile(t, repo, owner.ID, models.KindBackground, "snes.gif", "application/octet-stream", buf.Bytes(), bucket)
	if _, err := s.Approve(context.Background(), id, reviewer.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	canonical := s.ObjectKey(packID, models.KindBackground, "snes.gif")
	if got := bucket.types[canonical]; got != "image/gif" {
		t.Fatalf("GIF published as %q, want image/gif", got)
	}
	if len(bucket.copies) != 0 {
		t.Fatalf("GIF was copied with the uploader's metadata: %v", bucket.copies)
	}
}

func TestContributionDraftKeepsThePacksIdentity(t *testing.T) {
	s, repo := contentTestService(t, newStoreR2())
	owner := contentTestUser(t, repo, models.RoleUser)
	contributor := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	packID := approvedPack(t, repo, owner.ID, reviewer.ID, "https://ko-fi.com/owner")

	sub, err := s.CreateSubmission(context.Background(), models.CreateSubmissionRequest{
		Name: "Original Name", Author: "Original Author", Contribution: true, PackID: packID,
	}, contributor.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Submissions are created pending; a contributor edits one after a rejection.
	if _, err := repo.SetSubmissionStatus(t.Context(), sub.ID, models.StatusRejected, reviewer.ID, ""); err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpdateSubmission(context.Background(), sub.ID, contributor.ID, models.CreateSubmissionRequest{
		Name: "Renamed", Author: "Someone Else", Description: "new art", DonationURL: "https://example.com/other", AI: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Original Name" || updated.Author != "Original Author" ||
		updated.DonationURL != "https://ko-fi.com/owner" || updated.AI {
		t.Fatalf("contribution changed the pack's identity: %+v", updated)
	}
	if updated.Description != "new art" {
		t.Fatalf("description = %q, want the contribution's", updated.Description)
	}
}

func TestApprovedContributionKeepsThePacksIdentityAndOwner(t *testing.T) {
	s, repo := contentTestService(t, newStoreR2())
	owner := contentTestUser(t, repo, models.RoleUser)
	contributor := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	packID := approvedPack(t, repo, owner.ID, reviewer.ID, "https://ko-fi.com/owner")

	// A contribution draft saved before this fix can still carry other values.
	id, err := repo.CreateSubmission(t.Context(), packID, "Renamed", "Someone Else", "new art", "https://example.com/other", contributor.ID.String(), true, &contributor.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(context.Background(), id, reviewer.ID, "", ""); err != nil {
		t.Fatal(err)
	}

	detail, err := s.GetPackDetail(t.Context(), packID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Name != "Original Name" || detail.Author != "Original Author" || detail.DonationURL != "https://ko-fi.com/owner" {
		t.Fatalf("approved contribution changed the pack's identity: %+v", detail.Pack)
	}
	if detail.Description != "new art" {
		t.Fatalf("description = %q, want the contribution's", detail.Description)
	}
	if detail.SubmittedBy != owner.Username {
		t.Fatalf("submitted_by = %q, want the pack's owner %q", detail.SubmittedBy, owner.Username)
	}
	packs, _, err := s.ListedApprovedPacks(t.Context(), "created", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range packs {
		if p.PackID == packID {
			found = true
			if p.Name != "Original Name" || p.SubmittedBy != owner.Username {
				t.Fatalf("listing shows %q by %q", p.Name, p.SubmittedBy)
			}
		}
	}
	if !found {
		t.Fatal("pack missing from the listing")
	}
}

func TestNewPacksNeedAnHTTPSDonationURL(t *testing.T) {
	s, repo := contentTestService(t, newStoreR2())
	owner := contentTestUser(t, repo, models.RoleUser)

	_, err := s.CreateSubmission(context.Background(), models.CreateSubmissionRequest{
		Name: "Pack " + uuid.NewString()[:8], Author: "A", DonationURL: "data:text/plain,hello",
	}, owner.ID)
	if err == nil {
		t.Fatal("a non-web donation URL was accepted")
	}
	sub, err := s.CreateSubmission(context.Background(), models.CreateSubmissionRequest{
		Name: "Pack " + uuid.NewString()[:8], Author: "A", DonationURL: "ko-fi.com/someone",
	}, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sub.DonationURL != "https://ko-fi.com/someone" {
		t.Fatalf("donation_url stored as %q", sub.DonationURL)
	}
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	if _, err := repo.SetSubmissionStatus(t.Context(), sub.ID, models.StatusRejected, reviewer.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateSubmission(context.Background(), sub.ID, owner.ID, models.CreateSubmissionRequest{
		Name: sub.Name, Author: "A", DonationURL: "mailto:someone@example.com",
	}); err == nil {
		t.Fatal("a non-web donation URL was accepted on update")
	}
}

func TestPackResponsesDropUnsafeStoredDonationURLs(t *testing.T) {
	s, repo := contentTestService(t, newStoreR2())
	owner := contentTestUser(t, repo, models.RoleUser)
	contributor := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	unsafe := approvedPack(t, repo, owner.ID, reviewer.ID, "data:text/plain,hello")
	legacy := approvedPack(t, repo, owner.ID, reviewer.ID, "ko-fi.com/someone")

	detail, err := s.GetPackDetail(t.Context(), unsafe)
	if err != nil {
		t.Fatal(err)
	}
	if detail.DonationURL != "" {
		t.Fatalf("detail serves donation_url %q", detail.DonationURL)
	}
	detail, err = s.GetPackDetail(t.Context(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	if detail.DonationURL != "https://ko-fi.com/someone" {
		t.Fatalf("legacy donation_url served as %q", detail.DonationURL)
	}
	packs, _, err := s.ListedApprovedPacks(t.Context(), "created", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range packs {
		if p.PackID == unsafe {
			found = true
			if p.DonationURL != "" {
				t.Fatalf("listing serves donation_url %q", p.DonationURL)
			}
		}
	}
	if !found {
		t.Fatal("pack missing from the listing")
	}

	// A contribution to a pack with a stored value that no longer validates
	// still works: the pack's value is copied, not re-validated.
	if _, err := s.CreateSubmission(context.Background(), models.CreateSubmissionRequest{
		Name: "x", Author: "y", Contribution: true, PackID: unsafe,
	}, contributor.ID); err != nil {
		t.Fatal(err)
	}
}

func TestEveryUploadPathRefusesPageTypes(t *testing.T) {
	bucket := newStoreR2()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)
	ctx := context.Background()

	systemID := "sys" + uuid.NewString()[:8]
	if _, err := testdb.Open(t, "services_upload").Exec(`INSERT INTO metadata_systems (id, name) VALUES ($1, $1)`, systemID); err != nil {
		t.Fatal(err)
	}
	metaID, err := repo.CreateMetadataSubmission(ctx, nil, &systemID, owner.ID, "edit", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	packID := "pack-" + uuid.NewString()[:8]
	packSub, err := repo.CreateSubmission(ctx, packID, "Pack "+packID, "A", "", "", owner.ID.String(), false, &owner.ID, false)
	if err != nil {
		t.Fatal(err)
	}

	presign := map[string]func(mimeType string) error{
		"pack upload": func(m string) error {
			_, err := s.GetUploadURL(ctx, packSub, owner.ID, models.UploadRequest{Kind: models.KindBackground, FileName: "snes.png", MimeType: m, Size: 10})
			return err
		},
		"metadata upload before a submission": func(m string) error {
			_, err := s.MetadataUploadURLPre(ctx, owner.ID, models.MetadataUploadURLRequest{SystemID: &systemID, Kind: models.MediaFanart, FileName: "fanart.png", MimeType: m, Size: 10})
			return err
		},
		"metadata upload": func(m string) error {
			_, err := s.MetadataUploadURL(ctx, metaID, owner.ID, models.MetadataUploadRequest{Kind: models.MediaFanart, FileName: "fanart.png", MimeType: m, Size: 10})
			return err
		},
	}
	for name, sign := range presign {
		before := len(bucket.signed)
		if err := sign("text/html"); err == nil {
			t.Errorf("%s: a text/html upload was signed", name)
		}
		if err := sign("image/png"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if got := bucket.signed[before:]; len(got) != 1 || got[0] != "image/png" {
			t.Errorf("%s: signed %v, want [image/png]", name, got)
		}
	}

	// Registering an uploaded file checks its declared type too.
	if _, err := repo.SetSubmissionStatus(ctx, packSub, models.StatusRejected, reviewer.ID, ""); err != nil {
		t.Fatal(err)
	}
	key := reviewObjectKey(packID, "nes.png")
	bucket.objects[key] = []byte("x")
	file := models.SubmissionFileInput{Kind: models.KindBackground, ObjectKey: key, FileName: "nes.png", SystemID: "nes", MimeType: "text/html", Size: 1}
	if _, err := s.AddSubmissionFiles(ctx, packSub, owner.ID, []models.SubmissionFileInput{file}); err == nil {
		t.Fatal("a file declared as text/html was registered")
	}
	file.MimeType = "image/png"
	if _, err := s.AddSubmissionFiles(ctx, packSub, owner.ID, []models.SubmissionFileInput{file}); err != nil {
		t.Fatal(err)
	}
}

// A theme that can't be published fails the approval before any other file of
// the submission is published.
func TestApproveChecksThemesBeforePublishingAnything(t *testing.T) {
	bucket := newStoreR2()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)

	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	packID, id := pendingSubmissionWithFile(t, repo, owner.ID, models.KindBackground, "snes.png", "image/png", buf.Bytes(), bucket)
	themeKey := reviewObjectKey(packID, "theme.json")
	bucket.objects[themeKey] = []byte(`not json`)
	time.Sleep(10 * time.Millisecond) // files publish in created_at order
	if err := repo.AddFile(t.Context(), id, themeKey, "theme.json", "", models.KindTheme, "application/json", 8); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Approve(context.Background(), id, reviewer.ID, "", ""); err == nil {
		t.Fatal("a submission with a theme that is not JSON was approved")
	}
	for key := range bucket.objects {
		if strings.HasPrefix(key, "packs/"+packID+"/") && !isReviewObjectKey(key) {
			t.Fatalf("%s was published by a failed approval", key)
		}
	}
}

func TestCreateSubmissionRefusesPageTypesBeforeSaving(t *testing.T) {
	bucket := newStoreR2()
	s, repo := contentTestService(t, bucket)
	owner := contentTestUser(t, repo, models.RoleUser)

	name := "Pack " + uuid.NewString()[:8]
	packID := s.PackIDFromName(name)
	key := reviewObjectKey(packID, "snes.png")
	bucket.objects[key] = []byte("x")
	_, err := s.CreateSubmission(context.Background(), models.CreateSubmissionRequest{
		Name: name, Author: "A",
		Files: []models.SubmissionFileInput{{Kind: models.KindBackground, ObjectKey: key, FileName: "snes.png", SystemID: "snes", MimeType: "text/html", Size: 1}},
	}, owner.ID)
	if err == nil {
		t.Fatal("a file declared as text/html was registered")
	}
	if inUse, err := repo.PackIDInUse(t.Context(), packID); err != nil || inUse {
		t.Fatalf("a refused submission was saved (in use %v, %v)", inUse, err)
	}
}

// A pack whose only approved rows are contributions (possible in existing
// data) is listed under the latest revision's user, as before.
func TestSubmittedByFallsBackWithoutThePacksOwnSubmission(t *testing.T) {
	s, repo := contentTestService(t, newStoreR2())
	contributor := contentTestUser(t, repo, models.RoleUser)
	reviewer := contentTestUser(t, repo, models.RoleReviewer)

	packID := "pack-" + uuid.NewString()[:8]
	id, err := repo.CreateSubmission(t.Context(), packID, "Name", "Author", "", "", contributor.ID.String(), false, &contributor.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetSubmissionStatus(t.Context(), id, models.StatusApproved, reviewer.ID, "1.0"); err != nil {
		t.Fatal(err)
	}
	detail, err := s.GetPackDetail(t.Context(), packID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.SubmittedBy != contributor.Username {
		t.Fatalf("submitted_by = %q, want %q", detail.SubmittedBy, contributor.Username)
	}
}
