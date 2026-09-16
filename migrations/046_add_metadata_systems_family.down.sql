DROP INDEX IF EXISTS idx_metadata_systems_family;
ALTER TABLE metadata_systems DROP COLUMN IF EXISTS family;
