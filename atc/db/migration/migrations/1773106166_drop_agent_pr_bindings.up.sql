-- The provider-native pull-request lane is removed. It mirrored forge review
-- state server-side, projected that state into pipeline configuration, and
-- carried its cursor in a 45-column binding row. It never ran: startup
-- appended an unconditional authority-spine error whenever pull requests were
-- enabled, and the only production composition site never constructed the
-- binding store at all. So there are no live bindings to drain and no
-- in-flight observation to preserve -- the Go code that read and wrote this
-- schema is already gone.
--
-- Two things go with the table. The pr_binding_id discriminator on
-- agent_workflow_resource_source_pipelines split that registry into two
-- registration models, and every surviving query had to say
-- "AND pr_binding_id IS NULL" to mean "the only model that exists". With the
-- column gone the partial unique index over (team_id, workflow_definition_id)
-- becomes vacuously total, so the plain UNIQUE constraint 1773106151 replaced
-- is restored -- one definition-owned registration per definition, enforced
-- across the whole table again rather than only over the NULL slice.
--
-- The origin-identity index on agent_publication_occurrences exists solely to
-- back the two composite foreign keys 1773106154 put on agent_pr_bindings; it
-- must go after the table, and it is not the surviving outcome-identity or
-- id_team_evidence index, both of which back other tables' keys and stay.

-- Serialize the emptiness check with the drop, matching 1773106154's rule.
LOCK TABLE agent_pr_bindings IN ACCESS EXCLUSIVE MODE;

-- Refuse rather than destroy. Every database at or past 1773106154 was proven
-- empty at upgrade time -- that migration refuses to apply while any binding
-- exists -- so this can only fire on a database that reached here some other
-- way. Surfacing that as a named refusal beats dropping rows nobody can
-- reconstruct, and beats a bare unique_violation from the restored constraint.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_pr_bindings) THEN
        RAISE EXCEPTION
            'cannot remove agent PR bindings while bindings exist';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM agent_workflow_resource_source_pipelines
        WHERE pr_binding_id IS NOT NULL
    ) THEN
        RAISE EXCEPTION
            'cannot remove agent PR bindings while binding-owned source pipelines exist';
    END IF;
END
$$;

-- The three partial indexes are dropped before the column they are predicated
-- on. Dropping the column with CASCADE instead would take the active index
-- silently and never rebuild it, losing the one-active-pipeline-per-workflow
-- -name guarantee.
DROP INDEX agent_workflow_resource_source_pipelines_active;
DROP INDEX agent_workflow_resource_source_pipelines_binding;
DROP INDEX agent_workflow_resource_source_pipelines_definition;

ALTER TABLE agent_workflow_resource_source_pipelines
    DROP COLUMN pr_binding_id,
    ADD UNIQUE (team_id, workflow_definition_id);

CREATE UNIQUE INDEX agent_workflow_resource_source_pipelines_active
    ON agent_workflow_resource_source_pipelines (team_id, workflow_name)
    WHERE state = 'active';

-- Only possible once the referencing column is gone: pr_binding_id was the
-- single foreign key into this table, ON DELETE RESTRICT.
DROP TABLE agent_pr_bindings;

-- Only possible once the keys that used it went with the table.
DROP INDEX agent_publication_occurrences_pr_binding_origin_identity;
