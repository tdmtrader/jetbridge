package experiment

import (
	"context"
	"errors"
	"fmt"
)

// ErrBudgetExhausted is a durable admission result, not an infrastructure
// error. Callers terminalize the cell without creating more workflow runs.
var ErrBudgetExhausted = errors.New("experiment: budget exhausted")

// BudgetReservation is the immutable envelope allocated to one experiment
// cell. Candidate and evaluator runs share it; retries return the same row.
type BudgetReservation struct {
	CellID    CellID
	LimitUSD  float64
	MaxTokens int64
}

func (reservation BudgetReservation) Validate(cellID CellID) error {
	if reservation.CellID != cellID {
		return fmt.Errorf("experiment: budget reservation cell mismatch")
	}
	if !finiteNonnegative(reservation.LimitUSD) || reservation.MaxTokens < 0 {
		return fmt.Errorf("experiment: invalid budget reservation")
	}
	return nil
}

// BudgetUsage is sourced from the durable cost ledger for every candidate and
// evaluator workflow run linked to a cell.
type BudgetUsage struct {
	CostUSD float64
	Tokens  int64
}

// BudgetController is an optional persistence extension. It is mandatory for
// any cell whose experiment declares a nonzero limit. Implementations must
// make ReserveCandidateBudget idempotent and concurrency-safe.
type BudgetController interface {
	ReserveCandidateBudget(context.Context, CellID) (BudgetReservation, error)
	AdmitEvaluatorBudget(context.Context, CellID) (BudgetReservation, error)
	CheckCellBudget(context.Context, CellID) (BudgetUsage, error)
}
