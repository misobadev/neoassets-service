package handlers

import (
	"net/http"
	"strings"
	"time"

	"neoassets/pkg/auth"
)

// cookieSecure reports whether the request reached us over HTTPS. TLS terminates
// at the edge (Cloudflare/Traefik), so X-Forwarded-Proto is checked too. Over
// plain http://localhost the cookie is still set (browsers treat localhost as a
// secure context), which keeps local development working.
func cookieSecure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// setSessionCookie stores a JWT in an httpOnly, SameSite=Lax cookie so the token
// is never exposed to JavaScript.
func setSessionCookie(w http.ResponseWriter, r *http.Request, name, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie expires a session cookie.
func clearSessionCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// Logout clears the browser session cookies. It is intentionally public: it only
// expires cookies and never reveals whether a session existed.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w, r, auth.UserCookie)
	clearSessionCookie(w, r, auth.AdminCookie)
	writeJSON(w, http.StatusOK, map[string]string{"message": "logged out"})
}
