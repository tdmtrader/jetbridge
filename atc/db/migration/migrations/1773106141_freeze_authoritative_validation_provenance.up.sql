ALTER TABLE agent_workflow_runs
    ADD COLUMN dev_validation_provenance_hash TEXT NOT NULL DEFAULT ''
    CHECK (dev_validation_provenance_hash = '' OR dev_validation_provenance_hash ~ '^[0-9a-f]{64}$');

ALTER TABLE agent_experiment_variants
    ADD COLUMN dev_validation_provenance_hash TEXT
    CHECK (dev_validation_provenance_hash IS NULL OR dev_validation_provenance_hash ~ '^[0-9a-f]{64}$');

ALTER TABLE agent_experiments
    ADD COLUMN evaluator_dev_validation_provenance_hash TEXT
    CHECK (evaluator_dev_validation_provenance_hash IS NULL OR evaluator_dev_validation_provenance_hash ~ '^[0-9a-f]{64}$');

UPDATE agent_experiment_variants SET dev_validation_provenance_hash = '' WHERE target_config_hash IS NOT NULL;
UPDATE agent_experiments SET evaluator_dev_validation_provenance_hash = '' WHERE evaluator_target_config_hash IS NOT NULL;

ALTER TABLE agent_experiment_variants ADD CONSTRAINT agent_experiment_variants_validation_provenance_parity
    CHECK ((target_config_hash IS NULL) = (dev_validation_provenance_hash IS NULL));
ALTER TABLE agent_experiments ADD CONSTRAINT agent_experiments_evaluator_validation_provenance_parity
    CHECK ((evaluator_target_config_hash IS NULL) = (evaluator_dev_validation_provenance_hash IS NULL));
