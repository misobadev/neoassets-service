-- One-time repair: hash any plaintext email-verification / password-reset tokens
-- already in the table so the database no longer stores usable bearer tokens.
--
-- newToken() issues 32 random bytes hex-encoded (64 chars); auth.HashToken is the
-- hex-encoded SHA-256 of that string. PostgreSQL 11+ ships sha256(bytea), so no
-- pgcrypto extension is required. This migration runs once at deploy, before the
-- service serves requests, so every token present here is still plaintext.
UPDATE users
SET email_verification_token = encode(sha256(convert_to(email_verification_token, 'UTF8')), 'hex')
WHERE email_verification_token IS NOT NULL;

UPDATE users
SET password_reset_token = encode(sha256(convert_to(password_reset_token, 'UTF8')), 'hex')
WHERE password_reset_token IS NOT NULL;
