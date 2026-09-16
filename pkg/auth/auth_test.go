package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestTokenTypeConfusionPrevented(t *testing.T) {
	secret := "super-secret-test-value-0123456789abcdef"
	userID := uuid.New()
	adminID := uuid.New()

	userToken, err := GenerateUserToken(secret, userID, "u@example.com", "user", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	adminToken, err := GenerateToken(secret, adminID, "a@example.com", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// A user token must never parse as an admin token...
	if _, err := ParseToken(secret, userToken); err == nil {
		t.Fatal("user token was accepted as an admin token")
	}
	// ...and an admin token must never parse as a user token.
	if _, err := ParseUserToken(secret, adminToken); err == nil {
		t.Fatal("admin token was accepted as a user token")
	}

	// The intended use still works.
	u, err := ParseUserToken(secret, userToken)
	if err != nil || u.UserID != userID {
		t.Fatalf("valid user token rejected: %v", err)
	}
	a, err := ParseToken(secret, adminToken)
	if err != nil || a.AdminID != adminID {
		t.Fatalf("valid admin token rejected: %v", err)
	}
}

func TestWrongSigningMethodRejected(t *testing.T) {
	secret := "super-secret-test-value-0123456789abcdef"
	claims := UserClaims{Type: TokenTypeUser, UserID: uuid.New(), Email: "u@example.com"}
	token := newUnsafeToken(t, claims)
	if _, err := ParseUserToken(secret, token); err == nil {
		t.Fatal("token signed with an unexpected method was accepted")
	}
}

// newUnsafeToken signs with a different algorithm to prove the method check.
func newUnsafeToken(t *testing.T, claims UserClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	s, err := token.SignedString([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}