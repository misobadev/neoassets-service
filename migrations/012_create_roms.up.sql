CREATE TABLE IF NOT EXISTS roms (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id    UUID NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    size       BIGINT NOT NULL DEFAULT 0,
    crc        TEXT NOT NULL DEFAULT '',
    md5        TEXT NOT NULL DEFAULT '',
    sha1       TEXT NOT NULL DEFAULT '',
    sha256     TEXT NOT NULL DEFAULT '',
    region     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (game_id, sha1)
);
CREATE INDEX IF NOT EXISTS idx_roms_crc ON roms(crc);
CREATE INDEX IF NOT EXISTS idx_roms_md5 ON roms(md5);
CREATE INDEX IF NOT EXISTS idx_roms_sha1 ON roms(sha1);
CREATE INDEX IF NOT EXISTS idx_roms_sha256 ON roms(sha256);
