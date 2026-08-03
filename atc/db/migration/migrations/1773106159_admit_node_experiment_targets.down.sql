-- Node parameters are the independent variable of a node A/B. Dropping the
-- column under a live node experiment would not degrade it, it would make the
-- surviving rows claim two variants were identical. Refuse instead, matching
-- 1773106149's rule for node definitions and runs.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_experiment_variants WHERE target_kind = 'node')
       OR EXISTS (SELECT 1 FROM agent_experiments WHERE evaluator_target_kind = 'node') THEN
        RAISE EXCEPTION 'cannot downgrade node experiment targets while node target rows exist';
    END IF;
END $$;

ALTER TABLE agent_experiments
    DROP CONSTRAINT agent_experiments_evaluator_node_parameters_check,
    DROP CONSTRAINT agent_experiments_evaluator_function_selection_check,
    DROP CONSTRAINT agent_experiments_evaluator_target_kind_check,
    DROP COLUMN evaluator_node_parameters,
    ADD CONSTRAINT agent_experiments_evaluator_target_kind_check
        CHECK (evaluator_target_kind IN ('workflow', 'function')),
    ADD CONSTRAINT agent_experiments_check
        CHECK (
            (evaluator_target_kind = 'workflow' AND evaluator_function_id IS NULL)
            OR
            (evaluator_target_kind = 'function' AND evaluator_function_id IS NOT NULL
                AND btrim(evaluator_function_id) = evaluator_function_id
                AND evaluator_function_id <> '')
        );

ALTER TABLE agent_experiment_variants
    DROP CONSTRAINT agent_experiment_variants_node_parameters_check,
    DROP CONSTRAINT agent_experiment_variants_function_selection_check,
    DROP CONSTRAINT agent_experiment_variants_target_kind_check,
    DROP COLUMN node_parameters,
    ADD CONSTRAINT agent_experiment_variants_target_kind_check
        CHECK (target_kind IN ('workflow', 'function')),
    ADD CONSTRAINT agent_experiment_variants_check
        CHECK (
            (target_kind = 'workflow' AND function_id IS NULL)
            OR
            (target_kind = 'function' AND function_id IS NOT NULL
                AND btrim(function_id) = function_id AND function_id <> '')
        );
