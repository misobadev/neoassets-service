-- Unique (submission_id, object_key) so a submission never keeps duplicate files.
CREATE UNIQUE INDEX IF NOT EXISTS idx_metadata_submission_files_unique
    ON metadata_submission_files (submission_id, object_key);
