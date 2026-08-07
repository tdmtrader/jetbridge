-- Restore only the historical schema shape, exactly as 1773106151 and
-- 1773106154 together left it. Deliberately restore no row: the up migration
-- refuses to run while any binding or binding-owned source pipeline exists,
-- so an empty table IS the faithful inverse for every database that could
-- have reached 1773106166. There is no lossy reconstruction hiding here and
-- no data to invent.
--
-- The shape has to be exact rather than approximate because the rollback
-- chain continues past this file: 1773106154's down drops six named
-- constraints and six columns and then re-adds the two single-column foreign
-- keys it had replaced, and 1773106151's down drops the table outright. A
-- table recreated with 1773106151's original inline foreign keys, or without
-- 1773106154's composite ones, would make the next step down fail.
--
-- Order is forced twice. The origin-identity index must exist before the
-- composite keys that reference it, and pr_binding_id must exist before the
-- partial indexes predicated on it.

CREATE UNIQUE INDEX agent_publication_occurrences_pr_binding_origin_identity
    ON agent_publication_occurrences (id, team_id, workflow_run_id);

-- 1773106151's table, carrying 1773106154's frozen publication authority.
-- originating_workflow_run_id and originating_publication_occurrence_id
-- deliberately carry no inline reference: 1773106154 replaced those with the
-- team-scoped composite keys added at the end of this definition.
CREATE TABLE agent_pr_bindings (
    id                                  BIGSERIAL PRIMARY KEY,
    team_id                             INTEGER NOT NULL REFERENCES teams (id) ON DELETE RESTRICT,
    provider                            TEXT NOT NULL
                                            CHECK (provider IN ('github', 'azure-devops')),
    repository                          TEXT NOT NULL
                                            CHECK (btrim(repository) = repository
                                               AND repository <> ''
                                               AND octet_length(repository) <= 512),
    external_id                         TEXT NOT NULL
                                            CHECK (btrim(external_id) = external_id
                                               AND external_id <> ''
                                               AND octet_length(external_id) <= 256),
    url                                 TEXT NOT NULL
                                            CHECK (btrim(url) = url
                                               AND url <> ''
                                               AND octet_length(url) <= 2048),
    source_ref                          TEXT NOT NULL
                                            CHECK (btrim(source_ref) = source_ref
                                               AND source_ref <> ''
                                               AND source_ref !~ '[[:space:]]'
                                               AND octet_length(source_ref) <= 512),
    target_ref                          TEXT NOT NULL
                                            CHECK (btrim(target_ref) = target_ref
                                               AND target_ref <> ''
                                               AND target_ref !~ '[[:space:]]'
                                               AND octet_length(target_ref) <= 512),
    originating_workflow_run_id         BIGINT NOT NULL,
    originating_publication_occurrence_id BIGINT NOT NULL,
    monitor_workflow_definition_id      INTEGER NOT NULL
                                            REFERENCES agent_workflow_definitions (id) ON DELETE RESTRICT,
    monitor_workflow_version            INTEGER NOT NULL CHECK (monitor_workflow_version > 0),
    pipeline_id                         INTEGER REFERENCES pipelines (id) ON DELETE SET NULL,
    acknowledged_cursor                 JSONB NOT NULL
                                            CHECK (jsonb_typeof(acknowledged_cursor) = 'string'
                                               AND octet_length(acknowledged_cursor #>> '{}')
                                                   BETWEEN 1 AND 4096),
    last_observation_snapshot_id        BIGINT NOT NULL
                                            REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    last_acknowledged_action_digest     TEXT
                                            CHECK (last_acknowledged_action_digest IS NULL
                                               OR last_acknowledged_action_digest ~ '^sha256:[0-9a-f]{64}$'),
    last_acknowledged_workflow_run_id   BIGINT REFERENCES agent_workflow_runs (id) ON DELETE RESTRICT,
    last_reconciled_source_sha          TEXT NOT NULL
                                            CHECK (last_reconciled_source_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    last_reconciled_target_sha          TEXT NOT NULL
                                            CHECK (last_reconciled_target_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    last_reconciled_at                  TIMESTAMPTZ NOT NULL,
    lifecycle_state                     TEXT NOT NULL DEFAULT 'active'
                                            CHECK (lifecycle_state IN
                                                ('active', 'attention_required', 'completed', 'abandoned')),
    attention_reason                    TEXT NOT NULL DEFAULT ''
                                            CHECK (octet_length(attention_reason) <= 2048),
    paused                              BOOLEAN NOT NULL DEFAULT false,
    operator_terminated                 BOOLEAN NOT NULL DEFAULT false,
    observation_requested_at            TIMESTAMPTZ,
    active_base_revision                BIGINT CHECK (active_base_revision > 0),
    active_action_digest                TEXT
                                            CHECK (active_action_digest IS NULL
                                               OR active_action_digest ~ '^sha256:[0-9a-f]{64}$'),
    active_observation_snapshot_id      BIGINT REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    active_cursor                       JSONB
                                            CHECK (active_cursor IS NULL
                                               OR (jsonb_typeof(active_cursor) = 'string'
                                                   AND octet_length(active_cursor #>> '{}')
                                                       BETWEEN 1 AND 4096)),
    active_source_sha                   TEXT
                                            CHECK (active_source_sha IS NULL
                                               OR active_source_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    active_target_sha                   TEXT
                                            CHECK (active_target_sha IS NULL
                                               OR active_target_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    active_reservation_token            TEXT
                                            CHECK (active_reservation_token IS NULL
                                               OR (btrim(active_reservation_token) = active_reservation_token
                                                   AND active_reservation_token <> ''
                                                   AND octet_length(active_reservation_token) <= 128)),
    active_reservation_expires_at       TIMESTAMPTZ,
    active_workflow_run_id              BIGINT REFERENCES agent_workflow_runs (id) ON DELETE RESTRICT,
    terminal_observation_snapshot_id    BIGINT REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    terminal_at                         TIMESTAMPTZ,
    revision                            BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at                          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                          TIMESTAMPTZ NOT NULL DEFAULT now(),

    destination                         TEXT NOT NULL,
    approval_policy_version             TEXT NOT NULL,
    creation_publication_occurrence_id  BIGINT NOT NULL,
    approved_baseline_repository_snapshot_id BIGINT NOT NULL,
    approved_baseline_validation_snapshot_id BIGINT NOT NULL,
    approved_baseline_publication_occurrence_id BIGINT NOT NULL,

    UNIQUE (team_id, provider, repository, external_id),
    CHECK (
        (last_acknowledged_action_digest IS NULL) =
        (last_acknowledged_workflow_run_id IS NULL)
    ),
    CHECK (active_base_revision IS NULL OR active_base_revision < revision),
    CHECK (
        (lifecycle_state = 'attention_required' AND attention_reason <> '')
        OR (lifecycle_state <> 'attention_required' AND attention_reason = '')
    ),
    CHECK (
        (
            active_action_digest IS NULL
            AND active_base_revision IS NULL
            AND active_observation_snapshot_id IS NULL
            AND active_cursor IS NULL
            AND active_source_sha IS NULL
            AND active_target_sha IS NULL
            AND active_reservation_token IS NULL
            AND active_reservation_expires_at IS NULL
            AND active_workflow_run_id IS NULL
        )
        OR
        (
            active_action_digest IS NOT NULL
            AND active_base_revision IS NOT NULL
            AND active_observation_snapshot_id IS NOT NULL
            AND active_cursor IS NOT NULL
            AND active_source_sha IS NOT NULL
            AND active_target_sha IS NOT NULL
            AND active_reservation_token IS NOT NULL
            AND active_reservation_expires_at IS NOT NULL
            AND lifecycle_state IN ('active', 'attention_required')
        )
    ),
    CHECK (
        (
            lifecycle_state IN ('completed', 'abandoned')
            AND terminal_observation_snapshot_id IS NOT NULL
            AND terminal_at IS NOT NULL
            AND active_action_digest IS NULL
        )
        OR
        (
            lifecycle_state IN ('active', 'attention_required')
            AND terminal_observation_snapshot_id IS NULL
            AND terminal_at IS NULL
        )
    ),

    CONSTRAINT agent_pr_bindings_destination_check
        CHECK (
            btrim(destination) = destination
            AND destination <> ''
            AND octet_length(destination) <= 2048
        ),
    CONSTRAINT agent_pr_bindings_approval_policy_version_check
        CHECK (
            btrim(approval_policy_version) = approval_policy_version
            AND approval_policy_version <> ''
            AND octet_length(approval_policy_version) <= 128
        ),

    CONSTRAINT agent_pr_bindings_originating_run_team_fkey
        FOREIGN KEY (originating_workflow_run_id, team_id)
        REFERENCES agent_workflow_runs (id, team_id)
        ON DELETE RESTRICT,
    CONSTRAINT agent_pr_bindings_originating_occurrence_run_team_fkey
        FOREIGN KEY (
            originating_publication_occurrence_id,
            team_id,
            originating_workflow_run_id
        )
        REFERENCES agent_publication_occurrences
            (id, team_id, workflow_run_id)
        ON DELETE RESTRICT,
    CONSTRAINT agent_pr_bindings_creation_occurrence_run_team_fkey
        FOREIGN KEY (
            creation_publication_occurrence_id,
            team_id,
            originating_workflow_run_id
        )
        REFERENCES agent_publication_occurrences
            (id, team_id, workflow_run_id)
        ON DELETE RESTRICT,
    CONSTRAINT agent_pr_bindings_baseline_repository_team_fkey
        FOREIGN KEY (
            approved_baseline_repository_snapshot_id,
            team_id
        )
        REFERENCES agent_snapshots (id, team_id)
        ON DELETE RESTRICT,
    CONSTRAINT agent_pr_bindings_baseline_validation_team_fkey
        FOREIGN KEY (
            approved_baseline_validation_snapshot_id,
            team_id
        )
        REFERENCES agent_snapshots (id, team_id)
        ON DELETE RESTRICT,
    CONSTRAINT agent_pr_bindings_baseline_occurrence_team_fkey
        FOREIGN KEY (
            approved_baseline_publication_occurrence_id,
            team_id
        )
        REFERENCES agent_publication_occurrences (id, team_id)
        ON DELETE RESTRICT
);

CREATE UNIQUE INDEX agent_pr_bindings_active_reservation_token
    ON agent_pr_bindings (active_reservation_token)
    WHERE active_reservation_token IS NOT NULL;

CREATE UNIQUE INDEX agent_pr_bindings_active_workflow_run
    ON agent_pr_bindings (active_workflow_run_id)
    WHERE active_workflow_run_id IS NOT NULL;

CREATE UNIQUE INDEX agent_pr_bindings_active_action
    ON agent_pr_bindings (team_id, active_action_digest)
    WHERE active_action_digest IS NOT NULL;

CREATE INDEX agent_pr_bindings_active
    ON agent_pr_bindings (team_id, updated_at, id)
    WHERE lifecycle_state = 'active' AND NOT paused AND NOT operator_terminated;

-- The up migration restored UNIQUE (team_id, workflow_definition_id) as an
-- unnamed table constraint, so PostgreSQL auto-named it. Find it by its
-- definition rather than guessing the name, exactly as 1773106151 did.
DO $$
DECLARE
    total_constraint_names TEXT[];
BEGIN
    SELECT array_agg(constraint_entry.conname ORDER BY constraint_entry.conname)
      INTO total_constraint_names
      FROM pg_constraint constraint_entry
     WHERE constraint_entry.conrelid =
               'agent_workflow_resource_source_pipelines'::regclass
       AND constraint_entry.contype = 'u'
       AND pg_get_constraintdef(constraint_entry.oid) =
               'UNIQUE (team_id, workflow_definition_id)';

    IF coalesce(cardinality(total_constraint_names), 0) <> 1 THEN
        RAISE EXCEPTION
            'expected exactly one UNIQUE (team_id, workflow_definition_id) constraint, found %',
            coalesce(cardinality(total_constraint_names), 0);
    END IF;

    EXECUTE format(
        'ALTER TABLE agent_workflow_resource_source_pipelines DROP CONSTRAINT %I',
        total_constraint_names[1]
    );
END
$$;

DROP INDEX agent_workflow_resource_source_pipelines_active;

ALTER TABLE agent_workflow_resource_source_pipelines
    ADD COLUMN pr_binding_id BIGINT
        REFERENCES agent_pr_bindings (id) ON DELETE RESTRICT;

CREATE UNIQUE INDEX agent_workflow_resource_source_pipelines_definition
    ON agent_workflow_resource_source_pipelines (team_id, workflow_definition_id)
    WHERE pr_binding_id IS NULL;

CREATE UNIQUE INDEX agent_workflow_resource_source_pipelines_binding
    ON agent_workflow_resource_source_pipelines (team_id, pr_binding_id)
    WHERE pr_binding_id IS NOT NULL;

CREATE UNIQUE INDEX agent_workflow_resource_source_pipelines_active
    ON agent_workflow_resource_source_pipelines (team_id, workflow_name)
    WHERE state = 'active' AND pr_binding_id IS NULL;
