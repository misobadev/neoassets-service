-- Drops any email change still waiting for confirmation; the accounts keep their
-- current addresses.
DROP INDEX IF EXISTS idx_users_email_lower;
DROP INDEX IF EXISTS idx_users_pending_email_token;

ALTER TABLE users
    DROP COLUMN IF EXISTS pending_email_expires_at,
    DROP COLUMN IF EXISTS pending_email_token,
    DROP COLUMN IF EXISTS pending_email;
