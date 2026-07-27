package db

import (
	"database/sql"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/credentials"
)

//counterfeiter:generate . AgentUserCredentialsFactory
type AgentUserCredentialsFactory interface {
	credentials.Backend
}

func NewAgentUserCredentialsFactory(conn DbConn) AgentUserCredentialsFactory {
	return &agentUserCredentialsFactory{conn: conn}
}

type agentUserCredentialsFactory struct {
	conn DbConn
}

func (f *agentUserCredentialsFactory) UserBySub(sub string) (int, string, bool, error) {
	var (
		id   int
		name string
	)
	err := f.conn.QueryRow(`SELECT id, username FROM users WHERE sub = $1`, sub).Scan(&id, &name)
	if err == sql.ErrNoRows {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return id, name, true, nil
}

func (f *agentUserCredentialsFactory) Put(userID int, userName, kind, token string, expiresAt time.Time) error {
	encrypted, nonce, err := f.conn.EncryptionStrategy().Encrypt([]byte(token))
	if err != nil {
		return err
	}
	var expires any
	if !expiresAt.IsZero() {
		expires = expiresAt
	}
	_, err = psql.Insert("agent_user_credentials").
		Columns("user_id", "user_name", "kind", "encrypted_token", "nonce", "expires_at").
		Values(userID, userName, kind, encrypted, nonce, expires).
		Suffix(`ON CONFLICT (user_id, kind) DO UPDATE SET
			user_name = EXCLUDED.user_name,
			encrypted_token = EXCLUDED.encrypted_token,
			nonce = EXCLUDED.nonce,
			expires_at = EXCLUDED.expires_at,
			updated_at = now()`).
		RunWith(f.conn).
		Exec()
	return err
}

const credentialColumns = `user_id, user_name, kind,
	COALESCE(EXTRACT(EPOCH FROM expires_at)::bigint, 0)`

func scanCredential(scan func(...any) error, withSecret bool) (credentials.Credential, string, *string, error) {
	var (
		cred  credentials.Credential
		enc   string
		nonce sql.NullString
	)
	dest := []any{&cred.UserID, &cred.UserName, &cred.Kind, &cred.ExpiresAt}
	if withSecret {
		dest = append(dest, &enc, &nonce)
	}
	if err := scan(dest...); err != nil {
		return credentials.Credential{}, "", nil, err
	}
	var noncePtr *string
	if nonce.Valid {
		noncePtr = &nonce.String
	}
	return cred, enc, noncePtr, nil
}

func (f *agentUserCredentialsFactory) Status(userID int) ([]credentials.Credential, error) {
	rows, err := f.conn.Query(
		`SELECT `+credentialColumns+` FROM agent_user_credentials WHERE user_id = $1 ORDER BY kind`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []credentials.Credential{}
	for rows.Next() {
		cred, _, _, err := scanCredential(rows.Scan, false)
		if err != nil {
			return nil, err
		}
		out = append(out, cred)
	}
	return out, rows.Err()
}

func (f *agentUserCredentialsFactory) Resolve(userID int, kind string) (*credentials.Credential, bool, error) {
	row := f.conn.QueryRow(
		`SELECT `+credentialColumns+`, encrypted_token, nonce
		 FROM agent_user_credentials WHERE user_id = $1 AND kind = $2`,
		userID, kind,
	)
	cred, enc, nonce, err := scanCredential(row.Scan, true)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	plain, err := f.conn.EncryptionStrategy().Decrypt(enc, nonce)
	if err != nil {
		return nil, false, err
	}
	cred.Token = string(plain)
	return &cred, true, nil
}

func (f *agentUserCredentialsFactory) Delete(userID int, kind string) error {
	_, err := psql.Delete("agent_user_credentials").
		Where(sq.Eq{"user_id": userID, "kind": kind}).
		RunWith(f.conn).
		Exec()
	return err
}
