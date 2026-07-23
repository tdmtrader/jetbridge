package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

// FunctionNotFoundError distinguishes an absent stable function identity from
// malformed compiled definitions and ineligible extraction targets.
type FunctionNotFoundError struct {
	FunctionID string
}

func (err FunctionNotFoundError) Error() string {
	return fmt.Sprintf("workflow: function_id %q not found", err.FunctionID)
}

func ExtractFunctionTarget(definition Definition, functionID string) (FunctionTarget, error) {
	if strings.TrimSpace(functionID) == "" {
		return FunctionTarget{}, fmt.Errorf("workflow: extracted function ID is required")
	}
	if err := validateSafeIdentifier(functionID, "function ID"); err != nil {
		return FunctionTarget{}, err
	}
	function, _, err := cloneValidatedDefinitionFunction(definition)
	if err != nil {
		return FunctionTarget{}, err
	}
	if len(function.SkillFiles) > 0 {
		return FunctionTarget{}, fmt.Errorf("workflow: compiled skills are not supported by immutable extracted targets")
	}
	type match struct {
		step   atc.Step
		path   string
		nested bool
	}
	matches := make([]match, 0, 1)
	err = walkFunctionSteps(function.Plan, func(step atc.Step, path string, nested bool) error {
		if directLeafFunctionID(step) == functionID {
			matches = append(matches, match{step: step, path: path, nested: nested})
		}
		return nil
	})
	if err != nil {
		return FunctionTarget{}, err
	}
	switch len(matches) {
	case 0:
		return FunctionTarget{}, FunctionNotFoundError{FunctionID: functionID}
	case 1:
	default:
		paths := make([]string, len(matches))
		for index, found := range matches {
			paths[index] = found.path
		}
		return FunctionTarget{}, fmt.Errorf("workflow: invalid compiled definition: duplicate function_id %q at %s", functionID, strings.Join(paths, ", "))
	}
	found := matches[0]
	if found.nested {
		return FunctionTarget{}, fmt.Errorf("workflow: function_id %q at %s is not a direct top-level leaf; extracting through wrappers is unsupported", functionID, found.path)
	}

	inputs, outputs, resourceTypes, err := validateExtractedLeaf(found.step, found.path, function.ResourceTypes)
	if err != nil {
		return FunctionTarget{}, fmt.Errorf("workflow: function_id %q at %s: %w", functionID, found.path, err)
	}
	signature := PublicSignature{Inputs: inputs, Outputs: outputs}
	extracted := &FunctionConfig{
		SignatureVersion: definition.SignatureVersion,
		Inputs:           portsFromSignature(inputs),
		Outputs:          outputsFromSignature(outputs),
		ResourceTypes:    resourceTypes,
		Plan:             []atc.Step{found.step},
	}
	if err := validateRenderableFunction(extracted, signature, definition.ID); err != nil {
		return FunctionTarget{}, err
	}
	if err := AnnotatePublicOutputs(extracted, definition.ID); err != nil {
		return FunctionTarget{}, fmt.Errorf("workflow: annotate extracted function %q: %w", functionID, err)
	}

	return FunctionTarget{
		Kind:                 TargetFunction,
		WorkflowDefinitionID: definition.ID,
		WorkflowName:         definition.Name,
		WorkflowVersion:      definition.Version,
		SignatureVersion:     definition.SignatureVersion,
		FunctionID:           functionID,
		Signature:            clonePublicSignature(signature),
		Function:             *extracted,
	}, nil
}

func validateExtractedLeaf(step atc.Step, path string, resourceTypes atc.ResourceTypes) ([]SignaturePort, []SignaturePort, atc.ResourceTypes, error) {
	if len(step.UnknownFields) > 0 {
		return nil, nil, nil, fmt.Errorf("unknown step fields are not extractable")
	}
	if err := rejectExtractionInterpolation(step); err != nil {
		return nil, nil, nil, err
	}

	switch leaf := step.Config.(type) {
	case *atc.TaskStep:
		if leaf.Timeout != "" {
			return nil, nil, nil, fmt.Errorf("task timeout modifier is not extractable")
		}
		closure, err := validateImmutableTaskDependencies(leaf, resourceTypes)
		if err != nil {
			return nil, nil, nil, err
		}
		ordinaryInputs, ordinaryOutputs := effectiveTaskArtifactNames(leaf)
		if err := validateExactTypedCoverage("task input", ordinaryInputs, snapshotInputNames(leaf.SnapshotInputs)); err != nil {
			return nil, nil, nil, err
		}
		if err := validateExactTypedCoverage("task output", ordinaryOutputs, snapshotOutputNames(leaf.SnapshotOutputs)); err != nil {
			return nil, nil, nil, err
		}
		return signatureInputs(leaf.SnapshotInputs), signatureOutputs(leaf.SnapshotOutputs), closure, nil

	case *atc.AgentStep:
		if leaf.Timeout != "" {
			return nil, nil, nil, fmt.Errorf("agent timeout modifier is not extractable")
		}
		if err := validateImmutableAgentDependencies(leaf); err != nil {
			return nil, nil, nil, err
		}
		if err := validateExactTypedCoverage("agent input", leaf.Inputs, snapshotInputNames(leaf.SnapshotInputs)); err != nil {
			return nil, nil, nil, err
		}
		if err := validateExactTypedCoverage("agent output", leaf.Outputs, snapshotOutputNames(leaf.SnapshotOutputs)); err != nil {
			return nil, nil, nil, err
		}
		return signatureInputs(leaf.SnapshotInputs), signatureOutputs(leaf.SnapshotOutputs), nil, nil

	default:
		return nil, nil, nil, fmt.Errorf("unsupported future leaf %T is not extractable", step.Config)
	}
}

func validateImmutableTaskDependencies(task *atc.TaskStep, resourceTypes atc.ResourceTypes) (atc.ResourceTypes, error) {
	if task == nil {
		return nil, fmt.Errorf("task config is required")
	}
	if task.ConfigPath != "" {
		return nil, fmt.Errorf("file-backed task config %q is not immutable", task.ConfigPath)
	}
	if task.Config == nil {
		return nil, fmt.Errorf("inline task config is required")
	}
	if task.ImageArtifactName != "" {
		return nil, fmt.Errorf("task image artifact %q is not immutable", task.ImageArtifactName)
	}
	if len(task.Vars) > 0 {
		return nil, fmt.Errorf("task vars are an unresolved runtime dependency")
	}
	if err := validateImmutableSidecars(task.Sidecars); err != nil {
		return nil, err
	}
	if len(task.Config.Caches) > 0 {
		return nil, fmt.Errorf("task caches are mutable build state")
	}
	if task.Config.RootfsURI != "" {
		return nil, fmt.Errorf("task rootfs_uri is not an immutable image")
	}
	if task.Config.ImageResource == nil {
		return nil, fmt.Errorf("task image_resource is required for immutable execution")
	}
	return immutableTaskImageClosure(task.Config.ImageResource, resourceTypes)
}

func validateImmutableAgentDependencies(agent *atc.AgentStep) error {
	if agent == nil {
		return fmt.Errorf("agent config is required")
	}
	if agent.PromptFile != "" || agent.SystemPromptFile != "" || len(agent.ContextFiles) > 0 {
		return fmt.Errorf("agent file assets are unresolved runtime dependencies")
	}
	if agent.RuntimeImage != "" {
		return fmt.Errorf("agent runtime_image is server-selected admission data")
	}
	if len(agent.Capabilities) > 0 {
		return fmt.Errorf("unexpanded agent capabilities are unresolved runtime dependencies")
	}
	if len(agent.Skills) > 0 {
		return fmt.Errorf("agent skills are not supported by immutable function templates")
	}
	return validateImmutableSidecars(agent.Sidecars)
}

func validateImmutableSidecars(sidecars []atc.SidecarSource) error {
	for index, source := range sidecars {
		if source.File != "" {
			return fmt.Errorf("sidecar[%d] file %q is not immutable", index, source.File)
		}
		if source.Config == nil {
			return fmt.Errorf("sidecar[%d] must contain a literal config", index)
		}
		if source.Config.ImageArtifact != "" {
			return fmt.Errorf("sidecar[%d] image_artifact %q is not immutable", index, source.Config.ImageArtifact)
		}
		if err := source.Config.ValidateCapability(); err != nil {
			return fmt.Errorf("sidecar[%d] must use a literal exact digest: %w", index, err)
		}
	}
	return nil
}

func immutableTaskImageClosure(image *atc.ImageResource, resourceTypes atc.ResourceTypes) (atc.ResourceTypes, error) {
	if image == nil {
		return nil, nil
	}
	if len(image.Version) == 0 {
		return nil, fmt.Errorf("task image resource requires an explicit immutable version")
	}
	resourceType, found := resourceTypes.Lookup(image.Type)
	if !found {
		return nil, nil
	}
	if resourceType.Image == "" {
		return nil, fmt.Errorf("task image resource type %q requires a mutable check chain", image.Type)
	}
	if err := (atc.SidecarConfig{Name: "task-image-type", Image: resourceType.Image}).ValidateCapability(); err != nil {
		return nil, fmt.Errorf("task image resource type %q must use an exact digest: %w", image.Type, err)
	}
	if err := rejectRuntimeInterpolation(resourceType, "resource_type("+resourceType.Name+")"); err != nil {
		return nil, err
	}
	return atc.ResourceTypes{resourceType}, nil
}

func validateExactTypedCoverage(label string, ordinary, typed []string) error {
	ordinary = sortedUniqueStrings(ordinary)
	typed = sortedUniqueStrings(typed)
	if len(ordinary) != len(typed) {
		return fmt.Errorf("every declared %s must be typed: declared=%v typed=%v", label, ordinary, typed)
	}
	for index := range ordinary {
		if ordinary[index] != typed[index] {
			return fmt.Errorf("every declared %s must be typed: declared=%v typed=%v", label, ordinary, typed)
		}
	}
	return nil
}

func signatureInputs(configs map[string]atc.SnapshotInputConfig) []SignaturePort {
	names := snapshotInputNames(configs)
	sort.Strings(names)
	ports := make([]SignaturePort, len(names))
	for index, name := range names {
		config := configs[name]
		ports[index] = SignaturePort{Name: name, Type: config.Type, Optional: config.Optional}
	}
	return ports
}

func signatureOutputs(configs map[string]atc.SnapshotOutputConfig) []SignaturePort {
	names := snapshotOutputNames(configs)
	sort.Strings(names)
	ports := make([]SignaturePort, len(names))
	for index, name := range names {
		config := configs[name]
		ports[index] = SignaturePort{Name: name, Type: config.Type, Optional: config.Optional}
	}
	return ports
}

func snapshotInputNames(configs map[string]atc.SnapshotInputConfig) []string {
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	return names
}

func snapshotOutputNames(configs map[string]atc.SnapshotOutputConfig) []string {
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	return names
}

func portsFromSignature(signature []SignaturePort) []snapshot.Port {
	ports := make([]snapshot.Port, len(signature))
	for index, port := range signature {
		ports[index] = snapshot.Port{Name: port.Name, Type: port.Type, Optional: port.Optional}
	}
	return ports
}

func outputsFromSignature(signature []SignaturePort) []FunctionOutput {
	outputs := make([]FunctionOutput, len(signature))
	for index, port := range signature {
		outputs[index] = FunctionOutput{
			Port: snapshot.Port{Name: port.Name, Type: port.Type, Optional: port.Optional},
			From: port.Name,
		}
	}
	return outputs
}

func rejectExtractionInterpolation(step atc.Step) error {
	return rejectRuntimeInterpolation(step, "step")
}

func rejectRuntimeInterpolation(source any, root string) error {
	return rejectRuntimeInterpolationExcept(source, root, nil)
}

func rejectRuntimeInterpolationExcept(source any, root string, allowedAcrossVars map[string]struct{}) error {
	payload, err := json.Marshal(source)
	if err != nil {
		return fmt.Errorf("inspect immutable dependencies: %w", err)
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return fmt.Errorf("inspect immutable dependencies: %w", err)
	}
	hasUnresolvedInterpolation := func(value string) bool {
		for variable := range allowedAcrossVars {
			value = strings.ReplaceAll(value, "((.:"+variable+"))", "")
		}
		return strings.Contains(value, "((")
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
				if hasUnresolvedInterpolation(key) {
					return fmt.Errorf("unresolved runtime interpolation in map key %s", strings.Join(append(path, key), "."))
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
			if hasUnresolvedInterpolation(typed) {
				return fmt.Errorf("unresolved runtime interpolation in %s", joined)
			}
		}
		return nil
	}
	return inspect(value, []string{root})
}
