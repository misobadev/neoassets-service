CREATE TABLE IF NOT EXISTS games (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    system_id   TEXT NOT NULL REFERENCES metadata_systems(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    region      TEXT NOT NULL DEFAULT '',
    release_year INT,
    publisher   TEXT NOT NULL DEFAULT '',
    developer   TEXT NOT NULL DEFAULT '',
    genre       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (system_id, name)
);
CREATE INDEX IF NOT EXISTS idx_games_system ON games(system_id);
CREATE INDEX IF NOT EXISTS idx_games_name ON games(name);
CREATE INDEX IF NOT EXISTS idx_games_name_lower ON games(lower(name));
