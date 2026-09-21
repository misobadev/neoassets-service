package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Developer applications and personal API keys (scraping API credentials)
// ---------------------------------------------------------------------------

// DeveloperApp is an application registered to consume the public scraping API.
// The client secret hash is never serialized; the plaintext secret is returned
// only once at creation or rotation.
type DeveloperApp struct {
	ID          uuid.UUID `json:"id" db:"id"`
	UserID      uuid.UUID `json:"user_id" db:"user_id"`
	Name        string    `json:"name" db:"name"`
	Description string    `json:"description" db:"description"`
	HomepageURL string    `json:"homepage_url" db:"homepage_url"`
	ClientID    string    `json:"client_id" db:"client_id"`
	SecretHash  string    `json:"-" db:"client_secret_hash"`
	// DebugPassword is a low-sensitivity token that enables the debug query
	// parameters. It is stored as a SHA-256 hash (never serialized); the
	// plaintext is returned once when the app is created or rotated.
	DebugPassword string     `json:"-" db:"debug_password"`
	LastUsedAt    *time.Time `json:"last_used_at" db:"last_used_at"`
	RevokedAt     *time.Time `json:"revoked_at" db:"revoked_at"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" db:"updated_at"`
	// Lifetime usage counters for the developer dashboard.
	APICalls      int64      `json:"api_calls" db:"api_calls"`
	KOScraps      int64      `json:"ko_scraps" db:"ko_scraps"`
	RateLimited   int64      `json:"rate_limited" db:"rate_limited"`
	QuotaExceeded int64      `json:"quota_exceeded" db:"quota_exceeded"`
	DebugCalls    int64      `json:"debug_calls" db:"debug_calls"`
	LastScrapeAt  *time.Time `json:"last_scrape_at" db:"last_scrape_at"`
	// Software holds per-software usage, populated on the list endpoint.
	Software []SoftwareStats `json:"software,omitempty" db:"-"`
}

// SoftwareStats is per-software usage for a developer app. Software is a
// client-supplied name (`softname`) used to distinguish versions.
type SoftwareStats struct {
	AppID         uuid.UUID  `json:"app_id"`
	Name          string     `json:"name"`
	APICalls      int64      `json:"api_calls"`
	KOScraps      int64      `json:"ko_scraps"`
	RateLimited   int64      `json:"rate_limited"`
	QuotaExceeded int64      `json:"quota_exceeded"`
	DebugCalls    int64      `json:"debug_calls"`
	LastScrapeAt  *time.Time `json:"last_scrape_at"`
}

// CreatedDeveloperApp is a developer app bundled with its plaintext secret and
// debug password, returned only when the app is created or its secret is
// rotated.
type CreatedDeveloperApp struct {
	DeveloperApp
	ClientSecret  string `json:"client_secret"`
	DebugPassword string `json:"debug_password"`
}

// UserAPIKey is a personal API key used as the user credential of the scraping
// API. Only the hash and prefix are stored; the plaintext key is shown once.
type UserAPIKey struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	UserID     uuid.UUID  `json:"user_id" db:"user_id"`
	Name       string     `json:"name" db:"name"`
	Prefix     string     `json:"prefix" db:"prefix"`
	KeyHash    string     `json:"-" db:"key_hash"`
	LastUsedAt *time.Time `json:"last_used_at" db:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at" db:"revoked_at"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
}

// CreatedAPIKey is an API key bundled with its plaintext value, returned only
// when the key is created.
type CreatedAPIKey struct {
	UserAPIKey
	Key string `json:"key"`
}

// CreateDeveloperAppRequest is the payload for POST /api/v1/auth/developer/apps.
type CreateDeveloperAppRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	HomepageURL string `json:"homepage_url"`
}

// CreateAPIKeyRequest is the payload for POST /api/v1/auth/api-keys.
type CreateAPIKeyRequest struct {
	Name string `json:"name"`
}

// ---------------------------------------------------------------------------
// Public scraping API payloads
// ---------------------------------------------------------------------------

// ScrapeGameQuery is the set of selectors accepted by GET /api/v1/scrape/games.
// Exactly one of SystemID or Family is required; at least one of a ROM hash or
// Name must be provided. Type (base|hack|homebrew) optionally filters a name
// search.
type ScrapeGameQuery struct {
	CRC      string
	MD5      string
	SHA1     string
	SHA256   string
	Name     string
	SystemID string
	// Family selects every system of a broad category (e.g. "arcade") instead of
	// a single SystemID. Mutually exclusive with SystemID and Group.
	Family string
	// Group selects every system of an emulator sub-group (e.g. "mame-fbneo")
	// instead of a single SystemID. Mutually exclusive with SystemID and Family.
	Group string
	Type  string
	Media []string
	// IncludeRoms requests the full ROM list. It is off by default because
	// some games have dozens of dumps and the client already knows its hash.
	IncludeRoms bool
}

// ScrapeGamesResponse is the payload returned by GET /api/v1/scrape/games.
// MatchedBy is "hash" or "name" and tells the client how the game was found.
// Game is null when nothing matched.
type ScrapeGamesResponse struct {
	SystemID  string      `json:"system_id"`
	MatchedBy string      `json:"matched_by"`
	Game      *ScrapeGame `json:"game"`
}

// PopularGame is a most-scraped game row.
type PopularGame struct {
	ID            uuid.UUID  `json:"id"`
	SystemID      string     `json:"system_id"`
	Name          string     `json:"name"`
	Scrapes       int64      `json:"scrapes"`
	LastScrapedAt *time.Time `json:"last_scraped_at"`
}

// PopularSystem is a most-scraped system row (scrapes aggregated per system).
type PopularSystem struct {
	SystemID string `json:"system_id"`
	Name     string `json:"name"`
	Scrapes  int64  `json:"scrapes"`
}

// ScrapePopularResponse is the payload returned by GET /api/v1/scrape/popular.
type ScrapePopularResponse struct {
	Games []PopularGame `json:"games"`
}

// ScrapeMedia is a media asset exposed with a ready-to-use public URL.
type ScrapeMedia struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

// ScrapeRom is a ROM dump used to identify a game by its hashes.
type ScrapeRom struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	CRC    string `json:"crc"`
	MD5    string `json:"md5"`
	SHA1   string `json:"sha1"`
	SHA256 string `json:"sha256"`
	Region string `json:"region"`
}

// ScrapeGame is the full metadata payload for a single game.
type ScrapeGame struct {
	ID           uuid.UUID `json:"id"`
	SystemID     string    `json:"system_id"`
	SystemName   string    `json:"system_name"`
	Name         string    `json:"name"`
	Region       string    `json:"region"`
	ReleaseYear  *int      `json:"release_year"`
	ReleaseMonth *int      `json:"release_month"`
	Publisher    string    `json:"publisher"`
	Developer    string    `json:"developer"`
	Genre        string    `json:"genre"`
	Rating       int       `json:"rating"`
	Type         string    `json:"type"`
	// Scrapes is the lifetime scrape count for this game (bumped on each
	// successful resolution).
	Scrapes int64 `json:"scrapes"`
	// RomCount and RomName (the primary ROM file name, e.g. "sfa3.zip" for an
	// arcade set) are always returned; Roms is only included with `roms=true`.
	RomCount int           `json:"rom_count"`
	RomName  string        `json:"rom_name"`
	Roms     []ScrapeRom   `json:"roms,omitempty"`
	Media    []ScrapeMedia `json:"media"`
	// Descriptions maps a language code to its description. It is flattened on
	// marshal into `description_<code>` fields (empty when there is no
	// translation for that language).
	Descriptions map[string]string `json:"-"`
}

// MarshalJSON flattens Descriptions into description_<code> fields so every
// enabled language is exposed as its own top-level key.
func (g ScrapeGame) MarshalJSON() ([]byte, error) {
	type alias ScrapeGame
	base, err := json.Marshal(alias(g))
	if err != nil {
		return nil, err
	}
	if len(g.Descriptions) == 0 {
		return base, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(base, &fields); err != nil {
		return nil, err
	}
	for code, text := range g.Descriptions {
		raw, err := json.Marshal(text)
		if err != nil {
			return nil, err
		}
		fields["description_"+code] = raw
	}
	return json.Marshal(fields)
}

// ScrapeQuota is the daily quota state for a scrape subject.
type ScrapeQuota struct {
	DailyLimit int       `json:"daily_limit"`
	Used       int       `json:"used"`
	Remaining  int       `json:"remaining"`
	ResetsAt   time.Time `json:"resets_at"`
}

// ScrapeClient identifies the developer application behind a scrape request.
type ScrapeClient struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ScrapeUser identifies the registered user behind a scrape request.
type ScrapeUser struct {
	Username string `json:"username"`
	Threads  int    `json:"threads"`
}

// ScrapeAccount describes the authenticated scrape subject and its quota.
type ScrapeAccount struct {
	Subject string       `json:"subject"` // "user" | "guest"
	Client  ScrapeClient `json:"client"`
	User    *ScrapeUser  `json:"user"`
	Quota   ScrapeQuota  `json:"quota"`
}
