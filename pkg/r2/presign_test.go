package r2

import (
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// signedHeaders presigns an upload with the production client (offline: only
// the URL is built) and returns the headers its signature covers.
func signedHeaders(t *testing.T, size int64) ([]string, error) {
	t.Helper()
	t.Setenv("AWS_CONFIG_FILE", os.DevNull)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", os.DevNull)
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_DEFAULT_PROFILE", "")
	c, err := NewClient(t.Context(), "account", "AKIDEXAMPLE", "secret", "bucket", zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.GenerateSignedUploadURL(t.Context(), "packs/p/review/0a1b2c3d/snes.webp", "image/webp", size, time.Minute)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(u.Query().Get("X-Amz-SignedHeaders"), ";"), nil
}

// The signature must cover the Content-Type and the size: an upload with any
// other type or length is then refused by the store, so the type checked at
// presign is the one the object is stored and served with.
func TestSignedUploadURLBindsTheContentTypeAndSize(t *testing.T) {
	headers, err := signedHeaders(t, 1234)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"content-length", "content-type", "host"} {
		if !slices.Contains(headers, want) {
			t.Errorf("signed headers %v lack %q", headers, want)
		}
	}
}

// Without a size the SDK drops the Content-Type from the signature, so a
// presign without one is refused rather than signed loosely.
func TestSignedUploadURLNeedsASize(t *testing.T) {
	for _, size := range []int64{0, -1} {
		if headers, err := signedHeaders(t, size); err == nil {
			t.Errorf("size %d signed with headers %v", size, headers)
		}
	}
}
