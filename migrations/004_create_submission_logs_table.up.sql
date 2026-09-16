CREATE TABLE IF NOT EXISTS submission_logs (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    submission_id UUID NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,
    action        VARCHAR(50) NOT NULL,
    user_id       VARCHAR(255) NOT NULL DEFAULT '',
    user_agent    VARCHAR(512) NOT NULL DEFAULT '',
    detail        TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_submission_logs_submission_id ON submission_logs(submission_id);
CREATE INDEX idx_submission_logs_created_at ON submission_logs(created_at);
