-- Fast game name search. The search matches tokens with LIKE '%token%' against
-- an accent- and punctuation-normalized name, which a btree index cannot serve.
-- Trigram GIN indexes make those lookups indexable.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- unaccent() is STABLE, so it cannot appear in an index expression. This
-- immutable wrapper lets the normalized expression be indexed; the search query
-- calls the same function so the expression matches the index.
CREATE OR REPLACE FUNCTION f_unaccent(text)
RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
AS $$ SELECT public.unaccent('public.unaccent', $1) $$;

CREATE INDEX IF NOT EXISTS idx_games_name_norm_trgm
    ON games USING gin (regexp_replace(f_unaccent(lower(name)), '[^a-z0-9]+', ' ', 'g') gin_trgm_ops);

CREATE INDEX IF NOT EXISTS idx_games_short_name_norm_trgm
    ON games USING gin (regexp_replace(f_unaccent(lower(short_name)), '[^a-z0-9]+', ' ', 'g') gin_trgm_ops);

-- System listing filtered by game type, ordered by name.
CREATE INDEX IF NOT EXISTS idx_games_system_type_name ON games (system_id, type, name);
