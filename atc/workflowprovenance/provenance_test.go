package workflowprovenance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

const testDigestImage = "registry.example/image@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
const secondTestDigestImage = "registry.example/image@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

func TestFromPlanCapturesCanonicalPlanHashEmptyEnvelopeAndExactOutputContract(t *testing.T) {
	declaration := workflowOutput("review/v1", "review", false, 41, "9007199254740993")
	plan := agentPlan("a", "review-step", "finding", declaration)
	input := captureInput(t, 41, 9007199254740993, atc.Step{Config: &atc.AgentStep{
		Name: "review-step",
		SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
			"finding": declaration,
		},
	}})

	captured, err := FromPlan(input, plan)
	if err != nil {
		t.Fatalf("FromPlan: %v", err)
	}
	canonical, err := atc.CanonicalJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	if string(captured.CanonicalPlan) != string(canonical) {
		t.Fatalf("canonical plan = %s, want %s", captured.CanonicalPlan, canonical)
	}
	sum := sha256.Sum256(append([]byte("workflow-actual-plan/v1\x00"), canonical...))
	if captured.PlanHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("plan hash = %q", captured.PlanHash)
	}
	if string(captured.ResolvedDependencies) != `{"version":1,"resources":[],"images":[],"platform_resource_types":[]}` {
		t.Fatalf("dependencies = %s", captured.ResolvedDependencies)
	}
	wantOutputs := []ExpectedOutput{{
		Port: "review", Type: snapshot.TypeRef("review/v1"), Optional: false,
		WorkflowDefinitionID: 41, WorkflowRunID: snapshot.WorkflowRunID(9007199254740993),
		Producers: []ExpectedProducer{{PlanID: "a", StepKind: "agent", StepName: "review-step", LocalOutputPort: "finding"}},
	}}
	if !reflect.DeepEqual(captured.Outputs, wantOutputs) {
		t.Fatalf("outputs = %#v, want %#v", captured.Outputs, wantOutputs)
	}
}

func TestVerifyFrozenReconstructsCanonicalCaptureFromStrictPersistedEvidence(t *testing.T) {
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "agent"}})
	plan := emptyAgentPlan("agent")
	captured, err := FromPlan(input, plan)
	if err != nil {
		t.Fatalf("FromPlan: %v", err)
	}

	verified, err := VerifyFrozen(
		input, captured.CanonicalPlan, captured.PlanHash, captured.ResolvedDependencies,
	)
	if err != nil {
		t.Fatalf("VerifyFrozen: %v", err)
	}
	if !reflect.DeepEqual(verified, captured) {
		t.Fatalf("verified = %#v, want %#v", verified, captured)
	}

	tests := []struct {
		name         string
		actualPlan   json.RawMessage
		planHash     string
		dependencies json.RawMessage
	}{
		{
			name:         "altered hash",
			actualPlan:   captured.CanonicalPlan,
			planHash:     strings.Repeat("0", sha256.Size*2),
			dependencies: captured.ResolvedDependencies,
		},
		{
			name:       "altered dependencies",
			actualPlan: captured.CanonicalPlan,
			planHash:   captured.PlanHash,
			dependencies: json.RawMessage(
				`{"version":1,"resources":[],"images":[],"platform_resource_types":["git"]}`,
			),
		},
		{
			name:         "malformed dependencies",
			actualPlan:   captured.CanonicalPlan,
			planHash:     captured.PlanHash,
			dependencies: json.RawMessage(`{"version":`),
		},
		{
			name:         "unknown dependency field",
			actualPlan:   captured.CanonicalPlan,
			planHash:     captured.PlanHash,
			dependencies: json.RawMessage(`{"version":1,"resources":[],"images":[],"platform_resource_types":[],"future":true}`),
		},
		{
			name:         "malformed actual plan",
			actualPlan:   json.RawMessage(`{"id":`),
			planHash:     captured.PlanHash,
			dependencies: captured.ResolvedDependencies,
		},
		{
			name:         "unknown actual plan arm",
			actualPlan:   json.RawMessage(`{"id":"agent","agent":{"name":"agent"},"future":true}`),
			planHash:     captured.PlanHash,
			dependencies: captured.ResolvedDependencies,
		},
		{
			name:         "trailing actual plan",
			actualPlan:   append(append(json.RawMessage(nil), captured.CanonicalPlan...), []byte(`{}`)...),
			planHash:     captured.PlanHash,
			dependencies: captured.ResolvedDependencies,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := VerifyFrozen(input, test.actualPlan, test.planHash, test.dependencies)
			if !errors.Is(err, ErrInvalidProvenance) {
				t.Fatalf("error = %v, want invalid provenance", err)
			}
		})
	}
}

func TestVerifyFrozenAcceptsEquivalentJSONBReserializationAndReturnsCanonicalBytes(t *testing.T) {
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "agent"}})
	captured, err := FromPlan(input, emptyAgentPlan("agent"))
	if err != nil {
		t.Fatalf("FromPlan: %v", err)
	}
	persistedPlan := append(json.RawMessage(" \n\t"), captured.CanonicalPlan...)
	persistedDependencies := json.RawMessage(
		`{ "platform_resource_types": [], "images": [], "resources": [], "version": 1 }`,
	)

	verified, err := VerifyFrozen(input, persistedPlan, captured.PlanHash, persistedDependencies)
	if err != nil {
		t.Fatalf("VerifyFrozen: %v", err)
	}
	if !reflect.DeepEqual(verified, captured) {
		t.Fatalf("verified = %#v, want canonical %#v", verified, captured)
	}
}

func TestVerifyFrozenPreservesOutputContractMismatchForOtherwiseValidEvidence(t *testing.T) {
	declaration := workflowOutput("review/v1", "review", false, 41, "9")
	validInput := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{
		Name: "review", SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"finding": declaration},
	}})
	plan := agentPlan("agent", "review", "finding", declaration)
	captured, err := FromPlan(validInput, plan)
	if err != nil {
		t.Fatalf("FromPlan: %v", err)
	}
	mismatchedInput := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{
		Name: "different", SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"finding": declaration},
	}})

	_, err = VerifyFrozen(
		mismatchedInput, captured.CanonicalPlan, captured.PlanHash, captured.ResolvedDependencies,
	)
	if !errors.Is(err, ErrOutputContractMismatch) || errors.Is(err, ErrInvalidProvenance) {
		t.Fatalf("error = %v, want only output contract mismatch", err)
	}

	corruptDependencies := json.RawMessage(
		`{"version":1,"resources":[],"images":[],"platform_resource_types":["git"]}`,
	)
	_, err = VerifyFrozen(mismatchedInput, captured.CanonicalPlan, captured.PlanHash, corruptDependencies)
	if !errors.Is(err, ErrInvalidProvenance) || errors.Is(err, ErrOutputContractMismatch) {
		t.Fatalf("corrupt evidence error = %v, want only invalid provenance", err)
	}
}

func TestFromPlanValidatesInputAndConcreteConfigStrictly(t *testing.T) {
	valid := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "agent"}})
	plan := agentPlan("a", "agent", "", atc.SnapshotOutputConfig{})
	for _, test := range []struct {
		name   string
		mutate func(*Input)
	}{
		{name: "zero run", mutate: func(input *Input) { input.WorkflowRunID = 0 }},
		{name: "zero definition", mutate: func(input *Input) { input.WorkflowDefinitionID = 0 }},
		{name: "missing config", mutate: func(input *Input) { input.ConcreteConfig = nil }},
		{name: "malformed config", mutate: func(input *Input) { input.ConcreteConfig = json.RawMessage(`{"jobs":`) }},
		{name: "unknown config field", mutate: func(input *Input) {
			input.ConcreteConfig = json.RawMessage(`{"jobs":[{"name":"run","plan":[{"agent":"agent"}]}],"future":true}`)
		}},
		{name: "wrong job", mutate: func(input *Input) {
			input.ConcreteConfig = mustCanonical(t, atc.Config{Jobs: atc.JobConfigs{{Name: "other", PlanSequence: []atc.Step{{Config: &atc.AgentStep{Name: "agent"}}}}}})
		}},
		{name: "multiple jobs", mutate: func(input *Input) {
			input.ConcreteConfig = mustCanonical(t, atc.Config{Jobs: atc.JobConfigs{{Name: "run"}, {Name: "other"}}})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			input.ConcreteConfig = append(json.RawMessage(nil), valid.ConcreteConfig...)
			test.mutate(&input)
			_, err := FromPlan(input, plan)
			if !errors.Is(err, ErrInvalidProvenance) {
				t.Fatalf("error = %v, want invalid provenance", err)
			}
		})
	}
}

func TestFromPlanTraversesEveryWrapperAcrossTemplateAndTypeImage(t *testing.T) {
	get := resourceGetPlan("dep", "source", "repository", "git", atc.Version{"ref": "abc"}, atc.Source{
		"uri": "https://example.invalid/repo", "password": "must-not-leak",
	})
	wrappers := map[string]atc.Plan{
		"do":          {ID: "wrap", Do: planList(get)},
		"in_parallel": {ID: "wrap", InParallel: &atc.InParallelPlan{Steps: []atc.Plan{get}}},
		"try":         {ID: "wrap", Try: &atc.TryPlan{Step: get}},
		"timeout":     {ID: "wrap", Timeout: &atc.TimeoutPlan{Step: get, Duration: "1m"}},
		"retry":       {ID: "wrap", Retry: retryPlans(get)},
		"on_success":  {ID: "wrap", OnSuccess: &atc.OnSuccessPlan{Step: emptyAgentPlan("before"), Next: get}},
		"on_failure":  {ID: "wrap", OnFailure: &atc.OnFailurePlan{Step: emptyAgentPlan("before"), Next: get}},
		"on_abort":    {ID: "wrap", OnAbort: &atc.OnAbortPlan{Step: emptyAgentPlan("before"), Next: get}},
		"on_error":    {ID: "wrap", OnError: &atc.OnErrorPlan{Step: emptyAgentPlan("before"), Next: get}},
		"ensure":      {ID: "wrap", Ensure: &atc.EnsurePlan{Step: emptyAgentPlan("before"), Next: get}},
	}
	template, err := atc.CanonicalJSON(get)
	if err != nil {
		t.Fatal(err)
	}
	wrappers["across"] = atc.Plan{ID: "wrap", Across: &atc.AcrossPlan{
		Vars: []atc.AcrossVar{{Var: "item", Values: []string{"one"}}}, SubStepTemplate: string(template),
	}}

	input := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "contract"}})
	for name, plan := range wrappers {
		t.Run(name, func(t *testing.T) {
			captured, err := FromPlan(input, plan)
			if err != nil {
				t.Fatalf("FromPlan: %v", err)
			}
			var envelope DependencyEnvelope
			if err := json.Unmarshal(captured.ResolvedDependencies, &envelope); err != nil {
				t.Fatal(err)
			}
			if len(envelope.Resources) != 1 || envelope.Resources[0].PlanID != "dep" ||
				envelope.Resources[0].Name != "source" || envelope.Resources[0].Resource != "repository" ||
				envelope.Resources[0].Type != "git" || envelope.Resources[0].Version["ref"] != "abc" {
				t.Fatalf("resources = %#v", envelope.Resources)
			}
			if strings.Contains(string(captured.ResolvedDependencies), "must-not-leak") {
				t.Fatal("dependency envelope leaked source credentials")
			}
			if !reflect.DeepEqual(envelope.PlatformResourceTypes, []string{"git"}) {
				t.Fatalf("platform resource types = %#v", envelope.PlatformResourceTypes)
			}
		})
	}

	imagePlan := resourceGetPlan("get", "source", "repository", "custom-git", atc.Version{"ref": "abc"}, nil)
	imagePlan.Get.TypeImage = atc.TypeImage{ImageRef: testDigestImage}
	captured, err := FromPlan(input, imagePlan)
	if err != nil {
		t.Fatalf("direct type image: %v", err)
	}
	var envelope DependencyEnvelope
	if err := json.Unmarshal(captured.ResolvedDependencies, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Images) != 1 || envelope.Images[0].Kind != ImageKindCustomResourceType ||
		envelope.Images[0].Name != "custom-git" || envelope.Images[0].ImageRef != testDigestImage {
		t.Fatalf("images = %#v", envelope.Images)
	}
}

func TestFromPlanTraversesNestedTypeImageCheckAndGetPlans(t *testing.T) {
	imageCheck := atc.Plan{ID: "image-check", Check: &atc.CheckPlan{
		Name: "custom-git-image", Type: "image-bootstrap", Source: atc.Source{"token": "check-secret"},
		TypeImage: atc.TypeImage{ImageRef: testDigestImage}, ResourceType: "custom-git",
	}}
	imageGet := resourceGetPlan(
		"image-get", "custom-git-image", "", "registry-image",
		atc.Version{"digest": "sha256:resolved"}, atc.Source{"repository": "example/custom-git", "token": "get-secret"},
	)
	outer := resourceGetPlan(
		"source-get", "source", "repository", "custom-git",
		atc.Version{"ref": "abc"}, atc.Source{"uri": "https://example.invalid/repo"},
	)
	outer.Get.TypeImage = atc.TypeImage{
		BaseType: "registry-image", CheckPlan: &imageCheck, GetPlan: &imageGet,
	}
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "contract"}})

	captured, err := FromPlan(input, outer)
	if err != nil {
		t.Fatalf("FromPlan: %v", err)
	}
	var envelope DependencyEnvelope
	if err := json.Unmarshal(captured.ResolvedDependencies, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Resources) != 1 || envelope.Resources[0].PlanID != "source-get" {
		t.Fatalf("resources = %#v", envelope.Resources)
	}
	if len(envelope.Images) != 2 {
		t.Fatalf("images = %#v", envelope.Images)
	}
	if envelope.Images[0].PlanID != "image-check" || envelope.Images[0].Name != "image-bootstrap" ||
		envelope.Images[0].ImageRef != testDigestImage {
		t.Fatalf("check-plan image = %#v", envelope.Images[0])
	}
	if envelope.Images[1].PlanID != "image-get" || envelope.Images[1].Name != "custom-git" ||
		envelope.Images[1].Type != "registry-image" || envelope.Images[1].Version["digest"] != "sha256:resolved" {
		t.Fatalf("get-plan image = %#v", envelope.Images[1])
	}
	if !reflect.DeepEqual(envelope.PlatformResourceTypes, []string{"registry-image"}) {
		t.Fatalf("platform resource types = %#v", envelope.PlatformResourceTypes)
	}
	if strings.Contains(string(captured.ResolvedDependencies), "check-secret") ||
		strings.Contains(string(captured.ResolvedDependencies), "get-secret") {
		t.Fatal("dependency envelope leaked nested type-image source credentials")
	}
}

func TestFromPlanTraversesSharedPlanPointersInEveryDependencyContext(t *testing.T) {
	sharedImageGet := resourceGetPlan(
		"image-get", "custom-image", "", "registry-image",
		atc.Version{"digest": "sha256:resolved"}, atc.Source{"repository": "example/custom"},
	)
	put := func(id, typ string) atc.Plan {
		return atc.Plan{ID: atc.PlanID(id), Put: &atc.PutPlan{
			Name: id, Type: typ, TypeImage: atc.TypeImage{BaseType: "registry-image", GetPlan: &sharedImageGet},
		}}
	}
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "contract"}})
	captured, err := FromPlan(input, atc.Plan{ID: "root", Do: planList(put("first", "type-a"), put("second", "type-b"))})
	if err != nil {
		t.Fatalf("FromPlan: %v", err)
	}
	var envelope DependencyEnvelope
	if err := json.Unmarshal(captured.ResolvedDependencies, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Images) != 2 || envelope.Images[0].Name != "type-a" || envelope.Images[1].Name != "type-b" {
		t.Fatalf("images = %#v", envelope.Images)
	}
}

func TestFromPlanCapturesTaskAndCapabilityImageIdentitiesWithoutSecrets(t *testing.T) {
	task := atc.Plan{ID: "task", Task: &atc.TaskPlan{
		Name: "compile",
		Config: &atc.TaskConfig{Platform: "linux", ImageResource: &atc.ImageResource{
			Type: "custom-task", Source: atc.Source{"repository": "example/task", "token": "must-not-leak"},
			Version: atc.Version{"digest": "sha256:task"},
		}},
		ResourceTypes: atc.ResourceTypes{{Name: "custom-task", Image: testDigestImage}},
		Sidecars:      []atc.SidecarSource{{Config: &atc.SidecarConfig{Name: "database", Image: testDigestImage}}},
	}}
	agent := atc.Plan{ID: "agent", Agent: &atc.AgentPlan{
		Name: "review", Sidecars: []atc.SidecarSource{{Config: &atc.SidecarConfig{Name: "dev-mcp", Image: testDigestImage}}},
	}}
	plan := atc.Plan{ID: "root", Do: planList(task, agent)}
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "contract"}})

	captured, err := FromPlan(input, plan)
	if err != nil {
		t.Fatalf("FromPlan: %v", err)
	}
	var envelope DependencyEnvelope
	if err := json.Unmarshal(captured.ResolvedDependencies, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Images) != 4 {
		t.Fatalf("images = %#v", envelope.Images)
	}
	wantKinds := []ImageKind{
		ImageKindCapabilitySidecar, ImageKindCapabilitySidecar,
		ImageKindCustomResourceType, ImageKindTask,
	}
	for index, kind := range wantKinds {
		if envelope.Images[index].Kind != kind {
			t.Fatalf("image kinds = %#v", envelope.Images)
		}
	}
	if strings.Contains(string(captured.ResolvedDependencies), "must-not-leak") {
		t.Fatal("dependency envelope leaked task image credentials")
	}
}

func TestFromPlanAllowsOnlyRetryToSupplyMultipleConcreteProducerPlanIDs(t *testing.T) {
	declaration := workflowOutput("review/v1", "review", false, 41, "9")
	configAgent := &atc.AgentStep{Name: "review-step", SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"finding": declaration}}
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.RetryStep{Step: configAgent, Attempts: 2}})
	first := agentPlan("attempt-2", "review-step", "finding", declaration)
	second := agentPlan("attempt-1", "review-step", "finding", declaration)
	plan := atc.Plan{ID: "retry", Retry: retryPlans(first, second)}

	captured, err := FromPlan(input, plan)
	if err != nil {
		t.Fatalf("FromPlan: %v", err)
	}
	want := []ExpectedProducer{
		{PlanID: "attempt-1", StepKind: "agent", StepName: "review-step", LocalOutputPort: "finding"},
		{PlanID: "attempt-2", StepKind: "agent", StepName: "review-step", LocalOutputPort: "finding"},
	}
	if len(captured.Outputs) != 1 || !reflect.DeepEqual(captured.Outputs[0].Producers, want) {
		t.Fatalf("outputs = %#v", captured.Outputs)
	}

	ambiguous := atc.Plan{ID: "root", Do: planList(first, second)}
	_, err = FromPlan(input, ambiguous)
	if !errors.Is(err, ErrOutputContractMismatch) {
		t.Fatalf("non-retry ambiguity error = %v", err)
	}
}

func TestFromPlanRejectsDynamicAcrossWorkflowOutputAndMalformedTemplate(t *testing.T) {
	declaration := workflowOutput("review/v1", "review", false, 41, "9")
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{
		Name: "review", SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"finding": declaration},
	}})
	template, err := atc.CanonicalJSON(agentPlan("template", "review", "finding", declaration))
	if err != nil {
		t.Fatal(err)
	}
	plan := atc.Plan{ID: "across", Across: &atc.AcrossPlan{
		Vars: []atc.AcrossVar{{Var: "item", Values: []string{"one"}}}, SubStepTemplate: string(template),
	}}
	_, err = FromPlan(input, plan)
	if !errors.Is(err, ErrInvalidProvenance) || !errors.Is(err, ErrDynamicWorkflowOutput) {
		t.Fatalf("dynamic across error = %v", err)
	}

	plan.Across.SubStepTemplate = `{"id":"template","get":{"name":"source","type":"git","source":{},"image":{"check_plan":{"id":"nested","future":{"value":true}}},"version":{"ref":"abc"},"resource":"repository"}}`
	_, err = FromPlan(captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "contract"}}), plan)
	if !errors.Is(err, ErrInvalidProvenance) {
		t.Fatalf("unknown nested template arm error = %v", err)
	}
}

func TestFromPlanClassifiesExactOutputContractDisagreements(t *testing.T) {
	declaration := workflowOutput("review/v1", "review", false, 41, "9")
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{
		Name: "review", SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"finding": declaration},
	}})
	for _, test := range []struct {
		name   string
		mutate func(*atc.Plan)
	}{
		{name: "missing", mutate: func(plan *atc.Plan) { plan.Agent.SnapshotOutputs = nil }},
		{name: "extra", mutate: func(plan *atc.Plan) {
			plan.Agent.SnapshotOutputs["extra"] = workflowOutput("review/v1", "extra", false, 41, "9")
		}},
		{name: "type", mutate: func(plan *atc.Plan) {
			output := plan.Agent.SnapshotOutputs["finding"]
			output.Type = "other/v1"
			plan.Agent.SnapshotOutputs["finding"] = output
		}},
		{name: "optional", mutate: func(plan *atc.Plan) {
			output := plan.Agent.SnapshotOutputs["finding"]
			output.Optional = true
			plan.Agent.SnapshotOutputs["finding"] = output
		}},
		{name: "definition", mutate: func(plan *atc.Plan) {
			output := plan.Agent.SnapshotOutputs["finding"]
			output.WorkflowDefinitionID = 42
			plan.Agent.SnapshotOutputs["finding"] = output
		}},
		{name: "run", mutate: func(plan *atc.Plan) {
			output := plan.Agent.SnapshotOutputs["finding"]
			output.WorkflowRunID = "10"
			plan.Agent.SnapshotOutputs["finding"] = output
		}},
		{name: "kind", mutate: func(plan *atc.Plan) {
			agent := plan.Agent
			plan.Agent = nil
			plan.Task = &atc.TaskPlan{Name: agent.Name, SnapshotOutputs: agent.SnapshotOutputs, Config: immutableTaskConfig()}
		}},
		{name: "name", mutate: func(plan *atc.Plan) { plan.Agent.Name = "other" }},
		{name: "local port", mutate: func(plan *atc.Plan) {
			output := plan.Agent.SnapshotOutputs["finding"]
			delete(plan.Agent.SnapshotOutputs, "finding")
			plan.Agent.SnapshotOutputs["other"] = output
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := agentPlan("agent", "review", "finding", declaration)
			test.mutate(&plan)
			_, err := FromPlan(input, plan)
			if !errors.Is(err, ErrOutputContractMismatch) {
				t.Fatalf("error = %v, want output contract mismatch", err)
			}
		})
	}
}

func TestFromPlanDependencyOrderAndMapOrderAreStableExactDuplicatesCollapseAndContradictionsFail(t *testing.T) {
	leftSource := atc.Source{"z": "last", "a": "first"}
	rightSource := atc.Source{"a": "first", "z": "last"}
	first := resourceGetPlan("b", "second", "second-resource", "git", atc.Version{"ref": "b"}, leftSource)
	second := resourceGetPlan("a", "first", "first-resource", "registry-image", atc.Version{"digest": "a"}, rightSource)
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "contract"}})

	one, err := FromPlan(input, atc.Plan{ID: "root", Do: planList(first, second, second)})
	if err != nil {
		t.Fatalf("first capture: %v", err)
	}
	two, err := FromPlan(input, atc.Plan{ID: "root", Do: planList(second, first)})
	if err != nil {
		t.Fatalf("second capture: %v", err)
	}
	if string(one.ResolvedDependencies) != string(two.ResolvedDependencies) {
		t.Fatalf("dependency order changed bytes:\n%s\n%s", one.ResolvedDependencies, two.ResolvedDependencies)
	}
	var envelope DependencyEnvelope
	if err := json.Unmarshal(one.ResolvedDependencies, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Resources) != 2 || envelope.Resources[0].PlanID != "a" || envelope.Resources[1].PlanID != "b" {
		t.Fatalf("resources = %#v", envelope.Resources)
	}

	contradiction := second
	contradictoryGet := *second.Get
	contradictoryGet.Version = &atc.Version{"digest": "different"}
	contradiction.Get = &contradictoryGet
	_, err = FromPlan(input, atc.Plan{ID: "root", Do: planList(second, contradiction)})
	if !errors.Is(err, ErrInvalidProvenance) {
		t.Fatalf("contradiction error = %v", err)
	}
}

func TestFromPlanMapInsertionOrderDoesNotChangeCanonicalPlanOrHash(t *testing.T) {
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "contract"}})
	left := resourceGetPlan(
		"get", "source", "repository", "git", atc.Version{"ref": "abc", "tag": "v1"},
		atc.Source{"z": "last", "a": "first"},
	)
	right := resourceGetPlan(
		"get", "source", "repository", "git", atc.Version{"tag": "v1", "ref": "abc"},
		atc.Source{"a": "first", "z": "last"},
	)

	first, err := FromPlan(input, left)
	if err != nil {
		t.Fatalf("first capture: %v", err)
	}
	second, err := FromPlan(input, right)
	if err != nil {
		t.Fatalf("second capture: %v", err)
	}
	if !bytes.Equal(first.CanonicalPlan, second.CanonicalPlan) || first.PlanHash != second.PlanHash ||
		!bytes.Equal(first.ResolvedDependencies, second.ResolvedDependencies) {
		t.Fatalf("map insertion order changed provenance:\n%#v\n%#v", first, second)
	}
}

func TestFromPlanAcceptsOnlyCanonicalPutDependentGets(t *testing.T) {
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "contract"}})
	putID := atc.PlanID("put")
	source := atc.Source{"repository": "example/output"}
	put := atc.Plan{ID: putID, Put: &atc.PutPlan{
		Name: "publish", Resource: "output", Type: "git", Source: source,
		TypeImage: atc.TypeImage{BaseType: "git"},
	}}
	dependentGet := func() atc.Plan {
		return atc.Plan{ID: "dependent-get", Get: &atc.GetPlan{
			Name: "publish", Resource: "output", Type: "git", Source: source,
			VersionFrom: &putID, TypeImage: atc.TypeImage{BaseType: "git"},
		}}
	}
	putThenGet := func(next atc.Plan) atc.Plan {
		return atc.Plan{ID: "put-with-get", OnSuccess: &atc.OnSuccessPlan{Step: put, Next: next}}
	}

	captured, err := FromPlan(input, putThenGet(dependentGet()))
	if err != nil {
		t.Fatalf("canonical put dependent get: %v", err)
	}
	var dependencies DependencyEnvelope
	if err := json.Unmarshal(captured.ResolvedDependencies, &dependencies); err != nil {
		t.Fatal(err)
	}
	if len(dependencies.Resources) != 0 {
		t.Fatalf("dependent get became an external resource dependency: %#v", dependencies.Resources)
	}

	for _, test := range []struct {
		name   string
		mutate func(*atc.Plan)
	}{
		{name: "unknown put", mutate: func(plan *atc.Plan) {
			unknown := atc.PlanID("unknown")
			plan.Get.VersionFrom = &unknown
		}},
		{name: "different name", mutate: func(plan *atc.Plan) { plan.Get.Name = "other" }},
		{name: "different resource", mutate: func(plan *atc.Plan) { plan.Get.Resource = "other" }},
		{name: "different type", mutate: func(plan *atc.Plan) { plan.Get.Type = "registry-image" }},
		{name: "different source", mutate: func(plan *atc.Plan) {
			plan.Get.Source = atc.Source{"repository": "other/output"}
		}},
		{name: "also exact version", mutate: func(plan *atc.Plan) {
			version := atc.Version{"ref": "abc"}
			plan.Get.Version = &version
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := dependentGet()
			test.mutate(&next)
			_, err := FromPlan(input, putThenGet(next))
			if !errors.Is(err, ErrInvalidProvenance) {
				t.Fatalf("error = %v, want invalid provenance", err)
			}
		})
	}
}

func TestFromPlanRejectsContradictoryDuplicateImageIdentities(t *testing.T) {
	put := func(imageRef string) atc.Plan {
		return atc.Plan{ID: "put", Put: &atc.PutPlan{
			Name: "publish", Type: "custom-registry", Source: atc.Source{"repository": "example/output"},
			TypeImage: atc.TypeImage{ImageRef: imageRef},
		}}
	}
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "contract"}})
	_, err := FromPlan(input, atc.Plan{ID: "root", Do: planList(put(testDigestImage), put(secondTestDigestImage))})
	if !errors.Is(err, ErrInvalidProvenance) {
		t.Fatalf("contradictory image error = %v", err)
	}
}

func TestFromPlanRejectsMalformedCyclesFuturePlansAndMutableImages(t *testing.T) {
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "contract"}})
	for _, test := range []struct {
		name string
		plan func() atc.Plan
	}{
		{name: "missing id", plan: func() atc.Plan { return atc.Plan{Agent: &atc.AgentPlan{Name: "agent"}} }},
		{name: "missing arm", plan: func() atc.Plan { return atc.Plan{ID: "empty"} }},
		{name: "multiple arms", plan: func() atc.Plan {
			return atc.Plan{ID: "many", Agent: &atc.AgentPlan{Name: "agent"}, Task: &atc.TaskPlan{Name: "task"}}
		}},
		{name: "cycle", plan: func() atc.Plan {
			plan := resourceGetPlan("cycle", "source", "repository", "git", atc.Version{"ref": "a"}, nil)
			plan.Get.TypeImage = atc.TypeImage{BaseType: "git", GetPlan: &plan}
			return plan
		}},
		{name: "tag type image", plan: func() atc.Plan {
			plan := resourceGetPlan("get", "source", "repository", "custom", atc.Version{"ref": "a"}, nil)
			plan.Get.TypeImage = atc.TypeImage{ImageRef: "registry.example/type:latest"}
			return plan
		}},
		{name: "tag capability", plan: func() atc.Plan {
			return atc.Plan{ID: "agent", Agent: &atc.AgentPlan{Name: "agent", Sidecars: []atc.SidecarSource{{Config: &atc.SidecarConfig{Name: "dev", Image: "example/dev:latest"}}}}}
		}},
		{name: "unresolved task image", plan: func() atc.Plan {
			return atc.Plan{ID: "task", Task: &atc.TaskPlan{Name: "task", Config: &atc.TaskConfig{Platform: "linux", ImageResource: &atc.ImageResource{Type: "registry-image", Source: atc.Source{"repository": "example/task"}}}}}
		}},
		{name: "unresolved version_from", plan: func() atc.Plan {
			versionFrom := atc.PlanID("put")
			plan := resourceGetPlan("get", "source", "repository", "git", atc.Version{"ref": "ignored"}, nil)
			plan.Get.Version = nil
			plan.Get.VersionFrom = &versionFrom
			return plan
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := FromPlan(input, test.plan())
			if !errors.Is(err, ErrInvalidProvenance) {
				t.Fatalf("error = %v, want invalid provenance", err)
			}
		})
	}
}

func TestFromPlanEnforcesOutputDependencyAndByteBounds(t *testing.T) {
	t.Run("outputs", func(t *testing.T) {
		outputs := make(map[string]atc.SnapshotOutputConfig, MaxWorkflowOutputs+1)
		for index := 0; index <= MaxWorkflowOutputs; index++ {
			name := fmt.Sprintf("output-%04d", index)
			outputs[name] = workflowOutput("review/v1", name, false, 41, "9")
		}
		step := atc.Step{Config: &atc.AgentStep{Name: "agent", SnapshotOutputs: outputs}}
		_, err := FromPlan(captureInput(t, 41, 9, step), atc.Plan{ID: "agent", Agent: &atc.AgentPlan{Name: "agent", SnapshotOutputs: outputs}})
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("error = %v, want output limit", err)
		}
	})

	t.Run("dependency records", func(t *testing.T) {
		plans := make(atc.DoPlan, MaxDependencyRecords+1)
		for index := range plans {
			id := fmt.Sprintf("get-%05d", index)
			plans[index] = resourceGetPlan(id, id, "resource-"+id, "git", atc.Version{"ref": id}, nil)
		}
		_, err := FromPlan(captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "contract"}}), atc.Plan{ID: "root", Do: &plans})
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("error = %v, want dependency record limit", err)
		}
	})

	t.Run("canonical plan bytes", func(t *testing.T) {
		plan := atc.Plan{ID: "agent", Agent: &atc.AgentPlan{Name: "agent", Prompt: strings.Repeat("x", MaxCanonicalPlanBytes)}}
		_, err := FromPlan(captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "contract"}}), plan)
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("error = %v, want plan byte limit", err)
		}
	})

	t.Run("dependency bytes", func(t *testing.T) {
		const records = 2200
		plans := make(atc.DoPlan, records)
		largeVersion := strings.Repeat("v", 2000)
		for index := range plans {
			id := fmt.Sprintf("get-%04d", index)
			plans[index] = resourceGetPlan(id, id, "resource-"+id, "git", atc.Version{"ref": largeVersion + id}, nil)
		}
		_, err := FromPlan(captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "contract"}}), atc.Plan{ID: "root", Do: &plans})
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("error = %v, want dependency byte limit", err)
		}
	})
}

func TestFromPlanRepeatedCaptureIsDeterministic(t *testing.T) {
	declaration := workflowOutput("review/v1", "review", false, 41, "9")
	input := captureInput(t, 41, 9, atc.Step{Config: &atc.AgentStep{Name: "review", SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"finding": declaration}}})
	plan := agentPlan("agent", "review", "finding", declaration)
	first, err := FromPlan(input, plan)
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 100; iteration++ {
		got, err := FromPlan(input, plan)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, got) {
			t.Fatalf("iteration %d changed capture", iteration)
		}
	}
}

func captureInput(t *testing.T, definitionID int, runID snapshot.WorkflowRunID, steps ...atc.Step) Input {
	t.Helper()
	config := atc.Config{Jobs: atc.JobConfigs{{Name: "run", PlanSequence: steps}}}
	return Input{WorkflowRunID: runID, WorkflowDefinitionID: definitionID, ConcreteConfig: mustCanonical(t, config)}
}

func mustCanonical(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := atc.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func workflowOutput(typ, port string, optional bool, definitionID int, runID string) atc.SnapshotOutputConfig {
	return atc.SnapshotOutputConfig{
		Type: snapshot.TypeRef(typ), Optional: optional, Retention: snapshot.RetentionClassWorkflow,
		WorkflowPort: port, WorkflowDefinitionID: definitionID, WorkflowRunID: runID,
	}
}

func agentPlan(id, name, localPort string, declaration atc.SnapshotOutputConfig) atc.Plan {
	outputs := map[string]atc.SnapshotOutputConfig(nil)
	if localPort != "" {
		outputs = map[string]atc.SnapshotOutputConfig{localPort: declaration}
	}
	return atc.Plan{ID: atc.PlanID(id), Agent: &atc.AgentPlan{Name: name, SnapshotOutputs: outputs}}
}

func emptyAgentPlan(id string) atc.Plan {
	return atc.Plan{ID: atc.PlanID(id), Agent: &atc.AgentPlan{Name: id}}
}

func resourceGetPlan(id, name, resource, typ string, version atc.Version, source atc.Source) atc.Plan {
	versionCopy := atc.Version{}
	for key, value := range version {
		versionCopy[key] = value
	}
	return atc.Plan{ID: atc.PlanID(id), Get: &atc.GetPlan{
		Name: name, Resource: resource, Type: typ, Version: &versionCopy, Source: source,
		TypeImage: atc.TypeImage{BaseType: typ},
	}}
}

func immutableTaskConfig() *atc.TaskConfig {
	return &atc.TaskConfig{Platform: "linux", ImageResource: &atc.ImageResource{
		Type: "registry-image", Source: atc.Source{"repository": "example/task"}, Version: atc.Version{"digest": "sha256:task"},
	}}
}

func planList(plans ...atc.Plan) *atc.DoPlan {
	list := atc.DoPlan(plans)
	return &list
}

func retryPlans(plans ...atc.Plan) *atc.RetryPlan {
	list := atc.RetryPlan(plans)
	return &list
}
