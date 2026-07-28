package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	if len(profiles) == 0 {
		if provenance != "" {
			return "", fmt.Errorf("workflow: validation provenance requires rendered profiles")
		}
		return TargetConfigHash(config)
	}
	if err := ValidateDevValidationAuthority(profiles, provenance); err != nil {
		return "", err
	}
	canonical, err := config.CanonicalJSON()
	if err != nil {
		return "", fmt.Errorf("workflow: canonicalize target config: %w", err)
	}
	authority, err := json.Marshal(struct {
		Profiles   []CompiledDevValidationProfile `json:"profiles"`
		Provenance string                         `json:"provenance"`
	}{profiles, provenance})
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
		return RenderedFunction{}, fmt.Errorf("workflow: prepare publication approvals: %w", err)
	}

	params := []atc.ParamSchema{{
		Name:     "workflow_run_id",
		Type:     "string",
		Format:   atc.ParamFormatPositiveDecimalInt64,
		Required: true,
	}}
	loads := make([]atc.Step, 0, len(signature.Inputs))
	paramNames := make(map[string]string, len(signature.Inputs))
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

	plan := make([]atc.Step, 0, len(loads)+len(function.Plan))
	plan = append(plan, loads...)
	plan = append(plan, function.Plan...)
	flowFunction, err := cloneFunctionConfig(function)
	if err != nil {
		return RenderedFunction{}, fmt.Errorf("workflow: clone rendered flow: %w", err)
	}
	flowFunction.Inputs = nil
	flowFunction.Plan = plan
	if err := TypeCheckFunction(flowFunction); err != nil {
		return RenderedFunction{}, fmt.Errorf("workflow: rendered snapshot flow: %w", err)
	}

	config := atc.Config{
		Template:      true,
		Params:        params,
		Resources:     function.Resources,
		ResourceTypes: function.ResourceTypes,
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

	configHash, err := RenderedTargetConfigHash(config, function.DevValidationProfiles, function.DevValidationProvenanceHash)
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
	}, nil
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
	if len(function.SkillFiles) > 0 {
		return fmt.Errorf("workflow: compiled skills are not supported by immutable function templates")
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
