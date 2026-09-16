package repository

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"neoassets/internal/models"
)

const developerAppCols = `id, user_id, name, description, homepage_url, client_id,
	client_secret_hash, debug_password, last_used_at, revoked_at, created_at, updated_at,
	api_calls, ko_scraps, rate_limited, quota_exceeded, debug_calls, last_scrape_at`

func scanDeveloperApp(row interface{ Scan(...interface{}) error }) (*models.DeveloperApp, error) {
	var a models.DeveloperApp
	err := row.Scan(
		&a.ID, &a.UserID, &a.Name, &a.Description, &a.HomepageURL, &a.ClientID,
		&a.SecretHash, &a.DebugPassword, &a.LastUsedAt, &a.RevokedAt, &a.CreatedAt, &a.UpdatedAt,
		&a.APICalls, &a.KOScraps, &a.RateLimited, &a.QuotaExceeded, &a.DebugCalls, &a.LastScrapeAt,
	)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// CreateDeveloperApp inserts a developer application for the given owner. The
// debug password is stored hashed (SHA-256) like the client secret.
func (r *Repository) CreateDeveloperApp(userID uuid.UUID, name, description, homepageURL, clientID, secretHash, debugPasswordHash string) (*models.DeveloperApp, error) {
	app, err := scanDeveloperApp(r.db.QueryRow(
		`INSERT INTO developer_apps (user_id, name, description, homepage_url, client_id, client_secret_hash, debug_password)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING `+developerAppCols,
		userID, name, description, homepageURL, clientID, secretHash, debugPasswordHash,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to create developer app: %w", err)
	}
	return app, nil
}

// CountDeveloperApps returns the number of active (non-revoked) apps owned by
// the user.
func (r *Repository) CountDeveloperApps(userID uuid.UUID) (int, error) {
	var n int
	err := r.db.QueryRow(
		`SELECT count(*) FROM developer_apps WHERE user_id = $1 AND revoked_at IS NULL`, userID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count developer apps: %w", err)
	}
	return n, nil
}

// ListDeveloperAppsByUser returns the user's developer apps, newest first.
func (r *Repository) ListDeveloperAppsByUser(userID uuid.UUID) ([]models.DeveloperApp, error) {
	rows, err := r.db.Query(
		`SELECT `+developerAppCols+` FROM developer_apps WHERE user_id = $1 ORDER BY created_at DESC`, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list developer apps: %w", err)
	}
	defer rows.Close()
	var list []models.DeveloperApp
	for rows.Next() {
		app, err := scanDeveloperApp(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan developer app: %w", err)
		}
		list = append(list, *app)
	}
	return list, rows.Err()
}

// GetDeveloperAppByClientID returns an app by its public client id, or
// sql.ErrNoRows.
func (r *Repository) GetDeveloperAppByClientID(clientID string) (*models.DeveloperApp, error) {
	return scanDeveloperApp(r.db.QueryRow(
		`SELECT `+developerAppCols+` FROM developer_apps WHERE client_id = $1`, clientID,
	))
}

// RevokeDeveloperApp soft-deletes an app owned by the user.
func (r *Repository) RevokeDeveloperApp(id, userID uuid.UUID) error {
	res, err := r.db.Exec(
		`UPDATE developer_apps SET revoked_at = NOW(), updated_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`,
		id, userID,
	)
	if err != nil {
		return fmt.Errorf("failed to revoke developer app: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RotateDeveloperAppSecret replaces an app's secret hash and debug password
// hash and returns the app.
func (r *Repository) RotateDeveloperAppSecret(id, userID uuid.UUID, secretHash, debugPasswordHash string) (*models.DeveloperApp, error) {
	app, err := scanDeveloperApp(r.db.QueryRow(
		`UPDATE developer_apps SET client_secret_hash = $1, debug_password = $2, updated_at = NOW()
		 WHERE id = $3 AND user_id = $4 AND revoked_at IS NULL
		 RETURNING `+developerAppCols,
		secretHash, debugPasswordHash, id, userID,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to rotate developer app secret: %w", err)
	}
	return app, nil
}

// TouchDeveloperAppLastUsed records the last successful authentication of an app.
func (r *Repository) TouchDeveloperAppLastUsed(clientID string) error {
	_, err := r.db.Exec(`UPDATE developer_apps SET last_used_at = NOW() WHERE client_id = $1`, clientID)
	return err
}

const userAPIKeyCols = `id, user_id, name, prefix, key_hash, last_used_at, revoked_at, created_at`

func scanUserAPIKey(row interface{ Scan(...interface{}) error }) (*models.UserAPIKey, error) {
	var k models.UserAPIKey
	err := row.Scan(&k.ID, &k.UserID, &k.Name, &k.Prefix, &k.KeyHash, &k.LastUsedAt, &k.RevokedAt, &k.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// CreateUserAPIKey inserts a personal API key for the user.
func (r *Repository) CreateUserAPIKey(userID uuid.UUID, name, prefix, keyHash string) (*models.UserAPIKey, error) {
	key, err := scanUserAPIKey(r.db.QueryRow(
		`INSERT INTO user_api_keys (user_id, name, prefix, key_hash)
		 VALUES ($1, $2, $3, $4)
		 RETURNING `+userAPIKeyCols,
		userID, name, prefix, keyHash,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to create API key: %w", err)
	}
	return key, nil
}

// ListUserAPIKeys returns the user's API keys, newest first.
func (r *Repository) ListUserAPIKeys(userID uuid.UUID) ([]models.UserAPIKey, error) {
	rows, err := r.db.Query(
		`SELECT `+userAPIKeyCols+` FROM user_api_keys WHERE user_id = $1 ORDER BY created_at DESC`, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list API keys: %w", err)
	}
	defer rows.Close()
	var list []models.UserAPIKey
	for rows.Next() {
		k, err := scanUserAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan API key: %w", err)
		}
		list = append(list, *k)
	}
	return list, rows.Err()
}

// GetUserAPIKeyByPrefix returns the active API key matching the prefix, or
// sql.ErrNoRows. The caller must verify the hash in constant time.
func (r *Repository) GetUserAPIKeyByPrefix(prefix string) (*models.UserAPIKey, error) {
	return scanUserAPIKey(r.db.QueryRow(
		`SELECT `+userAPIKeyCols+` FROM user_api_keys
		 WHERE prefix = $1 AND revoked_at IS NULL`, prefix,
	))
}

// RevokeUserAPIKey soft-deletes an API key owned by the user.
func (r *Repository) RevokeUserAPIKey(id, userID uuid.UUID) error {
	res, err := r.db.Exec(
		`UPDATE user_api_keys SET revoked_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`,
		id, userID,
	)
	if err != nil {
		return fmt.Errorf("failed to revoke API key: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// TouchUserAPIKeyLastUsed records the last successful use of an API key.
func (r *Repository) TouchUserAPIKeyLastUsed(id uuid.UUID) error {
	_, err := r.db.Exec(`UPDATE user_api_keys SET last_used_at = NOW() WHERE id = $1`, id)
	return err
}

// IncrementScrapeUsage atomically increments the daily counter for a subject and
// returns the new count. The day is a UTC calendar date.
func (r *Repository) IncrementScrapeUsage(day time.Time, subjectType, subjectID string) (int, error) {
	var count int
	err := r.db.QueryRow(
		`INSERT INTO scrape_usage (day, subject_type, subject_id, count)
		 VALUES ($1, $2, $3, 1)
		 ON CONFLICT (day, subject_type, subject_id)
		 DO UPDATE SET count = scrape_usage.count + 1, updated_at = NOW()
		 RETURNING count`,
		day.Format("2006-01-02"), subjectType, subjectID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to increment scrape usage: %w", err)
	}
	return count, nil
}

// GetScrapeUsage returns the current daily count for a subject (0 when none).
func (r *Repository) GetScrapeUsage(day time.Time, subjectType, subjectID string) (int, error) {
	var count int
	err := r.db.QueryRow(
		`SELECT count FROM scrape_usage WHERE day = $1 AND subject_type = $2 AND subject_id = $3`,
		day.Format("2006-01-02"), subjectType, subjectID,
	).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to read scrape usage: %w", err)
	}
	return count, nil
}

// RecordScrapeOutcome bumps a developer app's lifetime counters. Every outcome
// counts as an API call; specific outcomes also bump their own counter.
func (r *Repository) RecordScrapeOutcome(appID uuid.UUID, outcome string) error {
	var query string
	switch outcome {
	case "not_found":
		query = `UPDATE developer_apps
		         SET api_calls = api_calls + 1, ko_scraps = ko_scraps + 1, last_scrape_at = NOW()
		         WHERE id = $1`
	case "rate_limited":
		query = `UPDATE developer_apps
		         SET api_calls = api_calls + 1, rate_limited = rate_limited + 1, last_scrape_at = NOW()
		         WHERE id = $1`
	case "quota_exceeded":
		query = `UPDATE developer_apps
		         SET api_calls = api_calls + 1, quota_exceeded = quota_exceeded + 1, last_scrape_at = NOW()
		         WHERE id = $1`
	default:
		query = `UPDATE developer_apps
		         SET api_calls = api_calls + 1, last_scrape_at = NOW()
		         WHERE id = $1`
	}
	if _, err := r.db.Exec(query, appID); err != nil {
		return fmt.Errorf("failed to record scrape outcome: %w", err)
	}
	return nil
}

// IncrementDebugUsage atomically increments the per-day debug counter for an
// app and returns the new count.
func (r *Repository) IncrementDebugUsage(day time.Time, appID uuid.UUID) (int, error) {
	var count int
	err := r.db.QueryRow(
		`INSERT INTO debug_usage (day, app_id, count)
		 VALUES ($1, $2, 1)
		 ON CONFLICT (day, app_id)
		 DO UPDATE SET count = debug_usage.count + 1, updated_at = NOW()
		 RETURNING count`,
		day.Format("2006-01-02"), appID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to increment debug usage: %w", err)
	}
	return count, nil
}

// IncrementDebugCalls bumps the app's lifetime debug-call counter.
func (r *Repository) IncrementDebugCalls(appID uuid.UUID) error {
	_, err := r.db.Exec(`UPDATE developer_apps SET debug_calls = debug_calls + 1 WHERE id = $1`, appID)
	return err
}

// RecordSoftwareOutcome bumps the per-software counters for an app. Debug calls
// are counted separately and do not increment api_calls.
func (r *Repository) RecordSoftwareOutcome(appID uuid.UUID, name, outcome string) error {
	api, ko, rl, qe, dbg := 1, 0, 0, 0, 0
	switch outcome {
	case "not_found":
		ko = 1
	case "rate_limited":
		rl = 1
	case "quota_exceeded":
		qe = 1
	case "debug":
		api, dbg = 0, 1
	}
	_, err := r.db.Exec(
		`INSERT INTO app_software_stats (app_id, name, api_calls, ko_scraps, rate_limited, quota_exceeded, debug_calls, last_scrape_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		 ON CONFLICT (app_id, name) DO UPDATE SET
		   api_calls = app_software_stats.api_calls + EXCLUDED.api_calls,
		   ko_scraps = app_software_stats.ko_scraps + EXCLUDED.ko_scraps,
		   rate_limited = app_software_stats.rate_limited + EXCLUDED.rate_limited,
		   quota_exceeded = app_software_stats.quota_exceeded + EXCLUDED.quota_exceeded,
		   debug_calls = app_software_stats.debug_calls + EXCLUDED.debug_calls,
		   last_scrape_at = NOW()`,
		appID, name, api, ko, rl, qe, dbg,
	)
	if err != nil {
		return fmt.Errorf("failed to record software outcome: %w", err)
	}
	return nil
}

// ListSoftwareStatsByUser returns the per-software stats for all of a user's
// apps, keyed by app id.
func (r *Repository) ListSoftwareStatsByUser(userID uuid.UUID) (map[uuid.UUID][]models.SoftwareStats, error) {
	rows, err := r.db.Query(
		`SELECT s.app_id, s.name, s.api_calls, s.ko_scraps, s.rate_limited, s.quota_exceeded,
		        s.debug_calls, s.last_scrape_at
		 FROM app_software_stats s
		 JOIN developer_apps a ON a.id = s.app_id
		 WHERE a.user_id = $1
		 ORDER BY s.api_calls DESC, s.name`, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list software stats: %w", err)
	}
	defer rows.Close()

	out := map[uuid.UUID][]models.SoftwareStats{}
	for rows.Next() {
		var s models.SoftwareStats
		if err := rows.Scan(&s.AppID, &s.Name, &s.APICalls, &s.KOScraps, &s.RateLimited,
			&s.QuotaExceeded, &s.DebugCalls, &s.LastScrapeAt); err != nil {
			return nil, fmt.Errorf("failed to scan software stats: %w", err)
		}
		out[s.AppID] = append(out[s.AppID], s)
	}
	return out, rows.Err()
}
