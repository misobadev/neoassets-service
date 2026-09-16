-- Add a per-file reason (why the user is replacing an existing image). The new
-- upload lives in a temp key; the canonical key keeps the old image until the
-- submission is approved, so the reviewer can compare old vs new.
ALTER TABLE submission_files ADD COLUMN IF NOT EXISTS reason TEXT NOT NULL DEFAULT '';
