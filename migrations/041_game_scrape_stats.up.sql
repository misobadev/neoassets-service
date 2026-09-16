-- Per-game scrape counter. Incremented every time GET /api/v1/scrape/games
-- resolves a game (by hash or name), so the most-scraped games can be ranked.
CREATE TABLE IF NOT EXISTS game_scrape_stats (
    game_id         UUID PRIMARY KEY REFERENCES games(id) ON DELETE CASCADE,
    scrapes         BIGINT NOT NULL DEFAULT 0,
    last_scraped_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_game_scrape_stats_scrapes ON game_scrape_stats(scrapes DESC);
