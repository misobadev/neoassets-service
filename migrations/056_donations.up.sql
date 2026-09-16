-- Donation-driven donor status. donation_events is an append-only, idempotent
-- log of Ko-fi/Patreon payments; donor_claims lets a user prove ownership of
-- the email a donation was made with when it does not match their account.
-- donor_source records whether the current status was set automatically by a
-- donation or manually by an admin (admin overrides are never auto-clobbered).
ALTER TABLE users ADD COLUMN IF NOT EXISTS donor_source VARCHAR(10) NOT NULL DEFAULT 'none';
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_donor_source_check;
ALTER TABLE users ADD CONSTRAINT users_donor_source_check
    CHECK (donor_source IN ('none', 'auto', 'admin'));

CREATE TABLE IF NOT EXISTS donation_events (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    platform     TEXT NOT NULL CHECK (platform IN ('kofi', 'patreon')),
    external_id  TEXT NOT NULL,
    email        TEXT NOT NULL,
    from_name    TEXT,
    amount_cents INTEGER,
    currency     TEXT,
    kind         TEXT NOT NULL CHECK (kind IN ('one_time', 'subscription')),
    tier_name    TEXT,
    user_id      UUID REFERENCES users(id) ON DELETE SET NULL,
    occurred_at  TIMESTAMPTZ,
    raw          JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (platform, external_id)
);

CREATE INDEX IF NOT EXISTS idx_donation_events_email ON donation_events (LOWER(email));
CREATE INDEX IF NOT EXISTS idx_donation_events_user ON donation_events (user_id);

CREATE TABLE IF NOT EXISTS donor_claims (
    user_id    UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    email      TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
