package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/goccy/go-yaml"
)

// ParseCompiled accepts only the durable compiled JSON model. Human source
// manifests must enter through CompileDefinition so referenced authority is
// resolved and frozen before any consumer can observe it.
func ParseCompiled(raw []byte) (*CompiledDefinition, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var definition CompiledDefinition
	if err := decoder.Decode(&definition); err != nil {
		return nil, fmt.Errorf("parse compiled workflow definition: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse compiled workflow definition: exactly one JSON value is required")
		}
		return nil, fmt.Errorf("parse compiled workflow definition trailing JSON: %w", err)
	}
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	return &definition, nil
}

// ParseCompiledNodeDefinition accepts only the durable compiled reusable-node
// JSON model. Source manifests must enter through CompileNodeDefinition so
// compiler-owned assets and broker authority are frozen before persistence.
func ParseCompiledNodeDefinition(raw []byte) (*CompiledNodeDefinition, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var definition CompiledNodeDefinition
	if err := decoder.Decode(&definition); err != nil {
		return nil, fmt.Errorf("parse compiled node definition: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse compiled node definition: exactly one JSON value is required")
		}
		return nil, fmt.Errorf("parse compiled node definition trailing JSON: %w", err)
	}
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	return &definition, nil
}

func RequireSchemaVersion3(source []byte) error {
	version, err := parseSchemaVersion(source)
	if err != nil {
		return err
	}
	if version != 3 {
		return UnsupportedSchemaVersionError{Got: version}
	}
	return nil
}

func parseSchemaVersion(raw []byte) (int, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		if err == io.EOF {
			return 0, fmt.Errorf("parse workflow definition: empty document")
		}
		return 0, fmt.Errorf("parse workflow definition discriminator: %w", err)
	}
	value, found := document["schema_version"]
	if !found {
		return 0, fmt.Errorf("workflow: schema_version is required")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0, fmt.Errorf("workflow: schema_version must be an integer")
	}
	var version int
	if err := json.Unmarshal(encoded, &version); err != nil {
		return 0, fmt.Errorf("workflow: schema_version must be an integer")
	}
	return version, nil
}

type functionSource struct {
	SchemaVersion         int                    `json:"schema_version"`
	Name                  string                 `json:"name"`
	SignatureVersion      int                    `json:"signature_version"`
	DispositionOutput     string                 `json:"disposition_output,omitempty"`
	Description           string                 `json:"description,omitempty"`
	Inputs                []snapshot.Port        `json:"inputs"`
	Outputs               []FunctionOutput       `json:"outputs"`
	Capabilities          map[string]Capability  `json:"capabilities,omitempty"`
	DevValidationProfiles []DevValidationProfile `json:"dev_validation_profiles,omitempty"`
	Resources             any                    `json:"resources,omitempty"`
	ResourceSources       []ResourceSource       `json:"resource_sources,omitempty"`
	ResourceTypes         any                    `json:"resource_types,omitempty"`
	Prototypes            any                    `json:"prototypes,omitempty"`
	VarSources            any                    `json:"var_sources,omitempty"`
	Plan                  any                    `json:"plan"`
}

type syntheticFunctionConfig struct {
	VarSources    any                    `yaml:"var_sources,omitempty"`
	Resources     any                    `yaml:"resources,omitempty"`
	ResourceTypes any                    `yaml:"resource_types,omitempty"`
	Prototypes    any                    `yaml:"prototypes,omitempty"`
	Jobs          []syntheticFunctionJob `yaml:"jobs"`
}

type syntheticFunctionJob struct {
	Name string `yaml:"name"`
	Plan any    `yaml:"plan"`
}

func parseFunctionDefinitionSource(raw []byte) (*CompiledDefinition, []DevValidationProfile, []sourceBrokerProfile, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, nil, nil, fmt.Errorf("parse workflow function: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, nil, nil, fmt.Errorf("parse workflow function: exactly one YAML or JSON document is required")
		}
		return nil, nil, nil, fmt.Errorf("parse workflow function trailing document: %w", err)
	}
	if err := validateFunctionSourceKeys(document); err != nil {
		return nil, nil, nil, err
	}
	documentJSON, err := json.Marshal(document)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse workflow function document: %w", err)
	}
	jsonDecoder := json.NewDecoder(bytes.NewReader(documentJSON))
	jsonDecoder.DisallowUnknownFields()
	var source functionSource
	if err := jsonDecoder.Decode(&source); err != nil {
		return nil, nil, nil, fmt.Errorf("parse workflow function: %w", err)
	}
	if err := jsonDecoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, nil, nil, fmt.Errorf("parse workflow function: unexpected trailing JSON value")
		}
		return nil, nil, nil, fmt.Errorf("parse workflow function trailing JSON: %w", err)
	}
	if source.SchemaVersion != 3 {
		return nil, nil, nil, fmt.Errorf("workflow: function parser requires schema_version 3, got %d", source.SchemaVersion)
	}
	brokerProfiles, err := extractSourceBrokerProfiles(source.Plan, "workflow.plan")
	if err != nil {
		return nil, nil, nil, err
	}
	synthetic := syntheticFunctionConfig{
		VarSources:    source.VarSources,
		Resources:     source.Resources,
		ResourceTypes: source.ResourceTypes,
		Prototypes:    source.Prototypes,
		Jobs: []syntheticFunctionJob{{
			Name: "agent-function",
			Plan: source.Plan,
		}},
	}
	payload, err := yaml.Marshal(synthetic)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("workflow: encode ordinary Concourse plan: %w", err)
	}
	var ordinary atc.Config
	if err := atc.UnmarshalConfig(payload, &ordinary); err != nil {
		return nil, nil, nil, fmt.Errorf("workflow: decode ordinary Concourse declarations and plan: %w", err)
	}
	if len(ordinary.Jobs) != 1 {
		return nil, nil, nil, fmt.Errorf("workflow: internal function plan must decode as exactly one job")
	}
	definition := &CompiledDefinition{
		SchemaVersion: source.SchemaVersion,
		Name:          source.Name,
		Description:   source.Description,
		Function: &FunctionConfig{
			SignatureVersion:  source.SignatureVersion,
			DispositionOutput: source.DispositionOutput,
			Inputs:            source.Inputs,
			Outputs:           source.Outputs,
			Capabilities:      source.Capabilities,
			Resources:         ordinary.Resources,
			ResourceSources:   source.ResourceSources,
			ResourceTypes:     ordinary.ResourceTypes,
			Prototypes:        ordinary.Prototypes,
			VarSources:        ordinary.VarSources,
			Plan:              ordinary.Jobs[0].PlanSequence,
		},
	}
	if err := definition.Validate(); err != nil {
		return nil, nil, nil, err
	}
	return definition, source.DevValidationProfiles, brokerProfiles, nil
}

func validateFunctionSourceKeys(document any) error {
	root, ok := document.(map[string]any)
	if !ok {
		return nil // the typed JSON pass reports the shape error
	}
	if err := rejectObjectKeys(root, "workflow", []string{
		"schema_version", "name", "signature_version", "disposition_output", "description", "inputs", "outputs",
		"capabilities", "resources", "resource_sources", "resource_types", "prototypes", "var_sources", "plan", "dev_validation_profiles",
	}); err != nil {
		return err
	}
	if resourceSources, ok := root["resource_sources"].([]any); ok {
		for index, resourceSource := range resourceSources {
			object, ok := resourceSource.(map[string]any)
			if !ok {
				continue
			}
			if _, found := object["passed"]; found {
				return fmt.Errorf("workflow: resource sources do not support passed constraints")
			}
			if err := rejectObjectKeys(object, fmt.Sprintf("workflow.resource_sources[%d]", index), []string{"name", "resource", "type", "trigger", "version"}); err != nil {
				return err
			}
		}
	}
	if profiles, ok := root["dev_validation_profiles"].([]any); ok {
		for index, profile := range profiles {
			if object, ok := profile.(map[string]any); ok {
				path := fmt.Sprintf("workflow.dev_validation_profiles[%d]", index)
				if err := rejectObjectKeys(object, path, []string{"name", "capability", "profile_file", "config_file", "candidate", "base_inputs"}); err != nil {
					return err
				}
				if candidate, ok := object["candidate"].(map[string]any); ok {
					if err := rejectObjectKeys(candidate, path+".candidate", []string{"name", "type"}); err != nil {
						return err
					}
				}
				if bases, ok := object["base_inputs"].([]any); ok {
					for baseIndex, base := range bases {
						if object, ok := base.(map[string]any); ok {
							if err := rejectObjectKeys(object, fmt.Sprintf("%s.base_inputs[%d]", path, baseIndex), []string{"name", "type"}); err != nil {
								return err
							}
						}
					}
				}
			}
		}
	}
	if inputs, ok := root["inputs"].([]any); ok {
		for index, input := range inputs {
			if object, ok := input.(map[string]any); ok {
				if err := rejectObjectKeys(object, fmt.Sprintf("workflow.inputs[%d]", index), []string{"name", "type", "optional", "description"}); err != nil {
					return err
				}
			}
		}
	}
	if outputs, ok := root["outputs"].([]any); ok {
		for index, output := range outputs {
			if object, ok := output.(map[string]any); ok {
				if err := rejectObjectKeys(object, fmt.Sprintf("workflow.outputs[%d]", index), []string{"name", "type", "optional", "description", "from"}); err != nil {
					return err
				}
			}
		}
	}
	if plan, found := root["plan"]; found {
		if err := validateFunctionPlanSource(plan, "workflow.plan"); err != nil {
			return err
		}
	}
	capabilities, ok := root["capabilities"].(map[string]any)
	if !ok {
		return nil
	}
	for name, value := range capabilities {
		capability, ok := value.(map[string]any)
		if !ok {
			continue
		}
		path := fmt.Sprintf("workflow.capabilities[%q]", name)
		if err := rejectObjectKeys(capability, path, []string{"contract", "sidecar"}); err != nil {
			return err
		}
		if sidecar, ok := capability["sidecar"].(map[string]any); ok {
			if err := validateSidecarSourceKeys(sidecar, path+".sidecar"); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSidecarSourceKeys(sidecar map[string]any, path string) error {
	return validateInlineSidecarSource(sidecar, path)
}

func rejectObjectKeys(object map[string]any, path string, allowedFields []string) error {
	allowed := make(map[string]struct{}, len(allowedFields))
	for _, field := range allowedFields {
		allowed[field] = struct{}{}
	}
	unknown := make([]string, 0)
	for field := range object {
		if _, found := allowed[field]; !found {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("workflow: %s: unknown field %q", path, unknown[0])
}

// validateFunctionPlanSource closes the ATC-owned object shapes before the
// ordinary Concourse decoder runs. Step.UnmarshalJSON deliberately preserves
// compatibility by recording unknown step-envelope fields, and several
// nested configs use encoding/json, which otherwise drops unknown fields (and
// matches field names case-insensitively). Version 3 is strict, so it audits
// those raw objects while leaving provider-owned maps such as resource params,
// image sources, vars, and across values opaque.
func validateFunctionPlanSource(plan any, path string) error {
	steps, ok := plan.([]any)
	if !ok {
		return nil // the ordinary ATC pass reports the shape error
	}
	for index, step := range steps {
		if err := validateFunctionStepSource(step, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateFunctionStepSource(value any, path string) error {
	step, ok := value.(map[string]any)
	if !ok {
		return nil // the ordinary ATC pass reports the shape error
	}
	if _, isAgent := step["agent"]; isAgent {
		if err := validateAgentAssetSourcePresence(step, path); err != nil {
			return err
		}
		if profiles, found := step["broker_profiles"]; found {
			if _, err := parseSourceBrokerProfiles(profiles, rawAgentFunctionID(step), path+".broker_profiles"); err != nil {
				return err
			}
		}
	}

	if nested, found := step["try"]; found {
		if err := validateFunctionStepSource(nested, path+".try"); err != nil {
			return err
		}
	}
	if nested, found := step["do"]; found {
		if err := validateFunctionStepListSource(nested, path+".do"); err != nil {
			return err
		}
	}
	if nested, found := step["in_parallel"]; found {
		if err := validateInParallelSource(nested, path+".in_parallel"); err != nil {
			return err
		}
	}
	for _, hook := range []string{"on_success", "on_failure", "on_abort", "on_error", "ensure"} {
		if nested, found := step[hook]; found {
			if err := validateFunctionStepSource(nested, path+"."+hook); err != nil {
				return err
			}
		}
	}

	if across, found := step["across"]; found {
		if err := validateObjectListSource(across, path+".across", []string{"var", "values", "max_in_flight"}); err != nil {
			return err
		}
	}

	if _, isTask := step["task"]; isTask {
		if config, found := step["config"]; found {
			if err := validateTaskConfigSource(config, path+".config"); err != nil {
				return err
			}
		}
	}

	for _, field := range []string{"container_limits", "container_requests"} {
		if limits, found := step[field]; found {
			if err := validateObjectSource(limits, path+"."+field, []string{"cpu", "memory", "ephemeral_storage"}); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"input_types", "output_types"} {
		if configs, found := step[field]; found {
			if err := validateSnapshotTypeMapSource(configs, path+"."+field, field == "output_types"); err != nil {
				return err
			}
		}
	}
	if sidecars, found := step["sidecars"]; found {
		if err := validateSidecarListSource(sidecars, path+".sidecars"); err != nil {
			return err
		}
	}
	if devMCP, found := step["dev_mcp"]; found {
		if err := validateInlineSidecarSource(devMCP, path+".dev_mcp"); err != nil {
			return err
		}
	}

	return nil
}

type sourceBrokerProfile struct {
	FunctionID string
	Tool       broker.Tool
	Selector   broker.Selector
}

func extractSourceBrokerProfiles(value any, path string) ([]sourceBrokerProfile, error) {
	var profiles []sourceBrokerProfile
	if err := visitSourceBrokerProfiles(value, path, &profiles); err != nil {
		return nil, err
	}
	return profiles, nil
}

func visitSourceBrokerProfiles(value any, path string, profiles *[]sourceBrokerProfile) error {
	switch typed := value.(type) {
	case []any:
		for index, child := range typed {
			if err := visitSourceBrokerProfiles(child, fmt.Sprintf("%s[%d]", path, index), profiles); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if _, isAgent := typed["agent"]; isAgent {
			if raw, found := typed["broker_profiles"]; found {
				selected, err := parseSourceBrokerProfiles(raw, rawAgentFunctionID(typed), path+".broker_profiles")
				if err != nil {
					return err
				}
				*profiles = append(*profiles, selected...)
				delete(typed, "broker_profiles")
			}
		}
		if nested, found := typed["try"]; found {
			if err := visitSourceBrokerProfiles(nested, path+".try", profiles); err != nil {
				return err
			}
		}
		if nested, found := typed["do"]; found {
			if err := visitSourceBrokerProfiles(nested, path+".do", profiles); err != nil {
				return err
			}
		}
		if nested, found := typed["in_parallel"]; found {
			switch parallel := nested.(type) {
			case []any:
				if err := visitSourceBrokerProfiles(parallel, path+".in_parallel", profiles); err != nil {
					return err
				}
			case map[string]any:
				if steps, found := parallel["steps"]; found {
					if err := visitSourceBrokerProfiles(steps, path+".in_parallel.steps", profiles); err != nil {
						return err
					}
				}
			}
		}
		for _, hook := range []string{"on_success", "on_failure", "on_abort", "on_error", "ensure"} {
			if nested, found := typed[hook]; found {
				if err := visitSourceBrokerProfiles(nested, path+"."+hook, profiles); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func parseSourceBrokerProfiles(value any, functionID, path string) ([]sourceBrokerProfile, error) {
	if strings.TrimSpace(functionID) == "" {
		return nil, fmt.Errorf("workflow: %s: broker_profiles requires a nonblank function_id", path)
	}
	entries, ok := value.([]any)
	if !ok || len(entries) == 0 {
		return nil, fmt.Errorf("workflow: %s must be a nonempty list", path)
	}
	profiles := make([]sourceBrokerProfile, 0, len(entries))
	for index, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("workflow: %s[%d] must be an object", path, index)
		}
		entryPath := fmt.Sprintf("%s[%d]", path, index)
		if err := rejectObjectKeys(object, entryPath, []string{"tool", "tier", "effort"}); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(object)
		if err != nil {
			return nil, fmt.Errorf("workflow: %s: encode broker selector: %w", entryPath, err)
		}
		var selector BrokerProfileSelector
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&selector); err != nil {
			return nil, fmt.Errorf("workflow: %s: parse broker selector: %w", entryPath, err)
		}
		profiles = append(profiles, sourceBrokerProfile{FunctionID: functionID, Tool: selector.Tool, Selector: broker.Selector{Tier: selector.Tier, Effort: selector.Effort}})
	}
	return profiles, nil
}

func rawAgentFunctionID(step map[string]any) string {
	functionID, ok := step["function_id"].(string)
	if !ok || strings.TrimSpace(functionID) == "" {
		return ""
	}
	return functionID
}

func validateAgentAssetSourcePresence(step map[string]any, path string) error {
	identity := rawAgentIdentity(step)
	_, promptPresent := step["prompt"]
	_, promptFilePresent := step["prompt_file"]
	if promptPresent && promptFilePresent {
		return fmt.Errorf("workflow: %s: %s: prompt and prompt_file are mutually exclusive", path, identity)
	}
	if promptFilePresent {
		if err := validateNonemptyStringSourceField(step["prompt_file"], "prompt_file"); err != nil {
			return fmt.Errorf("workflow: %s: %s: %w", path, identity, err)
		}
	}

	_, systemPromptPresent := step["system_prompt"]
	_, systemPromptFilePresent := step["system_prompt_file"]
	if systemPromptPresent && systemPromptFilePresent {
		return fmt.Errorf("workflow: %s: %s: system_prompt and system_prompt_file are mutually exclusive", path, identity)
	}
	if systemPromptFilePresent {
		if err := validateNonemptyStringSourceField(step["system_prompt_file"], "system_prompt_file"); err != nil {
			return fmt.Errorf("workflow: %s: %s: %w", path, identity, err)
		}
	}
	return nil
}

func validateNonemptyStringSourceField(value any, field string) error {
	text, ok := value.(string)
	if !ok || text == "" {
		return fmt.Errorf("%s must be a nonempty string", field)
	}
	return nil
}

func rawAgentIdentity(step map[string]any) string {
	name, _ := step["agent"].(string)
	identity := fmt.Sprintf("agent %q", name)
	if functionID, ok := step["function_id"].(string); ok && functionID != "" {
		identity += fmt.Sprintf(" (function_id %q)", functionID)
	}
	return identity
}

func validateFunctionStepListSource(value any, path string) error {
	steps, ok := value.([]any)
	if !ok {
		return nil
	}
	for index, step := range steps {
		if err := validateFunctionStepSource(step, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateInParallelSource(value any, path string) error {
	switch config := value.(type) {
	case []any:
		return validateFunctionStepListSource(config, path)
	case map[string]any:
		if err := rejectObjectKeys(config, path, []string{"steps", "limit", "fail_fast"}); err != nil {
			return err
		}
		if steps, found := config["steps"]; found {
			return validateFunctionStepListSource(steps, path+".steps")
		}
	}
	return nil
}

func validateTaskConfigSource(value any, path string) error {
	config, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if err := rejectObjectKeys(config, path, []string{
		"platform", "rootfs_uri", "image_resource", "container_limits", "container_requests",
		"params", "run", "inputs", "outputs", "caches", "scratch_paths",
	}); err != nil {
		return err
	}
	if image, found := config["image_resource"]; found {
		if err := validateObjectSource(image, path+".image_resource", []string{"name", "type", "source", "version", "params", "tags"}); err != nil {
			return err
		}
	}
	for _, field := range []string{"container_limits", "container_requests"} {
		if limits, found := config[field]; found {
			if err := validateObjectSource(limits, path+"."+field, []string{"cpu", "memory", "ephemeral_storage"}); err != nil {
				return err
			}
		}
	}
	if run, found := config["run"]; found {
		if err := validateObjectSource(run, path+".run", []string{"path", "args", "dir", "user"}); err != nil {
			return err
		}
	}
	for _, list := range []struct {
		field   string
		allowed []string
	}{
		{field: "inputs", allowed: []string{"name", "path", "optional"}},
		{field: "outputs", allowed: []string{"name", "path"}},
		{field: "caches", allowed: []string{"path"}},
		{field: "scratch_paths", allowed: []string{"path"}},
	} {
		if entries, found := config[list.field]; found {
			if err := validateObjectListSource(entries, path+"."+list.field, list.allowed); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSnapshotTypeMapSource(value any, path string, output bool) error {
	configs, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if output {
		for name, value := range configs {
			if _, ok := value.(string); ok {
				continue
			}
			if _, ok := value.(map[string]any); !ok {
				return fmt.Errorf("workflow: %s[%q]: output type must be a type-reference string or an object containing type and optional", path, name)
			}
			if err := validateObjectSource(value, fmt.Sprintf("%s[%q]", path, name), []string{"type", "optional"}); err != nil {
				return err
			}
		}
		return nil
	}

	// candidate is an input-only port declaration: it marks an alternative a
	// selecting step may choose between. Outputs have no candidate notion, so the
	// key stays out of the output allowlist above.
	for name, value := range configs {
		if err := validateObjectSource(value, fmt.Sprintf("%s[%q]", path, name), []string{"type", "optional", "candidate"}); err != nil {
			return err
		}
	}
	return nil
}

func validateSidecarListSource(value any, path string) error {
	entries, ok := value.([]any)
	if !ok {
		return nil
	}
	for index, entry := range entries {
		if err := validateInlineSidecarSource(entry, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateInlineSidecarSource(value any, path string) error {
	sidecar, ok := value.(map[string]any)
	if !ok {
		return nil // string file references and typed-shape errors go to ATC
	}
	if err := rejectObjectKeys(sidecar, path, []string{
		"name", "image", "command", "args", "env", "ports", "resources", "workingDir", "image_artifact",
	}); err != nil {
		return err
	}
	if env, found := sidecar["env"]; found {
		if err := validateObjectListSource(env, path+".env", []string{"name", "value"}); err != nil {
			return err
		}
	}
	if ports, found := sidecar["ports"]; found {
		if err := validateObjectListSource(ports, path+".ports", []string{"containerPort", "protocol"}); err != nil {
			return err
		}
	}
	resources, ok := sidecar["resources"].(map[string]any)
	if !ok {
		return nil
	}
	if err := rejectObjectKeys(resources, path+".resources", []string{"requests", "limits"}); err != nil {
		return err
	}
	for _, field := range []string{"requests", "limits"} {
		if quantities, found := resources[field]; found {
			if err := validateObjectSource(quantities, path+".resources."+field, []string{"cpu", "memory"}); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateObjectSource(value any, path string, allowed []string) error {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return rejectObjectKeys(object, path, allowed)
}

func validateObjectListSource(value any, path string, allowed []string) error {
	entries, ok := value.([]any)
	if !ok {
		return nil
	}
	for index, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		entryPath := fmt.Sprintf("%s[%d]", path, index)
		if err := rejectObjectKeys(object, entryPath, allowed); err != nil {
			return err
		}
	}
	return nil
}

func rejectUnknownPlanFields(steps []atc.Step) error {
	for index := range steps {
		if err := rejectUnknownStepFields(steps[index], fmt.Sprintf("plan[%d]", index)); err != nil {
			return err
		}
	}
	return nil
}

func rejectUnknownStepFields(step atc.Step, path string) error {
	if len(step.UnknownFields) > 0 {
		fields := make([]string, 0, len(step.UnknownFields))
		for field := range step.UnknownFields {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		return fmt.Errorf("workflow: %s: unknown step field %q", path, fields[0])
	}
	return rejectUnknownStepConfigFields(step.Config, path)
}

func rejectUnknownStepConfigFields(config atc.StepConfig, path string) error {
	switch step := config.(type) {
	case *atc.TryStep:
		return rejectUnknownStepFields(step.Step, path+".try")
	case *atc.DoStep:
		for index := range step.Steps {
			if err := rejectUnknownStepFields(step.Steps[index], fmt.Sprintf("%s.do[%d]", path, index)); err != nil {
				return err
			}
		}
	case *atc.InParallelStep:
		for index := range step.Config.Steps {
			if err := rejectUnknownStepFields(step.Config.Steps[index], fmt.Sprintf("%s.in_parallel[%d]", path, index)); err != nil {
				return err
			}
		}
	case *atc.AcrossStep:
		return rejectUnknownStepConfigFields(step.Step, path+".across")
	case *atc.RetryStep:
		return rejectUnknownStepConfigFields(step.Step, path+".attempts")
	case *atc.TimeoutStep:
		return rejectUnknownStepConfigFields(step.Step, path+".timeout")
	case *atc.OnSuccessStep:
		if err := rejectUnknownStepConfigFields(step.Step, path+".step"); err != nil {
			return err
		}
		return rejectUnknownStepFields(step.Hook, path+".on_success")
	case *atc.OnFailureStep:
		if err := rejectUnknownStepConfigFields(step.Step, path+".step"); err != nil {
			return err
		}
		return rejectUnknownStepFields(step.Hook, path+".on_failure")
	case *atc.OnAbortStep:
		if err := rejectUnknownStepConfigFields(step.Step, path+".step"); err != nil {
			return err
		}
		return rejectUnknownStepFields(step.Hook, path+".on_abort")
	case *atc.OnErrorStep:
		if err := rejectUnknownStepConfigFields(step.Step, path+".step"); err != nil {
			return err
		}
		return rejectUnknownStepFields(step.Hook, path+".on_error")
	case *atc.EnsureStep:
		if err := rejectUnknownStepConfigFields(step.Step, path+".step"); err != nil {
			return err
		}
		return rejectUnknownStepFields(step.Hook, path+".ensure")
	}
	return nil
}
