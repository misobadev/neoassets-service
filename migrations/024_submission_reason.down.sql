-- Revert the per-file reason column.
ALTER TABLE submission_files DROP COLUMN IF EXISTS reason;
