CREATE TABLE IF NOT EXISTS submission_files (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    submission_id UUID NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
    object_key    VARCHAR(512) NOT NULL,
    file_name     VARCHAR(255) NOT NULL,
    system_id     VARCHAR(100) NOT NULL DEFAULT '',
    kind          VARCHAR(20) NOT NULL DEFAULT 'background'
                  CHECK (kind IN ('background', 'preview', 'theme', 'logo')),
    size          BIGINT NOT NULL DEFAULT 0,
    mime_type     VARCHAR(100) NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (submission_id, object_key)
);

CREATE INDEX idx_submission_files_submission_id ON submission_files(submission_id);
