-- Allow a pack to have multiple submissions (revisions). A pack is identified
-- by pack_id; each approved submission becomes a new version of that pack
-- (1.0, 1.1, 1.2, ...). The UNIQUE constraint on pack_id is removed so the
-- same pack name can be submitted again as a new version.
ALTER TABLE submissions DROP CONSTRAINT IF EXISTS submissions_pack_id_key;
CREATE INDEX IF NOT EXISTS idx_submissions_pack ON submissions(pack_id);
