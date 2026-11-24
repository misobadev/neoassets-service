package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"neoassets/internal/apperr"
	"strings"
	"time"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/internal/repository"
	"neoassets/pkg/auth"
)

const (
	maxCredentialNameLen = 80
	maxCredentialDescLen = 280
	maxCredentialURLLen  = 255
	// maxDeveloperAppsPerUser caps how many developer applications one account
	// may hold, preventing quota multiplication through app creation.
	maxDeveloperAppsPerUser = 10
)

// DeveloperService manages self-service scraping credentials: developer
// applications and personal user API keys.
type DeveloperService struct {
	repo   *repository.Repository
	sealer *auth.Sealer
}

// apiKeySealPurpose labels the key derived for sealing personal API keys.
const apiKeySealPurpose = "neoassets/user-api-key/v1"

// NewDeveloperService creates a DeveloperService. secret seeds the key that
// seals personal API keys so their owners can copy them again.
func NewDeveloperService(repo *repository.Repository, secret string) (*DeveloperService, error) {
	sealer, err := auth.NewSealer(secret, apiKeySealPurpose)
	if err != nil {
		return nil, err
	}
	return &DeveloperService{repo: repo, sealer: sealer}, nil
}

// CreateApp registers a developer application and returns it with its plaintext
// secret (shown only once).
func (s *DeveloperService) CreateApp(ctx context.Context, userID uuid.UUID, req models.CreateDeveloperAppRequest) (*models.CreatedDeveloperApp, error) {
	name, err := credentialName(req.Name)
	if err != nil {
		return nil, err
	}
	if len(req.Description) > maxCredentialDescLen {
		return nil, fmt.Errorf("description must be at most %d characters", maxCredentialDescLen)
	}
	if len(req.HomepageURL) > maxCredentialURLLen {
		return nil, fmt.Errorf("homepage_url must be at most %d characters", maxCredentialURLLen)
	}

	clientID, err := auth.GenerateClientID()
	if err != nil {
		return nil, err
	}
	secret, err := auth.GenerateSecret()
	if err != nil {
		return nil, err
	}
	debugPassword, err := auth.GenerateDebugPassword()
	if err != nil {
		return nil, err
	}
	n, err := s.repo.CountDeveloperApps(ctx, userID)
	if err != nil {
		return nil, err
	}
	if n >= maxDeveloperAppsPerUser {
		return nil, fmt.Errorf("maximum of %d developer applications reached", maxDeveloperAppsPerUser)
	}
	app, err := s.repo.CreateDeveloperApp(ctx,
		userID, name, strings.TrimSpace(req.Description), strings.TrimSpace(req.HomepageURL),
		clientID, auth.HashToken(secret), auth.HashToken(debugPassword),
	)
	if err != nil {
		return nil, err
	}
	return &models.CreatedDeveloperApp{DeveloperApp: *app, ClientSecret: secret, DebugPassword: debugPassword}, nil
}

// ListApps returns the user's developer applications with their per-software
// usage statistics attached.
func (s *DeveloperService) ListApps(ctx context.Context, userID uuid.UUID) ([]models.DeveloperApp, error) {
	apps, err := s.repo.ListDeveloperAppsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	stats, err := s.repo.ListSoftwareStatsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range apps {
		if sw, ok := stats[apps[i].ID]; ok {
			apps[i].Software = sw
		}
	}
	return apps, nil
}

// RevokeApp revokes an application owned by the user.
func (s *DeveloperService) RevokeApp(ctx context.Context, userID, id uuid.UUID) error {
	return s.repo.RevokeDeveloperApp(ctx, id, userID)
}

// RotateApp issues a new secret and debug password for an app and returns them
// with the plaintext values (shown only once).
func (s *DeveloperService) RotateApp(ctx context.Context, userID, id uuid.UUID) (*models.CreatedDeveloperApp, error) {
	secret, err := auth.GenerateSecret()
	if err != nil {
		return nil, err
	}
	debugPassword, err := auth.GenerateDebugPassword()
	if err != nil {
		return nil, err
	}
	app, err := s.repo.RotateDeveloperAppSecret(ctx, id, userID, auth.HashToken(secret), auth.HashToken(debugPassword))
	if err != nil {
		return nil, err
	}
	return &models.CreatedDeveloperApp{DeveloperApp: *app, ClientSecret: secret, DebugPassword: debugPassword}, nil
}

// CreateAPIKey issues a personal API key and returns it with the plaintext key.
func (s *DeveloperService) CreateAPIKey(ctx context.Context, userID uuid.UUID, req models.CreateAPIKeyRequest) (*models.CreatedAPIKey, error) {
	name, err := credentialName(req.Name)
	if err != nil {
		return nil, err
	}
	prefix, plaintext, hash, err := auth.GenerateAPIKey()
	if err != nil {
		return nil, err
	}
	sealed, err := s.sealer.Seal(plaintext)
	if err != nil {
		return nil, err
	}
	key, err := s.repo.CreateUserAPIKey(ctx, userID, name, prefix, hash, sealed)
	if err != nil {
		return nil, err
	}
	return &models.CreatedAPIKey{UserAPIKey: *key, Key: plaintext}, nil
}

// RevealAPIKey returns the plaintext of an active API key owned by the user.
// Keys created before sealing existed cannot be revealed.
func (s *DeveloperService) RevealAPIKey(ctx context.Context, userID, id uuid.UUID) (string, error) {
	key, err := s.repo.GetActiveUserAPIKey(ctx, id, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", apperr.NotFound("API key not found")
		}
		return "", err
	}
	if !key.Copyable {
		return "", apperr.Conflict("this API key was created before keys could be copied again; create a new one")
	}
	plaintext, err := s.sealer.Open(*key.KeySealed)
	if err != nil {
		return "", apperr.Conflict("this API key can no longer be decrypted; create a new one")
	}
	return plaintext, nil
}

// ListAPIKeys returns the user's personal API keys, each with a masked form of
// the key for display.
func (s *DeveloperService) ListAPIKeys(ctx context.Context, userID uuid.UUID) ([]models.UserAPIKey, error) {
	keys, err := s.repo.ListUserAPIKeys(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range keys {
		keys[i].Masked = keys[i].Prefix + "..."
		if !keys[i].Copyable {
			continue
		}
		if plain, err := s.sealer.Open(*keys[i].KeySealed); err == nil {
			keys[i].Masked = auth.MaskAPIKey(plain)
		} else {
			keys[i].Copyable = false
		}
	}
	return keys, nil
}

// RevokeAPIKey revokes a personal API key owned by the user.
func (s *DeveloperService) RevokeAPIKey(ctx context.Context, userID, id uuid.UUID) error {
	return s.repo.RevokeUserAPIKey(ctx, id, userID)
}

// credentialName trims and validates a credential's display name.
func credentialName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", apperr.Invalid("name is required")
	}
	if len(name) > maxCredentialNameLen {
		return "", fmt.Errorf("name must be at most %d characters", maxCredentialNameLen)
	}
	return name, nil
}

// Overview returns every developer app and personal API key with their usage,
// plus totals, for the admin panel.
func (s *DeveloperService) Overview(ctx context.Context) (*models.DeveloperOverview, error) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	apps, err := s.repo.ListAllDeveloperApps(ctx)
	if err != nil {
		return nil, err
	}
	stats, err := s.repo.ListAllSoftwareStats(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := s.repo.ListAllUserAPIKeys(ctx, today)
	if err != nil {
		return nil, err
	}
	day, month, err := s.repo.ScrapeUsageTotals(ctx, today)
	if err != nil {
		return nil, err
	}
	out := &models.DeveloperOverview{
		Apps: make([]models.AdminDeveloperApp, 0, len(apps)),
		Keys: make([]models.AdminAPIKey, 0, len(keys)),
	}
	out.Totals.ScrapesToday = day
	out.Totals.Scrapes30Days = month
	for _, a := range apps {
		a.Software = stats[a.ID]
		if a.RevokedAt == nil {
			out.Totals.ActiveApps++
		}
		out.Totals.APICalls += a.APICalls
		out.Totals.QuotaExceeded += a.QuotaExceeded
		out.Totals.RateLimited += a.RateLimited
		out.Apps = append(out.Apps, a)
	}
	for _, k := range keys {
		if k.RevokedAt == nil {
			out.Totals.ActiveKeys++
		}
		out.Keys = append(out.Keys, k)
	}
	return out, nil
}
