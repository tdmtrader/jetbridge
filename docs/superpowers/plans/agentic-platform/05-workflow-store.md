# Versioned Workflow Definition Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Store, hash, version, import, and promote workflow definitions in `agent_workflow_definitions` — with the §6 composition grammar validated at write time, a content-hash provenance function generalized from ci-agent, API + `fly agent workflows` commands, and today's five ci-agent phases decomposed into a seed `standard-dev` v1 definition.

**Architecture:** A new root-module package `agent/workflow` owns the parsed `Definition`/`Config` types, the YAML grammar parser with phaseconfig-style eager validation, the sha256 content hash, and the `Store` interface. `atc/db/agent_workflows_factory.go` implements `Store` over migration `1773106040` (following the `agent_reviews_factory.go` recipe), `agent/api/workflows` serves the five §4.2 routes (following `agent/api/reviews`), and `fly agent workflows list/show/import/set-live` drives them over the authenticated `target.Client().HTTPClient()`. The renderer, execution, and scorecards are explicitly NOT built here — checkpoint and gate-policy slots are declared-but-inert grammar that this store only validates.

**Tech Stack:** Go (root module), `github.com/goccy/go-yaml` v1.19.1 (already a direct dep, same lib as `ci-agent/phaseconfig`), PostgreSQL migration via `atc/db/migration` embed, squirrel/`psql` + plain SQL in the factory, Ginkgo/Gomega for `atc/db`/`atc/api`/`atc/wrappa`/`fly/integration` suites, plain `testing` for `agent/*` packages (matching `agent/api/reviews`).

---

## Context

**Charter (workstreams.json id `workflow-store`, wave 1, size M):**

- Workflow schema grammar v1 (spec open item 2): linear ordered step list, inline prompt packaging (decided in contracts §6.2 — inline, so the content hash covers prompts), model(s), MCP sidecar mix, named checkpoint slots, gate-policy slot, default budget. Checkpoint and gate-policy slots are **declared-but-inert**: this workstream validates their shape and stores them; platform-mcp-hitl and harvest-step (wave 3) execute them.
- `agent_workflow_definitions` migration + factory (name, version, content hash, live flag, config blob) with write-time validation.
- Content hashing generalized from ci-agent's provenance mechanism (`ci-agent/phaseconfig.Hash` — sha256 hex over raw bytes; ci-agent is a separate Go module with no dependency edge to the root module, so the function is re-declared in `agent/workflow` with identical semantics, not imported).
- API + `fly agent workflows import/list/show/set-live` (import from repo files; promotion is a human action, no gate).
- Seed library: today's five ci-agent phases (`ci-agent/phases/{plan,implement,qa,review,fix}.yaml`) decomposed into a v1 definition.

**Scope OUT (do not implement):** the renderer (dispatch owns it; the `agent:` step never reads these tables — render-time-resolution rule, contracts §2.8), execution of definitions (agent-step, harvest-step), scorecard comparison (scorecards), any Elm UI.

**Prior waves:** none — this is wave 1; nothing is assumed landed. Wave-mates (agent-identity, credentials-and-budgets, pipeline-runs, dev-mcp) run in parallel. Two coordination points with wave-mates are handled explicitly:

1. **Auth wrappa placement.** Contracts §4.2 (decision 21) assigns the `CheckAgentAuthorizationHandler` (hardcoded team `main` for team-less `/api/v1/agent/*` authorized routes) to **agent-identity**. Until it lands, this plan wires the five workflow routes into the existing `auth.CheckAuthorizationHandler` case group — exactly where the agent feedback routes sit today — plus `DefaultRoles` entries. Per decision 21 these are effectively **admin-only** until agent-identity's handler lands and moves them (agent-identity owns that move; the `DefaultRoles` entries added here become effective at that moment with no further change in this workstream).
2. **`fly agent` command group.** credentials-and-budgets adds `fly agent auth` in the same wave. Whichever branch lands first creates the `AgentCommand` group struct; the second adds one field to it (trivial merge, noted in Task 10).

**Contract surfaces this plan PRODUCES** (owner: workflow-store), from `00-shared-contracts.md`:
- §1.6 `agent_workflow_definitions` (migration `1773106040` from the workflow-store block 1773106040–49)
- §2.2 WorkflowDefinition (`agent/workflow/definition.go` — `Definition`, `Config`, `Store`)
- §6 Workflow-definition YAML schema (+ §6.1 step grammar, §6.2 prompt packaging, §6.3 gate-policy YAML grammar — the *Go* `GatePolicy` in `agent/harvest/policy.go` stays owned by harvest-step; this plan defines a YAML-shape mirror in `agent/workflow` for validation only)
- §4.2 rows `ListAgentWorkflows`, `ListAgentWorkflowVersions`, `GetAgentWorkflowVersion`, `CreateAgentWorkflowVersion`, `PromoteAgentWorkflowVersion`

**Contract surfaces this plan CONSUMES:**
- §1.1 Wave/number allocation (migration block) and the document-wide conventions (factory recipe, TIMESTAMPTZ/epoch-seconds JSON, no cross-aggregate FKs)
- §4.1/§4.2 "Auth tiers" + decision 21 (`CheckAgentAuthorizationHandler`, produced by agent-identity — consumed as the future home of these routes, see coordination point 1)
- §2.8 agent/harvest step config and §3.2 platform-mcp checkpoint semantics — read-only design constraints on the grammar (the grammar's `agent:`/`checkpoint:` fields must resolve into those shapes at render time); nothing is imported from them in wave 1.

**Contract deviations found against real code/doc (resolved in Task 1 as owner amendments to §11):**
- §2.2 `Store.Promote(name, version int, promotedBy string)` takes `promotedBy`, but the §1.6 DDL has no column to persist it — Task 1 amends §1.6 to add `promoted_by TEXT NOT NULL DEFAULT ''`.
- §2.2 `Definition` has no field carrying the raw stored YAML, but `fly agent workflows show` and hash re-verification need the exact hashed bytes — Task 1 amends §2.2 to add a `RawYAML` field (populated by `Get`/`Live`, empty in `List`/`Versions`).
- §6.1 references an "optional top-level `schemas` map" for `output_schema` keys that the §6 YAML example omits — the `Config` type here includes `schemas: map[string]string` and validates `output_schema` references against it.

---

### Task 1: Contract addendum — owner amendments and slot-shape freeze

The §11 amendment log is the required mechanism for contract changes ("changes require a cross-workstream sign-off note appended to §11"). This task records the two owner amendments above, freezes the declared-but-inert slot shapes for wave-3 consumer review, and pins the HTTP request/response shapes for the five routes (the contracts doc names routes and auth tiers but not bodies).

**Files:**

- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md:181` (add `promoted_by` line to §1.6 DDL), `:551` (add `RawYAML` field to §2.2 struct), end of file §11 (append amendment entry)

**Steps:**

- [ ] In §1.6's `CREATE TABLE agent_workflow_definitions` block, insert after the `promoted_at  TIMESTAMPTZ,` line:

```sql
    promoted_by  TEXT NOT NULL DEFAULT '',       -- who ran set-live (audit; 2026-07 owner amendment)
```

- [ ] In §2.2's `Definition` struct, insert after the `Config Config` line:

```go
	// RawYAML is the exact stored definition bytes (the hashed provenance
	// unit). Populated by Get and Live; empty in List/Versions.
	RawYAML string `json:"raw_yaml,omitempty"`
```

- [ ] Append to the §11 amendment log:

```markdown
- 2026-07-08 (workflow-store, owner amendments; affects consumers: dispatch, harvest-step, platform-mcp-hitl, scorecards, process-intel-experiments — additive only):
  - §1.6: added `promoted_by TEXT NOT NULL DEFAULT ''` so `Store.Promote`'s existing `promotedBy` argument is persisted (the interface already carried it; the DDL had no column).
  - §2.2: added `Definition.RawYAML` (`json:"raw_yaml,omitempty"`) carrying the exact stored YAML bytes on `Get`/`Live` responses; `List`/`Versions` leave it empty (and leave `config` as a zero object — metadata-only listings).
  - §4.2 workflow-route HTTP shapes pinned by the owner:
    - `GET /api/v1/agent/workflows` → 200 `[{"name","description","latest_version","content_hash","live_version","created_at"}]` (`live_version` 0 = none live).
    - `GET /api/v1/agent/workflows/:workflow_name/versions` → 200 `[Definition]` (metadata only), 404 unknown name.
    - `GET /api/v1/agent/workflows/:workflow_name/versions/:version` → 200 `Definition` incl. `config` + `raw_yaml`, 404 unknown, 400 non-integer version.
    - `POST /api/v1/agent/workflows/:workflow_name/versions` — body is the raw definition YAML (any Content-Type, ≤1 MiB) → 200 `Definition` (idempotent on content hash: re-importing identical bytes returns the existing version), 400 on parse/validation/name-mismatch, 413 oversize.
    - `PUT /api/v1/agent/workflows/:workflow_name/versions/:version/live` → 204, 404 unknown (name, version).
  - §6 grammar: added the optional top-level `spec_delivery` field (Go `Config.SpecDelivery string`, yaml/json `spec_delivery,omitempty`; values `""`/`mcp`/`files`, empty ⇒ `mcp`; a normal hashed field, write-time validated to reject any other value). This replaces the prior "rendered spec.md/plan.md as env vars `AGENT_SPEC_MD`/`AGENT_PLAN_MD`" design: the DB stays the single source of truth and nothing is flattened by default. Owned by workflow-store (§6), referenced by contracts §6, consumed by dispatch's renderer (11-dispatch) — `mcp` injects no spec/plan bytes (agents read via platform-mcp `read_ticket`/`list_tasks`/`get_task`, implemented by platform-mcp-hitl over ticket-core `Store` methods `Get`/`LatestSpec`/`ActivePlan`); `files` materializes read-only `spec.md`/`plan.md` mounted as the `ticket` artifact. Affects consumers: platform-mcp-hitl, dispatch, workflow-store, ticket-core-consumers.
  - Slot-shape freeze for wave-3 review: the `checkpoint:` step fields (`on_reject: fail|send_back`), `hitl` block (`ask_timeout: park|default|fail`, `ask_timeout_seconds`), `gate_policy` block (§6.3 YAML grammar — each `gates[]` entry carries `gate`, `scope`, `focus`, `timeout`, and the optional `retries: 0..2` flake-retry key harvest-step consumes; workflow-store validates the `0..2` bound at import and carries `Gate.Retries` through so dispatch's renderer can map it onto `harvest.Gate.Retries`), and `judge` block are stored and write-time validated by workflow-store but INERT until platform-mcp-hitl and harvest-step consume them; those workstreams review these shapes at wave-3 start and any change lands as a new `schema_version`, never a mutation of v1. §6.1's "optional top-level `schemas` map" is realized as `schemas: map[string]string` in `Config`. The optional top-level `spec_delivery` field (`SpecDelivery string`, values `""`/`mcp`/`files`, empty ⇒ `mcp`) is a normal hashed field owned by this grammar and validated at import; it is INERT here (workflow-store never renders) and is consumed by dispatch's renderer to pick the spec/plan read model — `mcp` (default: no spec/plan bytes injected; agents read via platform-mcp `read_ticket`/`list_tasks`/`get_task`) vs `files` (read-only `spec.md`/`plan.md` mounted as the `ticket` artifact).
  - Wrappa placement note: the five workflow routes land in the existing `auth.CheckAuthorizationHandler` case group (admin-only in effect, per decision 21) with `DefaultRoles` entries in place; agent-identity moves them onto `CheckAgentAuthorizationHandler` together with the existing agent feedback routes.
```

- [ ] Commit: `git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md && git commit -m "docs(agentic-platform): workflow-store owner amendments — promoted_by, RawYAML, route shapes, slot freeze"`

---

### Task 2: Migration 1773106040 — `agent_workflow_definitions`

**Files:**

- Create: `atc/db/migration/migrations/1773106040_create_agent_workflow_definitions.up.sql`
- Create: `atc/db/migration/migrations/1773106040_create_agent_workflow_definitions.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go:37` (bump `jetbridgeHeadMigration`)

**Steps:**

- [ ] Write `atc/db/migration/migrations/1773106040_create_agent_workflow_definitions.up.sql` (contracts §1.6 DDL + the Task 1 `promoted_by` amendment; migration files are picked up automatically via the `//go:embed migrations` in `atc/db/migration/migration.go:153`):

```sql
CREATE TABLE agent_workflow_definitions (
    id           SERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    version      INTEGER NOT NULL,
    content_hash TEXT NOT NULL,
    definition   TEXT NOT NULL,
    live         BOOLEAN NOT NULL DEFAULT false,
    description  TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    promoted_at  TIMESTAMPTZ,
    promoted_by  TEXT NOT NULL DEFAULT '',
    UNIQUE (name, version)
);

CREATE UNIQUE INDEX agent_workflow_definitions_live ON agent_workflow_definitions (name) WHERE live;
CREATE UNIQUE INDEX agent_workflow_definitions_hash ON agent_workflow_definitions (name, content_hash);
```

- [ ] Write `atc/db/migration/migrations/1773106040_create_agent_workflow_definitions.down.sql`:

```sql
DROP TABLE agent_workflow_definitions;
```

- [ ] Edit `atc/db/migration/legacy_upgrade_test.go:37` — change `const jetbridgeHeadMigration = 1773105504` to `const jetbridgeHeadMigration = 1773106040`. NOTE for mergers: wave-1 branches each bump this constant; on conflict keep the **highest** number present in `atc/db/migration/migrations/`.
- [ ] Run the migration suite (PostgreSQL must be up — `pg_isready` first): `ginkgo ./atc/db/migration/` — expect all specs green, including the legacy-upgrade spec now asserting version `1773106040`.
- [ ] Commit: `git add atc/db/migration && git commit -m "feat(workflow-store): agent_workflow_definitions migration (1773106040)"`

---

### Task 3: `agent/workflow` — Config types, Hash, and happy-path Parse

**Files:**

- Create: `agent/workflow/config.go`
- Create: `agent/workflow/hash.go`
- Create: `agent/workflow/parse.go`
- Test: `agent/workflow/parse_test.go`, `agent/workflow/hash_test.go`

**Steps:**

- [ ] Write the failing test `agent/workflow/parse_test.go`:

```go
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
```

- [ ] Write the failing test `agent/workflow/hash_test.go`:

```go
package workflow_test

import (
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

func TestHashMatchesPhaseconfigSemantics(t *testing.T) {
	// hex(sha256("hello")) — fixed vector so the fn provably matches
	// ci-agent/phaseconfig.Hash (same input → same output).
	got := workflow.Hash([]byte("hello"))
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Errorf("Hash = %s, want %s", got, want)
	}
	if workflow.Hash([]byte("hello")) == workflow.Hash([]byte("hello\n")) {
		t.Error("Hash must be byte-sensitive")
	}
}
```

- [ ] Run to verify failure: `go test ./agent/workflow/` — expect `no required module provides package` / `undefined: workflow` compile failure.
- [ ] Write `agent/workflow/config.go`:

```go
package workflow

// Config is the parsed form of the workflow-definition YAML
// (docs/superpowers/plans/agentic-platform/00-shared-contracts.md §6,
// schema_version 1). Grammar decisions: §6.1 (linear agent/checkpoint
// sequence), §6.2 (prompts inline, covered by the content hash),
// §6.3 (gate-policy YAML grammar; interpreted by harvest-step in wave 3).
type Config struct {
	SchemaVersion int                `yaml:"schema_version" json:"schema_version"`
	Name          string             `yaml:"name" json:"name"`
	Description   string             `yaml:"description,omitempty" json:"description,omitempty"`
	// SpecDelivery selects how the ticket's spec/plan reach agent steps at
	// render time: "mcp" (default when empty — agents read via platform-mcp
	// read_ticket/list_tasks/get_task, no bytes injected) or "files"
	// (read-only spec.md/plan.md mounted as the "ticket" artifact). Consumed
	// by dispatch's renderer; a normal hashed field (contracts §6).
	SpecDelivery  string             `yaml:"spec_delivery,omitempty" json:"spec_delivery,omitempty"`
	Defaults      Defaults           `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	Budget        Budget             `yaml:"budget,omitempty" json:"budget,omitempty"`
	Sidecars      map[string]Sidecar `yaml:"sidecars,omitempty" json:"sidecars,omitempty"`
	Prompts       map[string]string  `yaml:"prompts,omitempty" json:"prompts,omitempty"`
	Schemas       map[string]string  `yaml:"schemas,omitempty" json:"schemas,omitempty"`
	Steps         []Step             `yaml:"steps" json:"steps"`
	HITL          HITL               `yaml:"hitl,omitempty" json:"hitl,omitempty"`
	GatePolicy    GatePolicy         `yaml:"gate_policy,omitempty" json:"gate_policy,omitempty"`
	Judge         *Judge             `yaml:"judge,omitempty" json:"judge,omitempty"`
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
```

- [ ] Write `agent/workflow/hash.go`:

```go
package workflow

import (
	"crypto/sha256"
	"fmt"
)

// Hash returns hex(sha256(raw)) over the exact definition bytes — the
// same function as ci-agent/phaseconfig.Hash, generalized here as the
// platform's content-hash provenance primitive (contracts §1.6/§2.2).
// ci-agent is a separate Go module with no dependency on this one, so
// the function is re-declared rather than imported; the fixed test
// vector in hash_test.go pins the semantics.
func Hash(raw []byte) string {
	h := sha256.Sum256(raw)
	return fmt.Sprintf("%x", h)
}
```

- [ ] Write `agent/workflow/parse.go` (validation body arrives in Task 4; `Validate` starts minimal so this task compiles and the happy-path test passes):

```go
package workflow

import (
	"fmt"

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

// Validate checks the §6 grammar rules. Unknown YAML keys are ignored
// (forward compatibility); known keys are strictly checked.
func (c *Config) Validate() error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("workflow: schema_version must be 1, got %d", c.SchemaVersion)
	}
	if c.Name == "" {
		return fmt.Errorf("workflow: name is required")
	}
	if len(c.Steps) == 0 {
		return fmt.Errorf("workflow: at least one step is required")
	}
	return nil
}
```

- [ ] Run to verify pass: `go test ./agent/workflow/` — expect `ok`.
- [ ] Commit: `git add agent/workflow && git commit -m "feat(workflow-store): agent/workflow Config types, Hash, and Parse"`

---

### Task 4: `agent/workflow` — full grammar validation (write-time)

**Files:**

- Modify: `agent/workflow/parse.go` (replace the minimal `Validate` from Task 3)
- Test: `agent/workflow/validate_test.go`

**Steps:**

- [ ] Write the failing table-driven test `agent/workflow/validate_test.go`:

```go
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
```

- [ ] Run to verify failure: `go test ./agent/workflow/ -run TestValidate` — expect multiple sub-test failures ("expected validation error, got nil").
- [ ] Replace `Validate` in `agent/workflow/parse.go` with the full grammar (add imports `"strings"`, `"text/template"`, `"time"`):

```go
var validSidecarRoles = map[string]bool{"dev": true, "platform": true, "gateway": true, "custom": true}
var validGates = map[string]bool{"build": true, "test": true, "lint": true}
var validGateScopes = map[string]bool{"affected": true, "full": true, "affected_then_full": true}

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
		if _, err := template.New(key).Parse(body); err != nil {
			return fmt.Errorf("workflow: prompt %q: invalid Go text/template: %w", key, err)
		}
	}

	if len(c.Steps) == 0 {
		return fmt.Errorf("workflow: at least one step is required")
	}
	seen := map[string]bool{}
	produced := map[string]bool{}
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
		}
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
```

- [ ] Run to verify pass: `go test ./agent/workflow/` — expect `ok` (all reject cases now error with the expected substrings; both accept cases pass).
- [ ] Commit: `git add agent/workflow && git commit -m "feat(workflow-store): full §6 grammar validation at write time"`

---

### Task 5: `agent/workflow` — Definition, Store interface, errors, MemoryStore

**Files:**

- Create: `agent/workflow/definition.go`
- Create: `agent/workflow/memory_store.go`
- Test: `agent/workflow/memory_store_test.go`

**Steps:**

- [ ] Write the failing test `agent/workflow/memory_store_test.go` (this doubles as the executable spec for `Store` semantics the DB factory must match):

```go
package workflow_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

func defYAML(name, promptBody string) []byte {
	return []byte(`schema_version: 1
name: ` + name + `
prompts:
  work: |
    ` + promptBody + `
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`)
}

func TestMemoryStoreImportAssignsMonotonicVersions(t *testing.T) {
	s := workflow.NewMemoryStore()

	v1, err := s.Import("wf", defYAML("wf", "Do the work."), "alice")
	if err != nil {
		t.Fatalf("import v1: %v", err)
	}
	if v1.Version != 1 || v1.Name != "wf" || v1.CreatedBy != "alice" {
		t.Errorf("v1 = %+v", v1)
	}
	if v1.ContentHash != workflow.Hash(defYAML("wf", "Do the work.")) {
		t.Errorf("hash mismatch: %s", v1.ContentHash)
	}

	v2, err := s.Import("wf", defYAML("wf", "Do the work carefully."), "bob")
	if err != nil {
		t.Fatalf("import v2: %v", err)
	}
	if v2.Version != 2 {
		t.Errorf("v2.Version = %d, want 2", v2.Version)
	}
}

func TestMemoryStoreImportIsIdempotentOnHash(t *testing.T) {
	s := workflow.NewMemoryStore()
	raw := defYAML("wf", "Do the work.")

	v1, _ := s.Import("wf", raw, "alice")
	again, err := s.Import("wf", raw, "bob")
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if again.Version != v1.Version || again.CreatedBy != "alice" {
		t.Errorf("re-import must return the existing version untouched, got %+v", again)
	}
	versions, _ := s.Versions("wf")
	if len(versions) != 1 {
		t.Errorf("expected 1 stored version, got %d", len(versions))
	}
}

func TestMemoryStoreImportRejectsNameMismatchAndInvalid(t *testing.T) {
	s := workflow.NewMemoryStore()

	_, err := s.Import("other-name", defYAML("wf", "Do the work."), "alice")
	var inv workflow.InvalidDefinitionError
	if !errors.As(err, &inv) || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("name mismatch must be InvalidDefinitionError, got %v", err)
	}

	_, err = s.Import("wf", []byte("schema_version: 1\nname: wf\nsteps: []\n"), "alice")
	if !errors.As(err, &inv) {
		t.Errorf("validation failure must be InvalidDefinitionError, got %v", err)
	}
}

func TestMemoryStoreGetLiveAndPromote(t *testing.T) {
	s := workflow.NewMemoryStore()
	s.Import("wf", defYAML("wf", "One."), "alice")
	s.Import("wf", defYAML("wf", "Two."), "alice")

	if _, found, _ := s.Live("wf"); found {
		t.Error("nothing should be live before Promote")
	}

	if err := s.Promote("wf", 1, "alice"); err != nil {
		t.Fatalf("promote v1: %v", err)
	}
	live, found, _ := s.Live("wf")
	if !found || live.Version != 1 {
		t.Fatalf("live = %+v, found=%v", live, found)
	}
	if live.RawYAML != string(defYAML("wf", "One.")) {
		t.Error("Live must populate RawYAML")
	}

	// Promotion atomically swaps: v2 live, v1 not.
	if err := s.Promote("wf", 2, "bob"); err != nil {
		t.Fatalf("promote v2: %v", err)
	}
	live, _, _ = s.Live("wf")
	if live.Version != 2 {
		t.Errorf("live.Version = %d, want 2", live.Version)
	}
	v1, _, _ := s.Get("wf", 1)
	if v1.Live {
		t.Error("v1 must no longer be live")
	}

	if err := s.Promote("wf", 99, "alice"); !errors.Is(err, workflow.ErrVersionNotFound) {
		t.Errorf("unknown version must be ErrVersionNotFound, got %v", err)
	}

	if _, found, _ := s.Get("wf", 99); found {
		t.Error("Get unknown version must report found=false")
	}
}

func TestMemoryStoreListReturnsLatestPerName(t *testing.T) {
	s := workflow.NewMemoryStore()
	s.Import("aa", defYAML("aa", "A one."), "alice")
	s.Import("aa", defYAML("aa", "A two."), "alice")
	s.Import("bb", defYAML("bb", "B one."), "alice")

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	if list[0].Name != "aa" || list[0].Version != 2 {
		t.Errorf("list[0] = %+v", list[0])
	}
	if list[1].Name != "bb" || list[1].Version != 1 {
		t.Errorf("list[1] = %+v", list[1])
	}
}
```

- [ ] Run to verify failure: `go test ./agent/workflow/ -run TestMemoryStore` — expect compile errors (`undefined: workflow.NewMemoryStore`, `workflow.InvalidDefinitionError`, ...).
- [ ] Write `agent/workflow/definition.go` (contracts §2.2 + the Task 1 `RawYAML` amendment):

```go
package workflow

import "errors"

// Definition is the parsed, validated form of the YAML in
// agent_workflow_definitions.definition (contracts §6). ContentHash is
// hex(sha256(raw YAML bytes)) — identical fn to ci-agent/phaseconfig.Hash
// (see Hash in this package).
type Definition struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
	Live        bool   `json:"live"`
	Description string `json:"description"`
	CreatedBy   string `json:"created_by"`
	CreatedAt   int64  `json:"created_at"`

	Config Config `json:"config"` // parsed YAML, §6 grammar

	// RawYAML is the exact stored definition bytes (the hashed provenance
	// unit). Populated by Get and Live; empty in List/Versions.
	RawYAML string `json:"raw_yaml,omitempty"`
}

// ErrVersionNotFound is returned by Promote when (name, version) does
// not exist.
var ErrVersionNotFound = errors.New("workflow version not found")

// InvalidDefinitionError wraps parse/validation/name-mismatch failures
// so API handlers can map them to 400 responses.
type InvalidDefinitionError struct{ Err error }

func (e InvalidDefinitionError) Error() string { return e.Err.Error() }
func (e InvalidDefinitionError) Unwrap() error { return e.Err }

//counterfeiter:generate . Store
type Store interface {
	Import(name string, rawYAML []byte, createdBy string) (*Definition, error) // idempotent on hash
	Get(name string, version int) (*Definition, bool, error)
	Live(name string) (*Definition, bool, error)
	List() ([]Definition, error) // latest version per name + live marker
	Versions(name string) ([]Definition, error)
	Promote(name string, version int, promotedBy string) error // atomically swaps the live flag
}
```

- [ ] Write `agent/workflow/memory_store.go`:

```go
package workflow

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for testing (mirrors
// agent/api/reviews.MemoryStore).
type MemoryStore struct {
	mu     sync.Mutex
	nextID int
	defs   []*Definition
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (m *MemoryStore) Import(name string, rawYAML []byte, createdBy string) (*Definition, error) {
	cfg, err := Parse(rawYAML)
	if err != nil {
		return nil, InvalidDefinitionError{Err: err}
	}
	if cfg.Name != name {
		return nil, InvalidDefinitionError{Err: fmt.Errorf("definition name %q does not match import name %q", cfg.Name, name)}
	}
	hash := Hash(rawYAML)

	m.mu.Lock()
	defer m.mu.Unlock()

	maxVersion := 0
	for _, d := range m.defs {
		if d.Name != name {
			continue
		}
		if d.ContentHash == hash {
			cp := *d
			return &cp, nil // idempotent on hash
		}
		if d.Version > maxVersion {
			maxVersion = d.Version
		}
	}

	m.nextID++
	def := &Definition{
		ID:          m.nextID,
		Name:        name,
		Version:     maxVersion + 1,
		ContentHash: hash,
		Description: cfg.Description,
		CreatedBy:   createdBy,
		CreatedAt:   time.Now().Unix(),
		Config:      *cfg,
		RawYAML:     string(rawYAML),
	}
	m.defs = append(m.defs, def)
	cp := *def
	return &cp, nil
}

func (m *MemoryStore) Get(name string, version int) (*Definition, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.defs {
		if d.Name == name && d.Version == version {
			cp := *d
			return &cp, true, nil
		}
	}
	return nil, false, nil
}

func (m *MemoryStore) Live(name string) (*Definition, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.defs {
		if d.Name == name && d.Live {
			cp := *d
			return &cp, true, nil
		}
	}
	return nil, false, nil
}

func (m *MemoryStore) List() ([]Definition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	latest := map[string]*Definition{}
	for _, d := range m.defs {
		if cur, ok := latest[d.Name]; !ok || d.Version > cur.Version {
			latest[d.Name] = d
		}
	}
	out := []Definition{}
	for _, d := range latest {
		cp := *d
		cp.RawYAML = "" // metadata-only listing
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemoryStore) Versions(name string) ([]Definition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Definition{}
	for _, d := range m.defs {
		if d.Name == name {
			cp := *d
			cp.RawYAML = ""
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func (m *MemoryStore) Promote(name string, version int, promotedBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var target *Definition
	for _, d := range m.defs {
		if d.Name == name && d.Version == version {
			target = d
			break
		}
	}
	if target == nil {
		return ErrVersionNotFound
	}
	for _, d := range m.defs {
		if d.Name == name {
			d.Live = false
		}
	}
	target.Live = true
	_ = promotedBy // persisted by the DB store; the memory store only flips flags
	return nil
}
```

- [ ] Run to verify pass: `go test ./agent/workflow/` — expect `ok`.
- [ ] Commit: `git add agent/workflow && git commit -m "feat(workflow-store): Definition/Store contract types and MemoryStore"`

---

### Task 6: `atc/db` factory — Import, Get, Live

**Files:**

- Create: `atc/db/agent_workflows_factory.go`
- Test: `atc/db/agent_workflows_factory_test.go` (Ginkgo, per `agent_reviews_factory_test.go`; `dbConn` comes from the suite)

**Steps:**

- [ ] Write the failing Ginkgo test `atc/db/agent_workflows_factory_test.go` (unique workflow names per spec keep specs order-independent):

```go
package db_test

import (
	"errors"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentWorkflowsFactory", func() {
	var factory db.AgentWorkflowsFactory

	BeforeEach(func() {
		factory = db.NewAgentWorkflowsFactory(dbConn)
	})

	defYAML := func(name, promptBody string) []byte {
		return []byte(`schema_version: 1
name: ` + name + `
description: test definition
prompts:
  work: |
    ` + promptBody + `
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`)
	}

	It("imports with monotonic versions and scans all fields", func() {
		v1, err := factory.Import("wf-import", defYAML("wf-import", "One."), "alice")
		Expect(err).ToNot(HaveOccurred())
		Expect(v1.Version).To(Equal(1))
		Expect(v1.ID).To(BeNumerically(">", 0))
		Expect(v1.Name).To(Equal("wf-import"))
		Expect(v1.ContentHash).To(Equal(workflow.Hash(defYAML("wf-import", "One."))))
		Expect(v1.Description).To(Equal("test definition"))
		Expect(v1.CreatedBy).To(Equal("alice"))
		Expect(v1.CreatedAt).To(BeNumerically(">", 0))
		Expect(v1.Live).To(BeFalse())
		Expect(v1.RawYAML).To(Equal(string(defYAML("wf-import", "One."))))
		Expect(v1.Config.Steps).To(HaveLen(1))

		v2, err := factory.Import("wf-import", defYAML("wf-import", "Two."), "bob")
		Expect(err).ToNot(HaveOccurred())
		Expect(v2.Version).To(Equal(2))
	})

	It("is idempotent on content hash", func() {
		raw := defYAML("wf-idem", "Same bytes.")
		v1, err := factory.Import("wf-idem", raw, "alice")
		Expect(err).ToNot(HaveOccurred())

		again, err := factory.Import("wf-idem", raw, "bob")
		Expect(err).ToNot(HaveOccurred())
		Expect(again.Version).To(Equal(v1.Version))
		Expect(again.CreatedBy).To(Equal("alice")) // existing row untouched
	})

	It("rejects name mismatch and invalid definitions as InvalidDefinitionError", func() {
		_, err := factory.Import("wrong-name", defYAML("wf-mismatch", "One."), "alice")
		var inv workflow.InvalidDefinitionError
		Expect(errors.As(err, &inv)).To(BeTrue())

		_, err = factory.Import("wf-bad", []byte("schema_version: 1\nname: wf-bad\nsteps: []\n"), "alice")
		Expect(errors.As(err, &inv)).To(BeTrue())
	})

	It("gets by version and reports found=false for unknowns", func() {
		_, err := factory.Import("wf-get", defYAML("wf-get", "One."), "alice")
		Expect(err).ToNot(HaveOccurred())

		def, found, err := factory.Get("wf-get", 1)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(def.RawYAML).ToNot(BeEmpty())
		Expect(def.Config.Name).To(Equal("wf-get"))

		_, found, err = factory.Get("wf-get", 42)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())

		_, found, err = factory.Live("wf-get")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})
})
```

- [ ] Run to verify failure: `ginkgo --focus="AgentWorkflowsFactory" ./atc/db/` — expect compile error `undefined: db.AgentWorkflowsFactory`.
- [ ] Write `atc/db/agent_workflows_factory.go` (Import/Get/Live now; List/Versions/Promote stubs return errors so the interface compiles — implemented in Task 7):

```go
package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/workflow"
)

//counterfeiter:generate . AgentWorkflowsFactory
type AgentWorkflowsFactory interface {
	workflow.Store
}

func NewAgentWorkflowsFactory(conn DbConn) AgentWorkflowsFactory {
	return &agentWorkflowsFactory{conn: conn}
}

type agentWorkflowsFactory struct {
	conn DbConn
}

const workflowMetaColumns = `id, name, version, content_hash, live, description, created_by,
	EXTRACT(EPOCH FROM created_at)::bigint`

func (f *agentWorkflowsFactory) Import(name string, rawYAML []byte, createdBy string) (*workflow.Definition, error) {
	cfg, err := workflow.Parse(rawYAML)
	if err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
	}
	if cfg.Name != name {
		return nil, workflow.InvalidDefinitionError{Err: fmt.Errorf("definition name %q does not match import name %q", cfg.Name, name)}
	}
	hash := workflow.Hash(rawYAML)

	tx, err := f.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)

	// Serialize imports per name so version assignment is race-free
	// under concurrent web nodes.
	_, err = tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('agent_workflow_definitions:' || $1))`, name)
	if err != nil {
		return nil, err
	}

	var def workflow.Definition
	err = tx.QueryRow(`
		SELECT `+workflowMetaColumns+`, definition
		FROM agent_workflow_definitions
		WHERE name = $1 AND content_hash = $2`,
		name, hash,
	).Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
		&def.Description, &def.CreatedBy, &def.CreatedAt, &def.RawYAML)
	if err == nil {
		// Idempotent on hash: byte-identical YAML returns the existing
		// version untouched (contracts §1.6).
		def.Config = *cfg
		return &def, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	err = tx.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(name, version, content_hash, definition, description, created_by)
		SELECT $1, COALESCE(MAX(version), 0) + 1, $2, $3, $4, $5
		FROM agent_workflow_definitions WHERE name = $1
		RETURNING id, version, EXTRACT(EPOCH FROM created_at)::bigint`,
		name, hash, string(rawYAML), cfg.Description, createdBy,
	).Scan(&def.ID, &def.Version, &def.CreatedAt)
	if err != nil {
		return nil, err
	}

	def.Name = name
	def.ContentHash = hash
	def.Description = cfg.Description
	def.CreatedBy = createdBy
	def.RawYAML = string(rawYAML)
	def.Config = *cfg
	return &def, tx.Commit()
}

func (f *agentWorkflowsFactory) Get(name string, version int) (*workflow.Definition, bool, error) {
	return f.getOne(`name = $1 AND version = $2`, name, version)
}

func (f *agentWorkflowsFactory) Live(name string) (*workflow.Definition, bool, error) {
	return f.getOne(`name = $1 AND live`, name)
}

func (f *agentWorkflowsFactory) getOne(where string, args ...any) (*workflow.Definition, bool, error) {
	var def workflow.Definition
	err := f.conn.QueryRow(`
		SELECT `+workflowMetaColumns+`, definition
		FROM agent_workflow_definitions
		WHERE `+where, args...,
	).Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
		&def.Description, &def.CreatedBy, &def.CreatedAt, &def.RawYAML)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	cfg, err := workflow.Parse([]byte(def.RawYAML))
	if err != nil {
		// Rows are validated at import; a parse failure here means the
		// stored bytes were corrupted out-of-band.
		return nil, false, fmt.Errorf("stored definition %s/v%d no longer parses: %w", def.Name, def.Version, err)
	}
	def.Config = *cfg
	return &def, true, nil
}

func (f *agentWorkflowsFactory) List() ([]workflow.Definition, error) {
	return nil, errors.New("not implemented") // Task 7
}

func (f *agentWorkflowsFactory) Versions(name string) ([]workflow.Definition, error) {
	return nil, errors.New("not implemented") // Task 7
}

func (f *agentWorkflowsFactory) Promote(name string, version int, promotedBy string) error {
	return errors.New("not implemented") // Task 7
}
```

- [ ] Run to verify pass: `ginkgo --focus="AgentWorkflowsFactory" ./atc/db/` — expect all four specs green. (If you see `database "testdb_template" already exists`, another test process is running — wait for it.)
- [ ] Commit: `git add atc/db/agent_workflows_factory.go atc/db/agent_workflows_factory_test.go && git commit -m "feat(workflow-store): agent_workflows_factory Import/Get/Live"`

---

### Task 7: `atc/db` factory — List, Versions, Promote + counterfeiter fake

**Files:**

- Modify: `atc/db/agent_workflows_factory.go` (replace the three Task 6 stubs)
- Create: `atc/db/dbfakes/fake_agent_workflows_factory.go` (generated)
- Test: `atc/db/agent_workflows_factory_test.go` (extend)

**Steps:**

- [ ] Append failing specs inside the `Describe("AgentWorkflowsFactory", ...)` block:

```go
	It("lists the latest version per name", func() {
		_, err := factory.Import("wf-list-a", defYAML("wf-list-a", "A one."), "alice")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.Import("wf-list-a", defYAML("wf-list-a", "A two."), "alice")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.Import("wf-list-b", defYAML("wf-list-b", "B one."), "alice")
		Expect(err).ToNot(HaveOccurred())

		list, err := factory.List()
		Expect(err).ToNot(HaveOccurred())

		byName := map[string]workflow.Definition{}
		for _, d := range list {
			byName[d.Name] = d
			Expect(d.RawYAML).To(BeEmpty()) // metadata-only listing
		}
		Expect(byName["wf-list-a"].Version).To(Equal(2))
		Expect(byName["wf-list-b"].Version).To(Equal(1))
	})

	It("returns all versions ascending", func() {
		_, err := factory.Import("wf-vers", defYAML("wf-vers", "One."), "alice")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.Import("wf-vers", defYAML("wf-vers", "Two."), "alice")
		Expect(err).ToNot(HaveOccurred())

		versions, err := factory.Versions("wf-vers")
		Expect(err).ToNot(HaveOccurred())
		Expect(versions).To(HaveLen(2))
		Expect(versions[0].Version).To(Equal(1))
		Expect(versions[1].Version).To(Equal(2))

		Expect(factory.Versions("wf-nonexistent")).To(BeEmpty())
	})

	It("promotes atomically, swapping the live flag", func() {
		_, err := factory.Import("wf-promote", defYAML("wf-promote", "One."), "alice")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.Import("wf-promote", defYAML("wf-promote", "Two."), "alice")
		Expect(err).ToNot(HaveOccurred())

		Expect(factory.Promote("wf-promote", 1, "alice")).To(Succeed())
		live, found, err := factory.Live("wf-promote")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(live.Version).To(Equal(1))

		Expect(factory.Promote("wf-promote", 2, "bob")).To(Succeed())
		live, _, err = factory.Live("wf-promote")
		Expect(err).ToNot(HaveOccurred())
		Expect(live.Version).To(Equal(2))

		v1, _, err := factory.Get("wf-promote", 1)
		Expect(err).ToNot(HaveOccurred())
		Expect(v1.Live).To(BeFalse())

		Expect(factory.Promote("wf-promote", 99, "alice")).To(MatchError(workflow.ErrVersionNotFound))
		Expect(factory.Promote("wf-nonexistent", 1, "alice")).To(MatchError(workflow.ErrVersionNotFound))
	})
```

- [ ] Run to verify failure: `ginkgo --focus="AgentWorkflowsFactory" ./atc/db/` — the three new specs fail with `not implemented`.
- [ ] Replace the three stubs in `atc/db/agent_workflows_factory.go`:

```go
func (f *agentWorkflowsFactory) List() ([]workflow.Definition, error) {
	rows, err := f.conn.Query(`
		SELECT DISTINCT ON (name) ` + workflowMetaColumns + `
		FROM agent_workflow_definitions
		ORDER BY name, version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWorkflowMetaRows(rows)
}

func (f *agentWorkflowsFactory) Versions(name string) ([]workflow.Definition, error) {
	rows, err := f.conn.Query(`
		SELECT `+workflowMetaColumns+`
		FROM agent_workflow_definitions
		WHERE name = $1
		ORDER BY version ASC`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWorkflowMetaRows(rows)
}

func (f *agentWorkflowsFactory) Promote(name string, version int, promotedBy string) error {
	tx, err := f.conn.Begin()
	if err != nil {
		return err
	}
	defer Rollback(tx)

	// Clear-then-set inside one tx: the partial unique index
	// agent_workflow_definitions_live enforces at most one live row per
	// name at every intermediate statement.
	_, err = tx.Exec(`UPDATE agent_workflow_definitions SET live = false WHERE name = $1 AND live`, name)
	if err != nil {
		return err
	}

	res, err := tx.Exec(`
		UPDATE agent_workflow_definitions
		SET live = true, promoted_at = now(), promoted_by = $3
		WHERE name = $1 AND version = $2`,
		name, version, promotedBy)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return workflow.ErrVersionNotFound
	}
	return tx.Commit()
}

func scanWorkflowMetaRows(rows *sql.Rows) ([]workflow.Definition, error) {
	out := []workflow.Definition{}
	for rows.Next() {
		var def workflow.Definition
		if err := rows.Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
			&def.Description, &def.CreatedBy, &def.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, def)
	}
	return out, rows.Err()
}
```

- [ ] Run to verify pass: `ginkgo --focus="AgentWorkflowsFactory" ./atc/db/` — all specs green.
- [ ] Generate the counterfeiter fake (single-interface generation; do NOT run package-wide `go generate`, which regenerates every fake): `cd atc/db && go run github.com/maxbrunsfeld/counterfeiter/v6 -o dbfakes/fake_agent_workflows_factory.go . AgentWorkflowsFactory && cd ../..`
- [ ] Verify the fake compiles: `go build ./atc/db/...` — expect no output.
- [ ] Run the full db suite once to catch interference: `ginkgo ./atc/db/` (~90s).
- [ ] Commit: `git add atc/db && git commit -m "feat(workflow-store): factory List/Versions/Promote + counterfeiter fake"`

---

### Task 8: `agent/api/workflows` HTTP handler

**Files:**

- Create: `agent/api/workflows/handler.go`
- Test: `agent/api/workflows/handler_test.go` (the route-registration guard is written in Task 9 — it references `atc.*` route constants that do not exist until then, and would break this package's compilation if written now)

**Steps:**

- [ ] Write the failing test `agent/api/workflows/handler_test.go` (plain `testing`, per `agent/api/reviews/handler_test.go`; rata exposes path params as `:name` form values, so tests put them in the query string):

```go
package workflows_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/workflows"
	"github.com/concourse/concourse/agent/workflow"
)

const validYAML = `schema_version: 1
name: wf
description: handler test workflow
prompts:
  work: |
    Do the work.
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`

func newHandler(t *testing.T) (*workflows.Handler, *workflow.MemoryStore) {
	t.Helper()
	store := workflow.NewMemoryStore()
	return workflows.NewHandler(store), store
}

func request(method, path string, params url.Values, body string) *http.Request {
	u := path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	if body != "" {
		return httptest.NewRequest(method, u, strings.NewReader(body))
	}
	return httptest.NewRequest(method, u, nil)
}

func TestImportCreatesAndIsIdempotent(t *testing.T) {
	h, _ := newHandler(t)

	w := httptest.NewRecorder()
	h.Import(w, request("POST", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, validYAML))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var def workflow.Definition
	if err := json.Unmarshal(w.Body.Bytes(), &def); err != nil {
		t.Fatal(err)
	}
	if def.Version != 1 || def.Name != "wf" || def.ContentHash == "" {
		t.Errorf("def = %+v", def)
	}

	w = httptest.NewRecorder()
	h.Import(w, request("POST", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, validYAML))
	if w.Code != http.StatusOK {
		t.Fatalf("idempotent re-import status = %d", w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &def)
	if def.Version != 1 {
		t.Errorf("re-import version = %d, want 1", def.Version)
	}
}

func TestImportRejectsBadDefinitions(t *testing.T) {
	h, _ := newHandler(t)

	w := httptest.NewRecorder()
	h.Import(w, request("POST", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, "schema_version: 1\nname: wf\nsteps: []\n"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid definition status = %d, want 400", w.Code)
	}

	w = httptest.NewRecorder()
	h.Import(w, request("POST", "/api/v1/agent/workflows/other/versions",
		url.Values{":workflow_name": {"other"}}, validYAML))
	if w.Code != http.StatusBadRequest {
		t.Errorf("name mismatch status = %d, want 400", w.Code)
	}

	w = httptest.NewRecorder()
	h.Import(w, request("POST", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty body status = %d, want 400", w.Code)
	}
}

func TestListShowsLiveVersion(t *testing.T) {
	h, store := newHandler(t)
	store.Import("wf", []byte(validYAML), "alice")
	store.Import("wf", []byte(strings.Replace(validYAML, "Do the work.", "Do it better.", 1)), "alice")
	store.Promote("wf", 1, "alice")

	w := httptest.NewRecorder()
	h.List(w, request("GET", "/api/v1/agent/workflows", nil, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got []workflows.WorkflowSummary
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].LatestVersion != 2 || got[0].LiveVersion != 1 {
		t.Errorf("summaries = %+v", got)
	}
}

func TestVersionsAndGet(t *testing.T) {
	h, store := newHandler(t)
	store.Import("wf", []byte(validYAML), "alice")

	w := httptest.NewRecorder()
	h.Versions(w, request("GET", "/api/v1/agent/workflows/wf/versions",
		url.Values{":workflow_name": {"wf"}}, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("versions status = %d", w.Code)
	}

	w = httptest.NewRecorder()
	h.Versions(w, request("GET", "/api/v1/agent/workflows/nope/versions",
		url.Values{":workflow_name": {"nope"}}, ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown name status = %d, want 404", w.Code)
	}

	w = httptest.NewRecorder()
	h.Get(w, request("GET", "/api/v1/agent/workflows/wf/versions/1",
		url.Values{":workflow_name": {"wf"}, ":version": {"1"}}, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d", w.Code)
	}
	var def workflow.Definition
	json.Unmarshal(w.Body.Bytes(), &def)
	if def.RawYAML != validYAML {
		t.Errorf("get must include raw_yaml, got %q", def.RawYAML)
	}

	w = httptest.NewRecorder()
	h.Get(w, request("GET", "/api/v1/agent/workflows/wf/versions/9",
		url.Values{":workflow_name": {"wf"}, ":version": {"9"}}, ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown version status = %d, want 404", w.Code)
	}

	w = httptest.NewRecorder()
	h.Get(w, request("GET", "/api/v1/agent/workflows/wf/versions/x",
		url.Values{":workflow_name": {"wf"}, ":version": {"x"}}, ""))
	if w.Code != http.StatusBadRequest {
		t.Errorf("non-integer version status = %d, want 400", w.Code)
	}
}

func TestPromote(t *testing.T) {
	h, store := newHandler(t)
	store.Import("wf", []byte(validYAML), "alice")

	w := httptest.NewRecorder()
	h.Promote(w, request("PUT", "/api/v1/agent/workflows/wf/versions/1/live",
		url.Values{":workflow_name": {"wf"}, ":version": {"1"}}, ""))
	if w.Code != http.StatusNoContent {
		t.Fatalf("promote status = %d body=%s", w.Code, w.Body.String())
	}
	live, found, _ := store.Live("wf")
	if !found || live.Version != 1 {
		t.Errorf("live = %+v found=%v", live, found)
	}

	w = httptest.NewRecorder()
	h.Promote(w, request("PUT", "/api/v1/agent/workflows/wf/versions/9/live",
		url.Values{":workflow_name": {"wf"}, ":version": {"9"}}, ""))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown version status = %d, want 404", w.Code)
	}
}
```

- [ ] Run to verify failure: `go test ./agent/api/workflows/` — expect compile error `undefined: workflows.NewHandler`.
- [ ] Write `agent/api/workflows/handler.go`:

```go
package workflows

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/api/accessor"
)

const maxDefinitionBytes = 1 << 20 // 1 MiB

// Handler serves the agent workflow-definition API (contracts §4.2:
// ListAgentWorkflows, ListAgentWorkflowVersions, GetAgentWorkflowVersion,
// CreateAgentWorkflowVersion, PromoteAgentWorkflowVersion).
type Handler struct {
	store workflow.Store
}

func NewHandler(store workflow.Store) *Handler {
	return &Handler{store: store}
}

// WorkflowSummary is the GET /api/v1/agent/workflows element.
type WorkflowSummary struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	LatestVersion int    `json:"latest_version"`
	ContentHash   string `json:"content_hash"`
	LiveVersion   int    `json:"live_version"` // 0 = no live version
	CreatedAt     int64  `json:"created_at"`
}

// requestUser mirrors accessor's userName(): preferred_username, falling
// back to name. Safe on requests without an accessor in context —
// accessor.GetAccessor returns an empty access whose Claims() are zero.
func requestUser(r *http.Request) string {
	claims := accessor.GetAccessor(r).Claims()
	if claims.PreferredUsername != "" {
		return claims.PreferredUsername
	}
	return claims.UserName
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// List handles GET /api/v1/agent/workflows.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	defs, err := h.store.List()
	if err != nil {
		http.Error(w, "failed to list workflows", http.StatusInternalServerError)
		return
	}
	summaries := []WorkflowSummary{}
	for _, d := range defs {
		s := WorkflowSummary{
			Name:          d.Name,
			Description:   d.Description,
			LatestVersion: d.Version,
			ContentHash:   d.ContentHash,
			CreatedAt:     d.CreatedAt,
		}
		if d.Live {
			s.LiveVersion = d.Version
		} else {
			live, found, err := h.store.Live(d.Name)
			if err != nil {
				http.Error(w, "failed to resolve live version", http.StatusInternalServerError)
				return
			}
			if found {
				s.LiveVersion = live.Version
			}
		}
		summaries = append(summaries, s)
	}
	writeJSON(w, http.StatusOK, summaries)
}

// Versions handles GET /api/v1/agent/workflows/:workflow_name/versions.
func (h *Handler) Versions(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")
	defs, err := h.store.Versions(name)
	if err != nil {
		http.Error(w, "failed to list versions", http.StatusInternalServerError)
		return
	}
	if len(defs) == 0 {
		http.Error(w, "unknown workflow", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, defs)
}

// Get handles GET /api/v1/agent/workflows/:workflow_name/versions/:version.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")
	version, err := strconv.Atoi(r.FormValue(":version"))
	if err != nil {
		http.Error(w, "version must be an integer", http.StatusBadRequest)
		return
	}
	def, found, err := h.store.Get(name, version)
	if err != nil {
		http.Error(w, "failed to get workflow version", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "unknown workflow version", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, def)
}

// Import handles POST /api/v1/agent/workflows/:workflow_name/versions.
// The request body is the raw definition YAML; the response is the
// stored Definition (200 both for a new version and an idempotent
// content-hash hit).
func (h *Handler) Import(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxDefinitionBytes+1))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	if len(raw) == 0 {
		http.Error(w, "request body must be the definition YAML", http.StatusBadRequest)
		return
	}
	if len(raw) > maxDefinitionBytes {
		http.Error(w, "definition exceeds 1 MiB", http.StatusRequestEntityTooLarge)
		return
	}

	def, err := h.store.Import(name, raw, requestUser(r))
	if err != nil {
		var inv workflow.InvalidDefinitionError
		if errors.As(err, &inv) {
			http.Error(w, inv.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "failed to import workflow", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, def)
}

// Promote handles PUT /api/v1/agent/workflows/:workflow_name/versions/:version/live.
func (h *Handler) Promote(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")
	version, err := strconv.Atoi(r.FormValue(":version"))
	if err != nil {
		http.Error(w, "version must be an integer", http.StatusBadRequest)
		return
	}
	err = h.store.Promote(name, version, requestUser(r))
	if errors.Is(err, workflow.ErrVersionNotFound) {
		http.Error(w, "unknown workflow version", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to promote workflow version", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] Run to verify pass: `go test ./agent/api/workflows/` — expect `ok`.
- [ ] Commit: `git add agent/api/workflows && git commit -m "feat(workflow-store): agent workflows HTTP handler"`

---

### Task 9: ATC wiring — routes, wrappa, roles, handler map, atccmd

**Files:**

- Modify: `atc/routes.go:129` (constants) and `atc/routes.go:262` (route entries)
- Modify: `atc/wrappa/api_auth_wrappa.go:174` (authorized case group)
- Modify: `atc/api/accessor/roles.go:114` (DefaultRoles entries)
- Modify: `atc/api/handler.go:91` (param), `atc/api/handler.go:139` (server construction), `atc/api/handler.go:277` (handlers map)
- Modify: `atc/api/api_suite_test.go:226` (NewHandler call)
- Modify: `atc/atccmd/command.go:2298` (factory arg)
- Test: `agent/api/workflows/route_registration_test.go` (created here), `ginkgo ./atc/wrappa/`, `ginkgo ./atc/api/`

**Steps:**

- [ ] Write the failing guard `agent/api/workflows/route_registration_test.go` (mirrors `agent/api/feedback/route_registration_test.go`; guards against handlers existing without `atc.Routes` entries — unreachable in production):

```go
package workflows_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

// TestWorkflowRoutesRegistered guards against handlers existing without
// atc.Routes entries (unreachable in production).
func TestWorkflowRoutesRegistered(t *testing.T) {
	required := []struct {
		name   string
		method string
		path   string
	}{
		{atc.ListAgentWorkflows, "GET", "/api/v1/agent/workflows"},
		{atc.ListAgentWorkflowVersions, "GET", "/api/v1/agent/workflows/:workflow_name/versions"},
		{atc.GetAgentWorkflowVersion, "GET", "/api/v1/agent/workflows/:workflow_name/versions/:version"},
		{atc.CreateAgentWorkflowVersion, "POST", "/api/v1/agent/workflows/:workflow_name/versions"},
		{atc.PromoteAgentWorkflowVersion, "PUT", "/api/v1/agent/workflows/:workflow_name/versions/:version/live"},
	}
	for _, rr := range required {
		found := false
		for _, route := range atc.Routes {
			if route.Name == rr.name {
				found = true
				if route.Method != rr.method {
					t.Errorf("route %q: method %s, want %s", rr.name, route.Method, rr.method)
				}
				if route.Path != rr.path {
					t.Errorf("route %q: path %s, want %s", rr.name, route.Path, rr.path)
				}
			}
		}
		if !found {
			t.Errorf("route %q not registered in atc.Routes", rr.name)
		}
	}
}
```

- [ ] Run to verify failure: `go test ./agent/api/workflows/` — expect compile error `undefined: atc.ListAgentWorkflows`.
- [ ] In `atc/routes.go`, after line 129 (`ListTeamAgentReviews = "ListTeamAgentReviews"`), add:

```go
	ListAgentWorkflows          = "ListAgentWorkflows"
	ListAgentWorkflowVersions   = "ListAgentWorkflowVersions"
	GetAgentWorkflowVersion     = "GetAgentWorkflowVersion"
	CreateAgentWorkflowVersion  = "CreateAgentWorkflowVersion"
	PromoteAgentWorkflowVersion = "PromoteAgentWorkflowVersion"
```

- [ ] In `atc/routes.go`, after line 262 (`{Path: "/api/v1/teams/:team_name/agent-reviews", ...}`), add (exact §4.2 paths):

```go
	{Path: "/api/v1/agent/workflows", Method: "GET", Name: ListAgentWorkflows},
	{Path: "/api/v1/agent/workflows/:workflow_name/versions", Method: "GET", Name: ListAgentWorkflowVersions},
	{Path: "/api/v1/agent/workflows/:workflow_name/versions/:version", Method: "GET", Name: GetAgentWorkflowVersion},
	{Path: "/api/v1/agent/workflows/:workflow_name/versions", Method: "POST", Name: CreateAgentWorkflowVersion},
	{Path: "/api/v1/agent/workflows/:workflow_name/versions/:version/live", Method: "PUT", Name: PromoteAgentWorkflowVersion},
```

- [ ] In `atc/wrappa/api_auth_wrappa.go`, in the `authorized` case group, after line 174 (`atc.ListTeamAgentReviews,`), add (see Context: agent-identity later moves these onto `CheckAgentAuthorizationHandler`; until then they are admin-only in effect per contracts decision 21):

```go
			atc.ListAgentWorkflows,
			atc.ListAgentWorkflowVersions,
			atc.GetAgentWorkflowVersion,
			atc.CreateAgentWorkflowVersion,
			atc.PromoteAgentWorkflowVersion,
```

- [ ] In `atc/api/accessor/roles.go`, after line 114 (`atc.GetBuildAgentReviews:    ViewerRole,`), add (tiers per contracts §4.2: viewer reads, member import/promote):

```go
	atc.ListAgentWorkflows:          ViewerRole,
	atc.ListAgentWorkflowVersions:   ViewerRole,
	atc.GetAgentWorkflowVersion:     ViewerRole,
	atc.CreateAgentWorkflowVersion:  MemberRole,
	atc.PromoteAgentWorkflowVersion: MemberRole,
```

- [ ] In `atc/api/handler.go`: add imports `workflowsapi "github.com/concourse/concourse/agent/api/workflows"` and `"github.com/concourse/concourse/agent/workflow"`; add a parameter after line 91 (`reviewsStore reviewsapi.Store,`):

```go
	workflowStore workflow.Store,
```

  after the `reviewsServer := ...` construction (ends at line 139), add:

```go
	workflowsServer := workflowsapi.NewHandler(workflowStore)
```

  and in the handlers map after line 277 (`atc.ListTeamAgentReviews: ...`), add:

```go
		atc.ListAgentWorkflows:          http.HandlerFunc(workflowsServer.List),
		atc.ListAgentWorkflowVersions:   http.HandlerFunc(workflowsServer.Versions),
		atc.GetAgentWorkflowVersion:     http.HandlerFunc(workflowsServer.Get),
		atc.CreateAgentWorkflowVersion:  http.HandlerFunc(workflowsServer.Import),
		atc.PromoteAgentWorkflowVersion: http.HandlerFunc(workflowsServer.Promote),
```

- [ ] In `atc/api/api_suite_test.go`, after line 226 (`reviews.NewMemoryStore(),`), add (plus import `"github.com/concourse/concourse/agent/workflow"`):

```go
		workflow.NewMemoryStore(),
```

- [ ] In `atc/atccmd/command.go`, after line 2298 (`db.NewAgentReviewsFactory(dbConn),`), add:

```go
		db.NewAgentWorkflowsFactory(dbConn),
```

- [ ] Run to verify pass, in order:
  - `go test ./agent/api/workflows/` — registration guard now green.
  - `ginkgo ./atc/wrappa/` — the "handles each route" exhaustive-switch spec must not panic.
  - `ginkgo ./atc/api/` — suite compiles and passes with the new param.
  - `go build ./atc/...` — atccmd wiring compiles.
- [ ] Commit: `git add agent/api/workflows/route_registration_test.go atc/routes.go atc/wrappa/api_auth_wrappa.go atc/api/accessor/roles.go atc/api/handler.go atc/api/api_suite_test.go atc/atccmd/command.go && git commit -m "feat(workflow-store): wire agent workflow routes through ATC"`

---

### Task 10: fly commands — `fly agent workflows list/show/import/set-live`

**Files:**

- Create: `fly/commands/agent.go`
- Create: `fly/commands/agent_workflows.go`
- Modify: `fly/commands/fly.go:96` (register the `agent` command group)

MERGE NOTE: credentials-and-budgets (wave-mate) adds `fly agent auth`. If `fly/commands/agent.go` already exists on the branch when this task runs, add only the `Workflows` field to the existing `AgentCommand` struct instead of creating the file.

**Steps:**

- [ ] Write `fly/commands/agent.go` (go-flags v1.6.1 supports nested subcommands via `command:` struct tags — verified in `scanSubcommandHandler`, `command.go:234` of the vendored module):

```go
package commands

// AgentCommand groups agentic-platform subcommands (`fly agent ...`).
type AgentCommand struct {
	Workflows AgentWorkflowsCommand `command:"workflows" description:"Manage versioned agent workflow definitions"`
}
```

- [ ] In `fly/commands/fly.go`, after line 96 (`Completion CompletionCommand ...`), add:

```go
	Agent AgentCommand `command:"agent" description:"Agentic platform commands"`
```

- [ ] Write `fly/commands/agent_workflows.go`:

```go
package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

// AgentWorkflowsCommand groups the workflow-definition subcommands.
type AgentWorkflowsCommand struct {
	List    WorkflowsListCommand    `command:"list" description:"List workflow definitions (latest and live versions)"`
	Show    WorkflowsShowCommand    `command:"show" description:"Print a workflow definition version"`
	Import  WorkflowsImportCommand  `command:"import" description:"Import a workflow definition YAML file as a new version"`
	SetLive WorkflowsSetLiveCommand `command:"set-live" description:"Mark a workflow definition version live (human promotion)"`
}

// workflowSummary mirrors agent/api/workflows.WorkflowSummary.
type workflowSummary struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	LatestVersion int    `json:"latest_version"`
	ContentHash   string `json:"content_hash"`
	LiveVersion   int    `json:"live_version"`
	CreatedAt     int64  `json:"created_at"`
}

func agentAPIRequest(target rc.Target, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, target.URL()+path, body)
	if err != nil {
		return nil, err
	}
	// target.Client().HTTPClient() carries the target's auth transport.
	return target.Client().HTTPClient().Do(req)
}

func decodeOrError(resp *http.Response, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func loadAgentTarget() (rc.Target, error) {
	target, err := rc.LoadTarget(Fly.Target, Fly.Verbose)
	if err != nil {
		return nil, err
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	return target, nil
}

type WorkflowsListCommand struct {
	Json bool `long:"json" description:"Print command result as JSON"`
}

func (command *WorkflowsListCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	resp, err := agentAPIRequest(target, "GET", "/api/v1/agent/workflows", nil)
	if err != nil {
		return err
	}
	var summaries []workflowSummary
	if err := decodeOrError(resp, &summaries); err != nil {
		return err
	}

	if command.Json {
		return displayhelpers.JsonPrint(summaries)
	}

	table := ui.Table{Headers: ui.TableRow{
		{Contents: "name", Color: color.New(color.Bold)},
		{Contents: "latest", Color: color.New(color.Bold)},
		{Contents: "live", Color: color.New(color.Bold)},
		{Contents: "description", Color: color.New(color.Bold)},
	}}
	for _, s := range summaries {
		live := "none"
		if s.LiveVersion > 0 {
			live = strconv.Itoa(s.LiveVersion)
		}
		table.Data = append(table.Data, ui.TableRow{
			{Contents: s.Name},
			{Contents: strconv.Itoa(s.LatestVersion)},
			{Contents: live},
			{Contents: s.Description},
		})
	}
	sort.Sort(table.Data)
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

type WorkflowsShowCommand struct {
	Args struct {
		Name    string `positional-arg-name:"NAME" required:"true" description:"Workflow definition name"`
		Version int    `positional-arg-name:"VERSION" description:"Version number (default: live version, else latest)"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print the full definition record as JSON"`
}

func (command *WorkflowsShowCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	name := url.PathEscape(command.Args.Name)
	version := command.Args.Version
	if version == 0 {
		resp, err := agentAPIRequest(target, "GET", "/api/v1/agent/workflows/"+name+"/versions", nil)
		if err != nil {
			return err
		}
		var versions []workflow.Definition
		if err := decodeOrError(resp, &versions); err != nil {
			return err
		}
		for _, v := range versions {
			if v.Version > version { // latest…
				version = v.Version
			}
		}
		for _, v := range versions {
			if v.Live { // …unless one is live
				version = v.Version
			}
		}
	}

	resp, err := agentAPIRequest(target, "GET",
		"/api/v1/agent/workflows/"+name+"/versions/"+strconv.Itoa(version), nil)
	if err != nil {
		return err
	}
	var def workflow.Definition
	if err := decodeOrError(resp, &def); err != nil {
		return err
	}

	if command.Json {
		return displayhelpers.JsonPrint(def)
	}
	fmt.Fprintf(os.Stderr, "# %s version %d  hash %s  live=%v\n", def.Name, def.Version, def.ContentHash, def.Live)
	fmt.Print(def.RawYAML)
	return nil
}

type WorkflowsImportCommand struct {
	Args struct {
		File string `positional-arg-name:"FILE" required:"true" description:"Path to the workflow definition YAML"`
	} `positional-args:"yes"`
}

func (command *WorkflowsImportCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	raw, err := os.ReadFile(command.Args.File)
	if err != nil {
		return err
	}
	// Parse client-side first: same validation the server runs, but the
	// error message points at the local file.
	cfg, err := workflow.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", command.Args.File, err)
	}

	resp, err := agentAPIRequest(target, "POST",
		"/api/v1/agent/workflows/"+url.PathEscape(cfg.Name)+"/versions", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	var def workflow.Definition
	if err := decodeOrError(resp, &def); err != nil {
		return err
	}

	fmt.Printf("imported %s version %d (hash %.12s)\n", def.Name, def.Version, def.ContentHash)
	return nil
}

type WorkflowsSetLiveCommand struct {
	Args struct {
		Name    string `positional-arg-name:"NAME" required:"true" description:"Workflow definition name"`
		Version int    `positional-arg-name:"VERSION" required:"true" description:"Version number to mark live"`
	} `positional-args:"yes"`
}

func (command *WorkflowsSetLiveCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	resp, err := agentAPIRequest(target, "PUT",
		"/api/v1/agent/workflows/"+url.PathEscape(command.Args.Name)+"/versions/"+strconv.Itoa(command.Args.Version)+"/live", nil)
	if err != nil {
		return err
	}
	if err := decodeOrError(resp, nil); err != nil {
		return err
	}

	fmt.Printf("workflow %s version %d is now live\n", command.Args.Name, command.Args.Version)
	return nil
}
```

- [ ] Verify it compiles and registers: `go build ./fly/... && go run ./fly agent workflows --help` — expect the four subcommands listed.
- [ ] Commit: `git add fly/commands && git commit -m "feat(workflow-store): fly agent workflows list/show/import/set-live"`

---

### Task 11: fly integration specs (mock ATC)

**Files:**

- Test: `fly/integration/agent_workflows_test.go`

**Steps:**

- [ ] Write the spec (recipe: `fly/integration/active_users_test.go`; `atcServer`/`targetName`/`flyPath` come from `fly/integration/suite_test.go`):

```go
package integration_test

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

const workflowDefYAML = `schema_version: 1
name: standard-dev
description: integration test workflow
prompts:
  work: |
    Do the work.
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`

var _ = Describe("fly agent workflows", func() {
	Describe("list", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []map[string]any{
						{"name": "standard-dev", "description": "the seed", "latest_version": 3, "live_version": 2, "content_hash": "abc123", "created_at": 1751900000},
					}),
				),
			)
		})

		It("prints name, latest, live, description", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "list")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`standard-dev\s+3\s+2\s+the seed`))
		})
	})

	Describe("show", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/agent/workflows/standard-dev/versions/2"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, workflow.Definition{
						Name: "standard-dev", Version: 2, ContentHash: "abc123",
						Live: true, RawYAML: workflowDefYAML,
					}),
				),
			)
		})

		It("prints the raw YAML for an explicit version", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "show", "standard-dev", "2")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`schema_version: 1`))
			Expect(sess.Out).To(gbytes.Say(`name: standard-dev`))
		})
	})

	Describe("import", func() {
		var defFile string

		BeforeEach(func() {
			dir := GinkgoT().TempDir()
			defFile = filepath.Join(dir, "standard-dev.yaml")
			Expect(os.WriteFile(defFile, []byte(workflowDefYAML), 0644)).To(Succeed())

			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", "/api/v1/agent/workflows/standard-dev/versions"),
					ghttp.VerifyBody([]byte(workflowDefYAML)),
					ghttp.RespondWithJSONEncoded(http.StatusOK, workflow.Definition{
						Name: "standard-dev", Version: 1, ContentHash: workflow.Hash([]byte(workflowDefYAML)),
					}),
				),
			)
		})

		It("POSTs the raw YAML and reports the assigned version", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "import", defFile)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`imported standard-dev version 1`))
		})

		It("rejects an invalid definition locally, before any API call", func() {
			bad := filepath.Join(GinkgoT().TempDir(), "bad.yaml")
			Expect(os.WriteFile(bad, []byte("schema_version: 1\nname: x\nsteps: []\n"), 0644)).To(Succeed())

			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "import", bad)
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`at least one step is required`))
		})
	})

	Describe("set-live", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/workflows/standard-dev/versions/2/live"),
					ghttp.RespondWith(http.StatusNoContent, nil),
				),
			)
		})

		It("PUTs the live marker and confirms", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "set-live", "standard-dev", "2")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
			Expect(sess.Out).To(gbytes.Say(`workflow standard-dev version 2 is now live`))
		})
	})

	Describe("set-live against an unknown version", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", "/api/v1/agent/workflows/standard-dev/versions/9/live"),
					ghttp.RespondWith(http.StatusNotFound, "unknown workflow version"),
				),
			)
		})

		It("exits non-zero with the server message", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "agent", "workflows", "set-live", "standard-dev", "9")
			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())
			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`unknown workflow version`))
		})
	})
})
```

- [ ] Run: `ginkgo --focus="fly agent workflows" ./fly/integration/` — expect all six specs green (the suite builds the fly binary; ~30s cold). These specs were written against the already-implemented Task 10 commands, so they should pass first run — if any fails, fix the command (or the spec's mock) until green; the RED half of this pair was Task 10's `--help` smoke gap.
- [ ] Run the whole fly integration tier to catch regressions: `make test-fly-integration`.
- [ ] Commit: `git add fly/integration/agent_workflows_test.go && git commit -m "test(workflow-store): fly agent workflows integration specs"`

---

### Task 12: Seed library — `standard-dev` v1 from the five ci-agent phases

The five ci-agent phases (`ci-agent/phases/plan.yaml` → spec+plan prompts, `implement.yaml`, `qa.yaml`, `review.yaml`, `fix.yaml`) decompose into a linear §6.1 sequence: plan → plan-approval checkpoint → implement → qa → review → fix. Prompts are inline (§6.2), adapted from `ci-agent/phases/prompts/*` to the frozen render context (`.Ticket .Spec .Tasks .Params`) and the tool surfaces later waves provide; iterating on this prose is exactly what v2+ imports are for.

**Files:**

- Create: `agent/workflow/seeds/standard-dev.yaml`
- Test: `agent/workflow/seed_test.go`

**Steps:**

- [ ] Write the failing test `agent/workflow/seed_test.go`:

```go
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
```

- [ ] Run to verify failure: `go test ./agent/workflow/ -run TestSeed` — expect `read seed: open seeds/standard-dev.yaml: no such file or directory`.
- [ ] Write `agent/workflow/seeds/standard-dev.yaml`:

```yaml
# standard-dev v1 — the five ci-agent phases (ci-agent/phases/{plan,
# implement,qa,review,fix}.yaml) decomposed into the §6 grammar.
# Import:  fly agent workflows import agent/workflow/seeds/standard-dev.yaml
# Promote: fly agent workflows set-live standard-dev 1
schema_version: 1
name: standard-dev
description: plan -> approve -> implement -> qa -> review -> fix; single agent per phase

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

prompts:
  plan: |
    You are a planning agent. Read the ticket with platform-mcp read_ticket
    (title: {{.Ticket.Title}}). Explore the repository to understand the
    affected components, then:
    1. Write a spec: problem statement, acceptance criteria, and
       out-of-scope notes. Submit it with platform-mcp submit_spec.
    2. Decompose the work into small, independently verifiable tasks and
       submit them with platform-mcp submit_plan.
    Keep rationale and tradeoffs in the spec body — downstream agents and
    humans rely on them.
  implement: |
    You are an implementation agent working in the workspace repository.
    Read the approved spec with platform-mcp read_ticket and the task list
    with platform-mcp list_tasks (call get_task per task for its detail as
    you reach it). For each task, follow TDD red-green-refactor: write the
    failing test, make it pass minimally, refactor. After each task, run
    dev-mcp run_tests with the affected components and mark the task done
    with platform-mcp update_task_status. Commit after each green task.
  qa: |
    You are a QA agent. Read the acceptance criteria with platform-mcp
    read_ticket and the tasks with platform-mcp list_tasks / get_task, then
    compare the implementation in the workspace against them. Identify
    untested behaviors and add meaningful coverage for them. Run dev-mcp
    run_tests on affected components, then the full suite. Fix any test you
    added that fails; report (do not fix) production defects you find, in
    the workspace notes.
  review: |
    You are a code-review agent. Read the spec and tasks with platform-mcp
    read_ticket and list_tasks / get_task, then review the workspace diff
    against the base branch for correctness bugs, missing coverage, and
    scope creep relative to that spec. For each finding, verify it by
    writing or running a test through dev-mcp before reporting. Write
    findings to review.json in the workspace.
  fix: |
    You are a fix agent. Read review.json in the workspace. Apply fixes for
    verified findings only, one commit per finding, re-running dev-mcp
    run_tests on affected components after each. Skip findings you can
    demonstrate are false positives, noting why in the commit trailer.

steps:
- agent: plan
  prompt: plan
  sidecars: [platform]
  budget_slice_usd: 2.0
  outputs: [workspace]

- checkpoint: plan-approval
  on_reject: send_back

- agent: implement
  prompt: implement
  sidecars: [dev, platform]
  budget_slice_usd: 6.0
  max_turns: 120
  inputs: [workspace]
  outputs: [workspace]

- agent: qa
  prompt: qa
  sidecars: [dev, platform]
  budget_slice_usd: 2.0
  inputs: [workspace]
  outputs: [workspace]

- agent: review
  prompt: review
  sidecars: [dev]
  budget_slice_usd: 2.0
  inputs: [workspace]
  outputs: [workspace]

- agent: fix
  prompt: fix
  sidecars: [dev]
  budget_slice_usd: 3.0
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
```

- [ ] Run to verify pass: `go test ./agent/workflow/ -run TestSeed` — expect `ok`.
- [ ] Full-workstream verification sweep:
  - `go test ./agent/workflow/ ./agent/api/workflows/`
  - `ginkgo ./atc/db/` (includes the factory specs)
  - `ginkgo ./atc/db/migration/`
  - `ginkgo ./atc/api/ ./atc/wrappa/`
  - `make test-fly-integration`
  - `go build ./...`
- [ ] Commit: `git add agent/workflow/seeds agent/workflow/seed_test.go && git commit -m "feat(workflow-store): standard-dev v1 seed from the five ci-agent phases"`

---

## Execution notes

**Full test suite for this workstream** (PostgreSQL required for the `atc/db` and `atc/api` tiers — check `pg_isready` first):

```bash
go test ./agent/workflow/ ./agent/api/workflows/   # plain Go, no DB
ginkgo ./atc/db/                                   # factory specs (~90s; template DB)
ginkgo ./atc/db/migration/                         # migration + legacy-upgrade specs
ginkgo ./atc/api/ ./atc/wrappa/                    # wiring + exhaustive-switch guard
make test-fly-integration                          # 576+ specs, mock ATC, ~30s
```

Never pass `--race` to the Ginkgo suites (parallel compilation failures, per CLAUDE.md). Note that `make test-unit`'s `ginkgo -r` only discovers Ginkgo suites — the plain-`testing` packages under `agent/` must be run with `go test ./agent/...` explicitly (same situation as the existing `agent/api/reviews` tests).

**Live verification (optional, after merge to the theborg deployment):** no live cluster is required by any test in this plan — everything runs against local Postgres and the mock ATC. To smoke-test against the live web node: `fly login` against concourse.home (theborg/cicd; see memory `reference_theborg_cicd_live_concourse.md`), then `fly agent workflows import agent/workflow/seeds/standard-dev.yaml`, `list`, `show standard-dev`, `set-live standard-dev 1`. Until agent-identity's `CheckAgentAuthorizationHandler` lands, these routes are admin-only in effect (contracts decision 21) — log in as an admin user.

**Merge coordination with wave-mates:**
- `atc/db/migration/legacy_upgrade_test.go:37` (`jetbridgeHeadMigration`): every wave-1 workstream bumps it; on conflict keep the highest number that exists in `atc/db/migration/migrations/`.
- `atc/api/handler.go` `NewHandler` signature and its two call sites (`atc/atccmd/command.go:2256`, `atc/api/api_suite_test.go:182`): parameter appends from parallel branches conflict textually but compose trivially — keep all appended params, in the same order across the three sites.
- `fly/commands/agent.go`: if credentials-and-budgets landed `fly agent auth` first, add the `Workflows` field to the existing `AgentCommand` instead of creating the file.
- The wrappa `authorized` case group and `roles.go` map: pure list appends; union on conflict.

**Rollback notes for the risky diffs:**
- The migration is additive (one new table, no existing-table changes); `1773106040_create_agent_workflow_definitions.down.sql` drops exactly the table, and no existing code path touches it unless the new routes are exercised. Reverting the workstream = revert the commits; no data migration to unwind.
- `atc/wrappa/api_auth_wrappa.go` has an exhaustive switch that panics on unknown route names — the routes.go and wrappa edits must land in the same commit (Task 9 does this). If a partial revert removes one side, the `ginkgo ./atc/wrappa/` "handles each route" spec catches the panic before deploy.
- `Promote` uses clear-then-set inside a single transaction against the `agent_workflow_definitions_live` partial unique index; if a rollout is interrupted there is no window where two versions are live (index-enforced), only a window where none is — dispatch (wave 4) treats "no live version" as not-dispatchable, so this is safe.

---

## Addendum

- **2026-07-08 (spec/plan delivery model — frozen delta):** Added the optional top-level `spec_delivery` grammar field to `workflow.Config` (Go `SpecDelivery string`, yaml/json `spec_delivery,omitempty`; values `""`/`mcp`/`files`, empty ⇒ `mcp`). Write-time validation (Task 4 `Config.Validate`) accepts only those three values and rejects any other; it is a normal hashed field (participates in the content hash like every other YAML key). This plan owns the grammar slot only — the field is INERT here (workflow-store never renders). It is consumed by **dispatch's renderer** (11-dispatch, which reads `SpecDelivery` to pick the read model: `mcp` injects no spec/plan bytes and DELETEs the old `AGENT_SPEC_MD`/`AGENT_PLAN_MD` env keys — agents read via the platform-mcp `read_ticket`/`list_tasks`/`get_task` tools; `files` materializes read-only `spec.md`/`plan.md` mounted as the `ticket` artifact) and is referenced by **contracts §6** (grammar mirror). Supersedes the prior "rendered spec.md/plan.md delivered via env vars" design so the DB stays the single source of truth and nothing is flattened by default. Affected workstreams: platform-mcp-hitl, dispatch, workflow-store, ticket-core-consumers. Edits landed: `Config` struct field (Task 3), `Validate` accept/reject rule (Task 4), Task 3 happy-path default-case assertion, Task 4 reject case + `files` accept assertion, and the Task 1 slot-shape freeze + §11 amendment-log entries.

- **2026-07-09 (design-review F5 — seed prompt/delivery coherence, WAVE-1 blocker):** The `standard-dev` seed omits `spec_delivery`, so it resolves to the default `mcp` read model — but its Task 8 `implement`/`qa`/`review` prompts still told agents "the approved spec and plan are in spec.md and plan.md" and embedded the bare `{{.Spec}}`/`{{.Tasks}}` template tokens, which are only populated under `spec_delivery: files`. Under `mcp` no spec/plan bytes are injected, so those prompts pointed the reference workflow (the exemplar every import copies) at files that never exist. Rewrote all three prompts to the default MCP read model — read via platform-mcp `read_ticket` / `list_tasks` / `get_task`, mirroring the `plan` prompt — and dropped every `spec.md`/`plan.md`/`{{.Spec}}`/`{{.Tasks}}` reference from the seed body. The seed stays on the default path (NOT switched to `files`) so it demonstrates the reference read model. Guarded by a new coherence assertion in `TestSeedStandardDevValidates` (Task 8): resolve `spec_delivery` (empty ⇒ `mcp`) and, when it is `mcp`, fail if any `cfg.Prompts` body contains `"spec.md"`, `"plan.md"`, `"{{.Spec}}"`, or `"{{.Tasks}}"` (added `"strings"` import). Affected workstreams: dispatch (renderer contract unchanged — this only aligns the seed prose with the already-frozen `mcp` model), platform-mcp-hitl (read-tool surface). Edits landed: seed `implement`/`qa`/`review` prompt bodies (Task 8 yaml), `TestSeedStandardDevValidates` coherence assertion + `strings` import (Task 8 test).

- **2026-07-09 (design-review F7 — ask_timeout cross-field validation):** `Config.Validate` (Task 4) validated `hitl.ask_timeout` (enum) and `hitl.ask_timeout_seconds` (>= 0) independently, so a definition with `ask_timeout: default` or `ask_timeout: fail` and `ask_timeout_seconds: 0` (or any value <= 0) passed import — yet that combination never times out, so the timeout policy never fires and the run silently parks forever, the exact opposite of what `default`/`fail` request. Added a cross-field rule after the two existing checks that REJECTS `ask_timeout ∈ {default, fail}` with `ask_timeout_seconds <= 0`, failing loudly at import. `park` (with or without a deadline) is unaffected, so the seed (`ask_timeout: park`, `ask_timeout_seconds: 0`) still validates. Guarded by two new `TestValidateRejects` cases (Task 4): mutate the fixture's `ask_timeout: park` to `default` and to `fail` (leaving `ask_timeout_seconds: 0`), each expecting the substring `requires ask_timeout_seconds > 0`. Affected workstreams: platform-mcp-hitl (consumes the `hitl` block; this only tightens import validation, no shape change). Edits landed: `Config.Validate` cross-field check (Task 4 parse.go), two `TestValidateRejects` cases (Task 4 test).
