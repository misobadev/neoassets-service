-- Snapshot of the target's published state captured when a contribution is
-- approved, so the review detail can still show the "old" text and media after
-- the target has been updated. The replaced media objects are preserved under
-- the history/ prefix in R2.
ALTER TABLE metadata_submissions ADD COLUMN IF NOT EXISTS old_payload JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE metadata_submissions ADD COLUMN IF NOT EXISTS old_media JSONB NOT NULL DEFAULT '[]'::jsonb;
