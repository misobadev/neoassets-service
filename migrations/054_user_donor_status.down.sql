ALTER TABLE users DROP CONSTRAINT IF EXISTS users_donor_status_check;
ALTER TABLE users DROP COLUMN IF EXISTS donor_status;