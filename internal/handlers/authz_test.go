package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"neoassets/pkg/auth"
)

// A password change or reset bumps the account's token version; every token
// issued before it, admin or user, must then count as revoked.
func TestTokenRevokedCoversAdminAndUserTokens(t *testing.T) {
	id := uuid.New()
	admin := func(ver int) bool {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r = r.WithContext(auth.WithAdmin(r.Context(), &auth.AdminClaims{AdminID: id, TokenVersion: ver}))
		return tokenRevoked(r, 2)
	}
	user := func(ver int) bool {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r = r.WithContext(auth.WithUser(r.Context(), &auth.UserClaims{UserID: id, TokenVersion: ver}))
		return tokenRevoked(r, 2)
	}

	if !admin(1) {
		t.Error("an admin token from before the password change is still accepted")
	}
	if admin(2) {
		t.Error("a current admin token was rejected")
	}
	if !user(1) {
		t.Error("a user token from before the password change is still accepted")
	}
	if user(2) {
		t.Error("a current user token was rejected")
	}
}
