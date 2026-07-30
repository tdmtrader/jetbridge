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
	SignatureVersion int `json:"signature_version" yaml:"signature_version"`
	// DispositionOutput names the public output whose durable snapshot owns
	// ticket/work-item terminal disposition. It is projection metadata rather
	// than part of the public type signature; omitting it preserves the
	// deterministic single-output compatibility path.
	DispositionOutput string                `json:"disposition_output,omitempty" yaml:"disposition_output,omitempty"`
	Inputs            []snapshot.Port       `json:"inputs" yaml:"inputs"`
	Outputs           []FunctionOutput      `json:"outputs" yaml:"outputs"`
	Capabilities      map[string]Capability `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Resources         atc.ResourceConfigs   `json:"resources,omitempty" yaml:"resources,omitempty"`
	ResourceSources   []ResourceSource      `json:"resource_sources,omitempty" yaml:"resource_sources,omitempty"`
	ResourceTypes     atc.ResourceTypes     `json:"resource_types,omitempty" yaml:"resource_types,omitempty"`
	Prototypes        atc.Prototypes        `json:"prototypes,omitempty" yaml:"prototypes,omitempty"`
	VarSources        atc.VarSourceConfigs  `json:"var_sources,omitempty" yaml:"var_sources,omitempty"`
	Plan              []atc.Step            `json:"plan" yaml:"plan"`
	// DevValidationProfiles is compiled-only authority. Source manifests use
	// file references that are intentionally erased during compilation.
	DevValidationProfiles       []CompiledDevValidationProfile `json:"dev_validation_profiles,omitempty" yaml:"dev_validation_profiles,omitempty"`
	DevValidationProvenanceHash string                         `json:"dev_validation_provenance_hash,omitempty" yaml:"dev_validation_provenance_hash,omitempty"`
	// BrokerProfiles is compiled-only node-scoped authority. Source manifests
	// attach neutral broker selectors to agent nodes; admission resolves them
	// through an operator catalog and stores only the selected exact profiles.
	BrokerProfiles              []CompiledBrokerProfile `json:"broker_profiles,omitempty" yaml:"broker_profiles,omitempty"`
	BrokerProfileProvenanceHash string                  `json:"broker_profile_provenance_hash,omitempty" yaml:"broker_profile_provenance_hash,omitempty"`
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

// UnmarshalJSON is intentionally stricter than the source parser. A compiled
// definition cannot retain source-only named capabilities: their permitted
// runtime material was expanded during compilation, while validation retained
// only its image and digest in the dedicated authority catalog.
func (config *FunctionConfig) UnmarshalJSON(data []byte) error {
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&object); err != nil {
		return err
	}
	for key := range object {
		switch key {
		case "signature_version", "disposition_output", "inputs", "outputs", "resources", "resource_sources", "resource_types", "prototypes", "var_sources", "plan", "dev_validation_profiles", "dev_validation_provenance_hash", "broker_profiles", "broker_profile_provenance_hash", "skill_files":
		default:
			return fmt.Errorf("workflow: compiled function: unknown or source-only field %q", key)
		}
	}
	if rawPlan, found := object["plan"]; found {
		var plan any
		if err := json.Unmarshal(rawPlan, &plan); err != nil {
			return err
		}
		if err := validateFunctionPlanSource(plan, "compiled.plan"); err != nil {
			return err
		}
	}
	type wire FunctionConfig
	var decoded wire
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("workflow: compiled function has trailing JSON value")
		}
		return err
	}
	parsed := FunctionConfig(decoded)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*config = parsed
	return nil
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
	if len(config.ResourceSources) > 0 {
		if err := ValidateResourceSources(config.ResourceSources, config.Resources, config.ResourceTypes); err != nil {
			return err
		}
		inputNames := make(map[string]struct{}, len(config.Inputs))
		for _, input := range config.Inputs {
			inputNames[input.Name] = struct{}{}
		}
		for _, source := range config.ResourceSources {
			if _, found := inputNames[source.Name]; found {
				return fmt.Errorf("workflow: resource source %q conflicts with workflow input", source.Name)
			}
		}
	}

	seenOutputs := make(map[string]struct{}, len(config.Outputs))
	var dispositionOutput *FunctionOutput
	for index, output := range config.Outputs {
		if err := output.Port.Validate(); err != nil {
			return fmt.Errorf("workflow: outputs[%d]: %w", index, err)
		}
		if _, found := seenOutputs[output.Name]; found {
			return fmt.Errorf("workflow: outputs: duplicate port %q", output.Name)
		}
		seenOutputs[output.Name] = struct{}{}
		if output.Name == config.DispositionOutput {
			matched := output
			dispositionOutput = &matched
		}
		if strings.TrimSpace(output.From) == "" {
			return fmt.Errorf("workflow: output %q: from is required", output.Name)
		}
	}
	for _, source := range config.ResourceSources {
		if _, found := seenOutputs[source.Name]; found {
			return fmt.Errorf("workflow: resource source %q conflicts with public output", source.Name)
		}
	}
	if config.DispositionOutput != "" {
		if strings.TrimSpace(config.DispositionOutput) != config.DispositionOutput {
			return fmt.Errorf("workflow: disposition_output must be a canonical public output name")
		}
		if dispositionOutput == nil {
			return fmt.Errorf("workflow: disposition_output %q is not a declared public output", config.DispositionOutput)
		}
		if dispositionOutput.Optional {
			return fmt.Errorf("workflow: disposition_output %q must be required", config.DispositionOutput)
		}
	}

	if len(config.Plan) == 0 {
		return fmt.Errorf("workflow: plan must contain at least one step")
	}
	if err := rejectUnknownPlanFields(config.Plan); err != nil {
		return err
	}
	if err := validateCompiledDevValidationProfiles(config.DevValidationProfiles, config.DevValidationProvenanceHash); err != nil {
		return err
	}
	if err := validateCompiledBrokerProfiles(config.BrokerProfiles, config.BrokerProfileProvenanceHash); err != nil {
		return err
	}
	if err := validateCompiledBrokerProfileNodes(config.Plan, config.BrokerProfiles); err != nil {
		return err
	}
	remaining := MaxCompiledAssetBytes
	for _, profile := range config.DevValidationProfiles {
		if len(profile.Profile) > remaining {
			return fmt.Errorf("workflow: compiled dev validation assets exceed %d bytes", MaxCompiledAssetBytes)
		}
		remaining -= len(profile.Profile)
		if len(profile.ProtectedConfig) > remaining {
			return fmt.Errorf("workflow: compiled dev validation assets exceed %d bytes", MaxCompiledAssetBytes)
		}
		remaining -= len(profile.ProtectedConfig)
	}
	return nil
}
