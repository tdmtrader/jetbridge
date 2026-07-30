package workflow_test

import (
	"strings"
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

func TestCompileDefinitionWithNodesComposesAgentFromMappedSuccessorPorts(t *testing.T) {
	node, err := workflow.CompileNodeDefinition(workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: code-review
inputs:
  - {name: repository, type: repository/v1}
  - {name: policy, type: policy/v1, optional: true}
outputs:
  - {name: review, type: review/v1}
  - {name: summary, type: summary/v1}
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
    uses: code-review@2
    input_mapping:
      repository: checked-out-repository
    output_mapping:
      review: review-result
`}

	compiled, bindings, err := workflow.CompileDefinitionWithNodes(manifest, releasedNodeResolver{node: workflow.NodeDefinition{
		ID: 43, Name: "code-review", Version: 2, Compiled: *node,
	}})
	if err != nil {
		t.Fatalf("compile workflow: %v", err)
	}
	if len(bindings) != 1 ||
		len(bindings[0].InputMapping) != 1 || bindings[0].InputMapping["repository"] != "checked-out-repository" ||
		len(bindings[0].OutputMapping) != 1 || bindings[0].OutputMapping["review"] != "review-result" {
		t.Fatalf("bindings = %#v", bindings)
	}
	agent, ok := compiled.Function.Plan[0].Config.(*atc.AgentStep)
	if !ok {
		t.Fatalf("expanded step = %T, want *atc.AgentStep", compiled.Function.Plan[0].Config)
	}
	if len(agent.Inputs) != 1 || agent.Inputs[0] != "repository" ||
		len(agent.Outputs) != 1 || agent.Outputs[0] != "review" {
		t.Fatalf("composed agent ports = inputs %#v, outputs %#v", agent.Inputs, agent.Outputs)
	}
	if len(agent.SnapshotInputs) != 1 || agent.SnapshotInputs["repository"].Type != "repository/v1" ||
		len(agent.SnapshotOutputs) != 1 || agent.SnapshotOutputs["review"].Type != "review/v1" {
		t.Fatalf("composed agent types = inputs %#v, outputs %#v", agent.SnapshotInputs, agent.SnapshotOutputs)
	}
	if len(agent.InputMapping) != 1 || agent.InputMapping["repository"] != "checked-out-repository" ||
		len(agent.OutputMapping) != 1 || agent.OutputMapping["review"] != "review-result" {
		t.Fatalf("composed agent mappings = inputs %#v, outputs %#v", agent.InputMapping, agent.OutputMapping)
	}
	direct, err := node.Instantiate(nil)
	if err != nil {
		t.Fatalf("instantiate node directly: %v", err)
	}
	directAgent := direct.Plan[0].Config.(*atc.AgentStep)
	if len(directAgent.Inputs) != 2 || len(directAgent.Outputs) != 2 ||
		len(directAgent.SnapshotInputs) != 2 || len(directAgent.SnapshotOutputs) != 2 {
		t.Fatalf("direct agent contract was filtered: %#v", directAgent)
	}
}

func TestCompileDefinitionWithNodesComposesTaskFromMappedSuccessorPorts(t *testing.T) {
	node, err := workflow.CompileNodeDefinition(workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: code-review
inputs:
  - {name: repository, type: repository/v1}
  - {name: policy, type: policy/v1, optional: true}
outputs:
  - {name: review, type: review/v1}
  - {name: summary, type: summary/v1}
step:
  task: review
  config:
    platform: linux
    image_resource:
      type: registry-image
      source: {repository: busybox, digest: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}
    inputs:
      - {name: repository}
      - {name: policy, optional: true}
    outputs:
      - {name: review}
      - {name: summary}
    run: {path: sh, args: ["-c", "true"]}
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
    uses: code-review@2
    input_mapping:
      repository: checked-out-repository
    output_mapping:
      review: review-result
`}

	compiled, bindings, err := workflow.CompileDefinitionWithNodes(manifest, releasedNodeResolver{node: workflow.NodeDefinition{
		ID: 44, Name: "code-review", Version: 2, Compiled: *node,
	}})
	if err != nil {
		t.Fatalf("compile workflow: %v", err)
	}
	if len(bindings) != 1 ||
		len(bindings[0].InputMapping) != 1 || bindings[0].InputMapping["repository"] != "checked-out-repository" ||
		len(bindings[0].OutputMapping) != 1 || bindings[0].OutputMapping["review"] != "review-result" {
		t.Fatalf("bindings = %#v", bindings)
	}
	task, ok := compiled.Function.Plan[0].Config.(*atc.TaskStep)
	if !ok {
		t.Fatalf("expanded step = %T, want *atc.TaskStep", compiled.Function.Plan[0].Config)
	}
	if task.Config == nil ||
		len(task.Config.Inputs) != 1 || task.Config.Inputs[0].Name != "repository" ||
		len(task.Config.Outputs) != 1 || task.Config.Outputs[0].Name != "review" {
		t.Fatalf("composed task config = %#v", task.Config)
	}
	if len(task.SnapshotInputs) != 1 || task.SnapshotInputs["checked-out-repository"].Type != "repository/v1" ||
		len(task.SnapshotOutputs) != 1 || task.SnapshotOutputs["review-result"].Type != "review/v1" {
		t.Fatalf("composed task types = inputs %#v, outputs %#v", task.SnapshotInputs, task.SnapshotOutputs)
	}
	if len(task.InputMapping) != 1 || task.InputMapping["repository"] != "checked-out-repository" ||
		len(task.OutputMapping) != 1 || task.OutputMapping["review"] != "review-result" {
		t.Fatalf("composed task mappings = inputs %#v, outputs %#v", task.InputMapping, task.OutputMapping)
	}
	direct, err := node.Instantiate(nil)
	if err != nil {
		t.Fatalf("instantiate node directly: %v", err)
	}
	directTask := direct.Plan[0].Config.(*atc.TaskStep)
	if len(directTask.Config.Inputs) != 2 || len(directTask.Config.Outputs) != 2 ||
		len(directTask.SnapshotInputs) != 2 || len(directTask.SnapshotOutputs) != 2 {
		t.Fatalf("direct task contract was filtered: %#v", directTask)
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
		"missing node": `- uses: code-review@5
  input_mapping: {repository: repository}
  output_mapping: {review: review}`,
		"missing uses": `- node: review
  input_mapping: {repository: repository}
  output_mapping: {review: review}`,
		"latest": `- node: review
  uses: code-review@latest
  input_mapping: {repository: repository}
  output_mapping: {review: review}`,
		"missing mapping": `- node: review
  uses: code-review@5
  input_mapping: {}
  output_mapping: {review: review}`,
		"unreleased version": `- node: review
  uses: code-review@6
  input_mapping: {repository: repository}
  output_mapping: {review: review}`,
		"undeclared parameter": `- node: review
  uses: code-review@5
  input_mapping: {repository: repository}
  output_mapping: {review: review}
  params: {NOT_DECLARED: value}`,
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

func TestCompileDefinitionWithNodesRejectsNonInjectiveMappings(t *testing.T) {
	node, err := workflow.CompileNodeDefinition(workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: compare
inputs:
  - {name: left, type: repository/v1}
  - {name: right, type: repository/v1}
outputs: []
step: {agent: compare, prompt: compare}`})
	if err != nil {
		t.Fatal(err)
	}
	manifest := workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: consumer
signature_version: 1
inputs: [{name: repository, type: repository/v1}]
outputs: []
plan:
  - node: compare
    uses: compare@1
    input_mapping: {left: repository, right: repository}
    output_mapping: {}
`}
	if _, _, err := workflow.CompileDefinitionWithNodes(manifest, releasedNodeResolver{node: workflow.NodeDefinition{Name: "compare", Version: 1, Compiled: *node}}); err == nil {
		t.Fatal("expected non-injective mapping rejection")
	}
}

func TestCompileDefinitionWithNodesPreservesStepModifiersAndExpandsNestedVisibleLeaves(t *testing.T) {
	node, err := workflow.CompileNodeDefinition(workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: review
inputs: []
outputs: []
step: {agent: review, prompt: review}`})
	if err != nil {
		t.Fatal(err)
	}
	resolver := releasedNodeResolver{node: workflow.NodeDefinition{ID: 1, Name: "review", Version: 1, Compiled: *node}}
	ref := `node: review-change
uses: review@1
input_mapping: {}
output_mapping: {}`
	indent := func(value, prefix string) string { return strings.ReplaceAll(value, "\n", "\n"+prefix) }
	for name, plan := range map[string]string{
		"direct with attempts": "- attempts: 2\n  " + indent(ref, "  "),
		"direct with timeout":  "- timeout: 1m\n  " + indent(ref, "  "),
		"nested try":           "- try:\n    " + indent(ref, "    "),
		"nested do":            "- do:\n    - " + indent(ref, "      "),
		"nested parallel":      "- in_parallel:\n    - " + indent(ref, "      "),
		"success hook":         "- " + indent(ref, "  ") + "\n  on_success:\n    " + indent(strings.Replace(ref, "review-change", "after-review", 1), "    "),
		"failure hook":         "- " + indent(ref, "  ") + "\n  on_failure:\n    " + indent(strings.Replace(ref, "review-change", "after-review", 1), "    "),
		"abort hook":           "- " + indent(ref, "  ") + "\n  on_abort:\n    " + indent(strings.Replace(ref, "review-change", "after-review", 1), "    "),
		"error hook":           "- " + indent(ref, "  ") + "\n  on_error:\n    " + indent(strings.Replace(ref, "review-change", "after-review", 1), "    "),
		"ensure hook":          "- " + indent(ref, "  ") + "\n  ensure:\n    " + indent(strings.Replace(ref, "review-change", "after-review", 1), "    "),
	} {
		t.Run(name, func(t *testing.T) {
			manifest := workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: consumer
signature_version: 1
inputs: []
outputs: []
plan:
` + plan + "\n"}
			compiled, bindings, err := workflow.CompileDefinitionWithNodes(manifest, resolver)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if len(compiled.Function.Plan) != 1 {
				t.Fatalf("plan = %#v", compiled.Function.Plan)
			}
			wantBindings := 1
			if strings.Contains(name, "hook") {
				wantBindings = 2
			}
			if len(bindings) != wantBindings {
				t.Fatalf("bindings = %#v", bindings)
			}
		})
	}
}
