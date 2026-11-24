-- Indexes for the hottest lookups, and a precomputed search column.

-- Every game detail (and every /scrape/games hit) lists the game's ROMs by
-- game_id. The only index starting with game_id is partial (sha1 <> ''), which
-- the planner cannot use for a plain game_id filter, so the query scanned the
-- whole table.
CREATE INDEX IF NOT EXISTS idx_roms_game ON roms (game_id);

-- A user's submissions (profile, notifications, "my submissions") and the
-- per-system review queue.
CREATE INDEX IF NOT EXISTS idx_metadata_submissions_user
    ON metadata_submissions (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_metadata_submissions_system
    ON metadata_submissions (system_id);
-- Dashboard: recently approved game contributions, and approvals in a window.
CREATE INDEX IF NOT EXISTS idx_metadata_submissions_approved_recent
    ON metadata_submissions (created_at DESC) WHERE status = 'approved' AND game_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_metadata_submissions_approved_reviewed
    ON metadata_submissions (reviewed_at) WHERE status = 'approved';

-- games.search_norm holds the normalized display name, short name (the
-- MAME/FBNeo set) and every regional name, separated by ' | '. A search token
-- is [a-z0-9]+, so it can never match across a separator: "every token in
-- search_norm" is exactly "every token in the name, short name or a regional
-- name", without running regexp_replace/unaccent on each row at query time.
CREATE OR REPLACE FUNCTION f_search_norm(text)
RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$ SELECT regexp_replace(f_unaccent(lower(coalesce($1, ''))), '[^a-z0-9]+', ' ', 'g') $$;

CREATE OR REPLACE FUNCTION game_search_norm(p_id uuid, p_name text, p_short_name text)
RETURNS text LANGUAGE sql STABLE
AS $$
    SELECT concat_ws(' | ', f_search_norm(p_name), f_search_norm(p_short_name),
        (SELECT string_agg(f_search_norm(gr.name), ' | ' ORDER BY gr.region)
           FROM game_regions gr WHERE gr.game_id = p_id AND gr.name <> ''))
$$;

ALTER TABLE games ADD COLUMN IF NOT EXISTS search_norm text NOT NULL DEFAULT '';

CREATE OR REPLACE FUNCTION games_search_norm_trg() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.search_norm := game_search_norm(NEW.id, NEW.name, NEW.short_name);
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS trg_games_search_norm ON games;
CREATE TRIGGER trg_games_search_norm
    BEFORE INSERT OR UPDATE OF name, short_name ON games
    FOR EACH ROW EXECUTE FUNCTION games_search_norm_trg();

CREATE OR REPLACE FUNCTION game_regions_search_norm_trg() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        UPDATE games g SET search_norm = game_search_norm(g.id, g.name, g.short_name)
         WHERE g.id = OLD.game_id;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') AND (TG_OP = 'INSERT' OR NEW.game_id <> OLD.game_id) THEN
        UPDATE games g SET search_norm = game_search_norm(g.id, g.name, g.short_name)
         WHERE g.id = NEW.game_id;
    END IF;
    RETURN NULL;
END $$;

DROP TRIGGER IF EXISTS trg_game_regions_search_norm ON game_regions;
CREATE TRIGGER trg_game_regions_search_norm
    AFTER INSERT OR DELETE OR UPDATE OF game_id, name ON game_regions
    FOR EACH ROW EXECUTE FUNCTION game_regions_search_norm_trg();

UPDATE games g SET search_norm = game_search_norm(g.id, g.name, g.short_name);

-- Global searches (no system) use the trigram index; system-scoped ones filter
-- the system's rows by search_norm, which is a plain LIKE per row.
CREATE INDEX IF NOT EXISTS idx_games_search_norm_trgm ON games USING gin (search_norm gin_trgm_ops);

-- Replaced by idx_games_search_norm_trgm.
DROP INDEX IF EXISTS idx_games_name_norm_trgm;
DROP INDEX IF EXISTS idx_games_short_name_norm_trgm;
DROP INDEX IF EXISTS idx_game_regions_name_norm_trgm;
