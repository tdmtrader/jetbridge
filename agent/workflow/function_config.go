package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

// FunctionConfig is the schema-version-3 function grammar. It deliberately
// embeds the ordinary Concourse declaration and step types rather than
// introducing a parallel workflow vocabulary.
type FunctionConfig struct {
	SignatureVersion int                   `json:"signature_version" yaml:"signature_version"`
	Inputs           []snapshot.Port       `json:"inputs" yaml:"inputs"`
	Outputs          []FunctionOutput      `json:"outputs" yaml:"outputs"`
	Capabilities     map[string]Capability `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Resources        atc.ResourceConfigs   `json:"resources,omitempty" yaml:"resources,omitempty"`
	ResourceTypes    atc.ResourceTypes     `json:"resource_types,omitempty" yaml:"resource_types,omitempty"`
	Prototypes       atc.Prototypes        `json:"prototypes,omitempty" yaml:"prototypes,omitempty"`
	VarSources       atc.VarSourceConfigs  `json:"var_sources,omitempty" yaml:"var_sources,omitempty"`
	Plan             []atc.Step            `json:"plan" yaml:"plan"`
	// SkillFiles is compiled-only content. Version-3 source selects skill
	// names on agent nodes; compilation copies the selected trees here.
	SkillFiles map[string]string `json:"skill_files,omitempty" yaml:"skill_files,omitempty"`
}

// Capability is a source-local named capability contract. Digest pinning and
// sidecar semantic validation are compiler concerns owned by workflow task 2.
type Capability struct {
	Contract string            `json:"contract" yaml:"contract"`
	Sidecar  atc.SidecarConfig `json:"sidecar" yaml:"sidecar"`
}

// FunctionOutput maps one public output port to the internal artifact that is
// exported through it.
type FunctionOutput struct {
	snapshot.Port `yaml:",inline"`
	From          string `json:"from" yaml:"from"`
}

func (output FunctionOutput) MarshalJSON() ([]byte, error) {
	if err := output.Port.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(output.From) == "" {
		return nil, fmt.Errorf("workflow: output %q: from is required", output.Name)
	}
	type wire struct {
		Name        string           `json:"name"`
		Type        snapshot.TypeRef `json:"type"`
		Optional    bool             `json:"optional,omitempty"`
		Description string           `json:"description,omitempty"`
		From        string           `json:"from"`
	}
	return json.Marshal(wire{
		Name:        output.Name,
		Type:        output.Type,
		Optional:    output.Optional,
		Description: output.Description,
		From:        output.From,
	})
}

func (output *FunctionOutput) UnmarshalJSON(data []byte) error {
	type wire struct {
		Name        string           `json:"name"`
		Type        snapshot.TypeRef `json:"type"`
		Optional    bool             `json:"optional,omitempty"`
		Description string           `json:"description,omitempty"`
		From        string           `json:"from"`
	}
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("workflow: unexpected trailing output value")
		}
		return fmt.Errorf("workflow: trailing output value: %w", err)
	}
	parsed := FunctionOutput{
		Port: snapshot.Port{
			Name:        decoded.Name,
			Type:        decoded.Type,
			Optional:    decoded.Optional,
			Description: decoded.Description,
		},
		From: decoded.From,
	}
	if err := parsed.Port.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(parsed.From) == "" {
		return fmt.Errorf("workflow: output %q: from is required", parsed.Name)
	}
	*output = parsed
	return nil
}

// Validate checks the v3 structure that is independent of source assets,
// capability authorization, ordinary Concourse semantic validation, and DAG
// type flow. Those layers are intentionally handled by later compiler tasks.
func (config FunctionConfig) Validate() error {
	if config.SignatureVersion <= 0 {
		return fmt.Errorf("workflow: signature_version must be a positive integer, got %d", config.SignatureVersion)
	}
	if err := snapshot.ValidatePorts(config.Inputs); err != nil {
		return fmt.Errorf("workflow: inputs: %w", err)
	}

	seenOutputs := make(map[string]struct{}, len(config.Outputs))
	for index, output := range config.Outputs {
		if err := output.Port.Validate(); err != nil {
			return fmt.Errorf("workflow: outputs[%d]: %w", index, err)
		}
		if _, found := seenOutputs[output.Name]; found {
			return fmt.Errorf("workflow: outputs: duplicate port %q", output.Name)
		}
		seenOutputs[output.Name] = struct{}{}
		if strings.TrimSpace(output.From) == "" {
			return fmt.Errorf("workflow: output %q: from is required", output.Name)
		}
	}

	if len(config.Plan) == 0 {
		return fmt.Errorf("workflow: plan must contain at least one step")
	}
	if err := rejectUnknownPlanFields(config.Plan); err != nil {
		return err
	}
	return nil
}
