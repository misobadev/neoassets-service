-- Minimalist media kinds: drop title/manual/cartridge/other (never used).
-- Rows are removed; their R2 objects are cleaned separately by the importer's
-- purge step (or a one-off purge), keeping the media table consistent.
DELETE FROM media WHERE kind IN ('title','manual','cartridge','other');

ALTER TABLE media DROP CONSTRAINT IF EXISTS media_kind_check;
ALTER TABLE media ADD CONSTRAINT media_kind_check CHECK (kind IN ('cover','boxfront','boxback','screenshot','logo','fanart','video'));