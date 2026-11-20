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
