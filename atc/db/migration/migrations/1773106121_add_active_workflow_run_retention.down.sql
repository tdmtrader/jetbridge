-- Older binaries do not understand run-scoped claims. Preserve their
-- retention conservatively as workflow claims before removing the owner link.
INSERT INTO agent_snapshot_retention_claims
    (snapshot_id, team_id, class, expires_at, actor, reason)
SELECT snapshot_id, team_id, 'workflow', NULL, actor, reason
FROM agent_snapshot_retention_claims
WHERE class = 'run'
ON CONFLICT (snapshot_id, team_id, class, actor) DO NOTHING;

-- Reconstruct the permanent input/wait claims that predated this migration,
-- including for runs that were already terminal when the up migration
-- intentionally released them.
INSERT INTO agent_snapshot_retention_claims
    (snapshot_id, team_id, class, expires_at, actor, reason)
SELECT binding.snapshot_id, run.team_id, 'workflow', NULL,
       format('workflow-run:%s:input:%s', run.id, binding.port_name),
       'durable workflow-run input'
FROM agent_workflow_run_snapshots binding
JOIN agent_workflow_runs run ON run.id = binding.workflow_run_id
WHERE binding.direction = 'input'
ON CONFLICT (snapshot_id, team_id, class, actor) DO NOTHING;

INSERT INTO agent_snapshot_retention_claims
    (snapshot_id, team_id, class, expires_at, actor, reason)
SELECT value.snapshot_id, wait.team_id, 'workflow', NULL,
       format('workflow-run:%s:wait:%s:%s', wait.workflow_run_id, wait.id, value.kind),
       format('durable workflow wait %s', value.kind)
FROM agent_workflow_waits wait
CROSS JOIN LATERAL (
    VALUES
        (wait.question_snapshot_id, 'question'),
        (wait.default_snapshot_id, 'default')
) AS value(snapshot_id, kind)
WHERE value.snapshot_id IS NOT NULL
ON CONFLICT (snapshot_id, team_id, class, actor) DO NOTHING;

INSERT INTO agent_snapshot_retention_claims
    (snapshot_id, team_id, class, expires_at, actor, reason)
SELECT wait.answer_snapshot_id, wait.team_id, 'workflow', NULL,
       format('workflow-run:%s:wait:%s', wait.workflow_run_id, wait.id),
       'durable workflow wait answer'
FROM agent_workflow_waits wait
WHERE wait.workflow_port IS NULL
  AND wait.answer_snapshot_id IS NOT NULL
ON CONFLICT (snapshot_id, team_id, class, actor) DO NOTHING;

DELETE FROM agent_snapshot_retention_claims
WHERE class = 'run';

DROP INDEX agent_snapshot_retention_claims_workflow_run;

ALTER TABLE agent_snapshot_retention_claims
    DROP CONSTRAINT agent_snapshot_retention_claims_run_owner_fkey,
    DROP CONSTRAINT agent_snapshot_retention_claims_run_shape_check,
    DROP CONSTRAINT agent_snapshot_retention_claims_class_check,
    DROP COLUMN workflow_run_id;

ALTER TABLE agent_snapshot_retention_claims
    ADD CONSTRAINT agent_snapshot_retention_claims_class_check
        CHECK (class IN ('binding', 'workflow', 'fixture', 'pin'));

ALTER TABLE agent_workflow_runs
    DROP CONSTRAINT agent_workflow_runs_id_team_key;
