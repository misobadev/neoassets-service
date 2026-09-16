-- Translation catalog for game and system descriptions.
-- Idempotent: the sibling data importer may already have
-- created these tables via its ensureSchema, so this must tolerate that.
CREATE TABLE IF NOT EXISTS lang (
    code        TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    native_name TEXT NOT NULL DEFAULT '',
    enabled     BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS game_translations (
    game_id       UUID NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    lang_code     TEXT NOT NULL REFERENCES lang(code) ON DELETE CASCADE,
    description   TEXT NOT NULL DEFAULT '',
    translated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (game_id, lang_code)
);

CREATE TABLE IF NOT EXISTS system_translations (
    system_id     TEXT NOT NULL REFERENCES metadata_systems(id) ON DELETE CASCADE,
    lang_code     TEXT NOT NULL REFERENCES lang(code) ON DELETE CASCADE,
    description   TEXT NOT NULL DEFAULT '',
    translated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (system_id, lang_code)
);

CREATE INDEX IF NOT EXISTS idx_game_translations_lang ON game_translations(lang_code);
CREATE INDEX IF NOT EXISTS idx_system_translations_lang ON system_translations(lang_code);