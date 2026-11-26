package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lib/pq"

	"neoassets/internal/apperr"
)

func TestWriteClientErrorHidesStorageErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"business", errors.New("submission is not pending"), http.StatusBadRequest, "submission is not pending"},
		{"missing row", fmt.Errorf("load: %w", sql.ErrNoRows), http.StatusBadRequest, msgNotFound},
		{"database", &pq.Error{Code: "23505", Message: `duplicate key value violates unique constraint "games_pkey"`}, http.StatusInternalServerError, msgInternalError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeClientError(rec, http.StatusBadRequest, tc.err)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s, want %d %q", rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
			if strings.Contains(rec.Body.String(), "sql:") || strings.Contains(rec.Body.String(), "constraint") {
				t.Fatalf("storage detail leaked: %s", rec.Body)
			}
		})
	}
}

// Account endpoints pass on only the service's own messages; an SMTP, bcrypt,
// signing or driver error becomes a generic 500.
func TestWriteAccountErrorPassesOnlyServiceMessages(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"service", apperr.Unauthorized("invalid credentials"), http.StatusUnauthorized, "invalid credentials"},
		{"wrapped service", fmt.Errorf("login: %w", apperr.Forbidden("email not verified")), http.StatusUnauthorized, "email not verified"},
		{"smtp", errors.New("535 5.7.8 Username and Password not accepted"), http.StatusInternalServerError, msgInternalError},
		{"bcrypt", errors.New("bcrypt: password length exceeds 72 bytes"), http.StatusInternalServerError, msgInternalError},
		{"driver", errors.New("dial tcp 10.0.0.5:5432: connect: connection refused"), http.StatusInternalServerError, msgInternalError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeAccountError(rec, http.StatusUnauthorized, tc.err)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s, want %d %q", rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
			for _, leak := range []string{"535", "bcrypt", "dial tcp", "login:"} {
				if strings.Contains(rec.Body.String(), leak) {
					t.Fatalf("detail %q leaked: %s", leak, rec.Body)
				}
			}
		})
	}
}
