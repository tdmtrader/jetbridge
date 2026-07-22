package workflow_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

func v2Manifest() workflow.Manifest {
	return workflow.Manifest{
		"workflow.yml":                     v2YAML, // from parse_v2_test.go
		"prompts/implement.md":             "Implement task by task. Ticket: {{.Ticket.Title}}",
		"system/base.md":                   "workflow-level system prompt",
		"system/implement.md":              "implement-step system prompt",
		"context/conventions.md":           "conventions body",
		"context/tdd.md":                   "tdd checklist body",
		"skills/tdd/SKILL.md":              "# tdd skill",
		"skills/tdd/refs/a.md":             "supporting file",
		"skills/concourse-idioms/SKILL.md": "# idioms",
		"skills/extra/SKILL.md":            "# extra",
		"README.md":                        "unreferenced files are allowed and hashed",
	}
}

func validV1YAML() string {
	return `schema_version: 1
name: v1
description: passthrough
prompts:
  work: |
    Do the work.
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`
}

func TestCompileResolvesEverything(t *testing.T) {
	cfg, err := workflow.Compile(v2Manifest())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := cfg.Prompts["implement"]; !strings.Contains(got, "Implement task by task") {
		t.Fatalf("prompt_files not inlined: %q", got)
	}
	if cfg.SystemPrompt != "workflow-level system prompt" {
		t.Fatalf("workflow system_prompt_file not resolved: %q", cfg.SystemPrompt)
	}
	if cfg.Steps[0].SystemPrompt != "implement-step system prompt" {
		t.Fatalf("step system_prompt_file not resolved: %q", cfg.Steps[0].SystemPrompt)
	}
	if cfg.ContextFiles["context/conventions.md"] != "conventions body" ||
		cfg.ContextFiles["context/tdd.md"] != "tdd checklist body" {
		t.Fatalf("context not resolved: %v", cfg.ContextFiles)
	}
	if cfg.SkillFiles["skills/tdd/SKILL.md"] == "" || cfg.SkillFiles["skills/tdd/refs/a.md"] == "" ||
		cfg.SkillFiles["skills/extra/SKILL.md"] == "" {
		t.Fatalf("skill trees not collected: %v", cfg.SkillFiles)
	}
	if _, ok := cfg.SkillFiles["README.md"]; ok {
		t.Fatal("unreferenced file must not land in SkillFiles")
	}
}

func TestCompileSingleFileV1Passthrough(t *testing.T) {
	cfg, err := workflow.Compile(workflow.Manifest{"workflow.yml": validV1YAML()})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if cfg.Name == "" || len(cfg.SkillFiles) != 0 || len(cfg.ContextFiles) != 0 {
		t.Fatalf("v1 passthrough drifted: %+v", cfg)
	}
}

func TestCompileErrors(t *testing.T) {
	missing := func(mutate func(m workflow.Manifest)) workflow.Manifest {
		m := v2Manifest()
		mutate(m)
		return m
	}
	cases := map[string]workflow.Manifest{
		"no workflow.yml":          {"prompts/x.md": "x"},
		"missing prompt file":      missing(func(m workflow.Manifest) { delete(m, "prompts/implement.md") }),
		"missing system file":      missing(func(m workflow.Manifest) { delete(m, "system/base.md") }),
		"missing context file":     missing(func(m workflow.Manifest) { delete(m, "context/tdd.md") }),
		"missing SKILL.md":         missing(func(m workflow.Manifest) { delete(m, "skills/tdd/SKILL.md") }),
		"prompt file bad template": missing(func(m workflow.Manifest) { m["prompts/implement.md"] = "{{.Spec.Title}}" }),
	}
	for name, m := range cases {
		if _, err := workflow.Compile(m); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
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
plan:
  - agent: first
    prompt: first
    capabilities: [files, dev]
  - agent: second
    prompt: second
    capabilities: [files]
`}

	definition, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}
	first := definition.Function.Plan[0].Config.(*atc.AgentStep)
	second := definition.Function.Plan[1].Config.(*atc.AgentStep)
	if len(first.Capabilities) != 0 || len(second.Capabilities) != 0 {
		t.Fatalf("capability references were not erased: first=%v second=%v", first.Capabilities, second.Capabilities)
	}
	if len(first.Sidecars) != 2 || first.Sidecars[0].Config.Name != "file-tools" || first.Sidecars[1].Config.Name != "dev-mcp" {
		t.Fatalf("authored capability order was not preserved: %#v", first.Sidecars)
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
	catalog := definition.Function.Capabilities["files"].Sidecar
	if catalog.Command[0] != "serve" || catalog.Resources.Requests.CPU != "100m" {
		t.Fatalf("expanded sidecar aliases catalog: %+v", catalog)
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
	sidecar := atc.SidecarConfig{Name: "tools", Image: "registry.example/acme/tools@sha256:" + digest}
	canonical, err := json.Marshal(sidecar)
	if err != nil {
		t.Fatalf("marshal sidecar: %v", err)
	}
	planPrefix := v3RepeatedPromptSteps(9)
	capabilities := "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: tools\n      image: registry.example/acme/tools@sha256:" + digest + "\n"
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
          - across:
              - var: item
                values: [one]
            agent: across-agent
            prompt_file: prompts/across.md
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
	names := []string{"do", "try", "across", "retry", "timeout", "base", "success", "failure", "error", "abort", "ensure"}
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

func TestCompileV3LeavesTaskSourcesUntouched(t *testing.T) {
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

func TestCompileV3RejectsInvalidCapabilitiesDeterministically(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	validSidecar := "    sidecar:\n      name: tools\n      image: registry.example/acme/tools@sha256:" + digest + "\n"
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
		{name: "unknown node reference", capabilities: "  tools:\n    contract: acme.tools/v1\n" + validSidecar, plan: "  - agent: work\n    prompt: work\n    capabilities: [missing]", want: "unknown capability"},
		{name: "duplicate node reference", capabilities: "  tools:\n    contract: acme.tools/v1\n" + validSidecar, plan: "  - agent: work\n    prompt: work\n    capabilities: [tools, tools]", want: "duplicate capability reference"},
		{name: "direct sidecar bypass", capabilities: "", plan: "  - agent: work\n    prompt: work\n    sidecars: [{name: bypass, image: registry.example/bypass@sha256:" + digest + "}]", want: "direct sidecars are not allowed"},
		{name: "mutable capability image", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: tools\n      image: registry.example/acme/tools:latest\n", plan: "  - agent: work\n    prompt: work", want: "exact sha256"},
		{name: "dynamic capability image", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: tools\n      image_artifact: built-image\n", plan: "  - agent: work\n    prompt: work", want: "image_artifact is not allowed"},
		{name: "missing sidecar name", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      image: registry.example/acme/tools@sha256:" + digest + "\n", plan: "  - agent: work\n    prompt: work", want: "missing 'name'"},
		{name: "missing sidecar image", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: tools\n", plan: "  - agent: work\n    prompt: work", want: "missing 'image'"},
		{name: "invalid sidecar protocol", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: tools\n      image: registry.example/acme/tools@sha256:" + digest + "\n      ports: [{containerPort: 8080, protocol: HTTP}]\n", plan: "  - agent: work\n    prompt: work", want: "invalid port protocol"},
		{name: "reserved sidecar name", capabilities: "  tools:\n    contract: acme.tools/v1\n    sidecar:\n      name: artifact-helper\n      image: registry.example/acme/tools@sha256:" + digest + "\n", plan: "  - agent: work\n    prompt: work", want: "reserved container name"},
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

func TestCompileDefinitionKeepsLegacyCompileBehavior(t *testing.T) {
	for name, manifest := range map[string]workflow.Manifest{
		"v1": {"workflow.yml": validV1YAML()},
		"v2": v2Manifest(),
	} {
		t.Run(name, func(t *testing.T) {
			legacy, err := workflow.Compile(manifest)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			definition, err := workflow.CompileDefinition(manifest)
			if err != nil {
				t.Fatalf("CompileDefinition: %v", err)
			}
			if definition.Function != nil || !reflect.DeepEqual(definition.Legacy, legacy) {
				t.Fatalf("legacy compile drifted: legacy=%+v definition=%+v", legacy, definition)
			}
		})
	}
}

func v3CompileSource(plan, capabilities string) string {
	capabilityBlock := ""
	if capabilities != "" {
		capabilityBlock = "capabilities:\n" + capabilities
	}
	return fmt.Sprintf(`schema_version: 3
name: compiler-test
signature_version: 1
inputs: []
outputs: []
%splan:%s
`, capabilityBlock, plan)
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
