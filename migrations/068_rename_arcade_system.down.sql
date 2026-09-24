UPDATE metadata_systems SET id = 'arc' WHERE id = 'arcade';

UPDATE submission_files
SET system_id = 'arc',
    object_key = replace(object_key, '/backgrounds/arcade.webp', '/backgrounds/arc.webp')
WHERE system_id = 'arcade';
