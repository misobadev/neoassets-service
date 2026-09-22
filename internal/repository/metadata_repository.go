package repository

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"neoassets/internal/models"
)

// ---------------------------------------------------------------------------
// Metadata: browse
// ---------------------------------------------------------------------------

// Completion is measured per game as the fraction of core text fields present
// plus the fraction of media kinds present, averaged over every game in the
// system. The combined percentage weights text and media equally.
const (
	completionTextFields = 6 // description, genre, developer, publisher, release_year, rating
	completionMediaKinds = 5 // cover, screenshot, fanart, video, logo
)

// ListMetadataSystems returns all metadata systems ordered by name, each with
// catalog stats: total games, counts by type (base/hack/homebrew) and the
// completion percentages (combined, text and media) over all its games.
func (r *Repository) ListMetadataSystems() ([]models.MetadataSystem, error) {
	rows, err := r.db.Query(
		`SELECT s.id, s.name, s.short_name, s.region, s.description,
		        s.family, s.system_group, s.virtual, s.created_at, s.updated_at,
		        COUNT(g.id) AS total,
		        COUNT(g.id) FILTER (WHERE g.type = 'base')     AS base,
		        COUNT(g.id) FILTER (WHERE g.type = 'hack')     AS hack,
		        COUNT(g.id) FILTER (WHERE g.type = 'homebrew') AS homebrew,
		        COALESCE(SUM(
		          (g.description <> '')::int + (g.genre <> '')::int + (g.developer <> '')::int +
		          (g.publisher <> '')::int + (g.release_year IS NOT NULL)::int + (g.rating > 0)::int
		        ), 0) AS text_points,
		        COALESCE(SUM(
		          EXISTS(SELECT 1 FROM media m WHERE m.game_id = g.id AND m.kind = 'cover')::int +
		          EXISTS(SELECT 1 FROM media m WHERE m.game_id = g.id AND m.kind = 'screenshot')::int +
		          EXISTS(SELECT 1 FROM media m WHERE m.game_id = g.id AND m.kind = 'fanart')::int +
		          EXISTS(SELECT 1 FROM media m WHERE m.game_id = g.id AND m.kind = 'video')::int +
		          EXISTS(SELECT 1 FROM media m WHERE m.game_id = g.id AND m.kind = 'logo')::int
		        ), 0) AS media_points
		 FROM metadata_systems s
		 LEFT JOIN games g ON g.system_id = s.id
		 GROUP BY s.id
		 ORDER BY s.name`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list metadata systems: %w", err)
	}
	defer rows.Close()

	var list []models.MetadataSystem
	for rows.Next() {
		var s models.MetadataSystem
		var textPoints, mediaPoints int
		if err := rows.Scan(&s.ID, &s.Name, &s.ShortName, &s.Region, &s.Description,
			&s.Family, &s.Group, &s.Virtual, &s.CreatedAt, &s.UpdatedAt,
			&s.TotalGames, &s.Base, &s.Hack, &s.Homebrew, &textPoints, &mediaPoints); err != nil {
			return nil, fmt.Errorf("failed to scan metadata system: %w", err)
		}
		if s.TotalGames > 0 {
			total := float64(s.TotalGames)
			textFrac := float64(textPoints) / (total * completionTextFields)
			mediaFrac := float64(mediaPoints) / (total * completionMediaKinds)
			s.TextPct = textFrac * 100
			s.MediaPct = mediaFrac * 100
			s.MetadataPct = (textFrac + mediaFrac) / 2 * 100
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// GetMetadataSystem returns a single metadata system by id.
func (r *Repository) GetMetadataSystem(id string) (*models.MetadataSystem, error) {
	var s models.MetadataSystem
	err := r.db.QueryRow(
		`SELECT id, name, short_name, region, description, family, system_group,
		        virtual, created_at, updated_at
		 FROM metadata_systems WHERE id = $1`, id,
	).Scan(&s.ID, &s.Name, &s.ShortName, &s.Region, &s.Description,
		&s.Family, &s.Group, &s.Virtual, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListSystemIDsByFamily returns the ids of every system in a family, ordered by
// name. It is used by the public scraping API to resolve a family to its
// systems.
func (r *Repository) ListSystemIDsByFamily(family string) ([]string, error) {
	rows, err := r.db.Query(
		`SELECT id FROM metadata_systems WHERE family = $1 AND NOT virtual ORDER BY name`, family,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list family systems: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan family system: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListSystemIDsByGroup returns the ids of every system in a group (e.g.
// "mame-fbneo"), ordered by name.
func (r *Repository) ListSystemIDsByGroup(group string) ([]string, error) {
	rows, err := r.db.Query(
		`SELECT id FROM metadata_systems WHERE system_group = $1 AND NOT virtual ORDER BY name`, group,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list group systems: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan group system: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListFamilies returns every non-empty family with its system and game counts.
func (r *Repository) ListFamilies() ([]models.MetadataFamily, error) {
	rows, err := r.db.Query(
		`SELECT s.family, COUNT(DISTINCT s.id) AS systems, COUNT(g.id) AS total_games
		 FROM metadata_systems s
		 LEFT JOIN games g ON g.system_id = s.id
		 WHERE s.family <> '' AND NOT s.virtual
		 GROUP BY s.family
		 ORDER BY s.family`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list families: %w", err)
	}
	defer rows.Close()
	var list []models.MetadataFamily
	for rows.Next() {
		var f models.MetadataFamily
		if err := rows.Scan(&f.Family, &f.Systems, &f.TotalGames); err != nil {
			return nil, fmt.Errorf("failed to scan family: %w", err)
		}
		list = append(list, f)
	}
	return list, rows.Err()
}

// ListGroups returns every non-empty group with its family and counts.
func (r *Repository) ListGroups() ([]models.MetadataGroup, error) {
	rows, err := r.db.Query(
		`SELECT s.system_group, MIN(s.family) AS family,
		        COUNT(DISTINCT s.id) AS systems, COUNT(g.id) AS total_games
		 FROM metadata_systems s
		 LEFT JOIN games g ON g.system_id = s.id
		 WHERE s.system_group <> '' AND NOT s.virtual
		 GROUP BY s.system_group
		 ORDER BY s.system_group`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list groups: %w", err)
	}
	defer rows.Close()
	var list []models.MetadataGroup
	for rows.Next() {
		var g models.MetadataGroup
		if err := rows.Scan(&g.Group, &g.Family, &g.Systems, &g.TotalGames); err != nil {
			return nil, fmt.Errorf("failed to scan group: %w", err)
		}
		list = append(list, g)
	}
	return list, rows.Err()
}

// primaryCover selects the cover with the highest region priority (the regions
// catalog order); region-less covers sort last.
const primaryCover = `COALESCE((SELECT m.object_key FROM media m LEFT JOIN regions r ON r.name = m.region
      WHERE m.game_id = g.id AND m.kind = 'cover' ORDER BY r.sort_order NULLS LAST, m.created_at LIMIT 1), '')`
const primaryCoverUpdated = `COALESCE((SELECT m.created_at::text FROM media m LEFT JOIN regions r ON r.name = m.region
      WHERE m.game_id = g.id AND m.kind = 'cover' ORDER BY r.sort_order NULLS LAST, m.created_at LIMIT 1), '')`

const gameCols = `g.id, g.system_id, g.name, g.description, g.release_year,
      g.release_month, g.publisher, g.developer, g.genre, g.rating,
      g.type, g.created_at, g.updated_at, COALESCE(s.name, ''),
      ` + primaryCover + `,
      ` + primaryCoverUpdated + `,
      COALESCE((SELECT gs.scrapes FROM game_scrape_stats gs WHERE gs.game_id = g.id), 0) AS scrapes`

// gameColsLang is gameCols with the description replaced by the translation
// lookup: the requested language's description when present, English otherwise.
const gameColsLang = `g.id, g.system_id, g.name, COALESCE(gt.description, g.description), g.release_year,
      g.release_month, g.publisher, g.developer, g.genre, g.rating,
      g.type, g.created_at, g.updated_at, COALESCE(s.name, ''),
      ` + primaryCover + `,
      ` + primaryCoverUpdated + `,
      COALESCE((SELECT gs.scrapes FROM game_scrape_stats gs WHERE gs.game_id = g.id), 0) AS scrapes`

func scanGame(row *sql.Row) (*models.Game, error) {
	var g models.Game
	err := row.Scan(
		&g.ID, &g.SystemID, &g.Name, &g.Description, &g.ReleaseYear,
		&g.ReleaseMonth, &g.Publisher, &g.Developer, &g.Genre, &g.Rating,
		&g.Type, &g.CreatedAt, &g.UpdatedAt, &g.SystemName,
		&g.Cover, &g.CoverUpdated, &g.Scrapes,
	)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func scanGameRows(rows *sql.Rows) ([]models.Game, error) {
	var list []models.Game
	for rows.Next() {
		var g models.Game
		if err := rows.Scan(
			&g.ID, &g.SystemID, &g.Name, &g.Description, &g.ReleaseYear,
			&g.ReleaseMonth, &g.Publisher, &g.Developer, &g.Genre, &g.Rating,
			&g.Type, &g.CreatedAt, &g.UpdatedAt, &g.SystemName,
			&g.Cover, &g.CoverUpdated, &g.Scrapes,
		); err != nil {
			return nil, fmt.Errorf("failed to scan game: %w", err)
		}
		list = append(list, g)
	}
	return list, rows.Err()
}

// GetGame returns a game by id with its system name. When lang is non-empty
// (and not "en") the description is resolved to that language's translation,
// falling back to the English text when no translation exists.
func (r *Repository) GetGame(id uuid.UUID, lang string) (*models.Game, error) {
	cols := gameCols
	query := `SELECT ` + gameCols + ` FROM games g
		 LEFT JOIN metadata_systems s ON s.id = g.system_id
		 WHERE g.id = $1`
	args := []interface{}{id}
	if lang != "" && lang != "en" {
		cols = gameColsLang
		query = `SELECT ` + cols + ` FROM games g
		 LEFT JOIN metadata_systems s ON s.id = g.system_id
		 LEFT JOIN game_translations gt ON gt.game_id = g.id AND gt.lang_code = $2
		 WHERE g.id = $1`
		args = append(args, lang)
	}
	row := r.db.QueryRow(query, args...)
	g, err := scanGame(row)
	if err != nil {
		return nil, err
	}
	return g, nil
}

// ListLanguages returns all enabled languages from the lang table.
func (r *Repository) ListLanguages() ([]models.Language, error) {
	rows, err := r.db.Query(
		`SELECT code, name, native_name FROM lang WHERE enabled = true ORDER BY code`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []models.Language
	for rows.Next() {
		var l models.Language
		if err := rows.Scan(&l.Code, &l.Name, &l.NativeName); err != nil {
			return nil, err
		}
		list = append(list, l)
	}
	return list, rows.Err()
}

// ListGameTranslations returns the languages that have a translation for a game.
func (r *Repository) ListGameTranslations(gameID uuid.UUID) ([]models.Language, error) {
	rows, err := r.db.Query(
		`SELECT l.code, l.name, l.native_name
		 FROM game_translations gt
		 JOIN lang l ON l.code = gt.lang_code
		 WHERE gt.game_id = $1
		 ORDER BY l.code`, gameID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []models.Language
	for rows.Next() {
		var l models.Language
		if err := rows.Scan(&l.Code, &l.Name, &l.NativeName); err != nil {
			return nil, err
		}
		list = append(list, l)
	}
	return list, rows.Err()
}

// GameDescriptionsByLang returns the translated descriptions for a game keyed by
// language code.
func (r *Repository) GameDescriptionsByLang(gameID uuid.UUID) (map[string]string, error) {
	rows, err := r.db.Query(
		`SELECT lang_code, description FROM game_translations WHERE game_id = $1`, gameID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var code, desc string
		if err := rows.Scan(&code, &desc); err != nil {
			return nil, err
		}
		out[code] = desc
	}
	return out, rows.Err()
}

// UpsertGameTranslations writes translations for a game, replacing any existing
// row for the same (game_id, lang_code).
func (r *Repository) UpsertGameTranslations(gameID uuid.UUID, translations map[string]string) error {
	return r.upsertTranslations(
		`INSERT INTO game_translations (game_id, lang_code, description)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (game_id, lang_code) DO UPDATE SET
		   description = EXCLUDED.description,
		   translated_at = NOW()`,
		translations, func(lang string) (any, string) {
			return gameID, lang
		},
	)
}

// UpsertSystemTranslations writes translations for a system, replacing any
// existing row for the same (system_id, lang_code).
func (r *Repository) UpsertSystemTranslations(systemID string, translations map[string]string) error {
	return r.upsertTranslations(
		`INSERT INTO system_translations (system_id, lang_code, description)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (system_id, lang_code) DO UPDATE SET
		   description = EXCLUDED.description,
		   translated_at = NOW()`,
		translations, func(lang string) (any, string) {
			return systemID, lang
		},
	)
}

// upsertTranslations runs an INSERT ... ON CONFLICT for each translation,
// binding the entity id first and the lang code second.
func (r *Repository) upsertTranslations(query string, translations map[string]string, id func(lang string) (any, string)) error {
	if len(translations) == 0 {
		return nil
	}
	for lang, text := range translations {
		entityID, code := id(lang)
		if _, err := r.db.Exec(query, entityID, code, text); err != nil {
			return fmt.Errorf("upsert translation %s: %w", code, err)
		}
	}
	return nil
}

// GameStats are the computed metadata status flags for a game.
type GameStats struct {
	TextComplete    bool
	HasTranslations bool
	HasScreenshot   bool
	HasFanart       bool
	HasVideo        bool
	HasLogo         bool
}

// ListGameStats returns the metadata status flags for a batch of games, keyed
// by game id. It inspects the text fields, the presence of translations, and
// which media kinds the game has (screenshot/fanart/video/logo).
func (r *Repository) ListGameStats(ids []uuid.UUID) (map[uuid.UUID]GameStats, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]GameStats{}, nil
	}
	// Build a single query over all ids using the IN clause.
	args := make([]interface{}, 0, len(ids))
	ph := make([]string, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
		ph = append(ph, fmt.Sprintf("$%d", len(args)))
	}
	in := strings.Join(ph, ",")

	rows, err := r.db.Query(
		`SELECT g.id,
		        (g.description <> '' AND g.genre <> '' AND g.developer <> '' AND g.publisher <> ''
		         AND g.release_year IS NOT NULL AND g.rating > 0) AS text_complete,
		        EXISTS(SELECT 1 FROM game_translations gt WHERE gt.game_id = g.id) AS has_translations,
		        EXISTS(SELECT 1 FROM media m WHERE m.game_id = g.id AND m.kind = 'screenshot') AS has_screenshot,
		        EXISTS(SELECT 1 FROM media m WHERE m.game_id = g.id AND m.kind = 'fanart') AS has_fanart,
		        EXISTS(SELECT 1 FROM media m WHERE m.game_id = g.id AND m.kind = 'video') AS has_video,
		        EXISTS(SELECT 1 FROM media m WHERE m.game_id = g.id AND m.kind = 'logo') AS has_logo
		 FROM games g WHERE g.id IN (`+in+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[uuid.UUID]GameStats, len(ids))
	for rows.Next() {
		var id uuid.UUID
		var st GameStats
		if err := rows.Scan(&id, &st.TextComplete, &st.HasTranslations, &st.HasScreenshot, &st.HasFanart, &st.HasVideo, &st.HasLogo); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// IncrementGameScrape bumps a game's scrape counter and returns the new value.
func (r *Repository) IncrementGameScrape(gameID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.QueryRow(
		`INSERT INTO game_scrape_stats (game_id, scrapes, last_scraped_at)
		 VALUES ($1, 1, NOW())
		 ON CONFLICT (game_id) DO UPDATE SET
		   scrapes = game_scrape_stats.scrapes + 1,
		   last_scraped_at = NOW()
		 RETURNING scrapes`, gameID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to increment game scrape: %w", err)
	}
	return count, nil
}

// ListPopularGames returns the most scraped games, optionally scoped to a
// system.
func (r *Repository) ListPopularGames(systemID string, limit int) ([]models.PopularGame, error) {
	query := `SELECT g.id, g.system_id, g.name, s.scrapes, s.last_scraped_at
	          FROM game_scrape_stats s
	          JOIN games g ON g.id = s.game_id
	          WHERE s.scrapes > 0`
	args := []interface{}{}
	if systemID != "" {
		args = append(args, systemID)
		query += fmt.Sprintf(" AND g.system_id = $%d", len(args))
	}
	args = append(args, limit)
	query += fmt.Sprintf(" ORDER BY s.scrapes DESC, g.name ASC LIMIT $%d", len(args))

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list popular games: %w", err)
	}
	defer rows.Close()

	var list []models.PopularGame
	for rows.Next() {
		var p models.PopularGame
		if err := rows.Scan(&p.ID, &p.SystemID, &p.Name, &p.Scrapes, &p.LastScrapedAt); err != nil {
			return nil, fmt.Errorf("failed to scan popular game: %w", err)
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

// ListPopularSystems returns the systems with the most scrapes, summing the
// scrape counters of all their games.
func (r *Repository) ListPopularSystems(limit int) ([]models.PopularSystem, error) {
	rows, err := r.db.Query(`
		SELECT g.system_id, COALESCE(s.name, g.system_id), SUM(st.scrapes) AS scrapes
		FROM game_scrape_stats st
		JOIN games g ON g.id = st.game_id
		LEFT JOIN metadata_systems s ON s.id = g.system_id
		GROUP BY g.system_id, s.name
		ORDER BY scrapes DESC, COALESCE(s.name, g.system_id) ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list popular systems: %w", err)
	}
	defer rows.Close()
	out := []models.PopularSystem{}
	for rows.Next() {
		var p models.PopularSystem
		if err := rows.Scan(&p.SystemID, &p.Name, &p.Scrapes); err != nil {
			return nil, fmt.Errorf("failed to scan popular system: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// orderByClause maps a sort key to a safe ORDER BY clause.
func orderByClause(sort string) string {
	switch sort {
	case "scrapes":
		return "scrapes DESC, g.name ASC"
	case "name_desc":
		return "g.name DESC"
	case "rating_asc":
		return "g.rating ASC, g.name ASC"
	case "rating_desc":
		return "g.rating DESC, g.name ASC"
	case "year_asc":
		return "g.release_year ASC NULLS LAST, g.name ASC"
	case "year_desc":
		return "g.release_year DESC NULLS LAST, g.name ASC"
	default:
		return "g.name ASC"
	}
}

// ListGamesBySystem returns games for a system, newest first, with paging.
// gtype filters by game type (base/hack/homebrew); sort orders the result.
func (r *Repository) ListGamesBySystem(systemID string, limit, offset int, gtype, sort string) ([]models.Game, int64, error) {
	where := []string{"g.system_id = $1"}
	args := []interface{}{systemID}
	if gtype != "" {
		args = append(args, gtype)
		where = append(where, fmt.Sprintf("g.type = $%d", len(args)))
	}
	cond := strings.Join(where, " AND ")
	var total int64
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM games g WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := r.db.Query(
		`SELECT `+gameCols+` FROM games g
		 LEFT JOIN metadata_systems s ON s.id = g.system_id
		 WHERE `+cond+` ORDER BY `+orderByClause(sort)+` LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list, err := scanGameRows(rows)
	return list, total, err
}

// SearchGames searches games by case-insensitive name with an optional system
// filter and game type (base/hack/homebrew).
func (r *Repository) SearchGames(q, systemID, gtype, sort string, limit, offset int) ([]models.Game, int64, error) {
	where := []string{"TRUE"}
	args := []interface{}{}
	// Accent- and punctuation-insensitive search: break the query into tokens
	// and require each token to appear in the normalized game name. This makes
	// "pokemon firered" match "Pokémon - FireRed Version" (accents + hyphens).
	for _, tok := range searchTokens(q) {
		args = append(args, "%"+tok+"%")
		n := len(args)
		// Match the display name, the short name (the MAME/FBNeo ROM set, e.g.
		// "sfa3") or any regional name (game_regions), so searching a localized
		// title like "Street Fighter Zero 3" finds "Street Fighter Alpha 3".
		// f_unaccent is the immutable wrapper indexed by migration 059.
		where = append(where, fmt.Sprintf(
			"(regexp_replace(f_unaccent(lower(g.name)), '[^a-z0-9]+', ' ', 'g') LIKE $%d OR regexp_replace(f_unaccent(lower(g.short_name)), '[^a-z0-9]+', ' ', 'g') LIKE $%d OR EXISTS (SELECT 1 FROM game_regions gr WHERE gr.game_id = g.id AND regexp_replace(f_unaccent(lower(gr.name)), '[^a-z0-9]+', ' ', 'g') LIKE $%d))",
			n, n, n))
	}
	if systemID != "" {
		args = append(args, systemID)
		where = append(where, fmt.Sprintf("g.system_id = $%d", len(args)))
	}
	if gtype != "" {
		args = append(args, gtype)
		where = append(where, fmt.Sprintf("g.type = $%d", len(args)))
	}
	cond := strings.Join(where, " AND ")

	argsCount := append(args, limit, offset)
	var total int64
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM games g WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	argsCount = append(args, limit, offset)
	rows, err := r.db.Query(
		`SELECT `+gameCols+` FROM games g
		 LEFT JOIN metadata_systems s ON s.id = g.system_id
		 WHERE `+cond+` ORDER BY `+orderByClause(sort)+` LIMIT $`+itoa(len(args)+1)+` OFFSET $`+itoa(len(args)+2),
		argsCount...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list, err := scanGameRows(rows)
	return list, total, err
}

// LookupGameByHash finds games matching any of the provided ROM hashes. When
// systemID is non-empty the search is scoped to that system.
func (r *Repository) LookupGameByHash(crc, md5, sha1, sha256, systemID string) ([]models.Game, error) {
	conds := []string{}
	args := []interface{}{}
	if crc != "" {
		args = append(args, strings.ToLower(crc))
		conds = append(conds, fmt.Sprintf("r.crc = $%d", len(args)))
	}
	if md5 != "" {
		args = append(args, strings.ToLower(md5))
		conds = append(conds, fmt.Sprintf("r.md5 = $%d", len(args)))
	}
	if sha1 != "" {
		args = append(args, strings.ToLower(sha1))
		conds = append(conds, fmt.Sprintf("r.sha1 = $%d", len(args)))
	}
	if sha256 != "" {
		args = append(args, strings.ToLower(sha256))
		conds = append(conds, fmt.Sprintf("r.sha256 = $%d", len(args)))
	}
	if len(conds) == 0 {
		return nil, fmt.Errorf("no hash provided")
	}
	where := `g.id IN (
	            SELECT DISTINCT r.game_id FROM roms r
	            WHERE ` + strings.Join(conds, " OR ") + `
	          )`
	if systemID != "" {
		args = append(args, systemID)
		where += fmt.Sprintf(" AND g.system_id = $%d", len(args))
	}
	query := `SELECT ` + gameCols + ` FROM games g
	          LEFT JOIN metadata_systems s ON s.id = g.system_id
	          WHERE ` + where + `
	          ORDER BY g.name`
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup game by hash: %w", err)
	}
	defer rows.Close()
	return scanGameRows(rows)
}

// ListRomsByGame returns the ROM dumps for a game.
func (r *Repository) ListRomsByGame(gameID uuid.UUID) ([]models.Rom, error) {
	rows, err := r.db.Query(
		`SELECT id, game_id, name, size, crc, md5, sha1, sha256, region, created_at
		 FROM roms WHERE game_id = $1 ORDER BY name`, gameID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []models.Rom
	for rows.Next() {
		var x models.Rom
		if err := rows.Scan(&x.ID, &x.GameID, &x.Name, &x.Size, &x.CRC, &x.MD5, &x.SHA1, &x.SHA256, &x.Region, &x.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, x)
	}
	return list, rows.Err()
}

// ListGenres returns the canonical genre catalog ordered for display.
func (r *Repository) ListGenres() ([]models.Genre, error) {
	rows, err := r.db.Query(`SELECT id, name, sort_order FROM genres ORDER BY sort_order, name`)
	if err != nil {
		return nil, fmt.Errorf("failed to list genres: %w", err)
	}
	defer rows.Close()
	out := []models.Genre{}
	for rows.Next() {
		var g models.Genre
		if err := rows.Scan(&g.ID, &g.Name, &g.SortOrder); err != nil {
			return nil, fmt.Errorf("failed to scan genre: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GenreExists reports whether a genre name is in the canonical catalog.
func (r *Repository) GenreExists(name string) (bool, error) {
	var n int
	err := r.db.QueryRow(`SELECT 1 FROM genres WHERE name = $1`, name).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check genre: %w", err)
	}
	return true, nil
}

// ListRegions returns the canonical region catalog in priority/display order.
func (r *Repository) ListRegions() ([]models.Region, error) {
	rows, err := r.db.Query(`SELECT id, name, sort_order FROM regions ORDER BY sort_order, name`)
	if err != nil {
		return nil, fmt.Errorf("failed to list regions: %w", err)
	}
	defer rows.Close()
	out := []models.Region{}
	for rows.Next() {
		var g models.Region
		if err := rows.Scan(&g.ID, &g.Name, &g.SortOrder); err != nil {
			return nil, fmt.Errorf("failed to scan region: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RegionExists reports whether a region name is in the canonical catalog.
func (r *Repository) RegionExists(name string) (bool, error) {
	var n int
	err := r.db.QueryRow(`SELECT 1 FROM regions WHERE name = $1`, name).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check region: %w", err)
	}
	return true, nil
}

// ListGameRegions returns a game's per-region text, ordered by the region
// priority. A region that only carries regional cover/logo media (no name or
// release row) is included too, so its assets are not dropped from the detail.
// Media is attached by the caller.
func (r *Repository) ListGameRegions(gameID uuid.UUID) ([]models.GameRegion, error) {
	rows, err := r.db.Query(`
		SELECT x.region, x.name, x.release_year, x.release_month
		FROM (
			SELECT gr.region, gr.name, gr.release_year, gr.release_month
			FROM game_regions gr
			WHERE gr.game_id = $1
			UNION ALL
			SELECT DISTINCT m.region, '', NULL::int, NULL::int
			FROM media m
			WHERE m.game_id = $1
			  AND m.region <> ''
			  AND m.kind IN ('cover', 'logo')
			  AND NOT EXISTS (SELECT 1 FROM game_regions gr2 WHERE gr2.game_id = $1 AND gr2.region = m.region)
		) x
		LEFT JOIN regions r ON r.name = x.region
		ORDER BY r.sort_order NULLS LAST, x.region`, gameID)
	if err != nil {
		return nil, fmt.Errorf("failed to list game regions: %w", err)
	}
	defer rows.Close()
	out := []models.GameRegion{}
	for rows.Next() {
		var g models.GameRegion
		if err := rows.Scan(&g.Region, &g.Name, &g.ReleaseYear, &g.ReleaseMonth); err != nil {
			return nil, fmt.Errorf("failed to scan game region: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// PrimaryGameRegion returns the game's primary region: the first per-region row
// with text, in catalog priority order. Empty when the game has no regional text.
func (r *Repository) PrimaryGameRegion(gameID uuid.UUID) (string, error) {
	regions, err := r.ListGameRegions(gameID)
	if err != nil {
		return "", err
	}
	for _, gr := range regions {
		if gr.Name != "" || gr.ReleaseYear != nil {
			return gr.Region, nil
		}
	}
	return "", nil
}

// UpsertGameRegion stores a game's name and/or release for a region, only
// overwriting the fields provided (non-empty name, non-zero year).
func (r *Repository) UpsertGameRegion(gameID uuid.UUID, region, name string, year, month *int) error {
	_, err := r.db.Exec(`
		INSERT INTO game_regions (game_id, region, name, release_year, release_month, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (game_id, region) DO UPDATE SET
		  name = CASE WHEN EXCLUDED.name <> '' THEN EXCLUDED.name ELSE game_regions.name END,
		  release_year = COALESCE(EXCLUDED.release_year, game_regions.release_year),
		  release_month = COALESCE(EXCLUDED.release_month, game_regions.release_month),
		  updated_at = NOW()`,
		gameID, region, name, year, month)
	if err != nil {
		return fmt.Errorf("failed to upsert game region: %w", err)
	}
	return nil
}

// refreshGamePrimary recomputes a game's canonical name and release from its
// per-region rows, using the region priority (catalog order). Fields without a
// regional value keep their current column value.
func (r *Repository) refreshGamePrimary(gameID uuid.UUID) error {
	regions, err := r.ListGameRegions(gameID)
	if err != nil {
		return err
	}
	var name string
	var year, month *int
	for _, gr := range regions {
		if name == "" && gr.Name != "" {
			name = gr.Name
		}
		if year == nil && gr.ReleaseYear != nil {
			year = gr.ReleaseYear
			month = gr.ReleaseMonth
		}
	}
	if name == "" && year == nil {
		return nil
	}
	_, err = r.db.Exec(`UPDATE games SET
		name = COALESCE(NULLIF($2, ''), name),
		release_year = COALESCE($3, release_year),
		release_month = COALESCE($4, release_month),
		updated_at = NOW() WHERE id = $1`, gameID, name, year, month)
	if err != nil {
		return fmt.Errorf("failed to refresh game primary: %w", err)
	}
	return nil
}

// MediaExists reports whether a game already has a media row of the given kind
// and object key (used to validate a region move).
func (r *Repository) MediaExists(gameID uuid.UUID, kind, objectKey string) (bool, error) {
	var n int
	err := r.db.QueryRow(`SELECT 1 FROM media WHERE game_id = $1 AND kind = $2 AND object_key = $3`, gameID, kind, objectKey).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to check media: %w", err)
	}
	return true, nil
}

// ClearGameRegion clears the name and/or release of a region (used when a value
// is moved to another region). The row is dropped when it becomes empty.
func (r *Repository) ClearGameRegion(gameID uuid.UUID, region string, clearName, clearRelease bool) error {
	sets := []string{}
	if clearName {
		sets = append(sets, "name = ''")
	}
	if clearRelease {
		sets = append(sets, "release_year = NULL", "release_month = NULL")
	}
	if len(sets) == 0 {
		return nil
	}
	if _, err := r.db.Exec(
		`UPDATE game_regions SET `+strings.Join(sets, ", ")+`, updated_at = NOW() WHERE game_id = $1 AND region = $2`,
		gameID, region,
	); err != nil {
		return fmt.Errorf("failed to clear game region: %w", err)
	}
	if _, err := r.db.Exec(
		`DELETE FROM game_regions WHERE game_id = $1 AND region = $2 AND name = '' AND release_year IS NULL`,
		gameID, region,
	); err != nil {
		return fmt.Errorf("failed to drop empty game region: %w", err)
	}
	return nil
}

// ListMediaByGame returns media attached to a game.
// mediaRegionOrder sorts regional media (cover/logo) by the region priority
// (catalog order) and leaves region-less media last, in upload order.
const mediaRegionOrder = `ORDER BY r.sort_order NULLS LAST, m.created_at`

func (r *Repository) ListMediaByGame(gameID uuid.UUID) ([]models.Media, error) {
	return r.listMedia(`WHERE m.game_id = $1 `+mediaRegionOrder, gameID)
}

// ListMediaBySystem returns media attached to a system.
func (r *Repository) ListMediaBySystem(systemID string) ([]models.Media, error) {
	return r.listMedia(`WHERE m.system_id = $1 `+mediaRegionOrder, systemID)
}

// ListGameContributors returns the users who have approved metadata
// contributions for a game, ordered by contribution count (descending). Hidden
// accounts (e.g. the importer bot) are excluded.
func (r *Repository) ListGameContributors(gameID uuid.UUID) ([]models.UserCountStat, error) {
	rows, err := r.db.Query(`
		SELECT u.id, u.username, u.avatar_key, COUNT(*) AS c
		FROM metadata_submissions m
		JOIN users u ON u.id = m.user_id
		WHERE m.game_id = $1 AND m.status = $2 AND NOT u.hidden
		GROUP BY u.id, u.username, u.avatar_key
		ORDER BY c DESC, u.username ASC`, gameID, models.MetadataApproved)
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

func (r *Repository) listMedia(where string, arg interface{}) ([]models.Media, error) {
	rows, err := r.db.Query(
		`SELECT m.id, m.game_id, m.system_id, m.kind, m.object_key, m.mime, m.size, m.region, m.created_at,
		        m.submitted_by, COALESCE(u.username, '')
		 FROM media m
		 LEFT JOIN users u ON u.id = m.submitted_by
		 LEFT JOIN regions r ON r.name = m.region `+where, arg,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []models.Media
	for rows.Next() {
		var m models.Media
		var submittedBy uuid.NullUUID
		if err := rows.Scan(&m.ID, &m.GameID, &m.SystemID, &m.Kind, &m.ObjectKey, &m.Mime, &m.Size, &m.Region, &m.CreatedAt,
			&submittedBy, &m.SubmittedByName); err != nil {
			return nil, err
		}
		if submittedBy.Valid {
			id := submittedBy.UUID
			m.SubmittedBy = &id
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

// ---------------------------------------------------------------------------
// Metadata: contributions + review
// ---------------------------------------------------------------------------

// CreateMetadataSubmission inserts a pending contribution. kind is "edit" for a
// change to an existing game/system, or "new_game" for a brand-new game.
func (r *Repository) CreateMetadataSubmission(gameID *uuid.UUID, systemID *string, userID uuid.UUID, kind string, payload []byte) (uuid.UUID, error) {
	if kind == "" {
		kind = "edit"
	}
	var id uuid.UUID
	err := r.db.QueryRow(
		`INSERT INTO metadata_submissions (game_id, system_id, user_id, status, kind, payload)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		gameID, systemID, userID, models.MetadataPending, kind, payload,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to create metadata submission: %w", err)
	}
	return id, nil
}

// SetMetadataSubmissionOldState stores the target's pre-approval snapshot (old
// text and old media) on the submission.
func (r *Repository) SetMetadataSubmissionOldState(id uuid.UUID, oldPayload, oldMedia []byte) error {
	if _, err := r.db.Exec(
		`UPDATE metadata_submissions SET old_payload = $1, old_media = $2 WHERE id = $3`,
		oldPayload, oldMedia, id,
	); err != nil {
		return fmt.Errorf("failed to store submission old state: %w", err)
	}
	return nil
}

// SetMetadataSubmissionGame points a submission at the game it created (used
// when approving a "new_game" contribution) and clears the system target.
func (r *Repository) SetMetadataSubmissionGame(id, gameID uuid.UUID) error {
	if _, err := r.db.Exec(
		`UPDATE metadata_submissions SET game_id = $1, system_id = NULL WHERE id = $2`, gameID, id,
	); err != nil {
		return fmt.Errorf("failed to set submission game: %w", err)
	}
	return nil
}

// CreateGameFromPayload inserts a brand-new game for a system from an approved
// contribution payload and returns its id. The games table has a unique
// (system_id, name) constraint, so a duplicate name returns an error.
// regionText is one per-region name/release entry of a new-game payload.
type regionText struct {
	Region       string
	Name         string
	ReleaseYear  *int
	ReleaseMonth *int
}

// parseRegionText reads the per-region name/release list of a new-game payload.
// Entries without a region are ignored.
func parseRegionText(payload map[string]any) []regionText {
	raw, ok := payload["regions"].([]any)
	if !ok {
		return nil
	}
	out := make([]regionText, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rt := regionText{}
		if s, ok := m["region"].(string); ok {
			rt.Region = strings.TrimSpace(s)
		}
		if rt.Region == "" {
			continue
		}
		if s, ok := m["name"].(string); ok {
			rt.Name = strings.TrimSpace(s)
		}
		if n, ok := m["release_year"].(float64); ok && int(n) > 0 {
			y := int(n)
			rt.ReleaseYear = &y
			if mm, ok := m["release_month"].(float64); ok && int(mm) >= 1 && int(mm) <= 12 {
				mv := int(mm)
				rt.ReleaseMonth = &mv
			}
		}
		out = append(out, rt)
	}
	return out
}

func (r *Repository) CreateGameFromPayload(systemID string, payload map[string]any) (uuid.UUID, error) {
	str := func(k string) string {
		if v, ok := payload[k].(string); ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	intVal := func(k string) (int, bool) {
		switch n := payload[k].(type) {
		case float64:
			return int(n), true
		case int:
			return n, true
		}
		return 0, false
	}

	regions := parseRegionText(payload)
	name := str("name")
	if name == "" {
		for _, rg := range regions {
			if rg.Name != "" {
				name = rg.Name
				break
			}
		}
	}
	if name == "" {
		return uuid.Nil, fmt.Errorf("game name is required")
	}
	gtype := str("type")
	switch gtype {
	case "base", "hack", "homebrew":
	default:
		gtype = "base"
	}
	var year, month *int
	if y, ok := intVal("release_year"); ok && y > 0 {
		year = &y
		if m, ok := intVal("release_month"); ok && m >= 1 && m <= 12 {
			month = &m
		}
	}
	rating, _ := intVal("rating")

	var id uuid.UUID
	err := r.db.QueryRow(
		`INSERT INTO games (system_id, name, description, release_year, release_month,
		     publisher, developer, genre, rating, type)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id`,
		systemID, name, str("description"), year, month,
		str("publisher"), str("developer"), str("genre"), rating, gtype,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to create game: %w", err)
	}
	// The game's region is stored per-region alongside its name/release. A new
	// game may submit several regions at once; otherwise a single region applies.
	if len(regions) > 0 {
		for _, rg := range regions {
			if err := r.UpsertGameRegion(id, rg.Region, rg.Name, rg.ReleaseYear, rg.ReleaseMonth); err != nil {
				return uuid.Nil, err
			}
		}
		if err := r.refreshGamePrimary(id); err != nil {
			return uuid.Nil, err
		}
	} else if region := str("region"); region != "" {
		if err := r.UpsertGameRegion(id, region, name, year, month); err != nil {
			return uuid.Nil, err
		}
	}
	return id, nil
}

// GameExistsByName reports whether a system already has a game with the given
// name (case-insensitive).
func (r *Repository) GameExistsByName(systemID, name string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM games WHERE system_id = $1 AND lower(name) = lower($2))`,
		systemID, strings.TrimSpace(name),
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check game name: %w", err)
	}
	return exists, nil
}

const msCols = `id, game_id, system_id, user_id, status, kind, payload, old_payload, old_media, review_comment, created_at, reviewed_at, reviewed_by`

func scanMS(row *sql.Row) (*models.MetadataSubmission, error) {
	var m models.MetadataSubmission
	err := row.Scan(&m.ID, &m.GameID, &m.SystemID, &m.UserID, &m.Status, &m.Kind, &m.Payload, &m.OldPayload, &m.OldMedia, &m.ReviewComment, &m.CreatedAt, &m.ReviewedAt, &m.ReviewedBy)
	if err != nil {
		return nil, err
	}
	if m.Payload == nil {
		m.Payload = json.RawMessage("{}")
	}
	if m.OldPayload == nil {
		m.OldPayload = json.RawMessage("{}")
	}
	if m.OldMedia == nil {
		m.OldMedia = json.RawMessage("[]")
	}
	return &m, nil
}

// GetMetadataSubmission returns a submission with its files.
func (r *Repository) GetMetadataSubmission(id uuid.UUID) (*models.MetadataSubmission, error) {
	return scanMS(r.db.QueryRow(`SELECT `+msCols+` FROM metadata_submissions WHERE id = $1`, id))
}

// GetMetadataSubmissionForUser returns a submission owned by the user.
func (r *Repository) GetMetadataSubmissionForUser(id, userID uuid.UUID) (*models.MetadataSubmission, error) {
	return scanMS(r.db.QueryRow(`SELECT `+msCols+` FROM metadata_submissions WHERE id = $1 AND user_id = $2`, id, userID))
}

// ListMetadataSubmissionsByUser lists a user's contributions, newest first.
// status filters by review state: "review" returns only pending/approved/rejected,
// an empty status returns everything (including drafts). When limit > 0 the
// result is paged and total is the unpaged count.
func (r *Repository) ListMetadataSubmissionsByUser(userID uuid.UUID, status string, limit, offset int) ([]models.MetadataSubmission, int64, error) {
	where := []string{"user_id = $1"}
	args := []interface{}{userID}
	switch status {
	case "review":
		where = append(where, "status IN ('pending','approved','rejected')")
	case "":
	default:
		where = append(where, "status = $2")
		args = append(args, status)
	}
	cond := strings.Join(where, " AND ")

	var total int64
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM metadata_submissions WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT ` + msCols + ` FROM metadata_submissions WHERE ` + cond + ` ORDER BY created_at DESC`
	if limit > 0 {
		args = append(args, limit, offset)
		query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	}
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	list, err := scanMSRows(rows)
	return list, total, err
}

// ListMetadataSubmissions lists contributions by an optional status filter.
func (r *Repository) ListMetadataSubmissions(status string) ([]models.MetadataSubmission, error) {
	query := `SELECT ` + msCols + ` FROM metadata_submissions`
	args := []interface{}{}
	if status != "" {
		query += ` WHERE status = $1`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMSRows(rows)
}

// ListMetadataReviewSubmissions lists every contribution that has entered the
// review workflow, newest first. Client-side drafts are never persisted, but
// this also excludes any stray created row so admins never see a draft.
func (r *Repository) ListMetadataReviewSubmissions() ([]models.MetadataSubmission, error) {
	rows, err := r.db.Query(
		`SELECT `+msCols+` FROM metadata_submissions WHERE status <> $1 ORDER BY created_at DESC`,
		models.MetadataCreated,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMSRows(rows)
}

func scanMSRows(rows *sql.Rows) ([]models.MetadataSubmission, error) {
	var list []models.MetadataSubmission
	for rows.Next() {
		var m models.MetadataSubmission
		if err := rows.Scan(&m.ID, &m.GameID, &m.SystemID, &m.UserID, &m.Status, &m.Kind, &m.Payload, &m.OldPayload, &m.OldMedia, &m.ReviewComment, &m.CreatedAt, &m.ReviewedAt, &m.ReviewedBy); err != nil {
			return nil, err
		}
		if m.Payload == nil {
			m.Payload = json.RawMessage("{}")
		}
		if m.OldPayload == nil {
			m.OldPayload = json.RawMessage("{}")
		}
		if m.OldMedia == nil {
			m.OldMedia = json.RawMessage("[]")
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

// SubmitMetadataSubmission moves a draft into pending for review.
func (r *Repository) SubmitMetadataSubmission(id, userID uuid.UUID) (*models.MetadataSubmission, error) {
	return scanMS(r.db.QueryRow(
		`UPDATE metadata_submissions SET status = $1
		 WHERE id = $2 AND user_id = $3 AND status = $4
		 RETURNING `+msCols,
		models.MetadataPending, id, userID, models.MetadataCreated,
	))
}

// SetMetadataStatusForAdmin sets a submission status on behalf of a reviewer.
func (r *Repository) SetMetadataStatusForAdmin(id uuid.UUID, status string, adminID uuid.UUID, comment string) (*models.MetadataSubmission, error) {
	return scanMS(r.db.QueryRow(
		`UPDATE metadata_submissions
		 SET status = $1, reviewed_by = $2, reviewed_at = NOW(), review_comment = $3
		 WHERE id = $4
		 RETURNING `+msCols,
		status, adminID, comment, id,
	))
}

// ListMetadataSubmissionFiles returns files for a submission.
func (r *Repository) ListMetadataSubmissionFiles(id uuid.UUID) ([]models.MetadataSubmissionFile, error) {
	rows, err := r.db.Query(
		`SELECT id, submission_id, kind, object_key, file_name, mime, size, region, is_delete, is_move, created_at,
		        video_format, video_codec, width, height, fps, duration_sec
		 FROM metadata_submission_files WHERE submission_id = $1 ORDER BY created_at`, id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []models.MetadataSubmissionFile
	for rows.Next() {
		var f models.MetadataSubmissionFile
		var dur sql.NullFloat64
		if err := rows.Scan(&f.ID, &f.SubmissionID, &f.Kind, &f.ObjectKey, &f.FileName, &f.Mime, &f.Size, &f.Region, &f.IsDelete, &f.IsMove, &f.CreatedAt,
			&f.VideoFormat, &f.VideoCodec, &f.Width, &f.Height, &f.FPS, &dur); err != nil {
			return nil, err
		}
		f.DurationSec = dur.Float64
		list = append(list, f)
	}
	return list, rows.Err()
}

// ListGamesByIDs returns games (with their system name and cover) for the given
// ids in a single query, used to enrich the admin review list.
func (r *Repository) ListGamesByIDs(ids []uuid.UUID) ([]models.Game, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := r.db.Query(
		`SELECT `+gameCols+` FROM games g
		 LEFT JOIN metadata_systems s ON s.id = g.system_id
		 WHERE g.id = ANY($1)`, pq.Array(ids),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list games: %w", err)
	}
	defer rows.Close()
	return scanGameRows(rows)
}

// SystemNamesByIDs returns a system id -> name map for the given ids.
func (r *Repository) SystemNamesByIDs(ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(`SELECT id, name FROM metadata_systems WHERE id = ANY($1)`, pq.Array(ids))
	if err != nil {
		return nil, fmt.Errorf("failed to list system names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// MetadataSubmissionFileKindsByIDs returns the distinct media kinds uploaded per
// submission id.
func (r *Repository) MetadataSubmissionFileKindsByIDs(ids []uuid.UUID) (map[uuid.UUID][]string, error) {
	out := map[uuid.UUID][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(
		`SELECT DISTINCT submission_id, kind FROM metadata_submission_files
		 WHERE submission_id = ANY($1) ORDER BY submission_id, kind`, pq.Array(ids),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list submission file kinds: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var kind string
		if err := rows.Scan(&id, &kind); err != nil {
			return nil, err
		}
		out[id] = append(out[id], kind)
	}
	return out, rows.Err()
}

// AddMetadataSubmissionFile records an uploaded media file for a submission.
// For video submissions, format/codec/resolution/fps/duration are captured at
// upload time so reviewers can inspect the source file without downloading it.
func (r *Repository) AddMetadataSubmissionFile(id uuid.UUID, kind, objectKey, fileName, mime, region string, size int64, isDelete, isMove bool, video *models.VideoMeta) error {
	if _, err := r.db.Exec(`DELETE FROM metadata_submission_files WHERE submission_id = $1 AND object_key = $2`, id, objectKey); err != nil {
		return fmt.Errorf("failed to replace metadata submission file: %w", err)
	}
	if video == nil {
		_, err := r.db.Exec(
			`INSERT INTO metadata_submission_files (submission_id, kind, object_key, file_name, mime, region, size, is_delete, is_move)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			id, kind, objectKey, fileName, mime, region, size, isDelete, isMove,
		)
		if err != nil {
			return fmt.Errorf("failed to add metadata submission file: %w", err)
		}
		return nil
	}
	_, err := r.db.Exec(
		`INSERT INTO metadata_submission_files (submission_id, kind, object_key, file_name, mime, region, size, is_delete, is_move,
		     video_format, video_codec, width, height, fps, duration_sec)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		id, kind, objectKey, fileName, mime, region, size, isDelete, isMove,
		video.Format, video.Codec, video.Width, video.Height, video.FPS, video.DurationSec,
	)
	if err != nil {
		return fmt.Errorf("failed to add metadata submission file: %w", err)
	}
	return nil
}

// UpdateMetadataSubmissionFileObjectKey rewrites a submission file's object key
// (used when an approved file is moved to its canonical location).
func (r *Repository) UpdateMetadataSubmissionFileObjectKey(fileID uuid.UUID, objectKey string) error {
	_, err := r.db.Exec(`UPDATE metadata_submission_files SET object_key = $1 WHERE id = $2`, objectKey, fileID)
	if err != nil {
		return fmt.Errorf("failed to update metadata submission file key: %w", err)
	}
	return nil
}

// UpdateMetadataSubmissionFileMime updates a submission file's MIME type (used
// when a video is converted to MP4 on approval).
func (r *Repository) UpdateMetadataSubmissionFileMime(fileID uuid.UUID, mime string) error {
	_, err := r.db.Exec(`UPDATE metadata_submission_files SET mime = $1 WHERE id = $2`, mime, fileID)
	if err != nil {
		return fmt.Errorf("failed to update metadata submission file mime: %w", err)
	}
	return nil
}

// UpdateMetadataSubmissionFileRegion rewrites a submission file's region (used
// when a region-less cover/logo is resolved to the game's primary region).
func (r *Repository) UpdateMetadataSubmissionFileRegion(fileID uuid.UUID, region string) error {
	_, err := r.db.Exec(`UPDATE metadata_submission_files SET region = $1 WHERE id = $2`, region, fileID)
	if err != nil {
		return fmt.Errorf("failed to update metadata submission file region: %w", err)
	}
	return nil
}

// ApplyMetadataSubmission copies approved files into media and applies the
// editable payload to the target game or system.
func (r *Repository) ApplyMetadataSubmission(id uuid.UUID) error {
	sub, err := r.GetMetadataSubmission(id)
	if err != nil {
		return err
	}

	// Apply approved files to media, replacing any existing media of the same
	// kind for that game/system (a submission replaces, it does not add).
	if sub.GameID != nil {
		gid := *sub.GameID
		files, err := r.ListMetadataSubmissionFiles(id)
		if err != nil {
			return err
		}
		// is_delete removes the media of that (kind, region); is_move reassigns
		// the region of an existing media. New uploads are inserted below.
		for _, f := range files {
			if f.IsDelete {
				if _, err := r.db.Exec(
					`DELETE FROM media WHERE game_id = $1 AND kind = $2 AND region = $3`,
					gid, f.Kind, f.Region,
				); err != nil {
					return fmt.Errorf("failed to delete media: %w", err)
				}
				continue
			}
			if !f.IsMove {
				continue
			}
			if _, err := r.db.Exec(
				`DELETE FROM media WHERE game_id = $1 AND kind = $2 AND region = $3 AND object_key <> $4`,
				gid, f.Kind, f.Region, f.ObjectKey,
			); err != nil {
				return fmt.Errorf("failed to clear target region media: %w", err)
			}
			if _, err := r.db.Exec(
				`UPDATE media SET region = $1 WHERE game_id = $2 AND object_key = $3`,
				f.Region, gid, f.ObjectKey,
			); err != nil {
				return fmt.Errorf("failed to move media region: %w", err)
			}
		}
		// Replace the media for the (kind, region) of the new uploads.
		if _, err := r.db.Exec(
			`DELETE FROM media m WHERE m.game_id = $1 AND EXISTS (
			   SELECT 1 FROM metadata_submission_files f
			   WHERE f.submission_id = $2 AND f.kind = m.kind AND f.region = m.region
			     AND NOT f.is_delete AND NOT f.is_move AND m.object_key <> f.object_key)`,
			gid, id,
		); err != nil {
			return fmt.Errorf("failed to replace media: %w", err)
		}
		if _, err := r.db.Exec(
			`INSERT INTO media (game_id, kind, object_key, mime, region, size, submitted_by)
			 SELECT $1, kind, object_key, mime, region, size, $3 FROM metadata_submission_files
			 WHERE submission_id = $2 AND NOT is_delete AND NOT is_move`,
			gid, id, sub.UserID,
		); err != nil {
			return fmt.Errorf("failed to apply media files: %w", err)
		}
	} else if sub.SystemID != nil {
		sid := *sub.SystemID
		if _, err := r.db.Exec(
			`DELETE FROM media m WHERE m.system_id = $1 AND EXISTS (
			   SELECT 1 FROM metadata_submission_files f
			   WHERE f.submission_id = $2 AND f.kind = m.kind AND f.region = m.region)`,
			sid, id,
		); err != nil {
			return fmt.Errorf("failed to replace media: %w", err)
		}
		if _, err := r.db.Exec(
			`INSERT INTO media (system_id, kind, object_key, mime, region, size, submitted_by)
			 SELECT $1, kind, object_key, mime, region, size, $3 FROM metadata_submission_files WHERE submission_id = $2`,
			sid, id, sub.UserID,
		); err != nil {
			return fmt.Errorf("failed to apply media files: %w", err)
		}
	}

	var p map[string]any
	if err := json.Unmarshal(sub.Payload, &p); err != nil {
		return fmt.Errorf("invalid submission payload: %w", err)
	}
	str := func(k string) string {
		if v, ok := p[k].(string); ok {
			return v
		}
		return ""
	}
	intVal := func(k string) int {
		if n, ok := p[k].(float64); ok {
			return int(n)
		}
		if n, ok := p[k].(int); ok {
			return n
		}
		return 0
	}

	// Apply each submitted field individually, only when present and non-empty,
	// so a single-field submission never clobbers the other columns.
	if sub.GameID != nil {
		gid := *sub.GameID
		region := str("region")
		regionFrom := str("region_from")
		// A new game may submit several per-region names/releases at once; each
		// row is upserted and the canonical columns are recomputed by priority.
		regions := parseRegionText(p)
		if len(regions) > 0 {
			for _, rg := range regions {
				if err := r.UpsertGameRegion(gid, rg.Region, rg.Name, rg.ReleaseYear, rg.ReleaseMonth); err != nil {
					return err
				}
			}
			if err := r.refreshGamePrimary(gid); err != nil {
				return err
			}
		}
		// A text deletion removes the name or the release of a region.
		if del, ok := p["delete"].(bool); ok && del && region != "" {
			switch str("field") {
			case "name":
				if err := r.ClearGameRegion(gid, region, true, false); err != nil {
					return err
				}
			case "release":
				if err := r.ClearGameRegion(gid, region, false, true); err != nil {
					return err
				}
			}
			if err := r.refreshGamePrimary(gid); err != nil {
				return err
			}
		}
		if v := str("name"); v != "" && len(regions) == 0 {
			// With a region, the name is stored per region and the game's
			// canonical name is recomputed by region priority. region_from moves
			// an existing name from another region.
			if region != "" {
				if regionFrom != "" && regionFrom != region {
					if err := r.ClearGameRegion(gid, regionFrom, true, false); err != nil {
						return err
					}
				}
				if err := r.UpsertGameRegion(gid, region, v, nil, nil); err != nil {
					return err
				}
				if err := r.refreshGamePrimary(gid); err != nil {
					return err
				}
			} else if _, err := r.db.Exec(`UPDATE games SET name=$1, updated_at=NOW() WHERE id=$2`, v, gid); err != nil {
				return fmt.Errorf("failed to apply game name: %w", err)
			}
		}
		if v := str("description"); v != "" {
			if _, err := r.db.Exec(`UPDATE games SET description=$1, updated_at=NOW() WHERE id=$2`, v, gid); err != nil {
				return fmt.Errorf("failed to apply game description: %w", err)
			}
		}
		if v := str("publisher"); v != "" {
			if _, err := r.db.Exec(`UPDATE games SET publisher=$1, updated_at=NOW() WHERE id=$2`, v, gid); err != nil {
				return fmt.Errorf("failed to apply game publisher: %w", err)
			}
		}
		if v := str("developer"); v != "" {
			if _, err := r.db.Exec(`UPDATE games SET developer=$1, updated_at=NOW() WHERE id=$2`, v, gid); err != nil {
				return fmt.Errorf("failed to apply game developer: %w", err)
			}
		}
		if v := str("genre"); v != "" {
			if _, err := r.db.Exec(`UPDATE games SET genre=$1, updated_at=NOW() WHERE id=$2`, v, gid); err != nil {
				return fmt.Errorf("failed to apply game genre: %w", err)
			}
		}
		if y := intVal("release_year"); y > 0 && len(regions) == 0 {
			m := intVal("release_month")
			if m < 1 || m > 12 {
				m = 0
			}
			var mp *int
			if m > 0 {
				mp = &m
			}
			if region != "" {
				if regionFrom != "" && regionFrom != region {
					if err := r.ClearGameRegion(gid, regionFrom, false, true); err != nil {
						return err
					}
				}
				if err := r.UpsertGameRegion(gid, region, "", &y, mp); err != nil {
					return err
				}
				if err := r.refreshGamePrimary(gid); err != nil {
					return err
				}
			} else {
				var month interface{}
				if m > 0 {
					month = m
				}
				if _, err := r.db.Exec(`UPDATE games SET release_year=$1, release_month=$2, updated_at=NOW() WHERE id=$3`, y, month, gid); err != nil {
					return fmt.Errorf("failed to apply game release: %w", err)
				}
			}
		}
		if rating := intVal("rating"); rating >= 1 && rating <= 10 {
			if _, err := r.db.Exec(`UPDATE games SET rating=$1, updated_at=NOW() WHERE id=$2`, rating, gid); err != nil {
				return fmt.Errorf("failed to apply game rating: %w", err)
			}
		}
		if v := str("type"); v == "base" || v == "homebrew" || v == "hack" {
			if _, err := r.db.Exec(`UPDATE games SET type=$1, updated_at=NOW() WHERE id=$2`, v, gid); err != nil {
				return fmt.Errorf("failed to apply game type: %w", err)
			}
		}
	} else if sub.SystemID != nil {
		sid := *sub.SystemID
		if v := str("description"); v != "" {
			if _, err := r.db.Exec(`UPDATE metadata_systems SET description=$1, updated_at=NOW() WHERE id=$2`, v, sid); err != nil {
				return fmt.Errorf("failed to apply system description: %w", err)
			}
		}
		if v := str("region"); v != "" {
			if _, err := r.db.Exec(`UPDATE metadata_systems SET region=$1, updated_at=NOW() WHERE id=$2`, v, sid); err != nil {
				return fmt.Errorf("failed to apply system region: %w", err)
			}
		}
	}
	return nil
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// searchTokens lowercases, de-accents and splits a search query into the words
// that must all appear in the normalized game name.
func searchTokens(q string) []string {
	q = deaccent(strings.ToLower(strings.TrimSpace(q)))
	fields := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		if f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// deaccent strips Latin diacritics so "pokémon" matches "pokemon".
func deaccent(s string) string {
	return strings.NewReplacer(
		"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "ā", "a", "å", "a", "ă", "a", "ą", "a",
		"é", "e", "è", "e", "ê", "e", "ë", "e", "ē", "e", "ė", "e", "ě", "e", "ę", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i", "ī", "i", "į", "i",
		"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o", "ō", "o", "ø", "o", "ő", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u", "ū", "u", "ů", "u", "ű", "u",
		"ç", "c", "č", "c", "ć", "c", "ĉ", "c", "ċ", "c", "ñ", "n", "ń", "n", "ň", "n",
		"ý", "y", "ÿ", "y", "ŷ", "y", "š", "s", "ś", "s", "ş", "s", "ž", "z", "ź", "z", "ż", "z",
		"ł", "l", "ľ", "l", "ĺ", "l", "đ", "d", "ď", "d", "ğ", "g", "ĝ", "g", "æ", "ae", "œ", "oe", "ß", "ss",
		"Á", "A", "À", "A", "Â", "A", "Ä", "A", "Ã", "A", "Å", "A",
		"É", "E", "È", "E", "Ê", "E", "Ë", "E", "Ę", "E",
		"Í", "I", "Ì", "I", "Î", "I", "Ï", "I",
		"Ó", "O", "Ò", "O", "Ô", "O", "Ö", "O", "Õ", "O", "Ø", "O",
		"Ú", "U", "Ù", "U", "Û", "U", "Ü", "U", "Ů", "U", "Ű", "U",
		"Ç", "C", "Č", "C", "Ñ", "N", "Ý", "Y", "Ÿ", "Y", "Ž", "Z", "Š", "S", "Ł", "L", "Đ", "D", "Æ", "AE", "Œ", "OE",
	).Replace(s)
}
