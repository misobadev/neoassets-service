-- Distinguish a plain metadata edit (existing game/system) from a brand-new
-- game contribution. Existing rows keep the default 'edit'.
ALTER TABLE metadata_submissions ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'edit';
CREATE INDEX IF NOT EXISTS idx_metadata_submissions_kind ON metadata_submissions(kind);
