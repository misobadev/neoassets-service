ALTER TABLE submissions DROP CONSTRAINT IF EXISTS submissions_status_check;
ALTER TABLE submissions ADD CONSTRAINT submissions_status_check
    CHECK (status IN ('pending', 'approved', 'rejected'));
ALTER TABLE submissions ALTER COLUMN status SET DEFAULT 'pending';
