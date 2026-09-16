package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/pkg/auth"
)

// maxSearchOffset bounds deep scans on the public catalog endpoints.
const maxSearchOffset = 50000

func parseLimitOffset(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	if offset > maxSearchOffset {
		offset = maxSearchOffset
	}
	return limit, offset
}

// parseUserListParams reads optional paging params for the user's own lists.
// Unlike parseLimitOffset, a missing or invalid limit means no limit (the caller
// gets everything), so callers that do not page keep their current behavior.
func parseUserListParams(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 0 || limit > 1000 {
		limit = 0
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// ListMetadataSystems returns the metadata system catalog.
func (h *Handler) ListMetadataSystems(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListMetadataSystems()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list systems")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"systems": list})
}

// ListGamesBySystem returns the games for a system.
func (h *Handler) ListGamesBySystem(w http.ResponseWriter, r *http.Request) {
	systemID := chi.URLParam(r, "id")
	limit, offset := parseLimitOffset(r)
	gtype := r.URL.Query().Get("type")
	sort := r.URL.Query().Get("sort")
	list, total, err := h.svc.ListGamesBySystem(systemID, limit, offset, gtype, sort)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list games")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"games": list, "total": total})
}

// SearchGames searches the game catalog by name or system.
func (h *Handler) SearchGames(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if r := []rune(q); len(r) > 200 {
		q = string(r[:200])
	}
	systemID := r.URL.Query().Get("system_id")
	gtype := r.URL.Query().Get("type")
	sort := r.URL.Query().Get("sort")
	limit, offset := parseLimitOffset(r)
	list, total, err := h.svc.SearchGames(q, systemID, gtype, sort, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to search games")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"games": list, "total": total})
}

// ListMetadataLanguages returns the supported description languages.
func (h *Handler) ListMetadataLanguages(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListLanguages()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list languages")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"languages": list})
}

// GetGameDetail returns a game bundled with ROMs and media. An optional ?lang=
// query param resolves the description to that language's translation
// (falling back to English when no translation exists).
func (h *Handler) GetGameDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid game id")
		return
	}
	lang := r.URL.Query().Get("lang")
	detail, err := h.svc.GetGame(id, lang)
	if err != nil {
		writeServerError(w, http.StatusNotFound, "game not found", err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// LookupGame finds games by ROM hash.
func (h *Handler) LookupGame(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := h.svc.LookupGame(q.Get("crc"), q.Get("md5"), q.Get("sha1"), q.Get("sha256"), q.Get("system_id"))
	if err != nil {
		writeServerError(w, http.StatusBadRequest, "failed to look up game", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"games": list})
}

// GetMetadataPending returns the fields/media kinds that already have a pending
// submission for the current user on a game (so the UI blocks re-submitting).
func (h *Handler) GetMetadataPending(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid game id")
		return
	}
	keys, err := h.svc.PendingMetadataKeys(userFromRequest(r), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load pending submissions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"keys": keys})
}

// ---------------------------------------------------------------------------
// Metadata contributions (user JWT)
// ---------------------------------------------------------------------------

// CreateMetadataSubmission creates a draft contribution.
func (h *Handler) CreateMetadataSubmission(w http.ResponseWriter, r *http.Request) {
	var req models.MetadataSubmissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	sub, err := h.svc.CreateMetadataSubmission(userFromRequest(r), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sub)
}

// MetadataUploadURL returns a presigned URL to upload a media file.
func (h *Handler) MetadataUploadURL(w http.ResponseWriter, r *http.Request) {
	submissionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}
	var req models.MetadataUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	resp, err := h.svc.MetadataUploadURL(r.Context(), submissionID, userFromRequest(r), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// MetadataUploadURLPre presigns an upload to the canonical location before a
// submission row exists (used when drafts stay client-side until review).
func (h *Handler) MetadataUploadURLPre(w http.ResponseWriter, r *http.Request) {
	var req models.MetadataUploadURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	resp, err := h.svc.MetadataUploadURLPre(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// SubmitMetadataSubmission moves a draft contribution into review.
func (h *Handler) SubmitMetadataSubmission(w http.ResponseWriter, r *http.Request) {
	submissionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}
	sub, err := h.svc.SubmitMetadataSubmission(submissionID, userFromRequest(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

// ListMyMetadataSubmissions lists the user's contributions.
func (h *Handler) ListMyMetadataSubmissions(w http.ResponseWriter, r *http.Request) {
	limit, offset := parseUserListParams(r)
	status := r.URL.Query().Get("status")
	list, total, totalXP, err := h.svc.ListMyMetadataSubmissions(userFromRequest(r), status, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list submissions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"submissions": list, "total": total, "total_xp": totalXP})
}

// ---------------------------------------------------------------------------
// Metadata admin review (admin JWT)
// ---------------------------------------------------------------------------

// ListMetadataSubmissions lists all contributions.
func (h *Handler) ListMetadataSubmissions(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	list, err := h.svc.ListMetadataSubmissions(status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list submissions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"submissions": list})
}

// GetMetadataSubmission returns a contribution with its files and the current
// target values so reviewers can compare before approving.
func (h *Handler) GetMetadataSubmission(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}
	detail, err := h.svc.GetMetadataSubmissionDetail(id)
	if err != nil {
		writeServerError(w, http.StatusNotFound, "submission not found", err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// ApproveMetadataSubmission approves a contribution and applies it.
func (h *Handler) ApproveMetadataSubmission(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}
	var req models.MediaReviewRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	adminID := actorIDFromContext(r.Context())
	sub, err := h.svc.ApproveMetadataSubmission(id, adminID, req.Comment)
	if err != nil {
		writeServerError(w, http.StatusBadRequest, "failed to approve submission", err)
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

// RejectMetadataSubmission rejects a contribution and deletes its files.
func (h *Handler) RejectMetadataSubmission(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}
	var req models.MediaReviewRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	adminID := actorIDFromContext(r.Context())
	sub, err := h.svc.RejectMetadataSubmission(r.Context(), id, adminID, req.Comment)
	if err != nil {
		writeServerError(w, http.StatusBadRequest, "failed to reject submission", err)
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

// RetranslateGame regenerates a game's translations from its English description.
func (h *Handler) RetranslateGame(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid game id")
		return
	}
	n, err := h.svc.RetranslateGame(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"translations": n})
}

// RetranslateSystem regenerates a system's translations from its description.
func (h *Handler) RetranslateSystem(w http.ResponseWriter, r *http.Request) {
	n, err := h.svc.RetranslateSystem(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"translations": n})
}

func actorIDFromContext(ctx context.Context) uuid.UUID {
	if claims, ok := auth.AdminFromContext(ctx); ok && claims != nil {
		if id, err := uuid.Parse(claims.AdminID.String()); err == nil {
			return id
		}
	}
	if claims, ok := auth.UserFromContext(ctx); ok && claims != nil {
		return claims.UserID
	}
	return uuid.Nil
}
