package migrations

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/agent/workflow"
)

type workflowSchemaSignatureBackfillRow struct {
	id         int
	name       string
	version    int
	definition string
	manifest   sql.NullString
	metadata   workflow.VersionMetadata
}

// Up_1773106101 intentionally compiles real stored workflow source instead of
// trusting a YAML scalar. CompileDefinition's schema-1/2 behavior is migration
// ABI: future compiler tightening must retain these historical fixtures or
// freeze an equivalent parser here before changing it.
func (m *migrations) Up_1773106101() error {
	if _, err := m.Exec(`
		ALTER TABLE agent_workflow_definitions
			ADD COLUMN schema_version INTEGER,
			ADD COLUMN signature_version INTEGER
	`); err != nil {
		return fmt.Errorf("workflow metadata migration: add columns: %w", err)
	}

	rows, err := m.Query(`
		SELECT id, name, version, definition, source_manifest
		FROM agent_workflow_definitions
		ORDER BY id
	`)
	if err != nil {
		return fmt.Errorf("workflow metadata migration: read definitions: %w", err)
	}
	backfill := []workflowSchemaSignatureBackfillRow{}
	for rows.Next() {
		var row workflowSchemaSignatureBackfillRow
		if err := rows.Scan(&row.id, &row.name, &row.version, &row.definition, &row.manifest); err != nil {
			_ = rows.Close()
			return fmt.Errorf("workflow metadata migration: scan definition row: %w", err)
		}
		backfill = append(backfill, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("workflow metadata migration: iterate definitions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("workflow metadata migration: close definition rows: %w", err)
	}

	positiveSignatures := map[string]workflow.PublicSignature{}
	for index := range backfill {
		row := &backfill[index]
		source := workflow.Manifest{"workflow.yml": row.definition}
		if row.manifest.Valid {
			if err := json.Unmarshal([]byte(row.manifest.String), &source); err != nil {
				return workflowMigrationRowError(*row, "stored manifest is malformed")
			}
		}
		compiled, err := workflow.CompileDefinition(source)
		if err != nil {
			return workflowMigrationRowError(*row, "stored source does not compile")
		}
		if compiled.Name != row.name {
			return workflowMigrationRowError(*row, "compiled name does not match stored name")
		}
		metadata, err := compiled.VersionMetadata()
		if err != nil {
			return workflowMigrationRowError(*row, "compiled schema/signature metadata is invalid")
		}
		row.metadata = metadata
		if metadata.SignatureVersion > 0 {
			signature, err := compiled.PublicSignature()
			if err != nil {
				return workflowMigrationRowError(*row, "compiled public signature is invalid")
			}
			key := fmt.Sprintf("%s\x00%d", row.name, metadata.SignatureVersion)
			if previous, found := positiveSignatures[key]; found && !previous.Equal(signature) {
				return workflowMigrationRowError(*row, "reuses an incompatible positive signature version")
			}
			positiveSignatures[key] = signature
		}
	}

	for _, row := range backfill {
		result, err := m.Exec(`
			UPDATE agent_workflow_definitions
			SET schema_version = $2, signature_version = $3
			WHERE id = $1
		`, row.id, row.metadata.SchemaVersion, row.metadata.SignatureVersion)
		if err != nil {
			return workflowMigrationRowError(row, "metadata update failed")
		}
		affected, err := result.RowsAffected()
		if err != nil || affected != 1 {
			return workflowMigrationRowError(row, "metadata update did not affect exactly one row")
		}
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

func workflowMigrationRowError(row workflowSchemaSignatureBackfillRow, category string) error {
	return fmt.Errorf("workflow metadata migration: definition row %d (%q version %d): %s", row.id, row.name, row.version, category)
}
