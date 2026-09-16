-- Add the 'trashed' (user-deleted) status. A user can trash a submission as
-- long as it has not been approved yet.
ALTER TABLE submissions DROP CONSTRAINT IF EXISTS submissions_status_check;
ALTER TABLE submissions ADD CONSTRAINT submissions_status_check
    CHECK (status IN ('created', 'pending', 'approved', 'rejected', 'trashed'));
