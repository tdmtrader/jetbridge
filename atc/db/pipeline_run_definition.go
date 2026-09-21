package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc/db/encryption"

	"github.com/concourse/concourse/atc"
)

func retainRunDefinition(tx Tx, runID int, definition atc.RunDefinition) error {
	template, err := json.Marshal(definition.Template)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(definition)
	if err != nil {
		return err
	}
	config, nonce, err := tx.EncryptionStrategy().Encrypt(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO pipeline_run_definitions (run_id, template_digest, config, nonce)
		VALUES ($1, $2, $3, $4)`, runID, templateDefinitionDigest(template), config, nonce)
	return err
}

// Definition reads retained admission evidence; it never consults a current
// template or payload. A legacy Run with no retained record returns found=false.
func (f *pipelineRunFactory) Definition(runID int) (atc.RunDefinition, bool, error) {
	return readRunDefinition(f.conn, runID)
}

type runDefinitionReader interface {
	QueryRow(string, ...any) sq.RowScanner
	EncryptionStrategy() encryption.Strategy
}

func readRunDefinition(reader runDefinitionReader, runID int) (atc.RunDefinition, bool, error) {
	var config, templateDigest, materializedDigest string
	var nonce *string
	err := reader.QueryRow(`SELECT d.config, d.nonce, d.template_digest, r.config_hash
		FROM pipeline_run_definitions d JOIN pipeline_runs r ON r.id = d.run_id
		WHERE d.run_id = $1`, runID).Scan(&config, &nonce, &templateDigest, &materializedDigest)
	if err == sql.ErrNoRows {
		return atc.RunDefinition{}, false, nil
	}
	if err != nil {
		return atc.RunDefinition{}, false, err
	}
	payload, err := reader.EncryptionStrategy().Decrypt(config, nonce)
	if err != nil {
		return atc.RunDefinition{}, false, err
	}
	// Verify the retained JSON before decoding interface-valued task fields;
	// a second encoding could change the representation of their numbers.
	var retained struct {
		Template     json.RawMessage `json:"template"`
		Materialized json.RawMessage `json:"materialized"`
	}
	if err := json.Unmarshal(payload, &retained); err != nil {
		return atc.RunDefinition{}, false, err
	}
	if templateDefinitionDigest(retained.Template) != templateDigest || materializedConfigDigest(retained.Materialized) != materializedDigest {
		return atc.RunDefinition{}, false, fmt.Errorf("Run %d definition attestation mismatch", runID)
	}
	var definition atc.RunDefinition
	if err := json.Unmarshal(payload, &definition); err != nil {
		return atc.RunDefinition{}, false, err
	}
	return definition, true, nil
}

func templateDefinitionDigest(payload []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(append([]byte("run-template-definition/v1\x00"), payload...)))
}

func materializedConfigDigest(payload []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(append([]byte("run-instance-config/v1\x00"), payload...)))
}
