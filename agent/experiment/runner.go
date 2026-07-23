package experiment

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/concourse/concourse/agent/snapshot"
)

var (
	ErrBindBudgetDenied    = errors.New("experiment binder: budget denied")
	ErrBindInvalidRequest  = errors.New("experiment binder: invalid admission")
	ErrBindAdmissionClosed = errors.New("experiment binder: admission closed")
)

type AdmissionPhase string

const (
	AdmissionCandidate AdmissionPhase = "candidate"
	AdmissionEvaluator AdmissionPhase = "evaluator"
)

type AdmissionGate struct {
	ExperimentID ID
	CellID       CellID
	Phase        AdmissionPhase
}

func (gate AdmissionGate) Validate() error {
	if err := gate.ExperimentID.Validate(); err != nil {
		return err
	}
	if err := gate.CellID.Validate(); err != nil {
		return err
	}
	switch gate.Phase {
	case AdmissionCandidate, AdmissionEvaluator:
		return nil
	default:
		return fmt.Errorf("experiment binder: invalid admission phase")
	}
}

type CandidateCell struct {
	ID               CellID
	ExperimentID     ID
	FixtureID        int64
	VariantID        int64
	TeamID           int
	TeamName         string
	CreatedBy        string
	Repetition       int
	Target           Target
	TargetConfigHash string
	Inputs           map[string]snapshot.SnapshotID
	Budget           Budget
}

func (cell CandidateCell) Validate() error {
	if err := cell.ID.Validate(); err != nil {
		return err
	}
	if err := cell.ExperimentID.Validate(); err != nil {
		return err
	}
	if cell.FixtureID <= 0 || cell.VariantID <= 0 {
		return fmt.Errorf("experiment runner: fixture and variant IDs must be positive")
	}
	if cell.TeamID <= 0 || cell.TeamName == "" || cell.CreatedBy == "" {
		return fmt.Errorf("experiment runner: durable team and creator identity are required")
	}
	if cell.Repetition <= 0 {
		return fmt.Errorf("experiment runner: repetition must be positive")
	}
	if err := cell.Target.Validate(); err != nil {
		return err
	}
	if !hashPattern.MatchString(cell.TargetConfigHash) {
		return fmt.Errorf("experiment runner: frozen target config hash is required")
	}
	if len(cell.Inputs) == 0 {
		return fmt.Errorf("experiment runner: fixture inputs are required")
	}
	for port, id := range cell.Inputs {
		if port == "" {
			return fmt.Errorf("experiment runner: fixture input port is required")
		}
		if err := id.Validate(); err != nil {
			return fmt.Errorf("experiment runner: fixture input %q: %w", port, err)
		}
	}
	if err := cell.Budget.Validate(); err != nil {
		return err
	}
	return nil
}

type RunnerStore interface {
	ClaimCandidateCells(context.Context, int) ([]CandidateCell, error)
	CreateAndRecordCandidateRun(context.Context, CandidateCell, CandidateRunCreator) (CandidateRunAdmission, error)
	RecordCandidateRun(context.Context, CellID, snapshot.WorkflowRunID) (bool, error)
	RecordCandidateFailure(context.Context, CellID, string) error
}

type CandidateRunCreator func(context.Context) (BindResult, error)

// CandidateRunAdmission reports the result of gated durable child allocation
// plus exact cell association. Started=false means the allocator's short
// transaction observed a closed parent before creating or reusing a child;
// target preparation may already have run outside that transaction.
type CandidateRunAdmission struct {
	Started   bool
	Result    BindResult
	BindError error
	Recorded  bool
}

// Binding DTOs keep experiment scheduling independent of ATC's persistence
// package. The workflowrun adapter translates these at the composition seam.
type Origin struct {
	Kind      string
	Reference string
}

type AdmissionContext struct {
	TeamID    int
	TeamName  string
	CreatedBy string
	Origin    Origin
}

type BindRequest struct {
	WorkflowName             string
	DefinitionID             int64
	Version                  *int
	FunctionID               string
	Inputs                   map[string]snapshot.SnapshotID
	IdempotencyKey           string
	ExpectedTargetConfigHash string
	AdmissionGate            AdmissionGate
}

type BindResult struct {
	WorkflowRunID        snapshot.WorkflowRunID
	WorkflowDefinitionID int64
	WorkflowName         string
	WorkflowVersion      int
	FunctionID           string
	TargetConfigHash     string
}

type WorkflowBinder interface {
	BindAndCreate(context.Context, AdmissionContext, BindRequest) (BindResult, error)
}

type RunnerConfig struct {
	MaxConcurrency int
}

type Runner struct {
	store          RunnerStore
	binder         WorkflowBinder
	maxConcurrency int
}

func NewRunner(store RunnerStore, binder WorkflowBinder, config RunnerConfig) (*Runner, error) {
	if nilInterface(store) {
		return nil, fmt.Errorf("experiment runner: store is required")
	}
	if nilInterface(binder) {
		return nil, fmt.Errorf("experiment runner: workflow binder is required")
	}
	if config.MaxConcurrency <= 0 {
		return nil, fmt.Errorf("experiment runner: max concurrency must be positive")
	}
	return &Runner{store: store, binder: binder, maxConcurrency: config.MaxConcurrency}, nil
}

// Run performs one bounded scheduling pass. Coordinator supplies polling and
// distributed serialization; claimed-cell leases and binder idempotency make
// a process restart safe at every boundary.
func (runner *Runner) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("experiment runner: context is required")
	}
	cells, err := runner.store.ClaimCandidateCells(ctx, runner.maxConcurrency)
	if err != nil {
		return fmt.Errorf("experiment runner: claim candidate cells: %w", err)
	}
	sort.Slice(cells, func(left, right int) bool { return cells[left].ID < cells[right].ID })
	validCells := make([]CandidateCell, 0, len(cells))
	var failures []error
	for index, cell := range cells {
		if err := cell.Validate(); err != nil {
			recordErr := runner.store.RecordCandidateFailure(ctx, cell.ID, "invalid_admission")
			failures = append(failures, fmt.Errorf("experiment runner: claimed cell %d: %w", index, err))
			if recordErr != nil {
				failures = append(failures, fmt.Errorf("experiment runner: quarantine cell %s: %w", cell.ID.String(), recordErr))
			}
			continue
		}
		validCells = append(validCells, cell)
	}
	admittedCells := make([]CandidateCell, 0, len(validCells))
	for _, cell := range validCells {
		if err := ctx.Err(); err != nil {
			break
		}
		admitted, reserveErr := runner.reserveCellBudget(ctx, cell)
		if reserveErr != nil {
			failures = append(failures, reserveErr)
		}
		if admitted {
			admittedCells = append(admittedCells, cell)
		}
	}

	sem := make(chan struct{}, runner.maxConcurrency)
	var wait sync.WaitGroup
	var failuresMu sync.Mutex
	for _, cell := range admittedCells {
		if err := ctx.Err(); err != nil {
			break
		}
		acquired := false
		select {
		case sem <- struct{}{}:
			acquired = true
		case <-ctx.Done():
		}
		if !acquired {
			break
		}
		wait.Add(1)
		cell := cloneCandidateCell(cell)
		go func() {
			defer wait.Done()
			defer func() { <-sem }()
			if err := runner.runCell(ctx, cell); err != nil {
				failuresMu.Lock()
				failures = append(failures, err)
				failuresMu.Unlock()
			}
		}()
	}
	wait.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.Join(failures...)
}

func (runner *Runner) reserveCellBudget(ctx context.Context, cell CandidateCell) (bool, error) {
	if !cell.Budget.Limited() {
		return true, nil
	}
	controller, ok := runner.store.(BudgetController)
	if !ok {
		return false, fmt.Errorf("experiment runner: budget controller is required for cell %s", cell.ID.String())
	}
	reservation, err := controller.ReserveCandidateBudget(ctx, cell.ID)
	if err != nil {
		if errors.Is(err, ErrBudgetExhausted) {
			if recordErr := runner.store.RecordCandidateFailure(ctx, cell.ID, "budget_denied"); recordErr != nil {
				return false, fmt.Errorf("experiment runner: record cell %s budget denial: %w", cell.ID.String(), recordErr)
			}
			return false, nil
		}
		return false, fmt.Errorf("experiment runner: reserve cell %s budget: %w", cell.ID.String(), err)
	}
	if err := reservation.Validate(cell.ID); err != nil {
		if recordErr := runner.store.RecordCandidateFailure(ctx, cell.ID, "invalid_admission"); recordErr != nil {
			return false, errors.Join(err, fmt.Errorf("experiment runner: quarantine cell %s: %w", cell.ID.String(), recordErr))
		}
		return false, err
	}
	return true, nil
}

func (runner *Runner) runCell(ctx context.Context, cell CandidateCell) error {
	version := cell.Target.Version
	request := BindRequest{
		WorkflowName:             cell.Target.WorkflowName,
		DefinitionID:             cell.Target.DefinitionID,
		Version:                  &version,
		FunctionID:               cell.Target.FunctionID,
		Inputs:                   cloneSnapshotIDs(cell.Inputs),
		ExpectedTargetConfigHash: cell.TargetConfigHash,
		AdmissionGate: AdmissionGate{
			ExperimentID: cell.ExperimentID,
			CellID:       cell.ID,
			Phase:        AdmissionCandidate,
		},
		IdempotencyKey: fmt.Sprintf(
			"experiment:%s:fixture:%d:variant:%d:rep:%d:candidate",
			cell.ExperimentID.String(), cell.FixtureID, cell.VariantID, cell.Repetition,
		),
	}
	admission := AdmissionContext{
		TeamID: cell.TeamID, TeamName: cell.TeamName, CreatedBy: cell.CreatedBy,
		Origin: Origin{
			Kind:      "experiment",
			Reference: fmt.Sprintf("experiment:%s:cell:%s", cell.ExperimentID.String(), cell.ID.String()),
		},
	}
	created, err := runner.store.CreateAndRecordCandidateRun(ctx, cell, func(bindContext context.Context) (BindResult, error) {
		return runner.binder.BindAndCreate(bindContext, admission, request)
	})
	if err != nil {
		return fmt.Errorf("experiment runner: admit candidate run for cell %s: %w", cell.ID.String(), err)
	}
	if !created.Started {
		return nil
	}
	if created.BindError != nil {
		if ctx.Err() != nil || errors.Is(created.BindError, context.Canceled) || errors.Is(created.BindError, context.DeadlineExceeded) {
			return nil
		}
		category, terminal := terminalBinderFailureCategory(created.BindError)
		if !terminal {
			return fmt.Errorf(
				"experiment runner: bind candidate run for cell %s: %w",
				cell.ID.String(), created.BindError,
			)
		}
		if recordErr := runner.store.RecordCandidateFailure(ctx, cell.ID, category); recordErr != nil {
			return fmt.Errorf("experiment runner: record cell %s failure: %w", cell.ID.String(), recordErr)
		}
		return nil
	}
	result := created.Result
	if err := result.WorkflowRunID.Validate(); err != nil {
		if recordErr := runner.store.RecordCandidateFailure(ctx, cell.ID, "platform_failure"); recordErr != nil {
			return fmt.Errorf("experiment runner: record invalid run for cell %s: %w", cell.ID.String(), recordErr)
		}
		return nil
	}
	if !bindResultMatchesTarget(result, cell.Target, cell.TargetConfigHash) {
		if recordErr := runner.store.RecordCandidateFailure(ctx, cell.ID, "invalid_admission"); recordErr != nil {
			return fmt.Errorf("experiment runner: record mismatched target for cell %s: %w", cell.ID.String(), recordErr)
		}
		return nil
	}
	if !created.Recorded {
		return fmt.Errorf("experiment runner: cell %s has a conflicting candidate workflow run", cell.ID.String())
	}
	return nil
}

func bindResultMatchesTarget(result BindResult, target Target, targetConfigHash string) bool {
	return result.WorkflowDefinitionID == target.DefinitionID &&
		result.WorkflowName == target.WorkflowName &&
		result.WorkflowVersion == target.Version &&
		result.FunctionID == target.FunctionID &&
		result.TargetConfigHash == targetConfigHash
}

func terminalBinderFailureCategory(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrBindBudgetDenied):
		return "budget_denied", true
	case errors.Is(err, ErrBindInvalidRequest):
		return "invalid_admission", true
	default:
		return "", false
	}
}

func cloneCandidateCell(cell CandidateCell) CandidateCell {
	cell.Inputs = cloneSnapshotIDs(cell.Inputs)
	return cell
}

func cloneSnapshotIDs(values map[string]snapshot.SnapshotID) map[string]snapshot.SnapshotID {
	result := make(map[string]snapshot.SnapshotID, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
