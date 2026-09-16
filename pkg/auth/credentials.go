package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// APIKeyPrefix is the human-readable prefix of generated API keys, so a leaked
// key is recognizable and can be searched for in secret scanners.
const APIKeyPrefix = "ns_live_"

// APIKeyPrefixLen is the number of leading characters stored as the lookup
// prefix of an API key.
const APIKeyPrefixLen = len(APIKeyPrefix) + 8

// tokenPrefix is kept as a short alias used internally.
const tokenPrefix = APIKeyPrefix

// randomBytes returns n cryptographically secure random bytes.
func randomBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("failed to read random bytes: %w", err)
	}
	return buf, nil
}

// GenerateClientID returns a public developer application identifier.
func GenerateClientID() (string, error) {
	buf, err := randomBytes(8)
	if err != nil {
		return "", err
	}
	return "nsapp_" + hex.EncodeToString(buf), nil
}

// GenerateSecret returns a high-entropy developer client secret.
func GenerateSecret() (string, error) {
	buf, err := randomBytes(32)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// GenerateDebugPassword returns a short developer debug token. It only enables
// the debug query parameters (force counters/limits), so it is stored in
// plaintext and shown to the owning user.
func GenerateDebugPassword() (string, error) {
	buf, err := randomBytes(12)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// GenerateAPIKey returns a new personal API key: its lookup prefix, the
// plaintext value (shown once), and the SHA-256 hash stored at rest.
func GenerateAPIKey() (prefix, plaintext, hash string, err error) {
	buf, err := randomBytes(32)
	if err != nil {
		return "", "", "", err
	}
	plaintext = tokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	prefix = plaintext[:APIKeyPrefixLen]
	hash = HashToken(plaintext)
	return prefix, plaintext, hash, nil
}

// HashToken returns the hex-encoded SHA-256 of a token. Tokens are high-entropy
// random values, so a fast hash is safe (unlike passwords, which use bcrypt).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CheckTokenHash compares a plaintext token against a stored hash in constant
// time.
func CheckTokenHash(token, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashToken(token)), []byte(hash)) == 1
}
