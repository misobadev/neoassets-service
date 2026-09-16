-- Track who submitted a media asset. NULL means it came from the importer
-- (the system), not a community submission.
ALTER TABLE media ADD COLUMN IF NOT EXISTS submitted_by UUID REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_media_submitted_by ON media(submitted_by);