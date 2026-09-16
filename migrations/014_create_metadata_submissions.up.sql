CREATE TABLE IF NOT EXISTS metadata_submissions (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id        UUID REFERENCES games(id) ON DELETE CASCADE,
    system_id      TEXT REFERENCES metadata_systems(id) ON DELETE CASCADE,
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status         TEXT NOT NULL DEFAULT 'created'
                   CHECK (status IN ('created','pending','approved','rejected')),
    payload        JSONB NOT NULL DEFAULT '{}'::jsonb,
    review_comment TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reviewed_at    TIMESTAMPTZ,
    reviewed_by    UUID REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_metadata_submissions_status ON metadata_submissions(status);
CREATE INDEX IF NOT EXISTS idx_metadata_submissions_game ON metadata_submissions(game_id);
