package db

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/api/principals"
	"github.com/jackc/pgx/v5/pgtype"
)

//counterfeiter:generate . AgentPrincipalsFactory
type AgentPrincipalsFactory interface {
	principals.Store
	RevokeByName(string) error
}

func NewAgentPrincipalsFactory(conn DbConn) AgentPrincipalsFactory {
	return &agentPrincipalsFactory{conn: conn}
}

type agentPrincipalsFactory struct {
	conn DbConn
}

// Create mints a principal. The token embeds the row id
// (cap1.<id>.<secret>), so the id is drawn from the sequence first and
// the row is inserted with hash+prefix already computed. The raw token
// is returned exactly once and never stored.
func (f *agentPrincipalsFactory) Create(spec principals.CreateSpec) (principals.Principal, string, error) {
	var id int
	err := f.conn.QueryRow(
		`SELECT nextval(pg_get_serial_sequence('agent_principals', 'id'))`,
	).Scan(&id)
	if err != nil {
		return principals.Principal{}, "", err
	}

	token, prefix, hash, err := principals.MintToken(id)
	if err != nil {
		return principals.Principal{}, "", err
	}

	teamName := spec.TeamName
	if teamName == "" {
		teamName = "main"
	}

	var expiresAt *time.Time
	if spec.ExpiresAt != nil {
		t := time.Unix(*spec.ExpiresAt, 0)
		expiresAt = &t
	}

	var createdAt int64
	err = psql.Insert("agent_principals").
		Columns("id", "name", "description", "token_prefix", "token_hash",
			"scopes", "team_name", "created_by", "expires_at").
		Values(id, spec.Name, spec.Description, prefix, hash,
			spec.Scopes, teamName, spec.CreatedBy, expiresAt).
		Suffix("RETURNING EXTRACT(EPOCH FROM created_at)::bigint").
		RunWith(f.conn).
		QueryRow().
		Scan(&createdAt)
	if err != nil {
		return principals.Principal{}, "", err
	}

	return principals.Principal{
		ID:          id,
		Name:        spec.Name,
		Description: spec.Description,
		TokenPrefix: prefix,
		TokenHash:   hash,
		Scopes:      append([]string{}, spec.Scopes...),
		TeamName:    teamName,
		CreatedBy:   spec.CreatedBy,
		CreatedAt:   createdAt,
		ExpiresAt:   spec.ExpiresAt,
	}, token, nil
}

const principalColumns = `id, name, description, token_prefix, token_hash,
	scopes, team_name, created_by,
	EXTRACT(EPOCH FROM created_at)::bigint,
	EXTRACT(EPOCH FROM expires_at)::bigint,
	EXTRACT(EPOCH FROM revoked_at)::bigint,
	EXTRACT(EPOCH FROM last_used_at)::bigint`

func (f *agentPrincipalsFactory) List() ([]principals.Principal, error) {
	rows, err := f.conn.Query(
		`SELECT ` + principalColumns + ` FROM agent_principals ORDER BY id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []principals.Principal{}
	for rows.Next() {
		p, err := scanPrincipal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (f *agentPrincipalsFactory) Get(id int) (principals.Principal, bool, error) {
	row := f.conn.QueryRow(
		`SELECT `+principalColumns+` FROM agent_principals WHERE id = $1`, id,
	)
	p, err := scanPrincipal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return principals.Principal{}, false, nil
	}
	if err != nil {
		return principals.Principal{}, false, err
	}
	return p, true, nil
}

func (f *agentPrincipalsFactory) Revoke(id int) (bool, error) {
	res, err := psql.Update("agent_principals").
		Set("revoked_at", sq.Expr("COALESCE(revoked_at, now())")).
		Where(sq.Eq{"id": id}).
		RunWith(f.conn).
		Exec()
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RevokeByName durably revokes every principal with the exact name. Per-run
// principals use the immutable agent-run-<pipeline-run-id> name, so this is
// safe to replay after an ATC restart and also closes any duplicate-mint crash
// window without relying on an in-memory principal index.
func (f *agentPrincipalsFactory) RevokeByName(name string) error {
	if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
		return errors.New("db: agent principal name is required")
	}
	_, err := psql.Update("agent_principals").
		Set("revoked_at", sq.Expr("COALESCE(revoked_at, now())")).
		Where(sq.Eq{"name": name}).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *agentPrincipalsFactory) RecordUse(id int, usedAt time.Time) error {
	_, err := psql.Update("agent_principals").
		Set("last_used_at", usedAt).
		Where(sq.Eq{"id": id}).
		RunWith(f.conn).
		Exec()
	return err
}

func scanPrincipal(row interface{ Scan(...any) error }) (principals.Principal, error) {
	var p principals.Principal
	var expiresAt, revokedAt, lastUsedAt sql.NullInt64
	m := pgtype.NewMap()
	err := row.Scan(
		&p.ID, &p.Name, &p.Description, &p.TokenPrefix, &p.TokenHash,
		m.SQLScanner(&p.Scopes), &p.TeamName, &p.CreatedBy, &p.CreatedAt,
		&expiresAt, &revokedAt, &lastUsedAt,
	)
	if err != nil {
		return principals.Principal{}, err
	}
	if expiresAt.Valid {
		p.ExpiresAt = &expiresAt.Int64
	}
	if revokedAt.Valid {
		p.RevokedAt = &revokedAt.Int64
	}
	if lastUsedAt.Valid {
		p.LastUsedAt = &lastUsedAt.Int64
	}
	return p, nil
}
