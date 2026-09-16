-- Redefine the system metadata columns to the agreed concepts:
--   family       -> broad category (arcade, console, computer, handheld, virtual)
--   system_group -> sub-group within a family, by emulator (e.g. mame-fbneo,
--                   flycast, supermodel, dolphin for arcade boards)
-- The previous "family" (arcade MAME/FBNeo) becomes system_group and the previous
-- "type" (broad category) becomes family. Guarded so it is idempotent even if the
-- importer already added system_group.
DROP INDEX IF EXISTS idx_metadata_systems_family;
DROP INDEX IF EXISTS idx_metadata_systems_type;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='metadata_systems' AND column_name='family')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='metadata_systems' AND column_name='system_group') THEN
    ALTER TABLE metadata_systems RENAME COLUMN family TO system_group;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='metadata_systems' AND column_name='type')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='metadata_systems' AND column_name='family') THEN
    ALTER TABLE metadata_systems RENAME COLUMN type TO family;
  END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_metadata_systems_family ON metadata_systems(family);
CREATE INDEX IF NOT EXISTS idx_metadata_systems_group ON metadata_systems(system_group);
