-- Clean up ROM cross-assignments left by a bulk enrichment: the
-- same ROM set was written to many games (e.g. every Super Mario World hack got
-- the base game's ROMs, and multicarts got a whole unrelated set).
--
-- Rule: for a crc assigned to more than one game, keep only the assignment whose
-- ROM name best matches the game name, and drop shared crcs that match no game
-- at all. Single-game assignments are left untouched. The unaccent extension is
-- enabled by migration 020.
WITH scored AS (
    SELECT r.id,
           r.crc,
           (SELECT count(*)
              FROM regexp_split_to_table(
                       regexp_replace(unaccent(lower(r.name)), '[^a-z0-9]+', ' ', 'g'), ' ') AS tok
             WHERE length(tok) >= 3
               AND regexp_replace(unaccent(lower(g.name)), '[^a-z0-9]+', ' ', 'g')
                   LIKE '%' || tok || '%') AS score
      FROM roms r
      JOIN games g ON g.id = r.game_id
),
ranked AS (
    SELECT id,
           count(*)     OVER (PARTITION BY crc) AS shared,
           max(score)   OVER (PARTITION BY crc) AS best,
           row_number() OVER (PARTITION BY crc ORDER BY score DESC, id) AS rn
      FROM scored
)
DELETE FROM roms
 WHERE id IN (
    SELECT id FROM ranked
     WHERE shared > 1 AND (rn > 1 OR best = 0)
 );
