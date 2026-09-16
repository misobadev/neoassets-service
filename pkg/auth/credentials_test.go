package auth

import (
	"strings"
	"testing"
)

func TestGenerateAPIKey(t *testing.T) {
	prefix, plaintext, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if !strings.HasPrefix(plaintext, APIKeyPrefix) {
		t.Errorf("plaintext %q missing prefix %q", plaintext, APIKeyPrefix)
	}
	if prefix != plaintext[:APIKeyPrefixLen] {
		t.Errorf("prefix %q does not match plaintext[:%d]", prefix, APIKeyPrefixLen)
	}
	if hash == "" || hash == plaintext {
		t.Errorf("hash must be a non-empty digest, got %q", hash)
	}
	if !CheckTokenHash(plaintext, hash) {
		t.Error("CheckTokenHash rejected the correct token")
	}
	if CheckTokenHash(plaintext+"x", hash) {
		t.Error("CheckTokenHash accepted a wrong token")
	}
}

func TestGenerateAPIKeyUnique(t *testing.T) {
	_, a, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	_, b, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if a == b {
		t.Error("two generated keys are identical")
	}
}

func TestHashTokenStable(t *testing.T) {
	if HashToken("abc") != HashToken("abc") {
		t.Error("HashToken is not deterministic")
	}
	if HashToken("abc") == HashToken("abd") {
		t.Error("HashToken collided on distinct input")
	}
}

func TestGenerateClientCredentials(t *testing.T) {
	id, err := GenerateClientID()
	if err != nil {
		t.Fatalf("GenerateClientID: %v", err)
	}
	if !strings.HasPrefix(id, "nsapp_") {
		t.Errorf("client id %q missing nsapp_ prefix", id)
	}
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	if len(secret) < 32 {
		t.Errorf("secret too short: %d chars", len(secret))
	}
}
