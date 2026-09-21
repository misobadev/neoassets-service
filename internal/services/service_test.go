package services

import (
	"testing"

	"neoassets/internal/models"
	"neoassets/internal/systems"
)

func TestValidateUploadRequestSystemID(t *testing.T) {
	cat, err := systems.Load()
	if err != nil {
		t.Fatalf("failed to load catalog: %v", err)
	}
	svc := NewService(nil, nil, "", "", cat)

	valid := []models.UploadRequest{
		{FileName: "gba.webp", Kind: models.KindBackground, Size: 1, MimeType: "image/webp"},
		{FileName: "arc.gif", Kind: models.KindBackground, Size: 1, MimeType: "image/gif"},
		// The backend normalizes images on approval, so any common raster
		// format is accepted from the client.
		{FileName: "gba.png", Kind: models.KindBackground, Size: 1, MimeType: "image/png"},
		{FileName: "gba.jpg", Kind: models.KindBackground, Size: 1, MimeType: "image/jpeg"},
		{FileName: "nes.webp", Kind: models.KindLogo, Size: 1, MimeType: "image/webp"},
		{FileName: "preview.webp", Kind: models.KindPreview, Size: 1, MimeType: "image/webp"},
		{FileName: "preview.png", Kind: models.KindPreview, Size: 1, MimeType: "image/png"},
		{FileName: "theme.json", Kind: models.KindTheme, Size: 1, MimeType: "application/json"},
		// Hack/homebrew system ids contain a hyphen.
		{FileName: "nes-hacks.webp", Kind: models.KindBackground, Size: 1, MimeType: "image/webp"},
		{FileName: "snes-hacks.png", Kind: models.KindBackground, Size: 1, MimeType: "image/png"},
		{FileName: "gb-hacks.gif", Kind: models.KindBackground, Size: 1, MimeType: "image/gif"},
	}
	for _, req := range valid {
		if err := svc.ValidateUploadRequest(req); err != nil {
			t.Errorf("expected valid, got error for %q: %v", req.FileName, err)
		}
	}

	invalid := []models.UploadRequest{
		// Background: unknown/unapproved system id or unsupported extension.
		{FileName: "nonexistent.webp", Kind: models.KindBackground, Size: 1, MimeType: "image/webp"},
		{FileName: "evil;rm.webp", Kind: models.KindBackground, Size: 1, MimeType: "image/webp"},
		{FileName: "gba.bmp", Kind: models.KindBackground, Size: 1, MimeType: "image/bmp"},
		// Theme: must be json.
		{FileName: "theme.json", Kind: models.KindBackground, Size: 1, MimeType: "application/json"},
		{FileName: "theme.txt", Kind: models.KindTheme, Size: 1, MimeType: "text/plain"},
	}
	for _, req := range invalid {
		if err := svc.ValidateUploadRequest(req); err == nil {
			t.Errorf("expected error for %s %q", req.Kind, req.FileName)
		}
	}
}

func TestValidatePackObjectKey(t *testing.T) {
	cat, err := systems.Load()
	if err != nil {
		t.Fatalf("failed to load catalog: %v", err)
	}
	svc := NewService(nil, nil, "", "", cat)
	packID := "mypack"

	ok := []models.SubmissionFileInput{
		// Own staging upload.
		{ObjectKey: "packs/mypack/review/abcd1234/preview.webp", FileName: "preview.webp", Kind: models.KindPreview},
		// Own canonical key (as presigned by the upload-url endpoint).
		{ObjectKey: "packs/mypack/preview.webp", FileName: "preview.webp", Kind: models.KindPreview},
		{ObjectKey: "packs/mypack/backgrounds/gba.webp", FileName: "gba.webp", Kind: models.KindBackground},
	}
	for _, f := range ok {
		if err := svc.validatePackObjectKey(packID, f); err != nil {
			t.Errorf("expected %q to be accepted: %v", f.ObjectKey, err)
		}
	}

	bad := []models.SubmissionFileInput{
		// Another pack's objects.
		{ObjectKey: "packs/other/preview.webp", FileName: "preview.webp", Kind: models.KindPreview},
		{ObjectKey: "packs/other/review/abcd1234/preview.webp", FileName: "preview.webp", Kind: models.KindPreview},
		// A canonical key that does not match this (kind, file name).
		{ObjectKey: "packs/mypack/backgrounds/snes.webp", FileName: "gba.webp", Kind: models.KindBackground},
		// Media catalog objects.
		{ObjectKey: "media/games/snes/whatever.webp", FileName: "preview.webp", Kind: models.KindPreview},
	}
	for _, f := range bad {
		if err := svc.validatePackObjectKey(packID, f); err == nil {
			t.Errorf("expected %q to be rejected", f.ObjectKey)
		}
	}
}

func TestObjectKeyNormalizesImagesToWebp(t *testing.T) {
	svc := NewService(nil, nil, "", "", nil)

	cases := []struct {
		kind     string
		fileName string
		want     string
	}{
		{models.KindBackground, "gba.png", "packs/mypack/backgrounds/gba.webp"},
		{models.KindBackground, "gba.jpg", "packs/mypack/backgrounds/gba.webp"},
		{models.KindBackground, "gba.webp", "packs/mypack/backgrounds/gba.webp"},
		// Animated GIFs keep their extension.
		{models.KindBackground, "gba.gif", "packs/mypack/backgrounds/gba.gif"},
		{models.KindBackground, "nes-hacks.png", "packs/mypack/backgrounds/nes-hacks.webp"},
		{models.KindLogo, "nes.png", "packs/mypack/logos/nes.webp"},
		{models.KindPreview, "preview.png", "packs/mypack/preview.webp"},
		{models.KindTheme, "theme.json", "packs/mypack/theme.json"},
	}
	for _, c := range cases {
		if got := svc.ObjectKey("mypack", c.kind, c.fileName); got != c.want {
			t.Errorf("ObjectKey(%s, %s) = %q, want %q", c.kind, c.fileName, got, c.want)
		}
	}
}
