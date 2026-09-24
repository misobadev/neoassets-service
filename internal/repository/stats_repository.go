package repository

import (
	"fmt"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/internal/progression"
)

// CountGames returns the total number of games in the catalog.
func (r *Repository) CountGames() (int, error) {
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM games`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountApprovedPacks returns the number of distinct approved system art packs.
func (r *Repository) CountApprovedPacks() (int, error) {
	var n int
	if err := r.db.QueryRow(`SELECT count(DISTINCT pack_id) FROM submissions WHERE status = 'approved'`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountMetadataSystems returns the number of real systems in the metadata
// catalog. Virtual systems (e.g. the "arcade" aggregate) are excluded
// because they own no games and are not browsable, so the dashboard count
// matches the metadata browse page.
func (r *Repository) CountMetadataSystems() (int, error) {
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM metadata_systems WHERE NOT virtual`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountUsers returns the number of non-hidden accounts.
func (r *Repository) CountUsers() (int, error) {
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM users WHERE NOT hidden`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountApprovedContributions returns the total number of approved contributions
// (SAP submissions + metadata submissions).
func (r *Repository) CountApprovedContributions() (int, error) {
	var n int
	err := r.db.QueryRow(`
		SELECT (SELECT count(*) FROM submissions WHERE status = 'approved')
		     + (SELECT count(*) FROM metadata_submissions WHERE status = 'approved')`,
	).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// TopLevels returns the users with the most XP (hidden users excluded), with
// their level, rank and unlocked threads.
func (r *Repository) TopLevels(limit int) ([]models.LevelStat, error) {
	rows, err := r.db.Query(`
		SELECT id, username, xp, role, donor_status, avatar_key
		FROM users WHERE NOT hidden
		ORDER BY xp DESC, username ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.LevelStat{}
	for rows.Next() {
		var id uuid.UUID
		var username, role, donor, avatar string
		var xp int
		if err := rows.Scan(&id, &username, &xp, &role, &donor, &avatar); err != nil {
			return nil, err
		}
		level := progression.LevelForXP(xp)
		out = append(out, models.LevelStat{
			ID:        id,
			Username:  username,
			AvatarKey: avatar,
			XP:        xp,
			Level:     level,
			Rank:      progression.RankForLevel(level),
			Threads:   ThreadsForUser(role, donor, xp),
		})
	}
	return out, rows.Err()
}

// TopContributions returns users with the most approved contributions across
// both SAP and metadata, excluding hidden users. A metadata submission counts
// once, while a SAP submission counts once per uploaded image file.
func (r *Repository) TopContributions(limit int) ([]models.UserCountStat, error) {
	return r.topApprovedContributions(limit, 0)
}

// TopApprovedThisWeek returns users with the most approved contributions in
// the last seven days, using the same counting rule as TopContributions.
func (r *Repository) TopApprovedThisWeek(limit int) ([]models.UserCountStat, error) {
	return r.topApprovedContributions(limit, 7)
}

// topApprovedContributions counts approved contributions per user. When
// sinceDays > 0 only contributions reviewed within that window are counted.
func (r *Repository) topApprovedContributions(limit, sinceDays int) ([]models.UserCountStat, error) {
	sapFilter := ""
	metaFilter := ""
	if sinceDays > 0 {
		sapFilter = fmt.Sprintf(" AND s.reviewed_at >= NOW() - INTERVAL '%d days'", sinceDays)
		metaFilter = fmt.Sprintf(" AND m.reviewed_at >= NOW() - INTERVAL '%d days'", sinceDays)
	}
	rows, err := r.db.Query(`
		SELECT u.id, u.username, u.avatar_key, SUM(t.c) AS c
		FROM (
			SELECT s.user_id AS user_id, COUNT(sf.id) AS c
			FROM submissions s
			JOIN submission_files sf ON sf.submission_id = s.id
			WHERE s.status = 'approved' AND s.user_id IS NOT NULL`+sapFilter+`
			GROUP BY s.user_id
			UNION ALL
			SELECT m.user_id AS user_id, COUNT(*) AS c
			FROM metadata_submissions m
			WHERE m.status = 'approved'`+metaFilter+`
			GROUP BY m.user_id
		) t
		JOIN users u ON u.id = t.user_id
		WHERE NOT u.hidden
		GROUP BY u.id, u.username, u.avatar_key
		ORDER BY SUM(t.c) DESC, u.username ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.UserCountStat{}
	for rows.Next() {
		var s models.UserCountStat
		if err := rows.Scan(&s.ID, &s.Username, &s.AvatarKey, &s.Count); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RecentApprovedPacks returns the most recently approved SAP submissions.
func (r *Repository) RecentApprovedPacks(limit int) ([]models.RecentPack, error) {
	rows, err := r.db.Query(`
		SELECT s.id, s.pack_id, s.name, s.author,
		       COALESCE(NULLIF(s.admin_version,''), s.version),
		       s.created_at, u.username,
		       COALESCE((
		         SELECT sf.object_key
		         FROM submission_files sf
		         JOIN submissions s2 ON s2.id = sf.submission_id
		         WHERE s2.pack_id = s.pack_id AND s2.status = 'approved'
		           AND sf.kind = 'background'
		           AND sf.system_id IN ('snes','ps1','gba','genesis','2600')
		         ORDER BY array_position(ARRAY['snes','ps1','gba','genesis','2600'], sf.system_id), sf.created_at DESC
		         LIMIT 1
		       ), '') AS image
		FROM submissions s LEFT JOIN users u ON u.id = s.user_id
		WHERE s.status = 'approved'
		ORDER BY s.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.RecentPack{}
	for rows.Next() {
		var s models.RecentPack
		if err := rows.Scan(&s.ID, &s.PackID, &s.Name, &s.Author, &s.Version, &s.CreatedAt, &s.AuthorName, &s.Image); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RecentApprovedMetadata returns the most recently approved game metadata
// contributions (game contributions only; system ones are excluded).
func (r *Repository) RecentApprovedMetadata(limit int) ([]models.RecentMetadata, error) {
	rows, err := r.db.Query(`
		SELECT m.id, m.game_id, COALESCE(g.system_id,''), COALESCE(g.name,''), COALESCE(s.name,''),
		       COALESCE(u.username,''), m.created_at
		FROM metadata_submissions m
		LEFT JOIN games g ON g.id = m.game_id
		LEFT JOIN metadata_systems s ON s.id = COALESCE(m.system_id, g.system_id)
		LEFT JOIN users u ON u.id = m.user_id
		WHERE m.status = 'approved' AND m.game_id IS NOT NULL
		ORDER BY m.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.RecentMetadata{}
	for rows.Next() {
		var s models.RecentMetadata
		var gameID *uuid.UUID
		if err := rows.Scan(&s.ID, &gameID, &s.SystemID, &s.GameName, &s.SystemName, &s.SubmittedBy, &s.CreatedAt); err != nil {
			return nil, err
		}
		s.GameID = gameID
		out = append(out, s)
	}
	return out, rows.Err()
}
