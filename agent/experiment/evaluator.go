package experiment

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
)

type ObservedRunStatus string

const (
	ObservedRunPending   ObservedRunStatus = "pending"
	ObservedRunRunning   ObservedRunStatus = "running"
	ObservedRunSucceeded ObservedRunStatus = "succeeded"
	ObservedRunFailed    ObservedRunStatus = "failed"
)

type ObservedFailure string

const (
	ObservedContractFailure ObservedFailure = "contract_failure"
	ObservedPlatformFailure ObservedFailure = "platform_failure"
)

type RunObservation struct {
	Status  ObservedRunStatus
	Failure ObservedFailure
	Outputs map[string]snapshot.SnapshotRef
}

type EvaluationCell struct {
	ID                        CellID
	ExperimentID              ID
	ResourceSourceAdmissionID *int64
	TeamID                    int
	TeamName                  string
	CreatedBy                 string
	CandidateWorkflowRunID    snapshot.WorkflowRunID
	EvaluatorWorkflowRunID    *snapshot.WorkflowRunID
	CandidateSignature        workflow.PublicSignature
	FixtureInputs             map[string]snapshot.SnapshotID
	Evaluator                 Evaluator
	Role                      FixtureRole
	Assertions                []Assertion
	Budget                    Budget
}

func (cell EvaluationCell) Validate() error {
	if err := cell.ID.Validate(); err != nil {
		return err
	}
	if err := cell.ExperimentID.Validate(); err != nil {
		return err
	}
	if cell.ResourceSourceAdmissionID != nil && *cell.ResourceSourceAdmissionID <= 0 {
		return fmt.Errorf("experiment evaluator: resource source admission ID must be positive when present")
	}
	if cell.TeamID <= 0 || strings.TrimSpace(cell.TeamName) == "" || strings.TrimSpace(cell.CreatedBy) == "" {
		return fmt.Errorf("experiment evaluator: durable team and creator identity are required")
	}
	if err := cell.CandidateWorkflowRunID.Validate(); err != nil {
		return err
	}
	if cell.EvaluatorWorkflowRunID != nil {
		if err := cell.EvaluatorWorkflowRunID.Validate(); err != nil {
			return err
		}
	}
	if err := validateSignature(cell.CandidateSignature); err != nil {
		return fmt.Errorf("experiment evaluator: candidate signature: %w", err)
	}
	if err := validateEvaluator(cell.Evaluator, cell.CandidateSignature); err != nil {
		return err
	}
	if err := cell.Budget.Validate(); err != nil {
		return err
	}
	if !hashPattern.MatchString(cell.Evaluator.TargetConfigHash) {
		return fmt.Errorf("experiment evaluator: frozen target config hash is required")
	}
	if cell.Evaluator.DevValidationProvenanceHash != "" &&
		!hashPattern.MatchString(cell.Evaluator.DevValidationProvenanceHash) {
		return fmt.Errorf("experiment evaluator: frozen dev validation provenance hash is invalid")
	}
	switch cell.Role {
	case FixtureNormal:
		if len(cell.Assertions) != 0 {
			return fmt.Errorf("experiment evaluator: normal fixture must not have assertions")
		}
	case FixtureNegativeControl:
		if len(cell.Assertions) == 0 {
			return fmt.Errorf("experiment evaluator: negative control requires assertions")
		}
	default:
		return fmt.Errorf("experiment evaluator: fixture role is invalid")
	}
	seen := make(map[string]struct{}, len(cell.Assertions))
	for _, assertion := range cell.Assertions {
		if _, found := seen[assertion.Metric]; found {
			return fmt.Errorf("experiment evaluator: duplicate assertion metric %q", assertion.Metric)
		}
		seen[assertion.Metric] = struct{}{}
		if err := assertion.Validate(); err != nil {
			return err
		}
	}
	inputPorts := portMap(cell.CandidateSignature.Inputs)
	for port, id := range cell.FixtureInputs {
		if _, found := inputPorts[port]; !found {
			return fmt.Errorf("experiment evaluator: unknown fixture input %q", port)
		}
		if err := id.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type EvaluationStore interface {
	ClaimEvaluationCells(context.Context, int) ([]EvaluationCell, error)
	CreateAndRecordEvaluatorRun(context.Context, EvaluationCell, EvaluatorRunCreator) (EvaluatorRunAdmission, error)
	RecordEvaluatorRun(context.Context, CellID, snapshot.WorkflowRunID) (bool, error)
	CompleteEvaluation(context.Context, CellID, CellStatus, *snapshot.SnapshotID) (bool, error)
}

type EvaluatorRunCreator func(context.Context) (BindResult, error)

// EvaluatorRunAdmission has the same short allocation-gate semantics as
// CandidateRunAdmission. Started=false guarantees that no evaluator child was
// created or reused after the parent stopped running.
type EvaluatorRunAdmission struct {
	Started   bool
	Result    BindResult
	BindError error
	Recorded  bool
}

// EvaluationMeasurementsStore is an optional durability extension used by
// stores that build scorecards without reopening snapshot content. Evaluator
// execution remains compatible with the narrow EvaluationStore contract.
type EvaluationMeasurementsStore interface {
	RecordMeasurements(context.Context, CellID, contracts.MeasurementsDocument) error
}

type RunObserver interface {
	Inspect(context.Context, int, snapshot.WorkflowRunID) (RunObservation, bool, error)
}

type MeasurementsReader interface {
	ReadMeasurements(context.Context, int, snapshot.SnapshotID) (contracts.MeasurementsDocument, bool, error)
}

type EvaluatorRunner struct {
	store        EvaluationStore
	runs         RunObserver
	measurements MeasurementsReader
	binder       WorkflowBinder
	limit        int
}

func NewEvaluator(
	store EvaluationStore,
	runs RunObserver,
	measurements MeasurementsReader,
	binder WorkflowBinder,
	limit int,
) (*EvaluatorRunner, error) {
	for name, dependency := range map[string]any{
		"store": store, "run observer": runs, "measurements reader": measurements, "workflow binder": binder,
	} {
		if nilInterface(dependency) {
			return nil, fmt.Errorf("experiment evaluator: %s is required", name)
		}
	}
	if limit <= 0 {
		return nil, fmt.Errorf("experiment evaluator: limit must be positive")
	}
	return &EvaluatorRunner{store: store, runs: runs, measurements: measurements, binder: binder, limit: limit}, nil
}

func (evaluator *EvaluatorRunner) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("experiment evaluator: context is required")
	}
	cells, err := evaluator.store.ClaimEvaluationCells(ctx, evaluator.limit)
	if err != nil {
		return fmt.Errorf("experiment evaluator: claim cells: %w", err)
	}
	var failures []error
	for index, cell := range cells {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := cell.Validate(); err != nil {
			failures = append(failures, fmt.Errorf("experiment evaluator: claimed cell %d: %w", index, err))
			if cell.ID.Validate() == nil {
				if terminalErr := evaluator.complete(ctx, cell.ID, CellEvaluatorFailure, nil); terminalErr != nil {
					failures = append(failures, terminalErr)
				}
			}
			continue
		}
		if err := evaluator.runCell(ctx, cell); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (evaluator *EvaluatorRunner) runCell(ctx context.Context, cell EvaluationCell) error {
	if cell.EvaluatorWorkflowRunID == nil {
		return evaluator.bind(ctx, cell)
	}
	return evaluator.collect(ctx, cell)
}

func (evaluator *EvaluatorRunner) bind(ctx context.Context, cell EvaluationCell) error {
	observation, found, err := evaluator.runs.Inspect(ctx, cell.TeamID, cell.CandidateWorkflowRunID)
	if err != nil {
		return fmt.Errorf("experiment evaluator: inspect candidate run for cell %s: %w", cell.ID.String(), err)
	}
	if !found {
		return evaluator.complete(ctx, cell.ID, CellCandidatePlatformFailure, nil)
	}
	switch observation.Status {
	case ObservedRunPending, ObservedRunRunning:
		return nil
	case ObservedRunFailed:
		status := CellCandidatePlatformFailure
		if observation.Failure == ObservedContractFailure {
			status = CellCandidateContractFailure
		}
		return evaluator.complete(ctx, cell.ID, status, nil)
	case ObservedRunSucceeded:
	default:
		return evaluator.complete(ctx, cell.ID, CellCandidatePlatformFailure, nil)
	}

	inputs, err := mapEvaluatorInputs(cell, observation.Outputs)
	if err != nil {
		return evaluator.complete(ctx, cell.ID, CellCandidateContractFailure, nil)
	}
	if cell.Budget.Limited() {
		controller, ok := evaluator.store.(BudgetController)
		if !ok {
			return fmt.Errorf("experiment evaluator: budget controller is required for cell %s", cell.ID.String())
		}
		reservation, err := controller.AdmitEvaluatorBudget(ctx, cell.ID)
		if errors.Is(err, ErrBudgetExhausted) {
			return evaluator.complete(ctx, cell.ID, CellSkippedBudget, nil)
		}
		if err != nil {
			return fmt.Errorf("experiment evaluator: admit cell %s budget: %w", cell.ID.String(), err)
		}
		if err := reservation.Validate(cell.ID); err != nil {
			return fmt.Errorf("experiment evaluator: %w", err)
		}
	}
	version := cell.Evaluator.Target.Version
	request := BindRequest{
		DefinitionKind:                      cell.Evaluator.Target.DefinitionKind(),
		WorkflowName:                        cell.Evaluator.Target.WorkflowName,
		DefinitionID:                        cell.Evaluator.Target.DefinitionID,
		Version:                             &version,
		FunctionID:                          cell.Evaluator.Target.FunctionID,
		NodeParameters:                      cloneNodeParameters(cell.Evaluator.Target.NodeParameters),
		Inputs:                              inputs,
		ExpectedTargetConfigHash:            cell.Evaluator.TargetConfigHash,
		ExpectedDevValidationProvenanceHash: cell.Evaluator.DevValidationProvenanceHash,
		ResourceSourceAdmissionID:           cloneOptionalInt64(cell.ResourceSourceAdmissionID),
		AdmissionGate: AdmissionGate{
			ExperimentID: cell.ExperimentID,
			CellID:       cell.ID,
			Phase:        AdmissionEvaluator,
		},
		IdempotencyKey: fmt.Sprintf(
			"experiment:%s:cell:%s:evaluator", cell.ExperimentID.String(), cell.ID.String(),
		),
	}
	admission := AdmissionContext{
		TeamID: cell.TeamID, TeamName: cell.TeamName, CreatedBy: cell.CreatedBy,
		Origin: Origin{
			Kind:      "experiment",
			Reference: fmt.Sprintf("experiment:%s:cell:%s:evaluator", cell.ExperimentID.String(), cell.ID.String()),
		},
	}
	created, err := evaluator.store.CreateAndRecordEvaluatorRun(ctx, cell, func(bindContext context.Context) (BindResult, error) {
		return evaluator.binder.BindAndCreate(bindContext, admission, request)
	})
	if err != nil {
		return fmt.Errorf("experiment evaluator: admit evaluator run for cell %s: %w", cell.ID.String(), err)
	}
	if !created.Started {
		return nil
	}
	if created.BindError != nil {
		if ctx.Err() != nil || errors.Is(created.BindError, context.Canceled) || errors.Is(created.BindError, context.DeadlineExceeded) {
			return nil
		}
		if errors.Is(created.BindError, ErrBindBudgetDenied) {
			return evaluator.complete(ctx, cell.ID, CellSkippedBudget, nil)
		}
		if errors.Is(created.BindError, ErrBindInvalidRequest) {
			return evaluator.complete(ctx, cell.ID, CellEvaluatorFailure, nil)
		}
		return fmt.Errorf(
			"experiment evaluator: bind evaluator run for cell %s: %w",
			cell.ID.String(), created.BindError,
		)
	}
	result := created.Result
	if err := result.WorkflowRunID.Validate(); err != nil {
		return evaluator.complete(ctx, cell.ID, CellEvaluatorFailure, nil)
	}
	if !bindResultMatchesTarget(result, cell.Evaluator.Target, cell.Evaluator.TargetConfigHash, cell.Evaluator.DevValidationProvenanceHash) {
		return evaluator.complete(ctx, cell.ID, CellEvaluatorFailure, nil)
	}
	if !created.Recorded {
		return fmt.Errorf("experiment evaluator: cell %s has a conflicting evaluator workflow run", cell.ID.String())
	}
	return nil
}

func (evaluator *EvaluatorRunner) collect(ctx context.Context, cell EvaluationCell) error {
	runID := *cell.EvaluatorWorkflowRunID
	observation, found, err := evaluator.runs.Inspect(ctx, cell.TeamID, runID)
	if err != nil {
		return fmt.Errorf("experiment evaluator: inspect evaluator run for cell %s: %w", cell.ID.String(), err)
	}
	if !found {
		return evaluator.complete(ctx, cell.ID, CellEvaluatorFailure, nil)
	}
	switch observation.Status {
	case ObservedRunPending, ObservedRunRunning:
		return nil
	case ObservedRunFailed:
		return evaluator.complete(ctx, cell.ID, CellEvaluatorFailure, nil)
	case ObservedRunSucceeded:
	default:
		return evaluator.complete(ctx, cell.ID, CellEvaluatorFailure, nil)
	}
	ref, found := observation.Outputs[cell.Evaluator.MeasurementsPort]
	if !found || ref.Type != snapshot.TypeRef("measurements/v1") || ref.Validate() != nil {
		return evaluator.complete(ctx, cell.ID, CellEvaluatorFailure, nil)
	}
	document, found, err := evaluator.measurements.ReadMeasurements(ctx, cell.TeamID, ref.ID)
	if err != nil {
		return fmt.Errorf("experiment evaluator: read measurements for cell %s: %w", cell.ID.String(), err)
	}
	if !found || len(document.Metrics) > MaxMeasurementsPerCell ||
		(document.Conclusion != "measured" && document.Conclusion != "partial") ||
		len(document.Metrics) == 0 {
		return evaluator.complete(ctx, cell.ID, CellEvaluatorFailure, nil)
	}
	if cell.Budget.Limited() {
		controller, ok := evaluator.store.(BudgetController)
		if !ok {
			return fmt.Errorf("experiment evaluator: budget controller is required for cell %s", cell.ID.String())
		}
		_, err := controller.CheckCellBudget(ctx, cell.ID)
		if errors.Is(err, ErrBudgetExhausted) {
			return evaluator.complete(ctx, cell.ID, CellSkippedBudget, nil)
		}
		if err != nil {
			return fmt.Errorf("experiment evaluator: check cell %s budget: %w", cell.ID.String(), err)
		}
	}
	status := CellValidMeasurement
	if cell.Role == FixtureNegativeControl && !assertionsPass(cell.Assertions, document) {
		status = CellNegativeControlFailure
	}
	if store, ok := evaluator.store.(EvaluationMeasurementsStore); ok {
		if err := store.RecordMeasurements(ctx, cell.ID, document); err != nil {
			return fmt.Errorf("experiment evaluator: record measurements for cell %s: %w", cell.ID.String(), err)
		}
	}
	id := ref.ID
	return evaluator.complete(ctx, cell.ID, status, &id)
}

func (evaluator *EvaluatorRunner) complete(ctx context.Context, cell CellID, status CellStatus, measurement *snapshot.SnapshotID) error {
	completed, err := evaluator.store.CompleteEvaluation(ctx, cell, status, measurement)
	if err != nil {
		return fmt.Errorf("experiment evaluator: complete cell %s: %w", cell.String(), err)
	}
	if !completed {
		return fmt.Errorf("experiment evaluator: cell %s has a conflicting terminal result", cell.String())
	}
	return nil
}

func mapEvaluatorInputs(cell EvaluationCell, candidateOutputs map[string]snapshot.SnapshotRef) (map[string]snapshot.SnapshotID, error) {
	targetPorts := portMap(cell.Evaluator.Signature.Inputs)
	candidatePorts := portMap(cell.CandidateSignature.Outputs)
	inputs := make(map[string]snapshot.SnapshotID, len(cell.Evaluator.Mappings))
	for _, mapping := range cell.Evaluator.Mappings {
		target := targetPorts[mapping.EvaluatorPort]
		switch mapping.SourceDirection {
		case SourceFixtureInput:
			id, found := cell.FixtureInputs[mapping.SourcePort]
			if !found || id.Validate() != nil {
				return nil, fmt.Errorf("fixture input %q is unavailable", mapping.SourcePort)
			}
			inputs[mapping.EvaluatorPort] = id
		case SourceCandidateOutput:
			ref, found := candidateOutputs[mapping.SourcePort]
			declared := candidatePorts[mapping.SourcePort]
			if !found || ref.Validate() != nil || ref.Type != declared.Type || ref.Type != target.Type {
				return nil, fmt.Errorf("candidate output %q is unavailable or has the wrong type", mapping.SourcePort)
			}
			inputs[mapping.EvaluatorPort] = ref.ID
		}
	}
	return inputs, nil
}

func assertionsPass(assertions []Assertion, document contracts.MeasurementsDocument) bool {
	metrics := make(map[string]float64, len(document.Metrics))
	for _, metric := range document.Metrics {
		metrics[metric.ID] = metric.Value
	}
	for _, assertion := range assertions {
		value, found := metrics[assertion.Metric]
		if !found || !assertion.Evaluate(value) {
			return false
		}
	}
	return true
}
