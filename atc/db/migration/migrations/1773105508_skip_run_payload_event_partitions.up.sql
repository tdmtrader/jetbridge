CREATE OR REPLACE FUNCTION on_pipeline_insert() RETURNS TRIGGER AS $$
BEGIN
  IF NEW.pipeline_run_id IS NOT NULL THEN
    RETURN NULL;
  END IF;

  EXECUTE format('CREATE TABLE IF NOT EXISTS pipeline_build_events_%s () INHERITS (build_events)', NEW.id);
  EXECUTE format('CREATE UNIQUE INDEX pipeline_build_events_%s_build_id_event_id ON pipeline_build_events_%s (build_id, event_id)', NEW.id, NEW.id);
  EXECUTE format('CREATE UNIQUE INDEX pipeline_build_events_%s_build_id_old_event_id ON pipeline_build_events_%s (build_id_old, event_id)', NEW.id, NEW.id);
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;
