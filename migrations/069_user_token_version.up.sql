-- Token version lets a password change (or reset) invalidate every previously
-- issued JWT for the user without a server-side session store.
ALTER TABLE users ADD COLUMN IF NOT EXISTS token_version integer NOT NULL DEFAULT 0;
