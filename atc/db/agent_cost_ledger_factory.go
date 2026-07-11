package db

import (
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
	_, err := psql.Insert("agent_cost_ledger").
		Columns(
			"occurred_at", "user_id", "user_name", "ticket_id", "pipeline_run_id",
			"build_id", "step_name", "source", "provider", "model",
			"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
			"turns", "cost_usd", "metadata",
		).
		Values(
			occurred, entry.UserID, entry.UserName, entry.TicketID, entry.PipelineRunID,
			entry.BuildID, entry.StepName, entry.Source, provider, entry.Model,
			entry.InputTokens, entry.OutputTokens, entry.CacheReadTokens, entry.CacheCreationTokens,
			entry.Turns, entry.CostUSD, metadata,
		).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *agentCostLedgerFactory) SpentForTicket(ticketID int) (float64, error) {
	// harvest_judge spend never depletes the ticket budget (§1.13); the
	// judge is capped separately by the workflow's judge_usd.
	var spent float64
	err := f.conn.QueryRow(
		`SELECT COALESCE(SUM(cost_usd), 0)::float8 FROM agent_cost_ledger
		 WHERE ticket_id = $1 AND source <> 'harvest_judge'`,
		ticketID,
	).Scan(&spent)
	return spent, err
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
	switch groupBy {
	case budget.GroupByUser:
		keyExpr = `COALESCE(user_name, '')`
	case budget.GroupByTicket:
		keyExpr = `COALESCE(ticket_id::text, '')`
	case budget.GroupByWorkflow:
		// Contract addendum: workflow attribution rides metadata->>'workflow'.
		keyExpr = `COALESCE(metadata->>'workflow', '')`
	case budget.GroupByDay:
		keyExpr = `to_char((occurred_at AT TIME ZONE 'UTC')::date, 'YYYY-MM-DD')`
	default:
		return nil, fmt.Errorf("unsupported group_by %q", groupBy)
	}

	query := `SELECT ` + keyExpr + ` AS key,
		COUNT(*)::int,
		COALESCE(SUM(input_tokens), 0)::bigint,
		COALESCE(SUM(output_tokens), 0)::bigint,
		COALESCE(SUM(turns), 0)::bigint,
		COALESCE(SUM(cost_usd), 0)::float8
		FROM agent_cost_ledger
		WHERE occurred_at >= $1`
	args := []any{since}
	if !until.IsZero() {
		args = append(args, until)
		query += ` AND occurred_at < $2`
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
