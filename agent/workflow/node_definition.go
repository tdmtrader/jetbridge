package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/concourse/concourse/atc"
)

// NodeParameter is a caller-supplied string value for an atomic reusable
// node. An omitted Default makes the parameter required.
type NodeParameter struct {
	Name    string  `json:"name" yaml:"name"`
	Default *string `json:"default,omitempty" yaml:"default,omitempty"`
}

// CompiledNodeDefinition is the immutable, one-leaf implementation of a
// reusable node. Function intentionally retains the complete compiled
// function so compiler-owned assets and authority remain in their existing
// validated representation.
type CompiledNodeDefinition struct {
	SchemaVersion int             `json:"schema_version" yaml:"schema_version"`
	Name          string          `json:"name" yaml:"name"`
	Description   string          `json:"description,omitempty" yaml:"description,omitempty"`
	Parameters    []NodeParameter `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	Function      FunctionConfig  `json:"function" yaml:"function"`
}

// Validate checks the durable node model independently of ordinary Concourse
// execution validation, which source compilation runs after asset freezing.
func (definition CompiledNodeDefinition) Validate() error {
	return definition.validate(false)
}

func (definition CompiledNodeDefinition) validate(allowSourceCapabilities bool) error {
	if definition.SchemaVersion != 1 {
		return fmt.Errorf("workflow: node schema_version must be 1, got %d", definition.SchemaVersion)
	}
	if strings.TrimSpace(definition.Name) == "" {
		return fmt.Errorf("workflow: node name is required")
	}
	if err := validateNodeParameters(definition.Parameters); err != nil {
		return err
	}
	if !allowSourceCapabilities && len(definition.Function.Capabilities) != 0 {
		return fmt.Errorf("workflow: compiled node retains source capabilities")
	}
	if err := definition.Function.Validate(); err != nil {
		return err
	}
	if len(definition.Function.Plan) != 1 {
		return fmt.Errorf("workflow: node function must contain exactly one atomic leaf")
	}
	switch definition.Function.Plan[0].Config.(type) {
	case *atc.TaskStep, *atc.AgentStep:
	case *atc.PublishSnapshotStep:
		if len(definition.Parameters) != 0 {
			return fmt.Errorf("workflow: publish_snapshot nodes cannot declare caller-supplied parameters")
		}
	default:
		return fmt.Errorf("workflow: node function must contain one permitted leaf (task, agent, or publish_snapshot)")
	}
	for index, output := range definition.Function.Outputs {
		if output.From != output.Name {
			return fmt.Errorf("workflow: node output %d must map from its logical name", index)
		}
	}
	return nil
}

// Instantiate returns an independent compiled function with the supplied
// values injected solely into the ordinary parameter surface of its leaf.
func (definition CompiledNodeDefinition) Instantiate(values map[string]string) (*FunctionConfig, error) {
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	resolved, err := resolveNodeParameters(definition.Parameters, values)
	if err != nil {
		return nil, err
	}
	function, err := cloneNodeFunction(definition.Function)
	if err != nil {
		return nil, err
	}
	switch step := function.Plan[0].Config.(type) {
	case *atc.TaskStep:
		step.Params = cloneTaskEnv(step.Params)
		for name, value := range resolved {
			step.Params[name] = value
		}
	case *atc.AgentStep:
		step.Env = cloneTaskEnv(step.Env)
		for name, value := range resolved {
			step.Env[name] = value
		}
	case *atc.PublishSnapshotStep:
		// Validation above forbids declared values. The JSON round trip used for
		// cloning also copies the baked publication map, leaving it immutable to
		// callers of this instance.
		if len(resolved) != 0 {
			return nil, fmt.Errorf("workflow: publish_snapshot nodes cannot accept caller-supplied parameters")
		}
	}
	return function, nil
}

func validateNodeParameters(parameters []NodeParameter) error {
	seen := make(map[string]struct{}, len(parameters))
	for _, parameter := range parameters {
		if strings.TrimSpace(parameter.Name) == "" {
			return fmt.Errorf("workflow: node parameter name is required")
		}
		if parameter.Name != strings.TrimSpace(parameter.Name) {
			return fmt.Errorf("workflow: node parameter %q must not contain surrounding whitespace", parameter.Name)
		}
		if _, duplicate := seen[parameter.Name]; duplicate {
			return fmt.Errorf("workflow: duplicate node parameter %q", parameter.Name)
		}
		seen[parameter.Name] = struct{}{}
	}
	return nil
}

func resolveNodeParameters(parameters []NodeParameter, values map[string]string) (map[string]string, error) {
	if err := validateNodeParameters(parameters); err != nil {
		return nil, err
	}
	declared := make(map[string]NodeParameter, len(parameters))
	for _, parameter := range parameters {
		declared[parameter.Name] = parameter
	}
	for name := range values {
		if _, found := declared[name]; !found {
			return nil, fmt.Errorf("workflow: node received unknown parameter %q", name)
		}
	}
	resolved := make(map[string]string, len(parameters))
	for _, parameter := range parameters {
		if value, supplied := values[parameter.Name]; supplied {
			resolved[parameter.Name] = value
			continue
		}
		if parameter.Default != nil {
			resolved[parameter.Name] = *parameter.Default
			continue
		}
		return nil, fmt.Errorf("workflow: node parameter %q is required", parameter.Name)
	}
	return resolved, nil
}

func cloneNodeFunction(source FunctionConfig) (*FunctionConfig, error) {
	payload, err := json.Marshal(source)
	if err != nil {
		return nil, fmt.Errorf("workflow: clone compiled node function: %w", err)
	}
	var cloned FunctionConfig
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cloned); err != nil {
		return nil, fmt.Errorf("workflow: clone compiled node function: %w", err)
	}
	return &cloned, nil
}

func cloneTaskEnv(source atc.TaskEnv) atc.TaskEnv {
	cloned := make(atc.TaskEnv, len(source))
	for name, value := range source {
		cloned[name] = value
	}
	return cloned
}
