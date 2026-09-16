CREATE TABLE IF NOT EXISTS submissions (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pack_id        VARCHAR(255) NOT NULL UNIQUE,
    name           VARCHAR(255) NOT NULL,
    author         VARCHAR(255) NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    donation_url   VARCHAR(512) NOT NULL DEFAULT '',
    ai             BOOLEAN NOT NULL DEFAULT FALSE,
    version        VARCHAR(50) NOT NULL DEFAULT '',
    status         VARCHAR(20) NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending', 'approved', 'rejected')),
    anon_user_id   VARCHAR(255) NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reviewed_at    TIMESTAMPTZ,
    reviewed_by    UUID REFERENCES admins(id),
    admin_version  VARCHAR(50) NOT NULL DEFAULT ''
);

CREATE INDEX idx_submissions_status ON submissions(status);
CREATE INDEX idx_submissions_created_at ON submissions(created_at);
