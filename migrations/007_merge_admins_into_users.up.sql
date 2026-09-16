-- Consolidate admins into a single users table with a role column.
ALTER TABLE users ADD COLUMN role VARCHAR(20) NOT NULL DEFAULT 'user';

-- Backfill existing admins as users with the admin role, preserving their ids
-- so submissions.reviewed_by references stay valid.
INSERT INTO users (id, username, email, password_hash, email_verified, created_at, updated_at, role)
SELECT
    admins.id,
    COALESCE(NULLIF(split_part(admins.email, '@', 1), ''), 'admin'),
    admins.email,
    admins.password_hash,
    TRUE,
    admins.created_at,
    NOW(),
    'admin'
FROM admins
ON CONFLICT DO NOTHING;

-- Point the submissions.reviewed_by foreign key at users instead of admins.
ALTER TABLE submissions DROP CONSTRAINT IF EXISTS submissions_reviewed_by_fkey;
ALTER TABLE submissions ADD CONSTRAINT submissions_reviewed_by_fkey
    FOREIGN KEY (reviewed_by) REFERENCES users(id) ON DELETE SET NULL;

DROP TABLE IF EXISTS admins;

CREATE INDEX idx_users_role ON users(role) WHERE role = 'admin';