-- System family: a coarse group of systems that share a scraping unit. All the
-- arcade boards that run on MAME/FBNeo belong to the "arcade" family, so the
-- public scraping API can resolve the whole family at once instead of forcing
-- the caller to know every board id. Empty means the system belongs to none.
ALTER TABLE metadata_systems ADD COLUMN IF NOT EXISTS family TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_metadata_systems_family ON metadata_systems(family);
