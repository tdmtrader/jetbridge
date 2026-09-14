package mcpauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/concourse/concourse/atc/db"
)

type SQLStore struct{ conn db.DbConn }

// NewSQLStore uses the deployment's database encryption strategy. When no
// encryption key is configured this strategy stores plaintext, like other
// JetBridge sensitive database columns; production should configure a key.
func NewSQLStore(conn db.DbConn) *SQLStore { return &SQLStore{conn: conn} }

func (s *SQLStore) WithTx(ctx context.Context, f func(Tx) error) error {
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := f(&sqlTx{ctx: ctx, tx: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

type sqlTx struct {
	ctx context.Context
	tx  db.Tx
}

func (t *sqlTx) Lock(key string, shared bool) error {
	function := "pg_advisory_xact_lock"
	if shared {
		function = "pg_advisory_xact_lock_shared"
	}
	_, err := t.tx.ExecContext(t.ctx, "SELECT "+function+"(hashtextextended($1, 0))", "mcp-oauth:"+key)
	return err
}

func (t *sqlTx) Get(kind, key string, dst any) error {
	id := kind + ":" + key
	// Locking a deterministic key also serializes concurrent first logins, when
	// an identity session row does not exist yet. Locks are released on rollback.
	if err := t.Lock(id, false); err != nil {
		return err
	}
	var data string
	var nonce *string
	err := t.tx.QueryRowContext(t.ctx, "SELECT data, nonce FROM mcp_oauth_state WHERE id = $1 FOR UPDATE", id).Scan(&data, &nonce)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	plain, err := t.tx.EncryptionStrategy().Decrypt(data, nonce)
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, dst)
}

func (t *sqlTx) Put(kind, key string, value any, expires time.Time) error {
	plain, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data, nonce, err := t.tx.EncryptionStrategy().Encrypt(plain)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO mcp_oauth_state (id, kind, data, nonce, expires_at)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT (id) DO UPDATE SET data=EXCLUDED.data, nonce=EXCLUDED.nonce, expires_at=EXCLUDED.expires_at`, kind+":"+key, kind, data, nonce, expires)
	return err
}

func (t *sqlTx) Delete(kind, key string) error {
	_, err := t.tx.ExecContext(t.ctx, "DELETE FROM mcp_oauth_state WHERE id=$1", kind+":"+key)
	return err
}

func (t *sqlTx) List(kind string) ([]Record, error) {
	rows, err := t.tx.QueryContext(t.ctx, "SELECT id, data, nonce FROM mcp_oauth_state WHERE kind=$1 AND expires_at > CURRENT_TIMESTAMP ORDER BY id", kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		var id, data string
		var nonce *string
		if err := rows.Scan(&id, &data, &nonce); err != nil {
			return nil, err
		}
		plain, err := t.tx.EncryptionStrategy().Decrypt(data, nonce)
		if err != nil {
			return nil, err
		}
		records = append(records, Record{Key: id[len(kind)+1:], Data: plain})
	}
	return records, rows.Err()
}

// Cleanup removes expired artifacts. A row's expiry is a storage retention
// deadline, never the sole authorization check. Used refresh-token tombstones
// live until their grant's absolute expiry so replay still revokes that grant.
func (s *SQLStore) Cleanup(ctx context.Context) error {
	_, err := s.conn.ExecContext(ctx, "DELETE FROM mcp_oauth_state WHERE expires_at < CURRENT_TIMESTAMP")
	return err
}
