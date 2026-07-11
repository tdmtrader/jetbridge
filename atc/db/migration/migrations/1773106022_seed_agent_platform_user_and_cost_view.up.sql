-- §1.13: dedicated service user that owns the platform Anthropic
-- credential funding platform-initiated LLM work (harvest judge,
-- retrospective agent, calibration jobs).
INSERT INTO users (username, connector, sub)
VALUES ('platform', 'local', 'agent-platform')
ON CONFLICT (sub) DO NOTHING;

-- Dashboard view over the append-only ledger: per UTC-day, user, source.
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
