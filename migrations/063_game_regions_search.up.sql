-- Regional game names are searchable (SearchGames also matches game_regions),
-- so index their normalized form with the same trigram expression used for the
-- games name (f_unaccent is defined in migration 059).
CREATE INDEX IF NOT EXISTS idx_game_regions_name_norm_trgm
    ON game_regions USING gin (regexp_replace(f_unaccent(lower(name)), '[^a-z0-9]+', ' ', 'g') gin_trgm_ops);
