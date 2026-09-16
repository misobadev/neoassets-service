-- Donor status runs in parallel to the XP/level progression: it grants extra
-- scraping threads but never XP or rank. none | supporter | monthly_supporter.
ALTER TABLE users ADD COLUMN IF NOT EXISTS donor_status VARCHAR(20) NOT NULL DEFAULT 'none';
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_donor_status_check;
ALTER TABLE users ADD CONSTRAINT users_donor_status_check
    CHECK (donor_status IN ('none', 'supporter', 'monthly_supporter'));