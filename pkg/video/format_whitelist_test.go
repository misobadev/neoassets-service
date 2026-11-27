package video

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func needFFmpegTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not available")
		}
	}
}

// dashManifest returns an MPD whose segment is served by a local server, and a
// counter of the requests that server gets.
func dashManifest(t *testing.T) ([]byte, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	mpd := fmt.Sprintf(`<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" profiles="urn:mpeg:dash:profile:isoff-on-demand:2011" type="static" mediaPresentationDuration="PT10S">
<Period><AdaptationSet mimeType="video/mp4"><Representation id="1" bandwidth="1000" width="320" height="240"><BaseURL>%s/segment.mp4</BaseURL></Representation></AdaptationSet></Period></MPD>`, srv.URL)
	return []byte(mpd), &hits
}

// An uploaded file that is a DASH manifest is refused by every probe and
// conversion without fetching the URL inside it.
func TestManifestsFetchNothing(t *testing.T) {
	needFFmpegTools(t)
	out, err := exec.Command("ffprobe", "-hide_banner", "-demuxers").Output()
	if err != nil || !strings.Contains(string(out), " dash ") {
		t.Skip("this ffmpeg has no DASH demuxer")
	}
	ctx := context.Background()
	mpd, hits := dashManifest(t)
	calls := map[string]func() error{
		"Probe":      func() error { _, err := Probe(ctx, mpd); return err },
		"ProbeImage": func() error { _, err := ProbeImage(ctx, mpd); return err },
		"NormalizeImage": func() error {
			_, err := NormalizeImage(ctx, mpd, ImageSpec{MaxSize: 64, Quality: 90, FirstFrameOnly: true})
			return err
		},
		"ConvertToMP4":   func() error { _, err := ConvertToMP4(ctx, mpd); return err },
		"RemuxFaststart": func() error { _, err := RemuxFaststart(ctx, mpd); return err },
	}
	for name, call := range calls {
		before := hits.Load()
		err := call()
		if got := hits.Load() - before; got != 0 {
			t.Errorf("%s fetched the manifest's URL %d times", name, got)
		}
		if err == nil {
			t.Errorf("%s accepted a DASH manifest", name)
		}
	}
}

// ffmpegSample writes a short clip with the given encoder arguments and
// returns its bytes, or skips when this ffmpeg can't encode it.
func ffmpegSample(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	full := append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=s=64x64:r=30:d=0.5", "-f", "lavfi", "-i", "sine=d=0.5"}, args...)
	full = append(full, "-shortest", path)
	if err := exec.Command("ffmpeg", full...).Run(); err != nil {
		t.Skipf("this ffmpeg can't write %s: %v", name, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Every accepted video extension's format is still read.
func TestAcceptedVideoFormatsAreRead(t *testing.T) {
	needFFmpegTools(t)
	ctx := context.Background()
	for ext, args := range map[string][]string{
		"webm":    {"-c:v", "libvpx"},
		"mp4":     {"-c:v", "libx264"},
		"mov":     {"-c:v", "mpeg4"},
		"mkv":     {"-c:v", "libx264"},
		"avi":     {"-c:v", "mpeg4"},
		"m4v":     {"-c:v", "libx264", "-f", "mp4"},
		"raw m4v": {"-an", "-c:v", "mpeg4", "-f", "m4v"},
		"mpg":     {"-c:v", "mpeg1video"},
		"raw mpg": {"-an", "-c:v", "mpeg1video", "-f", "mpeg1video"},
		"ts":      {"-c:v", "mpeg2video"},
		"ogv":     {"-c:v", "libtheora"},
		"wmv":     {"-c:v", "wmv2"},
		"flv":     {"-c:v", "flv"},
	} {
		t.Run(ext, func(t *testing.T) {
			meta, err := Probe(ctx, ffmpegSample(t, "clip."+strings.TrimPrefix(ext, "raw "), args...))
			if err != nil || meta.Width != 64 || meta.Height != 64 {
				t.Errorf("Probe: %+v, %v", meta, err)
			}
		})
	}
}

// An nginx-rtmp recording (its metadata names the server where ffmpeg looks for
// it) is read with the live_flv demuxer.
func TestNginxRTMPRecordingsAreRead(t *testing.T) {
	needFFmpegTools(t)
	flv := ffmpegSample(t, "clip.flv", "-c:v", "flv")
	// ffmpeg's live_flv probe looks for "NGINX RTMP" 40 bytes after the 9-byte
	// header (byte 49), where nginx-rtmp's onMetaData has it. Here it
	// overwrites part of ffmpeg's own onMetaData, so ffprobe warns about the
	// metadata; the stream is still read.
	copy(flv[49:], "NGINX RTMP")
	path := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(path, flv, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=format_name", "-of", "csv=p=0", path).Output(); err != nil || strings.TrimSpace(string(out)) != "live_flv" {
		t.Skipf("this ffmpeg doesn't read the sample as live_flv: %q, %v", out, err)
	}
	if meta, err := Probe(context.Background(), flv); err != nil || meta.Width != 64 {
		t.Errorf("Probe: %+v, %v", meta, err)
	}
}

// Every accepted image extension's format is still read and re-encoded.
func TestAcceptedImageFormatsAreRead(t *testing.T) {
	needFFmpegTools(t)
	if !hasWebpEncoder() {
		t.Skip("ffmpeg has no libwebp encoder")
	}
	ctx := context.Background()
	for name, args := range map[string][]string{
		"a.png":  {"-frames:v", "1"},
		"a.apng": {"-plays", "0", "-f", "apng"},
		"a.jpg":  {"-frames:v", "1"},
		"a.webp": {"-frames:v", "1", "-c:v", "libwebp"},
		"a.gif":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			if err := exec.Command("ffmpeg", append(append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", "testsrc=s=32x32:r=10:d=0.3"}, args...), path)...).Run(); err != nil {
				t.Skipf("this ffmpeg can't write %s: %v", name, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if info, err := ProbeImage(ctx, data); err != nil || info.Width != 32 {
				t.Errorf("ProbeImage %+v, %v", info, err)
			}
			if _, err := NormalizeImage(ctx, data, ImageSpec{MaxSize: 16, Quality: 90, FirstFrameOnly: true}); err != nil {
				t.Errorf("NormalizeImage: %v", err)
			}
		})
	}
}
