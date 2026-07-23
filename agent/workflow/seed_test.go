package workflow_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

func TestVersionThreeEngineeringSeedsCompileAndRender(t *testing.T) {
	tests := []struct {
		directory         string
		name              string
		inputs            []workflow.SignaturePort
		outputs           []workflow.SignaturePort
		dispositionOutput string
		humanWait         bool
		publisher         bool
	}{
		{
			directory: "seeds/code-review-v3",
			name:      "code-review",
			inputs: []workflow.SignaturePort{
				{Name: "before", Type: snapshot.TypeRef("repository/v1")},
				{Name: "after", Type: snapshot.TypeRef("repository/v1")},
			},
			outputs:           []workflow.SignaturePort{{Name: "review", Type: snapshot.TypeRef("review/v1")}},
			dispositionOutput: "review",
		},
		{
			directory: "seeds/small-fix-v3",
			name:      "small-fix",
			inputs: []workflow.SignaturePort{
				{Name: "repository", Type: snapshot.TypeRef("repository/v1")},
				{Name: "work-item", Type: snapshot.TypeRef("work-item/v1")},
			},
			outputs: []workflow.SignaturePort{
				{Name: "change", Type: snapshot.TypeRef("repository-change/v1")},
				{Name: "report", Type: snapshot.TypeRef("opaque/v1")},
			},
			dispositionOutput: "change",
			humanWait:         true,
		},
		{
			directory: "seeds/version-upgrade-v3",
			name:      "version-upgrade",
			inputs: []workflow.SignaturePort{
				{Name: "repository", Type: snapshot.TypeRef("repository/v1")},
				{Name: "request", Type: snapshot.TypeRef("upgrade-request/v1")},
			},
			outputs: []workflow.SignaturePort{
				{Name: "change", Type: snapshot.TypeRef("repository-change/v1")},
				{Name: "report", Type: snapshot.TypeRef("upgrade-report/v1")},
			},
			dispositionOutput: "change",
			humanWait:         true,
		},
		{
			directory: "seeds/anonymization-audit-v3",
			name:      "anonymization-audit",
			inputs: []workflow.SignaturePort{
				{Name: "repository", Type: snapshot.TypeRef("repository/v1")},
				{Name: "database", Type: snapshot.TypeRef("database-snapshot/v1")},
			},
			outputs: []workflow.SignaturePort{
				{Name: "findings", Type: snapshot.TypeRef("audit-findings/v1")},
				{Name: "change", Type: snapshot.TypeRef("repository-change/v1"), Optional: true},
			},
		},
		{
			directory: "seeds/log-diagnosis-v3",
			name:      "log-diagnosis",
			inputs: []workflow.SignaturePort{
				{Name: "logs", Type: snapshot.TypeRef("log-bundle/v1")},
				{Name: "deployment", Type: snapshot.TypeRef("deployment-snapshot/v1"), Optional: true},
			},
			outputs: []workflow.SignaturePort{
				{Name: "diagnosis", Type: snapshot.TypeRef("diagnosis/v1")},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest, err := workflow.ManifestFromDir(test.directory)
			if err != nil {
				t.Fatalf("ManifestFromDir: %v", err)
			}
			definition, err := workflow.NewMemoryStore().ImportManifest(test.name, manifest, "seed-test")
			if err != nil {
				t.Fatalf("compile/import seed: %v", err)
			}
			if definition.SchemaVersion != 3 || definition.SignatureVersion != 1 {
				t.Fatalf("version identity = schema %d signature %d", definition.SchemaVersion, definition.SignatureVersion)
			}
			signature, err := definition.Compiled.PublicSignature()
			if err != nil {
				t.Fatalf("PublicSignature: %v", err)
			}
			if !reflect.DeepEqual(signature.Inputs, test.inputs) || !reflect.DeepEqual(signature.Outputs, test.outputs) {
				t.Fatalf("signature = %+v, want inputs=%+v outputs=%+v", signature, test.inputs, test.outputs)
			}
			if definition.Compiled.Function.DispositionOutput != test.dispositionOutput {
				t.Fatalf("disposition_output = %q, want %q", definition.Compiled.Function.DispositionOutput, test.dispositionOutput)
			}

			target, err := workflow.FullFunctionTarget(*definition)
			if err != nil {
				t.Fatalf("FullFunctionTarget: %v", err)
			}
			rendered, err := workflow.RenderFunction(target)
			if err != nil {
				t.Fatalf("RenderFunction: %v", err)
			}
			if len(rendered.Config.Jobs) != 1 || len(rendered.Config.Jobs[0].PlanSequence) <= len(test.inputs) {
				t.Fatalf("rendered plan omitted the authored DAG: %+v", rendered.Config.Jobs)
			}
			var waits, publishers, harvests int
			recursor := atc.StepRecursor{
				OnAwaitSnapshot:   func(*atc.AwaitSnapshotStep) error { waits++; return nil },
				OnPublishSnapshot: func(*atc.PublishSnapshotStep) error { publishers++; return nil },
				OnHarvest:         func(*atc.HarvestStep) error { harvests++; return nil },
			}
			for _, step := range rendered.Config.Jobs[0].PlanSequence {
				if err := step.Config.Visit(recursor); err != nil {
					t.Fatalf("inspect rendered plan: %v", err)
				}
			}
			if harvests != 0 {
				t.Fatal("version-3 seed gained an implicit compatibility harvest")
			}
			if (waits > 0) != test.humanWait || (publishers > 0) != test.publisher {
				t.Fatalf("visible boundaries: waits=%d publishers=%d, want wait=%t publisher=%t", waits, publishers, test.humanWait, test.publisher)
			}
			if strings.Contains(string(manifest["workflow.yml"]), "ticket_id") || strings.Contains(string(manifest["workflow.yml"]), "workspace") {
				t.Fatal("version-3 seed is coupled to the legacy ticket/workspace model")
			}
		})
	}
}

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

// TestSeedDevelopFlavorsValidate covers the two manual-dispatch dev seeds
// (develop, develop-fable) that run live on concourse.home. Both carry the
// resolve-once workspace protocol: ticket #16's agent (build 567384)
// re-expanded $AGENT_OUTPUT_WORKSPACE per shell call, one expansion came up
// empty, and the protocol cp copied the repo checkout onto "/". The seeds
// must keep pinning the literal-path discipline, not per-call expansion.
func TestSeedDevelopFlavorsValidate(t *testing.T) {
	for _, seed := range []struct {
		file, name string
	}{
		{"seeds/develop.yaml", "develop"},
		{"seeds/develop-fable.yaml", "develop-fable"},
	} {
		raw, err := os.ReadFile(seed.file)
		if err != nil {
			t.Fatalf("read seed: %v", err)
		}
		cfg, err := workflow.Parse(raw)
		if err != nil {
			t.Fatalf("%s must validate: %v", seed.file, err)
		}
		if cfg.Name != seed.name {
			t.Errorf("%s Name = %q", seed.file, cfg.Name)
		}
		// v0 manual dispatch renders spec_delivery: files only.
		if cfg.SpecDelivery != "files" {
			t.Errorf("%s spec_delivery = %q, want files", seed.file, cfg.SpecDelivery)
		}
		// The import gate guarantees a workspace-producing step; keep the
		// seed shaped so the terminal harvest has something to push.
		producesWorkspace := false
		for _, s := range cfg.Steps {
			for _, out := range s.Outputs {
				if out == "workspace" {
					producesWorkspace = true
				}
			}
		}
		if !producesWorkspace {
			t.Errorf("%s must declare a step producing the workspace output", seed.file)
		}
		// Resolve-once protocol: the prompt must point at the runner's
		// "# Step outputs" literal block and forbid re-expansion.
		prompt := cfg.Prompts["do"]
		for _, want := range []string{"# Step outputs", "ONCE", "NEVER"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s prompt lost the resolve-once workspace protocol (missing %q)", seed.file, want)
			}
		}
	}
}
