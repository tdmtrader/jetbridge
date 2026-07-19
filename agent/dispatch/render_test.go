package dispatch_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
)

func renderInput() dispatch.RenderInput {
	version := 3
	return dispatch.RenderInput{
		Workflow: workflow.Config{
			Name:         "smoke",
			SpecDelivery: "files",
			Defaults:     workflow.Defaults{Model: "claude-sonnet-5", MaxTurns: 80},
			Prompts: map[string]string{
				"do": "Do the thing described in ticket/spec.md.",
			},
			Schemas: map[string]string{
				"result": `{"type":"object"}`,
			},
			Steps: []workflow.Step{
				{Agent: "implement", Prompt: "do", BudgetSliceUSD: 2.0,
					Inputs: []string{"repo", "ticket"}, Outputs: []string{"workspace"}},
			},
		},
		WorkflowName:    "smoke",
		WorkflowVersion: 3,
		WorkflowHash:    "abc123",
		Ticket: tickets.Ticket{
			ID: 42, Title: "fix X", Body: "details", Origin: "fly",
			Repo: "tdmtrader/jetbridge", TargetBranch: "main",
			WorkflowName: "smoke", WorkflowVersion: &version,
		},
		ATCExternalURL: "http://concourse.home",
		RepoBaseURL:    "https://github.com",
	}
}

func TestRenderAgentStepResolvesPromptDefaultsAndIdentityEnv(t *testing.T) {
	in := renderInput()
	step := in.Workflow.Steps[0]

	got, err := dispatch.RenderAgentStep(in, step)
	if err != nil {
		t.Fatalf("RenderAgentStep: %v", err)
	}
	if got.Name != "implement" {
		t.Errorf("name = %q", got.Name)
	}
	if got.Prompt != "Do the thing described in ticket/spec.md." {
		t.Errorf("prompt not inlined: %q", got.Prompt)
	}
	if got.Model != "claude-sonnet-5" || got.MaxTurns != 80 {
		t.Errorf("defaults not applied: model=%q turns=%d", got.Model, got.MaxTurns)
	}
	if got.BudgetSliceUSD != 2.0 {
		t.Errorf("budget slice = %v", got.BudgetSliceUSD)
	}
	if got.Env["AGENT_TICKET_ID"] != "42" ||
		got.Env["AGENT_WORKFLOW_NAME"] != "smoke" ||
		got.Env["AGENT_WORKFLOW_VERSION"] != "3" ||
		got.Env["AGENT_WORKFLOW_HASH"] != "abc123" ||
		got.Env["AGENT_BUDGET_SLICE_USD"] != "2" ||
		got.Env["ATC_EXTERNAL_URL"] != "http://concourse.home" {
		t.Errorf("identity/provenance env wrong: %+v", got.Env)
	}
	// F30: the run id is the ((run_id)) reserved var — pipeline_runs.id,
	// interpolated at CreateRun materialization, NOT the per-template
	// ((run)) number.
	if got.Env["AGENT_PIPELINE_RUN_ID"] != "((run_id))" {
		t.Errorf("AGENT_PIPELINE_RUN_ID = %q, want ((run_id))", got.Env["AGENT_PIPELINE_RUN_ID"])
	}
	if len(got.Inputs) != 2 || len(got.Outputs) != 1 || got.Outputs[0] != "workspace" {
		t.Errorf("inputs/outputs wrong: %+v / %+v", got.Inputs, got.Outputs)
	}
}

func TestRenderAgentStepExecutesPromptTemplates(t *testing.T) {
	// §6.2: prompts are Go text/templates rendered against the frozen
	// {Ticket, Spec, Tasks, Params} context. The import gate validates
	// with tolerant maps (unknown fields render "<no value>", only a
	// .Spec nil-deref blocks import) — the real context mirrors that:
	// JSON-shaped maps, snake_case keys.
	in := renderInput()
	in.Workflow.Prompts["do"] = "Work ticket #{{.Ticket.id}}: {{.Ticket.title}}.{{if .Spec}} Spec: {{.Spec.title}}.{{end}}{{range .Tasks}} [{{.title}}]{{end}}"

	got, err := dispatch.RenderAgentStep(in, in.Workflow.Steps[0])
	if err != nil {
		t.Fatalf("RenderAgentStep: %v", err)
	}
	if got.Prompt != "Work ticket #42: fix X." {
		t.Errorf("spec-less render = %q", got.Prompt)
	}

	in.Spec = &tickets.Spec{Version: 1, Title: "the spec", Body: "b"}
	in.PlanTasks = []tickets.Task{{PlanVersion: 1, Ordering: 1, Title: "one", Status: tickets.TaskPending}}
	got, err = dispatch.RenderAgentStep(in, in.Workflow.Steps[0])
	if err != nil {
		t.Fatalf("RenderAgentStep with spec: %v", err)
	}
	if got.Prompt != "Work ticket #42: fix X. Spec: the spec. [one]" {
		t.Errorf("spec-ful render = %q", got.Prompt)
	}

	// unknown envelope fields are tolerant, matching the import gate
	in.Workflow.Prompts["do"] = "x{{.Ticket.mystery}}y"
	got, err = dispatch.RenderAgentStep(in, in.Workflow.Steps[0])
	if err != nil {
		t.Fatalf("unknown field must not error: %v", err)
	}
	if got.Prompt != "x<no value>y" {
		t.Errorf("tolerant render = %q", got.Prompt)
	}
}

func TestRenderAgentStepStepOverridesBeatDefaults(t *testing.T) {
	in := renderInput()
	step := in.Workflow.Steps[0]
	step.Model = "claude-fable-5"
	step.MaxTurns = 10
	step.OutputSchema = "result"

	got, err := dispatch.RenderAgentStep(in, step)
	if err != nil {
		t.Fatalf("RenderAgentStep: %v", err)
	}
	if got.Model != "claude-fable-5" || got.MaxTurns != 10 {
		t.Errorf("overrides lost: model=%q turns=%d", got.Model, got.MaxTurns)
	}
	if got.OutputSchema != `{"type":"object"}` {
		t.Errorf("output schema not inlined from Schemas: %q", got.OutputSchema)
	}
}

func TestRenderAgentStepV0Refusals(t *testing.T) {
	in := renderInput()

	missing := in.Workflow.Steps[0]
	missing.Prompt = "nope"
	if _, err := dispatch.RenderAgentStep(in, missing); err == nil {
		t.Error("missing prompt key must error")
	}

	noPrompt := in.Workflow.Steps[0]
	noPrompt.Prompt = ""
	if _, err := dispatch.RenderAgentStep(in, noPrompt); err == nil {
		t.Error("empty prompt key must error")
	}

	badSchema := in.Workflow.Steps[0]
	badSchema.OutputSchema = "nope"
	if _, err := dispatch.RenderAgentStep(in, badSchema); err == nil {
		t.Error("missing schema key must error")
	}

	// v0 refusals: wave-3 surfaces are not deployed — fail loudly at
	// render time instead of producing a run that cannot execute.
	sidecars := in.Workflow.Steps[0]
	sidecars.Sidecars = []string{"platform"}
	if _, err := dispatch.RenderAgentStep(in, sidecars); err == nil {
		t.Error("sidecar refs must error in v0 (wave-3 images not deployed)")
	}

	checkpoint := in.Workflow.Steps[0]
	checkpoint.Checkpoint = "review the spec"
	if _, err := dispatch.RenderAgentStep(in, checkpoint); err == nil {
		t.Error("checkpoints must error in v0 (platform-mcp not deployed)")
	}
}

func TestRenderProducesValidTemplateConfig(t *testing.T) {
	in := renderInput()
	spec := &tickets.Spec{Version: 1, Title: "the spec", Body: "spec body"}
	in.Spec = spec
	in.PlanTasks = []tickets.Task{{PlanVersion: 1, Ordering: 1, Title: "one", Status: tickets.TaskPending}}

	cfg, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !cfg.Template {
		t.Error("rendered config must be a template pipeline")
	}
	warnings, errs := configvalidate.Validate(cfg)
	if len(errs) > 0 {
		t.Fatalf("configvalidate errors: %v (warnings %v)", errs, warnings)
	}

	if len(cfg.Resources) != 1 || cfg.Resources[0].Name != "repo" || cfg.Resources[0].Type != "git" {
		t.Fatalf("repo resource wrong: %+v", cfg.Resources)
	}
	if cfg.Resources[0].Source["uri"] != "https://github.com/tdmtrader/jetbridge.git" ||
		cfg.Resources[0].Source["branch"] != "main" {
		t.Errorf("repo source wrong: %+v", cfg.Resources[0].Source)
	}

	if len(cfg.Jobs) != 1 || cfg.Jobs[0].Name != "run" {
		t.Fatalf("want one entry job 'run', got %+v", cfg.Jobs)
	}
	plan := cfg.Jobs[0].PlanSequence
	if len(plan) != 4 {
		t.Fatalf("plan length = %d, want 4 (get repo, write-ticket, agent, harvest)", len(plan))
	}
	get, ok := plan[0].Config.(*atc.GetStep)
	if !ok || get.Name != "repo" || get.Trigger {
		t.Errorf("plan[0] = %+v, want untriggered get repo", plan[0].Config)
	}
	task, ok := plan[1].Config.(*atc.TaskStep)
	if !ok || task.Name != "write-ticket" {
		t.Fatalf("plan[1] = %+v, want write-ticket task", plan[1].Config)
	}
	if task.Config == nil || len(task.Config.Outputs) != 1 || task.Config.Outputs[0].Name != "ticket" {
		t.Errorf("write-ticket outputs wrong: %+v", task.Config)
	}
	// the materialized spec.md/plan.md travel base64-encoded inside the
	// task script (quoting-safe, deterministic)
	script := strings.Join(task.Config.Run.Args, "\n")
	if !strings.Contains(script, "spec.md") || !strings.Contains(script, "plan.md") ||
		!strings.Contains(script, "base64") {
		t.Errorf("write-ticket script does not materialize spec.md/plan.md via base64: %q", script)
	}
	agent, ok := plan[2].Config.(*atc.AgentStep)
	if !ok || agent.Name != "implement" {
		t.Errorf("plan[2] = %+v, want the agent step", plan[2].Config)
	}

	// the terminal harvest step delivers the committed workspace: push to
	// the stable agent/ticket-<id> branch, identity via env (F30 — the
	// run id travels as ((run_id)), never the int field)
	hv, ok := plan[3].Config.(*atc.HarvestStep)
	if !ok || hv.Name != "harvest" {
		t.Fatalf("plan[3] = %+v, want the terminal harvest step", plan[3].Config)
	}
	if hv.Workspace != "workspace" || hv.Repo != "tdmtrader/jetbridge" ||
		hv.TargetBranch != "main" || hv.TicketID != 42 ||
		hv.Branch != "agent/ticket-42" || !hv.Push {
		t.Errorf("harvest step wrong: %+v", hv)
	}
	if hv.Env["AGENT_PIPELINE_RUN_ID"] != "((run_id))" || hv.Env["AGENT_TICKET_ID"] != "42" {
		t.Errorf("harvest identity env wrong: %+v", hv.Env)
	}

	// deterministic: identical input, identical bytes
	a, _ := json.Marshal(cfg)
	cfg2, _ := dispatch.Render(in)
	b, _ := json.Marshal(cfg2)
	if string(a) != string(b) {
		t.Error("render is not deterministic")
	}
}

func TestRenderWithoutRepoOrTicketInputs(t *testing.T) {
	in := renderInput()
	in.Workflow.Steps[0].Inputs = nil

	cfg, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(cfg.Resources) != 0 {
		t.Errorf("no repo input declared, but resources rendered: %+v", cfg.Resources)
	}
	if len(cfg.Jobs[0].PlanSequence) != 2 {
		t.Errorf("plan should be agent + terminal harvest, got %d entries", len(cfg.Jobs[0].PlanSequence))
	}
}

func TestRenderV0Refusals(t *testing.T) {
	mcp := renderInput()
	mcp.Workflow.SpecDelivery = "mcp"
	if _, err := dispatch.Render(mcp); err == nil {
		t.Error("spec_delivery mcp must error in v0 (platform-mcp not deployed)")
	}

	defaulted := renderInput()
	defaulted.Workflow.SpecDelivery = ""
	if _, err := dispatch.Render(defaulted); err == nil {
		t.Error("empty spec_delivery defaults to mcp per §6 and must error in v0")
	}

	empty := renderInput()
	empty.Workflow.Steps = nil
	if _, err := dispatch.Render(empty); err == nil {
		t.Error("a workflow with no steps must error")
	}

	unknownInput := renderInput()
	unknownInput.Workflow.Steps[0].Inputs = []string{"mystery"}
	if _, err := dispatch.Render(unknownInput); err == nil {
		t.Error("an input that is neither repo, ticket, nor a prior step's output must error")
	}
}

func TestRenderRefusesDeclaredButUnenforcedPolicyBlocks(t *testing.T) {
	// Ticket #5 finding #1 (highest blast radius): gate_policy/hitl/judge
	// validate at import and get content-hashed as authoritative, but
	// rendering must have an enforcing consumer or a workflow author
	// would believe gating/HITL/judge scoring is active when it is
	// silently absent. Refuse at render time, matching the sidecar/
	// checkpoint loud-fail pattern. Ticket #14 boundary: harvest-runner
	// now enforces full-scope gate_policy pre-push, so ONLY that shape
	// renders. Judge-evidence Slice E: the judge now renders too
	// (harvest executes it advisory — see TestRenderEmitsJudgeOntoHarvest)
	// — affected-scope gates and hitl still refuse.
	cases := []struct {
		name   string
		mutate func(*dispatch.RenderInput)
	}{
		{"gate_policy_affected_scope", func(in *dispatch.RenderInput) {
			in.Workflow.GatePolicy = workflow.GatePolicy{
				Gates: []workflow.Gate{{Gate: "test", Scope: "affected"}}, OnGateFailure: "needs_review",
			}
		}},
		{"hitl", func(in *dispatch.RenderInput) {
			in.Workflow.HITL = workflow.HITL{AskTimeout: "park"}
		}},
	}
	for _, tc := range cases {
		in := renderInput()
		tc.mutate(&in)
		if _, err := dispatch.Render(in); err == nil {
			t.Errorf("%s: Render must refuse a declared-but-unenforced %s block", tc.name, tc.name)
		}
	}

	// an all-empty policy surface still renders (the common case)
	if _, err := dispatch.Render(renderInput()); err != nil {
		t.Errorf("policy-free workflow must still render: %v", err)
	}

	// full-scope gate_policy is enforceable by harvest-runner (ticket
	// #14, v0.5) and MUST render, carrying the converted policy onto the
	// terminal harvest step.
	in := renderInput()
	in.Workflow.GatePolicy = workflow.GatePolicy{
		Gates: []workflow.Gate{
			{Gate: "build", Scope: "full"},
			{Gate: "test", Scope: "full", Retries: 1, Timeout: "10m"},
		},
		OnGateFailure: "needs_review",
	}
	cfg, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("full-scope gate_policy must render: %v", err)
	}
	harvestStep := cfg.Jobs[0].PlanSequence[len(cfg.Jobs[0].PlanSequence)-1].Config.(*atc.HarvestStep)
	if len(harvestStep.GatePolicy.Gates) != 2 || harvestStep.GatePolicy.OnGateFailure != "needs_review" {
		t.Errorf("harvest step gate_policy = %+v, want the workflow's full-scope gates carried through", harvestStep.GatePolicy)
	}
	if harvestStep.GatePolicy.Gates[1].Retries != 1 || harvestStep.GatePolicy.Gates[1].Timeout != "10m" {
		t.Errorf("harvest step gate 1 = %+v, want retries/timeout preserved", harvestStep.GatePolicy.Gates[1])
	}
}

func TestRenderAllowsPriorStepOutputsAsInputs(t *testing.T) {
	in := renderInput()
	in.Workflow.Prompts["review"] = "Review the workspace."
	in.Workflow.Steps = []workflow.Step{
		{Agent: "implement", Prompt: "do", Inputs: []string{"ticket"}, Outputs: []string{"workspace"}},
		{Agent: "review", Prompt: "review", Inputs: []string{"workspace"}},
	}

	cfg, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(cfg.Jobs[0].PlanSequence) != 4 { // write-ticket + 2 agent steps + harvest
		t.Errorf("plan length = %d, want 4", len(cfg.Jobs[0].PlanSequence))
	}
}

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
		"skills/tdd/SKILL.md":   "# tdd",
		"skills/tdd/refs/a.md":  "supporting",
		"skills/extra/SKILL.md": "# extra",
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
	for _, name := range agent.Inputs {
		if name == "skills" {
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

func TestRenderEmitsJudgeOntoHarvest(t *testing.T) {
	in := renderInput()
	in.Workflow.Judge = &workflow.Judge{
		Rubric:        []workflow.RubricDimension{{Name: "correctness", Weight: 2, Guidance: "works"}},
		PassThreshold: 7,
	}
	in.Workflow.Defaults.Model = "claude-sonnet-4-5"
	in.Workflow.Budget.JudgeUSD = 1.5

	cfg, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("judge workflows must now render: %v", err)
	}
	steps := cfg.Jobs[0].PlanSequence
	last, ok := steps[len(steps)-1].Config.(*atc.HarvestStep)
	if !ok {
		t.Fatalf("terminal step is not harvest: %T", steps[len(steps)-1].Config)
	}
	if last.Judge == nil {
		t.Fatal("judge not emitted onto the harvest step")
	}
	if last.Judge.PassThreshold != 7 || len(last.Judge.Rubric) != 1 ||
		last.Judge.Rubric[0].Name != "correctness" || last.Judge.Rubric[0].Weight != 2 {
		t.Fatalf("judge mis-converted: %+v", last.Judge)
	}
	if last.Judge.Model != "claude-sonnet-4-5" || last.Judge.BudgetUSD != 1.5 {
		t.Fatalf("model/budget defaults not applied: %+v", last.Judge)
	}
}

func TestRenderJudgelessWorkflowEmitsNoJudge(t *testing.T) {
	in := renderInput()
	cfg, err := dispatch.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	steps := cfg.Jobs[0].PlanSequence
	last := steps[len(steps)-1].Config.(*atc.HarvestStep)
	if last.Judge != nil {
		t.Fatal("no judge block, no judge emission")
	}
}
