-- Per-pack download counter. Incremented every time the public pack detail is
-- served (installs from front-ends), so packs can be ranked by popularity.
CREATE TABLE IF NOT EXISTS pack_scrape_stats (
    pack_id         VARCHAR(255) PRIMARY KEY,
    scrapes         BIGINT NOT NULL DEFAULT 0,
    last_scraped_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_pack_scrape_stats_scrapes ON pack_scrape_stats(scrapes DESC);