-- An email change no longer switches the address right away: the new address
-- waits here until the account, signed in, confirms it with the token mailed to
-- that address. Until then the account keeps its current, verified address, so
-- a session alone can't move the account and a mistyped address can't take it
-- over. Only the SHA-256 of the mailed token is stored (as for the other account
-- tokens, migration 070).
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS pending_email TEXT,
    ADD COLUMN IF NOT EXISTS pending_email_token TEXT,
    ADD COLUMN IF NOT EXISTS pending_email_expires_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_users_pending_email_token
    ON users (pending_email_token) WHERE pending_email_token IS NOT NULL;

-- Addresses are compared without regard to letter case (sign-up, email change,
-- confirmation), so those lookups get an index of their own.
CREATE INDEX IF NOT EXISTS idx_users_email_lower ON users (LOWER(email));
