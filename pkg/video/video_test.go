package video

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestImageScaleFilter(t *testing.T) {
	cases := []struct {
		spec ImageSpec
		want string
	}{
		{ImageSpec{}, ""},
		{ImageSpec{MaxSize: 1024}, "scale=w='min(1024,iw)':h='min(1024,ih)':force_original_aspect_ratio=decrease"},
		{ImageSpec{TargetW: 1920, TargetH: 1080}, "scale=1920:1080:force_original_aspect_ratio=increase,crop=1920:1080"},
		// Target wins over MaxSize.
		{ImageSpec{TargetW: 1024, TargetH: 1024, MaxSize: 512}, "scale=1024:1024:force_original_aspect_ratio=increase,crop=1024:1024"},
	}
	for _, c := range cases {
		if got := imageScaleFilter(c.spec); got != c.want {
			t.Errorf("imageScaleFilter(%+v) = %q, want %q", c.spec, got, c.want)
		}
	}
}

// hasWebpEncoder reports whether the local ffmpeg can encode WebP. Some
// development builds ship without libwebp, so the encoding test is skipped
// there.
func hasWebpEncoder() bool {
	out, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "libwebp ")
}

func TestNormalizeImage(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if !hasWebpEncoder() {
		t.Skip("ffmpeg has no libwebp encoder")
	}

	// Build a 200x100 test image as input.
	in, err := exec.Command("ffmpeg", "-y",
		"-f", "lavfi", "-i", "color=c=red:s=200x100",
		"-frames:v", "1",
		"-f", "image2pipe", "-vcodec", "png", "pipe:1",
	).Output()
	if err != nil {
		t.Fatalf("failed to build test image: %v", err)
	}

	ctx := context.Background()
	out, err := NormalizeImage(ctx, in, ImageSpec{TargetW: 64, TargetH: 64, Quality: 90})
	if err != nil {
		t.Fatalf("NormalizeImage: %v", err)
	}
	info, err := ProbeImage(ctx, out)
	if err != nil {
		t.Fatalf("ProbeImage: %v", err)
	}
	if info.Codec != "webp" {
		t.Errorf("codec = %q, want webp", info.Codec)
	}
	if info.Width != 64 || info.Height != 64 {
		t.Errorf("size = %dx%d, want 64x64", info.Width, info.Height)
	}

	// A max-size spec only downscales the longest side.
	out, err = NormalizeImage(ctx, in, ImageSpec{MaxSize: 50, Quality: 90})
	if err != nil {
		t.Fatalf("NormalizeImage max: %v", err)
	}
	info, err = ProbeImage(ctx, out)
	if err != nil {
		t.Fatalf("ProbeImage max: %v", err)
	}
	if info.Width != 50 || info.Height != 25 {
		t.Errorf("max size = %dx%d, want 50x25", info.Width, info.Height)
	}
}
