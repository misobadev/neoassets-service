package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/pkg/r2"
)

// sizedR2 serves object sizes for HEAD checks and records every call, so a
// test can assert what was (and wasn't) fetched. Unimplemented methods panic
// via the nil embedded interface.
type sizedR2 struct {
	r2.Client
	sizes     map[string]int64
	heads     []string
	downloads []string
	maxSizes  []int64
}

func (f *sizedR2) ObjectSize(_ context.Context, key string) (int64, bool, error) {
	f.heads = append(f.heads, key)
	size, ok := f.sizes[key]
	return size, ok, nil
}

func (f *sizedR2) DownloadObject(_ context.Context, key string, maxSize int64) ([]byte, error) {
	f.downloads = append(f.downloads, key)
	f.maxSizes = append(f.maxSizes, maxSize)
	return nil, nil
}

func TestEnsureUploadedCapsVideos(t *testing.T) {
	bucket := &sizedR2{sizes: map[string]int64{"ok": maxVideoSize, "big": maxVideoSize + 1}}
	svc := NewService(nil, bucket, "", "", nil)

	if err := svc.ensureUploaded(context.Background(), "ok", "clip.mp4"); err != nil {
		t.Fatalf("a video at the cap was refused: %v", err)
	}
	if err := svc.ensureUploaded(context.Background(), "big", "clip.mp4"); err == nil {
		t.Fatal("a video over the cap was accepted")
	}
}

func TestMaxUploadSize(t *testing.T) {
	cases := []struct {
		kind, file string
		want       int64
	}{
		{models.MediaVideo, "clip.mp4", maxVideoSize},
		{models.MediaVideo, "clip.webm", maxVideoSize},
		{models.MediaCover, "cover.gif", maxGIFSize},
		{models.MediaCover, "cover.png", maxImageSize},
		{models.KindBackground, "nes.webp", maxImageSize},
	}
	for _, c := range cases {
		if got := maxUploadSize(c.kind, c.file); got != c.want {
			t.Errorf("%s %s: got %d, want %d", c.kind, c.file, got, c.want)
		}
	}
}

// Video entries are downloaded and probed when a submission is created, so
// they must be checked before that: only the submitter's own upload, a video
// extension, one per submission, and a size within the cap — all without
// downloading anything.
func TestCheckVideoEntries(t *testing.T) {
	user := uuid.New()
	own := "media/staging/" + user.String() + "/" + uuid.NewString() + "/video/clip.mp4"
	big := "media/staging/" + user.String() + "/" + uuid.NewString() + "/video/big.mp4"
	bucket := &sizedR2{sizes: map[string]int64{own: 10 << 20, big: maxVideoSize + 1, "packs/p1/theme.json": 100}}
	svc := NewService(nil, bucket, "", "", nil)
	video := func(key, name string) models.MetadataUploadRequest {
		return models.MetadataUploadRequest{Kind: models.MediaVideo, ObjectKey: key, FileName: name}
	}

	cases := []struct {
		name    string
		files   []models.MetadataUploadRequest
		wantErr string
	}{
		{"own upload", []models.MetadataUploadRequest{video(own, "clip.mp4")}, ""},
		{"another object", []models.MetadataUploadRequest{video("packs/p1/theme.json", "clip.mp4")}, "invalid upload reference"},
		{"another user's upload", []models.MetadataUploadRequest{video("media/staging/"+uuid.NewString()+"/x/video/a.mp4", "a.mp4")}, "invalid upload reference"},
		{"over the cap", []models.MetadataUploadRequest{video(big, "big.mp4")}, "too large"},
		{"not a video file", []models.MetadataUploadRequest{video(own, "clip.png")}, "extension"},
		{"two videos", []models.MetadataUploadRequest{video(own, "clip.mp4"), video(own, "clip.mp4")}, "one video"},
	}
	for _, c := range cases {
		bucket.heads, bucket.downloads = nil, nil
		err := svc.checkVideoEntries(context.Background(), user, c.files)
		if c.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: got %v, want an error containing %q", c.name, err, c.wantErr)
		}
		if len(bucket.downloads) != 0 {
			t.Errorf("%s: downloaded %v", c.name, bucket.downloads)
		}
		if c.wantErr == "invalid upload reference" && len(bucket.heads) != 0 {
			t.Errorf("%s: looked up a key it doesn't own: %v", c.name, bucket.heads)
		}
	}
}

func TestProbeVideoDownloadsWithinTheCap(t *testing.T) {
	bucket := &sizedR2{}
	svc := NewService(nil, bucket, "", "", nil)
	_, _ = svc.probeVideo(context.Background(), "media/staging/u/s/video/clip.mp4")
	if len(bucket.maxSizes) != 1 || bucket.maxSizes[0] != maxVideoSize {
		t.Fatalf("download limit: got %v, want [%d]", bucket.maxSizes, maxVideoSize)
	}
}

// A probe that can't get a slot in time reports that the server is busy rather
// than asking the user to upload the video again.
func TestAcquireVideoSlotReportsBusy(t *testing.T) {
	slots := make(chan struct{}, 1)
	release, err := acquireVideoSlot(context.Background(), slots)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := acquireVideoSlot(ctx, slots); !errors.Is(err, errVideoBusy) {
		t.Fatalf("got %v, want errVideoBusy", err)
	}
}
