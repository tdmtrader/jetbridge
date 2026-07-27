-- Write-only columns, unreachable states, and the PARK-V2 leftovers.
--
-- Everything dropped here shares one property: no code reads it. Either the
-- writer is gone, the reader never existed, or a later constraint made the
-- value unreachable. Keeping such a column is not "harmless storage" — it is a
-- claim that the platform tracks something it does not.

-- 1. PARK-V2. 'parked' was a park-exit partial ingestion: the agent step
-- stopped awaiting a human, its event stream ended with step.park instead of
-- step.end, and the ingestion recorded the row as a defined end rather than an
-- error. The runner structurally never emits it — v3 human waits are
-- agent_workflow_waits on the durable run, not a step exit status — so the
-- ingestion's "no step.end is an error UNLESS the results say parked" special
-- case had no reachable true branch. Any surviving row is exactly what the
-- unconditional rule now produces: an event stream that ended without step.end.
UPDATE agent_run_metrics SET status = 'error' WHERE status = 'parked';

ALTER TABLE agent_run_metrics DROP CONSTRAINT agent_run_metrics_status_check;
ALTER TABLE agent_run_metrics ADD CONSTRAINT agent_run_metrics_status_check
    CHECK (status IN ('ok','failed','error','incomplete'));

-- session_id came in with PARK-V2 to carry the claude session a park could be
-- resumed from. No writer ever set it and no read path ever selected it.
ALTER TABLE agent_run_metrics DROP COLUMN session_id;

-- 2. agent_cost_daily_rollup: a dashboard view with no dashboard. Nothing in
-- the API, fly, or the web app has ever selected from it; the cost surfaces
-- aggregate agent_cost_ledger directly with their own grouping. (The §1.13
-- platform service user seeded by the same migration stays — it owns the
-- platform credential.)
DROP VIEW agent_cost_daily_rollup;

-- 3. agent_workflow_outcomes.publication_state: 1773106115 made the row's
-- publication evidence exact — a state is either 'not_requested' with no
-- evidence, or 'published' with an FK-verified succeeded publication. That
-- constraint already rejects 'pending' and 'failed' outright, so the column
-- CHECK still advertised two states the table could never hold.
ALTER TABLE agent_workflow_outcomes
    DROP CONSTRAINT agent_workflow_outcomes_publication_state_check;
ALTER TABLE agent_workflow_outcomes
    ADD CONSTRAINT agent_workflow_outcomes_publication_state_check
    CHECK (publication_state IN ('not_requested','published'));

-- 4. Jira seams. There is no Jira sync component and no plan for one: the
-- create handler rejected origin 'jira' with a 400 the moment it was offered,
-- so the origin value has never existed in a row and the work-item adapter
-- branch keyed off it has never been taken. agent_tickets.external_ref stays —
-- it is an origin-agnostic external identifier — but its constraint no longer
-- pretends a Jira issue key is the only thing that can land there.
ALTER TABLE agent_tickets DROP CONSTRAINT agent_tickets_origin_check;
UPDATE agent_tickets SET origin = 'web' WHERE origin = 'jira';
ALTER TABLE agent_tickets ADD CONSTRAINT agent_tickets_origin_check
    CHECK (origin IN ('web','fly','retrospective'));

-- jira_account_id: the mapping half of the same seam, write-only.
-- last_verified_at: read on every credential select, surfaced in the API and
-- rendered as a web-page column, and never written by anything. A "last
-- verified" that is always blank is worse than no column at all.
ALTER TABLE agent_user_credentials
    DROP COLUMN jira_account_id,
    DROP COLUMN last_verified_at;

-- 5. agent_tickets.branch: the branch the agent's work landed on, written by
-- compatibility harvest. Harvest is gone; the run-completion reconciler that
-- replaced it deliberately leaves the value empty, so every ticket transitioned
-- to needs_review since then carries ''. The delivered branch is on the durable
-- workflow run. repo and target_branch stay: they are the ticket's own
-- pre-dispatch repository selection, authored by the human who filed it.
ALTER TABLE agent_tickets DROP COLUMN branch;

-- 6. agent_reviews occurrence columns. A review row is one sealed review/v1
-- snapshot; WHERE it was produced comes from the agent_snapshot_productions row
-- the query selected, because one snapshot can be produced in several builds
-- and runs. Every read path already joins productions/builds/pipelines/jobs and
-- teams for exactly that reason, so the projector's copies were written and
-- never read — and idx_agent_reviews_team_created indexed a name no query
-- filters on.
DROP INDEX idx_agent_reviews_team_created;

ALTER TABLE agent_reviews
    DROP COLUMN build_name,
    DROP COLUMN team_name,
    DROP COLUMN pipeline_name,
    DROP COLUMN job_name,
    DROP COLUMN submitted_by;
