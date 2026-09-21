-- Region catalog. The order is both the display order and the priority used to
-- resolve a game's primary name/release/cover/logo (first region with data wins).
CREATE TABLE IF NOT EXISTS regions (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    sort_order INT  NOT NULL DEFAULT 0
);

INSERT INTO regions (id, name, sort_order) VALUES
    ('world',   'World',    1),
    ('usa',     'USA',      2),
    ('europe',  'Europe',   3),
    ('japan',   'Japan',    4),
    ('spain',   'Spain',    5),
    ('france',  'France',   6),
    ('germany', 'Germany',  7),
    ('italy',   'Italy',    8),
    ('korea',   'Korea',    9),
    ('china',   'China',   10)
ON CONFLICT (id) DO NOTHING;

-- Per-region text: the localized name and release date of a game.
CREATE TABLE IF NOT EXISTS game_regions (
    game_id       UUID NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    region        TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    release_year  INT,
    release_month INT,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (game_id, region)
);
CREATE INDEX IF NOT EXISTS idx_game_regions_game ON game_regions(game_id);

-- Regional media: covers and logos can exist once per region.
ALTER TABLE media ADD COLUMN IF NOT EXISTS region TEXT NOT NULL DEFAULT '';
ALTER TABLE metadata_submission_files ADD COLUMN IF NOT EXISTS region TEXT NOT NULL DEFAULT '';

-- Move the legacy game region into game_regions before dropping the column.
-- Multi-region values collapse to World; the game's current name/release are
-- stored under that region. Guarded so it is a no-op when the column is absent
-- (e.g. a database created by the importer, which no longer has it).
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'games' AND column_name = 'region'
  ) THEN
    INSERT INTO game_regions (game_id, region, name, release_year, release_month)
    SELECT g.id,
           CASE lower(trim(g.region))
             WHEN 'usa' THEN 'USA'
             WHEN 'us' THEN 'USA'
             WHEN 'europe' THEN 'Europe'
             WHEN 'eu' THEN 'Europe'
             WHEN 'japan' THEN 'Japan'
             WHEN 'jp' THEN 'Japan'
             WHEN 'spain' THEN 'Spain'
             WHEN 'es' THEN 'Spain'
             WHEN 'france' THEN 'France'
             WHEN 'fr' THEN 'France'
             WHEN 'germany' THEN 'Germany'
             WHEN 'de' THEN 'Germany'
             WHEN 'italy' THEN 'Italy'
             WHEN 'it' THEN 'Italy'
             WHEN 'korea' THEN 'Korea'
             WHEN 'kr' THEN 'Korea'
             WHEN 'south korea' THEN 'Korea'
             WHEN 'china' THEN 'China'
             WHEN 'cn' THEN 'China'
             WHEN 'world' THEN 'World'
             WHEN 'wor' THEN 'World'
             ELSE 'World'
           END,
           g.name, g.release_year, g.release_month
    FROM games g
    WHERE COALESCE(trim(g.region), '') <> ''
    ON CONFLICT (game_id, region) DO NOTHING;
  END IF;
END $$;

ALTER TABLE games DROP COLUMN IF EXISTS region;
