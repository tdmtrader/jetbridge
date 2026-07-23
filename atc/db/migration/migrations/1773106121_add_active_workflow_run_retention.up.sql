-- A workflow execution may consume and produce typed snapshots that are only
-- useful while the run is active. Keep those values alive without turning
-- every intermediate into permanent workflow history.
ALTER TABLE agent_snapshot_retention_claims
    DROP CONSTRAINT agent_snapshot_retention_claims_class_check,
    ADD COLUMN workflow_run_id BIGINT;

ALTER TABLE agent_snapshot_retention_claims
    ADD CONSTRAINT agent_snapshot_retention_claims_class_check
        CHECK (class IN ('binding', 'run', 'workflow', 'fixture', 'pin')),
    ADD CONSTRAINT agent_snapshot_retention_claims_run_shape_check
        CHECK (
            (class = 'run' AND workflow_run_id IS NOT NULL AND expires_at IS NULL)
            OR (class <> 'run' AND workflow_run_id IS NULL)
        );

ALTER TABLE agent_workflow_runs
    ADD CONSTRAINT agent_workflow_runs_id_team_key UNIQUE (id, team_id);

ALTER TABLE agent_snapshot_retention_claims
    ADD CONSTRAINT agent_snapshot_retention_claims_run_owner_fkey
        FOREIGN KEY (workflow_run_id, team_id)
        REFERENCES agent_workflow_runs (id, team_id)
        ON DELETE CASCADE;

CREATE INDEX agent_snapshot_retention_claims_workflow_run
    ON agent_snapshot_retention_claims (workflow_run_id, id)
    WHERE workflow_run_id IS NOT NULL;

-- Inputs were previously retained forever under workflow-scoped claims.
-- Reclassify their ownership: active runs receive a run claim and terminal
-- runs release the legacy claim.
DELETE FROM agent_snapshot_retention_claims claim
USING agent_workflow_run_snapshots binding, agent_workflow_runs run
WHERE binding.workflow_run_id = run.id
  AND binding.direction = 'input'
  AND claim.snapshot_id = binding.snapshot_id
  AND claim.team_id = run.team_id
  AND claim.class = 'workflow'
  AND claim.actor = format('workflow-run:%s:input:%s', run.id, binding.port_name);

INSERT INTO agent_snapshot_retention_claims
    (snapshot_id, team_id, class, workflow_run_id, actor, reason)
SELECT binding.snapshot_id, run.team_id, 'run', run.id,
       format('workflow-run:%s:input:%s', run.id, binding.port_name),
       'active workflow-run input'
FROM agent_workflow_run_snapshots binding
JOIN agent_workflow_runs run ON run.id = binding.workflow_run_id
WHERE binding.direction = 'input'
  AND run.status IN ('admitting', 'running', 'canceling')
ON CONFLICT (snapshot_id, team_id, class, actor) DO NOTHING;

-- Earlier await_snapshot materialization did not create the ordinary binding
-- grace claim that task and agent outputs receive. The configured duration is
-- not persisted, so historical answers use the server default (168 hours)
-- measured from their durable resolution time.
INSERT INTO agent_snapshot_retention_claims
    (snapshot_id, team_id, class, expires_at, actor, reason)
SELECT wait.answer_snapshot_id, wait.team_id, 'binding',
       COALESCE(wait.resolved_at, wait.updated_at, wait.created_at) + interval '168 hours',
       format('workflow-run:%s:wait:%s', wait.workflow_run_id, wait.id),
       'workflow wait answer post-run grace'
FROM agent_workflow_waits wait
WHERE wait.workflow_port IS NULL
  AND wait.answer_snapshot_id IS NOT NULL
ON CONFLICT (snapshot_id, team_id, class, actor) DO NOTHING;

-- Historical internal productions only have their ordinary expiring binding
-- claim. Backfill active executions so a long-running workflow cannot lose an
-- intermediate before it reaches the next node.
--
-- The pre-retention executor recorded no workflow identity when a producer had
-- only internal outputs. Its selected build is still durable evidence. Repair
-- the production only when that build belongs to exactly one same-team
-- workflow run; ambiguous or definition-conflicting evidence fails closed.
WITH unique_build_run AS (
    SELECT production.id AS production_id,
           min(run.id) AS workflow_run_id,
           min(run.workflow_definition_id) AS workflow_definition_id
    FROM agent_snapshot_productions production
    JOIN agent_workflow_runs run
      ON run.planned_build_id = production.build_id
     AND run.team_id = production.team_id
     AND (
         production.workflow_definition_id IS NULL
         OR production.workflow_definition_id = run.workflow_definition_id
     )
    WHERE production.occurrence_kind = 'build'
      AND production.workflow_run_id IS NULL
    GROUP BY production.id
    HAVING count(*) = 1
)
UPDATE agent_snapshot_productions production
SET workflow_run_id = association.workflow_run_id,
    workflow_definition_id = COALESCE(
        production.workflow_definition_id,
        association.workflow_definition_id
    )
FROM unique_build_run association
WHERE production.id = association.production_id;

INSERT INTO agent_snapshot_retention_claims
    (snapshot_id, team_id, class, workflow_run_id, actor, reason)
SELECT production.snapshot_id, production.team_id, 'run', run.id,
       format('workflow-run:%s:production:%s', run.id, production.id),
       'active workflow-run production'
FROM agent_snapshot_productions production
JOIN agent_workflow_runs run ON run.id = production.workflow_run_id
WHERE run.status IN ('admitting', 'running', 'canceling')
ON CONFLICT (snapshot_id, team_id, class, actor) DO NOTHING;

-- Await-snapshot questions, defaults, and internal answers used the permanent
-- workflow class before run-scoped retention existed. Public answers (those
-- with workflow_port) deliberately remain workflow-scoped outputs.
DELETE FROM agent_snapshot_retention_claims claim
USING agent_workflow_waits wait
WHERE claim.team_id = wait.team_id
  AND claim.class = 'workflow'
  AND (
      (claim.snapshot_id = wait.question_snapshot_id
       AND claim.actor = format('workflow-run:%s:wait:%s:question', wait.workflow_run_id, wait.id))
      OR
      (claim.snapshot_id = wait.default_snapshot_id
       AND claim.actor = format('workflow-run:%s:wait:%s:default', wait.workflow_run_id, wait.id))
      OR
      (wait.workflow_port IS NULL
       AND claim.snapshot_id = wait.answer_snapshot_id
       AND claim.actor = format('workflow-run:%s:wait:%s', wait.workflow_run_id, wait.id))
  );

INSERT INTO agent_snapshot_retention_claims
    (snapshot_id, team_id, class, workflow_run_id, actor, reason)
SELECT value.snapshot_id, wait.team_id, 'run', run.id,
       format('workflow-run:%s:wait:%s:%s', run.id, wait.id, value.kind),
       format('active workflow wait %s', value.kind)
FROM agent_workflow_waits wait
JOIN agent_workflow_runs run ON run.id = wait.workflow_run_id
CROSS JOIN LATERAL (
    VALUES
        (wait.question_snapshot_id, 'question'),
        (wait.default_snapshot_id, 'default')
) AS value(snapshot_id, kind)
WHERE value.snapshot_id IS NOT NULL
  AND run.status IN ('admitting', 'running', 'canceling')
ON CONFLICT (snapshot_id, team_id, class, actor) DO NOTHING;

INSERT INTO agent_snapshot_retention_claims
    (snapshot_id, team_id, class, workflow_run_id, actor, reason)
SELECT wait.answer_snapshot_id, wait.team_id, 'run', run.id,
       format('workflow-run:%s:wait:%s', run.id, wait.id),
       'active workflow wait answer'
FROM agent_workflow_waits wait
JOIN agent_workflow_runs run ON run.id = wait.workflow_run_id
WHERE wait.workflow_port IS NULL
  AND wait.answer_snapshot_id IS NOT NULL
  AND run.status IN ('admitting', 'running', 'canceling')
ON CONFLICT (snapshot_id, team_id, class, actor) DO NOTHING;
