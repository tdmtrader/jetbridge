ALTER TABLE agent_workflow_run_snapshots
    ADD COLUMN promoted_at TIMESTAMPTZ;

-- Inputs are immutable admission bindings and are visible immediately. Preserve
-- only outputs of already-successful runs when upgrading existing installs;
-- partial outputs from active or unsuccessful runs become hidden candidates.
UPDATE agent_workflow_run_snapshots binding
SET promoted_at = binding.created_at
FROM agent_workflow_runs run
WHERE run.id = binding.workflow_run_id
  AND (
    binding.direction = 'input'
    OR (binding.direction = 'output' AND run.status = 'succeeded')
  );

ALTER TABLE agent_workflow_run_snapshots
    ADD CONSTRAINT agent_workflow_run_snapshot_inputs_promoted
    CHECK (direction = 'output' OR promoted_at IS NOT NULL);

CREATE INDEX agent_workflow_run_snapshots_promoted_outputs
    ON agent_workflow_run_snapshots (workflow_run_id, port_name, snapshot_id)
    WHERE direction = 'output' AND promoted_at IS NOT NULL;
