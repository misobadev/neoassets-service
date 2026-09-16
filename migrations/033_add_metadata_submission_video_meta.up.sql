-- Video metadata captured when a video submission is uploaded, so reviewers can
-- see format/codec/resolution/fps/duration without downloading the file.
ALTER TABLE metadata_submission_files ADD COLUMN IF NOT EXISTS video_format TEXT NOT NULL DEFAULT '';
ALTER TABLE metadata_submission_files ADD COLUMN IF NOT EXISTS video_codec TEXT NOT NULL DEFAULT '';
ALTER TABLE metadata_submission_files ADD COLUMN IF NOT EXISTS width INT NOT NULL DEFAULT 0;
ALTER TABLE metadata_submission_files ADD COLUMN IF NOT EXISTS height INT NOT NULL DEFAULT 0;
ALTER TABLE metadata_submission_files ADD COLUMN IF NOT EXISTS fps INT NOT NULL DEFAULT 0;
ALTER TABLE metadata_submission_files ADD COLUMN IF NOT EXISTS duration_sec NUMERIC NOT NULL DEFAULT 0;