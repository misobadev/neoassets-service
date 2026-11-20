package handlers

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/lib/pq"
	"github.com/rs/zerolog/log"
)

// Messages that replace storage errors in responses.
const (
	msgNotFound      = "not found"
	msgInternalError = "internal server error"
)

// writeClientError reports a service error with the given status. Business
// errors carry user-facing messages and are returned as they are. Storage
// errors never reach the client verbatim: a missing row becomes "not found"
// (same status), and a database or driver failure is logged and answered
// with a generic 500, so SQL text, table or constraint names never leak.
func writeClientError(w http.ResponseWriter, status int, err error) {
	var pqErr *pq.Error
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, status, msgNotFound)
	case errors.As(err, &pqErr), errors.Is(err, sql.ErrConnDone), errors.Is(err, sql.ErrTxDone):
		log.Error().Err(err).Msg("storage error")
		writeError(w, http.StatusInternalServerError, msgInternalError)
	default:
		writeError(w, status, err.Error())
	}
}
