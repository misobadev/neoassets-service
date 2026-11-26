package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"neoassets/internal/models"
)

const userColumns = `id, username, email, password_hash, role, hidden, xp, donor_status, avatar_key, email_verified,
	email_verification_token, email_verification_expires_at,
	password_reset_token, password_reset_expires_at,
	CASE WHEN pending_email_expires_at > NOW() THEN pending_email END, pending_email_token, pending_email_expires_at,
	token_version, created_at, updated_at`

func scanUser(row interface{ Scan(...interface{}) error }) (*models.User, error) {
	var u models.User
	err := row.Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.Hidden, &u.XP, &u.DonorStatus, &u.AvatarKey, &u.EmailVerified,
		&u.EmailVerificationToken, &u.EmailVerificationExpires,
		&u.PasswordResetToken, &u.PasswordResetExpires,
		&u.PendingEmail, &u.PendingEmailToken, &u.PendingEmailExpires,
		&u.TokenVersion, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser inserts a new user and returns it.
func (r *Repository) CreateUser(ctx context.Context, u *models.User) (*models.User, error) {
	now := time.Now()
	if u.Role == "" {
		u.Role = models.RoleUser
	}
	if u.DonorStatus == "" {
		u.DonorStatus = models.DonorNone
	}
	var id uuid.UUID
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, hidden, xp, donor_status, email_verified,
			email_verification_token, email_verification_expires_at, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 RETURNING id`,
		u.Username, u.Email, u.PasswordHash, u.Role, u.Hidden, u.XP, u.DonorStatus, u.EmailVerified,
		u.EmailVerificationToken, u.EmailVerificationExpires, now, now,
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	u.ID = id
	u.CreatedAt = now
	u.UpdatedAt = now
	return u, nil
}

// GetUserByID returns a user by ID, or sql.ErrNoRows.
func (r *Repository) GetUserByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id)
	return scanUser(row)
}

// GetUserByEmail returns a user by email, or sql.ErrNoRows.
func (r *Repository) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, email)
	return scanUser(row)
}

// GetUserByUsername returns a user by username, or sql.ErrNoRows.
func (r *Repository) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = $1`, username)
	return scanUser(row)
}

// UsernameExists reports whether any user (case-insensitively) has the username.
func (r *Repository) UsernameExists(ctx context.Context, username string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE LOWER(username) = LOWER($1))`, username,
	).Scan(&exists)
	return exists, err
}

// UsernameExistsExcluding reports whether any other user (case-insensitively)
// has the username, ignoring the given id.
func (r *Repository) UsernameExistsExcluding(ctx context.Context, username string, excludeID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE LOWER(username) = LOWER($1) AND id <> $2)`,
		username, excludeID,
	).Scan(&exists)
	return exists, err
}

// EmailExists reports whether any user has the email, in any letter case.
func (r *Repository) EmailExists(ctx context.Context, email string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE LOWER(email) = LOWER($1))`, email,
	).Scan(&exists)
	return exists, err
}

// EmailExistsExcluding reports whether any other user has the email, ignoring id.
func (r *Repository) EmailExistsExcluding(ctx context.Context, email string, excludeID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE LOWER(email) = LOWER($1) AND id <> $2)`,
		email, excludeID,
	).Scan(&exists)
	return exists, err
}

// GetUserByVerificationToken returns a user by email verification token,
// or sql.ErrNoRows.
func (r *Repository) GetUserByVerificationToken(ctx context.Context, token string) (*models.User, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE email_verification_token = $1`,
		token,
	)
	return scanUser(row)
}

// GetUserByPasswordResetToken returns a user by password reset token,
// or sql.ErrNoRows.
func (r *Repository) GetUserByPasswordResetToken(ctx context.Context, token string) (*models.User, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE password_reset_token = $1`,
		token,
	)
	return scanUser(row)
}

// The account updates below each write only the columns their operation
// changes, so a concurrent request's write to any other column is never
// overwritten with a value read earlier. Values that more than one operation
// changes are updated in place (token_version) or only if they still hold
// what the caller checked (password_hash, one-time tokens).

// ProfileUpdate is a change to a user's username and/or a new email address
// waiting for confirmation. Empty or nil fields are left unchanged. The address
// in use only changes through ConfirmPendingEmail.
type ProfileUpdate struct {
	Username string
	Pending  *PendingEmail
}

// PendingEmail is a requested email change. CheckedHash is the password hash
// the caller verified the current password against: the change is only
// recorded while the password is still that one.
type PendingEmail struct {
	CheckedHash string
	Email       string
	TokenHash   string
	Expires     time.Time
}

// UpdateProfile applies a profile change in a single statement, writing only
// the fields it changes, so either all of it applies or none does. A pending
// email replaces any earlier one. It reports whether it applied (the user
// exists and, with a pending email, the password is unchanged).
func (r *Repository) UpdateProfile(ctx context.Context, id uuid.UUID, p ProfileUpdate) (bool, error) {
	pending := p.Pending != nil
	var checkedHash, email, tokenHash *string
	var expires *time.Time
	if pending {
		checkedHash, email, tokenHash, expires = &p.Pending.CheckedHash, &p.Pending.Email, &p.Pending.TokenHash, &p.Pending.Expires
	}
	return r.execOne(ctx,
		`UPDATE users SET
			username = COALESCE(NULLIF($1, ''), username),
			pending_email = CASE WHEN $3 THEN $4::text ELSE pending_email END,
			pending_email_token = CASE WHEN $3 THEN $5::text ELSE pending_email_token END,
			pending_email_expires_at = CASE WHEN $3 THEN $6::timestamptz ELSE pending_email_expires_at END,
			updated_at = NOW()
		 WHERE id = $2 AND (NOT $3 OR password_hash = $7::text)`,
		p.Username, id, pending, email, tokenHash, expires, checkedHash,
	)
}

// ErrEmailTaken reports that another account holds the address.
var ErrEmailTaken = errors.New("email already exists")

// EmailChange is a confirmed email change.
type EmailChange struct {
	UserID   uuid.UUID
	Username string
	OldEmail string
	NewEmail string
}

// ConfirmPendingEmail switches account userID to its pending address if
// tokenHash is that change's unexpired token, in one statement: the address
// becomes the verified email, the pending and reset tokens are consumed and
// every issued session is revoked (token_version). The row is locked, so a
// password change or reset committing first (which drops the pending change)
// wins. It applies only while no other account holds the address (in any
// letter case); ErrEmailTaken reports that case. ok is false when the account
// has no unexpired pending change with this token.
func (r *Repository) ConfirmPendingEmail(ctx context.Context, userID uuid.UUID, tokenHash string) (change EmailChange, ok bool, err error) {
	var taken bool
	err = r.db.QueryRowContext(ctx,
		`WITH target AS (
			SELECT id, email AS old_email, pending_email FROM users
			WHERE id = $2 AND pending_email_token = $1 AND pending_email_expires_at > NOW()
			FOR UPDATE
		), switched AS (
			UPDATE users u SET email = t.pending_email, email_verified = TRUE,
				pending_email = NULL, pending_email_token = NULL, pending_email_expires_at = NULL,
				email_verification_token = NULL, email_verification_expires_at = NULL,
				password_reset_token = NULL, password_reset_expires_at = NULL,
				token_version = u.token_version + 1, updated_at = NOW()
			FROM target t
			WHERE u.id = t.id AND u.pending_email_token = $1 AND NOT EXISTS (
				SELECT 1 FROM users o WHERE LOWER(o.email) = LOWER(t.pending_email) AND o.id <> t.id)
			RETURNING u.id, u.username, t.old_email, u.email
		)
		SELECT s.id, s.username, s.old_email, s.email, FALSE FROM switched s
		UNION ALL
		SELECT t.id, '', t.old_email, t.pending_email, TRUE FROM target t
		WHERE NOT EXISTS (SELECT 1 FROM switched)`,
		tokenHash, userID,
	).Scan(&change.UserID, &change.Username, &change.OldEmail, &change.NewEmail, &taken)
	if errors.Is(err, sql.ErrNoRows) {
		return EmailChange{}, false, nil
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		// Another account took the exact address between the check and the write.
		return EmailChange{}, false, ErrEmailTaken
	}
	if err != nil {
		return EmailChange{}, false, err
	}
	if taken {
		return EmailChange{}, false, ErrEmailTaken
	}
	return change, true, nil
}

// SetEmailVerificationToken stores a new verification token if the account
// still has the email the caller read and is still unverified, so the token is
// only ever mailed to the address it can verify, and if no token newer than the
// cooldown exists (the previous one expires no later than notAfter), checked in
// the same statement so concurrent requests can't each mint one. It reports
// whether it applied.
func (r *Repository) SetEmailVerificationToken(ctx context.Context, id uuid.UUID, email, tokenHash string, expires, notAfter time.Time) (bool, error) {
	return r.execOne(ctx,
		`UPDATE users SET email_verification_token = $3, email_verification_expires_at = $4, updated_at = NOW()
		 WHERE id = $1 AND email = $2 AND NOT email_verified
		   AND (email_verification_expires_at IS NULL OR email_verification_expires_at <= $5)`,
		id, email, tokenHash, expires, notAfter,
	)
}

// MarkEmailVerified verifies the email if tokenHash is still the account's
// unexpired verification token, consuming it. It reports whether it applied.
func (r *Repository) MarkEmailVerified(ctx context.Context, id uuid.UUID, tokenHash string) (bool, error) {
	return r.execOne(ctx,
		`UPDATE users SET email_verified = TRUE, email_verification_token = NULL,
			email_verification_expires_at = NULL, updated_at = NOW()
		 WHERE id = $1 AND email_verification_token = $2 AND email_verification_expires_at > NOW()`,
		id, tokenHash,
	)
}

// SetPasswordResetToken stores a new password reset token if the account still
// has the email the caller read (the one the token is mailed to) and no reset
// token newer than the cooldown exists (the previous one expires no later than
// notAfter), checked in the same statement. It reports whether it applied.
func (r *Repository) SetPasswordResetToken(ctx context.Context, id uuid.UUID, email, tokenHash string, expires, notAfter time.Time) (bool, error) {
	return r.execOne(ctx,
		`UPDATE users SET password_reset_token = $3, password_reset_expires_at = $4, updated_at = NOW()
		 WHERE id = $1 AND email = $2
		   AND (password_reset_expires_at IS NULL OR password_reset_expires_at <= $5)`,
		id, email, tokenHash, expires, notAfter,
	)
}

// ResetPasswordWithToken sets a new password hash if tokenHash is still the
// account's unexpired reset token, consuming it, revoking every issued token
// and dropping any email change still waiting (it may not be the owner's). It
// reports whether it applied.
func (r *Repository) ResetPasswordWithToken(ctx context.Context, id uuid.UUID, tokenHash, newHash string) (bool, error) {
	return r.execOne(ctx,
		`UPDATE users SET password_hash = $3, password_reset_token = NULL, password_reset_expires_at = NULL,
			pending_email = NULL, pending_email_token = NULL, pending_email_expires_at = NULL,
			token_version = token_version + 1, updated_at = NOW()
		 WHERE id = $1 AND password_reset_token = $2 AND password_reset_expires_at > NOW()`,
		id, tokenHash, newHash,
	)
}

// ChangePasswordHash replaces the password hash if it is still checkedHash
// (the one the current password was verified against), revoking every issued
// token and dropping any email change still waiting. It reports whether it
// applied.
func (r *Repository) ChangePasswordHash(ctx context.Context, id uuid.UUID, checkedHash, newHash string) (bool, error) {
	return r.execOne(ctx,
		`UPDATE users SET password_hash = $3, token_version = token_version + 1,
			pending_email = NULL, pending_email_token = NULL, pending_email_expires_at = NULL, updated_at = NOW()
		 WHERE id = $1 AND password_hash = $2`,
		id, checkedHash, newHash,
	)
}

// execOne runs a conditional single-row update and reports whether a row
// matched.
func (r *Repository) execOne(ctx context.Context, query string, args ...interface{}) (bool, error) {
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// SetDonorStatus updates a user's donor status and returns the updated user.
func (r *Repository) SetDonorStatus(ctx context.Context, id uuid.UUID, status string) (*models.User, error) {
	if _, err := r.db.ExecContext(ctx, `UPDATE users SET donor_status = $1, updated_at = NOW() WHERE id = $2`, status, id); err != nil {
		return nil, err
	}
	return r.GetUserByID(ctx, id)
}

// SetUserAvatar stores the R2 object key of a user's avatar and returns the
// updated user.
func (r *Repository) SetUserAvatar(ctx context.Context, id uuid.UUID, avatarKey string) (*models.User, error) {
	if _, err := r.db.ExecContext(ctx, `UPDATE users SET avatar_key = $1, updated_at = NOW() WHERE id = $2`, avatarKey, id); err != nil {
		return nil, err
	}
	return r.GetUserByID(ctx, id)
}

// ListUsers returns all non-hidden users (for role management). NeoBot (hidden)
// is never returned.
func (r *Repository) ListUsers(ctx context.Context) ([]models.User, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users WHERE NOT hidden ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// SetUserRole updates a user's role and returns the updated user.
func (r *Repository) SetUserRole(ctx context.Context, id uuid.UUID, role string) (*models.User, error) {
	if _, err := r.db.ExecContext(ctx, `UPDATE users SET role = $1, updated_at = NOW() WHERE id = $2`, role, id); err != nil {
		return nil, err
	}
	return r.GetUserByID(ctx, id)
}

// EnsureNeoBot creates the hidden NeoBot integration user if it does not exist.
// NeoBot is the origin/source of imported catalog data and is excluded from
// every list. Returns its id.
func (r *Repository) EnsureNeoBot(ctx context.Context, passwordHash string) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO users (username, email, password_hash, role, hidden, email_verified)
		 VALUES ('NeoBot', 'neobot@neostation.dev', $1, 'user', true, true)
		 ON CONFLICT (email) DO UPDATE SET hidden = true
		 RETURNING id`,
		passwordHash,
	).Scan(&id)
	return id, err
}
