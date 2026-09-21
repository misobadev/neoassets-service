-- A submission file can reference an existing media being moved to another
-- region. An explicit flag is needed because a new upload's object_key is also
-- rewritten to a canonical key before the media rows are applied.
ALTER TABLE metadata_submission_files ADD COLUMN IF NOT EXISTS is_move BOOLEAN NOT NULL DEFAULT false;
