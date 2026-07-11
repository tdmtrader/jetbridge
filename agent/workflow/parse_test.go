package workflow_test

import (
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

// fullSampleYAML is the contracts-doc §6 example, verbatim in structure
// (image tags added: pinned ':<version>' is required at import).
const fullSampleYAML = `schema_version: 1
name: standard-dev
description: spec -> plan -> implement -> review loop, single agent

defaults:
  model: claude-sonnet-4-5
  max_turns: 80

budget:
  ticket_usd: 15.0
  judge_usd: 1.0

sidecars:
  dev:
    image: ghcr.io/tdmtrader/mcp-dev-concourse:0.1.0
    role: dev
  platform:
    image: ghcr.io/tdmtrader/mcp-platform:0.1.0
    role: platform
  gateway:
    image: ghcr.io/tdmtrader/mcp-gateway:0.1.0
    role: gateway
    providers: [claude]

prompts:
  spec: |
    Read the ticket via platform-mcp read_ticket, explore the repo, then
    submit a spec with submit_spec. Ticket: {{.Ticket.Title}}
  implement: |
    Implement the active plan task by task. Use dev-mcp run_tests with
    affected components after each task.

steps:
- agent: write-spec
  prompt: spec
  sidecars: [dev, platform]
  budget_slice_usd: 2.0
  outputs: [workspace]

- checkpoint: plan-approval
  on_reject: fail

- agent: implement
  prompt: implement
  sidecars: [dev, platform, gateway]
  budget_slice_usd: 10.0
  max_turns: 120
  inputs: [workspace]
  outputs: [workspace]

hitl:
  ask_timeout: park
  ask_timeout_seconds: 0

gate_policy:
  gates:
  - gate: build
    scope: affected
  - gate: test
    scope: affected_then_full
    timeout: 45m
    retries: 1
  - gate: lint
    scope: affected
  on_gate_failure: needs_review

judge:
  rubric:
  - name: correctness
    weight: 3
    guidance: "Does the change do what the spec's acceptance criteria require?"
  - name: tests
    weight: 2
    guidance: "Are new behaviors covered by meaningful tests?"
  - name: scope-discipline
    weight: 1
    guidance: "Small tractable diff; no drive-by refactors."
  pass_threshold: 6.5
`

func TestParseFullSample(t *testing.T) {
	cfg, err := workflow.Parse([]byte(fullSampleYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", cfg.SchemaVersion)
	}
	if cfg.Name != "standard-dev" {
		t.Errorf("Name = %q", cfg.Name)
	}
	// spec_delivery is omitted from the sample: empty ⇒ mcp semantics
	// (dispatch's renderer injects no spec/plan bytes).
	if cfg.SpecDelivery != "" {
		t.Errorf("SpecDelivery = %q, want \"\" (defaults to mcp)", cfg.SpecDelivery)
	}
	if cfg.Defaults.Model != "claude-sonnet-4-5" || cfg.Defaults.MaxTurns != 80 {
		t.Errorf("Defaults = %+v", cfg.Defaults)
	}
	if cfg.Budget.TicketUSD != 15.0 || cfg.Budget.JudgeUSD != 1.0 {
		t.Errorf("Budget = %+v", cfg.Budget)
	}
	if len(cfg.Sidecars) != 3 || cfg.Sidecars["gateway"].Providers[0] != "claude" {
		t.Errorf("Sidecars = %+v", cfg.Sidecars)
	}
	if len(cfg.Steps) != 3 {
		t.Fatalf("Steps = %d, want 3", len(cfg.Steps))
	}
	if cfg.Steps[0].Agent != "write-spec" || cfg.Steps[0].Prompt != "spec" || cfg.Steps[0].BudgetSliceUSD != 2.0 {
		t.Errorf("step 0 = %+v", cfg.Steps[0])
	}
	if cfg.Steps[1].Checkpoint != "plan-approval" || cfg.Steps[1].OnReject != "fail" {
		t.Errorf("step 1 = %+v", cfg.Steps[1])
	}
	if cfg.Steps[2].MaxTurns != 120 || cfg.Steps[2].Inputs[0] != "workspace" {
		t.Errorf("step 2 = %+v", cfg.Steps[2])
	}
	if cfg.HITL.AskTimeout != "park" {
		t.Errorf("HITL = %+v", cfg.HITL)
	}
	if len(cfg.GatePolicy.Gates) != 3 || cfg.GatePolicy.OnGateFailure != "needs_review" {
		t.Errorf("GatePolicy = %+v", cfg.GatePolicy)
	}
	if cfg.GatePolicy.Gates[1].Timeout != "45m" || cfg.GatePolicy.Gates[1].Retries != 1 {
		t.Errorf("gate 1 = %+v", cfg.GatePolicy.Gates[1])
	}
	if cfg.Judge == nil || len(cfg.Judge.Rubric) != 3 || cfg.Judge.PassThreshold != 6.5 {
		t.Errorf("Judge = %+v", cfg.Judge)
	}
}

func TestParseRejectsMalformedYAML(t *testing.T) {
	_, err := workflow.Parse([]byte(":\t not yaml ["))
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}
