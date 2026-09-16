-- Short name of a game as used by external media. Arcade systems name
-- their gameplay videos after the MAME/FB Alpha ROM set (e.g. "1941", "ffight"),
-- which differs from the display name. Kept alongside the display name so the
-- importer can match those videos without losing the full title.
ALTER TABLE games ADD COLUMN IF NOT EXISTS short_name TEXT NOT NULL DEFAULT '';
