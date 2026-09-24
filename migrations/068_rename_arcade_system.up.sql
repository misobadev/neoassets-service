-- Rename the virtual arcade aggregate from "arc" to "arcade" so the id reads as
-- the arcade parent. Its SAP backgrounds move from
-- packs/{pack}/backgrounds/arc.webp to .../arcade.webp; the R2 objects are
-- copied separately (the DB only stores the key).
UPDATE metadata_systems SET id = 'arcade' WHERE id = 'arc';

UPDATE submission_files
SET system_id = 'arcade',
    object_key = replace(object_key, '/backgrounds/arc.webp', '/backgrounds/arcade.webp')
WHERE system_id = 'arc';
