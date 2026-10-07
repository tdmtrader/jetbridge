-- capture_is_one_row, part two, reversed: the handoff tables return empty,
-- with the functions, constraints and triggers they had. Capture rows stay
-- (1789793152 owns them). A credential delivery recorded against a capture
-- row has no handoff to point at, so the reversal refuses while one exists.
--
-- LOSSY. The up migration carried no output history across, and this one
-- carries nothing back: every dropped table (handoff predeclarations,
-- dispositions, reservations, capture leases, receipts, stat challenges,
-- announcements, policy snapshots, policy violations and the Run-side
-- pipeline_run_output_* tables) returns EMPTY. Integrity findings are not
-- moved back into hangar_policy_violations: they stay in
-- hangar_integrity_findings, which 1789793152's reversal then drops, so an
-- open finding no longer gates admission after a full reversal. Capture rows
-- and their pipeline_run_captures links have no handoff shape to return to;
-- they stay here and are lost with 1789793152's reversal.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_run_credential_handoffs) THEN
        RAISE EXCEPTION 'capture_is_one_row: Run credential handoffs name capture rows and cannot be carried back to handoffs';
    END IF;
END $$;

ALTER TABLE pipeline_run_credential_handoffs
    DROP CONSTRAINT pipeline_run_credential_handoffs_capture_fkey,
    DROP CONSTRAINT pipeline_run_credential_handoffs_capture_key,
    DROP COLUMN execution_id,
    DROP COLUMN output_name,
    ADD COLUMN handoff_id uuid NOT NULL,
    ADD CONSTRAINT pipeline_run_credential_handoffs_handoff_id_key UNIQUE (handoff_id);

ALTER TABLE pipeline_run_executions
    ADD COLUMN handoff_id uuid,
    ADD CONSTRAINT pipeline_run_executions_handoff_id_key UNIQUE (handoff_id);

CREATE TABLE public.hangar_capture_announcements (
    id bigint NOT NULL,
    handoff_id uuid NOT NULL,
    kind text NOT NULL,
    disposition text,
    reason text NOT NULL,
    announced_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT hangar_capture_announcements_disposition_check CHECK (((disposition IS NULL) OR (disposition = ANY (ARRAY['capture'::text, 'no_capture'::text, 'pre_reservation_cancel'::text])))),
    CONSTRAINT hangar_capture_announcements_kind_check CHECK ((kind = ANY (ARRAY['capture-selected'::text, 'capture-seal-started'::text, 'capture-disposition'::text]))),
    CONSTRAINT hangar_capture_announcements_reason_check CHECK (((octet_length(reason) >= 1) AND (octet_length(reason) <= 64)))
);

CREATE TABLE public.hangar_capture_attempt_leases (
    reservation_id uuid NOT NULL,
    owner_id uuid NOT NULL,
    capture_fence bigint NOT NULL,
    acquired_at timestamp with time zone DEFAULT now() NOT NULL,
    renewed_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT hangar_capture_attempt_leases_capture_fence_check CHECK ((capture_fence > 0)),
    CONSTRAINT hangar_capture_lease_term CHECK (((expires_at - renewed_at) >= '00:15:00'::interval))
);

CREATE TABLE public.hangar_capture_reservations (
    reservation_id uuid NOT NULL,
    handoff_id uuid NOT NULL,
    execution_id uuid NOT NULL,
    activation_epoch bigint NOT NULL,
    source_hold_id uuid NOT NULL,
    producer_checkpoint_id text NOT NULL,
    finish_acknowledgement jsonb NOT NULL,
    finish_successful boolean NOT NULL,
    capture_fence bigint NOT NULL,
    capture_deadline_at timestamp with time zone NOT NULL,
    seal_deadline_at timestamp with time zone,
    state text DEFAULT 'unresolved'::text NOT NULL,
    first_create_attempted_at timestamp with time zone,
    past_irreversible_publish_point boolean DEFAULT false NOT NULL,
    terminal_failure text,
    settled_at timestamp with time zone,
    release_intent_id uuid,
    release_acknowledged_at timestamp with time zone,
    release_acknowledgement jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT hangar_capture_reservations_capture_fence_check CHECK ((capture_fence > 0)),
    CONSTRAINT hangar_capture_reservations_finish_successful_check CHECK (finish_successful),
    CONSTRAINT hangar_capture_reservations_producer_checkpoint_id_check CHECK (((producer_checkpoint_id <> ''::text) AND (octet_length(producer_checkpoint_id) <= 256))),
    CONSTRAINT hangar_capture_reservations_state_check CHECK ((state = ANY (ARRAY['unresolved'::text, 'resolved'::text, 'registered'::text, 'failed'::text, 'cancelled'::text]))),
    CONSTRAINT hangar_release_intent_implies_terminal CHECK (((release_intent_id IS NULL) OR (state = ANY (ARRAY['registered'::text, 'cancelled'::text, 'failed'::text])))),
    CONSTRAINT hangar_reservation_failure_is_typed CHECK (((state = 'failed'::text) = (terminal_failure IS NOT NULL))),
    CONSTRAINT hangar_reservation_publish_point CHECK (((NOT past_irreversible_publish_point) OR (first_create_attempted_at IS NOT NULL))),
    CONSTRAINT hangar_reservation_release_names_its_intent CHECK (((release_acknowledged_at IS NULL) OR ((release_intent_id IS NOT NULL) AND (release_acknowledgement IS NOT NULL)))),
    CONSTRAINT hangar_reservation_settlement_is_earned CHECK (((settled_at IS NULL) OR (state = 'registered'::text) OR (release_acknowledged_at IS NOT NULL))),
    CONSTRAINT hangar_reservation_settlement_is_terminal CHECK (((settled_at IS NULL) OR (state = ANY (ARRAY['registered'::text, 'failed'::text, 'cancelled'::text]))))
);

CREATE TABLE public.hangar_handoff_dispositions (
    handoff_id uuid NOT NULL,
    disposition text NOT NULL,
    decided_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT hangar_handoff_dispositions_disposition_check CHECK ((disposition = ANY (ARRAY['capture'::text, 'no_capture'::text, 'pre_reservation_cancel'::text])))
);

CREATE TABLE public.hangar_handoff_predeclarations (
    handoff_id uuid NOT NULL,
    source_hold_id uuid NOT NULL,
    execution_id uuid NOT NULL,
    execution_fence bigint NOT NULL,
    output_name text NOT NULL,
    activation_epoch bigint NOT NULL,
    capture_deadline_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    hold_acknowledged_at timestamp with time zone,
    reserved_locator text,
    reserved_incarnation jsonb,
    reserved_directory text,
    reserved_at timestamp with time zone,
    CONSTRAINT hangar_handoff_predeclarations_execution_fence_check CHECK ((execution_fence > 0)),
    CONSTRAINT hangar_handoff_predeclarations_output_name_check CHECK ((output_name ~ '^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$'::text)),
    CONSTRAINT hangar_handoff_predeclarations_reserved_directory_check CHECK ((reserved_directory <> ''::text)),
    CONSTRAINT hangar_handoff_predeclarations_reserved_locator_check CHECK ((reserved_locator <> ''::text)),
    CONSTRAINT hangar_predeclaration_deadline_bounded CHECK ((((capture_deadline_at - created_at) >= '01:00:00'::interval) AND ((capture_deadline_at - created_at) <= '7 days'::interval))),
    CONSTRAINT hangar_predeclaration_hold_needs_a_reservation CHECK (((hold_acknowledged_at IS NULL) OR (reserved_at IS NOT NULL))),
    CONSTRAINT hangar_predeclaration_reservation_is_whole CHECK ((((reserved_at IS NULL) = (reserved_locator IS NULL)) AND ((reserved_at IS NULL) = (reserved_incarnation IS NULL)) AND ((reserved_at IS NULL) = (reserved_directory IS NULL))))
);

CREATE TABLE public.hangar_logical_reservations (
    reservation_id uuid NOT NULL,
    scope text NOT NULL,
    digest text NOT NULL,
    logical_bytes bigint NOT NULL,
    capture_fence bigint NOT NULL,
    resolved_at timestamp with time zone DEFAULT now() NOT NULL,
    state text DEFAULT 'unresolved_generation'::text NOT NULL,
    CONSTRAINT hangar_logical_reservations_capture_fence_check CHECK ((capture_fence > 0)),
    CONSTRAINT hangar_logical_reservations_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT hangar_logical_reservations_logical_bytes_check CHECK ((logical_bytes >= 0)),
    CONSTRAINT hangar_logical_reservations_scope_check CHECK ((scope ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text)),
    CONSTRAINT hangar_logical_reservations_state_check CHECK ((state = ANY (ARRAY['unresolved_generation'::text, 'registered'::text, 'terminal'::text])))
);

CREATE TABLE public.hangar_no_capture_dispositions (
    handoff_id uuid NOT NULL,
    execution_id uuid NOT NULL,
    activation_epoch bigint NOT NULL,
    source_hold_id uuid NOT NULL,
    reason text NOT NULL,
    release_intent_id uuid NOT NULL,
    finish_acknowledgement jsonb,
    intent_recorded_at timestamp with time zone DEFAULT now() NOT NULL,
    release_acknowledged_at timestamp with time zone,
    release_acknowledgement jsonb,
    CONSTRAINT hangar_no_capture_dispositions_reason_check CHECK ((reason = ANY (ARRAY['authoritative_non_success'::text, 'unresolved'::text, 'lost'::text]))),
    CONSTRAINT hangar_no_capture_release_pairs CHECK (((release_acknowledged_at IS NULL) = (release_acknowledgement IS NULL))),
    CONSTRAINT hangar_no_capture_witness_matches_reason CHECK (((reason = 'authoritative_non_success'::text) = (finish_acknowledgement IS NOT NULL)))
);

CREATE TABLE public.hangar_output_receipts (
    receipt_id bigint NOT NULL,
    reservation_id uuid NOT NULL,
    lifecycle_id bigint NOT NULL,
    handoff_id uuid NOT NULL,
    activation_epoch bigint NOT NULL,
    receipt_key_id text NOT NULL,
    challenge_nonce text NOT NULL,
    marker_version text NOT NULL,
    algorithm text NOT NULL,
    claims jsonb NOT NULL,
    signature text NOT NULL,
    registered_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT hangar_output_receipts_algorithm_check CHECK ((algorithm = 'ed25519'::text)),
    CONSTRAINT hangar_output_receipts_marker_version_check CHECK ((marker_version = 'hangar-output-v1'::text)),
    CONSTRAINT hangar_output_receipts_receipt_key_id_check CHECK ((receipt_key_id <> ''::text)),
    CONSTRAINT hangar_output_receipts_signature_check CHECK ((signature <> ''::text))
);

CREATE TABLE public.hangar_policy_snapshots (
    id bigint NOT NULL,
    activation_epoch bigint NOT NULL,
    bucket_fingerprint text NOT NULL,
    metageneration bigint NOT NULL,
    policy_hash text NOT NULL,
    lifecycle_delete_rules integer NOT NULL,
    state text NOT NULL,
    observed_at timestamp with time zone DEFAULT now() NOT NULL,
    source text DEFAULT 'attestation'::text NOT NULL,
    CONSTRAINT hangar_policy_safe_has_no_delete_rules CHECK (((state <> 'safe'::text) OR (lifecycle_delete_rules = 0))),
    CONSTRAINT hangar_policy_snapshots_bucket_fingerprint_check CHECK ((bucket_fingerprint <> ''::text)),
    CONSTRAINT hangar_policy_snapshots_lifecycle_delete_rules_check CHECK ((lifecycle_delete_rules >= 0)),
    CONSTRAINT hangar_policy_snapshots_metageneration_check CHECK ((metageneration > 0)),
    CONSTRAINT hangar_policy_snapshots_policy_hash_check CHECK ((policy_hash <> ''::text)),
    CONSTRAINT hangar_policy_snapshots_source_check CHECK ((source = ANY (ARRAY['attestation'::text, 'runtime_observation'::text]))),
    CONSTRAINT hangar_policy_snapshots_state_check CHECK ((state = ANY (ARRAY['unknown'::text, 'safe'::text, 'at_risk'::text]))),
    CONSTRAINT hangar_runtime_observation_is_never_safe CHECK (((source <> 'runtime_observation'::text) OR (state <> 'safe'::text)))
);

CREATE TABLE public.hangar_policy_violations (
    id bigint NOT NULL,
    activation_epoch bigint NOT NULL,
    snapshot_id bigint,
    violation text NOT NULL,
    subject text NOT NULL,
    detail text DEFAULT ''::text NOT NULL,
    observed_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    CONSTRAINT hangar_policy_violations_detail_check CHECK ((octet_length(detail) <= 1024)),
    CONSTRAINT hangar_policy_violations_subject_check CHECK ((subject <> ''::text)),
    CONSTRAINT hangar_policy_violations_violation_check CHECK ((violation = ANY (ARRAY['lifecycle_delete_rule'::text, 'evidence_stale'::text, 'evidence_unreadable'::text, 'excess_role'::text, 'insufficient_role'::text, 'wrong_principal'::text, 'shared_bucket'::text, 'mixed_cohort'::text, 'unrecognised_role'::text, 'out_of_band_absence'::text, 'runtime_principal_denied'::text]))),
    CONSTRAINT hangar_violation_snapshot_matches_trigger CHECK (((violation = ANY (ARRAY['out_of_band_absence'::text, 'runtime_principal_denied'::text])) OR (snapshot_id IS NOT NULL)))
);

CREATE TABLE public.hangar_pre_reservation_cancel_dispositions (
    handoff_id uuid NOT NULL,
    execution_id uuid NOT NULL,
    activation_epoch bigint NOT NULL,
    source_hold_id uuid NOT NULL,
    source_reserved boolean NOT NULL,
    release_intent_id uuid,
    intent_recorded_at timestamp with time zone DEFAULT now() NOT NULL,
    release_acknowledged_at timestamp with time zone,
    release_acknowledgement jsonb,
    finalized_at timestamp with time zone,
    CONSTRAINT hangar_cancel_release_pairs CHECK (((release_acknowledged_at IS NULL) = (release_acknowledgement IS NULL))),
    CONSTRAINT hangar_cancel_reserved_finalizes_on_acknowledgement CHECK (((finalized_at IS NULL) OR (NOT source_reserved) OR (release_acknowledged_at IS NOT NULL))),
    CONSTRAINT hangar_cancel_reserved_records_intent CHECK (((NOT source_reserved) OR (release_intent_id IS NOT NULL))),
    CONSTRAINT hangar_cancel_unreserved_carries_no_release CHECK ((source_reserved OR ((release_intent_id IS NULL) AND (release_acknowledged_at IS NULL) AND (release_acknowledgement IS NULL))))
);

CREATE TABLE public.hangar_receipt_stat_challenges (
    nonce text NOT NULL,
    handoff_id uuid NOT NULL,
    reservation_id uuid NOT NULL,
    activation_epoch bigint NOT NULL,
    receipt_public_key_id text NOT NULL,
    scope text NOT NULL,
    digest text NOT NULL,
    generation bigint NOT NULL,
    capture_fence bigint NOT NULL,
    issued_at timestamp with time zone DEFAULT now() NOT NULL,
    not_after timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    CONSTRAINT hangar_challenge_window CHECK (((not_after > issued_at) AND ((not_after - issued_at) <= '00:05:00'::interval))),
    CONSTRAINT hangar_receipt_stat_challenges_capture_fence_check CHECK ((capture_fence > 0)),
    CONSTRAINT hangar_receipt_stat_challenges_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT hangar_receipt_stat_challenges_generation_check CHECK ((generation > 0)),
    CONSTRAINT hangar_receipt_stat_challenges_nonce_check CHECK (((octet_length(nonce) >= 16) AND (octet_length(nonce) <= 128))),
    CONSTRAINT hangar_receipt_stat_challenges_receipt_public_key_id_check CHECK ((receipt_public_key_id <> ''::text)),
    CONSTRAINT hangar_receipt_stat_challenges_scope_check CHECK ((scope ~ '^[a-z0-9][a-z0-9._-]{0,62}$'::text))
);

CREATE TABLE public.pipeline_run_output_cancellation_classifications (
    handoff_id uuid NOT NULL,
    worker_epoch bigint NOT NULL,
    classification text NOT NULL,
    hold_acknowledged boolean NOT NULL,
    disposition text,
    reservation_id uuid,
    observation jsonb NOT NULL,
    recorded_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT pipeline_run_output_cancellation_classific_classification_check CHECK ((classification = ANY (ARRAY['never_started'::text, 'executing'::text, 'authoritative_finish'::text, 'authoritative_stop'::text]))),
    CONSTRAINT pipeline_run_output_cancellation_classificat_worker_epoch_check CHECK ((worker_epoch > 0)),
    CONSTRAINT pipeline_run_output_cancellation_classificati_disposition_check CHECK ((disposition = ANY (ARRAY['capture'::text, 'no_capture'::text, 'pre_reservation_cancel'::text]))),
    CONSTRAINT pipeline_run_output_cancellation_classifications_check CHECK (((NOT (disposition IS DISTINCT FROM 'capture'::text)) = (reservation_id IS NOT NULL)))
);

CREATE TABLE public.pipeline_run_output_cancellation_evidence (
    handoff_id uuid NOT NULL,
    worker_epoch bigint NOT NULL,
    classification text NOT NULL,
    evidence jsonb NOT NULL,
    recorded_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT pipeline_run_output_cancellation_evidence_classification_check CHECK ((classification = ANY (ARRAY['never_started'::text, 'authoritative_finish'::text, 'authoritative_stop'::text]))),
    CONSTRAINT pipeline_run_output_cancellation_evidence_worker_epoch_check CHECK ((worker_epoch > 0))
);

CREATE TABLE public.pipeline_run_output_candidates (
    handoff_id uuid NOT NULL,
    claim_id uuid NOT NULL,
    scope text NOT NULL,
    digest text NOT NULL,
    generation bigint NOT NULL,
    receipt jsonb NOT NULL,
    CONSTRAINT pipeline_run_output_candidates_generation_check CHECK ((generation > 0))
);

CREATE TABLE public.pipeline_run_output_discards (
    handoff_id uuid NOT NULL,
    reason text NOT NULL,
    CONSTRAINT pipeline_run_output_discards_reason_check CHECK ((reason = ANY (ARRAY['build_aborted'::text, 'run_cancelled'::text])))
);

CREATE TABLE public.pipeline_run_output_finishes (
    handoff_id uuid NOT NULL,
    disposition text NOT NULL,
    decision jsonb NOT NULL,
    producer_checkpoint_id text,
    reservation_id uuid,
    CONSTRAINT pipeline_run_output_finishes_check CHECK (((disposition = 'capture'::text) = (producer_checkpoint_id IS NOT NULL))),
    CONSTRAINT pipeline_run_output_finishes_check1 CHECK (((disposition = 'capture'::text) = (reservation_id IS NOT NULL))),
    CONSTRAINT pipeline_run_output_finishes_disposition_check CHECK ((disposition = ANY (ARRAY['capture'::text, 'no_capture'::text, 'pre_reservation_cancel'::text])))
);

CREATE TABLE public.pipeline_run_output_holds (
    handoff_id uuid NOT NULL,
    acknowledgement jsonb NOT NULL
);

CREATE TABLE public.pipeline_run_output_releases (
    handoff_id uuid NOT NULL,
    acknowledgement jsonb NOT NULL
);

CREATE TABLE public.pipeline_run_output_starts (
    run_id bigint NOT NULL,
    build_id integer NOT NULL,
    task_id uuid NOT NULL,
    result_name text NOT NULL,
    task_name text NOT NULL,
    node_name text NOT NULL,
    node_uid text NOT NULL,
    handoff_id uuid NOT NULL,
    source_requested_at timestamp with time zone,
    CONSTRAINT pipeline_run_output_starts_node_name_check CHECK ((node_name <> ''::text)),
    CONSTRAINT pipeline_run_output_starts_node_uid_check CHECK ((node_uid <> ''::text))
);

CREATE SEQUENCE public.hangar_capture_announcements_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.hangar_capture_announcements_id_seq OWNED BY public.hangar_capture_announcements.id;

CREATE SEQUENCE public.hangar_output_receipts_receipt_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.hangar_output_receipts_receipt_id_seq OWNED BY public.hangar_output_receipts.receipt_id;

CREATE SEQUENCE public.hangar_policy_snapshots_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.hangar_policy_snapshots_id_seq OWNED BY public.hangar_policy_snapshots.id;

CREATE SEQUENCE public.hangar_policy_violations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.hangar_policy_violations_id_seq OWNED BY public.hangar_policy_violations.id;

ALTER TABLE ONLY public.hangar_capture_announcements ALTER COLUMN id SET DEFAULT nextval('public.hangar_capture_announcements_id_seq'::regclass);

ALTER TABLE ONLY public.hangar_output_receipts ALTER COLUMN receipt_id SET DEFAULT nextval('public.hangar_output_receipts_receipt_id_seq'::regclass);

ALTER TABLE ONLY public.hangar_policy_snapshots ALTER COLUMN id SET DEFAULT nextval('public.hangar_policy_snapshots_id_seq'::regclass);

ALTER TABLE ONLY public.hangar_policy_violations ALTER COLUMN id SET DEFAULT nextval('public.hangar_policy_violations_id_seq'::regclass);

ALTER TABLE ONLY public.hangar_capture_announcements
    ADD CONSTRAINT hangar_announcement_once UNIQUE (handoff_id, kind);

ALTER TABLE ONLY public.hangar_capture_announcements
    ADD CONSTRAINT hangar_capture_announcements_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.hangar_capture_attempt_leases
    ADD CONSTRAINT hangar_capture_attempt_leases_pkey PRIMARY KEY (reservation_id);

ALTER TABLE ONLY public.hangar_capture_reservations
    ADD CONSTRAINT hangar_capture_reservations_handoff_id_key UNIQUE (handoff_id);

ALTER TABLE ONLY public.hangar_capture_reservations
    ADD CONSTRAINT hangar_capture_reservations_pkey PRIMARY KEY (reservation_id);

ALTER TABLE ONLY public.hangar_handoff_dispositions
    ADD CONSTRAINT hangar_handoff_dispositions_pkey PRIMARY KEY (handoff_id);

ALTER TABLE ONLY public.hangar_handoff_predeclarations
    ADD CONSTRAINT hangar_handoff_predeclarations_execution_id_key UNIQUE (execution_id);

ALTER TABLE ONLY public.hangar_handoff_predeclarations
    ADD CONSTRAINT hangar_handoff_predeclarations_pkey PRIMARY KEY (handoff_id);

ALTER TABLE ONLY public.hangar_handoff_predeclarations
    ADD CONSTRAINT hangar_handoff_predeclarations_source_lease_id_key UNIQUE (source_hold_id);

ALTER TABLE ONLY public.hangar_logical_reservations
    ADD CONSTRAINT hangar_logical_reservations_pkey PRIMARY KEY (reservation_id);

ALTER TABLE ONLY public.hangar_no_capture_dispositions
    ADD CONSTRAINT hangar_no_capture_dispositions_pkey PRIMARY KEY (handoff_id);

ALTER TABLE ONLY public.hangar_no_capture_dispositions
    ADD CONSTRAINT hangar_no_capture_dispositions_release_intent_id_key UNIQUE (release_intent_id);

ALTER TABLE ONLY public.hangar_output_receipts
    ADD CONSTRAINT hangar_output_receipts_challenge_nonce_key UNIQUE (challenge_nonce);

ALTER TABLE ONLY public.hangar_output_receipts
    ADD CONSTRAINT hangar_output_receipts_pkey PRIMARY KEY (receipt_id);

ALTER TABLE ONLY public.hangar_output_receipts
    ADD CONSTRAINT hangar_output_receipts_reservation_id_key UNIQUE (reservation_id);

ALTER TABLE ONLY public.hangar_policy_snapshots
    ADD CONSTRAINT hangar_policy_snapshots_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.hangar_policy_violations
    ADD CONSTRAINT hangar_policy_violations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.hangar_pre_reservation_cancel_dispositions
    ADD CONSTRAINT hangar_pre_reservation_cancel_disposition_release_intent_id_key UNIQUE (release_intent_id);

ALTER TABLE ONLY public.hangar_pre_reservation_cancel_dispositions
    ADD CONSTRAINT hangar_pre_reservation_cancel_dispositions_pkey PRIMARY KEY (handoff_id);

ALTER TABLE ONLY public.hangar_receipt_stat_challenges
    ADD CONSTRAINT hangar_receipt_stat_challenges_pkey PRIMARY KEY (nonce);

ALTER TABLE ONLY public.pipeline_run_output_cancellation_classifications
    ADD CONSTRAINT pipeline_run_output_cancellation_classifications_pkey PRIMARY KEY (handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_cancellation_evidence
    ADD CONSTRAINT pipeline_run_output_cancellation_evidence_pkey PRIMARY KEY (handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_candidates
    ADD CONSTRAINT pipeline_run_output_candidates_claim_id_key UNIQUE (claim_id);

ALTER TABLE ONLY public.pipeline_run_output_candidates
    ADD CONSTRAINT pipeline_run_output_candidates_pkey PRIMARY KEY (handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_discards
    ADD CONSTRAINT pipeline_run_output_discards_pkey PRIMARY KEY (handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_finishes
    ADD CONSTRAINT pipeline_run_output_finishes_pkey PRIMARY KEY (handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_finishes
    ADD CONSTRAINT pipeline_run_output_finishes_producer_checkpoint_id_key UNIQUE (producer_checkpoint_id);

ALTER TABLE ONLY public.pipeline_run_output_finishes
    ADD CONSTRAINT pipeline_run_output_finishes_reservation_id_key UNIQUE (reservation_id);

ALTER TABLE ONLY public.pipeline_run_output_holds
    ADD CONSTRAINT pipeline_run_output_holds_pkey PRIMARY KEY (handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_releases
    ADD CONSTRAINT pipeline_run_output_releases_pkey PRIMARY KEY (handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_starts
    ADD CONSTRAINT pipeline_run_output_starts_handoff_id_key UNIQUE (handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_starts
    ADD CONSTRAINT pipeline_run_output_starts_pkey PRIMARY KEY (build_id, task_id);

CREATE INDEX hangar_capture_announcements_handoff_idx ON public.hangar_capture_announcements USING btree (handoff_id, id);

CREATE INDEX hangar_capture_attempt_leases_expiry_idx ON public.hangar_capture_attempt_leases USING btree (expires_at);

CREATE INDEX hangar_capture_reservations_due_idx ON public.hangar_capture_reservations USING btree (capture_deadline_at) WHERE (state = ANY (ARRAY['unresolved'::text, 'resolved'::text]));

CREATE INDEX hangar_logical_reservations_correlation_idx ON public.hangar_logical_reservations USING btree (scope, digest);

CREATE INDEX hangar_logical_reservations_unresolved_idx ON public.hangar_logical_reservations USING btree (scope, digest) WHERE (state = 'unresolved_generation'::text);

CREATE INDEX hangar_no_capture_release_due_idx ON public.hangar_no_capture_dispositions USING btree (intent_recorded_at) WHERE (release_acknowledged_at IS NULL);

CREATE INDEX hangar_output_receipts_handoff_idx ON public.hangar_output_receipts USING btree (handoff_id);

CREATE INDEX hangar_policy_snapshots_latest_idx ON public.hangar_policy_snapshots USING btree (activation_epoch, observed_at DESC);

CREATE UNIQUE INDEX hangar_policy_violations_one_open_idx ON public.hangar_policy_violations USING btree (activation_epoch, violation, subject) WHERE (resolved_at IS NULL);

CREATE INDEX hangar_pre_reservation_cancel_due_idx ON public.hangar_pre_reservation_cancel_dispositions USING btree (intent_recorded_at) WHERE (finalized_at IS NULL);

CREATE INDEX hangar_receipt_stat_challenges_open_idx ON public.hangar_receipt_stat_challenges USING btree (not_after) WHERE (consumed_at IS NULL);

CREATE INDEX pipeline_run_output_starts_run ON public.pipeline_run_output_starts USING btree (run_id, build_id);

CREATE OR REPLACE FUNCTION public.check_run_cancel_classified() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM pipeline_run_output_cancellation_classifications WHERE handoff_id=NEW.handoff_id) THEN
  RAISE EXCEPTION 'Run cancellation outcome requires prior handoff classification';
 END IF;
 RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.check_run_cancel_source_proof() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
 IF NEW.disposition='pre_reservation_cancel' AND
    EXISTS(SELECT 1 FROM hangar_handoff_predeclarations WHERE handoff_id=NEW.handoff_id AND reserved_at IS NOT NULL) AND
    NOT EXISTS(SELECT 1 FROM pipeline_run_output_cancellation_evidence WHERE handoff_id=NEW.handoff_id) THEN
  RAISE EXCEPTION 'Run source cancellation requires exact execution evidence';
 END IF;
 RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.check_run_candidate_claim_lifetime() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE owner bigint;
BEGIN
 SELECT s.run_id INTO owner FROM pipeline_run_output_candidates c JOIN pipeline_run_output_starts s USING(handoff_id) WHERE c.claim_id=NEW.claim_id;
 IF owner IS NOT NULL THEN PERFORM check_run_result_claims(owner); END IF;
 RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.check_run_output_candidate() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    handoff uuid := NEW.handoff_id;
    candidate pipeline_run_output_candidates%ROWTYPE;
    registered boolean;
    settled boolean;
    discarded boolean;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pipeline_run_output_starts WHERE handoff_id=handoff) THEN
        RETURN NULL;
    END IF;
    SELECT state='registered', release_acknowledged_at IS NOT NULL INTO registered,settled
      FROM hangar_capture_reservations WHERE handoff_id=handoff;
    SELECT * INTO candidate FROM pipeline_run_output_candidates WHERE handoff_id=handoff;
    SELECT EXISTS(SELECT 1 FROM pipeline_run_output_discards WHERE handoff_id=handoff) INTO discarded;
    IF (candidate.handoff_id IS NOT NULL AND discarded) OR
       coalesce(registered AND settled,false) IS DISTINCT FROM (candidate.handoff_id IS NOT NULL OR discarded) THEN
        RAISE EXCEPTION 'Run candidate and successful capture settlement must commit together';
    END IF;
    IF candidate.handoff_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM hangar_claims c JOIN hangar_exact_lifecycles l ON l.id=c.lifecycle_id
        JOIN hangar_output_receipts r ON r.lifecycle_id=l.id AND r.handoff_id=handoff
        WHERE c.claim_id=candidate.claim_id AND c.released_at IS NULL
        AND c.consumer_binding_id=handoff::text
        AND l.scope=candidate.scope AND l.digest=candidate.digest AND l.generation=candidate.generation
    ) THEN
        RAISE EXCEPTION 'Run candidate requires its matching active exact-generation claim';
    END IF;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.check_run_output_disposition() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    decision pipeline_run_output_finishes%ROWTYPE;
    branch text;
    reservation hangar_capture_reservations%ROWTYPE;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pipeline_run_output_starts WHERE handoff_id=NEW.handoff_id) THEN
        RETURN NULL;
    END IF;
    SELECT * INTO decision FROM pipeline_run_output_finishes WHERE handoff_id=NEW.handoff_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Run output disposition has no owning Run decision';
    END IF;
    SELECT disposition INTO branch FROM hangar_handoff_dispositions WHERE handoff_id=NEW.handoff_id;
    IF branch IS DISTINCT FROM decision.disposition THEN
        RAISE EXCEPTION 'Run and Hangar output dispositions differ';
    END IF;
    IF branch = 'capture' THEN
        SELECT * INTO reservation FROM hangar_capture_reservations WHERE handoff_id=NEW.handoff_id;
        IF NOT FOUND OR reservation.reservation_id IS DISTINCT FROM decision.reservation_id
           OR reservation.producer_checkpoint_id IS DISTINCT FROM decision.producer_checkpoint_id THEN
            RAISE EXCEPTION 'Run producer checkpoint does not match the capture reservation';
        END IF;
    END IF;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.check_run_output_release() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    handoff uuid := NEW.handoff_id;
    retained jsonb;
    generic jsonb;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pipeline_run_output_starts WHERE handoff_id=handoff) THEN
        RETURN NULL;
    END IF;
    SELECT acknowledgement INTO retained FROM pipeline_run_output_releases WHERE handoff_id=handoff;
    SELECT coalesce(c.release_acknowledgement,n.release_acknowledgement,x.release_acknowledgement)
      INTO generic FROM hangar_handoff_predeclarations h
      LEFT JOIN hangar_capture_reservations c USING(handoff_id)
      LEFT JOIN hangar_no_capture_dispositions n USING(handoff_id)
      LEFT JOIN hangar_pre_reservation_cancel_dispositions x USING(handoff_id)
      WHERE h.handoff_id=handoff;
    IF generic IS DISTINCT FROM retained THEN
        RAISE EXCEPTION 'Run and Hangar source release acknowledgements must commit together';
    END IF;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.check_run_result_claims(checked_run bigint) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE r pipeline_runs%ROWTYPE;
BEGIN
 SELECT * INTO r FROM pipeline_runs WHERE id=checked_run;
 IF r.id IS NULL THEN RETURN; END IF;
 IF EXISTS (
  SELECT 1 FROM pipeline_run_output_candidates c JOIN pipeline_run_output_starts s USING(handoff_id)
  JOIN hangar_claims claim USING(claim_id)
  WHERE s.run_id=r.id AND ((claim.released_at IS NULL) IS DISTINCT FROM
   (r.status='running' OR EXISTS(SELECT 1 FROM jsonb_each(r.result_manifest) item
     WHERE item.value->>'claim_id'=c.claim_id::text)))
 ) THEN RAISE EXCEPTION 'Run publication and candidate claim lifetime must commit together'; END IF;
 IF r.status='running' THEN RETURN; END IF;
 IF EXISTS(SELECT 1 FROM pipeline_run_output_starts WHERE run_id=r.id AND NOT run_output_closed(handoff_id)) OR
    EXISTS(SELECT 1 FROM builds WHERE pipeline_run_id=r.id AND status IN ('pending','started')) THEN
  RAISE EXCEPTION 'Run terminal publication requires closed producer and build work';
 END IF;
 IF EXISTS (
  SELECT 1 FROM jsonb_each(r.result_manifest) item WHERE NOT EXISTS (
   SELECT 1 FROM pipeline_run_output_candidates c JOIN pipeline_run_output_starts s USING(handoff_id)
   WHERE s.run_id=r.id AND s.result_name=item.key AND item.value=jsonb_build_object(
    'claim_id',c.claim_id::text,'ref',jsonb_build_object('scope',c.scope,'digest',c.digest,'generation',c.generation))
  )
 ) THEN RAISE EXCEPTION 'Run manifest requires its exact retained candidates'; END IF;
END;
$$;

CREATE OR REPLACE FUNCTION public.hangar_activation_grants() RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    role_name text := hangar_activation_role_name();
    readable  text;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
        RETURN;
    END IF;
    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %I', role_name);
    EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %I', role_name);

    -- begin inserts the epoch; attest, enable and drain move its facets.
    EXECUTE format('GRANT SELECT, INSERT ON hangar_output_activation_epochs TO %I', role_name);
    EXECUTE format('GRANT UPDATE (base_state, base_attestation, protocol_version, ledger_version, '
        'cohort_digest, output_state, output_attestation, receipt_public_key_id, '
        'receipt_key_valid_from, receipt_key_valid_until, materialization_key_id, '
        'bucket_fingerprint, derived_namespace, revision, updated_at) '
        'ON hangar_output_activation_epochs TO %I', role_name);

    -- enable checks the schema and the open findings; drain reads the residue.
    FOREACH readable IN ARRAY ARRAY[
        'migrations_history',
        'hangar_handoff_predeclarations', 'hangar_handoff_dispositions',
        'hangar_capture_reservations', 'hangar_exact_lifecycles', 'hangar_claims',
        'hangar_read_leases', 'hangar_reclaim_jobs', 'hangar_inventory_debt'
    ] LOOP
        EXECUTE format('GRANT SELECT ON %I TO %I', readable, role_name);
    END LOOP;

    -- reconcile-integrity closes a finding and keeps its history.
    EXECUTE format('GRANT SELECT ON hangar_policy_violations TO %I', role_name);
    EXECUTE format('GRANT UPDATE (resolved_at) ON hangar_policy_violations TO %I', role_name);
END $$;

CREATE OR REPLACE FUNCTION public.hangar_capture_lease_fence() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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

CREATE OR REPLACE FUNCTION public.hangar_capture_release_is_one_way() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.release_acknowledged_at IS NOT NULL
        AND NEW.release_acknowledged_at IS DISTINCT FROM OLD.release_acknowledged_at THEN
        RAISE EXCEPTION 'hangar: the source release for reservation % is already acknowledged; it is acknowledged once',
            OLD.reservation_id
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION public.hangar_capture_state_is_terminal() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.state IS DISTINCT FROM OLD.state
        AND OLD.state IN ('registered', 'failed', 'cancelled') THEN
        RAISE EXCEPTION 'hangar: capture reservation % is %, which is terminal; it cannot become %',
            OLD.reservation_id, OLD.state, NEW.state
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION public.hangar_challenge_one_use() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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

CREATE OR REPLACE FUNCTION public.hangar_check_create_follows_resolution() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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

CREATE OR REPLACE FUNCTION public.hangar_check_handoff_branch() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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

CREATE OR REPLACE FUNCTION public.hangar_check_logical_resolution() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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

CREATE OR REPLACE FUNCTION public.hangar_check_policy_admission() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM hangar_policy_violations
               WHERE activation_epoch = NEW.activation_epoch AND resolved_at IS NULL
                 AND violation IN ('out_of_band_absence', 'runtime_principal_denied')) THEN
        RAISE EXCEPTION 'hangar: epoch % has unresolved storage integrity findings; repair the cause and explicitly reconcile each finding before admitting new work', NEW.activation_epoch
            USING ERRCODE = 'JB002';
    END IF;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION public.hangar_check_receipt_admission() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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

CREATE OR REPLACE FUNCTION public.hangar_check_reclaim_admission() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM hangar_policy_violations
               WHERE activation_epoch = NEW.activation_epoch AND resolved_at IS NULL
                 AND violation = 'out_of_band_absence') THEN
        RAISE EXCEPTION 'hangar: epoch % has unexplained object loss; reclamation requires explicit reconciliation', NEW.activation_epoch
            USING ERRCODE = 'JB002';
    END IF;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION public.hangar_check_reclaim_exclusion() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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

CREATE OR REPLACE FUNCTION public.hangar_check_stage_two() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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

CREATE OR REPLACE FUNCTION public.hangar_disposition_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'hangar: handoff % is already dispositioned as %; the three branches exclude one another permanently',
        OLD.handoff_id, OLD.disposition
            USING ERRCODE = 'JB001';
END $$;

CREATE OR REPLACE FUNCTION public.hangar_logical_reservation_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.scope <> OLD.scope OR NEW.digest <> OLD.digest OR NEW.resolved_at <> OLD.resolved_at THEN
        RAISE EXCEPTION 'hangar: the logical identity of reservation % is immutable; a claim protects an immutable generation and cannot float to replacement content',
            OLD.reservation_id
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION public.hangar_policy_violation_resolution() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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

CREATE OR REPLACE FUNCTION public.hangar_predeclaration_immutable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
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

CREATE OR REPLACE FUNCTION public.immutable_run_credential_handoff() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF run_team_purge_active() THEN
            RETURN OLD;
        END IF;
        RAISE EXCEPTION 'A Run credential handoff cannot be reseeded';
    END IF;
    IF TG_OP = 'UPDATE' AND (
        (NEW.run_id, NEW.handoff_id, NEW.claimed_at) IS DISTINCT FROM (OLD.run_id, OLD.handoff_id, OLD.claimed_at)
        OR (OLD.ready_at IS NOT NULL AND NEW.ready_at IS DISTINCT FROM OLD.ready_at)
    ) THEN
        RAISE EXCEPTION 'Run credential delivery identity and readiness are immutable';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pipeline_run_output_starts s JOIN pipeline_run_invocations i ON i.run_id=s.run_id
        WHERE s.handoff_id=NEW.handoff_id AND s.run_id=NEW.run_id) THEN
        RAISE EXCEPTION 'A credential handoff must belong to its invoked Run';
    END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION public.immutable_run_output_evidence() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF run_team_purge_active() THEN
            RETURN OLD;
        END IF;
        IF run_check_gc_active() THEN
            -- Nested: OLD's columns differ by table, so each test is reached
            -- only for the table that has them.
            IF TG_TABLE_NAME = 'pipeline_run_executions' THEN
                IF OLD.kind IN ('check', 'get') AND OLD.handoff_id IS NULL
                   AND EXISTS (SELECT 1 FROM pipeline_run_execution_closures c
                       WHERE c.execution_id = OLD.execution_id AND c.execution_fence = OLD.execution_fence)
                   AND EXISTS (SELECT 1 FROM builds b WHERE b.id = OLD.build_id AND b.completed
                       AND b.pipeline_run_id IS NOT NULL AND b.run_job_name IS NULL)
                   AND NOT EXISTS (SELECT 1 FROM pipeline_run_output_starts s WHERE s.build_id = OLD.build_id) THEN
                    RETURN OLD;
                END IF;
            ELSIF TG_TABLE_NAME IN ('pipeline_run_execution_starts', 'pipeline_run_execution_closures') THEN
                IF NOT EXISTS (SELECT 1 FROM pipeline_run_executions e
                       WHERE e.execution_id = OLD.execution_id AND e.execution_fence = OLD.execution_fence) THEN
                    RETURN OLD;
                END IF;
            END IF;
            RAISE EXCEPTION 'check collection deletes only a closed check or image get execution of a completed Run check build, with its witnesses';
        END IF;
        RAISE EXCEPTION 'Run evidence is deleted only by its team''s purge';
    END IF;
    IF NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'Run output evidence is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.immutable_run_output_start() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF run_team_purge_active() THEN
            RETURN OLD;
        END IF;
        RAISE EXCEPTION 'Run output start identity is deleted only by its team''s purge';
    END IF;
    IF (to_jsonb(NEW) - 'source_requested_at') IS DISTINCT FROM (to_jsonb(OLD) - 'source_requested_at')
       OR (OLD.source_requested_at IS NOT NULL AND NEW.source_requested_at IS DISTINCT FROM OLD.source_requested_at) THEN
        RAISE EXCEPTION 'Run output start identity and committed source dispatch are immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.run_execution_closed(build bigint) RETURNS boolean
    LANGUAGE sql STABLE
    AS $$
 SELECT NOT EXISTS(SELECT 1 FROM pipeline_run_executions e
 LEFT JOIN pipeline_run_execution_closures c USING(execution_id,execution_fence)
 WHERE e.build_id=build AND c.execution_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM pipeline_run_output_cancellation_evidence x
  WHERE x.handoff_id=e.handoff_id
  AND (x.classification<>'never_started' OR NOT EXISTS(
   SELECT 1 FROM pipeline_run_execution_starts s WHERE s.execution_id=e.execution_id AND s.execution_fence=e.execution_fence))));
$$;

CREATE OR REPLACE FUNCTION public.run_output_closed(handoff uuid) RETURNS boolean
    LANGUAGE sql STABLE
    AS $$
 SELECT coalesce((SELECT CASE f.disposition
  WHEN 'capture' THEN c.state IN ('registered','failed','cancelled') AND c.release_acknowledged_at IS NOT NULL AND l.handoff_id IS NOT NULL
  WHEN 'no_capture' THEN n.release_acknowledged_at IS NOT NULL AND l.handoff_id IS NOT NULL
  WHEN 'pre_reservation_cancel' THEN x.finalized_at IS NOT NULL AND (NOT x.source_reserved OR l.handoff_id IS NOT NULL)
  ELSE false END
 FROM pipeline_run_output_finishes f
 LEFT JOIN pipeline_run_output_releases l USING(handoff_id)
 LEFT JOIN hangar_capture_reservations c USING(handoff_id)
 LEFT JOIN hangar_no_capture_dispositions n USING(handoff_id)
 LEFT JOIN hangar_pre_reservation_cancel_dispositions x USING(handoff_id)
 WHERE f.handoff_id=handoff),false);
$$;

ALTER TABLE ONLY public.hangar_capture_announcements
    ADD CONSTRAINT hangar_capture_announcements_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.hangar_handoff_predeclarations(handoff_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_capture_attempt_leases
    ADD CONSTRAINT hangar_capture_attempt_leases_reservation_id_fkey FOREIGN KEY (reservation_id) REFERENCES public.hangar_capture_reservations(reservation_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_capture_reservations
    ADD CONSTRAINT hangar_capture_reservations_activation_epoch_fkey FOREIGN KEY (activation_epoch) REFERENCES public.hangar_output_activation_epochs(epoch_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_capture_reservations
    ADD CONSTRAINT hangar_capture_reservations_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.hangar_handoff_dispositions(handoff_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_handoff_dispositions
    ADD CONSTRAINT hangar_handoff_dispositions_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.hangar_handoff_predeclarations(handoff_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_handoff_predeclarations
    ADD CONSTRAINT hangar_handoff_predeclarations_activation_epoch_fkey FOREIGN KEY (activation_epoch) REFERENCES public.hangar_output_activation_epochs(epoch_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_logical_reservations
    ADD CONSTRAINT hangar_logical_reservations_reservation_id_fkey FOREIGN KEY (reservation_id) REFERENCES public.hangar_capture_reservations(reservation_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_no_capture_dispositions
    ADD CONSTRAINT hangar_no_capture_dispositions_activation_epoch_fkey FOREIGN KEY (activation_epoch) REFERENCES public.hangar_output_activation_epochs(epoch_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_no_capture_dispositions
    ADD CONSTRAINT hangar_no_capture_dispositions_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.hangar_handoff_dispositions(handoff_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_output_receipts
    ADD CONSTRAINT hangar_output_receipts_activation_epoch_fkey FOREIGN KEY (activation_epoch) REFERENCES public.hangar_output_activation_epochs(epoch_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_output_receipts
    ADD CONSTRAINT hangar_output_receipts_challenge_nonce_fkey FOREIGN KEY (challenge_nonce) REFERENCES public.hangar_receipt_stat_challenges(nonce) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_output_receipts
    ADD CONSTRAINT hangar_output_receipts_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.hangar_handoff_predeclarations(handoff_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_output_receipts
    ADD CONSTRAINT hangar_output_receipts_lifecycle_id_fkey FOREIGN KEY (lifecycle_id) REFERENCES public.hangar_exact_lifecycles(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_output_receipts
    ADD CONSTRAINT hangar_output_receipts_reservation_id_fkey FOREIGN KEY (reservation_id) REFERENCES public.hangar_logical_reservations(reservation_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_policy_snapshots
    ADD CONSTRAINT hangar_policy_snapshots_activation_epoch_fkey FOREIGN KEY (activation_epoch) REFERENCES public.hangar_output_activation_epochs(epoch_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_policy_violations
    ADD CONSTRAINT hangar_policy_violations_activation_epoch_fkey FOREIGN KEY (activation_epoch) REFERENCES public.hangar_output_activation_epochs(epoch_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_policy_violations
    ADD CONSTRAINT hangar_policy_violations_snapshot_id_fkey FOREIGN KEY (snapshot_id) REFERENCES public.hangar_policy_snapshots(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_pre_reservation_cancel_dispositions
    ADD CONSTRAINT hangar_pre_reservation_cancel_disposition_activation_epoch_fkey FOREIGN KEY (activation_epoch) REFERENCES public.hangar_output_activation_epochs(epoch_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_pre_reservation_cancel_dispositions
    ADD CONSTRAINT hangar_pre_reservation_cancel_dispositions_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.hangar_handoff_dispositions(handoff_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_receipt_stat_challenges
    ADD CONSTRAINT hangar_receipt_stat_challenges_activation_epoch_fkey FOREIGN KEY (activation_epoch) REFERENCES public.hangar_output_activation_epochs(epoch_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_receipt_stat_challenges
    ADD CONSTRAINT hangar_receipt_stat_challenges_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.hangar_handoff_predeclarations(handoff_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.hangar_receipt_stat_challenges
    ADD CONSTRAINT hangar_receipt_stat_challenges_reservation_id_fkey FOREIGN KEY (reservation_id) REFERENCES public.hangar_capture_reservations(reservation_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.pipeline_run_credential_handoffs
    ADD CONSTRAINT pipeline_run_credential_handoffs_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.pipeline_run_output_starts(handoff_id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.pipeline_run_executions
    ADD CONSTRAINT pipeline_run_executions_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.pipeline_run_output_starts(handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_cancellation_classifications
    ADD CONSTRAINT pipeline_run_output_cancellation_classification_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.pipeline_run_output_starts(handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_cancellation_evidence
    ADD CONSTRAINT pipeline_run_output_cancellation_evidence_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.pipeline_run_output_starts(handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_candidates
    ADD CONSTRAINT pipeline_run_output_candidates_claim_id_fkey FOREIGN KEY (claim_id) REFERENCES public.hangar_claims(claim_id);

ALTER TABLE ONLY public.pipeline_run_output_candidates
    ADD CONSTRAINT pipeline_run_output_candidates_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.pipeline_run_output_finishes(handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_discards
    ADD CONSTRAINT pipeline_run_output_discards_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.pipeline_run_output_finishes(handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_finishes
    ADD CONSTRAINT pipeline_run_output_finishes_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.pipeline_run_output_starts(handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_finishes
    ADD CONSTRAINT pipeline_run_output_finishes_reservation_id_fkey FOREIGN KEY (reservation_id) REFERENCES public.hangar_capture_reservations(reservation_id);

ALTER TABLE ONLY public.pipeline_run_output_holds
    ADD CONSTRAINT pipeline_run_output_holds_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.pipeline_run_output_starts(handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_releases
    ADD CONSTRAINT pipeline_run_output_releases_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.pipeline_run_output_finishes(handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_starts
    ADD CONSTRAINT pipeline_run_output_starts_handoff_id_fkey FOREIGN KEY (handoff_id) REFERENCES public.hangar_handoff_predeclarations(handoff_id);

ALTER TABLE ONLY public.pipeline_run_output_starts
    ADD CONSTRAINT pipeline_run_output_starts_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.pipeline_runs(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.pipeline_run_output_starts
    ADD CONSTRAINT pipeline_run_output_starts_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.pipeline_template_task_identities(task_id);

CREATE TRIGGER hangar_capture_lease_fence_guard BEFORE UPDATE ON public.hangar_capture_attempt_leases FOR EACH ROW EXECUTE FUNCTION public.hangar_capture_lease_fence();

CREATE TRIGGER hangar_capture_release_one_way_guard BEFORE UPDATE ON public.hangar_capture_reservations FOR EACH ROW EXECUTE FUNCTION public.hangar_capture_release_is_one_way();

CREATE TRIGGER hangar_challenge_one_use_guard BEFORE UPDATE ON public.hangar_receipt_stat_challenges FOR EACH ROW EXECUTE FUNCTION public.hangar_challenge_one_use();

CREATE CONSTRAINT TRIGGER hangar_create_follows_resolution AFTER INSERT OR UPDATE ON public.hangar_capture_reservations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.hangar_check_create_follows_resolution();

CREATE TRIGGER hangar_disposition_immutability_guard BEFORE DELETE OR UPDATE ON public.hangar_handoff_dispositions FOR EACH ROW EXECUTE FUNCTION public.hangar_disposition_immutable();

CREATE CONSTRAINT TRIGGER hangar_handoff_branch_cardinality AFTER INSERT OR DELETE OR UPDATE ON public.hangar_capture_reservations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.hangar_check_handoff_branch();

CREATE CONSTRAINT TRIGGER hangar_handoff_branch_cardinality AFTER INSERT OR DELETE OR UPDATE ON public.hangar_handoff_dispositions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.hangar_check_handoff_branch();

CREATE CONSTRAINT TRIGGER hangar_handoff_branch_cardinality AFTER INSERT OR DELETE OR UPDATE ON public.hangar_no_capture_dispositions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.hangar_check_handoff_branch();

CREATE CONSTRAINT TRIGGER hangar_handoff_branch_cardinality AFTER INSERT OR DELETE OR UPDATE ON public.hangar_pre_reservation_cancel_dispositions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.hangar_check_handoff_branch();

CREATE TRIGGER hangar_logical_reservation_immutability_guard BEFORE UPDATE ON public.hangar_logical_reservations FOR EACH ROW EXECUTE FUNCTION public.hangar_logical_reservation_immutable();

CREATE CONSTRAINT TRIGGER hangar_logical_resolution_is_fenced AFTER INSERT OR UPDATE ON public.hangar_logical_reservations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.hangar_check_logical_resolution();

CREATE CONSTRAINT TRIGGER hangar_policy_admits_new_protection AFTER INSERT ON public.hangar_capture_reservations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.hangar_check_policy_admission();

CREATE CONSTRAINT TRIGGER hangar_policy_admits_new_protection AFTER INSERT ON public.hangar_handoff_predeclarations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.hangar_check_policy_admission();

CREATE TRIGGER hangar_policy_violation_resolution_guard BEFORE UPDATE ON public.hangar_policy_violations FOR EACH ROW EXECUTE FUNCTION public.hangar_policy_violation_resolution();

CREATE TRIGGER hangar_predeclaration_immutability_guard BEFORE UPDATE ON public.hangar_handoff_predeclarations FOR EACH ROW EXECUTE FUNCTION public.hangar_predeclaration_immutable();

CREATE CONSTRAINT TRIGGER hangar_receipt_admission AFTER INSERT OR UPDATE ON public.hangar_output_receipts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.hangar_check_receipt_admission();

CREATE TRIGGER hangar_reservation_transition BEFORE UPDATE ON public.hangar_capture_reservations FOR EACH ROW EXECUTE FUNCTION public.hangar_capture_state_is_terminal();

CREATE CONSTRAINT TRIGGER hangar_stage_two_matches_predeclaration AFTER INSERT OR UPDATE ON public.hangar_capture_reservations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.hangar_check_stage_two();

CREATE CONSTRAINT TRIGGER run_cancel_classified AFTER INSERT ON public.pipeline_run_output_cancellation_evidence DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.check_run_cancel_classified();

CREATE CONSTRAINT TRIGGER run_cancel_source_proof AFTER INSERT ON public.pipeline_run_output_finishes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.check_run_cancel_source_proof();

CREATE TRIGGER run_output_cancellation_classification_immutable BEFORE DELETE OR UPDATE ON public.pipeline_run_output_cancellation_classifications FOR EACH ROW EXECUTE FUNCTION public.immutable_run_output_evidence();

CREATE TRIGGER run_output_cancellation_evidence_immutable BEFORE DELETE OR UPDATE ON public.pipeline_run_output_cancellation_evidence FOR EACH ROW EXECUTE FUNCTION public.immutable_run_output_evidence();

CREATE TRIGGER run_output_candidate_immutable BEFORE DELETE OR UPDATE ON public.pipeline_run_output_candidates FOR EACH ROW EXECUTE FUNCTION public.immutable_run_output_evidence();

CREATE CONSTRAINT TRIGGER run_output_candidate_match AFTER UPDATE OF release_acknowledged_at ON public.hangar_capture_reservations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.check_run_output_candidate();

CREATE CONSTRAINT TRIGGER run_output_candidate_match AFTER INSERT ON public.pipeline_run_output_candidates DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.check_run_output_candidate();

CREATE TRIGGER run_output_discard_immutable BEFORE DELETE OR UPDATE ON public.pipeline_run_output_discards FOR EACH ROW EXECUTE FUNCTION public.immutable_run_output_evidence();

CREATE CONSTRAINT TRIGGER run_output_discard_match AFTER INSERT ON public.pipeline_run_output_discards DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.check_run_output_candidate();

CREATE CONSTRAINT TRIGGER run_output_disposition_match AFTER INSERT ON public.hangar_handoff_dispositions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.check_run_output_disposition();

CREATE CONSTRAINT TRIGGER run_output_disposition_match AFTER INSERT ON public.pipeline_run_output_finishes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.check_run_output_disposition();

CREATE TRIGGER run_output_finish_immutable BEFORE DELETE OR UPDATE ON public.pipeline_run_output_finishes FOR EACH ROW EXECUTE FUNCTION public.immutable_run_output_evidence();

CREATE TRIGGER run_output_hold_immutable BEFORE DELETE OR UPDATE ON public.pipeline_run_output_holds FOR EACH ROW EXECUTE FUNCTION public.immutable_run_output_evidence();

CREATE TRIGGER run_output_release_immutable BEFORE DELETE OR UPDATE ON public.pipeline_run_output_releases FOR EACH ROW EXECUTE FUNCTION public.immutable_run_output_evidence();

CREATE CONSTRAINT TRIGGER run_output_release_match AFTER UPDATE OF release_acknowledgement ON public.hangar_capture_reservations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.check_run_output_release();

CREATE CONSTRAINT TRIGGER run_output_release_match AFTER UPDATE OF release_acknowledgement ON public.hangar_no_capture_dispositions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.check_run_output_release();

CREATE CONSTRAINT TRIGGER run_output_release_match AFTER UPDATE OF release_acknowledgement ON public.hangar_pre_reservation_cancel_dispositions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.check_run_output_release();

CREATE CONSTRAINT TRIGGER run_output_release_match AFTER INSERT ON public.pipeline_run_output_releases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.check_run_output_release();

CREATE TRIGGER run_output_start_immutable BEFORE DELETE OR UPDATE ON public.pipeline_run_output_starts FOR EACH ROW EXECUTE FUNCTION public.immutable_run_output_start();

-- Open runtime findings return to the newest epoch, when there is one.
INSERT INTO hangar_policy_violations (activation_epoch, violation, subject, detail, observed_at, resolved_at)
SELECT e.epoch_id, f.violation, f.subject, f.detail, f.observed_at, f.resolved_at
FROM hangar_integrity_findings f
CROSS JOIN (SELECT max(epoch_id) AS epoch_id FROM hangar_output_activation_epochs) e
WHERE e.epoch_id IS NOT NULL AND f.violation IN ('out_of_band_absence', 'runtime_principal_denied');
DELETE FROM hangar_integrity_findings;

ALTER TABLE hangar_output_activation_epochs
    DROP CONSTRAINT hangar_output_epoch_enabled_output_identity,
    ADD CONSTRAINT hangar_output_epoch_enabled_output_identity CHECK (output_state <> 'enabled'::text OR receipt_public_key_id IS NOT NULL AND materialization_key_id IS NOT NULL AND receipt_public_key_id <> materialization_key_id AND bucket_fingerprint IS NOT NULL AND derived_namespace IS NOT NULL AND receipt_key_valid_from IS NOT NULL AND receipt_key_valid_until IS NOT NULL AND receipt_key_valid_until > receipt_key_valid_from) NOT VALID;
-- NOT VALID because an epoch enabled without a receipt key under this
-- migration cannot be given one back; validated whenever none exists, so a
-- reversal of a database that never enabled one restores the shape exactly.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM hangar_output_activation_epochs
                   WHERE output_state = 'enabled' AND NOT (receipt_public_key_id IS NOT NULL
                     AND materialization_key_id IS NOT NULL AND receipt_public_key_id <> materialization_key_id
                     AND bucket_fingerprint IS NOT NULL AND derived_namespace IS NOT NULL
                     AND receipt_key_valid_from IS NOT NULL AND receipt_key_valid_until IS NOT NULL
                     AND receipt_key_valid_until > receipt_key_valid_from)) THEN
        ALTER TABLE hangar_output_activation_epochs VALIDATE CONSTRAINT hangar_output_epoch_enabled_output_identity;
    END IF;
END $$;

SELECT hangar_activation_grants();
