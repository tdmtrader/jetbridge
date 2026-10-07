-- Restores the shapes no_activation_no_inventory dropped, EMPTY.
--
-- Activation epochs are not reconstructible: what an epoch's facets were,
-- which cohort attested them and when they moved is gone, and inventing a row
-- would be inventing that evidence. The foreign keys that referenced an epoch
-- come back NOT VALID so the rows recorded since keep their numbers; an
-- operator going back to the activation command begins a new epoch with it.
-- The inventory cursor, its debt and the operation leases restart from nothing,
-- which is how a fresh controller finds them anyway.

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

CREATE TABLE hangar_output_activation_epochs (
    epoch_id bigint PRIMARY KEY CHECK (epoch_id > 0),
    base_state text NOT NULL DEFAULT 'initial' CHECK (hangar_output_facet_ordinal(base_state) IS NOT NULL),
    output_state text NOT NULL DEFAULT 'initial' CHECK (hangar_output_facet_ordinal(output_state) IS NOT NULL),
    base_attestation jsonb,
    output_attestation jsonb,
    receipt_public_key_id text,
    receipt_key_valid_from timestamptz,
    receipt_key_valid_until timestamptz,
    materialization_key_id text,
    bucket_fingerprint text,
    derived_namespace text,
    protocol_version text,
    ledger_version text,
    cohort_digest text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    CONSTRAINT hangar_output_epoch_base_evidence CHECK (base_state IN ('initial', 'attesting') OR base_attestation IS NOT NULL),
    CONSTRAINT hangar_output_epoch_enabled_output_identity CHECK (output_state <> 'enabled' OR
        (materialization_key_id IS NOT NULL AND bucket_fingerprint IS NOT NULL AND derived_namespace IS NOT NULL)),
    CONSTRAINT hangar_output_epoch_needs_base CHECK (output_state IN ('initial', 'disabled') OR base_state IN ('attested', 'enabled')),
    CONSTRAINT hangar_output_epoch_output_evidence CHECK (output_state IN ('initial', 'attesting') OR output_attestation IS NOT NULL)
);
CREATE UNIQUE INDEX hangar_output_one_enabled_base_epoch ON hangar_output_activation_epochs ((true)) WHERE base_state = 'enabled';
CREATE UNIQUE INDEX hangar_output_one_enabled_output_epoch ON hangar_output_activation_epochs ((true)) WHERE output_state = 'enabled';

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

CREATE FUNCTION hangar_activation_role_name() RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT current_database() || '_hangar_activation'
$$;

CREATE FUNCTION hangar_activation_role_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF current_user <> hangar_activation_role_name() THEN
        RAISE EXCEPTION 'hangar_output_activation_epochs is written only by the activation database role (%), not %',
            hangar_activation_role_name(), current_user
            USING ERRCODE = 'JB003';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER hangar_output_activation_role_guard
    BEFORE INSERT OR UPDATE OR DELETE ON hangar_output_activation_epochs
    FOR EACH ROW EXECUTE FUNCTION hangar_activation_role_guard();

CREATE TRIGGER hangar_output_activation_role_guard_truncate
    BEFORE TRUNCATE ON hangar_output_activation_epochs
    FOR EACH STATEMENT EXECUTE FUNCTION hangar_activation_role_guard();

REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON hangar_output_activation_epochs FROM CURRENT_USER;
GRANT UPDATE (epoch_id) ON hangar_output_activation_epochs TO CURRENT_USER;

CREATE TABLE hangar_operation_leases (
    kind text NOT NULL CHECK (kind IN ('capture_recovery', 'no_capture_release', 'inventory', 'adoption',
        'reclaim_admission', 'reclaim_delete', 'reclaim_finalization', 'read_lease_cleanup', 'policy_attestation')),
    activation_epoch bigint NOT NULL REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    owner_id uuid NOT NULL,
    lease_fence bigint NOT NULL CHECK (lease_fence > 0),
    acquired_at timestamptz NOT NULL DEFAULT now(),
    renewed_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
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

CREATE TABLE hangar_inventory_cursors (
    bucket_fingerprint text NOT NULL CHECK (bucket_fingerprint <> ''),
    activation_epoch bigint NOT NULL REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT,
    cursor_fence bigint NOT NULL CHECK (cursor_fence > 0),
    after_key text NOT NULL DEFAULT '',
    after_generation bigint NOT NULL DEFAULT 0 CHECK (after_generation >= 0),
    cycle bigint NOT NULL DEFAULT 0 CHECK (cycle >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (bucket_fingerprint, activation_epoch),
    CONSTRAINT hangar_cursor_generation_needs_key CHECK (after_key <> '' OR after_generation = 0)
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

CREATE TABLE hangar_inventory_debt (
    id bigserial PRIMARY KEY,
    bucket_fingerprint text NOT NULL,
    activation_epoch bigint NOT NULL,
    object_key text NOT NULL CHECK (object_key <> ''),
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    reason text NOT NULL CHECK (reason IN ('poison_metadata', 'corrupt_cursor', 'stat_failure',
        'list_failure', 'unmanaged_object', 'marker_mismatch', 'generation_conflict')),
    attempts integer NOT NULL DEFAULT 1 CHECK (attempts >= 1),
    detail text NOT NULL DEFAULT '' CHECK (octet_length(detail) <= 2048),
    observed_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (bucket_fingerprint, activation_epoch, object_key, generation, reason),
    FOREIGN KEY (bucket_fingerprint, activation_epoch)
        REFERENCES hangar_inventory_cursors (bucket_fingerprint, activation_epoch) ON DELETE RESTRICT
);
CREATE INDEX hangar_inventory_debt_due_idx ON hangar_inventory_debt (bucket_fingerprint, activation_epoch, observed_at);

ALTER TABLE hangar_exact_lifecycles ADD CONSTRAINT hangar_exact_lifecycles_activation_epoch_fkey
    FOREIGN KEY (activation_epoch) REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE hangar_claims ADD CONSTRAINT hangar_claims_activation_epoch_fkey
    FOREIGN KEY (activation_epoch) REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE hangar_read_leases ADD CONSTRAINT hangar_read_leases_activation_epoch_fkey
    FOREIGN KEY (activation_epoch) REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE hangar_reclaim_jobs ADD CONSTRAINT hangar_reclaim_jobs_activation_epoch_fkey
    FOREIGN KEY (activation_epoch) REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE hangar_input_publications ADD CONSTRAINT hangar_input_publications_activation_epoch_fkey
    FOREIGN KEY (activation_epoch) REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT NOT VALID;
ALTER TABLE pipeline_run_inputs ADD CONSTRAINT pipeline_run_inputs_activation_epoch_fkey
    FOREIGN KEY (activation_epoch) REFERENCES hangar_output_activation_epochs (epoch_id) NOT VALID;
ALTER TABLE pipeline_run_input_uploads ADD CONSTRAINT pipeline_run_input_uploads_activation_epoch_fkey
    FOREIGN KEY (activation_epoch) REFERENCES hangar_output_activation_epochs (epoch_id) ON DELETE RESTRICT NOT VALID;

-- The activation role's grants, as capture_is_one_row left them.
CREATE FUNCTION hangar_activation_grants() RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    role_name text := hangar_activation_role_name();
    readable  text;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
        RETURN;
    END IF;
    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %I', role_name);
    EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %I', role_name);

    EXECUTE format('GRANT SELECT, INSERT ON hangar_output_activation_epochs TO %I', role_name);
    EXECUTE format('GRANT UPDATE (base_state, base_attestation, protocol_version, ledger_version, '
        'cohort_digest, output_state, output_attestation, receipt_public_key_id, '
        'receipt_key_valid_from, receipt_key_valid_until, materialization_key_id, '
        'bucket_fingerprint, derived_namespace, revision, updated_at) '
        'ON hangar_output_activation_epochs TO %I', role_name);

    FOREACH readable IN ARRAY ARRAY[
        'migrations_history', 'hangar_captures', 'hangar_exact_lifecycles', 'hangar_claims',
        'hangar_read_leases', 'hangar_reclaim_jobs', 'hangar_inventory_debt'
    ] LOOP
        EXECUTE format('GRANT SELECT ON %I TO %I', readable, role_name);
    END LOOP;

    EXECUTE format('GRANT SELECT ON hangar_integrity_findings TO %I', role_name);
    EXECUTE format('GRANT UPDATE (resolved_at) ON hangar_integrity_findings TO %I', role_name);
END $$;
SELECT hangar_activation_grants();
