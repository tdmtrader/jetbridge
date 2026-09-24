DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pipeline_run_build_closures WHERE closed_at IS NULL) THEN
  RAISE EXCEPTION 'cannot remove open build closures';
 END IF;
END $$;
DROP TRIGGER run_build_closure_immutable ON pipeline_run_build_closures;
DROP FUNCTION immutable_run_build_closure();
DROP TABLE pipeline_run_build_closures;
