package auth

import "testing"

func TestSealerRoundTrip(t *testing.T) {
	s, err := NewSealer("a-long-enough-test-secret-value-123", "api-key")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal("ns_live_abc")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Open(sealed)
	if err != nil || got != "ns_live_abc" {
		t.Fatalf("Open = %q, %v", got, err)
	}

	other, _ := NewSealer("a-long-enough-test-secret-value-123", "other")
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("a different purpose must not open the value")
	}
	if _, err := s.Open("not-base64!"); err == nil {
		t.Fatal("garbage must not open")
	}
}

func TestMaskAPIKey(t *testing.T) {
	if got := MaskAPIKey("ns_live_huWlxLabcdefghijlcOv2V"); got != "ns_live_huWlxL...lcOv2V" {
		t.Fatalf("MaskAPIKey = %q", got)
	}
	if got := MaskAPIKey("ns_live_short"); got != "ns_live_..." {
		t.Fatalf("short key = %q", got)
	}
}
