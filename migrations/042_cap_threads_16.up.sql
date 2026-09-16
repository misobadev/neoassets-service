-- The maximum thread count is now 16 (was 24). Cap any existing rows so the
-- stored value matches the new limit.
UPDATE users SET threads = 16 WHERE threads > 16;
