package migration

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/encryption"
)

var encryptedColumns = []encryptedColumn{
	{"teams", "legacy_auth", "id"},
	{"resources", "config", "id"},
	{"jobs", "config", "id"},
	{"resource_types", "config", "id"},
	{"builds", "private_plan", "id"},
	{"pipeline_run_definitions", "config", "run_id"},
	{"mcp_oauth_state", "data", "id"},
}

type encryptedColumn struct {
	Table      string
	Column     string
	PrimaryKey string
}

// encryptedColumnsInSchema is the subset of encryptedColumns the database
// has at its current schema version: the list names today's columns, and a
// migration to an earlier version (a test, or --migrate-db-to-version) runs
// against a schema where a later table or column does not exist yet.
func (m migrator) encryptedColumnsInSchema() ([]encryptedColumn, error) {
	var columns []encryptedColumn
	for _, column := range encryptedColumns {
		var present bool
		if err := m.db.QueryRow(`
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
			) AND EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = $1 AND column_name = 'nonce'
			)
		`, column.Table, column.Column).Scan(&present); err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		columns = append(columns, column)
	}
	return columns, nil
}

// encryptPlaintext encrypts, under key, every row of an encrypted column that
// is still plaintext (nonce IS NULL). Every write since the key became
// required is ciphertext; this pass picks up a database that was once run
// without a key, on its first start under one.
//
// The pass works in batches of plaintextBatchSize rows per column: each
// batch is read to memory, the cursor closed, then the column and its nonce
// written together in one transaction, until the SELECT returns no rows. The
// predicate makes a batch idempotent, so a crash between batches leaves only
// whole rows and the next start resumes. Reading before writing also keeps
// the pass off the connection the cursor holds, which matters on the test
// helpers' one-connection pools; and a batch, not the whole column, is what
// a long-keyless install's builds.private_plan holds under locks at a time.
func (m migrator) encryptPlaintext(key *encryption.Key) error {
	logger := m.logger.Session("encrypt")
	columns, err := m.encryptedColumnsInSchema()
	if err != nil {
		return err
	}

	for _, ec := range columns {
		tLog := logger.Session("table", lager.Data{
			"table": ec.Table,
		})

		encryptedRows := 0
		for {
			n, err := m.encryptPlaintextBatch(tLog, key, ec)
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
			encryptedRows += n
		}

		if encryptedRows > 0 {
			tLog.Info("encrypted-existing-plaintext-data", lager.Data{
				"rows": encryptedRows,
			})
		}
	}

	return nil
}

// plaintextBatchSize is how many plaintext rows one transaction encrypts.
const plaintextBatchSize = 1000

// encryptPlaintextBatch encrypts up to plaintextBatchSize plaintext rows of
// the column in one transaction and returns how many it wrote.
func (m migrator) encryptPlaintextBatch(tLog lager.Logger, key *encryption.Key, ec encryptedColumn) (int, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	plaintext, err := plaintextRows(tx, ec, plaintextBatchSize)
	if err != nil {
		tLog.Error("failed-to-read-plaintext-rows", err)
		return 0, err
	}
	if len(plaintext) == 0 {
		return 0, nil
	}

	for _, row := range plaintext {
		rLog := tLog.Session("row", lager.Data{
			"primary-key": row.primaryKey,
		})

		encrypted, nonce, err := key.Encrypt([]byte(row.value))
		if err != nil {
			rLog.Error("failed-to-encrypt", err)
			return 0, err
		}

		_, err = tx.Exec(`
			UPDATE `+ec.Table+`
			SET `+ec.Column+` = $1, nonce = $2
			WHERE `+ec.PrimaryKey+` = $3
		`, encrypted, nonce, row.primaryKey)
		if err != nil {
			rLog.Error("failed-to-update", err)
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(plaintext), nil
}

type plaintextRow struct {
	primaryKey any
	value      string
}

// plaintextRows reads up to limit plaintext rows of the column and closes the
// cursor before returning.
func plaintextRows(tx *sql.Tx, ec encryptedColumn, limit int) ([]plaintextRow, error) {
	rows, err := tx.Query(`
		SELECT `+ec.PrimaryKey+`, `+ec.Column+`
		FROM `+ec.Table+`
		WHERE nonce IS NULL
		AND `+ec.Column+` IS NOT NULL
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plaintext []plaintextRow
	for rows.Next() {
		var (
			primaryKey any
			val        sql.NullString
		)
		if err := rows.Scan(&primaryKey, &val); err != nil {
			return nil, err
		}
		if !val.Valid {
			continue
		}
		plaintext = append(plaintext, plaintextRow{primaryKey: primaryKey, value: val.String})
	}
	return plaintext, rows.Err()
}
