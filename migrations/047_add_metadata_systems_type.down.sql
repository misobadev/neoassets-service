DROP INDEX IF EXISTS idx_metadata_systems_type;
ALTER TABLE metadata_systems DROP COLUMN IF EXISTS type;
