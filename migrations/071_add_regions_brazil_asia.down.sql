DELETE FROM regions WHERE id IN ('brazil', 'asia');

-- Restore the previous sort order of the tail.
UPDATE regions SET sort_order = 5  WHERE id = 'spain';
UPDATE regions SET sort_order = 6  WHERE id = 'france';
UPDATE regions SET sort_order = 7  WHERE id = 'germany';
UPDATE regions SET sort_order = 8  WHERE id = 'italy';
UPDATE regions SET sort_order = 9  WHERE id = 'korea';
UPDATE regions SET sort_order = 10 WHERE id = 'china';
