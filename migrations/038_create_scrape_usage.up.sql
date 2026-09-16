-- Daily scrape quota counter. One row per (day, subject). Registered users are
-- counted by their user id; guests (developer app without a user key) are
-- counted by the app's client_id. The day is the UTC date and the counter is
-- reset implicitly by rolling to the next day.
CREATE TABLE IF NOT EXISTS scrape_usage (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    day          DATE NOT NULL,
    subject_type TEXT NOT NULL CHECK (subject_type IN ('user', 'guest')),
    subject_id   TEXT NOT NULL,
    count        INT NOT NULL DEFAULT 0,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (day, subject_type, subject_id)
);

CREATE INDEX IF NOT EXISTS idx_scrape_usage_subject ON scrape_usage(subject_type, subject_id);
