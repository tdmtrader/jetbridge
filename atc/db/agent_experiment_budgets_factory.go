package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot"
)

// Ledger inserts and experiment reservations take the same transaction-level
// advisory lock. This closes the read-ledger/reserve race across ATC web nodes.
const agentBudgetAdvisoryLockKey int64 = 0x0a63e7b0d9e7

type storedExperimentBudgetReservation struct {
	experiment.BudgetReservation
	experimentID experiment.ID
	state        string
}

func (factory *agentExperimentsFactory) ReserveCandidateBudget(
	ctx context.Context,
	cellID experiment.CellID,
) (experiment.BudgetReservation, error) {
	if ctx == nil || cellID.Validate() != nil {
		return experiment.BudgetReservation{}, fmt.Errorf("db: invalid experiment budget reservation")
	}
	if !validAgentExperimentBudgetConfig(factory.budgetConfig) {
		return experiment.BudgetReservation{}, fmt.Errorf("db: invalid experiment budget configuration")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return experiment.BudgetReservation{}, err
	}
	defer Rollback(tx)
	if err := lockAgentBudgetAccounting(ctx, tx); err != nil {
		return experiment.BudgetReservation{}, err
	}

	if existing, found, err := loadExperimentBudgetReservation(ctx, tx, cellID, true); err != nil {
		return experiment.BudgetReservation{}, err
	} else if found {
		if existing.state != "active" {
			return experiment.BudgetReservation{}, experiment.ErrBudgetExhausted
		}
		if err := tx.Commit(); err != nil {
			return experiment.BudgetReservation{}, err
		}
		return existing.BudgetReservation, nil
	}

	var experimentID experiment.ID
	var experimentState experiment.State
	var cellStatus experiment.CellStatus
	var budget experiment.Budget
	var expectedCells int
	err = tx.QueryRowContext(ctx, `
		SELECT cell.experiment_id, parent.state, cell.status,
		       parent.per_cell_budget_usd::double precision,
		       parent.total_budget_usd::double precision,
		       parent.max_tokens_per_cell,
		       (SELECT count(*) FROM agent_experiment_cells sibling
		        WHERE sibling.experiment_id = cell.experiment_id)
		FROM agent_experiment_cells cell
		JOIN agent_experiments parent ON parent.id = cell.experiment_id
		WHERE cell.id = $1
		FOR UPDATE OF cell, parent
	`, int64(cellID)).Scan(&experimentID, &experimentState, &cellStatus,
		&budget.PerCellUSD, &budget.TotalUSD, &budget.MaxTokensPerCell, &expectedCells)
	if errors.Is(err, sql.ErrNoRows) {
		return experiment.BudgetReservation{}, experiment.ErrBudgetExhausted
	}
	if err != nil {
		return experiment.BudgetReservation{}, err
	}
	if experimentState != experiment.StateRunning ||
		(cellStatus != experiment.CellPending && cellStatus != experiment.CellRunning) ||
		expectedCells <= 0 || budget.Validate() != nil || !budget.Limited() ||
		budget.MaxTokensPerCell > 0 {
		return experiment.BudgetReservation{}, experiment.ErrBudgetExhausted
	}

	amount := reservationAmount(budget, expectedCells)
	if (budget.PerCellUSD > 0 || budget.TotalUSD > 0) && amount <= 0 {
		return experiment.BudgetReservation{}, experiment.ErrBudgetExhausted
	}
	if budget.TotalUSD > 0 {
		liability, err := experimentBudgetLiability(ctx, tx, experimentID)
		if err != nil {
			return experiment.BudgetReservation{}, err
		}
		if exceedsBudget(liability+amount, budget.TotalUSD) {
			return experiment.BudgetReservation{}, experiment.ErrBudgetExhausted
		}
	}
	if factory.budgetConfig.GlobalDailyCapUSD > 0 {
		liability, err := globalDailyBudgetLiability(ctx, tx, factory.budgetDayStart())
		if err != nil {
			return experiment.BudgetReservation{}, err
		}
		if exceedsBudget(liability+amount, factory.budgetConfig.GlobalDailyCapUSD) {
			return experiment.BudgetReservation{}, experiment.ErrBudgetExhausted
		}
	}

	reservation := experiment.BudgetReservation{
		CellID: cellID, LimitUSD: amount, MaxTokens: budget.MaxTokensPerCell,
	}
	var inserted experiment.CellID
	err = tx.QueryRowContext(ctx, `
		INSERT INTO agent_experiment_budget_reservations
			(cell_id, experiment_id, reserved_usd, max_tokens, state, budget_day)
		SELECT cell.id, cell.experiment_id, $2, $3, 'active', $4
		FROM agent_experiment_cells cell
		JOIN agent_experiments parent ON parent.id = cell.experiment_id
		WHERE cell.id = $1 AND cell.experiment_id = $5
		  AND parent.state = 'running' AND cell.status IN ('pending', 'running')
		ON CONFLICT (cell_id) DO NOTHING
		RETURNING cell_id
	`, int64(cellID), reservation.LimitUSD, reservation.MaxTokens,
		factory.budgetDayStart(), int64(experimentID)).Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		return experiment.BudgetReservation{}, fmt.Errorf("db: experiment budget reservation lost its admission lock")
	}
	if err != nil {
		return experiment.BudgetReservation{}, err
	}
	if inserted != cellID {
		return experiment.BudgetReservation{}, fmt.Errorf("db: experiment budget reservation identity mismatch")
	}
	if err := tx.Commit(); err != nil {
		return experiment.BudgetReservation{}, err
	}
	return reservation, nil
}

func (factory *agentExperimentsFactory) AdmitEvaluatorBudget(
	ctx context.Context,
	cellID experiment.CellID,
) (experiment.BudgetReservation, error) {
	if ctx == nil || cellID.Validate() != nil {
		return experiment.BudgetReservation{}, fmt.Errorf("db: invalid evaluator budget admission")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return experiment.BudgetReservation{}, err
	}
	defer Rollback(tx)
	if err := lockAgentBudgetAccounting(ctx, tx); err != nil {
		return experiment.BudgetReservation{}, err
	}
	reservation, found, err := loadExperimentBudgetReservation(ctx, tx, cellID, true)
	if err != nil {
		return experiment.BudgetReservation{}, err
	}
	if !found || reservation.state != "active" {
		return experiment.BudgetReservation{}, experiment.ErrBudgetExhausted
	}
	// Start admission proves that the combined candidate and evaluator agent
	// slices fit this immutable reservation. Do not reject an evaluator merely
	// because the candidate consumed the envelope exactly: a deterministic
	// evaluator still costs nothing, while agent evaluators remain bounded by
	// their already-validated slice. CheckCellBudget enforces the final actual
	// usage before accepting measurements.
	if err := tx.Commit(); err != nil {
		return experiment.BudgetReservation{}, err
	}
	return reservation.BudgetReservation, nil
}

func (factory *agentExperimentsFactory) CheckCellBudget(
	ctx context.Context,
	cellID experiment.CellID,
) (experiment.BudgetUsage, error) {
	if ctx == nil || cellID.Validate() != nil {
		return experiment.BudgetUsage{}, fmt.Errorf("db: invalid experiment budget check")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return experiment.BudgetUsage{}, err
	}
	defer Rollback(tx)
	if err := lockAgentBudgetAccounting(ctx, tx); err != nil {
		return experiment.BudgetUsage{}, err
	}
	reservation, found, err := loadExperimentBudgetReservation(ctx, tx, cellID, true)
	if err != nil {
		return experiment.BudgetUsage{}, err
	}
	if !found || reservation.state != "active" {
		return experiment.BudgetUsage{}, experiment.ErrBudgetExhausted
	}
	usage, _, err := cellBudgetUsage(ctx, tx, cellID)
	if err != nil {
		return experiment.BudgetUsage{}, err
	}
	if err := tx.Commit(); err != nil {
		return experiment.BudgetUsage{}, err
	}
	if budgetUsageExceedsReservation(reservation.BudgetReservation, usage) {
		return usage, experiment.ErrBudgetExhausted
	}
	return usage, nil
}

func validAgentExperimentBudgetConfig(config AgentExperimentBudgetConfig) bool {
	return !math.IsNaN(config.GlobalDailyCapUSD) && !math.IsInf(config.GlobalDailyCapUSD, 0) &&
		config.GlobalDailyCapUSD >= 0 && config.Location != nil && config.Now != nil
}

func (factory *agentExperimentsFactory) budgetDayStart() time.Time {
	now := factory.budgetConfig.Now().In(factory.budgetConfig.Location)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, factory.budgetConfig.Location)
}

func reservationAmount(budget experiment.Budget, expectedCells int) float64 {
	if budget.PerCellUSD > 0 {
		return floorUSD(budget.PerCellUSD)
	}
	if budget.TotalUSD > 0 && expectedCells > 0 {
		return floorUSD(budget.TotalUSD / float64(expectedCells))
	}
	return 0
}

func floorUSD(value float64) float64 { return math.Floor(value*1_000_000) / 1_000_000 }

func exceedsBudget(value, limit float64) bool { return value > limit+0.0000001 }

func budgetUsageExceedsReservation(reservation experiment.BudgetReservation, usage experiment.BudgetUsage) bool {
	return (reservation.LimitUSD > 0 && exceedsBudget(usage.CostUSD, reservation.LimitUSD)) ||
		(reservation.MaxTokens > 0 && usage.Tokens > reservation.MaxTokens)
}

func lockAgentBudgetAccounting(ctx context.Context, tx Tx) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, agentBudgetAdvisoryLockKey)
	return err
}

func loadExperimentBudgetReservation(
	ctx context.Context,
	queryer agentExperimentQueryer,
	cellID experiment.CellID,
	forUpdate bool,
) (storedExperimentBudgetReservation, bool, error) {
	query := `
		SELECT cell_id, experiment_id, reserved_usd::double precision, max_tokens, state
		FROM agent_experiment_budget_reservations WHERE cell_id = $1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var value storedExperimentBudgetReservation
	err := queryer.QueryRowContext(ctx, query, int64(cellID)).Scan(
		&value.CellID, &value.experimentID, &value.LimitUSD, &value.MaxTokens, &value.state,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedExperimentBudgetReservation{}, false, nil
	}
	return value, err == nil, err
}

func cellBudgetUsage(ctx context.Context, queryer agentExperimentQueryer, cellID experiment.CellID) (experiment.BudgetUsage, bool, error) {
	var usage experiment.BudgetUsage
	var hasRun bool
	err := queryer.QueryRowContext(ctx, `
		WITH cell_identity AS (
			SELECT cell.id, cell.experiment_id, cell.candidate_workflow_run_id
			FROM agent_experiment_cells cell WHERE cell.id = $1
		), run_ids AS (
			SELECT candidate_workflow_run_id AS id FROM cell_identity
			WHERE candidate_workflow_run_id IS NOT NULL
			UNION
			SELECT evaluation.evaluator_workflow_run_id
			FROM agent_experiment_evaluations evaluation
			JOIN cell_identity ON cell_identity.id = evaluation.cell_id
			WHERE evaluation.evaluator_workflow_run_id IS NOT NULL
			UNION
			SELECT run.id
			FROM agent_workflow_runs run
			JOIN cell_identity ON run.origin_kind = 'experiment'
			 AND run.origin_reference IN (
				'experiment:' || cell_identity.experiment_id::text || ':cell:' || cell_identity.id::text,
				'experiment:' || cell_identity.experiment_id::text || ':cell:' || cell_identity.id::text || ':evaluator'
			 )
		), run_links AS (
			SELECT run.id, run.planned_build_id, run.pipeline_run_id
			FROM agent_workflow_runs run JOIN run_ids ON run_ids.id = run.id
		), build_ids AS (
			SELECT planned_build_id AS id FROM run_links WHERE planned_build_id IS NOT NULL
			UNION
			SELECT anomaly.build_id FROM agent_workflow_run_anomalies anomaly
			JOIN run_ids ON run_ids.id = anomaly.workflow_run_id
		), pipeline_ids AS (
			SELECT pipeline_run_id AS id FROM run_links WHERE pipeline_run_id IS NOT NULL
		), totals AS (
			SELECT COALESCE(SUM(ledger.cost_usd), 0)::double precision AS cost,
			       COALESCE(SUM(ledger.input_tokens + ledger.output_tokens), 0)::bigint AS tokens
			FROM agent_cost_ledger ledger
			WHERE (ledger.build_id > 0 AND ledger.build_id IN (SELECT id FROM build_ids))
			   OR (ledger.pipeline_run_id IS NOT NULL AND ledger.pipeline_run_id IN (SELECT id FROM pipeline_ids))
		)
		SELECT totals.cost, totals.tokens, EXISTS (SELECT 1 FROM run_ids)
		FROM totals
	`, int64(cellID)).Scan(&usage.CostUSD, &usage.Tokens, &hasRun)
	return usage, hasRun, err
}

func experimentBudgetLiability(ctx context.Context, queryer agentExperimentQueryer, experimentID experiment.ID) (float64, error) {
	actual, err := experimentActualSpend(ctx, queryer, experimentID)
	if err != nil {
		return 0, err
	}
	rows, err := queryer.QueryContext(ctx, `
		SELECT cell_id, reserved_usd::double precision
		FROM agent_experiment_budget_reservations
		WHERE experiment_id = $1 AND state IN ('active', 'settled') ORDER BY cell_id
	`, int64(experimentID))
	if err != nil {
		return 0, err
	}
	type activeReservation struct {
		cellID   experiment.CellID
		reserved float64
	}
	var reservations []activeReservation
	for rows.Next() {
		var reservation activeReservation
		if err := rows.Scan(&reservation.cellID, &reservation.reserved); err != nil {
			_ = rows.Close()
			return 0, err
		}
		reservations = append(reservations, reservation)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	remaining := 0.0
	for _, reservation := range reservations {
		usage, _, err := cellBudgetUsage(ctx, queryer, reservation.cellID)
		if err != nil {
			return 0, err
		}
		remaining += math.Max(reservation.reserved-usage.CostUSD, 0)
	}
	return actual + remaining, nil
}

func experimentActualSpend(ctx context.Context, queryer agentExperimentQueryer, experimentID experiment.ID) (float64, error) {
	var spent float64
	err := queryer.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(ledger.cost_usd), 0)::double precision
		FROM agent_cost_ledger ledger
		WHERE EXISTS (
			SELECT 1
			FROM agent_experiment_cells cell
			JOIN agent_workflow_runs run ON run.origin_kind = 'experiment'
			 AND run.origin_reference IN (
				'experiment:' || cell.experiment_id::text || ':cell:' || cell.id::text,
				'experiment:' || cell.experiment_id::text || ':cell:' || cell.id::text || ':evaluator'
			 )
			WHERE cell.experiment_id = $1
			  AND (
				(ledger.build_id > 0 AND (
					ledger.build_id = run.planned_build_id OR EXISTS (
						SELECT 1 FROM agent_workflow_run_anomalies anomaly
						WHERE anomaly.workflow_run_id = run.id AND anomaly.build_id = ledger.build_id
					)
				))
				OR (ledger.pipeline_run_id IS NOT NULL AND ledger.pipeline_run_id = run.pipeline_run_id)
			  )
		)
	`, int64(experimentID)).Scan(&spent)
	return spent, err
}

func globalDailyBudgetLiability(ctx context.Context, queryer agentExperimentQueryer, since time.Time) (float64, error) {
	var actual float64
	if err := queryer.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(cost_usd), 0)::double precision
		FROM agent_cost_ledger WHERE occurred_at >= $1
	`, since).Scan(&actual); err != nil {
		return 0, err
	}
	rows, err := queryer.QueryContext(ctx, `
		SELECT cell_id, reserved_usd::double precision
		FROM agent_experiment_budget_reservations
		WHERE state = 'active' OR (state = 'settled' AND settled_at >= $1)
		ORDER BY cell_id
	`, since)
	if err != nil {
		return 0, err
	}
	type activeReservation struct {
		cellID   experiment.CellID
		reserved float64
	}
	var reservations []activeReservation
	for rows.Next() {
		var reservation activeReservation
		if err := rows.Scan(&reservation.cellID, &reservation.reserved); err != nil {
			_ = rows.Close()
			return 0, err
		}
		reservations = append(reservations, reservation)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	remaining := 0.0
	for _, reservation := range reservations {
		usage, _, err := cellBudgetUsage(ctx, queryer, reservation.cellID)
		if err != nil {
			return 0, err
		}
		remaining += math.Max(reservation.reserved-usage.CostUSD, 0)
	}
	workflowRows, err := queryer.QueryContext(ctx, `
		SELECT reservation.workflow_run_id, reservation.reserved_usd::double precision
		FROM agent_workflow_budget_reservations reservation
		JOIN agent_workflow_runs run ON run.id = reservation.workflow_run_id
		WHERE run.status IN ('admitting', 'running', 'canceling')
		   OR run.completed_at >= $1
		ORDER BY reservation.workflow_run_id
	`, since)
	if err != nil {
		return 0, err
	}
	type workflowReservation struct {
		runID    snapshot.WorkflowRunID
		reserved float64
	}
	var workflowReservations []workflowReservation
	for workflowRows.Next() {
		var reservation workflowReservation
		if err := workflowRows.Scan(&reservation.runID, &reservation.reserved); err != nil {
			_ = workflowRows.Close()
			return 0, err
		}
		workflowReservations = append(workflowReservations, reservation)
	}
	if err := workflowRows.Close(); err != nil {
		return 0, err
	}
	for _, reservation := range workflowReservations {
		usage, err := workflowRunBudgetUsage(ctx, queryer, reservation.runID)
		if err != nil {
			return 0, err
		}
		remaining += math.Max(reservation.reserved-usage, 0)
	}
	return actual + remaining, nil
}

func settleExperimentBudgetReservation(ctx context.Context, tx Tx, cellID experiment.CellID) error {
	reservation, found, err := loadExperimentBudgetReservation(ctx, tx, cellID, true)
	if err != nil || !found || reservation.state != "active" {
		return err
	}
	usage, hasRun, err := cellBudgetUsage(ctx, tx, cellID)
	if err != nil {
		return err
	}
	state := "settled"
	if !hasRun && usage.CostUSD == 0 && usage.Tokens == 0 {
		state = "released"
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_experiment_budget_reservations
		SET state = $2, settled_cost_usd = $3, settled_tokens = $4,
		    settled_at = now(), updated_at = now()
		WHERE cell_id = $1 AND state = 'active'
	`, int64(cellID), state, usage.CostUSD, usage.Tokens)
	return err
}
