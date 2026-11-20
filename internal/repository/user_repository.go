package repository

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"neoassets/internal/models"
)

const userColumns = `id, username, email, password_hash, role, hidden, xp, donor_status, avatar_key, email_verified,
	email_verification_token, email_verification_expires_at,
	password_reset_token, password_reset_expires_at, token_version, created_at, updated_at`

func scanUser(row interface{ Scan(...interface{}) error }) (*models.User, error) {
	var u models.User
	err := row.Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.Hidden, &u.XP, &u.DonorStatus, &u.AvatarKey, &u.EmailVerified,
		&u.EmailVerificationToken, &u.EmailVerificationExpires,
		&u.PasswordResetToken, &u.PasswordResetExpires,
		&u.TokenVersion, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser inserts a new user and returns it.
func (r *Repository) CreateUser(u *models.User) (*models.User, error) {
	now := time.Now()
	if u.Role == "" {
		u.Role = models.RoleUser
	}
	if u.DonorStatus == "" {
		u.DonorStatus = models.DonorNone
	}
	var id uuid.UUID
	err := r.db.QueryRow(
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
func (r *Repository) GetUserByID(id uuid.UUID) (*models.User, error) {
	row := r.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = $1`, id)
	return scanUser(row)
}

// GetUserByEmail returns a user by email, or sql.ErrNoRows.
func (r *Repository) GetUserByEmail(email string) (*models.User, error) {
	row := r.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE email = $1`, email)
	return scanUser(row)
}

// GetUserByUsername returns a user by username, or sql.ErrNoRows.
func (r *Repository) GetUserByUsername(username string) (*models.User, error) {
	row := r.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE username = $1`, username)
	return scanUser(row)
}

// UsernameExists reports whether any user (case-insensitively) has the username.
func (r *Repository) UsernameExists(username string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM users WHERE LOWER(username) = LOWER($1))`, username,
	).Scan(&exists)
	return exists, err
}

// UsernameExistsExcluding reports whether any other user (case-insensitively)
// has the username, ignoring the given id.
func (r *Repository) UsernameExistsExcluding(username string, excludeID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM users WHERE LOWER(username) = LOWER($1) AND id <> $2)`,
		username, excludeID,
	).Scan(&exists)
	return exists, err
}

// EmailExistsExcluding reports whether any other user has the email, ignoring id.
func (r *Repository) EmailExistsExcluding(email string, excludeID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM users WHERE LOWER(email) = LOWER($1) AND id <> $2)`,
		email, excludeID,
	).Scan(&exists)
	return exists, err
}

// GetUserByVerificationToken returns a user by email verification token,
// or sql.ErrNoRows.
func (r *Repository) GetUserByVerificationToken(token string) (*models.User, error) {
	row := r.db.QueryRow(
		`SELECT `+userColumns+` FROM users WHERE email_verification_token = $1`,
		token,
	)
	return scanUser(row)
}

// GetUserByPasswordResetToken returns a user by password reset token,
// or sql.ErrNoRows.
func (r *Repository) GetUserByPasswordResetToken(token string) (*models.User, error) {
	row := r.db.QueryRow(
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

// ProfileUpdate is a change to a user's username and/or email. Empty fields are
// left unchanged. A non-nil Verification replaces the email verification state
// (unverified, with a new token) and is only set together with a new Email.
type ProfileUpdate struct {
	Username     string
	Email        string
	Verification *EmailVerification
}

// EmailVerification is a pending email verification token.
type EmailVerification struct {
	TokenHash string
	Expires   time.Time
}

// UpdateProfile applies a profile change in a single statement, writing only
// the fields it changes. A new email also cancels any pending password reset,
// whose link went to the previous address. It reports whether the user exists.
func (r *Repository) UpdateProfile(id uuid.UUID, p ProfileUpdate) (bool, error) {
	reset := p.Verification != nil
	if reset && p.Email == "" {
		return false, fmt.Errorf("an email verification reset needs a new email")
	}
	var tokenHash *string
	var expires *time.Time
	if reset {
		tokenHash, expires = &p.Verification.TokenHash, &p.Verification.Expires
	}
	return r.execOne(
		`UPDATE users SET
			username = COALESCE(NULLIF($1, ''), username),
			email = COALESCE(NULLIF($2, ''), email),
			email_verified = CASE WHEN $3 THEN FALSE ELSE email_verified END,
			email_verification_token = CASE WHEN $3 THEN $4::text ELSE email_verification_token END,
			email_verification_expires_at = CASE WHEN $3 THEN $5::timestamptz ELSE email_verification_expires_at END,
			password_reset_token = CASE WHEN $2 <> '' THEN NULL ELSE password_reset_token END,
			password_reset_expires_at = CASE WHEN $2 <> '' THEN NULL ELSE password_reset_expires_at END,
			updated_at = NOW()
		 WHERE id = $6`,
		p.Username, p.Email, reset, tokenHash, expires, id,
	)
}

// SetEmailVerificationToken stores a new verification token if the account
// still has the email the caller read and is still unverified, so the token is
// only ever mailed to the address it can verify. It reports whether it applied.
func (r *Repository) SetEmailVerificationToken(id uuid.UUID, email, tokenHash string, expires time.Time) (bool, error) {
	return r.execOne(
		`UPDATE users SET email_verification_token = $3, email_verification_expires_at = $4, updated_at = NOW()
		 WHERE id = $1 AND email = $2 AND NOT email_verified`,
		id, email, tokenHash, expires,
	)
}

// MarkEmailVerified verifies the email if tokenHash is still the account's
// unexpired verification token, consuming it. It reports whether it applied.
func (r *Repository) MarkEmailVerified(id uuid.UUID, tokenHash string) (bool, error) {
	return r.execOne(
		`UPDATE users SET email_verified = TRUE, email_verification_token = NULL,
			email_verification_expires_at = NULL, updated_at = NOW()
		 WHERE id = $1 AND email_verification_token = $2 AND email_verification_expires_at > NOW()`,
		id, tokenHash,
	)
}

// SetPasswordResetToken stores a new password reset token if the account still
// has the email the caller read (the one the token is mailed to). It reports
// whether it applied.
func (r *Repository) SetPasswordResetToken(id uuid.UUID, email, tokenHash string, expires time.Time) (bool, error) {
	return r.execOne(
		`UPDATE users SET password_reset_token = $3, password_reset_expires_at = $4, updated_at = NOW()
		 WHERE id = $1 AND email = $2`,
		id, email, tokenHash, expires,
	)
}

// ResetPasswordWithToken sets a new password hash if tokenHash is still the
// account's unexpired reset token, consuming it and revoking every issued
// token. It reports whether it applied.
func (r *Repository) ResetPasswordWithToken(id uuid.UUID, tokenHash, newHash string) (bool, error) {
	return r.execOne(
		`UPDATE users SET password_hash = $3, password_reset_token = NULL, password_reset_expires_at = NULL,
			token_version = token_version + 1, updated_at = NOW()
		 WHERE id = $1 AND password_reset_token = $2 AND password_reset_expires_at > NOW()`,
		id, tokenHash, newHash,
	)
}

// ChangePasswordHash replaces the password hash if it is still checkedHash
// (the one the current password was verified against), revoking every issued
// token. It reports whether it applied.
func (r *Repository) ChangePasswordHash(id uuid.UUID, checkedHash, newHash string) (bool, error) {
	return r.execOne(
		`UPDATE users SET password_hash = $3, token_version = token_version + 1, updated_at = NOW()
		 WHERE id = $1 AND password_hash = $2`,
		id, checkedHash, newHash,
	)
}

// execOne runs a conditional single-row update and reports whether a row
// matched.
func (r *Repository) execOne(query string, args ...interface{}) (bool, error) {
	res, err := r.db.Exec(query, args...)
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
func (r *Repository) SetDonorStatus(id uuid.UUID, status string) (*models.User, error) {
	if _, err := r.db.Exec(`UPDATE users SET donor_status = $1, updated_at = NOW() WHERE id = $2`, status, id); err != nil {
		return nil, err
	}
	return r.GetUserByID(id)
}

// SetUserAvatar stores the R2 object key of a user's avatar and returns the
// updated user.
func (r *Repository) SetUserAvatar(id uuid.UUID, avatarKey string) (*models.User, error) {
	if _, err := r.db.Exec(`UPDATE users SET avatar_key = $1, updated_at = NOW() WHERE id = $2`, avatarKey, id); err != nil {
		return nil, err
	}
	return r.GetUserByID(id)
}

// ListUsers returns all non-hidden users (for role management). NeoBot (hidden)
// is never returned.
func (r *Repository) ListUsers() ([]models.User, error) {
	rows, err := r.db.Query(`SELECT ` + userColumns + ` FROM users WHERE NOT hidden ORDER BY username`)
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
func (r *Repository) SetUserRole(id uuid.UUID, role string) (*models.User, error) {
	if _, err := r.db.Exec(`UPDATE users SET role = $1, updated_at = NOW() WHERE id = $2`, role, id); err != nil {
		return nil, err
	}
	return r.GetUserByID(id)
}

// EnsureNeoBot creates the hidden NeoBot integration user if it does not exist.
// NeoBot is the origin/source of imported catalog data and is excluded from
// every list. Returns its id.
func (r *Repository) EnsureNeoBot(passwordHash string) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.QueryRow(
		`INSERT INTO users (username, email, password_hash, role, hidden, email_verified)
		 VALUES ('NeoBot', 'neobot@neostation.dev', $1, 'user', true, true)
		 ON CONFLICT (email) DO UPDATE SET hidden = true
		 RETURNING id`,
		passwordHash,
	).Scan(&id)
	return id, err
}
