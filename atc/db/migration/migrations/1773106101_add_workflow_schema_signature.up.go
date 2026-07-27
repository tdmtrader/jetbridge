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
