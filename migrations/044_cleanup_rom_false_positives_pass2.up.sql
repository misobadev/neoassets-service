-- Second pass for the bulk-enrichment bug. After the shared-crc
-- pass (043), a few games still have an implausibly large ROM list made of
-- unique crcs (so they were not caught as duplicates). For games with more than
-- 50 ROMs, drop every ROM whose name shares no token (>= 3 chars, extension
-- stripped) with the game name.
DELETE FROM roms
 WHERE id IN (
    SELECT r.id
      FROM roms r
      JOIN games g ON g.id = r.game_id
     WHERE (SELECT count(*) FROM roms r2 WHERE r2.game_id = g.id) > 50
       AND (SELECT count(*)
              FROM regexp_split_to_table(
                     regexp_replace(
                       unaccent(lower(regexp_replace(r.name, '\.[A-Za-z0-9]+$', ''))),
                       '[^a-z0-9]+', ' ', 'g'),
                     ' ') AS tok
             WHERE length(tok) >= 3
               AND regexp_replace(unaccent(lower(g.name)), '[^a-z0-9]+', ' ', 'g')
                   LIKE '%' || tok || '%') = 0
 );
