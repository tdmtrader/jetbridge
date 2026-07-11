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
