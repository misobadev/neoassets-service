package repository

import (
	"time"

	"github.com/google/uuid"

	"neoassets/internal/models"
)

const userColumns = `id, username, email, password_hash, role, hidden, xp, donor_status, avatar_key, email_verified,
	email_verification_token, email_verification_expires_at,
	password_reset_token, password_reset_expires_at, created_at, updated_at`

func scanUser(row interface{ Scan(...interface{}) error }) (*models.User, error) {
	var u models.User
	err := row.Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.Hidden, &u.XP, &u.DonorStatus, &u.AvatarKey, &u.EmailVerified,
		&u.EmailVerificationToken, &u.EmailVerificationExpires,
		&u.PasswordResetToken, &u.PasswordResetExpires,
		&u.CreatedAt, &u.UpdatedAt,
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

// UpdateUser persists every mutable field of a user, keyed by ID.
func (r *Repository) UpdateUser(u *models.User) error {
	_, err := r.db.Exec(
		`UPDATE users SET
			username = $1, email = $2, password_hash = $3, role = $4, hidden = $5, xp = $6, donor_status = $7, email_verified = $8,
			email_verification_token = $9, email_verification_expires_at = $10,
			password_reset_token = $11, password_reset_expires_at = $12,
			updated_at = NOW()
		 WHERE id = $13`,
		u.Username, u.Email, u.PasswordHash, u.Role, u.Hidden, u.XP, u.DonorStatus, u.EmailVerified,
		u.EmailVerificationToken, u.EmailVerificationExpires,
		u.PasswordResetToken, u.PasswordResetExpires, u.ID,
	)
	return err
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
