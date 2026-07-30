package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/goccy/go-yaml"
)

type nodeSource struct {
	SchemaVersion int                   `json:"schema_version"`
	Name          string                `json:"name"`
	Description   string                `json:"description,omitempty"`
	Inputs        []snapshot.Port       `json:"inputs"`
	Outputs       []snapshot.Port       `json:"outputs"`
	Parameters    []NodeParameter       `json:"parameters,omitempty"`
	Capabilities  map[string]Capability `json:"capabilities,omitempty"`
	Step          any                   `json:"step"`
}

func parseNodeDefinitionSource(raw []byte) (*CompiledNodeDefinition, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse node definition: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse node definition: exactly one YAML or JSON document is required")
		}
		return nil, fmt.Errorf("parse node definition trailing document: %w", err)
	}
	if err := validateNodeSourceKeys(document); err != nil {
		return nil, err
	}
	documentJSON, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("parse node definition document: %w", err)
	}
	jsonDecoder := json.NewDecoder(bytes.NewReader(documentJSON))
	jsonDecoder.DisallowUnknownFields()
	var source nodeSource
	if err := jsonDecoder.Decode(&source); err != nil {
		return nil, fmt.Errorf("parse node definition: %w", err)
	}
	if err := jsonDecoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse node definition: unexpected trailing JSON value")
		}
		return nil, fmt.Errorf("parse node definition trailing JSON: %w", err)
	}
	if source.SchemaVersion != 1 {
		return nil, fmt.Errorf("workflow: node parser requires schema_version 1, got %d", source.SchemaVersion)
	}

	ordinary, err := decodeNodeStep(source.Step)
	if err != nil {
		return nil, err
	}
	if len(ordinary.Jobs) != 1 || len(ordinary.Jobs[0].PlanSequence) != 1 {
		return nil, fmt.Errorf("workflow: node step must contain exactly one atomic leaf")
	}
	step := ordinary.Jobs[0].PlanSequence[0]
	if err := rejectUnknownPlanFields([]atc.Step{step}); err != nil {
		return nil, err
	}
	function := FunctionConfig{
		SignatureVersion: 1,
		Inputs:           source.Inputs,
		Outputs:          nodeFunctionOutputs(source.Outputs),
		Capabilities:     source.Capabilities,
		Plan:             []atc.Step{step},
	}
	if err := bindNodeLeafPorts(&function); err != nil {
		return nil, err
	}
	definition := &CompiledNodeDefinition{SchemaVersion: source.SchemaVersion, Name: source.Name, Description: source.Description, Parameters: source.Parameters, Function: function}
	if err := definition.validate(true); err != nil {
		return nil, err
	}
	return definition, nil
}

func validateNodeSourceKeys(document any) error {
	root, ok := document.(map[string]any)
	if !ok {
		return nil
	}
	if err := rejectObjectKeys(root, "node", []string{"schema_version", "name", "description", "inputs", "outputs", "parameters", "capabilities", "step"}); err != nil {
		return err
	}
	for _, field := range []string{"inputs", "outputs"} {
		if ports, ok := root[field].([]any); ok {
			for index, port := range ports {
				if object, ok := port.(map[string]any); ok {
					if err := rejectObjectKeys(object, fmt.Sprintf("node.%s[%d]", field, index), []string{"name", "type", "optional", "description"}); err != nil {
						return err
					}
				}
			}
		}
	}
	if parameters, ok := root["parameters"].([]any); ok {
		for index, parameter := range parameters {
			if object, ok := parameter.(map[string]any); ok {
				if err := rejectObjectKeys(object, fmt.Sprintf("node.parameters[%d]", index), []string{"name", "default"}); err != nil {
					return err
				}
			}
		}
	}
	if capabilities, ok := root["capabilities"].(map[string]any); ok {
		for name, value := range capabilities {
			if capability, ok := value.(map[string]any); ok {
				path := fmt.Sprintf("node.capabilities[%q]", name)
				if err := rejectObjectKeys(capability, path, []string{"contract", "sidecar"}); err != nil {
					return err
				}
				if sidecar, ok := capability["sidecar"].(map[string]any); ok {
					if err := validateSidecarSourceKeys(sidecar, path+".sidecar"); err != nil {
						return err
					}
				}
			}
		}
	}
	if step, found := root["step"]; found {
		return validateAtomicNodeStepSource(step)
	}
	return nil
}

func validateAtomicNodeStepSource(value any) error {
	step, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if err := validateFunctionStepSource(step, "node.step"); err != nil {
		return err
	}
	for _, wrapper := range []string{"try", "do", "in_parallel", "across", "attempts", "timeout", "on_success", "on_failure", "on_abort", "on_error", "ensure"} {
		if _, found := step[wrapper]; found {
			return fmt.Errorf("workflow: node.step must contain exactly one atomic leaf; %q is not permitted", wrapper)
		}
	}
	for _, forbidden := range []string{"get", "put", "run", "set_pipeline", "load_var", "load_snapshot", "await_snapshot"} {
		if _, found := step[forbidden]; found {
			return fmt.Errorf("workflow: node.step must contain one permitted leaf (task, agent, or publish_snapshot)")
		}
	}
	permitted := 0
	for _, kind := range []string{"task", "agent", "publish_snapshot"} {
		if _, found := step[kind]; found {
			permitted++
		}
	}
	if permitted != 1 {
		return fmt.Errorf("workflow: node.step must contain exactly one atomic leaf")
	}
	return nil
}

func decodeNodeStep(step any) (atc.Config, error) {
	payload, err := yaml.Marshal(struct {
		Jobs []syntheticFunctionJob `yaml:"jobs"`
	}{Jobs: []syntheticFunctionJob{{Name: "node", Plan: []any{step}}}})
	if err != nil {
		return atc.Config{}, fmt.Errorf("workflow: encode ordinary node step: %w", err)
	}
	var ordinary atc.Config
	if err := atc.UnmarshalConfig(payload, &ordinary); err != nil {
		return atc.Config{}, fmt.Errorf("workflow: decode ordinary node step: %w", err)
	}
	return ordinary, nil
}

func nodeFunctionOutputs(ports []snapshot.Port) []FunctionOutput {
	outputs := make([]FunctionOutput, len(ports))
	for index, port := range ports {
		outputs[index] = FunctionOutput{Port: port, From: port.Name}
	}
	return outputs
}

func bindNodeLeafPorts(function *FunctionConfig) error {
	step := function.Plan[0].Config
	inputNames := nodePortNames(function.Inputs)
	outputNames := nodeOutputNames(function.Outputs)
	inputs := nodeInputTypes(function.Inputs)
	outputs := nodeOutputTypes(function.Outputs)
	switch leaf := step.(type) {
	case *atc.TaskStep:
		if len(leaf.InputMapping) != 0 || len(leaf.OutputMapping) != 0 {
			return fmt.Errorf("workflow: node task mappings belong to a workflow node invocation")
		}
		if leaf.ConfigPath != "" || leaf.Config == nil {
			return fmt.Errorf("workflow: node task must declare an inline config")
		}
		if leaf.FunctionID == "" {
			leaf.FunctionID = leaf.Name
		}
		if !sameNodePortNames(nodeTaskInputNames(leaf), inputNames) || !sameNodePortNames(nodeTaskOutputNames(leaf), outputNames) {
			return fmt.Errorf("workflow: node task config inputs and outputs must exactly match declared node ports")
		}
		leaf.SnapshotInputs = inputs
		leaf.SnapshotOutputs = outputs
	case *atc.AgentStep:
		if len(leaf.InputMapping) != 0 || len(leaf.OutputMapping) != 0 {
			return fmt.Errorf("workflow: node agent mappings belong to a workflow node invocation")
		}
		if leaf.FunctionID == "" {
			leaf.FunctionID = leaf.Name
		}
		if len(leaf.Inputs) != 0 && !sameNodePortNames(leaf.Inputs, inputNames) || len(leaf.Outputs) != 0 && !sameNodePortNames(leaf.Outputs, outputNames) {
			return fmt.Errorf("workflow: node agent inputs and outputs must be declared only at the node boundary")
		}
		leaf.Inputs = inputNames
		leaf.Outputs = outputNames
		leaf.SnapshotInputs = inputs
		leaf.SnapshotOutputs = outputs
	case *atc.PublishSnapshotStep:
		if len(function.Inputs) != 1 || len(function.Outputs) != 0 || leaf.Input != function.Inputs[0].Name || leaf.InputType != function.Inputs[0].Type {
			return fmt.Errorf("workflow: publish_snapshot node requires one matching input and no outputs")
		}
	default:
		return fmt.Errorf("workflow: node step must contain one permitted leaf (task, agent, or publish_snapshot)")
	}
	return nil
}

func nodePortNames(ports []snapshot.Port) []string {
	names := make([]string, len(ports))
	for index, port := range ports {
		names[index] = port.Name
	}
	return names
}

func nodeOutputNames(ports []FunctionOutput) []string {
	names := make([]string, len(ports))
	for index, port := range ports {
		names[index] = port.Name
	}
	return names
}

func nodeInputTypes(ports []snapshot.Port) map[string]atc.SnapshotInputConfig {
	types := make(map[string]atc.SnapshotInputConfig, len(ports))
	for _, port := range ports {
		types[port.Name] = atc.SnapshotInputConfig{Type: port.Type, Optional: port.Optional}
	}
	return types
}

func nodeOutputTypes(ports []FunctionOutput) map[string]atc.SnapshotOutputConfig {
	types := make(map[string]atc.SnapshotOutputConfig, len(ports))
	for _, port := range ports {
		types[port.Name] = atc.SnapshotOutputConfig{Type: port.Type, Optional: port.Optional}
	}
	return types
}

func nodeTaskInputNames(step *atc.TaskStep) []string {
	names := make([]string, len(step.Config.Inputs))
	for index, input := range step.Config.Inputs {
		names[index] = input.Name
	}
	return names
}

func nodeTaskOutputNames(step *atc.TaskStep) []string {
	names := make([]string, len(step.Config.Outputs))
	for index, output := range step.Config.Outputs {
		names[index] = output.Name
	}
	return names
}

func sameNodePortNames(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	seen := make(map[string]struct{}, len(actual))
	for _, name := range actual {
		seen[name] = struct{}{}
	}
	if len(seen) != len(actual) {
		return false
	}
	for _, name := range expected {
		if _, found := seen[name]; !found {
			return false
		}
	}
	return true
}
