package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	app, err := h.devSvc.CreateApp(userFromRequest(r), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, app)
}

// RevokeDeveloperApp revokes one of the current user's applications.
func (h *Handler) RevokeDeveloperApp(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid app id")
		return
	}
	if err := h.devSvc.RevokeApp(userFromRequest(r), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "developer app not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

// RotateDeveloperApp issues a new client secret for an application. The
// response includes the plaintext secret, shown only once.
func (h *Handler) RotateDeveloperApp(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid app id")
		return
	}
	app, err := h.devSvc.RotateApp(userFromRequest(r), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "developer app not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	key, err := h.devSvc.CreateAPIKey(userFromRequest(r), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, key)
}

// RevokeAPIKey revokes one of the current user's personal API keys.
func (h *Handler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key id")
		return
	}
	if err := h.devSvc.RevokeAPIKey(userFromRequest(r), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "API key not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}
