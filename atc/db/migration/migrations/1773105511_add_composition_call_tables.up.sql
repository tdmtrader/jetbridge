-- The composition layer's own tables. They travel in core's migration stream
-- because there is exactly one schema_migrations table and one linear version
-- history; a second migrator over its own fs.FS would write its bookkeeping to
-- the same hardcoded tables and the two would interleave. Travelling here is
-- not DDL *on* a core table, which is what the boundary rule forbids.

-- One row per composition call: one node of one build asking for one child run.
--
-- The unique constraint is the mechanism, not decoration. It -- and nothing
-- else in this schema -- is what makes a second admission of the same call
-- impossible: there is deliberately no suppressed, skipped, cancelled or
-- status column that could admit or suppress a run on its own, so there is
-- exactly one dedup path and it is this index. Two callers racing both issue
-- the same INSERT ... ON CONFLICT DO NOTHING; the loser blocks on this index
-- until the winner commits and then re-reads the winner's iteration row.
--
-- It carries no run id. The admitted run belongs to the iteration, because a
-- run id on this row would have to move with each iteration -- and a column
-- that accumulates iteration state makes the Nth iteration unaddressable.
-- When the run contract's server-scoped key lands, input_digest stays a
-- recorded value and this table becomes a join rather than a dedup mechanism.
CREATE TABLE composition_calls (
    id bigserial PRIMARY KEY,
    build_id integer NOT NULL REFERENCES builds(id) ON DELETE CASCADE,
    plan_id text NOT NULL,
    input_digest text NOT NULL,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    UNIQUE (build_id, plan_id)
);

-- One row per iteration of a call, and the only place a child run id is
-- recorded. Keyed (call_id, ordinal) so the Nth iteration is addressable.
--
-- The foreign key to pipeline_runs is what makes "no iteration names a run
-- that does not exist" a schema property rather than a hope.
CREATE TABLE composition_iterations (
    call_id bigint NOT NULL REFERENCES composition_calls(id) ON DELETE CASCADE,
    ordinal integer NOT NULL CHECK (ordinal > 0),
    run_id bigint NOT NULL REFERENCES pipeline_runs(id) ON DELETE CASCADE,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (call_id, ordinal)
);

-- Every foreign key above is ON DELETE CASCADE, never RESTRICT: a side table
-- must not be able to veto the deletion of a core row. The stated consequence
-- is that deleting a build takes its call and that call's iterations with it
-- while the child pipeline_runs row survives -- so "exactly one iteration
-- names this run" is an invariant over a run's admission, not over its whole
-- lifetime. That is the correct trade: the alternative is RESTRICT, and a side
-- table that can block a core delete is the coupling this boundary exists to
-- prevent.
