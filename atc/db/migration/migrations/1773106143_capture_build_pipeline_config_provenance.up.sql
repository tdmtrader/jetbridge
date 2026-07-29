ALTER TABLE builds ADD COLUMN pipeline_config_version INTEGER CHECK (pipeline_config_version IS NULL OR pipeline_config_version >= 0);
CREATE FUNCTION capture_build_pipeline_config_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.pipeline_id IS NOT NULL AND NEW.job_id IS NOT NULL THEN
    SELECT pipeline.version INTO NEW.pipeline_config_version
      FROM pipelines pipeline JOIN jobs job ON job.id = NEW.job_id AND job.pipeline_id = pipeline.id
      WHERE pipeline.id = NEW.pipeline_id AND pipeline.team_id = NEW.team_id;
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER builds_capture_pipeline_config_version BEFORE INSERT ON builds FOR EACH ROW EXECUTE FUNCTION capture_build_pipeline_config_version();
