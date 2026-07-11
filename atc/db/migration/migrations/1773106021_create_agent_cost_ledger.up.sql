CREATE TABLE agent_cost_ledger (
    id                    BIGSERIAL PRIMARY KEY,
    occurred_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id               INTEGER,
    user_name             TEXT NOT NULL DEFAULT '',
    ticket_id             INTEGER,
    pipeline_run_id       INTEGER,
    build_id              INTEGER NOT NULL DEFAULT 0,
    step_name             TEXT NOT NULL DEFAULT '',
    source                TEXT NOT NULL
                          CHECK (source IN ('agent_step','gateway','harvest_judge','retrospective','ci_agent','probe')),
    provider              TEXT NOT NULL DEFAULT 'anthropic',
    model                 TEXT NOT NULL DEFAULT '',
    input_tokens          BIGINT NOT NULL DEFAULT 0,
    output_tokens         BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens     BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    turns                 INTEGER NOT NULL DEFAULT 0,
    cost_usd              NUMERIC(12,6) NOT NULL DEFAULT 0,
    metadata              JSONB
);

CREATE INDEX agent_cost_ledger_user_day ON agent_cost_ledger (user_id, occurred_at DESC);
CREATE INDEX agent_cost_ledger_ticket   ON agent_cost_ledger (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX agent_cost_ledger_day      ON agent_cost_ledger (occurred_at DESC);
