-- Games type: "base" (original retail), "hack" (ROM hack), "homebrew".
-- Defaults to base; the importer marks hacks when merging the hacks DAT.
ALTER TABLE games ADD COLUMN IF NOT EXISTS type TEXT NOT NULL DEFAULT 'base';
