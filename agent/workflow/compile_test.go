package workflow_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

func TestCompileDefinitionRejectsLegacyBeforeContentOrAssetValidation(t *testing.T) {
	tests := []struct {
		name     string
		version  int
		manifest workflow.Manifest
	}{
		{
			name:    "schema 1 before invalid legacy content",
			version: 1,
			manifest: workflow.Manifest{"workflow.yml": `schema_version: 1
name: legacy-invalid
steps: []
`},
		},
		{
			name:    "schema 2 before missing prompt asset",
			version: 2,
			manifest: workflow.Manifest{"workflow.yml": `schema_version: 2
name: legacy-assets
prompt_files:
  work: prompts/missing.md
steps:
  - agent: work
    prompt: work
    outputs: [workspace]
`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition, err := workflow.CompileDefinition(test.manifest)
			if definition != nil {
				t.Fatalf("definition = %+v, want nil", definition)
			}
			var unsupported workflow.UnsupportedSchemaVersionError
			if !errors.As(err, &unsupported) {
				t.Fatalf("error = %T %v, want UnsupportedSchemaVersionError", err, err)
			}
			if unsupported.Got != test.version {
				t.Fatalf("Got = %d, want %d", unsupported.Got, test.version)
			}
			want := fmt.Sprintf(
				"workflow: unsupported schema_version %d; only schema_version 3 is supported",
				test.version,
			)
			if err.Error() != want {
				t.Fatalf("error = %q, want %q", err, want)
			}
		})
	}
}

func TestCompileDefinitionManifestValidationPrecedesSchemaInspection(t *testing.T) {
	for _, test := range []struct {
		name     string
		manifest workflow.Manifest
		want     string
	}{
		{name: "empty", manifest: workflow.Manifest{}, want: "workflow: manifest has no files"},
		{
			name:     "missing workflow",
			manifest: workflow.Manifest{"README.md": "source only"},
			want:     "workflow: manifest has no workflow.yaml (or legacy workflow.yml)",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition, err := workflow.CompileDefinition(test.manifest)
			if definition != nil || err == nil {
				t.Fatalf("CompileDefinition = (%+v, %v), want nil definition and error", definition, err)
			}
			if err.Error() != test.want {
				t.Fatalf("error = %q, want %q", err, test.want)
			}
			var unsupported workflow.UnsupportedSchemaVersionError
			if errors.As(err, &unsupported) {
				t.Fatalf("error = %T %v, must not be UnsupportedSchemaVersionError", err, err)
			}
		})
	}

	malformedV3 := workflow.Manifest{"workflow.yml": `schema_version: 3
name: malformed-v3
signature_version: 1
inputs: []
outputs: []
plan: []
`}
	definition, err := workflow.CompileDefinition(malformedV3)
	if definition != nil || err == nil {
		t.Fatalf("CompileDefinition malformed v3 = (%+v, %v), want nil definition and error", definition, err)
	}
	var unsupported workflow.UnsupportedSchemaVersionError
	if errors.As(err, &unsupported) {
		t.Fatalf("malformed v3 error = %T %v, must not be UnsupportedSchemaVersionError", err, err)
	}
}

func TestCompileDefinitionAcceptsWorkflowYAMLAndLegacyYML(t *testing.T) {
	source := v3CompileSource(`
  - agent: literal
    prompt: hi`, "")

	preferred := workflow.Manifest{workflow.WorkflowFileName: source}
	if _, err := workflow.CompileDefinition(preferred); err != nil {
		t.Fatalf("CompileDefinition(%s only): %v", workflow.WorkflowFileName, err)
	}

	legacy := workflow.Manifest{workflow.LegacyWorkflowFileName: source}
	if _, err := workflow.CompileDefinition(legacy); err != nil {
		t.Fatalf("CompileDefinition(%s only): %v", workflow.LegacyWorkflowFileName, err)
	}

	// workflow.yaml takes precedence when both keys are present.
	both := workflow.Manifest{
		workflow.WorkflowFileName: source,
		workflow.LegacyWorkflowFileName: v3CompileSource(`
  - agent: literal
    prompt: ignored`, ""),
	}
	definition, err := workflow.CompileDefinition(both)
	if err != nil {
		t.Fatalf("CompileDefinition(both keys): %v", err)
	}
	agent := definition.Function.Plan[0].Config.(*atc.AgentStep)
	if agent.Prompt != "hi" {
		t.Fatalf("%s did not take precedence over legacy %s: prompt = %q", workflow.WorkflowFileName, workflow.LegacyWorkflowFileName, agent.Prompt)
	}
}

func TestCompileV3ResolvesAgentAssets(t *testing.T) {
	manifest := workflow.Manifest{
		"workflow.yml": `schema_version: 3
name: asset-compiler
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: implement
    function_id: implement
    prompt_file: prompts/implement.md
    system_prompt_file: prompts/system.md
    context_files: [context/conventions.md, context/testing.md]
`,
		"prompts/implement.md":   "implement exactly\n",
		"prompts/system.md":      "system exactly\r\n",
		"context/conventions.md": "conventions",
		"context/testing.md":     "testing\n",
	}

	definition, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}
	agent := definition.Function.Plan[0].Config.(*atc.AgentStep)
	if agent.Prompt != "implement exactly\n" {
		t.Fatalf("prompt = %q", agent.Prompt)
	}
	if agent.SystemPrompt != "system exactly\r\n" {
		t.Fatalf("system prompt = %q", agent.SystemPrompt)
	}
	wantContext := "## context/conventions.md\n\nconventions\n\n## context/testing.md\n\ntesting\n\n\n"
	if agent.Context != wantContext {
		t.Fatalf("context = %q, want %q", agent.Context, wantContext)
	}
	if agent.PromptFile != "" || agent.SystemPromptFile != "" || len(agent.ContextFiles) != 0 {
		t.Fatalf("author-only fields were not erased: %+v", agent)
	}

	manifest["prompts/implement.md"] = "mutated prompt"
	manifest["prompts/system.md"] = "mutated system"
	manifest["context/conventions.md"] = "mutated context"
	if agent.Prompt != "implement exactly\n" || agent.SystemPrompt != "system exactly\r\n" || agent.Context != wantContext {
		t.Fatalf("compiled assets retained a runtime manifest dependency: %+v", agent)
	}
}

func TestCompileV3InlineAssetsRemainLiteral(t *testing.T) {
	prompt := "{{.Ticket.Title}}\n((prompt-var))"
	system := "{{if .Spec}}not rendered{{end}}\n((system-var))"
	manifest := workflow.Manifest{"workflow.yml": v3CompileSource(`
  - agent: literal
    prompt: |-
      {{.Ticket.Title}}
      ((prompt-var))
    system_prompt: |-
      {{if .Spec}}not rendered{{end}}
      ((system-var))`, "")}
	definition, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}
	agent := definition.Function.Plan[0].Config.(*atc.AgentStep)
	if agent.Prompt != prompt || agent.SystemPrompt != system {
		t.Fatalf("inline assets changed: prompt=%q system=%q", agent.Prompt, agent.SystemPrompt)
	}
}

func TestCompileV3CollectsWholeSelectedSkillTrees(t *testing.T) {
	manifest := workflow.Manifest{
		"workflow.yml": `schema_version: 3
name: skill-compiler
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: implement
    function_id: implement
    prompt: implement
    skills: [review, testing]
`,
		"skills/review/SKILL.md":  "review root",
		"skills/review/refs/z.md": "review z",
		"skills/review/refs/a.md": "review a",
		"skills/testing/SKILL.md": "testing root",
		"skills/unused/SKILL.md":  "unused",
		"README.md":               "unreferenced",
	}

	definition, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}
	want := map[string]string{
		"skills/review/SKILL.md":  "review root",
		"skills/review/refs/a.md": "review a",
		"skills/review/refs/z.md": "review z",
		"skills/testing/SKILL.md": "testing root",
	}
	if !reflect.DeepEqual(definition.Function.SkillFiles, want) {
		t.Fatalf("skill files = %#v, want %#v", definition.Function.SkillFiles, want)
	}
	agent := definition.Function.Plan[0].Config.(*atc.AgentStep)
	if !reflect.DeepEqual(agent.Skills, []string{"review", "testing"}) {
		t.Fatalf("skill order = %v", agent.Skills)
	}
	// Each executable agent needs its exact frozen leaf. A global union is
	// insufficient authority because it would let this agent receive skills it
	// did not select.
	if !reflect.DeepEqual(agent.SkillFiles, want) {
		t.Fatalf("agent skill files = %#v, want %#v", agent.SkillFiles, want)
	}

	manifest["skills/review/SKILL.md"] = "mutated"
	delete(manifest, "skills/testing/SKILL.md")
	if definition.Function.SkillFiles["skills/review/SKILL.md"] != "review root" ||
		definition.Function.SkillFiles["skills/testing/SKILL.md"] != "testing root" {
		t.Fatalf("compiled skills retained a runtime manifest dependency: %#v", definition.Function.SkillFiles)
	}
}

func TestCompileV3ExpandsCapabilitiesInAuthoredOrderAndDeepCopies(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	manifest := workflow.Manifest{"workflow.yml": `schema_version: 3
name: capability-compiler
signature_version: 1
inputs: []
outputs: []
capabilities:
  files:
    contract: acme.files/v3
    sidecar:
      name: file-tools
      image: registry.example/acme/files@sha256:` + digest + `
      command: [serve]
      args: [--stdio]
      env: [{name: MODE, value: review}]
      ports: [{containerPort: 8080, protocol: TCP}]
      resources:
        requests: {cpu: 100m, memory: 64Mi}
        limits: {cpu: "1", memory: 1Gi}
      workingDir: /workspace
  dev:
    contract: dev-mcp/v1
    sidecar:
      name: dev-mcp
      image: registry.example/acme/dev@sha256:` + digest + `
      ports: [{containerPort: 7780, protocol: TCP}]
plan:
  - agent: first
    function_id: first
    prompt: first
    capabilities: [files, dev]
  - agent: second
    function_id: second
    prompt: second
    capabilities: [files]
`}

	definition, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}
	first := definition.Function.Plan[0].Config.(*atc.AgentStep)
	second := definition.Function.Plan[1].Config.(*atc.AgentStep)
	if !first.Hermetic || !second.Hermetic {
		t.Fatalf("compiled transformation agents must be hermetic: first=%t second=%t", first.Hermetic, second.Hermetic)
	}
	if len(first.Capabilities) != 0 || len(second.Capabilities) != 0 {
		t.Fatalf("capability references were not erased: first=%v second=%v", first.Capabilities, second.Capabilities)
	}
	if len(first.Sidecars) != 2 || first.Sidecars[0].Config.Name != "file-tools" || first.Sidecars[1].Config.Name != "dev-mcp" {
		t.Fatalf("authored capability order was not preserved: %#v", first.Sidecars)
	}
	if first.Env["FILES_MCP_URL"] != "http://127.0.0.1:8080/mcp" || first.Env["DEV_MCP_URL"] != "http://127.0.0.1:7780/mcp" ||
		second.Env["FILES_MCP_URL"] != "http://127.0.0.1:8080/mcp" {
		t.Fatalf("compiled capability endpoints = first:%v second:%v", first.Env, second.Env)
	}
	if len(second.Sidecars) != 1 || second.Sidecars[0].Config.Name != "file-tools" {
		t.Fatalf("second expansion = %#v", second.Sidecars)
	}
	if first.Sidecars[0].Config == second.Sidecars[0].Config {
		t.Fatal("two expanded nodes share a SidecarConfig pointer")
	}

	first.Sidecars[0].Config.Command[0] = "mutated"
	first.Sidecars[0].Config.Args[0] = "mutated"
	first.Sidecars[0].Config.Env[0].Value = "mutated"
	first.Sidecars[0].Config.Ports[0].ContainerPort = 1
	first.Sidecars[0].Config.Resources.Requests.CPU = "mutated"
	if second.Sidecars[0].Config.Command[0] != "serve" || second.Sidecars[0].Config.Args[0] != "--stdio" ||
		second.Sidecars[0].Config.Env[0].Value != "review" || second.Sidecars[0].Config.Ports[0].ContainerPort != 8080 ||
		second.Sidecars[0].Config.Resources.Requests.CPU != "100m" {
		t.Fatalf("expanded sidecars alias each other: %+v", second.Sidecars[0].Config)
	}
	if len(definition.Function.Capabilities) != 0 {
		t.Fatalf("compiled function retained source capability catalog: %+v", definition.Function.Capabilities)
	}
}

func TestCompileV3ContextOrderAndDeduplication(t *testing.T) {
	manifest := workflow.Manifest{
		"workflow.yml": v3CompileSource(`
  - agent: context
    prompt: work
    context_files: [context/b.md, context/a.md, context/b.md]`, ""),
		"context/a.md": "A",
		"context/b.md": "B",
	}
	definition, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}
	agent := definition.Function.Plan[0].Config.(*atc.AgentStep)
	want := "## context/b.md\n\nB\n\n## context/a.md\n\nA\n\n"
	if agent.Context != want {
		t.Fatalf("context = %q, want %q", agent.Context, want)
	}
}

func TestCompileV3CompiledAssetBudgetCountsRepeatedPromptReferencesPerNode(t *testing.T) {
	const (
		compiledAssetLimit = 10 << 20
		manifestFileLimit  = 1 << 20
	)
	prompt := strings.Repeat("p", manifestFileLimit)
	manifest := workflow.Manifest{
		"workflow.yml":    v3RepeatedPromptPlan(10),
		"prompts/work.md": prompt,
	}

	if _, err := workflow.CompileDefinition(manifest); err != nil {
		t.Fatalf("CompileDefinition at exact %d-byte boundary: %v", compiledAssetLimit, err)
	}

	manifest["workflow.yml"] = v3RepeatedPromptPlan(11)
	if definition, err := workflow.CompileDefinition(manifest); err == nil || definition != nil {
		t.Fatalf("CompileDefinition above %d-byte boundary = (%+v, %v), want nil and error", compiledAssetLimit, definition, err)
	} else if !strings.Contains(err.Error(), "compiled assets") || !strings.Contains(err.Error(), fmt.Sprint(compiledAssetLimit)) {
		t.Fatalf("error = %q, want compiled-asset limit %d", err, compiledAssetLimit)
	}
}

func TestCompileV3CompiledAssetBudgetCountsContextFramingOncePerPathPerNode(t *testing.T) {
	const manifestFileLimit = 1 << 20
	contextPath := "context/rules.md"
	contextContent := "rules"
	contextFrameBytes := len("## ") + len(contextPath) + len("\n\n") + len(contextContent) + len("\n\n")
	lastPrompt := strings.Repeat("p", manifestFileLimit-contextFrameBytes)
	planPrefix := v3RepeatedPromptSteps(9)
	manifest := workflow.Manifest{
		"workflow.yml": v3CompileSource(planPrefix+fmt.Sprintf(`
  - agent: final
    prompt_file: prompts/final.md
    context_files: [%s, %s]`, contextPath, contextPath), ""),
		"prompts/work.md":  strings.Repeat("p", manifestFileLimit),
		"prompts/final.md": lastPrompt,
		contextPath:        contextContent,
	}

	if _, err := workflow.CompileDefinition(manifest); err != nil {
		t.Fatalf("CompileDefinition with one framed first occurrence: %v", err)
	}

	manifest["context/extra.md"] = "x"
	manifest["workflow.yml"] = v3CompileSource(planPrefix+fmt.Sprintf(`
  - agent: final
    prompt_file: prompts/final.md
    context_files: [%s, %s, context/extra.md]`, contextPath, contextPath), "")
	if definition, err := workflow.CompileDefinition(manifest); err == nil || definition != nil {
		t.Fatalf("CompileDefinition with a second distinct framed context = (%+v, %v), want nil and error", definition, err)
	}
}

func TestCompileV3CompiledAssetBudgetCountsSystemPromptPerNode(t *testing.T) {
	const manifestFileLimit = 1 << 20
	planPrefix := v3RepeatedPromptSteps(9)
	manifest := workflow.Manifest{
		"workflow.yml": v3CompileSource(planPrefix+`
  - agent: final
    prompt: x
    system_prompt_file: prompts/system.md`, ""),
		"prompts/work.md":   strings.Repeat("p", manifestFileLimit),
		"prompts/system.md": strings.Repeat("s", manifestFileLimit-1),
	}

	if _, err := workflow.CompileDefinition(manifest); err != nil {
		t.Fatalf("CompileDefinition with prompt and system prompt at exact boundary: %v", err)
	}

	overPrefix := strings.Replace(planPrefix,
		"    prompt_file: prompts/work.md",
		"    prompt_file: prompts/work.md\n    system_prompt_file: prompts/system.md", 1)
	manifest["workflow.yml"] = v3CompileSource(overPrefix+`
  - agent: final
    prompt: x
    system_prompt_file: prompts/system.md`, "")
	if definition, err := workflow.CompileDefinition(manifest); err == nil || definition != nil {
		t.Fatalf("CompileDefinition with repeated system prompt above boundary = (%+v, %v), want nil and error", definition, err)
	}
}

func TestCompileV3CompiledAssetBudgetCountsCanonicalSidecarPerReference(t *testing.T) {
	const manifestFileLimit = 1 << 20
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sidecar := atc.SidecarConfig{Name: "tools", Image: "registry.example/acme/tools@sha256:" + digest, Ports: []atc.SidecarPort{{ContainerPort: 7780, Protocol: "TCP"}}}
	canonical, err := json.Marshal(sidecar)
	if err != nil {
		t.Fatalf("marshal sidecar: %v", err)
	}
	planPrefix := v3RepeatedPromptSteps(9)
	capabilities := "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: tools\n      image: registry.example/acme/tools@sha256:" + digest + "\n      ports: [{containerPort: 7780, protocol: TCP}]\n"
	manifest := workflow.Manifest{
		"workflow.yml": v3CompileSource(planPrefix+`
  - agent: final
    prompt_file: prompts/final.md
    capabilities: [tools]`, capabilities),
		"prompts/work.md":  strings.Repeat("p", manifestFileLimit),
		"prompts/final.md": strings.Repeat("p", manifestFileLimit-len(canonical)),
	}

	if _, err := workflow.CompileDefinition(manifest); err != nil {
		t.Fatalf("CompileDefinition with canonical sidecar bytes at exact boundary: %v", err)
	}

	overPrefix := strings.Replace(planPrefix,
		"    prompt_file: prompts/work.md",
		"    prompt_file: prompts/work.md\n    capabilities: [tools]", 1)
	manifest["workflow.yml"] = v3CompileSource(overPrefix+`
  - agent: final
    prompt_file: prompts/final.md
    capabilities: [tools]`, capabilities)
	if definition, err := workflow.CompileDefinition(manifest); err == nil || definition != nil {
		t.Fatalf("CompileDefinition with another sidecar reference above boundary = (%+v, %v), want nil and error", definition, err)
	}
}

func TestCompileV3SkillBudgetCountsDeduplicatedSelectedUnion(t *testing.T) {
	const compiledSkillLimit = 512 << 10
	manifest := workflow.Manifest{
		"workflow.yml": v3CompileSource(`
  - agent: first
    prompt: first
    skills: [testing]
  - agent: second
    prompt: second
    skills: [testing]`, ""),
		"skills/testing/SKILL.md": strings.Repeat("s", compiledSkillLimit),
	}

	definition, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition at exact %d-byte skill boundary: %v", compiledSkillLimit, err)
	}
	if got := len(definition.Function.SkillFiles["skills/testing/SKILL.md"]); got != compiledSkillLimit {
		t.Fatalf("compiled skill bytes = %d, want %d", got, compiledSkillLimit)
	}

	manifest["skills/testing/refs/extra.md"] = "x"
	if definition, err := workflow.CompileDefinition(manifest); err == nil || definition != nil {
		t.Fatalf("CompileDefinition above %d-byte skill boundary = (%+v, %v), want nil and error", compiledSkillLimit, definition, err)
	} else if !strings.Contains(err.Error(), "compiled skills") || !strings.Contains(err.Error(), fmt.Sprint(compiledSkillLimit)) {
		t.Fatalf("error = %q, want compiled-skill limit %d", err, compiledSkillLimit)
	}
}

func TestCompileV3WalksAllCompositionForms(t *testing.T) {
	plan := `
  - do:
      - agent: do-agent
        prompt_file: prompts/do.md
      - in_parallel:
          - try:
              agent: try-agent
              prompt_file: prompts/try.md
      - attempts: 2
        agent: retry-agent
        prompt_file: prompts/retry.md
      - timeout: 1m
        agent: timeout-agent
        prompt_file: prompts/timeout.md
      - agent: base-agent
        prompt_file: prompts/base.md
        on_success:
          agent: success-agent
          prompt_file: prompts/success.md
        on_failure:
          agent: failure-agent
          prompt_file: prompts/failure.md
        on_error:
          agent: error-agent
          prompt_file: prompts/error.md
        on_abort:
          agent: abort-agent
          prompt_file: prompts/abort.md
        ensure:
          agent: ensure-agent
          prompt_file: prompts/ensure.md`
	manifest := workflow.Manifest{"workflow.yml": v3CompileSource(plan, "")}
	names := []string{"do", "try", "retry", "timeout", "base", "success", "failure", "error", "abort", "ensure"}
	for _, name := range names {
		manifest["prompts/"+name+".md"] = "compiled " + name
	}

	definition, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}
	visited := map[string]string{}
	for _, step := range definition.Function.Plan {
		err := step.Config.Visit(atc.StepRecursor{OnAgent: func(agent *atc.AgentStep) error {
			visited[agent.Name] = agent.Prompt
			if agent.PromptFile != "" {
				t.Fatalf("agent %q retained prompt_file %q", agent.Name, agent.PromptFile)
			}
			return nil
		}})
		if err != nil {
			t.Fatalf("visit: %v", err)
		}
	}
	if len(visited) != len(names) {
		t.Fatalf("visited %d agents, want %d: %v", len(visited), len(names), visited)
	}
	for _, name := range names {
		if visited[name+"-agent"] != "compiled "+name {
			t.Errorf("%s-agent prompt = %q", name, visited[name+"-agent"])
		}
	}
}

func TestCompileV3PreservesTaskSourcesAndForcesHermeticExecution(t *testing.T) {
	manifest := workflow.Manifest{"workflow.yml": v3CompileSource(`
  - task: ordinary
    file: repository/ci/task.yml
    sidecars:
      - repository/ci/sidecars.yml
      - name: mutable
        image: registry.example/mutable:latest`, "")}
	definition, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}
	task := definition.Function.Plan[0].Config.(*atc.TaskStep)
	if task.ConfigPath != "repository/ci/task.yml" || len(task.Sidecars) != 2 ||
		task.Sidecars[0].File != "repository/ci/sidecars.yml" || task.Sidecars[1].Config.Image != "registry.example/mutable:latest" {
		t.Fatalf("ordinary task sources changed: %+v", task)
	}
	if !task.Hermetic {
		t.Fatal("compiled transformation task is not hermetic")
	}
}

func TestCompileV3RejectsPrivilegedTransformationTask(t *testing.T) {
	manifest := workflow.Manifest{"workflow.yml": v3CompileSource(`
  - task: unsafe
    privileged: true
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: example/task, digest: sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef}
      run: {path: /bin/true}`, "")}
	definition, err := workflow.CompileDefinition(manifest)
	if err == nil || definition != nil {
		t.Fatalf("CompileDefinition = (%+v, %v), want rejection", definition, err)
	}
	if !strings.Contains(err.Error(), `task "unsafe"`) || !strings.Contains(err.Error(), "privileged execution is not allowed") {
		t.Fatalf("error = %q", err)
	}
}

func TestCompileV3RejectsInvalidAgentAssets(t *testing.T) {
	cases := []struct {
		name     string
		plan     string
		files    workflow.Manifest
		want     string
		wantPath string
	}{
		{name: "missing prompt", plan: "  - agent: work", want: "prompt is required"},
		{name: "prompt conflict", plan: "  - agent: work\n    prompt: inline\n    prompt_file: prompts/work.md", files: workflow.Manifest{"prompts/work.md": "TOP-SECRET-CONTENT"}, want: "mutually exclusive"},
		{name: "missing prompt file", plan: "  - agent: work\n    prompt_file: prompts/missing.md", want: "not in the manifest", wantPath: "prompts/missing.md"},
		{name: "unsafe prompt path", plan: "  - agent: work\n    prompt_file: ../secret", want: "segment", wantPath: "../secret"},
		{name: "absolute prompt path", plan: "  - agent: work\n    prompt_file: /secret", want: "absolute", wantPath: "/secret"},
		{name: "dot prompt path", plan: "  - agent: work\n    prompt_file: prompts/./work.md", want: "segment", wantPath: "prompts/./work.md"},
		{name: "empty segment prompt path", plan: "  - agent: work\n    prompt_file: prompts//work.md", want: "empty segment", wantPath: "prompts//work.md"},
		{name: "hidden prompt path", plan: "  - agent: work\n    prompt_file: prompts/.secret/work.md", want: "hidden segment", wantPath: "prompts/.secret/work.md"},
		{name: "backslash prompt path", plan: "  - agent: work\n    prompt_file: prompts\\work.md", want: "backslash", wantPath: `prompts\\work.md`},
		{name: "system conflict", plan: "  - agent: work\n    prompt: work\n    system_prompt: inline\n    system_prompt_file: prompts/system.md", files: workflow.Manifest{"prompts/system.md": "file"}, want: "mutually exclusive"},
		{name: "missing system file", plan: "  - agent: work\n    prompt: work\n    system_prompt_file: prompts/missing.md", want: "not in the manifest", wantPath: "prompts/missing.md"},
		{name: "unsafe system path", plan: "  - agent: work\n    prompt: work\n    system_prompt_file: ../system", want: "segment", wantPath: "../system"},
		{name: "compiled context authored", plan: "  - agent: work\n    prompt: work\n    context: forbidden", want: "context is compiled-only"},
		{name: "empty context path", plan: "  - agent: work\n    prompt: work\n    context_files: ['']", want: "empty path"},
		{name: "missing context file", plan: "  - agent: work\n    prompt: work\n    context_files: [context/missing.md]", want: "not in the manifest", wantPath: "context/missing.md"},
		{name: "unsafe context path", plan: "  - agent: work\n    prompt: work\n    context_files: [../context]", want: "segment", wantPath: "../context"},
		{name: "missing skill root", plan: "  - agent: work\n    prompt: work\n    skills: [testing]", want: "SKILL.md"},
		{name: "empty skill name", plan: "  - agent: work\n    prompt: work\n    skills: ['']", want: "name is required"},
		{name: "invalid skill name", plan: "  - agent: work\n    prompt: work\n    skills: [nested/testing]", want: "bare directory"},
		{name: "hidden skill name", plan: "  - agent: work\n    prompt: work\n    skills: [.hidden]", want: "dot-prefixed"},
		{name: "duplicate skill", plan: "  - agent: work\n    prompt: work\n    skills: [testing, testing]", files: workflow.Manifest{"skills/testing/SKILL.md": "test"}, want: "duplicate skill"},
		{name: "skills output collision", plan: "  - agent: work\n    prompt: work\n    skills: [testing]\n    outputs: [skills]\n    output_types: {skills: {type: review/v1}}", files: workflow.Manifest{"skills/testing/SKILL.md": "test"}, want: "outputs"},
		{name: "authored runtime image", plan: "  - agent: work\n    prompt: work\n    runtime_image: registry.example/agent@sha256:" + strings.Repeat("a", 64), want: "server-selected"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			manifest := workflow.Manifest{"workflow.yml": v3CompileSource("\n"+test.plan, "")}
			for path, content := range test.files {
				manifest[path] = content
			}
			definition, err := workflow.CompileDefinition(manifest)
			if err == nil || definition != nil {
				t.Fatalf("CompileDefinition = (%+v, %v), want nil error result", definition, err)
			}
			if !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), `agent "work"`) {
				t.Fatalf("error = %q, want agent identity and %q", err, test.want)
			}
			if test.wantPath != "" && !strings.Contains(err.Error(), test.wantPath) {
				t.Fatalf("error = %q, want path %q", err, test.wantPath)
			}
			if strings.Contains(err.Error(), "TOP-SECRET-CONTENT") {
				t.Fatalf("error leaked asset content: %v", err)
			}
		})
	}
}

func TestCompileV3RejectsAuthoredCompiledSkillBytes(t *testing.T) {
	for name, source := range map[string]string{
		"function authority": `schema_version: 3
name: authored-skills
signature_version: 1
inputs: []
outputs: []
skill_files: {skills/review/SKILL.md: injected}
plan:
  - agent: review
    prompt: review
`,
		"agent authority": `schema_version: 3
name: authored-agent-skills
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: review
    prompt: review
    skill_files: {skills/review/SKILL.md: injected}
`,
	} {
		t.Run(name, func(t *testing.T) {
			definition, err := workflow.CompileDefinition(workflow.Manifest{"workflow.yml": source})
			if err == nil || definition != nil || !strings.Contains(err.Error(), "skill_files") {
				t.Fatalf("CompileDefinition = (%#v, %v), want compiler-owned skill_bytes rejection", definition, err)
			}
		})
	}
}

func TestCompileV3RejectsInvalidCapabilitiesDeterministically(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	validSidecar := "    sidecar:\n      name: tools\n      image: registry.example/acme/tools@sha256:" + digest + "\n      ports: [{containerPort: 7780, protocol: TCP}]\n"
	cases := []struct {
		name         string
		capabilities string
		plan         string
		want         string
	}{
		{name: "blank catalog name", capabilities: "  '':\n    contract: acme.tools/v1\n" + validSidecar, plan: "  - agent: work\n    prompt: work", want: "capability name is required"},
		{name: "blank contract", capabilities: "  tools:\n    contract: ''\n" + validSidecar, plan: "  - agent: work\n    prompt: work", want: "invalid contract"},
		{name: "zero contract version", capabilities: "  tools:\n    contract: acme.tools/v0\n" + validSidecar, plan: "  - agent: work\n    prompt: work", want: "invalid contract"},
		{name: "leading-zero contract version", capabilities: "  tools:\n    contract: acme.tools/v01\n" + validSidecar, plan: "  - agent: work\n    prompt: work", want: "invalid contract"},
		{name: "uppercase contract", capabilities: "  tools:\n    contract: Acme.tools/v1\n" + validSidecar, plan: "  - agent: work\n    prompt: work", want: "invalid contract"},
		{name: "malformed contract", capabilities: "  tools:\n    contract: acme.tools-1\n" + validSidecar, plan: "  - agent: work\n    prompt: work", want: "invalid contract"},
		{name: "unused invalid catalog entry", capabilities: "  unused:\n    contract: INVALID/v0\n" + validSidecar, plan: "  - agent: work\n    prompt: work", want: `capability "unused"`},
		{name: "sorted catalog errors", capabilities: "  zed:\n    contract: INVALID/v0\n" + validSidecar + "  alpha:\n    contract: ALSO-INVALID/v0\n" + strings.Replace(validSidecar, "name: tools", "name: alpha-tools", 1), plan: "  - agent: work\n    prompt: work", want: `capability "alpha"`},
		{name: "duplicate catalog sidecar name", capabilities: "  alpha:\n    contract: acme.alpha/v1\n" + validSidecar + "  beta:\n    contract: acme.beta/v1\n" + validSidecar, plan: "  - agent: work\n    prompt: work", want: "also declared"},
		{name: "invalid capability name", capabilities: "  Bad_Name:\n    contract: acme.tools/v1\n" + validSidecar, plan: "  - agent: work\n    prompt: work", want: "[a-z][a-z0-9-]*"},
		{name: "unknown node reference", capabilities: "  tools:\n    contract: acme.tools/v1\n" + validSidecar, plan: "  - agent: work\n    prompt: work\n    capabilities: [missing]", want: "unknown capability"},
		{name: "duplicate node reference", capabilities: "  tools:\n    contract: acme.tools/v1\n" + validSidecar, plan: "  - agent: work\n    prompt: work\n    capabilities: [tools, tools]", want: "duplicate capability reference"},
		{name: "direct sidecar bypass", capabilities: "", plan: "  - agent: work\n    prompt: work\n    sidecars: [{name: bypass, image: registry.example/bypass@sha256:" + digest + "}]", want: "direct sidecars are not allowed"},
		{name: "mutable capability image", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: tools\n      image: registry.example/acme/tools:latest\n", plan: "  - agent: work\n    prompt: work", want: "exact sha256"},
		{name: "dynamic capability image", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: tools\n      image_artifact: built-image\n", plan: "  - agent: work\n    prompt: work", want: "image_artifact is not allowed"},
		{name: "missing sidecar name", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      image: registry.example/acme/tools@sha256:" + digest + "\n", plan: "  - agent: work\n    prompt: work", want: "missing 'name'"},
		{name: "missing sidecar image", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: tools\n", plan: "  - agent: work\n    prompt: work", want: "missing 'image'"},
		{name: "invalid sidecar protocol", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: tools\n      image: registry.example/acme/tools@sha256:" + digest + "\n      ports: [{containerPort: 8080, protocol: HTTP}]\n", plan: "  - agent: work\n    prompt: work", want: "invalid port protocol"},
		{name: "reserved sidecar name", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: artifact-helper\n      image: registry.example/acme/tools@sha256:" + digest + "\n", plan: "  - agent: work\n    prompt: work", want: "reserved container name"},
		{name: "retired privileged platform name", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: platform\n      image: registry.example/acme/tools@sha256:" + digest + "\n      ports: [{containerPort: 7781, protocol: TCP}]\n", plan: "  - agent: work\n    prompt: work", want: "retired privileged runtime roles"},
		{name: "missing MCP endpoint", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: tools\n      image: registry.example/acme/tools@sha256:" + digest + "\n", plan: "  - agent: work\n    prompt: work", want: "exactly one TCP port"},
		{name: "ambiguous MCP endpoint", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: tools\n      image: registry.example/acme/tools@sha256:" + digest + "\n      ports: [{containerPort: 7780}, {containerPort: 7781}]\n", plan: "  - agent: work\n    prompt: work", want: "exactly one TCP port"},
		{name: "selected port collision", capabilities: "  first:\n    contract: acme.first/v1\n" + validSidecar + "  second:\n    contract: acme.second/v1\n" + strings.Replace(validSidecar, "name: tools", "name: other-tools", 1), plan: "  - agent: work\n    prompt: work\n    capabilities: [first, second]", want: "both bind localhost MCP port"},
		{name: "authored endpoint override", capabilities: "  tools:\n    contract: acme.tools/v1\n" + validSidecar, plan: "  - agent: work\n    prompt: work\n    env: {TOOLS_MCP_URL: http://attacker.invalid}\n    capabilities: [tools]", want: "reserved for the compiled capability"},
		{name: "undeclared endpoint", capabilities: "", plan: "  - agent: work\n    prompt: work\n    env: {SHADOW_MCP_URL: http://attacker.invalid}", want: "without a named capability"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			manifest := workflow.Manifest{"workflow.yml": v3CompileSource("\n"+test.plan, test.capabilities)}
			definition, err := workflow.CompileDefinition(manifest)
			if err == nil || definition != nil {
				t.Fatalf("CompileDefinition = (%+v, %v), want nil error result", definition, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want %q", err, test.want)
			}
		})
	}
}

func v3CompileSource(plan, capabilities string) string {
	capabilityBlock := ""
	if capabilities != "" {
		capabilityBlock = "capabilities:\n" + capabilities
	}
	plan = v3PlanWithFunctionIDs(plan)
	return fmt.Sprintf(`schema_version: 3
name: compiler-test
signature_version: 1
inputs: []
outputs: []
%splan:%s
`, capabilityBlock, plan)
}

func v3PlanWithFunctionIDs(plan string) string {
	lines := strings.Split(plan, "\n")
	withIDs := make([]string, 0, len(lines)*2)
	for _, line := range lines {
		withIDs = append(withIDs, line)

		trimmed := strings.TrimSpace(line)
		listItem := strings.HasPrefix(trimmed, "- ")
		if listItem {
			trimmed = strings.TrimPrefix(trimmed, "- ")
		}
		kindAndName := strings.SplitN(trimmed, ":", 2)
		if len(kindAndName) != 2 || (kindAndName[0] != "agent" && kindAndName[0] != "task") {
			continue
		}
		name := strings.TrimSpace(kindAndName[1])
		if name == "" || strings.ContainsAny(name, " \t{}[],") {
			continue
		}

		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		if listItem {
			indent += "  "
		}
		withIDs = append(withIDs, indent+"function_id: "+name)
	}
	return strings.Join(withIDs, "\n")
}

func v3RepeatedPromptPlan(count int) string {
	return v3CompileSource(v3RepeatedPromptSteps(count), "")
}

func v3RepeatedPromptSteps(count int) string {
	var plan strings.Builder
	for index := 0; index < count; index++ {
		fmt.Fprintf(&plan, "\n  - agent: work-%d\n    prompt_file: prompts/work.md", index)
	}
	return plan.String()
}
