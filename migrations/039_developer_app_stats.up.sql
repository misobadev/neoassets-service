-- Developer-facing usage statistics and debug mode.
--
-- Each developer app accumulates lifetime counters used by the developer
-- dashboard (API calls, not-found scraps, rate-limited and over-quota calls,
-- debug calls). A per-app debug password enables the debug query parameters
-- (force counters / threads / rate limit) capped at 100 uses per day.
ALTER TABLE developer_apps
    ADD COLUMN IF NOT EXISTS debug_password TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS api_calls BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS ko_scraps BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS rate_limited BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS quota_exceeded BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS debug_calls BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_scrape_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS debug_usage (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    day        DATE NOT NULL,
    app_id     UUID NOT NULL REFERENCES developer_apps(id) ON DELETE CASCADE,
    count      INT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (day, app_id)
);

CREATE INDEX IF NOT EXISTS idx_debug_usage_app ON debug_usage(app_id);
