-- Approval re-encodes PNG and JPG backgrounds and logos to WebP and moves them
-- to packs/{pack}/{backgrounds|logos}/{system}.webp, but file_name kept the
-- uploaded extension (snes.png). Clients save each file under file_name, so the
-- WebP was stored as snes.png and never found. Point file_name of every
-- published background and logo at the last segment of its canonical key.
UPDATE submission_files
SET file_name = regexp_replace(object_key, '^.*/', '')
WHERE kind IN ('background', 'logo')
  AND object_key ~ '^packs/[^/]+/(backgrounds|logos)/[^/]+$'
  AND file_name <> regexp_replace(object_key, '^.*/', '');
