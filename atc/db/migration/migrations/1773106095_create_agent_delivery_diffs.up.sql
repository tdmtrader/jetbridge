CREATE TABLE agent_delivery_diffs (
    build_id          INTEGER     NOT NULL,
    plan_id           TEXT        NOT NULL,
    ticket_id         INTEGER     NOT NULL,
    pipeline_run_id   INTEGER,
    repo              TEXT        NOT NULL,
    target_branch     TEXT        NOT NULL,
    delivered_branch  TEXT        NOT NULL,
    base_sha          TEXT        NOT NULL,
    pushed_sha        TEXT        NOT NULL,
    diff              JSONB       NOT NULL,
    total_files       INTEGER     NOT NULL,
    captured_files    INTEGER     NOT NULL,
    byte_len          INTEGER     NOT NULL,
    truncated         BOOLEAN     NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (build_id, plan_id)
);

CREATE INDEX agent_delivery_diffs_ticket
    ON agent_delivery_diffs (ticket_id, build_id DESC);
