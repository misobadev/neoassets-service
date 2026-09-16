DROP INDEX IF EXISTS idx_media_submitted_by;
ALTER TABLE media DROP COLUMN IF EXISTS submitted_by;