package builds

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type Planner struct {
	planFactory atc.PlanFactory
}

func NewPlanner(planFactory atc.PlanFactory) Planner {
	return Planner{
		planFactory: planFactory,
	}
}

func (planner Planner) Create(
	planConfig atc.StepConfig,
	resources db.SchedulerResources,
	resourceTypes atc.ResourceTypes,
	inputs []db.BuildInput,
	manuallyTriggered bool,
) (atc.Plan, error) {
	visitor := &planVisitor{
		planFactory: planner.planFactory,

		resources:         resources,
		resourceTypes:     resourceTypes,
		inputs:            inputs,
		manuallyTriggered: manuallyTriggered,
	}

	err := planConfig.Visit(visitor)
	if err != nil {
		return atc.Plan{}, err
	}

	return visitor.plan, nil
}

type planVisitor struct {
	planFactory atc.PlanFactory

	resources         db.SchedulerResources
	resourceTypes     atc.ResourceTypes
	inputs            []db.BuildInput
	manuallyTriggered bool

	plan atc.Plan

	// runs maps each run_pipeline step name visited so far, in the order the
	// steps run, to the plan a later step's `inputs.from` resolves to. A name
	// called again shadows the earlier call, the way a later artifact of the
	// same name does.
	runs map[string]runPipelineRef
}

// runPipelineRef is what a later run_pipeline step may bind under a step
// name: the plan id of the call, or the reason it may not bind that call.
type runPipelineRef struct {
	id      atc.PlanID
	refusal string
}

// snapshotRuns copies the names visible so far, for a block whose steps must
// not see each other (in_parallel) or must not be seen as-is after it ends
// (retry).
func (visitor *planVisitor) snapshotRuns() map[string]runPipelineRef {
	return maps.Clone(visitor.runs)
}

// addedSince lists the names whose call changed since the snapshot.
func (visitor *planVisitor) addedSince(before map[string]runPipelineRef) []string {
	var added []string
	for name, ref := range visitor.runs {
		if prev, ok := before[name]; !ok || prev.id != ref.id {
			added = append(added, name)
		}
	}
	return added
}

// refuseAddedSince keeps every call made since the snapshot from being bound
// by a later step, for the given reason, unless it already may not be.
func (visitor *planVisitor) refuseAddedSince(before map[string]runPipelineRef, reason string) {
	for _, name := range visitor.addedSince(before) {
		ref := visitor.runs[name]
		if ref.refusal == "" {
			ref.refusal = reason
		}
		visitor.runs[name] = ref
	}
}

func (visitor *planVisitor) recordRun(name string, ref runPipelineRef) {
	if visitor.runs == nil {
		visitor.runs = map[string]runPipelineRef{}
	}
	visitor.runs[name] = ref
}

func (visitor *planVisitor) VisitTask(step *atc.TaskStep) error {
	visitor.plan = visitor.planFactory.NewPlan(atc.TaskPlan{
		Name:              step.Name,
		TaskID:            step.TaskID,
		RunResult:         step.RunResult,
		RunInputs:         step.RunInputs,
		Privileged:        step.Privileged,
		Hermetic:          step.Hermetic,
		Limits:            step.Limits,
		Requests:          step.Requests,
		Config:            step.Config,
		ConfigPath:        step.ConfigPath,
		Vars:              step.Vars,
		Tags:              step.Tags,
		Params:            step.Params,
		InputMapping:      step.InputMapping,
		OutputMapping:     step.OutputMapping,
		ImageArtifactName: step.ImageArtifactName,
		Timeout:           step.Timeout,
		Sidecars:          step.Sidecars,

		ResourceTypes:     visitor.resourceTypes,
		CheckSkipInterval: visitor.manuallyTriggered,
	})

	return nil
}

func (visitor *planVisitor) VisitGet(step *atc.GetStep) error {
	resourceName := step.Resource
	if resourceName == "" {
		resourceName = step.Name
	}

	resource, found := visitor.resources.Lookup(resourceName)
	if !found {
		return UnknownResourceError{resourceName}
	}

	var version atc.Version
	for _, input := range visitor.inputs {
		if input.Name == step.Name {
			version = atc.Version(input.Version)
			break
		}
	}

	if version == nil {
		return VersionNotProvidedError{step.Name}
	}

	resource.ApplySourceDefaults(visitor.resourceTypes)

	plan := visitor.planFactory.NewPlan(atc.GetPlan{
		Name: step.Name,

		Type:     resource.Type,
		Resource: resourceName,
		Source:   resource.Source,
		Params:   step.Params,
		Version:  &version,
		Tags:     step.Tags,
		Timeout:  step.Timeout,

		SkipDownload: step.SkipDownload,
	})

	plan.Get.TypeImage = visitor.resourceTypes.ImageForType(plan.ID, resource.Type, step.Tags, false)

	visitor.plan = plan
	return nil
}

func (visitor *planVisitor) VisitPut(step *atc.PutStep) error {
	logicalName := step.Name

	resourceName := step.Resource
	if resourceName == "" {
		resourceName = logicalName
	}

	resource, found := visitor.resources.Lookup(resourceName)
	if !found {
		return UnknownResourceError{resourceName}
	}

	resource.ApplySourceDefaults(visitor.resourceTypes)

	plan := visitor.planFactory.NewPlan(atc.PutPlan{
		Type:     resource.Type,
		Name:     logicalName,
		Resource: resourceName,
		Source:   resource.Source,
		Params:   step.Params,
		Tags:     step.Tags,
		Inputs:   step.Inputs,
		Timeout:  step.Timeout,

		ExposeBuildCreatedBy: resource.ExposeBuildCreatedBy,
	})

	plan.Put.TypeImage = visitor.resourceTypes.ImageForType(plan.ID, resource.Type, step.Tags, visitor.manuallyTriggered)

	if step.NoGet {
		visitor.plan = plan
		return nil
	}

	dependentGetPlan := visitor.planFactory.NewPlan(atc.GetPlan{
		Type:        resource.Type,
		Name:        logicalName,
		Resource:    resourceName,
		Source:      resource.Source,
		Params:      step.GetParams,
		VersionFrom: &plan.ID,

		Tags:    step.Tags,
		Timeout: step.Timeout,
	})

	dependentGetPlan.Get.TypeImage = visitor.resourceTypes.ImageForType(dependentGetPlan.ID, resource.Type, step.Tags, visitor.manuallyTriggered)

	visitor.plan = visitor.planFactory.NewPlan(atc.OnSuccessPlan{
		Step: plan,
		Next: dependentGetPlan,
	})

	return nil
}

func (visitor *planVisitor) VisitDo(step *atc.DoStep) error {
	do := atc.DoPlan{}

	for _, step := range step.Steps {
		err := step.Config.Visit(visitor)
		if err != nil {
			return err
		}

		do = append(do, visitor.plan)
	}

	visitor.plan = visitor.planFactory.NewPlan(do)

	return nil
}

func (visitor *planVisitor) VisitInParallel(step *atc.InParallelStep) error {
	var steps []atc.Plan

	// Each step sees only the calls made before the in_parallel; the steps
	// after it see every call made inside it, unless two steps made one under
	// the same name.
	before := visitor.snapshotRuns()
	added := map[string][]runPipelineRef{}

	for _, sub := range step.Config.Steps {
		visitor.runs = maps.Clone(before)

		err := sub.Config.Visit(visitor)
		if err != nil {
			return err
		}

		for _, name := range visitor.addedSince(before) {
			added[name] = append(added[name], visitor.runs[name])
		}

		steps = append(steps, visitor.plan)
	}

	visitor.runs = before
	for name, refs := range added {
		ref := refs[0]
		if len(refs) > 1 {
			ref.refusal = RunPipelineFromParallel
		}
		visitor.recordRun(name, ref)
	}

	visitor.plan = visitor.planFactory.NewPlan(atc.InParallelPlan{
		Steps:    steps,
		Limit:    step.Config.Limit,
		FailFast: step.Config.FailFast,
	})

	return nil
}

func (visitor *planVisitor) VisitAcross(step *atc.AcrossStep) error {
	vars := make([]atc.AcrossVar, len(step.Vars))
	for i, v := range step.Vars {
		vars[i] = atc.AcrossVar(v)
	}

	// Inside the across, a step binds an earlier call of the same iteration:
	// the engine maps the template's plan ids to the iteration's own when it
	// builds the substeps. After the across, a name called inside it is one
	// call per iteration, so it names no single run.
	before := visitor.snapshotRuns()

	err := step.Step.Visit(visitor)
	if err != nil {
		return err
	}

	visitor.refuseAddedSince(before, RunPipelineFromAcross)

	template, err := json.Marshal(visitor.plan)
	if err != nil {
		return err
	}

	acrossPlan := atc.AcrossPlan{
		Vars:            vars,
		SubStepTemplate: string(template),
		FailFast:        step.FailFast,
	}

	visitor.plan = visitor.planFactory.NewPlan(acrossPlan)

	return nil
}

func (visitor *planVisitor) VisitSetPipeline(step *atc.SetPipelineStep) error {
	visitor.plan = visitor.planFactory.NewPlan(atc.SetPipelinePlan{
		Name:         step.Name,
		File:         step.File,
		Team:         step.Team,
		Vars:         step.Vars,
		VarFiles:     step.VarFiles,
		InstanceVars: step.InstanceVars,
	})

	return nil
}

// VisitRunPipeline resolves each input's `from` to the plan id of an earlier
// waited run_pipeline step, the way a put's dependent get carries its put's
// plan id in version_from, and records this step for the steps after it.
func (visitor *planVisitor) VisitRunPipeline(step *atc.RunPipelineStep) error {
	var inputs map[string]atc.RunPipelineInputPlan
	if len(step.Inputs) > 0 {
		inputs = make(map[string]atc.RunPipelineInputPlan, len(step.Inputs))
	}

	for _, name := range slices.Sorted(maps.Keys(step.Inputs)) {
		input := step.Inputs[name]

		ref, found := visitor.runs[input.From]
		if !found {
			ref.refusal = RunPipelineFromUnknown
		}
		if ref.refusal != "" {
			return RunPipelineInputError{
				Step:   step.Name,
				Input:  name,
				From:   input.From,
				Reason: ref.refusal,
			}
		}

		inputs[name] = atc.RunPipelineInputPlan{
			From:   ref.id,
			Result: input.Result,
		}
	}

	visitor.plan = visitor.planFactory.NewPlan(atc.RunPipelinePlan{
		Name:   step.Name,
		Params: step.Params,
		Wait:   step.Wait,
		Inputs: inputs,
	})

	ref := runPipelineRef{id: visitor.plan.ID}
	if !step.Wait {
		ref.refusal = RunPipelineFromNotWaited
	}
	visitor.recordRun(step.Name, ref)

	return nil
}

func (visitor *planVisitor) VisitLoadVar(step *atc.LoadVarStep) error {
	visitor.plan = visitor.planFactory.NewPlan(atc.LoadVarPlan{
		Name:   step.Name,
		File:   step.File,
		Format: step.Format,
		Reveal: step.Reveal,
	})

	return nil
}

func (visitor *planVisitor) VisitTry(step *atc.TryStep) error {
	err := step.Step.Config.Visit(visitor)
	if err != nil {
		return err
	}

	visitor.plan = visitor.planFactory.NewPlan(atc.TryPlan{
		Step: visitor.plan,
	})

	return nil
}

func (visitor *planVisitor) VisitTimeout(step *atc.TimeoutStep) error {
	err := step.Step.Visit(visitor)
	if err != nil {
		return err
	}

	visitor.plan = visitor.planFactory.NewPlan(atc.TimeoutPlan{
		Duration: step.Duration,
		Step:     visitor.plan,
	})

	return nil
}

func (visitor *planVisitor) VisitRetry(step *atc.RetryStep) error {
	retryStep := make(atc.RetryPlan, step.Attempts)

	// Every attempt is planned with its own plan ids, so a step inside an
	// attempt binds the call made in that same attempt. After the retry, which
	// attempt's call is the one that ran to the end is not known yet.
	before := visitor.snapshotRuns()

	for i := 0; i < step.Attempts; i++ {
		visitor.runs = maps.Clone(before)

		err := step.Step.Visit(visitor)
		if err != nil {
			return err
		}

		retryStep[i] = visitor.plan
	}

	visitor.refuseAddedSince(before, RunPipelineFromRetry)

	visitor.plan = visitor.planFactory.NewPlan(retryStep)

	return nil
}

func (visitor *planVisitor) VisitOnSuccess(step *atc.OnSuccessStep) error {
	plan := atc.OnSuccessPlan{}

	err := step.Step.Visit(visitor)
	if err != nil {
		return err
	}

	plan.Step = visitor.plan

	err = step.Hook.Config.Visit(visitor)
	if err != nil {
		return err
	}

	plan.Next = visitor.plan

	visitor.plan = visitor.planFactory.NewPlan(plan)

	return nil
}

func (visitor *planVisitor) VisitOnFailure(step *atc.OnFailureStep) error {
	plan := atc.OnFailurePlan{}

	err := step.Step.Visit(visitor)
	if err != nil {
		return err
	}

	plan.Step = visitor.plan

	err = step.Hook.Config.Visit(visitor)
	if err != nil {
		return err
	}

	plan.Next = visitor.plan

	visitor.plan = visitor.planFactory.NewPlan(plan)

	return nil
}

func (visitor *planVisitor) VisitOnAbort(step *atc.OnAbortStep) error {
	plan := atc.OnAbortPlan{}

	err := step.Step.Visit(visitor)
	if err != nil {
		return err
	}

	plan.Step = visitor.plan

	err = step.Hook.Config.Visit(visitor)
	if err != nil {
		return err
	}

	plan.Next = visitor.plan

	visitor.plan = visitor.planFactory.NewPlan(plan)

	return nil
}

func (visitor *planVisitor) VisitOnError(step *atc.OnErrorStep) error {
	plan := atc.OnErrorPlan{}

	err := step.Step.Visit(visitor)
	if err != nil {
		return err
	}

	plan.Step = visitor.plan

	err = step.Hook.Config.Visit(visitor)
	if err != nil {
		return err
	}

	plan.Next = visitor.plan

	visitor.plan = visitor.planFactory.NewPlan(plan)

	return nil
}
func (visitor *planVisitor) VisitEnsure(step *atc.EnsureStep) error {
	plan := atc.EnsurePlan{}

	err := step.Step.Visit(visitor)
	if err != nil {
		return err
	}

	plan.Step = visitor.plan

	err = step.Hook.Config.Visit(visitor)
	if err != nil {
		return err
	}

	plan.Next = visitor.plan

	visitor.plan = visitor.planFactory.NewPlan(plan)

	return nil
}
