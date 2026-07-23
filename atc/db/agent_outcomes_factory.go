package db

import (
	"database/sql"
	"fmt"

	"github.com/concourse/concourse/agent/api/outcomes"
)

//counterfeiter:generate . AgentOutcomesFactory
type AgentOutcomesFactory interface {
	outcomes.Store
}

func NewAgentOutcomesFactory(conn DbConn) AgentOutcomesFactory {
	return &agentOutcomesFactory{conn: conn}
}

type agentOutcomesFactory struct {
	conn DbConn
}

const outcomeColumns = `ticket_id, repo, branch, pushed_sha, base_sha, merge_state,
	merged_sha,
	EXTRACT(EPOCH FROM merged_at)::bigint,
	human_commit_count, human_lines_added, human_lines_deleted,
	disposition, disposition_reason, disposition_notes, disposed_by,
	EXTRACT(EPOCH FROM last_checked_at)::bigint,
	EXTRACT(EPOCH FROM created_at)::bigint,
	EXTRACT(EPOCH FROM updated_at)::bigint`

// Ensure inserts a fresh open row, or refreshes branch/pushed_sha/base_sha
// on an existing OPEN row (re-push after send-back). The WHERE-guarded
// UPDATE-in-DO leaves terminal rows untouched.
func (f *agentOutcomesFactory) Ensure(o *outcomes.Outcome) error {
	// The ON CONFLICT WHERE also re-arms a send-back row (closed_unmerged with
	// disposition='sent_back') back to 'open' and clears the disposition, so the
	// re-dispatch loop's eventual human merge is detected (F6). Truly terminal
	// rows (merged/merged_with_fixes, abandoned, or concluded) never match the
	// WHERE — 'concluded' is its own merge_state, so neither arm can touch it.
	_, err := f.conn.Exec(
		`INSERT INTO agent_outcomes (ticket_id, repo, branch, pushed_sha, base_sha, merge_state)
		 VALUES ($1, $2, $3, $4, $5, 'open')
		 ON CONFLICT (ticket_id) DO UPDATE SET
		   branch      = EXCLUDED.branch,
		   pushed_sha  = EXCLUDED.pushed_sha,
		   base_sha    = EXCLUDED.base_sha,
		   merge_state = 'open',
		   disposition = CASE WHEN agent_outcomes.merge_state = 'closed_unmerged'
		                      THEN '' ELSE agent_outcomes.disposition END,
		   disposition_reason = CASE WHEN agent_outcomes.merge_state = 'closed_unmerged'
		                             THEN '' ELSE agent_outcomes.disposition_reason END,
		   disposition_notes  = CASE WHEN agent_outcomes.merge_state = 'closed_unmerged'
		                             THEN '' ELSE agent_outcomes.disposition_notes END,
		   disposed_by = CASE WHEN agent_outcomes.merge_state = 'closed_unmerged'
		                      THEN '' ELSE agent_outcomes.disposed_by END,
		   updated_at = now()
		 WHERE agent_outcomes.merge_state = 'open'
		    OR (agent_outcomes.merge_state = 'closed_unmerged'
		        AND agent_outcomes.disposition = 'sent_back')`,
		o.TicketID, o.Repo, o.Branch, o.PushedSha, o.BaseSha,
	)
	return err
}

func (f *agentOutcomesFactory) Get(ticketID int) (*outcomes.Outcome, bool, error) {
	row := f.conn.QueryRow(
		`SELECT `+outcomeColumns+` FROM agent_outcomes WHERE ticket_id = $1`, ticketID,
	)
	o, err := scanOutcome(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return o, true, nil
}

func (f *agentOutcomesFactory) ListOpen() ([]outcomes.Outcome, error) {
	rows, err := f.conn.Query(
		`SELECT ` + outcomeColumns + ` FROM agent_outcomes
		 WHERE merge_state = 'open' ORDER BY ticket_id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []outcomes.Outcome{}
	for rows.Next() {
		o, err := scanOutcome(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, *o)
	}
	return results, rows.Err()
}

func (f *agentOutcomesFactory) ListTerminal() ([]outcomes.Outcome, error) {
	rows, err := f.conn.Query(
		`SELECT ` + outcomeColumns + ` FROM agent_outcomes
		 WHERE merge_state <> 'open' ORDER BY ticket_id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []outcomes.Outcome{}
	for rows.Next() {
		outcome, err := scanOutcome(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, *outcome)
	}
	return results, rows.Err()
}

func (f *agentOutcomesFactory) RecordMerge(ticketID int, res outcomes.MergeResult) error {
	if res.State != outcomes.Merged && res.State != outcomes.MergedWithFixes {
		return outcomes.ErrNotOpen
	}
	result, err := f.conn.Exec(
		`UPDATE agent_outcomes SET
		   merge_state = $2, merged_sha = $3, merged_at = now(),
		   human_commit_count = $4, human_lines_added = $5, human_lines_deleted = $6,
		   updated_at = now()
		 WHERE ticket_id = $1 AND merge_state = 'open'`,
		ticketID, string(res.State), res.MergedSha,
		res.HumanCommitCount, res.HumanLinesAdded, res.HumanLinesDeleted,
	)
	if err != nil {
		return err
	}
	return classifyOutcomeUpdate(f, ticketID, result)
}

func (f *agentOutcomesFactory) SetDisposition(ticketID int, d outcomes.DispositionInput) error {
	// Open rows become closed_unmerged — or 'concluded' for a concluded
	// disposition (positive terminal, §1.11.1); terminal rows keep merge_state.
	result, err := f.conn.Exec(
		`UPDATE agent_outcomes SET
		   disposition = $2, disposition_reason = $3, disposition_notes = $4, disposed_by = $5,
		   merge_state = CASE WHEN merge_state = 'open'
		                      THEN CASE WHEN $2 = 'concluded' THEN 'concluded' ELSE 'closed_unmerged' END
		                      ELSE merge_state END,
		   updated_at = now()
		 WHERE ticket_id = $1`,
		ticketID, d.Disposition, d.Reason, d.Notes, d.By,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return outcomes.ErrOutcomeNotFound
	}
	return nil
}

// Close closes an OPEN row to the given terminal merge_state without touching
// disposition columns — the watcher's terminal sweep for tickets closed by a
// bypassing raw-transition writer (§1.11.1 writer reconciliation). No-op when
// the row is absent or not open.
func (f *agentOutcomesFactory) Close(ticketID int, state outcomes.MergeState) error {
	if state != outcomes.ClosedUnmerged && state != outcomes.MergeConcluded {
		return fmt.Errorf("close: invalid terminal state %q", state)
	}
	_, err := f.conn.Exec(
		`UPDATE agent_outcomes
		 SET merge_state = $2, updated_at = now()
		 WHERE ticket_id = $1 AND merge_state = 'open'`,
		ticketID, string(state),
	)
	return err
}

func (f *agentOutcomesFactory) Touch(ticketID int) error {
	result, err := f.conn.Exec(
		`UPDATE agent_outcomes SET last_checked_at = now(), updated_at = now() WHERE ticket_id = $1`,
		ticketID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return outcomes.ErrOutcomeNotFound
	}
	return nil
}

// classifyOutcomeUpdate turns a zero-row RecordMerge into ErrOutcomeNotFound
// (row absent) or ErrNotOpen (row terminal), matching the MemoryStore.
func classifyOutcomeUpdate(f *agentOutcomesFactory, ticketID int, result sql.Result) error {
	if n, _ := result.RowsAffected(); n > 0 {
		return nil
	}
	if _, found, err := f.Get(ticketID); err != nil {
		return err
	} else if !found {
		return outcomes.ErrOutcomeNotFound
	}
	return outcomes.ErrNotOpen
}

func scanOutcome(row scannable) (*outcomes.Outcome, error) {
	var o outcomes.Outcome
	var mergedAt, lastChecked, createdAt, updatedAt sql.NullInt64
	if err := row.Scan(
		&o.TicketID, &o.Repo, &o.Branch, &o.PushedSha, &o.BaseSha, &o.MergeState,
		&o.MergedSha, &mergedAt,
		&o.HumanCommitCount, &o.HumanLinesAdded, &o.HumanLinesDeleted,
		&o.Disposition, &o.DispositionReason, &o.DispositionNotes, &o.DisposedBy,
		&lastChecked, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	o.MergedAt = mergedAt.Int64
	o.LastCheckedAt = lastChecked.Int64
	o.CreatedAt = createdAt.Int64
	o.UpdatedAt = updatedAt.Int64
	return &o, nil
}
