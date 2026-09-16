-- Optional profile avatar: the R2 object key of the user's image (empty when
-- none). Avatars are uploaded to profile/{userID}/{uuid}.webp|.gif.
ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_key VARCHAR(512) NOT NULL DEFAULT '';