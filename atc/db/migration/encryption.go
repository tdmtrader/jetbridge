package migration

import (
	"database/sql"
	"errors"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/encryption"
)

var encryptedColumns = []encryptedColumn{
	{Table: "teams", Column: "legacy_auth", PrimaryKey: "id"},
	{Table: "resources", Column: "config", PrimaryKey: "id"},
	{Table: "jobs", Column: "config", PrimaryKey: "id"},
	{Table: "resource_types", Column: "config", PrimaryKey: "id"},
	{Table: "prototypes", Column: "config", PrimaryKey: "id"},
	{Table: "builds", Column: "private_plan", PrimaryKey: "id"},
	{Table: "cert_cache", Column: "cert", PrimaryKey: "domain"},
	{Table: "pipelines", Column: "var_sources", PrimaryKey: "id"},
	{Table: "agent_user_credentials", Column: "encrypted_token", PrimaryKey: "id"},
	{Table: "agent_workflow_runs", Column: "actual_plan", PrimaryKey: "id", NonceColumn: "actual_plan_nonce"},
	{Table: "agent_workflow_runs", Column: "actual_plan_hash", PrimaryKey: "id", NonceColumn: "actual_plan_hash_nonce"},
	{Table: "agent_workflow_runs", Column: "resolved_dependencies", PrimaryKey: "id", NonceColumn: "resolved_dependencies_nonce"},
}

type encryptedColumn struct {
	Table       string
	Column      string
	PrimaryKey  string
	NonceColumn string
}

func (column encryptedColumn) nonceColumn() string {
	if column.NonceColumn == "" {
		return "nonce"
	}
	return column.NonceColumn
}

// existingEncryptedColumns filters encryptedColumns down to tables that
// exist at the current schema version: a downgrade below the migration
// that created a table (e.g. 1773106020's agent_user_credentials) drops
// it, and the post-migration encryption pass must skip it, not fail.
func (m migrator) existingEncryptedColumns() ([]encryptedColumn, error) {
	existing := make([]encryptedColumn, 0, len(encryptedColumns))
	for _, ec := range encryptedColumns {
		exists, err := checkTableExist(m.db, ec.Table)
		if err != nil {
			return nil, err
		}
		if exists {
			existing = append(existing, ec)
		}
	}
	return existing, nil
}

func (m migrator) encryptPlaintext(key *encryption.Key) error {
	logger := m.logger.Session("encrypt")
	columns, err := m.existingEncryptedColumns()
	if err != nil {
		return err
	}
	for _, ec := range columns {
		nonceColumn := ec.nonceColumn()
		rows, err := m.db.Query(`
			SELECT ` + ec.PrimaryKey + `, ` + ec.Column + `
			FROM ` + ec.Table + `
			WHERE ` + nonceColumn + ` IS NULL
			AND ` + ec.Column + ` IS NOT NULL
		`)
		if err != nil {
			return err
		}

		tLog := logger.Session("table", lager.Data{
			"table": ec.Table,
		})

		encryptedRows := 0

		for rows.Next() {
			var (
				primaryKey any
				val        sql.NullString
			)

			err := rows.Scan(&primaryKey, &val)
			if err != nil {
				tLog.Error("failed-to-scan", err)
				return err
			}

			if !val.Valid {
				continue
			}

			rLog := tLog.Session("row", lager.Data{
				"primary-key": primaryKey,
			})

			encrypted, nonce, err := key.Encrypt([]byte(val.String))
			if err != nil {
				rLog.Error("failed-to-encrypt", err)
				return err
			}

			_, err = m.db.Exec(`
				UPDATE `+ec.Table+`
				SET `+ec.Column+` = $1, `+nonceColumn+` = $2
				WHERE `+ec.PrimaryKey+` = $3
			`, encrypted, nonce, primaryKey)
			if err != nil {
				rLog.Error("failed-to-update", err)
				return err
			}

			encryptedRows++
		}

		if encryptedRows > 0 {
			tLog.Info("encrypted-existing-plaintext-data", lager.Data{
				"rows": encryptedRows,
			})
		}
	}

	return nil
}

func (m migrator) decryptToPlaintext(oldKey *encryption.Key) error {
	logger := m.logger.Session("decrypt")
	columns, err := m.existingEncryptedColumns()
	if err != nil {
		return err
	}
	for _, ec := range columns {
		nonceColumn := ec.nonceColumn()
		rows, err := m.db.Query(`
			SELECT ` + ec.PrimaryKey + `, ` + nonceColumn + `, ` + ec.Column + `
			FROM ` + ec.Table + `
			WHERE ` + nonceColumn + ` IS NOT NULL
		`)
		if err != nil {
			return err
		}

		tLog := logger.Session("table", lager.Data{
			"table": ec.Table,
		})

		decryptedRows := 0

		for rows.Next() {
			var (
				primaryKey any
				val, nonce string
			)

			err := rows.Scan(&primaryKey, &nonce, &val)
			if err != nil {
				tLog.Error("failed-to-scan", err)
				return err
			}

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
				SET `+ec.Column+` = $1, `+nonceColumn+` = NULL
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

var ErrEncryptedWithUnknownKey = errors.New("row encrypted with neither old nor new key")

func (m migrator) encryptWithNewKey(newKey *encryption.Key, oldKey *encryption.Key) error {
	logger := m.logger.Session("rotate")
	columns, err := m.existingEncryptedColumns()
	if err != nil {
		return err
	}
	for _, ec := range columns {
		nonceColumn := ec.nonceColumn()
		rows, err := m.db.Query(`
			SELECT ` + ec.PrimaryKey + `, ` + nonceColumn + `, ` + ec.Column + `
			FROM ` + ec.Table + `
			WHERE ` + nonceColumn + ` IS NOT NULL
		`)
		if err != nil {
			return err
		}

		tLog := logger.Session("table", lager.Data{
			"table": ec.Table,
		})

		encryptedRows := 0

		for rows.Next() {
			var (
				primaryKey any
				val, nonce string
			)

			err := rows.Scan(&primaryKey, &nonce, &val)
			if err != nil {
				tLog.Error("failed-to-scan", err)
				return err
			}

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
				return ErrEncryptedWithUnknownKey
			}

			encrypted, newNonce, err := newKey.Encrypt(decrypted)
			if err != nil {
				rLog.Error("failed-to-encrypt", err)
				return err
			}

			_, err = m.db.Exec(`
				UPDATE `+ec.Table+`
				SET `+ec.Column+` = $1, `+nonceColumn+` = $2
				WHERE `+ec.PrimaryKey+` = $3
			`, encrypted, newNonce, primaryKey)
			if err != nil {
				rLog.Error("failed-to-update", err)
				return err
			}

			encryptedRows++
		}

		if encryptedRows > 0 {
			tLog.Info("re-encrypted-existing-encrypted-data", lager.Data{
				"rows": encryptedRows,
			})
		}
	}

	return nil
}
