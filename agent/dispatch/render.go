// Package dispatch renders a ticket's workflow definition into a
// concrete template pipeline and dispatches queued tickets as pipeline
// runs (plan 11, MILESTONE 1 + the manual-trigger core of MILESTONE 2).
//
// v0 scope (manual-dispatch slice, 2026-07-17 — see the contract §11
// entry): plain agent: steps only. Sidecars, checkpoints, harvest
// terminal steps, PARK-V2 env, budget admission, and the autonomous
// Dispatcher loop are wave-3/4 surfaces and are refused loudly at
// render time rather than emitting runs that cannot execute.
//
// Hand-dispatch (plan 11 Task 15 remainder, 2026-07-17): the wave-3
// hand-written template pipeline is retired — hand-dispatch is
// `fly agent tickets dispatch` (or the DispatchAgentTicket route), and
// the autonomous loop is the SAME DispatchOne call driven by the
// agent_dispatcher component under --agent-dispatcher-enabled.
package dispatch

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/harvest"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

// RenderInput is everything the renderer needs, resolved by the
// dispatcher before rendering: the frozen workflow definition, the
// ticket row, its latest spec and active plan, and platform config.
type RenderInput struct {
	Workflow        workflow.Config
	WorkflowName    string
	WorkflowVersion int
	WorkflowHash    string // Definition.ContentHash, frozen at dispatch time

	Ticket    tickets.Ticket
	Spec      *tickets.Spec  // latest spec, nil when none submitted
	PlanTasks []tickets.Task // active plan, empty when none submitted

	ATCExternalURL string
	// RepoBaseURL prefixes the ticket's repo slug to form the git URI
	// (e.g. https://github.com + tdmtrader/jetbridge -> …/tdmtrader/jetbridge.git).
	// v0 renders anonymous clones only — private-repo credentials arrive
	// with harvest's git-cred machinery (wave 3).
	RepoBaseURL string
}

// RenderAgentStep resolves one workflow.Step into a fully-inlined
// atc.AgentStep: prompt text from Config.Prompts, schema text from
// Config.Schemas, Defaults fallback for model/max-turns, and the §8.1
// identity/provenance env baked in. AGENT_TICKET_ID is a literal (the
// dispatcher only renders a verified ticket); AGENT_PIPELINE_RUN_ID is
// the ((run_id)) reserved var — pipeline_runs.id, interpolated into the
// instance config by CreateRun materialization (F30), never the
// per-template ((run)) number.
func RenderAgentStep(in RenderInput, step workflow.Step) (atc.AgentStep, error) {
	if len(step.Sidecars) > 0 {
		return atc.AgentStep{}, fmt.Errorf("agent step %q declares sidecars %v: v0 manual dispatch renders sidecar-less steps only (wave-3 sidecar images are not deployed)", step.Agent, step.Sidecars)
	}
	if step.Checkpoint != "" {
		return atc.AgentStep{}, fmt.Errorf("agent step %q declares a checkpoint: checkpoints require platform-mcp (wave 3)", step.Agent)
	}
	if step.Prompt == "" {
		return atc.AgentStep{}, fmt.Errorf("agent step %q has no prompt key", step.Agent)
	}
	promptTemplate, ok := in.Workflow.Prompts[step.Prompt]
	if !ok {
		return atc.AgentStep{}, fmt.Errorf("agent step %q references unknown prompt %q", step.Agent, step.Prompt)
	}
	prompt, err := renderPrompt(step.Prompt, promptTemplate, in)
	if err != nil {
		return atc.AgentStep{}, fmt.Errorf("agent step %q: %w", step.Agent, err)
	}

	schema := ""
	if step.OutputSchema != "" {
		schema, ok = in.Workflow.Schemas[step.OutputSchema]
		if !ok {
			return atc.AgentStep{}, fmt.Errorf("agent step %q references unknown schema %q", step.Agent, step.OutputSchema)
		}
	}

	model := step.Model
	if model == "" {
		model = in.Workflow.Defaults.Model
	}
	maxTurns := step.MaxTurns
	if maxTurns == 0 {
		maxTurns = in.Workflow.Defaults.MaxTurns
	}

	env := map[string]string{
		"AGENT_TICKET_ID":        strconv.Itoa(in.Ticket.ID),
		"AGENT_PIPELINE_RUN_ID":  "((run_id))",
		"AGENT_WORKFLOW_NAME":    in.WorkflowName,
		"AGENT_WORKFLOW_VERSION": strconv.Itoa(in.WorkflowVersion),
		"AGENT_WORKFLOW_HASH":    in.WorkflowHash,
		"ATC_EXTERNAL_URL":       in.ATCExternalURL,
	}
	if step.BudgetSliceUSD > 0 {
		env["AGENT_BUDGET_SLICE_USD"] = strconv.FormatFloat(step.BudgetSliceUSD, 'f', -1, 64)
	}

	// Source-format layers (design 2026-07-17 §4): resolve the step's
	// effective values and make sure the "skills" artifact rides along
	// whenever the step has a non-empty effective set.
	skills := effectiveSkills(in.Workflow, step)
	inputs := step.Inputs
	if len(skills) > 0 {
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

	return atc.AgentStep{
		Name:           step.Agent,
		Prompt:         prompt,
		Model:          model,
		MaxTurns:       maxTurns,
		BudgetSliceUSD: step.BudgetSliceUSD,
		OutputSchema:   schema,
		SystemPrompt:   effectiveSystemPrompt(in.Workflow, step),
		Context:        effectiveContext(in.Workflow, step),
		Skills:         skills,
		Inputs:         inputs,
		Outputs:        step.Outputs,
		Env:            env,
	}, nil
}

// Render assembles the full template pipeline for a ticket: one entry
// job "run" (no passed: upstream, so CreateRun auto-triggers it) whose
// plan is [get repo?] -> [write-ticket?] -> agent steps in workflow
// order. The write-ticket task materializes the deterministic
// spec.md/plan.md workspace inputs (tickets.RenderSpecMarkdown /
// RenderPlanMarkdown) as the read-only "ticket" artifact; contents
// travel base64-encoded inside the task script so arbitrary markdown
// survives shell quoting byte-exact.
func Render(in RenderInput) (atc.Config, error) {
	switch in.Workflow.SpecDelivery {
	case "files":
	case "", "mcp":
		return atc.Config{}, fmt.Errorf("workflow %q spec_delivery %q: v0 manual dispatch supports spec_delivery: files only (mcp delivery requires the wave-3 platform sidecar)", in.WorkflowName, in.Workflow.SpecDelivery)
	default:
		return atc.Config{}, fmt.Errorf("workflow %q has unknown spec_delivery %q", in.WorkflowName, in.Workflow.SpecDelivery)
	}
	if len(in.Workflow.Steps) == 0 {
		return atc.Config{}, fmt.Errorf("workflow %q has no steps", in.WorkflowName)
	}
	// Refuse declared-but-unenforced policy blocks (dogfood ticket #5,
	// highest-blast-radius finding): gate_policy/hitl/judge validate at
	// import and get content-hashed as authoritative, but rendering must
	// have an enforcing consumer or an author would believe gating/HITL/
	// judge scoring is active when it is absent. Fail loudly at render
	// time. Relaxed further (judge-evidence Slice E): the judge now
	// RENDERS onto the terminal harvest step and harvest executes it.
	// time, matching sidecars/checkpoints. Relaxed (ticket #14, v0.5):
	// harvest-runner now enforces gate_policy pre-push when every gate's
	// scope is "full" (its in-pod fixed command map) — affected/
	// affected_then_full still need dev-mcp (full harvest-step
	// workstream, wave 3) and stay refused, as does any on_gate_failure
	// other than the only v1 value.
	gatesEnforceable := true
	for _, g := range in.Workflow.GatePolicy.Gates {
		if g.Scope != "full" {
			gatesEnforceable = false
			break
		}
	}
	onGateFailureOK := in.Workflow.GatePolicy.OnGateFailure == "" || in.Workflow.GatePolicy.OnGateFailure == "needs_review"
	if !gatesEnforceable || !onGateFailureOK {
		return atc.Config{}, fmt.Errorf("workflow %q declares a gate_policy: v0 manual dispatch cannot enforce gates (harvest-step, wave 3) — remove the block or wait for harvest", in.WorkflowName)
	}
	if in.Workflow.HITL.AskTimeout != "" || in.Workflow.HITL.AskTimeoutSeconds != 0 {
		return atc.Config{}, fmt.Errorf("workflow %q declares an hitl block: v0 manual dispatch has no human-in-the-loop pause (platform-mcp-hitl, wave 3) — remove the block or wait for platform-mcp", in.WorkflowName)
	}

	needsRepo, needsTicket := false, false
	available := map[string]bool{}
	for _, step := range in.Workflow.Steps {
		for _, input := range step.Inputs {
			switch {
			case input == "repo":
				needsRepo = true
			case input == "ticket":
				needsTicket = true
			case input == "skills":
				if len(in.Workflow.SkillFiles) == 0 {
					return atc.Config{}, fmt.Errorf("agent step %q consumes the skills artifact but the workflow references no skills", step.Agent)
				}
			case available[input]:
			default:
				return atc.Config{}, fmt.Errorf("agent step %q input %q is neither repo, ticket, nor a prior step's output", step.Agent, input)
			}
		}
		for _, output := range step.Outputs {
			available[output] = true
		}
	}

	plan := []atc.Step{}
	if needsRepo {
		plan = append(plan, atc.Step{Config: &atc.GetStep{Name: "repo"}})
	}
	if needsTicket {
		plan = append(plan, atc.Step{Config: writeTicketTask(in)})
	}
	if len(in.Workflow.SkillFiles) > 0 {
		task, err := writeSkillsTask(in.Workflow)
		if err != nil {
			return atc.Config{}, err
		}
		plan = append(plan, atc.Step{Config: task})
	}
	for _, step := range in.Workflow.Steps {
		agentStep, err := RenderAgentStep(in, step)
		if err != nil {
			return atc.Config{}, err
		}
		cp := agentStep
		plan = append(plan, atc.Step{Config: &cp})
	}

	// Terminal harvest (§2.8.1): deliver the committed workspace as the
	// stable agent/ticket-<id> branch. The workspace artifact is
	// guaranteed by the import gate (a workflow must produce
	// "workspace"); identity travels as env because a Go renderer cannot
	// place ((run_id)) in the int pipeline_run_id field (F30). A
	// full-scope gate_policy rides along (harvest-runner enforces it
	// pre-push); the judge is emitted when the workflow declares one
	// (judge-evidence Slice E — harvest executes it advisory); only
	// dev_mcp is still refused above.
	if in.Ticket.ID > 0 {
		plan = append(plan, atc.Step{Config: &atc.HarvestStep{
			Name:         "harvest",
			Workspace:    "workspace",
			Repo:         in.Ticket.Repo,
			TargetBranch: in.Ticket.TargetBranch,
			TicketID:     in.Ticket.ID,
			Branch:       fmt.Sprintf("agent/ticket-%d", in.Ticket.ID),
			Push:         true,
			GatePolicy:   harvestGatePolicy(in.Workflow.GatePolicy),
			Judge:        harvestJudge(in.Workflow.Judge, in.Workflow.Defaults.Model, in.Workflow.Budget.JudgeUSD),
			Env: map[string]string{
				"AGENT_TICKET_ID":        strconv.Itoa(in.Ticket.ID),
				"AGENT_PIPELINE_RUN_ID":  "((run_id))",
				"AGENT_WORKFLOW_NAME":    in.WorkflowName,
				"AGENT_WORKFLOW_VERSION": strconv.Itoa(in.WorkflowVersion),
				"AGENT_WORKFLOW_HASH":    in.WorkflowHash,
				"ATC_EXTERNAL_URL":       in.ATCExternalURL,
			},
		}})
	}

	cfg := atc.Config{
		Template: true,
		Jobs: atc.JobConfigs{{
			Name:         "run",
			PlanSequence: plan,
		}},
	}
	if needsRepo {
		cfg.Resources = atc.ResourceConfigs{{
			Name: "repo",
			Type: "git",
			Source: atc.Source{
				"uri":    in.RepoBaseURL + "/" + in.Ticket.Repo + ".git",
				"branch": in.Ticket.TargetBranch,
			},
		}}
	}
	return cfg, nil
}

// renderPrompt executes a §6.2 prompt template against the frozen
// {Ticket, Spec, Tasks, Params} render context. The import gate
// (workflow.Parse) validates every prompt against the spec-less ground
// state with TOLERANT maps — unknown envelope fields render "<no
// value>", only a .Spec nil-deref blocks import — so the real context
// keeps the same semantics: JSON-shaped maps (snake_case keys), .Spec
// nil until a spec exists. Params is empty in v0 (run params arrive
// with the Dispatcher loop).
func renderPrompt(key, body string, in RenderInput) (string, error) {
	tmpl, err := template.New(key).Parse(body)
	if err != nil {
		return "", fmt.Errorf("prompt %q: %w", key, err)
	}

	ctx := struct {
		Ticket map[string]any
		Spec   map[string]any
		Tasks  []map[string]any
		Params map[string]any
	}{
		Ticket: toJSONMap(in.Ticket),
		Tasks:  []map[string]any{},
		Params: map[string]any{},
	}
	if in.Spec != nil {
		ctx.Spec = toJSONMap(*in.Spec)
	}
	for _, task := range in.PlanTasks {
		ctx.Tasks = append(ctx.Tasks, toJSONMap(task))
	}

	var out strings.Builder
	if err := tmpl.Execute(&out, ctx); err != nil {
		return "", fmt.Errorf("prompt %q: %w", key, err)
	}
	return out.String(), nil
}

// maxSkillsRenderBytes bounds the base64 write-skills task payload. The
// spec's escape hatch for larger sets is an authenticated
// fetch-by-version endpoint (design 2026-07-17 §4) — refuse loudly
// until someone needs it.
const maxSkillsRenderBytes = 512 << 10

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

// harvestGatePolicy converts the workflow-YAML gate_policy grammar
// (agent/workflow.GatePolicy, validated at import) into the executable
// shape harvest-runner interprets (agent/harvest.GatePolicy). Callers
// must only pass a policy Render has already verified is enforceable
// (every gate scope "full") — the conversion itself is a plain field
// copy, no further validation.
// harvestJudge converts the workflow judge block (validated at import)
// into the executable §6.4 shape. Model defaults to the workflow's
// default model; the budget cap comes from budget.judge_usd (§6) — both
// resolved at render time per the §2.8 render-time-resolution rule.
func harvestJudge(j *workflow.Judge, defaultModel string, budgetUSD float64) *harvest.JudgeConfig {
	if j == nil {
		return nil
	}
	rubric := make([]harvest.RubricDimension, len(j.Rubric))
	for i, d := range j.Rubric {
		rubric[i] = harvest.RubricDimension{Name: d.Name, Weight: d.Weight, Guidance: d.Guidance}
	}
	return &harvest.JudgeConfig{
		Rubric: rubric, PassThreshold: j.PassThreshold,
		Model: defaultModel, BudgetUSD: budgetUSD,
	}
}

func harvestGatePolicy(p workflow.GatePolicy) harvest.GatePolicy {
	gates := make([]harvest.Gate, len(p.Gates))
	for i, g := range p.Gates {
		gates[i] = harvest.Gate{
			Gate: g.Gate, Scope: g.Scope, Focus: g.Focus,
			Timeout: g.Timeout, Retries: g.Retries,
		}
	}
	return harvest.GatePolicy{Gates: gates, OnGateFailure: p.OnGateFailure}
}

func toJSONMap(v any) map[string]any {
	payload, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	m := map[string]any{}
	if err := json.Unmarshal(payload, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// writeTicketTask emits the task step materializing the "ticket"
// artifact: spec.md (envelope + problem statement + latest spec) and
// plan.md (active plan with status glyphs).
func writeTicketTask(in RenderInput) *atc.TaskStep {
	specMD := base64.StdEncoding.EncodeToString(tickets.RenderSpecMarkdown(in.Ticket, in.Spec))
	planMD := base64.StdEncoding.EncodeToString(tickets.RenderPlanMarkdown(in.Ticket, in.PlanTasks))

	script := fmt.Sprintf(
		"set -eu\necho %s | base64 -d > ticket/spec.md\necho %s | base64 -d > ticket/plan.md\nls -l ticket/",
		specMD, planMD)

	return &atc.TaskStep{
		Name: "write-ticket",
		Config: &atc.TaskConfig{
			Platform: "linux",
			ImageResource: &atc.ImageResource{
				Type:   "registry-image",
				Source: atc.Source{"repository": "busybox"},
			},
			Outputs: []atc.TaskOutputConfig{{Name: "ticket"}},
			Run: atc.TaskRunConfig{
				Path: "sh",
				Args: []string{"-ec", script},
			},
		},
	}
}
