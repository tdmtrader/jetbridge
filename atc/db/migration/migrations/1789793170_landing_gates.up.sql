-- A landing queue's gates (ADR-0011). Once a candidate's compose Run has
-- succeeded, the queue admits one Run per gate it declares and records each
-- here, keyed by the candidate (its compose Run) and the gate's place in the
-- queue's list. The template and wait are copied from the config at
-- admission, so a config change never alters a candidate already gating.
--
-- verdict is pending | passed | failed | no_verdict. A pending gate with no
-- run_id is due another Run. attempts counts every Run admitted for the gate
-- and keys the next admission; no_verdicts counts Runs in a row that errored,
-- aborted or overran wait_seconds, and reaching two pauses the queue.
CREATE TABLE landing_gates (
    id bigserial PRIMARY KEY,
    compose_run_id bigint NOT NULL REFERENCES landing_intents(compose_run_id) ON DELETE CASCADE,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    template text NOT NULL,
    wait_seconds integer NOT NULL CHECK (wait_seconds > 0),
    run_id bigint UNIQUE REFERENCES pipeline_runs(id) ON DELETE SET NULL,
    admitted_at timestamp with time zone,
    attempts integer NOT NULL DEFAULT 0,
    no_verdicts integer NOT NULL DEFAULT 0,
    verdict text NOT NULL DEFAULT 'pending' CHECK (verdict IN ('pending','passed','failed','no_verdict')),
    last_error text NOT NULL DEFAULT '',
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    UNIQUE (compose_run_id, ordinal)
);

-- A candidate between its compose and its land is gating.
ALTER TABLE landing_intents DROP CONSTRAINT landing_intents_state_check;
ALTER TABLE landing_intents ADD CONSTRAINT landing_intents_state_check
    CHECK (state IN ('composing','gating','landing','done'));

-- A queue pauses on a gate that gave no verdict twice in a row, and stays
-- paused until fly resume-landing-queue clears it. A paused queue admits and
-- settles nothing.
ALTER TABLE landing_queues
    ADD COLUMN paused boolean NOT NULL DEFAULT false,
    ADD COLUMN pause_reason text NOT NULL DEFAULT '';
