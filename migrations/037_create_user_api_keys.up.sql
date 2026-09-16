-- Personal API keys used as the user credential of the scraping API. Only the
-- SHA-256 hash and a short prefix are stored; the plaintext key is shown once at
-- creation. Keys are revocable and belong to a single user.
CREATE TABLE IF NOT EXISTS user_api_keys (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    prefix       TEXT NOT NULL,
    key_hash     TEXT NOT NULL,
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_api_keys_user ON user_api_keys(user_id);
CREATE INDEX IF NOT EXISTS idx_user_api_keys_prefix ON user_api_keys(prefix);
