package atc_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"sigs.k8s.io/yaml"
)

func TestPublishSnapshotStepWireContractIsStrictAndVisible(t *testing.T) {
	raw := []byte(`
publish_snapshot: publish-change
publisher: git-publisher/v1
input: change
input_type: repository-change/v1
destination: github.example/team/repo
mode: branch
parameters:
  source_branch: agent/change
  target_branch: main
approval_policy_version: engineering/v2
`)
	var step atc.Step
	if err := yaml.Unmarshal(raw, &step); err != nil {
		t.Fatal(err)
	}
	want := &atc.PublishSnapshotStep{
		Name: "publish-change", Publisher: publisher.GitPublisher,
		Input: "change", InputType: snapshot.TypeRef("repository-change/v1"),
		Destination: "github.example/team/repo", Mode: publisher.ModeBranch,
		Parameters:            map[string]string{"source_branch": "agent/change", "target_branch": "main"},
		ApprovalPolicyVersion: "engineering/v2",
	}
	got, ok := step.Config.(*atc.PublishSnapshotStep)
	if !ok || !equalPublishSnapshotStep(got, want) {
		t.Fatalf("step = %#v, want %#v", step.Config, want)
	}
	payload, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	var replay atc.Step
	if err := json.Unmarshal(payload, &replay); err != nil {
		t.Fatal(err)
	}
	got, ok = replay.Config.(*atc.PublishSnapshotStep)
	if !ok || !equalPublishSnapshotStep(got, want) {
		t.Fatalf("round trip = %#v, want %#v", replay.Config, want)
	}
}

func TestPublishSnapshotStepRejectsMalformedOrUnsafeDeclarations(t *testing.T) {
	valid := map[string]any{
		"publish_snapshot": "publish-change", "publisher": "git-publisher/v1",
		"input": "change", "input_type": "repository-change/v1",
		"destination": "github.example/team/repo", "mode": "branch",
		"parameters":              map[string]string{"source_branch": "agent/change", "target_branch": "main"},
		"approval_policy_version": "engineering/v2",
	}
	tests := map[string]func(map[string]any){
		"missing name":        func(value map[string]any) { value["publish_snapshot"] = "" },
		"missing input":       func(value map[string]any) { value["input"] = "" },
		"wrong git type":      func(value map[string]any) { value["input_type"] = "review/v1" },
		"unsupported mode":    func(value map[string]any) { value["mode"] = "comment" },
		"missing destination": func(value map[string]any) { value["destination"] = "" },
		"missing policy":      func(value map[string]any) { value["approval_policy_version"] = "" },
		"unknown field":       func(value map[string]any) { value["credential"] = "secret" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := clonePublishMap(valid)
			mutate(candidate)
			payload, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			var step atc.PublishSnapshotStep
			if err := json.Unmarshal(payload, &step); err == nil {
				t.Fatalf("invalid declaration accepted: %s", payload)
			}
		})
	}
}

func TestPublishSnapshotMergeWireCarriesOnlyArtifactAndRendererRunIdentity(t *testing.T) {
	raw := []byte(`
publish_snapshot: merge-change
publisher: git-publisher/v1
input: change
input_type: repository-change/v1
destination: github.example/team/repo
mode: merge
parameters:
  target_branch: main
approval_policy_version: engineering/v2
approval: merge-approval
workflow_run_id: ((workflow_run_id))
`)
	var step atc.Step
	if err := yaml.Unmarshal(raw, &step); err != nil {
		t.Fatal(err)
	}
	merge, ok := step.Config.(*atc.PublishSnapshotStep)
	if !ok || merge.Approval != "merge-approval" || merge.WorkflowRunID != "((workflow_run_id))" {
		t.Fatalf("merge step = %#v", step.Config)
	}
	payload, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) == "" || json.Valid(payload) == false {
		t.Fatalf("invalid merge JSON: %s", payload)
	}
	for _, forbidden := range []string{"approved_by", "resolved_by", "approval_wait_id"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("authored merge leaked server-owned %q: %s", forbidden, payload)
		}
	}

	invalid := map[string]any{
		"publish_snapshot": "merge-change", "publisher": "git-publisher/v1",
		"input": "change", "input_type": "repository-change/v1", "destination": "github.example/team/repo",
		"mode": "merge", "parameters": map[string]string{"target_branch": "main"},
		"approval_policy_version": "engineering/v2", "approval": "merge-approval",
	}
	for name, mutate := range map[string]func(map[string]any){
		"missing approval": func(value map[string]any) { delete(value, "approval") },
		"bad run template": func(value map[string]any) { value["workflow_run_id"] = "((build_id))" },
		"nonmerge approval": func(value map[string]any) {
			value["mode"] = "branch"
			value["parameters"] = map[string]string{"source_branch": "agent/change", "target_branch": "main"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := clonePublishMap(invalid)
			mutate(candidate)
			encoded, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			var parsed atc.PublishSnapshotStep
			if err := json.Unmarshal(encoded, &parsed); err == nil {
				t.Fatalf("invalid merge declaration accepted: %s", encoded)
			}
		})
	}
}

func equalPublishSnapshotStep(left, right *atc.PublishSnapshotStep) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func clonePublishMap(source map[string]any) map[string]any {
	copy := make(map[string]any, len(source))
	for key, value := range source {
		if parameters, ok := value.(map[string]string); ok {
			cloned := make(map[string]string, len(parameters))
			for name, parameter := range parameters {
				cloned[name] = parameter
			}
			copy[key] = cloned
			continue
		}
		copy[key] = value
	}
	return copy
}
