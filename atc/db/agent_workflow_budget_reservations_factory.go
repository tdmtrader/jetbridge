package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

//counterfeiter:generate . AgentWorkflowBudgetReservationsFactory
type AgentWorkflowBudgetReservationsFactory interface {
	ReserveWorkflowBudget(context.Context, snapshot.WorkflowRunID, float64) (bool, error)
}

type AgentWorkflowBudgetConfig struct {
	GlobalDailyCapUSD float64
	Location          *time.Location
	Now               func() time.Time
}

func NewAgentWorkflowBudgetReservationsFactory(
	conn DbConn,
	config AgentWorkflowBudgetConfig,
) AgentWorkflowBudgetReservationsFactory {
	if config.Location == nil {
		config.Location = time.Local
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &agentWorkflowBudgetReservationsFactory{conn: conn, config: config}
}

type agentWorkflowBudgetReservationsFactory struct {
	conn   DbConn
	config AgentWorkflowBudgetConfig
}

func (factory *agentWorkflowBudgetReservationsFactory) ReserveWorkflowBudget(
	ctx context.Context,
	runID snapshot.WorkflowRunID,
	amount float64,
) (bool, error) {
	if ctx == nil || runID.Validate() != nil || !validWorkflowBudgetConfig(factory.config) ||
		math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return false, fmt.Errorf("db: invalid workflow budget reservation")
	}
	if factory.config.GlobalDailyCapUSD <= 0 || amount == 0 {
		return true, nil
	}
	amount = floorUSD(amount)
	if amount <= 0 {
		return false, fmt.Errorf("db: workflow budget reservation is below one micro-dollar")
	}

	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	if err := lockAgentBudgetAccounting(ctx, tx); err != nil {
		return false, err
	}

	var status AgentWorkflowRunStatus
	err = tx.QueryRowContext(ctx, `
		SELECT status FROM agent_workflow_runs WHERE id = $1 FOR UPDATE
	`, int64(runID)).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("db: workflow budget run does not exist")
	}
	if err != nil {
		return false, err
	}

	var existing float64
	var existingDay time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT reserved_usd::double precision, budget_day
		FROM agent_workflow_budget_reservations WHERE workflow_run_id = $1
	`, int64(runID)).Scan(&existing, &existingDay)
	if err == nil {
		if !sameMicroUSD(existing, amount) {
			return false, fmt.Errorf("db: workflow budget reservation conflicts with durable amount")
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if status != AgentWorkflowRunStatusAdmitting {
		return false, fmt.Errorf("db: workflow budget can only be reserved during admission")
	}

	budgetDay := factory.budgetDayStart()
	liability, err := globalDailyBudgetLiability(ctx, tx, budgetDay)
	if err != nil {
		return false, err
	}
	if exceedsBudget(liability+amount, factory.config.GlobalDailyCapUSD) {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_workflow_budget_reservations
			(workflow_run_id, reserved_usd, budget_day)
		SELECT id, $2, $3 FROM agent_workflow_runs
		WHERE id = $1 AND status = 'admitting'
	`, int64(runID), amount, budgetDay)
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if inserted != 1 {
		return false, fmt.Errorf("db: workflow budget reservation lost its admission lock")
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func validWorkflowBudgetConfig(config AgentWorkflowBudgetConfig) bool {
	return !math.IsNaN(config.GlobalDailyCapUSD) && !math.IsInf(config.GlobalDailyCapUSD, 0) &&
		config.GlobalDailyCapUSD >= 0 && config.Location != nil && config.Now != nil
}

func (factory *agentWorkflowBudgetReservationsFactory) budgetDayStart() time.Time {
	now := factory.config.Now().In(factory.config.Location)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, factory.config.Location)
}

func sameMicroUSD(left, right float64) bool {
	return math.Abs(left-right) <= 0.0000001
}

func workflowRunBudgetUsage(
	ctx context.Context,
	queryer agentExperimentQueryer,
	runID snapshot.WorkflowRunID,
) (float64, error) {
	// Spend carries the run id the server assigned it, so attribution is a
	// direct key lookup: no build/pipeline inference, and no anti-join to
	// keep two runs sharing a build from double-counting the same dollar.
	var spent float64
	err := queryer.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(ledger.cost_usd), 0)::double precision
		FROM agent_cost_ledger ledger
		WHERE ledger.workflow_run_id = $1
	`, int64(runID)).Scan(&spent)
	return spent, err
}
