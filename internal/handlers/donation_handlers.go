package handlers

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/rs/zerolog/log"

	"neoassets/internal/models"
)

// kofiMaxBody caps the Ko-fi webhook body; real payloads are a few KB.
const kofiMaxBody = 64 << 10

// KofiWebhook receives payment notifications from Ko-fi. It is intentionally
// unauthenticated (Ko-fi cannot send credentials) but every request must carry
// the shared verification token, which is checked in constant time.
func (h *Handler) KofiWebhook(w http.ResponseWriter, r *http.Request) {
	if h.donationSvc == nil || !h.donationSvc.KofiEnabled() {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, kofiMaxBody)
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	data := r.PostFormValue("data")
	if data == "" {
		writeError(w, http.StatusBadRequest, "missing data field")
		return
	}
	if err := h.donationSvc.HandleKofi([]byte(data)); err != nil {
		log.Warn().Err(err).Msg("ko-fi webhook rejected")
		writeError(w, http.StatusBadRequest, "invalid webhook")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// PatreonWebhook receives member events from Patreon. The raw body is verified
// against the X-Patreon-Signature header (HMAC with the webhook secret) before
// it is parsed.
func (h *Handler) PatreonWebhook(w http.ResponseWriter, r *http.Request) {
	if h.donationSvc == nil || !h.donationSvc.PatreonEnabled() {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, kofiMaxBody)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	event := r.Header.Get("X-Patreon-Event")
	signature := r.Header.Get("X-Patreon-Signature")
	if err := h.donationSvc.HandlePatreon(event, signature, raw); err != nil {
		log.Warn().Err(err).Msg("patreon webhook rejected")
		writeError(w, http.StatusBadRequest, "invalid webhook")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ImportDonations backfills historical donations (admin only). Each item is
// idempotent by external_id and the donor status is recomputed for matched users.
func (h *Handler) ImportDonations(w http.ResponseWriter, r *http.Request) {
	if h.donationSvc == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var body struct {
		Donations []models.DonationImportItem `json:"donations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	imported, linked, skipped, err := h.donationSvc.ImportDonations(body.Donations)
	if err != nil {
		log.Error().Err(err).Msg("donation import failed")
		writeServerError(w, http.StatusInternalServerError, "import failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"imported": imported, "linked": linked, "skipped": skipped})
}

// DonorClaim starts a claim for donations made with another email by sending a
// verification code to that address.
func (h *Handler) DonorClaim(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.donationSvc.ClaimStart(userFromRequest(r), body.Email); err != nil {
		log.Warn().Err(err).Str("email", body.Email).Msg("donor claim failed")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "claim code sent"})
}

// DonorClaimVerify confirms the claim code and links the donations.
func (h *Handler) DonorClaimVerify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	n, err := h.donationSvc.ClaimVerify(userFromRequest(r), body.Email, body.Code)
	if err != nil {
		log.Warn().Err(err).Str("email", body.Email).Msg("donor claim verify failed")
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"linked": n})
}
