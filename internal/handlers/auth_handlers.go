package handlers

import (
	"neoassets/internal/httpx"
	"net/http"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/pkg/auth"
)

// Register creates a new user account.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req models.RegisterRequest
	if !httpx.Decode(w, r, &req) {
		return
	}

	result, err := h.userSvc.Register(r.Context(), req)
	if err != nil {
		writeAccountError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

// Login authenticates a user and returns a JWT.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req models.UserLoginRequest
	if !httpx.Decode(w, r, &req) {
		return
	}

	result, err := h.userSvc.Login(r.Context(), req)
	if err != nil {
		writeAccountError(w, http.StatusUnauthorized, err)
		return
	}

	// Set httpOnly session cookies for the browser. The tokens are still returned
	// in the body for programmatic clients (e.g. developer self-service); the web
	// app ignores them and relies on the cookies.
	ttl := h.userSvc.TokenTTL()
	setSessionCookie(w, r, auth.UserCookie, result.Token, ttl)
	if result.IsAdmin && result.AdminToken != "" {
		setSessionCookie(w, r, auth.AdminCookie, result.AdminToken, auth.AdminTokenTTL)
	} else {
		clearSessionCookie(w, r, auth.AdminCookie)
	}
	writeJSON(w, http.StatusOK, result)
}

// VerifyEmail verifies an account email using a token from the request body.
func (h *Handler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	var req models.VerifyEmailRequest
	if !httpx.Decode(w, r, &req) {
		return
	}

	if err := h.userSvc.VerifyEmail(r.Context(), req.Token); err != nil {
		writeAccountError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "email verified successfully"})
}

// ResendVerification resends the account verification email.
func (h *Handler) ResendVerification(w http.ResponseWriter, r *http.Request) {
	var req models.ResendVerificationRequest
	if !httpx.Decode(w, r, &req) {
		return
	}

	if err := h.userSvc.ResendVerification(r.Context(), req.Email); err != nil {
		writeAccountError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "verification email sent"})
}

// ForgotPassword emails a password reset link. Unknown emails still receive a
// success response to avoid account enumeration.
func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req models.ForgotPasswordRequest
	if !httpx.Decode(w, r, &req) {
		return
	}

	if err := h.userSvc.ForgotPassword(r.Context(), req.Email); err != nil {
		writeAccountError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "if an account exists with this email, a password reset link has been sent"})
}

// ResetPassword sets a new password using the emailed token.
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req models.ResetPasswordRequest
	if !httpx.Decode(w, r, &req) {
		return
	}

	if err := h.userSvc.ResetPassword(r.Context(), req.Token, req.NewPassword); err != nil {
		writeAccountError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "password reset successfully"})
}

// GetMe returns the profile of the authenticated user.
func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	user, err := h.userSvc.GetUserByID(r.Context(), claims.UserID)
	if err != nil {
		writeAccountError(w, http.StatusNotFound, err)
		return
	}

	writeJSON(w, http.StatusOK, user)
}

// UpdateProfile updates the authenticated user's username and/or email. A new
// email needs current_password and is confirmed from the link mailed to it.
func (h *Handler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		// CurrentPassword is required when the email changes.
		CurrentPassword string `json:"current_password"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	user, err := h.userSvc.UpdateProfile(r.Context(), claims.UserID, req.Username, req.Email, req.CurrentPassword)
	if err != nil {
		writeAccountError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// ConfirmEmailChange confirms the authenticated user's pending email with the
// token mailed to the new address.
func (h *Handler) ConfirmEmailChange(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	if err := h.userSvc.ConfirmEmailChange(r.Context(), claims.UserID, req.Token); err != nil {
		writeAccountError(w, http.StatusBadRequest, err)
		return
	}
	// Confirming signed the account out everywhere (token_version).
	clearSessionCookie(w, r, auth.UserCookie)
	clearSessionCookie(w, r, auth.AdminCookie)
	writeJSON(w, http.StatusOK, map[string]string{"message": "email changed; please sign in again"})
}

// ChangePassword updates the authenticated user's password.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	if err := h.userSvc.ChangePassword(r.Context(), claims.UserID, req.CurrentPassword, req.NewPassword); err != nil {
		writeAccountError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "password updated"})
}

// AvatarUploadURL presigns an upload for the authenticated user's avatar.
func (h *Handler) AvatarUploadURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MimeType string `json:"mime_type"`
		Size     int64  `json:"size"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	resp, err := h.svc.AvatarUploadURL(r.Context(), userFromRequest(r), req.MimeType, req.Size)
	if err != nil {
		writeClientError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// SetAvatar stores the uploaded avatar object key for the authenticated user.
func (h *Handler) SetAvatar(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ObjectKey string `json:"object_key"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	user, err := h.svc.SetAvatar(r.Context(), userFromRequest(r), req.ObjectKey)
	if err != nil {
		writeClientError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// RemoveAvatar clears the authenticated user's avatar.
func (h *Handler) RemoveAvatar(w http.ResponseWriter, r *http.Request) {
	user, err := h.svc.RemoveAvatar(r.Context(), userFromRequest(r))
	if err != nil {
		writeClientError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// userFromRequest extracts the authenticated user id from the request context.
func userFromRequest(r *http.Request) uuid.UUID {
	claims, _ := auth.UserFromContext(r.Context())
	return claims.UserID
}
