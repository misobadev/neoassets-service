package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"neoassets/pkg/r2"
	"neoassets/pkg/r2/r2test"
	"neoassets/pkg/video"
)

// encode builds a 1 s test clip, with the moov box first when faststart.
func encode(t *testing.T, faststart bool) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clip.mp4")
	args := []string{"-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=64x48:rate=10:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:v", "mpeg4", "-c:a", "aac", "-shortest"}
	if faststart {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, path)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("encode clip: %v: %s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func listMP4(t *testing.T, bucket *r2test.Memory) []r2.ObjectInfo {
	t.Helper()
	objs, err := bucket.ListKeysWithSize(context.Background(), "media/games/")
	if err != nil {
		t.Fatal(err)
	}
	var out []r2.ObjectInfo
	for _, o := range objs {
		if filepath.Ext(o.Key) == ".mp4" {
			out = append(out, o)
		}
	}
	return out
}

func TestDryRunThenApply(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	ctx := context.Background()
	fast, slow := encode(t, true), encode(t, false)
	corrupt := append(append([]byte(nil), slow[:64]...), bytes.Repeat([]byte{0}, 32)...)

	bucket := r2test.NewMemory()
	bucket.Put("media/games/snes/fast.mp4", "video/mp4", fast)
	bucket.Put("media/games/snes/slow.mp4", "video/mp4", slow)
	bucket.Put("media/games/psx/slow2.mp4", "video/mp4", slow)
	bucket.Put("media/games/psx/broken.mp4", "video/mp4", corrupt)

	collect := func(opts options, limit int) (map[outcome]int, map[string]outcome) {
		byKey := map[string]outcome{}
		counts := run(ctx, bucket, listMP4(t, bucket), opts, 2, limit, func(r result) {
			byKey[r.Key] = r.Outcome
		})
		return counts, byKey
	}

	// Dry run: reports, writes nothing. The truncated file still has valid
	// headers (mdat before moov), so it is listed as needing a remux.
	counts, byKey := collect(options{MaxSize: 1 << 30}, 0)
	if counts[outcomeOK] != 1 || counts[outcomeNeeds] != 3 {
		t.Fatalf("dry run counts = %v", counts)
	}
	if byKey["media/games/snes/fast.mp4"] != outcomeOK || byKey["media/games/snes/slow.mp4"] != outcomeNeeds {
		t.Fatalf("dry run outcomes = %v", byKey)
	}
	if got, _, _ := bucket.Get("media/games/snes/slow.mp4"); !bytes.Equal(got, slow) {
		t.Fatal("dry run modified an object")
	}

	// Too large to remux: left alone.
	counts, _ = collect(options{Apply: true, MaxSize: 10}, 0)
	if counts[outcomeTooLarge] != 3 {
		t.Fatalf("max-size counts = %v", counts)
	}

	// Limit: stops once one object needed a remux.
	counts, _ = collect(options{MaxSize: 1 << 30}, 1)
	if counts[outcomeNeeds] < 1 || counts[outcomeNeeds]+counts[outcomeOK]+counts[outcomeFailed] > 4 {
		t.Fatalf("limit counts = %v", counts)
	}

	// Apply: the two slow files are remuxed, the broken one is untouched.
	counts, _ = collect(options{Apply: true, MaxSize: 1 << 30}, 0)
	if counts[outcomeRemuxed] != 2 || counts[outcomeOK] != 1 || counts[outcomeFailed] != 1 {
		t.Fatalf("apply counts = %v", counts)
	}
	got, typ, _ := bucket.Get("media/games/psx/slow2.mp4")
	if first, err := video.MoovFirstBytes(got); err != nil || !first {
		t.Fatalf("remuxed object MoovFirst = %v, %v", first, err)
	}
	if typ != "video/mp4" {
		t.Errorf("content type = %q, want video/mp4", typ)
	}
	if b, _, _ := bucket.Get("media/games/psx/broken.mp4"); !bytes.Equal(b, corrupt) {
		t.Error("a failed object was overwritten")
	}
	if b, _, _ := bucket.Get("media/games/snes/fast.mp4"); !bytes.Equal(b, fast) {
		t.Error("an already faststart object was rewritten")
	}

	// Idempotent: a second pass has nothing left to do.
	counts, _ = collect(options{Apply: true, MaxSize: 1 << 30}, 0)
	if counts[outcomeOK] != 3 || counts[outcomeRemuxed] != 0 || counts[outcomeFailed] != 1 {
		t.Fatalf("second pass counts = %v", counts)
	}
}

func TestVerifyRemuxRejectsDifferentVideo(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	slow := encode(t, false)
	// A "remux" whose moov box is still last must be refused.
	if err := verifyRemux(context.Background(), slow, slow); err == nil {
		t.Error("verifyRemux accepted a file whose moov box is still last")
	}
	fast := encode(t, true)
	if err := verifyRemux(context.Background(), slow, fast); err != nil {
		t.Errorf("verifyRemux rejected an equivalent faststart file: %v", err)
	}
}
