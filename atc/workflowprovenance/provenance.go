// Package workflowprovenance captures immutable, canonical execution evidence
// from a post-planning Concourse plan without depending on persistence types.
package workflowprovenance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

const (
	MaxCanonicalPlanBytes  = 32 << 20
	MaxDependencyJSONBytes = 4 << 20
	MaxDependencyRecords   = 10_000
	MaxWorkflowOutputs     = 1_000

	dependencyEnvelopeVersion = 1
	maxTraversalNodes         = 1_000_000
	maxTraversalDepth         = 10_000
	maxPersistedPlanJSONBytes = MaxCanonicalPlanBytes * 2
	maxPersistedDepsJSONBytes = MaxDependencyJSONBytes * 2

	actualPlanHashDomain = "workflow-actual-plan/v1\x00"
	sourceHashDomain     = "workflow-dependency-source/v1\x00"
)

var (
	ErrInvalidProvenance      = errors.New("workflow provenance is invalid")
	ErrOutputContractMismatch = errors.New("workflow output contract mismatch")
	ErrDynamicWorkflowOutput  = errors.New("workflow output inside across is not statically provable")
	ErrLimitExceeded          = errors.New("workflow provenance limit exceeded")
)

type Input struct {
	WorkflowRunID        snapshot.WorkflowRunID
	WorkflowDefinitionID int
	ConcreteConfig       json.RawMessage
}

func (input Input) Validate() error {
	if err := input.WorkflowRunID.Validate(); err != nil {
		return invalidError("workflow run ID: %v", err)
	}
	if input.WorkflowDefinitionID <= 0 {
		return invalidError("workflow definition ID must be positive")
	}
	if len(input.ConcreteConfig) == 0 || !json.Valid(input.ConcreteConfig) {
		return invalidError("concrete config must be valid JSON")
	}
	return nil
}

type Capture struct {
	CanonicalPlan        json.RawMessage
	PlanHash             string
	ResolvedDependencies json.RawMessage
	Outputs              []ExpectedOutput
}

type ExpectedOutput struct {
	Port                 string                 `json:"port"`
	Type                 snapshot.TypeRef       `json:"type"`
	Optional             bool                   `json:"optional"`
	WorkflowDefinitionID int                    `json:"workflow_definition_id"`
	WorkflowRunID        snapshot.WorkflowRunID `json:"workflow_run_id"`
	Producers            []ExpectedProducer     `json:"producers"`
}

type ExpectedProducer struct {
	PlanID          string `json:"plan_id"`
	StepKind        string `json:"step_kind"`
	StepName        string `json:"step_name"`
	LocalOutputPort string `json:"local_output_port"`
}

type DependencyEnvelope struct {
	Version               int                  `json:"version"`
	Resources             []ResourceDependency `json:"resources"`
	Images                []ImageDependency    `json:"images"`
	PlatformResourceTypes []string             `json:"platform_resource_types"`
}

type ResourceDependency struct {
	PlanID             string      `json:"plan_id"`
	Name               string      `json:"name"`
	Resource           string      `json:"resource"`
	Type               string      `json:"type"`
	Version            atc.Version `json:"version"`
	SourceIdentityHash string      `json:"source_identity_hash"`
}

type ImageKind string

const (
	ImageKindTask               ImageKind = "task"
	ImageKindCustomResourceType ImageKind = "custom_resource_type"
	ImageKindCapabilitySidecar  ImageKind = "capability_sidecar"
	ImageKindAgentRuntime       ImageKind = "agent_runtime"
)

func (kind ImageKind) Validate() error {
	switch kind {
	case ImageKindTask, ImageKindCustomResourceType, ImageKindCapabilitySidecar, ImageKindAgentRuntime:
		return nil
	default:
		return invalidError("unsupported image dependency kind %q", kind)
	}
}

type ImageDependency struct {
	PlanID             string      `json:"plan_id"`
	Kind               ImageKind   `json:"kind"`
	Name               string      `json:"name"`
	Type               string      `json:"type,omitempty"`
	Version            atc.Version `json:"version,omitempty"`
	SourceIdentityHash string      `json:"source_identity_hash,omitempty"`
	ImageRef           string      `json:"image_ref,omitempty"`
}

// FromPlan captures the exact typed post-planning plan and compares its public
// output annotations against the independently decoded concrete config.
func FromPlan(input Input, plan atc.Plan) (Capture, error) {
	if err := input.Validate(); err != nil {
		return Capture{}, err
	}
	declared, err := declaredOutputs(input)
	if err != nil {
		return Capture{}, err
	}
	captured, actualOutputs, err := capturePlanEvidence(input, plan)
	if err != nil {
		return Capture{}, err
	}
	outputs, err := compareOutputContracts(declared, actualOutputs)
	if err != nil {
		return Capture{}, err
	}
	captured.Outputs = outputs
	return captured, nil
}

// VerifyFrozen reconstructs provenance from strict persisted execution JSON.
// JSONB may reserialize whitespace and object keys, so identity is established
// by recomputing the canonical bytes, plan hash, and dependency semantics.
// Persisted evidence corruption is reported independently from a valid
// plan/config output-contract mismatch.
func VerifyFrozen(
	input Input,
	actualPlan json.RawMessage,
	actualPlanHash string,
	resolvedDependencies json.RawMessage,
) (Capture, error) {
	if err := input.Validate(); err != nil {
		return Capture{}, err
	}
	if len(actualPlan) == 0 {
		return Capture{}, invalidError("persisted actual plan is empty")
	}
	if len(actualPlan) > maxPersistedPlanJSONBytes {
		return Capture{}, limitError("serialized persisted actual plan is %d bytes, hard maximum is %d", len(actualPlan), maxPersistedPlanJSONBytes)
	}
	if len(resolvedDependencies) == 0 {
		return Capture{}, invalidError("persisted dependency envelope is empty")
	}
	if len(resolvedDependencies) > maxPersistedDepsJSONBytes {
		return Capture{}, limitError("serialized persisted dependency envelope is %d bytes, hard maximum is %d", len(resolvedDependencies), maxPersistedDepsJSONBytes)
	}

	plan, err := decodeActualPlan(actualPlan)
	if err != nil {
		return Capture{}, invalidError("decode persisted actual plan: %v", err)
	}
	captured, actualOutputs, err := capturePlanEvidence(input, plan)
	if err != nil {
		return Capture{}, err
	}
	if actualPlanHash != captured.PlanHash {
		return Capture{}, invalidError("persisted actual plan hash does not match the canonical plan")
	}

	persistedEnvelope, err := decodeDependencyEnvelope(resolvedDependencies)
	if err != nil {
		return Capture{}, invalidError("decode persisted dependency envelope: %v", err)
	}
	canonicalPersistedEnvelope, err := atc.CanonicalJSON(persistedEnvelope)
	if err != nil {
		return Capture{}, invalidError("canonicalize persisted dependency envelope: %v", err)
	}
	if len(canonicalPersistedEnvelope) > MaxDependencyJSONBytes {
		return Capture{}, limitError("canonical persisted dependency envelope is %d bytes, maximum is %d", len(canonicalPersistedEnvelope), MaxDependencyJSONBytes)
	}
	if !bytes.Equal(canonicalPersistedEnvelope, captured.ResolvedDependencies) {
		return Capture{}, invalidError("persisted dependency envelope does not match the actual plan")
	}

	declared, err := declaredOutputs(input)
	if err != nil {
		return Capture{}, err
	}
	outputs, err := compareOutputContracts(declared, actualOutputs)
	if err != nil {
		return Capture{}, err
	}
	captured.Outputs = outputs
	return captured, nil
}

func capturePlanEvidence(input Input, plan atc.Plan) (Capture, map[string][]actualOutput, error) {

	builder := provenanceBuilder{
		input:         input,
		resources:     map[string]ResourceDependency{},
		images:        map[string]ImageDependency{},
		platformTypes: map[string]struct{}{},
		actualOutputs: map[string][]actualOutput{},
		pointerState:  map[*atc.Plan]visitState{},
		planKindsByID: map[string]string{},
		putPlansByID:  map[string]putPlanIdentity{},
	}
	if err := builder.walkPlan(&plan, walkContext{}, 0); err != nil {
		return Capture{}, nil, err
	}

	canonicalPlan, err := atc.CanonicalJSON(plan)
	if err != nil {
		return Capture{}, nil, invalidError("canonicalize actual plan: %v", err)
	}
	if len(canonicalPlan) > MaxCanonicalPlanBytes {
		return Capture{}, nil, limitError("canonical actual plan is %d bytes, maximum is %d", len(canonicalPlan), MaxCanonicalPlanBytes)
	}

	envelope := builder.dependencyEnvelope()
	dependencyRecords := len(envelope.Resources) + len(envelope.Images) + len(envelope.PlatformResourceTypes)
	if dependencyRecords > MaxDependencyRecords {
		return Capture{}, nil, limitError("dependency envelope contains %d records, maximum is %d", dependencyRecords, MaxDependencyRecords)
	}
	canonicalDependencies, err := atc.CanonicalJSON(envelope)
	if err != nil {
		return Capture{}, nil, invalidError("canonicalize dependency envelope: %v", err)
	}
	if len(canonicalDependencies) > MaxDependencyJSONBytes {
		return Capture{}, nil, limitError("dependency envelope is %d bytes, maximum is %d", len(canonicalDependencies), MaxDependencyJSONBytes)
	}

	hasher := sha256.New()
	_, _ = hasher.Write([]byte(actualPlanHashDomain))
	_, _ = hasher.Write(canonicalPlan)
	return Capture{
		CanonicalPlan:        append(json.RawMessage(nil), canonicalPlan...),
		PlanHash:             hex.EncodeToString(hasher.Sum(nil)),
		ResolvedDependencies: append(json.RawMessage(nil), canonicalDependencies...),
	}, builder.actualOutputs, nil
}

type declaredOutput struct {
	port                 string
	typ                  snapshot.TypeRef
	optional             bool
	workflowDefinitionID int
	workflowRunID        snapshot.WorkflowRunID
	stepKind             string
	stepName             string
	localOutputPort      string
}

func declaredOutputs(input Input) (map[string]declaredOutput, error) {
	decoder := json.NewDecoder(bytes.NewReader(input.ConcreteConfig))
	decoder.DisallowUnknownFields()
	var config atc.Config
	if err := decoder.Decode(&config); err != nil {
		return nil, invalidError("decode concrete config: %v", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, invalidError("decode concrete config: %v", err)
	}
	if len(config.Jobs) != 1 || config.Jobs[0].Name != "run" {
		return nil, invalidError("concrete config must contain exactly the workflow entry job %q", "run")
	}
	if len(config.Jobs[0].PlanSequence) == 0 {
		return nil, invalidError("concrete workflow entry job has no plan")
	}
	collector := declarationCollector{input: input, outputs: map[string]declaredOutput{}}
	if err := collector.walkStep(config.Jobs[0].Step(), false, 0); err != nil {
		return nil, err
	}
	return collector.outputs, nil
}

type declarationCollector struct {
	input   Input
	outputs map[string]declaredOutput
	count   int
}

func (collector *declarationCollector) walkStep(step atc.Step, insideAcross bool, depth int) error {
	if depth > maxTraversalDepth {
		return limitError("concrete plan nesting exceeds %d", maxTraversalDepth)
	}
	if step.Config == nil {
		return invalidError("concrete plan contains a step without a type")
	}
	if len(step.UnknownFields) != 0 {
		return invalidError("concrete plan contains unknown step fields")
	}
	walkWrapped := func(config atc.StepConfig) error {
		if config == nil {
			return invalidError("concrete plan wrapper contains no step")
		}
		return collector.walkStep(atc.Step{Config: config}, insideAcross, depth+1)
	}
	walkHook := func(config atc.StepConfig, hook atc.Step) error {
		if err := walkWrapped(config); err != nil {
			return err
		}
		return collector.walkStep(hook, insideAcross, depth+1)
	}

	switch config := step.Config.(type) {
	case *atc.TaskStep:
		return collector.collectOutputs("task", config.Name, config.SnapshotOutputs, insideAcross)
	case *atc.AgentStep:
		return collector.collectOutputs("agent", config.Name, config.SnapshotOutputs, insideAcross)
	case *atc.AwaitSnapshotStep:
		return collector.collectOutputs("await_snapshot", config.Name, awaitSnapshotOutputs(
			config.Name, config.Type, config.WorkflowPort, config.WorkflowDefinitionID, config.WorkflowRunID,
		), insideAcross)
	case *atc.DoStep:
		if len(config.Steps) == 0 {
			return invalidError("concrete do step is empty")
		}
		for _, child := range config.Steps {
			if err := collector.walkStep(child, insideAcross, depth+1); err != nil {
				return err
			}
		}
	case *atc.InParallelStep:
		if len(config.Config.Steps) == 0 {
			return invalidError("concrete in_parallel step is empty")
		}
		for _, child := range config.Config.Steps {
			if err := collector.walkStep(child, insideAcross, depth+1); err != nil {
				return err
			}
		}
	case *atc.AcrossStep:
		if len(config.Vars) == 0 || config.Step == nil {
			return invalidError("concrete across step is incomplete")
		}
		return collector.walkStep(atc.Step{Config: config.Step}, true, depth+1)
	case *atc.TryStep:
		return collector.walkStep(config.Step, insideAcross, depth+1)
	case *atc.RetryStep:
		if config.Attempts <= 0 {
			return invalidError("concrete retry step has invalid attempts")
		}
		return walkWrapped(config.Step)
	case *atc.TimeoutStep:
		return walkWrapped(config.Step)
	case *atc.OnSuccessStep:
		return walkHook(config.Step, config.Hook)
	case *atc.OnFailureStep:
		return walkHook(config.Step, config.Hook)
	case *atc.OnAbortStep:
		return walkHook(config.Step, config.Hook)
	case *atc.OnErrorStep:
		return walkHook(config.Step, config.Hook)
	case *atc.EnsureStep:
		return walkHook(config.Step, config.Hook)
	case *atc.GetStep, *atc.PutStep, *atc.RunStep,
		*atc.SetPipelineStep, *atc.LoadVarStep, *atc.LoadSnapshotStep, *atc.PublishSnapshotStep:
		return nil
	default:
		return invalidError("concrete plan contains unsupported step type %T", step.Config)
	}
	return nil
}

func (collector *declarationCollector) collectOutputs(
	kind, name string,
	outputs map[string]atc.SnapshotOutputConfig,
	insideAcross bool,
) error {
	if strings.TrimSpace(name) == "" {
		return invalidError("concrete %s producer has no name", kind)
	}
	for _, localPort := range sortedKeys(outputs) {
		output := outputs[localPort]
		if err := output.Validate(); err != nil {
			return invalidError("concrete %s %q output %q: %v", kind, name, localPort, err)
		}
		if output.Retention != snapshot.RetentionClassWorkflow {
			continue
		}
		if insideAcross {
			return dynamicOutputError("concrete %s %q output %q", kind, name, localPort)
		}
		collector.count++
		if collector.count > MaxWorkflowOutputs {
			return limitError("concrete config declares more than %d workflow outputs", MaxWorkflowOutputs)
		}
		parsedRunID, err := snapshot.ParseWorkflowRunID(output.WorkflowRunID)
		if err != nil {
			return invalidError("concrete output %q workflow run ID: %v", output.WorkflowPort, err)
		}
		if output.WorkflowDefinitionID != collector.input.WorkflowDefinitionID || parsedRunID != collector.input.WorkflowRunID {
			return invalidError("concrete output %q does not match the durable workflow definition/run", output.WorkflowPort)
		}
		if strings.TrimSpace(localPort) == "" {
			return invalidError("concrete %s %q has a blank local output port", kind, name)
		}
		declared := declaredOutput{
			port: output.WorkflowPort, typ: output.Type, optional: output.Optional,
			workflowDefinitionID: output.WorkflowDefinitionID, workflowRunID: parsedRunID,
			stepKind: kind, stepName: name, localOutputPort: localPort,
		}
		if previous, found := collector.outputs[declared.port]; found {
			return invalidError("concrete workflow port %q has multiple producers (%s %q and %s %q)",
				declared.port, previous.stepKind, previous.stepName, kind, name)
		}
		collector.outputs[declared.port] = declared
	}
	return nil
}

type visitState uint8

const visitActive visitState = iota + 1

type walkContext struct {
	insideAcross     bool
	retryRoot        string
	suppressResource bool
	image            *imageWalkContext
}

type imageWalkContext struct {
	kind ImageKind
	name string
}

type actualOutput struct {
	ExpectedOutput
	producer  ExpectedProducer
	retryRoot string
}

type provenanceBuilder struct {
	input         Input
	resources     map[string]ResourceDependency
	images        map[string]ImageDependency
	platformTypes map[string]struct{}
	actualOutputs map[string][]actualOutput
	outputCount   int

	pointerState  map[*atc.Plan]visitState
	planKindsByID map[string]string
	putPlansByID  map[string]putPlanIdentity
	nodes         int
}

type putPlanIdentity struct {
	name               string
	resource           string
	typ                string
	sourceIdentityHash string
}

func (builder *provenanceBuilder) walkPlan(plan *atc.Plan, context walkContext, depth int) (err error) {
	if plan == nil {
		return invalidError("actual plan contains a nil subplan")
	}
	if depth > maxTraversalDepth {
		return limitError("actual plan nesting exceeds %d", maxTraversalDepth)
	}
	if builder.pointerState[plan] == visitActive {
		return invalidError("actual plan contains a pointer cycle at plan %q", plan.ID)
	}
	builder.pointerState[plan] = visitActive
	defer delete(builder.pointerState, plan)
	builder.nodes++
	if builder.nodes > maxTraversalNodes {
		return limitError("actual plan contains more than %d nodes", maxTraversalNodes)
	}
	if strings.TrimSpace(plan.ID.String()) == "" || plan.ID.String() != strings.TrimSpace(plan.ID.String()) {
		return invalidError("actual plan contains an invalid plan ID %q", plan.ID)
	}
	for _, attempt := range plan.Attempts {
		if attempt <= 0 {
			return invalidError("plan %q has an invalid attempt number", plan.ID)
		}
	}

	kind, arms := planKind(plan)
	if arms != 1 {
		return invalidError("plan %q has %d plan types, want exactly one", plan.ID, arms)
	}
	if previous, found := builder.planKindsByID[plan.ID.String()]; found && previous != kind {
		return invalidError("plan ID %q has contradictory types %q and %q", plan.ID, previous, kind)
	}
	builder.planKindsByID[plan.ID.String()] = kind

	walk := func(child *atc.Plan, childContext walkContext) error {
		return builder.walkPlan(child, childContext, depth+1)
	}
	switch {
	case plan.Do != nil:
		if len(*plan.Do) == 0 {
			return invalidError("plan %q has an empty do step", plan.ID)
		}
		for index := range *plan.Do {
			if err := walk(&(*plan.Do)[index], context); err != nil {
				return err
			}
		}
	case plan.InParallel != nil:
		if len(plan.InParallel.Steps) == 0 {
			return invalidError("plan %q has an empty in_parallel step", plan.ID)
		}
		for index := range plan.InParallel.Steps {
			if err := walk(&plan.InParallel.Steps[index], context); err != nil {
				return err
			}
		}
	case plan.Across != nil:
		if len(plan.Across.Vars) == 0 || strings.TrimSpace(plan.Across.SubStepTemplate) == "" {
			return invalidError("plan %q has an incomplete across step", plan.ID)
		}
		template, err := decodeAcrossTemplate(plan.Across.SubStepTemplate)
		if err != nil {
			return invalidError("plan %q across template: %v", plan.ID, err)
		}
		childContext := context
		childContext.insideAcross = true
		if err := walk(&template, childContext); err != nil {
			return err
		}
	case plan.OnSuccess != nil:
		if err := walk(&plan.OnSuccess.Step, context); err != nil {
			return err
		}
		if err := walk(&plan.OnSuccess.Next, context); err != nil {
			return err
		}
	case plan.OnFailure != nil:
		if err := walk(&plan.OnFailure.Step, context); err != nil {
			return err
		}
		if err := walk(&plan.OnFailure.Next, context); err != nil {
			return err
		}
	case plan.OnAbort != nil:
		if err := walk(&plan.OnAbort.Step, context); err != nil {
			return err
		}
		if err := walk(&plan.OnAbort.Next, context); err != nil {
			return err
		}
	case plan.OnError != nil:
		if err := walk(&plan.OnError.Step, context); err != nil {
			return err
		}
		if err := walk(&plan.OnError.Next, context); err != nil {
			return err
		}
	case plan.Ensure != nil:
		if err := walk(&plan.Ensure.Step, context); err != nil {
			return err
		}
		if err := walk(&plan.Ensure.Next, context); err != nil {
			return err
		}
	case plan.Try != nil:
		if err := walk(&plan.Try.Step, context); err != nil {
			return err
		}
	case plan.Timeout != nil:
		if err := walk(&plan.Timeout.Step, context); err != nil {
			return err
		}
	case plan.Retry != nil:
		if len(*plan.Retry) == 0 {
			return invalidError("plan %q has an empty retry step", plan.ID)
		}
		childContext := context
		if childContext.retryRoot == "" {
			childContext.retryRoot = plan.ID.String()
		}
		for index := range *plan.Retry {
			if err := walk(&(*plan.Retry)[index], childContext); err != nil {
				return err
			}
		}
	case plan.Get != nil:
		if err := builder.captureGet(plan, context); err != nil {
			return err
		}
		if err := builder.walkTypeImage(plan.ID.String(), plan.Get.Type, plan.Get.TypeImage, context, depth+1); err != nil {
			return err
		}
	case plan.Put != nil:
		if err := builder.capturePut(plan); err != nil {
			return err
		}
		if err := builder.walkTypeImage(plan.ID.String(), plan.Put.Type, plan.Put.TypeImage, context, depth+1); err != nil {
			return err
		}
	case plan.Check != nil:
		if strings.TrimSpace(plan.Check.Name) == "" || strings.TrimSpace(plan.Check.Type) == "" {
			return invalidError("check plan %q has incomplete identity", plan.ID)
		}
		if err := builder.walkTypeImage(plan.ID.String(), plan.Check.Type, plan.Check.TypeImage, context, depth+1); err != nil {
			return err
		}
	case plan.Task != nil:
		if err := builder.captureTask(plan, context); err != nil {
			return err
		}
	case plan.Agent != nil:
		if strings.TrimSpace(plan.Agent.Name) == "" {
			return invalidError("agent plan %q has no name", plan.ID)
		}
		if err := builder.capturePlanOutputs(plan.ID.String(), "agent", plan.Agent.Name, plan.Agent.SnapshotOutputs, context); err != nil {
			return err
		}
		if plan.Agent.RuntimeImage != "" {
			if err := builder.addDigestImage(ImageDependency{
				PlanID: plan.ID.String(), Kind: ImageKindAgentRuntime,
				Name: plan.Agent.Name, ImageRef: plan.Agent.RuntimeImage,
			}); err != nil {
				return err
			}
		}
		if err := builder.captureSidecars(plan.ID.String(), plan.Agent.Sidecars); err != nil {
			return err
		}
	case plan.Run != nil:
		if strings.TrimSpace(plan.Run.Type) == "" {
			return invalidError("run plan %q has no prototype type", plan.ID)
		}
		// RunPlan does not retain the resolved prototype image/source identity.
		// Accepting it would fabricate provenance from only a logical name.
		return invalidError("run plan %q does not contain immutable prototype provenance", plan.ID)
	case plan.LoadSnapshot != nil:
		if err := plan.LoadSnapshot.Type.Validate(); err != nil {
			return invalidError("load_snapshot plan %q: %v", plan.ID, err)
		}
		if plan.LoadSnapshot.WorkflowRunID != "" {
			runID, err := snapshot.ParseWorkflowRunID(plan.LoadSnapshot.WorkflowRunID)
			if err != nil || runID != builder.input.WorkflowRunID {
				return invalidError("load_snapshot plan %q has invalid workflow run identity", plan.ID)
			}
		}
	case plan.AwaitSnapshot != nil:
		if plan.AwaitSnapshot.Type != snapshot.TypeRef("human-answer/v1") {
			return invalidError("await_snapshot plan %q has invalid output type", plan.ID)
		}
		runID, err := snapshot.ParseWorkflowRunID(plan.AwaitSnapshot.WorkflowRunID)
		if err != nil || runID != builder.input.WorkflowRunID {
			return invalidError("await_snapshot plan %q has invalid workflow run identity", plan.ID)
		}
		if err := builder.capturePlanOutputs(plan.ID.String(), "await_snapshot", plan.AwaitSnapshot.Name, awaitSnapshotOutputs(
			plan.AwaitSnapshot.Name, plan.AwaitSnapshot.Type, plan.AwaitSnapshot.WorkflowPort,
			plan.AwaitSnapshot.WorkflowDefinitionID, plan.AwaitSnapshot.WorkflowRunID,
		), context); err != nil {
			return err
		}
	case plan.PublishSnapshot != nil:
		if err := validatePublishSnapshotPlan(plan.PublishSnapshot); err != nil {
			return invalidError("publish_snapshot plan %q: %v", plan.ID, err)
		}
		if plan.PublishSnapshot.Mode == publisher.ModeMerge {
			runID, err := snapshot.ParseWorkflowRunID(plan.PublishSnapshot.WorkflowRunID)
			if err != nil || runID != builder.input.WorkflowRunID {
				return invalidError("publish_snapshot plan %q has invalid workflow run identity", plan.ID)
			}
		}
	case plan.Sidecar != nil:
		if err := builder.addDigestImage(ImageDependency{
			PlanID: plan.ID.String(), Kind: ImageKindCapabilitySidecar,
			Name: plan.Sidecar.Name, ImageRef: plan.Sidecar.Image,
		}); err != nil {
			return err
		}
	case plan.DependentGet != nil:
		return invalidError("plan %q uses deprecated dependent_get without an exact version", plan.ID)
	case plan.SetPipeline != nil, plan.LoadVar != nil,
		plan.ArtifactInput != nil, plan.ArtifactOutput != nil:
		return nil
	default:
		return invalidError("plan %q contains an unsupported future construct", plan.ID)
	}
	return nil
}

func planKind(plan *atc.Plan) (string, int) {
	typed := []struct {
		name    string
		present bool
	}{
		{"get", plan.Get != nil}, {"put", plan.Put != nil}, {"check", plan.Check != nil},
		{"task", plan.Task != nil}, {"run", plan.Run != nil}, {"agent", plan.Agent != nil},
		{"set_pipeline", plan.SetPipeline != nil},
		{"load_var", plan.LoadVar != nil}, {"load_snapshot", plan.LoadSnapshot != nil},
		{"await_snapshot", plan.AwaitSnapshot != nil},
		{"publish_snapshot", plan.PublishSnapshot != nil},
		{"do", plan.Do != nil}, {"in_parallel", plan.InParallel != nil}, {"across", plan.Across != nil},
		{"on_success", plan.OnSuccess != nil}, {"on_failure", plan.OnFailure != nil},
		{"on_abort", plan.OnAbort != nil}, {"on_error", plan.OnError != nil}, {"ensure", plan.Ensure != nil},
		{"try", plan.Try != nil}, {"timeout", plan.Timeout != nil}, {"retry", plan.Retry != nil},
		{"artifact_input", plan.ArtifactInput != nil}, {"artifact_output", plan.ArtifactOutput != nil},
		{"sidecar", plan.Sidecar != nil}, {"dependent_get", plan.DependentGet != nil},
	}
	kind := ""
	count := 0
	for _, candidate := range typed {
		if candidate.present {
			kind = candidate.name
			count++
		}
	}
	return kind, count
}

func validatePublishSnapshotPlan(plan *atc.PublishSnapshotPlan) error {
	if plan == nil || strings.TrimSpace(plan.Name) == "" || strings.TrimSpace(plan.Input) == "" {
		return fmt.Errorf("name and input are required")
	}
	if plan.Mode == publisher.ModeMerge {
		if strings.TrimSpace(plan.Approval) == "" || strings.TrimSpace(plan.WorkflowRunID) == "" {
			return fmt.Errorf("merge approval and workflow run identity are required")
		}
	} else if plan.Approval != "" || plan.WorkflowRunID != "" {
		return fmt.Errorf("approval linkage requires merge")
	}
	request := publisher.Request{
		Publisher: plan.Publisher,
		Input: snapshot.SnapshotRef{
			ID: 1, Type: plan.InputType,
			Digest: snapshot.Digest("sha256:" + strings.Repeat("0", sha256.Size*2)),
		},
		Destination: plan.Destination, Mode: plan.Mode, Parameters: plan.Parameters,
		ApprovalPolicyVersion: plan.ApprovalPolicyVersion,
		// Provenance validates immutable plan shape; execution replaces this
		// sentinel with authenticated build identity before acquisition.
		Authority: publisher.Authority{TeamID: 1, TeamName: "server-verified", BuildID: 1, Actor: "server-verified"},
	}
	if plan.Mode == publisher.ModeMerge {
		// The merge base assertion is derived from the bound
		// repository-change/v1 snapshot at execution time, so an immutable plan
		// never carries it and must never author it. Stand a probe in to
		// validate the rest of the publication shape.
		probed, err := publisher.ProbeMergeBase(plan.Parameters)
		if err != nil {
			return err
		}
		request.Parameters = probed
		// Runtime approval comes only from the server. This placeholder lets the
		// provenance verifier validate the immutable publication shape without
		// representing an authored approval actor.
		request.ApprovedBy = "server-verified"
		request.Approval = &publisher.ApprovalEvidence{
			WaitID:     1,
			Question:   snapshot.SnapshotRef{ID: 2, Type: "question/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("1", 64))},
			Answer:     snapshot.SnapshotRef{ID: 3, Type: "human-answer/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("2", 64))},
			ResolvedBy: "server-verified", ResolvedAt: time.Unix(1, 0).UTC(),
		}
	}
	return request.Validate()
}

func (builder *provenanceBuilder) captureGet(plan *atc.Plan, context walkContext) error {
	get := plan.Get
	if strings.TrimSpace(get.Name) == "" || strings.TrimSpace(get.Type) == "" {
		return invalidError("get plan %q has incomplete identity", plan.ID)
	}
	if get.Version != nil && get.VersionFrom != nil {
		return invalidError("get plan %q has both version and version_from", plan.ID)
	}
	if context.image != nil {
		if get.Version == nil || len(*get.Version) == 0 || get.VersionFrom != nil {
			return invalidError("custom resource type image %q is not resolved to an exact version", context.image.name)
		}
		hash, err := hashSource(get.Source)
		if err != nil {
			return err
		}
		return builder.addImage(ImageDependency{
			PlanID: plan.ID.String(), Kind: context.image.kind, Name: context.image.name,
			Type: get.Type, Version: cloneVersion(*get.Version), SourceIdentityHash: hash,
		})
	}
	if context.suppressResource {
		return nil
	}
	if get.VersionFrom != nil {
		put, found := builder.putPlansByID[get.VersionFrom.String()]
		if !found {
			return invalidError("get plan %q references unknown put plan %q", plan.ID, *get.VersionFrom)
		}
		sourceHash, err := hashSource(get.Source)
		if err != nil {
			return err
		}
		actual := putPlanIdentity{
			name: get.Name, resource: get.Resource, typ: get.Type, sourceIdentityHash: sourceHash,
		}
		if actual != put {
			return invalidError("get plan %q does not match referenced put plan %q", plan.ID, *get.VersionFrom)
		}
		// The version is produced by the visible put in this same immutable plan,
		// rather than selected from an external resource version set. Preserve the
		// version_from edge in the canonical plan, but do not misclassify it as an
		// independently frozen input dependency.
		return nil
	}
	if get.Version == nil || len(*get.Version) == 0 || strings.TrimSpace(get.Resource) == "" {
		return invalidError("resource get plan %q lacks an exact selected version or resource name", plan.ID)
	}
	hash, err := hashSource(get.Source)
	if err != nil {
		return err
	}
	return builder.addResource(ResourceDependency{
		PlanID: plan.ID.String(), Name: get.Name, Resource: get.Resource, Type: get.Type,
		Version: cloneVersion(*get.Version), SourceIdentityHash: hash,
	})
}

func (builder *provenanceBuilder) capturePut(plan *atc.Plan) error {
	put := plan.Put
	if strings.TrimSpace(put.Name) == "" || strings.TrimSpace(put.Type) == "" {
		return invalidError("put plan %q has incomplete identity", plan.ID)
	}
	sourceHash, err := hashSource(put.Source)
	if err != nil {
		return err
	}
	identity := putPlanIdentity{
		name: put.Name, resource: put.Resource, typ: put.Type, sourceIdentityHash: sourceHash,
	}
	if previous, found := builder.putPlansByID[plan.ID.String()]; found && previous != identity {
		return invalidError("put plan %q has contradictory identities", plan.ID)
	}
	builder.putPlansByID[plan.ID.String()] = identity
	return nil
}

func (builder *provenanceBuilder) walkTypeImage(
	ownerPlanID, ownerType string,
	image atc.TypeImage,
	context walkContext,
	depth int,
) error {
	hasBase := strings.TrimSpace(image.BaseType) != ""
	hasRef := strings.TrimSpace(image.ImageRef) != ""
	hasGet := image.GetPlan != nil
	hasCheck := image.CheckPlan != nil
	if !hasBase && !hasRef && !hasGet && !hasCheck {
		return invalidError("plan %q type %q has no resource type image identity", ownerPlanID, ownerType)
	}
	if hasRef && (hasGet || hasCheck || hasBase) {
		return invalidError("plan %q type %q has contradictory direct and planned image identities", ownerPlanID, ownerType)
	}
	if hasCheck && !hasGet {
		return invalidError("plan %q type %q has an image check without an image get", ownerPlanID, ownerType)
	}
	if hasBase {
		builder.platformTypes[image.BaseType] = struct{}{}
	}
	if hasRef {
		return builder.addDigestImage(ImageDependency{
			PlanID: ownerPlanID, Kind: ImageKindCustomResourceType,
			Name: ownerType, ImageRef: image.ImageRef,
		})
	}
	if hasCheck {
		checkContext := context
		checkContext.suppressResource = true
		checkContext.image = nil
		if err := builder.walkPlan(image.CheckPlan, checkContext, depth); err != nil {
			return err
		}
	}
	if hasGet {
		getContext := context
		getContext.suppressResource = true
		getContext.image = &imageWalkContext{kind: ImageKindCustomResourceType, name: ownerType}
		if err := builder.walkPlan(image.GetPlan, getContext, depth); err != nil {
			return err
		}
	}
	return nil
}

func (builder *provenanceBuilder) captureTask(plan *atc.Plan, context walkContext) error {
	task := plan.Task
	if strings.TrimSpace(task.Name) == "" {
		return invalidError("task plan %q has no name", plan.ID)
	}
	if err := builder.capturePlanOutputs(plan.ID.String(), "task", task.Name, task.SnapshotOutputs, context); err != nil {
		return err
	}
	if task.ConfigPath != "" || task.Config == nil || task.ImageArtifactName != "" {
		return invalidError("task plan %q does not contain an inline immutable task image", plan.ID)
	}
	if task.Config.RootfsURI != "" || task.Config.ImageResource == nil {
		return invalidError("task plan %q does not contain an immutable image_resource", plan.ID)
	}
	image := task.Config.ImageResource
	if len(image.Version) == 0 {
		return invalidError("task plan %q image is not resolved to an exact version", plan.ID)
	}
	hash, err := hashSource(image.Source)
	if err != nil {
		return err
	}
	if err := builder.addImage(ImageDependency{
		PlanID: plan.ID.String(), Kind: ImageKindTask, Name: task.Name,
		Type: image.Type, Version: cloneVersion(image.Version), SourceIdentityHash: hash,
	}); err != nil {
		return err
	}
	resourceType, custom, err := exactResourceType(task.ResourceTypes, image.Type)
	if err != nil {
		return err
	}
	if custom {
		if strings.TrimSpace(resourceType.Image) == "" {
			return invalidError("task plan %q image resource type %q requires a mutable check chain", plan.ID, image.Type)
		}
		if err := builder.addDigestImage(ImageDependency{
			PlanID: plan.ID.String(), Kind: ImageKindCustomResourceType,
			Name: resourceType.Name, ImageRef: resourceType.Image,
		}); err != nil {
			return err
		}
	} else {
		if strings.TrimSpace(image.Type) == "" {
			return invalidError("task plan %q image resource type is blank", plan.ID)
		}
		builder.platformTypes[image.Type] = struct{}{}
	}
	return builder.captureSidecars(plan.ID.String(), task.Sidecars)
}

func exactResourceType(types atc.ResourceTypes, name string) (atc.ResourceType, bool, error) {
	var selected atc.ResourceType
	found := false
	for _, resourceType := range types {
		if resourceType.Name != name {
			continue
		}
		if found && !reflect.DeepEqual(selected, resourceType) {
			return atc.ResourceType{}, false, invalidError("resource type %q has contradictory definitions", name)
		}
		selected = resourceType
		found = true
	}
	return selected, found, nil
}

func (builder *provenanceBuilder) captureSidecars(planID string, sidecars []atc.SidecarSource) error {
	for index, source := range sidecars {
		if source.File != "" || source.Config == nil || source.Config.ImageArtifact != "" {
			return invalidError("plan %q sidecar[%d] is not a literal immutable image", planID, index)
		}
		if err := builder.addDigestImage(ImageDependency{
			PlanID: planID, Kind: ImageKindCapabilitySidecar,
			Name: source.Config.Name, ImageRef: source.Config.Image,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (builder *provenanceBuilder) capturePlanOutputs(
	planID, kind, name string,
	outputs map[string]atc.SnapshotOutputConfig,
	context walkContext,
) error {
	for _, localPort := range sortedKeys(outputs) {
		output := outputs[localPort]
		if err := output.Validate(); err != nil {
			return invalidError("plan %q %s output %q: %v", planID, kind, localPort, err)
		}
		if output.Retention != snapshot.RetentionClassWorkflow {
			continue
		}
		if context.insideAcross {
			return dynamicOutputError("plan %q %s output %q", planID, kind, localPort)
		}
		builder.outputCount++
		if builder.outputCount > MaxWorkflowOutputs {
			return limitError("actual plan declares more than %d workflow outputs", MaxWorkflowOutputs)
		}
		runID, err := snapshot.ParseWorkflowRunID(output.WorkflowRunID)
		if err != nil {
			return invalidError("plan %q workflow output %q run ID: %v", planID, output.WorkflowPort, err)
		}
		if strings.TrimSpace(localPort) == "" {
			return invalidError("plan %q has a blank local workflow output", planID)
		}
		producer := ExpectedProducer{PlanID: planID, StepKind: kind, StepName: name, LocalOutputPort: localPort}
		builder.actualOutputs[output.WorkflowPort] = append(builder.actualOutputs[output.WorkflowPort], actualOutput{
			ExpectedOutput: ExpectedOutput{
				Port: output.WorkflowPort, Type: output.Type, Optional: output.Optional,
				WorkflowDefinitionID: output.WorkflowDefinitionID, WorkflowRunID: runID,
			},
			producer: producer, retryRoot: context.retryRoot,
		})
	}
	return nil
}

func awaitSnapshotOutputs(
	name string,
	typ snapshot.TypeRef,
	workflowPort string,
	workflowDefinitionID int,
	workflowRunID string,
) map[string]atc.SnapshotOutputConfig {
	if workflowPort == "" {
		return nil
	}
	return map[string]atc.SnapshotOutputConfig{name: {
		Type: typ, Retention: snapshot.RetentionClassWorkflow, WorkflowPort: workflowPort,
		WorkflowDefinitionID: workflowDefinitionID, WorkflowRunID: workflowRunID,
	}}
}

func compareOutputContracts(
	declared map[string]declaredOutput,
	actual map[string][]actualOutput,
) ([]ExpectedOutput, error) {
	if len(declared) != len(actual) {
		return nil, contractError("concrete config declares %d public ports but actual plan declares %d", len(declared), len(actual))
	}
	ports := make([]string, 0, len(declared))
	for port := range declared {
		ports = append(ports, port)
	}
	sort.Strings(ports)
	outputs := make([]ExpectedOutput, 0, len(ports))
	for _, port := range ports {
		expected := declared[port]
		occurrences, found := actual[port]
		if !found || len(occurrences) == 0 {
			return nil, contractError("public port %q is absent from the actual plan", port)
		}
		byPlanID := map[string]actualOutput{}
		for _, occurrence := range occurrences {
			if occurrence.Port != expected.port || occurrence.Type != expected.typ ||
				occurrence.Optional != expected.optional ||
				occurrence.WorkflowDefinitionID != expected.workflowDefinitionID ||
				occurrence.WorkflowRunID != expected.workflowRunID ||
				occurrence.producer.StepKind != expected.stepKind ||
				occurrence.producer.StepName != expected.stepName ||
				occurrence.producer.LocalOutputPort != expected.localOutputPort {
				return nil, contractError("public port %q actual producer or typed annotation differs from concrete config", port)
			}
			if previous, duplicate := byPlanID[occurrence.producer.PlanID]; duplicate {
				if previous.producer != occurrence.producer || previous.retryRoot != occurrence.retryRoot {
					return nil, contractError("public port %q has contradictory producer plan ID %q", port, occurrence.producer.PlanID)
				}
				continue
			}
			byPlanID[occurrence.producer.PlanID] = occurrence
		}
		unique := make([]actualOutput, 0, len(byPlanID))
		for _, occurrence := range byPlanID {
			unique = append(unique, occurrence)
		}
		if len(unique) > 1 {
			retryRoot := unique[0].retryRoot
			if retryRoot == "" {
				return nil, contractError("public port %q has multiple producers outside retry", port)
			}
			for _, occurrence := range unique[1:] {
				if occurrence.retryRoot != retryRoot {
					return nil, contractError("public port %q has producers from different retry scopes", port)
				}
			}
		}
		producers := make([]ExpectedProducer, 0, len(unique))
		for _, occurrence := range unique {
			producers = append(producers, occurrence.producer)
		}
		sort.Slice(producers, func(left, right int) bool {
			if producers[left].PlanID != producers[right].PlanID {
				return producers[left].PlanID < producers[right].PlanID
			}
			if producers[left].StepKind != producers[right].StepKind {
				return producers[left].StepKind < producers[right].StepKind
			}
			if producers[left].StepName != producers[right].StepName {
				return producers[left].StepName < producers[right].StepName
			}
			return producers[left].LocalOutputPort < producers[right].LocalOutputPort
		})
		outputs = append(outputs, ExpectedOutput{
			Port: expected.port, Type: expected.typ, Optional: expected.optional,
			WorkflowDefinitionID: expected.workflowDefinitionID, WorkflowRunID: expected.workflowRunID,
			Producers: producers,
		})
	}
	return outputs, nil
}

func (builder *provenanceBuilder) addResource(record ResourceDependency) error {
	key := record.PlanID
	if previous, found := builder.resources[key]; found {
		if !reflect.DeepEqual(previous, record) {
			return invalidError("resource dependency plan %q has contradictory identities", record.PlanID)
		}
		return nil
	}
	builder.resources[key] = record
	return builder.checkDependencyCount()
}

func (builder *provenanceBuilder) addDigestImage(record ImageDependency) error {
	if strings.TrimSpace(record.Name) == "" || strings.TrimSpace(record.ImageRef) == "" {
		return invalidError("plan %q has an incomplete %s image identity", record.PlanID, record.Kind)
	}
	if err := (atc.SidecarConfig{Name: "provenance-image", Image: record.ImageRef}).ValidateCapability(); err != nil {
		return invalidError("plan %q image %q is not pinned to an exact digest: %v", record.PlanID, record.Name, err)
	}
	return builder.addImage(record)
}

func (builder *provenanceBuilder) addImage(record ImageDependency) error {
	if err := record.Kind.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(record.PlanID) == "" || strings.TrimSpace(record.Name) == "" {
		return invalidError("image dependency has incomplete identity")
	}
	hasRef := record.ImageRef != ""
	hasResourceIdentity := record.Type != "" && len(record.Version) > 0 && record.SourceIdentityHash != ""
	if hasRef == hasResourceIdentity {
		return invalidError("image dependency %s/%s must have exactly one immutable identity form", record.PlanID, record.Name)
	}
	key := string(record.Kind) + "\x00" + record.PlanID + "\x00" + record.Name
	if previous, found := builder.images[key]; found {
		if !reflect.DeepEqual(previous, record) {
			return invalidError("image dependency %s/%s has contradictory identities", record.PlanID, record.Name)
		}
		return nil
	}
	builder.images[key] = record
	return builder.checkDependencyCount()
}

func (builder *provenanceBuilder) checkDependencyCount() error {
	count := len(builder.resources) + len(builder.images) + len(builder.platformTypes)
	if count > MaxDependencyRecords {
		return limitError("dependency envelope contains more than %d records", MaxDependencyRecords)
	}
	return nil
}

func (builder *provenanceBuilder) dependencyEnvelope() DependencyEnvelope {
	envelope := DependencyEnvelope{
		Version:               dependencyEnvelopeVersion,
		Resources:             make([]ResourceDependency, 0, len(builder.resources)),
		Images:                make([]ImageDependency, 0, len(builder.images)),
		PlatformResourceTypes: make([]string, 0, len(builder.platformTypes)),
	}
	for _, dependency := range builder.resources {
		envelope.Resources = append(envelope.Resources, dependency)
	}
	for _, dependency := range builder.images {
		envelope.Images = append(envelope.Images, dependency)
	}
	for typ := range builder.platformTypes {
		envelope.PlatformResourceTypes = append(envelope.PlatformResourceTypes, typ)
	}
	sort.Slice(envelope.Resources, func(left, right int) bool {
		if envelope.Resources[left].PlanID != envelope.Resources[right].PlanID {
			return envelope.Resources[left].PlanID < envelope.Resources[right].PlanID
		}
		if envelope.Resources[left].Name != envelope.Resources[right].Name {
			return envelope.Resources[left].Name < envelope.Resources[right].Name
		}
		return envelope.Resources[left].Resource < envelope.Resources[right].Resource
	})
	sort.Slice(envelope.Images, func(left, right int) bool {
		if envelope.Images[left].Kind != envelope.Images[right].Kind {
			return envelope.Images[left].Kind < envelope.Images[right].Kind
		}
		if envelope.Images[left].PlanID != envelope.Images[right].PlanID {
			return envelope.Images[left].PlanID < envelope.Images[right].PlanID
		}
		return envelope.Images[left].Name < envelope.Images[right].Name
	})
	sort.Strings(envelope.PlatformResourceTypes)
	return envelope
}

func decodeAcrossTemplate(raw string) (atc.Plan, error) {
	return decodeActualPlan(json.RawMessage(raw))
}

func decodeActualPlan(raw json.RawMessage) (atc.Plan, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var plan atc.Plan
	if err := decoder.Decode(&plan); err != nil {
		return atc.Plan{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return atc.Plan{}, err
	}
	return plan, nil
}

func decodeDependencyEnvelope(raw json.RawMessage) (DependencyEnvelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope DependencyEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return DependencyEnvelope{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return DependencyEnvelope{}, err
	}
	return envelope, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func hashSource(source atc.Source) (string, error) {
	canonical, err := atc.CanonicalJSON(source)
	if err != nil {
		return "", invalidError("canonicalize dependency source identity: %v", err)
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(sourceHashDomain))
	_, _ = hasher.Write(canonical)
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func cloneVersion(version atc.Version) atc.Version {
	cloned := make(atc.Version, len(version))
	for key, value := range version {
		cloned[key] = value
	}
	return cloned
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func invalidError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidProvenance, fmt.Sprintf(format, args...))
}

func contractError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrOutputContractMismatch, fmt.Sprintf(format, args...))
}

func dynamicOutputError(format string, args ...any) error {
	return errors.Join(ErrInvalidProvenance, ErrDynamicWorkflowOutput, fmt.Errorf(format, args...))
}

func limitError(format string, args ...any) error {
	return errors.Join(ErrInvalidProvenance, ErrLimitExceeded, fmt.Errorf(format, args...))
}
