-- S-6: workflow-name-level lifecycle metadata. Distinct from
-- agent_workflow_definitions (which is per-version and whose `description`
-- is derived from the version's YAML): annotation is a human operator note,
-- hidden deprecates a workflow from default listings without deleting its
-- versions. Keyed by name; a row is created lazily on first annotate/hide.
CREATE TABLE agent_workflow_lifecycle (
    name       TEXT PRIMARY KEY,
    hidden     BOOLEAN NOT NULL DEFAULT false,
    annotation TEXT NOT NULL DEFAULT '',
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
