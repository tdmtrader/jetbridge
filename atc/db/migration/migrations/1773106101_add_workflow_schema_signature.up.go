package migrations

import "fmt"

// Up_1773106101 adds the workflow schema/signature metadata columns to
// agent_workflow_definitions together with the invariants the v3 runtime
// relies on: schema 1/2 carry no signature, schema 3+ always carries a
// positive one.
func (m *migrations) Up_1773106101() error {
	if _, err := m.Exec(`
		ALTER TABLE agent_workflow_definitions
			ADD COLUMN schema_version INTEGER,
			ADD COLUMN signature_version INTEGER
	`); err != nil {
		return fmt.Errorf("workflow metadata migration: add columns: %w", err)
	}

	// Backfill rows that predate these columns. A pre-v3 definition declares its
	// schema textually, so reading the `schema_version:` key out of the stored
	// source is enough here: schema 1/2 definitions are inert metadata in v3 —
	// never compiled, rendered, or run — so nothing downstream needs the full
	// manifest semantics the retired legacy decoder used to compute.
	// The source is the manifest's entry-point file when one is stored, and the
	// standalone definition otherwise; an absent key means the original default, 1.
	if _, err := m.Exec(`
		UPDATE agent_workflow_definitions
		SET schema_version = COALESCE(
				NULLIF(substring(
					COALESCE(
						source_manifest ->> 'workflow.yml',
						source_manifest ->> 'workflow.yaml',
						definition
					) from 'schema_version:[[:space:]]*([0-9]+)'
				), '')::integer,
				1
			),
			signature_version = 0
		WHERE schema_version IS NULL
	`); err != nil {
		return fmt.Errorf("workflow metadata migration: backfill existing rows: %w", err)
	}

	// A signature is derived by compiling a schema-3 definition, which this
	// migration cannot do. No such row can legitimately exist yet — schema 3
	// arrives later in the chain — so refuse rather than invent one.
	var unsupported int
	if err := m.QueryRow(`
		SELECT count(*) FROM agent_workflow_definitions WHERE schema_version >= 3
	`).Scan(&unsupported); err != nil {
		return fmt.Errorf("workflow metadata migration: inspect backfilled rows: %w", err)
	}
	if unsupported > 0 {
		return fmt.Errorf(
			"workflow metadata migration: %d definition(s) declare schema_version >= 3 before the v3 columns exist; their signature cannot be derived here",
			unsupported,
		)
	}

	if _, err := m.Exec(`
		ALTER TABLE agent_workflow_definitions
			ADD CONSTRAINT agent_workflow_definitions_schema_version_check
				CHECK (schema_version > 0),
			ADD CONSTRAINT agent_workflow_definitions_signature_version_check
				CHECK (signature_version >= 0),
			ADD CONSTRAINT agent_workflow_definitions_schema_signature_check
				CHECK (
					(schema_version IN (1, 2) AND signature_version = 0)
					OR (schema_version >= 3 AND signature_version > 0)
				),
			ALTER COLUMN schema_version SET NOT NULL,
			ALTER COLUMN signature_version SET NOT NULL;
		CREATE INDEX agent_workflow_definitions_schema_version
			ON agent_workflow_definitions (schema_version, name, version DESC);
		CREATE INDEX agent_workflow_definitions_name_signature_version
			ON agent_workflow_definitions (name, signature_version, version DESC)
	`); err != nil {
		return fmt.Errorf("workflow metadata migration: install constraints and indexes: %w", err)
	}
	return nil
}
