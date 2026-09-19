-- The output plane's word for the daemon's admission over one source
-- incarnation is "source hold" (SourceHoldGate, hold_acknowledged_at, the
-- protocol's hold acknowledgement). 1788936403 named the identity column for
-- it source_lease_id, after a word the vocabulary has since retired: a lease
-- in this plane is a READ lease over a published ref, and it is a different
-- thing on a different table. Rename the column on the four tables that carry
-- it, and re-create the two trigger functions whose PL/pgSQL bodies name it --
-- a RENAME COLUMN does not rewrite a function body, and the first UPDATE or
-- Stage 2 INSERT after the rename would otherwise fail on a column that no
-- longer exists.
--
-- The plane is contracts-only and not enabled, so no row exists to carry
-- across; the rename is nevertheless written to be correct over any rows that
-- do, because a migration that assumes an empty table is a migration that is
-- wrong once.
ALTER TABLE hangar_handoff_predeclarations RENAME COLUMN source_lease_id TO source_hold_id;
ALTER TABLE hangar_capture_reservations RENAME COLUMN source_lease_id TO source_hold_id;
ALTER TABLE hangar_no_capture_dispositions RENAME COLUMN source_lease_id TO source_hold_id;
ALTER TABLE hangar_pre_reservation_cancel_dispositions RENAME COLUMN source_lease_id TO source_hold_id;

CREATE OR REPLACE FUNCTION hangar_predeclaration_immutable() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.handoff_id <> OLD.handoff_id
        OR NEW.source_hold_id <> OLD.source_hold_id
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

CREATE OR REPLACE FUNCTION hangar_check_stage_two() RETURNS trigger
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
        OR pre.source_hold_id <> NEW.source_hold_id
        OR pre.activation_epoch <> NEW.activation_epoch
        OR pre.capture_deadline_at <> NEW.capture_deadline_at THEN
        RAISE EXCEPTION 'hangar: capture reservation % does not match the predeclared execution, source hold, activation epoch and deadline for handoff %',
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
