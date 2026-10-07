-- capture_is_one_row, part one: the new durable shape, beside the old one.
--
-- hangar_captures is the ONLY durable capture state. One row per (execution,
-- output); every step of the capture sequence is a compare-and-set on its
-- state. The node-local half is one marker file per step directory, written
-- by the daemon; nothing here mirrors it.
--
-- Admission still consults the activation epoch in this track. hangar_enabled
-- is created and seeded from it so that the switch to it (no_activation) finds
-- an in-service deployment in service: enabled exactly when some epoch's
-- output facet is enabled today.
CREATE TABLE hangar_captures (
    execution_id        uuid NOT NULL,
    output_name         text NOT NULL CHECK (output_name ~ '^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$'),
    state               text NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'publishing', 'published', 'discarded', 'failed')),
    node                text NOT NULL CHECK (node <> ''),
    node_uid            text NOT NULL CHECK (node_uid <> ''),
    pod_uid             text CHECK (pod_uid <> ''),
    scope               text CHECK (scope ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    digest              text CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    generation          bigint CHECK (generation > 0),
    capture_deadline_at timestamptz NOT NULL,
    released_at         timestamptz,
    error               text CHECK (error <> '' AND octet_length(error) <= 1024),
    created_at          timestamptz NOT NULL DEFAULT now(),
    finished_at         timestamptz,
    PRIMARY KEY (execution_id, output_name),
    -- The digest is written WITH the move to publishing, before any object can
    -- exist; the generation only with the move to published.
    CHECK ((scope IS NULL) = (digest IS NULL)),
    CHECK (state NOT IN ('publishing', 'published') OR digest IS NOT NULL),
    CHECK (state <> 'pending' OR digest IS NULL),
    CHECK ((state = 'published') = (generation IS NOT NULL)),
    CHECK ((state IN ('published', 'discarded', 'failed')) = (finished_at IS NOT NULL)),
    CHECK (state NOT IN ('discarded', 'failed') OR error IS NOT NULL),
    CHECK (released_at IS NULL OR state IN ('published', 'discarded', 'failed'))
);

-- The recovery, deadline and release passes' work queries.
CREATE INDEX hangar_captures_open_idx ON hangar_captures (capture_deadline_at)
    WHERE state IN ('pending', 'publishing');
CREATE INDEX hangar_captures_unreleased_idx ON hangar_captures (finished_at)
    WHERE released_at IS NULL AND state IN ('published', 'discarded', 'failed');
-- Reclaim admission's exclusion of a tree some capture is about to publish.
CREATE INDEX hangar_captures_publishing_ref_idx ON hangar_captures (scope, digest)
    WHERE state IN ('pending', 'publishing');

-- Every transition is one of five, identity never moves, a terminal state is
-- terminal, and release is a one-way stamp on a terminal row.
CREATE FUNCTION hangar_capture_transition() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF run_team_purge_active() THEN
            RETURN OLD;
        END IF;
        RAISE EXCEPTION 'hangar: a capture row is deleted only by its team''s purge'
            USING ERRCODE = 'JB001';
    END IF;
    IF (NEW.execution_id, NEW.output_name, NEW.node, NEW.node_uid, NEW.capture_deadline_at, NEW.created_at)
       IS DISTINCT FROM (OLD.execution_id, OLD.output_name, OLD.node, OLD.node_uid, OLD.capture_deadline_at, OLD.created_at) THEN
        RAISE EXCEPTION 'hangar: capture %/% identity is immutable', OLD.execution_id, OLD.output_name
            USING ERRCODE = 'JB001';
    END IF;
    IF OLD.pod_uid IS NOT NULL AND NEW.pod_uid IS DISTINCT FROM OLD.pod_uid THEN
        RAISE EXCEPTION 'hangar: capture %/% is bound to pod %', OLD.execution_id, OLD.output_name, OLD.pod_uid
            USING ERRCODE = 'JB001';
    END IF;
    IF OLD.digest IS NOT NULL AND (NEW.scope, NEW.digest) IS DISTINCT FROM (OLD.scope, OLD.digest) THEN
        RAISE EXCEPTION 'hangar: capture %/% resolved to %/%; a resolved digest never moves',
            OLD.execution_id, OLD.output_name, OLD.scope, OLD.digest
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.state <> OLD.state AND NOT (
        (OLD.state = 'pending' AND NEW.state IN ('publishing', 'discarded', 'failed')) OR
        (OLD.state = 'publishing' AND NEW.state IN ('published', 'failed'))) THEN
        RAISE EXCEPTION 'hangar: capture %/% cannot move from % to %',
            OLD.execution_id, OLD.output_name, OLD.state, NEW.state
            USING ERRCODE = 'JB001';
    END IF;
    IF OLD.state IN ('published', 'discarded', 'failed') AND
       (NEW.generation, NEW.error, NEW.finished_at) IS DISTINCT FROM (OLD.generation, OLD.error, OLD.finished_at) THEN
        RAISE EXCEPTION 'hangar: capture %/% is % and terminal', OLD.execution_id, OLD.output_name, OLD.state
            USING ERRCODE = 'JB001';
    END IF;
    IF OLD.released_at IS NOT NULL AND NEW.released_at IS DISTINCT FROM OLD.released_at THEN
        RAISE EXCEPTION 'hangar: capture %/% was released at %; release is one-way',
            OLD.execution_id, OLD.output_name, OLD.released_at
            USING ERRCODE = 'JB001';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER hangar_capture_transition_guard
    BEFORE UPDATE OR DELETE ON hangar_captures
    FOR EACH ROW EXECUTE FUNCTION hangar_capture_transition();

-- In service: one row, taken FOR SHARE by admission once the activation epoch
-- is gone. Seeded from today's authority so the switch changes nothing.
CREATE TABLE hangar_enabled (
    singleton  boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    enabled    boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO hangar_enabled (enabled)
    SELECT EXISTS (SELECT 1 FROM hangar_output_activation_epochs WHERE output_state = 'enabled');

-- The integrity findings that gate admission: the policy violations of before,
-- without an activation epoch. Rows move here when the old table is dropped.
CREATE TABLE hangar_integrity_findings (
    id          bigserial PRIMARY KEY,
    violation   text NOT NULL CHECK (violation IN ('lifecycle_delete_rule', 'evidence_stale',
        'evidence_unreadable', 'excess_role', 'insufficient_role', 'wrong_principal',
        'shared_bucket', 'mixed_cohort', 'unrecognised_role', 'out_of_band_absence',
        'runtime_principal_denied')),
    subject     text NOT NULL CHECK (subject <> ''),
    detail      text NOT NULL DEFAULT '' CHECK (octet_length(detail) <= 1024),
    observed_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz
);
CREATE UNIQUE INDEX hangar_integrity_findings_one_open_idx
    ON hangar_integrity_findings (violation, subject) WHERE resolved_at IS NULL;

CREATE FUNCTION hangar_integrity_finding_resolution() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.resolved_at IS NOT NULL AND NEW.resolved_at IS DISTINCT FROM OLD.resolved_at THEN
        RAISE EXCEPTION 'hangar: integrity finding % was resolved at % and cannot be reopened or re-dated',
            OLD.id, OLD.resolved_at
            USING ERRCODE = 'JB002';
    END IF;
    IF NEW.violation <> OLD.violation OR NEW.subject <> OLD.subject THEN
        RAISE EXCEPTION 'hangar: integrity finding % is immutable in what it is about', OLD.id
            USING ERRCODE = 'JB002';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER hangar_integrity_finding_resolution_guard
    BEFORE UPDATE ON hangar_integrity_findings
    FOR EACH ROW EXECUTE FUNCTION hangar_integrity_finding_resolution();

-- The Run's side of a capture: which named result of which task in which
-- build a capture row produces. Hangar's row names no Run; this one does.
CREATE TABLE pipeline_run_captures (
    run_id       bigint NOT NULL REFERENCES pipeline_runs(id) ON DELETE RESTRICT,
    build_id     integer NOT NULL,
    task_id      uuid NOT NULL REFERENCES pipeline_template_task_identities(task_id),
    result_name  text NOT NULL,
    task_name    text NOT NULL,
    execution_id uuid NOT NULL,
    output_name  text NOT NULL,
    PRIMARY KEY (build_id, task_id),
    UNIQUE (execution_id, output_name),
    FOREIGN KEY (execution_id, output_name) REFERENCES hangar_captures (execution_id, output_name)
        ON DELETE RESTRICT
);
CREATE INDEX pipeline_run_captures_run ON pipeline_run_captures (run_id, build_id);

CREATE TRIGGER run_capture_immutable BEFORE DELETE OR UPDATE ON pipeline_run_captures
    FOR EACH ROW EXECUTE FUNCTION immutable_run_output_evidence();

-- The execution that captures names the capture it feeds. The column joins the
-- old handoff link here and replaces it when that is dropped.
ALTER TABLE pipeline_run_executions ADD COLUMN capture_output text;
ALTER TABLE pipeline_run_executions ADD CONSTRAINT pipeline_run_executions_capture_fkey
    FOREIGN KEY (execution_id, capture_output) REFERENCES hangar_captures (execution_id, output_name);

-- Reclaim admission is refused for any tree a capture is about to publish:
-- a publishing row's (scope, digest) joins the unresolved reservations.
CREATE OR REPLACE FUNCTION hangar_check_reclaim_exclusion() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    target    bigint;
    lifecycle hangar_exact_lifecycles%ROWTYPE;
    reclaims  integer;
    claims    integer;
    leases    integer;
    pending   integer;
BEGIN
    target := NEW.lifecycle_id;
    SELECT * INTO lifecycle FROM hangar_exact_lifecycles WHERE id = target;

    SELECT count(*) INTO reclaims FROM hangar_reclaim_jobs
        WHERE lifecycle_id = target AND finalized_at IS NULL;
    SELECT count(*) INTO claims FROM hangar_claims
        WHERE lifecycle_id = target AND released_at IS NULL;
    SELECT count(*) INTO leases FROM hangar_read_leases
        WHERE lifecycle_id = target AND released_at IS NULL AND expires_at > now();
    SELECT count(*) INTO pending FROM hangar_logical_reservations
        WHERE scope = lifecycle.scope AND digest = lifecycle.digest
          AND state = 'unresolved_generation';
    SELECT pending + count(*) INTO pending FROM hangar_captures
        WHERE scope = lifecycle.scope AND digest = lifecycle.digest
          AND state IN ('pending', 'publishing');
    SELECT pending + count(*) INTO pending FROM hangar_input_publications
        WHERE scope = lifecycle.scope AND digest = lifecycle.digest
          AND lifecycle_id IS NULL AND expires_at > clock_timestamp();

    IF reclaims > 0 AND (claims > 0 OR leases > 0 OR pending > 0) THEN
        RAISE EXCEPTION 'hangar: tree ref %/%/% has an admitted reclaim beside % active claim(s), % active read lease(s) and % unresolved reservation(s); reclaim admission is refused while any of them exists',
            lifecycle.scope, lifecycle.digest, lifecycle.generation, claims, leases, pending
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NULL;
END $$;
