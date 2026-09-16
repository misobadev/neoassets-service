DROP INDEX IF EXISTS idx_metadata_systems_group;
DROP INDEX IF EXISTS idx_metadata_systems_family;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='metadata_systems' AND column_name='family')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='metadata_systems' AND column_name='type') THEN
    ALTER TABLE metadata_systems RENAME COLUMN family TO type;
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='metadata_systems' AND column_name='system_group')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='metadata_systems' AND column_name='family') THEN
    ALTER TABLE metadata_systems RENAME COLUMN system_group TO family;
  END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_metadata_systems_family ON metadata_systems(family);
CREATE INDEX IF NOT EXISTS idx_metadata_systems_type ON metadata_systems(type);
