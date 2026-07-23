-- Pre-6117 readers cannot distinguish staged outputs from committed outputs.
-- Remove only the bindings that were never promoted; retain the immutable
-- snapshots and their production/lineage records for audit and retention.
DELETE FROM agent_workflow_run_snapshots
WHERE direction = 'output'
  AND promoted_at IS NULL;

DROP INDEX agent_workflow_run_snapshots_promoted_outputs;

ALTER TABLE agent_workflow_run_snapshots
    DROP CONSTRAINT agent_workflow_run_snapshot_inputs_promoted,
    DROP COLUMN promoted_at;
