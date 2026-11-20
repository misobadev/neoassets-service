package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"neoassets/internal/models"
	"neoassets/internal/testdb"
)

// testRepo returns a Repository on a migrated test database (see testdb).
func testRepo(t *testing.T) *Repository {
	t.Helper()
	return NewRepository(testdb.Open(t, "repository_users"))
}

func testUser(t *testing.T, r *Repository) *models.User {
	t.Helper()
	u, err := r.CreateUser(&models.User{
		Username:     "u" + uuid.NewString()[:8],
		Email:        uuid.NewString()[:8] + "@example.com",
		PasswordHash: "hash-0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func mustGet(t *testing.T, r *Repository, id uuid.UUID) *models.User {
	t.Helper()
	u, err := r.GetUserByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// Each account operation writes only the columns it changes, so a password
// change made after a profile update read the row is kept.
func TestProfileUpdateLeavesOtherColumnsAlone(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)

	if ok, err := r.ChangePasswordHash(u.ID, "hash-0", "hash-1"); err != nil || !ok {
		t.Fatalf("password change: ok=%v err=%v", ok, err)
	}
	newName, newEmail := "renamed"+uuid.NewString()[:4], "new-"+u.Email
	ok, err := r.UpdateProfile(u.ID, ProfileUpdate{
		Username:     newName,
		Email:        newEmail,
		Verification: &EmailVerification{TokenHash: "verify", Expires: time.Now().Add(time.Hour)},
	})
	if err != nil || !ok {
		t.Fatalf("profile update: ok=%v err=%v", ok, err)
	}

	got := mustGet(t, r, u.ID)
	if got.PasswordHash != "hash-1" || got.TokenVersion != u.TokenVersion+1 {
		t.Fatalf("password change lost: hash=%q version=%d", got.PasswordHash, got.TokenVersion)
	}
	if got.Username != newName || got.Email != newEmail || got.EmailVerified ||
		got.EmailVerificationToken == nil || *got.EmailVerificationToken != "verify" {
		t.Fatalf("profile not applied: %+v", got)
	}
}

// Without a verification reset (a username change, or an email change with
// verification skipped) the verification state is not written at all, so a
// verification that lands in between is kept.
func TestProfileUpdateKeepsConcurrentVerification(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)

	if ok, err := r.SetEmailVerificationToken(u.ID, u.Email, "verify", time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("token: ok=%v err=%v", ok, err)
	}
	if ok, err := r.MarkEmailVerified(u.ID, "verify"); err != nil || !ok {
		t.Fatalf("verify: ok=%v err=%v", ok, err)
	}
	if _, err := r.UpdateProfile(u.ID, ProfileUpdate{Username: "renamed" + uuid.NewString()[:4], Email: "new-" + u.Email}); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, r, u.ID)
	if !got.EmailVerified || got.EmailVerificationToken != nil {
		t.Fatalf("verification lost: verified=%v token=%v", got.EmailVerified, got.EmailVerificationToken)
	}
	if ok, err := r.MarkEmailVerified(u.ID, "verify"); err != nil || ok {
		t.Fatalf("verification token reused: ok=%v err=%v", ok, err)
	}
}

func TestMarkEmailVerifiedNeedsTheCurrentUnexpiredToken(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)

	if _, err := r.SetEmailVerificationToken(u.ID, u.Email, "verify", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.MarkEmailVerified(u.ID, "verify"); ok {
		t.Fatal("an expired token verified the email")
	}
	if _, err := r.SetEmailVerificationToken(u.ID, u.Email, "verify", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.MarkEmailVerified(u.ID, "other"); ok {
		t.Fatal("a wrong token verified the email")
	}
	if mustGet(t, r, u.ID).EmailVerified {
		t.Fatal("email verified")
	}
}

// A verification or reset token is only stored for the email it is mailed to:
// if the email changed after it was read, nothing is stored.
func TestTokensAreTiedToTheEmailRead(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)
	oldEmail := u.Email

	if _, err := r.UpdateProfile(u.ID, ProfileUpdate{
		Email:        "new-" + oldEmail,
		Verification: &EmailVerification{TokenHash: "for-new", Expires: time.Now().Add(time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.SetEmailVerificationToken(u.ID, oldEmail, "for-old", time.Now().Add(time.Hour)); err != nil || ok {
		t.Fatalf("verification token stored for the old email: ok=%v err=%v", ok, err)
	}
	if ok, err := r.SetPasswordResetToken(u.ID, oldEmail, "reset", time.Now().Add(time.Hour)); err != nil || ok {
		t.Fatalf("reset token stored for the old email: ok=%v err=%v", ok, err)
	}
	got := mustGet(t, r, u.ID)
	if got.EmailVerificationToken == nil || *got.EmailVerificationToken != "for-new" || got.PasswordResetToken != nil {
		t.Fatalf("tokens: verify=%v reset=%v", got.EmailVerificationToken, got.PasswordResetToken)
	}
}

// A reset link already mailed to the previous address stops working once the
// email changes.
func TestEmailChangeCancelsPendingReset(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)

	if ok, err := r.SetPasswordResetToken(u.ID, u.Email, "reset", time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("reset token: ok=%v err=%v", ok, err)
	}
	if _, err := r.UpdateProfile(u.ID, ProfileUpdate{Email: "new-" + u.Email}); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, r, u.ID); got.PasswordResetToken != nil || got.PasswordResetExpires != nil {
		t.Fatalf("pending reset kept: token=%v expires=%v", got.PasswordResetToken, got.PasswordResetExpires)
	}
	if ok, err := r.ResetPasswordWithToken(u.ID, "reset", "hash-1"); err != nil || ok {
		t.Fatalf("reset after an email change applied: ok=%v err=%v", ok, err)
	}
	// A username-only change leaves a pending reset alone.
	if _, err := r.SetPasswordResetToken(u.ID, "new-"+u.Email, "reset2", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.UpdateProfile(u.ID, ProfileUpdate{Username: "renamed" + uuid.NewString()[:4]}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.ResetPasswordWithToken(u.ID, "reset2", "hash-1"); !ok {
		t.Fatal("a username change cancelled the reset")
	}
}

func TestUpdateProfileReportsAMissingUser(t *testing.T) {
	r := testRepo(t)
	if ok, err := r.UpdateProfile(uuid.New(), ProfileUpdate{Username: "nobody" + uuid.NewString()[:4]}); err != nil || ok {
		t.Fatalf("missing user: ok=%v err=%v", ok, err)
	}
	if _, err := r.UpdateProfile(uuid.New(), ProfileUpdate{Verification: &EmailVerification{}}); err == nil {
		t.Fatal("a verification reset without an email was accepted")
	}
}

func TestTokenVersionBumpsAccumulate(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)

	if ok, err := r.SetPasswordResetToken(u.ID, u.Email, "reset", time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("reset token: ok=%v err=%v", ok, err)
	}
	if ok, err := r.ResetPasswordWithToken(u.ID, "reset", "hash-1"); err != nil || !ok {
		t.Fatalf("reset: ok=%v err=%v", ok, err)
	}
	if ok, err := r.ChangePasswordHash(u.ID, "hash-1", "hash-2"); err != nil || !ok {
		t.Fatalf("change: ok=%v err=%v", ok, err)
	}
	if got := mustGet(t, r, u.ID).TokenVersion; got != u.TokenVersion+2 {
		t.Fatalf("token version: got %d, want %d", got, u.TokenVersion+2)
	}
}

// A password change checks the current password against the hash it read; if
// the password changed since, the write must not apply.
func TestChangePasswordHashRequiresTheCheckedHash(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)

	if ok, _ := r.ChangePasswordHash(u.ID, "hash-0", "hash-1"); !ok {
		t.Fatal("first change refused")
	}
	if ok, err := r.ChangePasswordHash(u.ID, "hash-0", "hash-2"); err != nil || ok {
		t.Fatalf("a change based on the old hash applied: ok=%v err=%v", ok, err)
	}
	if got := mustGet(t, r, u.ID).PasswordHash; got != "hash-1" {
		t.Fatalf("hash: got %q, want hash-1", got)
	}
}

func TestResetTokenIsSingleUse(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)

	if _, err := r.SetPasswordResetToken(u.ID, u.Email, "reset", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.ResetPasswordWithToken(u.ID, "reset", "hash-1"); !ok {
		t.Fatal("first use refused")
	}
	if ok, err := r.ResetPasswordWithToken(u.ID, "reset", "hash-2"); err != nil || ok {
		t.Fatalf("token reused: ok=%v err=%v", ok, err)
	}
	if got := mustGet(t, r, u.ID); got.PasswordHash != "hash-1" || got.PasswordResetToken != nil {
		t.Fatalf("after reuse: hash=%q token=%v", got.PasswordHash, got.PasswordResetToken)
	}
}

func TestExpiredResetTokenIsRefused(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)

	if _, err := r.SetPasswordResetToken(u.ID, u.Email, "reset", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.ResetPasswordWithToken(u.ID, "reset", "hash-1"); err != nil || ok {
		t.Fatalf("expired token accepted: ok=%v err=%v", ok, err)
	}
}
