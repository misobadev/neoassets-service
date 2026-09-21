-- Region-less cover/logo media are presented by the API under the game's primary
-- region. Store that region on the row so region moves and deletes match the
-- actual media instead of silently missing it.
UPDATE media m
SET region = sub.region
FROM (
    SELECT gr.game_id,
           gr.region,
           ROW_NUMBER() OVER (PARTITION BY gr.game_id ORDER BY r.sort_order NULLS LAST, gr.region) AS rn
    FROM game_regions gr
    LEFT JOIN regions r ON r.name = gr.region
) sub
WHERE m.game_id = sub.game_id
  AND sub.rn = 1
  AND m.region = ''
  AND m.kind IN ('cover', 'logo');
