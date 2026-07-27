package db

import (
	"context"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/budget"
)

//counterfeiter:generate . AgentCostLedgerFactory
type AgentCostLedgerFactory interface {
	budget.Ledger
}

func NewAgentCostLedgerFactory(conn DbConn) AgentCostLedgerFactory {
	return &agentCostLedgerFactory{conn: conn}
}

type agentCostLedgerFactory struct {
	conn DbConn
}

func (f *agentCostLedgerFactory) Insert(entry budget.LedgerEntry) error {
	tx, err := f.conn.Begin()
	if err != nil {
		return err
	}
	defer Rollback(tx)
	if err := lockAgentBudgetAccounting(context.Background(), tx); err != nil {
		return err
	}
	var occurred any = sq.Expr("now()")
	if !entry.OccurredAt.IsZero() {
		occurred = entry.OccurredAt
	}
	provider := entry.Provider
	if provider == "" {
		provider = "anthropic"
	}
	var metadata any
	if len(entry.Metadata) > 0 {
		metadata = []byte(entry.Metadata)
	}
	var workflowRunID any
	if entry.WorkflowRunID != nil {
		workflowRunID = *entry.WorkflowRunID
	}
	_, err = psql.Insert("agent_cost_ledger").
		Columns(
			"occurred_at", "user_id", "user_name", "workflow_run_id", "function_id",
			"build_id", "step_name", "source", "provider", "model",
			"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
			"turns", "cost_usd", "metadata",
		).
		Values(
			occurred, entry.UserID, entry.UserName, workflowRunID, entry.FunctionID,
			entry.BuildID, entry.StepName, entry.Source, provider, entry.Model,
			entry.InputTokens, entry.OutputTokens, entry.CacheReadTokens, entry.CacheCreationTokens,
			entry.Turns, entry.CostUSD, metadata,
		).
		RunWith(tx).
		Exec()
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (f *agentCostLedgerFactory) SpentSince(since time.Time) (float64, error) {
	var spent float64
	err := f.conn.QueryRow(
		`SELECT COALESCE(SUM(cost_usd), 0)::float8 FROM agent_cost_ledger WHERE occurred_at >= $1`,
		since,
	).Scan(&spent)
	return spent, err
}

func (f *agentCostLedgerFactory) Rollup(groupBy string, since, until time.Time) ([]budget.RollupRow, error) {
	var keyExpr string
	from := ` FROM agent_cost_ledger ledger`
	switch groupBy {
	case budget.GroupByUser:
		keyExpr = `COALESCE(ledger.user_name, '')`
	case budget.GroupByWorkflow:
		// Workflow attribution is the server-owned run identity, so only
		// run-bound spend appears in this dimension (INNER JOIN).
		keyExpr = `run.workflow_name`
		from += `
		JOIN agent_workflow_runs run ON run.id = ledger.workflow_run_id`
	case budget.GroupByModel:
		keyExpr = `COALESCE(ledger.model, '')`
	case budget.GroupByStep:
		keyExpr = `COALESCE(ledger.step_name, '')`
	case budget.GroupByDay:
		keyExpr = `to_char((ledger.occurred_at AT TIME ZONE 'UTC')::date, 'YYYY-MM-DD')`
	default:
		return nil, fmt.Errorf("unsupported group_by %q", groupBy)
	}

	query := `SELECT ` + keyExpr + ` AS key,
		COUNT(*)::int,
		COALESCE(SUM(ledger.input_tokens), 0)::bigint,
		COALESCE(SUM(ledger.output_tokens), 0)::bigint,
		COALESCE(SUM(ledger.turns), 0)::bigint,
		COALESCE(SUM(ledger.cost_usd), 0)::float8` +
		from + `
		WHERE ledger.occurred_at >= $1`
	args := []any{since}
	if !until.IsZero() {
		args = append(args, until)
		query += ` AND ledger.occurred_at < $2`
	}
	query += ` GROUP BY 1 ORDER BY 1`

	rows, err := f.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []budget.RollupRow{}
	for rows.Next() {
		var row budget.RollupRow
		if err := rows.Scan(&row.Key, &row.Entries, &row.InputTokens, &row.OutputTokens, &row.Turns, &row.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
