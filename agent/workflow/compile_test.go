package workflow_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
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
