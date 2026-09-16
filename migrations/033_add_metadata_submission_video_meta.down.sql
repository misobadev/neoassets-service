ALTER TABLE metadata_submission_files DROP COLUMN IF EXISTS video_format;
ALTER TABLE metadata_submission_files DROP COLUMN IF EXISTS video_codec;
ALTER TABLE metadata_submission_files DROP COLUMN IF EXISTS width;
ALTER TABLE metadata_submission_files DROP COLUMN IF EXISTS height;
ALTER TABLE metadata_submission_files DROP COLUMN IF EXISTS fps;
ALTER TABLE metadata_submission_files DROP COLUMN IF EXISTS duration_sec;