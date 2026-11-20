-- Intentionally a no-op: the column predates this migration on existing
-- databases (created by the importer), so dropping it would lose data.
SELECT 1;
