package atc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"sigs.k8s.io/yaml"
)

// SnapshotInputConfig declares the semantic type required at one effective
// build-artifact input name. It is intentionally separate from TaskInputConfig:
// the legacy declaration creates the mount, while this declaration constrains
// a value that already exists at that mount.
type SnapshotInputConfig struct {
	Type     snapshot.TypeRef `json:"type"`
	Optional bool             `json:"optional,omitempty"`
}

func (config SnapshotInputConfig) Validate() error {
	return config.Type.Validate()
}

func (config SnapshotInputConfig) MarshalJSON() ([]byte, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	type wire SnapshotInputConfig
	return json.Marshal(wire(config))
}

func (config *SnapshotInputConfig) UnmarshalJSON(data []byte) error {
	type wire SnapshotInputConfig
	var decoded wire
	if err := strictSnapshotConfig(data, &decoded); err != nil {
		return err
	}
	parsed := SnapshotInputConfig(decoded)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*config = parsed
	return nil
}

// SnapshotOutputConfig declares the semantic type produced at one effective
// build-artifact output name. Workflow binding fields are untrusted linkage
// claims; execution re-authorizes them against the durable run and build.
type SnapshotOutputConfig struct {
	Type                 snapshot.TypeRef        `json:"type"`
	Optional             bool                    `json:"optional,omitempty"`
	Retention            snapshot.RetentionClass `json:"retention,omitempty"`
	WorkflowPort         string                  `json:"workflow_port,omitempty"`
	WorkflowDefinitionID int                     `json:"workflow_definition_id,omitempty"`
	WorkflowRunID        string                  `json:"workflow_run_id,omitempty"`
	// SourceMetadata describes this particular production occurrence. It is
	// forwarded to the snapshot sealer, but is deliberately redacted from the
	// public build plan and never contributes to snapshot value identity.
	SourceMetadata json.RawMessage `json:"source_metadata,omitempty"`
}

func (config SnapshotOutputConfig) Validate() error {
	if err := config.Type.Validate(); err != nil {
		return err
	}
	if err := validateSnapshotSourceMetadata(config.SourceMetadata); err != nil {
		return err
	}

	hasWorkflowMetadata := config.WorkflowPort != "" || config.WorkflowDefinitionID != 0 || config.WorkflowRunID != ""
	switch config.Retention {
	case "", snapshot.RetentionClassBinding:
		if hasWorkflowMetadata {
			return fmt.Errorf("snapshot: workflow metadata requires workflow retention")
		}
		return nil
	case snapshot.RetentionClassWorkflow:
		if config.WorkflowPort == "" {
			return fmt.Errorf("snapshot: workflow retention requires workflow_port")
		}
		if warning, err := ValidateIdentifier(config.WorkflowPort); err != nil || warning != nil {
			return fmt.Errorf("snapshot: workflow_port %q is not a safe artifact identifier", config.WorkflowPort)
		}
		if config.WorkflowDefinitionID <= 0 {
			return fmt.Errorf("snapshot: workflow retention requires a positive workflow_definition_id")
		}
		if config.WorkflowRunID == "" {
			return fmt.Errorf("snapshot: workflow retention requires workflow_run_id")
		}
		if config.WorkflowRunID != "((workflow_run_id))" {
			if _, err := snapshot.ParseWorkflowRunID(config.WorkflowRunID); err != nil {
				return fmt.Errorf("snapshot: workflow_run_id: %w", err)
			}
		}
		return nil
	case snapshot.RetentionClassFixture, snapshot.RetentionClassPin:
		return fmt.Errorf("snapshot: retention %q cannot be claimed by a producer", config.Retention)
	default:
		return fmt.Errorf("snapshot: unsupported producer retention %q", config.Retention)
	}
}

const maxSnapshotSourceMetadataBytes = 16 * 1024

func validateSnapshotSourceMetadata(metadata json.RawMessage) error {
	if len(metadata) == 0 {
		return nil
	}
	if len(metadata) > maxSnapshotSourceMetadataBytes {
		return fmt.Errorf("snapshot: source_metadata exceeds %d bytes", maxSnapshotSourceMetadataBytes)
	}
	trimmed := bytes.TrimSpace(metadata)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return fmt.Errorf("snapshot: source_metadata must be a JSON object")
	}
	return nil
}

// MarshalJSON always emits the long object form, including when the value was
// authored using the output scalar shorthand.
func (config SnapshotOutputConfig) MarshalJSON() ([]byte, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	type wire SnapshotOutputConfig
	return json.Marshal(wire(config))
}

func (config *SnapshotOutputConfig) UnmarshalJSON(data []byte) error {
	if len(bytes.TrimSpace(data)) > 0 && bytes.TrimSpace(data)[0] == '"' {
		var raw string
		if err := strictSnapshotConfig(data, &raw); err != nil {
			return err
		}
		parsed, err := snapshot.ParseTypeRef(raw)
		if err != nil {
			return err
		}
		*config = SnapshotOutputConfig{Type: parsed}
		return nil
	}

	type wire SnapshotOutputConfig
	var decoded wire
	if err := strictSnapshotConfig(data, &decoded); err != nil {
		return err
	}
	parsed := SnapshotOutputConfig(decoded)
	if err := parsed.Validate(); err != nil {
		return err
	}
	*config = parsed
	return nil
}

func strictSnapshotConfig(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("snapshot: unexpected trailing JSON value")
		}
		return fmt.Errorf("snapshot: trailing JSON: %w", err)
	}
	return nil
}

type TaskConfig struct {
	// The platform the task must run on (e.g. linux, windows).
	Platform string `json:"platform,omitempty"`

	// Optional string specifying an image to use for the build. Depending on the
	// platform, this may or may not be required (e.g. Windows/OS X vs. Linux).
	RootfsURI string `json:"rootfs_uri,omitempty"`

	ImageResource *ImageResource `json:"image_resource,omitempty"`

	// Limits to set on the Task Container
	Limits *ContainerLimits `json:"container_limits,omitempty"`

	// Requests to set on the Task Container (independent from limits for Burstable QoS)
	Requests *ContainerLimits `json:"container_requests,omitempty"`

	// Parameters to pass to the task via environment variables.
	Params TaskEnv `json:"params,omitempty"`

	// Script to execute.
	Run TaskRunConfig `json:"run,omitempty"`

	// The set of (logical, name-only) inputs required by the task.
	Inputs []TaskInputConfig `json:"inputs,omitempty"`

	// The set of (logical, name-only) outputs provided by the task.
	Outputs []TaskOutputConfig `json:"outputs,omitempty"`

	// Path to cached directory that will be shared between builds for the same task.
	Caches []TaskCacheConfig `json:"caches,omitempty"`

	// Ephemeral scratch volumes — mounted as emptyDir, never cached or preserved.
	ScratchPaths []TaskScratchConfig `json:"scratch_paths,omitempty"`
}

type ImageResource struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	Source  Source  `json:"source"`
	Version Version `json:"version,omitempty"`
	Params  Params  `json:"params,omitempty"`
	Tags    Tags    `json:"tags,omitempty"`
}

func (ir *ImageResource) ApplySourceDefaults(resourceTypes ResourceTypes) {
	if ir == nil {
		return
	}

	parentType, found := resourceTypes.Lookup(ir.Type)
	if found {
		ir.Source = parentType.Defaults.Merge(ir.Source)
	} else {
		brtDefaults, found := FindBaseResourceTypeDefaults(ir.Type)
		if found {
			ir.Source = brtDefaults.Merge(ir.Source)
		}
	}
}

func NewTaskConfig(configBytes []byte) (TaskConfig, error) {
	var config TaskConfig
	err := yaml.UnmarshalStrict(configBytes, &config, yaml.DisallowUnknownFields)
	if err != nil {
		return TaskConfig{}, err
	}

	err = config.Validate()
	if err != nil {
		return TaskConfig{}, err
	}

	return config, nil
}

type TaskValidationError struct {
	Errors []string
}

func (err TaskValidationError) Error() string {
	return fmt.Sprintf("invalid task configuration:\n%s", strings.Join(err.Errors, "\n"))
}

func (config TaskConfig) Validate() error {
	var errors []string

	if config.Platform == "" {
		errors = append(errors, "missing 'platform'")
	}

	errors = append(errors, config.validateInputContainsNames()...)
	errors = append(errors, config.validateOutputContainsNames()...)

	if len(errors) > 0 {
		return TaskValidationError{
			Errors: errors,
		}
	}

	return nil
}

func (config TaskConfig) validateOutputContainsNames() []string {
	var messages []string

	for i, output := range config.Outputs {
		if output.Name == "" {
			messages = append(messages, fmt.Sprintf("  output in position %d is missing a name", i))
		}
	}

	return messages
}

func (config TaskConfig) validateInputContainsNames() []string {
	messages := []string{}

	for i, input := range config.Inputs {
		if input.Name == "" {
			messages = append(messages, fmt.Sprintf("  input in position %d is missing a name", i))
		}
	}

	return messages
}

type TaskRunConfig struct {
	Path string   `json:"path"`
	Args []string `json:"args,omitempty"`
	Dir  string   `json:"dir,omitempty"`

	// The user that the task will run as (defaults to whatever the docker image specifies)
	User string `json:"user,omitempty"`
}

type TaskInputConfig struct {
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

type TaskOutputConfig struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

type TaskCacheConfig struct {
	Path string `json:"path,omitempty"`
}

type TaskScratchConfig struct {
	Path string `json:"path,omitempty"`
}

type TaskEnv map[string]string

func (te *TaskEnv) UnmarshalJSON(p []byte) error {
	raw := map[string]CoercedString{}
	err := json.Unmarshal(p, &raw)
	if err != nil {
		return err
	}

	m := map[string]string{}
	for k, v := range raw {
		m[k] = string(v)
	}

	*te = m

	return nil
}

func (te TaskEnv) Env() []string {
	env := make([]string, 0, len(te))

	for k, v := range te {
		env = append(env, k+"="+v)
	}

	return env
}

type CoercedString string

func (cs *CoercedString) UnmarshalJSON(p []byte) error {
	var raw any
	dec := json.NewDecoder(bytes.NewReader(p))
	dec.UseNumber()
	err := dec.Decode(&raw)
	if err != nil {
		return err
	}

	if raw == nil {
		*cs = CoercedString("")
		return nil
	}
	switch v := raw.(type) {
	case string:
		*cs = CoercedString(v)

	case json.Number:
		*cs = CoercedString(v)

	default:
		j, err := json.Marshal(v)
		if err != nil {
			return err
		}

		*cs = CoercedString(j)
	}

	return nil
}
