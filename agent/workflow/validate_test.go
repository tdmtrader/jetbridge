package workflow_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

// mutate returns fullSampleYAML with one snippet replaced (must occur).
func mutate(t *testing.T, old, new string) string {
	t.Helper()
	if !strings.Contains(fullSampleYAML, old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return strings.Replace(fullSampleYAML, old, new, 1)
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"wrong schema_version", mutate(t, "schema_version: 1", "schema_version: 2"), "schema_version must be 1"},
		{"missing name", mutate(t, "name: standard-dev", "name: \"\""), "name is required"},
		{"bad spec_delivery", mutate(t, "name: standard-dev", "name: standard-dev\nspec_delivery: telepathy"), "spec_delivery must be mcp or files"},
		{"step with both agent and checkpoint", mutate(t, "- checkpoint: plan-approval", "- checkpoint: plan-approval\n  agent: sneaky\n  prompt: spec"), "exactly one of"},
		{"agent step without prompt", mutate(t, "  prompt: spec\n", "\n"), "prompt is required"},
		{"unknown prompt key", mutate(t, "  prompt: spec", "  prompt: nonexistent"), "unknown prompt"},
		{"unknown sidecar name", mutate(t, "sidecars: [dev, platform]\n", "sidecars: [dev, missing]\n"), "unknown sidecar"},
		{"negative budget slice", mutate(t, "budget_slice_usd: 2.0", "budget_slice_usd: -1"), "budget_slice_usd"},
		{"unpinned sidecar image", mutate(t, "image: ghcr.io/tdmtrader/mcp-dev-concourse:0.1.0", "image: ghcr.io/tdmtrader/mcp-dev-concourse"), "pinned"},
		{"bad sidecar role", mutate(t, "role: platform", "role: butler"), "role"},
		{"providers on non-gateway", mutate(t, "role: dev", "role: dev\n    providers: [claude]"), "providers"},
		{"bad on_reject", mutate(t, "on_reject: fail", "on_reject: retry"), "on_reject"},
		{"bad ask_timeout", mutate(t, "ask_timeout: park", "ask_timeout: snooze"), "ask_timeout"},
		{"negative ask_timeout_seconds", mutate(t, "ask_timeout_seconds: 0", "ask_timeout_seconds: -5"), "ask_timeout_seconds"},
		// default/fail timeout policy with a non-positive deadline never fires —
		// the ask parks forever, defeating the policy. Reject it at import.
		{"default ask_timeout with zero seconds", mutate(t, "ask_timeout: park", "ask_timeout: default"), "requires ask_timeout_seconds > 0"},
		{"fail ask_timeout with zero seconds", mutate(t, "ask_timeout: park", "ask_timeout: fail"), "requires ask_timeout_seconds > 0"},
		{"bad gate name", mutate(t, "- gate: lint", "- gate: vibes"), "gate"},
		{"bad gate scope", mutate(t, "scope: affected_then_full", "scope: sometimes"), "scope"},
		{"bad gate timeout", mutate(t, "timeout: 45m", "timeout: eventually"), "timeout"},
		{"gate retries out of range", mutate(t, "retries: 1", "retries: 3"), "retries must be 0-2"},
		{"bad on_gate_failure", mutate(t, "on_gate_failure: needs_review", "on_gate_failure: explode"), "on_gate_failure"},
		{"judge weight zero", mutate(t, "weight: 3", "weight: 0"), "weight"},
		{"judge threshold out of range", mutate(t, "pass_threshold: 6.5", "pass_threshold: 11"), "pass_threshold"},
		{"duplicate judge dimension", mutate(t, "- name: tests", "- name: correctness"), "duplicate"},
		{"input not produced earlier", mutate(t, "  inputs: [workspace]\n", "  inputs: [workspace, phantom]\n"), "phantom"},
		{"unparseable prompt template", mutate(t, "{{.Ticket.Title}}", "{{.Ticket.Title"), "template"},
		// E2b (2026-07-09): .Spec is nil at EVERY dispatch render (rendering
		// happens before any agent can submit a spec — contracts §6.2
		// nil-safety), so a bare deref must fail at IMPORT, not at dispatch.
		{"bare .Spec deref in prompt", mutate(t, "{{.Ticket.Title}}", "{{.Spec.Title}}"), "spec-less"},
		{"duplicate step name", mutate(t, "- agent: implement", "- agent: write-spec"), "duplicate"},
		{"checkpoint with agent fields", mutate(t, "  on_reject: fail", "  on_reject: fail\n  max_turns: 5"), "checkpoint"},
		{"negative ticket budget", mutate(t, "ticket_usd: 15.0", "ticket_usd: -2"), "ticket_usd"},
		{"output_schema without schemas entry", mutate(t, "  budget_slice_usd: 2.0", "  budget_slice_usd: 2.0\n  output_schema: spec_out"), "output_schema"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := workflow.Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestValidateAcceptsMinimalDefinition(t *testing.T) {
	minimal := `schema_version: 1
name: tiny
spec_delivery: files
prompts:
  work: |
    Do the work.
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`
	cfg, err := workflow.Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("minimal definition must validate: %v", err)
	}
	if cfg.HITL.AskTimeout != "" || cfg.Judge != nil || len(cfg.GatePolicy.Gates) != 0 {
		t.Errorf("optional blocks must stay zero: %+v", cfg)
	}
	if cfg.SpecDelivery != "files" {
		t.Errorf("SpecDelivery = %q, want files", cfg.SpecDelivery)
	}
}

// TestValidateAcceptsNilSafeSpecTemplates (E2b, 2026-07-09): templates that
// guard .Spec and range .Tasks render cleanly against the spec-less context
// every dispatch sees (contracts §6.2 nil-safety), so they must import clean —
// including envelope/params field access, which is always tolerated.
func TestValidateAcceptsNilSafeSpecTemplates(t *testing.T) {
	guarded := mutate(t, "{{.Ticket.Title}}",
		"{{if .Spec}}{{.Spec.Title}}{{end}} {{range .Tasks}}{{.Title}} {{end}}{{.Ticket.Body}} {{.Params.extra}}")
	if _, err := workflow.Parse([]byte(guarded)); err != nil {
		t.Fatalf("guarded spec/tasks templates must validate: %v", err)
	}
}

// TestValidateRejectsWorkspacelessDefinition (E6a, 2026-07-09, FLOWS S4): the
// implicit terminal harvest step consumes the `workspace` artifact
// (11-dispatch's RenderHarvestStep hard-codes it as harvest's input). A
// definition in which no step outputs it used to validate clean and then fail
// EVERY run at the harvest step — reject it at import instead.
func TestValidateRejectsWorkspacelessDefinition(t *testing.T) {
	noWorkspace := `schema_version: 1
name: no-workspace
prompts:
  work: |
    Do the work.
steps:
- agent: work
  prompt: work
  outputs: [notes]
`
	_, err := workflow.Parse([]byte(noWorkspace))
	if err == nil {
		t.Fatal("definition with no workspace output must be rejected")
	}
	if !strings.Contains(err.Error(), `no step outputs "workspace"`) {
		t.Errorf("error %q must name the missing workspace output", err.Error())
	}
}

// TestValidateRejectsCheckpointWithoutPlatformSidecar (E6b, 2026-07-09,
// FLOWS S5): import mirror of dispatch's F36 render-time guard — a rendered
// checkpoint task mounts the definition's "platform" sidecar; without that
// entry every dispatch of this definition errors at render. Catch it at
// import.
func TestValidateRejectsCheckpointWithoutPlatformSidecar(t *testing.T) {
	noPlatform := `schema_version: 1
name: no-platform
sidecars:
  dev:
    image: ghcr.io/tdmtrader/mcp-dev-concourse:0.1.0
    role: dev
prompts:
  work: |
    Do the work.
steps:
- agent: work
  prompt: work
  sidecars: [dev]
  outputs: [workspace]
- checkpoint: human-gate
  on_reject: fail
`
	_, err := workflow.Parse([]byte(noPlatform))
	if err == nil {
		t.Fatal("checkpoint without a platform sidecar must be rejected")
	}
	if !strings.Contains(err.Error(), `checkpoint "human-gate" requires a "platform" sidecar`) {
		t.Errorf("error %q must point at the missing platform sidecar", err.Error())
	}
}
