-- Add hidden flag to users. Hidden users (e.g. NeoBot, the origin of imported
-- catalog data) never appear in lists, leaderboards or the admin user list.
ALTER TABLE users ADD COLUMN IF NOT EXISTS hidden BOOLEAN NOT NULL DEFAULT false;
