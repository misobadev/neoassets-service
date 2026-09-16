ALTER TABLE users ADD COLUMN IF NOT EXISTS threads INT NOT NULL DEFAULT 4;
ALTER INDEX IF EXISTS idx_xp_ledger_user RENAME TO idx_point_ledger_user;
ALTER TABLE xp_ledger RENAME TO point_ledger;
ALTER TABLE users RENAME COLUMN xp TO points;