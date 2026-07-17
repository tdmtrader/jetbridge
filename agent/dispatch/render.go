// Package dispatch renders a ticket's workflow definition into a
// concrete template pipeline and dispatches queued tickets as pipeline
// runs (plan 11, MILESTONE 1 + the manual-trigger core of MILESTONE 2).
//
// v0 scope (manual-dispatch slice, 2026-07-17 — see the contract §11
// entry): plain agent: steps only. Sidecars, checkpoints, harvest
// terminal steps, PARK-V2 env, budget admission, and the autonomous
// Dispatcher loop are wave-3/4 surfaces and are refused loudly at
// render time rather than emitting runs that cannot execute.
package dispatch

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/template"

	"github.com/concourse/concourse/agent/api/tickets"
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

	return atc.AgentStep{
		Name:           step.Agent,
		Prompt:         prompt,
		Model:          model,
		MaxTurns:       maxTurns,
		BudgetSliceUSD: step.BudgetSliceUSD,
		OutputSchema:   schema,
		Inputs:         step.Inputs,
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

	needsRepo, needsTicket := false, false
	available := map[string]bool{}
	for _, step := range in.Workflow.Steps {
		for _, input := range step.Inputs {
			switch {
			case input == "repo":
				needsRepo = true
			case input == "ticket":
				needsTicket = true
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
	for _, step := range in.Workflow.Steps {
		agentStep, err := RenderAgentStep(in, step)
		if err != nil {
			return atc.Config{}, err
		}
		cp := agentStep
		plan = append(plan, atc.Step{Config: &cp})
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
