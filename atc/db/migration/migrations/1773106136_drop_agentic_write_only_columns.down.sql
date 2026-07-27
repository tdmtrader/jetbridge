ALTER TABLE agent_reviews
    ADD COLUMN build_name    TEXT NOT NULL DEFAULT '',
    ADD COLUMN team_name     TEXT NOT NULL DEFAULT '',
    ADD COLUMN pipeline_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN job_name      TEXT NOT NULL DEFAULT '',
    ADD COLUMN submitted_by  TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_agent_reviews_team_created
    ON agent_reviews (team_name, created_at DESC);

ALTER TABLE agent_tickets ADD COLUMN branch TEXT NOT NULL DEFAULT '';

ALTER TABLE agent_user_credentials
    ADD COLUMN last_verified_at TIMESTAMPTZ,
    ADD COLUMN jira_account_id  TEXT NOT NULL DEFAULT '';

ALTER TABLE agent_tickets DROP CONSTRAINT agent_tickets_origin_check;
ALTER TABLE agent_tickets ADD CONSTRAINT agent_tickets_origin_check
    CHECK (origin IN ('web','fly','jira','retrospective'));

ALTER TABLE agent_workflow_outcomes
    DROP CONSTRAINT agent_workflow_outcomes_publication_state_check;
ALTER TABLE agent_workflow_outcomes
    ADD CONSTRAINT agent_workflow_outcomes_publication_state_check
    CHECK (publication_state IN ('not_requested','pending','published','failed'));

CREATE VIEW agent_cost_daily_rollup AS
SELECT
    (occurred_at AT TIME ZONE 'UTC')::date AS day,
    COALESCE(user_name, '')                AS user_name,
    source,
    COUNT(*)::int                          AS entries,
    SUM(input_tokens)                      AS input_tokens,
    SUM(output_tokens)                     AS output_tokens,
    SUM(turns)::int                        AS turns,
    SUM(cost_usd)                          AS cost_usd
FROM agent_cost_ledger
GROUP BY 1, 2, 3;

ALTER TABLE agent_run_metrics ADD COLUMN session_id TEXT NOT NULL DEFAULT '';

ALTER TABLE agent_run_metrics DROP CONSTRAINT agent_run_metrics_status_check;
ALTER TABLE agent_run_metrics ADD CONSTRAINT agent_run_metrics_status_check
    CHECK (status IN ('ok','failed','error','parked','incomplete'));
