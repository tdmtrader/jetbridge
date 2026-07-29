DROP INDEX IF EXISTS agent_experiment_resource_source_admissions_lookup;
DROP TABLE IF EXISTS agent_experiment_resource_source_admissions;

ALTER TABLE agent_experiments
    DROP CONSTRAINT IF EXISTS agent_experiments_id_team_key;
