CREATE TABLE IF NOT EXISTS metadata_submission_files (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    submission_id UUID NOT NULL REFERENCES metadata_submissions(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL DEFAULT 'cover',
    object_key    TEXT NOT NULL,
    file_name     TEXT NOT NULL DEFAULT '',
    mime          TEXT NOT NULL DEFAULT '',
    size          BIGINT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_metadata_submission_files_submission ON metadata_submission_files(submission_id);
