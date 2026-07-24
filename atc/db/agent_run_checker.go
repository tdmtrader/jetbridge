package db

import (
	"errors"

	"github.com/concourse/concourse/agent/credentials"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
)

// AgentRunChecker implements credentials.RunChecker (the RunSecretReaper's
// narrow RunActive seam, final-review F22) over the pipeline_runs table
// (contracts §1.5, owned by the pipeline-runs workstream).
type AgentRunChecker struct {
	conn DbConn
}

func NewAgentRunChecker(conn DbConn) *AgentRunChecker {
	return &AgentRunChecker{conn: conn}
}

// RunActive reports whether the run row exists and is still active:
// running, or parked in the non-terminal awaiting_human state — per
// PARK-V2 (contracts §11) awaiting_human COUNTS AS ACTIVE, so the
// agent-run-<run-id> secret survives the wait
// for the continuation to re-attach. Absent rows are inactive — the run
// finished, was deleted, or was never created; either way its secret
// must not outlive it. An absent pipeline_runs TABLE (credentials merges
// before pipeline-runs per the Task 1 merge-order addendum) also means
// no run can be active: dispatch does not exist yet, so any labeled
// secret is a stray.
func (c *AgentRunChecker) RunActive(runID int) (bool, error) {
	var active bool
	err := c.conn.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM pipeline_runs WHERE id = $1 AND status IN ('running','awaiting_human'))`,
		runID,
	).Scan(&active)

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UndefinedTable {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return active, nil
}

var _ credentials.RunChecker = (*AgentRunChecker)(nil)
