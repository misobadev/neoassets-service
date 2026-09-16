DROP TABLE IF EXISTS donor_claims;
DROP TABLE IF EXISTS donation_events;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_donor_source_check;
ALTER TABLE users DROP COLUMN IF EXISTS donor_source;
