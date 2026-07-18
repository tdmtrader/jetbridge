# Workflow Skills Materialization (Slice B) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement slice (b) of `docs/superpowers/specs/2026-07-17-workflow-source-format-and-skills-design.md`: skills, system prompt, and context authored in workflow definitions actually reach the agent — renderer materializes skill trees into a `skills` artifact, the `AgentStep` schema carries the resolved text layers and skill names, the runner maps them into claude's discovery surface, the slice-(a) render refusal is removed, an example deploy pipeline ships, and a live theborg smoke proves the loop.

**Architecture:** The compiled `workflow.Config` (slice a) already holds resolved `SystemPrompt`, `ContextFiles`, and `SkillFiles`. The renderer computes each step's *effective* layers (workflow-global ∪ step-additive; step system-prompt replaces the workflow layer) into three new fields on `atc.AgentStep`/`atc.AgentPlan` (`SystemPrompt`, `Context`, `Skills` names), and emits one `write-skills` busybox task (same base64 mechanism as `write-ticket`) producing a `skills` artifact holding the union of referenced skill trees. The exec exports the §8.1 env rows (`AGENT_SYSTEM_PROMPT`, `AGENT_CONTEXT`, `AGENT_SKILLS`, `AGENT_SKILLS_DIR`); the runner copies each named skill from the mounted artifact into `<workdir>/.claude/skills/` (claude's CWD **is** the workdir, so these are project skills and the workspace repo's git tree stays clean for harvest), prepends the context block to the prompt, and passes the system prompt via `--append-system-prompt`.

**Tech Stack:** Go 1.25 (`os.CopyFS` available), plain `testing` in `agent/runner` + `agent/dispatch`, Ginkgo in `atc/exec` and `atc/builds`, busybox write-task in rendered pipelines. Live rollout: release pipeline on concourse.home (`fly -t home`), agent-runner image via home-infra tag bump (ArgoCD selfHeal — never `kubectl patch`).

**Key facts an engineer needs (verified against code, 2026-07-18):**
- Runner (`agent/runner/runner.go`): `FromEnv()` reads the §8.1 env; `Run()` execs `claude -p <prompt> --output-format json [--model][--max-turns] --dangerously-skip-permissions [--mcp-config]` with `cmd.Dir = cfg.WorkDir`.
- Exec (`atc/exec/agent_step.go:344-396`): `workdir := step.containerMetadata.WorkingDirectory`; env rows appended around line 351-383; inputs mount at `artifactPath(workdir, name, "")` — i.e. `<workdir>/<input-name>`.
- Planner (`atc/builds/planner.go:105` `VisitAgent`): field-by-field copy `atc.AgentStep` → `atc.AgentPlan`.
- Renderer (`agent/dispatch/render.go`): `RenderAgentStep` resolves per-step values; `Render` assembles the plan (`writeTicketTask` at the bottom is the base64 write-task pattern); the slice-(a) refusal to delete is the `SourceFormatField()` block after the judge refusal.
- Test styles: `agent/runner/runner_test.go` plain Go with `writeStubClaude(t, dir, envelope)` (a `#!/bin/sh` echo stub); `atc/exec/agent_step_test.go` Ginkgo with `Expect(spec.Env).To(ContainElement(...))`; `agent/dispatch/render_test.go` plain Go with `renderInput()` helper.

---

### Task 1: `atc` schema + planner — SystemPrompt/Context/Skills travel to the plan

**Files:**
- Modify: `atc/steps.go` (AgentStep struct, ~line 403)
- Modify: `atc/plan.go` (AgentPlan struct, ~line 415)
- Modify: `atc/builds/planner.go:105` (VisitAgent)
- Test: `atc/builds/planner_test.go` (extend the existing agent-step case)

- [ ] **Step 1: Find and extend the existing planner agent case**

Run: `grep -n "AgentStep\|AgentPlan" atc/builds/planner_test.go | head` to locate the agent-step planner spec. Add the three new fields to BOTH the input step and the expected plan in that spec:

In the input `atc.AgentStep` literal add:

```go
				SystemPrompt: "be careful",
				Context:      "## context/x.md\n\nbody\n",
				Skills:       []string{"tdd", "extra"},
```

In the expected `atc.AgentPlan` literal add:

```go
				SystemPrompt: "be careful",
				Context:      "## context/x.md\n\nbody\n",
				Skills:       []string{"tdd", "extra"},
```

- [ ] **Step 2: Run to verify it fails**

Run: `go build ./atc/... 2>&1 | head -5`
Expected: compile FAIL — `unknown field SystemPrompt in struct literal of type atc.AgentStep`

- [ ] **Step 3: Add the fields**

`atc/steps.go`, inside `AgentStep` after `OutputSchema`:

```go
	// Source-format layers (design 2026-07-17 §4), renderer-resolved to
	// literal values like Prompt: SystemPrompt is appended to the
	// runner's baseline system prompt; Context is a pre-concatenated
	// session-start block; Skills names select subtrees of the "skills"
	// input artifact for materialization into the agent's project
	// skill directory.
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Context      string   `json:"context,omitempty"`
	Skills       []string `json:"skills,omitempty"`
```

`atc/plan.go`, inside `AgentPlan` after `OutputSchema`:

```go
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Context      string   `json:"context,omitempty"`
	Skills       []string `json:"skills,omitempty"`
```

`atc/builds/planner.go` `VisitAgent`, add to the field copy:

```go
		SystemPrompt:   step.SystemPrompt,
		Context:        step.Context,
		Skills:         step.Skills,
```

- [ ] **Step 4: Run to verify pass**

Run: `go build ./atc/... && ginkgo ./atc/builds/ 2>&1 | tail -3`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add atc/steps.go atc/plan.go atc/builds/planner.go atc/builds/planner_test.go
git commit -m "feat(atc): SystemPrompt/Context/Skills on the agent step schema and plan"
```

---

### Task 2: exec — §8.1 env rows for the new layers

**Files:**
- Modify: `atc/exec/agent_step.go` (env assembly, after the `AGENT_OUTPUT_*` loop ~line 380)
- Test: `atc/exec/agent_step_test.go` (extend the env-assertion spec near line 236)

- [ ] **Step 1: Write the failing spec** — the file's idiom (see the spec "builds the container spec per the s8.1 env contract", line ~221): run `step.Run(ctx, state)`, then capture the spec via `fakePool.FindOrSelectWorkerArgsForCall(0)`. Plan mutations happen in a `Context`'s `BeforeEach` before the step is (re)built — mirror the `BASE_REF` env-static context near line 269. Deliberately do NOT add `skills` to `agentPlan.Inputs` here: the env rows derive from `plan.Skills` alone, and an unmounted input would fail the run with MissingInputsError (the renderer owns adding the input in production).

```go
	Context("with source-format layers on the plan", func() {
		BeforeEach(func() {
			agentPlan.SystemPrompt = "be careful"
			agentPlan.Context = "## context/x.md\n\nbody\n"
			agentPlan.Skills = []string{"tdd", "extra"}
		})

		It("exports them as §8.1 env", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
			Expect(spec.Env).To(ContainElements(
				"AGENT_SYSTEM_PROMPT=be careful",
				"AGENT_CONTEXT=## context/x.md\n\nbody\n",
				"AGENT_SKILLS=tdd,extra",
			))
			Expect(spec.Env).To(ContainElement("AGENT_SKILLS_DIR=some-artifact-root/skills"))
		})
	})
```

Place the `Context` at the same nesting level as the `BASE_REF` context so the shared `BeforeEach`/`JustBeforeEach` that builds `step` from `agentPlan` runs after the mutation. If the suite builds `step` in a plain `BeforeEach` at an outer level, follow whatever the `BASE_REF` context does — it solves the identical ordering problem.

- [ ] **Step 2: Run to verify it fails**

Run: `ginkgo --focus="source-format layers" ./atc/exec/ 2>&1 | tail -5`
Expected: FAIL — env rows absent (fields exist since Task 1, so it compiles)

- [ ] **Step 3: Implement** — in `atc/exec/agent_step.go`, immediately after the `AGENT_OUTPUT_*` loop (line ~380):

```go
	// Source-format layers (design 2026-07-17 §4): resolved text travels
	// like AGENT_PROMPT; skill CONTENT travels via the "skills" input
	// artifact, so only the selected names and the mount path go here.
	if step.plan.SystemPrompt != "" {
		env = append(env, "AGENT_SYSTEM_PROMPT="+step.plan.SystemPrompt)
	}
	if step.plan.Context != "" {
		env = append(env, "AGENT_CONTEXT="+step.plan.Context)
	}
	if len(step.plan.Skills) > 0 {
		env = append(env, "AGENT_SKILLS="+strings.Join(step.plan.Skills, ","))
		env = append(env, "AGENT_SKILLS_DIR="+artifactPath(workdir, "skills", ""))
	}
```

- [ ] **Step 4: Run to verify pass**

Run: `ginkgo --focus="source-format layers" ./atc/exec/ 2>&1 | tail -3` then the whole file's suite: `ginkgo --focus-file=agent_step_test.go ./atc/exec/ 2>&1 | tail -3`
Expected: PASS (note the pre-existing `atc/exec/artifact_input_step_test.go` vet failure is a known issue — if the suite trips on it, run with `--focus-file` as shown)

- [ ] **Step 5: Commit**

```bash
git add atc/exec/agent_step.go atc/exec/agent_step_test.go
git commit -m "feat(exec): export AGENT_SYSTEM_PROMPT/AGENT_CONTEXT/AGENT_SKILLS env"
```

---

### Task 3: runner — materialize skills, prepend context, append system prompt

**Files:**
- Modify: `agent/runner/runner.go` (Config, FromEnv, Run)
- Test: `agent/runner/runner_test.go` (three new tests + an arg-recording stub)

- [ ] **Step 1: Write the failing tests** (append to `runner_test.go`; the healthz-server pattern comes from `TestRunWritesFlightRecorder`):

```go
// writeRecordingStubClaude is writeStubClaude plus an args.txt capture so
// tests can assert the exact CLI invocation.
func writeRecordingStubClaude(t *testing.T, dir, envelope string) (claudePath, argsPath string) {
	t.Helper()
	claudePath = filepath.Join(dir, "claude")
	argsPath = filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsPath + "\necho '" + envelope + "'\n"
	if err := os.WriteFile(claudePath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return claudePath, argsPath
}

const okEnvelope = `{"type":"result","subtype":"success","result":"\"done\"","model":"m1","cost_usd":0.01,"num_turns":1,"is_error":false,"usage":{"input_tokens":1,"output_tokens":1}}`

func TestRunAppendsSystemPromptAndPrependsContext(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)
	claude, argsPath := writeRecordingStubClaude(t, dir, okEnvelope)

	exit, err := runner.Run(context.Background(), runner.Config{
		Prompt:       "do it",
		SystemPrompt: "be careful",
		Context:      "## context/x.md\n\nbody\n",
		FlightDir:    flight, WorkDir: dir, StepName: "s", ClaudePath: claude,
	})
	if err != nil || exit != 0 {
		t.Fatalf("run: exit %d err %v", exit, err)
	}

	raw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	// -p <prompt>: context block prepended, original prompt at the end.
	promptIdx := -1
	for i, a := range args {
		if a == "-p" {
			promptIdx = i + 1
		}
	}
	if promptIdx < 0 || promptIdx >= len(args) {
		t.Fatalf("no -p arg captured: %v", args)
	}
	prompt := args[promptIdx]
	if !strings.HasPrefix(prompt, "# Workflow context") || !strings.Contains(prompt, "## context/x.md") || !strings.HasSuffix(prompt, "do it") {
		t.Fatalf("context not prepended: %q", prompt)
	}

	// --append-system-prompt <value>
	sysIdx := -1
	for i, a := range args {
		if a == "--append-system-prompt" {
			sysIdx = i + 1
		}
	}
	if sysIdx < 0 || args[sysIdx] != "be careful" {
		t.Fatalf("system prompt not appended: %v", args)
	}
}

func TestRunMaterializesSkills(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)
	claude, _ := writeRecordingStubClaude(t, dir, okEnvelope)

	skillsDir := filepath.Join(dir, "skills")
	for _, f := range []string{"tdd/SKILL.md", "tdd/refs/a.md", "extra/SKILL.md", "unselected/SKILL.md"} {
		full := filepath.Join(skillsDir, f)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte("# "+f), 0o644)
	}

	exit, err := runner.Run(context.Background(), runner.Config{
		Prompt: "p", FlightDir: flight, WorkDir: dir, StepName: "s", ClaudePath: claude,
		Skills: []string{"tdd", "extra"}, SkillsDir: skillsDir,
	})
	if err != nil || exit != 0 {
		t.Fatalf("run: exit %d err %v", exit, err)
	}

	for _, want := range []string{"tdd/SKILL.md", "tdd/refs/a.md", "extra/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(dir, ".claude", "skills", want)); err != nil {
			t.Errorf("skill file not materialized: %s (%v)", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "skills", "unselected")); err == nil {
		t.Error("unselected skill must not be materialized")
	}
}

func TestRunFailsOnMissingSkill(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)
	claude, argsPath := writeRecordingStubClaude(t, dir, okEnvelope)

	exit, _ := runner.Run(context.Background(), runner.Config{
		Prompt: "p", FlightDir: flight, WorkDir: dir, StepName: "s", ClaudePath: claude,
		Skills: []string{"ghost"}, SkillsDir: filepath.Join(dir, "skills"),
	})
	if exit != 2 {
		t.Fatalf("missing skill must be a platform error (exit 2), got %d", exit)
	}
	if _, err := os.Stat(argsPath); err == nil {
		t.Fatal("claude must not run when a skill is missing")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./agent/runner/ -run 'TestRunAppends|TestRunMaterializes|TestRunFailsOnMissingSkill' -count=1 2>&1 | head -6`
Expected: compile FAIL — `unknown field SystemPrompt in struct literal of type runner.Config`

- [ ] **Step 3: Implement** in `agent/runner/runner.go`:

3a. `Config` — add after `OutputSchema`:

```go
	// Source-format layers (design 2026-07-17 §4).
	SystemPrompt string   // appended to claude's baseline via --append-system-prompt
	Context      string   // pre-concatenated block, injected at session start (prompt prefix)
	Skills       []string // skill names to materialize from SkillsDir
	SkillsDir    string   // mount path of the "skills" input artifact
```

3b. `FromEnv` — add after the `OutputSchema` line:

```go
		SystemPrompt: os.Getenv("AGENT_SYSTEM_PROMPT"),
		Context:      os.Getenv("AGENT_CONTEXT"),
		SkillsDir:    os.Getenv("AGENT_SKILLS_DIR"),
```

and after the `AGENT_MAX_TURNS` block:

```go
	if v := os.Getenv("AGENT_SKILLS"); v != "" {
		cfg.Skills = strings.Split(v, ",")
	}
```

3c. `Run` — after the prompt is resolved (right after the `if prompt == "" { return 2, ... }` guard) and BEFORE the flight recorder opens, add the skill materialization and context/system-prompt handling. Materialization failure is a platform error (exit 2) and must happen before claude can run:

```go
	// Materialize the step's selected skills from the mounted "skills"
	// artifact into <workdir>/.claude/skills — claude's CWD is WorkDir,
	// so these are its project skills; the workspace repo's git tree is
	// untouched (design 2026-07-17 §4). A missing skill means the
	// renderer and runner disagree about the artifact — platform error.
	for _, name := range cfg.Skills {
		src := filepath.Join(cfg.SkillsDir, name)
		if _, err := os.Stat(src); err != nil {
			return 2, fmt.Errorf("skill %q not present in skills artifact %s: %w", name, cfg.SkillsDir, err)
		}
		dst := filepath.Join(cfg.WorkDir, ".claude", "skills", name)
		if _, err := os.Stat(dst); err == nil {
			fmt.Fprintf(stderr, "agent-runner: overwriting existing project skill %q with the workflow's copy\n", name)
			if err := os.RemoveAll(dst); err != nil {
				return 2, fmt.Errorf("replace project skill %q: %w", name, err)
			}
		}
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return 2, fmt.Errorf("create skill dir %q: %w", name, err)
		}
		if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
			return 2, fmt.Errorf("materialize skill %q: %w", name, err)
		}
	}

	// Session-start context (design §4): superpowers-style injection,
	// done platform-side — the block precedes the step prompt.
	if cfg.Context != "" {
		prompt = "# Workflow context\n\n" + cfg.Context + "\n\n---\n\n" + prompt
	}
```

3d. In the claude arg assembly (the `args := []string{"-p", prompt, ...}` block), after the `--max-turns` append:

```go
	if cfg.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", cfg.SystemPrompt)
	}
```

Note ordering: 3c must run before the `args` slice is built so the mutated `prompt` is what `-p` carries — keep the materialization/context block between prompt resolution and the flight-recorder open, and the arg append inside the existing arg-assembly section.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./agent/runner/ -count=1 2>&1 | tail -2`
Expected: PASS (all existing runner tests still green — new env vars absent means empty Config fields, all paths no-op)

- [ ] **Step 5: Commit**

```bash
git add agent/runner/runner.go agent/runner/runner_test.go
git commit -m "feat(runner): materialize skills, prepend context, append system prompt"
```

---

### Task 4: renderer — effective layers, write-skills task, refusal removed

**Files:**
- Modify: `agent/dispatch/render.go`
- Modify: `agent/workflow/parse.go` (reserve `skills` as a renderer-provided input name)
- Test: `agent/dispatch/render_test.go`, `agent/workflow/parse_v2_test.go`

- [ ] **Step 1: Write the failing tests**

4a. Replace `TestRenderRefusesSourceFormatSurfaces` in `agent/dispatch/render_test.go` entirely with:

```go
// sourceFormatInput returns a renderInput whose workflow uses every
// source-format surface, as the compiled Config (slice a) would deliver
// it: *_file references already resolved, SkillFiles/ContextFiles
// populated.
func sourceFormatInput() dispatch.RenderInput {
	in := renderInput()
	in.Workflow.SchemaVersion = 2
	in.Workflow.SystemPrompt = "workflow system prompt"
	in.Workflow.Skills = []string{"tdd"}
	in.Workflow.Context = []string{"context/conventions.md"}
	in.Workflow.ContextFiles = map[string]string{
		"context/conventions.md": "conventions body",
		"context/step.md":        "step body",
	}
	in.Workflow.SkillFiles = map[string]string{
		"skills/tdd/SKILL.md":    "# tdd",
		"skills/tdd/refs/a.md":   "supporting",
		"skills/extra/SKILL.md":  "# extra",
	}
	in.Workflow.Steps[0].Skills = []string{"extra"}
	in.Workflow.Steps[0].SystemPrompt = "step system prompt"
	in.Workflow.Steps[0].Context = []string{"context/step.md"}
	return in
}

func TestRenderSourceFormatSurfaces(t *testing.T) {
	cfg, err := dispatch.Render(sourceFormatInput())
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	plan := cfg.Jobs[0].PlanSequence

	// A write-skills task materializes the union of referenced skill
	// trees as the "skills" artifact, before the agent steps.
	var writeSkills *atc.TaskStep
	agentIdx, taskIdx := -1, -1
	var agent *atc.AgentStep
	for i, s := range plan {
		if ts, ok := s.Config.(*atc.TaskStep); ok && ts.Name == "write-skills" {
			writeSkills, taskIdx = ts, i
		}
		if as, ok := s.Config.(*atc.AgentStep); ok && agent == nil {
			agent, agentIdx = as, i
		}
	}
	if writeSkills == nil {
		t.Fatal("no write-skills task emitted")
	}
	if taskIdx > agentIdx {
		t.Fatal("write-skills must precede the agent steps")
	}
	script := writeSkills.Config.Run.Args[1]
	for _, frag := range []string{"tdd/SKILL.md", "tdd/refs/a.md", "extra/SKILL.md"} {
		if !strings.Contains(script, frag) {
			t.Errorf("write-skills script missing %s", frag)
		}
	}
	if len(writeSkills.Config.Outputs) != 1 || writeSkills.Config.Outputs[0].Name != "skills" {
		t.Fatalf("write-skills must output the skills artifact: %+v", writeSkills.Config.Outputs)
	}

	// The agent step carries the resolved layers and the skills input.
	if agent.SystemPrompt != "step system prompt" {
		t.Errorf("step system prompt must replace the workflow layer: %q", agent.SystemPrompt)
	}
	if !strings.Contains(agent.Context, "## context/conventions.md") || !strings.Contains(agent.Context, "## context/step.md") ||
		!strings.Contains(agent.Context, "conventions body") {
		t.Errorf("context block not assembled: %q", agent.Context)
	}
	if len(agent.Skills) != 2 || agent.Skills[0] != "tdd" || agent.Skills[1] != "extra" {
		t.Errorf("effective skills = %v", agent.Skills)
	}
	hasSkillsInput := false
	for _, in := range agent.Inputs {
		if in == "skills" {
			hasSkillsInput = true
		}
	}
	if !hasSkillsInput {
		t.Errorf("agent step must consume the skills artifact: %v", agent.Inputs)
	}
}

func TestRenderWorkflowSystemPromptFallback(t *testing.T) {
	in := sourceFormatInput()
	in.Workflow.Steps[0].SystemPrompt = ""
	cfg, err := dispatch.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range cfg.Jobs[0].PlanSequence {
		if as, ok := s.Config.(*atc.AgentStep); ok {
			if as.SystemPrompt != "workflow system prompt" {
				t.Fatalf("workflow system prompt must apply when the step has none: %q", as.SystemPrompt)
			}
			return
		}
	}
	t.Fatal("no agent step")
}

func TestRenderRefusesOversizeSkills(t *testing.T) {
	in := sourceFormatInput()
	in.Workflow.SkillFiles["skills/tdd/big.md"] = strings.Repeat("a", 600_000)
	if _, err := dispatch.Render(in); err == nil || !strings.Contains(err.Error(), "skill") {
		t.Fatalf("oversize skill set must refuse at render: %v", err)
	}
}
```

4b. In `agent/workflow/parse_v2_test.go` add:

```go
func TestParseAllowsExplicitSkillsInput(t *testing.T) {
	doc := strings.Replace(v2YAML, "  context: [context/tdd.md]\n  outputs: [workspace]",
		"  context: [context/tdd.md]\n  inputs: [skills]\n  outputs: [workspace]", 1)
	if _, err := workflow.Parse([]byte(doc)); err != nil {
		t.Fatalf("skills is a renderer-provided reserved input: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failures**

Run: `go test ./agent/dispatch/ -run TestRenderSourceFormat -count=1 2>&1 | head -5` and `go test ./agent/workflow/ -run TestParseAllowsExplicitSkillsInput -count=1 2>&1 | tail -3`
Expected: dispatch FAIL (render currently refuses); workflow FAIL (`input "skills" is not produced by an earlier step`)

- [ ] **Step 3: Implement**

3a. `agent/workflow/parse.go` — extend the reserved-artifact seeding (the `produced := map[string]bool{"repo": true, "ticket": true}` line):

```go
	// "repo", "ticket", and "skills" are renderer-provided reserved
	// artifacts: the ticket's git checkout, the spec.md/plan.md
	// files-delivery input, and the materialized skill trees (design
	// 2026-07-17 §4). Steps may consume them without an earlier producer.
	produced := map[string]bool{"repo": true, "ticket": true, "skills": true}
```

3b. `agent/dispatch/render.go`:

Delete the slice-(a) refusal block (the `if field := in.Workflow.SourceFormatField(); field != "" {...}` added after the judge refusal).

Add the render-side size guard constant near the top of the file:

```go
// maxSkillsRenderBytes bounds the base64 write-skills task payload. The
// spec's escape hatch for larger sets is an authenticated
// fetch-by-version endpoint (design 2026-07-17 §4) — refuse loudly
// until someone needs it.
const maxSkillsRenderBytes = 512 << 10
```

Add the effective-layer helpers (near `harvestGatePolicy`):

```go
// effectiveSkills is the step's materialization set: workflow-global
// then step-additional, first occurrence wins (design §1 semantics —
// additive, never replacing).
func effectiveSkills(wf workflow.Config, step workflow.Step) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, name := range append(append([]string{}, wf.Skills...), step.Skills...) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// effectiveContext assembles the session-start block: workflow-global
// files then step additions, each under a "## <path>" header, deduped
// on path.
func effectiveContext(wf workflow.Config, step workflow.Step) string {
	seen := map[string]bool{}
	var b strings.Builder
	for _, path := range append(append([]string{}, wf.Context...), step.Context...) {
		if seen[path] {
			continue
		}
		seen[path] = true
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", path, wf.ContextFiles[path])
	}
	return b.String()
}

// effectiveSystemPrompt: the step layer replaces the workflow layer
// (never the runner baseline — the runner appends whichever value wins).
func effectiveSystemPrompt(wf workflow.Config, step workflow.Step) string {
	if step.SystemPrompt != "" {
		return step.SystemPrompt
	}
	return wf.SystemPrompt
}
```

In `RenderAgentStep`, extend the returned `atc.AgentStep` literal:

```go
		SystemPrompt:   effectiveSystemPrompt(in.Workflow, step),
		Context:        effectiveContext(in.Workflow, step),
		Skills:         effectiveSkills(in.Workflow, step),
```

and after computing them, ensure the skills input rides along — replace `Inputs: step.Inputs,` with `Inputs: inputs,` where, above the literal:

```go
	inputs := step.Inputs
	if len(effectiveSkills(in.Workflow, step)) > 0 {
		has := false
		for _, name := range inputs {
			if name == "skills" {
				has = true
			}
		}
		if !has {
			inputs = append(append([]string{}, inputs...), "skills")
		}
	}
```

In `Render`, in the input-accounting `switch` (cases `input == "repo"`, `input == "ticket"`, `available[input]`, default error), add a new case **immediately after the `ticket` case and before `available[input]`** so an explicit `skills` input is only legal when the workflow actually has skills:

```go
			case input == "skills":
				if len(in.Workflow.SkillFiles) == 0 {
					return atc.Config{}, fmt.Errorf("agent step %q consumes the skills artifact but the workflow references no skills", step.Agent)
				}
```

Still in `Render`, after the write-ticket emission (`if needsTicket {...}`), emit the write-skills task when any skill is referenced:

```go
	if len(in.Workflow.SkillFiles) > 0 {
		task, err := writeSkillsTask(in.Workflow)
		if err != nil {
			return atc.Config{}, err
		}
		plan = append(plan, atc.Step{Config: task})
	}
```

and add the task builder next to `writeTicketTask`:

```go
// writeSkillsTask materializes the union of referenced skill trees as
// the "skills" artifact — same base64-through-busybox mechanism as
// writeTicketTask, one write per file. SkillFiles keys are manifest
// paths ("skills/tdd/SKILL.md"); inside the artifact the leading
// "skills/" segment is dropped so the runner sees <name>/SKILL.md.
func writeSkillsTask(wf workflow.Config) (*atc.TaskStep, error) {
	total := 0
	for _, content := range wf.SkillFiles {
		total += len(content)
	}
	if total > maxSkillsRenderBytes {
		return nil, fmt.Errorf("workflow skill files total %d bytes (max %d for base64 materialization; larger sets need the fetch-by-version endpoint, design §4)", total, maxSkillsRenderBytes)
	}

	paths := make([]string, 0, len(wf.SkillFiles))
	for path := range wf.SkillFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var script strings.Builder
	script.WriteString("set -eu\n")
	for _, path := range paths {
		rel := strings.TrimPrefix(path, "skills/")
		encoded := base64.StdEncoding.EncodeToString([]byte(wf.SkillFiles[path]))
		fmt.Fprintf(&script, "mkdir -p \"skills/%s\"\n", filepath.Dir(rel))
		fmt.Fprintf(&script, "echo %s | base64 -d > \"skills/%s\"\n", encoded, rel)
	}
	script.WriteString("find skills -type f | head -50\n")

	return &atc.TaskStep{
		Name: "write-skills",
		Config: &atc.TaskConfig{
			Platform: "linux",
			ImageResource: &atc.ImageResource{
				Type:   "registry-image",
				Source: atc.Source{"repository": "busybox"},
			},
			Outputs: []atc.TaskOutputConfig{{Name: "skills"}},
			Run: atc.TaskRunConfig{
				Path: "sh",
				Args: []string{"-ec", script.String()},
			},
		},
	}, nil
}
```

(`render.go` needs `"path/filepath"` and `"sort"` added to its imports. Manifest paths are slash-separated and validated — `filepath.Dir` is safe on linux web nodes; the paths cannot contain `"`, spaces are legal and the quotes in the script handle them.)

- [ ] **Step 4: Run to verify pass**

Run: `go test ./agent/dispatch/ ./agent/workflow/ -count=1 2>&1 | tail -3`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/dispatch/render.go agent/dispatch/render_test.go agent/workflow/parse.go agent/workflow/parse_v2_test.go
git commit -m "feat(dispatch): render source-format layers — write-skills artifact, resolved env, refusal removed"
```

---

### Task 5: contracts §8.1 note + example deploy pipeline

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (§8.1 env table)
- Create: `deploy/workflows-deploy-pipeline.yml`

- [ ] **Step 1: Amend §8.1** — locate the env contract: `grep -n "AGENT_PROMPT" docs/superpowers/plans/agentic-platform/00-shared-contracts.md`. Add four rows in the same format as the existing `AGENT_PROMPT` / `AGENT_OUTPUT_<NAME>` entries, with an amendment marker:

- `AGENT_SYSTEM_PROMPT` — resolved system-prompt layer; runner passes via `--append-system-prompt` (2026-07-18 source-format slice b).
- `AGENT_CONTEXT` — pre-concatenated session-start context block; runner prepends to the prompt (2026-07-18).
- `AGENT_SKILLS` — comma-joined skill names the runner materializes (2026-07-18).
- `AGENT_SKILLS_DIR` — absolute mount path of the `skills` input artifact (2026-07-18).

- [ ] **Step 2: Write the example deploy pipeline** `deploy/workflows-deploy-pipeline.yml`:

```yaml
# Example: continuous deployment for workflow definitions (design
# 2026-07-17 §6 — pipelines-that-deploy; this is an EXAMPLE, not a
# platform component). Point `workflows-repo` at a repo containing one
# or more workflow source directories (each with a workflow.yml), set
# it live with `fly set-pipeline`, and every merge imports new
# versions. Auto-promotion is the pipeline author's policy: keep
# --set-live for merge-implies-promote, drop it to promote manually
# with `fly agent workflows set-live`.
#
# Required vars/secrets:
#   ((workflows-repo-uri))    git URI of the workflows repo
#   ((concourse-url))         external URL of this Concourse
#   ((concourse-username))    local user for fly login
#   ((concourse-password))
resources:
- name: workflows-repo
  type: git
  source:
    uri: ((workflows-repo-uri))
    branch: main

jobs:
- name: deploy-workflows
  plan:
  - get: workflows-repo
    trigger: true
  - task: import
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: concourse/concourse}
      inputs:
      - name: workflows-repo
      params:
        CONCOURSE_URL: ((concourse-url))
        CONCOURSE_USERNAME: ((concourse-username))
        CONCOURSE_PASSWORD: ((concourse-password))
      run:
        path: sh
        args:
        - -ec
        - |
          fly -t deploy login -c "$CONCOURSE_URL" \
            -u "$CONCOURSE_USERNAME" -p "$CONCOURSE_PASSWORD"
          fly -t deploy agent workflows import workflows-repo/ --set-live
```

- [ ] **Step 3: Sanity-check the YAML**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('deploy/workflows-deploy-pipeline.yml')); print('yaml ok')"`
Expected: `yaml ok`

- [ ] **Step 4: Commit**

```bash
git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md deploy/workflows-deploy-pipeline.yml
git commit -m "docs(contracts)+deploy: §8.1 source-format env rows; example workflows deploy pipeline"
```

---

### Task 6: full local verification

- [ ] **Step 1: Suites**

```bash
go build ./...
go test ./agent/... -count=1
ginkgo --focus-file=agent_step_test.go ./atc/exec/
ginkgo ./atc/builds/
make test-fly-integration
make test-quick
```

Expected: all green (known pre-existing issues: `artifact_input_step_test.go` vet failure, gardenruntime BeforeSuite port conflict, gofmt drift in untouched files).

- [ ] **Step 2: gofmt scope check**

Run: `gofmt -l atc/steps.go atc/plan.go atc/builds/planner.go atc/exec/agent_step.go agent/runner/runner.go agent/dispatch/render.go agent/workflow/parse.go`
Expected: no output.

- [ ] **Step 3: Commit anything outstanding** (there should be nothing — each task committed).

---

### Task 7: live rollout + theborg smoke (operator task — coordinate spend and deploy timing)

This task runs against the live cluster. Checkpoints marked **[CONFIRM]** need the user (shared rate-limit window; deploy timing rules: push → release settles → dispatch; never `kubectl patch` images — bump home-infra, ArgoCD selfHeal reverts patches).

- [ ] **Step 1: Create the smoke workflow source dir** `deploy/workflows/skills-smoke/`:

`deploy/workflows/skills-smoke/workflow.yml`:

```yaml
schema_version: 2
name: skills-smoke
description: slice-b smoke — proves skills/system-prompt/context materialize
spec_delivery: files

defaults:
  model: claude-sonnet-5
  max_turns: 12

budget:
  ticket_usd: 1.0

skills: [marker]
system_prompt_file: system/base.md
context: [context/note.md]

prompt_files:
  work: prompts/work.md

steps:
- agent: work
  prompt: work
  inputs: [repo, ticket]
  outputs: [workspace]
```

`deploy/workflows/skills-smoke/system/base.md`:

```markdown
You are running as a platform smoke test. Be terse; do not explore beyond the instructions.
```

`deploy/workflows/skills-smoke/context/note.md`:

```markdown
Smoke context: the platform materialized this file from the workflow definition. Mention the word CONTEXT-SEEN once in your final message.
```

`deploy/workflows/skills-smoke/prompts/work.md`:

```markdown
Copy the repo into the workspace preserving git metadata: `cp -a repo/. workspace/`.
Consult your available project skills and follow the marker skill exactly.
Then, in workspace/, create the file it tells you to create, and commit it:
`git -C workspace add -A && git -C workspace -c user.email=agent@jetbridge -c user.name=jetbridge-agent commit -m "skills smoke marker"`.
Finish with a one-line summary.
```

`deploy/workflows/skills-smoke/skills/marker/SKILL.md`:

```markdown
---
name: marker
description: Slice-b smoke marker skill — proves skill materialization end to end
---

When asked to create a marker file, create `SKILLS_SMOKE.md` at the
workspace root containing exactly one line: `SKILL-MARKER-OK`.
Always end your final message with the exact token: SKILL-MARKER-OK
```

Commit:

```bash
git add deploy/workflows/skills-smoke/
git commit -m "feat(deploy): skills-smoke workflow source dir for the slice-b live smoke"
```

- [ ] **Step 2: Local render sanity** — before any live spend, prove the full local pipeline: import into a scratch store via the unit path is already covered; additionally run `fly` packaging against the real dir:

```bash
go run ./cmd/concourse --version >/dev/null 2>&1 || true   # no-op warmup
go test ./agent/dispatch/ -run TestRenderSourceFormat -count=1
```

and compile-check the smoke dir exactly as fly will:

```bash
cat > /tmp/smoke_compile_check.go <<'EOF'
package main

import (
	"fmt"
	"os"

	"github.com/concourse/concourse/agent/workflow"
)

func main() {
	m, err := workflow.ManifestFromDir("deploy/workflows/skills-smoke")
	if err != nil {
		fmt.Println("manifest:", err)
		os.Exit(1)
	}
	cfg, err := workflow.Compile(m)
	if err != nil {
		fmt.Println("compile:", err)
		os.Exit(1)
	}
	fmt.Printf("ok: %s skills=%v hash=%.12s\n", cfg.Name, cfg.Skills, m.Hash())
}
EOF
go run /tmp/smoke_compile_check.go
```

Expected: `ok: skills-smoke skills=[marker] hash=...`

- [ ] **Step 3 [CONFIRM]: Push and release** — push `jetbridge`, let the release pipeline build (web + agent-runner image via `build-agent-runner-image`), bump the agent-runner tag in home-infra `apps/concourse.yaml`, wait for ArgoCD sync + web rollout to settle (retry Bad Gateway after "successfully rolled out" — ingress lags).

- [ ] **Step 4: Import + set-live on theborg**

```bash
go build -o /tmp/fly ./fly    # jetbridge image ships no darwin-arm64 fly
/tmp/fly -t home agent workflows import deploy/workflows/skills-smoke/ --set-live
/tmp/fly -t home agent workflows show skills-smoke
```

Expected: `imported skills-smoke version 1 ...` + `workflow skills-smoke version 1 is now live`; show prints workflow.yml plus a 5-file source summary.

- [ ] **Step 5 [CONFIRM — spends from the shared window, ~$0.20]: Dispatch the smoke ticket**

```bash
/tmp/fly -t home agent tickets create --workflow skills-smoke \
  --title "slice-b skills smoke" \
  --body "Prove skills/system-prompt/context materialization. Follow the workflow prompt." \
  --agent-repo tdmtrader/jetbridge --target-branch jetbridge --dispatch
/tmp/fly -t home agent tickets watch --id <N>
```

(Exact create flags: check `fly agent tickets create --help` — the repo/target-branch flag names landed with manual dispatch; `--target-branch jetbridge` matters, default main builds against main.)

- [ ] **Step 6: Verify**

```bash
/tmp/fly -t home agent runs | head -5        # run row for ticket N, summary ends SKILL-MARKER-OK
/tmp/fly -t home agent tickets show --id <N> # needs_review, branch agent/ticket-<N>
git fetch origin agent/ticket-<N> && git show FETCH_HEAD --stat | head  # SKILLS_SMOKE.md in the commit
git show FETCH_HEAD:SKILLS_SMOKE.md          # exactly "SKILL-MARKER-OK"
```

Also check the run summary mentions CONTEXT-SEEN (context injection) — the skill marker proves skills; CONTEXT-SEEN proves the context block; both flowing through `--append-system-prompt` terseness is observational only.

- [ ] **Step 7: Close out**

```bash
/tmp/fly -t home agent tickets close --id <N> --disposition concluded
git push origin --delete agent/ticket-<N>    # smoke branch, no merge intended
```

Update the spec's §8 rollout section: mark slice (b) landed with the smoke evidence (ticket number, run cost), commit.

---

## Out of scope

Fetch-by-version endpoint for large skill sets (render refuses >512 KiB with a pointing error), hooks, output_schema enforcement, non-claude runner mappings (the env contract is provider-neutral; only the claude runner exists), Elm/UI surfacing of skills.

## Spec-coverage map (self-review)

- §4 renderer materialization → Task 4 (write-skills artifact + effective sets)
- §4 AgentStep schema + §8.1 env family → Tasks 1, 2 (+ Task 5 contract doc)
- §4 runner mapping (claude `.claude/skills`, system-prompt append, session-start context) → Task 3
- §4 shadowing log → Task 3 (overwrite log; per pod layout the workdir-level `.claude` is empty at start, so collisions are re-runs)
- §4 refusal removal ("until the render/runner slice lands") → Task 4
- §6 example deploy pipeline → Task 5
- §8 slice (b) live smoke on theborg → Task 7
- Trust boundary (agent pods only, never harvest) → structural: harvest exec/runner consume no AgentStep fields and no skills artifact; nothing to change
