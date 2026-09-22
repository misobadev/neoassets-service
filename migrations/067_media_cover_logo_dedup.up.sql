-- A cover/logo submitted without a region belongs to the game's primary region
-- (there is one cover and one logo per region). Resolve any region-less row to
-- that region, then drop duplicates so a region shows a single asset.
WITH primary_region AS (
    SELECT DISTINCT ON (gr.game_id) gr.game_id, gr.region
    FROM game_regions gr
    LEFT JOIN regions r ON r.name = gr.region
    WHERE gr.name <> '' OR gr.release_year IS NOT NULL
    ORDER BY gr.game_id, r.sort_order NULLS LAST, gr.region
)
UPDATE media m
SET region = pr.region
FROM primary_region pr
WHERE m.game_id = pr.game_id
  AND m.region = ''
  AND m.kind IN ('cover', 'logo');

-- Keep only the newest cover/logo per (game, kind, region).
DELETE FROM media m
USING media keep
WHERE m.game_id = keep.game_id
  AND m.kind = keep.kind
  AND m.region = keep.region
  AND m.kind IN ('cover', 'logo')
  AND (m.created_at, m.id) < (keep.created_at, keep.id);
