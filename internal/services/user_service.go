package services

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"neoassets/internal/apperr"
	"regexp"
	"strings"
	"sync"
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
	// resendCooldown spaces out verification emails for one account.
	resendCooldown = 1 * time.Minute
	// maxPasswordBytes is bcrypt's input limit; longer passwords are refused
	// rather than silently truncated or failing with bcrypt's own error.
	maxPasswordBytes = 72
	// maxEmailBytes is the longest address users.email (VARCHAR(255)) holds,
	// less one, the RFC 5321 path limit.
	maxEmailBytes = 254
	// accountJobWorkers and accountJobQueue size the background queue for the
	// recovery emails; accountJobTimeout bounds one job (lookup, token, send).
	accountJobWorkers = 4
	accountJobQueue   = 256
	accountJobTimeout = 45 * time.Second
)

// accountMailer sends the account emails. *email.Sender implements it.
type accountMailer interface {
	SendVerificationEmail(to, username, token string) error
	SendPasswordResetEmail(to, username, token string) error
	SendEmailChangeConfirmation(to, username, token string) error
	SendEmailChangedNotice(to, username string) error
}

// checkPassword is auth.CheckPassword, replaceable in tests to count bcrypt
// comparisons.
var checkPassword = auth.CheckPassword

// dummyPasswordHash is a bcrypt hash (at the default cost) compared against
// when an account doesn't exist, so a login takes as long for an unknown email
// as for a wrong password.
var dummyPasswordHash = sync.OnceValue(func() string {
	hash, err := auth.HashPassword("neoassets-dummy-password-for-timing")
	if err != nil {
		panic(err)
	}
	return hash
})

var (
	usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9]{3,20}$`)
	emailRegex    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// UserService handles account registration, authentication and recovery.
type UserService struct {
	repo   *repository.Repository
	mailer accountMailer
	// sendInBackground runs an account email off the request, so the answer
	// and its timing don't depend on whether (or how fast) one is sent.
	sendInBackground      func(send func())
	jwtSecret             string
	tokenTTL              time.Duration
	protectedAdminEmail   string
	SkipEmailVerification bool
}

// NewUserService creates a UserService.
func NewUserService(repo *repository.Repository, mailer *email.Sender, jwtSecret string, tokenTTL time.Duration, protectedAdminEmail string) *UserService {
	s := &UserService{
		repo:                repo,
		jwtSecret:           jwtSecret,
		tokenTTL:            tokenTTL,
		protectedAdminEmail: protectedAdminEmail,
		sendInBackground:    newJobQueue(accountJobWorkers, accountJobQueue),
	}
	// Hash the dummy password now, so the first login for an unknown email
	// doesn't take longer than the rest.
	dummyPasswordHash()
	// A nil *email.Sender stored in the interface would not compare equal to
	// nil, so only a real sender is kept.
	if mailer != nil {
		s.mailer = mailer
	}
	return s
}

// newJobQueue runs jobs on a fixed set of workers. A job is dropped (and
// logged) only when the queue is full, and a panicking job doesn't stop its
// worker or the service.
func newJobQueue(workers, size int) func(func()) {
	jobs := make(chan func(), size)
	for i := 0; i < workers; i++ {
		go func() {
			for job := range jobs {
				runJob(job)
			}
		}()
	}
	return func(job func()) {
		select {
		case jobs <- job:
		default:
			log.Warn().Msg("account email dropped: the queue is full")
		}
	}
}

func runJob(job func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Msg("account email job panicked")
		}
	}()
	job()
}

// validateEmail checks an address's form and length.
func validateEmail(email string) error {
	if len(email) > maxEmailBytes || !emailRegex.MatchString(email) {
		return apperr.Invalid("invalid email address")
	}
	return nil
}

// validatePassword checks a new password's length.
func validatePassword(password string) error {
	if len(password) < 8 {
		return apperr.Invalid("password must be at least 8 characters")
	}
	if len(password) > maxPasswordBytes {
		return apperr.Invalid(fmt.Sprintf("password must be at most %d bytes", maxPasswordBytes))
	}
	return nil
}

// Register validates the payload, creates the user and sends a verification
// email. No JWT is issued until the email is verified, so an unverified account
// cannot use the API. It never fails because the email could not be sent.
func (s *UserService) Register(ctx context.Context, req models.RegisterRequest) (*models.AuthResult, error) {
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	if !usernameRegex.MatchString(req.Username) {
		return nil, apperr.Invalid("username must be 3-20 alphanumeric characters")
	}
	if err := validateEmail(req.Email); err != nil {
		return nil, err
	}
	if err := validatePassword(req.Password); err != nil {
		return nil, err
	}

	if _, err := s.repo.GetUserByUsername(ctx, req.Username); err == nil {
		return nil, apperr.Conflict("username already exists")
	} else if err != sql.ErrNoRows {
		return nil, err
	}
	if exists, err := s.repo.UsernameExists(ctx, req.Username); err != nil {
		return nil, err
	} else if exists {
		return nil, apperr.Conflict("username already exists")
	}
	if exists, err := s.repo.EmailExists(ctx, req.Email); err != nil {
		return nil, err
	} else if exists {
		return nil, apperr.Conflict("email already exists")
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
		// Compare anyway, so an unknown email takes as long as a wrong password.
		checkPassword(dummyPasswordHash(), req.Password)
		log.Warn().Str("email", req.Email).Msg("failed login: unknown email")
		return nil, apperr.Unauthorized("invalid credentials")
	}
	// The password is checked first, so without it the answer is the same for
	// an unknown, an unverified and a verified account. Only someone who has
	// the password learns that the account still needs verifying.
	if !checkPassword(user.PasswordHash, req.Password) {
		log.Warn().Str("email", req.Email).Msg("failed login: wrong password")
		return nil, apperr.Unauthorized("invalid credentials")
	}
	if !s.SkipEmailVerification && !user.EmailVerified {
		log.Warn().Str("email", req.Email).Msg("failed login: email not verified")
		return nil, apperr.Forbidden("email not verified")
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
		return apperr.Invalid("invalid verification token")
	}
	if user.EmailVerified {
		return nil
	}
	if user.EmailVerificationExpires == nil || time.Now().After(*user.EmailVerificationExpires) {
		return apperr.Invalid("verification token has expired")
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
		return apperr.Invalid("invalid verification token")
	}
	// Attach donations already made with this email so users who donated before
	// registering get their donor benefits automatically.
	s.linkPendingDonations(ctx, user.ID, user.Email)
	return nil
}

// ConfirmEmailChange switches the signed-in account userID to its pending
// address, using the token mailed to that address. Only the account that asked
// can confirm, so a mistyped address's owner (or a mail scanner) opening the
// link can't take the account over.
func (s *UserService) ConfirmEmailChange(ctx context.Context, userID uuid.UUID, token string) error {
	if token == "" {
		return apperr.Invalid("confirmation token is required")
	}
	return s.confirmEmailChange(ctx, userID, auth.HashToken(token))
}

// confirmEmailChange switches account userID to its pending address if
// tokenHash is that change's token. Every session is revoked, the previous
// address is told (with no link), and donations already made with the new
// address are attached.
func (s *UserService) confirmEmailChange(ctx context.Context, userID uuid.UUID, tokenHash string) error {
	change, ok, err := s.repo.ConfirmPendingEmail(ctx, userID, tokenHash)
	if errors.Is(err, repository.ErrEmailTaken) {
		return apperr.Conflict("email already exists")
	}
	if err != nil {
		return err
	}
	if !ok {
		return apperr.Invalid("this confirmation link is invalid, has expired or was already used")
	}
	log.Warn().Str("user_id", change.UserID.String()).Msg("account email changed")
	// The change is committed: finish its follow-ups even if the client goes
	// away meanwhile.
	ctx = context.WithoutCancel(ctx)
	s.linkPendingDonations(ctx, change.UserID, change.NewEmail)
	// Sent before answering, not queued: it's the owner's only warning if the
	// change wasn't theirs, so it must not be dropped. Only the account that
	// asked can get here, so its timing reveals nothing.
	if s.mailer != nil {
		if err := s.mailer.SendEmailChangedNotice(change.OldEmail, change.Username); err != nil {
			log.Error().Err(err).Str("email", change.OldEmail).Msg("failed to send email-changed notice")
		}
	}
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

// ResendVerification issues a fresh verification token and emails it. It
// answers at once and does the work in the background, so neither the answer
// nor its timing reveals whether the account exists or is verified.
func (s *UserService) ResendVerification(_ context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	s.sendInBackground(func() {
		ctx, cancel := context.WithTimeout(context.Background(), accountJobTimeout)
		defer cancel()
		s.resendVerification(ctx, email)
	})
	return nil
}

func (s *UserService) resendVerification(ctx context.Context, email string) {
	user, err := s.repo.GetUserByEmail(ctx, email)
	if err != nil || user.EmailVerified {
		return
	}
	token, err := newToken()
	if err != nil {
		log.Error().Err(err).Msg("resend verification: token")
		return
	}
	expires := time.Now().Add(verificationTokenTTL)
	// One token a minute at most, checked in the same statement that stores it.
	ok, err := s.repo.SetEmailVerificationToken(ctx, user.ID, user.Email, auth.HashToken(token), expires, expires.Add(-resendCooldown))
	if err != nil {
		log.Error().Err(err).Msg("resend verification: store token")
		return
	}
	// Inside the cooldown, or the email changed or was verified meanwhile:
	// nothing to send.
	if !ok || s.mailer == nil {
		return
	}
	if err := s.mailer.SendVerificationEmail(user.Email, user.Username, token); err != nil {
		log.Error().Err(err).Str("email", user.Email).Msg("failed to send verification email")
	}
}

// ForgotPassword issues a reset token and emails it. It answers at once and
// does the work in the background, so neither the answer nor its timing
// reveals whether an account exists.
func (s *UserService) ForgotPassword(_ context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	s.sendInBackground(func() {
		ctx, cancel := context.WithTimeout(context.Background(), accountJobTimeout)
		defer cancel()
		s.forgotPassword(ctx, email)
	})
	return nil
}

func (s *UserService) forgotPassword(ctx context.Context, email string) {
	user, err := s.repo.GetUserByEmail(ctx, email)
	if err != nil {
		return
	}
	token, err := newToken()
	if err != nil {
		log.Error().Err(err).Msg("forgot password: token")
		return
	}
	expires := time.Now().Add(resetTokenTTL)
	// One reset email a minute at most, checked in the same statement that
	// stores the token, so parallel requests can't each send one.
	ok, err := s.repo.SetPasswordResetToken(ctx, user.ID, user.Email, auth.HashToken(token), expires, expires.Add(-resetCooldown))
	if err != nil {
		log.Error().Err(err).Msg("forgot password: store token")
		return
	}
	// Inside the cooldown, or the email changed meanwhile: don't mail a link.
	if !ok || s.mailer == nil {
		return
	}
	if err := s.mailer.SendPasswordResetEmail(user.Email, user.Username, token); err != nil {
		log.Error().Err(err).Str("email", user.Email).Msg("failed to send password reset email")
	}
}

// ResetPassword sets a new password using the emailed token.
func (s *UserService) ResetPassword(ctx context.Context, token, newPassword string) error {
	if token == "" {
		return apperr.Invalid("reset token is required")
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}

	user, err := s.repo.GetUserByPasswordResetToken(ctx, auth.HashToken(token))
	if err != nil {
		return apperr.Invalid("invalid or expired reset token")
	}
	if user.PasswordResetExpires == nil || time.Now().After(*user.PasswordResetExpires) {
		return apperr.Invalid("reset token has expired")
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
		return apperr.Invalid("invalid or expired reset token")
	}
	return nil
}

// GetUserByID returns a user by ID.
func (s *UserService) GetUserByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
	user, err := s.repo.GetUserByID(ctx, id)
	if err != nil {
		return nil, apperr.NotFound("user not found")
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
		return nil, apperr.NotFound("user not found")
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
		if s.isProtectedAccount(&list[i]) {
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
		return nil, apperr.NotFound("user not found")
	}
	if s.isProtectedAccount(user) {
		return nil, apperr.Forbidden("this account's role is locked and cannot be changed")
	}
	updated, err := s.repo.SetUserRole(ctx, id, role)
	if err != nil {
		return nil, apperr.NotFound("user not found")
	}
	log.Warn().Str("user_id", id.String()).Str("role", role).Msg("user role changed")
	return updated, nil
}

// UpdateProfile updates a user's username and/or email. Fields may be empty to
// leave them unchanged. Usernames are required to be present and unique.
//
// A new email needs the current password and doesn't replace the address in
// use: it waits as the pending email until the account, signed in, confirms it
// with the token mailed to the new address (ConfirmEmailChange). So a session
// alone can't move the account, and a mistyped address's owner can't take it
// over. The protected admin account's email can't be changed.
func (s *UserService) UpdateProfile(ctx context.Context, id uuid.UUID, username, email, currentPassword string) (*models.User, error) {
	user, err := s.repo.GetUserByID(ctx, id)
	if err != nil {
		return nil, apperr.NotFound("user not found")
	}
	newUsername, newEmail := "", ""
	if strings.TrimSpace(email) != "" && strings.ToLower(strings.TrimSpace(email)) != strings.ToLower(user.Email) {
		newEmail = strings.ToLower(strings.TrimSpace(email))
		if s.isProtectedAccount(user) {
			return nil, apperr.Forbidden("this account's email is locked and cannot be changed")
		}
		// The password is checked before anything about the new address is
		// looked up, so a session alone can't test which addresses exist.
		if !checkPassword(user.PasswordHash, currentPassword) {
			return nil, apperr.Unauthorized("current password is incorrect")
		}
		if err := validateEmail(newEmail); err != nil {
			return nil, err
		}
		exists, err := s.repo.EmailExistsExcluding(ctx, newEmail, id)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, apperr.Conflict("email already exists")
		}
	}
	if strings.TrimSpace(username) != "" && strings.TrimSpace(username) != user.Username {
		uname := strings.TrimSpace(username)
		if !usernameRegex.MatchString(uname) {
			return nil, apperr.Invalid("username must be 3-20 alphanumeric characters")
		}
		exists, err := s.repo.UsernameExistsExcluding(ctx, uname, id)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, apperr.Conflict("username already exists")
		}
		newUsername = uname
	}
	if newUsername == "" && newEmail == "" {
		return user, nil
	}
	update := repository.ProfileUpdate{Username: newUsername}
	var token, tokenHash string
	if newEmail != "" {
		token, err = newToken()
		if err != nil {
			return nil, err
		}
		tokenHash = auth.HashToken(token)
		update.Pending = &repository.PendingEmail{
			CheckedHash: user.PasswordHash,
			Email:       newEmail,
			TokenHash:   tokenHash,
			Expires:     time.Now().Add(verificationTokenTTL),
		}
	}
	// One statement for both, so a failure leaves neither applied. The pending
	// email is recorded only if the password is still the one just checked.
	ok, err := s.repo.UpdateProfile(ctx, id, update)
	if err != nil {
		return nil, err
	}
	if !ok {
		if newEmail != "" {
			return nil, apperr.Conflict("the password was changed by another request, please try again")
		}
		return nil, apperr.NotFound("user not found")
	}
	if newEmail != "" {
		if s.SkipEmailVerification {
			// Local development: no email to confirm, so switch right away.
			if err := s.confirmEmailChange(ctx, id, tokenHash); err != nil {
				return nil, err
			}
		} else if s.mailer != nil {
			name := user.Username
			if newUsername != "" {
				name = newUsername
			}
			// Sent before answering: the request needs the password, so its
			// timing reveals nothing, and a failure is logged.
			if err := s.mailer.SendEmailChangeConfirmation(newEmail, name, token); err != nil {
				log.Error().Str("email", newEmail).Err(err).Msg("failed to send email change confirmation")
			}
		}
	}
	return s.repo.GetUserByID(ctx, id)
}

// isProtectedAccount reports whether user is the protected root account.
func (s *UserService) isProtectedAccount(user *models.User) bool {
	return s.protectedAdminEmail != "" && strings.EqualFold(user.Email, s.protectedAdminEmail)
}

// ChangePassword updates a user's password after verifying the current one.
func (s *UserService) ChangePassword(ctx context.Context, id uuid.UUID, currentPassword, newPassword string) error {
	user, err := s.repo.GetUserByID(ctx, id)
	if err != nil {
		return apperr.NotFound("user not found")
	}
	if !checkPassword(user.PasswordHash, currentPassword) {
		return apperr.Unauthorized("current password is incorrect")
	}
	if err := validatePassword(newPassword); err != nil {
		return err
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
		return apperr.Conflict("the password was changed by another request, please try again")
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
