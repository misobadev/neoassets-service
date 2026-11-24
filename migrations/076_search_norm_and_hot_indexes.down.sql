CREATE INDEX IF NOT EXISTS idx_games_name_norm_trgm
    ON games USING gin (regexp_replace(f_unaccent(lower(name)), '[^a-z0-9]+', ' ', 'g') gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_games_short_name_norm_trgm
    ON games USING gin (regexp_replace(f_unaccent(lower(short_name)), '[^a-z0-9]+', ' ', 'g') gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_game_regions_name_norm_trgm
    ON game_regions USING gin (regexp_replace(f_unaccent(lower(name)), '[^a-z0-9]+', ' ', 'g') gin_trgm_ops);

DROP INDEX IF EXISTS idx_games_search_norm_trgm;
DROP TRIGGER IF EXISTS trg_game_regions_search_norm ON game_regions;
DROP TRIGGER IF EXISTS trg_games_search_norm ON games;
DROP FUNCTION IF EXISTS game_regions_search_norm_trg();
DROP FUNCTION IF EXISTS games_search_norm_trg();
ALTER TABLE games DROP COLUMN IF EXISTS search_norm;
DROP FUNCTION IF EXISTS game_search_norm(uuid, text, text);
DROP FUNCTION IF EXISTS f_search_norm(text);

DROP INDEX IF EXISTS idx_metadata_submissions_approved_reviewed;
DROP INDEX IF EXISTS idx_metadata_submissions_approved_recent;
DROP INDEX IF EXISTS idx_metadata_submissions_system;
DROP INDEX IF EXISTS idx_metadata_submissions_user;
DROP INDEX IF EXISTS idx_roms_game;
