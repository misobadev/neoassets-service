package handlers

import (
	"net/http"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/pkg/auth"
)

// actorID returns the user id behind a request that passed the admin or user
// middleware. Admin tokens carry an admin_id; user tokens carry a user_id.
func actorID(r *http.Request) (uuid.UUID, bool) {
	if claims, ok := auth.AdminFromContext(r.Context()); ok && claims != nil {
		return claims.AdminID, claims.AdminID != uuid.Nil
	}
	if claims, ok := auth.UserFromContext(r.Context()); ok && claims != nil {
		return claims.UserID, claims.UserID != uuid.Nil
	}
	return uuid.Nil, false
}

// tokenVersion returns the token version carried by the request's admin or
// user token, and whether there was one.
func tokenVersion(r *http.Request) (int, bool) {
	if claims, ok := auth.AdminFromContext(r.Context()); ok && claims != nil {
		return claims.TokenVersion, true
	}
	if claims, ok := auth.UserFromContext(r.Context()); ok && claims != nil {
		return claims.TokenVersion, true
	}
	return 0, false
}

// tokenRevoked reports whether the request's admin or user token was
// invalidated (its version no longer matches the account, e.g. after a
// password change).
func tokenRevoked(r *http.Request, current int) bool {
	v, ok := tokenVersion(r)
	return ok && v != current
}

// RequireAdmin is defense-in-depth on top of auth.Middleware: it re-checks the
// actor's current role from the database so a stale or forged token claim can
// never grant admin access.
func (h *Handler) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := actorID(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		role, _, tokenVersion, err := h.userSvc.UserAuthState(id)
		if err != nil {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		if tokenRevoked(r, tokenVersion) {
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}
		if role != models.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireReviewer is defense-in-depth on top of auth.ReviewMiddleware: it
// re-checks the actor's current role from the database (admin or reviewer), so
// a reviewer that was demoted loses access immediately instead of waiting for
// the token to expire.
func (h *Handler) RequireReviewer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := actorID(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		role, _, tokenVersion, err := h.userSvc.UserAuthState(id)
		if err != nil {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		if tokenRevoked(r, tokenVersion) {
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}
		if role != models.RoleAdmin && role != models.RoleReviewer {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireVerifiedUser blocks unverified accounts from the authenticated user
// routes unless email verification is disabled for local development.
func (h *Handler) RequireVerifiedUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := actorID(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		_, verified, tokenVersion, err := h.userSvc.UserAuthState(id)
		if err != nil {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		if tokenRevoked(r, tokenVersion) {
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}
		if !verified {
			if h.skipEmailVerification {
				next.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusForbidden, "email not verified")
			return
		}
		next.ServeHTTP(w, r)
	})
}
