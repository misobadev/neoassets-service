package services

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"neoassets/internal/email"
	"neoassets/internal/models"
	"neoassets/internal/repository"
	"neoassets/pkg/auth"
)

// donorClaimTTL is how long a donor claim code stays valid.
const donorClaimTTL = 15 * time.Minute

// DonationService ingests Ko-fi/Patreon payments and maps them to donor status.
type DonationService struct {
	repo          *repository.Repository
	mailer        *email.Sender
	kofiToken     string
	patreonSecret string
}

// NewDonationService creates a DonationService.
func NewDonationService(repo *repository.Repository, mailer *email.Sender, kofiToken, patreonSecret string) *DonationService {
	return &DonationService{repo: repo, mailer: mailer, kofiToken: kofiToken, patreonSecret: patreonSecret}
}

// KofiEnabled reports whether the Ko-fi webhook is configured.
func (s *DonationService) KofiEnabled() bool { return s.kofiToken != "" }

// PatreonEnabled reports whether the Patreon webhook is configured.
func (s *DonationService) PatreonEnabled() bool { return s.patreonSecret != "" }

// HandleKofi validates and stores a Ko-fi payment. It is idempotent: the same
// transaction id is only ever stored once. When the donor email matches a
// registered account the donor status is refreshed immediately.
func (s *DonationService) HandleKofi(raw []byte) error {
	if !s.KofiEnabled() {
		return fmt.Errorf("ko-fi webhook is not configured")
	}
	var p models.KofiWebhookPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("invalid ko-fi payload")
	}
	if subtle.ConstantTimeCompare([]byte(p.VerificationToken), []byte(s.kofiToken)) != 1 {
		return fmt.Errorf("invalid ko-fi verification token")
	}
	// Ko-fi sends Tip, Subscription, Commission or Shop Order. Only tips and
	// subscriptions affect donor status; the rest are ignored (still 200 so
	// Ko-fi stops retrying).
	var kind string
	switch {
	case p.Type == "Commission" || p.Type == "Shop Order":
		return nil
	case p.Type == "Subscription" || p.IsSubscriptionPayment:
		kind = models.DonationKindSubscription
	case p.Type == "Donation" || p.Type == "Tip":
		kind = models.DonationKindOneTime
	default:
		log.Warn().Str("type", p.Type).Msg("unknown ko-fi event type, skipping")
		return nil
	}

	email := normalizeEmail(p.Email)
	externalID := p.KofiTransactionID
	if externalID == "" {
		externalID = p.MessageID
	}
	if email == "" || externalID == "" {
		log.Warn().Str("kofi_tx", externalID).Msg("ko-fi event without email or transaction id, skipping")
		return nil
	}

	ev := &models.DonationEvent{
		Platform:    models.DonationPlatformKofi,
		ExternalID:  externalID,
		Email:       email,
		FromName:    p.FromName,
		AmountCents: amountToCents(p.Amount),
		Currency:    strings.ToUpper(strings.TrimSpace(p.Currency)),
		Kind:        kind,
		TierName:    p.TierName,
		Active:      true,
		OccurredAt:  parseKofiTime(p.Timestamp),
		Raw:         raw,
	}
	if u, err := s.repo.GetUserByEmail(email); err == nil {
		ev.UserID = &u.ID
	}

	saved, err := s.repo.UpsertDonationEvent(ev)
	if err != nil {
		return err
	}
	if saved.UserID != nil {
		if _, err := s.repo.RecomputeDonorStatus(*saved.UserID); err != nil {
			return err
		}
	}
	return nil
}

// HandlePatreon validates and stores a Patreon member event. Membership is
// recurring, so every event is a subscription: active while patron_status is
// active_patron, inactive after members:delete or a downgrade. The email in the
// payload is matched to an account just like Ko-fi.
func (s *DonationService) HandlePatreon(event, signature string, raw []byte) error {
	if !s.PatreonEnabled() {
		return fmt.Errorf("patreon webhook is not configured")
	}
	if !verifyPatreonSignature(s.patreonSecret, raw, signature) {
		return fmt.Errorf("invalid patreon signature")
	}
	var p models.PatreonWebhookPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("invalid patreon payload")
	}
	if p.Data.ID == "" {
		return fmt.Errorf("patreon payload without member id")
	}

	active := p.Data.Attributes.PatronStatus == "active_patron"
	if event == "members:delete" {
		active = false
	}

	email := normalizeEmail(p.Data.Attributes.Email)
	occurred := parsePatreonTime(p.Data.Attributes.LastChargeDate)
	if active && occurred == nil {
		now := time.Now()
		occurred = &now
	}

	tier := ""
	if tiers := p.Data.Relationships.CurrentlyEntitledTiers.Data; len(tiers) > 0 {
		for _, t := range p.Included {
			if t.Type == "tier" && t.ID == tiers[0].ID {
				tier = t.Attributes.Title
				break
			}
		}
	}

	ev := &models.DonationEvent{
		Platform:    models.DonationPlatformPatreon,
		ExternalID:  p.Data.ID,
		Email:       email,
		FromName:    p.Data.Attributes.FullName,
		AmountCents: p.Data.Attributes.CurrentlyEntitledAmountCents,
		Currency:    "USD",
		Kind:        models.DonationKindSubscription,
		TierName:    tier,
		Active:      active,
		OccurredAt:  occurred,
		Raw:         raw,
	}
	if email != "" {
		if u, err := s.repo.GetUserByEmail(email); err == nil {
			ev.UserID = &u.ID
		}
	}

	saved, err := s.repo.UpsertDonationEvent(ev)
	if err != nil {
		return err
	}
	if saved.UserID != nil {
		if _, err := s.repo.RecomputeDonorStatus(*saved.UserID); err != nil {
			return err
		}
	}
	return nil
}

// ImportDonations backfills historical donations (for example from the Ko-fi
// dashboard supporters export). Each item is upserted idempotently by
// external_id, matched to a user by email, and the donor status is recomputed.
// It returns how many events were imported and how many users were relinked.
func (s *DonationService) ImportDonations(items []models.DonationImportItem) (imported, linked, skipped int, err error) {
	userIDs := map[uuid.UUID]bool{}
	for _, it := range items {
		email := normalizeEmail(it.Email)
		if email == "" {
			skipped++
			continue
		}
		kind := it.Kind
		if kind != models.DonationKindSubscription && kind != models.DonationKindOneTime {
			kind = models.DonationKindOneTime
		}
		platform := strings.TrimSpace(it.Platform)
		if platform != models.DonationPlatformPatreon {
			platform = models.DonationPlatformKofi
		}
		active := true
		if it.Active != nil {
			active = *it.Active
		}
		externalID := strings.TrimSpace(it.ExternalID)
		if externalID == "" {
			externalID = importExternalID(platform, email, it.OccurredAt, kind)
		}
		ev := &models.DonationEvent{
			Platform:    platform,
			ExternalID:  externalID,
			Email:       email,
			FromName:    strings.TrimSpace(it.FromName),
			AmountCents: it.AmountCents,
			Currency:    strings.ToUpper(strings.TrimSpace(it.Currency)),
			Kind:        kind,
			TierName:    strings.TrimSpace(it.TierName),
			Active:      active,
			OccurredAt:  parseImportTime(it.OccurredAt),
			Raw:         json.RawMessage(`{"source":"import"}`),
		}
		if ev.Currency == "" {
			ev.Currency = "USD"
		}
		if u, err := s.repo.GetUserByEmail(email); err == nil {
			ev.UserID = &u.ID
		}
		saved, err := s.repo.UpsertDonationEvent(ev)
		if err != nil {
			return imported, linked, skipped, err
		}
		imported++
		if saved.UserID != nil {
			userIDs[*saved.UserID] = true
		}
	}
	for id := range userIDs {
		if _, err := s.repo.RecomputeDonorStatus(id); err != nil {
			return imported, linked, skipped, err
		}
		linked++
	}
	return imported, linked, skipped, nil
}

// importExternalID derives a stable transaction id for an imported row that has
// no platform id, so re-importing the same list stays idempotent.
func importExternalID(platform, email, occurredAt, kind string) string {
	sum := sha256.Sum256([]byte(platform + "|" + strings.ToLower(strings.TrimSpace(email)) + "|" + occurredAt + "|" + kind))
	return "import-" + hex.EncodeToString(sum[:8])
}

// parseImportTime parses the timestamp of an imported row. It accepts RFC3339
// and the "2006-01-02 15:04" form used by Ko-fi exports (assumed UTC).
func parseImportTime(ts string) *time.Time {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, ts); err == nil {
			utc := t.UTC()
			return &utc
		}
	}
	return nil
}

// ClaimStart emails a code that proves ownership of the donation email when it
// differs from the account email.
func (s *DonationService) ClaimStart(userID uuid.UUID, email string) error {
	email = normalizeEmail(email)
	if email == "" {
		return fmt.Errorf("invalid email address")
	}
	pending, err := s.repo.HasPendingDonationsForEmail(email)
	if err != nil {
		return err
	}
	if !pending {
		return fmt.Errorf("no donations found for that email")
	}
	user, err := s.repo.GetUserByID(userID)
	if err != nil {
		return fmt.Errorf("user not found")
	}
	code, err := numericCode(6)
	if err != nil {
		return err
	}
	if err := s.repo.UpsertDonorClaim(userID, email, auth.HashToken(code), time.Now().Add(donorClaimTTL)); err != nil {
		return err
	}
	if s.mailer == nil {
		return fmt.Errorf("email delivery is not configured")
	}
	if err := s.mailer.SendDonorClaimEmail(email, user.Username, code); err != nil {
		log.Error().Err(err).Str("user_id", userID.String()).Msg("failed to send donor claim email")
		return fmt.Errorf("could not send the verification email")
	}
	return nil
}

// ClaimVerify validates the code, links every pending donation for the email to
// the user and refreshes the donor status. Returns the number of events linked.
func (s *DonationService) ClaimVerify(userID uuid.UUID, email, code string) (int64, error) {
	email = normalizeEmail(email)
	claimEmail, hash, expires, err := s.repo.GetDonorClaim(userID)
	if err != nil {
		return 0, fmt.Errorf("no claim in progress")
	}
	if !strings.EqualFold(claimEmail, email) {
		return 0, fmt.Errorf("email does not match the pending claim")
	}
	if time.Now().After(expires) {
		return 0, fmt.Errorf("claim code has expired")
	}
	if subtle.ConstantTimeCompare([]byte(hash), []byte(auth.HashToken(strings.TrimSpace(code)))) != 1 {
		return 0, fmt.Errorf("invalid claim code")
	}

	n, err := s.repo.LinkDonationEventsByEmail(userID, email)
	if err != nil {
		return 0, err
	}
	if _, err := s.repo.RecomputeDonorStatus(userID); err != nil {
		return 0, err
	}
	_ = s.repo.DeleteDonorClaim(userID)
	return n, nil
}

// LinkPendingForEmail attaches any donations already made with the account email
// and refreshes the donor status. Called after an email is verified so users who
// donated before registering get their benefits automatically.
func (s *DonationService) LinkPendingForEmail(userID uuid.UUID, email string) {
	n, err := s.repo.LinkDonationEventsByEmail(userID, normalizeEmail(email))
	if err != nil {
		log.Warn().Err(err).Str("user_id", userID.String()).Msg("failed to link pending donations")
		return
	}
	if n == 0 {
		return
	}
	if _, err := s.repo.RecomputeDonorStatus(userID); err != nil {
		log.Warn().Err(err).Str("user_id", userID.String()).Msg("failed to refresh donor status")
	}
}

// ExpireStaleSubscriptions downgrades auto-managed monthly supporters whose last
// subscription payment fell outside the grace window.
func (s *DonationService) ExpireStaleSubscriptions() {
	ids, err := s.repo.StaleSubscriptionUserIDs()
	if err != nil {
		log.Error().Err(err).Msg("failed to list stale donor subscriptions")
		return
	}
	for _, id := range ids {
		if _, err := s.repo.RecomputeDonorStatus(id); err != nil {
			log.Warn().Err(err).Str("user_id", id.String()).Msg("failed to downgrade donor status")
		}
	}
	if len(ids) > 0 {
		log.Info().Int("count", len(ids)).Msg("downgraded stale donor subscriptions")
	}
}

// StartExpiryJob runs the subscription expiry sweep at boot and then every six
// hours until the context is cancelled.
func (s *DonationService) StartExpiryJob(ctx context.Context) {
	go func() {
		s.ExpireStaleSubscriptions()
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.ExpireStaleSubscriptions()
			}
		}
	}()
}

// normalizeEmail lowercases and trims an address for consistent matching.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// amountToCents parses a decimal amount string ("3.00") into cents.
func amountToCents(amount string) int {
	f, err := strconv.ParseFloat(strings.TrimSpace(amount), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int(math.Round(f * 100))
}

// parseKofiTime parses the Ko-fi RFC3339 timestamp, returning nil when absent.
func parseKofiTime(ts string) *time.Time {
	if strings.TrimSpace(ts) == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return nil
	}
	return &t
}

// parsePatreonTime parses a Patreon ISO-8601 timestamp (which may carry
// sub-second precision), returning nil when absent.
func parsePatreonTime(ts string) *time.Time {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z0700"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return &t
		}
	}
	return nil
}

// verifyPatreonSignature checks the X-Patreon-Signature header: an HMAC of the
// raw request body keyed by the webhook secret. Patreon has used both MD5 and
// SHA-256 historically, so either is accepted. An empty secret is rejected to
// avoid the empty-key forgery class of bug.
func verifyPatreonSignature(secret string, body []byte, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	md5Mac := hmac.New(md5.New, []byte(secret))
	md5Mac.Write(body)
	md5Sum := hex.EncodeToString(md5Mac.Sum(nil))

	shaMac := hmac.New(sha256.New, []byte(secret))
	shaMac.Write(body)
	shaSum := hex.EncodeToString(shaMac.Sum(nil))

	return subtle.ConstantTimeCompare([]byte(signature), []byte(md5Sum)) == 1 ||
		subtle.ConstantTimeCompare([]byte(signature), []byte(shaSum)) == 1
}

// numericCode returns a random code of the given number of decimal digits.
func numericCode(digits int) (string, error) {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", digits, n.Int64()), nil
}
