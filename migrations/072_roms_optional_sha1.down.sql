DROP INDEX IF EXISTS idx_roms_game_sha1;
ALTER TABLE roms ADD CONSTRAINT roms_game_id_sha1_key UNIQUE (game_id, sha1);
