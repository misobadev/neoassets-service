-- Android games are looked up by package name, which the catalog stores as
-- the ROM name (GET /api/v1/scrape/games?package=...).
CREATE INDEX IF NOT EXISTS idx_roms_name ON roms (name);
