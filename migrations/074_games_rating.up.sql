-- games.rating is read by the catalog queries but was only ever created by the
-- local importer (neoassets-data). Create it here so a fresh database migrated
-- by the service alone has the full schema. A no-op where it already exists.
ALTER TABLE games ADD COLUMN IF NOT EXISTS rating INTEGER NOT NULL DEFAULT 0;
