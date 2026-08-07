package workflow

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

const repositoryChangeV1 snapshot.TypeRef = "repository-change/v1"

func exactValidationStep(candidate string) atc.Step {
	authority := &atc.DevValidationAuthority{ProfileName: "exact", CandidateInput: candidate}
	return atc.Step{Config: &atc.TaskStep{Name: "validate", FunctionID: "validate", Config: &atc.TaskConfig{Inputs: []atc.TaskInputConfig{{Name: candidate}}, Outputs: []atc.TaskOutputConfig{{Name: "validation"}}}, SnapshotInputs: map[string]atc.SnapshotInputConfig{candidate: {Type: repositoryChangeV1}}, SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"validation": {Type: snapshot.TypeRef("validation/v1")}}, DevValidationAuthority: authority}}
}

func validationProfiles() []CompiledDevValidationProfile {
	return validationProfilesFor("change")
}

func validationProfilesFor(candidate string) []CompiledDevValidationProfile {
	profile := []byte("schema_version: 1\nname: exact\nchecks:\n  - id: tests\n    operation: test\n    scope: full\n    timeout: 20m\n    retries: 0\n")
	config := []byte("schema_version: 1\nrepo:\n  test: {cmd: [\"go\", \"test\", \"./...\"]}\ncomponents:\n  - id: repository\n    description: repository\n    paths: [\"src/\"]\n    kind: other\n")
	image := snapshot.Digest("sha256:" + strings.Repeat("a", 64))
	return []CompiledDevValidationProfile{{Name: "exact", Candidate: DevValidationContract{Name: candidate, Type: repositoryChangeV1}, CapabilityImage: "registry.example/validator@" + image.String(), CapabilityImageDigest: image, Command: devValidationCommand(), Profile: profile, ProfileDigest: validationDigest(profile), ProtectedConfig: config, ProtectedConfigDigest: validationDigest(config)}}
}

func validationDigest(raw []byte) snapshot.Digest {
	sum := sha256.Sum256(raw)
	return snapshot.Digest(fmt.Sprintf("sha256:%x", sum))
}
func validationProvenance() string {
	value, err := DevValidationProvenanceHash(validationProfiles())
	if err != nil {
		panic(err)
	}
	return value
}

func validationProvenanceFor(candidate string) string {
	value, err := DevValidationProvenanceHash(validationProfilesFor(candidate))
	if err != nil {
		panic(err)
	}
	return value
}

func renderValidationStep(candidate string) atc.Step {
	profile := validationProfilesFor(candidate)[0]
	authority := &atc.DevValidationAuthority{ProfileName: profile.Name, Profile: profile.Profile, ProfileDigest: profile.ProfileDigest, ProtectedConfig: profile.ProtectedConfig, ProtectedConfigDigest: profile.ProtectedConfigDigest, CapabilityImage: profile.CapabilityImage, CapabilityImageDigest: profile.CapabilityImageDigest, CandidateInput: candidate}
	return atc.Step{Config: &atc.TaskStep{Name: "validate", FunctionID: "validate", Config: &atc.TaskConfig{Platform: "linux", RootfsURI: profile.CapabilityImage, Run: atc.TaskRunConfig{Path: "/bin/true"}, Inputs: []atc.TaskInputConfig{{Name: candidate}}, Outputs: []atc.TaskOutputConfig{{Name: "validation"}}}, SnapshotInputs: map[string]atc.SnapshotInputConfig{candidate: {Type: repositoryChangeV1}}, SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"validation": {Type: snapshot.TypeRef("validation/v1")}}, DevValidationAuthority: authority}}
}

func namedPublishStep(name string, inputType snapshot.TypeRef) atc.Step {
	return atc.Step{Config: &atc.PublishSnapshotStep{
		Name:                  name,
		Publisher:             publisher.GitPublisher,
		Input:                 "change",
		InputType:             inputType,
		Destination:           "github.example/team/repo",
		Mode:                  publisher.ModeBranch,
		Parameters:            map[string]string{"source_branch": "agent/change", "target_branch": "main"},
		ApprovalPolicyVersion: "engineering/v2",
		Validation:            "validation",
	}}
}

func TestTypeCheckPublishSnapshotConsumesExactRequiredTypedInput(t *testing.T) {
	t.Run("exact required typed input is accepted without changing the environment", func(t *testing.T) {
		// Two distinctly named publish_snapshot steps, both publishing the
		// same exact validated input, prove that publish leaves the
		// environment unchanged. They must use distinct workflow-local node
		// identities: identity is name-keyed for publish_snapshot exactly
		// like it is function_id-keyed for agent/task, so two structurally
		// distinct nodes may never share one.
		function := &FunctionConfig{
			SignatureVersion:            1,
			Inputs:                      []snapshot.Port{{Name: "change", Type: repositoryChangeV1}},
			Plan:                        []atc.Step{exactValidationStep("change"), namedPublishStep("publish-change", repositoryChangeV1), namedPublishStep("publish-change-again", repositoryChangeV1)},
			DevValidationProfiles:       validationProfiles(),
			DevValidationProvenanceHash: validationProvenance(),
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
			plan = append(plan, namedPublishStep("publish-change", test.inputType))
			function := &FunctionConfig{SignatureVersion: 1, Inputs: test.inputs, Plan: plan}

			err := TypeCheckFunction(function)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

// TestTypeCheckRejectsFunctionIDCollidingWithPublishName mirrors
// TestTypeCheckRejectsFunctionIDCollidingWithAwaitName for the third node
// kind that shares the workflow-local identity namespace: publish_snapshot.
// An agent's function_id must not silently collide with a publish_snapshot
// binding name.
func TestTypeCheckRejectsFunctionIDCollidingWithPublishName(t *testing.T) {
	agent := typedAgent("ship", "ship", nil, nil, []string{"repo"}, map[string]atc.SnapshotOutputConfig{
		"repo": {Type: repositoryV1},
	})

	function := &FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{
			{Config: agent},
			{Config: &atc.PublishSnapshotStep{
				Name:                  "ship",
				Publisher:             publisher.GitPublisher,
				Input:                 "repo",
				InputType:             repositoryV1,
				Destination:           "github.example/team/repo",
				Mode:                  publisher.ModeBranch,
				Parameters:            map[string]string{"source_branch": "agent/change", "target_branch": "main"},
				ApprovalPolicyVersion: "engineering/v2",
			}},
		},
	}

	err := TypeCheckFunction(function)
	if err == nil || !strings.Contains(err.Error(), `duplicate binding name "ship"`) {
		t.Fatalf("error = %v, want a duplicate node identity between the ship function_id and the ship publish_snapshot", err)
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
			Validation:            "validation",
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
			OnTimeout:  atc.AwaitSnapshotOnTimeoutFail,
			Validation: "validation",
		}}}
	}
	if err := TypeCheckFunction(&FunctionConfig{
		SignatureVersion: 1, Inputs: validInputs, Plan: []atc.Step{exactValidationStep("change"), wait(), {Config: merge()}}, DevValidationProfiles: validationProfiles(), DevValidationProvenanceHash: validationProvenance(),
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
				SignatureVersion: 1, Inputs: validInputs, Plan: []atc.Step{exactValidationStep("change"), waitStep, {Config: merge()}}, DevValidationProfiles: validationProfiles(), DevValidationProvenanceHash: validationProvenance(),
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
				SignatureVersion: 1, Inputs: validInputs, Plan: []atc.Step{exactValidationStep("change"), waitStep, {Config: publish}}, DevValidationProfiles: validationProfiles(), DevValidationProvenanceHash: validationProvenance(),
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
			SignatureVersion:      1,
			Inputs:                append(append([]snapshot.Port(nil), validInputs...), snapshot.Port{Name: "question", Type: "question/v1"}),
			Plan:                  []atc.Step{exactValidationStep("change"), ordinary, {Config: merge()}},
			DevValidationProfiles: validationProfiles(), DevValidationProvenanceHash: validationProvenance(),
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
				SignatureVersion: 1, Inputs: test.inputs, Plan: append([]atc.Step{exactValidationStep("change")}, test.plan...), DevValidationProfiles: validationProfiles(), DevValidationProvenanceHash: validationProvenance(),
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
	definition.Compiled.Function.DevValidationProfiles = validationProfilesFor("repo")
	definition.Compiled.Function.DevValidationProvenanceHash = validationProvenanceFor("repo")
	wait := &atc.TimeoutStep{Duration: "1h", Step: &atc.AwaitSnapshotStep{
		Name: "approval", Type: snapshot.TypeRef("human-answer/v1"),
		MergeApproval: &atc.MergeApprovalIntent{
			Input: "repo", Publisher: publisher.GitPublisher,
			Destination:           "github.example/team/repo",
			Parameters:            map[string]string{"target_branch": "main"},
			ApprovalPolicyVersion: "engineering/v2", Prompt: "Merge this exact change?",
		},
		OnTimeout:  atc.AwaitSnapshotOnTimeoutFail,
		Validation: "validation",
	}}
	merge := &atc.PublishSnapshotStep{
		Name: "merge-change", Publisher: publisher.GitPublisher,
		Input: "repo", InputType: repositoryChangeV1, Approval: "approval",
		Destination: "github.example/team/repo", Mode: publisher.ModeMerge,
		Parameters:            map[string]string{"target_branch": "main"},
		ApprovalPolicyVersion: "engineering/v2",
		Validation:            "validation",
	}
	definition.Compiled.Function.Plan = append([]atc.Step{renderValidationStep("repo")}, definition.Compiled.Function.Plan...)
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
