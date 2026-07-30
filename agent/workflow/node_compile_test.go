package workflow_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

const nodeSource = `schema_version: 1
name: code-review
description: Review one repository change
inputs:
  - {name: repository, type: repository/v1}
  - {name: change, type: repository-change/v1}
outputs:
  - {name: review, type: review/v1}
parameters:
  - {name: MINIMUM_SEVERITY, default: medium}
capabilities:
  dev-mcp:
    contract: dev-mcp/v1
    sidecar:
      name: dev-mcp
      image: registry.example/dev-mcp@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      command: [/usr/local/bin/dev-mcp]
      ports: [{containerPort: 8080, protocol: TCP}]
step:
  agent: review
  prompt_file: prompts/review.md
  model: claude-sonnet
  skills: [review]
  capabilities: [dev-mcp]
`

func nodeManifest(source string) workflow.Manifest {
	return workflow.Manifest{
		workflow.NodeFileName:          source,
		"prompts/review.md":            "Review the supplied change.",
		"skills/review/SKILL.md":       "review skill",
		"skills/review/guide.md":       "guide",
		"skills/not-selected/SKILL.md": "unused",
	}
}

func TestCompileNodeDefinitionFreezesAssetsAndCapabilities(t *testing.T) {
	definition, err := workflow.CompileNodeDefinition(nodeManifest(nodeSource))
	if err != nil {
		t.Fatalf("CompileNodeDefinition: %v", err)
	}
	if definition.SchemaVersion != 1 || definition.Name != "code-review" || len(definition.Parameters) != 1 || definition.Parameters[0].Name != "MINIMUM_SEVERITY" {
		t.Fatalf("definition envelope = %+v", definition)
	}
	if len(definition.Function.Plan) != 1 {
		t.Fatalf("plan length = %d, want 1", len(definition.Function.Plan))
	}
	agent, ok := definition.Function.Plan[0].Config.(*atc.AgentStep)
	if !ok {
		t.Fatalf("step = %T, want *atc.AgentStep", definition.Function.Plan[0].Config)
	}
	if agent.Prompt != "Review the supplied change." || len(agent.Sidecars) != 1 || agent.Sidecars[0].Config == nil || agent.Sidecars[0].Config.Image != "registry.example/dev-mcp@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || len(agent.Sidecars[0].Config.Command) != 1 || agent.Sidecars[0].Config.Command[0] != "/usr/local/bin/dev-mcp" {
		t.Fatalf("compiled agent = %+v", agent)
	}
	if agent.Capabilities != nil {
		t.Fatalf("source capability names retained: %#v", agent.Capabilities)
	}
	if len(agent.Sidecars[0].Config.Ports) != 1 || agent.Sidecars[0].Config.Ports[0].ContainerPort != 8080 {
		t.Fatalf("sidecar ports = %+v", agent.Sidecars[0].Config.Ports)
	}
	if got := definition.Function.SkillFiles; got["skills/review/SKILL.md"] != "review skill" || got["skills/review/guide.md"] != "guide" || got["skills/not-selected/SKILL.md"] != "" {
		t.Fatalf("skill files = %#v", got)
	}
	if definition.Function.Capabilities != nil {
		t.Fatalf("source capability catalog retained: %#v", definition.Function.Capabilities)
	}
}

func TestCompileNodeDefinitionRejectsNonAtomicOrUnsafeSource(t *testing.T) {
	for name, test := range map[string]struct {
		mutate func(string) string
		want   string
	}{
		"do": {func(source string) string {
			return strings.Replace(source, "step:\n  agent: review", "step:\n  do:\n    - agent: review", 1)
		}, "atomic leaf"},
		"in parallel": {func(source string) string {
			return strings.Replace(source, "step:\n  agent: review", "step:\n  in_parallel:\n    - agent: review", 1)
		}, "atomic leaf"},
		"retry": {func(source string) string {
			return strings.Replace(source, "step:\n  agent: review", "step:\n  attempts: 2\n  agent: review", 1)
		}, "atomic leaf"},
		"hook": {func(source string) string {
			return strings.Replace(source, "  model: claude-sonnet", "  model: claude-sonnet\n  on_success: {agent: follow-up, prompt: nope}", 1)
		}, "atomic leaf"},
		"await snapshot": {func(source string) string {
			return strings.Replace(source, "  agent: review", "  await_snapshot: answer", 1)
		}, "permitted leaf"},
		"get":      {func(source string) string { return strings.Replace(source, "  agent: review", "  get: repository", 1) }, "permitted leaf"},
		"load var": {func(source string) string { return strings.Replace(source, "  agent: review", "  load_var: value", 1) }, "permitted leaf"},
		"resources": {func(source string) string {
			return strings.Replace(source, "step:", "resources: [{name: source, type: git, source: {uri: https://example.invalid/repo}}]\nstep:", 1)
		}, "unknown field"},
		"resource sources": {func(source string) string {
			return strings.Replace(source, "step:", "resource_sources: [{name: source, resource: source}]\nstep:", 1)
		}, "unknown field"},
		"two leaves": {func(source string) string {
			return strings.Replace(source, "  agent: review", "  agent: review\n  task: other", 1)
		}, "atomic leaf"},
		"mutable image": {func(source string) string {
			return strings.Replace(source, "@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ":latest", 1)
		}, "digest"},
		"undeclared asset": {func(source string) string {
			return strings.Replace(source, "prompts/review.md", "prompts/missing.md", 1)
		}, "not in the manifest"},
		"unknown source field": {func(source string) string { return source + "unexpected: value\n" }, "unknown field"},
	} {
		t.Run(name, func(t *testing.T) {
			definition, err := workflow.CompileNodeDefinition(nodeManifest(test.mutate(nodeSource)))
			if definition != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CompileNodeDefinition = (%+v, %v), want error containing %q", definition, err, test.want)
			}
		})
	}
}

func TestParseNodeDefinitionRejectsWrongSchemaAndMissingNodeSource(t *testing.T) {
	for name, manifest := range map[string]workflow.Manifest{
		"wrong schema": {workflow.NodeFileName: strings.Replace(nodeSource, "schema_version: 1", "schema_version: 2", 1)},
		"missing node": {"README.md": "node source"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := workflow.CompileNodeDefinition(manifest); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
