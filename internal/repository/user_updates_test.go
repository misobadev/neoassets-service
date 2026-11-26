package repository

import (
	"context"
	"errors"
	"strings"
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
	u, err := r.CreateUser(t.Context(), &models.User{
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
	u, err := r.GetUserByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// noCooldown lets a token write replace any earlier token (no cooldown).
var noCooldown = time.Now().Add(1000 * time.Hour)

// Each account operation writes only the columns it changes, so a password
// change made after a profile update read the row is kept.
func TestProfileUpdateLeavesOtherColumnsAlone(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)

	if ok, err := r.ChangePasswordHash(t.Context(), u.ID, "hash-0", "hash-1"); err != nil || !ok {
		t.Fatalf("password change: ok=%v err=%v", ok, err)
	}
	newName := "renamed" + uuid.NewString()[:4]
	if ok, err := r.UpdateProfile(t.Context(), u.ID, ProfileUpdate{Username: newName}); err != nil || !ok {
		t.Fatalf("profile update: ok=%v err=%v", ok, err)
	}

	got := mustGet(t, r, u.ID)
	if got.PasswordHash != "hash-1" || got.TokenVersion != u.TokenVersion+1 {
		t.Fatalf("password change lost: hash=%q version=%d", got.PasswordHash, got.TokenVersion)
	}
	if got.Username != newName || got.Email != u.Email {
		t.Fatalf("profile not applied: %+v", got)
	}
}

// A username change doesn't write the verification state, so a verification
// that lands in between is kept.
func TestProfileUpdateKeepsConcurrentVerification(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)

	if ok, err := r.SetEmailVerificationToken(t.Context(), u.ID, u.Email, "verify", time.Now().Add(time.Hour), noCooldown); err != nil || !ok {
		t.Fatalf("token: ok=%v err=%v", ok, err)
	}
	if ok, err := r.MarkEmailVerified(t.Context(), u.ID, "verify"); err != nil || !ok {
		t.Fatalf("verify: ok=%v err=%v", ok, err)
	}
	if _, err := r.UpdateProfile(t.Context(), u.ID, ProfileUpdate{Username: "renamed" + uuid.NewString()[:4]}); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, r, u.ID)
	if !got.EmailVerified || got.EmailVerificationToken != nil {
		t.Fatalf("verification lost: verified=%v token=%v", got.EmailVerified, got.EmailVerificationToken)
	}
	if ok, err := r.MarkEmailVerified(t.Context(), u.ID, "verify"); err != nil || ok {
		t.Fatalf("verification token reused: ok=%v err=%v", ok, err)
	}
}

func TestMarkEmailVerifiedNeedsTheCurrentUnexpiredToken(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)

	if _, err := r.SetEmailVerificationToken(t.Context(), u.ID, u.Email, "verify", time.Now().Add(-time.Minute), noCooldown); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.MarkEmailVerified(t.Context(), u.ID, "verify"); ok {
		t.Fatal("an expired token verified the email")
	}
	if _, err := r.SetEmailVerificationToken(t.Context(), u.ID, u.Email, "verify", time.Now().Add(time.Hour), noCooldown); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.MarkEmailVerified(t.Context(), u.ID, "other"); ok {
		t.Fatal("a wrong token verified the email")
	}
	if mustGet(t, r, u.ID).EmailVerified {
		t.Fatal("email verified")
	}
}

// setPendingEmail records a pending email change, as UpdateProfile does for a
// new address.
func setPendingEmail(r *Repository, ctx context.Context, id uuid.UUID, checkedHash, email, tokenHash string, expires time.Time) (bool, error) {
	return r.UpdateProfile(ctx, id, ProfileUpdate{Pending: &PendingEmail{
		CheckedHash: checkedHash, Email: email, TokenHash: tokenHash, Expires: expires,
	}})
}

// tok returns a token unique to this run: the tests share one database, so a
// fixed token left pending by one test could match another test's confirm.
func tok(name string) string { return name + "-" + uuid.NewString() }

// changeEmail records a pending change to newEmail and confirms it.
func changeEmail(t *testing.T, r *Repository, u *models.User, newEmail, token string) EmailChange {
	t.Helper()
	cur := mustGet(t, r, u.ID)
	if ok, err := setPendingEmail(r, t.Context(), u.ID, cur.PasswordHash, newEmail, token, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("pending email: ok=%v err=%v", ok, err)
	}
	change, ok, err := r.ConfirmPendingEmail(t.Context(), u.ID, token)
	if err != nil || !ok {
		t.Fatalf("confirm: ok=%v err=%v", ok, err)
	}
	return change
}

// A verification or reset token is only stored for the email it is mailed to:
// if the email changed after it was read, nothing is stored.
func TestTokensAreTiedToTheEmailRead(t *testing.T) {
	pendingTok := tok("pending")
	r := testRepo(t)
	u := testUser(t, r)
	oldEmail := u.Email

	changeEmail(t, r, u, "new-"+oldEmail, pendingTok)
	if ok, err := r.SetEmailVerificationToken(t.Context(), u.ID, oldEmail, "for-old", time.Now().Add(time.Hour), noCooldown); err != nil || ok {
		t.Fatalf("verification token stored for the old email: ok=%v err=%v", ok, err)
	}
	if ok, err := r.SetPasswordResetToken(t.Context(), u.ID, oldEmail, "reset", time.Now().Add(time.Hour), noCooldown); err != nil || ok {
		t.Fatalf("reset token stored for the old email: ok=%v err=%v", ok, err)
	}
	got := mustGet(t, r, u.ID)
	if got.EmailVerificationToken != nil || got.PasswordResetToken != nil {
		t.Fatalf("tokens: verify=%v reset=%v", got.EmailVerificationToken, got.PasswordResetToken)
	}
}

// A reset link already mailed to the previous address stops working once the
// email changes.
func TestEmailChangeCancelsPendingReset(t *testing.T) {
	pendingTok := tok("pending")
	r := testRepo(t)
	u := testUser(t, r)

	if ok, err := r.SetPasswordResetToken(t.Context(), u.ID, u.Email, "reset", time.Now().Add(time.Hour), noCooldown); err != nil || !ok {
		t.Fatalf("reset token: ok=%v err=%v", ok, err)
	}
	changeEmail(t, r, u, "new-"+u.Email, pendingTok)
	if got := mustGet(t, r, u.ID); got.PasswordResetToken != nil || got.PasswordResetExpires != nil {
		t.Fatalf("pending reset kept: token=%v expires=%v", got.PasswordResetToken, got.PasswordResetExpires)
	}
	if ok, err := r.ResetPasswordWithToken(t.Context(), u.ID, "reset", "hash-1"); err != nil || ok {
		t.Fatalf("reset after an email change applied: ok=%v err=%v", ok, err)
	}
	// A username-only change leaves a pending reset alone.
	if _, err := r.SetPasswordResetToken(t.Context(), u.ID, "new-"+u.Email, "reset2", time.Now().Add(time.Hour), noCooldown); err != nil {
		t.Fatal(err)
	}
	if _, err := r.UpdateProfile(t.Context(), u.ID, ProfileUpdate{Username: "renamed" + uuid.NewString()[:4]}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.ResetPasswordWithToken(t.Context(), u.ID, "reset2", "hash-1"); !ok {
		t.Fatal("a username change cancelled the reset")
	}
}

func TestUpdateProfileReportsAMissingUser(t *testing.T) {
	r := testRepo(t)
	if ok, err := r.UpdateProfile(t.Context(), uuid.New(), ProfileUpdate{Username: "nobody" + uuid.NewString()[:4]}); err != nil || ok {
		t.Fatalf("missing user: ok=%v err=%v", ok, err)
	}
}

// Recording a new address changes nothing else: the account keeps its email,
// its verification and its sessions until the new address is confirmed.
func TestPendingEmailLeavesTheAccountAlone(t *testing.T) {
	pendingTok := tok("pending")
	r := testRepo(t)
	u := testUser(t, r)
	if _, err := r.db.ExecContext(t.Context(), `UPDATE users SET email_verified = TRUE WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}

	if ok, err := setPendingEmail(r, t.Context(), u.ID, "stale-hash", "new-"+u.Email, pendingTok, time.Now().Add(time.Hour)); err != nil || ok {
		t.Fatalf("pending email with a stale password hash: ok=%v err=%v", ok, err)
	}
	if ok, err := setPendingEmail(r, t.Context(), u.ID, "hash-0", "new-"+u.Email, pendingTok, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("pending email: ok=%v err=%v", ok, err)
	}
	got := mustGet(t, r, u.ID)
	if got.Email != u.Email || !got.EmailVerified || got.TokenVersion != u.TokenVersion {
		t.Fatalf("account changed before confirmation: %+v", got)
	}
	if got.PendingEmail == nil || *got.PendingEmail != "new-"+u.Email || got.PendingEmailToken == nil || *got.PendingEmailToken != pendingTok {
		t.Fatalf("pending change not recorded: email=%v token=%v", got.PendingEmail, got.PendingEmailToken)
	}
}

// Confirming switches the address, verifies it, consumes every pending token
// and revokes every session, once.
func TestConfirmPendingEmailSwitchesOnce(t *testing.T) {
	pendingTok := tok("pending")
	r := testRepo(t)
	u := testUser(t, r)
	oldEmail := u.Email

	if ok, err := r.SetPasswordResetToken(t.Context(), u.ID, u.Email, "reset", time.Now().Add(time.Hour), noCooldown); err != nil || !ok {
		t.Fatalf("reset token: ok=%v err=%v", ok, err)
	}
	change := changeEmail(t, r, u, "new-"+oldEmail, pendingTok)
	if change.UserID != u.ID || change.OldEmail != oldEmail || change.NewEmail != "new-"+oldEmail || change.Username != u.Username {
		t.Fatalf("change: %+v", change)
	}
	got := mustGet(t, r, u.ID)
	if got.Email != "new-"+oldEmail || !got.EmailVerified || got.TokenVersion != u.TokenVersion+1 {
		t.Fatalf("after confirm: email=%q verified=%v version=%d", got.Email, got.EmailVerified, got.TokenVersion)
	}
	if got.PendingEmail != nil || got.PendingEmailToken != nil || got.PendingEmailExpires != nil ||
		got.PasswordResetToken != nil || got.EmailVerificationToken != nil {
		t.Fatalf("tokens kept: %+v", got)
	}
	if _, ok, err := r.ConfirmPendingEmail(t.Context(), u.ID, pendingTok); err != nil || ok {
		t.Fatalf("token reused: ok=%v err=%v", ok, err)
	}
	if v := mustGet(t, r, u.ID).TokenVersion; v != u.TokenVersion+1 {
		t.Fatalf("token version after reuse: %d", v)
	}
}

func TestExpiredPendingEmailIsRefused(t *testing.T) {
	pendingTok := tok("pending")
	r := testRepo(t)
	u := testUser(t, r)

	if ok, err := setPendingEmail(r, t.Context(), u.ID, "hash-0", "new-"+u.Email, pendingTok, time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("pending email: ok=%v err=%v", ok, err)
	}
	if _, ok, err := r.ConfirmPendingEmail(t.Context(), u.ID, pendingTok); err != nil || ok {
		t.Fatalf("expired token confirmed: ok=%v err=%v", ok, err)
	}
	if got := mustGet(t, r, u.ID); got.Email != u.Email {
		t.Fatalf("email switched: %q", got.Email)
	}
}

// An address taken by another account (in any letter case) after the change was
// requested is never switched to.
func TestConfirmPendingEmailRefusesATakenAddress(t *testing.T) {
	pendingTok := tok("pending")
	r := testRepo(t)
	u := testUser(t, r)
	newEmail := "taken-" + u.Email

	if ok, err := setPendingEmail(r, t.Context(), u.ID, "hash-0", newEmail, pendingTok, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("pending email: ok=%v err=%v", ok, err)
	}
	if _, err := r.CreateUser(t.Context(), &models.User{
		Username: "o" + uuid.NewString()[:8], Email: strings.ToUpper(newEmail), PasswordHash: "x",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := r.ConfirmPendingEmail(t.Context(), u.ID, pendingTok); !errors.Is(err, ErrEmailTaken) || ok {
		t.Fatalf("taken address: ok=%v err=%v", ok, err)
	}
	got := mustGet(t, r, u.ID)
	if got.Email != u.Email || got.TokenVersion != u.TokenVersion {
		t.Fatalf("account changed: email=%q version=%d", got.Email, got.TokenVersion)
	}
}

// A password change or reset drops an email change still waiting: whoever asked
// for it may not be the owner.
func TestPasswordChangesDropAPendingEmail(t *testing.T) {
	p1Tok := tok("p1")
	p2Tok := tok("p2")
	r := testRepo(t)
	u := testUser(t, r)

	if ok, err := setPendingEmail(r, t.Context(), u.ID, "hash-0", "new-"+u.Email, p1Tok, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("pending email: ok=%v err=%v", ok, err)
	}
	if ok, err := r.ChangePasswordHash(t.Context(), u.ID, "hash-0", "hash-1"); err != nil || !ok {
		t.Fatalf("change: ok=%v err=%v", ok, err)
	}
	if got := mustGet(t, r, u.ID); got.PendingEmail != nil || got.PendingEmailToken != nil {
		t.Fatalf("pending email kept by a password change: %v", got.PendingEmail)
	}
	if _, ok, _ := r.ConfirmPendingEmail(t.Context(), u.ID, p1Tok); ok {
		t.Fatal("dropped change confirmed")
	}

	if ok, err := setPendingEmail(r, t.Context(), u.ID, "hash-1", "new-"+u.Email, p2Tok, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("pending email: ok=%v err=%v", ok, err)
	}
	if ok, err := r.SetPasswordResetToken(t.Context(), u.ID, u.Email, "reset", time.Now().Add(time.Hour), noCooldown); err != nil || !ok {
		t.Fatalf("reset token: ok=%v err=%v", ok, err)
	}
	if ok, err := r.ResetPasswordWithToken(t.Context(), u.ID, "reset", "hash-2"); err != nil || !ok {
		t.Fatalf("reset: ok=%v err=%v", ok, err)
	}
	if got := mustGet(t, r, u.ID); got.PendingEmail != nil || got.PendingEmailToken != nil {
		t.Fatalf("pending email kept by a reset: %v", got.PendingEmail)
	}
}

// Token writes respect the cooldown in the same statement: a second token inside
// the window is refused, one after it applies.
func TestTokenCooldownsAreAtomic(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)
	now := time.Now()
	expires := now.Add(time.Hour)
	inWindow := expires.Add(-time.Minute) // the previous token expires later than this

	if ok, err := r.SetPasswordResetToken(t.Context(), u.ID, u.Email, "r1", expires, inWindow); err != nil || !ok {
		t.Fatalf("first reset token: ok=%v err=%v", ok, err)
	}
	if ok, err := r.SetPasswordResetToken(t.Context(), u.ID, u.Email, "r2", expires, inWindow); err != nil || ok {
		t.Fatalf("second reset token inside the cooldown: ok=%v err=%v", ok, err)
	}
	if ok, err := r.SetPasswordResetToken(t.Context(), u.ID, u.Email, "r3", expires.Add(time.Minute), expires); err != nil || !ok {
		t.Fatalf("reset token after the cooldown: ok=%v err=%v", ok, err)
	}

	if ok, err := r.SetEmailVerificationToken(t.Context(), u.ID, u.Email, "v1", expires, inWindow); err != nil || !ok {
		t.Fatalf("first verification token: ok=%v err=%v", ok, err)
	}
	if ok, err := r.SetEmailVerificationToken(t.Context(), u.ID, u.Email, "v2", expires, inWindow); err != nil || ok {
		t.Fatalf("second verification token inside the cooldown: ok=%v err=%v", ok, err)
	}
	if ok, err := r.SetEmailVerificationToken(t.Context(), u.ID, u.Email, "v3", expires.Add(time.Minute), expires); err != nil || !ok {
		t.Fatalf("verification token after the cooldown: ok=%v err=%v", ok, err)
	}
	got := mustGet(t, r, u.ID)
	if *got.PasswordResetToken != "r3" || *got.EmailVerificationToken != "v3" {
		t.Fatalf("tokens: reset=%v verify=%v", *got.PasswordResetToken, *got.EmailVerificationToken)
	}
}

func TestEmailExistsIgnoresCase(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)
	if ok, err := r.EmailExists(t.Context(), strings.ToUpper(u.Email)); err != nil || !ok {
		t.Fatalf("upper-case lookup: ok=%v err=%v", ok, err)
	}
	if ok, err := r.EmailExists(t.Context(), "missing-"+u.Email); err != nil || ok {
		t.Fatalf("missing address: ok=%v err=%v", ok, err)
	}
}

func TestTokenVersionBumpsAccumulate(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)

	if ok, err := r.SetPasswordResetToken(t.Context(), u.ID, u.Email, "reset", time.Now().Add(time.Hour), noCooldown); err != nil || !ok {
		t.Fatalf("reset token: ok=%v err=%v", ok, err)
	}
	if ok, err := r.ResetPasswordWithToken(t.Context(), u.ID, "reset", "hash-1"); err != nil || !ok {
		t.Fatalf("reset: ok=%v err=%v", ok, err)
	}
	if ok, err := r.ChangePasswordHash(t.Context(), u.ID, "hash-1", "hash-2"); err != nil || !ok {
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

	if ok, _ := r.ChangePasswordHash(t.Context(), u.ID, "hash-0", "hash-1"); !ok {
		t.Fatal("first change refused")
	}
	if ok, err := r.ChangePasswordHash(t.Context(), u.ID, "hash-0", "hash-2"); err != nil || ok {
		t.Fatalf("a change based on the old hash applied: ok=%v err=%v", ok, err)
	}
	if got := mustGet(t, r, u.ID).PasswordHash; got != "hash-1" {
		t.Fatalf("hash: got %q, want hash-1", got)
	}
}

func TestResetTokenIsSingleUse(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)

	if _, err := r.SetPasswordResetToken(t.Context(), u.ID, u.Email, "reset", time.Now().Add(time.Hour), noCooldown); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.ResetPasswordWithToken(t.Context(), u.ID, "reset", "hash-1"); !ok {
		t.Fatal("first use refused")
	}
	if ok, err := r.ResetPasswordWithToken(t.Context(), u.ID, "reset", "hash-2"); err != nil || ok {
		t.Fatalf("token reused: ok=%v err=%v", ok, err)
	}
	if got := mustGet(t, r, u.ID); got.PasswordHash != "hash-1" || got.PasswordResetToken != nil {
		t.Fatalf("after reuse: hash=%q token=%v", got.PasswordHash, got.PasswordResetToken)
	}
}

func TestExpiredResetTokenIsRefused(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)

	if _, err := r.SetPasswordResetToken(t.Context(), u.ID, u.Email, "reset", time.Now().Add(-time.Minute), noCooldown); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.ResetPasswordWithToken(t.Context(), u.ID, "reset", "hash-1"); err != nil || ok {
		t.Fatalf("expired token accepted: ok=%v err=%v", ok, err)
	}
}

// Only the account the change belongs to can confirm it with its token.
func TestConfirmPendingEmailIsBoundToTheAccount(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)
	other := testUser(t, r)
	pendingTok := tok("pending")

	if ok, err := setPendingEmail(r, t.Context(), u.ID, "hash-0", "new-"+u.Email, pendingTok, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("pending email: ok=%v err=%v", ok, err)
	}
	if _, ok, err := r.ConfirmPendingEmail(t.Context(), other.ID, pendingTok); err != nil || ok {
		t.Fatalf("another account confirmed: ok=%v err=%v", ok, err)
	}
	if got := mustGet(t, r, u.ID); got.Email != u.Email || got.PendingEmail == nil {
		t.Fatalf("after another account's attempt: email=%q pending=%v", got.Email, got.PendingEmail)
	}
}

// A password reset that commits while a confirm waits for the row drops the
// pending change, and the confirm then applies nothing.
func TestConfirmWaitsForAConcurrentPasswordReset(t *testing.T) {
	db := testdb.Open(t, "repository_users")
	r := NewRepository(db)
	u := testUser(t, r)
	pendingTok := tok("pending")
	if ok, err := setPendingEmail(r, t.Context(), u.ID, "hash-0", "new-"+u.Email, pendingTok, time.Now().Add(time.Hour)); err != nil || !ok {
		t.Fatalf("pending email: ok=%v err=%v", ok, err)
	}

	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `UPDATE users SET password_hash = 'hash-1', pending_email = NULL,
		pending_email_token = NULL, pending_email_expires_at = NULL, token_version = token_version + 1 WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, ok, err := r.ConfirmPendingEmail(context.Background(), u.ID, pendingTok)
		done <- result{ok, err}
	}()
	// Commit only once the confirm is waiting on the row lock.
	for deadline := time.Now().Add(10 * time.Second); ; {
		select {
		case res := <-done:
			t.Fatalf("confirm didn't wait for the row lock: %+v", res)
		default:
		}
		var waiting int
		if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the confirm never waited on the row lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.err != nil || res.ok {
		t.Fatalf("confirm after the reset: ok=%v err=%v", res.ok, res.err)
	}
	if got := mustGet(t, r, u.ID); got.Email != u.Email || got.TokenVersion != u.TokenVersion+1 {
		t.Fatalf("after the race: email=%q version=%d", got.Email, got.TokenVersion)
	}
}

// A username change and a pending email apply together or not at all.
func TestProfileUpdateIsAllOrNothing(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)
	newName := "renamed" + uuid.NewString()[:4]

	ok, err := r.UpdateProfile(t.Context(), u.ID, ProfileUpdate{Username: newName, Pending: &PendingEmail{
		CheckedHash: "stale-hash", Email: "new-" + u.Email, TokenHash: tok("pending"), Expires: time.Now().Add(time.Hour),
	}})
	if err != nil || ok {
		t.Fatalf("update with a stale password hash: ok=%v err=%v", ok, err)
	}
	if got := mustGet(t, r, u.ID); got.Username != u.Username || got.PendingEmail != nil {
		t.Fatalf("partly applied: username=%q pending=%v", got.Username, got.PendingEmail)
	}
}

// A pending address whose link has expired isn't reported any more.
func TestExpiredPendingEmailIsNotShown(t *testing.T) {
	r := testRepo(t)
	u := testUser(t, r)
	if ok, err := setPendingEmail(r, t.Context(), u.ID, "hash-0", "new-"+u.Email, tok("pending"), time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("pending email: ok=%v err=%v", ok, err)
	}
	if got := mustGet(t, r, u.ID); got.PendingEmail != nil {
		t.Fatalf("expired pending email shown: %q", *got.PendingEmail)
	}
}
