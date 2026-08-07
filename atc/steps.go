package atc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
)

// Step is an "envelope" type, acting as a wrapper to handle the marshaling and
// unmarshaling of an underlying StepConfig.
type Step struct {
	Config        StepConfig
	UnknownFields map[string]*json.RawMessage
}

// ErrNoStepConfigured is returned when a step does not have any keys that
// indicate its step type.
var ErrNoStepConfigured = errors.New("no step configured")
var ErrNoCoreStepDeclared = errors.New("no core step type declared (e.g. get, put, task, etc.)")

// UnmarshalJSON unmarshals step configuration in multiple passes, determining
// precedence by the order of StepDetectors listed in the StepPrecedence
// variable.
//
// First, the step data is unmarshalled into a map[string]*json.RawMessage. Next,
// UnmarshalJSON loops over StepPrecedence to determine the type of step.
//
// For any StepDetector with a .Key field present in the map, .New is called to
// construct an empty StepConfig, and then json.Unmarshal is called on it to parse
// the data.
//
// For step modifiers like `timeout:` and `attempts:` they eventuallly wrap a
// core step type (e.g. get, put, task etc.). Core step types do not wrap other
// steps.
//
// When a core step type is encountered parsing stops and any remaining keys in
// rawStepConfig are considered invalid. This is how we stop someone from
// putting a `get` and `put` in the same step while still allowing valid step
// modifiers. This is also why step modifiers are listed first in
// StepPrecedence.
//
// If no StepDetectors match, no step is parsed, ErrNoStepConfigured is
// returned.
func (step *Step) UnmarshalJSON(data []byte) error {
	var rawStepConfig map[string]*json.RawMessage
	err := json.Unmarshal(data, &rawStepConfig)
	if err != nil {
		return err
	}

	var prevStep StepWrapper
	var coreStepDeclared bool
	for _, s := range StepPrecedence {
		_, found := rawStepConfig[s.Key]
		if !found {
			continue
		}

		curStep := s.New()

		err := json.Unmarshal(data, curStep)
		if err != nil {
			return MalformedStepError{
				StepType: s.Key,
				Err:      err,
			}
		}

		if step.Config == nil {
			step.Config = curStep
		}

		if prevStep != nil {
			prevStep.Wrap(curStep)
		}

		deleteKnownFields(rawStepConfig, curStep)

		if wrapper, isWrapper := curStep.(StepWrapper); isWrapper {
			prevStep = wrapper
		} else {
			coreStepDeclared = true
			break
		}

		data, err = json.Marshal(rawStepConfig)
		if err != nil {
			return fmt.Errorf("re-marshal rawStepConfig parsing: %w", err)
		}
	}

	if step.Config == nil {
		return ErrNoStepConfigured
	}

	if !coreStepDeclared {
		return ErrNoCoreStepDeclared
	}

	if len(rawStepConfig) != 0 {
		step.UnknownFields = rawStepConfig
	}

	return nil
}

// MarshalJSON marshals step configuration in multiple passes, looping and
// calling .Unwrap to marshal all nested steps into one big set of fields which
// is then marshalled and returned.
func (step Step) MarshalJSON() ([]byte, error) {
	fields := make(map[string]*json.RawMessage, len(step.UnknownFields))
	for name, raw := range step.UnknownFields {
		if raw == nil {
			fields[name] = nil
			continue
		}
		cloned := append(json.RawMessage(nil), (*raw)...)
		fields[name] = &cloned
	}

	unwrapped := step.Config
	for unwrapped != nil {
		payload, err := json.Marshal(unwrapped)
		if err != nil {
			return nil, err
		}

		err = json.Unmarshal(payload, &fields)
		if err != nil {
			return nil, err
		}

		if wrapper, isWrapper := unwrapped.(StepWrapper); isWrapper {
			unwrapped = wrapper.Unwrap()
		} else {
			break
		}
	}

	return json.Marshal(fields)
}

// See the note about json tags here: https://golang.org/pkg/encoding/json/#Marshal
func deleteKnownFields(rawStepConfig map[string]*json.RawMessage, step StepConfig) {
	stepType := reflect.TypeOf(step).Elem()
	for i := 0; i < stepType.NumField(); i++ {
		field := stepType.Field(i)
		jsonTag := field.Tag.Get("json")
		if jsonTag == "-" {
			continue
		}
		jsonTagParts := strings.Split(jsonTag, ",")
		if len(jsonTagParts) < 1 {
			continue
		}
		jsonKey := jsonTagParts[0]
		if jsonKey == "" {
			jsonKey = field.Name
		}
		delete(rawStepConfig, jsonKey)
	}
}

// StepConfig is implemented by all step types.
type StepConfig interface {
	// Visit must call StepVisitor with the appropriate method corresponding to
	// this step type.
	//
	// When a new step type is added, the StepVisitor interface must be extended.
	// This allows the compiler to help us track down all the places where steps
	// must be handled type-by-type.
	Visit(StepVisitor) error
}

// StepWrapper is an optional interface for step types that is implemented by
// steps that wrap/modify other steps (e.g. hooks like `on_success`, `timeout`, etc.)
type StepWrapper interface {
	// Wrap is called during (Step).UnmarshalJSON whenever an 'inner' step is
	// parsed.
	//
	// Modifier step types should implement this function by assigning the
	// passed in StepConfig to an internal field that has a `json:"-"` tag.
	Wrap(StepConfig)

	// Unwrap is called during (Step).MarshalJSON and must return the wrapped
	// StepConfig.
	Unwrap() StepConfig
}

// StepVisitor is an interface used to assist in finding all the places that
// need to be updated whenever a new step type is introduced.
//
// Each StepConfig must implement .Visit to call the appropriate method on the
// given StepVisitor.
type StepVisitor interface {
	VisitTask(*TaskStep) error
	VisitGet(*GetStep) error
	VisitPut(*PutStep) error
	VisitRun(*RunStep) error
	VisitAgent(*AgentStep) error
	VisitSetPipeline(*SetPipelineStep) error
	VisitLoadVar(*LoadVarStep) error
	VisitLoadSnapshot(*LoadSnapshotStep) error
	VisitAwaitSnapshot(*AwaitSnapshotStep) error
	VisitPublishSnapshot(*PublishSnapshotStep) error
	VisitTry(*TryStep) error
	VisitDo(*DoStep) error
	VisitInParallel(*InParallelStep) error
	VisitAcross(*AcrossStep) error
	VisitTimeout(*TimeoutStep) error
	VisitRetry(*RetryStep) error
	VisitOnSuccess(*OnSuccessStep) error
	VisitOnFailure(*OnFailureStep) error
	VisitOnAbort(*OnAbortStep) error
	VisitOnError(*OnErrorStep) error
	VisitEnsure(*EnsureStep) error
}

// StepDetector is a simple structure used to detect whether a step type is
// configured.
type StepDetector struct {
	// Key is the field that, if present, indicates that the step is configured.
	Key string

	// If Key is present, New will be called to construct an empty StepConfig.
	New func() StepConfig
}

// StepPrecedence is a static list of all of the step types, listed in the
// order that they should be parsed. Broadly, modifiers are parsed first - with
// some important inter-modifier precedence - while core step types are parsed
// last.
var StepPrecedence = []StepDetector{
	{
		Key: "ensure",
		New: func() StepConfig { return &EnsureStep{} },
	},
	{
		Key: "on_error",
		New: func() StepConfig { return &OnErrorStep{} },
	},
	{
		Key: "on_abort",
		New: func() StepConfig { return &OnAbortStep{} },
	},
	{
		Key: "on_failure",
		New: func() StepConfig { return &OnFailureStep{} },
	},
	{
		Key: "on_success",
		New: func() StepConfig { return &OnSuccessStep{} },
	},
	{
		Key: "across",
		New: func() StepConfig { return &AcrossStep{} },
	},
	{
		Key: "attempts",
		New: func() StepConfig { return &RetryStep{} },
	},
	{
		Key: "agent",
		New: func() StepConfig { return &AgentStep{} },
	},
	{
		Key: "run",
		New: func() StepConfig { return &RunStep{} },
	},
	{
		Key: "task",
		New: func() StepConfig { return &TaskStep{} },
	},
	{
		Key: "put",
		New: func() StepConfig { return &PutStep{} },
	},
	{
		Key: "get",
		New: func() StepConfig { return &GetStep{} },
	},
	{
		Key: "timeout",
		New: func() StepConfig { return &TimeoutStep{} },
	},
	{
		Key: "load_snapshot",
		New: func() StepConfig { return &LoadSnapshotStep{} },
	},
	{
		Key: "await_snapshot",
		New: func() StepConfig { return &AwaitSnapshotStep{} },
	},
	{
		Key: "publish_snapshot",
		New: func() StepConfig { return &PublishSnapshotStep{} },
	},
	{
		Key: "set_pipeline",
		New: func() StepConfig { return &SetPipelineStep{} },
	},
	{
		Key: "load_var",
		New: func() StepConfig { return &LoadVarStep{} },
	},
	{
		Key: "try",
		New: func() StepConfig { return &TryStep{} },
	},
	{
		Key: "do",
		New: func() StepConfig { return &DoStep{} },
	},
	{
		Key: "in_parallel",
		New: func() StepConfig { return &InParallelStep{} },
	},
}

type GetStep struct {
	Name         string         `json:"get"`
	Resource     string         `json:"resource,omitempty"`
	Version      *VersionConfig `json:"version,omitempty"`
	Params       Params         `json:"params,omitempty"`
	Passed       []string       `json:"passed,omitempty"`
	Trigger      bool           `json:"trigger,omitempty"`
	Tags         Tags           `json:"tags,omitempty"`
	Timeout      string         `json:"timeout,omitempty"`
	SkipDownload bool           `json:"skip_download,omitempty"`
}

func (step *GetStep) ResourceName() string {
	if step.Resource != "" {
		return step.Resource
	}

	return step.Name
}

func (step *GetStep) Visit(v StepVisitor) error {
	return v.VisitGet(step)
}

type PutStep struct {
	Name      string        `json:"put"`
	Resource  string        `json:"resource,omitempty"`
	Params    Params        `json:"params,omitempty"`
	Inputs    *InputsConfig `json:"inputs,omitempty"`
	Tags      Tags          `json:"tags,omitempty"`
	GetParams Params        `json:"get_params,omitempty"`
	Timeout   string        `json:"timeout,omitempty"`
	NoGet     bool          `json:"no_get,omitempty"`
}

func (step *PutStep) ResourceName() string {
	if step.Resource != "" {
		return step.Resource
	}

	return step.Name
}

func (step *PutStep) Visit(v StepVisitor) error {
	return v.VisitPut(step)
}

type TaskStep struct {
	Name       string `json:"task"`
	FunctionID string `json:"function_id,omitempty"`
	// DevValidationProfile is the only source-level selector for an
	// authoritative validation task. Compilation replaces it with the fixed
	// task shape and server-owned authority below.
	DevValidationProfile   string                  `json:"dev_validation_profile,omitempty"`
	DevValidationAuthority *DevValidationAuthority `json:"dev_validation_authority,omitempty"`
	// MergePreflightAuthority is renderer-only authority for the fixed delivery
	// merge report task. function_id is the sole source selector.
	MergePreflightAuthority *MergePreflightAuthority `json:"merge_preflight_authority,omitempty"`
	// ResourceCaptureAuthority is minted only by the server-side resource
	// capture adapter, which saves it into the server-owned one-shot capture
	// template. It is decodable here because that template is an ordinary
	// pipeline; execution refuses to honor it unless the build's pipeline run
	// is authenticated as belonging to that template.
	ResourceCaptureAuthority *ResourceCaptureAuthority       `json:"resource_capture_authority,omitempty"`
	Privileged               bool                            `json:"privileged,omitempty"`
	Hermetic                 bool                            `json:"hermetic,omitempty"`
	ConfigPath               string                          `json:"file,omitempty"`
	Limits                   *ContainerLimits                `json:"container_limits,omitempty"`
	Requests                 *ContainerLimits                `json:"container_requests,omitempty"`
	Config                   *TaskConfig                     `json:"config,omitempty"`
	Params                   TaskEnv                         `json:"params,omitempty"`
	Vars                     Params                          `json:"vars,omitempty"`
	Tags                     Tags                            `json:"tags,omitempty"`
	InputMapping             map[string]string               `json:"input_mapping,omitempty"`
	OutputMapping            map[string]string               `json:"output_mapping,omitempty"`
	SnapshotInputs           map[string]SnapshotInputConfig  `json:"input_types,omitempty"`
	SnapshotOutputs          map[string]SnapshotOutputConfig `json:"output_types,omitempty"`
	ImageArtifactName        string                          `json:"image,omitempty"`
	Timeout                  string                          `json:"timeout,omitempty"`
	Sidecars                 []SidecarSource                 `json:"sidecars,omitempty"`
}

func (step *TaskStep) Visit(v StepVisitor) error {
	return v.VisitTask(step)
}

type RunStep struct {
	Message    string           `json:"run"`
	Type       string           `json:"type"`
	Params     Params           `json:"params,omitempty"`
	Privileged bool             `json:"privileged,omitempty"`
	Tags       Tags             `json:"tags,omitempty"`
	Limits     *ContainerLimits `json:"container_limits,omitempty"`
	Requests   *ContainerLimits `json:"container_requests,omitempty"`
	Timeout    string           `json:"timeout,omitempty"`

	// XXX(prototypes): inputs, outputs, input_mapping, output_mapping?
	// see https://github.com/concourse/rfcs/pull/103

	// XXX(prototypes): set_vars?

	// XXX(prototypes): image? That way, you can build a prototype and run it
	// in the same pipeline. This would be in place of type.
}

func (step *RunStep) Visit(v StepVisitor) error {
	return v.VisitRun(step)
}

// AgentStep runs the claude CLI in a jetbridge pod with declared MCP
// sidecars (shared-contracts §2.8). The renderer resolves everything from
// the workflow definition into literal values here; the step implementation
// never reads workflow tables.
type AgentStep struct {
	Name       string `json:"agent"`
	FunctionID string `json:"function_id,omitempty"`
	// Hermetic requests runtime-enforced network isolation. Schema-v3
	// workflow admission always sets this for transformation agents; direct
	// pipeline authors may opt in for ordinary agent steps as well.
	Hermetic bool `json:"hermetic,omitempty"`
	// RuntimeImage is injected by the trusted workflow admission renderer.
	// Versioned source definitions must leave it empty; schema-v3 admission
	// fills an exact OCI digest so it participates in the immutable config
	// hash and can be checked again by the executor.
	RuntimeImage     string   `json:"runtime_image,omitempty"`
	Prompt           string   `json:"prompt,omitempty"`
	PromptFile       string   `json:"prompt_file,omitempty"`
	SystemPromptFile string   `json:"system_prompt_file,omitempty"`
	ContextFiles     []string `json:"context_files,omitempty"`
	Model            string   `json:"model,omitempty"`
	MaxTurns         int      `json:"max_turns,omitempty"`
	BudgetSliceUSD   float64  `json:"budget_slice_usd,omitempty"`

	// Source-format layers (design 2026-07-17 §4), renderer-resolved to
	// literal values like Prompt: SystemPrompt is appended to the
	// runner's baseline system prompt; Context is a pre-concatenated
	// session-start block; Skills names select subtrees of the "skills"
	// input artifact for materialization into the agent's project
	// skill directory.
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Context      string   `json:"context,omitempty"`
	Skills       []string `json:"skills,omitempty"`
	// SkillFiles is compiler-owned frozen content for this exact agent's
	// selected skills. Source manifests must never author it.
	SkillFiles map[string]string `json:"skill_files,omitempty"`

	Sidecars         []SidecarSource                 `json:"sidecars,omitempty"`
	Inputs           []string                        `json:"inputs,omitempty"`
	Outputs          []string                        `json:"outputs,omitempty"`
	InputMapping     map[string]string               `json:"input_mapping,omitempty"`
	OutputMapping    map[string]string               `json:"output_mapping,omitempty"`
	Capabilities     []string                        `json:"capabilities,omitempty"`
	SnapshotInputs   map[string]SnapshotInputConfig  `json:"input_types,omitempty"`
	SnapshotOutputs  map[string]SnapshotOutputConfig `json:"output_types,omitempty"`
	Validation       string                          `json:"validation,omitempty"`
	ReviewValidation *ReviewValidationRequirement    `json:"review_validation,omitempty"`
	// Env is TaskEnv (underlying map[string]string) so numeric values —
	// e.g. the ((run_id)) reserved var interpolated into
	// AGENT_PIPELINE_RUN_ID by CreateRun materialization (F30) — coerce
	// to strings instead of failing the instance-config unmarshal.
	Env      TaskEnv          `json:"env,omitempty"`
	Timeout  string           `json:"timeout,omitempty"`
	Limits   *ContainerLimits `json:"container_limits,omitempty"`
	Requests *ContainerLimits `json:"container_requests,omitempty"`
	// BrokerAuthority is renderer-owned compiled authority. Source selectors
	// live under broker_profiles and are erased before this field is injected.
	BrokerAuthority []AgentBrokerProfile `json:"broker_authority,omitempty"`
	// BrokerAuthorityTrusted is an in-memory server-derived discriminator. It
	// never serializes, so authored YAML/JSON cannot set it; it is retained on
	// the rendered config only long enough for ordinary config validation.
	BrokerAuthorityTrusted bool `json:"-"`
}

// SetBrokerAuthority is the only renderer-facing path that marks broker
// authority as server-derived. JSON decoding deliberately cannot set its trust
// discriminator, so ordinary authored pipeline steps are rejected before
// planning even if they reproduce a syntactically valid profile.
func (step *AgentStep) SetBrokerAuthority(profiles []AgentBrokerProfile) {
	step.BrokerAuthority = cloneAgentBrokerProfiles(profiles)
	step.BrokerAuthorityTrusted = len(profiles) > 0
}

func (step *AgentStep) brokerAuthorityIsTrusted() bool { return step.BrokerAuthorityTrusted }

func cloneAgentBrokerProfiles(source []AgentBrokerProfile) []AgentBrokerProfile {
	if source == nil {
		return nil
	}
	cloned := make([]AgentBrokerProfile, len(source))
	for index, profile := range source {
		cloned[index] = profile
		cloned[index].Profile = append([]byte(nil), profile.Profile...)
	}
	return cloned
}

// ValidateBrokerAuthority ensures runtime broker authority is complete,
// function-scoped, digest-pinned, and attached to one broker image. Profile
// semantic validation remains with the workflow compiler, which owns the
// broker domain types without creating an atc import cycle.
func (step *AgentStep) ValidateBrokerAuthority() error {
	if len(step.BrokerAuthority) == 0 {
		return nil
	}
	if strings.TrimSpace(step.FunctionID) == "" {
		return fmt.Errorf("agent broker authority requires function_id")
	}
	image := ""
	seen := map[string]struct{}{}
	for index, profile := range step.BrokerAuthority {
		if profile.FunctionID != step.FunctionID || strings.TrimSpace(profile.Tool) == "" || strings.TrimSpace(profile.Tier) == "" || strings.TrimSpace(profile.Effort) == "" || strings.TrimSpace(profile.ProfileID) == "" || profile.ProfileRevision <= 0 {
			return fmt.Errorf("agent broker authority %d has invalid function scope or identity", index)
		}
		if err := snapshot.Digest(profile.ProfileDigest).Validate(); err != nil || !strings.HasPrefix(profile.ProfileDigest, "sha256:") {
			return fmt.Errorf("agent broker authority %d has invalid profile digest", index)
		}
		if err := ValidatePinnedOCIImage(profile.WorkerImage); err != nil {
			return fmt.Errorf("agent broker authority %d has invalid worker image: %w", index, err)
		}
		if len(profile.Profile) == 0 || !json.Valid(profile.Profile) {
			return fmt.Errorf("agent broker authority %d has invalid frozen profile", index)
		}
		key := profile.Tool + "\x00" + profile.Tier + "\x00" + profile.Effort
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("agent broker authority has duplicate tool selector")
		}
		seen[key] = struct{}{}
		if image == "" {
			image = profile.WorkerImage
		} else if image != profile.WorkerImage {
			return fmt.Errorf("agent broker authority profiles must share one worker image")
		}
	}
	return nil
}

func (step *AgentStep) Visit(v StepVisitor) error {
	return v.VisitAgent(step)
}

type SetPipelineStep struct {
	Name         string       `json:"set_pipeline"`
	File         string       `json:"file,omitempty"`
	Team         string       `json:"team,omitempty"`
	Vars         Params       `json:"vars,omitempty"`
	VarFiles     []string     `json:"var_files,omitempty"`
	InstanceVars InstanceVars `json:"instance_vars,omitempty"`
}

func (step *SetPipelineStep) Visit(v StepVisitor) error {
	return v.VisitSetPipeline(step)
}

type LoadVarStep struct {
	Name   string `json:"load_var"`
	File   string `json:"file,omitempty"`
	Format string `json:"format,omitempty"`
	Reveal bool   `json:"reveal,omitempty"`
}

type LoadSnapshotStep struct {
	Name          string           `json:"load_snapshot"`
	ID            string           `json:"id"`
	Type          snapshot.TypeRef `json:"type"`
	Optional      bool             `json:"optional,omitempty"`
	WorkflowRunID string           `json:"workflow_run_id,omitempty"`
}

var loadSnapshotParameterPattern = regexp.MustCompile(`^\(\(([a-z][a-z0-9_-]*)\)\)$`)

func (step LoadSnapshotStep) validateWire() error {
	if err := step.Type.Validate(); err != nil {
		return err
	}
	if step.ID == "" {
		return fmt.Errorf("load_snapshot: id is required")
	}
	if _, parameter := loadSnapshotParameterName(step.ID); !parameter {
		if step.ID == "0" {
			if !step.Optional {
				return fmt.Errorf("load_snapshot: id 0 requires optional: true")
			}
		} else if _, err := snapshot.ParseSnapshotID(step.ID); err != nil {
			return fmt.Errorf("load_snapshot: id: %w", err)
		}
	}
	if step.WorkflowRunID != "" {
		if parameter, templated := loadSnapshotParameterName(step.WorkflowRunID); templated {
			if parameter != "workflow_run_id" {
				return fmt.Errorf("load_snapshot: workflow_run_id must use ((workflow_run_id))")
			}
		} else if _, err := snapshot.ParseWorkflowRunID(step.WorkflowRunID); err != nil {
			return fmt.Errorf("load_snapshot: workflow_run_id: %w", err)
		}
	}
	return nil
}

func loadSnapshotParameterName(value string) (string, bool) {
	matches := loadSnapshotParameterPattern.FindStringSubmatch(value)
	if len(matches) != 2 {
		return "", false
	}
	return matches[1], true
}

func (step LoadSnapshotStep) MarshalJSON() ([]byte, error) {
	if err := step.validateWire(); err != nil {
		return nil, err
	}
	type wire LoadSnapshotStep
	return json.Marshal(wire(step))
}

func (step *LoadSnapshotStep) UnmarshalJSON(data []byte) error {
	type wire LoadSnapshotStep
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("load_snapshot: trailing JSON value")
		}
		return err
	}
	parsed := LoadSnapshotStep(decoded)
	if err := parsed.validateWire(); err != nil {
		return err
	}
	*step = parsed
	return nil
}

func (step *LoadSnapshotStep) Visit(v StepVisitor) error {
	return v.VisitLoadSnapshot(step)
}

type AwaitSnapshotOnTimeout string

const (
	AwaitSnapshotOnTimeoutFail    AwaitSnapshotOnTimeout = "fail"
	AwaitSnapshotOnTimeoutDefault AwaitSnapshotOnTimeout = "default"
)

func (policy AwaitSnapshotOnTimeout) Validate() error {
	switch policy {
	case AwaitSnapshotOnTimeoutFail, AwaitSnapshotOnTimeoutDefault:
		return nil
	default:
		return fmt.Errorf("await_snapshot: on_timeout must be fail or default")
	}
}

// AwaitSnapshotStep is a visible interaction boundary. It consumes one
// trusted question/v1 artifact, or synthesizes a server-bound merge question
// from one exact repository-change/v1 input, and publishes one immutable
// human-answer/v1 artifact after the durable wait is resolved.
// MergeApprovalIntent is the authored half of a merge approval. It carries no
// base assertion: publisher.MergeBaseParameter is server-derived from the
// bound repository-change/v1 input at execution time, exactly like
// workflow_run_id is renderer-owned.
type MergeApprovalIntent struct {
	Input                 string            `json:"input"`
	Publisher             snapshot.TypeRef  `json:"publisher"`
	Destination           string            `json:"destination"`
	Parameters            map[string]string `json:"parameters"`
	ApprovalPolicyVersion string            `json:"approval_policy_version"`
	Prompt                string            `json:"prompt"`
}

func (intent MergeApprovalIntent) validateWire() error {
	if strings.TrimSpace(intent.Input) == "" {
		return fmt.Errorf("await_snapshot: merge_approval input is required")
	}
	if strings.TrimSpace(intent.Prompt) != intent.Prompt || intent.Prompt == "" || len(intent.Prompt) > 4096 || strings.IndexByte(intent.Prompt, 0) >= 0 {
		return fmt.Errorf("await_snapshot: merge_approval prompt is invalid")
	}
	if err := publisher.ValidateAuthoredMergeIntent(publisher.AuthoredMergeIntent{
		Publisher: intent.Publisher, Destination: intent.Destination,
		Parameters: intent.Parameters, ApprovalPolicyVersion: intent.ApprovalPolicyVersion,
	}); err != nil {
		return fmt.Errorf("await_snapshot: merge_approval intent is invalid: %w", err)
	}
	return nil
}

type AwaitSnapshotStep struct {
	Name                    string                              `json:"await_snapshot"`
	Question                string                              `json:"question,omitempty"`
	MergeApproval           *MergeApprovalIntent                `json:"merge_approval,omitempty"`
	Validation              string                              `json:"validation,omitempty"`
	MergeApprovalValidation *MergeApprovalValidationRequirement `json:"merge_approval_validation,omitempty"`
	Type                    snapshot.TypeRef                    `json:"type"`
	OnTimeout               AwaitSnapshotOnTimeout              `json:"on_timeout"`
	DefaultSnapshotID       string                              `json:"default_snapshot_id,omitempty"`
	WorkflowPort            string                              `json:"workflow_port,omitempty"`
	WorkflowDefinitionID    int                                 `json:"workflow_definition_id,omitempty"`
	WorkflowRunID           string                              `json:"workflow_run_id,omitempty"`
}

func (step AwaitSnapshotStep) validateWire() error {
	if strings.TrimSpace(step.Name) == "" {
		return fmt.Errorf("await_snapshot: output name is required")
	}
	hasQuestion := strings.TrimSpace(step.Question) != ""
	hasMergeApproval := step.MergeApproval != nil
	selected := 0
	for _, present := range []bool{hasQuestion, hasMergeApproval} {
		if present {
			selected++
		}
	}
	if selected != 1 {
		return fmt.Errorf("await_snapshot: exactly one of question or merge_approval is required")
	}
	if hasMergeApproval {
		if err := step.MergeApproval.validateWire(); err != nil {
			return err
		}
		if step.OnTimeout != AwaitSnapshotOnTimeoutFail || step.DefaultSnapshotID != "" {
			return fmt.Errorf("await_snapshot: merge approval must fail on timeout without a default")
		}
	}
	if hasQuestion && (step.Validation != "" || step.MergeApprovalValidation != nil) {
		return fmt.Errorf("await_snapshot: validation is only valid for a server-bound approval")
	}
	if err := step.Type.Validate(); err != nil || step.Type != snapshot.TypeRef("human-answer/v1") {
		return fmt.Errorf("await_snapshot: type must be human-answer/v1")
	}
	if err := step.OnTimeout.Validate(); err != nil {
		return err
	}
	switch step.OnTimeout {
	case AwaitSnapshotOnTimeoutFail:
		if step.DefaultSnapshotID != "" {
			return fmt.Errorf("await_snapshot: default_snapshot_id is only valid when on_timeout is default")
		}
	case AwaitSnapshotOnTimeoutDefault:
		if _, err := snapshot.ParseSnapshotID(step.DefaultSnapshotID); err != nil {
			return fmt.Errorf("await_snapshot: default_snapshot_id: %w", err)
		}
	}
	if step.WorkflowRunID != "" {
		if parameter, templated := loadSnapshotParameterName(step.WorkflowRunID); templated {
			if parameter != "workflow_run_id" {
				return fmt.Errorf("await_snapshot: workflow_run_id must use ((workflow_run_id))")
			}
		} else if _, err := snapshot.ParseWorkflowRunID(step.WorkflowRunID); err != nil {
			return fmt.Errorf("await_snapshot: workflow_run_id: %w", err)
		}
	}
	if step.WorkflowDefinitionID < 0 {
		return fmt.Errorf("await_snapshot: workflow definition ID must not be negative")
	}
	if step.WorkflowPort != "" && (step.WorkflowDefinitionID <= 0 || step.WorkflowRunID == "") {
		return fmt.Errorf("await_snapshot: workflow output linkage is incomplete")
	}
	if step.WorkflowDefinitionID > 0 && step.WorkflowRunID == "" {
		return fmt.Errorf("await_snapshot: workflow execution linkage is incomplete")
	}
	return nil
}

func (step AwaitSnapshotStep) MarshalJSON() ([]byte, error) {
	if err := step.validateWire(); err != nil {
		return nil, err
	}
	type wire AwaitSnapshotStep
	return json.Marshal(wire(step))
}

func (step *AwaitSnapshotStep) UnmarshalJSON(data []byte) error {
	type wire AwaitSnapshotStep
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("await_snapshot: trailing JSON value")
		}
		return err
	}
	parsed := AwaitSnapshotStep(decoded)
	if err := parsed.validateWire(); err != nil {
		return err
	}
	*step = parsed
	return nil
}

func (step *AwaitSnapshotStep) Visit(v StepVisitor) error {
	return v.VisitAwaitSnapshot(step)
}

// PublishSnapshotStep is an explicit external side-effect boundary over one
// exact typed snapshot. Credentials and verified approval actors are never
// authored in this wire shape; the web node supplies them at execution.
type PublishSnapshotStep struct {
	Name                  string                        `json:"publish_snapshot"`
	Publisher             snapshot.TypeRef              `json:"publisher"`
	Input                 string                        `json:"input"`
	InputType             snapshot.TypeRef              `json:"input_type"`
	Destination           string                        `json:"destination"`
	Mode                  publisher.Mode                `json:"mode"`
	Parameters            map[string]string             `json:"parameters,omitempty"`
	ApprovalPolicyVersion string                        `json:"approval_policy_version"`
	Approval              string                        `json:"approval,omitempty"`
	WorkflowRunID         string                        `json:"workflow_run_id,omitempty"`
	Validation            string                        `json:"validation,omitempty"`
	PublishValidation     *PublishValidationRequirement `json:"publish_validation,omitempty"`
}

func (step PublishSnapshotStep) validateWire() error {
	if strings.TrimSpace(step.Name) == "" {
		return fmt.Errorf("publish_snapshot: name is required")
	}
	if strings.TrimSpace(step.Input) == "" {
		return fmt.Errorf("publish_snapshot: input is required")
	}
	if err := step.InputType.Validate(); err != nil {
		return fmt.Errorf("publish_snapshot: input_type: %w", err)
	}
	request := publisher.Request{
		Publisher: step.Publisher,
		Input: snapshot.SnapshotRef{
			ID: 1, Type: step.InputType,
			Digest: snapshot.Digest("sha256:" + strings.Repeat("0", 64)),
		},
		Destination: step.Destination, Mode: step.Mode,
		Parameters: step.Parameters, ApprovalPolicyVersion: step.ApprovalPolicyVersion,
		// Wire validation checks only the authored publication shape. Runtime
		// replaces this sentinel with authenticated build identity.
		Authority: publisher.Authority{TeamID: 1, TeamName: "server-verified", BuildID: 1, Actor: "server-verified"},
	}
	if step.Mode == publisher.ModeMerge {
		if warning, err := ValidateIdentifier(step.Approval); strings.TrimSpace(step.Approval) == "" || err != nil || warning != nil {
			return fmt.Errorf("publish_snapshot: merge approval artifact is invalid")
		}
		// The base assertion is server-derived at execution time from the same
		// repository-change/v1 snapshot the approval pinned by digest, so wire
		// validation stands a probe in for it and refuses an authored one.
		probed, err := publisher.ProbeMergeBase(step.Parameters)
		if err != nil {
			return fmt.Errorf("publish_snapshot: %w", err)
		}
		request.Parameters = probed
		if step.WorkflowRunID != "" {
			if parameter, templated := loadSnapshotParameterName(step.WorkflowRunID); templated {
				if parameter != "workflow_run_id" {
					return fmt.Errorf("publish_snapshot: workflow_run_id uses an invalid template parameter")
				}
			} else if _, err := snapshot.ParseWorkflowRunID(step.WorkflowRunID); err != nil {
				return fmt.Errorf("publish_snapshot: workflow_run_id is invalid")
			}
		}
		request.ApprovedBy = "server-verified"
		request.Approval = &publisher.ApprovalEvidence{
			WaitID:     1,
			Question:   snapshot.SnapshotRef{ID: 2, Type: "question/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("1", 64))},
			Answer:     snapshot.SnapshotRef{ID: 3, Type: "human-answer/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("2", 64))},
			ResolvedBy: "server-verified", ResolvedAt: time.Unix(1, 0).UTC(),
		}
	} else if step.Approval != "" || step.WorkflowRunID != "" {
		return fmt.Errorf("publish_snapshot: approval linkage requires merge")
	}
	if err := request.Validate(); err != nil {
		return fmt.Errorf("publish_snapshot: %w", err)
	}
	return nil
}

func (step PublishSnapshotStep) MarshalJSON() ([]byte, error) {
	if err := step.validateWire(); err != nil {
		return nil, err
	}
	type wire PublishSnapshotStep
	return json.Marshal(wire(step))
}

func (step *PublishSnapshotStep) UnmarshalJSON(data []byte) error {
	type wire PublishSnapshotStep
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("publish_snapshot: trailing JSON value")
		}
		return err
	}
	parsed := PublishSnapshotStep(decoded)
	if err := parsed.validateWire(); err != nil {
		return err
	}
	parsed.Parameters = clonePublishSnapshotParameters(parsed.Parameters)
	*step = parsed
	return nil
}

func clonePublishSnapshotParameters(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for name, value := range source {
		cloned[name] = value
	}
	return cloned
}

func (step *PublishSnapshotStep) Visit(visitor StepVisitor) error {
	return visitor.VisitPublishSnapshot(step)
}

func (step *LoadVarStep) Visit(v StepVisitor) error {
	return v.VisitLoadVar(step)
}

type TryStep struct {
	Step Step `json:"try"`
}

func (step *TryStep) Visit(v StepVisitor) error {
	return v.VisitTry(step)
}

type DoStep struct {
	Steps []Step `json:"do"`
}

func (step *DoStep) Visit(v StepVisitor) error {
	return v.VisitDo(step)
}

type InParallelStep struct {
	Config InParallelConfig `json:"in_parallel"`
}

func (step *InParallelStep) Visit(v StepVisitor) error {
	return v.VisitInParallel(step)
}

type InParallelConfig struct {
	Steps    []Step `json:"steps,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	FailFast bool   `json:"fail_fast,omitempty"`
}

func (c *InParallelConfig) UnmarshalJSON(payload []byte) error {
	var data any
	err := json.Unmarshal(payload, &data)
	if err != nil {
		return err
	}

	switch actual := data.(type) {
	case []any:
		if err := json.Unmarshal(payload, &c.Steps); err != nil {
			return fmt.Errorf("failed to unmarshal parallel steps: %s", err)
		}
	case map[string]any:
		// Used to avoid infinite recursion when unmarshalling this variant.
		type target InParallelConfig

		var t target
		if err := json.Unmarshal(payload, &t); err != nil {
			return fmt.Errorf("failed to unmarshal parallel config: %s", err)
		}

		c.Steps, c.Limit, c.FailFast = t.Steps, t.Limit, t.FailFast
	default:
		return fmt.Errorf("wrong type for parallel config: %v", actual)
	}

	return nil
}

type AcrossVarConfig struct {
	Var         string             `json:"var"`
	Values      any                `json:"values,omitempty"`
	MaxInFlight *MaxInFlightConfig `json:"max_in_flight,omitempty"`
}

func (config *AcrossVarConfig) UnmarshalJSON(data []byte) error {
	// Used to avoid infinite recursion when unmarshalling.
	type target AcrossVarConfig

	var t target
	if err := unmarshalStrict(data, &t); err != nil {
		return err
	}

	*config = AcrossVarConfig(t)
	return nil
}

type AcrossStep struct {
	Step     StepConfig        `json:"-"`
	Vars     []AcrossVarConfig `json:"across"`
	FailFast bool              `json:"fail_fast,omitempty"`
}

func (step *AcrossStep) ParseJSON(data []byte) error {
	return json.Unmarshal(data, step)
}

func (step *AcrossStep) Visit(v StepVisitor) error {
	return v.VisitAcross(step)
}

func (step *AcrossStep) Wrap(sub StepConfig) {
	step.Step = sub
}

func (step *AcrossStep) Unwrap() StepConfig {
	return step.Step
}

type RetryStep struct {
	Step     StepConfig `json:"-"`
	Attempts int        `json:"attempts"`
}

func (step *RetryStep) Wrap(sub StepConfig) {
	step.Step = sub
}

func (step *RetryStep) Unwrap() StepConfig {
	return step.Step
}

func (step *RetryStep) Visit(v StepVisitor) error {
	return v.VisitRetry(step)
}

type TimeoutStep struct {
	Step StepConfig `json:"-"`

	// it's very tempting to make this a Duration type, but that would probably
	// prevent using `((vars))` to parameterize it
	Duration string `json:"timeout"`
}

func (step *TimeoutStep) Wrap(sub StepConfig) {
	step.Step = sub
}

func (step *TimeoutStep) Unwrap() StepConfig {
	return step.Step
}

func (step *TimeoutStep) Visit(v StepVisitor) error {
	return v.VisitTimeout(step)
}

type OnSuccessStep struct {
	Step StepConfig `json:"-"`
	Hook Step       `json:"on_success"`
}

func (step *OnSuccessStep) Wrap(sub StepConfig) {
	step.Step = sub
}

func (step *OnSuccessStep) Unwrap() StepConfig {
	return step.Step
}

func (step *OnSuccessStep) Visit(v StepVisitor) error {
	return v.VisitOnSuccess(step)
}

type OnFailureStep struct {
	Step StepConfig `json:"-"`
	Hook Step       `json:"on_failure"`
}

func (step *OnFailureStep) Wrap(sub StepConfig) {
	step.Step = sub
}

func (step *OnFailureStep) Unwrap() StepConfig {
	return step.Step
}

func (step *OnFailureStep) Visit(v StepVisitor) error {
	return v.VisitOnFailure(step)
}

type OnErrorStep struct {
	Step StepConfig `json:"-"`
	Hook Step       `json:"on_error"`
}

func (step *OnErrorStep) Wrap(sub StepConfig) {
	step.Step = sub
}

func (step *OnErrorStep) Unwrap() StepConfig {
	return step.Step
}

func (step *OnErrorStep) Visit(v StepVisitor) error {
	return v.VisitOnError(step)
}

type OnAbortStep struct {
	Step StepConfig `json:"-"`
	Hook Step       `json:"on_abort"`
}

func (step *OnAbortStep) Wrap(sub StepConfig) {
	step.Step = sub
}

func (step *OnAbortStep) Unwrap() StepConfig {
	return step.Step
}

func (step *OnAbortStep) Visit(v StepVisitor) error {
	return v.VisitOnAbort(step)
}

type EnsureStep struct {
	Step StepConfig `json:"-"`
	Hook Step       `json:"ensure"`
}

func (step *EnsureStep) Wrap(sub StepConfig) {
	step.Step = sub
}

func (step *EnsureStep) Unwrap() StepConfig {
	return step.Step
}

func (step *EnsureStep) Visit(v StepVisitor) error {
	return v.VisitEnsure(step)
}

// MaxInFlightConfig can represent either running all values in an AcrossStep
// in parallel or a applying a limit to the sub-steps that can run at once.
type MaxInFlightConfig struct {
	All   bool
	Limit int
}

const MaxInFlightAll = "all"

func (c *MaxInFlightConfig) UnmarshalJSON(version []byte) error {
	if bytes.HasPrefix(version, []byte{'"'}) {
		var data string
		err := json.Unmarshal(version, &data)
		if err != nil {
			return err
		}
		if data != MaxInFlightAll {
			return fmt.Errorf("invalid max_in_flight %q", data)
		}
		c.All = true
		return nil
	}
	err := json.Unmarshal(version, &c.Limit)
	if err != nil {
		return err
	}

	return nil
}

func (c *MaxInFlightConfig) MarshalJSON() ([]byte, error) {
	if c.All {
		return json.Marshal(MaxInFlightAll)
	}

	return json.Marshal(c.Limit)
}

func (c *MaxInFlightConfig) EffectiveLimit(numSteps int) int {
	if c == nil {
		return 1
	}
	if c.All {
		return numSteps
	}
	return c.Limit
}

// A VersionConfig represents the choice to include every version of a
// resource, the latest version of a resource, or a pinned (specific) one.
type VersionConfig struct {
	Every  bool
	Latest bool
	Pinned Version
}

const VersionLatest = "latest"
const VersionEvery = "every"

func (c *VersionConfig) UnmarshalJSON(version []byte) error {
	var data any

	err := json.Unmarshal(version, &data)
	if err != nil {
		return err
	}

	switch actual := data.(type) {
	case string:
		c.Every = actual == VersionEvery
		c.Latest = actual == VersionLatest
	case map[string]any:
		version := Version{}

		for k, v := range actual {
			if s, ok := v.(string); ok {
				version[k] = s
				continue
			}

			return fmt.Errorf("the value %v of %s is not a string", v, k)
		}

		c.Pinned = version
	default:
		return errors.New("unknown type for version")
	}

	return nil
}

func (c *VersionConfig) MarshalJSON() ([]byte, error) {
	if c.Latest {
		return json.Marshal(VersionLatest)
	}

	if c.Every {
		return json.Marshal(VersionEvery)
	}

	if c.Pinned != nil {
		return json.Marshal(c.Pinned)
	}

	return json.Marshal("")
}

const InputsAll = "all"
const InputsDetect = "detect"

// A InputsConfig represents the choice to include every artifact within the
// job as an input to the put step or specific ones.
type InputsConfig struct {
	All       bool
	Detect    bool
	Specified []string
}

func (c *InputsConfig) UnmarshalJSON(inputs []byte) error {
	var data any

	err := json.Unmarshal(inputs, &data)
	if err != nil {
		return err
	}

	switch actual := data.(type) {
	case string:
		c.All = actual == InputsAll
		c.Detect = actual == InputsDetect
	case []any:
		inputs := []string{}

		for _, v := range actual {
			str, ok := v.(string)
			if !ok {
				return fmt.Errorf("non-string put input: %v", v)
			}

			inputs = append(inputs, strings.TrimSpace(str))
		}

		c.Specified = inputs
	default:
		return errors.New("unknown type for put inputs")
	}

	return nil
}

func (c InputsConfig) MarshalJSON() ([]byte, error) {
	if c.All {
		return json.Marshal(InputsAll)
	}

	if c.Detect {
		return json.Marshal(InputsDetect)
	}

	if c.Specified != nil {
		return json.Marshal(c.Specified)
	}

	return json.Marshal("")
}

func unmarshalStrict(data []byte, to any) error {
	decoder := json.NewDecoder(bytes.NewBuffer(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(to)
}
