DROP INDEX IF EXISTS idx_games_system_type_name;
DROP INDEX IF EXISTS idx_games_short_name_norm_trgm;
DROP INDEX IF EXISTS idx_games_name_norm_trgm;
DROP FUNCTION IF EXISTS f_unaccent(text);
DROP EXTENSION IF EXISTS pg_trgm;
