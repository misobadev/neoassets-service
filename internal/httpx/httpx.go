// Package httpx holds the small HTTP helpers every handler shares: writing
// JSON responses, decoding request bodies and parsing path parameters. The
// messages are part of the API contract (clients and golden tests read them),
// so they must not change casually.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// MsgInvalidBody is the error returned when a request body is not valid JSON.
const MsgInvalidBody = "invalid request body"

// JSON writes payload as a JSON response with the given status code.
func JSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload != nil {
		_ = json.NewEncoder(w).Encode(payload)
	}
}

// Error writes {"error": message} with the given status code.
func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, map[string]string{"error": message})
}

// Decode reads the JSON request body into dst. On failure it writes a 400
// with MsgInvalidBody and returns false; the handler should then return.
func Decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		Error(w, http.StatusBadRequest, MsgInvalidBody)
		return false
	}
	return true
}

// DecodeOptional is Decode for endpoints whose body may be omitted: an empty
// body leaves dst untouched and succeeds, but a body that is present and not
// valid JSON is still refused.
func DecodeOptional(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		Error(w, http.StatusBadRequest, MsgInvalidBody)
		return false
	}
	return true
}

// PathUUID parses the named chi path parameter as a UUID. On failure it writes
// a 400 with invalidMessage (e.g. "invalid submission id") and returns false.
func PathUUID(w http.ResponseWriter, r *http.Request, param, invalidMessage string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, param))
	if err != nil {
		Error(w, http.StatusBadRequest, invalidMessage)
		return uuid.Nil, false
	}
	return id, true
}
