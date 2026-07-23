ALTER TABLE agent_experiments
    ADD COLUMN frozen_scorecard JSONB;

ALTER TABLE agent_experiments
    ADD CONSTRAINT agent_experiments_frozen_scorecard_shape CHECK (
        frozen_scorecard IS NULL
        OR (
            jsonb_typeof(frozen_scorecard) = 'object'
            AND frozen_scorecard ?& ARRAY[
                'experiment_id', 'control', 'variants', 'comparisons', 'cells'
            ]
            AND frozen_scorecard - ARRAY[
                'experiment_id', 'control', 'variants', 'comparisons', 'cells'
            ] = '{}'::jsonb
            AND jsonb_typeof(frozen_scorecard -> 'experiment_id') = 'string'
            AND (frozen_scorecard ->> 'experiment_id') ~ '^[1-9][0-9]*$'
            AND (frozen_scorecard ->> 'experiment_id')::BIGINT = id
            AND jsonb_typeof(frozen_scorecard -> 'control') = 'string'
            AND btrim(frozen_scorecard ->> 'control') = frozen_scorecard ->> 'control'
            AND frozen_scorecard ->> 'control' <> ''
            AND octet_length(frozen_scorecard ->> 'control') <= 128
            AND jsonb_typeof(frozen_scorecard -> 'variants') = 'object'
            AND jsonb_typeof(frozen_scorecard -> 'comparisons') = 'object'
            AND jsonb_typeof(frozen_scorecard -> 'cells') = 'array'
            AND jsonb_array_length(frozen_scorecard -> 'cells') <= 2000
        )
    ),
    ADD CONSTRAINT agent_experiments_frozen_scorecard_terminal CHECK (
        frozen_scorecard IS NULL
        OR state IN ('canceled', 'completed', 'failed')
    );

CREATE FUNCTION reject_agent_experiment_frozen_scorecard_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.frozen_scorecard IS NOT NULL
       AND NEW.frozen_scorecard IS DISTINCT FROM OLD.frozen_scorecard THEN
        RAISE EXCEPTION 'agent experiment frozen scorecards are immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_experiments_frozen_scorecard_immutable
BEFORE UPDATE OF frozen_scorecard ON agent_experiments
FOR EACH ROW
EXECUTE FUNCTION reject_agent_experiment_frozen_scorecard_mutation();
