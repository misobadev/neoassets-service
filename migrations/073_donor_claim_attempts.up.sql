-- Wrong codes tried against a pending donor claim; ClaimVerify allows a few
-- per code, and a new code resets the count.
ALTER TABLE donor_claims ADD COLUMN IF NOT EXISTS attempts INTEGER NOT NULL DEFAULT 0;
