package workflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
	"github.com/mitchellh/copystructure"
)

const targetConfigHashDomain = "workflow-target-config/v1\x00"

type TargetKind string

const (
	TargetWorkflow TargetKind = "workflow"
	TargetFunction TargetKind = "function"
)

type FunctionTarget struct {
	Kind                        TargetKind
	WorkflowDefinitionID        int
	WorkflowName                string
	WorkflowVersion             int
	SignatureVersion            int
	FunctionID                  string
	Signature                   PublicSignature
	Function                    FunctionConfig
	DevValidationProvenanceHash string
}

type RenderedFunction struct {
	TemplateName                string
	TargetSignature             PublicSignature
	Config                      atc.Config
	TargetConfigHash            string
	InputParamNames             map[string]string
	DevValidationProfiles       []CompiledDevValidationProfile
	DevValidationProvenanceHash string
	BrokerProfiles              []CompiledBrokerProfile
	BrokerProfileProvenanceHash string
	boundSourceRefs             map[string]snapshot.SnapshotRef
	executionCanonicalConfig    []byte
	executionTargetConfigHash   string
	templateKind                TargetKind
	templateWorkflowName        string
	templateWorkflowVersion     int
	templateFunctionID          string
}

// ExecutionEnvelope is the opaque launch artifact produced by a trusted
// render. Its private canonical configuration and bound source references are
// intentionally unavailable to callers submitting a workflow run.
type ExecutionEnvelope struct {
	canonicalConfig  []byte
	targetConfigHash string
	params           map[string]any
}

// BindExecutionParams is the only source-aware execution envelope. It owns
// every rendered resource-source parameter and refuses caller substitution;
// selected IDs therefore survive rendering as server-derived execution state.
func (rendered RenderedFunction) BindExecutionParams(params map[string]any) (map[string]any, error) {
	bound := make(map[string]any, len(params)+len(rendered.boundSourceRefs))
	for name, value := range params {
		bound[name] = value
	}
	for source, ref := range rendered.boundSourceRefs {
		name, err := InputParamName(source)
		if err != nil {
			return nil, err
		}
		value := ref.ID.String()
		if supplied, found := bound[name]; found && supplied != value {
			return nil, fmt.Errorf("workflow: bound resource source %q was substituted", source)
		}
		bound[name] = value
	}
	return bound, nil
}

// ExecutionEnvelope binds caller parameters to this exact render. It refuses
// zero or manually constructed RenderedFunctions so a source-bearing launch
// cannot replace the trusted render with a bare config and params map.
func (rendered RenderedFunction) ExecutionEnvelope(params map[string]any) (ExecutionEnvelope, error) {
	if len(rendered.executionCanonicalConfig) == 0 || rendered.executionTargetConfigHash == "" {
		return ExecutionEnvelope{}, fmt.Errorf("workflow: rendered function has no trusted execution authority")
	}
	bound, err := rendered.BindExecutionParams(params)
	if err != nil {
		return ExecutionEnvelope{}, err
	}
	cloned, err := cloneExecutionParams(bound)
	if err != nil {
		return ExecutionEnvelope{}, err
	}
	return ExecutionEnvelope{
		canonicalConfig:  append([]byte(nil), rendered.executionCanonicalConfig...),
		targetConfigHash: rendered.executionTargetConfigHash,
		params:           cloned,
	}, nil
}

// ParamsFor opens a trusted launch envelope only when the locked durable
// workflow config and its immutable template identity are exactly the render
// that created it. The returned params are always a defensive copy.
func (envelope ExecutionEnvelope) ParamsFor(canonicalConfig []byte, targetConfigHash string) (map[string]any, error) {
	if len(envelope.canonicalConfig) == 0 || envelope.targetConfigHash == "" || envelope.params == nil ||
		!bytes.Equal(envelope.canonicalConfig, canonicalConfig) || envelope.targetConfigHash != targetConfigHash {
		return nil, fmt.Errorf("workflow: execution envelope does not match durable workflow authority")
	}
	return cloneExecutionParams(envelope.params)
}

// Params returns a defensive parameter copy for adapters that observe a
// launch request. It cannot authorize a durable launch; that requires
// ParamsFor's canonical config and immutable hash check at the DB seam.
func (envelope ExecutionEnvelope) Params() (map[string]any, error) {
	if len(envelope.canonicalConfig) == 0 || envelope.targetConfigHash == "" || envelope.params == nil {
		return nil, fmt.Errorf("workflow: invalid execution envelope")
	}
	return cloneExecutionParams(envelope.params)
}

func cloneExecutionParams(params map[string]any) (map[string]any, error) {
	cloned, err := copystructure.Copy(params)
	if err != nil {
		return nil, fmt.Errorf("workflow: clone execution params: %w", err)
	}
	if cloned == nil {
		return nil, nil
	}
	return cloned.(map[string]any), nil
}

// Clone preserves private execution authority that reflection-based cloning
// drops, while retaining the public declarative render for preflight callers.
func (rendered RenderedFunction) Clone() (RenderedFunction, error) {
	cloned, err := copystructure.Copy(rendered)
	if err != nil {
		return RenderedFunction{}, err
	}
	value := cloned.(RenderedFunction)
	value.boundSourceRefs = cloneBoundSourceRefs(rendered.boundSourceRefs)
	value.executionCanonicalConfig = append([]byte(nil), rendered.executionCanonicalConfig...)
	value.executionTargetConfigHash = rendered.executionTargetConfigHash
	value.templateKind = rendered.templateKind
	value.templateWorkflowName = rendered.templateWorkflowName
	value.templateWorkflowVersion = rendered.templateWorkflowVersion
	value.templateFunctionID = rendered.templateFunctionID
	return value, nil
}

func (rendered *RenderedFunction) refreshExecutionAuthority() error {
	hash, err := RenderedTargetConfigHashWithBrokerProfiles(rendered.Config, rendered.DevValidationProfiles, rendered.DevValidationProvenanceHash, rendered.BrokerProfiles, rendered.BrokerProfileProvenanceHash)
	if err != nil {
		return err
	}
	if hash != rendered.TargetConfigHash {
		return fmt.Errorf("workflow: rendered target hash does not match execution config")
	}
	canonical, err := rendered.Config.CanonicalJSON()
	if err != nil {
		return fmt.Errorf("workflow: canonicalize execution config: %w", err)
	}
	rendered.executionCanonicalConfig = append([]byte(nil), canonical...)
	rendered.executionTargetConfigHash = hash
	return nil
}

// TargetConfigHash returns the canonical, domain-separated hash used to bind
// an immutable workflow template to its parameterized Concourse config.
func TargetConfigHash(config atc.Config) (string, error) {
	canonical, err := config.CanonicalJSON()
	if err != nil {
		return "", fmt.Errorf("workflow: canonicalize target config: %w", err)
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(targetConfigHashDomain))
	_, _ = hasher.Write(canonical)
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// renderedTargetConfigHash preserves the historical config-only identity for
// ordinary functions. Validation-bearing functions use a distinct domain so
// their frozen authority cannot be separated from the rendered template.
// RenderedTargetConfigHash is the durable template identity. It preserves the
// legacy config-only vector when no authority exists, and otherwise binds the
// canonical config to the exact rendered authority without placing that
// authority in the public ATC plan.
func RenderedTargetConfigHash(config atc.Config, profiles []CompiledDevValidationProfile, provenance string) (string, error) {
	return RenderedTargetConfigHashWithBrokerProfiles(config, profiles, provenance, nil, "")
}

// RenderedTargetConfigHashWithBrokerProfiles binds a rendered Concourse
// template to both kinds of compiled-only authority. Broker profiles are not
// public plan fields, so they must be included explicitly rather than relying
// on the ATC config canonicalization alone.
func RenderedTargetConfigHashWithBrokerProfiles(config atc.Config, profiles []CompiledDevValidationProfile, provenance string, brokerProfiles []CompiledBrokerProfile, brokerProvenance string) (string, error) {
	if len(profiles) == 0 {
		if provenance != "" {
			return "", fmt.Errorf("workflow: validation provenance requires rendered profiles")
		}
	} else if err := ValidateDevValidationAuthority(profiles, provenance); err != nil {
		return "", err
	}
	if err := validateCompiledBrokerProfiles(brokerProfiles, brokerProvenance); err != nil {
		return "", err
	}
	for jobIndex := range config.Jobs {
		for stepIndex := range config.Jobs[jobIndex].PlanSequence {
			if err := config.Jobs[jobIndex].PlanSequence[stepIndex].Config.Visit(atc.StepRecursor{OnAgent: func(step *atc.AgentStep) error {
				if err := validateRenderedBrokerAuthority(step.FunctionID, step.BrokerAuthority); err != nil {
					return err
				}
				expected, err := renderBrokerAuthority(step.FunctionID, brokerProfiles)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(step.BrokerAuthority, expected) {
					return fmt.Errorf("workflow: agent %q broker authority does not match compiled profile authority", step.Name)
				}
				return nil
			}}); err != nil {
				return "", err
			}
		}
	}
	if len(profiles) == 0 && len(brokerProfiles) == 0 {
		return TargetConfigHash(config)
	}
	canonical, err := config.CanonicalJSON()
	if err != nil {
		return "", fmt.Errorf("workflow: canonicalize target config: %w", err)
	}
	authority, err := json.Marshal(struct {
		Profiles         []CompiledDevValidationProfile `json:"profiles"`
		Provenance       string                         `json:"provenance"`
		BrokerProfiles   []CompiledBrokerProfile        `json:"broker_profiles"`
		BrokerProvenance string                         `json:"broker_provenance"`
	}{profiles, provenance, brokerProfiles, brokerProvenance})
	if err != nil {
		return "", fmt.Errorf("workflow: canonicalize validation target authority: %w", err)
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("workflow-target-config-with-dev-validation/v1\x00"))
	_, _ = hasher.Write(canonical)
	_, _ = hasher.Write(authority)
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// InputParamName derives the only template parameter name valid for a public
// snapshot input. It is exported so durable admission retries can reconstruct
// parameters without re-resolving or re-rendering a workflow definition.
func InputParamName(portName string) (string, error) {
	if err := validateSafeIdentifier(portName, "public input"); err != nil {
		return "", err
	}
	paramName := "snapshot_" + portName
	if err := validateSafeIdentifier(paramName, "derived snapshot parameter"); err != nil {
		return "", err
	}
	return paramName, nil
}

// TemplateName reconstructs the deterministic template name from durable
// target identity and the verified full target-config hash.
func TemplateName(kind TargetKind, workflowName string, workflowVersion int, functionID, targetHash string) (string, error) {
	if err := validateSafeIdentifier(workflowName, "workflow name"); err != nil {
		return "", err
	}
	if workflowVersion <= 0 {
		return "", fmt.Errorf("workflow: target version must be positive")
	}
	if len(targetHash) != sha256.Size*2 || targetHash != strings.ToLower(targetHash) {
		return "", fmt.Errorf("workflow: target config hash must be lower-case 64-hex")
	}
	if _, err := hex.DecodeString(targetHash); err != nil {
		return "", fmt.Errorf("workflow: target config hash must be lower-case 64-hex: %w", err)
	}

	var name string
	switch kind {
	case TargetWorkflow:
		if functionID != "" {
			return "", fmt.Errorf("workflow: workflow target must not carry a function ID")
		}
		name = fmt.Sprintf("agent-workflow-%s-v%d-%s", workflowName, workflowVersion, targetHash[:12])
	case TargetFunction:
		if strings.TrimSpace(functionID) == "" {
			return "", fmt.Errorf("workflow: function target requires a function ID")
		}
		if err := validateSafeIdentifier(functionID, "function ID"); err != nil {
			return "", err
		}
		name = fmt.Sprintf("agent-function-%s-v%d-%s-%s", workflowName, workflowVersion, functionID, targetHash[:12])
	default:
		return "", fmt.Errorf("workflow: unsupported target kind %q", kind)
	}
	if err := validateSafeIdentifier(name, "generated template name"); err != nil {
		return "", err
	}
	return name, nil
}

func FullFunctionTarget(definition Definition) (FunctionTarget, error) {
	function, signature, err := cloneValidatedDefinitionFunction(definition)
	if err != nil {
		return FunctionTarget{}, err
	}
	if err := validateRenderableFunction(function, signature, definition.ID); err != nil {
		return FunctionTarget{}, err
	}
	if err := AnnotatePublicOutputs(function, definition.ID); err != nil {
		return FunctionTarget{}, fmt.Errorf("workflow: prepare full target: %w", err)
	}
	return FunctionTarget{
		Kind:                        TargetWorkflow,
		WorkflowDefinitionID:        definition.ID,
		WorkflowName:                definition.Name,
		WorkflowVersion:             definition.Version,
		SignatureVersion:            definition.SignatureVersion,
		Signature:                   clonePublicSignature(signature),
		Function:                    *function,
		DevValidationProvenanceHash: function.DevValidationProvenanceHash,
	}, nil
}

func RenderFunction(target FunctionTarget) (RenderedFunction, error) {
	return renderFunction(target, nil, false)
}

// RenderFunctionWithRuntimeImage is the workflow-owned trusted rendering path
// for executable targets. Callers cannot re-finalize a public Config after it
// returns, so private launch authority remains bound to this exact render.
func RenderFunctionWithRuntimeImage(target FunctionTarget, runtimeImage string) (RenderedFunction, error) {
	rendered, err := RenderFunction(target)
	if err != nil {
		return RenderedFunction{}, err
	}
	return injectRuntimeImage(target, rendered, runtimeImage)
}

// RenderFunctionWithBoundSourcesAndRuntimeImage applies the same trusted
// runtime injection as ordinary executable rendering after exact sealed source
// refs have been bound. Source rendering must not bypass merge-preflight or
// agent runtime authority merely because its input IDs are supplied later.
func RenderFunctionWithBoundSourcesAndRuntimeImage(
	target FunctionTarget,
	sourceRefs map[string]snapshot.SnapshotRef,
	runtimeImage string,
) (RenderedFunction, error) {
	rendered, err := RenderFunctionWithBoundSources(target, sourceRefs)
	if err != nil {
		return RenderedFunction{}, err
	}
	return injectRuntimeImage(target, rendered, runtimeImage)
}

func injectRuntimeImage(target FunctionTarget, rendered RenderedFunction, runtimeImage string) (RenderedFunction, error) {
	agentCount := 0
	mergePreflightCount := 0
	for jobIndex := range rendered.Config.Jobs {
		for stepIndex := range rendered.Config.Jobs[jobIndex].PlanSequence {
			err := rendered.Config.Jobs[jobIndex].PlanSequence[stepIndex].Config.Visit(atc.StepRecursor{
				OnAgent: func(step *atc.AgentStep) error {
					agentCount++
					if step.RuntimeImage != "" {
						return fmt.Errorf("workflow runtime image was already populated")
					}
					if err := atc.ValidatePinnedOCIImage(runtimeImage); err != nil {
						return fmt.Errorf("workflow agent runtime image: %w", err)
					}
					step.RuntimeImage = runtimeImage
					return nil
				},
				OnTask: func(step *atc.TaskStep) error {
					if step.MergePreflightAuthority == nil {
						return nil
					}
					mergePreflightCount++
					if err := atc.ValidatePinnedOCIImage(runtimeImage); err != nil {
						return fmt.Errorf("workflow merge preflight runtime image: %w", err)
					}
					a := step.MergePreflightAuthority.Clone()
					a.WorkflowDefinitionID = target.WorkflowDefinitionID
					a.WorkflowVersion = target.WorkflowVersion
					a.CapabilityImage = runtimeImage
					at := strings.LastIndexByte(runtimeImage, '@')
					if at < 0 {
						return fmt.Errorf("workflow merge preflight runtime image is not pinned")
					}
					a.CapabilityImageDigest = snapshot.Digest(runtimeImage[at+1:])
					config, err := atc.NewMergePreflightTaskConfig(*a)
					if err != nil {
						return err
					}
					step.MergePreflightAuthority, step.Config = a, config
					return nil
				},
			})
			if err != nil {
				return RenderedFunction{}, err
			}
		}
	}
	if agentCount == 0 && mergePreflightCount == 0 {
		return rendered, nil
	}
	hash, err := RenderedTargetConfigHashWithBrokerProfiles(rendered.Config, rendered.DevValidationProfiles, rendered.DevValidationProvenanceHash, rendered.BrokerProfiles, rendered.BrokerProfileProvenanceHash)
	if err != nil {
		return RenderedFunction{}, err
	}
	name, err := TemplateName(target.Kind, target.WorkflowName, target.WorkflowVersion, target.FunctionID, hash)
	if err != nil {
		return RenderedFunction{}, err
	}
	rendered.TargetConfigHash, rendered.TemplateName = hash, name
	if err := rendered.refreshExecutionAuthority(); err != nil {
		return RenderedFunction{}, err
	}
	return rendered, nil
}

// RenderFunctionWithBoundSources renders a source-bearing workflow only after
// the standing admission pipeline has persisted exact sealed snapshots. The
// snapshot IDs are parameters, never template identity or resource lookups.
func RenderFunctionWithBoundSources(target FunctionTarget, sourceRefs map[string]snapshot.SnapshotRef) (RenderedFunction, error) {
	return renderFunction(target, sourceRefs, true)
}

func renderFunction(target FunctionTarget, sourceRefs map[string]snapshot.SnapshotRef, bindSources bool) (RenderedFunction, error) {
	function, err := cloneFunctionConfig(&target.Function)
	if err != nil {
		return RenderedFunction{}, fmt.Errorf("workflow: clone target: %w", err)
	}
	// The catalog is authoring-time indirection. Compilation has already copied
	// selected capabilities into literal, digest-pinned agent sidecars and
	// erased each agent's catalog references.
	function.Capabilities = nil
	signature := clonePublicSignature(target.Signature)
	if err := validateFunctionTarget(target, function, signature); err != nil {
		return RenderedFunction{}, err
	}
	hasSources := len(function.ResourceSources) > 0
	if hasSources && !bindSources {
		return RenderedFunction{}, fmt.Errorf("workflow: resource-source function requires exact bound source refs")
	}
	if bindSources {
		if err := validateBoundResourceSourceRefs(function.ResourceSources, sourceRefs); err != nil {
			return RenderedFunction{}, err
		}
	}
	if err := walkFunctionSteps(function.Plan, func(step atc.Step, _ string, _ bool) error {
		task, ok := step.Config.(*atc.TaskStep)
		if !ok || task.DevValidationAuthority == nil {
			return nil
		}
		authority := task.DevValidationAuthority.Clone()
		authority.WorkflowDefinitionID = target.WorkflowDefinitionID
		authority.WorkflowVersion = target.WorkflowVersion
		candidate := task.SnapshotInputs[authority.CandidateInput]
		config, err := atc.NewDevValidationTaskConfig(*authority, candidate.Type)
		if err != nil {
			return err
		}
		task.DevValidationAuthority = authority
		task.Config = config
		task.Hermetic = true
		return nil
	}); err != nil {
		return RenderedFunction{}, fmt.Errorf("workflow: render validation task: %w", err)
	}
	if err := renderValidationRequirements(function); err != nil {
		return RenderedFunction{}, err
	}
	if err := validateRenderableFunction(function, signature, target.WorkflowDefinitionID); err != nil {
		return RenderedFunction{}, err
	}
	if err := AnnotatePublicOutputs(function, target.WorkflowDefinitionID); err != nil {
		return RenderedFunction{}, fmt.Errorf("workflow: prepare target outputs: %w", err)
	}
	if err := annotateAwaitExecution(function, target.WorkflowDefinitionID); err != nil {
		return RenderedFunction{}, fmt.Errorf("workflow: prepare interaction waits: %w", err)
	}
	if err := annotatePublishExecution(function); err != nil {
		return RenderedFunction{}, fmt.Errorf(
			"workflow: prepare publication approvals: %w", err,
		)
	}
	params := []atc.ParamSchema{{
		Name:     "workflow_run_id",
		Type:     "string",
		Format:   atc.ParamFormatPositiveDecimalInt64,
		Required: true,
	}}
	loads := make([]atc.Step, 0, len(signature.Inputs)+len(function.ResourceSources))
	paramNames := make(map[string]string, len(signature.Inputs)+len(function.ResourceSources))
	for _, input := range signature.Inputs {
		paramName, err := InputParamName(input.Name)
		if err != nil {
			return RenderedFunction{}, err
		}
		if _, found := paramNames[input.Name]; found {
			return RenderedFunction{}, fmt.Errorf("workflow: duplicate public input %q", input.Name)
		}
		paramNames[input.Name] = paramName
		param := atc.ParamSchema{
			Name:     paramName,
			Type:     "string",
			Format:   atc.ParamFormatPositiveDecimalInt64,
			Required: true,
		}
		if input.Optional {
			param.Required = false
			param.Default = "0"
			param.Format = atc.ParamFormatZeroOrPositiveDecimalInt64
		}
		params = append(params, param)
		loads = append(loads, atc.Step{Config: &atc.LoadSnapshotStep{
			Name:          input.Name,
			ID:            "((" + paramName + "))",
			Type:          input.Type,
			Optional:      input.Optional,
			WorkflowRunID: "((workflow_run_id))",
		}})
	}
	if bindSources {
		for _, source := range function.ResourceSources {
			paramName, err := InputParamName(source.Name)
			if err != nil {
				return RenderedFunction{}, err
			}
			if _, found := paramNames[source.Name]; found {
				return RenderedFunction{}, fmt.Errorf("workflow: duplicate rendered snapshot input %q", source.Name)
			}
			paramNames[source.Name] = paramName
			params = append(params, atc.ParamSchema{Name: paramName, Type: "string", Format: atc.ParamFormatPositiveDecimalInt64, Required: true})
			loads = append(loads, atc.Step{Config: &atc.LoadSnapshotStep{Name: source.Name, ID: "((" + paramName + "))", Type: source.Type, WorkflowRunID: "((workflow_run_id))"}})
		}
	}

	plan := make([]atc.Step, 0, len(loads)+len(function.Plan))
	plan = append(plan, loads...)
	plan = append(plan, function.Plan...)
	flowFunction, err := cloneFunctionConfig(function)
	if err != nil {
		return RenderedFunction{}, fmt.Errorf("workflow: clone rendered flow: %w", err)
	}
	flowFunction.Inputs = nil
	flowFunction.Plan = plan
	renderResources := function.Resources
	renderResourceTypes := function.ResourceTypes
	if hasSources {
		renderResourceTypes, err = finalFunctionResourceTypes(function)
		if err != nil {
			return RenderedFunction{}, err
		}
		renderResources = nil
		flowFunction.Resources = nil
		flowFunction.ResourceSources = nil
		flowFunction.ResourceTypes = renderResourceTypes
	}
	if err := TypeCheckFunction(flowFunction); err != nil {
		return RenderedFunction{}, fmt.Errorf("workflow: rendered snapshot flow: %w", err)
	}
	for index := range function.Plan {
		if err := function.Plan[index].Config.Visit(atc.StepRecursor{OnAgent: func(step *atc.AgentStep) error {
			authority, err := renderBrokerAuthority(step.FunctionID, function.BrokerProfiles)
			if err != nil {
				return err
			}
			step.SetBrokerAuthority(authority)
			return nil
		}}); err != nil {
			return RenderedFunction{}, err
		}
	}

	config := atc.Config{
		Template:      true,
		Params:        params,
		Resources:     renderResources,
		ResourceTypes: renderResourceTypes,
		Prototypes:    function.Prototypes,
		VarSources:    function.VarSources,
		Jobs: atc.JobConfigs{{
			Name:         "run",
			PlanSequence: plan,
		}},
	}
	warnings, errorMessages := configvalidate.Validate(config)
	if len(errorMessages) > 0 {
		return RenderedFunction{}, fmt.Errorf("workflow: invalid rendered Concourse config:\n%s", strings.Join(errorMessages, "\n"))
	}
	if err := rejectIdentifierWarnings(warnings); err != nil {
		return RenderedFunction{}, err
	}

	canonicalConfig, err := config.CanonicalJSON()
	if err != nil {
		return RenderedFunction{}, fmt.Errorf("workflow: canonicalize rendered config: %w", err)
	}
	configHash, err := RenderedTargetConfigHashWithBrokerProfiles(config, function.DevValidationProfiles, function.DevValidationProvenanceHash, function.BrokerProfiles, function.BrokerProfileProvenanceHash)
	if err != nil {
		return RenderedFunction{}, err
	}
	templateName, err := TemplateName(target.Kind, target.WorkflowName, target.WorkflowVersion, target.FunctionID, configHash)
	if err != nil {
		return RenderedFunction{}, err
	}

	return RenderedFunction{
		TemplateName:                templateName,
		TargetSignature:             clonePublicSignature(signature),
		Config:                      config,
		TargetConfigHash:            configHash,
		InputParamNames:             paramNames,
		DevValidationProfiles:       cloneCompiledDevValidationProfiles(function.DevValidationProfiles),
		DevValidationProvenanceHash: function.DevValidationProvenanceHash,
		BrokerProfiles:              cloneCompiledBrokerProfiles(function.BrokerProfiles),
		BrokerProfileProvenanceHash: function.BrokerProfileProvenanceHash,
		boundSourceRefs:             cloneBoundSourceRefs(sourceRefs),
		executionCanonicalConfig:    append([]byte(nil), canonicalConfig...),
		executionTargetConfigHash:   configHash,
		templateKind:                target.Kind,
		templateWorkflowName:        target.WorkflowName,
		templateWorkflowVersion:     target.WorkflowVersion,
		templateFunctionID:          target.FunctionID,
	}, nil
}

func cloneBoundSourceRefs(refs map[string]snapshot.SnapshotRef) map[string]snapshot.SnapshotRef {
	if len(refs) == 0 {
		return nil
	}
	cloned := make(map[string]snapshot.SnapshotRef, len(refs))
	for name, ref := range refs {
		cloned[name] = ref
	}
	return cloned
}

// BoundSourceParamNames reports only the parameter names that trusted source
// binding owns. The snapshot identities remain private in ExecutionEnvelope.
func (rendered RenderedFunction) BoundSourceParamNames() map[string]struct{} {
	names := make(map[string]struct{}, len(rendered.boundSourceRefs))
	for source := range rendered.boundSourceRefs {
		name, err := InputParamName(source)
		if err == nil {
			names[name] = struct{}{}
		}
	}
	return names
}

func validateBoundResourceSourceRefs(sources []ResourceSource, refs map[string]snapshot.SnapshotRef) error {
	names := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		names[source.Name] = struct{}{}
	}
	for name := range refs {
		if _, found := names[name]; !found {
			return fmt.Errorf("workflow: extra bound resource source ref %q", name)
		}
	}
	used := make(map[snapshot.SnapshotID]string, len(sources))
	for _, source := range sources {
		ref, found := refs[source.Name]
		if !found {
			return fmt.Errorf("workflow: missing bound resource source ref %q", source.Name)
		}
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("workflow: resource source %q is not a sealed snapshot ref: %w", source.Name, err)
		}
		if ref.Type != source.Type {
			return fmt.Errorf("workflow: resource source %q snapshot type mismatch: have %s, require %s", source.Name, ref.Type, source.Type)
		}
		if previous, found := used[ref.ID]; found {
			return fmt.Errorf("workflow: duplicate bound resource source snapshot ref %s for %q and %q", ref.ID, previous, source.Name)
		}
		used[ref.ID] = source.Name
	}
	return nil
}

func finalFunctionResourceTypes(function *FunctionConfig) (atc.ResourceTypes, error) {
	required := map[string]struct{}{}
	if err := walkFunctionSteps(function.Plan, func(step atc.Step, path string, _ bool) error {
		task, ok := step.Config.(*atc.TaskStep)
		if !ok {
			return nil
		}
		closure, err := validateImmutableTaskDependencies(task, function.ResourceTypes)
		if err != nil {
			return fmt.Errorf("workflow: %s: %w", path, err)
		}
		for _, resourceType := range closure {
			required[resourceType.Name] = struct{}{}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	types := make(atc.ResourceTypes, 0, len(required))
	for _, resourceType := range function.ResourceTypes {
		if _, found := required[resourceType.Name]; found {
			types = append(types, resourceType)
		}
	}
	return types, nil
}

func cloneValidatedDefinitionFunction(definition Definition) (*FunctionConfig, PublicSignature, error) {
	if definition.ID <= 0 {
		return nil, PublicSignature{}, fmt.Errorf("workflow: durable definition ID must be positive")
	}
	if definition.Version <= 0 {
		return nil, PublicSignature{}, fmt.Errorf("workflow: durable definition version must be positive")
	}
	if definition.SchemaVersion != 3 {
		return nil, PublicSignature{}, fmt.Errorf("workflow: function targets require schema_version 3, got %d", definition.SchemaVersion)
	}
	if definition.SignatureVersion <= 0 {
		return nil, PublicSignature{}, fmt.Errorf("workflow: durable signature version must be positive")
	}
	if err := validateSafeIdentifier(definition.Name, "workflow name"); err != nil {
		return nil, PublicSignature{}, err
	}
	metadata, err := definition.Compiled.VersionMetadata()
	if err != nil {
		return nil, PublicSignature{}, err
	}
	if definition.Compiled.Name != definition.Name {
		return nil, PublicSignature{}, fmt.Errorf("workflow: durable name %q does not match compiled name %q", definition.Name, definition.Compiled.Name)
	}
	if metadata.SchemaVersion != definition.SchemaVersion || metadata.SignatureVersion != definition.SignatureVersion {
		return nil, PublicSignature{}, fmt.Errorf("workflow: durable schema/signature metadata does not match compiled definition")
	}
	signature, err := definition.Compiled.PublicSignature()
	if err != nil {
		return nil, PublicSignature{}, err
	}
	if err := validatePublicSignature(signature); err != nil {
		return nil, PublicSignature{}, err
	}
	warnings, err := ValidateFunction(definition.Compiled.Function)
	if err != nil {
		return nil, PublicSignature{}, fmt.Errorf("workflow: compiled definition is not renderable: %w", err)
	}
	if err := rejectIdentifierWarnings(warnings); err != nil {
		return nil, PublicSignature{}, err
	}
	function, err := cloneFunctionConfig(definition.Compiled.Function)
	if err != nil {
		return nil, PublicSignature{}, fmt.Errorf("workflow: clone compiled function: %w", err)
	}
	function.Capabilities = nil
	return function, clonePublicSignature(signature), nil
}

func validateFunctionTarget(target FunctionTarget, function *FunctionConfig, signature PublicSignature) error {
	if target.WorkflowDefinitionID <= 0 {
		return fmt.Errorf("workflow: target definition ID must be positive")
	}
	if target.WorkflowVersion <= 0 {
		return fmt.Errorf("workflow: target version must be positive")
	}
	if target.SignatureVersion <= 0 || function.SignatureVersion != target.SignatureVersion {
		return fmt.Errorf("workflow: target signature version is incomplete or inconsistent")
	}
	if err := validateSafeIdentifier(target.WorkflowName, "workflow name"); err != nil {
		return err
	}
	if err := validatePublicSignature(signature); err != nil {
		return err
	}
	if target.DevValidationProvenanceHash != function.DevValidationProvenanceHash {
		return fmt.Errorf("workflow: target dev validation provenance does not match its function")
	}

	switch target.Kind {
	case TargetWorkflow:
		if target.FunctionID != "" {
			return fmt.Errorf("workflow: workflow target must not carry a function ID")
		}
		if !signature.Equal(publicSignatureForFunction(function)) {
			return fmt.Errorf("workflow: workflow target signature does not match its function")
		}
	case TargetFunction:
		if strings.TrimSpace(target.FunctionID) == "" {
			return fmt.Errorf("workflow: function target requires a function ID")
		}
		if err := validateSafeIdentifier(target.FunctionID, "function ID"); err != nil {
			return err
		}
		if len(function.Plan) != 1 || directLeafFunctionID(function.Plan[0]) != target.FunctionID {
			return fmt.Errorf("workflow: function target must contain exactly its direct selected leaf %q", target.FunctionID)
		}
		if !signature.Equal(publicSignatureForFunction(function)) {
			return fmt.Errorf("workflow: extracted target signature does not match its function")
		}
	default:
		return fmt.Errorf("workflow: unsupported target kind %q", target.Kind)
	}
	return nil
}

func validateRenderableFunction(function *FunctionConfig, signature PublicSignature, workflowDefinitionID int) error {
	if function == nil {
		return fmt.Errorf("workflow: function is required")
	}
	if err := validateCompiledDevValidationProfiles(function.DevValidationProfiles, function.DevValidationProvenanceHash); err != nil {
		return err
	}
	if err := validateCompiledSkillAuthority(function); err != nil {
		return err
	}
	if err := validateCanonicalWorkflowOutputLinkage(function, workflowDefinitionID); err != nil {
		return err
	}
	// Renderer-owned tokens have a narrower authority boundary than generic
	// runtime interpolation. Diagnose attempted token forgery before reporting
	// the broader immutable-dependency violation.
	if err := rejectReservedTokenInjection(function, signature); err != nil {
		return err
	}
	if err := walkFunctionSteps(function.Plan, func(step atc.Step, path string, _ bool) error {
		if len(step.UnknownFields) > 0 {
			return fmt.Errorf("workflow: %s: unknown step fields are not renderable", path)
		}
		switch leaf := step.Config.(type) {
		case *atc.LoadSnapshotStep:
			return fmt.Errorf("workflow: %s: authored load_snapshot steps are not allowed; workflow inputs are loaded by the renderer", path)
		case *atc.TaskStep:
			if leaf.FunctionID != "" {
				if err := validateSafeIdentifier(leaf.FunctionID, "function ID"); err != nil {
					return fmt.Errorf("workflow: %s: %w", path, err)
				}
			}
			if _, err := validateImmutableTaskDependencies(leaf, function.ResourceTypes); err != nil {
				return fmt.Errorf("workflow: %s: %w", path, err)
			}
		case *atc.AgentStep:
			if leaf.FunctionID != "" {
				if err := validateSafeIdentifier(leaf.FunctionID, "function ID"); err != nil {
					return fmt.Errorf("workflow: %s: %w", path, err)
				}
			}
			if err := validateImmutableAgentDependencies(leaf); err != nil {
				return fmt.Errorf("workflow: %s: %w", path, err)
			}
		case *atc.PublishSnapshotStep:
			if leaf.WorkflowRunID != "" {
				return fmt.Errorf("workflow: %s: publish_snapshot workflow_run_id is renderer-owned", path)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := validateImmutableRuntimeDependencies(function.Plan); err != nil {
		return err
	}
	return nil
}

func validateImmutableRuntimeDependencies(steps []atc.Step) error {
	for index, step := range steps {
		if err := validateImmutableRuntimeStep(step, fmt.Sprintf("plan[%d]", index), nil); err != nil {
			return err
		}
	}
	return nil
}

func validateImmutableRuntimeStep(step atc.Step, path string, acrossVars map[string]struct{}) error {
	switch config := step.Config.(type) {
	case *atc.TaskStep:
		copy := *config
		copy.SnapshotOutputs = cloneSnapshotOutputs(config.SnapshotOutputs)
		sanitizeTypedOutputRunTokens(copy.SnapshotOutputs)
		if err := rejectRuntimeInterpolationExcept(copy, path+".task", acrossVars); err != nil {
			return fmt.Errorf("workflow: %w", err)
		}
	case *atc.AgentStep:
		copy := *config
		copy.SnapshotOutputs = cloneSnapshotOutputs(config.SnapshotOutputs)
		sanitizeTypedOutputRunTokens(copy.SnapshotOutputs)
		if err := rejectRuntimeInterpolationExcept(copy, path+".agent", acrossVars); err != nil {
			return fmt.Errorf("workflow: %w", err)
		}
	case *atc.AwaitSnapshotStep:
		copy := *config
		if copy.WorkflowRunID == "((workflow_run_id))" {
			copy.WorkflowRunID = "1"
		}
		if err := rejectRuntimeInterpolationExcept(copy, path+".await_snapshot", acrossVars); err != nil {
			return fmt.Errorf("workflow: %w", err)
		}
	case *atc.PublishSnapshotStep:
		copy := *config
		if copy.WorkflowRunID == "((workflow_run_id))" {
			copy.WorkflowRunID = "1"
		}
		if err := rejectRuntimeInterpolationExcept(copy, path+".publish_snapshot", acrossVars); err != nil {
			return fmt.Errorf("workflow: %w", err)
		}
	case *atc.DoStep:
		for index, child := range config.Steps {
			if err := validateImmutableRuntimeStep(child, fmt.Sprintf("%s.do[%d]", path, index), acrossVars); err != nil {
				return err
			}
		}
	case *atc.InParallelStep:
		for index, child := range config.Config.Steps {
			if err := validateImmutableRuntimeStep(child, fmt.Sprintf("%s.in_parallel[%d]", path, index), acrossVars); err != nil {
				return err
			}
		}
	case *atc.TryStep:
		return validateImmutableRuntimeStep(config.Step, path+".try", acrossVars)
	case *atc.AcrossStep:
		childVars := cloneStringSet(acrossVars)
		for index, variable := range config.Vars {
			if err := rejectRuntimeInterpolationExcept(variable.Values, fmt.Sprintf("%s.across[%d].values", path, index), acrossVars); err != nil {
				return fmt.Errorf("workflow: dynamic across values are not immutable: %w", err)
			}
			childVars[variable.Var] = struct{}{}
		}
		return validateImmutableRuntimeStep(atc.Step{Config: config.Step}, path+".across", childVars)
	case *atc.RetryStep:
		return validateImmutableRuntimeStep(atc.Step{Config: config.Step}, path+".attempts", acrossVars)
	case *atc.TimeoutStep:
		return validateImmutableRuntimeStep(atc.Step{Config: config.Step}, path+".timeout", acrossVars)
	case *atc.OnSuccessStep:
		if err := validateImmutableRuntimeStep(atc.Step{Config: config.Step}, path+".step", acrossVars); err != nil {
			return err
		}
		return validateImmutableRuntimeStep(config.Hook, path+".on_success", acrossVars)
	case *atc.OnFailureStep:
		if err := validateImmutableRuntimeStep(atc.Step{Config: config.Step}, path+".step", acrossVars); err != nil {
			return err
		}
		return validateImmutableRuntimeStep(config.Hook, path+".on_failure", acrossVars)
	case *atc.OnErrorStep:
		if err := validateImmutableRuntimeStep(atc.Step{Config: config.Step}, path+".step", acrossVars); err != nil {
			return err
		}
		return validateImmutableRuntimeStep(config.Hook, path+".on_error", acrossVars)
	case *atc.OnAbortStep:
		if err := validateImmutableRuntimeStep(atc.Step{Config: config.Step}, path+".step", acrossVars); err != nil {
			return err
		}
		return validateImmutableRuntimeStep(config.Hook, path+".on_abort", acrossVars)
	case *atc.EnsureStep:
		if err := validateImmutableRuntimeStep(atc.Step{Config: config.Step}, path+".step", acrossVars); err != nil {
			return err
		}
		return validateImmutableRuntimeStep(config.Hook, path+".ensure", acrossVars)
	}
	return nil
}

func cloneSnapshotOutputs(source map[string]atc.SnapshotOutputConfig) map[string]atc.SnapshotOutputConfig {
	if source == nil {
		return nil
	}
	cloned := make(map[string]atc.SnapshotOutputConfig, len(source))
	for name, output := range source {
		cloned[name] = output
	}
	return cloned
}

func cloneStringSet(source map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(source)+1)
	for value := range source {
		cloned[value] = struct{}{}
	}
	return cloned
}

type workflowOutputLinkage struct {
	Retention            snapshot.RetentionClass
	WorkflowPort         string
	WorkflowDefinitionID int
	WorkflowRunID        string
}

type workflowOutputsAtPath struct {
	path    string
	outputs map[string]atc.SnapshotOutputConfig
}

func validateCanonicalWorkflowOutputLinkage(function *FunctionConfig, workflowDefinitionID int) error {
	expected, err := cloneFunctionConfig(function)
	if err != nil {
		return fmt.Errorf("workflow: inspect workflow output linkage: %w", err)
	}
	if err := walkFunctionSteps(expected.Plan, func(step atc.Step, _ string, _ bool) error {
		var outputs map[string]atc.SnapshotOutputConfig
		switch leaf := step.Config.(type) {
		case *atc.TaskStep:
			outputs = leaf.SnapshotOutputs
		case *atc.AgentStep:
			outputs = leaf.SnapshotOutputs
		case *atc.AwaitSnapshotStep:
			leaf.WorkflowPort = ""
			leaf.WorkflowDefinitionID = 0
			leaf.WorkflowRunID = ""
		}
		for name, output := range outputs {
			if output.Retention == snapshot.RetentionClassWorkflow {
				output.Retention = ""
			}
			output.WorkflowPort = ""
			output.WorkflowDefinitionID = 0
			output.WorkflowRunID = ""
			outputs[name] = output
		}
		return nil
	}); err != nil {
		return fmt.Errorf("workflow: inspect workflow output linkage: %w", err)
	}
	if err := AnnotatePublicOutputs(expected, workflowDefinitionID); err != nil {
		return fmt.Errorf("workflow: derive canonical workflow output linkage: %w", err)
	}

	actualOutputs, err := collectWorkflowOutputs(function)
	if err != nil {
		return err
	}
	expectedOutputs, err := collectWorkflowOutputs(expected)
	if err != nil {
		return err
	}
	if len(actualOutputs) != len(expectedOutputs) {
		return fmt.Errorf("workflow: workflow output linkage traversal is inconsistent")
	}
	for index := range actualOutputs {
		actualAtPath := actualOutputs[index]
		expectedAtPath := expectedOutputs[index]
		if actualAtPath.path != expectedAtPath.path {
			return fmt.Errorf("workflow: workflow output linkage traversal is inconsistent")
		}
		names := make([]string, 0, len(actualAtPath.outputs))
		for name := range actualAtPath.outputs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			actual := outputWorkflowLinkage(actualAtPath.outputs[name])
			if actual == (workflowOutputLinkage{}) {
				continue
			}
			expectedConfig, found := expectedAtPath.outputs[name]
			if !found || actual != outputWorkflowLinkage(expectedConfig) {
				return fmt.Errorf("workflow: %s: output %q has noncanonical workflow output linkage", actualAtPath.path, name)
			}
		}
	}
	return nil
}

func collectWorkflowOutputs(function *FunctionConfig) ([]workflowOutputsAtPath, error) {
	collected := make([]workflowOutputsAtPath, 0)
	err := walkFunctionSteps(function.Plan, func(step atc.Step, path string, _ bool) error {
		switch leaf := step.Config.(type) {
		case *atc.TaskStep:
			collected = append(collected, workflowOutputsAtPath{path: path, outputs: leaf.SnapshotOutputs})
		case *atc.AgentStep:
			collected = append(collected, workflowOutputsAtPath{path: path, outputs: leaf.SnapshotOutputs})
		case *atc.AwaitSnapshotStep:
			output := atc.SnapshotOutputConfig{Type: leaf.Type}
			if leaf.WorkflowPort != "" {
				output.Retention = snapshot.RetentionClassWorkflow
			}
			output.WorkflowPort = leaf.WorkflowPort
			output.WorkflowDefinitionID = leaf.WorkflowDefinitionID
			output.WorkflowRunID = leaf.WorkflowRunID
			collected = append(collected, workflowOutputsAtPath{path: path, outputs: map[string]atc.SnapshotOutputConfig{leaf.Name: output}})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("workflow: inspect workflow output linkage: %w", err)
	}
	return collected, nil
}

func outputWorkflowLinkage(config atc.SnapshotOutputConfig) workflowOutputLinkage {
	if config.Retention != snapshot.RetentionClassWorkflow && config.WorkflowPort == "" && config.WorkflowDefinitionID == 0 && config.WorkflowRunID == "" {
		return workflowOutputLinkage{}
	}
	return workflowOutputLinkage{
		Retention:            config.Retention,
		WorkflowPort:         config.WorkflowPort,
		WorkflowDefinitionID: config.WorkflowDefinitionID,
		WorkflowRunID:        config.WorkflowRunID,
	}
}

func rejectReservedTokenInjection(function *FunctionConfig, signature PublicSignature) error {
	inspected, err := cloneFunctionConfig(function)
	if err != nil {
		return fmt.Errorf("workflow: inspect reserved tokens: %w", err)
	}
	if err := walkFunctionSteps(inspected.Plan, func(step atc.Step, _ string, _ bool) error {
		switch leaf := step.Config.(type) {
		case *atc.TaskStep:
			sanitizeTypedOutputRunTokens(leaf.SnapshotOutputs)
		case *atc.AgentStep:
			sanitizeTypedOutputRunTokens(leaf.SnapshotOutputs)
		case *atc.AwaitSnapshotStep:
			if leaf.WorkflowRunID == "((workflow_run_id))" {
				leaf.WorkflowRunID = "1"
			}
		case *atc.PublishSnapshotStep:
			if leaf.WorkflowRunID == "((workflow_run_id))" {
				leaf.WorkflowRunID = "1"
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("workflow: inspect reserved tokens: %w", err)
	}

	payload, err := json.Marshal(inspected)
	if err != nil {
		return fmt.Errorf("workflow: inspect reserved tokens: %w", err)
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return fmt.Errorf("workflow: inspect reserved tokens: %w", err)
	}
	tokens := []string{"((workflow_run_id))"}
	for _, input := range signature.Inputs {
		tokens = append(tokens, "((snapshot_"+input.Name+"))")
	}
	var inspect func(any, []string) error
	inspect = func(current any, path []string) error {
		switch typed := current.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				for _, token := range tokens {
					if strings.Contains(key, token) {
						return fmt.Errorf("workflow: reserved renderer token %q appears in authored map key %s", token, strings.Join(append(path, key), "."))
					}
				}
				if err := inspect(typed[key], append(path, key)); err != nil {
					return err
				}
			}
		case []any:
			for index, item := range typed {
				if err := inspect(item, append(path, fmt.Sprintf("[%d]", index))); err != nil {
					return err
				}
			}
		case string:
			joined := strings.Join(path, ".")
			for _, token := range tokens {
				if strings.Contains(typed, token) {
					return fmt.Errorf("workflow: reserved renderer token %q appears in authored field %s", token, joined)
				}
			}
		}
		return nil
	}
	return inspect(value, []string{"function"})
}

func sanitizeTypedOutputRunTokens(outputs map[string]atc.SnapshotOutputConfig) {
	for name, output := range outputs {
		if output.WorkflowRunID != "((workflow_run_id))" {
			continue
		}
		// Keep the value valid for SnapshotOutputConfig.MarshalJSON while
		// removing the sole renderer-owned token from generic inspection. This
		// operates on the typed field itself, never on a JSON path that an
		// arbitrary authored map could spoof.
		output.WorkflowRunID = "1"
		outputs[name] = output
	}
}

func annotateAwaitExecution(function *FunctionConfig, workflowDefinitionID int) error {
	if workflowDefinitionID <= 0 {
		return fmt.Errorf("workflow definition ID must be positive")
	}
	return walkFunctionSteps(function.Plan, func(step atc.Step, path string, _ bool) error {
		wait, ok := step.Config.(*atc.AwaitSnapshotStep)
		if !ok {
			return nil
		}
		if wait.WorkflowRunID != "" && wait.WorkflowRunID != "((workflow_run_id))" {
			return fmt.Errorf("%s: await_snapshot workflow run linkage is noncanonical", path)
		}
		if wait.WorkflowDefinitionID != 0 && wait.WorkflowDefinitionID != workflowDefinitionID {
			return fmt.Errorf("%s: await_snapshot workflow definition linkage is noncanonical", path)
		}
		wait.WorkflowDefinitionID = workflowDefinitionID
		wait.WorkflowRunID = "((workflow_run_id))"
		return nil
	})
}

func annotatePublishExecution(function *FunctionConfig) error {
	return walkFunctionSteps(function.Plan, func(step atc.Step, path string, _ bool) error {
		publish, ok := step.Config.(*atc.PublishSnapshotStep)
		if !ok || publish.Mode != publisher.ModeMerge {
			return nil
		}
		if publish.WorkflowRunID != "" && publish.WorkflowRunID != "((workflow_run_id))" {
			return fmt.Errorf("%s: publish_snapshot workflow run linkage is noncanonical", path)
		}
		publish.WorkflowRunID = "((workflow_run_id))"
		return nil
	})
}

func validatePublicSignature(signature PublicSignature) error {
	seen := make(map[string]struct{}, len(signature.Inputs)+len(signature.Outputs))
	for _, group := range []struct {
		name  string
		ports []SignaturePort
	}{{"input", signature.Inputs}, {"output", signature.Outputs}} {
		for _, port := range group.ports {
			if err := validateSafeIdentifier(port.Name, "public "+group.name); err != nil {
				return err
			}
			if err := port.Type.Validate(); err != nil {
				return fmt.Errorf("workflow: public %s %q: %w", group.name, port.Name, err)
			}
			key := group.name + "\x00" + port.Name
			if _, found := seen[key]; found {
				return fmt.Errorf("workflow: duplicate public %s %q", group.name, port.Name)
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func publicSignatureForFunction(function *FunctionConfig) PublicSignature {
	signature := PublicSignature{
		Inputs:  make([]SignaturePort, len(function.Inputs)),
		Outputs: make([]SignaturePort, len(function.Outputs)),
	}
	for index, input := range function.Inputs {
		signature.Inputs[index] = SignaturePort{Name: input.Name, Type: input.Type, Optional: input.Optional}
	}
	for index, output := range function.Outputs {
		signature.Outputs[index] = SignaturePort{Name: output.Name, Type: output.Type, Optional: output.Optional}
	}
	return signature
}

func clonePublicSignature(signature PublicSignature) PublicSignature {
	return PublicSignature{
		Inputs:  append([]SignaturePort(nil), signature.Inputs...),
		Outputs: append([]SignaturePort(nil), signature.Outputs...),
	}
}

func cloneFunctionConfig(function *FunctionConfig) (*FunctionConfig, error) {
	if function == nil {
		return nil, fmt.Errorf("function is required")
	}
	copied, err := copystructure.Copy(function)
	if err != nil {
		return nil, err
	}
	cloned, ok := copied.(*FunctionConfig)
	if !ok {
		return nil, fmt.Errorf("unexpected cloned function type %T", copied)
	}
	return cloned, nil
}

func validateSafeIdentifier(value, label string) error {
	warning, err := atc.ValidateIdentifier(value)
	if err != nil {
		return fmt.Errorf("workflow: %s %q is not a safe identifier: %w", label, value, err)
	}
	if warning != nil {
		return fmt.Errorf("workflow: %s %q is not a safe identifier: %s", label, value, warning.Message)
	}
	return nil
}

func rejectIdentifierWarnings(warnings []atc.ConfigWarning) error {
	for _, warning := range warnings {
		if warning.Type == "invalid_identifier" {
			return fmt.Errorf("workflow: rendered config has unsafe identifier: %s", warning.Message)
		}
	}
	return nil
}

func directLeafFunctionID(step atc.Step) string {
	switch leaf := step.Config.(type) {
	case *atc.TaskStep:
		return leaf.FunctionID
	case *atc.AgentStep:
		return leaf.FunctionID
	default:
		return ""
	}
}

func walkFunctionSteps(steps []atc.Step, visit func(atc.Step, string, bool) error) error {
	for index, step := range steps {
		if err := walkFunctionStep(step, fmt.Sprintf("plan[%d]", index), false, visit); err != nil {
			return err
		}
	}
	return nil
}

func walkFunctionStep(step atc.Step, path string, nested bool, visit func(atc.Step, string, bool) error) error {
	if err := visit(step, path, nested); err != nil {
		return err
	}
	switch config := step.Config.(type) {
	case *atc.DoStep:
		for index, child := range config.Steps {
			if err := walkFunctionStep(child, fmt.Sprintf("%s.do[%d]", path, index), true, visit); err != nil {
				return err
			}
		}
	case *atc.InParallelStep:
		for index, child := range config.Config.Steps {
			if err := walkFunctionStep(child, fmt.Sprintf("%s.in_parallel[%d]", path, index), true, visit); err != nil {
				return err
			}
		}
	case *atc.TryStep:
		return walkFunctionStep(config.Step, path+".try", true, visit)
	case *atc.AcrossStep:
		return walkFunctionStep(atc.Step{Config: config.Step}, path+".across", true, visit)
	case *atc.RetryStep:
		return walkFunctionStep(atc.Step{Config: config.Step}, path+".attempts", true, visit)
	case *atc.TimeoutStep:
		return walkFunctionStep(atc.Step{Config: config.Step}, path+".timeout", true, visit)
	case *atc.OnSuccessStep:
		if err := walkFunctionStep(atc.Step{Config: config.Step}, path+".step", true, visit); err != nil {
			return err
		}
		return walkFunctionStep(config.Hook, path+".on_success", true, visit)
	case *atc.OnFailureStep:
		if err := walkFunctionStep(atc.Step{Config: config.Step}, path+".step", true, visit); err != nil {
			return err
		}
		return walkFunctionStep(config.Hook, path+".on_failure", true, visit)
	case *atc.OnErrorStep:
		if err := walkFunctionStep(atc.Step{Config: config.Step}, path+".step", true, visit); err != nil {
			return err
		}
		return walkFunctionStep(config.Hook, path+".on_error", true, visit)
	case *atc.OnAbortStep:
		if err := walkFunctionStep(atc.Step{Config: config.Step}, path+".step", true, visit); err != nil {
			return err
		}
		return walkFunctionStep(config.Hook, path+".on_abort", true, visit)
	case *atc.EnsureStep:
		if err := walkFunctionStep(atc.Step{Config: config.Step}, path+".step", true, visit); err != nil {
			return err
		}
		return walkFunctionStep(config.Hook, path+".ensure", true, visit)
	}
	return nil
}
