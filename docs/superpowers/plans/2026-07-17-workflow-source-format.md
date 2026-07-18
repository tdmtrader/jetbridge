# Workflow Source Format + Manifest Import (Slice A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement slice (a) of `docs/superpowers/specs/2026-07-17-workflow-source-format-and-skills-design.md`: directory-based workflow source (schema_version 2: prompt files, skills, context, system prompt), JSON source-manifest import with server-side compile and canonical-manifest hashing, manifest storage, `fly agent workflows import <dir>` / `--set-live` / show summary — with dispatch render refusing the new surfaces until slice (b) (materialization, separate follow-up plan) lands.

**Architecture:** A `Manifest` (path→content map) becomes the import unit and hash basis. `workflow.Compile(Manifest)` parses+validates `workflow.yml` and resolves all file references into a self-contained `Config` (new compile-populated fields `ContextFiles`/`SkillFiles`; prompts/system-prompt inlined). Storage: new nullable `source_manifest JSONB` column; the factory compiles-on-read when present, legacy rows keep the `Parse(definition)` path. The existing per-name import route gains a JSON body; raw YAML stays as the single-file degenerate case. No new routes, no changes to promotion.

**Tech Stack:** Go, goccy/go-yaml, plain `testing` in `agent/workflow` + `agent/api/workflows`, Ginkgo in `atc/db` (needs local PostgreSQL — `pg_isready` first) and `fly/integration` (ghttp mock ATC).

**Key repo rules:** never `--race` with the parallel test flags; commit per task; migration head bumps in BOTH `atc/db/migration/legacy_upgrade_test.go:37` and `docs/migration/migrate-preflight.sh:33`.

---

### Task 1: Manifest type — validation, canonical form, hash

**Files:**
- Create: `agent/workflow/manifest.go`
- Test: `agent/workflow/manifest_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package workflow_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

func TestManifestValidateAcceptsCleanTree(t *testing.T) {
	m := workflow.Manifest{
		"workflow.yml":        "name: x\n",
		"prompts/work.md":     "do it",
		"skills/tdd/SKILL.md": "# tdd",
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestManifestValidateRejectsBadPaths(t *testing.T) {
	cases := map[string]workflow.Manifest{
		"empty manifest": {},
		"absolute":       {"/etc/passwd": "x"},
		"dotdot":         {"a/../b": "x"},
		"dot segment":    {"a/./b": "x"},
		"empty segment":  {"a//b": "x"},
		"hidden segment": {".claude/settings.json": "x"},
		"backslash":      {`a\b`: "x"},
		"empty path":     {"": "x"},
		"not utf8":       {"bin": string([]byte{0xff, 0xfe})},
	}
	for name, m := range cases {
		if err := m.Validate(); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestManifestValidateEnforcesCaps(t *testing.T) {
	big := workflow.Manifest{"big.md": strings.Repeat("a", workflow.MaxManifestFileBytes+1)}
	if err := big.Validate(); err == nil {
		t.Error("per-file cap: expected error")
	}

	many := workflow.Manifest{}
	for i := 0; i <= workflow.MaxManifestFiles; i++ {
		many[fmt.Sprintf("f%04d.md", i)] = "x"
	}
	if err := many.Validate(); err == nil {
		t.Error("file-count cap: expected error")
	}
}

func TestManifestCanonicalIsDeterministicAndPinned(t *testing.T) {
	a := workflow.Manifest{"b.md": "2", "a.md": "1"}
	b := workflow.Manifest{"a.md": "1", "b.md": "2"}
	want := `{"a.md":"1","b.md":"2"}`
	if got := string(a.Canonical()); got != want {
		t.Fatalf("canonical form drifted: got %s want %s", got, want)
	}
	if a.Hash() != b.Hash() {
		t.Fatal("insertion order changed the hash")
	}
	if len(a.Hash()) != 64 {
		t.Fatal("hash is not sha256-hex")
	}
}

func TestManifestPathsSorted(t *testing.T) {
	m := workflow.Manifest{"b": "", "a": "", "c": ""}
	got := m.Paths()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got %v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/workflow/ -run TestManifest -v -count=1`
Expected: FAIL — `undefined: workflow.Manifest`

- [ ] **Step 3: Implement `agent/workflow/manifest.go`**

```go
package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaxManifestFiles bounds one workflow source tree's file count.
	MaxManifestFiles = 512
	// MaxManifestFileBytes bounds one file's content.
	MaxManifestFileBytes = 1 << 20 // 1 MiB
	// MaxManifestBytes bounds the tree's total content.
	MaxManifestBytes = 10 << 20 // 10 MiB
)

// Manifest is a workflow source tree: relative path -> UTF-8 content
// (design 2026-07-17 §2). Its canonical serialization is the
// content-hash provenance unit for manifest imports — versions change
// iff the source files change, stable across fly and server upgrades.
type Manifest map[string]string

// Validate checks paths and caps. Dot-prefixed segments are refused:
// fly excludes hidden files at packaging, and the server refuses them
// so nothing can smuggle e.g. a .claude/ tree past that convention.
func (m Manifest) Validate() error {
	if len(m) == 0 {
		return fmt.Errorf("workflow: manifest has no files")
	}
	if len(m) > MaxManifestFiles {
		return fmt.Errorf("workflow: manifest has %d files (max %d)", len(m), MaxManifestFiles)
	}
	total := 0
	for path, content := range m {
		if err := validateManifestPath(path); err != nil {
			return err
		}
		if len(content) > MaxManifestFileBytes {
			return fmt.Errorf("workflow: manifest file %q is %d bytes (max %d)", path, len(content), MaxManifestFileBytes)
		}
		if !utf8.ValidString(content) {
			return fmt.Errorf("workflow: manifest file %q is not valid UTF-8 (binary assets are out of scope, design §2)", path)
		}
		total += len(content)
	}
	if total > MaxManifestBytes {
		return fmt.Errorf("workflow: manifest is %d bytes total (max %d)", total, MaxManifestBytes)
	}
	return nil
}

func validateManifestPath(path string) error {
	if path == "" {
		return fmt.Errorf("workflow: manifest contains an empty path")
	}
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("workflow: manifest path %q is absolute; paths must be relative", path)
	}
	if strings.Contains(path, `\`) {
		return fmt.Errorf("workflow: manifest path %q contains a backslash; use forward slashes", path)
	}
	for _, seg := range strings.Split(path, "/") {
		switch {
		case seg == "":
			return fmt.Errorf("workflow: manifest path %q contains an empty segment", path)
		case seg == "." || seg == "..":
			return fmt.Errorf("workflow: manifest path %q contains a %q segment", path, seg)
		case strings.HasPrefix(seg, "."):
			return fmt.Errorf("workflow: manifest path %q contains hidden segment %q", path, seg)
		}
	}
	return nil
}

// Canonical is the deterministic serialization hashed for provenance:
// JSON with sorted keys (encoding/json sorts map keys; the pinned test
// vector in manifest_test.go guards against codec drift).
func (m Manifest) Canonical() []byte {
	out, _ := json.Marshal(m) // map[string]string cannot fail to marshal
	return out
}

// Hash is hex(sha256(Canonical())) — the version identity of a
// manifest import.
func (m Manifest) Hash() string {
	return Hash(m.Canonical())
}

// Paths returns the manifest's paths, sorted, for summaries and stable
// iteration.
func (m Manifest) Paths() []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/workflow/ -run TestManifest -v -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/workflow/manifest.go agent/workflow/manifest_test.go
git commit -m "feat(workflow): source-manifest type with canonical hash"
```

---

### Task 2: schema_version 2 grammar — prompt_files, skills, system_prompt, context; hooks rejected

**Files:**
- Modify: `agent/workflow/config.go` (Config + Step fields, `SourceFormatField`)
- Modify: `agent/workflow/parse.go` (schema gate, new validations, hooks probe, extract `validatePromptTemplate`)
- Test: `agent/workflow/parse_v2_test.go` (new file)

- [ ] **Step 1: Write the failing tests**

Create `agent/workflow/parse_v2_test.go`:

```go
package workflow_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

// v2YAML is a minimal schema_version 2 document exercising every
// source-format field. File references are resolved by Compile (Task 3);
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
```

Note for the checkpoint case: the base doc has a `platform` sidecar requirement for checkpoints — that rejection may fire first; any error is acceptable for that case (the assertion is only non-nil).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/workflow/ -run 'TestParseV2|TestParseRejectsHooks|TestParseRejectsSchema' -v -count=1`
Expected: FAIL — v2 doc rejected with "schema_version must be 1, got 2", missing fields, undefined `SourceFormatField`

- [ ] **Step 3: Add the fields to `agent/workflow/config.go`**

Append to the `Config` struct (after `Judge`):

```go
	// schema_version 2 source-format fields (design 2026-07-17 §1).
	// PromptFiles maps a prompt key to a manifest path; Compile inlines
	// the content into Prompts. skills/ names resolve to skills/<name>/
	// trees; Context paths resolve into ContextFiles. SystemPrompt is
	// appended to the runner's baseline (steps replace the workflow
	// layer, never the baseline).
	PromptFiles      map[string]string `yaml:"prompt_files,omitempty" json:"prompt_files,omitempty"`
	Skills           []string          `yaml:"skills,omitempty" json:"skills,omitempty"`
	SystemPrompt     string            `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	SystemPromptFile string            `yaml:"system_prompt_file,omitempty" json:"system_prompt_file,omitempty"`
	Context          []string          `yaml:"context,omitempty" json:"context,omitempty"`

	// Compile-populated resolutions (never authorable in YAML):
	// context path -> content, and skill file path -> content for every
	// referenced skill's tree. The compiled Config is self-contained —
	// the render path never reads the manifest (design §3).
	ContextFiles map[string]string `yaml:"-" json:"context_files,omitempty"`
	SkillFiles   map[string]string `yaml:"-" json:"skill_files,omitempty"`
```

Append to the `Step` struct (after `OutputSchema`, before the checkpoint fields):

```go
	// schema_version 2 source-format fields: additive skills/context on
	// top of the workflow-global sets; step system_prompt REPLACES the
	// workflow-level layer (not the runner baseline).
	Skills           []string `yaml:"skills,omitempty" json:"skills,omitempty"`
	SystemPrompt     string   `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	SystemPromptFile string   `yaml:"system_prompt_file,omitempty" json:"system_prompt_file,omitempty"`
	Context          []string `yaml:"context,omitempty" json:"context,omitempty"`
```

Add the probe method at the bottom of config.go:

```go
// SourceFormatField names the first schema_version-2 source-format
// field in use, or "". Two consumers: the v1 schema gate in Validate,
// and dispatch's v0 render refusal (slice (b) materializes these;
// until then rendering them would silently drop authored behavior).
func (c *Config) SourceFormatField() string {
	switch {
	case len(c.PromptFiles) > 0:
		return "prompt_files"
	case len(c.Skills) > 0:
		return "skills"
	case c.SystemPrompt != "" || c.SystemPromptFile != "":
		return "system_prompt"
	case len(c.Context) > 0:
		return "context"
	}
	for _, s := range c.Steps {
		name := s.Agent
		if name == "" {
			name = s.Checkpoint
		}
		switch {
		case len(s.Skills) > 0:
			return fmt.Sprintf("step %q skills", name)
		case s.SystemPrompt != "" || s.SystemPromptFile != "":
			return fmt.Sprintf("step %q system_prompt", name)
		case len(s.Context) > 0:
			return fmt.Sprintf("step %q context", name)
		}
	}
	return ""
}
```

(config.go now needs `import "fmt"`.)

- [ ] **Step 4: Update `agent/workflow/parse.go`**

4a. In `Parse`, after the successful `yaml.Unmarshal(raw, &cfg)` and before `cfg.Validate()`, add the hooks probe:

```go
	// Hooks are deferred (design 2026-07-17): reject the key rather than
	// silently ignoring it — an author must never believe hook behavior
	// is active when nothing runs it.
	var probe struct {
		Hooks any              `yaml:"hooks"`
		Steps []map[string]any `yaml:"steps"`
	}
	if err := yaml.Unmarshal(raw, &probe); err == nil {
		if probe.Hooks != nil {
			return nil, fmt.Errorf("workflow: hooks are not supported (deferred, design 2026-07-17); remove the hooks key")
		}
		for i, s := range probe.Steps {
			if _, ok := s["hooks"]; ok {
				return nil, fmt.Errorf("workflow: step %d: hooks are not supported (deferred, design 2026-07-17); remove the hooks key", i)
			}
		}
	}
```

4b. Replace the schema-version check at the top of `Validate`:

```go
	if c.SchemaVersion != 1 && c.SchemaVersion != 2 {
		return fmt.Errorf("workflow: schema_version must be 1 or 2, got %d", c.SchemaVersion)
	}
	if c.SchemaVersion == 1 {
		if field := c.SourceFormatField(); field != "" {
			return fmt.Errorf("workflow: %s requires schema_version: 2", field)
		}
	}
```

4c. Extract the prompt-template validation into a helper (replace the body of the existing `for key, body := range c.Prompts` loop with a call):

```go
	for key, body := range c.Prompts {
		if err := validatePromptTemplate(key, body); err != nil {
			return err
		}
	}
```

and add (module-level, reusing the existing error wording verbatim):

```go
// validatePromptTemplate is the §6.2 import gate for one prompt body:
// it must parse as a Go text/template and render against the spec-less
// dispatch ground state. Shared by Validate (inline prompts) and
// Compile (prompt_files content, validated after resolution).
func validatePromptTemplate(key, body string) error {
	tmpl, err := template.New(key).Parse(body)
	if err != nil {
		return fmt.Errorf("workflow: prompt %q: invalid Go text/template: %w", key, err)
	}
	if err := tmpl.Execute(io.Discard, nilRenderContext); err != nil {
		return fmt.Errorf("workflow: prompt %q: does not render against a spec-less ticket (.Spec is nil and .Tasks is empty at every dispatch render — guard with {{if .Spec}} or read via platform-mcp read_ticket/list_tasks): %w", key, err)
	}
	return nil
}
```

4d. After the prompts loop, validate the new top-level fields:

```go
	for key, path := range c.PromptFiles {
		if _, dup := c.Prompts[key]; dup {
			return fmt.Errorf("workflow: prompt %q is defined both inline (prompts) and as a file (prompt_files)", key)
		}
		if path == "" {
			return fmt.Errorf("workflow: prompt_files %q: path is required", key)
		}
	}
	if c.SystemPrompt != "" && c.SystemPromptFile != "" {
		return fmt.Errorf("workflow: system_prompt and system_prompt_file are mutually exclusive")
	}
	if err := validateSkillList("skills", c.Skills); err != nil {
		return err
	}
	for i, p := range c.Context {
		if p == "" {
			return fmt.Errorf("workflow: context[%d]: path is required", i)
		}
	}
```

with the helper:

```go
func validateSkillList(where string, names []string) error {
	seen := map[string]bool{}
	for _, n := range names {
		if n == "" {
			return fmt.Errorf("workflow: %s: skill name is required", where)
		}
		if strings.ContainsAny(n, `/\`) || strings.HasPrefix(n, ".") {
			return fmt.Errorf("workflow: %s: skill name %q must be a bare directory name under skills/", where, n)
		}
		if seen[n] {
			return fmt.Errorf("workflow: %s: duplicate skill %q", where, n)
		}
		seen[n] = true
	}
	return nil
}
```

4e. In the agent-step branch, replace the prompt lookup:

```go
			_, inline := c.Prompts[s.Prompt]
			_, fromFile := c.PromptFiles[s.Prompt]
			if !inline && !fromFile {
				return fmt.Errorf("workflow: agent step %q: unknown prompt %q", s.Agent, s.Prompt)
			}
```

and after the existing per-step checks add:

```go
			if s.SystemPrompt != "" && s.SystemPromptFile != "" {
				return fmt.Errorf("workflow: agent step %q: system_prompt and system_prompt_file are mutually exclusive", s.Agent)
			}
			if err := validateSkillList(fmt.Sprintf("agent step %q skills", s.Agent), s.Skills); err != nil {
				return err
			}
			for i, p := range s.Context {
				if p == "" {
					return fmt.Errorf("workflow: agent step %q: context[%d]: path is required", s.Agent, i)
				}
			}
```

4f. In the checkpoint branch, extend the agent-fields-not-allowed condition:

```go
			if s.Prompt != "" || len(s.Sidecars) > 0 || s.BudgetSliceUSD != 0 || s.Model != "" ||
				s.MaxTurns != 0 || len(s.Inputs) > 0 || len(s.Outputs) > 0 || s.OutputSchema != "" ||
				len(s.Skills) > 0 || s.SystemPrompt != "" || s.SystemPromptFile != "" || len(s.Context) > 0 {
				return fmt.Errorf("workflow: checkpoint %q: agent-step fields are not allowed on a checkpoint", s.Checkpoint)
			}
```

- [ ] **Step 5: Run the package tests**

Run: `go test ./agent/workflow/ -count=1`
Expected: PASS (including the existing v1 tests and seed tests — the grammar is purely additive)

- [ ] **Step 6: Commit**

```bash
git add agent/workflow/config.go agent/workflow/parse.go agent/workflow/parse_v2_test.go
git commit -m "feat(workflow): schema_version 2 grammar (prompt_files, skills, system_prompt, context); hooks rejected"
```

---

### Task 3: Compile — manifest → self-contained Config

**Files:**
- Create: `agent/workflow/compile.go`
- Test: `agent/workflow/compile_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package workflow_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

func v2Manifest() workflow.Manifest {
	return workflow.Manifest{
		"workflow.yml":          v2YAML, // from parse_v2_test.go
		"prompts/implement.md":  "Implement task by task. Ticket: {{.Ticket.Title}}",
		"system/base.md":        "workflow-level system prompt",
		"system/implement.md":   "implement-step system prompt",
		"context/conventions.md": "conventions body",
		"context/tdd.md":        "tdd checklist body",
		"skills/tdd/SKILL.md":   "# tdd skill",
		"skills/tdd/refs/a.md":  "supporting file",
		"skills/concourse-idioms/SKILL.md": "# idioms",
		"skills/extra/SKILL.md": "# extra",
		"README.md":             "unreferenced files are allowed and hashed",
	}
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
		"no workflow.yml":       {"prompts/x.md": "x"},
		"missing prompt file":   missing(func(m workflow.Manifest) { delete(m, "prompts/implement.md") }),
		"missing system file":   missing(func(m workflow.Manifest) { delete(m, "system/base.md") }),
		"missing context file":  missing(func(m workflow.Manifest) { delete(m, "context/tdd.md") }),
		"missing SKILL.md":      missing(func(m workflow.Manifest) { delete(m, "skills/tdd/SKILL.md") }),
		"prompt file bad template": missing(func(m workflow.Manifest) { m["prompts/implement.md"] = "{{.Spec.Title}}" }),
	}
	for name, m := range cases {
		if _, err := workflow.Compile(m); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}
```

Add the small fixture helper at the bottom of `compile_test.go` (a valid v1 doc):

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/workflow/ -run TestCompile -v -count=1`
Expected: FAIL — `undefined: workflow.Compile`

- [ ] **Step 3: Implement `agent/workflow/compile.go`**

```go
package workflow

import (
	"fmt"
	"strings"
)

// Compile validates a source manifest and compiles it into a
// self-contained Config (design 2026-07-17 §3): workflow.yml is parsed
// and grammar-validated, prompt_files / system_prompt_file references
// are inlined, context paths are resolved into ContextFiles, and every
// referenced skill's tree is collected into SkillFiles. The render path
// consumes only the compiled Config — never the manifest. Unreferenced
// files (a README, notes) are allowed: they are source, they are
// hashed, and an edit to them correctly mints a new version.
func Compile(m Manifest) (*Config, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	raw, ok := m["workflow.yml"]
	if !ok {
		return nil, fmt.Errorf("workflow: manifest has no workflow.yml")
	}
	cfg, err := Parse([]byte(raw))
	if err != nil {
		return nil, err
	}

	for key, path := range cfg.PromptFiles {
		content, ok := m[path]
		if !ok {
			return nil, fmt.Errorf("workflow: prompt_files %q: %q is not in the manifest", key, path)
		}
		if err := validatePromptTemplate(key, content); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if cfg.Prompts == nil {
			cfg.Prompts = map[string]string{}
		}
		cfg.Prompts[key] = content
	}

	if cfg.SystemPromptFile != "" {
		content, ok := m[cfg.SystemPromptFile]
		if !ok {
			return nil, fmt.Errorf("workflow: system_prompt_file %q is not in the manifest", cfg.SystemPromptFile)
		}
		cfg.SystemPrompt = content
	}

	skillNames := append([]string{}, cfg.Skills...)
	contextPaths := append([]string{}, cfg.Context...)
	for i := range cfg.Steps {
		s := &cfg.Steps[i]
		if s.SystemPromptFile != "" {
			content, ok := m[s.SystemPromptFile]
			if !ok {
				return nil, fmt.Errorf("workflow: agent step %q: system_prompt_file %q is not in the manifest", s.Agent, s.SystemPromptFile)
			}
			s.SystemPrompt = content
		}
		skillNames = append(skillNames, s.Skills...)
		contextPaths = append(contextPaths, s.Context...)
	}

	for _, path := range contextPaths {
		content, ok := m[path]
		if !ok {
			return nil, fmt.Errorf("workflow: context file %q is not in the manifest", path)
		}
		if cfg.ContextFiles == nil {
			cfg.ContextFiles = map[string]string{}
		}
		cfg.ContextFiles[path] = content
	}

	for _, name := range skillNames {
		prefix := "skills/" + name + "/"
		if _, ok := m[prefix+"SKILL.md"]; !ok {
			return nil, fmt.Errorf("workflow: skill %q: %sSKILL.md is not in the manifest", name, prefix)
		}
		for path, content := range m {
			if strings.HasPrefix(path, prefix) {
				if cfg.SkillFiles == nil {
					cfg.SkillFiles = map[string]string{}
				}
				cfg.SkillFiles[path] = content
			}
		}
	}

	return cfg, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/workflow/ -run TestCompile -v -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/workflow/compile.go agent/workflow/compile_test.go
git commit -m "feat(workflow): compile source manifests into self-contained configs"
```

---

### Task 4: Directory loading — ManifestFromDir + DiscoverWorkflowDirs

**Files:**
- Create: `agent/workflow/dirload.go`
- Test: `agent/workflow/dirload_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package workflow_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManifestFromDirWalksAndSkipsHidden(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"workflow.yml":        "name: x\n",
		"prompts/work.md":     "body",
		"skills/tdd/SKILL.md": "# tdd",
		".DS_Store":           "junk",
		".git/config":         "junk",
	})
	m, err := workflow.ManifestFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"prompts/work.md", "skills/tdd/SKILL.md", "workflow.yml"}
	got := m.Paths()
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestManifestFromDirDereferencesSymlinks(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"lib/skills/tdd/SKILL.md": "# shared tdd",
		"develop/workflow.yml":    "name: develop\n",
	})
	if err := os.MkdirAll(filepath.Join(root, "develop/skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "lib/skills/tdd"), filepath.Join(root, "develop/skills/tdd")); err != nil {
		t.Fatal(err)
	}
	m, err := workflow.ManifestFromDir(filepath.Join(root, "develop"))
	if err != nil {
		t.Fatal(err)
	}
	if m["skills/tdd/SKILL.md"] != "# shared tdd" {
		t.Fatalf("symlinked skill not dereferenced: %v", m.Paths())
	}
}

func TestManifestFromDirDetectsSymlinkCycle(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"workflow.yml": "name: x\n"})
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.ManifestFromDir(root); err == nil {
		t.Fatal("expected symlink-cycle error")
	}
}

func TestDiscoverWorkflowDirs(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"develop/workflow.yml": "name: develop\n",
		"analyze/workflow.yml": "name: analyze\n",
		"lib/skills/x/SKILL.md": "# not a workflow",
	})
	dirs, err := workflow.DiscoverWorkflowDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 2 || filepath.Base(dirs[0]) != "analyze" || filepath.Base(dirs[1]) != "develop" {
		t.Fatalf("got %v", dirs)
	}

	single, err := workflow.DiscoverWorkflowDirs(filepath.Join(root, "develop"))
	if err != nil || len(single) != 1 {
		t.Fatalf("single-dir root: got %v, %v", single, err)
	}

	if _, err := workflow.DiscoverWorkflowDirs(filepath.Join(root, "lib")); err == nil {
		t.Fatal("expected no-workflow.yml error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/workflow/ -run 'TestManifestFromDir|TestDiscover' -v -count=1`
Expected: FAIL — `undefined: workflow.ManifestFromDir`

- [ ] **Step 3: Implement `agent/workflow/dirload.go`**

```go
package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ManifestFromDir builds a Manifest from a workflow source directory:
// fly's packaging half of the import pipeline (design 2026-07-17 §3 —
// fly packages, the server compiles). Symlinks to files AND directories
// are dereferenced — the sharing mechanism across workflows in one repo
// (develop/skills/tdd -> ../../lib/skills/tdd) — and hidden entries
// never reach the manifest. True cycles (a symlink to an ancestor) are
// detected via the recursion stack; diamonds (two names resolving to
// one target) are legal and simply duplicate content under both paths.
func ManifestFromDir(dir string) (Manifest, error) {
	m := Manifest{}
	if err := walkInto(dir, "", m, map[string]bool{}); err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

func walkInto(dir, rel string, m Manifest, stack map[string]bool) error {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", dir, err)
	}
	if stack[real] {
		return fmt.Errorf("workflow: symlink cycle at %s (resolves to already-visited %s)", dir, real)
	}
	stack[real] = true
	defer delete(stack, real)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue // hidden files never reach the manifest (design §3)
		}
		full := filepath.Join(dir, e.Name())
		relPath := e.Name()
		if rel != "" {
			relPath = rel + "/" + e.Name()
		}
		info, err := os.Stat(full) // follows symlinks
		if err != nil {
			return fmt.Errorf("stat %s: %w", full, err)
		}
		if info.IsDir() {
			if err := walkInto(full, relPath, m, stack); err != nil {
				return err
			}
			continue
		}
		if len(m) >= MaxManifestFiles {
			return fmt.Errorf("workflow: %s exceeds %d files", dir, MaxManifestFiles)
		}
		content, err := os.ReadFile(full) // follows symlinks
		if err != nil {
			return err
		}
		m[relPath] = string(content)
	}
	return nil
}

// DiscoverWorkflowDirs returns the workflow source directories under
// root: root itself when it directly contains workflow.yml, otherwise
// every immediate subdirectory that does (a multi-workflow repo).
// Sorted for deterministic multi-import order.
func DiscoverWorkflowDirs(root string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(root, "workflow.yml")); err == nil {
		return []string{root}, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	dirs := []string{}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || !e.IsDir() {
			continue
		}
		sub := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(sub, "workflow.yml")); err == nil {
			dirs = append(dirs, sub)
		}
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("%s: no workflow.yml in the directory or its immediate subdirectories", root)
	}
	sort.Strings(dirs)
	return dirs, nil
}
```

Note: `DiscoverWorkflowDirs` uses `e.IsDir()` on the DirEntry, which does NOT follow a symlinked subdirectory. A top-level symlinked *workflow* directory is out of scope (symlinks are the sharing mechanism *inside* a workflow dir); if one shows up in practice, revisit with `os.Stat`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/workflow/ -run 'TestManifestFromDir|TestDiscover' -v -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/workflow/dirload.go agent/workflow/dirload_test.go
git commit -m "feat(workflow): load source manifests from directories (symlink deref, hidden-skip)"
```

---

### Task 5: Store interface — ImportManifest + MemoryStore + Definition.SourceManifest

**Files:**
- Modify: `agent/workflow/definition.go`
- Modify: `agent/workflow/memory_store.go`
- Test: `agent/workflow/memory_store_test.go` (add cases)

- [ ] **Step 1: Write the failing test** (append to `memory_store_test.go`)

```go
func TestMemoryStoreImportManifest(t *testing.T) {
	store := workflow.NewMemoryStore()

	m := v2Manifest() // from compile_test.go
	def, err := store.ImportManifest("dev", m, "alice")
	if err != nil {
		t.Fatalf("import manifest: %v", err)
	}
	if def.ContentHash != m.Hash() {
		t.Fatalf("hash must be the canonical-manifest hash: %s vs %s", def.ContentHash, m.Hash())
	}
	if def.RawYAML != m["workflow.yml"] {
		t.Fatal("RawYAML must be the manifest's workflow.yml")
	}
	if def.Config.SkillFiles["skills/tdd/SKILL.md"] == "" {
		t.Fatal("stored Config must be compiled (skill trees resolved)")
	}

	again, err := store.ImportManifest("dev", m, "bob")
	if err != nil || again.Version != def.Version {
		t.Fatalf("expected idempotent hit, got v%d err %v", again.Version, err)
	}

	got, found, err := store.Get("dev", def.Version)
	if err != nil || !found {
		t.Fatalf("get: %v %v", found, err)
	}
	if got.SourceManifest["prompts/implement.md"] == "" {
		t.Fatal("Get must return the source manifest")
	}

	// Import(raw) is the single-file degenerate case: same hash scheme.
	raw := []byte(validV1YAML())
	viaRaw, err := store.Import("v1", raw, "alice")
	if err != nil {
		t.Fatal(err)
	}
	wantHash := workflow.Manifest{"workflow.yml": string(raw)}.Hash()
	if viaRaw.ContentHash != wantHash {
		t.Fatalf("raw import must wrap into a single-file manifest: %s vs %s", viaRaw.ContentHash, wantHash)
	}

	// Metadata listings stay lean.
	list, _ := store.List()
	for _, d := range list {
		if d.RawYAML != "" || len(d.SourceManifest) != 0 {
			t.Fatal("List must not carry RawYAML/SourceManifest")
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./agent/workflow/ -run TestMemoryStoreImportManifest -v -count=1`
Expected: FAIL — `store.ImportManifest undefined`

- [ ] **Step 3: Update `definition.go`**

Add to the `Definition` struct after `RawYAML`:

```go
	// SourceManifest is the imported source tree (path -> content), the
	// hashed provenance unit for manifest imports. Populated by Get and
	// Live (like RawYAML); empty in List/Versions. Nil for legacy rows
	// imported before the source-format slice.
	SourceManifest Manifest `json:"source_manifest,omitempty"`
```

Update the `Store` interface (and its doc):

```go
//counterfeiter:generate . Store
type Store interface {
	// Import wraps rawYAML into the single-file manifest
	// {"workflow.yml": rawYAML} and delegates to ImportManifest — the
	// degenerate case of the source format (design 2026-07-17 §2).
	Import(name string, rawYAML []byte, createdBy string) (*Definition, error)
	// ImportManifest compiles and stores a source tree; idempotent on
	// the canonical-manifest hash.
	ImportManifest(name string, m Manifest, createdBy string) (*Definition, error)
	Get(name string, version int) (*Definition, bool, error)
	Live(name string) (*Definition, bool, error)
	Latest(name string) (*Definition, bool, error) // highest version, live or not
	List() ([]Definition, error)                   // latest version per name + live marker
	LiveVersions() (map[string]int, error)         // name -> live version, one query for all names
	Versions(name string) ([]Definition, error)
	Promote(name string, version int, promotedBy string) error // atomically swaps the live flag
}
```

Also update the `ContentHash` doc comment on `Definition` (it currently says "hex(sha256(raw YAML bytes))"):

```go
	// ContentHash is hex(sha256(Manifest.Canonical())) — raw-YAML
	// imports hash their single-file wrapping, so the scheme is uniform
	// (design 2026-07-17 §3; pre-slice rows carry the legacy raw-bytes
	// hash and re-mint one version on their next import).
```

- [ ] **Step 4: Update `memory_store.go`**

Replace the existing `Import` with:

```go
func (m *MemoryStore) Import(name string, rawYAML []byte, createdBy string) (*Definition, error) {
	return m.ImportManifest(name, Manifest{"workflow.yml": string(rawYAML)}, createdBy)
}

func (m *MemoryStore) ImportManifest(name string, src Manifest, createdBy string) (*Definition, error) {
	cfg, err := Compile(src)
	if err != nil {
		return nil, InvalidDefinitionError{Err: err}
	}
	if cfg.Name != name {
		return nil, InvalidDefinitionError{Err: fmt.Errorf("definition name %q does not match import name %q", cfg.Name, name)}
	}
	hash := src.Hash()

	m.mu.Lock()
	defer m.mu.Unlock()

	maxVersion := 0
	for _, d := range m.defs {
		if d.Name != name {
			continue
		}
		if d.ContentHash == hash {
			cp := *d
			return &cp, nil // idempotent on hash
		}
		if d.Version > maxVersion {
			maxVersion = d.Version
		}
	}

	stored := Manifest{}
	for p, c := range src {
		stored[p] = c
	}
	m.nextID++
	def := &Definition{
		ID:             m.nextID,
		Name:           name,
		Version:        maxVersion + 1,
		ContentHash:    hash,
		Description:    cfg.Description,
		CreatedBy:      createdBy,
		CreatedAt:      time.Now().Unix(),
		Config:         *cfg,
		RawYAML:        src["workflow.yml"],
		SourceManifest: stored,
	}
	m.defs = append(m.defs, def)
	cp := *def
	return &cp, nil
}
```

In `List()` and `Versions()`, alongside `cp.RawYAML = ""` add `cp.SourceManifest = nil`.

- [ ] **Step 5: Run the package tests**

Run: `go test ./agent/workflow/ -count=1`
Expected: PASS. If `seed_test.go` or others pin `Hash(raw)` against `Import` results, update them to `Manifest{"workflow.yml": string(raw)}.Hash()` — the standalone `Hash()` function and its test vector are unchanged.

- [ ] **Step 6: Keep the DB factory compiling (interim ImportManifest, no column yet)**

The `Store` interface change breaks `atc/db.agentWorkflowsFactory`. Land the interim version NOW so every commit builds; Task 6 replaces it with the final column-aware form. In `atc/db/agent_workflows_factory.go`, replace the whole `Import` method with:

```go
func (f *agentWorkflowsFactory) Import(name string, rawYAML []byte, createdBy string) (*workflow.Definition, error) {
	return f.ImportManifest(name, workflow.Manifest{"workflow.yml": string(rawYAML)}, createdBy)
}

// ImportManifest — interim (Task 5): manifest-hash identity and compile,
// but the manifest itself is not yet persisted (source_manifest column
// arrives with migration 1773106066 in Task 6).
func (f *agentWorkflowsFactory) ImportManifest(name string, src workflow.Manifest, createdBy string) (*workflow.Definition, error) {
	cfg, err := workflow.Compile(src)
	if err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
	}
	if cfg.Name != name {
		return nil, workflow.InvalidDefinitionError{Err: fmt.Errorf("definition name %q does not match import name %q", cfg.Name, name)}
	}
	hash := src.Hash()

	tx, err := f.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)

	_, err = tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('agent_workflow_definitions:' || $1))`, name)
	if err != nil {
		return nil, err
	}

	var def workflow.Definition
	err = tx.QueryRow(`
		SELECT `+workflowMetaColumns+`, definition
		FROM agent_workflow_definitions
		WHERE name = $1 AND content_hash = $2`,
		name, hash,
	).Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
		&def.Description, &def.CreatedBy, &def.CreatedAt, &def.RawYAML)
	if err == nil {
		def.Config = *cfg
		return &def, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	err = tx.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(name, version, content_hash, definition, description, created_by)
		SELECT $1, COALESCE(MAX(version), 0) + 1, $2, $3, $4, $5
		FROM agent_workflow_definitions WHERE name = $1
		RETURNING id, version, EXTRACT(EPOCH FROM created_at)::bigint`,
		name, hash, src["workflow.yml"], cfg.Description, createdBy,
	).Scan(&def.ID, &def.Version, &def.CreatedAt)
	if err != nil {
		return nil, err
	}

	def.Name = name
	def.ContentHash = hash
	def.Description = cfg.Description
	def.CreatedBy = createdBy
	def.RawYAML = src["workflow.yml"]
	def.Config = *cfg
	return &def, tx.Commit()
}
```

The hash scheme changed for `Import`, so fix the pinned expectation at `atc/db/agent_workflows_factory_test.go:41` in this task (not Task 6):

```go
		Expect(v1.ContentHash).To(Equal(workflow.Manifest{"workflow.yml": string(defYAML("wf-import", "One."))}.Hash()))
```

If any counterfeiter fake of `workflow.Store` exists, regenerate with `go generate ./agent/workflow/...`.

Run: `go build ./...` — expected: clean.
Run (PostgreSQL required): `ginkgo --focus-file=agent_workflows_factory_test.go ./atc/db/` — expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add agent/workflow/definition.go agent/workflow/memory_store.go agent/workflow/memory_store_test.go \
        atc/db/agent_workflows_factory.go atc/db/agent_workflows_factory_test.go
git commit -m "feat(workflow): ImportManifest on the Store interface; manifest-hash identity"
```

---

### Task 6: DB — source_manifest column, factory manifest imports, compile-on-read

**Prerequisite:** PostgreSQL running (`pg_isready`).

**Files:**
- Create: `atc/db/migration/migrations/1773106066_add_agent_workflow_source_manifest.up.sql`
- Create: `atc/db/migration/migrations/1773106066_add_agent_workflow_source_manifest.down.sql`
- Modify: `atc/db/agent_workflows_factory.go`
- Modify: `atc/db/migration/legacy_upgrade_test.go:37` (head bump)
- Modify: `docs/migration/migrate-preflight.sh:33` (head bump)
- Test: `atc/db/agent_workflows_factory_test.go`

Migration-number rule (ticket-core precedent): 1773106065 is re-reserved for PARK-V2 `agent_run_step_state`; this slice takes **1773106066**. Before creating the files, confirm nothing landed above 1773106064 in the meantime: `ls atc/db/migration/migrations/ | sort | tail -5`. If something did, take the next free number and update both head pins accordingly.

- [ ] **Step 1: Write the migration files**

`1773106066_add_agent_workflow_source_manifest.up.sql`:

```sql
ALTER TABLE agent_workflow_definitions ADD COLUMN source_manifest JSONB;
```

`1773106066_add_agent_workflow_source_manifest.down.sql`:

```sql
ALTER TABLE agent_workflow_definitions DROP COLUMN source_manifest;
```

(SQL migrations are picked up by filename embedding — no registry edit; confirm by pattern-matching how 1773106062–64 landed.)

- [ ] **Step 2: Bump both head pins**

- `atc/db/migration/legacy_upgrade_test.go:37`: `const jetbridgeHeadMigration = 1773106066`
- `docs/migration/migrate-preflight.sh:33`: `JETBRIDGE_VERSION=1773106066`

- [ ] **Step 3: Write the failing factory specs** (append inside the existing `Describe("AgentWorkflowsFactory")` in `atc/db/agent_workflows_factory_test.go`)

```go
	Describe("manifest imports", func() {
		manifest := func(name string) workflow.Manifest {
			return workflow.Manifest{
				"workflow.yml": `schema_version: 2
name: ` + name + `
description: manifest test
skills: [tdd]
context: [context/conventions.md]
system_prompt_file: system/base.md
prompt_files:
  work: prompts/work.md
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`,
				"prompts/work.md":         "Do the work.",
				"system/base.md":          "base system prompt",
				"context/conventions.md":  "conventions",
				"skills/tdd/SKILL.md":     "# tdd",
				"skills/tdd/refs/red.md":  "red-green",
			}
		}

		It("imports, compiles on read, and is idempotent on the manifest hash", func() {
			src := manifest("wf-manifest")
			v1, err := factory.ImportManifest("wf-manifest", src, "alice")
			Expect(err).ToNot(HaveOccurred())
			Expect(v1.Version).To(Equal(1))
			Expect(v1.ContentHash).To(Equal(src.Hash()))

			again, err := factory.ImportManifest("wf-manifest", src, "bob")
			Expect(err).ToNot(HaveOccurred())
			Expect(again.Version).To(Equal(1))

			got, found, err := factory.Get("wf-manifest", 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(got.RawYAML).To(Equal(src["workflow.yml"]))
			Expect(got.SourceManifest["skills/tdd/refs/red.md"]).To(Equal("red-green"))
			Expect(got.Config.SystemPrompt).To(Equal("base system prompt"))
			Expect(got.Config.SkillFiles).To(HaveKey("skills/tdd/SKILL.md"))
			Expect(got.Config.ContextFiles["context/conventions.md"]).To(Equal("conventions"))
		})

		It("reads legacy rows (no source_manifest) via the Parse path", func() {
			raw := defYAML("wf-legacy", "Legacy.")
			// Simulate a pre-slice row: definition only, NULL manifest.
			_, err := dbConn.Exec(`
				INSERT INTO agent_workflow_definitions
					(name, version, content_hash, definition, description, created_by)
				VALUES ('wf-legacy', 1, 'legacyhash', $1, 'legacy', 'alice')`, string(raw))
			Expect(err).ToNot(HaveOccurred())

			got, found, err := factory.Get("wf-legacy", 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(got.Config.Name).To(Equal("wf-legacy"))
			Expect(got.SourceManifest).To(BeEmpty())
		})
	})
```

(The line-41 hash expectation was already fixed in Task 5.)

- [ ] **Step 4: Run to verify failures**

Run: `ginkgo --focus-file=agent_workflows_factory_test.go ./atc/db/`
Expected: FAIL — column `source_manifest` scanned but factory doesn't select it yet / new specs fail. (If `database "testdb_template" already exists`, another test process is running — wait or kill it.)

- [ ] **Step 5: Finish the factory**

In `atc/db/agent_workflows_factory.go` (building on the Task 5 stub):

`ImportManifest` — final form:

```go
func (f *agentWorkflowsFactory) ImportManifest(name string, src workflow.Manifest, createdBy string) (*workflow.Definition, error) {
	cfg, err := workflow.Compile(src)
	if err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
	}
	if cfg.Name != name {
		return nil, workflow.InvalidDefinitionError{Err: fmt.Errorf("definition name %q does not match import name %q", cfg.Name, name)}
	}
	hash := src.Hash()

	tx, err := f.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)

	// Serialize imports per name so version assignment is race-free
	// under concurrent web nodes.
	_, err = tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('agent_workflow_definitions:' || $1))`, name)
	if err != nil {
		return nil, err
	}

	var def workflow.Definition
	err = tx.QueryRow(`
		SELECT `+workflowMetaColumns+`
		FROM agent_workflow_definitions
		WHERE name = $1 AND content_hash = $2`,
		name, hash,
	).Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
		&def.Description, &def.CreatedBy, &def.CreatedAt)
	if err == nil {
		// Idempotent on hash: byte-identical source returns the existing
		// version untouched (contracts §1.6).
		def.Config = *cfg
		def.RawYAML = src["workflow.yml"]
		def.SourceManifest = src
		return &def, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	err = tx.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(name, version, content_hash, definition, source_manifest, description, created_by)
		SELECT $1, COALESCE(MAX(version), 0) + 1, $2, $3, $4::jsonb, $5, $6
		FROM agent_workflow_definitions WHERE name = $1
		RETURNING id, version, EXTRACT(EPOCH FROM created_at)::bigint`,
		name, hash, src["workflow.yml"], string(src.Canonical()), cfg.Description, createdBy,
	).Scan(&def.ID, &def.Version, &def.CreatedAt)
	if err != nil {
		return nil, err
	}

	def.Name = name
	def.ContentHash = hash
	def.Description = cfg.Description
	def.CreatedBy = createdBy
	def.RawYAML = src["workflow.yml"]
	def.SourceManifest = src
	def.Config = *cfg
	return &def, tx.Commit()
}
```

`getOne` — add the manifest column and compile-on-read:

```go
func (f *agentWorkflowsFactory) getOne(where string, args ...any) (*workflow.Definition, bool, error) {
	var def workflow.Definition
	var manifestJSON sql.NullString
	err := f.conn.QueryRow(`
		SELECT `+workflowMetaColumns+`, definition, source_manifest
		FROM agent_workflow_definitions
		WHERE `+where, args...,
	).Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
		&def.Description, &def.CreatedBy, &def.CreatedAt, &def.RawYAML, &manifestJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if manifestJSON.Valid {
		var src workflow.Manifest
		if err := json.Unmarshal([]byte(manifestJSON.String), &src); err != nil {
			return nil, false, fmt.Errorf("stored manifest %s/v%d no longer parses: %w", def.Name, def.Version, err)
		}
		cfg, err := workflow.Compile(src)
		if err != nil {
			// Rows are compiled at import; a failure here means the stored
			// manifest was corrupted out-of-band.
			return nil, false, fmt.Errorf("stored manifest %s/v%d no longer compiles: %w", def.Name, def.Version, err)
		}
		def.Config = *cfg
		def.SourceManifest = src
		return &def, true, nil
	}
	// Legacy pre-manifest row: the definition column is the whole source.
	cfg, err := workflow.Parse([]byte(def.RawYAML))
	if err != nil {
		return nil, false, fmt.Errorf("stored definition %s/v%d no longer parses: %w", def.Name, def.Version, err)
	}
	def.Config = *cfg
	return &def, true, nil
}
```

Add `"encoding/json"` to the factory's imports.

- [ ] **Step 6: Run the factory specs**

Run: `ginkgo --focus-file=agent_workflows_factory_test.go ./atc/db/`
Expected: PASS

- [ ] **Step 7: Run the migration suite**

Run: `ginkgo ./atc/db/migration/`
Expected: PASS (head now 1773106066)

- [ ] **Step 8: Commit**

```bash
git add atc/db/migration/migrations/1773106066_add_agent_workflow_source_manifest.up.sql \
        atc/db/migration/migrations/1773106066_add_agent_workflow_source_manifest.down.sql \
        atc/db/migration/legacy_upgrade_test.go docs/migration/migrate-preflight.sh \
        atc/db/agent_workflows_factory.go atc/db/agent_workflows_factory_test.go
git commit -m "feat(db): source_manifest column; manifest imports + compile-on-read in the workflows factory"
```

---

### Task 7: API — JSON manifest body on the import route

**Files:**
- Modify: `agent/api/workflows/handler.go`
- Test: `agent/api/workflows/handler_test.go` (add cases)

- [ ] **Step 1: Write the failing tests** (append to `handler_test.go`; reuse its `request` helper but with a Content-Type variant)

```go
func jsonRequest(path string, params url.Values, body string) *http.Request {
	r := request("POST", path, params, body)
	r.Header.Set("Content-Type", "application/json")
	return r
}

const manifestBody = `{"files": {
  "workflow.yml": "schema_version: 2\nname: wf\ndescription: manifest import\nskills: [tdd]\nprompt_files:\n  work: prompts/work.md\nsteps:\n- agent: work\n  prompt: work\n  outputs: [workspace]\n",
  "prompts/work.md": "Do the work.",
  "skills/tdd/SKILL.md": "# tdd"
}}`

func TestImportManifestBody(t *testing.T) {
	h, _ := newHandler(t)

	w := httptest.NewRecorder()
	h.Import(w, jsonRequest("/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, manifestBody))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var def workflow.Definition
	if err := json.Unmarshal(w.Body.Bytes(), &def); err != nil {
		t.Fatal(err)
	}
	if def.Version != 1 || def.Config.SkillFiles["skills/tdd/SKILL.md"] == "" {
		t.Fatalf("manifest not compiled: %+v", def)
	}
}

func TestImportManifestBodyRejections(t *testing.T) {
	h, _ := newHandler(t)

	cases := map[string]struct {
		body string
		code int
	}{
		"malformed json":     {"{not json", http.StatusBadRequest},
		"empty files":        {`{"files": {}}`, http.StatusBadRequest},
		"missing skill file": {`{"files": {"workflow.yml": "schema_version: 2\nname: wf\nskills: [ghost]\nprompts:\n  work: w\nsteps:\n- agent: work\n  prompt: work\n  outputs: [workspace]\n"}}`, http.StatusBadRequest},
	}
	for name, tc := range cases {
		w := httptest.NewRecorder()
		h.Import(w, jsonRequest("/api/v1/agent/workflows/wf/versions",
			url.Values{":workflow_name": {"wf"}}, tc.body))
		if w.Code != tc.code {
			t.Errorf("%s: expected %d, got %d: %s", name, tc.code, w.Code, w.Body.String())
		}
	}
}

func TestImportRawYAMLStillWorks(t *testing.T) {
	h, _ := newHandler(t)
	w := httptest.NewRecorder()
	h.Import(w, request("POST", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, validYAML))
	if w.Code != http.StatusOK {
		t.Fatalf("raw path regressed: %d %s", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 2: Run to verify failures**

Run: `go test ./agent/api/workflows/ -run 'TestImportManifest|TestImportRaw' -v -count=1`
Expected: FAIL — JSON body treated as YAML (400 from the raw path)

- [ ] **Step 3: Implement the content-type switch in `handler.go`**

Add near `maxDefinitionBytes`:

```go
// maxManifestRequestBytes bounds the JSON manifest envelope: 10 MiB of
// content (workflow.MaxManifestBytes) plus JSON-encoding overhead.
const maxManifestRequestBytes = 12 << 20
```

Replace the body-reading half of `Import` with:

```go
func (h *Handler) Import(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")

	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxManifestRequestBytes+1))
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		if len(raw) > maxManifestRequestBytes {
			http.Error(w, "manifest exceeds 12 MiB", http.StatusRequestEntityTooLarge)
			return
		}
		var body struct {
			Files workflow.Manifest `json:"files"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "malformed manifest body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(body.Files) == 0 {
			http.Error(w, `manifest body must carry a non-empty "files" map`, http.StatusBadRequest)
			return
		}
		def, err := h.store.ImportManifest(name, body.Files, requestUser(r))
		if err != nil {
			var inv workflow.InvalidDefinitionError
			if errors.As(err, &inv) {
				http.Error(w, inv.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, "failed to import workflow", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, def)
		return
	}

	// Raw-YAML body: the single-file degenerate case (any other
	// Content-Type, as before).
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxDefinitionBytes+1))
	// ... existing raw path unchanged from here ...
```

(`handler.go` needs `"strings"` added to imports.)

- [ ] **Step 4: Run to verify pass**

Run: `go test ./agent/api/workflows/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/api/workflows/handler.go agent/api/workflows/handler_test.go
git commit -m "feat(agent): accept JSON source manifests on the workflow import route"
```

---

### Task 8: dispatch — refuse source-format surfaces at v0 render

**Files:**
- Modify: `agent/dispatch/render.go` (in `Render`, after the judge refusal)
- Test: `agent/dispatch/render_test.go` (add case)

- [ ] **Step 1: Write the failing test** (append to `render_test.go`; it is a plain-Go test file with a `renderInput()` helper returning a valid minimal `dispatch.RenderInput` — reuse it)

```go
func TestRenderRefusesSourceFormatSurfaces(t *testing.T) {
	in := renderInput()
	in.Workflow.SchemaVersion = 2
	in.Workflow.Skills = []string{"tdd"}
	if _, err := dispatch.Render(in); err == nil || !strings.Contains(err.Error(), "slice b") {
		t.Fatalf("workflow-level skills must refuse: %v", err)
	}

	in = renderInput()
	in.Workflow.SchemaVersion = 2
	in.Workflow.Steps[0].SystemPrompt = "step system prompt"
	if _, err := dispatch.Render(in); err == nil {
		t.Fatal("step-level system_prompt must refuse")
	}

	in = renderInput()
	in.Workflow.SchemaVersion = 2
	in.Workflow.ContextFiles = map[string]string{"context/x.md": "body"}
	in.Workflow.Context = []string{"context/x.md"}
	if _, err := dispatch.Render(in); err == nil {
		t.Fatal("context must refuse")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./agent/dispatch/ -run TestRender -v -count=1` (or `ginkgo ./agent/dispatch/` if the suite is Ginkgo)
Expected: FAIL — render currently succeeds with the new fields present

- [ ] **Step 3: Add the refusal in `Render`** (immediately after the judge refusal block)

```go
	// Refuse source-format surfaces until slice (b) materializes them
	// (design 2026-07-17 §4): skills/system-prompt/context validate at
	// import and are content-hashed as authoritative, so rendering
	// without materialization would silently drop authored behavior —
	// same refuse-don't-drop rule as sidecars and gate_policy above.
	if field := in.Workflow.SourceFormatField(); field != "" {
		return atc.Config{}, fmt.Errorf("workflow %q declares %s: v0 render does not materialize source-format surfaces (slice b) — remove them or wait for materialization", in.WorkflowName, field)
	}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./agent/dispatch/ -count=1` (or `ginkgo ./agent/dispatch/`)
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/dispatch/render.go agent/dispatch/render_test.go
git commit -m "feat(dispatch): refuse source-format surfaces at v0 render (slice b materializes)"
```

---

### Task 9: fly — import directories, --set-live, manifest summary in show

**Files:**
- Modify: `fly/commands/agent_workflows.go`
- Test: `fly/integration/agent_workflows_test.go` (add specs)

- [ ] **Step 1: Write the failing integration specs** (append to the `Describe("fly agent workflows")` block; Ginkgo + ghttp)

```go
	Describe("import from a directory", func() {
		var srcDir string

		BeforeEach(func() {
			srcDir = GinkgoT().TempDir()
			Expect(os.MkdirAll(filepath.Join(srcDir, "prompts"), 0o755)).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(srcDir, "skills", "tdd"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(srcDir, "workflow.yml"), []byte(
				"schema_version: 2\nname: dev\ndescription: dir import\nskills: [tdd]\nprompt_files:\n  work: prompts/work.md\nsteps:\n- agent: work\n  prompt: work\n  outputs: [workspace]\n"), 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(srcDir, "prompts", "work.md"), []byte("Do the work."), 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(srcDir, "skills", "tdd", "SKILL.md"), []byte("# tdd"), 0o644)).To(Succeed())
			// hidden junk must be excluded from the posted manifest
			Expect(os.WriteFile(filepath.Join(srcDir, ".DS_Store"), []byte("junk"), 0o644)).To(Succeed())

			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/workflows/dev/versions"),
					ghttp.VerifyContentType("application/json"),
					ghttp.VerifyJSONRepresenting(map[string]any{"files": map[string]any{
						"workflow.yml":        "schema_version: 2\nname: dev\ndescription: dir import\nskills: [tdd]\nprompt_files:\n  work: prompts/work.md\nsteps:\n- agent: work\n  prompt: work\n  outputs: [workspace]\n",
						"prompts/work.md":     "Do the work.",
						"skills/tdd/SKILL.md": "# tdd",
					}}),
					ghttp.RespondWithJSONEncoded(http.StatusOK, workflow.Definition{
						Name: "dev", Version: 4, ContentHash: "deadbeefdeadbeef",
					}),
				),
			)
		})

		It("packages the directory as a manifest and posts it", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "import", srcDir)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`imported dev version 4`))
		})

		It("promotes with --set-live", func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/workflows/dev/versions/4/live"),
					ghttp.RespondWith(http.StatusNoContent, nil),
				),
			)
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "import", srcDir, "--set-live")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`workflow dev version 4 is now live`))
		})
	})
```

- [ ] **Step 2: Run to verify failures**

Run: `ginkgo --focus="fly agent workflows" ./fly/integration/`
Expected: FAIL — import rejects a directory path ("is a directory") and `--set-live` is an unknown flag. (This suite builds the fly binary; the mock ATC version must match `versions.go` — currently `0.1.0` — which the suite already handles.)

- [ ] **Step 3: Rework `WorkflowsImportCommand` and `show` in `fly/commands/agent_workflows.go`**

Replace the import command with:

```go
type WorkflowsImportCommand struct {
	Args struct {
		Path string `positional-arg-name:"PATH" required:"true" description:"Workflow definition YAML file, a workflow source directory, or a directory of workflow directories"`
	} `positional-args:"yes"`
	SetLive bool `long:"set-live" description:"Promote each imported version live immediately (auto-promote deploy pipelines; manual set-live stays the default)"`
}

func (command *WorkflowsImportCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	info, err := os.Stat(command.Args.Path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return importWorkflowFile(target, command.Args.Path, command.SetLive)
	}

	dirs, err := workflow.DiscoverWorkflowDirs(command.Args.Path)
	if err != nil {
		return err
	}
	// Each import is independent and idempotent: a failure leaves the
	// others in place and a re-run converges (design §5).
	for _, dir := range dirs {
		if err := importWorkflowDir(target, dir, command.SetLive); err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
	}
	return nil
}

func importWorkflowDir(target rc.Target, dir string, setLive bool) error {
	m, err := workflow.ManifestFromDir(dir)
	if err != nil {
		return err
	}
	// Compile client-side first: same validation the server runs, but
	// the error message points at local files.
	cfg, err := workflow.Compile(m)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(map[string]any{"files": m})
	if err != nil {
		return err
	}
	resp, err := agentAPIRequestWithType(target, "POST",
		"/api/v1/agent/workflows/"+url.PathEscape(cfg.Name)+"/versions",
		"application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	var def workflow.Definition
	if err := decodeOrError(resp, &def); err != nil {
		return err
	}
	fmt.Printf("imported %s version %d (hash %.12s)\n", def.Name, def.Version, def.ContentHash)

	if setLive {
		return setLiveVersion(target, def.Name, def.Version)
	}
	return nil
}

func importWorkflowFile(target rc.Target, path string, setLive bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cfg, err := workflow.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	resp, err := agentAPIRequest(target, "POST",
		"/api/v1/agent/workflows/"+url.PathEscape(cfg.Name)+"/versions", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	var def workflow.Definition
	if err := decodeOrError(resp, &def); err != nil {
		return err
	}
	fmt.Printf("imported %s version %d (hash %.12s)\n", def.Name, def.Version, def.ContentHash)

	if setLive {
		return setLiveVersion(target, def.Name, def.Version)
	}
	return nil
}

func setLiveVersion(target rc.Target, name string, version int) error {
	resp, err := agentAPIRequest(target, "PUT",
		"/api/v1/agent/workflows/"+url.PathEscape(name)+"/versions/"+strconv.Itoa(version)+"/live", nil)
	if err != nil {
		return err
	}
	if err := decodeOrError(resp, nil); err != nil {
		return err
	}
	fmt.Printf("workflow %s version %d is now live\n", name, version)
	return nil
}
```

Refactor `WorkflowsSetLiveCommand.Execute` to call `setLiveVersion(target, command.Args.Name, command.Args.Version)` (dedup).

Add the header-carrying request helper next to `agentAPIRequest`:

```go
func agentAPIRequestWithType(target rc.Target, method, path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, target.URL()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return target.Client().HTTPClient().Do(req)
}
```

In `WorkflowsShowCommand.Execute`, after `fmt.Print(def.RawYAML)` add the manifest summary (stderr, so stdout stays pipeable YAML):

```go
	if len(def.SourceManifest) > 1 {
		fmt.Fprintf(os.Stderr, "# source files (%d):\n", len(def.SourceManifest))
		for _, p := range def.SourceManifest.Paths() {
			fmt.Fprintf(os.Stderr, "#   %-48s %7d bytes\n", p, len(def.SourceManifest[p]))
		}
	}
```

- [ ] **Step 4: Run the integration specs**

Run: `ginkgo --focus="fly agent workflows" ./fly/integration/`
Expected: PASS (including the pre-existing list/show/import/set-live specs)

- [ ] **Step 5: Commit**

```bash
git add fly/commands/agent_workflows.go fly/integration/agent_workflows_test.go
git commit -m "feat(fly): import workflow source directories, --set-live, manifest summary in show"
```

---

### Task 10: Spec amendment + full verification

**Files:**
- Modify: `docs/superpowers/specs/2026-07-17-workflow-source-format-and-skills-design.md`

- [ ] **Step 1: Amend the spec's grammar snippet**

The spec's §1 shows `prompts: {implement: {file: ...}}` (a string-or-object union). Implementation uses a separate `prompt_files:` map — additive, no change to the existing `Prompts map[string]string` type or its consumers, and cleaner YAML. Update the §1 snippet to:

```yaml
prompts:
  review: |
    Inline prompts keep working.
prompt_files:
  implement: prompts/implement.md    # inlined into prompts at import
```

and append to the Decision log:

```markdown
- Grammar realization: `prompt_files` sibling map instead of a
  string-or-object union under `prompts:` (implementation, slice (a) —
  additive, keeps every existing Prompts consumer untouched; a key may
  not appear in both maps).
```

- [ ] **Step 2: Full verification**

```bash
gofmt -l agent/ atc/db/ fly/commands/ | grep -v _test_data || true   # expect no output
go build ./...
go test ./agent/... -count=1
ginkgo --focus-file=agent_workflows_factory_test.go ./atc/db/
make test-fly-integration
```

Expected: all green. Then the broader gate:

```bash
make test-quick
```

Expected: PASS (~5 min; PostgreSQL required). Known pre-existing issues that are NOT this change's fault: `atc/exec/artifact_input_step_test.go` vet failure, gardenruntime BeforeSuite postgres port conflict.

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/specs/2026-07-17-workflow-source-format-and-skills-design.md
git commit -m "docs(specs): grammar realization — prompt_files map (slice a implementation note)"
```

---

## Out of scope for this plan (slice (b), separate follow-up plan)

Renderer materialization of skills into agent pods, runner mapping (claude: `.claude/skills/`, system-prompt append, session-start context), the `AgentStep` schema fields (`SystemPrompt`/`Context`/`Skills`) and §8.1 env plumbing, the published example deploy pipeline, and the live theborg smoke with a skill-bearing workflow. Slice (a) deliberately leaves render refusing the new surfaces (Task 8) so nothing silently drops authored behavior in the meantime.

## Spec-coverage map (self-review)

- §1 source format + semantics → Tasks 2, 3 (additive/replace semantics encoded in Compile + field docs; enforcement of runtime semantics is slice (b))
- §2 transport (JSON manifest, caps, path rules, UTF-8, raw-YAML wrap) → Tasks 1, 7; hash-scheme uniformity → Tasks 5, 6
- §3 server compile/hash/store, source-cited errors, migration numbering → Tasks 3, 6
- §4 render/runner → Task 8 (refusal only; rest is slice (b) by design)
- §5 fly UX (dir import, iteration independence, --set-live, show summary) → Task 9
- §6 example deploy pipeline → slice (b)
- §7 hooks rejected at import → Task 2
- §9 testing strategy → per-task tests + Task 10 gate
