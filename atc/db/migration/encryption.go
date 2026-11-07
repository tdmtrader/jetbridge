package migration

import (
	"database/sql"
	"encoding/json"
	"errors"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/encryption"
)

// SigningKeysEncryptedAtRest says whether idtoken signing keys are stored
// encrypted under the database encryption key: the signing key factory
// encrypts what it writes, and the start-up pass encrypts plaintext rows.
// Rotation and decryption cover encrypted rows either way, and reads decrypt
// them either way.
//
// It stays false until no web that may run against this database predates
// the nonce column (1789793169). Such a web json-decodes jwk directly, so an
// encrypted row breaks its JWKS and token signing, and its lifecycler mints
// a fresh key. Turn it on in a release whose every supported predecessor
// reads encrypted keys. signing_keys' RotateOnly follows it; drop RotateOnly
// from the entry below when this goes, as it then has no other reader.
const SigningKeysEncryptedAtRest = false

var encryptedColumns = []encryptedColumn{
	{Table: "teams", Column: "legacy_auth", PrimaryKey: "id"},
	{Table: "resources", Column: "config", PrimaryKey: "id"},
	{Table: "jobs", Column: "config", PrimaryKey: "id"},
	{Table: "resource_types", Column: "config", PrimaryKey: "id"},
	{Table: "builds", Column: "private_plan", PrimaryKey: "id"},
	{Table: "pipelines", Column: "var_sources", PrimaryKey: "id"},
	{Table: "pipeline_run_definitions", Column: "config", PrimaryKey: "run_id", Optional: true},
	{Table: "mcp_oauth_state", Column: "data", PrimaryKey: "id", Optional: true},
	{Table: "signing_keys", Column: "jwk", PrimaryKey: "kid", Optional: true, JSON: true, RotateOnly: !SigningKeysEncryptedAtRest},
}

type encryptedColumn struct {
	Table      string
	Column     string
	PrimaryKey string
	// A newer table, or its nonce, can be absent at a supported historical
	// schema version.
	Optional bool
	// JSON marks a json column. Its ciphertext is stored as a JSON string,
	// which the column accepts; its plaintext is the JSON itself.
	JSON bool
	// RotateOnly leaves the column's plaintext rows out of the start-up
	// encryption pass; rotation and decryption still cover its encrypted
	// rows.
	RotateOnly bool
}

// storedCiphertext is how ciphertext is written to the column.
func (ec encryptedColumn) storedCiphertext(ciphertext string) (string, error) {
	if !ec.JSON {
		return ciphertext, nil
	}
	encoded, err := json.Marshal(ciphertext)
	return string(encoded), err
}

// ciphertext is the ciphertext of an encrypted row as the column stores it.
func (ec encryptedColumn) ciphertext(stored string) (string, error) {
	if !ec.JSON {
		return stored, nil
	}
	var ciphertext string
	err := json.Unmarshal([]byte(stored), &ciphertext)
	return ciphertext, err
}

func (m migrator) encryptedColumnsInSchema() ([]encryptedColumn, error) {
	var columns []encryptedColumn
	for _, column := range encryptedColumns {
		if column.Optional {
			var present bool
			if err := m.db.QueryRow(`
				SELECT EXISTS (
					SELECT 1 FROM pg_attribute
					WHERE attrelid = to_regclass($1) AND attname = 'nonce' AND NOT attisdropped
				)`, column.Table).Scan(&present); err != nil {
				return nil, err
			}
			if !present {
				continue
			}
		}
		columns = append(columns, column)
	}
	return columns, nil
}

// encryptPlaintext encrypts, under key, every row of an encrypted column that
// is still plaintext (nonce IS NULL): a database run without a key, on its
// first start under one.
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
		if ec.RotateOnly {
			continue
		}

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
		encrypted, err = ec.storedCiphertext(encrypted)
		if err != nil {
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

func (m migrator) decryptToPlaintext(oldKey *encryption.Key) error {
	logger := m.logger.Session("decrypt")
	columns, err := m.encryptedColumnsInSchema()
	if err != nil {
		return err
	}
	for _, ec := range columns {
		stored, err := encryptedRows(m.db, ec)
		if err != nil {
			return err
		}

		tLog := logger.Session("table", lager.Data{
			"table": ec.Table,
		})

		decryptedRows := 0

		for _, row := range stored {
			primaryKey, val, nonce := row.primaryKey, row.value, row.nonce

			rLog := tLog.Session("row", lager.Data{
				"primary-key": primaryKey,
			})

			decrypted, err := oldKey.Decrypt(val, &nonce)
			if err != nil {
				rLog.Error("failed-to-decrypt", err)
				return err
			}

			_, err = m.db.Exec(`
				UPDATE `+ec.Table+`
				SET `+ec.Column+` = $1, nonce = NULL
				WHERE `+ec.PrimaryKey+` = $2
			`, decrypted, primaryKey)
			if err != nil {
				rLog.Error("failed-to-update", err)
				return err
			}

			decryptedRows++
		}

		if decryptedRows > 0 {
			tLog.Info("decrypted-existing-encrypted-data", lager.Data{
				"rows": decryptedRows,
			})
		}
	}

	return nil
}

var errEncryptedWithUnknownKey = errors.New("row encrypted with neither old nor new key")

func (m migrator) encryptWithNewKey(newKey *encryption.Key, oldKey *encryption.Key) error {
	logger := m.logger.Session("rotate")
	columns, err := m.encryptedColumnsInSchema()
	if err != nil {
		return err
	}
	for _, ec := range columns {
		stored, err := encryptedRows(m.db, ec)
		if err != nil {
			return err
		}

		tLog := logger.Session("table", lager.Data{
			"table": ec.Table,
		})

		reencrypted := 0

		for _, row := range stored {
			primaryKey, val, nonce := row.primaryKey, row.value, row.nonce

			rLog := tLog.Session("row", lager.Data{
				"primary-key": primaryKey,
			})

			decrypted, err := oldKey.Decrypt(val, &nonce)
			if err != nil {
				_, err = newKey.Decrypt(val, &nonce)
				if err == nil {
					rLog.Debug("already-encrypted-with-new-key")
					continue
				}

				logger.Error("failed-to-decrypt-with-either-key", err)
				return errEncryptedWithUnknownKey
			}

			encrypted, newNonce, err := newKey.Encrypt(decrypted)
			if err != nil {
				rLog.Error("failed-to-encrypt", err)
				return err
			}
			encrypted, err = ec.storedCiphertext(encrypted)
			if err != nil {
				return err
			}

			_, err = m.db.Exec(`
				UPDATE `+ec.Table+`
				SET `+ec.Column+` = $1, nonce = $2
				WHERE `+ec.PrimaryKey+` = $3
			`, encrypted, newNonce, primaryKey)
			if err != nil {
				rLog.Error("failed-to-update", err)
				return err
			}

			reencrypted++
		}

		if reencrypted > 0 {
			tLog.Info("re-encrypted-existing-encrypted-data", lager.Data{
				"rows": reencrypted,
			})
		}
	}

	return nil
}

type encryptedRow struct {
	primaryKey any
	value      string
	nonce      string
}

// encryptedRows reads every encrypted row of a column into memory and closes
// the cursor before the caller writes any of them back, so a rotation pass
// never holds a cursor open while it updates: on a pool of one connection
// that would wait on itself.
func encryptedRows(db *sql.DB, ec encryptedColumn) ([]encryptedRow, error) {
	rows, err := db.Query(`
		SELECT ` + ec.PrimaryKey + `, nonce, ` + ec.Column + `
		FROM ` + ec.Table + `
		WHERE nonce IS NOT NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var encrypted []encryptedRow
	for rows.Next() {
		var (
			row    encryptedRow
			stored string
		)
		if err := rows.Scan(&row.primaryKey, &row.nonce, &stored); err != nil {
			return nil, err
		}
		row.value, err = ec.ciphertext(stored)
		if err != nil {
			return nil, err
		}
		encrypted = append(encrypted, row)
	}
	return encrypted, rows.Err()
}
