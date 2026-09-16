-- Allow the 'title' media kind (RetroArch Named_Titles screens) by extending the
-- media.kind CHECK constraint. PostgreSQL cannot ALTER a CHECK constraint, so we
-- drop it and re-create it with the additional allowed value.
ALTER TABLE media DROP CONSTRAINT IF EXISTS media_kind_check;
ALTER TABLE media ADD CONSTRAINT media_kind_check CHECK (kind IN ('cover','boxfront','boxback','screenshot','logo','cartridge','manual','other','title'));
