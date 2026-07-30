package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/goccy/go-yaml"
)

// NodeResolver is the trusted import-time lookup for exact released node
// versions. It deliberately has no latest/channel operation: those choices
// belong to an explicit workflow source update, never compilation.
type NodeResolver interface {
	Released(name string, version int) (NodeDefinition, bool, error)
}

// NodeReference is the authoring-only workflow spelling of an invocation.
// It is resolved before ordinary Concourse decoding and never persists in the
// compiled plan.
type NodeReference struct {
	InstanceName  string            `json:"node" yaml:"node"`
	Uses          string            `json:"uses" yaml:"uses"`
	InputMapping  map[string]string `json:"input_mapping,omitempty" yaml:"input_mapping,omitempty"`
	OutputMapping map[string]string `json:"output_mapping,omitempty" yaml:"output_mapping,omitempty"`
	Parameters    map[string]string `json:"params,omitempty" yaml:"params,omitempty"`
}

// ResolvedNodeBinding is the durable record of an exact import decision.
type ResolvedNodeBinding struct {
	InstanceName     string            `json:"instance_name"`
	NodeDefinitionID int               `json:"node_definition_id"`
	NodeName         string            `json:"node_name"`
	NodeVersion      int               `json:"node_version"`
	NodeContentHash  string            `json:"node_content_hash"`
	InputMapping     map[string]string `json:"input_mapping,omitempty"`
	OutputMapping    map[string]string `json:"output_mapping,omitempty"`
	Parameters       map[string]string `json:"parameters,omitempty"`
}

// CompileDefinitionWithNodes compiles an authored workflow using only exact
// released node versions. It copies and rewrites only an ephemeral decoded
// source document: m and its selected workflow source stay byte-for-byte
// authored for later inspection and upgrades.
func CompileDefinitionWithNodes(m Manifest, resolver NodeResolver) (*CompiledDefinition, []ResolvedNodeBinding, error) {
	if resolver == nil {
		return nil, nil, fmt.Errorf("workflow: node resolver is required")
	}
	if err := m.Validate(); err != nil {
		return nil, nil, err
	}
	raw, ok := m.DefinitionSource()
	if !ok {
		return nil, nil, fmt.Errorf("workflow: manifest has no %s (or legacy %s)", WorkflowFileName, LegacyWorkflowFileName)
	}
	if err := RequireSchemaVersion3([]byte(raw)); err != nil {
		return nil, nil, err
	}

	document, err := decodeNodeReferenceDocument([]byte(raw))
	if err != nil {
		return nil, nil, err
	}
	expander := nodeReferenceExpander{resolver: resolver, instances: map[string]struct{}{}, frozen: map[string]frozenNodeAssets{}}
	if err := expander.expandWorkflow(document); err != nil {
		return nil, nil, err
	}
	expanded, err := yaml.Marshal(document)
	if err != nil {
		return nil, nil, fmt.Errorf("workflow: encode expanded node references: %w", err)
	}
	definition, profiles, err := parseFunctionDefinitionSource(expanded)
	if err != nil {
		return nil, nil, err
	}
	if err := mergeFrozenSkillFiles(definition.Function, expander.frozen); err != nil {
		return nil, nil, err
	}
	if err := compileFunctionAssetsWithFrozenNodes(m, definition, profiles, expander.frozen); err != nil {
		return nil, nil, err
	}
	if err := mergeFrozenDevValidationProfiles(definition.Function, expander.frozen); err != nil {
		return nil, nil, err
	}
	if _, err := ValidateFunction(definition.Function); err != nil {
		return nil, nil, err
	}
	sort.Slice(expander.bindings, func(i, j int) bool { return expander.bindings[i].InstanceName < expander.bindings[j].InstanceName })
	return definition, expander.bindings, nil
}

func decodeNodeReferenceDocument(raw []byte) (any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse workflow node references: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse workflow node references: exactly one YAML or JSON document is required")
		}
		return nil, fmt.Errorf("parse workflow node references trailing document: %w", err)
	}
	return document, nil
}

type nodeReferenceExpander struct {
	resolver  NodeResolver
	instances map[string]struct{}
	bindings  []ResolvedNodeBinding
	frozen    map[string]frozenNodeAssets
}

type frozenNodeAssets struct {
	skillFiles map[string]string
	profiles   []CompiledDevValidationProfile
}

func (expander *nodeReferenceExpander) expandWorkflow(value any) error {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	plan, found := root["plan"]
	if !found {
		return nil
	}
	return expander.expandStepList(plan, "workflow.plan")
}

func (expander *nodeReferenceExpander) expandStepList(value any, path string) error {
	steps, ok := value.([]any)
	if !ok {
		return nil
	}
	for index := range steps {
		step, ok := steps[index].(map[string]any)
		if !ok {
			continue
		}
		if err := expander.expandStep(step, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func (expander *nodeReferenceExpander) expandStep(step map[string]any, path string) error {
	_, hasNode := step["node"]
	_, hasUses := step["uses"]
	if hasNode || hasUses {
		replacement, binding, err := expander.resolve(step, path)
		if err != nil {
			return err
		}
		for key := range step {
			delete(step, key)
		}
		for key, child := range replacement {
			step[key] = child
		}
		expander.bindings = append(expander.bindings, binding)
		return expander.expandStep(step, path)
	}
	for _, field := range []string{"do", "try", "timeout", "across", "on_success", "on_failure", "on_abort", "on_error", "ensure"} {
		child, found := step[field]
		if !found {
			continue
		}
		if field == "do" {
			if err := expander.expandStepList(child, path+"."+field); err != nil {
				return err
			}
			continue
		}
		if childStep, ok := child.(map[string]any); ok {
			if err := expander.expandStep(childStep, path+"."+field); err != nil {
				return err
			}
		}
	}
	if parallel, found := step["in_parallel"]; found {
		switch config := parallel.(type) {
		case []any:
			if err := expander.expandStepList(config, path+".in_parallel"); err != nil {
				return err
			}
		case map[string]any:
			if err := expander.expandStepList(config["steps"], path+".in_parallel.steps"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (expander *nodeReferenceExpander) resolve(source map[string]any, path string) (map[string]any, ResolvedNodeBinding, error) {
	refSource, modifiers, err := splitNodeReferenceStep(source, path)
	if err != nil {
		return nil, ResolvedNodeBinding{}, err
	}
	ref, err := decodeNodeReference(refSource)
	if err != nil {
		return nil, ResolvedNodeBinding{}, fmt.Errorf("workflow: %s: %w", path, err)
	}
	if err := validateSafeIdentifier(ref.InstanceName, "node instance name"); err != nil {
		return nil, ResolvedNodeBinding{}, err
	}
	if _, exists := expander.instances[ref.InstanceName]; exists {
		return nil, ResolvedNodeBinding{}, fmt.Errorf("workflow: duplicate node instance %q", ref.InstanceName)
	}
	name, version, err := parseExactNodeUse(ref.Uses)
	if err != nil {
		return nil, ResolvedNodeBinding{}, err
	}
	node, found, err := expander.resolver.Released(name, version)
	if err != nil {
		return nil, ResolvedNodeBinding{}, fmt.Errorf("workflow: resolve node %s@%d: %w", name, version, err)
	}
	if !found {
		return nil, ResolvedNodeBinding{}, fmt.Errorf("workflow: released node %s@%d not found", name, version)
	}
	if node.Name != name || node.Version != version {
		return nil, ResolvedNodeBinding{}, fmt.Errorf("workflow: resolver returned a different node for %s@%d", name, version)
	}
	if err := validateNodeInvocationMappings(node.Compiled, ref); err != nil {
		return nil, ResolvedNodeBinding{}, err
	}
	function, err := node.Compiled.Instantiate(ref.Parameters)
	if err != nil {
		return nil, ResolvedNodeBinding{}, err
	}
	step := function.Plan[0]
	if err := applyNodeInvocation(&step, ref); err != nil {
		return nil, ResolvedNodeBinding{}, err
	}
	expander.frozen[ref.InstanceName] = frozenNodeAssets{
		skillFiles: cloneStringMap(function.SkillFiles),
		profiles:   cloneCompiledDevValidationProfiles(function.DevValidationProfiles),
	}
	replacement, err := stepToSource(step)
	if err != nil {
		return nil, ResolvedNodeBinding{}, err
	}
	for key, value := range modifiers {
		if _, collision := replacement[key]; collision {
			return nil, ResolvedNodeBinding{}, fmt.Errorf("workflow: %s: node modifier %q collides with resolved leaf", path, key)
		}
		replacement[key] = value
	}
	expander.instances[ref.InstanceName] = struct{}{}
	return replacement, ResolvedNodeBinding{
		InstanceName: ref.InstanceName, NodeDefinitionID: node.ID, NodeName: node.Name, NodeVersion: node.Version, NodeContentHash: node.ContentHash,
		InputMapping: cloneStringMap(ref.InputMapping), OutputMapping: cloneStringMap(ref.OutputMapping), Parameters: cloneStringMap(ref.Parameters),
	}, nil
}

func splitNodeReferenceStep(source map[string]any, path string) (map[string]any, map[string]any, error) {
	reference := map[string]any{}
	modifiers := map[string]any{}
	for key, value := range source {
		switch key {
		case "node", "uses", "input_mapping", "output_mapping", "params":
			reference[key] = value
		case "attempts", "timeout", "across", "on_success", "on_failure", "on_abort", "on_error", "ensure":
			modifiers[key] = value
		default:
			return nil, nil, fmt.Errorf("workflow: %s: node reference cannot override core field %q", path, key)
		}
	}
	return reference, modifiers, nil
}

func decodeNodeReference(source map[string]any) (NodeReference, error) {
	payload, err := json.Marshal(source)
	if err != nil {
		return NodeReference{}, err
	}
	var ref NodeReference
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ref); err != nil {
		return NodeReference{}, fmt.Errorf("invalid node reference: %w", err)
	}
	if strings.TrimSpace(ref.InstanceName) == "" {
		return NodeReference{}, fmt.Errorf("node is required")
	}
	if strings.TrimSpace(ref.Uses) == "" {
		return NodeReference{}, fmt.Errorf("uses is required")
	}
	return ref, nil
}

func parseExactNodeUse(value string) (string, int, error) {
	parts := strings.Split(value, "@")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", 0, fmt.Errorf("workflow: node uses must be name@exact-integer-version, got %q", value)
	}
	if err := validateSafeIdentifier(parts[0], "node name"); err != nil {
		return "", 0, err
	}
	if strings.HasPrefix(parts[1], "+") || strings.HasPrefix(parts[1], "-") || (len(parts[1]) > 1 && strings.HasPrefix(parts[1], "0")) {
		return "", 0, fmt.Errorf("workflow: node uses must use a positive exact integer version, got %q", value)
	}
	version, err := strconv.Atoi(parts[1])
	if err != nil || version <= 0 {
		return "", 0, fmt.Errorf("workflow: node uses must use a positive exact integer version, got %q", value)
	}
	return parts[0], version, nil
}

func validateNodeInvocationMappings(node CompiledNodeDefinition, ref NodeReference) error {
	if err := validateNodeInputMapping(node.Function.Inputs, ref.InputMapping); err != nil {
		return err
	}
	outputs := make([]string, len(node.Function.Outputs))
	for index, output := range node.Function.Outputs {
		outputs[index] = output.Name
	}
	if err := validateNodePortMapping("output_mapping", outputs, ref.OutputMapping); err != nil {
		return err
	}
	return nil
}

func validateNodeInputMapping(ports []snapshot.Port, mapping map[string]string) error {
	names := make([]string, len(ports))
	for index, port := range ports {
		names[index] = port.Name
	}
	if err := validateNodePortMapping("input_mapping", names, mapping); err != nil {
		return err
	}
	for _, port := range ports {
		if _, found := mapping[port.Name]; !found && !port.Optional {
			return fmt.Errorf("workflow: node input_mapping is missing required port %q", port.Name)
		}
	}
	return nil
}

func validateNodePortMapping(label string, names []string, mapping map[string]string) error {
	declared := make(map[string]struct{}, len(names))
	for _, name := range names {
		declared[name] = struct{}{}
	}
	artifacts := map[string]string{}
	for name, artifact := range mapping {
		if _, found := declared[name]; !found {
			return fmt.Errorf("workflow: node %s has unknown port %q", label, name)
		}
		if err := validateSafeIdentifier(artifact, "mapped artifact name"); err != nil {
			return err
		}
		if previous, duplicate := artifacts[artifact]; duplicate {
			return fmt.Errorf("workflow: node %s maps both %q and %q to artifact %q", label, previous, name, artifact)
		}
		artifacts[artifact] = name
	}
	return nil
}

func applyNodeInvocation(step *atc.Step, ref NodeReference) error {
	switch leaf := step.Config.(type) {
	case *atc.TaskStep:
		leaf.FunctionID = ref.InstanceName
		leaf.InputMapping = cloneStringMap(ref.InputMapping)
		leaf.OutputMapping = cloneStringMap(ref.OutputMapping)
		if leaf.Config != nil {
			leaf.Config.Inputs = filterTaskInputs(leaf.Config.Inputs, ref.InputMapping)
			leaf.Config.Outputs = filterTaskOutputs(leaf.Config.Outputs, ref.OutputMapping)
		}
		// Native task planning/type checking consumes typed declarations by the
		// physical artifact name after applying mappings. Agents intentionally
		// retain logical declaration keys because their executor translates at
		// the repository/sealer boundary.
		leaf.SnapshotInputs = mappedSnapshotInputs(filterSnapshotInputs(leaf.SnapshotInputs, ref.InputMapping), ref.InputMapping)
		leaf.SnapshotOutputs = mappedSnapshotOutputs(filterSnapshotOutputs(leaf.SnapshotOutputs, ref.OutputMapping), ref.OutputMapping)
	case *atc.AgentStep:
		leaf.FunctionID = ref.InstanceName
		leaf.InputMapping = cloneStringMap(ref.InputMapping)
		leaf.OutputMapping = cloneStringMap(ref.OutputMapping)
		leaf.Inputs = filterNodePortNames(leaf.Inputs, ref.InputMapping)
		leaf.Outputs = filterNodePortNames(leaf.Outputs, ref.OutputMapping)
		leaf.SnapshotInputs = filterSnapshotInputs(leaf.SnapshotInputs, ref.InputMapping)
		leaf.SnapshotOutputs = filterSnapshotOutputs(leaf.SnapshotOutputs, ref.OutputMapping)
	case *atc.PublishSnapshotStep:
		if len(ref.OutputMapping) != 0 || len(ref.Parameters) != 0 {
			return fmt.Errorf("workflow: publish_snapshot nodes do not accept output mappings or parameters")
		}
		for _, input := range ref.InputMapping {
			leaf.Input = input
		}
		leaf.Name = ref.InstanceName
	default:
		return fmt.Errorf("workflow: node compiled to unsupported leaf %T", step.Config)
	}
	return nil
}

func filterTaskInputs(source []atc.TaskInputConfig, mapping map[string]string) []atc.TaskInputConfig {
	if source == nil {
		return nil
	}
	filtered := make([]atc.TaskInputConfig, 0, len(mapping))
	for _, input := range source {
		if _, composed := mapping[input.Name]; composed {
			filtered = append(filtered, input)
		}
	}
	return filtered
}

func filterTaskOutputs(source []atc.TaskOutputConfig, mapping map[string]string) []atc.TaskOutputConfig {
	if source == nil {
		return nil
	}
	filtered := make([]atc.TaskOutputConfig, 0, len(mapping))
	for _, output := range source {
		if _, composed := mapping[output.Name]; composed {
			filtered = append(filtered, output)
		}
	}
	return filtered
}

func filterNodePortNames(source []string, mapping map[string]string) []string {
	if source == nil {
		return nil
	}
	filtered := make([]string, 0, len(mapping))
	for _, name := range source {
		if _, composed := mapping[name]; composed {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

func filterSnapshotInputs(source map[string]atc.SnapshotInputConfig, mapping map[string]string) map[string]atc.SnapshotInputConfig {
	if source == nil {
		return nil
	}
	filtered := make(map[string]atc.SnapshotInputConfig, len(mapping))
	for name := range mapping {
		if config, found := source[name]; found {
			filtered[name] = config
		}
	}
	return filtered
}

func filterSnapshotOutputs(source map[string]atc.SnapshotOutputConfig, mapping map[string]string) map[string]atc.SnapshotOutputConfig {
	if source == nil {
		return nil
	}
	filtered := make(map[string]atc.SnapshotOutputConfig, len(mapping))
	for name := range mapping {
		if config, found := source[name]; found {
			filtered[name] = config
		}
	}
	return filtered
}

func stepToSource(step atc.Step) (map[string]any, error) {
	payload, err := json.Marshal(step)
	if err != nil {
		return nil, err
	}
	var source map[string]any
	if err := json.Unmarshal(payload, &source); err != nil {
		return nil, err
	}
	return source, nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func mergeFrozenSkillFiles(function *FunctionConfig, frozen map[string]frozenNodeAssets) error {
	for instance, assets := range frozen {
		for path, content := range assets.skillFiles {
			if existing, found := function.SkillFiles[path]; found && existing != content {
				return fmt.Errorf("workflow: node %q conflicts on compiled skill file %q", instance, path)
			}
			if function.SkillFiles == nil {
				function.SkillFiles = map[string]string{}
			}
			function.SkillFiles[path] = content
		}
	}
	return nil
}

func mergeFrozenDevValidationProfiles(function *FunctionConfig, frozen map[string]frozenNodeAssets) error {
	profiles := append([]CompiledDevValidationProfile(nil), function.DevValidationProfiles...)
	for instance, assets := range frozen {
		for _, profile := range assets.profiles {
			matched := false
			for _, existing := range profiles {
				if existing.Name != profile.Name {
					continue
				}
				matched = true
				left, _ := json.Marshal(existing)
				right, _ := json.Marshal(profile)
				if !bytes.Equal(left, right) {
					return fmt.Errorf("workflow: node %q conflicts on compiled dev validation profile %q", instance, profile.Name)
				}
			}
			if !matched {
				profiles = append(profiles, profile)
			}
		}
	}
	if len(profiles) == 0 {
		return nil
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	provenance, err := DevValidationProvenanceHash(profiles)
	if err != nil {
		return err
	}
	function.DevValidationProfiles = profiles
	function.DevValidationProvenanceHash = provenance
	return nil
}
