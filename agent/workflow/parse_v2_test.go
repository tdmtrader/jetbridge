package workflow_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

// v2YAML is a minimal schema_version 2 document exercising every
// source-format field. File references are resolved by Compile;
// Parse only validates structure.
const v2YAML = `schema_version: 2
name: dev
description: v2 grammar test
skills: [tdd, concourse-idioms]
system_prompt_file: system/base.md
context:
  - context/conventions.md
prompt_files:
  implement: prompts/implement.md
prompts:
  review: |
    Review the diff.
steps:
- agent: implement
  prompt: implement
  skills: [extra]
  system_prompt_file: system/implement.md
  context: [context/tdd.md]
  outputs: [workspace]
- agent: review
  prompt: review
  system_prompt: inline step system prompt
  inputs: [workspace]
  outputs: [workspace]
`

func TestParseV2Fields(t *testing.T) {
	cfg, err := workflow.Parse([]byte(v2YAML))
	if err != nil {
		t.Fatalf("expected valid v2 doc, got %v", err)
	}
	if len(cfg.Skills) != 2 || cfg.Skills[0] != "tdd" {
		t.Fatalf("skills not parsed: %v", cfg.Skills)
	}
	if cfg.SystemPromptFile != "system/base.md" {
		t.Fatalf("system_prompt_file not parsed: %q", cfg.SystemPromptFile)
	}
	if cfg.PromptFiles["implement"] != "prompts/implement.md" {
		t.Fatalf("prompt_files not parsed: %v", cfg.PromptFiles)
	}
	if cfg.Steps[0].Skills[0] != "extra" || cfg.Steps[0].Context[0] != "context/tdd.md" {
		t.Fatalf("step-level fields not parsed: %+v", cfg.Steps[0])
	}
	if got := cfg.SourceFormatField(); got == "" {
		t.Fatal("SourceFormatField should report a v2 field in use")
	}
}

func TestParseV2FieldsRequireSchemaVersion2(t *testing.T) {
	doc := strings.Replace(v2YAML, "schema_version: 2", "schema_version: 1", 1)
	_, err := workflow.Parse([]byte(doc))
	if err == nil || !strings.Contains(err.Error(), "schema_version: 2") {
		t.Fatalf("expected schema-gate error, got %v", err)
	}
}

func TestParseRejectsSchemaVersion3(t *testing.T) {
	doc := strings.Replace(v2YAML, "schema_version: 2", "schema_version: 3", 1)
	if _, err := workflow.Parse([]byte(doc)); err == nil {
		t.Fatal("expected unknown schema_version error")
	}
}

func TestParseRejectsHooks(t *testing.T) {
	top := v2YAML + "hooks:\n  post_tool: echo hi\n"
	if _, err := workflow.Parse([]byte(top)); err == nil || !strings.Contains(err.Error(), "hooks") {
		t.Fatalf("top-level hooks: expected rejection, got %v", err)
	}
	step := strings.Replace(v2YAML, "  skills: [extra]", "  skills: [extra]\n  hooks: {x: y}", 1)
	if _, err := workflow.Parse([]byte(step)); err == nil || !strings.Contains(err.Error(), "hooks") {
		t.Fatalf("step-level hooks: expected rejection, got %v", err)
	}
}

func TestParseV2Rejections(t *testing.T) {
	cases := map[string]string{
		"prompt defined inline and as file": strings.Replace(v2YAML,
			"prompts:\n  review: |",
			"prompts:\n  implement: dup\n  review: |", 1),
		"system_prompt and system_prompt_file together": strings.Replace(v2YAML,
			"system_prompt_file: system/base.md",
			"system_prompt_file: system/base.md\nsystem_prompt: also inline", 1),
		"skill name with slash": strings.Replace(v2YAML,
			"skills: [tdd, concourse-idioms]", "skills: [../escape]", 1),
		"duplicate skill": strings.Replace(v2YAML,
			"skills: [tdd, concourse-idioms]", "skills: [tdd, tdd]", 1),
		"empty context entry": strings.Replace(v2YAML,
			"  - context/conventions.md", `  - ""`, 1),
		"checkpoint with skills": strings.Replace(v2YAML,
			"- agent: review\n  prompt: review\n  system_prompt: inline step system prompt\n  inputs: [workspace]\n  outputs: [workspace]",
			"- checkpoint: approve\n  skills: [tdd]", 1),
	}
	for name, doc := range cases {
		if _, err := workflow.Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}
