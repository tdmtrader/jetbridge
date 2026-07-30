package workflow_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

func TestCompiledNodeDefinitionInstantiate(t *testing.T) {
	medium := "medium"
	node := workflow.CompiledNodeDefinition{
		SchemaVersion: 1,
		Name:          "code-review",
		Parameters: []workflow.NodeParameter{
			{Name: "MINIMUM_SEVERITY", Default: &medium},
		},
		Function: workflow.FunctionConfig{
			SignatureVersion: 1,
			Inputs:           []snapshot.Port{{Name: "repository", Type: "repository/v1"}},
			Outputs:          []workflow.FunctionOutput{{Port: snapshot.Port{Name: "review", Type: "review/v1"}, From: "review"}},
			Plan:             []atc.Step{{Config: &atc.AgentStep{Name: "review", Prompt: "review"}}},
		},
	}

	function, err := node.Instantiate(map[string]string{"MINIMUM_SEVERITY": "high"})
	if err != nil {
		t.Fatal(err)
	}
	agent := function.Plan[0].Config.(*atc.AgentStep)
	if agent.Env["MINIMUM_SEVERITY"] != "high" {
		t.Fatalf("env = %#v", agent.Env)
	}
	agent.Env["MUTATED"] = "true"
	if node.Function.Plan[0].Config.(*atc.AgentStep).Env["MUTATED"] != "" {
		t.Fatal("instantiation retained the source environment map")
	}
}

func TestCompiledNodeDefinitionInstantiateAppliesDefaultsToTaskParams(t *testing.T) {
	defaultValue := "default"
	node := workflow.CompiledNodeDefinition{
		SchemaVersion: 1,
		Name:          "task",
		Parameters:    []workflow.NodeParameter{{Name: "MODE", Default: &defaultValue}},
		Function: workflow.FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.TaskStep{
			Name:   "task",
			Params: atc.TaskEnv{"EXISTING": "preserved"},
			Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "/bin/true"}},
		}}}},
	}

	function, err := node.Instantiate(nil)
	if err != nil {
		t.Fatal(err)
	}
	task := function.Plan[0].Config.(*atc.TaskStep)
	if task.Params["MODE"] != defaultValue || task.Params["EXISTING"] != "preserved" {
		t.Fatalf("params = %#v", task.Params)
	}
}

func TestCompiledNodeDefinitionRejectsInvalidParameters(t *testing.T) {
	required := workflow.NodeParameter{Name: "REQUIRED"}
	defaultValue := "default"
	validFunction := workflow.FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.AgentStep{Name: "agent", Prompt: "work"}}}}

	for name, test := range map[string]struct {
		node   workflow.CompiledNodeDefinition
		values map[string]string
		want   string
	}{
		"blank declaration": {
			node: workflow.CompiledNodeDefinition{SchemaVersion: 1, Name: "node", Parameters: []workflow.NodeParameter{{Name: " "}}, Function: validFunction},
			want: "parameter name is required",
		},
		"duplicate declaration": {
			node: workflow.CompiledNodeDefinition{SchemaVersion: 1, Name: "node", Parameters: []workflow.NodeParameter{{Name: "MODE"}, {Name: "MODE"}}, Function: validFunction},
			want: "duplicate node parameter",
		},
		"missing required": {
			node: workflow.CompiledNodeDefinition{SchemaVersion: 1, Name: "node", Parameters: []workflow.NodeParameter{required}, Function: validFunction},
			want: "parameter \"REQUIRED\" is required",
		},
		"unknown supplied": {
			node:   workflow.CompiledNodeDefinition{SchemaVersion: 1, Name: "node", Parameters: []workflow.NodeParameter{{Name: "MODE", Default: &defaultValue}}, Function: validFunction},
			values: map[string]string{"UNKNOWN": "value"},
			want:   "unknown parameter",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := test.node.Instantiate(test.values)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Instantiate error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCompiledNodeDefinitionRejectsPublishSnapshotParameters(t *testing.T) {
	node := workflow.CompiledNodeDefinition{
		SchemaVersion: 1,
		Name:          "publish",
		Parameters:    []workflow.NodeParameter{{Name: "TARGET"}},
		Function: workflow.FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.PublishSnapshotStep{
			Name:                  "publish",
			Publisher:             "git-publisher/v1",
			Input:                 "change",
			InputType:             "repository-change/v1",
			Destination:           "github.com/acme/project",
			Mode:                  "pull-request",
			Parameters:            map[string]string{"source_branch": "codex/node", "target_branch": "main"},
			ApprovalPolicyVersion: "engineering/v1",
		}}}},
	}
	if _, err := node.Instantiate(map[string]string{"TARGET": "other"}); err == nil || !strings.Contains(err.Error(), "publish_snapshot") {
		t.Fatalf("Instantiate error = %v, want publication parameter rejection", err)
	}
}

func TestCompiledNodeDefinitionInstantiatePreservesPublishSnapshotParameters(t *testing.T) {
	node := workflow.CompiledNodeDefinition{
		SchemaVersion: 1,
		Name:          "publish",
		Function: workflow.FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.PublishSnapshotStep{
			Name:                  "publish",
			Publisher:             "git-publisher/v1",
			Input:                 "change",
			InputType:             "repository-change/v1",
			Destination:           "github.com/acme/project",
			Mode:                  "pull-request",
			Parameters:            map[string]string{"source_branch": "codex/node", "target_branch": "main"},
			ApprovalPolicyVersion: "engineering/v1",
		}}}},
	}

	function, err := node.Instantiate(nil)
	if err != nil {
		t.Fatal(err)
	}
	publish := function.Plan[0].Config.(*atc.PublishSnapshotStep)
	publish.Parameters["target_branch"] = "changed"
	original := node.Function.Plan[0].Config.(*atc.PublishSnapshotStep)
	if original.Parameters["target_branch"] != "main" {
		t.Fatalf("source publication parameters mutated: %#v", original.Parameters)
	}
}
