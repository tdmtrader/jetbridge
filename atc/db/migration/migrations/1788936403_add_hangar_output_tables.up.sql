-- The durable half of the Hangar output plane: capture handoffs, publication
-- reservations, exact-generation lifecycles, claims, read leases, inventory,
-- reclamation, policy health and the activation epoch that gates all of it.
--
-- The upgrade creates HELD state only. There is no default-ready epoch and no
-- seed row of any kind: every table below either references
-- hangar_output_activation_epochs or hangs off something that does, so on a
-- freshly migrated database nothing can be admitted until an operator runs the
-- activation command. A migration that inserted an enabled epoch would be a
-- migration that turned the feature on, and Req 57 says the epoch attests
-- migrations, keys, workers, bucket, policy, namespaces and principals -- none
-- of which a DDL script can observe.
--
-- Deadlines and lease terms are expressed as differences between two columns
-- rather than against now(). A CHECK constraint may only call IMMUTABLE
-- functions, and timestamptz subtraction is one while `timestamptz + interval`
-- is not; the difference form is also the honest one, because these are terms,
-- not wall-clock instants.

-- Every refusal below names its class on the RAISE itself, and the classes are
-- JB001 conflict, JB002 at-risk policy, JB003 stale fence or epoch, JB004
-- incomplete. One rule decides between the first and the last where they could
-- both be argued for: **a rewrite of an immutable identity is JB001**. It is a
-- conflict -- two statements about one fact, the first of which already won --
-- and never JB004, which is reserved for a structurally valid value whose parts
-- contradict each other. Review finding R2-4: three of these refusals said
-- JB004 and three said JB001, so the same refusal reached a caller as
-- ErrIncomplete or ErrConflict depending on which table it came from.

-- The six states each activation facet moves through, as an ordinal, so that
-- "may not move backwards" is expressible in a CHECK. There is deliberately no
-- `retired` state: `disabled` is terminal, and rotation creates a new epoch row
-- rather than reviving an old one (decision F1).
CREATE FUNCTION hangar_output_facet_ordinal(facet_state text) RETURNS integer
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE facet_state
        WHEN 'initial'   THEN 0
        WHEN 'attesting' THEN 1
        WHEN 'attested'  THEN 2
        WHEN 'enabled'   THEN 3
        WHEN 'draining'  THEN 4
        WHEN 'disabled'  THEN 5
    END;
$$;

-- The single source of activation truth. One row per epoch; `revision` is what
-- every transition CASes on.
CREATE TABLE hangar_output_activation_epochs (
    epoch_id                bigint PRIMARY KEY CHECK (epoch_id > 0),
    base_state              text NOT NULL DEFAULT 'initial'
        CHECK (hangar_output_facet_ordinal(base_state) IS NOT NULL),
    output_state            text NOT NULL DEFAULT 'initial'
        CHECK (hangar_output_facet_ordinal(output_state) IS NOT NULL),
    base_attestation        jsonb,
    output_attestation      jsonb,
    receipt_public_key_id   text,
    receipt_key_valid_from  timestamp with time zone,
    receipt_key_valid_until timestamp with time zone,
    materialization_key_id  text,
    bucket_fingerprint      text,
    derived_namespace       text,
    protocol_version        text,
    ledger_version          text,
    cohort_digest           text,
    created_at              timestamp with time zone NOT NULL DEFAULT now(),
    updated_at              timestamp with time zone NOT NULL DEFAULT now(),
    revision                bigint NOT NULL DEFAULT 1 CHECK (revision > 0),

    -- Output can never be ready without base control. Stated over the states
    -- that mean "in service" rather than over an ordinal comparison with
    -- 'initial': `disabled` is terminal and out of service, so requiring base
    -- readiness for it would make `drain --facet=all` -- which takes output to
    -- disabled and only then drains base -- unreachable.
    CONSTRAINT hangar_output_epoch_needs_base CHECK (
        output_state IN ('initial', 'disabled')
        OR base_state IN ('attested', 'enabled')
    ),
    -- Attestation evidence is written in the statement that moves to
    -- `attested`, so an enabled row cannot exist without the bundle that
    -- justified it.
    CONSTRAINT hangar_output_epoch_base_evidence CHECK (
        base_state IN ('initial', 'attesting') OR base_attestation IS NOT NULL
    ),
    CONSTRAINT hangar_output_epoch_output_evidence CHECK (
        output_state IN ('initial', 'attesting') OR output_attestation IS NOT NULL
    ),
    -- A read grant must not be signable by anything that can mint a
    -- publication receipt, so the two key ids are present and distinct before
    -- output can be enabled.
    CONSTRAINT hangar_output_epoch_enabled_output_identity CHECK (
        output_state <> 'enabled' OR (
            receipt_public_key_id IS NOT NULL
            AND materialization_key_id IS NOT NULL
            AND receipt_public_key_id <> materialization_key_id
            AND bucket_fingerprint IS NOT NULL
            AND derived_namespace IS NOT NULL
            AND receipt_key_valid_from IS NOT NULL
            AND receipt_key_valid_until IS NOT NULL
            AND receipt_key_valid_until > receipt_key_valid_from
        )
    )
);

-- At most one `enabled` row PER FACET, and nothing said about any other state
-- (decision F1). Rotation begins the next epoch in `initial`, attests it, CASes
-- it to `enabled`, and only then moves the outgoing row enabled -> draining ->
-- disabled: for that moment a `draining` row and an `enabled` row coexist for
-- the same facet, which is exactly what gives rotation no emission gap. An
-- index phrased "at most one non-terminal row" would forbid the overlap and
-- force a gap on every rotation.
CREATE UNIQUE INDEX hangar_output_one_enabled_base_epoch
    ON hangar_output_activation_epochs ((true)) WHERE base_state = 'enabled';
CREATE UNIQUE INDEX hangar_output_one_enabled_output_epoch
    ON hangar_output_activation_epochs ((true)) WHERE output_state = 'enabled';

-- A facet never moves backwards and `disabled` is terminal, so `attested ->
-- attesting` requires a new epoch row rather than a rewrite of this one. Every
-- write bumps the revision, which is what makes the CAS predicate meaningful.
CREATE FUNCTION hangar_output_epoch_transition() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF hangar_output_facet_ordinal(NEW.base_state) < hangar_output_facet_ordinal(OLD.base_state) THEN
        RAISE EXCEPTION 'hangar: activation epoch % moved base_state backwards, % -> %; a new epoch row is the only way back',
            OLD.epoch_id, OLD.base_state, NEW.base_state
            USING ERRCODE = 'JB001';
    END IF;
    IF hangar_output_facet_ordinal(NEW.output_state) < hangar_output_facet_ordinal(OLD.output_state) THEN
        RAISE EXCEPTION 'hangar: activation epoch % moved output_state backwards, % -> %; a new epoch row is the only way back',
            OLD.epoch_id, OLD.output_state, NEW.output_state
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.epoch_id <> OLD.epoch_id OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'hangar: activation epoch identity is immutable'
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.revision <= OLD.revision THEN
        RAISE EXCEPTION 'hangar: activation epoch % was written without advancing its revision (% -> %)',
            OLD.epoch_id, OLD.revision, NEW.revision
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_output_epoch_transition_guard
    BEFORE UPDATE ON hangar_output_activation_epochs
    FOR EACH ROW EXECUTE FUNCTION hangar_output_epoch_transition();

-- The pre-start, non-authorizing predeclaration.
--
-- Read the column list as the contract: there is no producer checkpoint, no
-- success fact, no capture owner or lease, no scope or digest, no seal, no
-- receipt, no publication and no claim here, and no API can put one here
-- because there is nowhere to put it. `hold_acknowledged_at` is the one column
-- written after the row is created -- the daemon's pre-start acknowledgement of
-- the provisional hold -- and the trigger below permits exactly that one
-- transition and nothing else.
CREATE TABLE hangar_handoff_predeclarations (
    handoff_id           uuid PRIMARY KEY,
    source_lease_id      uuid NOT NULL UNIQUE,
    execution_id         uuid NOT NULL UNIQUE,
    execution_fence      bigint NOT NULL CHECK (execution_fence > 0),
    output_name          text NOT NULL
        CHECK (output_name ~ '^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$'),
    activation_epoch     bigint NOT NULL
        REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    capture_deadline_at  timestamp with time zone NOT NULL,
    created_at           timestamp with time zone NOT NULL DEFAULT now(),
    hold_acknowledged_at timestamp with time zone,

    -- The daemon's answer to `reserve-incarnation`, recorded because a
    -- coordinator that has to RELEASE a source needs to know which node holds
    -- it and which incarnation it is, and after a crash there is nowhere else
    -- to learn either: the reservation is taken before the Pod exists, the
    -- ledger that knows it is node-local, and the control plane cannot ask
    -- every node in the cluster whether it happens to be holding something.
    --
    -- The locator is OPAQUE to Hangar. It is whatever a deployment's source
    -- dialer needs to reach that one node, stored and handed back unchanged;
    -- nothing here parses it, exactly as nothing here parses an object key.
    reserved_locator     text CHECK (reserved_locator <> ''),
    reserved_incarnation jsonb,
    reserved_directory   text CHECK (reserved_directory <> ''),
    reserved_at          timestamp with time zone,

    -- A hold binds to an incarnation this node issued FIRST. A hold over no
    -- reservation is the Phase 4 seam reopening -- a producer writing into a
    -- directory nothing protects -- and the schema is where that stays shut.
    CONSTRAINT hangar_predeclaration_reservation_is_whole CHECK (
        (reserved_at IS NULL) = (reserved_locator IS NULL)
        AND (reserved_at IS NULL) = (reserved_incarnation IS NULL)
        AND (reserved_at IS NULL) = (reserved_directory IS NULL)
    ),
    CONSTRAINT hangar_predeclaration_hold_needs_a_reservation CHECK (
        hold_acknowledged_at IS NULL OR reserved_at IS NOT NULL
    ),

    -- 24 hours by default, configurable from 1 hour through 7 days (Req 11).
    CONSTRAINT hangar_predeclaration_deadline_bounded CHECK (
        capture_deadline_at - created_at BETWEEN interval '1 hour' AND interval '7 days'
    )
);

CREATE FUNCTION hangar_predeclaration_immutable() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.handoff_id <> OLD.handoff_id
        OR NEW.source_lease_id <> OLD.source_lease_id
        OR NEW.execution_id <> OLD.execution_id
        OR NEW.execution_fence <> OLD.execution_fence
        OR NEW.output_name <> OLD.output_name
        OR NEW.activation_epoch <> OLD.activation_epoch
        OR NEW.capture_deadline_at <> OLD.capture_deadline_at
        OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'hangar: the predeclaration for handoff % is immutable; a new build uses new identities',
            OLD.handoff_id
            USING ERRCODE = 'JB001';
    END IF;
    IF OLD.hold_acknowledged_at IS NOT NULL AND NEW.hold_acknowledged_at IS DISTINCT FROM OLD.hold_acknowledged_at THEN
        RAISE EXCEPTION 'hangar: the source hold for handoff % is already acknowledged; it is acknowledged once',
            OLD.handoff_id
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.hold_acknowledged_at IS NULL AND OLD.hold_acknowledged_at IS NOT NULL THEN
        RAISE EXCEPTION 'hangar: the source hold for handoff % cannot be un-acknowledged', OLD.handoff_id
            USING ERRCODE = 'JB001';
    END IF;
    IF OLD.reserved_at IS NOT NULL
        AND (NEW.reserved_at IS DISTINCT FROM OLD.reserved_at
             OR NEW.reserved_locator IS DISTINCT FROM OLD.reserved_locator
             OR NEW.reserved_incarnation IS DISTINCT FROM OLD.reserved_incarnation
             OR NEW.reserved_directory IS DISTINCT FROM OLD.reserved_directory) THEN
        RAISE EXCEPTION 'hangar: handoff % already reserved a source incarnation; a reservation is issued once and a second location is a second capture',
            OLD.handoff_id
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_predeclaration_immutability_guard
    BEFORE UPDATE ON hangar_handoff_predeclarations
    FOR EACH ROW EXECUTE FUNCTION hangar_predeclaration_immutable();

-- The one-row arbiter. Exactly one row per handoff, forever: the primary key is
-- what makes "exactly one" true rather than hoped for, and the trigger below
-- makes it immutable so a decided branch can never be re-decided.
--
-- Note what is absent. There is no seal, publication, receipt, object or claim
-- column here. Winning the arbiter as `capture` records a checkpoint and
-- creates an unresolved reservation; it seals, publishes and binds nothing, and
-- no trigger in this schema treats the arbiter row as authority for any of them.
CREATE TABLE hangar_handoff_dispositions (
    handoff_id  uuid PRIMARY KEY
        REFERENCES hangar_handoff_predeclarations (handoff_id) ON DELETE RESTRICT,
    disposition text NOT NULL
        CHECK (disposition IN ('capture', 'no_capture', 'pre_reservation_cancel')),
    decided_at  timestamp with time zone NOT NULL DEFAULT now()
);

CREATE FUNCTION hangar_disposition_immutable() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'hangar: handoff % is already dispositioned as %; the three branches exclude one another permanently',
        OLD.handoff_id, OLD.disposition
            USING ERRCODE = 'JB001';
END $$;

CREATE TRIGGER hangar_disposition_immutability_guard
    BEFORE UPDATE OR DELETE ON hangar_handoff_dispositions
    FOR EACH ROW EXECUTE FUNCTION hangar_disposition_immutable();

-- Stage 2. The successful-finish-only branch.
--
-- `finish_successful` is a column that can only be true. That is the structural
-- form of Req 5: this slice captures only after the producer's main command
-- exits successfully, and a reservation recording anything else is not a
-- narrower case of this table, it is a different table.
CREATE TABLE hangar_capture_reservations (
    reservation_id                  uuid PRIMARY KEY,
    handoff_id                      uuid NOT NULL UNIQUE
        REFERENCES hangar_handoff_dispositions (handoff_id) ON DELETE RESTRICT,
    execution_id                    uuid NOT NULL,
    activation_epoch                bigint NOT NULL
        REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    source_lease_id                 uuid NOT NULL,
    producer_checkpoint_id          text NOT NULL
        CHECK (producer_checkpoint_id <> '' AND octet_length(producer_checkpoint_id) <= 256),
    finish_acknowledgement          jsonb NOT NULL,
    finish_successful               boolean NOT NULL CHECK (finish_successful),
    capture_fence                   bigint NOT NULL CHECK (capture_fence > 0),
    capture_deadline_at             timestamp with time zone NOT NULL,
    -- The Req 17 seal deadline, on the DATABASE clock, stamped once when the
    -- seal begins. It is a column and not a value the coordinator holds
    -- because the process that begins a seal is not necessarily the process
    -- that has to decide whether it was proved in time: a capture crosses an
    -- ATC restart, and a deadline in a dead process's memory is a deadline
    -- that never expires. Without it `seal_unconfirmed` had no producer at
    -- all, and a seal an hour past its deadline registered a receipt.
    --
    -- Nullable, because it is meaningless before a seal begins, and one-way:
    -- every writer coalesces, so a repeat after a lost answer inherits the
    -- deadline the first attempt set rather than granting itself a fresh one.
    seal_deadline_at                timestamp with time zone,
    state                           text NOT NULL DEFAULT 'unresolved'
        CHECK (state IN ('unresolved', 'resolved', 'registered', 'failed', 'cancelled')),
    first_create_attempted_at       timestamp with time zone,
    past_irreversible_publish_point boolean NOT NULL DEFAULT false,
    terminal_failure                text,
    settled_at                      timestamp with time zone,
    -- The source stops being held when the daemon says so, and only then. A
    -- capture that terminally cancels or fails before the irreversible publish
    -- point owes a fenced release of the source (Reqs 5, 11), and until that
    -- release is acknowledged the capture is decided but not settled -- which
    -- is what `settled` already means on the other two branches, and what the
    -- drain predicate needs it to mean here.
    --
    -- One nullable column with a one-way trigger, the same shape as
    -- `hold_acknowledged_at`, plus the pair's other two halves: the intent this
    -- release is for, and the signed statement the daemon made about it.
    --
    -- The intent id is DATABASE-issued rather than caller-supplied. The generic
    -- CancelOrSettle seam takes a handoff and nothing else -- T7 calls it and
    -- must not learn what a release intent is -- so the identity is minted
    -- where the decision is recorded. It is still exact and still fenced: a
    -- release acknowledgement names one intent, and one intent is acknowledged
    -- once.
    release_intent_id               uuid,
    release_acknowledged_at         timestamp with time zone,
    release_acknowledgement         jsonb,
    created_at                      timestamp with time zone NOT NULL DEFAULT now(),

    -- A settlement time means a terminal state, and a terminal state that is
    -- not a registration has to have earned it. Note which direction each of
    -- these runs: a `cancelled` row with no settlement is legal and expected --
    -- it is a capture waiting for its release to be acknowledged.
    CONSTRAINT hangar_reservation_settlement_is_terminal CHECK (
        settled_at IS NULL OR state IN ('registered', 'failed', 'cancelled')
    ),
    CONSTRAINT hangar_reservation_settlement_is_earned CHECK (
        settled_at IS NULL
        OR state = 'registered'
        OR release_acknowledged_at IS NOT NULL
    ),
    -- A release is acknowledged for an INTENT. Without this a row could carry
    -- an acknowledgement naming nothing, and "which release was this" would
    -- have no answer -- which is the whole reason the pair has two halves.
    CONSTRAINT hangar_reservation_release_names_its_intent CHECK (
        release_acknowledged_at IS NULL
        OR (release_intent_id IS NOT NULL AND release_acknowledgement IS NOT NULL)
    ),
    CONSTRAINT hangar_reservation_failure_is_typed CHECK (
        (state = 'failed') = (terminal_failure IS NOT NULL)
    ),
    CONSTRAINT hangar_reservation_publish_point CHECK (
        NOT past_irreversible_publish_point OR first_create_attempted_at IS NOT NULL
    )
);

-- The source is released once. The same one-way rule the predeclaration's hold
-- acknowledgement has, for the same reason: a release that could be withdrawn
-- would make "the source is no longer held" a claim with no expiry date on it.
CREATE FUNCTION hangar_capture_release_is_one_way() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.release_acknowledged_at IS NOT NULL
        AND NEW.release_acknowledged_at IS DISTINCT FROM OLD.release_acknowledged_at THEN
        RAISE EXCEPTION 'hangar: the source release for reservation % is already acknowledged; it is acknowledged once',
            OLD.reservation_id
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_capture_release_one_way_guard
    BEFORE UPDATE ON hangar_capture_reservations
    FOR EACH ROW EXECUTE FUNCTION hangar_capture_release_is_one_way();

-- A decided capture is decided. Req 11 says a capture may *terminally* cancel
-- before sealing or object creation, and `terminally` is the word this trigger
-- makes true: without it `cancelled` was a state the row could leave, so an
-- owner still holding the current capture fence -- the cancellation does not
-- take the lease away -- could resolve a logical identity, publish and register
-- a receipt over a capture the control plane had already given up on, and the
-- source release owed for that cancellation would then be owed for a capture
-- that had published. `registered` and `failed` are terminal for the same
-- reason and were already unreachable-from only by convention.
--
-- Settlement, release acknowledgement and the publish-point columns are not
-- states and may still be stamped on a terminal row: that is precisely how a
-- cancelled capture becomes settled once its release is acknowledged.
CREATE FUNCTION hangar_capture_state_is_terminal() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.state IS DISTINCT FROM OLD.state
        AND OLD.state IN ('registered', 'failed', 'cancelled') THEN
        RAISE EXCEPTION 'hangar: capture reservation % is %, which is terminal; it cannot become %',
            OLD.reservation_id, OLD.state, NEW.state
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_reservation_transition
    BEFORE UPDATE ON hangar_capture_reservations
    FOR EACH ROW EXECUTE FUNCTION hangar_capture_state_is_terminal();

-- Renewable capture ownership on the database clock: a 15-minute floor, and a
-- monotonic fence advanced by takeover (Req 10). A stale owner may not seal,
-- publish, sign, register, finalize or release, which is why the fence lives
-- here rather than being inferred from the lease's expiry.
CREATE TABLE hangar_capture_attempt_leases (
    reservation_id uuid PRIMARY KEY
        REFERENCES hangar_capture_reservations (reservation_id) ON DELETE RESTRICT,
    owner_id       uuid NOT NULL,
    capture_fence  bigint NOT NULL CHECK (capture_fence > 0),
    acquired_at    timestamp with time zone NOT NULL DEFAULT now(),
    renewed_at     timestamp with time zone NOT NULL DEFAULT now(),
    expires_at     timestamp with time zone NOT NULL,

    CONSTRAINT hangar_capture_lease_term CHECK (expires_at - renewed_at >= interval '15 minutes')
);

CREATE FUNCTION hangar_capture_lease_fence() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.capture_fence < OLD.capture_fence THEN
        RAISE EXCEPTION 'hangar: capture fence for reservation % moved backwards, % -> %',
            OLD.reservation_id, OLD.capture_fence, NEW.capture_fence
            USING ERRCODE = 'JB003';
    END IF;
    IF NEW.owner_id <> OLD.owner_id AND NEW.capture_fence <= OLD.capture_fence THEN
        RAISE EXCEPTION 'hangar: capture ownership of reservation % changed without advancing the fence (still %); takeover advances the epoch',
            OLD.reservation_id, OLD.capture_fence
            USING ERRCODE = 'JB003';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_capture_lease_fence_guard
    BEFORE UPDATE ON hangar_capture_attempt_leases
    FOR EACH ROW EXECUTE FUNCTION hangar_capture_lease_fence();

-- The no_capture branch: an ordinary authoritative non-success, or a typed
-- unresolved or lost reconciliation. Two crash-recoverable halves, and nothing
-- here calls them one atomic commit.
CREATE TABLE hangar_no_capture_dispositions (
    handoff_id              uuid PRIMARY KEY
        REFERENCES hangar_handoff_dispositions (handoff_id) ON DELETE RESTRICT,
    execution_id            uuid NOT NULL,
    activation_epoch        bigint NOT NULL
        REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    source_lease_id         uuid NOT NULL,
    reason                  text NOT NULL
        CHECK (reason IN ('authoritative_non_success', 'unresolved', 'lost')),
    release_intent_id       uuid NOT NULL UNIQUE,
    finish_acknowledgement  jsonb,
    intent_recorded_at      timestamp with time zone NOT NULL DEFAULT now(),
    release_acknowledged_at timestamp with time zone,
    release_acknowledgement jsonb,

    -- An authoritative non-success is authoritative because of the witness. A
    -- reconciliation exists precisely because no witness could be obtained, so
    -- one carrying a witness is contradicting itself.
    CONSTRAINT hangar_no_capture_witness_matches_reason CHECK (
        (reason = 'authoritative_non_success') = (finish_acknowledgement IS NOT NULL)
    ),
    CONSTRAINT hangar_no_capture_release_pairs CHECK (
        (release_acknowledged_at IS NULL) = (release_acknowledgement IS NULL)
    )
);

-- What a capture told a watcher, in emission order.
--
-- Requirement 18 says a capture-enabled task exposes the loss of
-- post-completion hijack and the checkpoint, sealing and capture outcomes in
-- existing diagnostics, and nothing else in this plane emits anything a
-- watcher can see -- the daemon has logs and metrics, and those are for an
-- operator. This is the durable half of that: the coordinator appends, and
-- whatever renders a task's diagnostics reads.
--
-- IT IS DURABLE AND NOT A LOG LINE because the process that announces a
-- selection is not the process that announces the outcome: a capture crosses
-- an ATC restart, and an announcement stream held in memory would lose exactly
-- the announcement that explains why hijack stopped working.
--
-- THE PAYLOAD IS CLOSED. A disposition and a reason word, both constrained,
-- and nowhere to put a grant, a key, a path or a consumer reference. A jsonb
-- detail column here would be the column one eventually appears in.
CREATE TABLE hangar_capture_announcements (
    id           bigserial PRIMARY KEY,
    handoff_id   uuid NOT NULL
        REFERENCES hangar_handoff_predeclarations (handoff_id) ON DELETE RESTRICT,
    kind         text NOT NULL
        CHECK (kind IN ('capture-selected', 'capture-seal-started', 'capture-disposition')),
    disposition  text
        CHECK (disposition IS NULL
               OR disposition IN ('capture', 'no_capture', 'pre_reservation_cancel')),
    reason       text NOT NULL CHECK (octet_length(reason) BETWEEN 1 AND 64),
    announced_at timestamp with time zone NOT NULL DEFAULT now(),

    -- One announcement of each kind per handoff. A capture that announced its
    -- selection twice across a restart would be telling a watcher that hijack
    -- went away twice, and the retry that produced it is not news.
    CONSTRAINT hangar_announcement_once UNIQUE (handoff_id, kind)
);

CREATE INDEX hangar_capture_announcements_handoff_idx
    ON hangar_capture_announcements (handoff_id, id);

-- The pre_reservation_cancel branch: cancellation winning before Stage 2.
--
-- `source_reserved` is the fork. With no incarnation reserved anywhere there is
-- nothing on any node to release, so the branch closes with no daemon call and
-- a release intent would be a promise to no one; with one, the intent and its
-- acknowledgement are both required before the branch may finalize.
CREATE TABLE hangar_pre_reservation_cancel_dispositions (
    handoff_id              uuid PRIMARY KEY
        REFERENCES hangar_handoff_dispositions (handoff_id) ON DELETE RESTRICT,
    execution_id            uuid NOT NULL,
    activation_epoch        bigint NOT NULL
        REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    source_lease_id         uuid NOT NULL,
    -- The fork this branch takes, and it is NOT "was a hold acknowledged".
    --
    -- The incarnation is reserved and its directory created before the
    -- producing Pod exists, so a cancellation that beats the control init
    -- still has bytes on a node to release. A column that asked about the hold
    -- would close those cancellations with no daemon call and leave a
    -- directory behind for every capture cancelled before it started.
    source_reserved         boolean NOT NULL,
    release_intent_id       uuid UNIQUE,
    intent_recorded_at      timestamp with time zone NOT NULL DEFAULT now(),
    release_acknowledged_at timestamp with time zone,
    release_acknowledgement jsonb,
    finalized_at            timestamp with time zone,

    CONSTRAINT hangar_cancel_unreserved_carries_no_release CHECK (
        source_reserved
        OR (release_intent_id IS NULL
            AND release_acknowledged_at IS NULL
            AND release_acknowledgement IS NULL)
    ),
    CONSTRAINT hangar_cancel_reserved_records_intent CHECK (
        NOT source_reserved OR release_intent_id IS NOT NULL
    ),
    CONSTRAINT hangar_cancel_release_pairs CHECK (
        (release_acknowledged_at IS NULL) = (release_acknowledgement IS NULL)
    ),
    CONSTRAINT hangar_cancel_reserved_finalizes_on_acknowledgement CHECK (
        finalized_at IS NULL
        OR NOT source_reserved
        OR release_acknowledged_at IS NOT NULL
    )
);

-- The logical reservation: the server-derived scope and digest, bound after
-- canonicalization and before the first object create, so that every
-- possibly-created object has a pre-existing reservation recovery and inventory
-- can correlate without trusting a key some task supplied.
CREATE TABLE hangar_logical_reservations (
    reservation_id uuid PRIMARY KEY
        REFERENCES hangar_capture_reservations (reservation_id) ON DELETE RESTRICT,
    scope          text NOT NULL CHECK (scope ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    digest         text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    logical_bytes  bigint NOT NULL CHECK (logical_bytes >= 0),
    capture_fence  bigint NOT NULL CHECK (capture_fence > 0),
    resolved_at    timestamp with time zone NOT NULL DEFAULT now(),
    state          text NOT NULL DEFAULT 'unresolved_generation'
        CHECK (state IN ('unresolved_generation', 'registered', 'terminal'))
);

CREATE FUNCTION hangar_logical_reservation_immutable() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.scope <> OLD.scope OR NEW.digest <> OLD.digest OR NEW.resolved_at <> OLD.resolved_at THEN
        RAISE EXCEPTION 'hangar: the logical identity of reservation % is immutable; a claim protects an immutable generation and cannot float to replacement content',
            OLD.reservation_id
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_logical_reservation_immutability_guard
    BEFORE UPDATE ON hangar_logical_reservations
    FOR EACH ROW EXECUTE FUNCTION hangar_logical_reservation_immutable();

-- One lifecycle identity per exact (scope, digest, generation). A claim
-- protects that immutable generation and cannot float to a replacement or a
-- mutable alias, which is why the uniqueness is on all three.
CREATE TABLE hangar_exact_lifecycles (
    id               bigserial PRIMARY KEY,
    scope            text NOT NULL CHECK (scope ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    digest           text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    generation       bigint NOT NULL CHECK (generation > 0),
    metageneration   bigint NOT NULL CHECK (metageneration > 0),
    activation_epoch bigint NOT NULL
        REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    marker_version   text NOT NULL CHECK (marker_version = 'hangar-output-v1'),
    origin           text NOT NULL CHECK (origin IN ('registered', 'adopted')),
    state            text NOT NULL
        CHECK (state IN ('registered', 'adopted', 'reclaiming',
                         'reclaimed_confirmed', 'reclaimed_inferred',
                         'conflicted', 'missing_out_of_band')),
    registered_at    timestamp with time zone NOT NULL DEFAULT now(),
    updated_at       timestamp with time zone NOT NULL DEFAULT now(),

    -- When the lifetime audit last STATTED this generation and found it there.
    --
    -- It is a separate column from updated_at because it answers a different
    -- question: updated_at says when this plane last changed its mind about the
    -- row, and this says when the plane last confirmed the object it names still
    -- exists. Without it the absence reconciliation would re-stat the same
    -- oldest rows every pass and never reach the rest of the bucket, which is
    -- the same starvation the inventory cursor exists to prevent one tier down.
    lifetime_audited_at timestamp with time zone,

    UNIQUE (scope, digest, generation)
);

CREATE FUNCTION hangar_lifecycle_transition() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.scope <> OLD.scope OR NEW.digest <> OLD.digest OR NEW.generation <> OLD.generation THEN
        RAISE EXCEPTION 'hangar: the exact ref of lifecycle % is immutable', OLD.id
            USING ERRCODE = 'JB001';
    END IF;
    IF OLD.state IN ('reclaimed_confirmed', 'reclaimed_inferred') AND NEW.state <> OLD.state THEN
        RAISE EXCEPTION 'hangar: lifecycle % is % and terminal; a reclaimed generation never resurrects',
            OLD.id, OLD.state
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.state IN ('reclaimed_confirmed', 'reclaimed_inferred') AND OLD.state <> 'reclaiming' THEN
        RAISE EXCEPTION 'hangar: lifecycle % finalized as % from %; admission marks a generation reclaiming durably before any external delete',
            OLD.id, NEW.state, OLD.state
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.state = 'reclaiming' AND OLD.state NOT IN ('registered', 'adopted') THEN
        RAISE EXCEPTION 'hangar: lifecycle % admitted to reclaim from %; only a registered or adopted generation may be',
            OLD.id, OLD.state
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_lifecycle_transition_guard
    BEFORE UPDATE ON hangar_exact_lifecycles
    FOR EACH ROW EXECUTE FUNCTION hangar_lifecycle_transition();

-- The one-use stat challenge. A signature over old facts proves only that the
-- facts were once true, so admission consumes a nonce that was issued for this
-- exact capture, reservation, key, ref and fence, and expires in five minutes.
CREATE TABLE hangar_receipt_stat_challenges (
    nonce                 text PRIMARY KEY
        CHECK (octet_length(nonce) BETWEEN 16 AND 128),
    handoff_id            uuid NOT NULL
        REFERENCES hangar_handoff_predeclarations (handoff_id) ON DELETE RESTRICT,
    reservation_id        uuid NOT NULL
        REFERENCES hangar_capture_reservations (reservation_id) ON DELETE RESTRICT,
    activation_epoch      bigint NOT NULL
        REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    receipt_public_key_id text NOT NULL CHECK (receipt_public_key_id <> ''),
    scope                 text NOT NULL CHECK (scope ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    digest                text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    generation            bigint NOT NULL CHECK (generation > 0),
    capture_fence         bigint NOT NULL CHECK (capture_fence > 0),
    issued_at             timestamp with time zone NOT NULL DEFAULT now(),
    not_after             timestamp with time zone NOT NULL,
    consumed_at           timestamp with time zone,

    CONSTRAINT hangar_challenge_window CHECK (
        not_after > issued_at AND not_after - issued_at <= interval '5 minutes'
    )
);

CREATE FUNCTION hangar_challenge_one_use() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.consumed_at IS NOT NULL THEN
        RAISE EXCEPTION 'hangar: stat challenge % was already consumed at %; it is one-use, which is what stops a receipt being replayed',
            OLD.nonce, OLD.consumed_at
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.nonce <> OLD.nonce
        OR NEW.reservation_id <> OLD.reservation_id
        OR NEW.scope <> OLD.scope OR NEW.digest <> OLD.digest
        OR NEW.generation <> OLD.generation
        OR NEW.capture_fence <> OLD.capture_fence
        OR NEW.not_after <> OLD.not_after THEN
        RAISE EXCEPTION 'hangar: the facts stat challenge % binds are immutable', OLD.nonce
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_challenge_one_use_guard
    BEFORE UPDATE ON hangar_receipt_stat_challenges
    FOR EACH ROW EXECUTE FUNCTION hangar_challenge_one_use();

-- The registered receipt. Its foreign keys are the ordering: there is no
-- receipt without a logical reservation, no receipt without the exact lifecycle
-- it registered, and no receipt without the epoch whose public key can check it.
CREATE TABLE hangar_output_receipts (
    receipt_id       bigserial PRIMARY KEY,
    reservation_id   uuid NOT NULL UNIQUE
        REFERENCES hangar_logical_reservations (reservation_id) ON DELETE RESTRICT,
    lifecycle_id     bigint NOT NULL
        REFERENCES hangar_exact_lifecycles (id) ON DELETE RESTRICT,
    handoff_id       uuid NOT NULL
        REFERENCES hangar_handoff_predeclarations (handoff_id) ON DELETE RESTRICT,
    activation_epoch bigint NOT NULL
        REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    receipt_key_id   text NOT NULL CHECK (receipt_key_id <> ''),
    challenge_nonce  text NOT NULL UNIQUE
        REFERENCES hangar_receipt_stat_challenges (nonce) ON DELETE RESTRICT,
    marker_version   text NOT NULL CHECK (marker_version = 'hangar-output-v1'),
    algorithm        text NOT NULL CHECK (algorithm = 'ed25519'),
    claims           jsonb NOT NULL,
    signature        text NOT NULL CHECK (signature <> ''),
    registered_at    timestamp with time zone NOT NULL DEFAULT now()
);

-- Claims. The identity is a caller-generated UUID with no domain meaning, and
-- it is the primary key, so acquiring the same id for the same ref is
-- idempotent and reusing it for another ref is a conflict the schema states.
--
-- A released claim is its own tombstone: the row is never deleted, so the
-- identity can never be reused, and the trigger below refuses to un-release it.
-- Tombstone purging is out of scope, which is another way of saying these rows
-- outlive the lifecycle record they protect.
CREATE TABLE hangar_claims (
    claim_id            uuid PRIMARY KEY,
    lifecycle_id        bigint NOT NULL
        REFERENCES hangar_exact_lifecycles (id) ON DELETE RESTRICT,
    activation_epoch    bigint NOT NULL
        REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    consumer_binding_id text NOT NULL
        CHECK (consumer_binding_id <> '' AND octet_length(consumer_binding_id) <= 256),
    acquired_at         timestamp with time zone NOT NULL DEFAULT now(),
    released_at         timestamp with time zone
);

CREATE FUNCTION hangar_claim_tombstone() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'hangar: claim % cannot be deleted; a released identity stays tombstoned for the lifetime of the exact-ref lifecycle record',
            OLD.claim_id
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.lifecycle_id <> OLD.lifecycle_id THEN
        RAISE EXCEPTION 'hangar: claim % was moved to another exact ref; a claim protects one immutable generation',
            OLD.claim_id
            USING ERRCODE = 'JB001';
    END IF;
    -- A tombstone's INSTANT is as immutable as the tombstone. The arm used to
    -- refuse only released_at going back to NULL, so a claim could be released
    -- at one time and then "released" at another -- which is a reactivation
    -- with a cover story, and the timestamp is what a later audit reads.
    -- ReleaseClaim writes coalesce(released_at, now()) and never moves it, so
    -- nothing in production notices the difference; that is the point.
    IF OLD.released_at IS NOT NULL AND NEW.released_at IS DISTINCT FROM OLD.released_at THEN
        RAISE EXCEPTION 'hangar: claim % was released at % and cannot silently reactivate or be re-dated to %',
            OLD.claim_id, OLD.released_at, NEW.released_at
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_claim_tombstone_guard
    BEFORE UPDATE OR DELETE ON hangar_claims
    FOR EACH ROW EXECUTE FUNCTION hangar_claim_tombstone();

-- Read leases. A claim is the consumer's protection and a read lease is the
-- reader's, and the two have different lifetimes: releasing the last claim
-- during a transfer must not delete the bytes out from under a reader.
CREATE TABLE hangar_read_leases (
    read_lease_id    uuid PRIMARY KEY,
    claim_id         uuid NOT NULL REFERENCES hangar_claims (claim_id) ON DELETE RESTRICT,
    lifecycle_id     bigint NOT NULL
        REFERENCES hangar_exact_lifecycles (id) ON DELETE RESTRICT,
    activation_epoch bigint NOT NULL
        REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    -- lease_fence HAS NO WRITER, and that is recorded here rather than left to
    -- be rediscovered. It is inserted as 1 and nothing moves it: the retry of an
    -- ambiguous commit deliberately does not advance it (requirement 37 wants a
    -- byte-identical re-mint, which a moving fence would make impossible), and
    -- the takeover Phase 7 performs works on hangar_capture_attempt_leases's
    -- capture_fence, which is a different column on a different table. The read
    -- grant does not bind it and ValidateReadLease does not compare it -- a
    -- comparison against a value with exactly one possible spelling is a check
    -- that passes for a reason nobody can state. The column stays because a
    -- column with no reader is cheaper than a renumbered migration, and because
    -- the monotonicity arm below is the thing that would have to be right if a
    -- writer ever arrived.
    lease_fence      bigint NOT NULL CHECK (lease_fence > 0),
    granted_at       timestamp with time zone NOT NULL DEFAULT now(),
    renewed_at       timestamp with time zone NOT NULL DEFAULT now(),
    expires_at       timestamp with time zone NOT NULL,
    released_at      timestamp with time zone,

    -- What the grant for this lease binds, stored because the grant is minted
    -- AFTER this transaction commits and may have to be minted again. A nonce
    -- chosen at mint time would make two mints of one lease differ, and the
    -- ambiguous-commit retry would have to create a second lease to be
    -- answerable. The destination is here for the same reason and for one more:
    -- the daemon asks the control plane to confirm the destination it was told,
    -- and a control plane with nothing to compare it against would be
    -- confirming the caller's own claim back to it.
    grant_nonce        text NOT NULL CHECK (length(grant_nonce) BETWEEN 16 AND 128),
    destination_handle text NOT NULL CHECK (destination_handle <> ''),
    destination_volume text NOT NULL CHECK (destination_volume <> ''),

    -- The exact-generation stat this lease was admitted on. Requirement 35
    -- says a grant may be issued only after a stat proves the registered marked
    -- generation is PRESENT; the metageneration is what makes that proof exact,
    -- and observed_at is what stops a stat from an hour ago standing in for one.
    stat_metageneration bigint NOT NULL CHECK (stat_metageneration > 0),
    stat_marker_version text NOT NULL CHECK (stat_marker_version <> ''),
    stat_observed_at    timestamp with time zone NOT NULL,

    -- The term this lease was ADMITTED for, stored because a renewal grants one
    -- of them and there is nowhere else the length could honestly come from.
    --
    -- Not `expires_at - granted_at`: expires_at is what the last renewal moved
    -- and granted_at never moves, so that difference grows by the age of the
    -- lease and every renewal would be longer than the one before it. And not
    -- the daemon's own number either -- the length of a protection is not
    -- something the party being protected gets to choose.
    lease_term_seconds integer NOT NULL
        CHECK (lease_term_seconds >= 900 AND lease_term_seconds <= 86400),

    CONSTRAINT hangar_read_lease_term CHECK (expires_at - granted_at >= interval '15 minutes')
);

CREATE FUNCTION hangar_read_lease_guard() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'hangar: read lease % cannot be deleted; its tombstone is what prevents stale resurrection',
            OLD.read_lease_id
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.lifecycle_id <> OLD.lifecycle_id OR NEW.claim_id <> OLD.claim_id THEN
        RAISE EXCEPTION 'hangar: read lease % was moved to another ref or claim', OLD.read_lease_id
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.grant_nonce <> OLD.grant_nonce
        OR NEW.destination_handle <> OLD.destination_handle
        OR NEW.destination_volume <> OLD.destination_volume THEN
        RAISE EXCEPTION 'hangar: read lease % changed what its grant binds; a re-mint is byte-identical or it is a different lease',
            OLD.read_lease_id
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.lease_term_seconds <> OLD.lease_term_seconds THEN
        RAISE EXCEPTION 'hangar: read lease % changed its admitted term, % -> %; a renewal grants one term and does not choose a new one',
            OLD.read_lease_id, OLD.lease_term_seconds, NEW.lease_term_seconds
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.lease_fence < OLD.lease_fence THEN
        RAISE EXCEPTION 'hangar: read lease % moved its fence backwards, % -> %',
            OLD.read_lease_id, OLD.lease_fence, NEW.lease_fence
            USING ERRCODE = 'JB003';
    END IF;
    IF OLD.released_at IS NOT NULL AND NEW.released_at IS NULL THEN
        RAISE EXCEPTION 'hangar: read lease % was released at % and cannot reactivate',
            OLD.read_lease_id, OLD.released_at
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_read_lease_guard_trigger
    BEFORE UPDATE OR DELETE ON hangar_read_leases
    FOR EACH ROW EXECUTE FUNCTION hangar_read_lease_guard();

-- Exactly one durable inventory cursor and one lease owner per output bucket
-- and activation epoch: parallel cursor owners and caller-created partitions
-- are how an object ends up neither adopted nor collected by either sweep.
--
-- The cursor is a validated lexicographic (key, generation) after-key plus a
-- cycle counter, never a provider continuation token. A token is a promise
-- about a session; this is a fact about the bucket, and only a fact survives a
-- crash, a takeover and a restart.
CREATE TABLE hangar_inventory_cursors (
    bucket_fingerprint text NOT NULL CHECK (bucket_fingerprint <> ''),
    activation_epoch   bigint NOT NULL
        REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    cursor_fence       bigint NOT NULL CHECK (cursor_fence > 0),
    after_key          text NOT NULL DEFAULT '',
    after_generation   bigint NOT NULL DEFAULT 0 CHECK (after_generation >= 0),
    cycle              bigint NOT NULL DEFAULT 0 CHECK (cycle >= 0),
    updated_at         timestamp with time zone NOT NULL DEFAULT now(),

    PRIMARY KEY (bucket_fingerprint, activation_epoch),

    -- An empty after-key is the legitimate start of a cycle. A generation with
    -- no key is not: it would order against nothing.
    CONSTRAINT hangar_cursor_generation_needs_key CHECK (
        after_key <> '' OR after_generation = 0
    )
);

CREATE FUNCTION hangar_cursor_fence() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.cursor_fence < OLD.cursor_fence THEN
        RAISE EXCEPTION 'hangar: inventory cursor for % at epoch % moved its fence backwards, % -> %; only the current fencing epoch may reserve a page or advance the cursor',
            OLD.bucket_fingerprint, OLD.activation_epoch, OLD.cursor_fence, NEW.cursor_fence
            USING ERRCODE = 'JB003';
    END IF;
    IF NEW.cycle < OLD.cycle THEN
        RAISE EXCEPTION 'hangar: inventory cursor for % went back a cycle, % -> %',
            OLD.bucket_fingerprint, OLD.cycle, NEW.cycle
            USING ERRCODE = 'JB003';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_cursor_fence_guard
    BEFORE UPDATE ON hangar_inventory_cursors
    FOR EACH ROW EXECUTE FUNCTION hangar_cursor_fence();

-- Per-object debt, committed before the cursor advances, so that poison, stale
-- ownership and cursor corruption cannot strand later objects or become
-- authoritative absence.
--
-- object_key is a value the deployment's own listing observed, not one a caller
-- supplied. That is why it can be written down here while no API accepts a key:
-- the rule is about who chooses a location.
CREATE TABLE hangar_inventory_debt (
    id                 bigserial PRIMARY KEY,
    bucket_fingerprint text NOT NULL,
    activation_epoch   bigint NOT NULL,
    object_key         text NOT NULL CHECK (object_key <> ''),
    generation         bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    reason             text NOT NULL
        CHECK (reason IN ('poison_metadata', 'corrupt_cursor', 'stat_failure',
                          'list_failure', 'unmanaged_object', 'marker_mismatch',
                          'generation_conflict')),
    attempts           integer NOT NULL DEFAULT 1 CHECK (attempts >= 1),
    detail             text NOT NULL DEFAULT '' CHECK (octet_length(detail) <= 2048),
    observed_at        timestamp with time zone NOT NULL DEFAULT now(),

    -- Debt belongs to a cursor. A debt row for a bucket or epoch this
    -- deployment holds no cursor for is a row from somebody else's sweep.
    FOREIGN KEY (bucket_fingerprint, activation_epoch)
        REFERENCES hangar_inventory_cursors (bucket_fingerprint, activation_epoch) ON DELETE RESTRICT,
    UNIQUE (bucket_fingerprint, activation_epoch, object_key, generation, reason)
);

-- Reclaim work: one admitted job per exact generation, its fenced lease, and
-- the attempts made under it.
CREATE TABLE hangar_reclaim_jobs (
    id                  bigserial PRIMARY KEY,
    lifecycle_id        bigint NOT NULL
        REFERENCES hangar_exact_lifecycles (id) ON DELETE RESTRICT,
    activation_epoch    bigint NOT NULL
        REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    owner_id            uuid NOT NULL,
    lease_fence         bigint NOT NULL CHECK (lease_fence > 0),
    generation          bigint NOT NULL CHECK (generation > 0),
    metageneration      bigint NOT NULL CHECK (metageneration > 0),
    admitted_at         timestamp with time zone NOT NULL DEFAULT now(),
    renewed_at          timestamp with time zone NOT NULL DEFAULT now(),
    expires_at          timestamp with time zone NOT NULL,
    absence_observed_at timestamp with time zone,
    outcome             text
        CHECK (outcome IN ('reclaimed_confirmed', 'reclaimed_inferred', 'conflicted', 'abandoned')),
    finalized_at        timestamp with time zone,

    CONSTRAINT hangar_reclaim_lease_term CHECK (expires_at - renewed_at >= interval '15 minutes'),
    CONSTRAINT hangar_reclaim_outcome_pairs CHECK ((outcome IS NULL) = (finalized_at IS NULL))
);

-- At most one unfinalized reclaim job per exact generation.
CREATE UNIQUE INDEX hangar_reclaim_jobs_one_active_idx
    ON hangar_reclaim_jobs (lifecycle_id) WHERE finalized_at IS NULL;

CREATE TABLE hangar_reclaim_attempts (
    id                 bigserial PRIMARY KEY,
    job_id             bigint NOT NULL REFERENCES hangar_reclaim_jobs (id) ON DELETE RESTRICT,
    lease_fence        bigint NOT NULL CHECK (lease_fence > 0),
    admitted_delete_at timestamp with time zone NOT NULL DEFAULT now(),
    outcome            text
        CHECK (outcome IN ('deleted', 'already_absent', 'generation_conflict',
                           'unauthorized', 'timeout', 'infrastructure_failure')),
    observed_at        timestamp with time zone,

    CONSTRAINT hangar_reclaim_attempt_pairs CHECK ((outcome IS NULL) = (observed_at IS NULL))
);

-- One attestation of the bucket's lifetime policy. It records what was observed
-- and when, so a later reader decides for itself whether the evidence is fresh
-- rather than trusting a boolean somebody computed earlier.
CREATE TABLE hangar_policy_snapshots (
    id                     bigserial PRIMARY KEY,
    activation_epoch       bigint NOT NULL
        REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    bucket_fingerprint     text NOT NULL CHECK (bucket_fingerprint <> ''),
    metageneration         bigint NOT NULL CHECK (metageneration > 0),
    policy_hash            text NOT NULL CHECK (policy_hash <> ''),
    lifecycle_delete_rules integer NOT NULL CHECK (lifecycle_delete_rules >= 0),
    state                  text NOT NULL CHECK (state IN ('unknown', 'safe', 'at_risk')),
    observed_at            timestamp with time zone NOT NULL DEFAULT now(),

    -- Where the row came from. Two of Req 52's three at-risk triggers are not
    -- policy readings at all: an exact generation found absent with no admitted
    -- delete, and a principal that lost its grant while a delete was in flight.
    -- Both have to enter the SAME durable at-risk state -- Req 52 blocks new
    -- captures, claims, grants, adoption and reclaim admission from detection
    -- onward, whichever trigger fired -- and this table is what the admission
    -- gate reads. So they are written here, and the column is what keeps them
    -- honest: an operator reading this table can tell an attestation of the
    -- bucket's policy from a runtime observation that no policy read was made
    -- at all.
    source                 text NOT NULL DEFAULT 'attestation'
        CHECK (source IN ('attestation', 'runtime_observation')),

    CONSTRAINT hangar_policy_safe_has_no_delete_rules CHECK (
        state <> 'safe' OR lifecycle_delete_rules = 0
    ),

    -- A runtime observation is never SAFE. It exists only because something
    -- went wrong; a safe one would be this plane attesting a policy it did not
    -- read.
    CONSTRAINT hangar_runtime_observation_is_never_safe CHECK (
        source <> 'runtime_observation' OR state <> 'safe'
    )
);

-- What an attestation CONCLUDED, beside what it observed.
--
-- The snapshot above records the reading; this records the findings derived
-- from it. They are separate tables because they have different lifetimes: a
-- snapshot is superseded by the next reading, and Req 52 says recovery needs a
-- fresh safe attestation AND violation reconciliation -- re-attesting alone
-- never erases an unresolved violation. A findings column on the snapshot would
-- make the second half impossible to state, because the row carrying the
-- violation would be the row the next refresh replaced.
--
-- `resolved_at` is the reconciliation, and it is nullable and one-way: an
-- operator (or an operation that provably repaired the cause) closes a
-- violation, and nothing reopens a closed one under the same id.
CREATE TABLE hangar_policy_violations (
    id               bigserial PRIMARY KEY,
    activation_epoch bigint NOT NULL
        REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    -- NULLABLE, because two of the three triggers have no snapshot to attach
    -- to. An out-of-band absence and a runtime principal denial are observed by
    -- a controller doing its work, not by a policy read, and a violation forced
    -- to name a snapshot would have to invent one -- which is the same as
    -- claiming a policy reading that never happened.
    snapshot_id      bigint
        REFERENCES hangar_policy_snapshots (id) ON DELETE RESTRICT,
    violation        text NOT NULL
        CHECK (violation IN ('lifecycle_delete_rule', 'evidence_stale', 'evidence_unreadable',
                             'excess_role', 'insufficient_role', 'wrong_principal',
                             'shared_bucket', 'mixed_cohort', 'unrecognised_role',
                             'out_of_band_absence', 'runtime_principal_denied')),

    -- And the two that have no snapshot are exactly the two runtime members.
    -- An attestation-derived finding without its reading would be a finding
    -- nothing can be traced back to.
    CONSTRAINT hangar_violation_snapshot_matches_trigger CHECK (
        (violation IN ('out_of_band_absence', 'runtime_principal_denied'))
        OR snapshot_id IS NOT NULL
    ),
    subject          text NOT NULL CHECK (subject <> ''),
    detail           text NOT NULL DEFAULT '' CHECK (octet_length(detail) <= 1024),
    observed_at      timestamp with time zone NOT NULL DEFAULT now(),
    resolved_at      timestamp with time zone
);

-- One OPEN finding per (epoch, violation, subject). A monitor running every
-- fifteen minutes against an unfixed bucket would otherwise write a row per
-- pass forever, and the count an operator reads would be the age of the problem
-- rather than its size.
--
-- A partial index and not a four-column UNIQUE including `resolved_at`: NULLs
-- are distinct in a unique index, so that spelling constrains nothing at all
-- for exactly the rows it was meant to constrain -- every open finding has a
-- NULL there. Resolved rows are deliberately left unconstrained: the same
-- violation can be found, fixed, and found again, and each of those is a
-- separate fact with its own dates.
CREATE UNIQUE INDEX hangar_policy_violations_one_open_idx
    ON hangar_policy_violations (activation_epoch, violation, subject)
    WHERE resolved_at IS NULL;

CREATE FUNCTION hangar_policy_violation_resolution() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.resolved_at IS NOT NULL AND NEW.resolved_at IS DISTINCT FROM OLD.resolved_at THEN
        RAISE EXCEPTION 'hangar: policy violation % was resolved at % and cannot be reopened or re-dated to %; recovery needs a fresh safe attestation AND violation reconciliation, and a reopened finding is a reconciliation that never happened',
            OLD.id, OLD.resolved_at, NEW.resolved_at
            USING ERRCODE = 'JB002';
    END IF;
    IF NEW.violation <> OLD.violation OR NEW.subject <> OLD.subject THEN
        RAISE EXCEPTION 'hangar: policy violation % is immutable in what it is about', OLD.id
            USING ERRCODE = 'JB002';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_policy_violation_resolution_guard
    BEFORE UPDATE ON hangar_policy_violations
    FOR EACH ROW EXECUTE FUNCTION hangar_policy_violation_resolution();

-- One durable lease per operation kind per epoch. One operation cannot advance
-- another's cursor, which is why the kind is half the primary key rather than a
-- column on a shared lease.
CREATE TABLE hangar_operation_leases (
    kind             text NOT NULL
        CHECK (kind IN ('capture_recovery', 'no_capture_release', 'inventory',
                        'adoption', 'reclaim_admission', 'reclaim_delete',
                        'reclaim_finalization', 'read_lease_cleanup',
                        'policy_attestation')),
    activation_epoch bigint NOT NULL
        REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    owner_id         uuid NOT NULL,
    lease_fence      bigint NOT NULL CHECK (lease_fence > 0),
    acquired_at      timestamp with time zone NOT NULL DEFAULT now(),
    renewed_at       timestamp with time zone NOT NULL DEFAULT now(),
    expires_at       timestamp with time zone NOT NULL,

    PRIMARY KEY (kind, activation_epoch),
    CONSTRAINT hangar_operation_lease_term CHECK (expires_at - renewed_at >= interval '15 minutes')
);

CREATE FUNCTION hangar_operation_lease_fence() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.lease_fence < OLD.lease_fence THEN
        RAISE EXCEPTION 'hangar: % lease at epoch % moved its fence backwards, % -> %',
            OLD.kind, OLD.activation_epoch, OLD.lease_fence, NEW.lease_fence
            USING ERRCODE = 'JB003';
    END IF;
    IF NEW.owner_id <> OLD.owner_id AND NEW.lease_fence <= OLD.lease_fence THEN
        RAISE EXCEPTION 'hangar: % lease at epoch % changed owner without advancing the fence (still %); expired owners cannot delete or finalize and takeover advances the epoch',
            OLD.kind, OLD.activation_epoch, OLD.lease_fence
            USING ERRCODE = 'JB003';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_operation_lease_fence_guard
    BEFORE UPDATE ON hangar_operation_leases
    FOR EACH ROW EXECUTE FUNCTION hangar_operation_lease_fence();

-- ---------------------------------------------------------------------------
-- Deferred constraint triggers.
--
-- Each of these is a rule about more than one row, so it is checked at COMMIT
-- rather than at statement time: a transaction that writes the arbiter and its
-- child in either order is legal, and a transaction that writes only half of a
-- pair is not. They are DEFERRABLE INITIALLY DEFERRED for that reason and no
-- other -- none of them is deferred to give anybody a window.
-- ---------------------------------------------------------------------------

-- Exactly one branch child per decided handoff, of the branch the arbiter says.
CREATE FUNCTION hangar_check_handoff_branch() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    target      uuid;
    decided     text;
    captures    integer;
    no_captures integer;
    cancels     integer;
BEGIN
    target := CASE TG_OP WHEN 'DELETE' THEN OLD.handoff_id ELSE NEW.handoff_id END;

    SELECT disposition INTO decided FROM hangar_handoff_dispositions WHERE handoff_id = target;
    IF decided IS NULL THEN
        RAISE EXCEPTION 'hangar: handoff % has a branch record but no disposition arbiter row; the arbiter is what makes exactly-one true',
            target
            USING ERRCODE = 'JB004';
    END IF;

    SELECT count(*) INTO captures FROM hangar_capture_reservations WHERE handoff_id = target;
    SELECT count(*) INTO no_captures FROM hangar_no_capture_dispositions WHERE handoff_id = target;
    SELECT count(*) INTO cancels FROM hangar_pre_reservation_cancel_dispositions WHERE handoff_id = target;

    IF captures + no_captures + cancels = 0 THEN
        RAISE EXCEPTION 'hangar: handoff % is dispositioned % and carries no branch record; the arbiter says which branch won and the branch record is what that branch did',
            target, decided
            USING ERRCODE = 'JB004';
    END IF;
    IF captures + no_captures + cancels > 1 THEN
        RAISE EXCEPTION 'hangar: handoff % has % capture, % no_capture and % pre_reservation_cancel records; the three branches exclude one another permanently',
            target, captures, no_captures, cancels
            USING ERRCODE = 'JB001';
    END IF;

    IF (CASE decided
            WHEN 'capture' THEN captures
            WHEN 'no_capture' THEN no_captures
            ELSE cancels
        END) <> 1 THEN
        RAISE EXCEPTION 'hangar: handoff % is dispositioned % but carries the wrong branch record (% capture, % no_capture, % pre_reservation_cancel)',
            target, decided, captures, no_captures, cancels
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER hangar_handoff_branch_cardinality
    AFTER INSERT OR UPDATE OR DELETE ON hangar_handoff_dispositions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_handoff_branch();
CREATE CONSTRAINT TRIGGER hangar_handoff_branch_cardinality
    AFTER INSERT OR UPDATE OR DELETE ON hangar_capture_reservations
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_handoff_branch();
CREATE CONSTRAINT TRIGGER hangar_handoff_branch_cardinality
    AFTER INSERT OR UPDATE OR DELETE ON hangar_no_capture_dispositions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_handoff_branch();
CREATE CONSTRAINT TRIGGER hangar_handoff_branch_cardinality
    AFTER INSERT OR UPDATE OR DELETE ON hangar_pre_reservation_cancel_dispositions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_handoff_branch();

-- Stage 2 must match the predeclaration it extends, and the source hold must
-- already be acknowledged. A Stage 2 row that named a different execution,
-- lease, epoch or deadline would be a second, contradictory statement of facts
-- the predeclaration already froze.
CREATE FUNCTION hangar_check_stage_two() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    pre hangar_handoff_predeclarations%ROWTYPE;
BEGIN
    SELECT * INTO pre FROM hangar_handoff_predeclarations WHERE handoff_id = NEW.handoff_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'hangar: capture reservation % has no predeclaration', NEW.reservation_id
            USING ERRCODE = 'JB004';
    END IF;
    IF pre.execution_id <> NEW.execution_id
        OR pre.source_lease_id <> NEW.source_lease_id
        OR pre.activation_epoch <> NEW.activation_epoch
        OR pre.capture_deadline_at <> NEW.capture_deadline_at THEN
        RAISE EXCEPTION 'hangar: capture reservation % does not match the predeclared execution, source lease, activation epoch and deadline for handoff %',
            NEW.reservation_id, NEW.handoff_id
            USING ERRCODE = 'JB001';
    END IF;
    IF pre.hold_acknowledged_at IS NULL THEN
        RAISE EXCEPTION 'hangar: capture reservation % entered Stage 2 with no acknowledged source hold for handoff %; start and recovery fail closed until the hold matches current execution admission',
            NEW.reservation_id, NEW.handoff_id
            USING ERRCODE = 'JB004';
    END IF;

    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER hangar_stage_two_matches_predeclaration
    AFTER INSERT OR UPDATE ON hangar_capture_reservations
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_stage_two();

-- Logical resolution runs after canonicalization and only under the current
-- capture ownership fence. A stale owner resolves nothing.
--
-- The FENCED ACT is the resolution itself -- the insert, and any statement that
-- moves the fence the resolution was made under. It is not every later write to
-- the row: `capture_fence` here is the fence the resolution was recorded at and
-- is never rewritten, so comparing it to the CURRENT lease on an UPDATE that
-- touches only `state` refuses exactly the writes a takeover exists to permit.
-- `RegisterReceipt` updates `state` to 'registered'; under a new owner that
-- update raised JB003 at commit, so an ATC restart anywhere between the
-- resolution and the receipt -- which includes the upload, the slow half --
-- left a marked object in the bucket that no owner could ever register and no
-- owner could ever fail. The lease is the one fence source (see
-- hangarCurrentCaptureFence); this is the reader that had not joined it.
--
-- Every statement that IS fenced still checks its own fence: RegisterReceipt,
-- the irreversible publish point, the seal deadline and the terminal failure
-- all derive the current fence from the lease and refuse a stale one.
CREATE FUNCTION hangar_check_logical_resolution() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    current_fence bigint;
    reservation   hangar_capture_reservations%ROWTYPE;
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.capture_fence = OLD.capture_fence THEN
        RETURN NULL;
    END IF;

    SELECT * INTO reservation FROM hangar_capture_reservations WHERE reservation_id = NEW.reservation_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'hangar: logical reservation % has no Stage 2 reservation', NEW.reservation_id
            USING ERRCODE = 'JB004';
    END IF;
    IF reservation.first_create_attempted_at IS NOT NULL
        AND reservation.first_create_attempted_at < NEW.resolved_at THEN
        RAISE EXCEPTION 'hangar: reservation % attempted its first object create at %, before the logical reservation resolved at %; every possibly-created object must have a pre-existing reservation',
            NEW.reservation_id, reservation.first_create_attempted_at, NEW.resolved_at
            USING ERRCODE = 'JB004';
    END IF;

    SELECT capture_fence INTO current_fence
        FROM hangar_capture_attempt_leases WHERE reservation_id = NEW.reservation_id;
    IF current_fence IS NULL THEN
        RAISE EXCEPTION 'hangar: reservation % resolved a logical reservation while owned by nobody',
            NEW.reservation_id
            USING ERRCODE = 'JB004';
    END IF;
    IF NEW.capture_fence <> current_fence THEN
        RAISE EXCEPTION 'hangar: reservation % resolved at fence % while the current capture owner holds fence %; a stale owner may not seal, publish, sign, register or finalize',
            NEW.reservation_id, NEW.capture_fence, current_fence
            USING ERRCODE = 'JB003';
    END IF;

    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER hangar_logical_resolution_is_fenced
    AFTER INSERT OR UPDATE ON hangar_logical_reservations
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_logical_resolution();

-- A reservation may not record a first object create before it has resolved its
-- logical reservation. Stated from the reservation's side as well, so the
-- ordering holds whichever row moves.
CREATE FUNCTION hangar_check_create_follows_resolution() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.first_create_attempted_at IS NULL THEN
        RETURN NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM hangar_logical_reservations WHERE reservation_id = NEW.reservation_id) THEN
        RAISE EXCEPTION 'hangar: reservation % recorded an object create with no resolved logical reservation; recovery and inventory would have nothing to correlate the object with',
            NEW.reservation_id
            USING ERRCODE = 'JB004';
    END IF;

    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER hangar_create_follows_resolution
    AFTER INSERT OR UPDATE ON hangar_capture_reservations
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_create_follows_resolution();

-- A receipt names the epoch whose public key can check it, registers the exact
-- generation of its own logical reservation, and consumes a challenge issued
-- for that same reservation and ref.
CREATE FUNCTION hangar_check_receipt_admission() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    epoch     hangar_output_activation_epochs%ROWTYPE;
    logical   hangar_logical_reservations%ROWTYPE;
    lifecycle hangar_exact_lifecycles%ROWTYPE;
    challenge hangar_receipt_stat_challenges%ROWTYPE;
BEGIN
    SELECT * INTO epoch FROM hangar_output_activation_epochs WHERE epoch_id = NEW.activation_epoch;
    IF epoch.receipt_public_key_id IS DISTINCT FROM NEW.receipt_key_id THEN
        RAISE EXCEPTION 'hangar: receipt % names key % but activation epoch % attests key %',
            NEW.receipt_id, NEW.receipt_key_id, NEW.activation_epoch,
            coalesce(epoch.receipt_public_key_id, '<none>')
            USING ERRCODE = 'JB001';
    END IF;

    SELECT * INTO logical FROM hangar_logical_reservations WHERE reservation_id = NEW.reservation_id;
    SELECT * INTO lifecycle FROM hangar_exact_lifecycles WHERE id = NEW.lifecycle_id;
    IF logical.scope <> lifecycle.scope OR logical.digest <> lifecycle.digest THEN
        RAISE EXCEPTION 'hangar: receipt % registers %/% against a logical reservation for %/%; a ref may not be registered ahead of the logical reservation that correlates it',
            NEW.receipt_id, lifecycle.scope, lifecycle.digest, logical.scope, logical.digest
            USING ERRCODE = 'JB001';
    END IF;

    SELECT * INTO challenge FROM hangar_receipt_stat_challenges WHERE nonce = NEW.challenge_nonce;
    IF challenge.reservation_id <> NEW.reservation_id
        OR challenge.scope <> lifecycle.scope
        OR challenge.digest <> lifecycle.digest
        OR challenge.generation <> lifecycle.generation THEN
        RAISE EXCEPTION 'hangar: receipt % consumed a challenge issued for another capture or ref; a receipt cannot be replayed for another capture, source, output or fence',
            NEW.receipt_id
            USING ERRCODE = 'JB001';
    END IF;
    IF challenge.consumed_at IS NULL THEN
        RAISE EXCEPTION 'hangar: receipt % registered without consuming its one-use stat challenge',
            NEW.receipt_id
            USING ERRCODE = 'JB004';
    END IF;

    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER hangar_receipt_admission
    AFTER INSERT OR UPDATE ON hangar_output_receipts
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_receipt_admission();

-- Reclamation and protection exclude one another, stated from both sides so
-- that whichever transaction commits second is the one refused.
CREATE FUNCTION hangar_check_reclaim_exclusion() RETURNS trigger
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
    -- An EXPIRED lease is not an active one. AC 13 says a reader's protection
    -- ends when the read lease closes OR safely expires, and counting an
    -- abandoned lease forever would make one crashed materializer pin a
    -- generation for the life of the deployment. Recovery still closes it --
    -- only after this same database clock says it has expired -- and the
    -- tombstone is what stops it resurrecting.
    SELECT count(*) INTO leases FROM hangar_read_leases
        WHERE lifecycle_id = target AND released_at IS NULL AND expires_at > now();
    SELECT count(*) INTO pending FROM hangar_logical_reservations
        WHERE scope = lifecycle.scope AND digest = lifecycle.digest
          AND state = 'unresolved_generation';

    IF reclaims > 0 AND (claims > 0 OR leases > 0 OR pending > 0) THEN
        RAISE EXCEPTION 'hangar: exact ref %/%/% has an admitted reclaim beside % active claim(s), % active read lease(s) and % unresolved reservation(s); reclaim admission is refused while any of them exists',
            lifecycle.scope, lifecycle.digest, lifecycle.generation, claims, leases, pending
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER hangar_reclaim_exclusion
    AFTER INSERT OR UPDATE ON hangar_reclaim_jobs
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_reclaim_exclusion();
CREATE CONSTRAINT TRIGGER hangar_reclaim_exclusion
    AFTER INSERT OR UPDATE ON hangar_claims
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_reclaim_exclusion();
CREATE CONSTRAINT TRIGGER hangar_reclaim_exclusion
    AFTER INSERT OR UPDATE ON hangar_read_leases
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_reclaim_exclusion();

-- A read lease reads what its claim protects.
CREATE FUNCTION hangar_check_read_lease_claim() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    claim hangar_claims%ROWTYPE;
BEGIN
    SELECT * INTO claim FROM hangar_claims WHERE claim_id = NEW.claim_id;
    IF claim.lifecycle_id <> NEW.lifecycle_id THEN
        RAISE EXCEPTION 'hangar: read lease % reads lifecycle % under a claim on lifecycle %',
            NEW.read_lease_id, NEW.lifecycle_id, claim.lifecycle_id
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER hangar_read_lease_matches_claim
    AFTER INSERT OR UPDATE ON hangar_read_leases
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_read_lease_claim();

-- New protection and new reclamation both need a currently provable safe
-- policy. Releases and diagnosis do not, which is why this fires on INSERT
-- alone: from detection onward new captures, claim acquires, grants, adoption
-- and reclaim admission stop, while existing claims and leases stay recorded
-- and already-admitted delete work may finish.
--
-- Req 52 names five admissions, so this is attached five times: to
-- hangar_capture_reservations (a new capture), hangar_claims (a claim
-- acquire), hangar_read_leases (a managed-output grant),
-- hangar_exact_lifecycles for adopted rows only (orphan adoption) and
-- hangar_reclaim_jobs (reclaim admission). A renewal reaches the ON CONFLICT
-- UPDATE path and fires none of them, which is the "existing claims and read
-- leases remain recorded" half of the same requirement.
--
-- A sixth attachment, on hangar_handoff_predeclarations, is the *earliest* of
-- those admissions rather than a new one (review finding R2-2, ruled by the
-- orchestrator). A predeclaration is what makes a producer hold its source, and
-- a capture admitted while the policy is healthy whose policy goes at-risk
-- before Stage 2 holds that source through a reservation the fifth attachment
-- will refuse, and then has to be routed down no_capture to let it go. Safe,
-- but a hold for nothing: nothing downstream of a predeclaration can be
-- admitted while at risk, so admitting the predeclaration buys only work to
-- undo. PredeclareHandoff's ON CONFLICT (handoff_id) DO NOTHING re-admission
-- fires no INSERT trigger, so an idempotent repeat stays idempotent.
CREATE FUNCTION hangar_check_policy_admission() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    latest hangar_policy_snapshots%ROWTYPE;
BEGIN
    SELECT * INTO latest FROM hangar_policy_snapshots
        WHERE activation_epoch = NEW.activation_epoch
        ORDER BY observed_at DESC, id DESC LIMIT 1;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'hangar: epoch % has no lifetime-policy attestation; "we have not checked" and "the check failed" are the same amount of evidence',
            NEW.activation_epoch
            USING ERRCODE = 'JB002';
    END IF;
    IF latest.state <> 'safe' THEN
        RAISE EXCEPTION 'hangar: epoch % is % on its lifetime policy; new captures, claim acquires, grants, adoption and reclaim admission stop from detection onward',
            NEW.activation_epoch, latest.state
            USING ERRCODE = 'JB002';
    END IF;
    IF now() - latest.observed_at > interval '15 minutes' THEN
        RAISE EXCEPTION 'hangar: the lifetime-policy attestation for epoch % is % old, past the 15-minute detection bound; a stale check is not a safe one',
            NEW.activation_epoch, now() - latest.observed_at
            USING ERRCODE = 'JB002';
    END IF;

    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER hangar_policy_admits_new_protection
    AFTER INSERT ON hangar_handoff_predeclarations
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_policy_admission();
CREATE CONSTRAINT TRIGGER hangar_policy_admits_new_protection
    AFTER INSERT ON hangar_claims
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_policy_admission();
CREATE CONSTRAINT TRIGGER hangar_policy_admits_new_protection
    AFTER INSERT ON hangar_reclaim_jobs
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_policy_admission();
CREATE CONSTRAINT TRIGGER hangar_policy_admits_new_protection
    AFTER INSERT ON hangar_capture_reservations
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_policy_admission();
CREATE CONSTRAINT TRIGGER hangar_policy_admits_new_protection
    AFTER INSERT ON hangar_read_leases
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_policy_admission();
-- Adoption only. A registration is a capture that has already passed its first
-- object create: the bytes exist in the bucket, and refusing the row that names
-- them would leave a marked generation with nothing correlating it -- an orphan
-- of exactly the kind adoption exists to clean up, manufactured by the guard
-- meant to protect the plane. Req 52 stops admission and lets already-admitted
-- work finish; this is that work finishing.
CREATE CONSTRAINT TRIGGER hangar_policy_admits_new_protection
    AFTER INSERT ON hangar_exact_lifecycles
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (NEW.origin = 'adopted')
    EXECUTE FUNCTION hangar_check_policy_admission();

-- What a finalized reclaim job may claim to have proved. `reclaimed_confirmed`
-- needs an acknowledged conditional delete; `reclaimed_inferred` needs a
-- durable admitted-delete record whose response was lost, plus observed exact
-- absence. Absence without a prior admitted delete is an out-of-band lifetime
-- violation, and this is where that distinction is kept.
CREATE FUNCTION hangar_check_reclaim_evidence() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    confirmed integer;
    admitted  integer;
BEGIN
    IF NEW.outcome IS NULL THEN
        RETURN NULL;
    END IF;

    SELECT count(*) INTO confirmed FROM hangar_reclaim_attempts
        WHERE job_id = NEW.id AND outcome = 'deleted';
    SELECT count(*) INTO admitted FROM hangar_reclaim_attempts WHERE job_id = NEW.id;

    IF NEW.outcome = 'reclaimed_confirmed' AND confirmed = 0 THEN
        RAISE EXCEPTION 'hangar: reclaim job % finalized as reclaimed_confirmed with no acknowledged conditional delete; an ambiguous deletion is inferred at best',
            NEW.id
            USING ERRCODE = 'JB004';
    END IF;
    IF NEW.outcome = 'reclaimed_inferred' THEN
        IF admitted = 0 THEN
            RAISE EXCEPTION 'hangar: reclaim job % finalized as reclaimed_inferred with no durable admitted-delete record; absence without a prior admitted delete is an out-of-band lifetime violation, not normal reclamation',
                NEW.id
            USING ERRCODE = 'JB004';
        END IF;
        IF NEW.absence_observed_at IS NULL THEN
            RAISE EXCEPTION 'hangar: reclaim job % finalized as reclaimed_inferred without observing exact absence',
                NEW.id
            USING ERRCODE = 'JB004';
        END IF;
    END IF;

    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER hangar_reclaim_evidence
    AFTER INSERT OR UPDATE ON hangar_reclaim_jobs
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_reclaim_evidence();

-- ---------------------------------------------------------------------------
-- Indexes for the work each worker selects.
-- ---------------------------------------------------------------------------

-- Capture recovery: unresolved reservations by deadline.
CREATE INDEX hangar_capture_reservations_due_idx
    ON hangar_capture_reservations (capture_deadline_at)
    WHERE state IN ('unresolved', 'resolved');

-- Expired capture ownership, for takeover.
CREATE INDEX hangar_capture_attempt_leases_expiry_idx
    ON hangar_capture_attempt_leases (expires_at);

-- The second half of each release handoff, still owed.
CREATE INDEX hangar_no_capture_release_due_idx
    ON hangar_no_capture_dispositions (intent_recorded_at)
    WHERE release_acknowledged_at IS NULL;
CREATE INDEX hangar_pre_reservation_cancel_due_idx
    ON hangar_pre_reservation_cancel_dispositions (intent_recorded_at)
    WHERE finalized_at IS NULL;

-- Unresolved reservations block orphan adoption of their (scope, digest), and
-- inventory asks that question per correlation rather than per row.
CREATE INDEX hangar_logical_reservations_unresolved_idx
    ON hangar_logical_reservations (scope, digest)
    WHERE state = 'unresolved_generation';

-- The lock suffix's class-1 statement. It asks for a correlation and carries no
-- state predicate -- it cannot, because it locks every reservation for that
-- correlation whatever state each is in -- so the partial index above cannot
-- answer it, and without this one every Hangar transaction that touches a
-- correlation scans the table.
CREATE INDEX hangar_logical_reservations_correlation_idx
    ON hangar_logical_reservations (scope, digest);

-- Exact generation lookup, and the reclaim candidate scan.
CREATE INDEX hangar_exact_lifecycles_reclaimable_idx
    ON hangar_exact_lifecycles (registered_at)
    WHERE state IN ('registered', 'adopted');

-- The receipt for a handoff, which is what classifying a handoff reads. The
-- table's own unique keys are on the reservation and the challenge nonce, and
-- neither of those is what a caller holding a handoff id has.
CREATE INDEX hangar_output_receipts_handoff_idx
    ON hangar_output_receipts (handoff_id);

-- Active claims and active read leases, which are what reclaim admission asks
-- about and what the lock helper orders.
CREATE INDEX hangar_claims_active_idx
    ON hangar_claims (lifecycle_id) WHERE released_at IS NULL;
CREATE INDEX hangar_read_leases_active_idx
    ON hangar_read_leases (lifecycle_id) WHERE released_at IS NULL;
CREATE INDEX hangar_read_leases_expiry_idx
    ON hangar_read_leases (expires_at) WHERE released_at IS NULL;

-- Cursor debt, oldest first, per bucket and epoch.
CREATE INDEX hangar_inventory_debt_due_idx
    ON hangar_inventory_debt (bucket_fingerprint, activation_epoch, observed_at);

-- The freshest policy attestation per epoch, which every admission check reads.
CREATE INDEX hangar_policy_snapshots_latest_idx
    ON hangar_policy_snapshots (activation_epoch, observed_at DESC);

-- Unconsumed challenges, for expiry sweeps.
CREATE INDEX hangar_receipt_stat_challenges_open_idx
    ON hangar_receipt_stat_challenges (not_after) WHERE consumed_at IS NULL;
