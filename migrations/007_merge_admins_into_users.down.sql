-- Restore the admins table.
CREATE TABLE admins (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Recreate admins from users with the admin role, preserving ids.
INSERT INTO admins (id, email, password_hash, created_at)
SELECT id, email, password_hash, created_at FROM users WHERE role = 'admin'
ON CONFLICT DO NOTHING;

-- Point submissions.reviewed_by back at admins.
ALTER TABLE submissions DROP CONSTRAINT IF EXISTS submissions_reviewed_by_fkey;
ALTER TABLE submissions ADD CONSTRAINT submissions_reviewed_by_fkey
    FOREIGN KEY (reviewed_by) REFERENCES admins(id);

ALTER TABLE users DROP COLUMN IF EXISTS role;