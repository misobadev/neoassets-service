package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"neoassets/internal/models"
	"neoassets/internal/services"
	"neoassets/pkg/auth"
)

// Handler wraps the services and exposes HTTP handlers.
type Handler struct {
	svc                   *services.Service
	userSvc               *services.UserService
	scrapeSvc             *services.ScrapeService
	devSvc                *services.DeveloperService
	donationSvc           *services.DonationService
	jwtSecret             string
	skipEmailVerification bool
}

// NewHandler creates a Handler.
func NewHandler(svc *services.Service, userSvc *services.UserService, scrapeSvc *services.ScrapeService, devSvc *services.DeveloperService, donationSvc *services.DonationService, jwtSecret string, skipEmailVerification bool) *Handler {
	return &Handler{svc: svc, userSvc: userSvc, scrapeSvc: scrapeSvc, devSvc: devSvc, donationSvc: donationSvc, jwtSecret: jwtSecret, skipEmailVerification: skipEmailVerification}
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload != nil {
		_ = json.NewEncoder(w).Encode(payload)
	}
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeServerError logs the real error and returns a generic message, so
// internal details (SQL text, S3 keys, schema names) never reach the client.
func writeServerError(w http.ResponseWriter, status int, message string, err error) {
	if err != nil {
		log.Error().Err(err).Msg(message)
	}
	writeError(w, status, message)
}

// HealthCheck returns service liveness.
func (h *Handler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// Public: approved packs list
// ---------------------------------------------------------------------------

// ListPacks returns the list of approved packs (for the web/API consumers),
// sorted and paginated. sort can be "downloads" (default), "name" or "created".
func (h *Handler) ListPacks(w http.ResponseWriter, r *http.Request) {
	sort := r.URL.Query().Get("sort")
	limit, offset := parseLimitOffset(r)
	packs, total, err := h.svc.ListedApprovedPacks(sort, limit, offset)
	if err != nil {
		writeServerError(w, http.StatusInternalServerError, "failed to list packs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"themes": packs, "total": total})
}

// GetPack returns the full public payload of one approved pack (every published
// file with its public URL). This is a pure read and does not bump the download
// counter; use the /download endpoint to register an install.
func (h *Handler) GetPack(w http.ResponseWriter, r *http.Request) {
	packID := chi.URLParam(r, "packID")
	detail, err := h.svc.GetPackDetail(packID)
	if err != nil {
		writeServerError(w, http.StatusNotFound, "pack not found", err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// DownloadPack returns the full public payload of one approved pack and bumps
// its download counter. Front-ends call this when they actually install a pack.
func (h *Handler) DownloadPack(w http.ResponseWriter, r *http.Request) {
	packID := chi.URLParam(r, "packID")
	detail, err := h.svc.DownloadPack(packID)
	if err != nil {
		writeServerError(w, http.StatusNotFound, "pack not found", err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// ListSystems returns the official system catalog to populate selectors
// (e.g. the background upload form) in the web frontend.
func (h *Handler) ListSystems(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.svc.ListSystems())
}

// GetConfig returns the runtime rewards/scrape configuration (threads, daily
// quota per thread, thread cost curve and points), so the UI renders the
// current economy without hardcoded values.
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.svc.PublicConfig())
}

// ---------------------------------------------------------------------------
// Authenticated submission flow
// ---------------------------------------------------------------------------

// CreateSubmission registers a new pending submission for the logged-in user.
func (h *Handler) CreateSubmission(w http.ResponseWriter, r *http.Request) {
	var req models.CreateSubmissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	sub, err := h.svc.CreateSubmission(r.Context(), req, userFromRequest(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, sub)
}

// SubmissionUploadURLPre presigns an upload to the pack's canonical location
// from the pack name, before a submission row exists (drafts stay client-side).
func (h *Handler) SubmissionUploadURLPre(w http.ResponseWriter, r *http.Request) {
	var req models.SubmissionUploadURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	resp, err := h.svc.SubmissionUploadURLPre(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetUploadURL returns a presigned URL to upload a single file to R2.
func (h *Handler) GetUploadURL(w http.ResponseWriter, r *http.Request) {
	submissionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}

	var req models.UploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	resp, err := h.svc.GetUploadURL(r.Context(), submissionID, userFromRequest(r), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// UpdateSubmission edits the metadata of a draft owned by the user.
func (h *Handler) UpdateSubmission(w http.ResponseWriter, r *http.Request) {
	submissionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}
	var req models.CreateSubmissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	sub, err := h.svc.UpdateSubmission(r.Context(), submissionID, userFromRequest(r), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

// AddSubmissionFiles registers uploaded files on an existing draft/rejected
// submission (submitter iterating after a rejection).
func (h *Handler) AddSubmissionFiles(w http.ResponseWriter, r *http.Request) {
	submissionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}
	var req struct {
		Files []models.SubmissionFileInput `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	sub, err := h.svc.AddSubmissionFiles(r.Context(), submissionID, userFromRequest(r), req.Files)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

// DashStats returns the app home leaderboards and recent published content.
func (h *Handler) DashStats(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.Dashboard()
	if err != nil {
		writeServerError(w, http.StatusInternalServerError, "failed to load dashboard", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// GetRewards returns the authenticated user's XP progression and unlocked limits.
func (h *Handler) GetRewards(w http.ResponseWriter, r *http.Request) {
	rw, err := h.svc.Rewards(userFromRequest(r))
	if err != nil {
		writeServerError(w, http.StatusInternalServerError, "failed to load rewards", err)
		return
	}
	writeJSON(w, http.StatusOK, rw)
}

// PublicProfile returns a user's public profile. When authenticated via a user
// token it also reports whether the requester follows the profile.
func (h *Handler) PublicProfile(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	var requester *uuid.UUID
	if claims, ok := auth.UserFromContext(r.Context()); ok {
		requester = &claims.UserID
	}
	p, err := h.svc.PublicProfile(username, requester)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// Follow makes the authenticated user follow a user.
func (h *Handler) Follow(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Follow(userFromRequest(r), chi.URLParam(r, "username")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"following": true})
}

// Unfollow removes a follow relationship.
func (h *Handler) Unfollow(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Unfollow(userFromRequest(r), chi.URLParam(r, "username")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"following": false})
}

// Followers lists the usernames following a user.
func (h *Handler) Followers(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Followers(chi.URLParam(r, "username"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"usernames": list})
}

// Following lists the usernames a user follows.
func (h *Handler) Following(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Following(chi.URLParam(r, "username"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"usernames": list})
}

// UserSubmissions lists a user's latest submissions (SAP + metadata).
func (h *Handler) UserSubmissions(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.RecentSubmissions(chi.URLParam(r, "username"), 10)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string][]models.UserSubmissionItem{"submissions": list})
}

// ListUsers returns every non-hidden user, for admin role management.
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	list, err := h.userSvc.ListUsers()
	if err != nil {
		writeServerError(w, http.StatusInternalServerError, "failed to list users", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"users": list})
}

// SetUserRole updates a user's role (user | reviewer | admin). Admin only.
func (h *Handler) SetUserRole(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	user, err := h.userSvc.SetUserRole(id, body.Role)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// SetDonorStatus updates a user's donor status (none | supporter | monthly_supporter).
// Admin only. Donations grant extra threads, never XP or rank.
func (h *Handler) SetDonorStatus(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	user, err := h.userSvc.SetDonorStatus(id, body.Status)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// SubmitSubmission moves a draft owned by the user into the admin review queue.
func (h *Handler) SubmitSubmission(w http.ResponseWriter, r *http.Request) {
	submissionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}

	sub, err := h.svc.SubmitForReview(r.Context(), submissionID, userFromRequest(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, sub)
}

// TrashSubmission moves a non-approved submission owned by the user to trash.
func (h *Handler) TrashSubmission(w http.ResponseWriter, r *http.Request) {
	submissionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}

	sub, err := h.svc.Trash(r.Context(), submissionID, userFromRequest(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, sub)
}

// ListUserSubmissions returns the submissions of the logged-in user.
func (h *Handler) ListUserSubmissions(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListUserSubmissions(r.Context(), userFromRequest(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list submissions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"submissions": list})
}

// GetUserSubmission returns a single submission owned by the logged-in user.
func (h *Handler) GetUserSubmission(w http.ResponseWriter, r *http.Request) {
	submissionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}

	detail, err := h.svc.GetUserSubmissionDetail(r.Context(), submissionID, userFromRequest(r))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, detail)
}

// ---------------------------------------------------------------------------
// Admin
// ---------------------------------------------------------------------------

// AdminLogin authenticates an admin and returns a JWT.
func (h *Handler) AdminLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	admin, err := h.svc.Login(req.Email, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":   admin.Token,
		"email":   admin.Email,
		"expires": admin.ExpiresAt,
	})
}

// ListSubmissions returns submissions for the admin, optionally filtered.
func (h *Handler) ListSubmissions(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	list, err := h.svc.ListAdminSubmissions(r.Context(), status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list submissions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"submissions": list})
}

// GetSubmission returns a single submission with its files and logs.
func (h *Handler) GetSubmission(w http.ResponseWriter, r *http.Request) {
	submissionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}

	detail, err := h.svc.GetSubmissionDetail(r.Context(), submissionID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, detail)
}

// Approve marks a submission as approved with an admin-controlled version and
// an optional comment.
func (h *Handler) Approve(w http.ResponseWriter, r *http.Request) {
	submissionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}

	var req models.ReviewRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	sub, err := h.svc.Approve(r.Context(), submissionID, actorIDFromContext(r.Context()), req.Version, req.Comment)
	if err != nil {
		writeServerError(w, http.StatusBadRequest, "failed to approve submission", err)
		return
	}

	writeJSON(w, http.StatusOK, sub)
}

// Reject marks a submission as rejected and deletes its files from R2,
// optionally recording an admin comment for the submitter.
func (h *Handler) Reject(w http.ResponseWriter, r *http.Request) {
	submissionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}

	var req models.ReviewRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	sub, err := h.svc.Reject(r.Context(), submissionID, actorIDFromContext(r.Context()), req.Comment)
	if err != nil {
		writeServerError(w, http.StatusBadRequest, "failed to reject submission", err)
		return
	}

	writeJSON(w, http.StatusOK, sub)
}

// DeleteSubmission removes a system art pack submission completely (R2 objects +
// DB rows). Admin only.
func (h *Handler) DeleteSubmission(w http.ResponseWriter, r *http.Request) {
	submissionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid submission id")
		return
	}
	if err := h.svc.DeleteSubmission(r.Context(), submissionID); err != nil {
		writeServerError(w, http.StatusBadRequest, "failed to delete submission", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// GetStorageUsage returns the object storage usage breakdown.
func (h *Handler) GetStorageUsage(w http.ResponseWriter, r *http.Request) {
	// Force refresh if admin requests it
	if r.URL.Query().Get("refresh") == "true" {
		if err := h.svc.RefreshStorageUsage(r.Context()); err != nil {
			writeServerError(w, http.StatusInternalServerError, "failed to refresh storage usage", err)
			return
		}
	}

	usage := h.svc.GetCachedStorageUsage()
	if usage == nil {
		// Not cached yet, compute synchronously (first time)
		var err error
		usage, err = h.svc.GetStorageUsage(r.Context())
		if err != nil {
			writeServerError(w, http.StatusInternalServerError, "failed to load storage usage", err)
			return
		}
	}

	writeJSON(w, http.StatusOK, usage)
}
