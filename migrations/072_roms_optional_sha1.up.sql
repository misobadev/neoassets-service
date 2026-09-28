-- Arcade ROM dumps are usually ZIP sets and are contributed by name and size
-- only (no hashes). Make the per-game SHA1 uniqueness partial so several
-- hash-less dumps can coexist; hashed dumps are still unique per game.
ALTER TABLE roms DROP CONSTRAINT IF EXISTS roms_game_id_sha1_key;
CREATE UNIQUE INDEX IF NOT EXISTS idx_roms_game_sha1 ON roms(game_id, sha1) WHERE sha1 <> '';
