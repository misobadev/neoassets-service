-- Systems catalog for metadata (seeded from the DAT importer, NOT from the
-- art-pack systems.json which belongs to the NeoAssets art feature).
CREATE TABLE IF NOT EXISTS metadata_systems (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    short_name  TEXT NOT NULL DEFAULT '',
    region      TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_metadata_systems_name ON metadata_systems(name);
