package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"neoassets/internal/apperr"
	"neoassets/internal/models"
	"neoassets/internal/repository"
	"neoassets/internal/testdb"
	"neoassets/pkg/auth"
)

const userTestPassword = "correct horse battery"

// fakeMailer records the account emails a test sends.
type fakeMailer struct {
	mu   sync.Mutex
	sent []sentMail
	fail error
}

type sentMail struct{ kind, to, token string }

func (m *fakeMailer) record(kind, to, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, sentMail{kind, to, token})
	return m.fail
}

func (m *fakeMailer) SendVerificationEmail(to, _, token string) error {
	return m.record("verify", to, token)
}
func (m *fakeMailer) SendPasswordResetEmail(to, _, token string) error {
	return m.record("reset", to, token)
}
func (m *fakeMailer) SendEmailChangeConfirmation(to, _, token string) error {
	return m.record("change", to, token)
}
func (m *fakeMailer) SendEmailChangedNotice(to, _ string) error {
	return m.record("notice", to, "")
}

func (m *fakeMailer) of(kind string) []sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []sentMail
	for _, s := range m.sent {
		if s.kind == kind {
			out = append(out, s)
		}
	}
	return out
}

// userTestService returns a UserService on a migrated test database with a
// recording mailer whose background sends run inline.
func userTestService(t *testing.T) (*UserService, *repository.Repository, *fakeMailer) {
	t.Helper()
	repo := repository.NewRepository(testdb.Open(t, "services_users"))
	s := NewUserService(repo, nil, strings.Repeat("user-test-secret-", 3), time.Hour, "")
	m := &fakeMailer{}
	s.mailer = m
	s.sendInBackground = func(job func()) { job() }
	return s, repo, m
}

func userTestAccount(t *testing.T, repo *repository.Repository, verified bool) *models.User {
	t.Helper()
	hash, err := auth.HashPassword(userTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	u, err := repo.CreateUser(t.Context(), &models.User{
		Username:      "u" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
		Email:         uuid.NewString()[:8] + "@example.com",
		PasswordHash:  hash,
		EmailVerified: verified,
	})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func isKind(err error, kind apperr.Kind) bool { return err != nil && apperr.KindOf(err) == kind }

// countPasswordChecks counts bcrypt comparisons for the rest of the test.
func countPasswordChecks(t *testing.T) *int {
	t.Helper()
	n := 0
	orig := checkPassword
	checkPassword = func(hash, password string) bool { n++; return orig(hash, password) }
	t.Cleanup(func() { checkPassword = orig })
	return &n
}

// Without the password, Login answers the same for an unknown, an unverified
// and a verified account, and an unknown email still costs a bcrypt
// comparison. Only the right password reveals that an account is unverified.
func TestLoginChecksThePasswordFirst(t *testing.T) {
	s, repo, _ := userTestService(t)
	unverified := userTestAccount(t, repo, false)
	verified := userTestAccount(t, repo, true)
	ctx := context.Background()
	checks := countPasswordChecks(t)

	for name, email := range map[string]string{"unknown": "nobody-" + uuid.NewString()[:8] + "@example.com", "unverified": unverified.Email, "verified": verified.Email} {
		before := *checks
		_, err := s.Login(ctx, models.UserLoginRequest{Email: email, Password: "wrong password"})
		if !isKind(err, apperr.KindUnauthorized) || err.Error() != "invalid credentials" {
			t.Errorf("%s, wrong password: %v", name, err)
		}
		if *checks != before+1 {
			t.Errorf("%s: %d bcrypt comparisons, want 1", name, *checks-before)
		}
	}
	if _, err := s.Login(ctx, models.UserLoginRequest{Email: unverified.Email, Password: userTestPassword}); !isKind(err, apperr.KindForbidden) {
		t.Errorf("unverified, right password: %v, want email not verified", err)
	}
	res, err := s.Login(ctx, models.UserLoginRequest{Email: verified.Email, Password: userTestPassword})
	if err != nil || res.Token == "" {
		t.Fatalf("verified, right password: %v", err)
	}
}

// An email change needs the current password, checked before anything about
// the new address is looked up, and leaves the account on its address until
// the new one is confirmed.
func TestEmailChangeNeedsThePasswordAndWaitsForConfirmation(t *testing.T) {
	s, repo, mail := userTestService(t)
	u := userTestAccount(t, repo, true)
	other := userTestAccount(t, repo, true)
	ctx := context.Background()
	newEmail := "new-" + u.Email

	for _, pw := range []string{"", "wrong password"} {
		if _, err := s.UpdateProfile(ctx, u.ID, "", newEmail, pw); !isKind(err, apperr.KindUnauthorized) {
			t.Errorf("password %q: %v", pw, err)
		}
		// A taken address is only reported after the password check.
		if _, err := s.UpdateProfile(ctx, u.ID, "", strings.ToUpper(other.Email), pw); !isKind(err, apperr.KindUnauthorized) {
			t.Errorf("taken address, password %q: %v", pw, err)
		}
	}
	if got, _ := repo.GetUserByID(ctx, u.ID); got.PendingEmail != nil {
		t.Fatalf("pending email recorded without the password: %v", *got.PendingEmail)
	}
	if _, err := s.UpdateProfile(ctx, u.ID, "", other.Email, userTestPassword); !isKind(err, apperr.KindConflict) {
		t.Errorf("taken address with the password: %v", err)
	}

	got, err := s.UpdateProfile(ctx, u.ID, "", newEmail, userTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != u.Email || !got.EmailVerified || got.PendingEmail == nil || *got.PendingEmail != newEmail {
		t.Fatalf("after the request: email=%q verified=%v pending=%v", got.Email, got.EmailVerified, got.PendingEmail)
	}
	confirm := mail.of("change")
	if len(confirm) != 1 || confirm[0].to != newEmail || confirm[0].token == "" {
		t.Fatalf("confirmation mails: %+v", confirm)
	}
	if n := len(mail.of("notice")); n != 0 {
		t.Fatalf("%d notices before confirmation", n)
	}

	// The token only works for the signed-in account that asked, not through
	// the public verification route and not for another account.
	if err := s.VerifyEmail(ctx, confirm[0].token); !isKind(err, apperr.KindInvalid) {
		t.Fatalf("change token accepted by VerifyEmail: %v", err)
	}
	if err := s.ConfirmEmailChange(ctx, other.ID, confirm[0].token); !isKind(err, apperr.KindInvalid) {
		t.Fatalf("change token accepted for another account: %v", err)
	}
	if err := s.ConfirmEmailChange(ctx, u.ID, confirm[0].token); err != nil {
		t.Fatal(err)
	}
	after, _ := repo.GetUserByID(ctx, u.ID)
	if after.Email != newEmail || !after.EmailVerified || after.TokenVersion != u.TokenVersion+1 || after.PendingEmail != nil {
		t.Fatalf("after confirmation: %+v", after)
	}
	notice := mail.of("notice")
	if len(notice) != 1 || notice[0].to != u.Email {
		t.Fatalf("notices: %+v, want one to %s", notice, u.Email)
	}
	if err := s.ConfirmEmailChange(ctx, u.ID, confirm[0].token); !isKind(err, apperr.KindInvalid) {
		t.Fatalf("confirmation reused: %v", err)
	}
}

// An address taken by another account after the change was requested is
// refused at confirmation as a conflict, and donations made with the new
// address are attached when a change is confirmed.
func TestEmailChangeConfirmationConflictsAndDonations(t *testing.T) {
	s, repo, mail := userTestService(t)
	u := userTestAccount(t, repo, true)
	ctx := context.Background()
	taken := "taken-" + u.Email
	if _, err := s.UpdateProfile(ctx, u.ID, "", taken, userTestPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateUser(ctx, &models.User{Username: "o" + uuid.NewString()[:8], Email: strings.ToUpper(taken), PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmEmailChange(ctx, u.ID, mail.of("change")[0].token); !isKind(err, apperr.KindConflict) {
		t.Fatalf("taken address: %v", err)
	}

	donated := "donor-" + u.Email
	if _, err := repo.UpsertDonationEvent(ctx, &models.DonationEvent{Platform: "kofi", ExternalID: uuid.NewString(), Email: donated, AmountCents: 500, Currency: "USD", Kind: "one_time", Active: true, Raw: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateProfile(ctx, u.ID, "", donated, userTestPassword); err != nil {
		t.Fatal(err)
	}
	changes := mail.of("change")
	if err := s.ConfirmEmailChange(ctx, u.ID, changes[len(changes)-1].token); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetUserByID(ctx, u.ID); got.DonorStatus == models.DonorNone {
		t.Fatalf("donation not attached on confirmation: donor status %q", got.DonorStatus)
	}
}

// An over-long address (users.email holds 255) is refused up front.
func TestOverlongEmailIsRefused(t *testing.T) {
	s, repo, _ := userTestService(t)
	u := userTestAccount(t, repo, true)
	long := strings.Repeat("a", maxEmailBytes) + "@example.com"
	if _, err := s.UpdateProfile(context.Background(), u.ID, "", long, userTestPassword); !isKind(err, apperr.KindInvalid) {
		t.Errorf("email change: %v", err)
	}
	if _, err := s.Register(context.Background(), models.RegisterRequest{Username: "long" + uuid.NewString()[:6], Email: long, Password: userTestPassword}); !isKind(err, apperr.KindInvalid) {
		t.Errorf("register: %v", err)
	}
}

// The job queue runs jobs on its workers, survives a panicking job, and drops
// a job only when the queue is full.
func TestJobQueue(t *testing.T) {
	run := newJobQueue(1, 1)
	release := make(chan struct{})
	started := make(chan struct{})
	ran := make(chan string, 4)
	panicked := make(chan struct{})
	run(func() { close(panicked); panic("boom") })
	<-panicked // the worker has taken it, so the queue is empty again
	run(func() { close(started); <-release; ran <- "first" })
	<-started
	run(func() { ran <- "queued" })
	run(func() { ran <- "dropped" })
	close(release)
	for _, want := range []string{"first", "queued"} {
		select {
		case got := <-ran:
			if got != want {
				t.Fatalf("ran %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%q never ran", want)
		}
	}
	select {
	case got := <-ran:
		t.Fatalf("ran %q; the queue was full, so it should have been dropped", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestUsernameChangeNeedsNoPassword(t *testing.T) {
	s, repo, _ := userTestService(t)
	u := userTestAccount(t, repo, true)
	name := "n" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	got, err := s.UpdateProfile(context.Background(), u.ID, name, "", "")
	if err != nil || got.Username != name {
		t.Fatalf("username change: %v %+v", err, got)
	}
}

func TestProtectedAccountEmailIsLocked(t *testing.T) {
	s, repo, _ := userTestService(t)
	u := userTestAccount(t, repo, true)
	s.protectedAdminEmail = strings.ToUpper(u.Email)
	if _, err := s.UpdateProfile(context.Background(), u.ID, "", "new-"+u.Email, userTestPassword); !isKind(err, apperr.KindForbidden) {
		t.Fatalf("protected account email change: %v", err)
	}
}

// With verification skipped (local development) a change applies at once.
func TestEmailChangeWithoutVerificationAppliesAtOnce(t *testing.T) {
	s, repo, _ := userTestService(t)
	s.SkipEmailVerification = true
	u := userTestAccount(t, repo, true)
	got, err := s.UpdateProfile(context.Background(), u.ID, "", "new-"+u.Email, userTestPassword)
	if err != nil || got.Email != "new-"+u.Email || got.PendingEmail != nil {
		t.Fatalf("change: %v %+v", err, got)
	}
}

// Resend and forgot send at most one email a minute per account, report
// success either way, and a mail failure doesn't reach the caller.
func TestRecoveryEmailsAreRateLimitedAndQuiet(t *testing.T) {
	s, repo, mail := userTestService(t)
	unverified := userTestAccount(t, repo, false)
	verified := userTestAccount(t, repo, true)
	ctx := context.Background()
	mail.fail = errors.New("smtp: 421 try again later")

	for i := 0; i < 3; i++ {
		if err := s.ResendVerification(ctx, unverified.Email); err != nil {
			t.Fatalf("resend %d: %v", i, err)
		}
		if err := s.ForgotPassword(ctx, verified.Email); err != nil {
			t.Fatalf("forgot %d: %v", i, err)
		}
	}
	if n := len(mail.of("verify")); n != 1 {
		t.Errorf("%d verification emails, want 1", n)
	}
	if n := len(mail.of("reset")); n != 1 {
		t.Errorf("%d reset emails, want 1", n)
	}
	for _, unknown := range []string{"nobody@example.com", verified.Email} {
		if err := s.ResendVerification(ctx, unknown); err != nil {
			t.Errorf("resend for %s: %v", unknown, err)
		}
	}
	if err := s.ForgotPassword(ctx, "nobody@example.com"); err != nil {
		t.Errorf("forgot for an unknown address: %v", err)
	}
}

// Resend and forgot do their work in the background on a context of their own,
// so a request cancelled after the answer still gets its email.
func TestRecoveryEmailsOutliveTheRequest(t *testing.T) {
	s, repo, mail := userTestService(t)
	verified := userTestAccount(t, repo, true)
	unverified := userTestAccount(t, repo, false)
	var queued []func()
	s.sendInBackground = func(job func()) { queued = append(queued, job) }
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.ForgotPassword(ctx, verified.Email); err != nil {
		t.Fatal(err)
	}
	if err := s.ResendVerification(ctx, unverified.Email); err != nil {
		t.Fatal(err)
	}
	cancel()
	if n := len(mail.sent); n != 0 || len(queued) != 2 {
		t.Fatalf("sent %d during the requests, queued %d jobs", n, len(queued))
	}
	for _, job := range queued {
		job()
	}
	if r, v := len(mail.of("reset")), len(mail.of("verify")); r != 1 || v != 1 {
		t.Fatalf("after the requests were cancelled: %d reset and %d verification emails, want 1 each", r, v)
	}
}

// A password longer than bcrypt's 72-byte limit is refused with a clear
// message, wherever a new password is set.
func TestLongPasswordsAreRefused(t *testing.T) {
	s, repo, _ := userTestService(t)
	u := userTestAccount(t, repo, true)
	long := strings.Repeat("p", maxPasswordBytes+1)
	ctx := context.Background()

	if _, err := s.Register(ctx, models.RegisterRequest{Username: "longpw" + uuid.NewString()[:4], Email: uuid.NewString()[:8] + "@example.com", Password: long}); !isKind(err, apperr.KindInvalid) {
		t.Errorf("register: %v", err)
	}
	if err := s.ChangePassword(ctx, u.ID, userTestPassword, long); !isKind(err, apperr.KindInvalid) {
		t.Errorf("change: %v", err)
	}
	if err := s.ResetPassword(ctx, "some-token", long); !isKind(err, apperr.KindInvalid) {
		t.Errorf("reset: %v", err)
	}
	if err := validatePassword(strings.Repeat("p", maxPasswordBytes)); err != nil {
		t.Errorf("72 bytes refused: %v", err)
	}
}

// Register refuses an address another account holds in any letter case,
// including a stored mixed-case one (such as a seeded admin's).
func TestRegisterRefusesAnAddressInAnyCase(t *testing.T) {
	s, repo, _ := userTestService(t)
	mixed := "Seed-" + uuid.NewString()[:8] + "@Example.com"
	if _, err := repo.CreateUser(t.Context(), &models.User{Username: "seed" + uuid.NewString()[:6], Email: mixed, PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Register(context.Background(), models.RegisterRequest{Username: "dup" + uuid.NewString()[:6], Email: strings.ToLower(mixed), Password: userTestPassword})
	if !isKind(err, apperr.KindConflict) {
		t.Fatalf("register with a lower-case copy of a mixed-case address: %v", err)
	}
}

// A nil sender leaves the service without a mailer (not a typed nil that would
// look non-nil and panic on the first send).
func TestNilSenderMeansNoMailer(t *testing.T) {
	if s := NewUserService(nil, nil, "secret", time.Hour, ""); s.mailer != nil {
		t.Fatalf("mailer = %#v, want nil", s.mailer)
	}
}

// Donations link only to an account whose address is verified.
func TestDonationsLinkOnlyToVerifiedAccounts(t *testing.T) {
	_, repo, _ := userTestService(t)
	unverified := userTestAccount(t, repo, false)
	verified := userTestAccount(t, repo, true)
	d := NewDonationService(repo, nil, "", "")
	ctx := context.Background()

	if _, ok := d.accountForDonation(ctx, unverified.Email); ok {
		t.Error("a donation linked to an unverified account")
	}
	if u, ok := d.accountForDonation(ctx, verified.Email); !ok || u.ID != verified.ID {
		t.Error("a donation didn't link to the verified account")
	}
	d.SkipEmailVerification = true
	if _, ok := d.accountForDonation(ctx, unverified.Email); !ok {
		t.Error("with verification skipped, the account wasn't linked")
	}
}

// Admin login costs one bcrypt comparison whether or not the email belongs to
// an admin, so admin accounts can't be told apart by timing, and surrounding
// spaces in the email are ignored.
func TestAdminLoginComparesForUnknownAccounts(t *testing.T) {
	_, repo, _ := userTestService(t)
	user := userTestAccount(t, repo, true)
	admin := userTestAccount(t, repo, true)
	if _, err := repo.SetUserRole(t.Context(), admin.ID, models.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	s := NewService(repo, nil, "https://cdn.test", strings.Repeat("admin-test-secret-", 3), nil)
	checks := countPasswordChecks(t)
	ctx := context.Background()

	for name, email := range map[string]string{"unknown": "nobody@example.com", "not an admin": user.Email} {
		before := *checks
		if _, err := s.Login(ctx, email, userTestPassword); !isKind(err, apperr.KindUnauthorized) {
			t.Errorf("%s: %v", name, err)
		}
		if *checks != before+1 {
			t.Errorf("%s: %d bcrypt comparisons, want 1", name, *checks-before)
		}
	}
	if _, err := s.Login(ctx, "  "+admin.Email+" ", userTestPassword); err != nil {
		t.Fatalf("admin with surrounding spaces: %v", err)
	}
}
