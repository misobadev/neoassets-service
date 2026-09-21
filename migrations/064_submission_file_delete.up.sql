-- A submission file can also request removing an existing regional media
-- (cover/logo) instead of adding or moving one.
ALTER TABLE metadata_submission_files ADD COLUMN IF NOT EXISTS is_delete BOOLEAN NOT NULL DEFAULT false;
