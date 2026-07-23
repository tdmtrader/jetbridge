DROP TRIGGER agent_experiments_frozen_scorecard_immutable ON agent_experiments;
DROP FUNCTION reject_agent_experiment_frozen_scorecard_mutation();

ALTER TABLE agent_experiments
    DROP COLUMN frozen_scorecard;
