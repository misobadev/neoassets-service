package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// ErrReviewInProgress is returned when another reviewer is already approving
// or rejecting the same submission.
var ErrReviewInProgress = errors.New("submission is already being reviewed")

// WithReviewLock runs fn while holding a PostgreSQL advisory lock on the
// submission id, so two approve/reject requests for the same submission (a
// double click, two reviewers, two instances) never run at the same time. The
// second request gets ErrReviewInProgress instead of waiting; once the first
// finishes, the usual "not pending" check rejects a repeat.
//
// The lock is session-level, so it is taken and released on one dedicated
// connection; fn itself uses the regular pool.
func (r *Repository) WithReviewLock(ctx context.Context, submissionID uuid.UUID, fn func() error) error {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("review lock: %w", err)
	}
	defer conn.Close()

	key := "review:" + submissionID.String()
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, key).Scan(&locked); err != nil {
		return fmt.Errorf("review lock: %w", err)
	}
	if !locked {
		return ErrReviewInProgress
	}
	defer func() {
		// Unlock even if the request was cancelled, so the connection goes
		// back to the pool without the lock.
		if _, err := conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, key); err != nil {
			log.Warn().Err(err).Str("submission", submissionID.String()).Msg("review unlock failed")
		}
	}()
	return fn()
}
