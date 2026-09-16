package services

import (
	"fmt"
	"strings"

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
	repo *repository.Repository
}

// NewDeveloperService creates a DeveloperService.
func NewDeveloperService(repo *repository.Repository) *DeveloperService {
	return &DeveloperService{repo: repo}
}

// CreateApp registers a developer application and returns it with its plaintext
// secret (shown only once).
func (s *DeveloperService) CreateApp(userID uuid.UUID, req models.CreateDeveloperAppRequest) (*models.CreatedDeveloperApp, error) {
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
	n, err := s.repo.CountDeveloperApps(userID)
	if err != nil {
		return nil, err
	}
	if n >= maxDeveloperAppsPerUser {
		return nil, fmt.Errorf("maximum of %d developer applications reached", maxDeveloperAppsPerUser)
	}
	app, err := s.repo.CreateDeveloperApp(
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
func (s *DeveloperService) ListApps(userID uuid.UUID) ([]models.DeveloperApp, error) {
	apps, err := s.repo.ListDeveloperAppsByUser(userID)
	if err != nil {
		return nil, err
	}
	stats, err := s.repo.ListSoftwareStatsByUser(userID)
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
func (s *DeveloperService) RevokeApp(userID, id uuid.UUID) error {
	return s.repo.RevokeDeveloperApp(id, userID)
}

// RotateApp issues a new secret and debug password for an app and returns them
// with the plaintext values (shown only once).
func (s *DeveloperService) RotateApp(userID, id uuid.UUID) (*models.CreatedDeveloperApp, error) {
	secret, err := auth.GenerateSecret()
	if err != nil {
		return nil, err
	}
	debugPassword, err := auth.GenerateDebugPassword()
	if err != nil {
		return nil, err
	}
	app, err := s.repo.RotateDeveloperAppSecret(id, userID, auth.HashToken(secret), auth.HashToken(debugPassword))
	if err != nil {
		return nil, err
	}
	return &models.CreatedDeveloperApp{DeveloperApp: *app, ClientSecret: secret, DebugPassword: debugPassword}, nil
}

// CreateAPIKey issues a personal API key and returns it with the plaintext key
// (shown only once).
func (s *DeveloperService) CreateAPIKey(userID uuid.UUID, req models.CreateAPIKeyRequest) (*models.CreatedAPIKey, error) {
	name, err := credentialName(req.Name)
	if err != nil {
		return nil, err
	}
	prefix, plaintext, hash, err := auth.GenerateAPIKey()
	if err != nil {
		return nil, err
	}
	key, err := s.repo.CreateUserAPIKey(userID, name, prefix, hash)
	if err != nil {
		return nil, err
	}
	return &models.CreatedAPIKey{UserAPIKey: *key, Key: plaintext}, nil
}

// ListAPIKeys returns the user's personal API keys.
func (s *DeveloperService) ListAPIKeys(userID uuid.UUID) ([]models.UserAPIKey, error) {
	return s.repo.ListUserAPIKeys(userID)
}

// RevokeAPIKey revokes a personal API key owned by the user.
func (s *DeveloperService) RevokeAPIKey(userID, id uuid.UUID) error {
	return s.repo.RevokeUserAPIKey(id, userID)
}

// credentialName trims and validates a credential's display name.
func credentialName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if len(name) > maxCredentialNameLen {
		return "", fmt.Errorf("name must be at most %d characters", maxCredentialNameLen)
	}
	return name, nil
}
