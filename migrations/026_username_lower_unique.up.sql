-- Usernames are required and unique at the DB level (username UNIQUE NOT NULL).
-- This index enforces uniqueness case-insensitively so "Misoba" and "misoba" are
-- treated as the same identifier.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username_lower ON users (LOWER(username));
