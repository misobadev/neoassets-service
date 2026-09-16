package repository

import (
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

// CountMetadataSystems returns the number of systems in the metadata catalog.
func (r *Repository) CountMetadataSystems() (int, error) {
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM metadata_systems`).Scan(&n); err != nil {
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

// TopReviewers returns the users with the most review actions (approved +
// rejected) across SAP and metadata submissions. Hidden users (NeoBot) and
// users without review actions are excluded.
func (r *Repository) TopReviewers(limit int) ([]models.UserStat, error) {
	rows, err := r.db.Query(`
		SELECT r.reviewed_by, u.username, u.avatar_key,
		       COUNT(*) FILTER (WHERE r.status = 'approved') AS approved,
		       COUNT(*) FILTER (WHERE r.status = 'rejected') AS rejected
		FROM (
			SELECT reviewed_by, status FROM submissions
			 WHERE reviewed_by IS NOT NULL AND status IN ('approved','rejected')
			UNION ALL
			SELECT reviewed_by, status FROM metadata_submissions
			 WHERE reviewed_by IS NOT NULL AND status IN ('approved','rejected')
		) r
		JOIN users u ON u.id = r.reviewed_by
		WHERE NOT u.hidden
		GROUP BY r.reviewed_by, u.username, u.avatar_key
		ORDER BY (COUNT(*) FILTER (WHERE r.status = 'approved') + COUNT(*) FILTER (WHERE r.status = 'rejected')) DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.UserStat{}
	for rows.Next() {
		var s models.UserStat
		if err := rows.Scan(&s.ID, &s.Username, &s.AvatarKey, &s.Approved, &s.Rejected); err != nil {
			return nil, err
		}
		s.Total = s.Approved + s.Rejected
		out = append(out, s)
	}
	return out, rows.Err()
}

// TopSubmitters returns users with the most SAP submissions (any non-trashed
// state), excluding hidden users.
func (r *Repository) TopSubmitters(limit int) ([]models.UserCountStat, error) {
	return r.userSubmissions(limit, `s.status <> 'trashed'`)
}

// TopApproved returns users with the most approved SAP submissions.
func (r *Repository) TopApproved(limit int) ([]models.UserCountStat, error) {
	return r.userSubmissions(limit, `s.status = 'approved'`)
}

func (r *Repository) userSubmissions(limit int, cond string) ([]models.UserCountStat, error) {
	rows, err := r.db.Query(`
		SELECT s.user_id, u.username, u.avatar_key, COUNT(*) AS c
		FROM submissions s JOIN users u ON u.id = s.user_id
		WHERE s.user_id IS NOT NULL AND `+cond+` AND NOT u.hidden
		GROUP BY s.user_id, u.username, u.avatar_key
		ORDER BY c DESC
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
		       s.created_at, u.username
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
		if err := rows.Scan(&s.ID, &s.PackID, &s.Name, &s.Author, &s.Version, &s.CreatedAt, &s.AuthorName); err != nil {
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
