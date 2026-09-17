-- Restore the legacy column and its indexes. The data it held is gone: the up
-- migration folded it into build_id, which is where every row now carries its
-- id. Old binaries read `build_id OR build_id_old` and find every event under
-- the first arm, so an empty legacy column is correct, not lossy.
ALTER TABLE build_events ADD COLUMN build_id_old integer;

CREATE UNIQUE INDEX build_events_build_id_old_event_id ON build_events (build_id_old, event_id);

DO $$
DECLARE
  pipeline record;
BEGIN
FOR pipeline IN
  SELECT id FROM pipelines WHERE pipeline_run_id IS NULL
LOOP
  BEGIN
    EXECUTE format('CREATE UNIQUE INDEX IF NOT EXISTS pipeline_build_events_%s_build_id_old_event_id ON pipeline_build_events_%s (build_id_old, event_id)', pipeline.id, pipeline.id);
  EXCEPTION
  WHEN undefined_table THEN
    RAISE NOTICE '%', SQLERRM;
  END;
END LOOP;
END;
$$ LANGUAGE plpgsql;

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
