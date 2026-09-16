DROP TABLE IF EXISTS debug_usage;
ALTER TABLE developer_apps
    DROP COLUMN IF EXISTS debug_password,
    DROP COLUMN IF EXISTS api_calls,
    DROP COLUMN IF EXISTS ko_scraps,
    DROP COLUMN IF EXISTS rate_limited,
    DROP COLUMN IF EXISTS quota_exceeded,
    DROP COLUMN IF EXISTS debug_calls,
    DROP COLUMN IF EXISTS last_scrape_at;
