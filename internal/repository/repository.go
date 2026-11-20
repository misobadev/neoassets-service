package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"neoassets/internal/economy"
	"neoassets/internal/models"
)

// ErrDuplicatePack is returned when a submission's pack id (derived from the
// name) already exists. The pack_id column is unique.
var ErrDuplicatePack = errors.New("a pack with this name already exists")

// isUniqueViolation reports whether err is a PostgreSQL unique constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505"
}

// Repository groups all database access for the service.
type Repository struct {
	// db runs the queries: the pool, or a transaction inside WithTx.
	db dbtx
	// pool is the underlying pool, for operations that need their own
	// connection or transaction (advisory locks, AwardXP).
	pool *sql.DB
	// econ is the rewards/scraping economy (threads, quotas, XP).
	econ economy.Config
}

// Option configures a Repository.
type Option func(*Repository)

// WithEconomy sets the economy (default: economy.Default()).
func WithEconomy(c economy.Config) Option {
	return func(r *Repository) { r.econ = c }
}

// Economy returns the economy the repository was built with.
func (r *Repository) Economy() economy.Config { return r.econ }

// dbtx is what the repository's queries need; *sql.DB and *sql.Tx both
// satisfy it, so the same methods run inside or outside a transaction.
type dbtx interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// NewRepository creates a Repository backed by the given database.
func NewRepository(db *sql.DB, opts ...Option) *Repository {
	r := &Repository{db: db, pool: db, econ: economy.Default()}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// WithTx runs fn with a Repository whose queries share one transaction. It
// commits when fn returns nil and rolls back otherwise. Called on a
// Repository that is already inside a transaction, it reuses it.
func (r *Repository) WithTx(ctx context.Context, fn func(tx *Repository) error) error {
	if _, inTx := r.db.(*sql.Tx); inTx {
		return fn(r)
	}
	tx, err := r.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(&Repository{db: tx, pool: r.pool, econ: r.econ}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Admins
// ---------------------------------------------------------------------------

// GetAdminByEmail returns the user with the admin role matching the email, or
// sql.ErrNoRows.
func (r *Repository) GetAdminByEmail(ctx context.Context, email string) (*models.User, error) {
	return r.GetUserByEmailAndRole(ctx, email, models.RoleAdmin)
}

// GetUserByEmailAndRole returns a user by email and role, or sql.ErrNoRows.
func (r *Repository) GetUserByEmailAndRole(ctx context.Context, email, role string) (*models.User, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE email = $1 AND role = $2`,
		email, role,
	)
	return scanUser(row)
}

// EnsureAdmin inserts the seed admin if one does not already exist.
func (r *Repository) EnsureAdmin(ctx context.Context, email, passwordHash string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, email_verified)
		 VALUES ($1, $2, $3, $4, TRUE)
		 ON CONFLICT (email) DO NOTHING`,
		strings.Split(email, "@")[0], email, passwordHash, models.RoleAdmin,
	)
	if err != nil {
		return fmt.Errorf("failed to ensure admin: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Submissions
// ---------------------------------------------------------------------------

// CreateSubmission inserts a new submission. pack_id is not unique: a new
// version of a pack, or a contribution to an existing approved pack
// (contribution=true), is allowed.
func (r *Repository) CreateSubmission(ctx context.Context, packID, name, author, description, donationURL, anonUserID string, ai bool, userID *uuid.UUID, contribution bool) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO submissions (pack_id, name, author, description, donation_url, ai, anon_user_id, user_id, status, contribution)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id`,
		packID, name, author, description, donationURL, ai, anonUserID, userID, models.StatusPending, contribution,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to create submission: %w", err)
	}
	return id, nil
}

// PackIDInUse reports whether any non-trashed submission already owns the given
// pack id. It guards a brand-new pack from colliding with a pack namespace that
// belongs to another (approved or in-review) submission.
func (r *Repository) PackIDInUse(ctx context.Context, packID string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM submissions WHERE pack_id = $1 AND status <> 'trashed')`,
		packID,
	).Scan(&exists)
	return exists, err
}

// GetSubmission returns a submission by ID.
func (r *Repository) GetSubmission(ctx context.Context, id uuid.UUID) (*models.Submission, error) {
	var s models.Submission
	err := r.db.QueryRowContext(ctx,
		`SELECT id, pack_id, name, author, description, donation_url, ai, version,
		        status, anon_user_id, user_id, created_at, reviewed_at, reviewed_by, admin_version, contribution
		 FROM submissions WHERE id = $1`,
		id,
	).Scan(
		&s.ID, &s.PackID, &s.Name, &s.Author, &s.Description, &s.DonationURL, &s.AI, &s.Version,
		&s.Status, &s.AnonUserID, &s.UserID, &s.CreatedAt, &s.ReviewedAt, &s.ReviewedBy, &s.AdminVersion, &s.Contribution,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// GetSubmissionByIDForUser returns a submission only if it belongs to the
// given user, otherwise sql.ErrNoRows.
func (r *Repository) GetSubmissionByIDForUser(ctx context.Context, id uuid.UUID, userID uuid.UUID) (*models.Submission, error) {
	var s models.Submission
	err := r.db.QueryRowContext(ctx,
		`SELECT id, pack_id, name, author, description, donation_url, ai, version,
		        status, anon_user_id, user_id, created_at, reviewed_at, reviewed_by, admin_version, contribution
		 FROM submissions WHERE id = $1 AND user_id = $2`,
		id, userID,
	).Scan(
		&s.ID, &s.PackID, &s.Name, &s.Author, &s.Description, &s.DonationURL, &s.AI, &s.Version,
		&s.Status, &s.AnonUserID, &s.UserID, &s.CreatedAt, &s.ReviewedAt, &s.ReviewedBy, &s.AdminVersion, &s.Contribution,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListSubmissions returns submissions filtered by status (empty for all),
// newest first.
func (r *Repository) ListSubmissions(ctx context.Context, status string) ([]models.Submission, error) {
	query := `SELECT id, pack_id, name, author, description, donation_url, ai, version,
	                 status, anon_user_id, user_id, created_at, reviewed_at, reviewed_by, admin_version, contribution
	          FROM submissions`
	args := []interface{}{}
	if status != "" {
		query += ` WHERE status = $1`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list submissions: %w", err)
	}
	defer rows.Close()

	var list []models.Submission
	for rows.Next() {
		var s models.Submission
		if err := rows.Scan(
			&s.ID, &s.PackID, &s.Name, &s.Author, &s.Description, &s.DonationURL, &s.AI, &s.Version,
			&s.Status, &s.AnonUserID, &s.UserID, &s.CreatedAt, &s.ReviewedAt, &s.ReviewedBy, &s.AdminVersion, &s.Contribution,
		); err != nil {
			return nil, fmt.Errorf("failed to scan submission: %w", err)
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// ListReviewSubmissions returns every submission that has entered the review
// workflow (pending, approved or rejected), newest first. Private drafts
// (created) and user-trashed rows are excluded, so admins never see them.
func (r *Repository) ListReviewSubmissions(ctx context.Context) ([]models.Submission, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, pack_id, name, author, description, donation_url, ai, version,
		        status, anon_user_id, user_id, created_at, reviewed_at, reviewed_by, admin_version, contribution
		 FROM submissions WHERE status IN ($1, $2, $3) ORDER BY created_at DESC`,
		models.StatusPending, models.StatusApproved, models.StatusRejected,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list review submissions: %w", err)
	}
	defer rows.Close()

	var list []models.Submission
	for rows.Next() {
		var s models.Submission
		if err := rows.Scan(
			&s.ID, &s.PackID, &s.Name, &s.Author, &s.Description, &s.DonationURL, &s.AI, &s.Version,
			&s.Status, &s.AnonUserID, &s.UserID, &s.CreatedAt, &s.ReviewedAt, &s.ReviewedBy, &s.AdminVersion, &s.Contribution,
		); err != nil {
			return nil, fmt.Errorf("failed to scan review submission: %w", err)
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// ListSubmissionsByUser returns submissions belonging to a user, newest first.
// status filters by review state: "review" returns only pending/approved/rejected
// (the review feed), an empty status returns everything except trashed. When
// limit > 0 the result is paged and total is the unpaged count.
func (r *Repository) ListSubmissionsByUser(ctx context.Context, userID uuid.UUID, status string, limit, offset int) ([]models.Submission, int64, error) {
	where := []string{"user_id = $1"}
	args := []interface{}{userID}
	switch status {
	case "":
		where = append(where, "status <> $2")
		args = append(args, models.StatusTrashed)
	case "review":
		where = append(where, "status IN ('pending','approved','rejected')")
	default:
		where = append(where, "status = $2")
		args = append(args, status)
	}
	cond := strings.Join(where, " AND ")

	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM submissions WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count user submissions: %w", err)
	}

	query := `SELECT id, pack_id, name, author, description, donation_url, ai, version,
	                 status, anon_user_id, user_id, created_at, reviewed_at, reviewed_by, admin_version, contribution
	          FROM submissions WHERE ` + cond + ` ORDER BY created_at DESC`
	if limit > 0 {
		args = append(args, limit, offset)
		query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list user submissions: %w", err)
	}
	defer rows.Close()

	var list []models.Submission
	for rows.Next() {
		var s models.Submission
		if err := rows.Scan(
			&s.ID, &s.PackID, &s.Name, &s.Author, &s.Description, &s.DonationURL, &s.AI, &s.Version,
			&s.Status, &s.AnonUserID, &s.UserID, &s.CreatedAt, &s.ReviewedAt, &s.ReviewedBy, &s.AdminVersion, &s.Contribution,
		); err != nil {
			return nil, 0, fmt.Errorf("failed to scan user submission: %w", err)
		}
		list = append(list, s)
	}
	return list, total, rows.Err()
}

// SetSubmissionStatus updates the status of a submission and records who/when
// reviewed it. Returns the updated submission.
func (r *Repository) SetSubmissionStatus(ctx context.Context, id uuid.UUID, status string, adminID uuid.UUID, adminVersion string) (*models.Submission, error) {
	_, err := r.db.ExecContext(ctx,
		`UPDATE submissions
		 SET status = $1, reviewed_by = $2, reviewed_at = NOW(),
		     admin_version = CASE WHEN $3 = '' THEN admin_version ELSE $3 END
		 WHERE id = $4`,
		status, adminID, adminVersion, id,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update submission status: %w", err)
	}
	return r.GetSubmission(ctx, id)
}

// UpdateSubmission edits the metadata of a draft owned by the user. It only
// applies while the submission is still in the 'created' or 'rejected' state;
// approved submissions are read-only (changes go through a new contribution).
// A contribution only changes its description: it keeps the name, author,
// donation link and AI flag copied from the pack it contributes to.
func (r *Repository) UpdateSubmission(ctx context.Context, id uuid.UUID, userID uuid.UUID, name, author, description, donationURL string, ai bool) (*models.Submission, error) {
	var s models.Submission
	err := r.db.QueryRowContext(ctx,
		`UPDATE submissions
		 SET name = CASE WHEN contribution THEN name ELSE $1 END,
		     author = CASE WHEN contribution THEN author ELSE $2 END,
		     description = $3,
		     donation_url = CASE WHEN contribution THEN donation_url ELSE $4 END,
		     ai = CASE WHEN contribution THEN ai ELSE $5 END
		 WHERE id = $6 AND user_id = $7 AND status IN ('created','rejected')
		 RETURNING id, pack_id, name, author, description, donation_url, ai, version,
		           status, anon_user_id, user_id, created_at, reviewed_at, reviewed_by, admin_version, contribution`,
		name, author, description, donationURL, ai, id, userID,
	).Scan(
		&s.ID, &s.PackID, &s.Name, &s.Author, &s.Description, &s.DonationURL, &s.AI, &s.Version,
		&s.Status, &s.AnonUserID, &s.UserID, &s.CreatedAt, &s.ReviewedAt, &s.ReviewedBy, &s.AdminVersion, &s.Contribution,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// SyncContributionIdentity copies the name, author, donation link and AI flag
// of the pack's latest approved revision onto a contribution, so approving it
// never changes how the pack is listed. It is a no-op for a pack's own
// submission.
func (r *Repository) SyncContributionIdentity(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE submissions c
		 SET name = p.name, author = p.author, donation_url = p.donation_url, ai = p.ai
		 FROM (
		   SELECT a.name, a.author, a.donation_url, a.ai
		   FROM submissions a
		   WHERE a.pack_id = (SELECT pack_id FROM submissions WHERE id = $1)
		     AND a.status = 'approved'
		   ORDER BY a.created_at DESC
		   LIMIT 1
		 ) p
		 WHERE c.id = $1 AND c.contribution`,
		id,
	)
	if err != nil {
		return fmt.Errorf("failed to sync contribution identity: %w", err)
	}
	return nil
}

// SubmitSubmission moves a submission owned by the user from the draft
// ('created') or rejected state into the 'pending' state for admin review.
// Approved submissions cannot be resubmitted directly; changes must be a new
// contribution.
func (r *Repository) SubmitSubmission(ctx context.Context, id uuid.UUID, userID uuid.UUID) (*models.Submission, error) {
	var s models.Submission
	err := r.db.QueryRowContext(ctx,
		`UPDATE submissions
		 SET status = $1
		 WHERE id = $2 AND user_id = $3 AND status IN ('created','rejected')
		 RETURNING id, pack_id, name, author, description, donation_url, ai, version,
		           status, anon_user_id, user_id, created_at, reviewed_at, reviewed_by, admin_version, contribution`,
		models.StatusPending, id, userID,
	).Scan(
		&s.ID, &s.PackID, &s.Name, &s.Author, &s.Description, &s.DonationURL, &s.AI, &s.Version,
		&s.Status, &s.AnonUserID, &s.UserID, &s.CreatedAt, &s.ReviewedAt, &s.ReviewedBy, &s.AdminVersion, &s.Contribution,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// TrashSubmission moves a submission owned by the user into the 'trashed'
// state. It only works while the pack is still a draft or has been rejected
// (not while it is pending review or already approved). Returns sql.ErrNoRows
// otherwise.
func (r *Repository) TrashSubmission(ctx context.Context, id uuid.UUID, userID uuid.UUID) (*models.Submission, error) {
	var s models.Submission
	err := r.db.QueryRowContext(ctx,
		`UPDATE submissions
		 SET status = $1
		 WHERE id = $2 AND user_id = $3 AND status IN ($4, $5)
		 RETURNING id, pack_id, name, author, description, donation_url, ai, version,
		           status, anon_user_id, user_id, created_at, reviewed_at, reviewed_by, admin_version, contribution`,
		models.StatusTrashed, id, userID, models.StatusCreated, models.StatusRejected,
	).Scan(
		&s.ID, &s.PackID, &s.Name, &s.Author, &s.Description, &s.DonationURL, &s.AI, &s.Version,
		&s.Status, &s.AnonUserID, &s.UserID, &s.CreatedAt, &s.ReviewedAt, &s.ReviewedBy, &s.AdminVersion, &s.Contribution,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ---------------------------------------------------------------------------
// Submission files
// ---------------------------------------------------------------------------

// AddFile records a file that was uploaded for a submission.
func (r *Repository) AddFile(ctx context.Context, submissionID uuid.UUID, objectKey, fileName, systemID, kind, mimeType string, size int64) error {
	return r.AddFileReason(ctx, submissionID, objectKey, fileName, systemID, kind, mimeType, size, "")
}

// AddFileReason records a file with an optional "why replace" reason. Keeping
// a single row per (file_name, system_id, kind) avoids duplicates when a draft
// is re-saved or a file is replaced (which would otherwise register a new review
// key while leaving the old row behind).
func (r *Repository) AddFileReason(ctx context.Context, submissionID uuid.UUID, objectKey, fileName, systemID, kind, mimeType string, size int64, reason string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO submission_files (submission_id, object_key, file_name, system_id, kind, mime_type, size, reason)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (submission_id, object_key) DO UPDATE SET
		   file_name = EXCLUDED.file_name, system_id = EXCLUDED.system_id,
		   kind = EXCLUDED.kind, mime_type = EXCLUDED.mime_type, size = EXCLUDED.size, reason = EXCLUDED.reason`,
		submissionID, objectKey, fileName, systemID, kind, mimeType, size, reason,
	)
	if err != nil {
		return fmt.Errorf("failed to add submission file: %w", err)
	}
	// Drop older rows for the same logical file (same name/system/kind) so
	// re-saves or replacements do not accumulate duplicates.
	_, err = r.db.ExecContext(ctx,
		`DELETE FROM submission_files
		 WHERE submission_id = $1 AND file_name = $2 AND system_id = $3 AND kind = $4 AND object_key <> $5`,
		submissionID, fileName, systemID, kind, objectKey,
	)
	if err != nil {
		return fmt.Errorf("failed to deduplicate submission file: %w", err)
	}
	return nil
}

// ListFiles returns all files belonging to a submission.
func (r *Repository) ListFiles(ctx context.Context, submissionID uuid.UUID) ([]models.SubmissionFile, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, submission_id, object_key, file_name, system_id, kind, size, mime_type, created_at, reason
		 FROM submission_files WHERE submission_id = $1`,
		submissionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list submission files: %w", err)
	}
	defer rows.Close()

	var list []models.SubmissionFile
	for rows.Next() {
		var f models.SubmissionFile
		if err := rows.Scan(
			&f.ID, &f.SubmissionID, &f.ObjectKey, &f.FileName, &f.SystemID, &f.Kind, &f.Size, &f.MimeType, &f.CreatedAt, &f.Reason,
		); err != nil {
			return nil, fmt.Errorf("failed to scan submission file: %w", err)
		}
		list = append(list, f)
	}
	return list, rows.Err()
}

// GetSubmissionFileByObjectKey returns a submission's file row by its object
// key (unique within a submission).
func (r *Repository) GetSubmissionFileByObjectKey(ctx context.Context, submissionID uuid.UUID, objectKey string) (*models.SubmissionFile, error) {
	var f models.SubmissionFile
	err := r.db.QueryRowContext(ctx,
		`SELECT id, submission_id, object_key, file_name, system_id, kind, size, mime_type, created_at, reason
		 FROM submission_files WHERE submission_id = $1 AND object_key = $2`, submissionID, objectKey,
	).Scan(&f.ID, &f.SubmissionID, &f.ObjectKey, &f.FileName, &f.SystemID, &f.Kind, &f.Size, &f.MimeType, &f.CreatedAt, &f.Reason)
	if err != nil {
		return nil, fmt.Errorf("failed to get submission file: %w", err)
	}
	return &f, nil
}

// ListFilesBySubmissionIDs returns every file of the given submissions in a
// single query, keyed by submission id (N+1 avoidance).
func (r *Repository) ListFilesBySubmissionIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]models.SubmissionFile, error) {
	out := make(map[uuid.UUID][]models.SubmissionFile, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, submission_id, object_key, file_name, system_id, kind, size, mime_type, created_at, reason
		 FROM submission_files WHERE submission_id = ANY($1) ORDER BY submission_id, created_at`,
		pq.Array(ids),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list submission files: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var f models.SubmissionFile
		if err := rows.Scan(
			&f.ID, &f.SubmissionID, &f.ObjectKey, &f.FileName, &f.SystemID, &f.Kind, &f.Size, &f.MimeType, &f.CreatedAt, &f.Reason,
		); err != nil {
			return nil, fmt.Errorf("failed to scan submission file: %w", err)
		}
		out[f.SubmissionID] = append(out[f.SubmissionID], f)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Logs
// ---------------------------------------------------------------------------

// AddLog records an audit log entry for a submission.
func (r *Repository) AddLog(ctx context.Context, submissionID uuid.UUID, action, userID, userAgent, detail string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO submission_logs (submission_id, action, user_id, user_agent, detail)
		 VALUES ($1, $2, $3, $4, $5)`,
		submissionID, action, userID, userAgent, detail,
	)
	if err != nil {
		return fmt.Errorf("failed to add submission log: %w", err)
	}
	return nil
}

// ListLogs returns all audit logs for a submission, oldest first.
func (r *Repository) ListLogs(ctx context.Context, submissionID uuid.UUID) ([]models.SubmissionLog, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, submission_id, action, user_id, user_agent, detail, created_at
		 FROM submission_logs WHERE submission_id = $1 ORDER BY created_at ASC`,
		submissionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list submission logs: %w", err)
	}
	defer rows.Close()

	var list []models.SubmissionLog
	for rows.Next() {
		var l models.SubmissionLog
		if err := rows.Scan(&l.ID, &l.SubmissionID, &l.Action, &l.UserID, &l.UserAgent, &l.Detail, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan submission log: %w", err)
		}
		list = append(list, l)
	}
	return list, rows.Err()
}

// ListLogsBySubmissionIDs returns every log of the given submissions in a
// single query, keyed by submission id (N+1 avoidance).
func (r *Repository) ListLogsBySubmissionIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]models.SubmissionLog, error) {
	out := make(map[uuid.UUID][]models.SubmissionLog, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, submission_id, action, user_id, user_agent, detail, created_at
		 FROM submission_logs WHERE submission_id = ANY($1) ORDER BY submission_id, created_at ASC`,
		pq.Array(ids),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list submission logs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var l models.SubmissionLog
		if err := rows.Scan(&l.ID, &l.SubmissionID, &l.Action, &l.UserID, &l.UserAgent, &l.Detail, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan submission log: %w", err)
		}
		out[l.SubmissionID] = append(out[l.SubmissionID], l)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Public manifest
// ---------------------------------------------------------------------------

// ListApprovedPacks returns the latest approved revision of each pack (with its
// download counter), sorted and paginated. Only approved submissions are ever
// served publicly; pending/rejected revisions never leak into the catalog.
func (r *Repository) ListApprovedPacks(ctx context.Context, sort string, limit, offset int) ([]models.Pack, int64, error) {
	var total int64
	if err := r.db.QueryRowContext(ctx,
		`SELECT count(DISTINCT pack_id) FROM submissions WHERE status = 'approved'`,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count approved packs: %w", err)
	}

	order := "latest.created_at DESC"
	switch sort {
	case "name":
		order = "latest.name ASC"
	case "created":
		order = "latest.created_at DESC"
	default:
		order = "COALESCE(st.scrapes, 0) DESC, latest.created_at DESC"
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT latest.pack_id, latest.name, latest.author, latest.description, latest.donation_url, latest.ai,
		        latest.version, COALESCE(st.scrapes, 0) AS downloads,
		        COALESCE(cov.systems_covered, 0), COALESCE(u.username, latest.author)
		 FROM (
		   SELECT DISTINCT ON (pack_id) pack_id, name, author, description, donation_url, ai,
		          COALESCE(NULLIF(admin_version, ''), version) AS version, created_at, user_id
		   FROM submissions
		   WHERE status = 'approved'
		   ORDER BY pack_id, created_at DESC
		 ) latest
		 LEFT JOIN pack_scrape_stats st ON st.pack_id = latest.pack_id
		 LEFT JOIN users u ON u.id = `+packOwnerSQL("latest.pack_id", "latest.user_id")+`
		 LEFT JOIN (
		   SELECT s.pack_id, count(DISTINCT sf.system_id) AS systems_covered
		   FROM submission_files sf
		   JOIN submissions s ON s.id = sf.submission_id
		   WHERE s.status = 'approved' AND sf.kind = 'background' AND sf.system_id <> ''
		   GROUP BY s.pack_id
		 ) cov ON cov.pack_id = latest.pack_id
		 ORDER BY `+order+`
		 LIMIT $1 OFFSET $2`,
		limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list approved packs: %w", err)
	}
	defer rows.Close()

	var list []models.Pack
	for rows.Next() {
		var p models.Pack
		if err := rows.Scan(
			&p.PackID, &p.Name, &p.Author, &p.Description, &p.DonationURL, &p.AI, &p.Version, &p.Downloads,
			&p.SystemsCovered, &p.SubmittedBy,
		); err != nil {
			return nil, 0, fmt.Errorf("failed to scan pack: %w", err)
		}
		list = append(list, p)
	}
	return list, total, rows.Err()
}

// packOwnerSQL is the SQL for a pack's owner: the user who submitted the pack
// itself (its first approved, non-contribution submission), so an approved
// contribution never shows its contributor as the pack's submitter. It falls
// back to the latest revision's user for packs with no such row.
func packOwnerSQL(packIDCol, fallbackUserCol string) string {
	return `COALESCE((
		   SELECT o.user_id FROM submissions o
		   WHERE o.pack_id = ` + packIDCol + ` AND o.status = 'approved' AND NOT o.contribution
		   ORDER BY o.created_at
		   LIMIT 1
		 ), ` + fallbackUserCol + `)`
}

// GetPackDetail returns the latest approved revision of a pack together with
// every published file, or sql.ErrNoRows when the pack is not approved.
func (r *Repository) GetPackDetail(ctx context.Context, packID string) (*models.PackDetail, error) {
	var d models.PackDetail
	err := r.db.QueryRowContext(ctx,
		`SELECT p.pack_id, p.name, p.author, p.description, p.donation_url, p.ai,
		        COALESCE(NULLIF(p.admin_version, ''), p.version) AS version,
		        COALESCE(st.scrapes, 0) AS downloads,
		        COALESCE(cov.systems_covered, 0), COALESCE(u.username, p.author)
		 FROM (
		   SELECT pack_id, name, author, description, donation_url, ai, version, admin_version, user_id
		   FROM submissions
		   WHERE pack_id = $1 AND status = 'approved'
		   ORDER BY created_at DESC
		   LIMIT 1
		 ) p
		 LEFT JOIN users u ON u.id = `+packOwnerSQL("p.pack_id", "p.user_id")+`
		 LEFT JOIN pack_scrape_stats st ON st.pack_id = p.pack_id
		 LEFT JOIN (
		   SELECT s.pack_id, count(DISTINCT sf.system_id) AS systems_covered
		   FROM submission_files sf
		   JOIN submissions s ON s.id = sf.submission_id
		   WHERE s.pack_id = $1 AND s.status = 'approved' AND sf.kind = 'background' AND sf.system_id <> ''
		   GROUP BY s.pack_id
		 ) cov ON cov.pack_id = p.pack_id`,
		packID,
	).Scan(&d.PackID, &d.Name, &d.Author, &d.Description, &d.DonationURL, &d.AI, &d.Version, &d.Downloads, &d.SystemsCovered, &d.SubmittedBy)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT ON (sf.object_key) sf.kind, sf.system_id, sf.file_name, sf.object_key, sf.size, sf.mime_type
		 FROM submission_files sf
		 JOIN submissions s ON s.id = sf.submission_id
		 WHERE s.pack_id = $1 AND s.status = 'approved'
		 ORDER BY sf.object_key, sf.created_at DESC`,
		packID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list pack files: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var f models.PackFile
		if err := rows.Scan(&f.Kind, &f.SystemID, &f.FileName, &f.ObjectKey, &f.Size, &f.Mime); err != nil {
			return nil, fmt.Errorf("failed to scan pack file: %w", err)
		}
		d.Files = append(d.Files, f)
	}
	return &d, rows.Err()
}

// IncrementPackScrape bumps a pack's download counter and returns the new value.
func (r *Repository) IncrementPackScrape(ctx context.Context, packID string) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO pack_scrape_stats (pack_id, scrapes, last_scraped_at)
		 VALUES ($1, 1, NOW())
		 ON CONFLICT (pack_id) DO UPDATE SET
		   scrapes = pack_scrape_stats.scrapes + 1,
		   last_scraped_at = NOW()
		 RETURNING scrapes`, packID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to increment pack scrape: %w", err)
	}
	return count, nil
}

// GetApprovedPackMeta returns the metadata of the latest approved revision of a
// pack, or sql.ErrNoRows when the pack is not approved.
func (r *Repository) GetApprovedPackMeta(ctx context.Context, packID string) (*models.Pack, error) {
	var p models.Pack
	err := r.db.QueryRowContext(ctx,
		`SELECT pack_id, name, author, description, donation_url, ai,
		        COALESCE(NULLIF(admin_version, ''), version) AS version,
		        COALESCE((SELECT scrapes FROM pack_scrape_stats WHERE pack_id = $1), 0) AS downloads
		 FROM submissions
		 WHERE pack_id = $1 AND status = 'approved'
		 ORDER BY created_at DESC
		 LIMIT 1`,
		packID,
	).Scan(&p.PackID, &p.Name, &p.Author, &p.Description, &p.DonationURL, &p.AI, &p.Version, &p.Downloads)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListPendingPackContributionCounts returns, per pack, how many pending or
// draft contributions it currently has.
func (r *Repository) ListPendingPackContributionCounts(ctx context.Context, packIDs []string) (map[string]int, error) {
	out := make(map[string]int, len(packIDs))
	if len(packIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT pack_id, count(*) FROM submissions
		 WHERE pack_id = ANY($1) AND contribution AND status IN ('created','pending')
		 GROUP BY pack_id`,
		pq.Array(packIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to count pack contributions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var packID string
		var n int
		if err := rows.Scan(&packID, &n); err != nil {
			return nil, err
		}
		out[packID] = n
	}
	return out, rows.Err()
}

// ListPackContributorNames returns, per pack, the usernames of users with an
// approved contribution to that pack.
func (r *Repository) ListPackContributorNames(ctx context.Context, packIDs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(packIDs))
	if len(packIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT s.pack_id, u.username
		 FROM submissions s
		 JOIN users u ON u.id = s.user_id
		 WHERE s.pack_id = ANY($1) AND s.contribution AND s.status = 'approved'
		 ORDER BY u.username`,
		pq.Array(packIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list pack contributors: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var packID, username string
		if err := rows.Scan(&packID, &username); err != nil {
			return nil, err
		}
		out[packID] = append(out[packID], username)
	}
	return out, rows.Err()
}

// ListPackContributions returns the pending/draft contributions to a pack.
func (r *Repository) ListPackContributions(ctx context.Context, packID string) ([]models.PackContribution, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT s.id, s.status, u.username, s.created_at,
		        (SELECT count(*) FROM submission_files sf WHERE sf.submission_id = s.id) AS file_count
		 FROM submissions s
		 JOIN users u ON u.id = s.user_id
		 WHERE s.pack_id = $1 AND s.contribution AND s.status IN ('created','pending')
		 ORDER BY s.created_at DESC`,
		packID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list pack contributions: %w", err)
	}
	defer rows.Close()
	var list []models.PackContribution
	for rows.Next() {
		var c models.PackContribution
		if err := rows.Scan(&c.ID, &c.Status, &c.Username, &c.CreatedAt, &c.FileCount); err != nil {
			return nil, fmt.Errorf("failed to scan pack contribution: %w", err)
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// packImageSystems is the ordered list of systems whose backgrounds are used to
// preview an approved pack, so every front-end shows the same four popular
// systems. packImageFallback covers a missing slot.
var packImageSystems = []string{"snes", "ps1", "gba", "genesis"}

const packImageFallback = "2600"

// ListApprovedPackBackgrounds returns, per approved pack, up to four published
// background object keys for the most popular systems (snes, ps1, gba, genesis),
// falling back to the Atari 2600 background when one of them is missing. They
// are used to render the pack's image grid in the public UI.
func (r *Repository) ListApprovedPackBackgrounds(ctx context.Context, packIDs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(packIDs))
	if len(packIDs) == 0 {
		return out, nil
	}
	systems := append(append([]string{}, packImageSystems...), packImageFallback)
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT ON (s.pack_id, sf.system_id) s.pack_id, sf.system_id, sf.object_key
		 FROM submission_files sf
		 JOIN submissions s ON s.id = sf.submission_id
		 WHERE s.pack_id = ANY($1) AND s.status = 'approved' AND sf.kind = 'background'
		   AND sf.system_id = ANY($2)
		 ORDER BY s.pack_id, sf.system_id, sf.created_at DESC`,
		pq.Array(packIDs), pq.Array(systems),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list approved pack backgrounds: %w", err)
	}
	defer rows.Close()
	byPack := map[string]map[string]string{}
	for rows.Next() {
		var packID, systemID, objectKey string
		if err := rows.Scan(&packID, &systemID, &objectKey); err != nil {
			return nil, fmt.Errorf("failed to scan pack background: %w", err)
		}
		if byPack[packID] == nil {
			byPack[packID] = map[string]string{}
		}
		byPack[packID][systemID] = objectKey
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for packID, keys := range byPack {
		seen := map[string]bool{}
		list := make([]string, 0, len(packImageSystems))
		for _, systemID := range packImageSystems {
			key, ok := keys[systemID]
			if !ok {
				key, ok = keys[packImageFallback]
			}
			if ok && key != "" && !seen[key] {
				list = append(list, key)
				seen[key] = true
			}
		}
		if len(list) > 0 {
			out[packID] = list
		}
	}
	return out, nil
}

// CountApprovedByPack returns how many approved submissions already exist for a
// pack, excluding the given submission id, used to compute the next version.
func (r *Repository) CountApprovedByPack(ctx context.Context, packID string, excludeID uuid.UUID) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT count(*) FROM submissions WHERE pack_id = $1 AND status = 'approved' AND id <> $2`,
		packID, excludeID,
	).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ListLivePackObjectKeys returns the R2 object keys referenced by live
// (created/pending/approved) submissions of a pack, excluding one submission.
func (r *Repository) ListLivePackObjectKeys(ctx context.Context, packID string, excludeID uuid.UUID) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT sf.object_key
		 FROM submission_files sf
		 JOIN submissions s ON s.id = sf.submission_id
		 WHERE s.pack_id = $1 AND s.status IN ('created','pending','approved') AND s.id <> $2`,
		packID, excludeID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// DeleteSubmissionFiles removes the file rows of a submission (used on
// trash/reject so no stale references to deleted objects remain).
func (r *Repository) DeleteSubmissionFiles(ctx context.Context, submissionID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM submission_files WHERE submission_id = $1`, submissionID)
	if err != nil {
		return fmt.Errorf("failed to delete submission files: %w", err)
	}
	return nil
}

// DeleteSubmissionFile removes a single file row of a submission.
func (r *Repository) DeleteSubmissionFile(ctx context.Context, fileID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM submission_files WHERE id = $1`, fileID)
	if err != nil {
		return fmt.Errorf("failed to delete submission file: %w", err)
	}
	return nil
}

// DeleteSubmission removes a submission entirely (cascades to its file rows and
// logs). Used by admins to remove a system art pack submission completely.
func (r *Repository) DeleteSubmission(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM submissions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete submission: %w", err)
	}
	return nil
}

// ListApprovedPackKeysByPacks returns, per pack, all object keys referenced by
// its approved submissions, in a single query (N+1 avoidance).
func (r *Repository) ListApprovedPackKeysByPacks(ctx context.Context, packIDs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(packIDs))
	if len(packIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT s.pack_id, sf.object_key
		 FROM submission_files sf
		 JOIN submissions s ON s.id = sf.submission_id
		 WHERE s.pack_id = ANY($1) AND s.status = 'approved'`,
		pq.Array(packIDs),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var packID, key string
		if err := rows.Scan(&packID, &key); err != nil {
			return nil, err
		}
		out[packID] = append(out[packID], key)
	}
	return out, rows.Err()
}

// UpdateSubmissionFileObjectKey rewrites a submission file's object key (used
// when an approved file is moved to its canonical location).
func (r *Repository) UpdateSubmissionFileObjectKey(ctx context.Context, fileID uuid.UUID, objectKey string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE submission_files SET object_key = $1 WHERE id = $2`, objectKey, fileID)
	if err != nil {
		return fmt.Errorf("failed to update submission file key: %w", err)
	}
	return nil
}

// UpdateSubmissionFileMime updates a submission file's MIME type (used when an
// approved image is normalized to WebP or a GIF is kept as-is).
func (r *Repository) UpdateSubmissionFileMime(ctx context.Context, fileID uuid.UUID, mime string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE submission_files SET mime_type = $1 WHERE id = $2`, mime, fileID)
	if err != nil {
		return fmt.Errorf("failed to update submission file mime: %w", err)
	}
	return nil
}

// DeleteSubmissionFileByObjectKey removes other files of a submission that
// already use the given object key (except the one being updated). This makes
// approval idempotent when a submission has duplicate file rows for the same
// system (e.g. a canonical row and a leftover review row).
func (r *Repository) DeleteSubmissionFileByObjectKey(ctx context.Context, submissionID uuid.UUID, objectKey string, excludeID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM submission_files WHERE submission_id = $1 AND object_key = $2 AND id <> $3`,
		submissionID, objectKey, excludeID,
	)
	if err != nil {
		return fmt.Errorf("failed to remove duplicate submission file key: %w", err)
	}
	return nil
}

// UserNamesByID returns a userID -> username map for the given ids.
func (r *Repository) UserNamesByID(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d::uuid", i+1)
		args[i] = id.String()
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, username FROM users WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// PackPreviousObjectKey returns the object_key of the most recent file for a
// given (kind, system) belonging to the pack, from a different submission
// (approved or rejected) than excludeID. Used to resolve the "old" image in a
// revision review. Returns ("", false, nil) when there is no previous file.
func (r *Repository) PackPreviousObjectKey(ctx context.Context, packID string, excludeID uuid.UUID, kind string, systemID string) (string, bool, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT sf.object_key
		FROM submission_files sf
		JOIN submissions s ON s.id = sf.submission_id
		WHERE s.pack_id = $1
		  AND s.id <> $2
		  AND sf.kind = $3
		  AND sf.system_id = $4
		  AND s.status IN ('approved', 'rejected')
		ORDER BY s.created_at DESC, sf.created_at DESC
		LIMIT 1`, packID, excludeID.String(), kind, systemID)
	var key string
	err := row.Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return key, true, nil
}

// GetDatabaseSize returns the total size of the database in bytes.
func (r *Repository) GetDatabaseSize(ctx context.Context) (int64, error) {
	var size int64
	err := r.db.QueryRowContext(ctx, `SELECT pg_database_size(current_database())`).Scan(&size)
	if err != nil {
		return 0, fmt.Errorf("failed to get database size: %w", err)
	}
	return size, nil
}
