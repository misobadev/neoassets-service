DROP INDEX IF EXISTS idx_metadata_submissions_kind;
ALTER TABLE metadata_submissions DROP COLUMN IF EXISTS kind;
