package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

// Sealer encrypts and decrypts small secrets at rest with AES-256-GCM. The key
// is derived from a server secret with a purpose label, so the same secret can
// back several independent keys.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer derives an AES-256 key from secret and purpose.
func NewSealer(secret, purpose string) (*Sealer, error) {
	if secret == "" {
		return nil, errors.New("sealer secret is empty")
	}
	key := sha256.Sum256([]byte(purpose + "\x00" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plaintext and returns base64(nonce || ciphertext).
func (s *Sealer) Seal(plaintext string) (string, error) {
	nonce, err := randomBytes(s.aead.NonceSize())
	if err != nil {
		return "", err
	}
	out := s.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawStdEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal.
func (s *Sealer) Open(sealed string) (string, error) {
	raw, err := base64.RawStdEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("decode sealed value: %w", err)
	}
	n := s.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("sealed value too short")
	}
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", fmt.Errorf("open sealed value: %w", err)
	}
	return string(plain), nil
}
