-- Marks a submission as a contribution to an existing approved pack (as
-- opposed to a brand-new pack). Contributions share the pack_id of the pack
-- they add images/description to.
ALTER TABLE submissions ADD COLUMN IF NOT EXISTS contribution BOOLEAN NOT NULL DEFAULT FALSE;