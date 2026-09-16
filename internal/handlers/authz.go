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
		role, err := h.userSvc.UserRole(id)
		if err != nil || role != models.RoleAdmin {
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
		role, err := h.userSvc.UserRole(id)
		if err != nil {
			writeError(w, http.StatusForbidden, "forbidden")
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
		verified, err := h.userSvc.UserEmailVerified(id)
		if err != nil {
			writeError(w, http.StatusForbidden, "forbidden")
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