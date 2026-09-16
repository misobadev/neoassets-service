package services

import (
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
func (s *UserService) Register(req models.RegisterRequest) (*models.AuthResult, error) {
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

	if _, err := s.repo.GetUserByUsername(req.Username); err == nil {
		return nil, fmt.Errorf("username already exists")
	} else if err != sql.ErrNoRows {
		return nil, err
	}
	if exists, err := s.repo.UsernameExists(req.Username); err != nil {
		return nil, err
	} else if exists {
		return nil, fmt.Errorf("username already exists")
	}
	if _, err := s.repo.GetUserByEmail(req.Email); err == nil {
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
	now := time.Now()
	expires := now.Add(verificationTokenTTL)

	user, err := s.repo.CreateUser(&models.User{
		Username:                 req.Username,
		Email:                    req.Email,
		PasswordHash:             hash,
		EmailVerified:            false,
		EmailVerificationToken:   &token,
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
func (s *UserService) Login(req models.UserLoginRequest) (*models.AuthResult, error) {
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	user, err := s.repo.GetUserByEmail(req.Email)
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

	jwt, err := auth.GenerateUserToken(s.jwtSecret, user.ID, user.Email, user.Role, s.tokenTTL)
	if err != nil {
		return nil, err
	}

	result := &models.AuthResult{User: *user, Token: jwt}
	if user.Role == models.RoleAdmin {
		adminToken, err := auth.GenerateToken(s.jwtSecret, user.ID, user.Email, s.tokenTTL)
		if err != nil {
			return nil, err
		}
		result.IsAdmin = true
		result.AdminToken = adminToken
	}

	return result, nil
}

// VerifyEmail marks a user's email as verified using the emailed token.
func (s *UserService) VerifyEmail(token string) error {
	user, err := s.repo.GetUserByVerificationToken(token)
	if err != nil {
		return fmt.Errorf("invalid verification token")
	}
	if user.EmailVerified {
		return nil
	}
	if user.EmailVerificationExpires == nil || time.Now().After(*user.EmailVerificationExpires) {
		return fmt.Errorf("verification token has expired")
	}

	user.EmailVerified = true
	user.EmailVerificationToken = nil
	user.EmailVerificationExpires = nil
	if err := s.repo.UpdateUser(user); err != nil {
		return err
	}
	// Attach donations already made with this email so users who donated before
	// registering get their donor benefits automatically.
	s.linkPendingDonations(user.ID, user.Email)
	return nil
}

// linkPendingDonations attaches unclaimed donations matching the account email
// and refreshes the donor status. Safe to call repeatedly: linking is a no-op
// once every matching event is claimed.
func (s *UserService) linkPendingDonations(userID uuid.UUID, email string) {
	if _, err := s.repo.LinkDonationEventsByEmail(userID, email); err != nil {
		log.Warn().Err(err).Str("user_id", userID.String()).Msg("failed to link pending donations")
		return
	}
	if _, err := s.repo.RecomputeDonorStatus(userID); err != nil {
		log.Warn().Err(err).Str("user_id", userID.String()).Msg("failed to refresh donor status")
	}
}

// ResendVerification issues a fresh verification token and emails it. The
// response is generic to avoid revealing whether an account exists.
func (s *UserService) ResendVerification(email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	user, err := s.repo.GetUserByEmail(email)
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
	user.EmailVerificationToken = &token
	user.EmailVerificationExpires = &expires
	if err := s.repo.UpdateUser(user); err != nil {
		return err
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
func (s *UserService) ForgotPassword(email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	user, err := s.repo.GetUserByEmail(email)
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
	user.PasswordResetToken = &token
	user.PasswordResetExpires = &expires
	if err := s.repo.UpdateUser(user); err != nil {
		return err
	}

	if s.mailer != nil {
		if err := s.mailer.SendPasswordResetEmail(user.Email, user.Username, token); err != nil {
			return err
		}
	}
	return nil
}

// ResetPassword sets a new password using the emailed token.
func (s *UserService) ResetPassword(token, newPassword string) error {
	if token == "" {
		return fmt.Errorf("reset token is required")
	}
	if len(newPassword) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}

	user, err := s.repo.GetUserByPasswordResetToken(token)
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
	user.PasswordHash = hash
	user.PasswordResetToken = nil
	user.PasswordResetExpires = nil
	if err := s.repo.UpdateUser(user); err != nil {
		return err
	}
	return nil
}

// GetUserByID returns a user by ID.
func (s *UserService) GetUserByID(id uuid.UUID) (*models.User, error) {
	user, err := s.repo.GetUserByID(id)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	return user, nil
}

// UserRole returns the user's current role from the database. It is used for
// defense-in-depth authorization instead of trusting the token's role claim.
func (s *UserService) UserRole(id uuid.UUID) (string, error) {
	user, err := s.repo.GetUserByID(id)
	if err != nil {
		return "", err
	}
	return user.Role, nil
}

// UserEmailVerified reports whether the user's email is currently verified.
func (s *UserService) UserEmailVerified(id uuid.UUID) (bool, error) {
	user, err := s.repo.GetUserByID(id)
	if err != nil {
		return false, err
	}
	return user.EmailVerified, nil
}

// SetDonorStatus updates a user's donor status (none | supporter | monthly_supporter).
// Donations only grant extra threads, never XP or rank.
func (s *UserService) SetDonorStatus(id uuid.UUID, status string) (*models.User, error) {
	switch status {
	case models.DonorNone, models.DonorSupporter, models.DonorMonthlySupporter:
	default:
		return nil, fmt.Errorf("invalid donor status %q", status)
	}
	updated, err := s.repo.SetDonorStatus(id, status)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	log.Warn().Str("user_id", id.String()).Str("donor_status", status).Msg("user donor status changed")
	return updated, nil
}

// ListUsers returns every non-hidden user (NeoBot excluded). Admin only.
func (s *UserService) ListUsers() ([]models.User, error) {
	list, err := s.repo.ListUsers()
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
func (s *UserService) SetUserRole(id uuid.UUID, role string) (*models.User, error) {
	switch role {
	case models.RoleUser, models.RoleReviewer, models.RoleAdmin:
	default:
		return nil, fmt.Errorf("invalid role %q", role)
	}
	user, err := s.repo.GetUserByID(id)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	if s.protectedAdminEmail != "" && strings.EqualFold(user.Email, s.protectedAdminEmail) {
		return nil, fmt.Errorf("this account's role is locked and cannot be changed")
	}
	updated, err := s.repo.SetUserRole(id, role)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	log.Warn().Str("user_id", id.String()).Str("role", role).Msg("user role changed")
	return updated, nil
}

// UpdateProfile updates a user's username and/or email. Fields may be empty to
// leave them unchanged. Usernames are required to be present and unique.
func (s *UserService) UpdateProfile(id uuid.UUID, username, email string) (*models.User, error) {
	user, err := s.repo.GetUserByID(id)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	if strings.TrimSpace(username) != "" && strings.TrimSpace(username) != user.Username {
		uname := strings.TrimSpace(username)
		if !usernameRegex.MatchString(uname) {
			return nil, fmt.Errorf("username must be 3-20 alphanumeric characters")
		}
		exists, err := s.repo.UsernameExistsExcluding(uname, id)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("username already exists")
		}
		user.Username = uname
	}
	if strings.TrimSpace(email) != "" && strings.ToLower(strings.TrimSpace(email)) != strings.ToLower(user.Email) {
		newEmail := strings.ToLower(strings.TrimSpace(email))
		if !emailRegex.MatchString(newEmail) {
			return nil, fmt.Errorf("invalid email address")
		}
		exists, err := s.repo.EmailExistsExcluding(newEmail, id)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("email already exists")
		}
		user.Email = newEmail
		// A new email address must be proven: reset the verified flag and issue
		// a fresh verification token. Until it is verified the account's tokens
		// no longer grant access.
		if !s.SkipEmailVerification {
			token, err := newToken()
			if err != nil {
				return nil, err
			}
			expires := time.Now().Add(verificationTokenTTL)
			user.EmailVerified = false
			user.EmailVerificationToken = &token
			user.EmailVerificationExpires = &expires
			if s.mailer != nil {
				if err := s.mailer.SendVerificationEmail(user.Email, user.Username, token); err != nil {
					log.Error().Str("email", user.Email).Err(err).Msg("failed to send verification email after email change")
				}
			}
		}
	}
	if err := s.repo.UpdateUser(user); err != nil {
		return nil, err
	}
	return s.repo.GetUserByID(id)
}

// ChangePassword updates a user's password after verifying the current one.
func (s *UserService) ChangePassword(id uuid.UUID, currentPassword, newPassword string) error {
	user, err := s.repo.GetUserByID(id)
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
	user.PasswordHash = hash
	return s.repo.UpdateUser(user)
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
