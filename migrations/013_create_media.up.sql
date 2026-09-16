CREATE TABLE IF NOT EXISTS media (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id    UUID REFERENCES games(id) ON DELETE CASCADE,
    system_id  TEXT REFERENCES metadata_systems(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL DEFAULT 'cover'
               CHECK (kind IN ('cover','boxfront','boxback','screenshot','logo','cartridge','manual','other')),
    object_key TEXT NOT NULL,
    mime       TEXT NOT NULL DEFAULT '',
    size       BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_media_game ON media(game_id);
CREATE INDEX IF NOT EXISTS idx_media_system ON media(system_id);
