package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Submission status values.
const (
	StatusCreated  = "created"
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
	StatusTrashed  = "trashed"
)

// File kind values.
const (
	KindBackground = "background"
	KindPreview    = "preview"
	KindTheme      = "theme"
	KindLogo       = "logo"
)

// User roles.
const (
	RoleUser     = "user"
	RoleReviewer = "reviewer"
	RoleAdmin    = "admin"
)

// Donor statuses. Donations grant scraping threads (never XP or rank) and run
// in parallel to the XP level: the effective threads are the higher of the two.
const (
	DonorNone             = "none"
	DonorSupporter        = "supporter"
	DonorMonthlySupporter = "monthly_supporter"
)

// Donor status sources: whether the current status came from a donation event
// (auto) or was set by an admin (admin). Admin overrides are never recomputed.
const (
	DonorSourceNone  = "none"
	DonorSourceAuto  = "auto"
	DonorSourceAdmin = "admin"
)

// Donation platforms and kinds.
const (
	DonationPlatformKofi    = "kofi"
	DonationPlatformPatreon = "patreon"

	DonationKindOneTime      = "one_time"
	DonationKindSubscription = "subscription"
)

// DonationEvent is a normalized Ko-fi/Patreon payment, used both as an audit
// log and as the source of truth for a user's donor status. external_id is the
// platform's transaction id, making ingestion idempotent.
type DonationEvent struct {
	ID          uuid.UUID       `json:"id" db:"id"`
	Platform    string          `json:"platform" db:"platform"`
	ExternalID  string          `json:"external_id" db:"external_id"`
	Email       string          `json:"email" db:"email"`
	FromName    string          `json:"from_name" db:"from_name"`
	AmountCents int             `json:"amount_cents" db:"amount_cents"`
	Currency    string          `json:"currency" db:"currency"`
	Kind        string          `json:"kind" db:"kind"`
	TierName    string          `json:"tier_name" db:"tier_name"`
	Active      bool            `json:"active" db:"active"`
	UserID      *uuid.UUID      `json:"user_id" db:"user_id"`
	OccurredAt  *time.Time      `json:"occurred_at" db:"occurred_at"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
	Raw         json.RawMessage `json:"-" db:"raw"`
}

// DonationImportItem is one normalized supporter row from an exported list (e.g.
// the Ko-fi dashboard "supporters" CSV). It backfills donations made before the
// webhook was configured; ExternalID keeps the import idempotent.
type DonationImportItem struct {
	Platform    string `json:"platform"` // kofi | patreon (default kofi)
	Email       string `json:"email"`
	FromName    string `json:"from_name"`
	Kind        string `json:"kind"` // subscription | one_time
	AmountCents int    `json:"amount_cents"`
	Currency    string `json:"currency"`
	OccurredAt  string `json:"occurred_at"` // RFC3339 or "2006-01-02 15:04"
	ExternalID  string `json:"external_id"`
	TierName    string `json:"tier_name"`
	// Active defaults to true; Patreon members can be imported as inactive.
	Active *bool `json:"active,omitempty"`
}

// KofiWebhookPayload is the JSON Ko-fi sends inside the "data" form field.
type KofiWebhookPayload struct {
	VerificationToken          string `json:"verification_token"`
	MessageID                  string `json:"message_id"`
	Timestamp                  string `json:"timestamp"`
	Type                       string `json:"type"`
	FromName                   string `json:"from_name"`
	Message                    string `json:"message"`
	Amount                     string `json:"amount"`
	URL                        string `json:"url"`
	Email                      string `json:"email"`
	Currency                   string `json:"currency"`
	IsSubscriptionPayment      bool   `json:"is_subscription_payment"`
	IsFirstSubscriptionPayment bool   `json:"is_first_subscription_payment"`
	KofiTransactionID          string `json:"kofi_transaction_id"`
	TierName                   string `json:"tier_name"`
}

// PatreonWebhookPayload is the JSON:API body Patreon posts for member events.
type PatreonWebhookPayload struct {
	Data     PatreonMember `json:"data"`
	Included []PatreonTier `json:"included"`
}

// PatreonMember is the member resource from a Patreon webhook.
type PatreonMember struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Attributes struct {
		Email                        string `json:"email"`
		FullName                     string `json:"full_name"`
		PatronStatus                 string `json:"patron_status"`
		CurrentlyEntitledAmountCents int    `json:"currently_entitled_amount_cents"`
		CampaignLifetimeSupportCents int    `json:"campaign_lifetime_support_cents"`
		LastChargeDate               string `json:"last_charge_date"`
		LastChargeStatus             string `json:"last_charge_status"`
	} `json:"attributes"`
	Relationships struct {
		CurrentlyEntitledTiers struct {
			Data []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"data"`
		} `json:"currently_entitled_tiers"`
	} `json:"relationships"`
}

// PatreonTier is a tier resource included in a member webhook.
type PatreonTier struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Attributes struct {
		Title string `json:"title"`
	} `json:"attributes"`
}

// DonorClaimRequest starts a claim for donations made with another email.
type DonorClaimRequest struct {
	Email string `json:"email"`
}

// DonorClaimVerifyRequest confirms the code sent to the donation email.
type DonorClaimVerifyRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

// User represents an account in the app. Admins are users with the admin role.
type User struct {
	ID                       uuid.UUID  `json:"id" db:"id"`
	Username                 string     `json:"username" db:"username"`
	Email                    string     `json:"email" db:"email"`
	PasswordHash             string     `json:"-" db:"password_hash"`
	Role                     string     `json:"role" db:"role"`
	EmailVerified            bool       `json:"email_verified" db:"email_verified"`
	Hidden                   bool       `json:"hidden,omitempty" db:"hidden"`
	XP                       int        `json:"xp" db:"xp"`
	DonorStatus              string     `json:"donor_status" db:"donor_status"`
	AvatarKey                string     `json:"avatar_key" db:"avatar_key"`
	Protected                bool       `json:"protected,omitempty" db:"-"`
	EmailVerificationToken   *string    `json:"-" db:"email_verification_token"`
	EmailVerificationExpires *time.Time `json:"-" db:"email_verification_expires_at"`
	PasswordResetToken       *string    `json:"-" db:"password_reset_token"`
	PasswordResetExpires     *time.Time `json:"-" db:"password_reset_expires_at"`
	CreatedAt                time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at" db:"updated_at"`
}

// UserCountStat is a leaderboard row counting submissions.
type UserCountStat struct {
	ID        uuid.UUID `json:"id" db:"id"`
	Username  string    `json:"username" db:"username"`
	AvatarKey string    `json:"avatar_key" db:"avatar_key"`
	Count     int       `json:"count" db:"count"`
}

// RecentPack is a recently published system art pack.
type RecentPack struct {
	ID         uuid.UUID `json:"id" db:"id"`
	PackID     string    `json:"pack_id" db:"pack_id"`
	Name       string    `json:"name" db:"name"`
	Author     string    `json:"author" db:"author"`
	Version    string    `json:"version" db:"version"`
	CreatedAt  time.Time `json:"created_at" db:"created_at"`
	AuthorName *string   `json:"author_name,omitempty" db:"author_name"`
}

// RecentMetadata is a recently approved game metadata contribution.
type RecentMetadata struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	GameID      *uuid.UUID `json:"game_id,omitempty" db:"game_id"`
	SystemID    string     `json:"system_id,omitempty" db:"system_id"`
	GameName    string     `json:"game_name" db:"game_name"`
	SystemName  string     `json:"system_name" db:"system_name"`
	SubmittedBy string     `json:"submitted_by,omitempty" db:"submitted_by"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
}

// Dashboard is the app home payload: catalog totals, leaderboards and recent
// published content (SAP + game metadata).
type Dashboard struct {
	TotalGames         int              `json:"total_games"`
	TotalPacks         int              `json:"total_packs"`
	TotalSystems       int              `json:"total_systems"`
	TotalUsers         int              `json:"total_users"`
	TotalContributions int              `json:"total_contributions"`
	TopContributions   []UserCountStat  `json:"top_contributions"`
	TopApprovedWeek    []UserCountStat  `json:"top_approved_week"`
	TopLevels          []LevelStat      `json:"top_levels"`
	TopGames           []PopularGame    `json:"top_games"`
	TopSystems         []PopularSystem  `json:"top_systems"`
	RecentPacks        []RecentPack     `json:"recent_packs"`
	RecentMetadata     []RecentMetadata `json:"recent_metadata"`
}

// LevelStat is a dashboard leaderboard row for the XP level ranking.
type LevelStat struct {
	ID        uuid.UUID `json:"id"`
	Username  string    `json:"username"`
	AvatarKey string    `json:"avatar_key"`
	XP        int       `json:"xp"`
	Level     int       `json:"level"`
	Rank      string    `json:"rank"`
	Threads   int       `json:"threads"`
}

// Rewards is a user's XP progression and the scraping limits it unlocks.
type Rewards struct {
	XP    int    `json:"xp"`
	Level int    `json:"level"`
	Rank  string `json:"rank"`
	// Threads is the active concurrency: level base threads + donor bonus, capped.
	Threads int `json:"threads"`
	// DonorStatus is none | supporter | monthly_supporter; DonorBonusThreads is
	// the additive thread bonus and XPBonusPct the XP bonus (percent).
	DonorStatus       string `json:"donor_status"`
	DonorBonusThreads int    `json:"donor_bonus_threads"`
	XPBonusPct        int    `json:"xp_bonus_pct"`
	// DailyGames is the daily scrape limit (threads * daily games per thread).
	DailyGames int `json:"daily_games"`
	// LevelXP is the XP threshold of the current level; NextLevelXP is the
	// threshold of the next level (0 at max level).
	LevelXP     int `json:"level_xp"`
	NextLevelXP int `json:"next_level_xp"`
	// ProgressPct is the progress (0-100) from LevelXP to NextLevelXP.
	ProgressPct int `json:"progress_pct"`
}

// PublicConfig is the runtime rewards/scrape configuration, exposed publicly so
// the web UI can render the current economy without hardcoded values.
type PublicConfig struct {
	GuestThreads        int          `json:"guest_threads"`
	AdminThreads        int          `json:"admin_threads"`
	DailyGamesPerThread int          `json:"daily_games_per_thread"`
	Points              PointsConfig `json:"points"`
	Ranks               []RankConfig `json:"ranks"`
	DonorTiers          []DonorTier  `json:"donor_tiers"`
}

// DonorTier is a donation status with its additive thread bonus and XP bonus.
type DonorTier struct {
	Status       string `json:"status"`
	BonusThreads int    `json:"bonus_threads"`
	XPBonusPct   int    `json:"xp_bonus_pct"`
}

// RankConfig is a level band that unlocks a thread count.
type RankConfig struct {
	Rank     string `json:"rank"`
	MinLevel int    `json:"min_level"`
	MaxLevel int    `json:"max_level"`
	Threads  int    `json:"threads"`
}

// PointsConfig is the XP awarded per approved contribution type.
type PointsConfig struct {
	TextMetadata  int `json:"text_metadata"`
	ImageMetadata int `json:"image_metadata"`
	VideoMetadata int `json:"video_metadata"`
	SAPImage      int `json:"sap_image"`
	// NewGame is an extra bonus on top of the field/media XP when a brand-new
	// game contribution is approved.
	NewGame int `json:"new_game"`
}

// PublicProfile is a user's public profile page payload.
type PublicProfile struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
	XP          int       `json:"xp"`
	Level       int       `json:"level"`
	Rank        string    `json:"rank"`
	Threads     int       `json:"threads"`
	DonorStatus string    `json:"donor_status"`
	AvatarKey   string    `json:"avatar_key"`
	Approved    int       `json:"approved"`
	Submitted   int       `json:"submitted"`
	Followers   int       `json:"followers"`
	Following   int       `json:"following"`
	IsFollowing bool      `json:"is_following"`
}

// UserSubmissionItem is one row in a public profile's recent submissions feed,
// which mixes system art packs and metadata contributions.
type UserSubmissionItem struct {
	Kind      string     `json:"kind"` // "sap" | "metadata"
	ID        uuid.UUID  `json:"id"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	GameID    *uuid.UUID `json:"game_id,omitempty"`
	SystemID  string     `json:"system_id,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// Submission is a system art pack submitted by a registered user.
type Submission struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	PackID       string     `json:"pack_id" db:"pack_id"`
	Name         string     `json:"name" db:"name"`
	Author       string     `json:"author" db:"author"`
	Description  string     `json:"description" db:"description"`
	DonationURL  string     `json:"donation_url" db:"donation_url"`
	AI           bool       `json:"ai" db:"ai"`
	Version      string     `json:"version" db:"version"`
	Status       string     `json:"status" db:"status"`
	AnonUserID   string     `json:"anon_user_id" db:"anon_user_id"`
	UserID       *uuid.UUID `json:"user_id,omitempty" db:"user_id"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
	ReviewedAt   *time.Time `json:"reviewed_at" db:"reviewed_at"`
	ReviewedBy   *uuid.UUID `json:"reviewed_by" db:"reviewed_by"`
	AdminVersion string     `json:"admin_version" db:"admin_version"`
	// Contribution is true when this submission adds images/description to an
	// existing approved pack instead of creating a brand-new one.
	Contribution bool `json:"contribution" db:"contribution"`
}

// SubmissionFile is a single file belonging to a submission.
type SubmissionFile struct {
	ID           uuid.UUID `json:"id" db:"id"`
	SubmissionID uuid.UUID `json:"submission_id" db:"submission_id"`
	ObjectKey    string    `json:"object_key" db:"object_key"`
	FileName     string    `json:"file_name" db:"file_name"`
	SystemID     string    `json:"system_id" db:"system_id"`
	Kind         string    `json:"kind" db:"kind"`
	Size         int64     `json:"size" db:"size"`
	MimeType     string    `json:"mime_type" db:"mime_type"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	Reason       string    `json:"reason,omitempty" db:"reason"`
	// ReplacesObjectKey is set on review detail when the upload replaces an
	// already-published image (the canonical key the new image will replace).
	ReplacesObjectKey string `json:"replaces_object_key,omitempty" db:"-"`
	// Changed is true on review detail when this file differs from the pack's
	// previous version (or is a brand new system), so update reviews only show
	// what actually changed.
	Changed bool `json:"changed" db:"-"`
}

// SubmissionLog records who did what to a submission and when.
type SubmissionLog struct {
	ID           uuid.UUID `json:"id" db:"id"`
	SubmissionID uuid.UUID `json:"submission_id" db:"submission_id"`
	Action       string    `json:"action" db:"action"`
	UserID       string    `json:"user_id" db:"user_id"`
	UserAgent    string    `json:"user_agent" db:"user_agent"`
	Detail       string    `json:"detail" db:"detail"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	// UserName is the resolved username of the actor, for display.
	UserName string `json:"user_name,omitempty" db:"-"`
}

// CreateSubmissionRequest is the payload for POST /api/v1/submissions.
type CreateSubmissionRequest struct {
	Name        string                `json:"name"`
	Author      string                `json:"author"`
	Description string                `json:"description"`
	DonationURL string                `json:"donation_url"`
	AI          bool                  `json:"ai"`
	Files       []SubmissionFileInput `json:"files"`
	// Contribution mode: when true, PackID must reference an existing approved
	// pack and this submission adds images/description to it instead of
	// creating a brand-new pack.
	Contribution bool   `json:"contribution"`
	PackID       string `json:"pack_id"`
}

// SubmissionFileInput is an uploaded file to register on a submission.
type SubmissionFileInput struct {
	ObjectKey string `json:"object_key"`
	FileName  string `json:"file_name"`
	SystemID  string `json:"system_id,omitempty"`
	Kind      string `json:"kind"` // background, preview, theme, logo
	MimeType  string `json:"mime_type"`
	Size      int64  `json:"size"`
	Reason    string `json:"reason,omitempty"`
}

// SubmissionUploadURLRequest presigns an upload to the pack's canonical
// location from the pack name, without a submission row existing yet.
type SubmissionUploadURLRequest struct {
	Name     string `json:"name"`
	FileName string `json:"file_name"`
	SystemID string `json:"system_id,omitempty"`
	Kind     string `json:"kind"`
	MimeType string `json:"mime_type"`
	Size     int64  `json:"size"`
}

// UploadRequest is the payload for POST /api/v1/submissions/{id}/upload.
// It asks the server for a presigned URL to upload a single file to R2.
type UploadRequest struct {
	FileName string `json:"file_name"`
	Kind     string `json:"kind"` // background, preview, theme, logo
	SystemID string `json:"system_id,omitempty"`
	Size     int64  `json:"size"`
	MimeType string `json:"mime_type"`
}

// UploadResponse returns the presigned URL the client must PUT to.
type UploadResponse struct {
	UploadURL string    `json:"upload_url"`
	ObjectKey string    `json:"object_key"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Pack is an approved system art pack exposed publicly.
type Pack struct {
	PackID      string `json:"folder"`
	Name        string `json:"name"`
	Author      string `json:"author"`
	Description string `json:"description"`
	DonationURL string `json:"donation_url"`
	AI          bool   `json:"ai"`
	Version     string `json:"version"`
	Preview     string `json:"preview"`
	// Backgrounds lists some of the published background object keys, so
	// clients can render the pack's icons without knowing every system.
	Backgrounds []string `json:"backgrounds,omitempty"`
	// Downloads is the lifetime install/download counter for the pack.
	Downloads int64 `json:"downloads"`
	// SystemsCovered is the number of distinct systems with a published
	// background image in the pack.
	SystemsCovered int `json:"systems_covered"`
	// SubmittedBy is the username of the user behind the latest approved
	// revision of the pack.
	SubmittedBy string `json:"submitted_by,omitempty"`
	// Contributions is the number of pending/draft contributions to this pack.
	Contributions int `json:"contributions"`
	// Contributors lists the usernames with an approved contribution to the pack.
	Contributors []string `json:"contributors,omitempty"`
}

// PackFile is a published file of an approved pack (background, preview,
// theme or logo) with its object key and a ready-to-use public URL.
type PackFile struct {
	Kind      string `json:"kind"`
	SystemID  string `json:"system_id,omitempty"`
	FileName  string `json:"file_name"`
	ObjectKey string `json:"object_key"`
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	Mime      string `json:"mime"`
}

// PackContribution is a pending or draft contribution to an approved pack.
type PackContribution struct {
	ID        uuid.UUID `json:"id"`
	Status    string    `json:"status"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	FileCount int       `json:"file_count"`
}

// PackDetail is the full public payload of a single approved pack, used by
// front-ends to install it. It includes every published file.
type PackDetail struct {
	Pack
	Files []PackFile `json:"files"`
	// Contributions are the pending/draft contributions to this pack.
	Contributions []PackContribution `json:"contributions,omitempty"`
}

// LoginResult is returned on a successful admin login.
type LoginResult struct {
	Token     string    `json:"token"`
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SubmissionDetail is a submission bundled with its files and audit logs.
type SubmissionDetail struct {
	Submission
	Files       []SubmissionFile `json:"files"`
	Logs        []SubmissionLog  `json:"logs"`
	SubmittedBy string           `json:"submitted_by,omitempty"`
	// ReviewedByName is the username of the reviewer that last reviewed it.
	ReviewedByName string `json:"reviewed_by_name,omitempty" db:"-"`
	// PointsEarned is the XP awarded for this contribution once approved.
	PointsEarned int `json:"points_earned" db:"-"`
	// BasePointsEarned is the same XP before the donor boost, so the UI can tell
	// whether an approval was boosted.
	BasePointsEarned int `json:"base_points_earned" db:"-"`
}

// RegisterRequest is the payload for POST /api/v1/register.
type RegisterRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// UserLoginRequest is the payload for POST /api/v1/login.
type UserLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// AuthResult is returned on successful registration or login.
type AuthResult struct {
	User       User   `json:"user"`
	Token      string `json:"token"`
	IsAdmin    bool   `json:"is_admin"`
	AdminToken string `json:"admin_token,omitempty"`
}

// VerifyEmailRequest is the payload for POST /api/v1/verify-email.
type VerifyEmailRequest struct {
	Token string `json:"token"`
}

// ResendVerificationRequest is the payload for POST /api/v1/resend-verification.
type ResendVerificationRequest struct {
	Email string `json:"email"`
}

// ForgotPasswordRequest is the payload for POST /api/v1/forgot-password.
type ForgotPasswordRequest struct {
	Email string `json:"email"`
}

// ResetPasswordRequest is the payload for POST /api/v1/reset-password.
type ResetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// ReviewRequest is the payload for admin approve/reject actions.
type ReviewRequest struct {
	Version string `json:"version"`
	Comment string `json:"comment"`
}

// ---------------------------------------------------------------------------
// Metadata (game metadata / system metadata)
// ---------------------------------------------------------------------------

// Metadata status values.
const (
	MetadataCreated  = "created"
	MetadataPending  = "pending"
	MetadataApproved = "approved"
	MetadataRejected = "rejected"
)

// Media kind values.
const (
	MediaCover      = "cover"
	MediaBoxFront   = "boxfront"
	MediaBoxBack    = "boxback"
	MediaScreenshot = "screenshot"
	MediaLogo       = "logo"
	MediaFanart     = "fanart"
	MediaVideo      = "video"
)

// MetadataSystem is a system in the metadata catalog, seeded by the DAT importer
// (independent from the art-pack systems.json catalog).
type MetadataSystem struct {
	ID          string `json:"id" db:"id"`
	Name        string `json:"name" db:"name"`
	ShortName   string `json:"short_name" db:"short_name"`
	Region      string `json:"region" db:"region"`
	Description string `json:"description" db:"description"`
	// Family is the broad category: arcade, console, computer, handheld, virtual.
	Family string `json:"family" db:"family"`
	// Group is the sub-group within a family, by emulator (e.g. "mame-fbneo",
	// "flycast" for arcade boards). Empty when the system belongs to none.
	Group string `json:"group" db:"system_group"`
	// Virtual systems own no games and aggregate their family/group instead
	// (e.g. "arc" resolves to every arcade board).
	Virtual   bool      `json:"virtual" db:"virtual"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
	// Catalog stats, populated on the systems list endpoint.
	TotalGames int `json:"total_games" db:"total_games"`
	Base       int `json:"base" db:"base"`
	Hack       int `json:"hack" db:"hack"`
	Homebrew   int `json:"homebrew" db:"homebrew"`
	// Completion percentages over all the system's games. MetadataPct is the
	// combined score (average of TextPct and MediaPct); TextPct/MediaPct break
	// it down into text metadata vs media coverage.
	MetadataPct float64 `json:"metadata_pct" db:"metadata_pct"`
	TextPct     float64 `json:"text_pct" db:"text_pct"`
	MediaPct    float64 `json:"media_pct" db:"media_pct"`
}

// MetadataFamily is a broad system category (e.g. "arcade") exposed by the
// public scraping API so a caller can scrape a whole family at once.
type MetadataFamily struct {
	Family     string `json:"family" db:"family"`
	Systems    int    `json:"systems" db:"systems"`
	TotalGames int    `json:"total_games" db:"total_games"`
}

// MetadataGroup is a sub-group of systems within a family (e.g. "mame-fbneo"
// inside the arcade family), exposed by the public scraping API.
type MetadataGroup struct {
	Group      string `json:"group" db:"system_group"`
	Family     string `json:"family" db:"family"`
	Systems    int    `json:"systems" db:"systems"`
	TotalGames int    `json:"total_games" db:"total_games"`
}

// Language is a supported description language (code like "es", "de", ...).
type Language struct {
	Code       string `json:"code" db:"code"`
	Name       string `json:"name" db:"name"`
	NativeName string `json:"native_name" db:"native_name"`
}

// Genre is a canonical game genre from the genres catalog.
type Genre struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
}

// Region is a canonical region from the regions catalog. The catalog order is
// the priority used to resolve a game's primary name/release/media.
type Region struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
}

// GameRegion is a game's text and media for a single region.
type GameRegion struct {
	Region       string  `json:"region"`
	Name         string  `json:"name,omitempty"`
	ReleaseYear  *int    `json:"release_year,omitempty"`
	ReleaseMonth *int    `json:"release_month,omitempty"`
	Media        []Media `json:"media,omitempty"`
}

// Game is a single game in the metadata catalog.
type Game struct {
	ID           uuid.UUID `json:"id" db:"id"`
	SystemID     string    `json:"system_id" db:"system_id"`
	Name         string    `json:"name" db:"name"`
	Description  string    `json:"description" db:"description"`
	ReleaseYear  *int      `json:"release_year" db:"release_year"`
	ReleaseMonth *int      `json:"release_month" db:"release_month"`
	Publisher    string    `json:"publisher" db:"publisher"`
	Developer    string    `json:"developer" db:"developer"`
	Genre        string    `json:"genre" db:"genre"`
	Rating       int       `json:"rating" db:"rating"`
	Type         string    `json:"type" db:"type"` // base | hack | homebrew
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" db:"updated_at"`
	SystemName   string    `json:"system_name,omitempty" db:"system_name"`
	Cover        string    `json:"cover,omitempty" db:"cover"`
	// CoverUpdated is the cover media row's created_at, used by the frontend to
	// bust the browser cache when the cover is replaced.
	CoverUpdated string `json:"cover_updated,omitempty" db:"-"`
	// Scrapes is the lifetime scrape count (from game_scrape_stats), populated
	// on the list endpoints.
	Scrapes int64 `json:"scrapes" db:"-"`
	// Computed metadata status flags, populated on the list endpoints.
	TextComplete    bool `json:"text_complete,omitempty" db:"-"`
	HasTranslations bool `json:"has_translations,omitempty" db:"-"`
	HasScreenshot   bool `json:"has_screenshot,omitempty" db:"-"`
	HasFanart       bool `json:"has_fanart,omitempty" db:"-"`
	HasVideo        bool `json:"has_video,omitempty" db:"-"`
	HasLogo         bool `json:"has_logo,omitempty" db:"-"`
}

// GameDetail bundles a game with its ROM dumps, media and translations.
// Description is resolved for the requested lang (falling back to English);
// Translations lists the languages that have a translation for this game.
type GameDetail struct {
	Game
	Roms    []Rom        `json:"roms"`
	Media   []Media      `json:"media"`
	Regions []GameRegion `json:"regions,omitempty"`
	// Region is the resolved primary region (first region with data) and is kept
	// for the public scrape payload; the web uses Regions instead.
	Region       string          `json:"region,omitempty"`
	Lang         string          `json:"lang,omitempty"`
	Translations []Language      `json:"translations,omitempty"`
	Contributors []UserCountStat `json:"contributors,omitempty"`
}

// Rom is a ROM dump of a game, identified by its hashes.
type Rom struct {
	ID        uuid.UUID `json:"id" db:"id"`
	GameID    uuid.UUID `json:"game_id" db:"game_id"`
	Name      string    `json:"name" db:"name"`
	Size      int64     `json:"size" db:"size"`
	CRC       string    `json:"crc" db:"crc"`
	MD5       string    `json:"md5" db:"md5"`
	SHA1      string    `json:"sha1" db:"sha1"`
	SHA256    string    `json:"sha256" db:"sha256"`
	Region    string    `json:"region" db:"region"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// Media is an image/fan-art asset attached to a game or system.
type Media struct {
	ID        uuid.UUID  `json:"id" db:"id"`
	GameID    *uuid.UUID `json:"game_id" db:"game_id"`
	SystemID  *string    `json:"system_id" db:"system_id"`
	Kind      string     `json:"kind" db:"kind"`
	ObjectKey string     `json:"object_key" db:"object_key"`
	Mime      string     `json:"mime" db:"mime"`
	Size      int64      `json:"size" db:"size"`
	// Region is set for regional media (cover/logo); empty for the rest.
	Region    string    `json:"region,omitempty" db:"region"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	// Who contributed the asset; nil when it came from the importer (system).
	SubmittedBy     *uuid.UUID `json:"submitted_by,omitempty" db:"submitted_by"`
	SubmittedByName string     `json:"submitted_by_name,omitempty" db:"-"`
}

// MetadataSubmission is a user contribution of metadata/media for a game or
// system, reviewed by admins before being applied.
type MetadataSubmission struct {
	ID       uuid.UUID  `json:"id" db:"id"`
	GameID   *uuid.UUID `json:"game_id" db:"game_id"`
	SystemID *string    `json:"system_id" db:"system_id"`
	UserID   uuid.UUID  `json:"user_id" db:"user_id"`
	Status   string     `json:"status" db:"status"`
	// Kind distinguishes a plain edit of an existing game/system ("edit") from a
	// brand-new game contribution ("new_game").
	Kind    string          `json:"kind" db:"kind"`
	Payload json.RawMessage `json:"payload" db:"payload"`
	// OldPayload and OldMedia snapshot the target's published text and media at
	// approval time, so the review detail still shows the "old" side after the
	// target has been updated. The replaced media objects are kept under the
	// history/ prefix in R2.
	OldPayload    json.RawMessage `json:"old_payload" db:"old_payload"`
	OldMedia      json.RawMessage `json:"old_media" db:"old_media"`
	ReviewComment string          `json:"review_comment" db:"review_comment"`
	CreatedAt     time.Time       `json:"created_at" db:"created_at"`
	ReviewedAt    *time.Time      `json:"reviewed_at" db:"reviewed_at"`
	ReviewedBy    *uuid.UUID      `json:"reviewed_by" db:"reviewed_by"`
	// Computed usernames for the submitter and reviewer.
	SubmittedByName string `json:"submitted_by_name,omitempty" db:"-"`
	ReviewedByName  string `json:"reviewed_by_name,omitempty" db:"-"`
	// Computed for the admin review list: the target's display data (game name,
	// system name and cover) and the kinds of change the contribution carries
	// (payload text fields and uploaded media kinds).
	GameName     string   `json:"game_name,omitempty" db:"-"`
	SystemName   string   `json:"system_name,omitempty" db:"-"`
	Cover        string   `json:"cover,omitempty" db:"-"`
	CoverUpdated string   `json:"cover_updated,omitempty" db:"-"`
	ChangeKinds  []string `json:"change_kinds,omitempty" db:"-"`
	// PointsEarned is the XP awarded for this contribution once approved.
	PointsEarned int `json:"points_earned" db:"-"`
	// BasePointsEarned is the same XP before the donor boost, so the UI can tell
	// whether an approval was boosted.
	BasePointsEarned int `json:"base_points_earned" db:"-"`
}

// VideoMeta holds probe metadata captured from an uploaded video so reviewers
// can inspect format/resolution/fps/duration before approving.
type VideoMeta struct {
	Format      string  `json:"format"`
	Codec       string  `json:"codec"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	FPS         int     `json:"fps"`
	DurationSec float64 `json:"duration_sec"`
}

// MetadataSubmissionFile is a media file uploaded for a metadata submission.
type MetadataSubmissionFile struct {
	ID           uuid.UUID `json:"id" db:"id"`
	SubmissionID uuid.UUID `json:"submission_id" db:"submission_id"`
	Kind         string    `json:"kind" db:"kind"`
	ObjectKey    string    `json:"object_key" db:"object_key"`
	FileName     string    `json:"file_name" db:"file_name"`
	Mime         string    `json:"mime" db:"mime"`
	Size         int64     `json:"size" db:"size"`
	// Region is set for regional media (cover/logo); empty otherwise.
	Region string `json:"region,omitempty" db:"region"`
	// IsDelete requests removing the existing media for (kind, region).
	IsDelete bool `json:"delete,omitempty" db:"is_delete"`
	// IsMove references an existing media being moved to another region.
	IsMove    bool      `json:"move,omitempty" db:"is_move"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	// Video metadata, captured when a video submission is uploaded so reviewers
	// can inspect the source without downloading it.
	VideoFormat string  `json:"video_format,omitempty" db:"video_format"`
	VideoCodec  string  `json:"video_codec,omitempty" db:"video_codec"`
	Width       int     `json:"width,omitempty" db:"width"`
	Height      int     `json:"height,omitempty" db:"height"`
	FPS         int     `json:"fps,omitempty" db:"fps"`
	DurationSec float64 `json:"duration_sec,omitempty" db:"duration_sec"`
}

// MetadataSubmissionDetail bundles a contribution with its uploaded files and a
// snapshot of the current target values (game or system) and its media, so
// admins can compare the proposed change against what is already published
// before approving. Exactly one of Game/System is set (the contribution target).
type MetadataSubmissionDetail struct {
	Submission *MetadataSubmission      `json:"submission"`
	Files      []MetadataSubmissionFile `json:"files"`
	Game       *GameDetail              `json:"game,omitempty"`
	System     *MetadataSystem          `json:"system,omitempty"`
	Media      []Media                  `json:"media"`
}

// MetadataSubmissionRequest is the payload for POST /api/v1/metadata/submissions.
type MetadataSubmissionRequest struct {
	GameID   *uuid.UUID              `json:"game_id"`
	SystemID *string                 `json:"system_id"`
	Kind     string                  `json:"kind"`
	Payload  map[string]any          `json:"payload"`
	Files    []MetadataUploadRequest `json:"files"`
}

// MetadataUploadRequest is the payload for requesting a presigned media upload.
type MetadataUploadRequest struct {
	Kind      string `json:"kind"`
	FileName  string `json:"file_name"`
	MimeType  string `json:"mime_type"`
	Size      int64  `json:"size"`
	ObjectKey string `json:"object_key,omitempty"`
	// Region applies to regional media (cover/logo).
	Region string `json:"region,omitempty"`
	// Delete removes the existing media for (kind, region) instead of adding it.
	Delete bool `json:"delete,omitempty"`
	// Move references an existing media to be moved to another region.
	Move bool `json:"move,omitempty"`
}

// MetadataUploadURLRequest is the payload for presigning a media upload to its
// final canonical location before the submission row exists.
type MetadataUploadURLRequest struct {
	GameID   *uuid.UUID `json:"game_id"`
	SystemID *string    `json:"system_id"`
	Kind     string     `json:"kind"`
	FileName string     `json:"file_name"`
	MimeType string     `json:"mime_type"`
	Size     int64      `json:"size"`
	Region   string     `json:"region,omitempty"`
}

// MediaReviewRequest is the payload for admin approve/reject of a metadata submission.
type MediaReviewRequest struct {
	Comment string `json:"comment"`
}
