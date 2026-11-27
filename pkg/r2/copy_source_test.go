package r2

import "testing"

// The copy source is URL-escaped per path segment, so keys with characters
// that are special in a URL still name the object they are.
func TestCopySourceEscapesEachSegment(t *testing.T) {
	for key, want := range map[string]string{
		"packs/p/review/0a1b2c3d/snes.webp":        "bucket/packs/p/review/0a1b2c3d/snes.webp",
		"packs/p/review/0a1b2c3d/Preview 100%.png": "bucket/packs/p/review/0a1b2c3d/Preview%20100%25.png",
		"media/staging/u/x/cover/#2?.png":          "bucket/media/staging/u/x/cover/%232%3F.png",
		"packs/p/review/0a1b2c3d/c64+.png":         "bucket/packs/p/review/0a1b2c3d/c64%2B.png",
		"packs/p/review/0a1b2c3d/é&=.png":          "bucket/packs/p/review/0a1b2c3d/%C3%A9%26%3D.png",
	} {
		if got := copySource("bucket", key); got != want {
			t.Errorf("copySource(%q) = %q, want %q", key, got, want)
		}
	}
}
