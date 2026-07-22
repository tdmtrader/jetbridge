package workflow

import "fmt"

// Config is the legacy schema-version-1/2 parsed form of the
// workflow-definition YAML. Version 3 uses FunctionConfig and is dispatched
// through ParseCompiled; ticket-specific validation must not be applied to it.
// (docs/superpowers/plans/agentic-platform/00-shared-contracts.md §6,
// schema_version 1). Grammar decisions: §6.1 (linear agent/checkpoint
// sequence), §6.2 (prompts inline, covered by the content hash),
// §6.3 (gate-policy YAML grammar; interpreted by harvest-step in wave 3).
type Config struct {
	SchemaVersion int    `yaml:"schema_version" json:"schema_version"`
	Name          string `yaml:"name" json:"name"`
	Description   string `yaml:"description,omitempty" json:"description,omitempty"`
	// SpecDelivery selects how the ticket's spec/plan reach agent steps at
	// render time: "mcp" (default when empty — agents read via platform-mcp
	// read_ticket/list_tasks/get_task, no bytes injected) or "files"
	// (read-only spec.md/plan.md mounted as the "ticket" artifact). Consumed
	// by dispatch's renderer; a normal hashed field (contracts §6).
	SpecDelivery string             `yaml:"spec_delivery,omitempty" json:"spec_delivery,omitempty"`
	Defaults     Defaults           `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	Budget       Budget             `yaml:"budget,omitempty" json:"budget,omitempty"`
	Sidecars     map[string]Sidecar `yaml:"sidecars,omitempty" json:"sidecars,omitempty"`
	Prompts      map[string]string  `yaml:"prompts,omitempty" json:"prompts,omitempty"`
	Schemas      map[string]string  `yaml:"schemas,omitempty" json:"schemas,omitempty"`
	Steps        []Step             `yaml:"steps" json:"steps"`
	HITL         HITL               `yaml:"hitl,omitempty" json:"hitl,omitempty"`
	GatePolicy   GatePolicy         `yaml:"gate_policy,omitempty" json:"gate_policy,omitempty"`
	Judge        *Judge             `yaml:"judge,omitempty" json:"judge,omitempty"`

	// schema_version 2 source-format fields (design 2026-07-17 §1).
	// PromptFiles maps a prompt key to a manifest path; Compile inlines
	// the content into Prompts. skills/ names resolve to skills/<name>/
	// trees; Context paths resolve into ContextFiles. SystemPrompt is
	// appended to the runner's baseline (steps replace the workflow
	// layer, never the baseline).
	PromptFiles      map[string]string `yaml:"prompt_files,omitempty" json:"prompt_files,omitempty"`
	Skills           []string          `yaml:"skills,omitempty" json:"skills,omitempty"`
	SystemPrompt     string            `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	SystemPromptFile string            `yaml:"system_prompt_file,omitempty" json:"system_prompt_file,omitempty"`
	Context          []string          `yaml:"context,omitempty" json:"context,omitempty"`

	// Compile-populated resolutions (never authorable in YAML):
	// context path -> content, and skill file path -> content for every
	// referenced skill's tree. The compiled Config is self-contained —
	// the render path never reads the manifest (design §3).
	ContextFiles map[string]string `yaml:"-" json:"context_files,omitempty"`
	SkillFiles   map[string]string `yaml:"-" json:"skill_files,omitempty"`
}

// Defaults apply to every agent step unless the step overrides them.
type Defaults struct {
	Model    string `yaml:"model,omitempty" json:"model,omitempty"`
	MaxTurns int    `yaml:"max_turns,omitempty" json:"max_turns,omitempty"`
}

// Budget is the workflow's default money envelope (§6; tickets override
// ticket_usd, the judge cap is funded per contracts §1.13).
type Budget struct {
	TicketUSD float64 `yaml:"ticket_usd,omitempty" json:"ticket_usd,omitempty"`
	JudgeUSD  float64 `yaml:"judge_usd,omitempty" json:"judge_usd,omitempty"`
}

// Sidecar is a named MCP sidecar declaration; steps reference by name.
type Sidecar struct {
	Image     string   `yaml:"image" json:"image"` // must carry an explicit ':<version>' tag
	Role      string   `yaml:"role" json:"role"`   // dev | platform | gateway | custom
	Providers []string `yaml:"providers,omitempty" json:"providers,omitempty"`
}

// Step is exactly one of an agent step or a checkpoint (§6.1).
type Step struct {
	// agent step fields
	Agent          string   `yaml:"agent,omitempty" json:"agent,omitempty"`
	Prompt         string   `yaml:"prompt,omitempty" json:"prompt,omitempty"` // key into Prompts
	Sidecars       []string `yaml:"sidecars,omitempty" json:"sidecars,omitempty"`
	BudgetSliceUSD float64  `yaml:"budget_slice_usd,omitempty" json:"budget_slice_usd,omitempty"`
	Model          string   `yaml:"model,omitempty" json:"model,omitempty"`
	MaxTurns       int      `yaml:"max_turns,omitempty" json:"max_turns,omitempty"`
	Inputs         []string `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Outputs        []string `yaml:"outputs,omitempty" json:"outputs,omitempty"`
	OutputSchema   string   `yaml:"output_schema,omitempty" json:"output_schema,omitempty"` // key into Schemas

	// schema_version 2 source-format fields: additive skills/context on
	// top of the workflow-global sets; step system_prompt REPLACES the
	// workflow-level layer (not the runner baseline).
	Skills           []string `yaml:"skills,omitempty" json:"skills,omitempty"`
	SystemPrompt     string   `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	SystemPromptFile string   `yaml:"system_prompt_file,omitempty" json:"system_prompt_file,omitempty"`
	Context          []string `yaml:"context,omitempty" json:"context,omitempty"`

	// checkpoint fields (declared-but-inert until platform-mcp-hitl, wave 3)
	Checkpoint string `yaml:"checkpoint,omitempty" json:"checkpoint,omitempty"`
	OnReject   string `yaml:"on_reject,omitempty" json:"on_reject,omitempty"` // fail | send_back
}

// HITL is the ask_human timeout policy block (declared-but-inert until
// platform-mcp-hitl, wave 3).
type HITL struct {
	AskTimeout        string `yaml:"ask_timeout,omitempty" json:"ask_timeout,omitempty"` // park | default | fail
	AskTimeoutSeconds int    `yaml:"ask_timeout_seconds,omitempty" json:"ask_timeout_seconds,omitempty"`
}

// GatePolicy mirrors the §6.3 YAML grammar. Declared-but-inert in wave 1:
// this package validates the shape; harvest-step (wave 3) owns the
// interpreting Go type in agent/harvest/policy.go.
type GatePolicy struct {
	Gates         []Gate `yaml:"gates,omitempty" json:"gates"`
	OnGateFailure string `yaml:"on_gate_failure,omitempty" json:"on_gate_failure"` // "needs_review" (only v1 value)
}

// Gate is one named check in the gate policy.
type Gate struct {
	Gate    string `yaml:"gate" json:"gate"`   // build | test | lint
	Scope   string `yaml:"scope" json:"scope"` // affected | full | affected_then_full
	Focus   string `yaml:"focus,omitempty" json:"focus,omitempty"`
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"` // Go duration; default 30m (harvest-side)
	Retries int    `yaml:"retries,omitempty" json:"retries,omitempty"` // 0-2 failed-only re-runs; interpreted by harvest-step (§6.3 flake stance)
}

// Judge is the rubric block for the schema-constrained judge (§6.4;
// declared-but-inert until harvest-step, wave 3).
type Judge struct {
	Rubric        []RubricDimension `yaml:"rubric" json:"rubric"`
	PassThreshold float64           `yaml:"pass_threshold" json:"pass_threshold"` // 0-10 weighted total
}

// RubricDimension is one scored dimension of the judge rubric.
type RubricDimension struct {
	Name     string  `yaml:"name" json:"name"`
	Weight   float64 `yaml:"weight" json:"weight"`
	Guidance string  `yaml:"guidance,omitempty" json:"guidance,omitempty"`
}

// SourceFormatField names the first schema_version-2 source-format
// field in use, or "". Two consumers: the v1 schema gate in Validate,
// and dispatch's v0 render refusal (slice (b) materializes these;
// until then rendering them would silently drop authored behavior).
func (c *Config) SourceFormatField() string {
	switch {
	case len(c.PromptFiles) > 0:
		return "prompt_files"
	case len(c.Skills) > 0:
		return "skills"
	case c.SystemPrompt != "" || c.SystemPromptFile != "":
		return "system_prompt"
	case len(c.Context) > 0:
		return "context"
	}
	for _, s := range c.Steps {
		name := s.Agent
		if name == "" {
			name = s.Checkpoint
		}
		switch {
		case len(s.Skills) > 0:
			return fmt.Sprintf("step %q skills", name)
		case s.SystemPrompt != "" || s.SystemPromptFile != "":
			return fmt.Sprintf("step %q system_prompt", name)
		case len(s.Context) > 0:
			return fmt.Sprintf("step %q context", name)
		}
	}
	return ""
}
