package workflow

import (
	"fmt"
	"io"
	"strings"
	"text/template"
	"time"

	"github.com/goccy/go-yaml"
)

// Parse parses and eagerly validates a workflow definition
// (phaseconfig-style: any structural problem is an import-time error).
func Parse(raw []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse workflow definition: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

var validSidecarRoles = map[string]bool{"dev": true, "platform": true, "gateway": true, "custom": true}
var validGates = map[string]bool{"build": true, "test": true, "lint": true}
var validGateScopes = map[string]bool{"affected": true, "full": true, "affected_then_full": true}

// nilRenderContext mirrors the frozen §6.2 render context (.Ticket .Spec
// .Tasks .Params) in its dispatch-time ground state: .Spec nil and .Tasks
// empty — always true at render, which happens BEFORE any agent step can
// submit a spec (contracts §6.2 nil-safety, 2026-07-09). .Ticket and .Params
// are tolerant maps so envelope/params field access renders "<no value>"
// instead of erroring (the envelope is always present at render; only a
// nil-deref is the import-blocking bug). Validation-only mirror — dispatch
// owns the real context type.
var nilRenderContext = struct {
	Ticket map[string]any
	Spec   *struct{}
	Tasks  []map[string]any
	Params map[string]any
}{
	Ticket: map[string]any{},
	Spec:   nil,
	Tasks:  []map[string]any{},
	Params: map[string]any{},
}

// Validate checks the §6 grammar rules. Unknown YAML keys are ignored
// (forward compatibility); known keys are strictly checked.
func (c *Config) Validate() error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("workflow: schema_version must be 1, got %d", c.SchemaVersion)
	}
	if c.Name == "" {
		return fmt.Errorf("workflow: name is required")
	}
	switch c.SpecDelivery {
	case "", "mcp", "files":
		// "" ⇒ mcp (the default); dispatch's renderer treats the empty
		// string identically to "mcp".
	default:
		return fmt.Errorf("workflow: spec_delivery must be mcp or files, got %q", c.SpecDelivery)
	}
	if c.Budget.TicketUSD < 0 {
		return fmt.Errorf("workflow: budget.ticket_usd must be >= 0")
	}
	if c.Budget.JudgeUSD < 0 {
		return fmt.Errorf("workflow: budget.judge_usd must be >= 0")
	}

	for name, sc := range c.Sidecars {
		if sc.Image == "" {
			return fmt.Errorf("workflow: sidecar %q: image is required", name)
		}
		// The tag must be pinned: the segment after the final '/' must
		// contain ':' (handles registry:port hosts correctly).
		last := sc.Image[strings.LastIndex(sc.Image, "/")+1:]
		if !strings.Contains(last, ":") {
			return fmt.Errorf("workflow: sidecar %q: image %q must carry a pinned ':<version>' tag", name, sc.Image)
		}
		if !validSidecarRoles[sc.Role] {
			return fmt.Errorf("workflow: sidecar %q: role must be one of dev|platform|gateway|custom, got %q", name, sc.Role)
		}
		if len(sc.Providers) > 0 && sc.Role != "gateway" {
			return fmt.Errorf("workflow: sidecar %q: providers is only valid for role gateway", name)
		}
	}

	for key, body := range c.Prompts {
		tmpl, err := template.New(key).Parse(body)
		if err != nil {
			return fmt.Errorf("workflow: prompt %q: invalid Go text/template: %w", key, err)
		}
		// E2b (contracts §6.2 nil-safety, 2026-07-09): execute the template
		// against the spec-less context every dispatch render sees, so a bare
		// `.Spec.<field>` deref fails HERE with a clear error instead of
		// failing the dispatch of every ticket that runs this definition.
		if err := tmpl.Execute(io.Discard, nilRenderContext); err != nil {
			return fmt.Errorf("workflow: prompt %q: does not render against a spec-less ticket (.Spec is nil and .Tasks is empty at every dispatch render — guard with {{if .Spec}} or read via platform-mcp read_ticket/list_tasks): %w", key, err)
		}
	}

	if len(c.Steps) == 0 {
		return fmt.Errorf("workflow: at least one step is required")
	}
	seen := map[string]bool{}
	// "repo" and "ticket" are renderer-provided reserved artifacts: the
	// ticket's git checkout and the spec.md/plan.md files-delivery input
	// (dispatch manual-dispatch slice addendum, 2026-07-17). Steps may
	// consume them without an earlier producer.
	produced := map[string]bool{"repo": true, "ticket": true}
	for i, s := range c.Steps {
		isAgent := s.Agent != ""
		isCheckpoint := s.Checkpoint != ""
		if isAgent == isCheckpoint {
			return fmt.Errorf("workflow: step %d: exactly one of 'agent' or 'checkpoint' is required", i)
		}
		if isAgent {
			if seen[s.Agent] {
				return fmt.Errorf("workflow: step %d: duplicate step name %q", i, s.Agent)
			}
			seen[s.Agent] = true
			if s.Prompt == "" {
				return fmt.Errorf("workflow: agent step %q: prompt is required", s.Agent)
			}
			if _, ok := c.Prompts[s.Prompt]; !ok {
				return fmt.Errorf("workflow: agent step %q: unknown prompt %q", s.Agent, s.Prompt)
			}
			for _, name := range s.Sidecars {
				if _, ok := c.Sidecars[name]; !ok {
					return fmt.Errorf("workflow: agent step %q: unknown sidecar %q", s.Agent, name)
				}
			}
			if s.BudgetSliceUSD < 0 {
				return fmt.Errorf("workflow: agent step %q: budget_slice_usd must be >= 0", s.Agent)
			}
			if s.MaxTurns < 0 {
				return fmt.Errorf("workflow: agent step %q: max_turns must be >= 0", s.Agent)
			}
			for _, in := range s.Inputs {
				if !produced[in] {
					return fmt.Errorf("workflow: agent step %q: input %q is not produced by an earlier step", s.Agent, in)
				}
			}
			for _, out := range s.Outputs {
				produced[out] = true
			}
			if s.OutputSchema != "" {
				if _, ok := c.Schemas[s.OutputSchema]; !ok {
					return fmt.Errorf("workflow: agent step %q: output_schema %q has no entry in the top-level schemas map", s.Agent, s.OutputSchema)
				}
			}
			if s.OnReject != "" {
				return fmt.Errorf("workflow: agent step %q: on_reject is a checkpoint-only field", s.Agent)
			}
		} else {
			if seen[s.Checkpoint] {
				return fmt.Errorf("workflow: step %d: duplicate step name %q", i, s.Checkpoint)
			}
			seen[s.Checkpoint] = true
			switch s.OnReject {
			case "", "fail", "send_back":
			default:
				return fmt.Errorf("workflow: checkpoint %q: on_reject must be fail or send_back, got %q", s.Checkpoint, s.OnReject)
			}
			if s.Prompt != "" || len(s.Sidecars) > 0 || s.BudgetSliceUSD != 0 || s.Model != "" ||
				s.MaxTurns != 0 || len(s.Inputs) > 0 || len(s.Outputs) > 0 || s.OutputSchema != "" {
				return fmt.Errorf("workflow: checkpoint %q: agent-step fields are not allowed on a checkpoint", s.Checkpoint)
			}
			// E6b (FLOWS S5, 2026-07-09): import mirror of dispatch's F36
			// render-time guard — the rendered checkpoint task mounts the
			// definition's "platform" sidecar (the fixed role key); without
			// that entry every dispatch errors at render. Fail at import.
			if _, ok := c.Sidecars["platform"]; !ok {
				return fmt.Errorf("workflow: checkpoint %q requires a %q sidecar in the workflow definition (dispatch's F36 render guard, mirrored at import)", s.Checkpoint, "platform")
			}
		}
	}

	// E6a (FLOWS S4, 2026-07-09): the implicit terminal harvest step consumes
	// the "workspace" artifact (11-dispatch hard-codes it as harvest's input,
	// for push and advisory shapes alike). A definition in which no step
	// outputs it would import clean and then fail every run at harvest —
	// close the validate-clean/run-broken gap here.
	if !produced["workspace"] {
		return fmt.Errorf("workflow: no step outputs %q — the implicit harvest step consumes it, so every run of this definition would fail at harvest", "workspace")
	}

	switch c.HITL.AskTimeout {
	case "", "park", "default", "fail":
	default:
		return fmt.Errorf("workflow: hitl.ask_timeout must be park, default, or fail, got %q", c.HITL.AskTimeout)
	}
	if c.HITL.AskTimeoutSeconds < 0 {
		return fmt.Errorf("workflow: hitl.ask_timeout_seconds must be >= 0")
	}
	// A default/fail timeout policy needs a positive deadline to act on: with
	// ask_timeout_seconds <= 0 the ask never times out, so the policy never
	// fires and the run parks forever anyway — the opposite of what the author
	// asked for. Reject the incoherent combination loudly at import instead.
	if (c.HITL.AskTimeout == "default" || c.HITL.AskTimeout == "fail") && c.HITL.AskTimeoutSeconds <= 0 {
		return fmt.Errorf("workflow: hitl.ask_timeout %q requires ask_timeout_seconds > 0 (got %d) — otherwise the ask never times out and the run parks forever", c.HITL.AskTimeout, c.HITL.AskTimeoutSeconds)
	}

	for i, g := range c.GatePolicy.Gates {
		if !validGates[g.Gate] {
			return fmt.Errorf("workflow: gate_policy.gates[%d]: gate must be build|test|lint, got %q", i, g.Gate)
		}
		if !validGateScopes[g.Scope] {
			return fmt.Errorf("workflow: gate_policy.gates[%d]: scope must be affected|full|affected_then_full, got %q", i, g.Scope)
		}
		if g.Timeout != "" {
			if _, err := time.ParseDuration(g.Timeout); err != nil {
				return fmt.Errorf("workflow: gate_policy.gates[%d]: invalid timeout %q", i, g.Timeout)
			}
		}
		if g.Retries < 0 || g.Retries > 2 {
			return fmt.Errorf("workflow: gate_policy.gates[%d]: retries must be 0-2, got %d", i, g.Retries)
		}
	}
	if len(c.GatePolicy.Gates) > 0 && c.GatePolicy.OnGateFailure != "needs_review" {
		return fmt.Errorf("workflow: gate_policy.on_gate_failure must be needs_review (only v1 value), got %q", c.GatePolicy.OnGateFailure)
	}

	if c.Judge != nil {
		if len(c.Judge.Rubric) == 0 {
			return fmt.Errorf("workflow: judge.rubric must have at least one dimension")
		}
		dims := map[string]bool{}
		for _, d := range c.Judge.Rubric {
			if d.Name == "" {
				return fmt.Errorf("workflow: judge.rubric: dimension name is required")
			}
			if dims[d.Name] {
				return fmt.Errorf("workflow: judge.rubric: duplicate dimension %q", d.Name)
			}
			dims[d.Name] = true
			if d.Weight <= 0 {
				return fmt.Errorf("workflow: judge.rubric %q: weight must be > 0", d.Name)
			}
		}
		if c.Judge.PassThreshold < 0 || c.Judge.PassThreshold > 10 {
			return fmt.Errorf("workflow: judge.pass_threshold must be within [0,10]")
		}
	}

	return nil
}
