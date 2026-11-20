package services

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
)

func TestResolveSoftwareNameCutsOnACharacter(t *testing.T) {
	name := "a" + strings.Repeat("é", 100) // 201 bytes: byte 120 is mid-character
	got := resolveSoftwareName(name, "fallback")
	if !utf8.ValidString(got) {
		t.Fatalf("cut produced invalid UTF-8: %q", got)
	}
	if len(got) > maxSoftwareNameBytes {
		t.Fatalf("got %d bytes, want at most %d", len(got), maxSoftwareNameBytes)
	}
	if got := resolveSoftwareName("  ", "fallback"); got != "fallback" {
		t.Fatalf("empty name: got %q", got)
	}
}

// Refused requests are counted in memory and written in one batch, with a cap
// on how many distinct software names one app can hold between writes.
func TestRefusalsAreBatched(t *testing.T) {
	var b refusalBatch
	app := uuid.New()
	for i := 0; i < 1000; i++ {
		b.add(app, fmt.Sprintf("name-%d", i%200), "My App")
	}
	b.add(app, "My App", "My App")
	got := b.drain()
	var total int64
	names := map[string]bool{}
	for _, r := range got {
		if r.app != app {
			t.Fatalf("unexpected app %v", r.app)
		}
		total += r.count
		names[r.software] = true
	}
	if total != 1001 {
		t.Fatalf("batched %d refusals, want 1001", total)
	}
	if len(names) > maxPendingSoftwarePerApp+1 {
		t.Fatalf("%d distinct names pending for one app, want at most %d", len(names), maxPendingSoftwarePerApp+1)
	}
	if len(b.drain()) != 0 {
		t.Fatal("drain did not empty the batch")
	}
}

// A rate-limited request is only counted; nothing is written until the batch
// is flushed (the service here has no repository, so a write would panic).
func TestRateLimitedRequestsAreNotWrittenOneByOne(t *testing.T) {
	s := &ScrapeService{}
	app := uuid.New()
	for i := 0; i < 3; i++ {
		s.record(app, "v1", "My App", "rate_limited")
	}
	got := s.refused.drain()
	if len(got) != 1 || got[0].count != 3 {
		t.Fatalf("batch = %+v, want one entry counting 3", got)
	}
}
