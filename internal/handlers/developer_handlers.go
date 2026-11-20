package handlers

import (
	"database/sql"
	"errors"
	"neoassets/internal/httpx"
	"net/http"

	"neoassets/internal/models"
)

// ---------------------------------------------------------------------------
// Developer applications (user JWT)
// ---------------------------------------------------------------------------

// ListDeveloperApps returns the current user's developer applications.
func (h *Handler) ListDeveloperApps(w http.ResponseWriter, r *http.Request) {
	list, err := h.devSvc.ListApps(userFromRequest(r))
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
	app, err := h.devSvc.CreateApp(userFromRequest(r), req)
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
	if err := h.devSvc.RevokeApp(userFromRequest(r), id); err != nil {
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
	app, err := h.devSvc.RotateApp(userFromRequest(r), id)
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
	list, err := h.devSvc.ListAPIKeys(userFromRequest(r))
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
	key, err := h.devSvc.CreateAPIKey(userFromRequest(r), req)
	if err != nil {
		writeClientError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, key)
}

// RevokeAPIKey revokes one of the current user's personal API keys.
func (h *Handler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathUUID(w, r, "id", "invalid key id")
	if !ok {
		return
	}
	if err := h.devSvc.RevokeAPIKey(userFromRequest(r), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "API key not found")
			return
		}
		writeClientError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}
