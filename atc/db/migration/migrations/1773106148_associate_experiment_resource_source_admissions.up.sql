-- An experiment prepares resource sources once, at Start, then every cell,
-- evaluator, and retry reads the same ready admission through this durable
-- association. The association is deliberately keyed by both the immutable
-- workflow definition and the rendered source configuration hash.
ALTER TABLE agent_experiments
    ADD CONSTRAINT agent_experiments_id_team_key UNIQUE (id, team_id);

CREATE TABLE agent_experiment_resource_source_admissions (
    experiment_id BIGINT NOT NULL,
    team_id INTEGER NOT NULL,
    workflow_definition_id INTEGER NOT NULL,
    source_config_hash TEXT NOT NULL
        CHECK (source_config_hash ~ '^[0-9a-f]{64}$'),
    resource_source_admission_id BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (experiment_id, workflow_definition_id, source_config_hash),
    UNIQUE (experiment_id, resource_source_admission_id),
    FOREIGN KEY (experiment_id, team_id)
        REFERENCES agent_experiments (id, team_id)
        ON DELETE CASCADE,
    FOREIGN KEY (
        resource_source_admission_id,
        team_id,
        workflow_definition_id,
        source_config_hash
    ) REFERENCES agent_workflow_resource_source_admissions (
        id,
        team_id,
        workflow_definition_id,
        source_config_hash
    ) ON DELETE RESTRICT
);

CREATE INDEX agent_experiment_resource_source_admissions_lookup
    ON agent_experiment_resource_source_admissions
       (experiment_id, team_id, workflow_definition_id, source_config_hash);
