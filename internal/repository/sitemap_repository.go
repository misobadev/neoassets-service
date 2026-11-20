package repository

import (
	"context"
	"fmt"
	"time"
)

// SitemapGame is a game page for the sitemap.
type SitemapGame struct {
	ID        string
	SystemID  string
	UpdatedAt time.Time
}

// SitemapPack is an approved art pack page for the sitemap.
type SitemapPack struct {
	PackID    string
	UpdatedAt time.Time
}

// SitemapGames returns one page of games ordered by id, so pages are stable.
func (r *Repository) SitemapGames(ctx context.Context, limit, offset int) ([]SitemapGame, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id::text, system_id, updated_at FROM games ORDER BY id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("sitemap games: %w", err)
	}
	defer rows.Close()
	var out []SitemapGame
	for rows.Next() {
		var g SitemapGame
		if err := rows.Scan(&g.ID, &g.SystemID, &g.UpdatedAt); err != nil {
			return nil, fmt.Errorf("sitemap games: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SitemapSystems returns the ids of the systems that have at least one game.
func (r *Repository) SitemapSystems(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT s.id FROM metadata_systems s WHERE EXISTS (SELECT 1 FROM games g WHERE g.system_id = s.id) ORDER BY s.id`)
	if err != nil {
		return nil, fmt.Errorf("sitemap systems: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sitemap systems: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SitemapPacks returns every approved pack with its latest approval time.
func (r *Repository) SitemapPacks(ctx context.Context) ([]SitemapPack, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT pack_id, max(COALESCE(reviewed_at, created_at)) FROM submissions
		 WHERE status = 'approved' GROUP BY pack_id ORDER BY pack_id`)
	if err != nil {
		return nil, fmt.Errorf("sitemap packs: %w", err)
	}
	defer rows.Close()
	var out []SitemapPack
	for rows.Next() {
		var p SitemapPack
		if err := rows.Scan(&p.PackID, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("sitemap packs: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
