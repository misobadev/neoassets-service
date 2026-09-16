-- Per-software usage statistics. A client may send a `softname` (query param or
-- X-Software-Name header) to distinguish versions of its software; when absent
-- the developer app's name is used. Quota and rate limiting stay per app/user,
-- the software name is only a statistics dimension.
CREATE TABLE IF NOT EXISTS app_software_stats (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id         UUID NOT NULL REFERENCES developer_apps(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    api_calls      BIGINT NOT NULL DEFAULT 0,
    ko_scraps      BIGINT NOT NULL DEFAULT 0,
    rate_limited   BIGINT NOT NULL DEFAULT 0,
    quota_exceeded BIGINT NOT NULL DEFAULT 0,
    debug_calls    BIGINT NOT NULL DEFAULT 0,
    last_scrape_at TIMESTAMPTZ,
    UNIQUE (app_id, name)
);

CREATE INDEX IF NOT EXISTS idx_app_software_stats_app ON app_software_stats(app_id);
