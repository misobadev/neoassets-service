-- Add the 'created' (draft) status to submissions and make it the default so
-- new submissions start as drafts that the user must submit for review later.
ALTER TABLE submissions DROP CONSTRAINT IF EXISTS submissions_status_check;
ALTER TABLE submissions ADD CONSTRAINT submissions_status_check
    CHECK (status IN ('created', 'pending', 'approved', 'rejected'));
ALTER TABLE submissions ALTER COLUMN status SET DEFAULT 'created';
