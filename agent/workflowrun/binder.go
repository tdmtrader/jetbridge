package workflowrun

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	nodes       workflow.NodeStore
	renderer    TargetRenderer
	snapshots   SnapshotAuthorizer
	runs        WorkflowRunStore
	budget      BudgetAdmitter
	templates   ImmutableTemplateSaver
	executions  PipelineRunCreator
	credential  ModelCredentialAdmitter
	sources     ResourceSourceAdmitter
}

// BinderOption configures a server-owned source-admission hand-off. A binder
// without this dependency continues to serve source-free workflows.
type BinderOption func(*Binder) error

// WithResourceSourceAdmitter enables trusted source admissions. It is wired
// only by server composition; public BindRequest payloads carry no admission
// identity or caller-supplied source binding.
func WithResourceSourceAdmitter(admitter ResourceSourceAdmitter) BinderOption {
	return func(binder *Binder) error {
		if nilInterface(admitter) {
			return fmt.Errorf("%w: resource source admitter is required", ErrInvalidRequest)
		}
		binder.sources = admitter
		return nil
	}
}

// WithNodeStore enables trusted direct execution of exact reusable node
// versions. It is server composition, not request-provided authority.
func WithNodeStore(nodes workflow.NodeStore) BinderOption {
	return func(binder *Binder) error {
		if nilInterface(nodes) {
			return fmt.Errorf("%w: node store is required", ErrInvalidRequest)
		}
		binder.nodes = nodes
		return nil
	}
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
	options ...BinderOption,
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
	binder := &Binder{
		definitions: definitions, renderer: renderer, snapshots: snapshots, runs: runs,
		budget: budget, templates: templates, executions: executions, credential: credential,
	}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("%w: binder option is required", ErrInvalidRequest)
		}
		if err := option(binder); err != nil {
			return nil, err
		}
	}
	return binder, nil
}

func (b *Binder) BindAndCreate(
	ctx context.Context,
	admission AdmissionContext,
	request BindRequest,
) (BindResult, error) {
	return b.bindAndCreate(ctx, admission, request, nil)
}

// BindExperimentWithReadySourceAdmission is the only experiment-child entry
// point. The source admission ID comes from the server-owned, Start-time
// experiment association; it is deliberately absent from public BindRequest.
// A nil ID is valid only for a source-free target and makes a source-bearing
// child fail closed rather than opening a new manual source admission.
func (b *Binder) BindExperimentWithReadySourceAdmission(
	ctx context.Context,
	admission AdmissionContext,
	request BindRequest,
	resourceSourceAdmissionID *int64,
) (BindResult, error) {
	if request.ExperimentAdmission == nil ||
		(resourceSourceAdmissionID != nil && *resourceSourceAdmissionID <= 0) {
		return BindResult{}, fmt.Errorf("%w: experiment ready source admission is invalid", ErrInvalidRequest)
	}
	return b.bindAndCreate(ctx, admission, request, &trustedSourceAdmission{
		experiment:                true,
		resourceSourceAdmissionID: cloneInt64(resourceSourceAdmissionID),
	})
}

// BindReadySourceAdmission is the server-only automatic source-build handoff.
// The ready object is produced by the capture/reconciler runtime rather than a
// public workflow-run request.
func (b *Binder) BindReadySourceAdmission(
	ctx context.Context,
	admission AdmissionContext,
	ready ReadySourceAdmission,
	idempotencyKey string,
) (BindResult, error) {
	if ready.AdmissionID <= 0 || ready.TeamID != admission.TeamID ||
		ready.WorkflowDefinitionID <= 0 || ready.WorkflowVersion <= 0 ||
		strings.TrimSpace(ready.WorkflowName) == "" || strings.TrimSpace(idempotencyKey) == "" ||
		admission.Origin.Kind == "manual" || strings.TrimSpace(admission.Origin.Reference) == "" {
		return BindResult{}, fmt.Errorf("%w: automatic ready source admission is invalid", ErrInvalidRequest)
	}
	version := ready.WorkflowVersion
	clonedReady := cloneReadySourceAdmission(ready)
	return b.bindAndCreate(ctx, admission, BindRequest{
		WorkflowName:                 ready.WorkflowName,
		Version:                      &version,
		IdempotencyKey:               idempotencyKey,
		ExpectedWorkflowDefinitionID: int64(ready.WorkflowDefinitionID),
	}, &trustedSourceAdmission{ready: &clonedReady})
}

type trustedSourceAdmission struct {
	ready                     *ReadySourceAdmission
	experiment                bool
	resourceSourceAdmissionID *int64
}

func (trusted *trustedSourceAdmission) admissionID() *int64 {
	if trusted == nil {
		return nil
	}
	if trusted.ready != nil {
		return &trusted.ready.AdmissionID
	}
	return trusted.resourceSourceAdmissionID
}

func (b *Binder) bindAndCreate(
	ctx context.Context,
	admission AdmissionContext,
	request BindRequest,
	trusted *trustedSourceAdmission,
) (BindResult, error) {
	admission, request, err := validateAndClone(admission, request)
	if err != nil {
		return BindResult{}, err
	}
	// Resolved before the idempotent-replay check, so a re-entry compares
	// against the association the run was actually admitted under rather than
	// against a caller that legitimately left it to be inherited.
	admission, err = b.resolveTicketAssociation(ctx, admission, request)
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
			if trustedID := trusted.admissionID(); trustedID != nil &&
				!equalInt64Pointer(existing.ResourceSourceAdmissionID, trustedID) {
				return BindResult{}, ErrIdempotencyConflict
			}
			return b.handleExisting(ctx, admission, request, existing)
		}
	}
	if request.RetryOf != nil {
		if result, handled, err := b.bindSourceReplay(ctx, admission, request); handled {
			return result, err
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
	if len(target.Function.ResourceSources) != 0 && request.RetryOf != nil {
		if result, handled, err := b.bindSourceReplay(ctx, admission, request); handled {
			return result, err
		}
	}

	var ready *ReadySourceAdmission
	if len(target.Function.ResourceSources) != 0 {
		if nilInterface(b.sources) {
			return BindResult{}, fmt.Errorf("%w: resource source admission is unavailable", ErrPlatformFailure)
		}
		sourceTarget, sourceErr := workflow.ResourceSourcePipelineTargetFor(definition, admission.TeamID)
		if sourceErr != nil {
			return BindResult{}, fmt.Errorf("%w: derive resource source target: %v", ErrPlatformFailure, sourceErr)
		}
		var value ReadySourceAdmission
		if trusted != nil && trusted.ready != nil {
			if trusted.ready.AdmissionID <= 0 {
				return BindResult{}, fmt.Errorf("%w: automatic ready source admission is invalid", ErrInvalidRequest)
			}
			verified, loadErr := b.sources.LoadReady(ctx, admission.TeamID, trusted.ready.AdmissionID, sourceTarget)
			if loadErr != nil {
				return BindResult{}, sourceAdmissionError(loadErr)
			}
			if !sameReadySourceAdmission(verified, *trusted.ready) {
				return BindResult{}, fmt.Errorf("%w: automatic ready source admission drifted", ErrInvalidRequest)
			}
			value = verified
		} else if trusted != nil && trusted.experiment {
			if trusted.resourceSourceAdmissionID == nil {
				return BindResult{}, fmt.Errorf("%w: source-bearing experiment target has no prepared ready admission", ErrInvalidRequest)
			}
			verified, loadErr := b.sources.LoadReady(
				ctx, admission.TeamID, *trusted.resourceSourceAdmissionID, sourceTarget,
			)
			if loadErr != nil {
				return BindResult{}, sourceAdmissionError(loadErr)
			}
			value = verified
		} else {
			value, sourceErr = b.sources.AdmitManual(
				ctx, admission, sourceTarget, "workflow-run-source:"+request.IdempotencyKey,
			)
			if sourceErr != nil {
				return BindResult{}, sourceAdmissionError(sourceErr)
			}
		}
		if sourceErr = validateReadySourceAdmission(value, admission.TeamID, sourceTarget); sourceErr != nil {
			return BindResult{}, sourceErr
		}
		readyValue := cloneReadySourceAdmission(value)
		ready = &readyValue
	} else if trusted != nil &&
		(trusted.ready != nil || trusted.resourceSourceAdmissionID != nil) {
		return BindResult{}, fmt.Errorf("%w: source-free workflow cannot use a ready source admission", ErrInvalidRequest)
	}

	var rendered workflow.RenderedFunction
	if ready == nil {
		rendered, err = b.renderer.RenderFunction(target)
	} else {
		rendered, err = b.renderer.RenderFunctionWithBoundSources(target, ready.Inputs)
	}
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
	if request.ExpectedDevValidationProvenanceHash != nil &&
		rendered.DevValidationProvenanceHash != *request.ExpectedDevValidationProvenanceHash {
		return BindResult{}, fmt.Errorf("%w: frozen dev validation authority no longer matches the rendered workflow dependencies", ErrInvalidRequest)
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
	if ready != nil {
		refs, err = mergeSourceInputRefs(refs, ready.Inputs)
		if err != nil {
			return BindResult{}, err
		}
	}
	effectiveInputs := snapshotIDs(refs)
	functionID := optionalFunctionID(request.FunctionID)
	createRequest := db.AgentWorkflowRunCreateRequest{
		DefinitionKind: request.DefinitionKind,
		TeamID:         admission.TeamID, TeamName: admission.TeamName,
		WorkflowDefinitionID: definition.ID, WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: definition.SchemaVersion, SignatureVersion: definition.SignatureVersion,
		DefinitionContentHash: definition.ContentHash, FunctionID: functionID,
		IdempotencyKey: request.IdempotencyKey, ParameterizedConfig: cloneRaw(canonical),
		ParameterizedConfigHash:     rendered.TargetConfigHash,
		DevValidationProvenanceHash: rendered.DevValidationProvenanceHash,
		OriginKind:                  admission.Origin.Kind, OriginReference: admission.Origin.Reference,
		CreatedBy: admission.CreatedBy, Status: db.AgentWorkflowRunStatusAdmitting,
		RetryOfWorkflowRunID: cloneWorkflowRunID(request.RetryOf), Inputs: cloneRefs(refs),
		ExperimentAdmission: cloneDBExperimentAdmission(request.ExperimentAdmission),
	}
	applyTicketAssociation(&createRequest, admission.Ticket)
	if ready != nil {
		createRequest.ResourceSourceAdmissionID = cloneInt64(&ready.AdmissionID)
	}
	run, created, err := b.runs.CreateWithInputs(ctx, createRequest)
	if err != nil {
		if errors.Is(err, db.ErrAgentWorkflowRunExperimentAdmissionClosed) {
			return BindResult{}, ErrExperimentAdmissionClosed
		}
		if request.ExperimentAdmission != nil {
			return BindResult{}, fmt.Errorf("%w: allocate durable experiment workflow run", ErrPlatformFailure)
		}
		if winner, found, readErr := b.resolvedWinner(ctx, createRequest, effectiveInputs); readErr == nil && found {
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
	if _, err := b.matchBindings(ctx, run.ID, effectiveInputs); err != nil {
		if created && !errors.Is(err, ErrIdempotencyConflict) {
			return b.failAllocated(ctx, admission, request, run, true, err)
		}
		return BindResult{}, err
	}
	if !created {
		request.Inputs = effectiveInputs
		return b.handleExisting(ctx, admission, request, run)
	}
	if run.Status != db.AgentWorkflowRunStatusAdmitting || !executionEmpty(run) {
		return b.failAllocated(ctx, admission, request, run, true, ErrCorruptPartialAdmission)
	}
	request.Inputs = effectiveInputs
	return b.resume(ctx, admission, request, run, created, &durableRenderedTarget{target: target, rendered: rendered})
}

func (b *Binder) resolvedWinner(
	ctx context.Context,
	request db.AgentWorkflowRunCreateRequest,
	inputs map[string]snapshot.SnapshotID,
) (db.AgentWorkflowRun, bool, error) {
	run, found, err := b.findByIdempotencyKey(ctx, request.TeamID, request.DefinitionKind, request.IdempotencyKey)
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

// bindSourceReplay copies an exact prior source-aware run without reopening a
// source pipeline. The sealed ready admission and every immutable input remain
// part of the retry identity; resume re-renders against those same bindings.
func (b *Binder) bindSourceReplay(
	ctx context.Context,
	admission AdmissionContext,
	request BindRequest,
) (BindResult, bool, error) {
	source, found, err := b.get(ctx, admission.TeamID, request.DefinitionKind, *request.RetryOf)
	if err != nil {
		return BindResult{}, true, fmt.Errorf("%w: load replay source workflow run", ErrPlatformFailure)
	}
	if !found {
		// Source-free retries retain the ordinary bind path. The durable DB
		// retry validation remains authoritative if a source row disappears
		// between this read and allocation.
		return BindResult{}, false, nil
	}
	source = cloneRun(source)
	if source.ResourceSourceAdmissionID == nil {
		return BindResult{}, false, nil
	}
	if source.TeamID != admission.TeamID || source.ID != *request.RetryOf ||
		source.WorkflowName != request.WorkflowName ||
		(request.Version != nil && source.WorkflowVersion != *request.Version) ||
		!equalStringPointer(source.FunctionID, optionalFunctionID(request.FunctionID)) {
		return BindResult{}, true, ErrInvalidRequest
	}
	if request.ExpectedWorkflowDefinitionID != 0 &&
		int64(source.WorkflowDefinitionID) != request.ExpectedWorkflowDefinitionID {
		return BindResult{}, true, ErrInvalidRequest
	}
	if request.ExpectedTargetConfigHash != "" && source.ParameterizedConfigHash != request.ExpectedTargetConfigHash {
		return BindResult{}, true, ErrInvalidRequest
	}
	if request.ExpectedDevValidationProvenanceHash != nil &&
		source.DevValidationProvenanceHash != *request.ExpectedDevValidationProvenanceHash {
		return BindResult{}, true, ErrInvalidRequest
	}
	if err := b.mergeStoredSourceInputs(ctx, admission, &request, source); err != nil {
		return BindResult{}, true, err
	}
	bindings, err := b.matchBindings(ctx, source.ID, request.Inputs)
	if err != nil {
		return BindResult{}, true, err
	}
	inputs := make(map[string]snapshot.SnapshotRef, len(bindings))
	for _, binding := range bindings {
		inputs[binding.PortName] = binding.Snapshot
	}
	createRequest := db.AgentWorkflowRunCreateRequest{
		DefinitionKind: request.DefinitionKind,
		TeamID:         admission.TeamID, TeamName: admission.TeamName,
		WorkflowDefinitionID: source.WorkflowDefinitionID, WorkflowName: source.WorkflowName,
		WorkflowVersion: source.WorkflowVersion, SchemaVersion: source.SchemaVersion,
		SignatureVersion: source.SignatureVersion, DefinitionContentHash: source.DefinitionContentHash,
		FunctionID: cloneString(source.FunctionID), IdempotencyKey: request.IdempotencyKey,
		ParameterizedConfig: cloneRaw(source.ParameterizedConfig), ParameterizedConfigHash: source.ParameterizedConfigHash,
		DevValidationProvenanceHash: source.DevValidationProvenanceHash,
		ResourceSourceAdmissionID:   cloneInt64(source.ResourceSourceAdmissionID),
		OriginKind:                  admission.Origin.Kind, OriginReference: admission.Origin.Reference,
		CreatedBy: admission.CreatedBy, Status: db.AgentWorkflowRunStatusAdmitting,
		RetryOfWorkflowRunID: cloneWorkflowRunID(request.RetryOf), Inputs: cloneRefs(inputs),
		ExperimentAdmission: cloneDBExperimentAdmission(request.ExperimentAdmission),
	}
	applyTicketAssociation(&createRequest, admission.Ticket)
	run, created, err := b.runs.CreateWithInputs(ctx, createRequest)
	if err != nil {
		if errors.Is(err, db.ErrAgentWorkflowRunExperimentAdmissionClosed) {
			return BindResult{}, true, ErrExperimentAdmissionClosed
		}
		if request.ExperimentAdmission != nil {
			return BindResult{}, true, fmt.Errorf("%w: allocate durable experiment workflow run", ErrPlatformFailure)
		}
		if winner, found, readErr := b.resolvedWinner(ctx, createRequest, request.Inputs); readErr == nil && found {
			result, existingErr := b.handleExisting(ctx, admission, request, winner)
			return result, true, existingErr
		} else if readErr != nil && errors.Is(readErr, ErrIdempotencyConflict) {
			return BindResult{}, true, readErr
		}
		return BindResult{}, true, fmt.Errorf("%w: allocate replay workflow run", ErrPlatformFailure)
	}
	run = cloneRun(run)
	if err := compareAllocatedRun(run, createRequest); err != nil {
		if created && run.ID.Validate() == nil {
			result, failure := b.failAllocated(ctx, admission, request, run, true, fmt.Errorf("%w: allocated replay workflow-run identity mismatch", ErrCorruptPartialAdmission))
			return result, true, failure
		}
		return BindResult{}, true, err
	}
	if _, err := b.matchBindings(ctx, run.ID, request.Inputs); err != nil {
		if created && !errors.Is(err, ErrIdempotencyConflict) {
			result, failure := b.failAllocated(ctx, admission, request, run, true, err)
			return result, true, failure
		}
		return BindResult{}, true, err
	}
	if !created {
		result, existingErr := b.handleExisting(ctx, admission, request, run)
		return result, true, existingErr
	}
	if run.Status != db.AgentWorkflowRunStatusAdmitting || !executionEmpty(run) {
		result, failure := b.failAllocated(ctx, admission, request, run, true, ErrCorruptPartialAdmission)
		return result, true, failure
	}
	result, err := b.resume(ctx, admission, request, run, true, nil)
	return result, true, err
}

func (b *Binder) resolve(ctx context.Context, request BindRequest) (workflow.Definition, bool, error) {
	if request.DefinitionKind == workflow.DefinitionKindNode {
		if nilInterface(b.nodes) {
			return workflow.Definition{}, false, fmt.Errorf("%w: node store is unavailable", ErrPlatformFailure)
		}
		node, found, err := b.nodes.Get(request.WorkflowName, *request.Version)
		if err != nil {
			return workflow.Definition{}, false, fmt.Errorf("%w: resolve node definition: %v", ErrPlatformFailure, err)
		}
		if !found {
			return workflow.Definition{}, false, nil
		}
		return executableNodeDefinition(*node, request.NodeParameters)
	}
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

func executableNodeDefinition(node workflow.NodeDefinition, parameters map[string]string) (workflow.Definition, bool, error) {
	if node.ID <= 0 || node.Version <= 0 || strings.TrimSpace(node.Name) == "" || strings.TrimSpace(node.ContentHash) == "" {
		return workflow.Definition{}, false, fmt.Errorf("%w: inconsistent durable node identity", ErrPlatformFailure)
	}
	function, err := node.Compiled.Instantiate(parameters)
	if err != nil {
		return workflow.Definition{}, false, fmt.Errorf("%w: instantiate node: %v", ErrInvalidRequest, err)
	}
	if function.SignatureVersion <= 0 {
		return workflow.Definition{}, false, fmt.Errorf("%w: inconsistent compiled node metadata", ErrPlatformFailure)
	}
	definition := workflow.Definition{
		ID: node.ID, Name: node.Name, Version: node.Version, ContentHash: node.ContentHash,
		SchemaVersion: 3, SignatureVersion: function.SignatureVersion,
		Compiled: workflow.CompiledDefinition{SchemaVersion: 3, Name: node.Name, Function: function},
	}
	return definition, true, nil
}

func (b *Binder) findByIdempotencyKey(
	ctx context.Context,
	teamID int,
	kind workflow.DefinitionKind,
	key string,
) (db.AgentWorkflowRun, bool, error) {
	if kind == workflow.DefinitionKindWorkflow {
		return b.runs.FindByIdempotencyKey(ctx, teamID, key)
	}
	store, ok := b.runs.(KindAwareWorkflowRunStore)
	if !ok {
		return db.AgentWorkflowRun{}, false, fmt.Errorf("workflow run: kind-aware store is required")
	}
	return store.FindByIdempotencyKeyKind(ctx, teamID, kind, key)
}

func (b *Binder) get(
	ctx context.Context,
	teamID int,
	kind workflow.DefinitionKind,
	id snapshot.WorkflowRunID,
) (db.AgentWorkflowRun, bool, error) {
	if kind == workflow.DefinitionKindWorkflow {
		return b.runs.Get(ctx, teamID, id)
	}
	store, ok := b.runs.(KindAwareWorkflowRunStore)
	if !ok {
		return db.AgentWorkflowRun{}, false, fmt.Errorf("workflow run: kind-aware store is required")
	}
	return store.GetKind(ctx, teamID, kind, id)
}

func (b *Binder) existing(
	ctx context.Context,
	admission AdmissionContext,
	request BindRequest,
) (db.AgentWorkflowRun, bool, error) {
	run, found, err := b.findByIdempotencyKey(ctx, admission.TeamID, request.DefinitionKind, request.IdempotencyKey)
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
	if err := b.compareNodeParameterIntent(run, request); err != nil {
		return db.AgentWorkflowRun{}, false, err
	}
	if err := b.mergeStoredSourceInputs(ctx, admission, &request, run); err != nil {
		return db.AgentWorkflowRun{}, false, err
	}
	if _, err := b.matchBindings(ctx, run.ID, request.Inputs); err != nil {
		return db.AgentWorkflowRun{}, false, err
	}
	return run, true, nil
}

func (b *Binder) compareNodeParameterIntent(run db.AgentWorkflowRun, request BindRequest) error {
	if request.DefinitionKind != workflow.DefinitionKindNode {
		return nil
	}
	if nilInterface(b.nodes) {
		return fmt.Errorf("%w: node store is unavailable", ErrPlatformFailure)
	}
	node, found, err := b.nodes.Get(run.WorkflowName, run.WorkflowVersion)
	if err != nil {
		return fmt.Errorf("%w: resolve durable node definition", ErrPlatformFailure)
	}
	if !found || node.ID != run.WorkflowDefinitionID || node.Name != run.WorkflowName ||
		node.Version != run.WorkflowVersion || node.ContentHash != run.DefinitionContentHash ||
		node.Compiled.Function.SignatureVersion != run.SignatureVersion {
		return fmt.Errorf("%w: durable node definition mismatch", ErrCorruptPartialAdmission)
	}
	config, _, err := canonicalConfig(run.ParameterizedConfig)
	if err != nil {
		return fmt.Errorf("%w: invalid durable node parameterized config", ErrCorruptPartialAdmission)
	}
	durable, err := nodeParametersFromConfig(*node, config)
	if err != nil {
		return fmt.Errorf("%w: durable node parameters mismatch", ErrCorruptPartialAdmission)
	}
	requested, err := resolvedNodeParameters(*node, request.NodeParameters)
	if err != nil {
		return fmt.Errorf("%w: instantiate node: %v", ErrInvalidRequest, err)
	}
	if !equalNodeParameters(durable, requested) {
		return ErrIdempotencyConflict
	}
	return nil
}

// mergeStoredSourceInputs makes a durable ready admission part of an
// idempotent run's expected bindings. It intentionally only loads a ready
// admission: retries and resumes must not reopen a standing source pipeline.
func (b *Binder) mergeStoredSourceInputs(
	ctx context.Context,
	admission AdmissionContext,
	request *BindRequest,
	run db.AgentWorkflowRun,
) error {
	if run.ResourceSourceAdmissionID == nil {
		return nil
	}
	if *run.ResourceSourceAdmissionID <= 0 || nilInterface(b.sources) {
		return fmt.Errorf("%w: durable resource source admission is unavailable", ErrCorruptPartialAdmission)
	}
	definition, found, err := b.definitions.Get(ctx, run.WorkflowName, run.WorkflowVersion)
	if err != nil {
		return fmt.Errorf("%w: resolve durable resource source definition", ErrPlatformFailure)
	}
	if !found || definition.ID != run.WorkflowDefinitionID || definition.Name != run.WorkflowName ||
		definition.Version != run.WorkflowVersion || definition.ContentHash != run.DefinitionContentHash ||
		definition.SignatureVersion != run.SignatureVersion {
		return fmt.Errorf("%w: durable resource source definition changed", ErrInvalidRequest)
	}
	target, err := workflow.ResourceSourcePipelineTargetFor(definition, admission.TeamID)
	if err != nil {
		return fmt.Errorf("%w: derive durable resource source target", ErrInvalidRequest)
	}
	ready, err := b.sources.LoadReady(ctx, admission.TeamID, *run.ResourceSourceAdmissionID, target)
	if err != nil {
		return sourceAdmissionError(err)
	}
	if err := validateReadySourceAdmission(ready, admission.TeamID, target); err != nil {
		return err
	}
	for port, ref := range ready.Inputs {
		if got, found := request.Inputs[port]; found && got != ref.ID {
			return ErrIdempotencyConflict
		}
		request.Inputs[port] = ref.ID
	}
	return nil
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
		resolveRequest := request
		version := run.WorkflowVersion
		resolveRequest.WorkflowName = run.WorkflowName
		resolveRequest.Version = &version
		if request.DefinitionKind == workflow.DefinitionKindNode {
			node, nodeFound, nodeErr := b.nodes.Get(run.WorkflowName, run.WorkflowVersion)
			if nodeErr != nil || !nodeFound || node.ID != run.WorkflowDefinitionID || node.ContentHash != run.DefinitionContentHash {
				return b.failAllocated(ctx, admission, request, run, created, fmt.Errorf("%w: durable node definition mismatch", ErrCorruptPartialAdmission))
			}
			values, valuesErr := nodeParametersFromConfig(*node, config)
			if valuesErr != nil {
				return b.failAllocated(ctx, admission, request, run, created, fmt.Errorf("%w: durable node parameters mismatch", ErrCorruptPartialAdmission))
			}
			resolveRequest.NodeParameters = values
		}
		definition, found, getErr := b.resolve(ctx, resolveRequest)
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
		if len(target.Function.ResourceSources) == 0 {
			rendered, err = b.renderer.RenderFunction(target)
		} else {
			boundSources, sourceErr := sourceRefsForTarget(target, bindings)
			if sourceErr != nil {
				return b.failAllocated(ctx, admission, request, run, created, sourceErr)
			}
			rendered, err = b.renderer.RenderFunctionWithBoundSources(target, boundSources)
		}
		if err != nil {
			return b.failAllocated(ctx, admission, request, run, created, fmt.Errorf("%w: rerender durable workflow target", ErrCorruptPartialAdmission))
		}
	}
	expectedCanonical, err := validateRendered(target, rendered)
	if err != nil || !bytes.Equal(canonical, expectedCanonical) || rendered.TargetConfigHash != run.ParameterizedConfigHash {
		return b.failAllocated(ctx, admission, request, run, created, fmt.Errorf("%w: durable parameterized config hash mismatch", ErrCorruptPartialAdmission))
	}
	if request.DefinitionKind == workflow.DefinitionKindNode {
		rendered.TemplateName = fmt.Sprintf(
			"agent-node-%s-v%d-%s", run.WorkflowName, run.WorkflowVersion, rendered.TargetConfigHash[:12],
		)
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
	envelope, err := durableExecutionEnvelope(run.ID, bindings, config, rendered)
	if err != nil {
		return b.failAllocated(ctx, admission, request, run, created, err)
	}
	params, err := envelope.ParamsFor(canonical, rendered.TargetConfigHash)
	if err != nil {
		return b.failAllocated(ctx, admission, request, run, created, fmt.Errorf("%w: open workflow execution envelope", ErrCorruptPartialAdmission))
	}
	if _, err := atc.ValidateRunParams(config.Params, cloneParams(params)); err != nil {
		return b.failAllocated(ctx, admission, request, run, created, fmt.Errorf("%w: invalid durable parameters", ErrCorruptPartialAdmission))
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
		BrokerProfiles: cloneBrokerProfiles(rendered.BrokerProfiles), BrokerProfileProvenanceHash: rendered.BrokerProfileProvenanceHash,
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
		ctx, run.ID, templateRef, envelope, run.CreatedBy, nil,
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

func nodeParametersFromConfig(node workflow.NodeDefinition, config atc.Config) (map[string]string, error) {
	values := make(map[string]string, len(node.Compiled.Parameters))
	if len(node.Compiled.Parameters) == 0 {
		return values, nil
	}
	var environment atc.TaskEnv
	leaves := 0
	for _, job := range config.Jobs {
		for _, step := range job.PlanSequence {
			if err := step.Config.Visit(atc.StepRecursor{
				OnTask:  func(value *atc.TaskStep) error { leaves++; environment = value.Params; return nil },
				OnAgent: func(value *atc.AgentStep) error { leaves++; environment = value.Env; return nil },
			}); err != nil {
				return nil, err
			}
		}
	}
	if leaves != 1 {
		return nil, fmt.Errorf("node rendered %d leaves", leaves)
	}
	for _, parameter := range node.Compiled.Parameters {
		value, found := environment[parameter.Name]
		if !found {
			return nil, fmt.Errorf("node parameter %q is absent", parameter.Name)
		}
		values[parameter.Name] = value
	}
	return values, nil
}

func resolvedNodeParameters(node workflow.NodeDefinition, supplied map[string]string) (map[string]string, error) {
	function, err := node.Compiled.Instantiate(supplied)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(node.Compiled.Parameters))
	if len(node.Compiled.Parameters) == 0 {
		return values, nil
	}
	var environment atc.TaskEnv
	switch leaf := function.Plan[0].Config.(type) {
	case *atc.TaskStep:
		environment = leaf.Params
	case *atc.AgentStep:
		environment = leaf.Env
	default:
		return nil, fmt.Errorf("node parameters require a task or agent leaf")
	}
	for _, parameter := range node.Compiled.Parameters {
		value, found := environment[parameter.Name]
		if !found {
			return nil, fmt.Errorf("node parameter %q is absent", parameter.Name)
		}
		values[parameter.Name] = value
	}
	return values, nil
}

func equalNodeParameters(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for name, value := range left {
		if other, found := right[name]; !found || other != value {
			return false
		}
	}
	return true
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
	if request.DefinitionKind == "" {
		request.DefinitionKind = workflow.DefinitionKindWorkflow
	}
	if request.DefinitionKind != workflow.DefinitionKindWorkflow && request.DefinitionKind != workflow.DefinitionKindNode {
		return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: unknown definition kind", ErrInvalidRequest)
	}
	if request.DefinitionKind == workflow.DefinitionKindWorkflow && len(request.NodeParameters) != 0 {
		return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: workflow requests cannot include node parameters", ErrInvalidRequest)
	}
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
	if request.DefinitionKind == workflow.DefinitionKindNode {
		// Kept: a node's whole executable surface is one leaf step. There is
		// no function to select between, so a function ID is meaningless for
		// every caller, frozen or not.
		if request.FunctionID != "" {
			return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: node requests cannot select a workflow function", ErrInvalidRequest)
		}
		// Kept: a node has no live pointer, so an implicit version would bind
		// whichever version happened to be newest at admission time.
		if request.Version == nil {
			return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: node version must be explicit", ErrInvalidRequest)
		}
		// Relaxed for one caller. An experiment cell binds a target the
		// experiment froze at Start, and the freeze is what makes the graded
		// A/B trustworthy: without pinning the definition and the rendered
		// config, a node redefined mid-run would be compared against its own
		// replacement. An ordinary node request has no frozen record to pin
		// against, so these expectations stay rejected there -- accepting them
		// would let a caller assert an identity nobody recorded.
		if request.ExperimentAdmission == nil &&
			(request.ExpectedWorkflowDefinitionID != 0 ||
				request.ExpectedTargetConfigHash != "" ||
				request.ExpectedDevValidationProvenanceHash != nil) {
			return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: node requests cannot pin a frozen workflow identity outside an experiment", ErrInvalidRequest)
		}
		// Fail closed the other way too: an experiment node bind that lost its
		// frozen coordinates must error rather than run unfrozen and be graded
		// as if it had not.
		if request.ExperimentAdmission != nil &&
			(request.ExpectedWorkflowDefinitionID == 0 || request.ExpectedTargetConfigHash == "") {
			return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: experiment node requests must carry their frozen definition ID and target config hash", ErrInvalidRequest)
		}
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
	if request.ExpectedDevValidationProvenanceHash != nil &&
		*request.ExpectedDevValidationProvenanceHash != "" &&
		!targetConfigHashPattern.MatchString(*request.ExpectedDevValidationProvenanceHash) {
		return AdmissionContext{}, BindRequest{}, fmt.Errorf("%w: expected dev validation provenance hash must be empty or lower-case 64-hex", ErrInvalidRequest)
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
	request.NodeParameters = cloneNodeParameters(request.NodeParameters)
	request.Version = cloneInt(request.Version)
	request.ExpectedDevValidationProvenanceHash = cloneString(request.ExpectedDevValidationProvenanceHash)
	request.RetryOf = cloneWorkflowRunID(request.RetryOf)
	request.ExperimentAdmission = cloneExperimentAdmission(request.ExperimentAdmission)
	return cloneAdmission(admission), request, nil
}

func cloneNodeParameters(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for name, value := range values {
		cloned[name] = value
	}
	return cloned
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
	if err := validateRenderedParams(target.Signature, rendered); err != nil {
		return nil, err
	}
	canonical, err := rendered.Config.CanonicalJSON()
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize rendered target", ErrPlatformFailure)
	}
	hash, err := workflow.RenderedTargetConfigHashWithBrokerProfiles(rendered.Config, rendered.DevValidationProfiles, rendered.DevValidationProvenanceHash, rendered.BrokerProfiles, rendered.BrokerProfileProvenanceHash)
	if err != nil || hash != rendered.TargetConfigHash {
		return nil, fmt.Errorf("%w: rendered target hash mismatch", ErrPlatformFailure)
	}
	wantName, err := workflow.TemplateName(target.Kind, target.WorkflowName, target.WorkflowVersion, target.FunctionID, hash)
	if err != nil || wantName != rendered.TemplateName {
		return nil, fmt.Errorf("%w: rendered template name mismatch", ErrPlatformFailure)
	}
	boundSources := rendered.BoundSourceParamNames()
	if !target.Signature.Equal(rendered.TargetSignature) || len(rendered.InputParamNames) != len(target.Signature.Inputs)+len(boundSources) {
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
	for _, source := range target.Function.ResourceSources {
		name, err := workflow.InputParamName(source.Name)
		if err != nil {
			return nil, fmt.Errorf("%w: rendered resource-source parameter mismatch", ErrPlatformFailure)
		}
		if _, bound := boundSources[name]; !bound || rendered.InputParamNames[source.Name] != name {
			return nil, fmt.Errorf("%w: rendered resource-source parameter mismatch", ErrPlatformFailure)
		}
	}
	return canonical, nil
}

func validateRenderedParams(signature workflow.PublicSignature, rendered workflow.RenderedFunction) error {
	config := rendered.Config
	boundSources := rendered.BoundSourceParamNames()
	if !config.Template || len(config.Params) != len(signature.Inputs)+1+len(boundSources) {
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
	for name := range boundSources {
		param, found := params[name]
		if !found || param.Type != "string" || !param.Required || param.Format != atc.ParamFormatPositiveDecimalInt64 || param.Default != nil {
			return fmt.Errorf("%w: rendered resource-source parameter mismatch", ErrPlatformFailure)
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

func sourceAdmissionError(err error) error {
	if errors.Is(err, ErrSourceCapturePending) || errors.Is(err, ErrInvalidRequest) ||
		errors.Is(err, ErrSnapshotUnavailable) || errors.Is(err, ErrSnapshotTypeMismatch) {
		return err
	}
	return fmt.Errorf("%w: load ready resource source admission", ErrPlatformFailure)
}

func validateReadySourceAdmission(
	ready ReadySourceAdmission,
	teamID int,
	target workflow.ResourceSourcePipelineTarget,
) error {
	if ready.AdmissionID <= 0 || ready.TeamID != teamID ||
		ready.WorkflowDefinitionID != target.WorkflowDefinitionID ||
		ready.WorkflowName != target.WorkflowName ||
		ready.WorkflowVersion != target.WorkflowVersion {
		return fmt.Errorf("%w: ready resource source admission identity drifted", ErrInvalidRequest)
	}
	rendered, err := workflow.RenderResourceSourcePipeline(target)
	if err != nil {
		return fmt.Errorf("%w: ready resource source target is invalid", ErrInvalidRequest)
	}
	if ready.SourceConfigHash != rendered.ConfigHash {
		return fmt.Errorf("%w: ready resource source configuration changed", ErrInvalidRequest)
	}
	if len(ready.Inputs) != len(target.Sources) {
		return fmt.Errorf("%w: ready resource source inputs do not cover target", ErrInvalidRequest)
	}
	for _, source := range target.Sources {
		ref, found := ready.Inputs[source.Name]
		if !found || ref.Type != source.Type || ref.Validate() != nil {
			return fmt.Errorf("%w: ready resource source input drifted", ErrInvalidRequest)
		}
	}
	return nil
}

func sameReadySourceAdmission(left, right ReadySourceAdmission) bool {
	return left.AdmissionID == right.AdmissionID && left.TeamID == right.TeamID &&
		left.WorkflowDefinitionID == right.WorkflowDefinitionID &&
		left.WorkflowName == right.WorkflowName && left.WorkflowVersion == right.WorkflowVersion &&
		left.SourceConfigHash == right.SourceConfigHash && reflect.DeepEqual(left.Inputs, right.Inputs)
}

func mergeSourceInputRefs(
	caller map[string]snapshot.SnapshotRef,
	sources map[string]snapshot.SnapshotRef,
) (map[string]snapshot.SnapshotRef, error) {
	merged := cloneRefs(caller)
	for port, ref := range sources {
		if _, found := merged[port]; found {
			return nil, fmt.Errorf("%w: resource source conflicts with caller input %q", ErrInvalidRequest, port)
		}
		merged[port] = ref
	}
	return merged, nil
}

func snapshotIDs(refs map[string]snapshot.SnapshotRef) map[string]snapshot.SnapshotID {
	ids := make(map[string]snapshot.SnapshotID, len(refs))
	for port, ref := range refs {
		ids[port] = ref.ID
	}
	return ids
}

func sourceRefsForTarget(
	target workflow.FunctionTarget,
	bindings []db.AgentWorkflowRunSnapshotBinding,
) (map[string]snapshot.SnapshotRef, error) {
	byPort := make(map[string]snapshot.SnapshotRef, len(bindings))
	for _, binding := range bindings {
		if binding.Direction != db.AgentWorkflowRunSnapshotInput {
			continue
		}
		byPort[binding.PortName] = binding.Snapshot
	}
	refs := make(map[string]snapshot.SnapshotRef, len(target.Function.ResourceSources))
	for _, source := range target.Function.ResourceSources {
		if _, duplicate := refs[source.Name]; duplicate {
			return nil, fmt.Errorf("%w: durable resource source declaration is duplicated", ErrCorruptPartialAdmission)
		}
		ref, found := byPort[source.Name]
		if !found || ref.Type != source.Type || ref.Validate() != nil {
			return nil, fmt.Errorf("%w: durable resource source binding drifted", ErrCorruptPartialAdmission)
		}
		refs[source.Name] = ref
	}
	return refs, nil
}

func durableExecutionEnvelope(
	runID snapshot.WorkflowRunID,
	bindings []db.AgentWorkflowRunSnapshotBinding,
	config atc.Config,
	rendered workflow.RenderedFunction,
) (workflow.ExecutionEnvelope, error) {
	params := map[string]any{"workflow_run_id": runID.String()}
	generated := map[string]struct{}{"workflow_run_id": {}}
	boundSources := rendered.BoundSourceParamNames()
	for _, binding := range bindings {
		name, err := workflow.InputParamName(binding.PortName)
		if err != nil {
			return workflow.ExecutionEnvelope{}, fmt.Errorf("%w: invalid durable input port", ErrCorruptPartialAdmission)
		}
		params[name] = binding.Snapshot.ID.String()
		generated[name] = struct{}{}
	}
	for _, schema := range config.Params {
		if _, found := generated[schema.Name]; found {
			continue
		}
		if _, bound := boundSources[schema.Name]; bound {
			continue
		}
		defaultValue, ok := schema.Default.(string)
		if !ok || defaultValue != "0" {
			return workflow.ExecutionEnvelope{}, fmt.Errorf("%w: unexpected durable parameter schema", ErrCorruptPartialAdmission)
		}
		params[schema.Name] = "0"
		generated[schema.Name] = struct{}{}
	}
	envelope, err := rendered.ExecutionEnvelope(params)
	if err != nil {
		return workflow.ExecutionEnvelope{}, fmt.Errorf("%w: bind durable execution envelope", ErrCorruptPartialAdmission)
	}
	return envelope, nil
}

func compareCallerIntent(run db.AgentWorkflowRun, admission AdmissionContext, request BindRequest) error {
	if request.ExpectedWorkflowDefinitionID != 0 && int64(run.WorkflowDefinitionID) != request.ExpectedWorkflowDefinitionID {
		return fmt.Errorf("%w: frozen workflow definition does not match the durable workflow run", ErrInvalidRequest)
	}
	if request.ExpectedTargetConfigHash != "" && run.ParameterizedConfigHash != request.ExpectedTargetConfigHash {
		return fmt.Errorf("%w: frozen target config does not match the durable workflow run", ErrInvalidRequest)
	}
	if request.ExpectedDevValidationProvenanceHash != nil &&
		run.DevValidationProvenanceHash != *request.ExpectedDevValidationProvenanceHash {
		return fmt.Errorf("%w: frozen dev validation authority does not match the durable workflow run", ErrInvalidRequest)
	}
	wantFunction := optionalFunctionID(request.FunctionID)
	if normalizedRunDefinitionKind(run.DefinitionKind) != request.DefinitionKind || run.TeamID != admission.TeamID || run.IdempotencyKey != request.IdempotencyKey ||
		run.WorkflowName != request.WorkflowName || !equalStringPointer(run.FunctionID, wantFunction) ||
		run.OriginKind != admission.Origin.Kind || run.OriginReference != admission.Origin.Reference ||
		run.CreatedBy != admission.CreatedBy || !equalRunIDPointer(run.RetryOfWorkflowRunID, request.RetryOf) ||
		!runMatchesTicketAssociation(run, admission.Ticket) ||
		(request.Version != nil && run.WorkflowVersion != *request.Version) {
		return ErrIdempotencyConflict
	}
	return nil
}

// runMatchesTicketAssociation compares the run's durable evidence rather than
// only its live foreign key, so a run whose intake ticket was deleted still
// reports the association it was admitted under.
func runMatchesTicketAssociation(run db.AgentWorkflowRun, association *TicketAssociation) bool {
	if association == nil {
		return run.TicketID == nil && run.TicketReference == ""
	}
	return run.TicketReference == association.Reference &&
		(run.TicketID == nil || *run.TicketID == association.ID)
}

func compareAllocatedRun(run db.AgentWorkflowRun, request db.AgentWorkflowRunCreateRequest) error {
	if normalizedRunDefinitionKind(run.DefinitionKind) != request.DefinitionKind || run.TeamID != request.TeamID || run.WorkflowDefinitionID != request.WorkflowDefinitionID ||
		run.WorkflowName != request.WorkflowName || run.WorkflowVersion != request.WorkflowVersion ||
		run.SchemaVersion != request.SchemaVersion || run.SignatureVersion != request.SignatureVersion ||
		run.DefinitionContentHash != request.DefinitionContentHash || !equalStringPointer(run.FunctionID, request.FunctionID) ||
		run.IdempotencyKey != request.IdempotencyKey || run.ParameterizedConfigHash != request.ParameterizedConfigHash ||
		run.DevValidationProvenanceHash != request.DevValidationProvenanceHash ||
		!equalInt64Pointer(run.ResourceSourceAdmissionID, request.ResourceSourceAdmissionID) ||
		!jsonEqual(run.ParameterizedConfig, request.ParameterizedConfig) || run.OriginKind != request.OriginKind ||
		run.OriginReference != request.OriginReference || run.CreatedBy != request.CreatedBy ||
		!equalRunIDPointer(run.RetryOfWorkflowRunID, request.RetryOfWorkflowRunID) ||
		run.TicketReference != request.TicketReference ||
		(run.TicketID != nil && request.TicketID != nil && *run.TicketID != *request.TicketID) {
		return ErrIdempotencyConflict
	}
	return nil
}

func normalizedRunDefinitionKind(kind workflow.DefinitionKind) workflow.DefinitionKind {
	if kind == "" {
		return workflow.DefinitionKindWorkflow
	}
	return kind
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
	return value.Clone()
}

func cloneConfig(value atc.Config) atc.Config {
	cloned, err := copystructure.Copy(value)
	if err != nil {
		return value
	}
	return cloned.(atc.Config)
}

func cloneAdmission(value AdmissionContext) AdmissionContext {
	value.Ticket = cloneTicketAssociation(value.Ticket)
	return value
}

func applyTicketAssociation(
	request *db.AgentWorkflowRunCreateRequest,
	association *TicketAssociation,
) {
	if association == nil {
		return
	}
	id := association.ID
	request.TicketID = &id
	request.TicketReference = association.Reference
}

func cloneTicketAssociation(value *TicketAssociation) *TicketAssociation {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// resolveTicketAssociation decides the run's optional ticket context exactly
// once, keeping it explicit rather than inferred from origin strings or
// snapshot lineage.
//
// A retry inherits its source's association: the retried work is the same
// ticket's work, and the immutable evidence must travel with it. That
// inheritance is authoritative — a caller cannot use a retry to attach a
// ticket the source never had, nor to move a retry to a different ticket.
func (b *Binder) resolveTicketAssociation(
	ctx context.Context,
	admission AdmissionContext,
	request BindRequest,
) (AdmissionContext, error) {
	if admission.Ticket != nil {
		if err := admission.Ticket.Validate(); err != nil {
			return AdmissionContext{}, err
		}
	}
	if request.RetryOf == nil {
		return admission, nil
	}
	source, found, err := b.get(ctx, admission.TeamID, request.DefinitionKind, *request.RetryOf)
	if err != nil {
		return AdmissionContext{}, fmt.Errorf("%w: load retry source ticket association", ErrPlatformFailure)
	}
	if !found {
		// The durable retry validation stays authoritative if the source row is
		// missing; refusing to invent an association is the safe answer here.
		if admission.Ticket != nil {
			return AdmissionContext{}, fmt.Errorf(
				"%w: a retry cannot declare a ticket its source does not have", ErrInvalidRequest)
		}
		return admission, nil
	}
	inherited := (*TicketAssociation)(nil)
	if source.TicketID != nil {
		inherited = &TicketAssociation{ID: *source.TicketID, Reference: source.TicketReference}
	}
	if admission.Ticket != nil {
		if inherited == nil || *admission.Ticket != *inherited {
			return AdmissionContext{}, fmt.Errorf(
				"%w: a retry must inherit its source's ticket association", ErrInvalidRequest)
		}
	}
	admission.Ticket = inherited
	return admission, nil
}

// inheritTicketFrom reads the association of an explicitly named launching run.
// A missing or unattached source yields no association rather than an error:
// tickets are optional throughout, and a follow-on workflow launched from a
// standalone run is itself standalone.
//
// A source whose intake ticket was deleted keeps only its evidence, which has
// no live id to re-admit under, so the follow-on is unattached. The deleted
// ticket has no journal to appear in either.
func (b *Binder) inheritTicketFrom(
	ctx context.Context,
	teamID int,
	kind workflow.DefinitionKind,
	runID snapshot.WorkflowRunID,
) (*TicketAssociation, error) {
	if runID.Validate() != nil {
		return nil, nil
	}
	source, found, err := b.get(ctx, teamID, kind, runID)
	if err != nil {
		return nil, fmt.Errorf("%w: load launching run ticket association", ErrPlatformFailure)
	}
	if !found || source.TicketID == nil {
		return nil, nil
	}
	return &TicketAssociation{ID: *source.TicketID, Reference: source.TicketReference}, nil
}

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
	run.ResourceSourceAdmissionID = cloneInt64(run.ResourceSourceAdmissionID)
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

func equalInt64Pointer(left, right *int64) bool {
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
	var rendered workflow.RenderedFunction
	if len(target.Function.ResourceSources) == 0 {
		rendered, err = renderer.RenderFunction(target)
	} else {
		// Promotion only proves that this persisted definition can render. The
		// synthetic refs are never persisted, scheduled, or exposed as runtime
		// authority; live runs still require a sealed source admission.
		rendered, err = renderer.RenderFunctionWithBoundSources(
			target,
			promotionSourceRefs(target.Function.ResourceSources),
		)
	}
	if err != nil {
		return fmt.Errorf("render workflow target: %w", err)
	}
	if _, err := validateRendered(target, rendered); err != nil {
		return fmt.Errorf("validate rendered workflow target: %w", err)
	}
	return nil
}

func promotionSourceRefs(sources []workflow.ResourceSource) map[string]snapshot.SnapshotRef {
	refs := make(map[string]snapshot.SnapshotRef, len(sources))
	for index, source := range sources {
		payload := []byte("workflow-promotion-source-ref/v1\x00" + source.Name + "\x00" + string(source.Type))
		digest := sha256.Sum256(payload)
		refs[source.Name] = snapshot.SnapshotRef{
			ID:     snapshot.SnapshotID(index + 1),
			Type:   source.Type,
			Digest: snapshot.Digest(fmt.Sprintf("sha256:%x", digest)),
		}
	}
	return refs
}

func (WorkflowTargetRenderer) FullFunctionTarget(definition workflow.Definition) (workflow.FunctionTarget, error) {
	return workflow.FullFunctionTarget(definition)
}

func (WorkflowTargetRenderer) ExtractFunctionTarget(definition workflow.Definition, functionID string) (workflow.FunctionTarget, error) {
	return workflow.ExtractFunctionTarget(definition, functionID)
}

func (renderer WorkflowTargetRenderer) RenderFunction(target workflow.FunctionTarget) (workflow.RenderedFunction, error) {
	return workflow.RenderFunctionWithRuntimeImage(target, renderer.RuntimeImage)
}

func (renderer WorkflowTargetRenderer) RenderFunctionWithBoundSources(
	target workflow.FunctionTarget,
	sources map[string]snapshot.SnapshotRef,
) (workflow.RenderedFunction, error) {
	return workflow.RenderFunctionWithBoundSourcesAndRuntimeImage(
		target,
		sources,
		renderer.RuntimeImage,
	)
}
