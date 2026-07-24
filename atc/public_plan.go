package atc

import (
	"encoding/json"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
)

func (plan *Plan) Public() *json.RawMessage {
	if plan == nil {
		return nil
	}

	var public struct {
		ID PlanID `json:"id,omitempty"`

		InParallel      *json.RawMessage `json:"in_parallel,omitempty"`
		Across          *json.RawMessage `json:"across,omitempty"`
		Do              *json.RawMessage `json:"do,omitempty"`
		Get             *json.RawMessage `json:"get,omitempty"`
		Put             *json.RawMessage `json:"put,omitempty"`
		Check           *json.RawMessage `json:"check,omitempty"`
		Task            *json.RawMessage `json:"task,omitempty"`
		Run             *json.RawMessage `json:"run,omitempty"`
		Agent           *json.RawMessage `json:"agent,omitempty"`
		SetPipeline     *json.RawMessage `json:"set_pipeline,omitempty"`
		LoadVar         *json.RawMessage `json:"load_var,omitempty"`
		LoadSnapshot    *json.RawMessage `json:"load_snapshot,omitempty"`
		AwaitSnapshot   *json.RawMessage `json:"await_snapshot,omitempty"`
		PublishSnapshot *json.RawMessage `json:"publish_snapshot,omitempty"`
		OnAbort         *json.RawMessage `json:"on_abort,omitempty"`
		OnError         *json.RawMessage `json:"on_error,omitempty"`
		Ensure          *json.RawMessage `json:"ensure,omitempty"`
		OnSuccess       *json.RawMessage `json:"on_success,omitempty"`
		OnFailure       *json.RawMessage `json:"on_failure,omitempty"`
		Try             *json.RawMessage `json:"try,omitempty"`
		DependentGet    *json.RawMessage `json:"dependent_get,omitempty"`
		Timeout         *json.RawMessage `json:"timeout,omitempty"`
		Retry           *json.RawMessage `json:"retry,omitempty"`
		ArtifactInput   *json.RawMessage `json:"artifact_input,omitempty"`
		ArtifactOutput  *json.RawMessage `json:"artifact_output,omitempty"`
		Sidecar         *json.RawMessage `json:"sidecar,omitempty"`
	}

	public.ID = plan.ID

	if plan.InParallel != nil {
		public.InParallel = plan.InParallel.Public()
	}

	if plan.Across != nil {
		public.Across = plan.Across.Public()
	}

	if plan.Do != nil {
		public.Do = plan.Do.Public()
	}

	if plan.Get != nil {
		public.Get = plan.Get.Public()
	}

	if plan.Put != nil {
		public.Put = plan.Put.Public()
	}

	if plan.Check != nil {
		public.Check = plan.Check.Public()
	}

	if plan.Task != nil {
		public.Task = plan.Task.Public()
	}

	if plan.Run != nil {
		public.Run = plan.Run.Public()
	}

	if plan.Agent != nil {
		public.Agent = plan.Agent.Public()
	}

	if plan.SetPipeline != nil {
		public.SetPipeline = plan.SetPipeline.Public()
	}

	if plan.LoadVar != nil {
		public.LoadVar = plan.LoadVar.Public()
	}

	if plan.LoadSnapshot != nil {
		public.LoadSnapshot = plan.LoadSnapshot.Public()
	}

	if plan.AwaitSnapshot != nil {
		public.AwaitSnapshot = plan.AwaitSnapshot.Public()
	}

	if plan.PublishSnapshot != nil {
		public.PublishSnapshot = plan.PublishSnapshot.Public()
	}

	if plan.OnAbort != nil {
		public.OnAbort = plan.OnAbort.Public()
	}

	if plan.OnError != nil {
		public.OnError = plan.OnError.Public()
	}

	if plan.Ensure != nil {
		public.Ensure = plan.Ensure.Public()
	}

	if plan.OnSuccess != nil {
		public.OnSuccess = plan.OnSuccess.Public()
	}

	if plan.OnFailure != nil {
		public.OnFailure = plan.OnFailure.Public()
	}

	if plan.Try != nil {
		public.Try = plan.Try.Public()
	}

	if plan.Timeout != nil {
		public.Timeout = plan.Timeout.Public()
	}

	if plan.Retry != nil {
		public.Retry = plan.Retry.Public()
	}

	if plan.ArtifactInput != nil {
		public.ArtifactInput = plan.ArtifactInput.Public()
	}

	if plan.ArtifactOutput != nil {
		public.ArtifactOutput = plan.ArtifactOutput.Public()
	}

	if plan.Sidecar != nil {
		public.Sidecar = plan.Sidecar.Public()
	}

	if plan.DependentGet != nil {
		public.DependentGet = plan.DependentGet.Public()
	}

	return enc(public)
}

func (plan InParallelPlan) Public() *json.RawMessage {
	steps := make([]*json.RawMessage, len(plan.Steps))

	for i := 0; i < len(plan.Steps); i++ {
		steps[i] = plan.Steps[i].Public()
	}

	return enc(struct {
		Steps    []*json.RawMessage `json:"steps"`
		Limit    int                `json:"limit,omitempty"`
		FailFast bool               `json:"fail_fast,omitempty"`
	}{
		Steps:    steps,
		Limit:    plan.Limit,
		FailFast: plan.FailFast,
	})
}

func (plan AcrossPlan) Public() *json.RawMessage {
	type acrossVar struct {
		Name string `json:"name"`
	}

	vars := make([]acrossVar, len(plan.Vars))
	for i, v := range plan.Vars {
		vars[i] = acrossVar{
			Name: v.Var,
		}
	}

	return enc(struct {
		Vars     []acrossVar `json:"vars"`
		FailFast bool        `json:"fail_fast,omitempty"`
	}{
		Vars:     vars,
		FailFast: plan.FailFast,
	})
}

func (plan DoPlan) Public() *json.RawMessage {
	public := make([]*json.RawMessage, len(plan))

	for i := 0; i < len(plan); i++ {
		public[i] = plan[i].Public()
	}

	return enc(public)
}

func (plan EnsurePlan) Public() *json.RawMessage {
	return enc(struct {
		Step *json.RawMessage `json:"step"`
		Next *json.RawMessage `json:"ensure"`
	}{
		Step: plan.Step.Public(),
		Next: plan.Next.Public(),
	})
}

func (plan GetPlan) Public() *json.RawMessage {
	return enc(struct {
		Name           string           `json:"name"`
		Type           string           `json:"type"`
		Resource       string           `json:"resource,omitempty"`
		Version        *Version         `json:"version,omitempty"`
		ImageGetPlan   *json.RawMessage `json:"image_get_plan,omitempty"`
		ImageCheckPlan *json.RawMessage `json:"image_check_plan,omitempty"`
	}{
		Type:           plan.Type,
		Name:           plan.Name,
		Resource:       plan.Resource,
		Version:        plan.Version,
		ImageGetPlan:   plan.TypeImage.GetPlan.Public(),
		ImageCheckPlan: plan.TypeImage.CheckPlan.Public(),
	})
}

func (plan DependentGetPlan) Public() *json.RawMessage {
	return enc(struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Resource string `json:"resource"`
	}{
		Type:     plan.Type,
		Name:     plan.Name,
		Resource: plan.Resource,
	})
}

func (plan OnAbortPlan) Public() *json.RawMessage {
	return enc(struct {
		Step *json.RawMessage `json:"step"`
		Next *json.RawMessage `json:"on_abort"`
	}{
		Step: plan.Step.Public(),
		Next: plan.Next.Public(),
	})
}

func (plan OnErrorPlan) Public() *json.RawMessage {
	return enc(struct {
		Step *json.RawMessage `json:"step"`
		Next *json.RawMessage `json:"on_error"`
	}{
		Step: plan.Step.Public(),
		Next: plan.Next.Public(),
	})
}

func (plan OnFailurePlan) Public() *json.RawMessage {
	return enc(struct {
		Step *json.RawMessage `json:"step"`
		Next *json.RawMessage `json:"on_failure"`
	}{
		Step: plan.Step.Public(),
		Next: plan.Next.Public(),
	})
}

func (plan OnSuccessPlan) Public() *json.RawMessage {
	return enc(struct {
		Step *json.RawMessage `json:"step"`
		Next *json.RawMessage `json:"on_success"`
	}{
		Step: plan.Step.Public(),
		Next: plan.Next.Public(),
	})
}

func (plan PutPlan) Public() *json.RawMessage {
	return enc(struct {
		Name           string           `json:"name"`
		Type           string           `json:"type"`
		Resource       string           `json:"resource,omitempty"`
		ImageGetPlan   *json.RawMessage `json:"image_get_plan,omitempty"`
		ImageCheckPlan *json.RawMessage `json:"image_check_plan,omitempty"`
	}{
		Type:           plan.Type,
		Name:           plan.Name,
		Resource:       plan.Resource,
		ImageGetPlan:   plan.TypeImage.GetPlan.Public(),
		ImageCheckPlan: plan.TypeImage.CheckPlan.Public(),
	})
}

func (plan CheckPlan) Public() *json.RawMessage {
	return enc(struct {
		Name           string           `json:"name"`
		Type           string           `json:"type"`
		ImageGetPlan   *json.RawMessage `json:"image_get_plan,omitempty"`
		ImageCheckPlan *json.RawMessage `json:"image_check_plan,omitempty"`
	}{
		Type:           plan.Type,
		Name:           plan.Name,
		ImageGetPlan:   plan.TypeImage.GetPlan.Public(),
		ImageCheckPlan: plan.TypeImage.CheckPlan.Public(),
	})
}

func (plan TaskPlan) Public() *json.RawMessage {
	return enc(struct {
		Name            string                          `json:"name"`
		Privileged      bool                            `json:"privileged"`
		Hermetic        bool                            `json:"hermetic"`
		SnapshotInputs  map[string]SnapshotInputConfig  `json:"input_types,omitempty"`
		SnapshotOutputs map[string]SnapshotOutputConfig `json:"output_types,omitempty"`
	}{
		Name:            plan.Name,
		Privileged:      plan.Privileged,
		Hermetic:        plan.Hermetic,
		SnapshotInputs:  plan.SnapshotInputs,
		SnapshotOutputs: publicSnapshotOutputs(plan.SnapshotOutputs),
	})
}

func (plan RunPlan) Public() *json.RawMessage {
	return enc(struct {
		Message    string `json:"message"`
		Type       string `json:"type"`
		Privileged bool   `json:"privileged"`
	}{
		Message:    plan.Message,
		Type:       plan.Type,
		Privileged: plan.Privileged,
	})
}

func (plan AgentPlan) Public() *json.RawMessage {
	return enc(struct {
		Name            string                          `json:"name"`
		Hermetic        bool                            `json:"hermetic,omitempty"`
		RuntimeImage    string                          `json:"runtime_image,omitempty"`
		Model           string                          `json:"model,omitempty"`
		SnapshotInputs  map[string]SnapshotInputConfig  `json:"input_types,omitempty"`
		SnapshotOutputs map[string]SnapshotOutputConfig `json:"output_types,omitempty"`
	}{
		Name:            plan.Name,
		Hermetic:        plan.Hermetic,
		RuntimeImage:    plan.RuntimeImage,
		Model:           plan.Model,
		SnapshotInputs:  plan.SnapshotInputs,
		SnapshotOutputs: publicSnapshotOutputs(plan.SnapshotOutputs),
	})
}

func publicSnapshotOutputs(outputs map[string]SnapshotOutputConfig) map[string]SnapshotOutputConfig {
	if outputs == nil {
		return nil
	}
	public := make(map[string]SnapshotOutputConfig, len(outputs))
	for name, output := range outputs {
		output.SourceMetadata = nil
		public[name] = output
	}
	return public
}

func (plan SetPipelinePlan) Public() *json.RawMessage {
	return enc(struct {
		Name         string       `json:"name"`
		Team         string       `json:"team"`
		InstanceVars InstanceVars `json:"instance_vars"`
	}{
		Name:         plan.Name,
		Team:         plan.Team,
		InstanceVars: plan.InstanceVars,
	})
}

func (plan LoadVarPlan) Public() *json.RawMessage {
	return enc(struct {
		Name string `json:"name"`
	}{
		Name: plan.Name,
	})
}

func (plan LoadSnapshotPlan) Public() *json.RawMessage {
	return enc(struct {
		Name     string           `json:"name"`
		Type     snapshot.TypeRef `json:"type"`
		Optional bool             `json:"optional,omitempty"`
	}{Name: plan.Name, Type: plan.Type, Optional: plan.Optional})
}

func (plan AwaitSnapshotPlan) Public() *json.RawMessage {
	var mergeInput string
	var mergePublisher snapshot.TypeRef
	if plan.MergeApproval != nil {
		mergeInput = plan.MergeApproval.Input
		mergePublisher = plan.MergeApproval.Publisher
	}
	return enc(struct {
		Name                    string                 `json:"name"`
		Question                string                 `json:"question,omitempty"`
		MergeApprovalInput      string                 `json:"merge_approval_input,omitempty"`
		MergeApprovalPublisher  snapshot.TypeRef       `json:"merge_approval_publisher,omitempty"`
		MergeDestinationPresent bool                   `json:"merge_destination_configured,omitempty"`
		Type                    snapshot.TypeRef       `json:"type"`
		OnTimeout               AwaitSnapshotOnTimeout `json:"on_timeout"`
		HasDefault              bool                   `json:"has_default,omitempty"`
		WorkflowPort            string                 `json:"workflow_port,omitempty"`
	}{
		Name: plan.Name, Question: plan.Question,
		MergeApprovalInput: mergeInput, MergeApprovalPublisher: mergePublisher,
		MergeDestinationPresent: plan.MergeApproval != nil && plan.MergeApproval.Destination != "",
		Type:                    plan.Type, OnTimeout: plan.OnTimeout,
		HasDefault: plan.DefaultSnapshotID != "", WorkflowPort: plan.WorkflowPort,
	})
}

func (plan PublishSnapshotPlan) Public() *json.RawMessage {
	return enc(struct {
		Name                  string           `json:"name"`
		Publisher             snapshot.TypeRef `json:"publisher"`
		Input                 string           `json:"input"`
		InputType             snapshot.TypeRef `json:"input_type"`
		Mode                  publisher.Mode   `json:"mode"`
		ApprovalPolicyVersion string           `json:"approval_policy_version"`
		Approval              string           `json:"approval,omitempty"`
		DestinationConfigured bool             `json:"destination_configured"`
	}{
		Name: plan.Name, Publisher: plan.Publisher, Input: plan.Input, InputType: plan.InputType,
		Mode: plan.Mode, ApprovalPolicyVersion: plan.ApprovalPolicyVersion,
		Approval:              plan.Approval,
		DestinationConfigured: plan.Destination != "",
	})
}

func (plan TimeoutPlan) Public() *json.RawMessage {
	return enc(struct {
		Step     *json.RawMessage `json:"step"`
		Duration string           `json:"duration"`
	}{
		Step:     plan.Step.Public(),
		Duration: plan.Duration,
	})
}

func (plan TryPlan) Public() *json.RawMessage {
	return enc(struct {
		Step *json.RawMessage `json:"step"`
	}{
		Step: plan.Step.Public(),
	})
}

func (plan RetryPlan) Public() *json.RawMessage {
	public := make([]*json.RawMessage, len(plan))

	for i := 0; i < len(plan); i++ {
		public[i] = plan[i].Public()
	}

	return enc(public)
}

func (plan ArtifactInputPlan) Public() *json.RawMessage {
	return enc(plan)
}

func (plan ArtifactOutputPlan) Public() *json.RawMessage {
	return enc(plan)
}

func (plan SidecarPlan) Public() *json.RawMessage {
	return enc(struct {
		Name  string `json:"name"`
		Image string `json:"image,omitempty"`
	}{
		Name:  plan.Name,
		Image: plan.Image,
	})
}

func (plan VarScopedPlan) Public() *json.RawMessage {
	return enc(struct {
		Step   *json.RawMessage `json:"step"`
		Values []any            `json:"values"`
	}{
		Step:   plan.Step.Public(),
		Values: plan.Values,
	})
}

func enc(public any) *json.RawMessage {
	enc, _ := json.Marshal(public)
	return (*json.RawMessage)(&enc)
}
