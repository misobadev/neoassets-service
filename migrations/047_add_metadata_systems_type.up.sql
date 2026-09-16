-- Catalog kind of a system (console, handheld, computer, arcade, virtual).
-- Used to group systems independently of the scraping family, so the web can
-- show an "Arcade" group and keep consoles/computers/handhelds apart.
ALTER TABLE metadata_systems ADD COLUMN IF NOT EXISTS type TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_metadata_systems_type ON metadata_systems(type);
