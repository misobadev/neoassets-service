-- Convert the points economy into an XP-based progression: XP accumulates and
-- unlocks ranks (and their thread counts) automatically, so the purchased
-- threads column is removed.
ALTER TABLE users RENAME COLUMN points TO xp;
ALTER TABLE point_ledger RENAME TO xp_ledger;
ALTER INDEX IF EXISTS idx_point_ledger_user RENAME TO idx_xp_ledger_user;
ALTER TABLE users DROP COLUMN IF EXISTS threads;