package workflow

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

const repositoryChangeV1 snapshot.TypeRef = "repository-change/v1"

func TestTypeCheckPublishSnapshotConsumesExactRequiredTypedInput(t *testing.T) {
	step := func(inputType snapshot.TypeRef) atc.Step {
		return atc.Step{Config: &atc.PublishSnapshotStep{
			Name:                  "publish-change",
			Publisher:             publisher.GitPublisher,
			Input:                 "change",
			InputType:             inputType,
			Destination:           "github.example/team/repo",
			Mode:                  publisher.ModePullRequest,
			Parameters:            map[string]string{"source_branch": "agent/change", "target_branch": "main"},
			ApprovalPolicyVersion: "engineering/v2",
		}}
	}

	t.Run("exact required typed input is accepted without changing the environment", func(t *testing.T) {
		function := &FunctionConfig{
			SignatureVersion: 1,
			Inputs:           []snapshot.Port{{Name: "change", Type: repositoryChangeV1}},
			Plan:             []atc.Step{step(repositoryChangeV1), step(repositoryChangeV1)},
		}

		if err := TypeCheckFunction(function); err != nil {
			t.Fatalf("TypeCheckFunction: %v", err)
		}
	})

	tests := []struct {
		name      string
		inputs    []snapshot.Port
		prefix    []atc.Step
		inputType snapshot.TypeRef
		want      string
	}{
		{
			name:      "missing input",
			inputType: repositoryChangeV1,
			want:      "unavailable (use before produce)",
		},
		{
			name:      "wrong exact type",
			inputs:    []snapshot.Port{{Name: "change", Type: "repository/v1"}},
			inputType: repositoryChangeV1,
			want:      "type mismatch",
		},
		{
			name:      "conditional input",
			inputs:    []snapshot.Port{{Name: "change", Type: repositoryChangeV1, Optional: true}},
			inputType: repositoryChangeV1,
			want:      "conditional binding",
		},
		{
			name:      "ordinary producer omits its output type",
			prefix:    []atc.Step{{Config: &atc.AgentStep{Name: "plain", FunctionID: "plain", Prompt: "produce", Outputs: []string{"change"}}}},
			inputType: repositoryChangeV1,
			want:      "every declared agent output must be typed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := append([]atc.Step(nil), test.prefix...)
			plan = append(plan, step(test.inputType))
			function := &FunctionConfig{SignatureVersion: 1, Inputs: test.inputs, Plan: plan}

			err := TypeCheckFunction(function)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestFullFunctionTargetRejectsRuntimePublisherInterpolation(t *testing.T) {
	definition := renderTestDefinition()
	definition.Compiled.Function.Plan = append(definition.Compiled.Function.Plan, atc.Step{Config: &atc.PublishSnapshotStep{
		Name: "publish-comment", Publisher: publisher.WorkItemPublisher,
		Input: "repo", InputType: repositoryV1,
		Destination: "((runtime_destination))", Mode: publisher.ModeComment,
		Parameters:            map[string]string{"body": "review completed"},
		ApprovalPolicyVersion: "engineering/v2",
	}})

	_, err := FullFunctionTarget(definition)
	if err == nil || !strings.Contains(err.Error(), "interpolation") {
		t.Fatalf("error = %v, want immutable interpolation refusal", err)
	}
}

func TestTypeCheckMergePublisherRequiresExactGuaranteedHumanAnswer(t *testing.T) {
	merge := func() *atc.PublishSnapshotStep {
		return &atc.PublishSnapshotStep{
			Name: "merge-change", Publisher: publisher.GitPublisher,
			Input: "change", InputType: repositoryChangeV1, Approval: "approval",
			Destination: "github.example/team/repo", Mode: publisher.ModeMerge,
			Parameters:            map[string]string{"target_branch": "main"},
			ApprovalPolicyVersion: "engineering/v2",
		}
	}
	validInputs := []snapshot.Port{{Name: "change", Type: repositoryChangeV1}}
	wait := func() atc.Step {
		return atc.Step{Config: &atc.TimeoutStep{Duration: "1h", Step: &atc.AwaitSnapshotStep{
			Name: "approval", Type: snapshot.TypeRef("human-answer/v1"),
			MergeApproval: &atc.MergeApprovalIntent{
				Input: "change", Publisher: publisher.GitPublisher,
				Destination:           "github.example/team/repo",
				Parameters:            map[string]string{"target_branch": "main"},
				ApprovalPolicyVersion: "engineering/v2", Prompt: "Merge this exact change?",
			},
			OnTimeout: atc.AwaitSnapshotOnTimeoutFail,
		}}}
	}
	if err := TypeCheckFunction(&FunctionConfig{
		SignatureVersion: 1, Inputs: validInputs, Plan: []atc.Step{wait(), {Config: merge()}},
	}); err != nil {
		t.Fatalf("valid merge flow: %v", err)
	}
	for name, mutate := range map[string]func(*atc.MergeApprovalIntent){
		"destination":                   func(intent *atc.MergeApprovalIntent) { intent.Destination = "github.example/other/repo" },
		"target branch":                 func(intent *atc.MergeApprovalIntent) { intent.Parameters["target_branch"] = "release" },
		"additional provider parameter": func(intent *atc.MergeApprovalIntent) { intent.Parameters["semantic"] = "changed" },
		"policy":                        func(intent *atc.MergeApprovalIntent) { intent.ApprovalPolicyVersion = "engineering/v3" },
	} {
		t.Run("exact intent mismatch "+name, func(t *testing.T) {
			waitStep := wait()
			intent := waitStep.Config.(*atc.TimeoutStep).Step.(*atc.AwaitSnapshotStep).MergeApproval
			mutate(intent)
			err := TypeCheckFunction(&FunctionConfig{
				SignatureVersion: 1, Inputs: validInputs, Plan: []atc.Step{waitStep, {Config: merge()}},
			})
			if err == nil || !strings.Contains(err.Error(), "does not bind this exact merge publication") {
				t.Fatalf("error = %v, want exact intent mismatch", err)
			}
		})
	}
	// expected_base_sha names the target tip the change lands on, which is not
	// knowable when a workflow is written. It is server-derived at execution
	// time from the bound repository-change/v1 input; authoring it is refused
	// so nobody reintroduces the unrunnable 40-hex placeholder the seed used to
	// carry.
	for name, mutate := range map[string]func(atc.Step, *atc.PublishSnapshotStep){
		"await intent": func(waitStep atc.Step, _ *atc.PublishSnapshotStep) {
			intent := waitStep.Config.(*atc.TimeoutStep).Step.(*atc.AwaitSnapshotStep).MergeApproval
			intent.Parameters["expected_base_sha"] = strings.Repeat("a", 40)
		},
		"publication": func(_ atc.Step, publish *atc.PublishSnapshotStep) {
			publish.Parameters["expected_base_sha"] = strings.Repeat("a", 40)
		},
		"both": func(waitStep atc.Step, publish *atc.PublishSnapshotStep) {
			intent := waitStep.Config.(*atc.TimeoutStep).Step.(*atc.AwaitSnapshotStep).MergeApproval
			intent.Parameters["expected_base_sha"] = strings.Repeat("a", 40)
			publish.Parameters["expected_base_sha"] = strings.Repeat("a", 40)
		},
	} {
		t.Run("authored merge base rejected in "+name, func(t *testing.T) {
			waitStep := wait()
			publish := merge()
			mutate(waitStep, publish)
			err := TypeCheckFunction(&FunctionConfig{
				SignatureVersion: 1, Inputs: validInputs, Plan: []atc.Step{waitStep, {Config: publish}},
			})
			if err == nil || !strings.Contains(err.Error(), "expected_base_sha is server-derived") {
				t.Fatalf("error = %v, want server-derived merge base refusal", err)
			}
		})
	}

	t.Run("ordinary authored question cannot authorize merge", func(t *testing.T) {
		ordinary := atc.Step{Config: &atc.TimeoutStep{Duration: "1h", Step: &atc.AwaitSnapshotStep{
			Name: "approval", Question: "question", Type: snapshot.TypeRef("human-answer/v1"),
			OnTimeout: atc.AwaitSnapshotOnTimeoutFail,
		}}}
		err := TypeCheckFunction(&FunctionConfig{
			SignatureVersion: 1,
			Inputs:           append(append([]snapshot.Port(nil), validInputs...), snapshot.Port{Name: "question", Type: "question/v1"}),
			Plan:             []atc.Step{ordinary, {Config: merge()}},
		})
		if err == nil || !strings.Contains(err.Error(), "server-bound merge_approval") {
			t.Fatalf("error = %v, want server-bound approval refusal", err)
		}
	})

	for _, test := range []struct {
		name   string
		inputs []snapshot.Port
		plan   []atc.Step
		want   string
	}{
		{name: "missing", inputs: validInputs, plan: []atc.Step{{Config: merge()}}, want: "use before produce"},
		{name: "wrong type", inputs: []snapshot.Port{{Name: "change", Type: repositoryChangeV1}, {Name: "approval", Type: snapshot.TypeRef("review/v1")}}, plan: []atc.Step{{Config: merge()}}, want: "human-answer/v1"},
		{name: "conditional", inputs: []snapshot.Port{{Name: "change", Type: repositoryChangeV1}, {Name: "approval", Type: snapshot.TypeRef("human-answer/v1"), Optional: true}}, plan: []atc.Step{{Config: merge()}}, want: "conditional"},
		{name: "typed input is not a durable wait", inputs: []snapshot.Port{{Name: "change", Type: repositoryChangeV1}, {Name: "approval", Type: snapshot.TypeRef("human-answer/v1")}}, plan: []atc.Step{{Config: merge()}}, want: "merge_approval await_snapshot"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := TypeCheckFunction(&FunctionConfig{
				SignatureVersion: 1, Inputs: test.inputs, Plan: test.plan,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRenderMergePublisherInjectsUnforgeableWorkflowRunIdentity(t *testing.T) {
	definition := renderTestDefinition()
	definition.Compiled.Function.Inputs[0].Type = repositoryChangeV1
	definition.Compiled.Function.Plan[0].Config.(*atc.AgentStep).SnapshotInputs["repo"] = atc.SnapshotInputConfig{Type: repositoryChangeV1}
	wait := &atc.TimeoutStep{Duration: "1h", Step: &atc.AwaitSnapshotStep{
		Name: "approval", Type: snapshot.TypeRef("human-answer/v1"),
		MergeApproval: &atc.MergeApprovalIntent{
			Input: "repo", Publisher: publisher.GitPublisher,
			Destination:           "github.example/team/repo",
			Parameters:            map[string]string{"target_branch": "main"},
			ApprovalPolicyVersion: "engineering/v2", Prompt: "Merge this exact change?",
		},
		OnTimeout: atc.AwaitSnapshotOnTimeoutFail,
	}}
	merge := &atc.PublishSnapshotStep{
		Name: "merge-change", Publisher: publisher.GitPublisher,
		Input: "repo", InputType: repositoryChangeV1, Approval: "approval",
		Destination: "github.example/team/repo", Mode: publisher.ModeMerge,
		Parameters:            map[string]string{"target_branch": "main"},
		ApprovalPolicyVersion: "engineering/v2",
	}
	definition.Compiled.Function.Plan = append(definition.Compiled.Function.Plan,
		atc.Step{Config: wait}, atc.Step{Config: merge})
	target, err := FullFunctionTarget(definition)
	if err != nil {
		t.Fatalf("FullFunctionTarget: %v", err)
	}
	rendered, err := RenderFunction(target)
	if err != nil {
		t.Fatalf("RenderFunction: %v", err)
	}
	var renderedMerge *atc.PublishSnapshotStep
	for _, step := range rendered.Config.Jobs[0].PlanSequence {
		if publish, ok := step.Config.(*atc.PublishSnapshotStep); ok {
			renderedMerge = publish
		}
	}
	if renderedMerge == nil || renderedMerge.WorkflowRunID != "((workflow_run_id))" || renderedMerge.Approval != "approval" {
		t.Fatalf("rendered merge = %+v", renderedMerge)
	}
	var renderedWait *atc.AwaitSnapshotStep
	for _, step := range rendered.Config.Jobs[0].PlanSequence {
		if timeout, ok := step.Config.(*atc.TimeoutStep); ok {
			if wait, ok := timeout.Step.(*atc.AwaitSnapshotStep); ok {
				renderedWait = wait
			}
		}
	}
	if renderedWait == nil || renderedWait.WorkflowDefinitionID != definition.ID || renderedWait.WorkflowRunID != "((workflow_run_id))" {
		t.Fatalf("rendered merge approval wait = %+v", renderedWait)
	}

	definition.Compiled.Function.Plan[len(definition.Compiled.Function.Plan)-1].Config.(*atc.PublishSnapshotStep).WorkflowRunID = "17"
	_, err = FullFunctionTarget(definition)
	if err == nil || !strings.Contains(err.Error(), "renderer-owned") {
		t.Fatalf("authored workflow run identity error = %v", err)
	}
}
