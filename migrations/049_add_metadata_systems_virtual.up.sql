-- Virtual systems own no games and aggregate their family/group instead, so the
-- catalog stays free of duplicated games (e.g. "arc" resolves to every arcade
-- board). A virtual system's games are the union of its group (when set) or its
-- family.
ALTER TABLE metadata_systems ADD COLUMN IF NOT EXISTS virtual BOOLEAN NOT NULL DEFAULT false;
