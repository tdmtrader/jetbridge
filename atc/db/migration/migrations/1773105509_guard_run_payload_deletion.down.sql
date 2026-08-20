SELECT ensure_pipeline_template_runs_empty();

DROP TRIGGER run_payload_delete_guard ON pipelines;
DROP FUNCTION guard_run_payload_deletion();
DROP INDEX pipeline_runs_terminal_completed_at_idx;
DROP INDEX pipeline_runs_terminal_number_idx;
