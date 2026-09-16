package repository

import (
	"fmt"

	"github.com/google/uuid"

	"neoassets/internal/models"
)

// Follow records that follower now follows followee.
func (r *Repository) Follow(followerID, followeeID uuid.UUID) error {
	if _, err := r.db.Exec(
		`INSERT INTO follows (follower_id, followee_id) VALUES ($1, $2)
		 ON CONFLICT (follower_id, followee_id) DO NOTHING`,
		followerID, followeeID,
	); err != nil {
		return fmt.Errorf("follow: %w", err)
	}
	return nil
}

// Unfollow removes a follow relationship.
func (r *Repository) Unfollow(followerID, followeeID uuid.UUID) error {
	_, err := r.db.Exec(
		`DELETE FROM follows WHERE follower_id = $1 AND followee_id = $2`, followerID, followeeID,
	)
	return err
}

// IsFollowing reports whether follower follows followee.
func (r *Repository) IsFollowing(followerID, followeeID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM follows WHERE follower_id = $1 AND followee_id = $2)`,
		followerID, followeeID,
	).Scan(&exists)
	return exists, err
}

// FollowCounts returns the follower and following counts for a user.
func (r *Repository) FollowCounts(id uuid.UUID) (followers, following int, err error) {
	if err = r.db.QueryRow(`SELECT count(*) FROM follows WHERE followee_id = $1`, id).Scan(&followers); err != nil {
		return
	}
	err = r.db.QueryRow(`SELECT count(*) FROM follows WHERE follower_id = $1`, id).Scan(&following)
	return
}

// PublicProfileByUsername returns the public profile row for a username, or
// sql.ErrNoRows. Hidden users (NeoBot) are not public.
func (r *Repository) PublicProfileByUsername(username string) (*models.User, error) {
	row := r.db.QueryRow(
		`SELECT `+userColumns+` FROM users WHERE LOWER(username) = LOWER($1) AND NOT hidden`, username,
	)
	return scanUser(row)
}

// UserStats aggregates a user's contribution counts.
// UserStats returns the number of approved contributions and the total number
// of contributions (SAP packs + metadata), counting both kinds.
func (r *Repository) UserStats(id uuid.UUID) (approved, submitted int, err error) {
	if err = r.db.QueryRow(
		`SELECT
		   (SELECT COUNT(*) FROM submissions WHERE user_id = $1 AND status = 'approved')
		   + (SELECT COUNT(*) FROM metadata_submissions WHERE user_id = $1 AND status = 'approved'),
		   (SELECT COUNT(*) FROM submissions WHERE user_id = $1 AND status <> 'trashed')
		   + (SELECT COUNT(*) FROM metadata_submissions WHERE user_id = $1 AND status <> 'created')`,
		id,
	).Scan(&approved, &submitted); err != nil {
		return
	}
	return
}

// FollowersByUsername returns a user's follower usernames (newest first).
func (r *Repository) FollowersByUsername(username string) ([]string, error) {
	return r.followList(`SELECT u.username FROM follows f
		JOIN users u ON u.id = f.follower_id
		JOIN users t ON t.id = f.followee_id
		WHERE LOWER(t.username) = LOWER($1) AND NOT u.hidden
		ORDER BY f.created_at DESC`, username)
}

// FollowingByUsername returns the usernames a user follows (newest first).
func (r *Repository) FollowingByUsername(username string) ([]string, error) {
	return r.followList(`SELECT u.username FROM follows f
		JOIN users u ON u.id = f.followee_id
		JOIN users t ON t.id = f.follower_id
		WHERE LOWER(t.username) = LOWER($1) AND NOT u.hidden
		ORDER BY f.created_at DESC`, username)
}

func (r *Repository) followList(q, username string) ([]string, error) {
	rows, err := r.db.Query(q, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// RecentUserSubmissions returns the latest submissions (both SAP and metadata)
// for a user, newest first, up to limit.
func (r *Repository) RecentUserSubmissions(userID uuid.UUID, limit int) ([]models.UserSubmissionItem, error) {
	rows, err := r.db.Query(`
		SELECT * FROM (
			SELECT 'sap' AS kind, s.id AS id, s.name AS title, s.status,
			       NULL::uuid AS game_id, NULL::text AS system_id, s.created_at
			FROM submissions s
			WHERE s.user_id = $1 AND s.status <> 'trashed'
			UNION ALL
			SELECT 'metadata' AS kind, m.id AS id, COALESCE(g.name, ms.name, ''),
			       m.status, m.game_id, COALESCE(g.system_id, ms.id)::text AS system_id, m.created_at
			FROM metadata_submissions m
			LEFT JOIN games g ON g.id = m.game_id
			LEFT JOIN metadata_systems ms ON ms.id = m.system_id
			WHERE m.user_id = $1
		) u
		ORDER BY created_at DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.UserSubmissionItem
	for rows.Next() {
		var it models.UserSubmissionItem
		if err := rows.Scan(&it.Kind, &it.ID, &it.Title, &it.Status, &it.GameID, &it.SystemID, &it.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
