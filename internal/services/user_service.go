package services

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"neoassets/internal/email"
	"neoassets/internal/models"
	"neoassets/internal/repository"
	"neoassets/pkg/auth"
)

const (
	verificationTokenTTL = 24 * time.Hour
	resetTokenTTL        = 1 * time.Hour
	resetCooldown        = 1 * time.Minute
)

var (
	usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9]{3,20}$`)
	emailRegex    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// UserService handles account registration, authentication and recovery.
type UserService struct {
	repo                  *repository.Repository
	mailer                *email.Sender
	jwtSecret             string
	tokenTTL              time.Duration
	protectedAdminEmail   string
	SkipEmailVerification bool
}

// NewUserService creates a UserService.
func NewUserService(repo *repository.Repository, mailer *email.Sender, jwtSecret string, tokenTTL time.Duration, protectedAdminEmail string) *UserService {
	return &UserService{
		repo:                repo,
		mailer:              mailer,
		jwtSecret:           jwtSecret,
		tokenTTL:            tokenTTL,
		protectedAdminEmail: protectedAdminEmail,
	}
}

// Register validates the payload, creates the user and sends a verification
// email. No JWT is issued until the email is verified, so an unverified account
// cannot use the API. It never fails because the email could not be sent.
func (s *UserService) Register(ctx context.Context, req models.RegisterRequest) (*models.AuthResult, error) {
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	if !usernameRegex.MatchString(req.Username) {
		return nil, fmt.Errorf("username must be 3-20 alphanumeric characters")
	}
	if !emailRegex.MatchString(req.Email) {
		return nil, fmt.Errorf("invalid email address")
	}
	if len(req.Password) < 8 {
		return nil, fmt.Errorf("password must be at least 8 characters")
	}

	if _, err := s.repo.GetUserByUsername(ctx, req.Username); err == nil {
		return nil, fmt.Errorf("username already exists")
	} else if err != sql.ErrNoRows {
		return nil, err
	}
	if exists, err := s.repo.UsernameExists(ctx, req.Username); err != nil {
		return nil, err
	} else if exists {
		return nil, fmt.Errorf("username already exists")
	}
	if _, err := s.repo.GetUserByEmail(ctx, req.Email); err == nil {
		return nil, fmt.Errorf("email already exists")
	} else if err != sql.ErrNoRows {
		return nil, err
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	// Only the hash of the emailed token is stored; the plaintext exists only in
	// the email link.
	hashed := auth.HashToken(token)
	now := time.Now()
	expires := now.Add(verificationTokenTTL)

	user, err := s.repo.CreateUser(ctx, &models.User{
		Username:                 req.Username,
		Email:                    req.Email,
		PasswordHash:             hash,
		EmailVerified:            false,
		EmailVerificationToken:   &hashed,
		EmailVerificationExpires: &expires,
	})
	if err != nil {
		return nil, err
	}

	if s.mailer != nil {
		if err := s.mailer.SendVerificationEmail(user.Email, user.Username, token); err != nil {
			// Registration still succeeds; the user can resend the email.
			log.Error().Str("email", user.Email).Err(err).Msg("failed to send verification email")
		}
	}

	// The account is not usable until its email is verified: do not hand out a
	// signed token here.
	return &models.AuthResult{User: *user}, nil
}

// Login authenticates a user and returns a JWT. Admins get an extra admin
// token so they can use the admin panel from the same session.
func (s *UserService) Login(ctx context.Context, req models.UserLoginRequest) (*models.AuthResult, error) {
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	user, err := s.repo.GetUserByEmail(ctx, req.Email)
	if err != nil {
		log.Warn().Str("email", req.Email).Msg("failed login: unknown email")
		return nil, fmt.Errorf("invalid credentials")
	}
	// Check verification before the password so the endpoint never reveals
	// whether the supplied password was correct.
	if !s.SkipEmailVerification && !user.EmailVerified {
		log.Warn().Str("email", req.Email).Msg("failed login: email not verified")
		return nil, fmt.Errorf("email not verified")
	}
	if !auth.CheckPassword(user.PasswordHash, req.Password) {
		log.Warn().Str("email", req.Email).Msg("failed login: wrong password")
		return nil, fmt.Errorf("invalid credentials")
	}

	jwt, err := auth.GenerateUserToken(s.jwtSecret, user.ID, user.Email, user.Role, user.TokenVersion, s.tokenTTL)
	if err != nil {
		return nil, err
	}

	result := &models.AuthResult{User: *user, Token: jwt}
	if user.Role == models.RoleAdmin {
		adminToken, err := auth.GenerateToken(s.jwtSecret, user.ID, user.Email, user.TokenVersion, auth.AdminTokenTTL)
		if err != nil {
			return nil, err
		}
		result.IsAdmin = true
		result.AdminToken = adminToken
	}

	return result, nil
}

// TokenTTL is the lifetime of an issued user session token (used to size the
// session cookie).
func (s *UserService) TokenTTL() time.Duration { return s.tokenTTL }

// VerifyEmail marks a user's email as verified using the emailed token.
func (s *UserService) VerifyEmail(ctx context.Context, token string) error {
	user, err := s.repo.GetUserByVerificationToken(ctx, auth.HashToken(token))
	if err != nil {
		return fmt.Errorf("invalid verification token")
	}
	if user.EmailVerified {
		return nil
	}
	if user.EmailVerificationExpires == nil || time.Now().After(*user.EmailVerificationExpires) {
		return fmt.Errorf("verification token has expired")
	}

	ok, err := s.repo.MarkEmailVerified(ctx, user.ID, auth.HashToken(token))
	if err != nil {
		return err
	}
	if !ok {
		// The link may have been opened twice (or by a mail scanner first).
		if current, err := s.repo.GetUserByID(ctx, user.ID); err == nil && current.EmailVerified {
			return nil
		}
		return fmt.Errorf("invalid verification token")
	}
	// Attach donations already made with this email so users who donated before
	// registering get their donor benefits automatically.
	s.linkPendingDonations(ctx, user.ID, user.Email)
	return nil
}

// linkPendingDonations attaches unclaimed donations matching the account email
// and refreshes the donor status. Safe to call repeatedly: linking is a no-op
// once every matching event is claimed.
func (s *UserService) linkPendingDonations(ctx context.Context, userID uuid.UUID, email string) {
	if _, err := s.repo.LinkDonationEventsByEmail(ctx, userID, email); err != nil {
		log.Warn().Err(err).Str("user_id", userID.String()).Msg("failed to link pending donations")
		return
	}
	if _, err := s.repo.RecomputeDonorStatus(ctx, userID); err != nil {
		log.Warn().Err(err).Str("user_id", userID.String()).Msg("failed to refresh donor status")
	}
}

// ResendVerification issues a fresh verification token and emails it. The
// response is generic to avoid revealing whether an account exists.
func (s *UserService) ResendVerification(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	user, err := s.repo.GetUserByEmail(ctx, email)
	if err != nil {
		// Do not reveal whether the account exists.
		return nil
	}
	if user.EmailVerified {
		// Already verified; still return success so the endpoint is not an
		// oracle for account existence.
		return nil
	}

	token, err := newToken()
	if err != nil {
		return err
	}
	expires := time.Now().Add(verificationTokenTTL)
	ok, err := s.repo.SetEmailVerificationToken(ctx, user.ID, user.Email, auth.HashToken(token), expires)
	if err != nil {
		return err
	}
	if !ok {
		// The email changed or was verified meanwhile: nothing to send here.
		return nil
	}

	if s.mailer != nil {
		if err := s.mailer.SendVerificationEmail(user.Email, user.Username, token); err != nil {
			return err
		}
	}
	return nil
}

// ForgotPassword issues a reset token and emails it. Unknown emails still get
// a success response to avoid account enumeration.
func (s *UserService) ForgotPassword(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	user, err := s.repo.GetUserByEmail(ctx, email)
	if err != nil {
		return nil
	}

	// Cooldown: only send one reset email per minute.
	if user.PasswordResetExpires != nil {
		timeLeft := time.Until(*user.PasswordResetExpires)
		if timeLeft > resetTokenTTL-resetCooldown {
			return nil
		}
	}

	token, err := newToken()
	if err != nil {
		return err
	}
	expires := time.Now().Add(resetTokenTTL)
	ok, err := s.repo.SetPasswordResetToken(ctx, user.ID, user.Email, auth.HashToken(token), expires)
	if err != nil {
		return err
	}
	if !ok {
		// The email changed meanwhile: don't mail a reset link to the old one.
		return nil
	}

	if s.mailer != nil {
		if err := s.mailer.SendPasswordResetEmail(user.Email, user.Username, token); err != nil {
			return err
		}
	}
	return nil
}

// ResetPassword sets a new password using the emailed token.
func (s *UserService) ResetPassword(ctx context.Context, token, newPassword string) error {
	if token == "" {
		return fmt.Errorf("reset token is required")
	}
	if len(newPassword) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}

	user, err := s.repo.GetUserByPasswordResetToken(ctx, auth.HashToken(token))
	if err != nil {
		return fmt.Errorf("invalid or expired reset token")
	}
	if user.PasswordResetExpires == nil || time.Now().After(*user.PasswordResetExpires) {
		return fmt.Errorf("reset token has expired")
	}

	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	// Consumes the token and revokes every previously issued token for this
	// account; a token already used (or expired meanwhile) changes nothing.
	ok, err := s.repo.ResetPasswordWithToken(ctx, user.ID, auth.HashToken(token), hash)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("invalid or expired reset token")
	}
	return nil
}

// GetUserByID returns a user by ID.
func (s *UserService) GetUserByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
	user, err := s.repo.GetUserByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	return user, nil
}

// UserAuthState returns the user's current role, verification flag and token
// version from the database in a single lookup. It backs the defense-in-depth
// authorization checks so a stale token claim (role, or a token revoked by a
// password change) can never be trusted.
func (s *UserService) UserAuthState(ctx context.Context, id uuid.UUID) (role string, verified bool, tokenVersion int, err error) {
	user, err := s.repo.GetUserByID(ctx, id)
	if err != nil {
		return "", false, 0, err
	}
	return user.Role, user.EmailVerified, user.TokenVersion, nil
}

// SetDonorStatus updates a user's donor status (none | supporter | monthly_supporter).
// Donations only grant extra threads, never XP or rank.
func (s *UserService) SetDonorStatus(ctx context.Context, id uuid.UUID, status string) (*models.User, error) {
	switch status {
	case models.DonorNone, models.DonorSupporter, models.DonorMonthlySupporter:
	default:
		return nil, fmt.Errorf("invalid donor status %q", status)
	}
	updated, err := s.repo.SetDonorStatus(ctx, id, status)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	log.Warn().Str("user_id", id.String()).Str("donor_status", status).Msg("user donor status changed")
	return updated, nil
}

// ListUsers returns every non-hidden user (NeoBot excluded). Admin only.
func (s *UserService) ListUsers(ctx context.Context) ([]models.User, error) {
	list, err := s.repo.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if s.protectedAdminEmail != "" && strings.EqualFold(list[i].Email, s.protectedAdminEmail) {
			list[i].Protected = true
		}
	}
	return list, nil
}

// SetUserRole updates a user's role (user | reviewer | admin). Admin only.
// The protected root account can never be demoted below admin.
func (s *UserService) SetUserRole(ctx context.Context, id uuid.UUID, role string) (*models.User, error) {
	switch role {
	case models.RoleUser, models.RoleReviewer, models.RoleAdmin:
	default:
		return nil, fmt.Errorf("invalid role %q", role)
	}
	user, err := s.repo.GetUserByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	if s.protectedAdminEmail != "" && strings.EqualFold(user.Email, s.protectedAdminEmail) {
		return nil, fmt.Errorf("this account's role is locked and cannot be changed")
	}
	updated, err := s.repo.SetUserRole(ctx, id, role)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	log.Warn().Str("user_id", id.String()).Str("role", role).Msg("user role changed")
	return updated, nil
}

// UpdateProfile updates a user's username and/or email. Fields may be empty to
// leave them unchanged. Usernames are required to be present and unique.
func (s *UserService) UpdateProfile(ctx context.Context, id uuid.UUID, username, email string) (*models.User, error) {
	user, err := s.repo.GetUserByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	newUsername, newEmail := "", ""
	if strings.TrimSpace(username) != "" && strings.TrimSpace(username) != user.Username {
		uname := strings.TrimSpace(username)
		if !usernameRegex.MatchString(uname) {
			return nil, fmt.Errorf("username must be 3-20 alphanumeric characters")
		}
		exists, err := s.repo.UsernameExistsExcluding(ctx, uname, id)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("username already exists")
		}
		newUsername = uname
	}
	if strings.TrimSpace(email) != "" && strings.ToLower(strings.TrimSpace(email)) != strings.ToLower(user.Email) {
		newEmail = strings.ToLower(strings.TrimSpace(email))
		if !emailRegex.MatchString(newEmail) {
			return nil, fmt.Errorf("invalid email address")
		}
		exists, err := s.repo.EmailExistsExcluding(ctx, newEmail, id)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("email already exists")
		}
	}
	if newUsername == "" && newEmail == "" {
		return user, nil
	}
	// One statement writing only the fields that changed, so nothing else on
	// the account (password, token version, role, XP, verification) is
	// overwritten with the values read above.
	update := repository.ProfileUpdate{Username: newUsername, Email: newEmail}
	// A new email address must be proven: the verified flag is cleared and a
	// fresh verification token is emailed, and until then the account's tokens
	// no longer grant access.
	var verifyToken string
	if newEmail != "" && !s.SkipEmailVerification {
		verifyToken, err = newToken()
		if err != nil {
			return nil, err
		}
		update.Verification = &repository.EmailVerification{
			TokenHash: auth.HashToken(verifyToken),
			Expires:   time.Now().Add(verificationTokenTTL),
		}
	}
	ok, err := s.repo.UpdateProfile(ctx, id, update)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("user not found")
	}
	if update.Verification != nil && s.mailer != nil {
		name := user.Username
		if newUsername != "" {
			name = newUsername
		}
		if err := s.mailer.SendVerificationEmail(newEmail, name, verifyToken); err != nil {
			log.Error().Str("email", newEmail).Err(err).Msg("failed to send verification email after email change")
		}
	}
	return s.repo.GetUserByID(ctx, id)
}

// ChangePassword updates a user's password after verifying the current one.
func (s *UserService) ChangePassword(ctx context.Context, id uuid.UUID, currentPassword, newPassword string) error {
	user, err := s.repo.GetUserByID(ctx, id)
	if err != nil {
		return fmt.Errorf("user not found")
	}
	if !auth.CheckPassword(user.PasswordHash, currentPassword) {
		return fmt.Errorf("current password is incorrect")
	}
	if len(newPassword) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	// Applies only if the password is still the one just checked, and revokes
	// every previously issued token for this account.
	ok, err := s.repo.ChangePasswordHash(ctx, user.ID, user.PasswordHash, hash)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("the password was changed by another request, please try again")
	}
	return nil
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
