-- Agent transcript observability (ticket #43): capture the agent's turn-by-turn
-- tool-call transcript (raw `claude --output-format stream-json --verbose` stdout,
-- one JSON object per line). The runner writes flight/transcript.ndjson; harvest
-- ingestion persists it here, keyed like agent_run_metrics on (build_id, plan_id).
CREATE TABLE agent_run_transcripts (
    build_id   INTEGER     NOT NULL,
    plan_id    TEXT        NOT NULL,
    ticket_id  INTEGER,                 -- NULL for pure-CI agent steps
    step_name  TEXT,
    ndjson     TEXT        NOT NULL,
    byte_len   INTEGER     NOT NULL,
    truncated  BOOLEAN     NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (build_id, plan_id)
);

CREATE INDEX agent_run_transcripts_ticket ON agent_run_transcripts (ticket_id) WHERE ticket_id IS NOT NULL;
