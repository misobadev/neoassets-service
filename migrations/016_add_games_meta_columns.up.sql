-- Add metadata enrichment columns to games (populated by the data importer:
-- release month).
ALTER TABLE games ADD COLUMN IF NOT EXISTS release_month INT;
