package services

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/internal/testdb"
	"neoassets/pkg/r2/r2test"
)

// A metadata submission whose "video" is a DASH manifest is refused when it is
// created, and the server never fetches the URL in it.
func TestCreateRefusesAManifestVideoWithoutFetching(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	if out, err := exec.Command("ffprobe", "-hide_banner", "-demuxers").Output(); err != nil || !strings.Contains(string(out), " dash ") {
		t.Skip("this ffmpeg has no DASH demuxer")
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	bucket := r2test.NewMemory()
	s, repo := contentTestService(t, bucket)
	u := contentTestUser(t, repo, models.RoleUser)
	systemID := "sys" + uuid.NewString()[:8]
	if _, err := testdb.Open(t, "services_upload").Exec(`INSERT INTO metadata_systems (id, name) VALUES ($1, $1)`, systemID); err != nil {
		t.Fatal(err)
	}
	mpd := fmt.Sprintf(`<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" profiles="urn:mpeg:dash:profile:isoff-on-demand:2011" type="static" mediaPresentationDuration="PT10S">
<Period><AdaptationSet mimeType="video/mp4"><Representation id="1" bandwidth="1000" width="320" height="240"><BaseURL>%s/internal</BaseURL></Representation></AdaptationSet></Period></MPD>`, srv.URL)
	staged := stagingMediaKey(u.ID, models.MediaVideo, "clip.mp4")
	bucket.Put(staged, "video/mp4", []byte(mpd))

	_, err := s.CreateMetadataSubmission(context.Background(), u.ID, models.MetadataSubmissionRequest{
		SystemID: &systemID, Payload: map[string]any{"description": "a video"},
		Files: []models.MetadataUploadRequest{{Kind: models.MediaVideo, ObjectKey: staged, FileName: "clip.mp4", MimeType: "video/mp4", Size: int64(len(mpd))}},
	})
	// The probe itself refuses it (not an earlier check that never reaches it).
	if err == nil || !strings.Contains(err.Error(), "could not read the video") {
		t.Errorf("a DASH manifest as a video: %v, want the probe's refusal", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("creating the submission fetched the manifest's URL %d times", n)
	}
}
