CREATE TABLE pipeline_runs (
    id                   SERIAL PRIMARY KEY,
    template_pipeline_id INTEGER NOT NULL REFERENCES pipelines (id) ON DELETE CASCADE,
    instance_pipeline_id INTEGER REFERENCES pipelines (id) ON DELETE SET NULL,
    number               INTEGER NOT NULL,
    params               JSONB NOT NULL DEFAULT '{}',
    status               TEXT NOT NULL DEFAULT 'running'
                         CHECK (status IN ('running','succeeded','failed','errored','aborted')),
    created_by           TEXT NOT NULL DEFAULT '',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at         TIMESTAMPTZ,
    archived             BOOLEAN NOT NULL DEFAULT false
);

CREATE UNIQUE INDEX pipeline_runs_template_number ON pipeline_runs (template_pipeline_id, number);
CREATE INDEX pipeline_runs_status ON pipeline_runs (status) WHERE status = 'running';
