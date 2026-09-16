-- Revert pack revisions: enforce a single submission per pack_id.
DROP INDEX IF EXISTS idx_submissions_pack;
ALTER TABLE submissions ADD CONSTRAINT submissions_pack_id_key UNIQUE (pack_id);
