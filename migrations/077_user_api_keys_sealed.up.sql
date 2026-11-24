-- Encrypted copy of each personal API key (AES-GCM, server-side key), so the
-- owner can copy it again later. Keys created before this column stay NULL and
-- can only be replaced.
ALTER TABLE user_api_keys ADD COLUMN IF NOT EXISTS key_sealed TEXT;
