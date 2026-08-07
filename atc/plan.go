package atc

import (
	"encoding/json"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
)

type Plan struct {
	ID       PlanID `json:"id"`
	Attempts []int  `json:"attempts,omitempty"`

	Get             *GetPlan             `json:"get,omitempty"`
	Put             *PutPlan             `json:"put,omitempty"`
	Check           *CheckPlan           `json:"check,omitempty"`
	Task            *TaskPlan            `json:"task,omitempty"`
	Run             *RunPlan             `json:"run,omitempty"`
	Agent           *AgentPlan           `json:"agent,omitempty"`
	SetPipeline     *SetPipelinePlan     `json:"set_pipeline,omitempty"`
	LoadVar         *LoadVarPlan         `json:"load_var,omitempty"`
	LoadSnapshot    *LoadSnapshotPlan    `json:"load_snapshot,omitempty"`
	AwaitSnapshot   *AwaitSnapshotPlan   `json:"await_snapshot,omitempty"`
	PublishSnapshot *PublishSnapshotPlan `json:"publish_snapshot,omitempty"`

	Do         *DoPlan         `json:"do,omitempty"`
	InParallel *InParallelPlan `json:"in_parallel,omitempty"`
	Across     *AcrossPlan     `json:"across,omitempty"`

	OnSuccess *OnSuccessPlan `json:"on_success,omitempty"`
	OnFailure *OnFailurePlan `json:"on_failure,omitempty"`
	OnAbort   *OnAbortPlan   `json:"on_abort,omitempty"`
	OnError   *OnErrorPlan   `json:"on_error,omitempty"`
	Ensure    *EnsurePlan    `json:"ensure,omitempty"`

	Try     *TryPlan     `json:"try,omitempty"`
	Timeout *TimeoutPlan `json:"timeout,omitempty"`
	Retry   *RetryPlan   `json:"retry,omitempty"`

	// used for 'fly execute'
	ArtifactInput  *ArtifactInputPlan  `json:"artifact_input,omitempty"`
	ArtifactOutput *ArtifactOutputPlan `json:"artifact_output,omitempty"`

	// sidecar container (emitted dynamically via Sidecar events, similar to ImageCheck/ImageGet)
	Sidecar *SidecarPlan `json:"sidecar,omitempty"`

	// deprecated, kept for backwards compatibility to be able to show old builds
	DependentGet *DependentGetPlan `json:"dependent_get,omitempty"`
}

func (plan *Plan) Each(f func(*Plan)) {
	f(plan)

	if plan.Do != nil {
		for i, p := range *plan.Do {
			p.Each(f)
			(*plan.Do)[i] = p
		}
	}

	if plan.InParallel != nil {
		for i, p := range plan.InParallel.Steps {
			p.Each(f)
			plan.InParallel.Steps[i] = p
		}
	}

	if plan.OnSuccess != nil {
		plan.OnSuccess.Step.Each(f)
		plan.OnSuccess.Next.Each(f)
	}

	if plan.OnFailure != nil {
		plan.OnFailure.Step.Each(f)
		plan.OnFailure.Next.Each(f)
	}

	if plan.OnAbort != nil {
		plan.OnAbort.Step.Each(f)
		plan.OnAbort.Next.Each(f)
	}

	if plan.OnError != nil {
		plan.OnError.Step.Each(f)
		plan.OnError.Next.Each(f)
	}

	if plan.Ensure != nil {
		plan.Ensure.Step.Each(f)
		plan.Ensure.Next.Each(f)
	}

	if plan.Try != nil {
		plan.Try.Step.Each(f)
	}

	if plan.Timeout != nil {
		plan.Timeout.Step.Each(f)
	}

	if plan.Retry != nil {
		for i, p := range *plan.Retry {
			p.Each(f)
			(*plan.Retry)[i] = p
		}
	}

	if plan.Get != nil {
		(&plan.Get.TypeImage).EachPlan(f)
	}

	if plan.Put != nil {
		(&plan.Put.TypeImage).EachPlan(f)
	}

	if plan.Check != nil {
		(&plan.Check.TypeImage).EachPlan(f)
	}
}

type PlanID string

func (id PlanID) String() string {
	return string(id)
}

type TypeImage struct {
	// Image of the container. One of these must be specified.
	CheckPlan *Plan `json:"check_plan,omitempty"`
	GetPlan   *Plan `json:"get_plan,omitempty"`

	// The bottom-most resource type that this get step relies on.
	BaseType string `json:"base_type,omitempty"`

	// ImageRef is a direct container image reference (e.g. "repo:tag" or
	// "repo@sha256:digest"). When set, the image is passed directly to the
	// container runtime without any check/get plans.
	ImageRef string `json:"image_ref,omitempty"`

	// Privileged indicates whether the parent resource type is privileged.
	Privileged bool `json:"privileged,omitempty"`
}

func (t *TypeImage) EachPlan(f func(*Plan)) {
	if t.CheckPlan != nil {
		t.CheckPlan.Each(f)
	}
	if t.GetPlan != nil {
		t.GetPlan.Each(f)
	}
}

type ArtifactInputPlan struct {
	ArtifactID int    `json:"artifact_id"`
	Name       string `json:"name"`
}

type ArtifactOutputPlan struct {
	Name string `json:"name"`
}

type OnAbortPlan struct {
	Step Plan `json:"step"`
	Next Plan `json:"on_abort"`
}

type OnErrorPlan struct {
	Step Plan `json:"step"`
	Next Plan `json:"on_error"`
}

type OnFailurePlan struct {
	Step Plan `json:"step"`
	Next Plan `json:"on_failure"`
}

type EnsurePlan struct {
	Step Plan `json:"step"`
	Next Plan `json:"ensure"`
}

type OnSuccessPlan struct {
	Step Plan `json:"step"`
	Next Plan `json:"on_success"`
}

type TimeoutPlan struct {
	Step     Plan   `json:"step"`
	Duration string `json:"duration"`
}

type TryPlan struct {
	Step Plan `json:"step"`
}

type InParallelPlan struct {
	Steps    []Plan `json:"steps"`
	Limit    int    `json:"limit,omitempty"`
	FailFast bool   `json:"fail_fast,omitempty"`
}

type AcrossPlan struct {
	Vars []AcrossVar `json:"vars"`
	// SubStepTemplate contains the uninterpolated JSON encoded plan for the
	// substep. This template must be interpolated for each substep using the
	// across vars, and the plan IDs must be updated.
	SubStepTemplate string `json:"substep_template"`
	FailFast        bool   `json:"fail_fast,omitempty"`
}

type AcrossVar struct {
	Var         string             `json:"name"`
	Values      any                `json:"values,omitempty"`
	MaxInFlight *MaxInFlightConfig `json:"max_in_flight,omitempty"`
}

type VarScopedPlan struct {
	Step   Plan  `json:"step"`
	Values []any `json:"values"`
}

type DoPlan []Plan

type GetPlan struct {
	// The name of the step.
	Name string `json:"name,omitempty"`

	// The resource config to fetch from.
	Type   string `json:"type"`
	Source Source `json:"source"`

	// Information needed for fetching the image
	TypeImage TypeImage `json:"image"`

	// The version of the resource to fetch. One of these must be specified.
	Version     *Version `json:"version,omitempty"`
	VersionFrom *PlanID  `json:"version_from,omitempty"`

	// Params to pass to the get operation.
	Params Params `json:"params,omitempty"`

	// A pipeline resource to update with metadata.
	Resource string `json:"resource,omitempty"`

	// Worker tags to influence placement of the container.
	Tags Tags `json:"tags,omitempty"`

	// A timeout to enforce on the resource `get` process. Note that fetching the
	// resource's image does not count towards the timeout.
	Timeout string `json:"timeout,omitempty"`

	// SkipDownload, when true, resolves the resource version without downloading
	// artifacts. The version metadata and image ref URL are registered in the
	// artifact repository, but no container is created.
	SkipDownload bool `json:"skip_download,omitempty"`
}

type PutPlan struct {
	// The name of the step.
	Name string `json:"name"`

	// The resource config to push to.
	Type   string `json:"type"`
	Source Source `json:"source"`

	// Information needed for fetching the image
	TypeImage TypeImage `json:"image"`

	// Params to pass to the put operation.
	Params Params `json:"params,omitempty"`

	// Inputs to pass to the put operation.
	Inputs *InputsConfig `json:"inputs,omitempty"`

	// A pipeline resource to save the versions onto.
	Resource string `json:"resource,omitempty"`

	// Worker tags to influence placement of the container.
	Tags Tags `json:"tags,omitempty"`

	// A timeout to enforce on the resource `put` process. Note that fetching the
	// resource's image does not count towards the timeout.
	Timeout string `json:"timeout,omitempty"`

	// If or not expose BUILD_CREATED_BY to build metadata
	ExposeBuildCreatedBy bool `json:"expose_build_created_by,omitempty"`
}

type CheckPlan struct {
	// The name of the step.
	Name string `json:"name"`

	// The resource config to check.
	Type   string `json:"type"`
	Source Source `json:"source"`

	// Information needed for fetching the image
	TypeImage TypeImage `json:"image"`

	// The version to check from. If not specified, defaults to the latest
	// version of the config.
	FromVersion Version `json:"from_version,omitempty"`

	// A pipeline resource, resource type, or prototype to assign the config to.
	Resource     string `json:"resource,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
	Prototype    string `json:"prototype,omitempty"`

	// The interval on which to check - if it has not elapsed since the config
	// was last checked, and the build has not been manually triggered, the check
	// will be skipped. It will also be set as Never if the user has specified
	// for it to not be checked periodically.
	Interval CheckEvery `json:"interval,omitempty"`

	// If set, the check interval will not be respected, (i.e. a new check will
	// be run even if the interval has not elapsed).
	SkipInterval bool `json:"skip_interval,omitempty"`

	// A timeout to enforce on the resource `check` process. Note that fetching
	// the resource's image does not count towards the timeout.
	Timeout string `json:"timeout,omitempty"`

	// Worker tags to influence placement of the container.
	Tags Tags `json:"tags,omitempty"`
}

func (plan CheckPlan) IsResourceCheck() bool {
	return plan.Resource != ""
}

type TaskPlan struct {
	// The name of the step.
	Name string `json:"name"`

	// FunctionID is the stable v3 node identity a typed workflow function
	// declares for this task. It survives renames of Name and is what the plan
	// API exposes as the node's identity.
	FunctionID string `json:"function_id,omitempty"`

	// Run the task in 'privileged' mode. What this means depends on the
	// platform, but typically you expose your workers to more risk by enabling
	// this.
	Privileged bool `json:"privileged"`

	// Run the task in 'hermetic' mode. If set to true, network traffic from
	// the container to external will be dropped.
	Hermetic bool `json:"hermetic"`

	// Worker tags to influence placement of the container.
	Tags Tags `json:"tags,omitempty"`

	// The task config to execute - either fetched from a path at runtime, or
	// provided statically.
	ConfigPath string      `json:"config_path,omitempty"`
	Config     *TaskConfig `json:"config,omitempty"`

	// Limits to set on the Task Container
	Limits *ContainerLimits `json:"container_limits,omitempty"`

	// Requests to set on the Task Container (independent from limits for Burstable QoS)
	Requests *ContainerLimits `json:"container_requests,omitempty"`

	// An artifact in the build plan to use as the task's image. Overrides any
	// image set in the task's config.
	ImageArtifactName string `json:"image,omitempty"`

	// Vars to use to parameterize the task config.
	Vars Params `json:"vars,omitempty"`

	// Params to set in the task's environment.
	Params TaskEnv `json:"params,omitempty"`

	// Remap inputs and output artifacts from task names to other names in the
	// build plan.
	InputMapping  map[string]string `json:"input_mapping,omitempty"`
	OutputMapping map[string]string `json:"output_mapping,omitempty"`

	// Snapshot declarations constrain the effective mapped artifact names.
	SnapshotInputs  map[string]SnapshotInputConfig  `json:"input_types,omitempty"`
	SnapshotOutputs map[string]SnapshotOutputConfig `json:"output_types,omitempty"`
	// ReadOnlyInputs is server-only task authority. It is never decoded from a
	// task config: only the workflow renderer may mark an exact validation input
	// immutable before task execution.
	// It is persisted with the server-built execution plan for retries, but the
	// public plan projection deliberately omits it. TaskStep (the user-facing
	// configuration type) has no corresponding field.
	ReadOnlyInputs map[string]struct{} `json:"read_only_inputs,omitempty"`
	// DevValidationAuthority is emitted only by the trusted schema-v3 renderer.
	// It is retained in the private execution plan for retries but omitted by
	// TaskPlan.Public.
	DevValidationAuthority  *DevValidationAuthority  `json:"dev_validation_authority,omitempty"`
	MergePreflightAuthority *MergePreflightAuthority `json:"merge_preflight_authority,omitempty"`
	// ResourceCaptureAuthority waives exact typed input coverage for the one
	// fixed adapter task that turns untyped resource bytes into the first typed
	// snapshot. Retained in the private plan for retries; omitted by Public.
	ResourceCaptureAuthority *ResourceCaptureAuthority `json:"resource_capture_authority,omitempty"`

	// A timeout to enforce on the task's process. Note that fetching the task's
	// image does not count towards the timeout.
	Timeout string `json:"timeout,omitempty"`

	// Resource types to have available for use when fetching the task's image.
	ResourceTypes ResourceTypes `json:"resource_types,omitempty"`

	// If set, the check plan for the image will be forced to run and not respect
	// the checking interval
	CheckSkipInterval bool `json:"check_skip_interval,omitempty"`

	// Sidecars is a list of sidecar sources: either file paths pointing to
	// sidecar container definition files within the build's artifacts, or
	// inline sidecar configurations.
	Sidecars []SidecarSource `json:"sidecars,omitempty"`
}

type RunPlan struct {
	// The message to run on the prototype.
	Message string `json:"message"`

	// The prototype name.
	Type string `json:"type"`

	// Object to provide to the prototype. Result of merging run.params with
	// prototype.defaults.
	Object Params `json:"object,omitempty"`

	// Run in 'privileged' mode. What this means depends on the platform, but
	// typically you expose your workers to more risk by enabling this.
	Privileged bool `json:"privileged"`

	// Worker tags to influence placement of the container.
	Tags Tags `json:"tags,omitempty"`

	// Limits to set on the Container
	Limits *ContainerLimits `json:"container_limits,omitempty"`

	// Requests to set on the Container (independent from limits for Burstable QoS)
	Requests *ContainerLimits `json:"container_requests,omitempty"`

	// A timeout to enforce on the run step's process. Note that fetching the
	// prototype's image does not count towards the timeout.
	Timeout string `json:"timeout,omitempty"`
}

// AgentPlan is the plan payload for an agent step (shared-contracts §2.8).
// All fields are literal values resolved at plan time; the exec never reads
// workflow definition tables.
type AgentPlan struct {
	Name             string                          `json:"name"`
	FunctionID       string                          `json:"function_id,omitempty"`
	Hermetic         bool                            `json:"hermetic,omitempty"`
	RuntimeImage     string                          `json:"runtime_image,omitempty"`
	Prompt           string                          `json:"prompt,omitempty"`
	Model            string                          `json:"model,omitempty"`
	MaxTurns         int                             `json:"max_turns,omitempty"`
	BudgetSliceUSD   float64                         `json:"budget_slice_usd,omitempty"`
	SystemPrompt     string                          `json:"system_prompt,omitempty"`
	Context          string                          `json:"context,omitempty"`
	Skills           []string                        `json:"skills,omitempty"`
	SkillFiles       map[string]string               `json:"skill_files,omitempty"`
	Sidecars         []SidecarSource                 `json:"sidecars,omitempty"`
	Inputs           []string                        `json:"inputs,omitempty"`
	Outputs          []string                        `json:"outputs,omitempty"`
	InputMapping     map[string]string               `json:"input_mapping,omitempty"`
	OutputMapping    map[string]string               `json:"output_mapping,omitempty"`
	SnapshotInputs   map[string]SnapshotInputConfig  `json:"input_types,omitempty"`
	SnapshotOutputs  map[string]SnapshotOutputConfig `json:"output_types,omitempty"`
	Validation       string                          `json:"validation,omitempty"`
	ReviewValidation *ReviewValidationRequirement    `json:"review_validation,omitempty"`
	Env              map[string]string               `json:"env,omitempty"`
	Timeout          string                          `json:"timeout,omitempty"`
	Limits           *ContainerLimits                `json:"container_limits,omitempty"`
	Requests         *ContainerLimits                `json:"container_requests,omitempty"`
	// BrokerAuthority is trusted, execution-only broker authority. It is
	// projected by workflow rendering from compiled profiles and is omitted
	// from public plans, so authored configuration cannot name a provider or
	// mint broker access.
	BrokerAuthority []AgentBrokerProfile `json:"broker_authority,omitempty"`
}

// AgentBrokerProfile is the exact operator profile selected at compilation
// time for one agent function. Profile contains its canonical frozen authority
// rather than a mutable catalog reference.
type AgentBrokerProfile struct {
	FunctionID      string          `json:"function_id"`
	Tool            string          `json:"tool"`
	Tier            string          `json:"tier"`
	Effort          string          `json:"effort"`
	ProfileID       string          `json:"profile_id"`
	ProfileRevision int             `json:"profile_revision"`
	ProfileDigest   string          `json:"profile_digest"`
	WorkerImage     string          `json:"worker_image"`
	Profile         json.RawMessage `json:"profile"`
}

type SetPipelinePlan struct {
	Name         string         `json:"name"`
	File         string         `json:"file"`
	Team         string         `json:"team,omitempty"`
	Vars         map[string]any `json:"vars,omitempty"`
	VarFiles     []string       `json:"var_files,omitempty"`
	InstanceVars map[string]any `json:"instance_vars,omitempty"`
}

type LoadVarPlan struct {
	Name   string `json:"name"`
	File   string `json:"file"`
	Format string `json:"format,omitempty"`
	Reveal bool   `json:"reveal,omitempty"`
}

type LoadSnapshotPlan struct {
	Name          string           `json:"name"`
	ID            string           `json:"id"`
	Type          snapshot.TypeRef `json:"type"`
	Optional      bool             `json:"optional,omitempty"`
	WorkflowRunID string           `json:"workflow_run_id,omitempty"`
}

type AwaitSnapshotPlan struct {
	Name                    string                              `json:"name"`
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

// PublishSnapshotPlan is the complete, literal publication request planned by
// a visible publish_snapshot node. Destination and parameters are execution
// data and are deliberately redacted by Public.
type PublishSnapshotPlan struct {
	Name                  string                        `json:"name"`
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

type RetryPlan []Plan

type DependentGetPlan struct {
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	Resource string `json:"resource"`
}

// SidecarPlanID returns the plan ID for a sidecar container associated with
// a parent task step. The format mirrors image plan IDs (e.g. "parent/image-get").
func SidecarPlanID(parentPlanID PlanID, sidecarName string) PlanID {
	return parentPlanID + "/sidecar/" + PlanID(sidecarName)
}

// SidecarPlan is a lightweight plan representing a sidecar container in the
// build plan. It uses its own plan type so the UI can render it as a
// step with its own logs, status, and expand/collapse.
type SidecarPlan struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
}

// NewSidecarPlan constructs a Plan for a sidecar container that can be
// serialized and emitted as a Sidecar event. The UI uses this to create
// a nested step tree entry under the parent task step.
func NewSidecarPlan(parentPlanID PlanID, config SidecarConfig) Plan {
	return Plan{
		ID: SidecarPlanID(parentPlanID, config.Name),
		Sidecar: &SidecarPlan{
			Name:  config.Name,
			Image: config.Image,
		},
	}
}
