-- Reward system: users earn points for approved contributions and spend them to
-- buy extra scraping threads. Daily scrape limit = threads * 1000 games.
ALTER TABLE users ADD COLUMN IF NOT EXISTS points INT NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS threads INT NOT NULL DEFAULT 4;
UPDATE users SET threads = 24 WHERE role = 'admin';

-- Audit trail of point changes (awards + redemptions).
CREATE TABLE IF NOT EXISTS point_ledger (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    delta INT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    submission_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_point_ledger_user ON point_ledger(user_id);
