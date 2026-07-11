package workflow_test

import (
	"os"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

func TestSeedStandardDevValidates(t *testing.T) {
	raw, err := os.ReadFile("seeds/standard-dev.yaml")
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}

	cfg, err := workflow.Parse(raw)
	if err != nil {
		t.Fatalf("seed must validate: %v", err)
	}
	if cfg.Name != "standard-dev" {
		t.Errorf("Name = %q", cfg.Name)
	}

	// plan, checkpoint, implement, qa, review, fix — the five ci-agent
	// phases plus one inert checkpoint.
	wantSteps := []string{"plan", "plan-approval", "implement", "qa", "review", "fix"}
	if len(cfg.Steps) != len(wantSteps) {
		t.Fatalf("Steps = %d, want %d", len(cfg.Steps), len(wantSteps))
	}
	for i, want := range wantSteps {
		name := cfg.Steps[i].Agent
		if cfg.Steps[i].Checkpoint != "" {
			name = cfg.Steps[i].Checkpoint
		}
		if name != want {
			t.Errorf("step %d = %q, want %q", i, name, want)
		}
	}

	if cfg.Budget.TicketUSD <= 0 {
		t.Error("seed must declare a default ticket budget")
	}
	if len(cfg.GatePolicy.Gates) == 0 {
		t.Error("seed must declare the gate-policy slot")
	}
	if cfg.Judge == nil {
		t.Error("seed must declare the judge rubric slot")
	}

	// The seed omits spec_delivery, so it must resolve to the default "mcp"
	// read model — the reference workflow demonstrates the default path.
	resolved := cfg.SpecDelivery
	if resolved == "" {
		resolved = "mcp"
	}
	if resolved != "mcp" {
		t.Errorf("seed spec_delivery must resolve to mcp (default path), got %q", resolved)
	}
	// Coherence: under the mcp read model no spec/plan bytes are injected, so
	// no prompt body may point agents at spec.md/plan.md files or embed the
	// bare {{.Spec}}/{{.Tasks}} tokens — those belong only to spec_delivery:
	// files. Agents must read via platform-mcp read_ticket/list_tasks/get_task.
	if resolved == "mcp" {
		forbidden := []string{"spec.md", "plan.md", "{{.Spec}}", "{{.Tasks}}"}
		for name, body := range cfg.Prompts {
			for _, tok := range forbidden {
				if strings.Contains(body, tok) {
					t.Errorf("prompt %q contains %q, incoherent with spec_delivery=mcp (read via platform-mcp read_ticket/list_tasks/get_task)", name, tok)
				}
			}
		}
	}

	// The hash is the provenance unit: 64 hex chars over the exact bytes.
	if len(workflow.Hash(raw)) != 64 {
		t.Error("content hash must be a 64-char sha256 hex")
	}
}

// assertMCPPromptCoherence enforces the Task-12 coherence rule for any seed
// on the default mcp read model: no prompt may point agents at spec.md/plan.md
// files or embed the bare {{.Spec}}/{{.Tasks}} tokens — those belong only to
// spec_delivery: files. Agents read via platform-mcp
// read_ticket/list_tasks/get_task.
func assertMCPPromptCoherence(t *testing.T, cfg *workflow.Config) {
	t.Helper()
	resolved := cfg.SpecDelivery
	if resolved == "" {
		resolved = "mcp"
	}
	if resolved != "mcp" {
		t.Fatalf("seed spec_delivery must resolve to mcp (default path), got %q", resolved)
	}
	forbidden := []string{"spec.md", "plan.md", "{{.Spec}}", "{{.Tasks}}"}
	for name, body := range cfg.Prompts {
		for _, tok := range forbidden {
			if strings.Contains(body, tok) {
				t.Errorf("prompt %q contains %q, incoherent with spec_delivery=mcp", name, tok)
			}
		}
	}
}

// TestSeedDirectDevValidates (E3, 2026-07-09): the direct one-shot seed — the
// ticket body IS the spec. Importing it clean is the executable proof that a
// spec-less definition is first-class (FLOWS.md §1 bottom line), not merely
// tolerated: no submit_spec, no submit_plan, no checkpoint, anywhere.
func TestSeedDirectDevValidates(t *testing.T) {
	raw, err := os.ReadFile("seeds/direct-dev.yaml")
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}

	cfg, err := workflow.Parse(raw)
	if err != nil {
		t.Fatalf("spec-less seed must import clean: %v", err)
	}
	if cfg.Name != "direct-dev" {
		t.Errorf("Name = %q", cfg.Name)
	}

	// Exactly one agent step, no checkpoints, workspace produced (the E6a
	// import rule the implicit harvest depends on).
	if len(cfg.Steps) != 1 || cfg.Steps[0].Agent != "implement" {
		t.Fatalf("Steps = %+v, want the single implement step", cfg.Steps)
	}
	if len(cfg.Steps[0].Outputs) != 1 || cfg.Steps[0].Outputs[0] != "workspace" {
		t.Errorf("implement step must output workspace, got %+v", cfg.Steps[0].Outputs)
	}

	// Spec-less by construction: the prompts never call the spec/plan write
	// tools; the ticket body is the whole contract, read via read_ticket.
	for name, body := range cfg.Prompts {
		if strings.Contains(body, "submit_spec") || strings.Contains(body, "submit_plan") {
			t.Errorf("prompt %q must not call submit_spec/submit_plan in the direct one-shot seed", name)
		}
	}
	if !strings.Contains(cfg.Prompts["implement"], "read_ticket") {
		t.Error("implement prompt must read the ticket via platform-mcp read_ticket")
	}

	// The judge grades against the ticket body — there is no spec to cite.
	if cfg.Judge == nil || len(cfg.Judge.Rubric) == 0 {
		t.Fatal("seed must declare a judge rubric")
	}
	foundBody := false
	for _, d := range cfg.Judge.Rubric {
		if strings.Contains(d.Guidance, "ticket body") {
			foundBody = true
		}
		if strings.Contains(strings.ToLower(d.Guidance), "spec's acceptance criteria") {
			t.Errorf("rubric %q cites a spec; this flow has none", d.Name)
		}
	}
	if !foundBody {
		t.Error("at least one rubric dimension must grade against the ticket body")
	}

	if len(cfg.GatePolicy.Gates) == 0 {
		t.Error("seed must declare the gate-policy slot")
	}
	assertMCPPromptCoherence(t, cfg)
}

// TestSeedTestFirstDevValidates (seed #3, 2026-07-09): the test-first contract
// seed — failing tests ARE the approved contract, mirrored into the spec body
// via submit_spec (the checkpoint-evidence workaround in FLOWS.md §3),
// human-gated at the tests-approved checkpoint, then implemented to green
// under a full-suite gate.
func TestSeedTestFirstDevValidates(t *testing.T) {
	raw, err := os.ReadFile("seeds/test-first-dev.yaml")
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}

	cfg, err := workflow.Parse(raw)
	if err != nil {
		t.Fatalf("seed must validate: %v", err)
	}
	if cfg.Name != "test-first-dev" {
		t.Errorf("Name = %q", cfg.Name)
	}

	// write-tests -> tests-approved checkpoint -> implement.
	wantSteps := []string{"write-tests", "tests-approved", "implement"}
	if len(cfg.Steps) != len(wantSteps) {
		t.Fatalf("Steps = %d, want %d", len(cfg.Steps), len(wantSteps))
	}
	for i, want := range wantSteps {
		name := cfg.Steps[i].Agent
		if cfg.Steps[i].Checkpoint != "" {
			name = cfg.Steps[i].Checkpoint
		}
		if name != want {
			t.Errorf("step %d = %q, want %q", i, name, want)
		}
	}
	if cfg.Steps[1].OnReject != "send_back" {
		t.Errorf("tests-approved on_reject = %q, want send_back (a rejected contract goes back to its author)", cfg.Steps[1].OnReject)
	}

	// The checkpoint renders with the definition's platform sidecar
	// (Task 4 E6b import rule / dispatch F36 render guard).
	if _, ok := cfg.Sidecars["platform"]; !ok {
		t.Error("seed must declare the platform sidecar its checkpoint renders with")
	}

	// The contract mirror: the write-tests prompt submits the test manifest
	// as the spec body so the reviewer can approve from the ticket page.
	if !strings.Contains(cfg.Prompts["write-tests"], "submit_spec") {
		t.Error("write-tests prompt must mirror the test manifest via submit_spec")
	}
	if strings.Contains(cfg.Prompts["implement"], "submit_spec") || strings.Contains(cfg.Prompts["implement"], "submit_plan") {
		t.Error("implement prompt must not write spec/plan rows; the contract is already approved")
	}

	// The gate policy runs the FULL test suite — green-on-the-contract is
	// the whole point of the flow.
	foundFull := false
	for _, g := range cfg.GatePolicy.Gates {
		if g.Gate == "test" && g.Scope == "full" {
			foundFull = true
		}
	}
	if !foundFull {
		t.Error("gate policy must run gate test with scope full")
	}

	// The judge must grade contract integrity: tests unmodified since the
	// checkpoint (FLOWS.md test-first sketch).
	if cfg.Judge == nil {
		t.Fatal("seed must declare a judge rubric")
	}
	foundUnmodified := false
	for _, d := range cfg.Judge.Rubric {
		if d.Name == "tests-unmodified" {
			foundUnmodified = true
		}
	}
	if !foundUnmodified {
		t.Error("rubric must include the tests-unmodified dimension")
	}

	assertMCPPromptCoherence(t, cfg)
}
