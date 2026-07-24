package migrations

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/atc/db/migration/legacyworkflow"
)

type workflowSchemaSignatureBackfillRow struct {
	id         int
	name       string
	version    int
	definition string
	manifest   sql.NullString
	metadata   legacyworkflow.Metadata
}

// Up_1773106101 intentionally decodes real stored workflow source instead of
// trusting a YAML scalar. The migration-local decoder freezes the released
// schema-1/2 and schema-3 behavior independently of the live runtime parser.
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

	positiveSignatures := map[string]legacyworkflow.PublicSignature{}
	for index := range backfill {
		row := &backfill[index]
		source := map[string]string{"workflow.yml": row.definition}
		if row.manifest.Valid {
			if err := json.Unmarshal([]byte(row.manifest.String), &source); err != nil {
				return workflowMigrationRowError(*row, "stored manifest is malformed")
			}
		}
		metadata, signature, err := legacyworkflow.DecodeManifest(source)
		if err != nil {
			return workflowMigrationRowError(*row, "stored source does not compile")
		}
		if metadata.Name != row.name {
			return workflowMigrationRowError(*row, "compiled name does not match stored name")
		}
		if metadata.SchemaVersion <= 0 || metadata.SignatureVersion < 0 ||
			(metadata.SchemaVersion <= 2 && metadata.SignatureVersion != 0) ||
			(metadata.SchemaVersion >= 3 && metadata.SignatureVersion <= 0) {
			return workflowMigrationRowError(*row, "compiled schema/signature metadata is invalid")
		}
		row.metadata = metadata
		if metadata.SignatureVersion > 0 {
			if signature == nil {
				return workflowMigrationRowError(*row, "compiled public signature is invalid")
			}
			key := fmt.Sprintf("%s\x00%d", row.name, metadata.SignatureVersion)
			if previous, found := positiveSignatures[key]; found && !previous.Equal(*signature) {
				return workflowMigrationRowError(*row, "reuses an incompatible positive signature version")
			}
			positiveSignatures[key] = *signature
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
