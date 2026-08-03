-- experiment.TargetNode grades a reusable atomic node, but the experiment
-- schema admitted only 'workflow' and 'function'. Two separate gaps kept a
-- node experiment from ever reaching the database:
--
--   1. target_kind / evaluator_target_kind rejected 'node' outright, and the
--      paired function-selection CHECK required function_id to be NULL for a
--      workflow target and NOT NULL for a function target, so a 'node' row
--      satisfied neither branch even if the kind CHECK had allowed it.
--   2. there was no column for the node's parameters. A node A/B legitimately
--      varies a parameter -- a turn cap, a model, a severity floor -- while
--      holding the node definition fixed, so the parameter set IS the thing
--      being compared. Persisting a node variant without it would drop the
--      independent variable on write and silently grade two identical cells.
--
-- Parameters live beside the target identity rather than on the cell for the
-- same reason they live on experiment.Target: they are part of the frozen
-- identity a bound run must match, not per-repetition state.
ALTER TABLE agent_experiment_variants
    ADD COLUMN node_parameters JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE agent_experiments
    ADD COLUMN evaluator_node_parameters JSONB NOT NULL DEFAULT '{}'::jsonb;

-- The kind and function-selection CHECKs were unnamed table/column
-- constraints in 1773106111, so PostgreSQL auto-named them. Replacing them
-- with explicit names makes every later change to them nameable.
ALTER TABLE agent_experiment_variants
    DROP CONSTRAINT agent_experiment_variants_target_kind_check,
    DROP CONSTRAINT agent_experiment_variants_check,
    ADD CONSTRAINT agent_experiment_variants_target_kind_check
        CHECK (target_kind IN ('workflow', 'function', 'node')),
    -- A node has no functions to select between: its whole executable surface
    -- is one leaf step. Only a function target may name one.
    ADD CONSTRAINT agent_experiment_variants_function_selection_check
        CHECK (
            (target_kind IN ('workflow', 'node') AND function_id IS NULL)
            OR
            (target_kind = 'function' AND function_id IS NOT NULL
                AND btrim(function_id) = function_id AND function_id <> '')
        ),
    -- Values are a string map on the way in and must stay one: an integer or
    -- object here would round-trip into agent/experiment as a decode failure
    -- on read, long after the write that caused it. A non-node target has no
    -- parameter surface at all, so anything other than the empty object is a
    -- caller confusing two kinds.
    ADD CONSTRAINT agent_experiment_variants_node_parameters_check
        CHECK (
            jsonb_typeof(node_parameters) = 'object'
            AND NOT jsonb_path_exists(node_parameters, '$.* ? (@.type() <> "string")')
            AND (target_kind = 'node' OR node_parameters = '{}'::jsonb)
        );

ALTER TABLE agent_experiments
    DROP CONSTRAINT agent_experiments_evaluator_target_kind_check,
    DROP CONSTRAINT agent_experiments_check,
    ADD CONSTRAINT agent_experiments_evaluator_target_kind_check
        CHECK (evaluator_target_kind IN ('workflow', 'function', 'node')),
    ADD CONSTRAINT agent_experiments_evaluator_function_selection_check
        CHECK (
            (evaluator_target_kind IN ('workflow', 'node') AND evaluator_function_id IS NULL)
            OR
            (evaluator_target_kind = 'function' AND evaluator_function_id IS NOT NULL
                AND btrim(evaluator_function_id) = evaluator_function_id
                AND evaluator_function_id <> '')
        ),
    ADD CONSTRAINT agent_experiments_evaluator_node_parameters_check
        CHECK (
            jsonb_typeof(evaluator_node_parameters) = 'object'
            AND NOT jsonb_path_exists(evaluator_node_parameters, '$.* ? (@.type() <> "string")')
            AND (evaluator_target_kind = 'node' OR evaluator_node_parameters = '{}'::jsonb)
        );
