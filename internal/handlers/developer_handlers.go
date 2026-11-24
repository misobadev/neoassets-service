package handlers

import (
	"database/sql"
	"errors"
	"neoassets/internal/apperr"
	"neoassets/internal/httpx"
	"net/http"

	"neoassets/internal/models"
)

// ---------------------------------------------------------------------------
// Developer applications (user JWT)
// ---------------------------------------------------------------------------

// flushScrapeOutcomes writes the batched scrape counters before a page that
// shows them, so the usage shown is never up to one batch behind.
func (h *Handler) flushScrapeOutcomes(r *http.Request) {
	if h.scrapeSvc != nil {
		h.scrapeSvc.FlushOutcomes(r.Context())
	}
}

// ListDeveloperApps returns the current user's developer applications.
func (h *Handler) ListDeveloperApps(w http.ResponseWriter, r *http.Request) {
	h.flushScrapeOutcomes(r)
	list, err := h.devSvc.ListApps(r.Context(), userFromRequest(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list developer apps")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"apps": list})
}

// CreateDeveloperApp registers a developer application. The response includes
// the plaintext client secret, shown only once.
func (h *Handler) CreateDeveloperApp(w http.ResponseWriter, r *http.Request) {
	var req models.CreateDeveloperAppRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	app, err := h.devSvc.CreateApp(r.Context(), userFromRequest(r), req)
	if err != nil {
		writeClientError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, app)
}

// RevokeDeveloperApp revokes one of the current user's applications.
func (h *Handler) RevokeDeveloperApp(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathUUID(w, r, "id", "invalid app id")
	if !ok {
		return
	}
	if err := h.devSvc.RevokeApp(r.Context(), userFromRequest(r), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "developer app not found")
			return
		}
		writeClientError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

// RotateDeveloperApp issues a new client secret for an application. The
// response includes the plaintext secret, shown only once.
func (h *Handler) RotateDeveloperApp(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathUUID(w, r, "id", "invalid app id")
	if !ok {
		return
	}
	app, err := h.devSvc.RotateApp(r.Context(), userFromRequest(r), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "developer app not found")
			return
		}
		writeClientError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

// ---------------------------------------------------------------------------
// Personal API keys (user JWT)
// ---------------------------------------------------------------------------

// ListAPIKeys returns the current user's personal API keys.
func (h *Handler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	list, err := h.devSvc.ListAPIKeys(r.Context(), userFromRequest(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list API keys")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"keys": list})
}

// CreateAPIKey issues a personal API key. The response includes the plaintext
// key, shown only once.
func (h *Handler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req models.CreateAPIKeyRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	key, err := h.devSvc.CreateAPIKey(r.Context(), userFromRequest(r), req)
	if err != nil {
		writeClientError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, key)
}

// RevealAPIKey returns the full plaintext of one of the current user's active
// API keys, so the client can copy it.
func (h *Handler) RevealAPIKey(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathUUID(w, r, "id", "invalid key id")
	if !ok {
		return
	}
	key, err := h.devSvc.RevealAPIKey(r.Context(), userFromRequest(r), id)
	if err != nil {
		switch apperr.KindOf(err) {
		case apperr.KindNotFound:
			writeError(w, http.StatusNotFound, err.Error())
		case apperr.KindConflict:
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "failed to read API key")
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"key": key})
}

// RevokeAPIKey revokes one of the current user's personal API keys.
func (h *Handler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathUUID(w, r, "id", "invalid key id")
	if !ok {
		return
	}
	if err := h.devSvc.RevokeAPIKey(r.Context(), userFromRequest(r), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "API key not found")
			return
		}
		writeClientError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

// AdminDeveloperOverview lists every developer app and personal API key with
// their usage. Admin only.
func (h *Handler) AdminDeveloperOverview(w http.ResponseWriter, r *http.Request) {
	h.flushScrapeOutcomes(r)
	out, err := h.devSvc.Overview(r.Context())
	if err != nil {
		writeServerError(w, http.StatusInternalServerError, "failed to load developer overview", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
