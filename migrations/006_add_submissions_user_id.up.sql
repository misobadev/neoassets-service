-- Link submissions to the authenticated user that created them. The column is
-- nullable so pre-existing anonymous submissions keep working.
ALTER TABLE submissions ADD COLUMN user_id UUID REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX idx_submissions_user_id ON submissions(user_id) WHERE user_id IS NOT NULL;