package workflow_test

import (
	"testing"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

type releasedNodeResolver struct {
	node workflow.NodeDefinition
}

func (resolver releasedNodeResolver) Released(name string, version int) (workflow.NodeDefinition, bool, error) {
	if name != resolver.node.Name || version != resolver.node.Version {
		return workflow.NodeDefinition{}, false, nil
	}
	return resolver.node, true, nil
}

func TestCompileDefinitionWithNodesExpandsExactReleasedAgentReference(t *testing.T) {
	node, err := workflow.CompileNodeDefinition(workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: code-review
inputs:
  - {name: repository, type: repository/v1}
outputs:
  - {name: review, type: review/v1}
parameters:
  - {name: MINIMUM_SEVERITY, default: medium}
step:
  agent: review
  prompt: review the repository
`})
	if err != nil {
		t.Fatalf("compile node: %v", err)
	}

	manifest := workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: consumer
signature_version: 1
inputs:
  - {name: checked-out-repository, type: repository/v1}
outputs:
  - {name: review-result, type: review/v1, from: review-result}
plan:
  - node: review-change
    uses: code-review@5
    input_mapping:
      repository: checked-out-repository
    output_mapping:
      review: review-result
    params:
      MINIMUM_SEVERITY: high
`}

	compiled, bindings, err := workflow.CompileDefinitionWithNodes(manifest, releasedNodeResolver{node: workflow.NodeDefinition{
		ID: 42, Name: "code-review", Version: 5, ContentHash: "exact-content", Compiled: *node,
	}})
	if err != nil {
		t.Fatalf("compile workflow: %v", err)
	}
	if len(bindings) != 1 || bindings[0].InstanceName != "review-change" || bindings[0].NodeDefinitionID != 42 || bindings[0].Parameters["MINIMUM_SEVERITY"] != "high" {
		t.Fatalf("bindings = %#v", bindings)
	}
	agent, ok := compiled.Function.Plan[0].Config.(*atc.AgentStep)
	if !ok {
		t.Fatalf("expanded step = %T, want *atc.AgentStep", compiled.Function.Plan[0].Config)
	}
	if agent.FunctionID != "review-change" || agent.InputMapping["repository"] != "checked-out-repository" || agent.OutputMapping["review"] != "review-result" || agent.Env["MINIMUM_SEVERITY"] != "high" {
		t.Fatalf("expanded agent = %#v", agent)
	}
}

func TestCompileDefinitionWithNodesRejectsNonExactOrIncompleteReferences(t *testing.T) {
	node, err := workflow.CompileNodeDefinition(workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: code-review
inputs: [{name: repository, type: repository/v1}]
outputs: [{name: review, type: review/v1}]
step: {agent: review, prompt: review}`})
	if err != nil {
		t.Fatal(err)
	}
	resolver := releasedNodeResolver{node: workflow.NodeDefinition{ID: 1, Name: "code-review", Version: 5, Compiled: *node}}
	for name, plan := range map[string]string{
		"latest": `- node: review
  uses: code-review@latest
  input_mapping: {repository: repository}
  output_mapping: {review: review}`,
		"missing mapping": `- node: review
  uses: code-review@5
  input_mapping: {}
  output_mapping: {review: review}`,
		"implementation override": `- node: review
  uses: code-review@5
  input_mapping: {repository: repository}
  output_mapping: {review: review}
  prompt: override`,
	} {
		t.Run(name, func(t *testing.T) {
			manifest := workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: consumer
signature_version: 1
inputs: [{name: repository, type: repository/v1}]
outputs: [{name: review, type: review/v1, from: review}]
plan:
` + plan + "\n"}
			if _, _, err := workflow.CompileDefinitionWithNodes(manifest, resolver); err == nil {
				t.Fatal("expected node reference error")
			}
		})
	}
}

func TestCompileDefinitionWithNodesKeepsFrozenNodeSkillFilesOutsideWorkflowManifest(t *testing.T) {
	node, err := workflow.CompileNodeDefinition(workflow.Manifest{
		workflow.NodeFileName: `schema_version: 1
name: code-review
inputs: []
outputs: []
step:
  agent: review
  prompt: review
  skills: [review]
`,
		"skills/review/SKILL.md": "frozen node skill",
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: consumer
signature_version: 1
inputs: []
outputs: []
plan:
  - node: review
    uses: code-review@5
    input_mapping: {}
    output_mapping: {}
`}
	compiled, _, err := workflow.CompileDefinitionWithNodes(manifest, releasedNodeResolver{node: workflow.NodeDefinition{ID: 1, Name: "code-review", Version: 5, Compiled: *node}})
	if err != nil {
		t.Fatal(err)
	}
	if got := compiled.Function.SkillFiles["skills/review/SKILL.md"]; got != "frozen node skill" {
		t.Fatalf("frozen skill = %q", got)
	}
}
