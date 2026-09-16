-- Developer applications allowed to consume the public scraping API. Each app
-- carries a public client_id and a secret whose hash is stored (the plaintext
-- secret is shown only once at creation/rotation). Apps are owned by a user and
-- can be revoked at any time.
CREATE TABLE IF NOT EXISTS developer_apps (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name               TEXT NOT NULL,
    description        TEXT NOT NULL DEFAULT '',
    homepage_url       TEXT NOT NULL DEFAULT '',
    client_id          TEXT NOT NULL UNIQUE,
    client_secret_hash TEXT NOT NULL,
    last_used_at       TIMESTAMPTZ,
    revoked_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_developer_apps_user ON developer_apps(user_id);
CREATE INDEX IF NOT EXISTS idx_developer_apps_client_id ON developer_apps(client_id);
