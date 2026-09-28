-- Add Brazil and Asia to the region catalog. Brazil is a major market for Sega
-- (Master System / Mega Drive) and Asia covers the broader Asian release region.
-- The tail is re-numbered so the display order and primary-name priority stay
-- World > USA > Europe > Japan > Brazil > Asia > country-specific regions.
UPDATE regions SET sort_order = 7  WHERE id = 'spain';
UPDATE regions SET sort_order = 8  WHERE id = 'france';
UPDATE regions SET sort_order = 9  WHERE id = 'germany';
UPDATE regions SET sort_order = 10 WHERE id = 'italy';
UPDATE regions SET sort_order = 11 WHERE id = 'korea';
UPDATE regions SET sort_order = 12 WHERE id = 'china';

INSERT INTO regions (id, name, sort_order) VALUES
    ('brazil', 'Brazil', 5),
    ('asia',   'Asia',   6)
ON CONFLICT (id) DO NOTHING;
