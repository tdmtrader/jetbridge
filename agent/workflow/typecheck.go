package workflow

import (
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
)

type snapshotPresence uint8

const (
	snapshotGuaranteed snapshotPresence = iota
	snapshotConditional
)

type snapshotProducer struct {
	functionID  string
	kind        string
	path        string
	outputName  string
	insideRetry bool
	task        *atc.TaskStep
	agent       *atc.AgentStep
	await       *atc.AwaitSnapshotStep
}

func (producer *snapshotProducer) key() string {
	return producer.kind + "\x00" + producer.path + "\x00" + producer.functionID + "\x00" + producer.outputName
}

type snapshotBinding struct {
	typ            snapshot.TypeRef
	presence       snapshotPresence
	typed          bool
	ambiguous      bool
	producer       *snapshotProducer
	writePath      string
	resourceSource string
	validation     *validationBindingProvenance
}

type validationBindingProvenance struct {
	candidate string
	bases     map[string]string
	profile   string
}

type snapshotEnvironment map[string]snapshotBinding

type snapshotFlow struct {
	env      snapshotEnvironment
	produced map[string]snapshotBinding
	// mayProduced contains writes possible while this wrapper reports success.
	// allProduced additionally includes writes on failure/error/abort paths so
	// an enclosing try can promote those outcomes without losing their effects.
	mayProduced map[string]snapshotBinding
	allProduced map[string]snapshotBinding
}

type publicOutputTarget struct {
	portName string
	producer *snapshotProducer
}

// nodeIdentityDeclaration records who first claimed a workflow-local node
// identity, so a later collision can name both what kind of binding is being
// declared and what kind got there first.
type nodeIdentityDeclaration struct {
	label    string
	identity string
}

type snapshotFlowChecker struct {
	nodeIdentities        map[string]nodeIdentityDeclaration
	prototypes            map[string]struct{}
	devValidationProfiles map[string]CompiledDevValidationProfile
	timeoutDepth          int
	retryDepth            int
}

// TypeCheckFunction proves the typed snapshot flow of a compiled version-3
// function without adding persistence metadata. The latter depends on the
// durable definition ID, which does not exist during source compilation.
func TypeCheckFunction(function *FunctionConfig) error {
	_, err := analyzeFunctionFlow(function)
	return err
}

// AnnotatePublicOutputs atomically marks the concrete typed producer selected
// by each public output. Callers invoke this only after durable definition
// allocation; CompileDefinition deliberately remains identity-independent.
func AnnotatePublicOutputs(function *FunctionConfig, workflowDefinitionID int) error {
	if workflowDefinitionID <= 0 {
		return fmt.Errorf("workflow: workflow definition ID must be positive")
	}
	targets, err := analyzeFunctionFlow(function)
	if err != nil {
		return err
	}
	if _, err := validateOrdinaryFunction(function); err != nil {
		return err
	}

	type update struct {
		producer *snapshotProducer
		config   atc.SnapshotOutputConfig
	}
	updates := make([]update, 0, len(targets))
	for _, target := range targets {
		config, found := target.producer.outputConfig()
		if !found {
			return fmt.Errorf("workflow: internal error: %s %q at %s no longer declares output %q", target.producer.kind, target.producer.functionID, target.producer.path, target.producer.outputName)
		}
		config.Retention = snapshot.RetentionClassWorkflow
		config.WorkflowPort = target.portName
		config.WorkflowDefinitionID = workflowDefinitionID
		config.WorkflowRunID = "((workflow_run_id))"
		if err := config.Validate(); err != nil {
			return fmt.Errorf("workflow: output %q: annotate producer: %w", target.portName, err)
		}
		updates = append(updates, update{producer: target.producer, config: config})
	}

	for _, update := range updates {
		update.producer.setOutputConfig(update.config)
	}
	return nil
}

func (producer *snapshotProducer) outputConfig() (atc.SnapshotOutputConfig, bool) {
	switch {
	case producer.task != nil:
		config, found := producer.task.SnapshotOutputs[producer.outputName]
		return config, found
	case producer.agent != nil:
		name := producer.outputName
		for logical, mapped := range producer.agent.OutputMapping {
			if mapped == name {
				name = logical
				break
			}
		}
		config, found := producer.agent.SnapshotOutputs[name]
		return config, found
	case producer.await != nil && producer.outputName == producer.await.Name:
		config := atc.SnapshotOutputConfig{Type: producer.await.Type}
		if producer.await.WorkflowPort != "" {
			config.Retention = snapshot.RetentionClassWorkflow
			config.WorkflowPort = producer.await.WorkflowPort
			config.WorkflowDefinitionID = producer.await.WorkflowDefinitionID
			config.WorkflowRunID = producer.await.WorkflowRunID
		}
		return config, true
	default:
		return atc.SnapshotOutputConfig{}, false
	}
}

func (producer *snapshotProducer) setOutputConfig(config atc.SnapshotOutputConfig) {
	if producer.task != nil {
		producer.task.SnapshotOutputs[producer.outputName] = config
		return
	}
	if producer.agent != nil {
		name := producer.outputName
		for logical, mapped := range producer.agent.OutputMapping {
			if mapped == name {
				name = logical
				break
			}
		}
		producer.agent.SnapshotOutputs[name] = config
		return
	}
	producer.await.WorkflowPort = config.WorkflowPort
	producer.await.WorkflowDefinitionID = config.WorkflowDefinitionID
	producer.await.WorkflowRunID = config.WorkflowRunID
}

// FunctionATCConfig assembles the exact ordinary one-job template config used
// for version-3 validation and, later, rendering.
func FunctionATCConfig(function *FunctionConfig) atc.Config {
	plan := function.Plan
	if len(function.ResourceSources) > 0 {
		plan = make([]atc.Step, 0, len(function.ResourceSources)+len(function.Plan))
		for _, source := range function.ResourceSources {
			plan = append(plan, atc.Step{Config: &atc.GetStep{Name: source.Name, Resource: source.Resource, Version: source.Version, Trigger: source.Trigger}})
		}
		plan = append(plan, function.Plan...)
	}
	return atc.Config{
		Template:      true,
		Resources:     function.Resources,
		ResourceTypes: function.ResourceTypes,
		Prototypes:    function.Prototypes,
		VarSources:    function.VarSources,
		Jobs: atc.JobConfigs{{
			Name:         "entry",
			PlanSequence: plan,
		}},
	}
}

// ValidateFunction runs function-specific flow validation followed by the
// authoritative ordinary Concourse validator. Warnings remain warnings and
// are returned byte-for-byte in the validator's normal order.
func ValidateFunction(function *FunctionConfig) ([]atc.ConfigWarning, error) {
	if err := TypeCheckFunction(function); err != nil {
		return nil, err
	}
	return validateOrdinaryFunction(function)
}

func validateOrdinaryFunction(function *FunctionConfig) ([]atc.ConfigWarning, error) {
	warnings, errorMessages := configvalidate.Validate(FunctionATCConfig(function))
	if len(errorMessages) > 0 {
		return warnings, fmt.Errorf("workflow: invalid Concourse config:\n%s", strings.Join(errorMessages, "\n"))
	}
	return warnings, nil
}

func analyzeFunctionFlow(function *FunctionConfig) ([]publicOutputTarget, error) {
	if function == nil {
		return nil, fmt.Errorf("workflow: function is required")
	}
	if err := function.Validate(); err != nil {
		return nil, err
	}
	if len(function.Resources) > 0 && len(function.ResourceSources) == 0 {
		return nil, fmt.Errorf("workflow: resources are live-read boundaries and are not permitted in schema-version-3 workflow functions; capture resource versions into snapshots before invocation")
	}
	if len(function.VarSources) > 0 {
		return nil, fmt.Errorf("workflow: var_sources are live-read boundaries and are not permitted in schema-version-3 workflow functions; capture their values into snapshots before invocation")
	}

	env := make(snapshotEnvironment, len(function.Inputs)+len(function.ResourceSources))
	for index, input := range function.Inputs {
		presence := snapshotGuaranteed
		if input.Optional {
			presence = snapshotConditional
		}
		env[input.Name] = snapshotBinding{
			typ:       input.Type,
			presence:  presence,
			typed:     true,
			writePath: fmt.Sprintf("inputs[%d]", index),
		}
	}
	for index, source := range function.ResourceSources {
		env[source.Name] = snapshotBinding{typ: source.Type, presence: snapshotGuaranteed, typed: true, writePath: fmt.Sprintf("resource_sources[%d]", index), resourceSource: source.Name}
	}

	prototypes := make(map[string]struct{}, len(function.Prototypes))
	for _, prototype := range function.Prototypes {
		prototypes[prototype.Name] = struct{}{}
	}
	profiles := make(map[string]CompiledDevValidationProfile, len(function.DevValidationProfiles))
	for _, profile := range function.DevValidationProfiles {
		profiles[profile.Name] = profile
	}
	checker := &snapshotFlowChecker{nodeIdentities: make(map[string]nodeIdentityDeclaration), prototypes: prototypes, devValidationProfiles: profiles}
	result, err := checker.checkSequence(function.Plan, env, "plan")
	if err != nil {
		return nil, err
	}

	targets := make([]publicOutputTarget, 0, len(function.Outputs))
	selected := make(map[string]string, len(function.Outputs))
	for index, output := range function.Outputs {
		binding, found := result.env[output.From]
		if !found {
			return nil, fmt.Errorf("workflow: outputs[%d] %q: source %q is unavailable (use before produce)", index, output.Name, output.From)
		}
		if !binding.typed {
			if binding.ambiguous {
				return nil, fmt.Errorf("workflow: outputs[%d] %q: source %q has ambiguous possible writes at %s", index, output.Name, output.From, binding.writePath)
			}
			return nil, fmt.Errorf("workflow: outputs[%d] %q: source %q is occupied by an untyped producer at %s", index, output.Name, output.From, binding.writePath)
		}
		if binding.typ != output.Type {
			return nil, fmt.Errorf("workflow: outputs[%d] %q: source %q type mismatch: have %s, require %s", index, output.Name, output.From, binding.typ, output.Type)
		}
		if !output.Optional && binding.presence == snapshotConditional {
			return nil, fmt.Errorf("workflow: outputs[%d] %q: required public output cannot use conditional source %q", index, output.Name, output.From)
		}
		if binding.producer == nil {
			return nil, fmt.Errorf("workflow: outputs[%d] %q: source %q has no concrete typed producer; public input pass-through is unsupported", index, output.Name, output.From)
		}
		if binding.producer.insideRetry {
			return nil, fmt.Errorf("workflow: outputs[%d] %q: public output cannot be produced inside retry until attempt-aware snapshot promotion is implemented", index, output.Name)
		}
		producerKey := binding.producer.key()
		if previous, found := selected[producerKey]; found {
			return nil, fmt.Errorf("workflow: outputs[%d] %q and public output %q select the same internal output %q from %s", index, output.Name, previous, output.From, binding.producer.path)
		}
		selected[producerKey] = output.Name
		targets = append(targets, publicOutputTarget{portName: output.Name, producer: binding.producer})
	}
	return targets, nil
}

func (checker *snapshotFlowChecker) checkSequence(steps []atc.Step, entry snapshotEnvironment, path string) (snapshotFlow, error) {
	env := cloneSnapshotEnvironment(entry)
	produced := make(map[string]snapshotBinding)
	mayProduced := make(map[string]snapshotBinding)
	allProduced := make(map[string]snapshotBinding)
	for index := range steps {
		stepPath := fmt.Sprintf("%s[%d]", path, index)
		child, err := checker.checkStep(steps[index], env, stepPath)
		if err != nil {
			return snapshotFlow{}, err
		}
		env = child.env
		mergeProduced(produced, child.produced)
		mergeMayProduced(mayProduced, child.mayProduced)
		mergeMayProduced(allProduced, child.allProduced)
	}
	return snapshotFlow{env: env, produced: produced, mayProduced: mayProduced, allProduced: allProduced}, nil
}

func (checker *snapshotFlowChecker) checkStep(step atc.Step, entry snapshotEnvironment, path string) (snapshotFlow, error) {
	if step.Config == nil {
		return snapshotFlow{}, fmt.Errorf("workflow: %s: step config is required", path)
	}

	switch config := step.Config.(type) {
	case *atc.TaskStep:
		return checker.checkTask(config, entry, path)
	case *atc.AgentStep:
		return checker.checkAgent(config, entry, path)
	case *atc.GetStep:
		return snapshotFlow{}, fmt.Errorf("workflow: %s.get(%q): get is a live-read boundary and is not permitted in schema-version-3 workflow functions; capture the resource version into a snapshot before invocation", path, config.Name)
	case *atc.LoadSnapshotStep:
		return checker.checkLoadSnapshot(config, entry, path)
	case *atc.AwaitSnapshotStep:
		return checker.checkAwaitSnapshot(config, entry, path)
	case *atc.PublishSnapshotStep:
		return checker.checkPublishSnapshot(config, entry, path)
	case *atc.RunStep:
		if _, found := checker.prototypes[config.Type]; !found {
			// Preserve the ordinary Concourse validator's authoritative unknown
			// prototype diagnostic, which runs immediately after snapshot flow.
			return emptySnapshotFlow(entry), nil
		}
		// Concourse currently plans prototype runs without retaining their
		// resolved source/image identity, and the runtime RunStep itself is only
		// a placeholder. Reject the advertised syntax at the immutable function
		// boundary instead of allocating a durable run that must planning-error.
		return snapshotFlow{}, fmt.Errorf("workflow: %s: prototype run steps are not executable in workflow functions", path)
	case *atc.PutStep:
		return snapshotFlow{}, fmt.Errorf("workflow: %s.put(%q): put is not permitted in schema-version-3 workflow functions; use publish_snapshot for governed outbound effects", path, config.Name)
	case *atc.SetPipelineStep:
		return snapshotFlow{}, fmt.Errorf("workflow: %s.set_pipeline(%q): set_pipeline is not permitted in schema-version-3 workflow functions; use publish_snapshot for governed outbound effects", path, config.Name)
	case *atc.LoadVarStep:
		return snapshotFlow{}, fmt.Errorf("workflow: %s.load_var(%q): load_var is an untyped snapshot read and is not permitted in schema-version-3 workflow functions; model the value as a typed snapshot input", path, config.Name)
	case *atc.DoStep:
		return checker.checkSequence(config.Steps, entry, path+".do.steps")
	case *atc.InParallelStep:
		return checker.checkParallel(config, entry, path)
	case *atc.TryStep:
		child, err := checker.checkStep(config.Step, cloneSnapshotEnvironment(entry), path+".try")
		if err != nil {
			return snapshotFlow{}, err
		}
		return conditionalTryFlow(entry, child), nil
	case *atc.RetryStep:
		checker.retryDepth++
		child, err := checker.checkWrapped(config.Step, entry, path+".attempts")
		checker.retryDepth--
		if err != nil {
			return snapshotFlow{}, err
		}
		// Snapshot attempt scopes discard every failed attempt. Only a child's
		// successful writes can become externally observable through retry.
		child.allProduced = cloneProduced(child.mayProduced)
		return child, nil
	case *atc.TimeoutStep:
		checker.timeoutDepth++
		flow, err := checker.checkWrapped(config.Step, entry, path+".timeout")
		checker.timeoutDepth--
		return flow, err
	case *atc.OnSuccessStep:
		main, err := checker.checkWrapped(config.Step, entry, path)
		if err != nil {
			return snapshotFlow{}, err
		}
		hook, err := checker.checkStep(config.Hook, main.env, path+".on_success")
		if err != nil {
			return snapshotFlow{}, err
		}
		mergeProduced(main.produced, hook.produced)
		mergeMayProduced(main.mayProduced, hook.mayProduced)
		mergeMayProduced(main.allProduced, hook.allProduced)
		return snapshotFlow{env: hook.env, produced: main.produced, mayProduced: main.mayProduced, allProduced: main.allProduced}, nil
	case *atc.OnFailureStep:
		return checker.checkNonSuccessHook(config.Step, config.Hook, entry, path, "on_failure")
	case *atc.OnErrorStep:
		return checker.checkNonSuccessHook(config.Step, config.Hook, entry, path, "on_error")
	case *atc.OnAbortStep:
		return checker.checkNonSuccessHook(config.Step, config.Hook, entry, path, "on_abort")
	case *atc.EnsureStep:
		return checker.checkEnsure(config, entry, path)
	case *atc.AcrossStep:
		// Across expands an uninterpolated template into new runtime plans and
		// plan IDs. Workflow runs currently freeze provenance before execution,
		// so accepting this step would let the durable envelope describe a plan
		// other than the iterations that actually ran. Ordinary Concourse keeps
		// supporting across; the immutable workflow boundary rejects it until
		// expanded plans can be persisted and verified exactly.
		return snapshotFlow{}, fmt.Errorf("workflow: %s: across steps cannot provide exact execution provenance in workflow functions", path)
	default:
		return snapshotFlow{}, fmt.Errorf("workflow: %s: unsupported step config %T; snapshot-flow semantics must be defined explicitly", path, config)
	}
}

func (checker *snapshotFlowChecker) checkPublishSnapshot(step *atc.PublishSnapshotStep, entry snapshotEnvironment, path string) (snapshotFlow, error) {
	identity := fmt.Sprintf("%s.publish_snapshot(%q)", path, step.Name)
	if err := checker.registerNodeIdentity("binding name", step.Name, identity); err != nil {
		return snapshotFlow{}, err
	}
	if err := step.InputType.Validate(); err != nil {
		return snapshotFlow{}, fmt.Errorf("workflow: %s: input_type: %w", identity, err)
	}
	binding, found := entry[step.Input]
	if !found {
		return snapshotFlow{}, fmt.Errorf("workflow: %s: input %q is unavailable (use before produce)", identity, step.Input)
	}
	if !binding.typed {
		if binding.ambiguous {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: input %q has ambiguous possible writes at %s", identity, step.Input, binding.writePath)
		}
		return snapshotFlow{}, fmt.Errorf("workflow: %s: input %q is occupied by an untyped producer at %s", identity, step.Input, binding.writePath)
	}
	if binding.typ != step.InputType {
		return snapshotFlow{}, fmt.Errorf("workflow: %s: input %q type mismatch: have %s, require %s", identity, step.Input, binding.typ, step.InputType)
	}
	if binding.presence == snapshotConditional {
		return snapshotFlow{}, fmt.Errorf("workflow: %s: input %q cannot use a conditional binding", identity, step.Input)
	}
	if step.InputType == snapshot.TypeRef("repository-change/v1") {
		if err := checker.requireValidation(entry, step.Validation, step.Input, nil, nil, identity); err != nil {
			return snapshotFlow{}, err
		}
	} else if step.Validation != "" || step.PublishValidation != nil {
		return snapshotFlow{}, fmt.Errorf("workflow: %s: validation is only valid for repository-change/v1 publication", identity)
	}
	if step.Mode == publisher.ModeMerge {
		// expected_base_sha is server-derived at execution time, exactly like
		// workflow_run_id is renderer-owned. A workflow cannot know the target
		// tip it will land on when it is authored.
		if err := publisher.RejectAuthoredMergeBase(step.Parameters); err != nil {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: %w", identity, err)
		}
		approval, found := entry[step.Approval]
		if !found {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: approval %q is unavailable (use before produce)", identity, step.Approval)
		}
		if !approval.typed || approval.typ != snapshot.TypeRef("human-answer/v1") {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: approval %q must have exact type human-answer/v1", identity, step.Approval)
		}
		if approval.presence == snapshotConditional {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: approval %q cannot use a conditional binding", identity, step.Approval)
		}
		if approval.producer == nil || approval.producer.await == nil || approval.producer.await.MergeApproval == nil {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: approval %q must be produced by a server-bound merge_approval await_snapshot in this workflow", identity, step.Approval)
		}
		intent := approval.producer.await.MergeApproval
		if intent.Input != step.Input || intent.Publisher != step.Publisher ||
			intent.Destination != step.Destination || !maps.Equal(intent.Parameters, step.Parameters) ||
			intent.ApprovalPolicyVersion != step.ApprovalPolicyVersion {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: approval %q does not bind this exact merge publication", identity, step.Approval)
		}
	}
	return emptySnapshotFlow(entry), nil
}

func (checker *snapshotFlowChecker) checkLoadSnapshot(step *atc.LoadSnapshotStep, entry snapshotEnvironment, path string) (snapshotFlow, error) {
	identity := fmt.Sprintf("%s.load_snapshot(%q)", path, step.Name)
	if err := checker.registerNodeIdentity("binding name", step.Name, identity); err != nil {
		return snapshotFlow{}, err
	}
	if err := rejectResourceSourceShadow(entry, step.Name, identity); err != nil {
		return snapshotFlow{}, err
	}
	if step.Optional {
		if _, found := entry[step.Name]; found {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: optional load shadows an existing artifact; its value would be path-dependent", identity)
		}
	}

	presence := snapshotGuaranteed
	if step.Optional {
		presence = snapshotConditional
	}
	binding := snapshotBinding{
		typ:       step.Type,
		presence:  presence,
		typed:     true,
		writePath: identity,
	}
	env := cloneSnapshotEnvironment(entry)
	env[step.Name] = binding
	writes := map[string]snapshotBinding{step.Name: binding}
	return snapshotFlow{
		env:         env,
		produced:    writes,
		mayProduced: cloneProduced(writes),
		allProduced: cloneProduced(writes),
	}, nil
}

func (checker *snapshotFlowChecker) checkAwaitSnapshot(step *atc.AwaitSnapshotStep, entry snapshotEnvironment, path string) (snapshotFlow, error) {
	identity := fmt.Sprintf("%s.await_snapshot(%q)", path, step.Name)
	if err := checker.registerNodeIdentity("binding name", step.Name, identity); err != nil {
		return snapshotFlow{}, err
	}
	if err := rejectResourceSourceShadow(entry, step.Name, identity); err != nil {
		return snapshotFlow{}, err
	}
	if checker.timeoutDepth == 0 {
		return snapshotFlow{}, fmt.Errorf("workflow: %s: an ordinary timeout wrapper is required", identity)
	}
	if step.MergeApproval != nil {
		if err := publisher.RejectAuthoredMergeBase(step.MergeApproval.Parameters); err != nil {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: %w", identity, err)
		}
		subject, found := entry[step.MergeApproval.Input]
		if !found {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: merge approval input %q is unavailable (use before produce)", identity, step.MergeApproval.Input)
		}
		if !subject.typed || subject.typ != snapshot.TypeRef("repository-change/v1") {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: merge approval input %q must have exact type repository-change/v1", identity, step.MergeApproval.Input)
		}
		if subject.presence == snapshotConditional {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: merge approval input %q cannot use a conditional binding", identity, step.MergeApproval.Input)
		}
		if err := checker.requireValidation(entry, step.Validation, step.MergeApproval.Input, nil, nil, identity); err != nil {
			return snapshotFlow{}, err
		}
	} else {
		if step.Validation != "" || step.MergeApprovalValidation != nil {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: validation is only valid for merge approval", identity)
		}
		question, found := entry[step.Question]
		if !found {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: question %q is unavailable (use before produce)", identity, step.Question)
		}
		if !question.typed || question.typ != snapshot.TypeRef("question/v1") {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: question %q must have exact type question/v1", identity, step.Question)
		}
		if question.presence == snapshotConditional {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: question %q cannot use a conditional binding", identity, step.Question)
		}
	}
	if step.Type != snapshot.TypeRef("human-answer/v1") {
		return snapshotFlow{}, fmt.Errorf("workflow: %s: output must have exact type human-answer/v1", identity)
	}
	producer := &snapshotProducer{
		kind: "await_snapshot", path: identity, outputName: step.Name, insideRetry: checker.retryDepth > 0, await: step,
	}
	binding := snapshotBinding{
		typ: step.Type, presence: snapshotGuaranteed, typed: true, producer: producer, writePath: identity,
	}
	env := cloneSnapshotEnvironment(entry)
	env[step.Name] = binding
	writes := map[string]snapshotBinding{step.Name: binding}
	return snapshotFlow{
		env: env, produced: writes, mayProduced: cloneProduced(writes), allProduced: cloneProduced(writes),
	}, nil
}

func (checker *snapshotFlowChecker) checkWrapped(step atc.StepConfig, entry snapshotEnvironment, path string) (snapshotFlow, error) {
	if step == nil {
		return snapshotFlow{}, fmt.Errorf("workflow: %s: wrapped step config is required", path)
	}
	return checker.checkStep(atc.Step{Config: step}, entry, path)
}

func (checker *snapshotFlowChecker) checkNonSuccessHook(
	mainStep atc.StepConfig,
	hookStep atc.Step,
	entry snapshotEnvironment,
	path string,
	hookName string,
) (snapshotFlow, error) {
	main, err := checker.checkWrapped(mainStep, entry, path)
	if err != nil {
		return snapshotFlow{}, err
	}
	hook, err := checker.checkStep(hookStep, cloneSnapshotEnvironment(entry), path+"."+hookName)
	if err != nil {
		return snapshotFlow{}, err
	}
	// Hook outputs do not enter the wrapper's successful environment: failure,
	// error, and abort remain non-success results. They are still writes on a
	// non-success outcome, however, so an enclosing try must not lose them.
	mergeMayProduced(main.allProduced, hook.allProduced)
	return main, nil
}

func (checker *snapshotFlowChecker) checkEnsure(config *atc.EnsureStep, entry snapshotEnvironment, path string) (snapshotFlow, error) {
	main, err := checker.checkWrapped(config.Step, entry, path)
	if err != nil {
		return snapshotFlow{}, err
	}

	hookEntry := conservativeEnsureEnvironment(entry, main)
	hook, err := checker.checkStep(config.Hook, hookEntry, path+".ensure")
	if err != nil {
		return snapshotFlow{}, err
	}

	// On the wrapper's successful exit the main step necessarily succeeded, so
	// its exact environment is authoritative. Apply only writes made by the
	// successfully checked ensure hook; the conservative entry above exists to
	// keep the hook runnable on the main step's failure path too.
	outgoing := applySuccessfulFlow(main.env, hook)
	produced := cloneProduced(main.produced)
	mergeProduced(produced, hook.produced)
	mayProduced := cloneProduced(main.mayProduced)
	mergeMayProduced(mayProduced, hook.mayProduced)
	allProduced := cloneProduced(main.allProduced)
	mergeMayProduced(allProduced, hook.allProduced)
	return snapshotFlow{env: outgoing, produced: produced, mayProduced: mayProduced, allProduced: allProduced}, nil
}

func conservativeEnsureEnvironment(entry snapshotEnvironment, main snapshotFlow) snapshotEnvironment {
	env := cloneSnapshotEnvironment(entry)
	for _, name := range sortedSnapshotBindingKeys(main.allProduced) {
		write := main.allProduced[name]
		previous, existed := entry[name]
		if !existed {
			write.presence = snapshotConditional
			env[name] = write
			continue
		}
		env[name] = mergeBindingAlternatives(previous, write)
	}
	return env
}

func (checker *snapshotFlowChecker) checkParallel(config *atc.InParallelStep, entry snapshotEnvironment, path string) (snapshotFlow, error) {
	branches := make([]snapshotFlow, len(config.Config.Steps))
	untypedReads := make([]map[string]string, len(config.Config.Steps))
	producerBranch := make(map[string]int)
	for index := range config.Config.Steps {
		branchPath := fmt.Sprintf("%s.in_parallel.steps[%d]", path, index)
		untypedReads[index] = collectUntypedArtifactReads(config.Config.Steps[index], branchPath)
		branch, err := checker.checkStep(config.Config.Steps[index], cloneSnapshotEnvironment(entry), branchPath)
		if err != nil {
			return snapshotFlow{}, err
		}
		branches[index] = branch
		for _, name := range sortedSnapshotBindingKeys(branch.mayProduced) {
			if previous, found := producerBranch[name]; found {
				return snapshotFlow{}, fmt.Errorf("workflow: %s: parallel branches both produce %q (branches %d and %d)", path, name, previous, index)
			}
			producerBranch[name] = index
		}
	}
	for readerIndex := range branches {
		for _, name := range sortedStringMapKeys(untypedReads[readerIndex]) {
			for writerIndex := range branches {
				if writerIndex == readerIndex {
					continue
				}
				write, found := branches[writerIndex].allProduced[name]
				if !found || !write.typed {
					continue
				}
				return snapshotFlow{}, fmt.Errorf(
					"workflow: %s: parallel branch %d has an untyped read of %q at %s while branch %d may produce that typed snapshot",
					path, readerIndex, name, untypedReads[readerIndex][name], writerIndex,
				)
			}
		}
	}

	env := cloneSnapshotEnvironment(entry)
	produced := make(map[string]snapshotBinding)
	mayProduced := make(map[string]snapshotBinding)
	allProduced := make(map[string]snapshotBinding)
	for _, name := range sortedIntMapKeys(producerBranch) {
		branch := producerBranch[name]
		if binding, found := branches[branch].env[name]; found {
			env[name] = binding
		}
		if binding, found := branches[branch].produced[name]; found {
			produced[name] = binding
		}
		mayProduced[name] = branches[branch].mayProduced[name]
	}
	for index := range branches {
		mergeMayProduced(allProduced, branches[index].allProduced)
	}
	return snapshotFlow{env: env, produced: produced, mayProduced: mayProduced, allProduced: allProduced}, nil
}

func collectUntypedArtifactReads(step atc.Step, path string) map[string]string {
	reads := make(map[string]string)
	var collect func(atc.Step, string)
	collect = func(current atc.Step, currentPath string) {
		if current.Config == nil {
			return
		}
		record := func(name, readPath string) {
			if strings.TrimSpace(name) == "" {
				return
			}
			if _, found := reads[name]; !found {
				reads[name] = readPath
			}
		}
		recordPathSource := func(value, readPath string) {
			parts := strings.SplitN(value, "/", 2)
			if len(parts) == 2 {
				record(parts[0], readPath)
			}
		}

		switch config := current.Config.(type) {
		case *atc.TaskStep:
			inputs, _ := effectiveTaskArtifactNames(config)
			for _, name := range inputs {
				if _, typed := config.SnapshotInputs[name]; !typed {
					record(name, currentPath+".task("+config.Name+").inputs")
				}
			}
			record(config.ImageArtifactName, currentPath+".task("+config.Name+").image")
			recordPathSource(config.ConfigPath, currentPath+".task("+config.Name+").file")
		case *atc.AgentStep:
			inputs, _ := effectiveAgentArtifactNames(config)
			inputTypes := MappedSnapshotInputs(config.SnapshotInputs, config.InputMapping)
			for _, name := range inputs {
				if _, typed := inputTypes[name]; !typed {
					record(name, currentPath+".agent("+config.Name+").inputs")
				}
			}
		case *atc.LoadVarStep:
			recordPathSource(config.File, currentPath+".load_var("+config.Name+")")
		case *atc.DoStep:
			for index := range config.Steps {
				collect(config.Steps[index], fmt.Sprintf("%s.do.steps[%d]", currentPath, index))
			}
		case *atc.InParallelStep:
			for index := range config.Config.Steps {
				collect(config.Config.Steps[index], fmt.Sprintf("%s.in_parallel.steps[%d]", currentPath, index))
			}
		case *atc.TryStep:
			collect(config.Step, currentPath+".try")
		case *atc.RetryStep:
			collect(atc.Step{Config: config.Step}, currentPath+".attempts")
		case *atc.TimeoutStep:
			collect(atc.Step{Config: config.Step}, currentPath+".timeout")
		case *atc.OnSuccessStep:
			collect(atc.Step{Config: config.Step}, currentPath)
			collect(config.Hook, currentPath+".on_success")
		case *atc.OnFailureStep:
			collect(atc.Step{Config: config.Step}, currentPath)
			collect(config.Hook, currentPath+".on_failure")
		case *atc.OnErrorStep:
			collect(atc.Step{Config: config.Step}, currentPath)
			collect(config.Hook, currentPath+".on_error")
		case *atc.OnAbortStep:
			collect(atc.Step{Config: config.Step}, currentPath)
			collect(config.Hook, currentPath+".on_abort")
		case *atc.EnsureStep:
			collect(atc.Step{Config: config.Step}, currentPath)
			collect(config.Hook, currentPath+".ensure")
		case *atc.AcrossStep:
			collect(atc.Step{Config: config.Step}, currentPath+".across")
		}
	}
	collect(step, path)
	return reads
}

func (checker *snapshotFlowChecker) checkTask(step *atc.TaskStep, entry snapshotEnvironment, path string) (snapshotFlow, error) {
	if step.Config == nil && len(step.SnapshotInputs) == 0 && len(step.SnapshotOutputs) == 0 {
		for _, binding := range entry {
			if binding.resourceSource != "" {
				return snapshotFlow{}, fmt.Errorf("workflow: %s.task(%q): cannot prove that a file-backed task preserves resource sources without explicit input_types or output_types", path, step.Name)
			}
		}
	}
	inputs, outputs := effectiveTaskArtifactNames(step)
	// A file-backed task's legacy input/output membership is unknowable until
	// runtime. Its explicit input_types/output_types are still checked here;
	// unknown extra legacy artifacts remain ordinary Concourse dependencies.
	// Consequently an undeclared shadow by such a file is the one documented
	// static limitation, rather than a reason to ban file tasks from v3.
	return checker.checkLeaf("task", step.Name, step.FunctionID, inputs, step.SnapshotInputs, outputs, step.SnapshotOutputs, step, nil, entry, path)
}

func effectiveTaskArtifactNames(step *atc.TaskStep) ([]string, []string) {
	inputs := []string(nil)
	outputs := []string(nil)
	if step == nil || step.Config == nil {
		return inputs, outputs
	}
	inputs = make([]string, 0, len(step.Config.Inputs))
	for _, input := range step.Config.Inputs {
		name := input.Name
		if mapped, found := step.InputMapping[name]; found {
			name = mapped
		}
		inputs = append(inputs, name)
	}
	outputs = make([]string, 0, len(step.Config.Outputs))
	for _, output := range step.Config.Outputs {
		name := output.Name
		if mapped, found := step.OutputMapping[name]; found {
			name = mapped
		}
		outputs = append(outputs, name)
	}
	return inputs, outputs
}

func (checker *snapshotFlowChecker) checkAgent(step *atc.AgentStep, entry snapshotEnvironment, path string) (snapshotFlow, error) {
	inputs, outputs := effectiveAgentArtifactNames(step)
	flow, err := checker.checkLeaf("agent", step.Name, step.FunctionID, inputs, MappedSnapshotInputs(step.SnapshotInputs, step.InputMapping), outputs, MappedSnapshotOutputs(step.SnapshotOutputs, step.OutputMapping), nil, step, entry, path)
	if err != nil {
		return snapshotFlow{}, err
	}
	identity := fmt.Sprintf("%s.agent(%q)", path, step.Name)
	if candidate, governed, err := humanReviewCandidate(step); err != nil {
		return snapshotFlow{}, fmt.Errorf("workflow: %s: %w", identity, err)
	} else if governed {
		if err := checker.requireValidation(
			entry,
			step.Validation,
			candidate,
			MappedSnapshotInputs(step.SnapshotInputs, step.InputMapping),
			step.InputMapping,
			identity,
		); err != nil {
			return snapshotFlow{}, err
		}
	} else if step.Validation != "" || step.ReviewValidation != nil {
		return snapshotFlow{}, fmt.Errorf("workflow: %s: validation is only valid for a human review", identity)
	}
	return flow, nil
}

func effectiveAgentArtifactNames(step *atc.AgentStep) ([]string, []string) {
	if step == nil {
		return nil, nil
	}
	inputs := make([]string, len(step.Inputs))
	for index, name := range step.Inputs {
		if mapped, found := step.InputMapping[name]; found {
			name = mapped
		}
		inputs[index] = name
	}
	outputs := make([]string, len(step.Outputs))
	for index, name := range step.Outputs {
		if mapped, found := step.OutputMapping[name]; found {
			name = mapped
		}
		outputs[index] = name
	}
	return inputs, outputs
}

// MappedSnapshotInputs translates each declared input's logical name through
// mapping (an agent step's InputMapping) to the binding name actually
// consumed from the snapshot environment. Agents intentionally retain
// logical declaration keys in SnapshotInputs/SnapshotOutputs because their
// executor translates at the repository/sealer boundary (see
// applyNodeInvocation in node_reference.go); task steps do not need this
// translation because native task planning already consumes typed
// declarations by the physical artifact name after mapping. Exported so
// other packages deriving flow from a compiled function (e.g.
// agent/workflow/graph) apply the identical translation instead of
// maintaining a second copy that could silently diverge.
func MappedSnapshotInputs(source map[string]atc.SnapshotInputConfig, mapping map[string]string) map[string]atc.SnapshotInputConfig {
	if source == nil {
		return nil
	}
	mapped := make(map[string]atc.SnapshotInputConfig, len(source))
	for name, config := range source {
		if replacement, found := mapping[name]; found {
			name = replacement
		}
		mapped[name] = config
	}
	return mapped
}

// MappedSnapshotOutputs is the output-side counterpart of
// MappedSnapshotInputs.
func MappedSnapshotOutputs(source map[string]atc.SnapshotOutputConfig, mapping map[string]string) map[string]atc.SnapshotOutputConfig {
	if source == nil {
		return nil
	}
	mapped := make(map[string]atc.SnapshotOutputConfig, len(source))
	for name, config := range source {
		if replacement, found := mapping[name]; found {
			name = replacement
		}
		mapped[name] = config
	}
	return mapped
}

func humanReviewCandidate(step *atc.AgentStep) (string, bool, error) {
	if step == nil {
		return "", false, nil
	}
	question := false
	for _, output := range step.SnapshotOutputs {
		if output.Type == snapshot.TypeRef("question/v1") {
			question = true
			break
		}
	}
	if !question {
		return "", false, nil
	}
	var candidates []string
	for _, name := range SortedSnapshotInputKeys(step.SnapshotInputs) {
		if step.SnapshotInputs[name].Type == snapshot.TypeRef("repository-change/v1") {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) == 0 {
		return "", false, nil
	}
	if len(candidates) != 1 {
		return "", false, fmt.Errorf("human review requires exactly one repository-change/v1 input")
	}
	return candidates[0], true, nil
}

func bindingIdentity(binding snapshotBinding) (string, bool) {
	if binding.ambiguous || binding.presence == snapshotConditional {
		return "", false
	}
	if binding.producer != nil {
		return binding.producer.key(), true
	}
	return binding.writePath, binding.writePath != ""
}

func (checker *snapshotFlowChecker) requireValidation(
	entry snapshotEnvironment,
	validationName,
	candidateName string,
	inputs map[string]atc.SnapshotInputConfig,
	inputMapping map[string]string,
	identity string,
) error {
	validationName = mappedAgentValidationName(validationName, inputMapping)
	candidateName = mappedAgentValidationName(candidateName, inputMapping)
	if validationName == "" {
		return fmt.Errorf("workflow: %s: authoritative validation is required", identity)
	}
	for _, name := range []string{candidateName, validationName} {
		if inputs != nil {
			if declaration, ok := inputs[name]; !ok || declaration.Optional {
				return fmt.Errorf("workflow: %s: validation binding %q must be a required typed input", identity, name)
			}
		}
	}
	candidate, ok := entry[candidateName]
	if !ok || !candidate.typed || candidate.typ != snapshot.TypeRef("repository-change/v1") {
		return fmt.Errorf("workflow: %s: validation candidate is unavailable", identity)
	}
	candidateIdentity, exact := bindingIdentity(candidate)
	if !exact {
		return fmt.Errorf("workflow: %s: validation candidate is ambiguous", identity)
	}
	validation, ok := entry[validationName]
	if !ok || !validation.typed || validation.typ != snapshot.TypeRef("validation/v1") || validation.validation == nil {
		return fmt.Errorf("workflow: %s: validation is not authoritative dev validation", identity)
	}
	if validation.validation.candidate != candidateIdentity {
		return fmt.Errorf("workflow: %s: validation does not dominate the current candidate", identity)
	}
	for name, expected := range validation.validation.bases {
		name = mappedAgentValidationName(name, inputMapping)
		base, found := entry[name]
		actual, exact := bindingIdentity(base)
		if !found || !exact || actual != expected {
			return fmt.Errorf("workflow: %s: validation does not bind current base %q", identity, name)
		}
	}
	if _, found := checker.devValidationProfiles[validation.validation.profile]; !found {
		return fmt.Errorf("workflow: %s: validation profile is not frozen authority", identity)
	}
	return nil
}

// registerNodeIdentity records one workflow-local node identity. Within
// TypeCheckFunction itself, agent and task nodes use their authored
// function_id, while await_snapshot, publish_snapshot, and load_snapshot use
// their binding name, which downstream steps already reference by value.
// Every node checked by this pass shares one namespace: two nodes may never
// claim the same identity, because the durable node-occurrence projection is
// keyed by it.
//
// This does not by itself make the namespace uniform across a whole
// workflow: public input ports and bound resource sources join it only
// because RenderFunction prepends one synthetic load_snapshot step per
// input/source ahead of the authored plan before its own TypeCheckFunction
// pass. A function whose declared Inputs collide with a plan node's
// function_id passes this compile-time check (Inputs are bound straight into
// the environment, never through this method) and is only caught once
// rendering substitutes the synthetic load — see
// TestRenderFunctionRejectsInputPortCollidingWithFunctionID in
// render_test.go.
func (checker *snapshotFlowChecker) registerNodeIdentity(label, nodeID, identity string) error {
	if strings.TrimSpace(nodeID) == "" {
		return fmt.Errorf("workflow: %s: typed node requires a nonblank authored %s", identity, label)
	}
	if previous, found := checker.nodeIdentities[nodeID]; found {
		return fmt.Errorf("workflow: %s: duplicate %s %q; first declared as %s at %s", identity, label, nodeID, previous.label, previous.identity)
	}
	checker.nodeIdentities[nodeID] = nodeIdentityDeclaration{label: label, identity: identity}
	return nil
}

func (checker *snapshotFlowChecker) checkLeaf(
	kind string,
	displayName string,
	functionID string,
	ordinaryInputs []string,
	typedInputs map[string]atc.SnapshotInputConfig,
	ordinaryOutputs []string,
	typedOutputs map[string]atc.SnapshotOutputConfig,
	task *atc.TaskStep,
	agent *atc.AgentStep,
	entry snapshotEnvironment,
	path string,
) (snapshotFlow, error) {
	identity := fmt.Sprintf("%s.%s(%q)", path, kind, displayName)
	if err := checker.registerNodeIdentity("function_id", functionID, identity); err != nil {
		return snapshotFlow{}, err
	}

	// File-backed task membership is known only after its config snapshot is
	// fetched. The executor repeats this exact-coverage check against that
	// resolved config before selecting a worker.
	if task == nil || task.Config != nil {
		if err := validateExactTypedCoverage(kind+" input", ordinaryInputs, snapshotInputNames(typedInputs)); err != nil {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: %w", identity, err)
		}
		if err := validateExactTypedCoverage(kind+" output", ordinaryOutputs, snapshotOutputNames(typedOutputs)); err != nil {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: %w", identity, err)
		}
	}

	for _, name := range sortedUniqueStrings(ordinaryInputs) {
		binding, found := entry[name]
		if !found || !binding.typed {
			continue
		}
		if _, declared := typedInputs[name]; !declared {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: known typed artifact %q is consumed without input_types declaration", identity, name)
		}
	}

	for _, name := range SortedSnapshotInputKeys(typedInputs) {
		declaration := typedInputs[name]
		if err := declaration.Validate(); err != nil {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: input %q: %w", identity, name, err)
		}
		binding, found := entry[name]
		if !found {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: input %q is unavailable (use before produce or undeclared workflow input)", identity, name)
		}
		if !binding.typed {
			if binding.ambiguous {
				return snapshotFlow{}, fmt.Errorf("workflow: %s: input %q has ambiguous possible writes at %s", identity, name, binding.writePath)
			}
			return snapshotFlow{}, fmt.Errorf("workflow: %s: input %q is occupied by an untyped producer at %s", identity, name, binding.writePath)
		}
		if binding.typ != declaration.Type {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: input %q type mismatch: have %s, require %s", identity, name, binding.typ, declaration.Type)
		}
		if !declaration.Optional && binding.presence == snapshotConditional {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: required input %q cannot use a conditional binding", identity, name)
		}
	}

	env := cloneSnapshotEnvironment(entry)
	produced := make(map[string]snapshotBinding)
	for _, name := range sortedUniqueStrings(ordinaryOutputs) {
		if err := rejectResourceSourceShadow(entry, name, identity); err != nil {
			return snapshotFlow{}, err
		}
		if _, typed := typedOutputs[name]; typed {
			continue
		}
		binding := snapshotBinding{presence: snapshotGuaranteed, writePath: identity}
		env[name] = binding
		produced[name] = binding
	}

	for _, name := range SortedSnapshotOutputKeys(typedOutputs) {
		if err := rejectResourceSourceShadow(entry, name, identity); err != nil {
			return snapshotFlow{}, err
		}
		declaration := typedOutputs[name]
		if err := declaration.Validate(); err != nil {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: output %q: %w", identity, name, err)
		}
		if _, found := env[name]; found && declaration.Optional {
			return snapshotFlow{}, fmt.Errorf("workflow: %s: optional output %q shadows an existing artifact; its concrete producer would be path-dependent", identity, name)
		}
		presence := snapshotGuaranteed
		if declaration.Optional {
			presence = snapshotConditional
		}
		producer := &snapshotProducer{
			functionID:  functionID,
			kind:        kind,
			path:        identity,
			outputName:  name,
			insideRetry: checker.retryDepth > 0,
			task:        task,
			agent:       agent,
		}
		binding := snapshotBinding{
			typ:       declaration.Type,
			presence:  presence,
			typed:     true,
			producer:  producer,
			writePath: identity,
		}
		if task != nil && declaration.Type == snapshot.TypeRef("validation/v1") && task.DevValidationAuthority != nil && name == atc.DevValidationOutput {
			candidate, found := entry[task.DevValidationAuthority.CandidateInput]
			candidateID, exact := bindingIdentity(candidate)
			if !found || !exact {
				return snapshotFlow{}, fmt.Errorf("workflow: %s: authoritative validation candidate is not exact", identity)
			}
			bases := make(map[string]string, len(task.DevValidationAuthority.BaseInputs))
			for _, base := range task.DevValidationAuthority.BaseInputs {
				value, found := entry[base.Name]
				baseID, exact := bindingIdentity(value)
				if !found || !exact {
					return snapshotFlow{}, fmt.Errorf("workflow: %s: authoritative validation base %q is not exact", identity, base.Name)
				}
				bases[base.Name] = baseID
			}
			binding.validation = &validationBindingProvenance{candidate: candidateID, bases: bases, profile: task.DevValidationAuthority.ProfileName}
		}
		env[name] = binding
		produced[name] = binding
	}
	return snapshotFlow{
		env:         env,
		produced:    produced,
		mayProduced: cloneProduced(produced),
		allProduced: cloneProduced(produced),
	}, nil
}

func rejectResourceSourceShadow(entry snapshotEnvironment, name, path string) error {
	binding, found := entry[name]
	if !found || binding.resourceSource == "" {
		return nil
	}
	return fmt.Errorf("workflow: %s: resource source %q cannot be shadowed", path, binding.resourceSource)
}

func emptySnapshotFlow(entry snapshotEnvironment) snapshotFlow {
	return snapshotFlow{
		env:         cloneSnapshotEnvironment(entry),
		produced:    make(map[string]snapshotBinding),
		mayProduced: make(map[string]snapshotBinding),
		allProduced: make(map[string]snapshotBinding),
	}
}

func cloneSnapshotEnvironment(source snapshotEnvironment) snapshotEnvironment {
	clone := make(snapshotEnvironment, len(source))
	for name, binding := range source {
		clone[name] = binding
	}
	return clone
}

func cloneProduced(source map[string]snapshotBinding) map[string]snapshotBinding {
	clone := make(map[string]snapshotBinding, len(source))
	for name, binding := range source {
		clone[name] = binding
	}
	return clone
}

func mergeProduced(destination, source map[string]snapshotBinding) {
	for _, name := range sortedSnapshotBindingKeys(source) {
		destination[name] = source[name]
	}
}

func mergeMayProduced(destination, source map[string]snapshotBinding) {
	for _, name := range sortedSnapshotBindingKeys(source) {
		binding := source[name]
		if previous, found := destination[name]; found {
			binding = mergeBindingAlternatives(previous, binding)
		}
		destination[name] = binding
	}
}

func conditionalTryFlow(entry snapshotEnvironment, child snapshotFlow) snapshotFlow {
	env := cloneSnapshotEnvironment(entry)
	for _, name := range sortedSnapshotBindingKeys(child.allProduced) {
		previous, existed := entry[name]
		if !existed {
			write := child.allProduced[name]
			write.presence = snapshotConditional
			env[name] = write
			continue
		}
		env[name] = mergeBindingAlternatives(previous, child.allProduced[name])
	}
	return snapshotFlow{
		env:         env,
		produced:    make(map[string]snapshotBinding),
		mayProduced: cloneProduced(child.allProduced),
		allProduced: cloneProduced(child.allProduced),
	}
}

func applySuccessfulFlow(base snapshotEnvironment, flow snapshotFlow) snapshotEnvironment {
	env := cloneSnapshotEnvironment(base)
	for _, name := range sortedSnapshotBindingKeys(flow.mayProduced) {
		if _, guaranteedOrConditionalOutput := flow.produced[name]; guaranteedOrConditionalOutput {
			if final, found := flow.env[name]; found {
				env[name] = final
			} else {
				env[name] = flow.produced[name]
			}
			continue
		}
		previous, existed := env[name]
		if !existed {
			continue
		}
		env[name] = mergeBindingAlternatives(previous, flow.mayProduced[name])
	}
	return env
}

func mergeBindingAlternatives(left, right snapshotBinding) snapshotBinding {
	merged := snapshotBinding{
		presence:  snapshotGuaranteed,
		ambiguous: true,
		writePath: joinWritePaths(left.writePath, right.writePath),
	}
	if left.presence == snapshotConditional || right.presence == snapshotConditional {
		merged.presence = snapshotConditional
	}
	if left.typed && right.typed && left.typ == right.typ {
		merged.typed = true
		merged.typ = left.typ
		if left.producer == right.producer {
			merged.producer = left.producer
		}
	}
	return merged
}

func joinWritePaths(left, right string) string {
	switch {
	case left == "":
		return right
	case right == "" || left == right:
		return left
	default:
		return left + " or " + right
	}
}

// SortedSnapshotInputKeys returns a deterministic, sorted key order for a
// SnapshotInputs map. Exported for other packages (e.g. agent/workflow/graph)
// that need the same iteration order the type checker uses.
func SortedSnapshotInputKeys(values map[string]atc.SnapshotInputConfig) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// SortedSnapshotOutputKeys is the output-side counterpart of
// SortedSnapshotInputKeys.
func SortedSnapshotOutputKeys(values map[string]atc.SnapshotOutputConfig) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSnapshotBindingKeys(values map[string]snapshotBinding) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedIntMapKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedUniqueStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
