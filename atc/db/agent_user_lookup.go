package db

import (
	"database/sql"
	"errors"
)

// AgentUserLookup resolves users.id from a username for agent-ticket user
// attribution (§2.8.2 user-first credential resolution; dispatch remainder
// 2026-07-17). Usernames are not unique across connectors — the most
// recently logged-in row wins, which is exact for single-connector
// deployments and a documented approximation otherwise.
type AgentUserLookup interface {
	FindByUsername(username string) (int, bool, error)
}

func NewAgentUserLookup(conn DbConn) AgentUserLookup {
	return &agentUserLookup{conn: conn}
}

type agentUserLookup struct{ conn DbConn }

func (l *agentUserLookup) FindByUsername(username string) (int, bool, error) {
	if username == "" {
		return 0, false, nil
	}
	var id int
	err := l.conn.QueryRow(
		`SELECT id FROM users WHERE username = $1 ORDER BY last_login DESC NULLS LAST, id DESC LIMIT 1`,
		username,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}
