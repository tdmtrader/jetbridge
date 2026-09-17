-- Finish the 2020 int -> bigint move instead of reading around it forever.
--
-- 1603405319 renamed build_events.build_id to build_id_old, added a bigint
-- build_id, and left the new column to be "populated gradually at runtime":
-- pre-rename rows keep their id only in build_id_old, and nothing ever
-- backfills them. Every reader has carried a `build_id = $1 OR build_id_old =
-- $1` predicate since. Team partitions have never had an index on the legacy
-- column (1530823998 created them without one; 1606068653 gave them only the
-- new index), so that OR forces a sequential scan of the partition on every
-- read -- whether or not a single legacy row exists, because the planner
-- cannot prove the column is empty.
--
-- The backfill below is the work upstream could not ask of every operator: on
-- a database with pre-rename rows it rewrites them. On one without, it touches
-- nothing. If a legacy row and a current row ever shared (build_id, event_id)
-- the unique index rejects the backfill and this migration aborts -- the same
-- collision the reader used to report as AMBIGUOUS_EVENT_STREAM, surfaced once,
-- loudly, instead of on every page forever.
UPDATE build_events SET build_id = build_id_old WHERE build_id IS NULL AND build_id_old IS NOT NULL;

-- Stop giving new pipeline partitions an index on a column about to disappear.
CREATE OR REPLACE FUNCTION on_pipeline_insert() RETURNS TRIGGER AS $$
BEGIN
  IF NEW.pipeline_run_id IS NOT NULL THEN
    RETURN NULL;
  END IF;

  EXECUTE format('CREATE TABLE IF NOT EXISTS pipeline_build_events_%s () INHERITS (build_events)', NEW.id);
  EXECUTE format('CREATE UNIQUE INDEX pipeline_build_events_%s_build_id_event_id ON pipeline_build_events_%s (build_id, event_id)', NEW.id, NEW.id);
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- Dropping the column from the inheritance parent drops it, and every index
-- over it, from every pipeline and team partition.
ALTER TABLE build_events DROP COLUMN build_id_old;
