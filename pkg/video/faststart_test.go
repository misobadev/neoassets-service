package video

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// box builds an MP4 box with a 32-bit size and the given payload length.
func box(typ string, payload int) []byte {
	b := make([]byte, 8+payload)
	binary.BigEndian.PutUint32(b, uint32(8+payload))
	copy(b[4:], typ)
	return b
}

// largeBox builds a box that uses the 64-bit size form.
func largeBox(typ string, payload int) []byte {
	b := make([]byte, 16+payload)
	binary.BigEndian.PutUint32(b, 1)
	copy(b[4:], typ)
	binary.BigEndian.PutUint64(b[8:], uint64(16+payload))
	return b
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestMoovFirstBytes(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		want    bool
		wantErr bool
	}{
		{"faststart", concat(box("ftyp", 12), box("moov", 40), box("mdat", 100)), true, false},
		{"moov at end", concat(box("ftyp", 12), box("mdat", 100), box("moov", 40)), false, false},
		{"free before moov", concat(box("ftyp", 12), box("free", 8), box("moov", 40), box("mdat", 4)), true, false},
		{"large mdat first", concat(box("ftyp", 12), largeBox("mdat", 100), box("moov", 40)), false, false},
		{"large free then moov", concat(box("ftyp", 12), largeBox("free", 20), box("moov", 40)), true, false},
		{"no boxes", []byte("not an mp4 at all, just text"), false, true},
		{"truncated", box("ftyp", 12)[:10], false, true},
		{"zero size box", concat(box("ftyp", 12), []byte{0, 0, 0, 0, 'u', 'u', 'i', 'd'}), false, true},
	}
	for _, c := range cases {
		got, err := MoovFirstBytes(c.data)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.name, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("%s: MoovFirstBytes = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMoovFirstStopsOnCorruptSize(t *testing.T) {
	data := concat(box("ftyp", 12), []byte{0, 0, 0, 4, 'f', 'r', 'e', 'e'}, box("moov", 8))
	if _, err := MoovFirstBytes(data); err == nil || errors.Is(err, ErrNotMP4) {
		t.Fatalf("err = %v, want an invalid size error", err)
	}
}

func hasEncoder(name string) bool {
	out, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
	return err == nil && strings.Contains(string(out), " "+name+" ")
}

// sampleVideo encodes a short clip with the moov box at the end (ffmpeg's
// default without faststart).
func sampleVideo(t *testing.T, vcodec string, extra ...string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sample.mp4")
	args := []string{"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=64x48:rate=10:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:v", vcodec}
	args = append(args, extra...)
	args = append(args, "-c:a", "aac", "-shortest", path)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("build sample video: %v: %s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func videoTag(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe.mp4")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return probeVideoTag(context.Background(), path)
}

func TestRemuxFaststart(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	vcodec, extra, wantTag := "mpeg4", []string(nil), "mp4v"
	if hasEncoder("libx265") {
		vcodec, extra, wantTag = "libx265", []string{"-tag:v", "hvc1", "-x265-params", "log-level=none"}, "hvc1"
	}
	in := sampleVideo(t, vcodec, extra...)
	if first, err := MoovFirstBytes(in); err != nil || first {
		t.Fatalf("sample MoovFirst = %v, %v; want moov at the end", first, err)
	}

	ctx := context.Background()
	out, err := RemuxFaststart(ctx, in)
	if err != nil {
		t.Fatalf("RemuxFaststart: %v", err)
	}
	if first, err := MoovFirstBytes(out); err != nil || !first {
		t.Fatalf("remuxed MoovFirst = %v, %v; want moov first", first, err)
	}
	if tag := videoTag(t, out); tag != wantTag {
		t.Errorf("remuxed video tag = %q, want %q", tag, wantTag)
	}
	before, err := Probe(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Probe(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if before.Codec != after.Codec || before.Width != after.Width || before.Height != after.Height {
		t.Errorf("remux changed the video: %+v -> %+v", before, after)
	}
}

func TestConvertToMP4IsFaststart(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if !hasEncoder("libx265") {
		t.Skip("ffmpeg has no libx265 encoder")
	}
	in := sampleVideo(t, "mpeg4")
	out, err := ConvertToMP4(context.Background(), in)
	if err != nil {
		t.Fatalf("ConvertToMP4: %v", err)
	}
	if first, err := MoovFirstBytes(out); err != nil || !first {
		t.Fatalf("converted MoovFirst = %v, %v; want moov first", first, err)
	}
}
