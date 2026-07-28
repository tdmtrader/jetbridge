package workflowrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mitchellh/copystructure"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

var (
	originKindPattern       = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	targetConfigHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Binder struct {
	definitions DefinitionResolver
	renderer    TargetRenderer
	snapshots   SnapshotAuthorizer
	runs        WorkflowRunStore
	budget      BudgetAdmitter
	templates   ImmutableTemplateSaver
	executions  PipelineRunCreator
	credential  ModelCredentialAdmitter
}

func NewBinder(
	definitions DefinitionResolver,
	renderer TargetRenderer,
	snapshots SnapshotAuthorizer,
	runs WorkflowRunStore,
	budget BudgetAdmitter,
	templates ImmutableTemplateSaver,
	executions PipelineRunCreator,
	credential ModelCredentialAdmitter,
) (*Binder, error) {
	for name, dependency := range map[string]any{
		"definition resolver":       definitions,
		"target renderer":           renderer,
		"snapshot authorizer":       snapshots,
		"workflow-run store":        runs,
		"budget admitter":           budget,
		"template saver":            templates,
		"pipeline-run creator":      executions,
		"model credential admitter": credential,
	} {
		if nilInterface(dependency) {
			return nil, fmt.Errorf("%w: %s is required", ErrInvalidRequest, name)
		}
	}
	return &Binder{
		definitions: definitions, renderer: renderer, snapshots: snapshots, runs: runs,
		budget: budget, templates: templates, executions: executions, credential: credential,
	}, nil
}

func (b *Binder) BindAndCreate(
	ctx context.Context,
	admission AdmissionContext,
	request BindRequest,
) (BindResult, error) {
	admission, request, err := validateAndClone(admission, request)
	if err != nil {
		return BindResult{}, err
	}

	// Experiment children must pass through CreateWithInputs even when their
	// idempotency key already exists. That store call is the short
	// transaction which serializes child allocation with parent cancellation.
	if request.ExperimentAdmission == nil {
		if existing, found, err := b.existing(ctx, admission, request); err != nil {
			return BindResult{}, err
		} else if found {
			return b.handleExisting(ctx, admission, request, existing)
		}
	}

	definition, found, err := b.resolve(ctx, request)
	if err != nil {
		return BindResult{}, err
	}
	if !found {
		return BindResult{}, ErrDefinitionOrTargetNotFound
	}
	if err := validateDefinition(definition, request); err != nil {
		return BindResult{}, err
	}

	var target workflow.FunctionTarget
	if request.FunctionID == "" {
		target, err = b.renderer.FullFunctionTarget(definition)
	} else {
		target, err = b.renderer.ExtractFunctionTarget(definition, request.FunctionID)
	}
	if err != nil {
		var notFound workflow.FunctionNotFoundError
		if errors.As(err, &notFound) {
			return BindResult{}, ErrDefinitionOrTargetNotFound
		}
		return BindResult{}, fmt.Errorf("%w: select workflow target: %v", ErrPlatformFailure, err)
	}
	target, err = cloneTarget(target)
	if err != nil {
		return BindResult{}, fmt.Errorf("%w: clone workflow target: %v", ErrPlatformFailure, err)
	}
	if err := validateTargetIdentity(target, definition, request.FunctionID); err != nil {
		return BindResult{}, err
	}

	rendered, err := b.renderer.RenderFunction(target)
	if err != nil {
		return BindResult{}, fmt.Errorf("%w: render workflow target: %v", ErrPlatformFailure, err)
	}
	rendered, err = cloneRendered(rendered)
	if err != nil {
		return BindResult{}, fmt.Errorf("%w: clone rendered target: %v", ErrPlatformFailure, err)
	}
	canonical, err := validateRendered(target, rendered)
	if err != nil {
		return BindResult{}, err
	}
	if request.ExpectedTargetConfigHash != "" && rendered.TargetConfigHash != request.ExpectedTargetConfigHash {
		return BindResult{}, fmt.Errorf("%w: frozen target config no longer matches the rendered workflow dependencies", ErrInvalidRequest)
	}
	if err := b.authorizeAwaitDefaults(ctx, admission.TeamID, rendered.Config); err != nil {
		return BindResult{}, err
	}

	if err := validateInputCoverage(target.Signature, request.Inputs); err != nil {
		return BindResult{}, err
	}
	refs, err := b.authorizeInputs(ctx, admission.TeamID, target.Signature, request.Inputs)
	if err != nil {
		return BindResult{}, err
	}
	functionID := optionalFunctionID(request.FunctionID)
	createRequest := db.AgentWorkflowRunCreateRequest{
		TeamID: admission.TeamID, TeamName: admission.TeamName,
		WorkflowDefinitionID: definition.ID, WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: definition.SchemaVersion, SignatureVersion: definition.SignatureVersion,
		DefinitionContentHash: definition.ContentHash, FunctionID: functionID,
		IdempotencyKey: request.IdempotencyKey, ParameterizedConfig: cloneRaw(canonical),
		ParameterizedConfigHash: rendered.TargetConfigHash,
		OriginKind:              admission.Origin.Kind, OriginReference: admission.Origin.Reference,
		CreatedBy: admission.CreatedBy, Status: db.AgentWorkflowRunStatusAdmitting,
		RetryOfWorkflowRunID: cloneWorkflowRunID(request.RetryOf), Inputs: cloneRefs(refs),
		ExperimentAdmission: cloneDBExperimentAdmission(request.ExperimentAdmission),
	}
	run, created, err := b.runs.CreateWithInputs(ctx, createRequest)
	if err != nil {
		if errors.Is(err, db.ErrAgentWorkflowRunExperimentAdmissionClosed) {
			return BindResult{}, ErrExperimentAdmissionClosed
		}
		if request.ExperimentAdmission != nil {
			return BindResult{}, fmt.Errorf("%w: allocate durable experiment workflow run", ErrPlatformFailure)
		}
		if winner, found, readErr := b.resolvedWinner(ctx, createRequest, request.Inputs); readErr == nil && found {
			return b.handleExisting(ctx, admission, request, winner)
		} else if readErr != nil && errors.Is(readErr, ErrIdempotencyConflict) {
			return BindResult{}, readErr
		}
		return BindResult{}, fmt.Errorf("%w: allocate durable workflow run: %v", ErrPlatformFailure, err)
	}
	run = cloneRun(run)
	if err := compareAllocatedRun(run, createRequest); err != nil {
		if created && run.ID.Validate() == nil {
			return b.failAllocated(ctx, admission, request, run, true, fmt.Errorf("%w: allocated workflow-run identity mismatch", ErrCorruptPartialAdmission))
		}
		return BindResult{}, err
	}
	if _, err := b.matchBindings(ctx, run.ID, request.Inputs); err != nil {
		if created && !errors.Is(err, ErrIdempotencyConflict) {
			return b.failAllocated(ctx, admission, request, run, true, err)
		}
		return BindResult{}, err
	}
	if !created {
		return b.handleExisting(ctx, admission, request, run)
	}
	if run.Status != db.AgentWorkflowRunStatusAdmitting || !executionEmpty(run) {
		return b.failAllocated(ctx, admission, request, run, true, ErrCorruptPartialAdmission)
	}
	return b.resume(ctx, admission, request, run, created, &durableRenderedTarget{target: target, rendered: rendered})
}

func (b *Binder) resolvedWinner(
	ctx context.Context,
	request db.AgentWorkflowRunCreateRequest,
	inputs map[string]snapshot.SnapshotID,
) (db.AgentWorkflowRun, bool, error) {
	run, found, err := b.runs.FindByIdempotencyKey(ctx, request.TeamID, request.IdempotencyKey)
	if err != nil {
		return db.AgentWorkflowRun{}, false, fmt.Errorf("%w: read concurrent workflow-run winner", ErrPlatformFailure)
	}
	if !found {
		return db.AgentWorkflowRun{}, false, nil
	}
	run = cloneRun(run)
	if err := compareAllocatedRun(run, request); err != nil {
		return db.AgentWorkflowRun{}, false, err
	}
	if _, err := b.matchBindings(ctx, run.ID, inputs); err != nil {
		return db.AgentWorkflowRun{}, false, err
	}
	return run, true, nil
}

func (b *Binder) resolve(ctx context.Context, request BindRequest) (workflow.Definition, bool, error) {
	var definition workflow.Definition
	var found bool
	var err error
	if request.Version == nil {
		definition, found, err = b.definitions.Live(ctx, request.WorkflowName)
	} else {
		definition, found, err = b.definitions.Get(ctx, request.WorkflowName, *request.Version)
	}
	if err != nil {
		return workflow.Definition{}, false, fmt.Errorf("%w: resolve workflow definition: %v", ErrPlatformFailure, err)
	}
	if !found {
		return workflow.Definition{}, false, nil
	}
	definition, err = cloneDefinition(definition)
	if err != nil {
		return workflow.Definition{}, false, fmt.Errorf("%w: clone workflow definition: %v", ErrPlatformFailure, err)
	}
	return definition, true, nil
}

func (b *Binder) existing(
	ctx context.Context,
	admission AdmissionContext,
	request BindRequest,
) (db.AgentWorkflowRun, bool, error) {
	run, found, err := b.runs.FindByIdempotencyKey(ctx, admission.TeamID, request.IdempotencyKey)
	if err != nil {
		return db.AgentWorkflowRun{}, false, fmt.Errorf("%w: read idempotency key: %v", ErrPlatformFailure, err)
	}
	if !found {
		return db.AgentWorkflowRun{}, false, nil
	}
	run = cloneRun(run)
	if err := compareCallerIntent(run, admission, request); err != nil {
		return db.AgentWorkflowRun{}, false, err
	}
	if _, err := b.matchBindings(ctx, run.ID, request.Inputs); err != nil {
		return db.AgentWorkflowRun{}, false, err
	}
	return run, true, nil
}

func (b *Binder) handleExisting(
	ctx context.Context,
	admission AdmissionContext,
	request BindRequest,
	run db.AgentWorkflowRun,
) (BindResult, error) {
	switch run.Status {
	case db.AgentWorkflowRunStatusAdmitting:
		if executionComplete(run) {
			return b.advanceAdmission(ctx, admission, request, run, false)
		}
		if executionEmpty(run) {
			return b.resume(ctx, admission, request, run, false, nil)
		}
		return BindResult{}, ErrCorruptPartialAdmission
	case db.AgentWorkflowRunStatusRunning,
		db.AgentWorkflowRunStatusSucceeded,
		db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusErrored,
		db.AgentWorkflowRunStatusCanceling,
		db.AgentWorkflowRunStatusAborted:
		if run.Status == db.AgentWorkflowRunStatusRunning && !executionComplete(run) {
			return BindResult{}, ErrCorruptPartialAdmission
		}
		return BindResult{Run: cloneRun(run), Created: false}, nil
	default:
		return BindResult{}, ErrCorruptPartialAdmission
	}
}

func (b *Binder) resume(
	ctx context.Context,
	admission AdmissionContext,
	request BindRequest,
	run db.AgentWorkflowRun,
	created bool,
	frozen *durableRenderedTarget,
) (BindResult, error) {
	bindings, err := b.matchBindings(ctx, run.ID, request.Inputs)
	if err != nil {
		return BindResult{}, err
	}
	config, canonical, err := canonicalConfig(run.ParameterizedConfig)
	if err != nil {
		return b.failAllocated(ctx, admission, request, run, created, fmt.Errorf("%w: invalid durable parameterized config", ErrCorruptPartialAdmission))
	}
	functionID := ""
	if run.FunctionID != nil {
		functionID = *run.FunctionID
	}
	var target workflow.FunctionTarget
	var rendered workflow.RenderedFunction
	if frozen != nil {
		target, rendered = frozen.target, frozen.rendered
	} else {
		definition, found, getErr := b.definitions.Get(ctx, run.WorkflowName, run.WorkflowVersion)
		if getErr != nil || !found || definition.ID != run.WorkflowDefinitionID || definition.SchemaVersion != run.SchemaVersion || definition.SignatureVersion != run.SignatureVersion || definition.ContentHash != run.DefinitionContentHash {
			return b.failAllocated(ctx, admission, request, run, created, fmt.Errorf("%w: durable workflow definition mismatch", ErrCorruptPartialAdmission))
		}
		if run.FunctionID == nil {
			target, err = b.renderer.FullFunctionTarget(definition)
		} else {
			target, err = b.renderer.ExtractFunctionTarget(definition, functionID)
		}
		if err != nil || validateTargetIdentity(target, definition, functionID) != nil {
			return b.failAllocated(ctx, admission, request, run, created, fmt.Errorf("%w: durable workflow target mismatch", ErrCorruptPartialAdmission))
		}
		rendered, err = b.renderer.RenderFunction(target)
		if err != nil {
			return b.failAllocated(ctx, admission, request, run, created, fmt.Errorf("%w: rerender durable workflow target", ErrCorruptPartialAdmission))
		}
	}
	expectedCanonical, err := validateRendered(target, rendered)
	if err != nil || !bytes.Equal(canonical, expectedCanonical) || rendered.TargetConfigHash != run.ParameterizedConfigHash {
		return b.failAllocated(ctx, admission, request, run, created, fmt.Errorf("%w: durable parameterized config hash mismatch", ErrCorruptPartialAdmission))
	}
	hash := rendered.TargetConfigHash
	if err := b.budget.Admit(ctx, BudgetAdmission{
		WorkflowRunID:       run.ID,
		Config:              cloneConfig(config),
		ExperimentAdmission: cloneExperimentAdmission(request.ExperimentAdmission),
		Admission:           cloneAdmission(admission),
	}); err != nil {
		if errors.Is(err, ErrBudgetDenied) {
			return b.failAllocated(ctx, admission, request, run, created, fmt.Errorf("%w", ErrBudgetDenied))
		}
		// Reservation persistence is replayable against the same durable
		// admitting identity. Do not poison the idempotency key on a backend
		// fault; no external side effect has occurred yet.
		return BindResult{}, fmt.Errorf("%w: budget admission failed", ErrPlatformFailure)
	}
	templateName := rendered.TemplateName
	params, err := durableParams(run.ID, bindings, config)
	if err != nil {
		return b.failAllocated(ctx, admission, request, run, created, err)
	}

	if err := b.credential.AdmitModelCredential(ctx); err != nil {
		// The model credential is an external/platform dependency and is safe to
		// replay against the same admitting run. Keeping the durable identity
		// admitting avoids poisoning the caller's idempotency key. Never include
		// backend detail here: it may contain credential material.
		return BindResult{}, fmt.Errorf("%w: admit model credential", ErrPlatformFailure)
	}
	templateRef, err := b.templates.SaveOrReuse(ctx, cloneAdmission(admission), ImmutableTemplateSpec{
		Name: templateName, FullHash: hash, CanonicalJSON: append([]byte(nil), canonical...), Config: cloneConfig(config),
		DevValidationProfiles: cloneDevValidationProfiles(rendered.DevValidationProfiles), DevValidationProvenanceHash: rendered.DevValidationProvenanceHash,
	})
	if err != nil {
		if errors.Is(err, ErrImmutableTemplateCollision) {
			return b.failAllocated(ctx, admission, request, run, created, ErrImmutableTemplateCollision)
		}
		// Template persistence faults are retryable and SaveOrReuse is
		// idempotent by the immutable full hash.
		return BindResult{}, fmt.Errorf("%w: save immutable template", ErrPlatformFailure)
	}
	// No pre-commit callback: agent pods read the platform secret directly, so
	// nothing has to happen between allocating the pipeline run and committing
	// it. The seam stays on the store for callers that do need one.
	_, _, err = b.executions.CreateRunForWorkflowRun(
		ctx, run.ID, templateRef, cloneParams(params), run.CreatedBy, nil,
	)
	if err != nil {
		cause := fmt.Errorf("%w: create workflow execution", ErrPlatformFailure)
		winner, found, readErr := b.existing(ctx, admission, request)
		if readErr != nil {
			return BindResult{}, cause
		}
		if found {
			switch {
			case winner.Status == db.AgentWorkflowRunStatusAdmitting && executionComplete(winner):
				return b.advanceAdmission(ctx, admission, request, winner, created)
			case winner.Status == db.AgentWorkflowRunStatusRunning && executionComplete(winner):
				return BindResult{Run: winner, Created: created}, nil
			case winner.Status == db.AgentWorkflowRunStatusAdmitting && executionEmpty(winner):
				// CreateRunForWorkflowRun proved that no execution was committed;
				// retry the same durable admitting identity on the next call.
				return BindResult{}, cause
			case winner.Status == db.AgentWorkflowRunStatusSucceeded ||
				winner.Status == db.AgentWorkflowRunStatusFailed ||
				winner.Status == db.AgentWorkflowRunStatusErrored ||
				winner.Status == db.AgentWorkflowRunStatusCanceling ||
				winner.Status == db.AgentWorkflowRunStatusAborted:
				return BindResult{Run: winner, Created: created}, nil
			default:
				return BindResult{}, ErrCorruptPartialAdmission
			}
		}
		return BindResult{}, cause
	}
	return b.advanceAdmission(ctx, admission, request, run, created)
}

type durableRenderedTarget struct {
	target   workflow.FunctionTarget
	rendered workflow.RenderedFunction
}

func (b *Binder) advanceAdmission(
	ctx context.Context,
	admission AdmissionContext,
	request BindRequest,
	run db.AgentWorkflowRun,
	created bool,
) (BindResult, error) {
	transitioned, err := b.runs.Transition(
		ctx, run.ID, db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusRunning, "",
	)
	if err != nil {
		return BindResult{}, fmt.Errorf("%w: advance workflow admission", ErrPlatformFailure)
	}
	winner, found, err := b.existing(ctx, admission, request)
	if err != nil {
		return BindResult{}, err
	}
	if !found {
		return BindResult{}, ErrCorruptPartialAdmission
	}
	switch winner.Status {
	case db.AgentWorkflowRunStatusRunning:
		if !executionComplete(winner) {
			return BindResult{}, ErrCorruptPartialAdmission
		}
		return BindResult{Run: winner, Created: created}, nil
	case db.AgentWorkflowRunStatusAdmitting:
		if !executionComplete(winner) {
			return BindResult{}, ErrCorruptPartialAdmission
		}
		if transitioned {
			return BindResult{}, ErrCorruptPartialAdmission
		}
		return BindResult{}, fmt.Errorf("%w: workflow admission CAS did not advance", ErrPlatformFailure)
	case db.AgentWorkflowRunStatusSucceeded,
		db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusErrored,
		db.AgentWorkflowRunStatusCanceling,
		db.AgentWorkflowRunStatusAborted:
		return BindResult{Run: winner, Created: created}, nil
	default:
		return BindResult{}, ErrCorruptPartialAdmission
	}
}

func (b *Binder) failAllocated(
	ctx context.Context,
	admission AdmissionContext,
	request BindRequest,
	run db.AgentWorkflowRun,
	created bool,
	cause error,
) (BindResult, error) {
	transitioned, transitionErr := b.runs.Transition(
		ctx, run.ID, db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusErrored,
		durableErrorMessage(cause),
	)
	if transitionErr != nil {
		return BindResult{}, fmt.Errorf("%w: persist admission failure", ErrPlatformFailure)
	}
	if transitioned {
		return BindResult{}, cause
	}
	winner, found, err := b.existing(ctx, admission, request)
	if err != nil {
		return BindResult{}, err
	}
	if found {
		return BindResult{Run: winner, Created: created}, nil
	}
	return BindResult{}, cause
}

func (b *Binder) authorizeInputs(
	ctx context.Context,
	teamID int,
	signature workflow.PublicSignature,
	inputs map[string]snapshot.SnapshotID,
) (map[string]snapshot.SnapshotRef, error) {
	declared := make(map[string]workflow.SignaturePort, len(signature.Inputs))
	for _, port := range signature.Inputs {
		declared[port.Name] = port
	}
	ports := make([]string, 0, len(inputs))
	for port := range inputs {
		ports = append(ports, port)
	}
	sort.Strings(ports)
	refs := make(map[string]snapshot.SnapshotRef, len(inputs))
	for _, port := range ports {
		id := inputs[port]
		value, found, err := b.snapshots.GetAuthorized(ctx, teamID, id)
		if err != nil {
			return nil, fmt.Errorf("%w: authorize workflow input", ErrPlatformFailure)
		}
		if !found || value.ID != id || value.ContentState != snapshot.ContentStateAvailable {
			return nil, ErrSnapshotUnavailable
		}
		value = value.Clone()
		if err := value.Validate(); err != nil {
			return nil, ErrSnapshotUnavailable
		}
		if value.Type != declared[port].Type {
			return nil, ErrSnapshotTypeMismatch
		}
		refs[port] = snapshot.SnapshotRef{ID: value.ID, Type: value.Type, Digest: value.Digest}
	}
	return refs, nil
}

func (b *Binder) authorizeAwaitDefaults(ctx context.Context, teamID int, config atc.Config) error {
	ids := make(map[snapshot.SnapshotID]struct{})
	recursor := atc.StepRecursor{OnAwaitSnapshot: func(step *atc.AwaitSnapshotStep) error {
		if step.OnTimeout != atc.AwaitSnapshotOnTimeoutDefault {
			return nil
		}
		id, err := snapshot.ParseSnapshotID(step.DefaultSnapshotID)
		if err != nil {
			return fmt.Errorf("%w: invalid await_snapshot default", ErrPlatformFailure)
		}
		ids[id] = struct{}{}
		return nil
	}}
	for _, job := range config.Jobs {
		for _, step := range job.PlanSequence {
			if step.Config == nil {
				return fmt.Errorf("%w: invalid rendered await_snapshot plan", ErrPlatformFailure)
			}
			if err := step.Config.Visit(recursor); err != nil {
				return fmt.Errorf("%w: inspect await_snapshot defaults", ErrPlatformFailure)
			}
		}
	}
	ordered := make([]snapshot.SnapshotID, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, id := range ordered {
		value, found, err := b.snapshots.GetAuthorized(ctx, teamID, id)
		if err != nil {
			return fmt.Errorf("%w: authorize await_snapshot default", ErrPlatformFailure)
		}
		if !found || value.ID != id || value.ContentState != snapshot.ContentStateAvailable {
			return ErrSnapshotUnavailable
		}
		value = value.Clone()
		if err := value.Validate(); err != nil {
			return ErrSnapshotUnavailable
		}
		if value.Type != snapshot.TypeRef("human-answer/v1") {
			return ErrSnapshotTypeMismatch
		}
	}
	return nil
}

func (b *Binder) matchBindings(
	ctx context.Context,
	runID snapshot.WorkflowRunID,
	expected map[string]snapshot.SnapshotID,
) ([]db.AgentWorkflowRunSnapshotBinding, error) {
	bindings, err := b.runs.Snapshots(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("%w: load workflow-run inputs: %v", ErrPlatformFailure, err)
	}
	bindings = cloneBindings(bindings)
	inputs := make(map[string]db.AgentWorkflowRunSnapshotBinding)
	for _, binding := range bindings {
		if binding.Direction != db.AgentWorkflowRunSnapshotInput {
			continue
		}
		if _, duplicate := inputs[binding.PortName]; duplicate {
			return nil, ErrCorruptPartialAdmission
		}
		inputs[binding.PortName] = binding
	}
	if len(inputs) != len(expected) {
		return nil, ErrIdempotencyConflict
	}
	ordered := make([]db.AgentWorkflowRunSnapshotBinding, 0, len(inputs))
	ports := make([]string, 0, len(inputs))
	for port := range inputs {
		ports = append(ports, port)
	}
	sort.Strings(ports)
	for _, port := range ports {
		binding := inputs[port]
		if expected[port] != binding.Snapshot.ID {
			return nil, ErrIdempotencyConflict
		}
		ordered = append(ordered, binding)
	}
	return ordered, nil
}

func validateAndClone(admission AdmissionContext, request BindRequest) (AdmissionContext, BindRequest, error) {
	if admission.TeamID <= 0 {
		return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: team ID must be positive", ErrInvalidRequest)
	}
	for _, field := range []struct {
		label string
		value string
	}{
		{"team name", admission.TeamName}, {"creator", admission.CreatedBy},
		{"idempotency key", request.IdempotencyKey},
	} {
		if err := validateBoundedText(field.label, field.value, 256, false, true); err != nil {
			return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
	}
	if err := validateIdentifier(request.WorkflowName, "workflow name"); err != nil {
		return AdmissionContext{}, BindRequest{}, err
	}
	if request.FunctionID != "" {
		if err := validateIdentifier(request.FunctionID, "function ID"); err != nil {
			return AdmissionContext{}, BindRequest{}, err
		}
	}
	if request.Version != nil && *request.Version <= 0 {
		return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: version must be positive", ErrInvalidRequest)
	}
	if request.ExpectedWorkflowDefinitionID < 0 {
		return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: expected workflow definition ID must be positive", ErrInvalidRequest)
	}
	if request.RetryOf != nil {
		if err := request.RetryOf.Validate(); err != nil {
			return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: retry identity must be positive", ErrInvalidRequest)
		}
	}
	if request.ExpectedTargetConfigHash != "" && !targetConfigHashPattern.MatchString(request.ExpectedTargetConfigHash) {
		return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: expected target config hash must be lower-case 64-hex", ErrInvalidRequest)
	}
	if request.ExperimentAdmission != nil {
		gate := request.ExperimentAdmission
		if gate.ExperimentID <= 0 || gate.CellID <= 0 ||
			gate.Phase != "candidate" && gate.Phase != "evaluator" ||
			admission.Origin.Kind != "experiment" {
			return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: invalid experiment admission gate", ErrInvalidRequest)
		}
		expectedOrigin := fmt.Sprintf("experiment:%d:cell:%d", gate.ExperimentID, gate.CellID)
		if gate.Phase == "evaluator" {
			expectedOrigin += ":evaluator"
		}
		if admission.Origin.Reference != expectedOrigin {
			return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: experiment admission gate does not match its origin", ErrInvalidRequest)
		}
	}
	if !originKindPattern.MatchString(admission.Origin.Kind) {
		return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: invalid origin kind", ErrInvalidRequest)
	}
	if err := validateBoundedText("origin reference", admission.Origin.Reference, 1024, true, false); err != nil {
		return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if admission.Origin.Reference == "" && admission.Origin.Kind != "manual" {
		return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: origin reference is required", ErrInvalidRequest)
	}
	inputs := make(map[string]snapshot.SnapshotID, len(request.Inputs))
	for port, id := range request.Inputs {
		if err := validateIdentifier(port, "input port"); err != nil {
			return AdmissionContext{}, BindRequest{}, err
		}
		if err := id.Validate(); err != nil {
			return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: input snapshot ID must be positive", ErrInvalidRequest)
		}
		inputs[port] = id
	}
	request.Inputs = inputs
	request.Version = cloneInt(request.Version)
	request.RetryOf = cloneWorkflowRunID(request.RetryOf)
	request.ExperimentAdmission = cloneExperimentAdmission(request.ExperimentAdmission)
	return cloneAdmission(admission), request, nil
}

func cloneExperimentAdmission(value *ExperimentAdmissionGate) *ExperimentAdmissionGate {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneDBExperimentAdmission(value *ExperimentAdmissionGate) *db.AgentWorkflowRunExperimentAdmission {
	if value == nil {
		return nil
	}
	return &db.AgentWorkflowRunExperimentAdmission{
		ExperimentID: value.ExperimentID,
		CellID:       value.CellID,
		Phase:        value.Phase,
	}
}

func validateDefinition(definition workflow.Definition, request BindRequest) error {
	if request.ExpectedWorkflowDefinitionID != 0 && int64(definition.ID) != request.ExpectedWorkflowDefinitionID {
		return fmt.Errorf("%w: frozen workflow definition no longer matches the resolved workflow", ErrInvalidRequest)
	}
	if definition.ID <= 0 || definition.Version <= 0 || definition.SignatureVersion <= 0 ||
		definition.Name != request.WorkflowName || strings.TrimSpace(definition.ContentHash) == "" {
		return fmt.Errorf("%w: inconsistent durable definition identity", ErrPlatformFailure)
	}
	if request.Version != nil && definition.Version != *request.Version {
		return fmt.Errorf("%w: inconsistent resolved version", ErrPlatformFailure)
	}
	if definition.SchemaVersion != 3 {
		return fmt.Errorf(
			"%w: workflow %s v%d uses schema_version %d",
			ErrPlatformFailure,
			definition.Name,
			definition.Version,
			definition.SchemaVersion,
		)
	}
	metadata, err := definition.Compiled.VersionMetadata()
	if err != nil || definition.Compiled.Name != definition.Name ||
		metadata.SchemaVersion != definition.SchemaVersion || metadata.SignatureVersion != definition.SignatureVersion {
		return fmt.Errorf("%w: inconsistent compiled definition metadata", ErrPlatformFailure)
	}
	return nil
}

func validateTargetIdentity(target workflow.FunctionTarget, definition workflow.Definition, functionID string) error {
	if target.WorkflowDefinitionID != definition.ID || target.WorkflowName != definition.Name ||
		target.WorkflowVersion != definition.Version || target.SignatureVersion != definition.SignatureVersion {
		return fmt.Errorf("%w: rendered target identity mismatch", ErrPlatformFailure)
	}
	if functionID == "" {
		if target.Kind != workflow.TargetWorkflow || target.FunctionID != "" {
			return fmt.Errorf("%w: full-workflow target mismatch", ErrPlatformFailure)
		}
	} else if target.Kind != workflow.TargetFunction || target.FunctionID != functionID {
		return fmt.Errorf("%w: extracted-function target mismatch", ErrPlatformFailure)
	}
	return nil
}

func validateRendered(target workflow.FunctionTarget, rendered workflow.RenderedFunction) ([]byte, error) {
	if err := validateRenderedParams(target.Signature, rendered.Config); err != nil {
		return nil, err
	}
	canonical, err := rendered.Config.CanonicalJSON()
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize rendered target", ErrPlatformFailure)
	}
	hash, err := workflow.RenderedTargetConfigHash(rendered.Config, rendered.DevValidationProfiles, rendered.DevValidationProvenanceHash)
	if err != nil || hash != rendered.TargetConfigHash {
		return nil, fmt.Errorf("%w: rendered target hash mismatch", ErrPlatformFailure)
	}
	wantName, err := workflow.TemplateName(target.Kind, target.WorkflowName, target.WorkflowVersion, target.FunctionID, hash)
	if err != nil || wantName != rendered.TemplateName {
		return nil, fmt.Errorf("%w: rendered template name mismatch", ErrPlatformFailure)
	}
	if !target.Signature.Equal(rendered.TargetSignature) || len(rendered.InputParamNames) != len(target.Signature.Inputs) {
		return nil, fmt.Errorf("%w: rendered target signature mismatch", ErrPlatformFailure)
	}
	if target.DevValidationProvenanceHash != rendered.DevValidationProvenanceHash || !reflect.DeepEqual(target.Function.DevValidationProfiles, rendered.DevValidationProfiles) {
		return nil, fmt.Errorf("%w: rendered validation authority mismatch", ErrPlatformFailure)
	}
	for _, input := range target.Signature.Inputs {
		want, err := workflow.InputParamName(input.Name)
		if err != nil || rendered.InputParamNames[input.Name] != want {
			return nil, fmt.Errorf("%w: rendered input parameter mismatch", ErrPlatformFailure)
		}
	}
	return canonical, nil
}

func validateRenderedParams(signature workflow.PublicSignature, config atc.Config) error {
	if !config.Template || len(config.Params) != len(signature.Inputs)+1 {
		return fmt.Errorf("%w: rendered parameter schema mismatch", ErrPlatformFailure)
	}
	params := make(map[string]atc.ParamSchema, len(config.Params))
	for _, param := range config.Params {
		if _, duplicate := params[param.Name]; duplicate {
			return fmt.Errorf("%w: duplicate rendered parameter", ErrPlatformFailure)
		}
		params[param.Name] = param
	}
	runID, found := params["workflow_run_id"]
	if !found || runID.Type != "string" || runID.Format != atc.ParamFormatPositiveDecimalInt64 ||
		!runID.Required || runID.Default != nil {
		return fmt.Errorf("%w: workflow-run ID parameter mismatch", ErrPlatformFailure)
	}
	for _, input := range signature.Inputs {
		name, err := workflow.InputParamName(input.Name)
		if err != nil {
			return fmt.Errorf("%w: rendered input parameter mismatch", ErrPlatformFailure)
		}
		param, found := params[name]
		if !found || param.Type != "string" {
			return fmt.Errorf("%w: rendered input parameter mismatch", ErrPlatformFailure)
		}
		if input.Optional {
			defaultValue, ok := param.Default.(string)
			if param.Required || param.Format != atc.ParamFormatZeroOrPositiveDecimalInt64 || !ok || defaultValue != "0" {
				return fmt.Errorf("%w: rendered optional input parameter mismatch", ErrPlatformFailure)
			}
		} else if !param.Required || param.Format != atc.ParamFormatPositiveDecimalInt64 || param.Default != nil {
			return fmt.Errorf("%w: rendered required input parameter mismatch", ErrPlatformFailure)
		}
	}
	return nil
}

func validateInputCoverage(signature workflow.PublicSignature, inputs map[string]snapshot.SnapshotID) error {
	declared := make(map[string]workflow.SignaturePort, len(signature.Inputs))
	for _, port := range signature.Inputs {
		if _, duplicate := declared[port.Name]; duplicate {
			return fmt.Errorf("%w: duplicate target input", ErrPlatformFailure)
		}
		declared[port.Name] = port
		if !port.Optional {
			if _, found := inputs[port.Name]; !found {
				return fmt.Errorf("%w: required input %q is missing", ErrInvalidRequest, port.Name)
			}
		}
	}
	for port := range inputs {
		if _, found := declared[port]; !found {
			return fmt.Errorf("%w: undeclared input %q", ErrInvalidRequest, port)
		}
	}
	return nil
}

func durableParams(
	runID snapshot.WorkflowRunID,
	bindings []db.AgentWorkflowRunSnapshotBinding,
	config atc.Config,
) (map[string]any, error) {
	params := map[string]any{"workflow_run_id": runID.String()}
	generated := map[string]struct{}{"workflow_run_id": {}}
	for _, binding := range bindings {
		name, err := workflow.InputParamName(binding.PortName)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid durable input port", ErrCorruptPartialAdmission)
		}
		params[name] = binding.Snapshot.ID.String()
		generated[name] = struct{}{}
	}
	for _, schema := range config.Params {
		if _, found := generated[schema.Name]; found {
			continue
		}
		defaultValue, ok := schema.Default.(string)
		if !ok || defaultValue != "0" {
			return nil, fmt.Errorf("%w: unexpected durable parameter schema", ErrCorruptPartialAdmission)
		}
		params[schema.Name] = "0"
		generated[schema.Name] = struct{}{}
	}
	validated, err := atc.ValidateRunParams(config.Params, params)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid durable parameters", ErrCorruptPartialAdmission)
	}
	for _, value := range validated {
		if _, ok := value.(string); !ok {
			return nil, fmt.Errorf("%w: non-string durable parameter", ErrCorruptPartialAdmission)
		}
	}
	return validated, nil
}

func compareCallerIntent(run db.AgentWorkflowRun, admission AdmissionContext, request BindRequest) error {
	if request.ExpectedWorkflowDefinitionID != 0 && int64(run.WorkflowDefinitionID) != request.ExpectedWorkflowDefinitionID {
		return fmt.Errorf("%w: frozen workflow definition does not match the durable workflow run", ErrInvalidRequest)
	}
	if request.ExpectedTargetConfigHash != "" && run.ParameterizedConfigHash != request.ExpectedTargetConfigHash {
		return fmt.Errorf("%w: frozen target config does not match the durable workflow run", ErrInvalidRequest)
	}
	wantFunction := optionalFunctionID(request.FunctionID)
	if run.TeamID != admission.TeamID || run.IdempotencyKey != request.IdempotencyKey ||
		run.WorkflowName != request.WorkflowName || !equalStringPointer(run.FunctionID, wantFunction) ||
		run.OriginKind != admission.Origin.Kind || run.OriginReference != admission.Origin.Reference ||
		run.CreatedBy != admission.CreatedBy || !equalRunIDPointer(run.RetryOfWorkflowRunID, request.RetryOf) ||
		(request.Version != nil && run.WorkflowVersion != *request.Version) {
		return ErrIdempotencyConflict
	}
	return nil
}

func compareAllocatedRun(run db.AgentWorkflowRun, request db.AgentWorkflowRunCreateRequest) error {
	if run.TeamID != request.TeamID || run.WorkflowDefinitionID != request.WorkflowDefinitionID ||
		run.WorkflowName != request.WorkflowName || run.WorkflowVersion != request.WorkflowVersion ||
		run.SchemaVersion != request.SchemaVersion || run.SignatureVersion != request.SignatureVersion ||
		run.DefinitionContentHash != request.DefinitionContentHash || !equalStringPointer(run.FunctionID, request.FunctionID) ||
		run.IdempotencyKey != request.IdempotencyKey || run.ParameterizedConfigHash != request.ParameterizedConfigHash ||
		!jsonEqual(run.ParameterizedConfig, request.ParameterizedConfig) || run.OriginKind != request.OriginKind ||
		run.OriginReference != request.OriginReference || run.CreatedBy != request.CreatedBy ||
		!equalRunIDPointer(run.RetryOfWorkflowRunID, request.RetryOfWorkflowRunID) {
		return ErrIdempotencyConflict
	}
	return nil
}

func executionEmpty(run db.AgentWorkflowRun) bool {
	return run.PipelineRunID == nil && run.TemplatePipelineID == nil && run.InstancePipelineID == nil &&
		len(run.ConcreteConfig) == 0 && run.ConcreteConfigHash == nil && run.PlannedBuildID == nil
}

func executionComplete(run db.AgentWorkflowRun) bool {
	return run.PipelineRunID != nil && run.TemplatePipelineID != nil && run.InstancePipelineID != nil &&
		len(run.ConcreteConfig) != 0 && run.ConcreteConfigHash != nil && run.PlannedBuildID != nil
}

func canonicalConfig(raw json.RawMessage) (atc.Config, []byte, error) {
	var config atc.Config
	if len(raw) == 0 || !json.Valid(raw) {
		return atc.Config{}, nil, errors.New("invalid JSON")
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return atc.Config{}, nil, err
	}
	canonical, err := config.CanonicalJSON()
	return config, canonical, err
}

func durableErrorMessage(err error) string {
	for _, category := range []error{
		ErrInvalidRequest, ErrDefinitionOrTargetNotFound,
		ErrSnapshotUnavailable, ErrSnapshotTypeMismatch, ErrBudgetDenied,
		ErrIdempotencyConflict, ErrImmutableTemplateCollision,
		ErrCorruptPartialAdmission, ErrPlatformFailure,
	} {
		if errors.Is(err, category) {
			return truncateUTF8(category.Error(), 4096)
		}
	}
	return truncateUTF8(ErrPlatformFailure.Error(), 4096)
}

func truncateUTF8(value string, max int) string {
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func validateBoundedText(label, value string, max int, allowEmpty, outerWhitespace bool) error {
	if !utf8.ValidString(value) || len(value) > max || (!allowEmpty && strings.TrimSpace(value) == "") {
		return fmt.Errorf("%s is invalid", label)
	}
	if outerWhitespace && strings.TrimSpace(value) != value {
		return fmt.Errorf("%s has outer whitespace", label)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains control characters", label)
		}
	}
	return nil
}

func validateIdentifier(value, label string) error {
	warning, err := atc.ValidateIdentifier(value)
	if err != nil || warning != nil && warning.Type == "invalid_identifier" {
		return fmt.Errorf("%w: invalid %s", ErrInvalidRequest, label)
	}
	return nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

func cloneDefinition(value workflow.Definition) (workflow.Definition, error) {
	cloned, err := copystructure.Copy(value)
	if err != nil {
		return workflow.Definition{}, err
	}
	return cloned.(workflow.Definition), nil
}

func cloneTarget(value workflow.FunctionTarget) (workflow.FunctionTarget, error) {
	cloned, err := copystructure.Copy(value)
	if err != nil {
		return workflow.FunctionTarget{}, err
	}
	return cloned.(workflow.FunctionTarget), nil
}

func cloneRendered(value workflow.RenderedFunction) (workflow.RenderedFunction, error) {
	cloned, err := copystructure.Copy(value)
	if err != nil {
		return workflow.RenderedFunction{}, err
	}
	return cloned.(workflow.RenderedFunction), nil
}

func cloneConfig(value atc.Config) atc.Config {
	cloned, err := copystructure.Copy(value)
	if err != nil {
		return value
	}
	return cloned.(atc.Config)
}

func cloneAdmission(value AdmissionContext) AdmissionContext { return value }

func cloneRefs(values map[string]snapshot.SnapshotRef) map[string]snapshot.SnapshotRef {
	cloned := make(map[string]snapshot.SnapshotRef, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneParams(values map[string]any) map[string]any {
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneBindings(values []db.AgentWorkflowRunSnapshotBinding) []db.AgentWorkflowRunSnapshotBinding {
	return append([]db.AgentWorkflowRunSnapshotBinding(nil), values...)
}

func cloneRun(run db.AgentWorkflowRun) db.AgentWorkflowRun {
	run.FunctionID = cloneString(run.FunctionID)
	run.ParameterizedConfig = cloneRaw(run.ParameterizedConfig)
	run.ConcreteConfig = cloneRaw(run.ConcreteConfig)
	run.ConcreteConfigHash = cloneString(run.ConcreteConfigHash)
	run.ActualPlan = cloneRaw(run.ActualPlan)
	run.ActualPlanHash = cloneString(run.ActualPlanHash)
	run.ResolvedDependencies = cloneRaw(run.ResolvedDependencies)
	run.RetryOfWorkflowRunID = cloneWorkflowRunID(run.RetryOfWorkflowRunID)
	run.PipelineRunID = cloneInt(run.PipelineRunID)
	run.TemplatePipelineID = cloneInt(run.TemplatePipelineID)
	run.InstancePipelineID = cloneInt(run.InstancePipelineID)
	run.PlannedBuildID = cloneInt64(run.PlannedBuildID)
	return run
}

func optionalFunctionID(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneWorkflowRunID(value *snapshot.WorkflowRunID) *snapshot.WorkflowRunID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func equalStringPointer(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalRunIDPointer(left, right *snapshot.WorkflowRunID) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func jsonEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return bytes.Equal(left, right)
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

type WorkflowDefinitionStoreResolver struct{ Store workflow.Store }

func (r WorkflowDefinitionStoreResolver) Live(ctx context.Context, name string) (workflow.Definition, bool, error) {
	if err := ctx.Err(); err != nil {
		return workflow.Definition{}, false, err
	}
	value, found, err := r.Store.Live(name)
	if err != nil || !found {
		return workflow.Definition{}, found, err
	}
	cloned, err := cloneDefinition(*value)
	return cloned, err == nil, err
}

func (r WorkflowDefinitionStoreResolver) Get(ctx context.Context, name string, version int) (workflow.Definition, bool, error) {
	if err := ctx.Err(); err != nil {
		return workflow.Definition{}, false, err
	}
	value, found, err := r.Store.Get(name, version)
	if err != nil || !found {
		return workflow.Definition{}, found, err
	}
	cloned, err := cloneDefinition(*value)
	return cloned, err == nil, err
}

type WorkflowTargetRenderer struct {
	// RuntimeImage is trusted web-node configuration, never authored workflow
	// source. Agent-bearing schema-v3 targets require an exact OCI digest.
	RuntimeImage string
}

var _ workflow.PromotionValidator = WorkflowTargetRenderer{}

// ValidatePromotion performs the same full-target selection, trusted runtime
// injection, and rendered-target validation used by BindAndCreate. Stores call
// it against the exact persisted definition while holding their promotion
// serialization lock, before changing the live pointer.
func (renderer WorkflowTargetRenderer) ValidatePromotion(definition workflow.Definition) error {
	target, err := renderer.FullFunctionTarget(definition)
	if err != nil {
		return fmt.Errorf("select workflow target: %w", err)
	}
	rendered, err := renderer.RenderFunction(target)
	if err != nil {
		return fmt.Errorf("render workflow target: %w", err)
	}
	if _, err := validateRendered(target, rendered); err != nil {
		return fmt.Errorf("validate rendered workflow target: %w", err)
	}
	return nil
}

func (WorkflowTargetRenderer) FullFunctionTarget(definition workflow.Definition) (workflow.FunctionTarget, error) {
	return workflow.FullFunctionTarget(definition)
}

func (WorkflowTargetRenderer) ExtractFunctionTarget(definition workflow.Definition, functionID string) (workflow.FunctionTarget, error) {
	return workflow.ExtractFunctionTarget(definition, functionID)
}

func (renderer WorkflowTargetRenderer) RenderFunction(target workflow.FunctionTarget) (workflow.RenderedFunction, error) {
	rendered, err := workflow.RenderFunction(target)
	if err != nil {
		return workflow.RenderedFunction{}, err
	}
	agentCount := 0
	for jobIndex := range rendered.Config.Jobs {
		for stepIndex := range rendered.Config.Jobs[jobIndex].PlanSequence {
			err := rendered.Config.Jobs[jobIndex].PlanSequence[stepIndex].Config.Visit(atc.StepRecursor{
				OnAgent: func(step *atc.AgentStep) error {
					agentCount++
					if step.RuntimeImage != "" {
						return fmt.Errorf("workflow runtime image was already populated")
					}
					if err := atc.ValidatePinnedOCIImage(renderer.RuntimeImage); err != nil {
						return fmt.Errorf("workflow agent runtime image: %w", err)
					}
					step.RuntimeImage = renderer.RuntimeImage
					return nil
				},
			})
			if err != nil {
				return workflow.RenderedFunction{}, err
			}
		}
	}
	if agentCount == 0 {
		return rendered, nil
	}
	hash, err := workflow.RenderedTargetConfigHash(rendered.Config, rendered.DevValidationProfiles, rendered.DevValidationProvenanceHash)
	if err != nil {
		return workflow.RenderedFunction{}, err
	}
	name, err := workflow.TemplateName(target.Kind, target.WorkflowName, target.WorkflowVersion, target.FunctionID, hash)
	if err != nil {
		return workflow.RenderedFunction{}, err
	}
	rendered.TargetConfigHash = hash
	rendered.TemplateName = name
	return rendered, nil
}
