DROP TRIGGER IF EXISTS builds_capture_pipeline_config_version ON builds;
DROP FUNCTION IF EXISTS capture_build_pipeline_config_version();
ALTER TABLE builds DROP COLUMN IF EXISTS pipeline_config_version;
