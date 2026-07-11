# Agent Step Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a first-class `agent:` step type that runs the claude CLI in a jetbridge pod with declared MCP sidecars, ingests its flight recorder (results.json + events.ndjson) server-side into `agent_run_metrics`, proves sidecar-MCP wiring on a live theborg cluster, and cuts the live theborg/cicd agent-review job over to it with a dual-running verification period.

**Architecture:** The step follows the existing step-type recipe exactly (config union in `atc/steps.go`, plan union in `atc/plan.go`, planner visitor, validator visitor, engine builder + core step factory, `atc/exec` implementation modeled on `TaskStep` including its sidecar machinery and `attachOrRun` resume path). The pod's main container runs a new deterministic `agent-runner` binary that invokes the claude CLI and writes the flight recorder to an implicit `flight` output; the exec ingests that output into the DB synchronously before the step returns, so ingestion always beats artifact-fabric GC. The shared `agent/schema` package becomes its own nested Go module consumed by both the main module and ci-agent (spec open item 11).

**Tech Stack:** Go 1.25 (main module + `agent/schema` nested module + ci-agent module), PostgreSQL migrations (`atc/db/migration/migrations`), squirrel + counterfeiter factory recipe, Ginkgo/Gomega for atc packages, testify suites for `atc/steps_test.go` / `atc/builds/planner_test.go`, Elm for the build-page plan rendering, plain-Go `//go:build live` tests against theborg.

---

## Context

**Charter (workstreams.json `agent-step`, wave 2, size L).** Scope in: (1) full step-type recipe; (2) step config schema v1 with inline prompt, sidecar mounts, budget-slice env, artifacts in/out, resumable execution, no push credentials in the agent pod; (3) `agent_run_metrics` migration + factory + principal-authed ingest route with tolerant parsing and an ingestion-before-GC guarantee; (4) open item 11 — extract ONLY the results/events schema types from ci-agent into a shared module; (5) live theborg sidecar-MCP wiring proof; (6) cutover of the live theborg/cicd agent-review job with dual-running. Scope out: credential vaulting (v1 consumes ordinary var-source secrets), real MCP sidecar servers, harvest/push, scorecard views.

**Prior waves (assumed landed exactly as 00-shared-contracts.md defines):**
- **agent-identity**: `agent_principals` table (§1.2), `auth.CheckAgentPrincipalHandlerFactory` wrappa tier (consumed as a `wrappa` struct field via `checkAgentPrincipalHandlerFactory.HandlerFor(handler, rejector, scope)`; constructed with `auth.NewCheckAgentPrincipalHandlerFactory`), `CheckAgentAuthorizationHandler` for team-less `/api/v1/agent/*` authorized routes (§4.2 closing paragraph, decision 21), scope vocabulary including `metrics:write` exported as `principals.ScopeMetricsWrite` (§4.1). Note: contracts §4.1 describes this tier with the shorthand `auth.CheckAgentPrincipalHandler(...)`, but agent-identity froze the actual Go surface as the `CheckAgentPrincipalHandlerFactory` interface (01-agent-identity §11 addendum) — use the factory form.
- **credentials-and-budgets**: `agent_cost_ledger` (§1.4), `agent/budget` package with `budget.Checker` interface + `budget.LedgerEntry` + `Remaining` (§2.7) and its counterfeiter fake `agent/budget/budgetfakes.FakeChecker`, the `agent-run-<run-id>` ephemeral secret contract (§8.2).
- **pipeline-runs, dev-mcp, workflow-store**: not directly consumed by code in this plan; the render-time-resolution rule (§2.8) means this step never reads workflow tables — dispatch's wave-4 renderer emits the exact `AgentStep` shape defined here.

**Contract-surface sections this plan PRODUCES** (00-shared-contracts.md):
- §1.8 `agent_run_metrics` (migration 1773106060)
- §2.4 RunMetrics (`agent/schema/metrics.go`)
- §2.8 Agent step config (plan union) — the `AgentStep` half
- §4.2 route table rows `SubmitAgentRunMetrics` and `ListAgentRunMetrics`
- §5 Flight-recorder event schema (new event constants + payload structs in `agent/schema`)
- §8.1 the agent-step-owned env vars
- Conventions bullet 2 / decision 2: the `agent/schema` nested-module extraction

**Contract-surface sections this plan CONSUMES:**
- §1.2 / §4.1 agent-principal auth (`principal(metrics:write)` tier, `CheckAgentAuthorizationHandler`)
- §2.7 budget library (`budget.Checker.StepSlice`, `budget.Checker.Record`)
- §8.1 env-var table / §8.2 ephemeral run secret (env `secretKeyRef` consumption via the existing `BuildSecretEnv` seam)
- §8.5 sidecar image packaging convention (for the agent-runner image CI job shape)

**Key repo seams verified against real code** (branch `jetbridge`): `atc/steps.go` (StepPrecedence :224, StepVisitor :190, RunStep :364, TaskStep.Sidecars :357), `atc/plan.go` (:11 union, :379 RunPlan), `atc/step_recursor.go`, `atc/step_validator.go` (:233 VisitRun, :114 sidecar loop), `atc/builds/planner.go` (:83 VisitRun), `atc/engine/builder.go` (:20 CoreStepFactory, :138/:358 run dispatch), `atc/engine/step_factory.go` (:166 RunStep, :36 option pattern), `atc/exec/task_step.go` (whole file — the exec template), `atc/db/agent_reviews_factory.go` (factory recipe), `atc/routes.go` (:254 agent routes), `atc/wrappa/api_auth_wrappa.go` (:112), `atc/api/accessor/roles.go` (:108), `atc/api/handler.go` (:122, :269), `atc/atccmd/command.go` (:218 flag, :1990 CoreStepFactory wiring, :2297 factory args), `agent/schema/` + `ci-agent/schema/` (near-identical copies), `ci-agent/llm/result.go` (CLI envelope), `deploy/concourse-pipeline.yml` (:42 agent-review job), `atc/worker/jetbridge/live_sidecar_test.go` + `live_task_resume_test.go` (live-test patterns), `web/elm/src/Concourse.elm` (:666 plan decoder — unknown step types FAIL decoding, so a minimal Elm addition is required).

---

### Task 1: Wave-start contract addendum (env/flag conventions this plan adds)

The contracts doc does not specify how the prompt text, flight-recorder directory, or main-container image reach the pod. These are agent-step-owned decisions that dispatch (wave 4) and gateway (wave 3) will read, so they must be written down before code lands.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (§8.1 table region ~line 1358; §11 amendment log ~line 1463)

**Steps:**

- [ ] Append the following rows/notes immediately after the §8.1 env-var table (after the fixed-ports paragraph, ~line 1378):

```markdown
**Agent-step-owned additions (added by agent-step wave-2 plan; consumers: dispatch renderer, gateway):**

| Env var | Container(s) | Source | Meaning |
|---|---|---|---|
| `AGENT_PROMPT` | main | literal (from `AgentStep.Prompt`) | inline prompt text; mutually exclusive with `AGENT_PROMPT_FILE` |
| `AGENT_PROMPT_FILE` | main | literal (from `AgentStep.PromptFile`) | artifact-relative path (`input-name/path.md`) resolved inside the workdir |
| `AGENT_MODEL` / `AGENT_MAX_TURNS` | main | literal | claude CLI `--model` / `--max-turns` |
| `AGENT_OUTPUT_SCHEMA` | main | literal | artifact-relative JSON-schema path (optional) |
| `AGENT_FLIGHT_DIR` | main | literal `<workdir>/flight` | where agent-runner writes results.json + events.ndjson; backed by the implicit `flight` output volume |

- **Implicit `flight` output:** every agent step gets an output volume named `flight` (reserved; a user-declared output named `flight` is a validation error). The exec ingests `flight/results.json` and `flight/events.ndjson` synchronously before the step returns — this is the ingestion-before-artifact-GC guarantee.
- **Ticket/run identity via env:** the exec copies `AgentStep.Env` verbatim into the pod and *reads back* `AGENT_TICKET_ID`, `AGENT_PIPELINE_RUN_ID`, `AGENT_WORKFLOW_NAME`, `AGENT_WORKFLOW_VERSION`, `AGENT_WORKFLOW_HASH` from it for metrics tagging and budget-slice lookup. The renderer (dispatch) sets these keys; hand-written pipelines may set them too. Absent keys = pure-CI agent step (NULL tags).
- **MCP URL derivation:** for each sidecar whose `name` is `dev`, `platform`, or `gateway`, the exec sets `DEV_MCP_URL` / `PLATFORM_MCP_URL` / `GATEWAY_MCP_URL` to `http://127.0.0.1:7780|7781|7782/mcp` per the fixed-port decision. Other sidecar names get no URL env.
- **Main container image:** the `agent:` step config has no image field (per §2.8). The image comes from the web-node flag `--agent-step-image` (`CONCOURSE_AGENT_STEP_IMAGE`); the image must contain the claude CLI and the `agent-runner` entrypoint (image `ghcr.io/tdmtrader/agent-runner`, built per the §8.5 convention with a version tag; also pushed to `registry.home/agent-runner` for theborg). An unset flag makes any `agent:` step error at run time with a clear message.
- **Main process:** the exec runs `agent-runner` (argv0 only, no args) as process ID `agent` so `attachOrRun` reattaches across web restarts, exactly like the task step's `task` process ID.
```

- [ ] Append to §11 amendment log:

```markdown
- 2026-07-08 (agent-step wave-2 plan): §8.1 gains agent-step-owned env vars (`AGENT_PROMPT`, `AGENT_PROMPT_FILE`, `AGENT_MODEL`, `AGENT_MAX_TURNS`, `AGENT_OUTPUT_SCHEMA`, `AGENT_FLIGHT_DIR`), the implicit `flight` output convention, MCP-URL-by-sidecar-name derivation, the `--agent-step-image` web flag, and the `agent-runner` image + `agent` process-ID conventions. Affects: dispatch (renderer emits `AgentStep.Env` incl. identity keys), gateway-mcp (reads `AGENT_BUDGET_SLICE_USD` as already specified). No existing rows changed.
```

- [ ] Commit: `git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md && git commit -m "docs(agent-step): contract addendum for agent-step env, flight output, and image flag"`

---

### Task 2: Extract `agent/schema` into a nested Go module

Per contracts conventions bullet 2 / decision 2. `agent/schema` non-test code imports only the standard library (verified: `event.go`, `event_reader.go`, `event_writer.go`, `results.go`), so the nested module needs zero external requires (test files use Ginkgo — give the module its own small `go.mod` requires for tests).

**Files:**
- Create: `agent/schema/go.mod`
- Modify: `go.mod:1` (root — add require + replace)
- Modify: `Makefile:7` (test-unit: skip nested module; add explicit test line)
- Test: `agent/schema/schema_suite_test.go` (existing; must still pass inside the new module)

**Steps:**

- [ ] Create `agent/schema/go.mod`:

```
module github.com/concourse/concourse/agent/schema

go 1.25.6
```

- [ ] Run `cd /Users/tdmtrader/concourse/concourse/agent/schema && go mod tidy && go test ./...` — expect Ginkgo/Gomega requires to be added to the new go.mod by tidy and tests to pass.
- [ ] Add to the root `go.mod`: in the first `require (` block add `github.com/concourse/concourse/agent/schema v0.0.0` and at the bottom of the file add:

```
replace github.com/concourse/concourse/agent/schema => ./agent/schema
```

- [ ] Run `cd /Users/tdmtrader/concourse/concourse && go build ./... && go mod tidy` — expect clean build (nothing in the main module imports the package yet; the require is consumed starting Task 7).
- [ ] Update `Makefile` `test-unit` so the nested module is tested explicitly (ginkgo -r from the root no longer descends into a nested module correctly):

```make
test-unit:
	@echo "==> Running unit tests..."
	ginkgo -r -p --keep-going --flake-attempts=1 \
		--skip-package=./integration,testflight,topgun,./worker/integration,./worker/runtime/integration,./worker/baggageclaim,ci-agent,fly/integration,testhelpers/otel,agent/schema
	cd agent/schema && go test ./... -count=1
```

- [ ] Run `make test-unit` (needs local PostgreSQL, ~3 min) — expect green.
- [ ] Commit: `git add agent/schema/go.mod agent/schema/go.sum go.mod go.sum Makefile && git commit -m "refactor(agent-schema): extract agent/schema into nested Go module (shared-contracts decision 2)"`

---

### Task 3: Merge ci-agent/schema into agent/schema and switch ci-agent's imports

`ci-agent/schema` has five extra files (`feedback.go`, `review.go`, `qa.go`, `fix_report.go`, `planning_input.go`, all stdlib-only) plus `plan.*` event constants and `EventArtifactWritten` (agent/schema has a typo'd `EventArtifactWriten`). The two `Event` structs differ: ci-agent uses `EventType EventType` + `Data json.RawMessage`; agent/schema uses `Type EventType` + `Data map[string]interface{}`. The merged canonical shape is `Type EventType` + `Data json.RawMessage` (wire-identical, avoids double-decoding, and typed payload structs land in Task 4). Only `ci-agent/phaserunner/runner.go` constructs events outside the schema package, so the rename fallout is contained.

**Files:**
- Modify: `agent/schema/event.go` (merge constants; change `Data` to `json.RawMessage`; rename `EventArtifactWriten`→`EventArtifactWritten`; keep `Timestamp string` with RFC3339Nano-tolerant validation)
- Create: `agent/schema/feedback.go`, `agent/schema/review.go`, `agent/schema/qa.go`, `agent/schema/fix_report.go`, `agent/schema/planning_input.go` (moved from `ci-agent/schema/` verbatim, package `schema`)
- Modify: `ci-agent/go.mod` (require + replace `../agent/schema`)
- Modify: every ci-agent file importing `github.com/concourse/ci-agent/schema` (30 files; `grep -rl "ci-agent/schema" ci-agent --include='*.go'`)
- Modify: `ci-agent/phaserunner/runner.go` (`Event{EventType: …}` → `Event{Type: …}`)
- Delete: `ci-agent/schema/` (entire directory)
- Test: `agent/schema/event_test.go`, `agent/schema/event_io_test.go` (updated for `json.RawMessage` Data), moved `agent/schema/feedback_test.go` etc.; `cd ci-agent && go test ./...`

**Steps:**

- [ ] In `agent/schema/event.go`, write the failing test first — extend `agent/schema/event_test.go` with a spec asserting the merged surface:

```go
It("exposes the merged event-type constants and raw-message data", func() {
	e := schema.Event{
		Timestamp: "2026-07-08T12:00:00Z",
		Type:      schema.EventPlanInputParsed,
		Data:      json.RawMessage(`{"k":"v"}`),
	}
	Expect(e.Validate()).To(Succeed())
	Expect(string(schema.EventArtifactWritten)).To(Equal("artifact.written"))
})
```

- [ ] Run `cd agent/schema && go test ./...` — expect compile failure (`EventPlanInputParsed` undefined, `Data` type mismatch).
- [ ] Update `agent/schema/event.go`: change `Data` to `json.RawMessage`, rename the typo'd constant, add ci-agent's extra constants, validate timestamps with `time.RFC3339Nano` (parses both), require `len(e.Data) > 0`:

```go
const (
	EventAgentStart           EventType = "agent.start"
	EventAgentEnd             EventType = "agent.end"
	EventSkillStart           EventType = "skill.start"
	EventSkillEnd             EventType = "skill.end"
	EventToolCall             EventType = "tool.call"
	EventToolResult           EventType = "tool.result"
	EventArtifactWritten      EventType = "artifact.written"
	EventDecision             EventType = "decision"
	EventError                EventType = "error"
	EventPlanInputParsed      EventType = "plan.input_parsed"
	EventPlanSpecGenerated    EventType = "plan.spec_generated"
	EventPlanPlanGenerated    EventType = "plan.plan_generated"
	EventPlanConfidenceScored EventType = "plan.confidence_scored"
)

type Event struct {
	Timestamp string          `json:"ts"`
	Type      EventType       `json:"event"`
	Data      json.RawMessage `json:"data"`
}
```

  Keep `EventWriter.Write` setting a missing timestamp to `time.Now().UTC().Format(time.RFC3339)` before validating (ci-agent's writer behavior — phaserunner relies on it). Update `MarshalJSON` to emit `{}` for nil Data. Fix `event_io_test.go`/`event_test.go`/`results_test.go` fixtures for the RawMessage type.
- [ ] Run `cd agent/schema && go test ./...` — expect pass.
- [ ] `git mv` the five ci-agent-only schema files + their tests into `agent/schema/` (keep package name `schema`; they are stdlib-only — verified imports: `encoding/json`, `fmt`, `strings`). Do NOT move `ci-agent/schema/event.go`, `results.go`, `doc.go` (superseded) — delete them with the directory below.
- [ ] Run `cd agent/schema && go test ./...` — expect pass (moved tests run in the nested module).
- [ ] Add to `ci-agent/go.mod`:

```
require github.com/concourse/concourse/agent/schema v0.0.0

replace github.com/concourse/concourse/agent/schema => ../agent/schema
```

- [ ] Switch imports mechanically: `cd ci-agent && grep -rl 'github.com/concourse/ci-agent/schema' --include='*.go' . | xargs sed -i '' 's|github.com/concourse/ci-agent/schema|github.com/concourse/concourse/agent/schema|g'`
- [ ] Delete `ci-agent/schema/`: `git rm -r ci-agent/schema`
- [ ] Run `cd ci-agent && go build ./...` — expect exactly the field-name compile errors in `phaserunner/runner.go` (the `EventType:` literals). Fix them to `Type:`. Re-run until clean. (Any other `Data:` type mismatches surface here too — ci-agent already uses `json.RawMessage`, so none are expected.)
- [ ] Run `cd ci-agent && go mod tidy && go test ./... -count=1 -timeout 5m` (this is `make test-ci-agent`) — expect pass.
- [ ] Run `cd /Users/tdmtrader/concourse/concourse && go build ./...` — expect pass (main module untouched by the merge).
- [ ] Commit: `git add -A agent/schema ci-agent && git commit -m "refactor(schema): merge ci-agent/schema into shared agent/schema module and switch imports (spec open item 11)"`

---

### Task 4: Three-way status mapping + new flight-recorder event constants and payloads

Produces the §5 additions (all consumers in waves 3–5 import these) and the conventions-bullet status mapping (`pass→ok`, `fail→failed`, `error→error`, `abstain→failed` + abstained flag).

> **Amended 2026-07-10 (PARK-V2 seam delta §F).** The schema surface this task lands is extended additively by Task 21: wire status `parked` (`StatusParked`/`RunStatusParked`, `ThreeWayStatus` maps `parked → parked`), `session_id` on `Results` and `StepEndData`, `resumed`/`replayed` on `StepStartData`, and two new event types `step.park`/`step.resume`. Nothing in this task's code changes — implement it as written; Task 21 carries the additions so already-landed code needs no rebase.

**Files:**
- Create: `agent/schema/status.go`
- Create: `agent/schema/event_payloads.go`
- Test: `agent/schema/status_test.go`, `agent/schema/event_payloads_test.go`

**Steps:**

- [ ] Write `agent/schema/status_test.go`:

```go
package schema_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	schema "github.com/concourse/concourse/agent/schema"
)

var _ = Describe("ThreeWayStatus", func() {
	DescribeTable("maps results.json wire statuses to the DB/API taxonomy",
		func(in schema.Status, want string, wantAbstained bool) {
			got, abstained := schema.ThreeWayStatus(in)
			Expect(got).To(Equal(want))
			Expect(abstained).To(Equal(wantAbstained))
		},
		Entry("pass", schema.StatusPass, schema.RunStatusOK, false),
		Entry("fail", schema.StatusFail, schema.RunStatusFailed, false),
		Entry("error", schema.StatusError, schema.RunStatusError, false),
		Entry("abstain", schema.StatusAbstain, schema.RunStatusFailed, true),
		Entry("unknown", schema.Status("bogus"), schema.RunStatusError, false),
	)
})
```

- [ ] Run `cd agent/schema && go test ./...` — expect compile failure (undefined `ThreeWayStatus`).
- [ ] Write `agent/schema/status.go`:

```go
package schema

// Three-way run/step status taxonomy used by the DB and APIs
// (shared-contracts conventions: "agent did badly" != "platform broke").
// results.json keeps its v1.0 wire values (pass/fail/error/abstain);
// this mapping is the only bridge between the two vocabularies.
const (
	RunStatusOK     = "ok"
	RunStatusFailed = "failed"
	RunStatusError  = "error"
)

// ThreeWayStatus maps a results.json Status onto the three-way taxonomy.
// abstain maps to failed with abstained=true so callers can record
// `"abstained": true` metadata. Unknown values map to error.
func ThreeWayStatus(s Status) (status string, abstained bool) {
	switch s {
	case StatusPass:
		return RunStatusOK, false
	case StatusFail:
		return RunStatusFailed, false
	case StatusError:
		return RunStatusError, false
	case StatusAbstain:
		return RunStatusFailed, true
	default:
		return RunStatusError, false
	}
}
```

- [ ] Run `cd agent/schema && go test ./...` — expect pass.
- [ ] Write `agent/schema/event_payloads_test.go` (round-trip one payload through an Event, spot-check snake_case keys):

```go
package schema_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	schema "github.com/concourse/concourse/agent/schema"
)

var _ = Describe("event payloads", func() {
	It("marshals StepEndData with snake_case keys", func() {
		data, err := json.Marshal(schema.StepEndData{
			StepName: "implement", Status: schema.RunStatusOK,
			Summary: "done", WallTimeSeconds: 42, CostUSD: 0.5, Turns: 7,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(string(data)).To(MatchJSON(`{"step_name":"implement","status":"ok","summary":"done","wall_time_seconds":42,"cost_usd":0.5,"turns":7}`))
		e := schema.Event{Timestamp: "2026-07-08T12:00:00Z", Type: schema.EventStepEnd, Data: data}
		Expect(e.Validate()).To(Succeed())
	})
})
```

- [ ] Run `cd agent/schema && go test ./...` — expect compile failure.
- [ ] Write `agent/schema/event_payloads.go` — the full §5 table: constants and one struct per new event type (producers in later waves import these so they cannot drift):

```go
package schema

// New event types per shared-contracts §5. Producers may add data keys but
// never repurpose them; consumers must ignore unknown keys and types.
const (
	EventStepStart         EventType = "step.start"
	EventStepEnd           EventType = "step.end"
	EventGateStart         EventType = "gate.start"
	EventGateResult        EventType = "gate.result"
	EventSubagentCall      EventType = "subagent.call"
	EventSubagentResult    EventType = "subagent.result"
	EventCostRecord        EventType = "cost.record"
	EventBudgetWarn        EventType = "budget.warn"
	EventBudgetStop        EventType = "budget.stop"
	EventHumanAsk          EventType = "human.ask"
	EventHumanAnswer       EventType = "human.answer"
	EventCheckpointWait    EventType = "checkpoint.wait"
	EventCheckpointRelease EventType = "checkpoint.release"
	EventJudgeScore        EventType = "judge.score"
	EventPushDone          EventType = "push.done"
)

type StepStartData struct {
	StepName        string  `json:"step_name"`
	BuildID         int     `json:"build_id"`
	PlanID          string  `json:"plan_id"`
	TicketID        *int    `json:"ticket_id,omitempty"`
	WorkflowName    string  `json:"workflow_name,omitempty"`
	WorkflowVersion *int    `json:"workflow_version,omitempty"`
	WorkflowHash    string  `json:"workflow_hash,omitempty"`
	BudgetSliceUSD  float64 `json:"budget_slice_usd,omitempty"`
}

type StepEndData struct {
	StepName        string  `json:"step_name"`
	Status          string  `json:"status"` // ok | failed | error
	Summary         string  `json:"summary"`
	WallTimeSeconds int     `json:"wall_time_seconds"`
	CostUSD         float64 `json:"cost_usd"`
	Turns           int     `json:"turns"`
}

type GateStartData struct {
	Gate      string `json:"gate"` // build | test | lint
	Component string `json:"component"`
	Scope     string `json:"scope"` // affected | full
}

type GateResultData struct {
	Gate            string  `json:"gate"`
	Component       string  `json:"component"`
	Scope           string  `json:"scope"`
	Status          string  `json:"status"`
	DurationSeconds float64 `json:"duration_seconds"`
	Summary         string  `json:"summary"`
	LogArtifact     string  `json:"log_artifact,omitempty"`
}

type SubagentCallData struct {
	CallID      string `json:"call_id"`
	Tool        string `json:"tool"` // request_review | ask_agent
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	PromptChars int    `json:"prompt_chars"`
}

type SubagentResultData struct {
	CallID       string  `json:"call_id"`
	Status       string  `json:"status"`
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Turns        int     `json:"turns"`
	CostUSD      float64 `json:"cost_usd"`
	DurationMS   int     `json:"duration_ms"`
	FindingCount *int    `json:"finding_count,omitempty"`
}

// CostRecordData mirrors budget.LedgerEntry (shared-contracts §2.7 / §1.4).
type CostRecordData struct {
	Source              string  `json:"source"`
	Provider            string  `json:"provider"`
	Model               string  `json:"model"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	Turns               int     `json:"turns"`
	CostUSD             float64 `json:"cost_usd"`
}

type BudgetData struct {
	Scope        string  `json:"scope"` // step | ticket | daily
	LimitUSD     float64 `json:"limit_usd"`
	SpentUSD     float64 `json:"spent_usd"`
	RemainingUSD float64 `json:"remaining_usd"`
}

type HumanAskData struct {
	QuestionID int      `json:"question_id"`
	Kind       string   `json:"kind"` // question | checkpoint
	Question   string   `json:"question"`
	Options    []string `json:"options"`
}

type HumanAnswerData struct {
	QuestionID  int    `json:"question_id"`
	Answer      string `json:"answer"`
	AnsweredBy  string `json:"answered_by"`
	WaitSeconds int    `json:"wait_seconds"`
	TimedOut    bool   `json:"timed_out"`
}

type CheckpointWaitData struct {
	QuestionID int    `json:"question_id"`
	Checkpoint string `json:"checkpoint"`
}

type CheckpointReleaseData struct {
	QuestionID int    `json:"question_id"`
	Approved   bool   `json:"approved"`
	AnsweredBy string `json:"answered_by"`
}

type JudgeScoreDimension struct {
	Name      string  `json:"name"`
	Score     float64 `json:"score"`
	Max       float64 `json:"max"`
	Rationale string  `json:"rationale"`
}

type JudgeScoreData struct {
	RubricHash string                `json:"rubric_hash"`
	Dimensions []JudgeScoreDimension `json:"dimensions"`
	Total      float64               `json:"total"`
	MaxTotal   float64               `json:"max_total"`
	Model      string                `json:"model"`
	CostUSD    float64               `json:"cost_usd"`
}

type PushDoneData struct {
	Branch           string `json:"branch"`
	Sha              string `json:"sha"`
	ManifestArtifact string `json:"manifest_artifact"`
}
```

- [ ] Run `cd agent/schema && go test ./...` — expect pass.
- [ ] Commit: `git add agent/schema && git commit -m "feat(agent-schema): three-way status mapping and flight-recorder event payloads (contracts s5)"`

---

### Task 5: `Usage` and `RunMetrics` shared types

Produces §2.4 exactly. `Usage` keeps ci-agent's JSON field names (`ci-agent/llm/result.go` — `input_tokens`, `output_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`), per §2.4's "same shape as ci-agent/llm.Usage".

**Files:**
- Create: `agent/schema/metrics.go`
- Test: `agent/schema/metrics_test.go`

**Steps:**

- [ ] Write `agent/schema/metrics_test.go`:

```go
package schema_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	schema "github.com/concourse/concourse/agent/schema"
)

var _ = Describe("RunMetrics", func() {
	It("round-trips the ingest payload shape", func() {
		ticket := 7
		rm := schema.RunMetrics{
			TicketID: &ticket, BuildID: 123, PlanID: "5f2a", StepName: "implement",
			Status: schema.RunStatusOK, Summary: "did the thing", Model: "claude-sonnet-4-5",
			Usage: schema.Usage{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 10, CacheCreationInputTokens: 5},
			Turns: 9, WallTimeSeconds: 60, CostUSD: 0.42,
			Results:        json.RawMessage(`{"schema_version":"1.0","status":"pass"}`),
			EventsArtifact: "vol-abc123",
			EventCounts:    map[string]int{"tool.call": 4},
		}
		data, err := json.Marshal(rm)
		Expect(err).ToNot(HaveOccurred())
		var back schema.RunMetrics
		Expect(json.Unmarshal(data, &back)).To(Succeed())
		Expect(back).To(Equal(rm))
		Expect(string(data)).To(ContainSubstring(`"cache_read_input_tokens":10`))
	})
})
```

- [ ] Run `cd agent/schema && go test ./...` — expect compile failure.
- [ ] Write `agent/schema/metrics.go`:

```go
package schema

import "encoding/json"

// Usage captures token consumption from an LLM call. JSON field names match
// the claude CLI envelope (and ci-agent/llm.Usage).
type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// RunMetrics is one agent step's flight-recorder rollup — both the ingest
// payload for SubmitAgentRunMetrics and the row shape of agent_run_metrics
// (shared-contracts §2.4 / §1.8).
type RunMetrics struct {
	TicketID        *int            `json:"ticket_id,omitempty"`
	PipelineRunID   *int            `json:"pipeline_run_id,omitempty"`
	BuildID         int             `json:"build_id"`
	PlanID          string          `json:"plan_id"`
	StepName        string          `json:"step_name"`
	WorkflowName    string          `json:"workflow_name,omitempty"`
	WorkflowVersion *int            `json:"workflow_version,omitempty"`
	WorkflowHash    string          `json:"workflow_hash,omitempty"`
	Status          string          `json:"status"` // ok | failed | error
	Summary         string          `json:"summary"`
	Model           string          `json:"model"`
	Usage           Usage           `json:"usage"`
	Turns           int             `json:"turns"`
	WallTimeSeconds int             `json:"wall_time_seconds"`
	CostUSD         float64         `json:"cost_usd"`
	Results         json.RawMessage `json:"results,omitempty"`
	EventsArtifact  string          `json:"events_artifact,omitempty"`
	EventCounts     map[string]int  `json:"event_counts,omitempty"`
	CreatedAt       int64           `json:"created_at,omitempty"` // epoch seconds; set by the DB on read
}
```

- [ ] Run `cd agent/schema && go test ./...` — expect pass.
- [ ] Commit: `git add agent/schema && git commit -m "feat(agent-schema): Usage and RunMetrics ingest/row types (contracts s2.4)"`

---

### Task 6: `agent_run_metrics` migration (1773106060)

DDL exactly per §1.8. Migration numbers come from the agent-step block 1773106060–69.

> **Amended 2026-07-10 (PARK-V2 seam delta §B6/§F).** The `status` CHECK below is widened to include `'parked'` and the table gains a `session_id` column by the additive migration `1773106062` (Task 21) — do NOT edit this migration in place. Task 8's factory column lists (upsert + scan) must include `session_id` once Task 21 lands (one-line extensions, covered by the factory suite). The agent-step block also owns `1773106061` (`agent_run_step_state`, Task 25).

**Files:**
- Create: `atc/db/migration/migrations/1773106060_create_agent_run_metrics.up.sql`
- Create: `atc/db/migration/migrations/1773106060_create_agent_run_metrics.down.sql`
- Test: verified by the Task 8 factory suite (the atc/db suite migrates the template DB); syntax-check here via the migration suite

**Steps:**

- [ ] Write `1773106060_create_agent_run_metrics.up.sql`:

```sql
CREATE TABLE agent_run_metrics (
    id                    BIGSERIAL PRIMARY KEY,
    ticket_id             INTEGER,                 -- NULL for pure-CI agent steps
    pipeline_run_id       INTEGER,
    build_id              INTEGER NOT NULL,
    plan_id               TEXT NOT NULL DEFAULT '',    -- atc plan ID of the step (unique within build)
    step_name             TEXT NOT NULL,
    workflow_name         TEXT NOT NULL DEFAULT '',
    workflow_version      INTEGER,
    workflow_hash         TEXT NOT NULL DEFAULT '',    -- content_hash frozen at render time
    status                TEXT NOT NULL CHECK (status IN ('ok','failed','error')),
    summary               TEXT NOT NULL DEFAULT '',
    model                 TEXT NOT NULL DEFAULT '',
    input_tokens          BIGINT NOT NULL DEFAULT 0,
    output_tokens         BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens     BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    turns                 INTEGER NOT NULL DEFAULT 0,
    wall_time_seconds     INTEGER NOT NULL DEFAULT 0,
    cost_usd              NUMERIC(12,6) NOT NULL DEFAULT 0,
    results               JSONB,                   -- full results.json payload
    events_artifact       TEXT NOT NULL DEFAULT '',-- artifact-fabric handle for events.ndjson
    event_counts          JSONB,                   -- {"tool.call": 87, "subagent.call": 3, ...}
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX agent_run_metrics_build_plan ON agent_run_metrics (build_id, plan_id);
CREATE INDEX agent_run_metrics_ticket   ON agent_run_metrics (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX agent_run_metrics_workflow ON agent_run_metrics (workflow_name, workflow_version);
```

- [ ] Write `1773106060_create_agent_run_metrics.down.sql`:

```sql
DROP TABLE agent_run_metrics;
```

- [ ] Run `ginkgo ./atc/db/migration/` — expect pass (the migration bindata/list machinery picks up the new files; if a `migrations.go` list needs regenerating, follow the header comment in `atc/db/migration/migrations/migrations.go`).
- [ ] Commit: `git add atc/db/migration && git commit -m "feat(db): agent_run_metrics table (migration 1773106060, contracts s1.8)"`

---

### Task 7: `agent/api/metrics` — Store interface, submission parsing, memory store, HTTP handler

Follows the `agent/api/reviews` idiom exactly (types + `Store` + `memory_store.go` + `handler.go`, `r.FormValue(":ticket_id")` for rata params). The `Store` interface carries `Upsert(rm) error` (the convenience form consumed by harvest-step and delivery-outcomes), `UpsertReturningInserted(rm) (bool, error)` (a first-insert discriminator consumed by Task 13's ledger-dedup gate, finding F3), and — added by the 2026-07-09 final-review amendment, finding F24 — `InsertIfAbsent(rm) (bool, error)` (the DEGRADED-ingestion write: insert-only, `ON CONFLICT DO NOTHING`, so a re-ingestion that read no flight data can never clobber a real row). All additive, so existing consumers and the `metricsfakes.FakeStore` `Upsert*` methods are unchanged.

**Files:**
- Create: `agent/api/metrics/types.go`
- Create: `agent/api/metrics/memory_store.go`
- Create: `agent/api/metrics/handler.go`
- Test: `agent/api/metrics/types_test.go`, `agent/api/metrics/handler_test.go`

**Steps:**

- [ ] Write `agent/api/metrics/types_test.go` (plain Go test, like `agent/api/reviews/types_test.go`):

```go
package metrics_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/metrics"
)

func TestParseSubmissionRequiresBuildAndPlan(t *testing.T) {
	_, err := metrics.ParseSubmission([]byte(`{"plan_id":"a","step_name":"s","status":"ok"}`))
	if err == nil || err.Error() != "build_id is required" {
		t.Fatalf("expected build_id error, got %v", err)
	}
	_, err = metrics.ParseSubmission([]byte(`{"build_id":1,"step_name":"s","status":"ok"}`))
	if err == nil || err.Error() != "plan_id is required" {
		t.Fatalf("expected plan_id error, got %v", err)
	}
	_, err = metrics.ParseSubmission([]byte(`{"build_id":1,"plan_id":"a","step_name":"s","status":"nope"}`))
	if err == nil {
		t.Fatal("expected status taxonomy error")
	}
	rm, err := metrics.ParseSubmission([]byte(`{"build_id":1,"plan_id":"a","step_name":"s","status":"error","summary":"crashed"}`))
	if err != nil || rm.Status != "error" {
		t.Fatalf("expected valid submission, got %v %v", rm, err)
	}
}
```

- [ ] Run `go test ./agent/api/metrics/` — expect compile failure.
- [ ] Write `agent/api/metrics/types.go`:

```go
package metrics

import (
	"encoding/json"
	"fmt"

	schema "github.com/concourse/concourse/agent/schema"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// Store is the persistence interface for agent run metrics
// (shared-contracts §1.8/§2.4). Implemented by atc/db.AgentRunMetricsFactory.
//
//counterfeiter:generate . Store
type Store interface {
	// Upsert inserts the row, replacing any existing row with the same
	// (BuildID, PlanID) key. Ingestion is idempotent across step retries
	// and web-restart resumes.
	Upsert(rm *schema.RunMetrics) error
	// UpsertReturningInserted is Upsert with a first-insert discriminator:
	// inserted is true only when the row was newly INSERTed (the
	// ON CONFLICT (build_id, plan_id) clause did NOT fire), and false on a
	// resume/retry that updated an existing row. The append-only
	// agent_cost_ledger has no dedup key of its own (§1.4), so callers that
	// append a ledger row per ingestion (Task 13) gate that append on this
	// flag to charge each (build_id, plan_id) exactly once across web-restart
	// resumes — reusing the metrics table's unique key as the single dedup
	// authority. Upsert is Upsert(rm) = { _, err := UpsertReturningInserted(rm); return err }.
	UpsertReturningInserted(rm *schema.RunMetrics) (inserted bool, err error)
	// InsertIfAbsent writes the row only when no (BuildID, PlanID) row exists
	// yet (ON CONFLICT (build_id, plan_id) DO NOTHING) and reports whether it
	// inserted. This is the DEGRADED-ingestion write (finding F24, 2026-07-09):
	// when a re-ingestion read no flight data — a web-restart resume whose
	// in-memory volume locator is gone, or a reaped-pod rerun — its zero-cost
	// status=error row must never clobber a real row written by an earlier,
	// successful ingestion. inserted=false means a row already existed and
	// nothing was written.
	InsertIfAbsent(rm *schema.RunMetrics) (inserted bool, err error)
	// GetByBuild returns rows for a build, oldest-first.
	GetByBuild(buildID int) ([]schema.RunMetrics, error)
	// ListByTicket returns rows for a ticket, oldest-first.
	ListByTicket(ticketID int) ([]schema.RunMetrics, error)
}

// ParseSubmission validates a POST /api/v1/agent/metrics body.
func ParseSubmission(body []byte) (*schema.RunMetrics, error) {
	var rm schema.RunMetrics
	if err := json.Unmarshal(body, &rm); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if rm.BuildID <= 0 {
		return nil, fmt.Errorf("build_id is required")
	}
	if rm.PlanID == "" {
		return nil, fmt.Errorf("plan_id is required")
	}
	if rm.StepName == "" {
		return nil, fmt.Errorf("step_name is required")
	}
	switch rm.Status {
	case schema.RunStatusOK, schema.RunStatusFailed, schema.RunStatusError:
	default:
		return nil, fmt.Errorf("status must be one of ok|failed|error")
	}
	return &rm, nil
}
```

- [ ] Run `go test ./agent/api/metrics/` — expect pass.
- [ ] Write `agent/api/metrics/handler_test.go` (httptest against the handler with the memory store; mirror `agent/api/reviews/handler_test.go` style):

```go
package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/metrics"
)

func TestSubmitAndListByTicket(t *testing.T) {
	store := metrics.NewMemoryStore()
	h := metrics.NewHandler(store)

	body := `{"build_id":9,"plan_id":"abc","step_name":"implement","status":"ok","ticket_id":7,"cost_usd":0.5}`
	req := httptest.NewRequest("POST", "/api/v1/agent/metrics", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.SubmitMetrics(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// idempotent upsert on (build_id, plan_id)
	rec = httptest.NewRecorder()
	h.SubmitMetrics(rec, httptest.NewRequest("POST", "/api/v1/agent/metrics", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 on re-submit, got %d", rec.Code)
	}

	listReq := httptest.NewRequest("GET", "/api/v1/agent/tickets/7/metrics", nil)
	listReq.Form = map[string][]string{":ticket_id": {"7"}}
	rec = httptest.NewRecorder()
	h.ListByTicket(rec, listReq)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"plan_id":"abc"`) {
		t.Fatalf("expected one row, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSubmitRejectsBadPayload(t *testing.T) {
	h := metrics.NewHandler(metrics.NewMemoryStore())
	rec := httptest.NewRecorder()
	h.SubmitMetrics(rec, httptest.NewRequest("POST", "/api/v1/agent/metrics", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
```

- [ ] Run `go test ./agent/api/metrics/` — expect compile failure.
- [ ] Write `agent/api/metrics/memory_store.go`:

```go
package metrics

import (
	"sort"
	"sync"

	schema "github.com/concourse/concourse/agent/schema"
)

// MemoryStore is an in-memory Store for tests.
type MemoryStore struct {
	mu   sync.Mutex
	rows map[[2]any]schema.RunMetrics // key: {buildID, planID}
	seq  int
	ord  map[[2]any]int
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: map[[2]any]schema.RunMetrics{}, ord: map[[2]any]int{}}
}

func (s *MemoryStore) Upsert(rm *schema.RunMetrics) error {
	_, err := s.UpsertReturningInserted(rm)
	return err
}

func (s *MemoryStore) UpsertReturningInserted(rm *schema.RunMetrics) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := [2]any{rm.BuildID, rm.PlanID}
	_, existed := s.rows[key]
	if !existed {
		s.seq++
		s.ord[key] = s.seq
	}
	s.rows[key] = *rm
	return !existed, nil
}

func (s *MemoryStore) InsertIfAbsent(rm *schema.RunMetrics) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := [2]any{rm.BuildID, rm.PlanID}
	if _, existed := s.rows[key]; existed {
		return false, nil // never clobber an existing row (F24)
	}
	s.seq++
	s.ord[key] = s.seq
	s.rows[key] = *rm
	return true, nil
}

func (s *MemoryStore) GetByBuild(buildID int) ([]schema.RunMetrics, error) {
	return s.list(func(rm schema.RunMetrics) bool { return rm.BuildID == buildID })
}

func (s *MemoryStore) ListByTicket(ticketID int) ([]schema.RunMetrics, error) {
	return s.list(func(rm schema.RunMetrics) bool {
		return rm.TicketID != nil && *rm.TicketID == ticketID
	})
}

func (s *MemoryStore) list(match func(schema.RunMetrics) bool) ([]schema.RunMetrics, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type entry struct {
		ord int
		rm  schema.RunMetrics
	}
	var entries []entry
	for key, rm := range s.rows {
		if match(rm) {
			entries = append(entries, entry{s.ord[key], rm})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ord < entries[j].ord })
	out := make([]schema.RunMetrics, len(entries))
	for i, e := range entries {
		out[i] = e.rm
	}
	return out, nil
}
```

- [ ] Write `agent/api/metrics/handler.go`:

```go
package metrics

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	schema "github.com/concourse/concourse/agent/schema"
)

// Handler serves the agent run-metrics routes. Auth is enforced by the
// wrappa layer (principal(metrics:write) for submit; authorized viewer for
// list) — the handler trusts the request.
type Handler struct {
	store Store
}

func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// SubmitMetrics handles POST /api/v1/agent/metrics.
func (h *Handler) SubmitMetrics(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	rm, err := ParseSubmission(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.store.Upsert(rm); err != nil {
		http.Error(w, "failed to store metrics", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// ListByTicket handles GET /api/v1/agent/tickets/:ticket_id/metrics.
func (h *Handler) ListByTicket(w http.ResponseWriter, r *http.Request) {
	ticketID, err := strconv.Atoi(r.FormValue(":ticket_id"))
	if err != nil || ticketID <= 0 {
		http.Error(w, "invalid ticket_id", http.StatusBadRequest)
		return
	}
	rows, err := h.store.ListByTicket(ticketID)
	if err != nil {
		http.Error(w, "failed to list metrics", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []schema.RunMetrics{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}
```

- [ ] Run `go test ./agent/api/metrics/` — expect pass.
- [ ] Generate the counterfeiter fake (used by exec tests in Tasks 12–13): `go generate ./agent/api/metrics/...` — produces `agent/api/metrics/metricsfakes/fake_store.go`. Run `go build ./...`.
- [ ] Commit: `git add agent/api/metrics && git commit -m "feat(agent-api): run-metrics store interface, parser, and HTTP handler"`

---

### Task 8: `atc/db` AgentRunMetricsFactory

Implements `metrics.Store` with squirrel, upsert `ON CONFLICT (build_id, plan_id)`, epoch-seconds scan — the `agent_reviews_factory.go` recipe (atc/db/agent_reviews_factory.go:26 Upsert, :61 column scan). The upsert also returns a first-insert discriminator (`UpsertReturningInserted`, via `RETURNING (xmax = 0)`) that Task 13 gates the append-only ledger write on (finding F3). Amended 2026-07-09 (finding F24): the factory also implements `InsertIfAbsent` — same insert, `ON CONFLICT (build_id, plan_id) DO NOTHING` — the degraded-ingestion write that can never overwrite an existing row.

**Files:**
- Create: `atc/db/agent_run_metrics_factory.go`
- Test: `atc/db/agent_run_metrics_factory_test.go`

**Steps:**

- [ ] Write `atc/db/agent_run_metrics_factory_test.go` (Ginkgo, in the existing `db_test` suite which migrates the template DB — mirror `agent_reviews_factory_test.go` setup):

```go
package db_test

import (
	"encoding/json"

	"github.com/concourse/concourse/agent/api/metrics"
	schema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentRunMetricsFactory", func() {
	var factory metrics.Store

	BeforeEach(func() {
		factory = db.NewAgentRunMetricsFactory(dbConn)
	})

	It("upserts on (build_id, plan_id) and lists by ticket", func() {
		ticket := 7
		rm := &schema.RunMetrics{
			TicketID: &ticket, BuildID: 42, PlanID: "5f2a", StepName: "implement",
			Status: "ok", Summary: "first", Model: "claude-sonnet-4-5",
			Usage: schema.Usage{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 3, CacheCreationInputTokens: 2},
			Turns: 9, WallTimeSeconds: 61, CostUSD: 0.42,
			Results:        json.RawMessage(`{"schema_version":"1.0","status":"pass","confidence":1,"summary":"x","artifacts":[]}`),
			EventsArtifact: "vol-1",
			EventCounts:    map[string]int{"tool.call": 4},
		}
		inserted, err := factory.UpsertReturningInserted(rm)
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeTrue()) // first insert on (build_id, plan_id) 42/5f2a

		rm.Summary = "second"
		rm.CostUSD = 0.43
		inserted, err = factory.UpsertReturningInserted(rm)
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeFalse()) // ON CONFLICT fired — resume/retry, not a new row

		rows, err := factory.ListByTicket(7)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Summary).To(Equal("second"))
		Expect(rows[0].CostUSD).To(BeNumerically("~", 0.43, 1e-9))
		Expect(rows[0].Usage.InputTokens).To(Equal(int64(100)))
		Expect(rows[0].EventCounts).To(HaveKeyWithValue("tool.call", 4))
		Expect(rows[0].CreatedAt).To(BeNumerically(">", 0))

		byBuild, err := factory.GetByBuild(42)
		Expect(err).ToNot(HaveOccurred())
		Expect(byBuild).To(HaveLen(1))
	})

	It("stores NULL ticket/workflow tags for pure-CI steps", func() {
		Expect(factory.Upsert(&schema.RunMetrics{
			BuildID: 43, PlanID: "aa", StepName: "s", Status: "error", Summary: "crashed",
		})).To(Succeed())
		rows, err := factory.GetByBuild(43)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows[0].TicketID).To(BeNil())
		Expect(rows[0].WorkflowVersion).To(BeNil())
	})

	// --- finding F24: degraded re-ingestion must never clobber a real row ---
	It("InsertIfAbsent inserts when absent and preserves an existing row", func() {
		good := &schema.RunMetrics{
			BuildID: 44, PlanID: "bb", StepName: "implement",
			Status: "ok", Summary: "real ingestion", CostUSD: 0.42, Turns: 9,
		}
		inserted, err := factory.UpsertReturningInserted(good)
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeTrue())

		// Degraded re-ingestion (resume with no flight data readable): the
		// zero-cost error row hits ON CONFLICT DO NOTHING and writes nothing.
		degraded := &schema.RunMetrics{
			BuildID: 44, PlanID: "bb", StepName: "implement",
			Status: "error", Summary: "flight recorder output missing",
		}
		inserted, err = factory.InsertIfAbsent(degraded)
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeFalse())

		rows, err := factory.GetByBuild(44)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].Status).To(Equal("ok")) // web-1's real row survives
		Expect(rows[0].CostUSD).To(BeNumerically("~", 0.42, 1e-9))

		// And it still inserts when no row exists (crashed agent, first run).
		inserted, err = factory.InsertIfAbsent(&schema.RunMetrics{
			BuildID: 45, PlanID: "cc", StepName: "s", Status: "error", Summary: "crashed",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(inserted).To(BeTrue())
	})
})
```

- [ ] Run `ginkgo --focus="AgentRunMetricsFactory" ./atc/db/` — expect compile failure.
- [ ] Write `atc/db/agent_run_metrics_factory.go`:

```go
package db

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/concourse/concourse/agent/api/metrics"
	schema "github.com/concourse/concourse/agent/schema"
)

//counterfeiter:generate . AgentRunMetricsFactory
type AgentRunMetricsFactory interface {
	metrics.Store
}

func NewAgentRunMetricsFactory(conn DbConn) AgentRunMetricsFactory {
	return &agentRunMetricsFactory{conn: conn}
}

type agentRunMetricsFactory struct {
	conn DbConn
}

func (f *agentRunMetricsFactory) Upsert(rm *schema.RunMetrics) error {
	_, err := f.UpsertReturningInserted(rm)
	return err
}

// UpsertReturningInserted performs the ON CONFLICT (build_id, plan_id) upsert and
// reports whether the row was newly inserted. The discriminator is Postgres's
// system column `xmax`: on a fresh INSERT the tuple has no prior version so
// `xmax = 0`; when the ON CONFLICT DO UPDATE fires, the update replaces an
// existing tuple and `xmax <> 0`. `RETURNING (xmax = 0) AS inserted` therefore
// distinguishes a first insert from a resume/retry update in the same statement,
// with no extra round-trip. Callers gate the append-only ledger write on this
// flag so a web-restart resume never double-charges (§1.4 has no ledger dedup key).
func (f *agentRunMetricsFactory) UpsertReturningInserted(rm *schema.RunMetrics) (bool, error) {
	var eventCounts, results any
	if rm.EventCounts != nil {
		b, err := json.Marshal(rm.EventCounts)
		if err != nil {
			return false, err
		}
		eventCounts = b
	}
	if len(rm.Results) > 0 {
		results = []byte(rm.Results)
	}

	var inserted bool
	err := psql.Insert("agent_run_metrics").
		Columns(
			"ticket_id", "pipeline_run_id", "build_id", "plan_id", "step_name",
			"workflow_name", "workflow_version", "workflow_hash",
			"status", "summary", "model",
			"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
			"turns", "wall_time_seconds", "cost_usd",
			"results", "events_artifact", "event_counts",
		).
		Values(
			rm.TicketID, rm.PipelineRunID, rm.BuildID, rm.PlanID, rm.StepName,
			rm.WorkflowName, rm.WorkflowVersion, rm.WorkflowHash,
			rm.Status, rm.Summary, rm.Model,
			rm.Usage.InputTokens, rm.Usage.OutputTokens, rm.Usage.CacheReadInputTokens, rm.Usage.CacheCreationInputTokens,
			rm.Turns, rm.WallTimeSeconds, rm.CostUSD,
			results, rm.EventsArtifact, eventCounts,
		).
		Suffix(`ON CONFLICT (build_id, plan_id) DO UPDATE SET
			ticket_id = EXCLUDED.ticket_id,
			pipeline_run_id = EXCLUDED.pipeline_run_id,
			step_name = EXCLUDED.step_name,
			workflow_name = EXCLUDED.workflow_name,
			workflow_version = EXCLUDED.workflow_version,
			workflow_hash = EXCLUDED.workflow_hash,
			status = EXCLUDED.status,
			summary = EXCLUDED.summary,
			model = EXCLUDED.model,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			turns = EXCLUDED.turns,
			wall_time_seconds = EXCLUDED.wall_time_seconds,
			cost_usd = EXCLUDED.cost_usd,
			results = EXCLUDED.results,
			events_artifact = EXCLUDED.events_artifact,
			event_counts = EXCLUDED.event_counts
		RETURNING (xmax = 0) AS inserted`).
		RunWith(f.conn).
		QueryRow().
		Scan(&inserted)
	return inserted, err
}

// InsertIfAbsent is the degraded-ingestion write (finding F24): identical
// column/value construction to UpsertReturningInserted, but the conflict
// clause is DO NOTHING, so an existing (build_id, plan_id) row — a real row
// from an earlier, successful ingestion — is never overwritten. With
// DO NOTHING, RETURNING yields no row when the conflict fires, which scans
// as sql.ErrNoRows ⇒ inserted=false, nothing written.
func (f *agentRunMetricsFactory) InsertIfAbsent(rm *schema.RunMetrics) (bool, error) {
	var eventCounts, results any
	if rm.EventCounts != nil {
		b, err := json.Marshal(rm.EventCounts)
		if err != nil {
			return false, err
		}
		eventCounts = b
	}
	if len(rm.Results) > 0 {
		results = []byte(rm.Results)
	}

	var inserted bool
	err := psql.Insert("agent_run_metrics").
		Columns(
			"ticket_id", "pipeline_run_id", "build_id", "plan_id", "step_name",
			"workflow_name", "workflow_version", "workflow_hash",
			"status", "summary", "model",
			"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens",
			"turns", "wall_time_seconds", "cost_usd",
			"results", "events_artifact", "event_counts",
		).
		Values(
			rm.TicketID, rm.PipelineRunID, rm.BuildID, rm.PlanID, rm.StepName,
			rm.WorkflowName, rm.WorkflowVersion, rm.WorkflowHash,
			rm.Status, rm.Summary, rm.Model,
			rm.Usage.InputTokens, rm.Usage.OutputTokens, rm.Usage.CacheReadInputTokens, rm.Usage.CacheCreationInputTokens,
			rm.Turns, rm.WallTimeSeconds, rm.CostUSD,
			results, rm.EventsArtifact, eventCounts,
		).
		Suffix(`ON CONFLICT (build_id, plan_id) DO NOTHING RETURNING true`).
		RunWith(f.conn).
		QueryRow().
		Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // conflict fired — existing row preserved
	}
	return inserted, err
}

const runMetricsColumns = `m.ticket_id, m.pipeline_run_id, m.build_id, m.plan_id, m.step_name,
	m.workflow_name, m.workflow_version, m.workflow_hash,
	m.status, m.summary, m.model,
	m.input_tokens, m.output_tokens, m.cache_read_tokens, m.cache_creation_tokens,
	m.turns, m.wall_time_seconds, m.cost_usd,
	m.results, m.events_artifact, m.event_counts,
	EXTRACT(EPOCH FROM m.created_at)::bigint`

func (f *agentRunMetricsFactory) GetByBuild(buildID int) ([]schema.RunMetrics, error) {
	rows, err := f.conn.Query(
		`SELECT `+runMetricsColumns+` FROM agent_run_metrics m
		 WHERE m.build_id = $1 ORDER BY m.created_at ASC, m.id ASC`, buildID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRunMetricsRows(rows)
}

func (f *agentRunMetricsFactory) ListByTicket(ticketID int) ([]schema.RunMetrics, error) {
	rows, err := f.conn.Query(
		`SELECT `+runMetricsColumns+` FROM agent_run_metrics m
		 WHERE m.ticket_id = $1 ORDER BY m.created_at ASC, m.id ASC`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRunMetricsRows(rows)
}

func scanRunMetricsRows(rows *sql.Rows) ([]schema.RunMetrics, error) {
	results := []schema.RunMetrics{}
	for rows.Next() {
		var rm schema.RunMetrics
		var resultsPayload, eventCounts []byte
		err := rows.Scan(
			&rm.TicketID, &rm.PipelineRunID, &rm.BuildID, &rm.PlanID, &rm.StepName,
			&rm.WorkflowName, &rm.WorkflowVersion, &rm.WorkflowHash,
			&rm.Status, &rm.Summary, &rm.Model,
			&rm.Usage.InputTokens, &rm.Usage.OutputTokens, &rm.Usage.CacheReadInputTokens, &rm.Usage.CacheCreationInputTokens,
			&rm.Turns, &rm.WallTimeSeconds, &rm.CostUSD,
			&resultsPayload, &rm.EventsArtifact, &eventCounts,
			&rm.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		if len(resultsPayload) > 0 {
			rm.Results = json.RawMessage(resultsPayload)
		}
		if len(eventCounts) > 0 {
			if err := json.Unmarshal(eventCounts, &rm.EventCounts); err != nil {
				return nil, err
			}
		}
		results = append(results, rm)
	}
	return results, rows.Err()
}
```

  Note: `cost_usd NUMERIC` scans into `float64` — match `agent_reviews`' handling (its `score` column scans into float64 directly and works in production); if the driver rejects NUMERIC→float64 here, scan into `[]byte` + `strconv.ParseFloat` instead.
- [ ] Run `ginkgo --focus="AgentRunMetricsFactory" ./atc/db/` — expect pass (full suite: `ginkgo ./atc/db/`, ~90s).
- [ ] Commit: `git add atc/db && git commit -m "feat(db): AgentRunMetricsFactory implementing metrics.Store (with first-insert discriminator for ledger dedup, F3)"`

---

### Task 9: Ingest + list routes, auth tiers, handler wiring

Adds the two §4.2 rows owned by agent-step. Consumes agent-identity's landed `auth.CheckAgentPrincipalHandler` and `CheckAgentAuthorizationHandler` (find their exact case-group placement in `atc/wrappa/api_auth_wrappa.go` as landed by wave 1 — the five agent feedback routes will already sit on the agent-authorization handler per decision 21).

**Files:**
- Modify: `atc/routes.go:254` (agent routes block — add two route entries + name constants where the other `Agent` names are declared)
- Modify: `atc/wrappa/api_auth_wrappa.go:112` region (add `SubmitAgentRunMetrics` to the principal tier with scope `metrics:write`; add `ListAgentRunMetrics` to the agent-authorized case group landed by agent-identity)
- Modify: `atc/api/accessor/roles.go:108` region (`atc.ListAgentRunMetrics: ViewerRole`)
- Modify: `atc/api/handler.go:93` (new `metricsStore metricsapi.Store` param), `:122` (construct handler), `:269` (map entries)
- Modify: `atc/atccmd/command.go:2297` region (pass `db.NewAgentRunMetricsFactory(dbConn)`)
- Test: `atc/wrappa/api_auth_wrappa_test.go` (add the two routes to its exhaustive expectations table), `atc/api/*` suite compiles

**Steps:**

- [ ] In `atc/routes.go`, next to the existing agent route constants, add `SubmitAgentRunMetrics` and `ListAgentRunMetrics` name constants, and in the routes table (after line 258's findings route) add:

```go
	{Path: "/api/v1/agent/metrics", Method: "POST", Name: SubmitAgentRunMetrics},
	{Path: "/api/v1/agent/tickets/:ticket_id/metrics", Method: "GET", Name: ListAgentRunMetrics},
```

- [ ] Run `ginkgo ./atc/wrappa/` — expect failure: the exhaustive auth switch panics `you missed a spot: "SubmitAgentRunMetrics"` (this is the failing test for the wrappa change).
- [ ] In `atc/wrappa/api_auth_wrappa.go`:
  - Add `case atc.SubmitAgentRunMetrics:` to the principal-scope tier landed by agent-identity, with scope `principals.ScopeMetricsWrite` (per contracts §4.1). The landed pattern is a call on the `wrappa` struct's factory field (agent-identity added the field `checkAgentPrincipalHandlerFactory auth.CheckAgentPrincipalHandlerFactory` to the wrappa struct + constructor). Add:

```go
		case atc.SubmitAgentRunMetrics:
			newHandler = wrappa.checkAgentPrincipalHandlerFactory.HandlerFor(
				handler, rejector, principals.ScopeMetricsWrite)
```

    Import `"github.com/concourse/concourse/agent/api/principals"`. Grep `checkAgentPrincipalHandlerFactory` first and copy the existing `SubmitAgentReview` case as landed by agent-identity (which uses `HandlerForWithLegacyBypass` for its dual-accept window — metrics has no legacy token, so use the strict `HandlerFor`). Contracts §4.1 uses the shorthand `auth.CheckAgentPrincipalHandler(...)`, but the frozen Go surface is the factory interface — do not call a bare free function.
  - Add `atc.ListAgentRunMetrics` to the team-less agent-authorized case group (the one agent-identity moved `GetAgentFeedback` etc. onto — grep `CheckAgentAuthorizationHandler`).
- [ ] In `atc/api/accessor/roles.go` add to `DefaultRoles`:

```go
	atc.ListAgentRunMetrics: ViewerRole,
```

- [ ] In `atc/api/handler.go`: import `metricsapi "github.com/concourse/concourse/agent/api/metrics"`; add param `metricsStore metricsapi.Store,` after `reviewsStore reviewsapi.Store,` (line 91); construct `metricsServer := metricsapi.NewHandler(metricsStore)` next to `reviewsServer` (line 123); add to the handlers map (after line 277):

```go
		atc.SubmitAgentRunMetrics: http.HandlerFunc(metricsServer.SubmitMetrics),
		atc.ListAgentRunMetrics:   http.HandlerFunc(metricsServer.ListByTicket),
```

- [ ] In `atc/atccmd/command.go` `constructAPIHandler` call site (line ~2297), pass `db.NewAgentRunMetricsFactory(dbConn),` in the position matching the new `NewHandler` signature (immediately after `db.NewAgentReviewsFactory(dbConn),`). Note: agent-identity's wave-1 work may have already reshaped this argument list — append relative to whatever is landed, keeping parameter order consistent with `api.NewHandler`.
- [ ] Add both routes to `atc/wrappa/api_auth_wrappa_test.go`'s expectations (copy the assertion style used for `SubmitAgentReview` / the feedback routes as landed by agent-identity).
- [ ] Run `ginkgo ./atc/wrappa/ ./atc/api/accessor/ && go build ./atc/...` — expect pass.
- [ ] Run `ginkgo -r ./atc/api/` — expect pass (api suite constructs NewHandler; fix any fixture call sites the signature change broke — `atc/api/api_suite_test.go` builds the handler once).
- [ ] Commit: `git add atc/routes.go atc/wrappa atc/api atc/atccmd && git commit -m "feat(api): principal-authed agent run-metrics ingest route + viewer list route (contracts s4.2)"`

---

### Task 10: `atc.AgentStep` config, `atc.AgentPlan`, and all visitor implementations

One coherent compile unit: adding `VisitAgent` to `StepVisitor` forces `StepRecursor`, `StepValidator`, and `planVisitor` updates in the same change. Config shape is §2.8 verbatim.

**Files:**
- Modify: `atc/steps.go` (:194 StepVisitor — add `VisitAgent(*AgentStep) error`; :224 StepPrecedence — insert `agent` detector before the `run` entry at :253; add the `AgentStep` struct after `RunStep` :385)
- Modify: `atc/step_recursor.go` (:20 region — add `OnAgent` hook + `VisitAgent`)
- Modify: `atc/step_validator.go` (:233 region — add `VisitAgent` with full validation)
- Modify: `atc/plan.go` (:11 — add `Agent *AgentPlan` union field; :379 region — add `AgentPlan` struct)
- Modify: `atc/builds/planner.go` (:83 region — add `VisitAgent`)
- Test: `atc/steps_test.go` (parse cases), `atc/configvalidate/validate_test.go` (validator cases), `atc/builds/planner_test.go` (plan mapping case)

**Steps:**

- [ ] Add parse test cases to the `factoryTests` table in `atc/steps_test.go` (after the run-step cases at :261):

```go
	{
		Title: "agent step",
		ConfigYAML: `
			agent: write-spec
			prompt: |
			  Read the ticket, explore the repo, submit a spec.
			model: claude-sonnet-4-5
			max_turns: 80
			budget_slice_usd: 2.5
			output_schema: repo/schemas/spec.json
			sidecars:
			- name: platform
			  image: ghcr.io/tdmtrader/mcp-platform:v1.0.0
			inputs: [repo]
			outputs: [workspace]
			env: {BASE_REF: main}
			timeout: 1h
		`,
		StepConfig: &atc.AgentStep{
			Name:           "write-spec",
			Prompt:         "Read the ticket, explore the repo, submit a spec.\n",
			Model:          "claude-sonnet-4-5",
			MaxTurns:       80,
			BudgetSliceUSD: 2.5,
			OutputSchema:   "repo/schemas/spec.json",
			Sidecars: []atc.SidecarSource{
				{Config: &atc.SidecarConfig{Name: "platform", Image: "ghcr.io/tdmtrader/mcp-platform:v1.0.0"}},
			},
			Inputs:  []string{"repo"},
			Outputs: []string{"workspace"},
			Env:     map[string]string{"BASE_REF": "main"},
			Timeout: "1h",
		},
	},
	{
		Title: "agent step with prompt file",
		ConfigYAML: `
			agent: implement
			prompt_file: repo/prompts/implement.md
		`,
		StepConfig: &atc.AgentStep{
			Name:       "implement",
			PromptFile: "repo/prompts/implement.md",
		},
	},
```

- [ ] Run `go test ./atc/ -count=1` — expect compile failure (`atc.AgentStep` undefined). (The testify suites in package `atc_test` run under the standard go test binary; `ginkgo ./atc/` runs them too.)
- [ ] In `atc/steps.go`:
  - Add to `StepVisitor` (after `VisitRun` at :194): `VisitAgent(*AgentStep) error`
  - Insert into `StepPrecedence` immediately before the `run` detector (:253):

```go
	{
		Key: "agent",
		New: func() StepConfig { return &AgentStep{} },
	},
```

  - Add after `RunStep.Visit` (:385):

```go
// AgentStep runs the claude CLI in a jetbridge pod with declared MCP
// sidecars (shared-contracts §2.8). The renderer resolves everything from
// the workflow definition into literal values here; the step implementation
// never reads workflow tables.
type AgentStep struct {
	Name           string            `json:"agent"`
	Prompt         string            `json:"prompt,omitempty"`
	PromptFile     string            `json:"prompt_file,omitempty"`
	Model          string            `json:"model,omitempty"`
	MaxTurns       int               `json:"max_turns,omitempty"`
	BudgetSliceUSD float64           `json:"budget_slice_usd,omitempty"`
	OutputSchema   string            `json:"output_schema,omitempty"`
	Sidecars       []SidecarSource   `json:"sidecars,omitempty"`
	Inputs         []string          `json:"inputs,omitempty"`
	Outputs        []string          `json:"outputs,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Timeout        string            `json:"timeout,omitempty"`
	Limits         *ContainerLimits  `json:"container_limits,omitempty"`
	Requests       *ContainerLimits  `json:"container_requests,omitempty"`
}

func (step *AgentStep) Visit(v StepVisitor) error {
	return v.VisitAgent(step)
}
```

- [ ] In `atc/step_recursor.go` add (after `OnRun` :21 and `VisitRun` :57):

```go
	// OnAgent will be invoked for any *AgentStep present in the StepConfig.
	OnAgent func(*AgentStep) error
```

```go
// VisitAgent calls the OnAgent hook if configured.
func (recursor StepRecursor) VisitAgent(step *AgentStep) error {
	if recursor.OnAgent != nil {
		return recursor.OnAgent(step)
	}

	return nil
}
```

- [ ] In `atc/step_validator.go` add after `VisitRun` (:254):

```go
func (validator *StepValidator) VisitAgent(step *AgentStep) error {
	validator.pushContextf(".agent(%s)", step.Name)
	defer validator.popContext()

	warning, err := ValidateIdentifier(step.Name, validator.context...)
	if err != nil {
		validator.recordError(err.Error())
	}
	if warning != nil {
		validator.recordWarning(*warning)
	}

	if step.Prompt == "" && step.PromptFile == "" {
		validator.recordError("must specify either `prompt:` or `prompt_file:`")
	}

	if step.Prompt != "" && step.PromptFile != "" {
		validator.recordError("must specify one of `prompt:` or `prompt_file:`, not both")
	}

	if step.BudgetSliceUSD < 0 {
		validator.recordError("budget_slice_usd must not be negative")
	}

	if step.MaxTurns < 0 {
		validator.recordError("max_turns must not be negative")
	}

	for _, output := range step.Outputs {
		if output == "flight" {
			validator.recordError("output name 'flight' is reserved for the flight recorder")
		}
	}

	for i, src := range step.Sidecars {
		if src.Config == nil {
			continue // file references are validated at runtime
		}
		validator.pushContextf(".sidecars[%d]", i)
		if err := src.Config.Validate(); err != nil {
			validator.recordError(err.Error())
		}
		if IsReservedContainerName(src.Config.Name) {
			validator.recordErrorf("reserved container name %q", src.Config.Name)
		}
		validator.popContext()
	}

	return nil
}
```

- [ ] In `atc/plan.go`: add `Agent *AgentPlan \`json:"agent,omitempty"\`` to `Plan` after `Run` (:11), and add after `RunPlan` (:406):

```go
type AgentPlan struct {
	Name           string            `json:"name"`
	Prompt         string            `json:"prompt,omitempty"`
	PromptFile     string            `json:"prompt_file,omitempty"`
	Model          string            `json:"model,omitempty"`
	MaxTurns       int               `json:"max_turns,omitempty"`
	BudgetSliceUSD float64           `json:"budget_slice_usd,omitempty"`
	OutputSchema   string            `json:"output_schema,omitempty"`
	Sidecars       []SidecarSource   `json:"sidecars,omitempty"`
	Inputs         []string          `json:"inputs,omitempty"`
	Outputs        []string          `json:"outputs,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Timeout        string            `json:"timeout,omitempty"`
	Limits         *ContainerLimits  `json:"container_limits,omitempty"`
	Requests       *ContainerLimits  `json:"container_requests,omitempty"`
}
```

- [ ] In `atc/builds/planner.go` add after `VisitRun` (:103):

```go
func (visitor *planVisitor) VisitAgent(step *atc.AgentStep) error {
	visitor.plan = visitor.planFactory.NewPlan(atc.AgentPlan{
		Name:           step.Name,
		Prompt:         step.Prompt,
		PromptFile:     step.PromptFile,
		Model:          step.Model,
		MaxTurns:       step.MaxTurns,
		BudgetSliceUSD: step.BudgetSliceUSD,
		OutputSchema:   step.OutputSchema,
		Sidecars:       step.Sidecars,
		Inputs:         step.Inputs,
		Outputs:        step.Outputs,
		Env:            step.Env,
		Timeout:        step.Timeout,
		Limits:         step.Limits,
		Requests:       step.Requests,
	})

	return nil
}
```

- [ ] Run `go build ./atc/...` — expect pass (only `steps.go`, `step_recursor.go`, `step_validator.go`, `builds/planner.go` implement `StepVisitor` — verified by grep).
- [ ] Run `go test ./atc/ -count=1` — parse cases pass.
- [ ] Add validator cases to `atc/configvalidate/validate_test.go` (mirror the sidecar contexts at :1172; the file validates a full `atc.Config` and asserts on `errorMessages` — adapt the `BeforeEach` scaffolding to the fixture idiom used there):

```go
	Context("when an agent step has no prompt", func() {
		BeforeEach(func() {
			job.PlanSequence = append(job.PlanSequence, atc.Step{
				Config: &atc.AgentStep{Name: "write-spec"},
			})
			config.Jobs = append(config.Jobs, job)
		})

		It("returns an error", func() {
			Expect(errorMessages).To(HaveLen(1))
			Expect(errorMessages[0]).To(ContainSubstring("must specify either `prompt:` or `prompt_file:`"))
		})
	})

	Context("when an agent step declares a reserved flight output", func() {
		BeforeEach(func() {
			job.PlanSequence = append(job.PlanSequence, atc.Step{
				Config: &atc.AgentStep{Name: "a", Prompt: "p", Outputs: []string{"flight"}},
			})
			config.Jobs = append(config.Jobs, job)
		})

		It("returns an error", func() {
			Expect(errorMessages).To(HaveLen(1))
			Expect(errorMessages[0]).To(ContainSubstring("reserved for the flight recorder"))
		})
	})
```

- [ ] Run `ginkgo ./atc/configvalidate/` — expect pass.
- [ ] Add a planner case to the `atc/builds/planner_test.go` `PlannerTest` table:

```go
	{
		Title: "agent step",
		Config: &atc.AgentStep{
			Name:           "write-spec",
			Prompt:         "do it",
			Model:          "claude-sonnet-4-5",
			MaxTurns:       80,
			BudgetSliceUSD: 2.5,
			Inputs:         []string{"repo"},
			Outputs:        []string{"workspace"},
			Env:            map[string]string{"AGENT_TICKET_ID": "7"},
		},
		PlanJSON: `{
			"id": "(unique)",
			"agent": {
				"name": "write-spec",
				"prompt": "do it",
				"model": "claude-sonnet-4-5",
				"max_turns": 80,
				"budget_slice_usd": 2.5,
				"inputs": ["repo"],
				"outputs": ["workspace"],
				"env": {"AGENT_TICKET_ID": "7"}
			}
		}`,
	},
```

- [ ] Run `go test ./atc/builds/ -count=1` — expect pass.
- [ ] Run `ginkgo ./atc/ ./atc/builds/ ./atc/configvalidate/` — expect pass.
- [ ] Commit: `git add atc/steps.go atc/steps_test.go atc/step_recursor.go atc/step_validator.go atc/plan.go atc/builds atc/configvalidate && git commit -m "feat(atc): agent step config, plan union, validator, and planner (contracts s2.8)"`

---

### Task 11: Extract shared sidecar helpers in `atc/exec`

`TaskStep.loadSidecars` (:640), the image-artifact/digest resolution (:299–338), and `buildProcessIO` (:623) are needed verbatim by the agent step. Extract package-level helpers and re-point the task step, with the existing task sidecar specs as the regression net.

**Files:**
- Create: `atc/exec/sidecars.go`
- Modify: `atc/exec/task_step.go:294` (call helpers), delete the method bodies at :623–707 that move
- Test: existing `atc/exec/task_step_test.go` sidecar specs (regression only — no new tests)

**Steps:**

- [ ] Create `atc/exec/sidecars.go` and move the logic:
  - `loadSidecarConfigs(ctx context.Context, logger lager.Logger, repo *build.Repository, streamer Streamer, sources []atc.SidecarSource) ([]atc.SidecarConfig, error)` — body of `TaskStep.loadSidecars` (task_step.go:640–707) with `step.streamer` → parameter and `step.plan.Sidecars` → parameter.
  - `resolveSidecarImages(ctx context.Context, logger lager.Logger, state RunState, resolver imageresolver.Resolver, sidecars []atc.SidecarConfig) error` — the artifact-ref resolution + digest-pinning loops (task_step.go:299–338), operating on the slice in place; `resolver` may be nil (skip pinning, keep the artifact-ref resolution which needs only `state`).
  - `sidecarProcessIO(delegate TaskDelegate, sidecars []atc.SidecarConfig) runtime.ProcessIO` — body of `buildProcessIO` (task_step.go:623–635).
- [ ] Re-point `TaskStep.run` (:294–343) and the `buildProcessIO` call site (:399) at the helpers; delete the moved methods.
- [ ] Run `ginkgo --focus="sidecar" ./atc/exec/` then `ginkgo ./atc/exec/` — expect pass (pure refactor).
- [ ] Commit: `git add atc/exec && git commit -m "refactor(exec): extract sidecar load/resolve/process-io helpers for reuse by agent step"`

---

### Task 11B: jetbridge runtime seams (added 2026-07-09 — final-review findings F15/F18/F20/F31-pause)

The wave-2 runtime-seams work package, frozen 2026-07-09 across the co-signed plans. Five plans (07, 08, 09, 11, 04) depend on these seams; they are built exactly once, HERE. **Hard prerequisite of Tasks 12, 14, and 18 — sequence this task before them.** No jetbridge edits are permitted in any plan other than this task and 09 Task 11 (SecretMounts only). Consumers: dev-mcp (04, CWD convention), harvest-step (09, applySecretRefs append + dev-sidecar WorkingDir), platform-mcp-hitl (08) and dispatch (11) via the checkpoint renderer's co-signed delta, shared-contracts (00, §8.1/§8.2/§8.5/§11 registry edits).

Four pieces:
- **F15** — `runtime.ContainerSpec` gains per-sidecar env + secret-ref maps (`SidecarEnv`, `SidecarSecretEnv`), applied by `buildSidecarContainers`. The public YAML surface is UNCHANGED: `atc.SidecarConfig`/`atc.SidecarEnvVar` (atc/sidecar.go:13–35) gain NO ValueFrom and no new fields — secret refs reach sidecars exclusively through the runtime-level maps, populated by the owning exec implementation (never from pipeline YAML).
- **F18** — `supervised()` extends to `Task || Agent`. Agent steps REQUIRE supervision: web-restart resume (`attachOrRun`) and the ask_human/checkpoint park protocol are built on the supervisor keeping the process alive across severed exec sessions (shared-contracts §3.2). `db.ContainerTypeAgent` lands here (moved from Task 14, which now consumes it).
- **F20** — `applySecretRefs` gains APPEND semantics for SecretEnv-only keys (returns the slice). Required in wave 2 by this plan's own `SidecarSecretEnv` keys (no literal counterparts); consumed in wave 3 by harvest's judge token. The empty-placeholder workaround (a fake literal added just to be replaced) is FORBIDDEN.
- **F31 (pause leg)** — the pause command loops its bounded sleep so parked pods survive past 24h (a bare `sleep 86400` kills the pod 24h after creation, breaking ask_human/checkpoint parks).

**Files:**
- Modify: `atc/runtime/types.go` (:185 — two fields after `Sidecars`)
- Modify: `atc/db/container_metadata.go` (:26–32 const block + :34–49 `ContainerTypeFromString`)
- Modify: `atc/worker/jetbridge/container.go` (:361–363 `pauseCommand`, :399 + :439 `buildPod` call sites, :510 `buildSidecarContainers`, :704–721 `applySecretRefs`)
- Modify: `atc/worker/jetbridge/process.go` (:731–735 `supervised`)
- Test: `atc/worker/jetbridge/container_test.go` (new per-sidecar env/secret + append specs on the one-sidecar scaffolding at :2790; pause-literal updates at :1517/:3186), `atc/worker/jetbridge/process_test.go` (supervised table), `atc/worker/jetbridge/integration_test.go` (:101 pause literal)

**Steps:**

- [ ] Write the failing fake-clientset specs in `atc/worker/jetbridge/container_test.go`, copying the one-sidecar context scaffolding at :2790 (adjust the pod-fetch/container-lookup helpers to that scaffolding's exact shape at execution time):

```go
	Context("per-sidecar env and secret refs (runtime seams, F15)", func() {
		BeforeEach(func() {
			containerSpec.Sidecars = []atc.SidecarConfig{
				{Name: "platform", Image: "mcp-platform:v1"},
				{Name: "dev", Image: "mcp-dev:v1"},
			}
			containerSpec.SidecarEnv = map[string][]string{
				"platform": {"ATC_EXTERNAL_URL=https://ci.example.com", "AGENT_TICKET_ID=7"},
			}
			containerSpec.SidecarSecretEnv = map[string]map[string]vars.SecretRef{
				"platform": {"AGENT_PRINCIPAL_TOKEN": {Name: "agent-run-42", Key: "principal-token"}},
			}
		})

		It("applies literals then env rows then secret refs to the named sidecar only", func() {
			pod := createdPod() // the :2790 scaffolding's fake-clientset pod fetch
			platform := containerByName(pod, "platform")
			Expect(platform.Env).To(ContainElements(
				corev1.EnvVar{Name: "ATC_EXTERNAL_URL", Value: "https://ci.example.com"},
				corev1.EnvVar{Name: "AGENT_TICKET_ID", Value: "7"},
			))
			Expect(platform.Env).To(ContainElement(corev1.EnvVar{
				Name: "AGENT_PRINCIPAL_TOKEN",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "agent-run-42"},
						Key:                  "principal-token",
					},
				},
			}))
			// the secret ref never appears as a literal Value
			for _, e := range platform.Env {
				if e.Name == "AGENT_PRINCIPAL_TOKEN" {
					Expect(e.Value).To(BeEmpty())
				}
			}
			// the dev sidecar and the main container carry NONE of these entries
			for _, c := range []corev1.Container{containerByName(pod, "dev"), containerByName(pod, mainContainerName)} {
				for _, e := range c.Env {
					Expect(e.Name).ToNot(Or(
						Equal("ATC_EXTERNAL_URL"), Equal("AGENT_TICKET_ID"), Equal("AGENT_PRINCIPAL_TOKEN")))
				}
			}
		})
	})

	Context("SecretEnv-only keys (runtime seams, F20)", func() {
		It("appends a secretKeyRef-only EnvVar when no literal exists", func() {
			containerSpec.Env = nil
			containerSpec.SecretEnv = map[string]vars.SecretRef{
				"CLAUDE_CODE_OAUTH_TOKEN": {Name: "agent-platform-credential", Key: "anthropic-token"},
			}
			pod := createdPod()
			main := containerByName(pod, mainContainerName)
			var matches []corev1.EnvVar
			for _, e := range main.Env {
				if e.Name == "CLAUDE_CODE_OAUTH_TOKEN" {
					matches = append(matches, e)
				}
			}
			Expect(matches).To(HaveLen(1)) // exactly one — appended, not duplicated
			Expect(matches[0].Value).To(BeEmpty())
			Expect(matches[0].ValueFrom.SecretKeyRef.Name).To(Equal("agent-platform-credential"))
			Expect(matches[0].ValueFrom.SecretKeyRef.Key).To(Equal("anthropic-token"))
		})
	})
```

- [ ] Write the supervised-gate table spec in `atc/worker/jetbridge/process_test.go`: a table across container metadata types asserting the pod's exec command (recorded by the fake executor) is the **supervisor command** for `db.ContainerTypeTask` and `db.ContainerTypeAgent` with nil Stdin, and the **raw command** for `db.ContainerTypeGet` and for ANY type when `ProcessIO.Stdin` is non-nil:

```go
	DescribeTable("supervised gates the in-pod supervisor on container type and stdin (F18)",
		func(cType db.ContainerType, withStdin bool, wantSupervisor bool) {
			// build the container via the suite scaffolding with
			// db.ContainerMetadata{Type: cType}; run a process with
			// ProcessIO{Stdin: stdinFor(withStdin)}; then assert on the
			// command the fake executor received:
			//   wantSupervisor ⇒ command[0] ends in the supervisor invocation
			//     (supervisorCommand, supervisor.go:63)
			//   !wantSupervisor ⇒ command equals the raw ProcessSpec command
		},
		Entry("task, no stdin → supervised", db.ContainerTypeTask, false, true),
		Entry("agent, no stdin → supervised", db.ContainerTypeAgent, false, true),
		Entry("get, no stdin → raw command", db.ContainerTypeGet, false, false),
		Entry("task with stdin → raw command", db.ContainerTypeTask, true, false),
		Entry("agent with stdin → raw command", db.ContainerTypeAgent, true, false),
	)
```

- [ ] Update the three existing pause-command literal assertions to the new loop form — `container_test.go:1517`, `container_test.go:3186`, `integration_test.go:101` each become:

```go
	Expect(pod.Spec.Containers[0].Command).To(Equal([]string{"sh", "-c", "trap 'exit 0' TERM; while :; do sleep 86400 & wait $!; done"}))
```

  (Test commands inside live/behavioral fixtures that use the old one-shot idiom as their OWN payload — e.g. `live_sidecar_test.go` — are fine as-is; only the pod-lifetime pause constant is normative.)
- [ ] Run `ginkgo ./atc/worker/jetbridge/` — expect compile failure (`SidecarEnv`/`SidecarSecretEnv`/`ContainerTypeAgent` undefined), then spec failures.
- [ ] **Piece 1a** — `atc/runtime/types.go`: add two fields to `ContainerSpec`, immediately after `Sidecars []atc.SidecarConfig` (:185):

```go
	// SidecarEnv maps a sidecar name (matching Sidecars[i].Name) to extra
	// environment variables in "NAME=VALUE" form (same convention as Env),
	// injected into that sidecar's container only. Populated by the owning
	// exec implementation (agent/harvest/checkpoint steps) per
	// shared-contracts §8.1 — never from public pipeline YAML.
	SidecarEnv map[string][]string

	// SidecarSecretEnv maps a sidecar name to env-var-name → K8s Secret
	// coordinates, emitted as ValueFrom.SecretKeyRef in that sidecar's
	// container spec (same secretKeyRef-only rule as SecretEnv, §8.2).
	SidecarSecretEnv map[string]map[string]vars.SecretRef
```

- [ ] **Piece 2a** — `atc/db/container_metadata.go`: add `ContainerTypeAgent ContainerType = "agent"` to the const block (:26–32) and a `case "agent":` arm in `ContainerTypeFromString` (:34–49), mirroring `run`. (Moved into this task from Task 14, which now consumes it.)
- [ ] **Piece 1b** — `atc/worker/jetbridge/container.go`: `buildSidecarContainers` (:510) gains the two maps as parameters:

```go
func buildSidecarContainers(
	sidecars []atc.SidecarConfig,
	mainMounts []corev1.VolumeMount,
	defaultDir string,
	sidecarEnv map[string][]string,
	sidecarSecretEnv map[string]map[string]vars.SecretRef,
) []corev1.Container
```

  Call site in `buildPod` (:439) becomes:

```go
	containers = append(containers, buildSidecarContainers(
		c.containerSpec.Sidecars, volumeMounts, dir,
		c.containerSpec.SidecarEnv, c.containerSpec.SidecarSecretEnv)...)
```

  Inside the per-sidecar loop, the existing `for _, e := range sc.Env` block (:542–544) is replaced by:

```go
	var env []corev1.EnvVar
	for _, e := range sc.Env {
		env = append(env, corev1.EnvVar{Name: e.Name, Value: e.Value})
	}
	env = append(env, envVars(sidecarEnv[sc.Name])...)
	env = applySecretRefs(env, sidecarSecretEnv[sc.Name])
	c.Env = env
```

  Ordering is deterministic: YAML-declared literals first (declaration order), then `SidecarEnv` (caller order), then SecretEnv-only appends (sorted by name — Piece 3). Nil map lookups yield nil and are no-ops, so all existing callers (task sidecars) build byte-identical pods.
- [ ] **Piece 3** — `atc/worker/jetbridge/container.go` (:704–721): replace `applySecretRefs` in full (signature now RETURNS the slice; `sort` is already imported):

```go
// applySecretRefs converts literal Value entries to ValueFrom.SecretKeyRef
// for env vars with a matching entry in secretEnv, and APPENDS a
// secretKeyRef-only EnvVar (in sorted name order, for deterministic pod
// specs) for secretEnv keys that have no literal entry. This keeps secret
// values out of the pod spec either way. Returns the (possibly grown) slice.
func applySecretRefs(envList []corev1.EnvVar, secretEnv map[string]vars.SecretRef) []corev1.EnvVar {
	if len(secretEnv) == 0 {
		return envList
	}
	refFor := func(ref vars.SecretRef) *corev1.EnvVarSource {
		return &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
				Key:                  ref.Key,
			},
		}
	}
	seen := map[string]bool{}
	for i := range envList {
		ref, ok := secretEnv[envList[i].Name]
		if !ok {
			continue
		}
		seen[envList[i].Name] = true
		envList[i].Value = ""
		envList[i].ValueFrom = refFor(ref)
	}
	var missing []string
	for name := range secretEnv {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		envList = append(envList, corev1.EnvVar{Name: name, ValueFrom: refFor(secretEnv[name])})
	}
	return envList
}
```

  Call site in `buildPod` (:399) becomes `env = applySecretRefs(env, c.containerSpec.SecretEnv)`. Behavior-preserving for every existing caller: keys with matching literals are replaced exactly as before; the append leg only fires for SecretEnv-only keys, which no current caller produces (`BuildSecretEnv` derives SecretEnv from vars already present as literals).
- [ ] **Piece 2b** — `atc/worker/jetbridge/process.go` (:731–735), exact replacement (the Stdin==nil guard is kept verbatim; no other process.go change; `supervisorCommand` at supervisor.go:63 is untouched):

```go
// supervised reports whether this process should run under the in-pod task
// supervisor. Task and agent steps qualify: get/put/check use the
// stdin/stdout resource protocol, which the supervisor's log-file
// indirection would break. Agent steps REQUIRE supervision — web-restart
// resume (attachOrRun) and the ask_human/checkpoint park protocol are
// built on the supervisor keeping the process alive across severed exec
// sessions (shared-contracts §3.2).
func (p *execProcess) supervised() bool {
	if p.container == nil || p.processIO.Stdin != nil {
		return false
	}
	t := p.container.metadata.Type
	return t == db.ContainerTypeTask || t == db.ContainerTypeAgent
}
```

- [ ] **Piece 6** — `atc/worker/jetbridge/container.go` (:361–363), exact replacement:

```go
// pauseCommand is the shell command used by pause pods. It idles forever
// (a looped bounded sleep — busybox sh has no infinite sleep) and exits
// cleanly on SIGTERM so the pod can be stopped. The loop matters: a bare
// `sleep 86400` kills the pod 24h after creation, which breaks parked
// agent steps (ask_human/checkpoint park can exceed 24h).
const pauseCommand = "trap 'exit 0' TERM; while :; do sleep 86400 & wait $!; done"
```

  (`wait $!` — not bare `wait` — keeps the TERM trap responsive inside the loop.)
- [ ] Run `ginkgo ./atc/worker/jetbridge/ && ginkgo ./atc/runtime/ && ginkgo ./atc/db/ && go build ./atc/...` — expect pass.
- [ ] Commit: `git add atc/runtime atc/db/container_metadata.go atc/worker/jetbridge && git commit -m "feat(runtime): jetbridge runtime seams — per-sidecar env/secret refs, agent supervision, secret-ref append, pause loop"`

---

### Task 12: `exec.AgentStep` — container spec, env contract, budget slice, process

The core execution path, modeled line-by-line on `TaskStep.run`. Reuses the `TaskDelegate` interface (it already has `EmitSidecarPlans`/`SidecarWriter`; `SetTaskConfig` is simply not called) via `TaskDelegateFactory` — both defined in `atc/exec/task_step.go:60–86`, produced by the engine's `DelegateFactory.TaskDelegate` (atc/engine/delegate_factory.go:65). No engine changes in this task; the step is constructed directly in tests. Flight-recorder ingestion is added in Task 13 — this task's `Run` does not yet write metrics rows.

**Task 11B is a hard prerequisite** (amended 2026-07-09, runtime-seams findings F15/F21): step 7 below populates `ContainerSpec.SidecarEnv`/`SidecarSecretEnv` — the §8.1 "set by the owning exec implementation" assertion made real — and normalizes each MCP sidecar's unset `WorkingDir` to the workspace artifact's mount path (§8.5 CWD convention).

> **Amended 2026-07-10 (PARK-V2 seam delta §D, decision 32).** Step 4's `budgetChecker.StepSlice` call is now load-bearing for continuations too: `StepSlice(ticketID, sliceUSD)` is a **resolution** (min of slice and ticket remaining), NOT a reservation, and it MUST run at the start of **every** execution of the step — a continuation build re-resolves naturally and, because the park-exit partial spend was already ledgered (Task 26), the re-resolved slice is automatically tighter. Never cache the resolved slice across executions and never skip the call on a resume. Task 26 adds the continuation consult (`agent_run_step_state` lookup, replay/resume/cold) and the exit-86 distinguished end around this task's `run`; implement this task as written.

**Files:**
- Create: `atc/exec/agent_step.go`
- Test: `atc/exec/agent_step_test.go`

**Steps:**

- [ ] Write the first Ginkgo specs in `atc/exec/agent_step_test.go`. Copy the fixture scaffolding from `atc/exec/task_step_test.go` (:38 `fakePool *execfakes.FakePool`, :144–180 `chosenWorker *runtimetest.Worker` / `chosenContainer *runtimetest.WorkerContainer` with `runtimetest.NewContainer().WithProcess(...)` + `runtimetest.ProcessStub`, `execfakes.FakeTaskDelegate` wired through a delegate-factory fake, `exec.NewRunState` as that file constructs it), plus `execfakes.FakeStreamer` and `budgetfakes.FakeChecker`. The load-bearing specs:

```go
var _ = Describe("AgentStep", func() {
	// fixture: plan := atc.AgentPlan{
	//   Name: "write-spec", Prompt: "do it", Model: "m1", MaxTurns: 3,
	//   BudgetSliceUSD: 2.5, Outputs: []string{"workspace"},
	//   Env: map[string]string{"AGENT_TICKET_ID": "7", "BASE_REF": "main"},
	//   Sidecars: []atc.SidecarSource{{Config: &atc.SidecarConfig{Name: "platform", Image: "img:v1"}}},
	// }
	// step := exec.NewAgentStep(planID, plan, atc.ContainerLimits{}, atc.ContainerLimits{},
	//   stepMetadata, containerMetadata, fakePool, fakeStreamer, fakeDelegateFactory,
	//   0, "registry.home/agent-runner:v1", exec.WithAgentBudgetChecker(fakeChecker))

	It("errors clearly when no agent image is configured", func() {
		step := exec.NewAgentStep(planID, atc.AgentPlan{Name: "a", Prompt: "p"},
			atc.ContainerLimits{}, atc.ContainerLimits{}, stepMetadata, containerMetadata,
			fakePool, fakeStreamer, fakeDelegateFactory, 0, "")
		_, err := step.Run(ctx, state)
		Expect(err).To(MatchError(ContainSubstring("--agent-step-image")))
	})

	It("builds the container spec per the s8.1 env contract", func() {
		_, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.ImageSpec.ImageURL).To(Equal("registry.home/agent-runner:v1"))
		Expect(spec.Env).To(ContainElements(
			"AGENT_STEP_NAME=write-spec",
			"AGENT_PROMPT=do it",
			"AGENT_MODEL=m1",
			"AGENT_MAX_TURNS=3",
			"AGENT_TICKET_ID=7",
			"BASE_REF=main",
			"PLATFORM_MCP_URL=http://127.0.0.1:7781/mcp",
		))
		Expect(spec.Env).To(ContainElement(HavePrefix("AGENT_FLIGHT_DIR=")))
		Expect(spec.Outputs).To(HaveKey("workspace"))
		Expect(spec.Outputs).To(HaveKey("flight"))
		Expect(spec.Sidecars).To(HaveLen(1))
	})

	It("runs agent-runner as the well-known agent process", func() {
		step.Run(ctx, state)
		// assert on chosenContainer's recorded process spec (runtimetest):
		// Path "agent-runner", ID "agent", TTY {Columns: 500, Rows: 500}
	})

	It("resolves the step slice through the budget checker", func() {
		fakeChecker.StepSliceReturns(budget.Remaining{LimitUSD: 2.5, SpentUSD: 1.25, RemainingUSD: 1.25}, nil)
		step.Run(ctx, state)
		ticketID, slice := fakeChecker.StepSliceArgsForCall(0)
		Expect(ticketID).To(Equal(7))
		Expect(slice).To(Equal(2.5))
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.Env).To(ContainElement("AGENT_BUDGET_SLICE_USD=1.25"))
	})

	It("fails without starting when the slice is exhausted", func() {
		fakeChecker.StepSliceReturns(budget.Remaining{Exhausted: true}, nil)
		ok, err := step.Run(ctx, state)
		Expect(err).ToNot(HaveOccurred())
		Expect(ok).To(BeFalse())
		Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero())
	})

	It("fails on missing declared inputs", func() {
		// plan.Inputs = ["repo"] with an empty artifact repository
		_, err := step.Run(ctx, state)
		Expect(err).To(BeAssignableToTypeOf(exec.MissingInputsError{}))
	})
})
```

- [ ] Run `ginkgo --focus="AgentStep" ./atc/exec/` — expect compile failure.
- [ ] Write `atc/exec/agent_step.go`:

```go
package exec

// imports: context, errors, fmt, sort, strconv, strings, time,
// code.cloudfoundry.org/lager/v3 + lagerctx, atc, atc/creds, atc/db,
// atc/exec/build, atc/imageresolver, atc/metric, atc/runtime, tracing,
// go.opentelemetry.io/otel/trace,
// github.com/concourse/concourse/agent/api/metrics,
// github.com/concourse/concourse/agent/budget

const agentProcessID = "agent"

// mcpSidecarPorts maps well-known sidecar names to their fixed localhost
// ports (shared-contracts §8.1).
var mcpSidecarPorts = map[string]int{"dev": 7780, "platform": 7781, "gateway": 7782}

type AgentStepOption func(*AgentStep)

func WithAgentImageResolver(r imageresolver.Resolver) AgentStepOption {
	return func(s *AgentStep) { s.imageResolver = r }
}

func WithAgentMetricsStore(m metrics.Store) AgentStepOption {
	return func(s *AgentStep) { s.metricsStore = m }
}

func WithAgentBudgetChecker(c budget.Checker) AgentStepOption {
	return func(s *AgentStep) { s.budgetChecker = c }
}

// AgentStep runs the claude CLI (via the agent-runner entrypoint) in a
// jetbridge pod with declared MCP sidecars, then ingests the flight
// recorder server-side (shared-contracts §2.8, §5, §8.1).
type AgentStep struct {
	planID            atc.PlanID
	plan              atc.AgentPlan
	defaultLimits     atc.ContainerLimits
	defaultRequests   atc.ContainerLimits
	metadata          StepMetadata
	containerMetadata db.ContainerMetadata
	workerPool        Pool
	streamer          Streamer
	delegateFactory   TaskDelegateFactory
	defaultTimeout    time.Duration
	agentImage        string
	imageResolver     imageresolver.Resolver
	metricsStore      metrics.Store
	budgetChecker     budget.Checker
}

func NewAgentStep(
	planID atc.PlanID,
	plan atc.AgentPlan,
	defaultLimits atc.ContainerLimits,
	defaultRequests atc.ContainerLimits,
	metadata StepMetadata,
	containerMetadata db.ContainerMetadata,
	workerPool Pool,
	streamer Streamer,
	delegateFactory TaskDelegateFactory,
	defaultTimeout time.Duration,
	agentImage string,
	opts ...AgentStepOption,
) Step {
	s := &AgentStep{
		planID: planID, plan: plan,
		defaultLimits: defaultLimits, defaultRequests: defaultRequests,
		metadata: metadata, containerMetadata: containerMetadata,
		workerPool: workerPool, streamer: streamer,
		delegateFactory: delegateFactory, defaultTimeout: defaultTimeout,
		agentImage: agentImage,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (step *AgentStep) Run(ctx context.Context, state RunState) (bool, error) {
	start := time.Now()
	delegate := step.delegateFactory.TaskDelegate(state)
	ctx, span := delegate.StartSpan(ctx, "agent", tracing.Attrs{"name": step.plan.Name})

	ok, err := step.run(ctx, state, delegate)
	tracing.End(span, err)
	metric.RecordStepDuration(ctx, "agent", step.plan.Name, time.Since(start))

	return ok, err
}
```

  `run` implements, in order (each block is a direct adaptation of the cited `task_step.go` lines):
  1. Logger session `"agent-step"` (`lagerctx.FromContext` + `tracing.LoggerWithSpan`, task_step.go:175–180); `delegate.Initializing(logger)`.
  2. Guard: `if step.agentImage == "" { return false, errors.New("agent step requires the web node to be started with --agent-step-image") }`.
  3. **Env interpolation:** for each `step.plan.Env` key (sorted with `sort.Strings` for determinism), `value, err := creds.NewString(state, raw).Evaluate()` (`atc/creds/string.go:10`); collect into `resolvedEnv map[string]string`. Interpolation failure → return the error.
  4. **Budget slice:** `slice := step.plan.BudgetSliceUSD`. If `step.budgetChecker != nil && slice > 0` and `resolvedEnv["AGENT_TICKET_ID"]` parses to an int > 0: `remaining, err := step.budgetChecker.StepSlice(ticketID, slice)`; on err, log `"failed-to-resolve-budget-slice"` and keep the configured slice; if `remaining.Exhausted`: `delegate.Errored(logger, "budget slice exhausted before start")`, `delegate.Finished(logger, ExitStatus(1))`, return `(false, nil)` — no worker is selected, nothing runs; else `slice = remaining.RemainingUSD`.
  5. **Env assembly:** `env := step.metadata.TaskEnv()` (step_metadata.go:100) + `k+"="+v` for resolvedEnv + `AGENT_STEP_NAME=<plan.Name>`, `AGENT_MODEL` (when set), `AGENT_MAX_TURNS` (when > 0, `strconv.Itoa`), `AGENT_OUTPUT_SCHEMA` (when set), `AGENT_PROMPT=<plan.Prompt>` or `AGENT_PROMPT_FILE=<plan.PromptFile>`, `AGENT_FLIGHT_DIR=<workdir>/flight`, and when slice > 0 `AGENT_BUDGET_SLICE_USD=` + `strconv.FormatFloat(slice, 'f', 2, 64)`.
  6. **ContainerSpec** (task_step.go:513 pattern): `TeamID`/`TeamName`/`JobID` from metadata, `StepName: step.plan.Name`, `ImageSpec: runtime.ImageSpec{ImageURL: step.agentImage}`, `Env: env`, `Type: step.containerMetadata.Type`, `Dir: step.containerMetadata.WorkingDirectory`. Inputs: for each `plan.Inputs` name, `state.ArtifactRepository().ArtifactFor(build.ArtifactName(name))`; missing → collect and return `MissingInputsError{missing}` (exec/task_step.go:30); `DestinationPath: artifactPath(workdir, name, "")`. Outputs: `runtime.OutputPaths` mapping each `plan.Outputs` name PLUS the implicit `"flight"` to `ensureTrailingSlash(artifactPath(workdir, name, ""))`. Limits/Requests: merge `plan.Limits`/`plan.Requests` over `step.defaultLimits`/`step.defaultRequests` (the nil-field merge at task_step.go:241–261, applied to the plan fields instead of a task config). `SecretEnv: BuildSecretEnv(atc.TaskEnv(resolvedEnv), state)` (task_step.go:575) — this is how `CLAUDE_CODE_OAUTH_TOKEN` from a K8s-secret var source becomes a `secretKeyRef` per §8.2.
  7. **Sidecars:** `sidecars, err := loadSidecarConfigs(ctx, logger, state.ArtifactRepository(), step.streamer, step.plan.Sidecars)`; `resolveSidecarImages(ctx, logger, state, step.imageResolver, sidecars)`; when non-empty `delegate.EmitSidecarPlans(logger, sidecars)` (Task 11 helpers). Then for each sidecar name found in `mcpSidecarPorts`, append `strings.ToUpper(name)+"_MCP_URL=http://127.0.0.1:"+strconv.Itoa(port)+"/mcp"` to `containerSpec.Env` (after loading, so file-sourced sidecars count too).

     **Amended 2026-07-09 (runtime-seams F15/F21, Piece 1d + 4b — Task 11B prerequisite):** before assigning `containerSpec.Sidecars = sidecars`, populate the per-sidecar seam maps per §8.1 and normalize the workspace `WorkingDir`, in-place on the loaded slice:

```go
	// §8.1 sidecar rows (F15): common + identity rows for every MCP sidecar.
	// Identity rows are empty for pure-CI steps (no ticket/run env).
	common := []string{
		"ATC_EXTERNAL_URL=" + step.metadata.ExternalURL,
		"BUILD_ID=" + strconv.Itoa(step.metadata.BuildID),
	}
	if v := resolvedEnv["AGENT_TICKET_ID"]; v != "" {
		common = append(common, "AGENT_TICKET_ID="+v)
	}
	if v := resolvedEnv["AGENT_PIPELINE_RUN_ID"]; v != "" {
		common = append(common, "AGENT_PIPELINE_RUN_ID="+v)
	}

	// Sidecar secret refs derive from the deterministic §8.2 secret name.
	// No pipeline-run id ⇒ no sidecar secret env (pure-CI agent steps get
	// sidecars without platform credentials, per §8.1).
	runID, _ := strconv.Atoi(resolvedEnv["AGENT_PIPELINE_RUN_ID"])
	secretName := "agent-run-" + strconv.Itoa(runID)

	// §8.5 CWD convention (F21): sidecar images never hardcode /workspace.
	// When the plan carries a `workspace` artifact, the owning exec points
	// each unset MCP-sidecar WorkingDir at its mount path; otherwise leave
	// unset (jetbridge falls back to the main container's Dir).
	wsPath := ""
	for _, n := range append(append([]string{}, step.plan.Inputs...), step.plan.Outputs...) {
		if n == "workspace" {
			wsPath = artifactPath(step.containerMetadata.WorkingDirectory, "workspace", "")
		}
	}

	for i := range sidecars {
		name := sidecars[i].Name
		if _, ok := mcpSidecarPorts[name]; !ok {
			continue
		}

		rows := append([]string{}, common...)
		switch name {
		case "platform":
			for _, k := range []string{"PLATFORM_MCP_ASK_TIMEOUT_POLICY", "PLATFORM_MCP_ASK_TIMEOUT_SECONDS"} {
				if v := resolvedEnv[k]; v != "" {
					rows = append(rows, k+"="+v)
				}
			}
			if runID > 0 {
				setSidecarSecretRef(&containerSpec, name, "AGENT_PRINCIPAL_TOKEN",
					vars.SecretRef{Name: secretName, Key: "principal-token"})
			}
		case "gateway":
			if slice > 0 {
				rows = append(rows, "AGENT_BUDGET_SLICE_USD="+strconv.FormatFloat(slice, 'f', 2, 64))
			}
			if runID > 0 {
				setSidecarSecretRef(&containerSpec, name, "AGENT_PRINCIPAL_TOKEN",
					vars.SecretRef{Name: secretName, Key: "principal-token"})
				setSidecarSecretRef(&containerSpec, name, "CLAUDE_CODE_OAUTH_TOKEN",
					vars.SecretRef{Name: secretName, Key: "anthropic-token"})
			}
			// case "dev": common+identity only
		}
		if containerSpec.SidecarEnv == nil {
			containerSpec.SidecarEnv = map[string][]string{}
		}
		containerSpec.SidecarEnv[name] = rows

		if wsPath != "" && sidecars[i].WorkingDir == "" {
			sidecars[i].WorkingDir = wsPath
		}
	}

	containerSpec.Sidecars = sidecars
```

     with the package-level helper (also in `agent_step.go`; import `"github.com/concourse/concourse/vars"`):

```go
func setSidecarSecretRef(spec *runtime.ContainerSpec, sidecar, envName string, ref vars.SecretRef) {
	if spec.SidecarSecretEnv == nil {
		spec.SidecarSecretEnv = map[string]map[string]vars.SecretRef{}
	}
	if spec.SidecarSecretEnv[sidecar] == nil {
		spec.SidecarSecretEnv[sidecar] = map[string]vars.SecretRef{}
	}
	spec.SidecarSecretEnv[sidecar][envName] = ref
}
```

     `vars.SecretRef` is `{Namespace, Name, Key string}` (vars/tracker.go:15) — `Namespace` stays empty (pod namespace). The main container's `CLAUDE_CODE_OAUTH_TOKEN` path is unchanged (`SecretEnv: BuildSecretEnv(...)`, step 6).
  8. **Placement + timeout** (task_step.go:345–378): `tracing.Inject(ctx, &containerSpec)`; `owner := db.NewBuildStepContainerOwner(step.metadata.BuildID, step.planID, step.metadata.TeamID)`; `delegate.BeforeSelectWorker`; `step.workerPool.FindOrSelectWorker(ctx, owner, containerSpec, worker.Spec{TeamID: step.metadata.TeamID})`; `MaybeTimeout(ctx, step.plan.Timeout, step.defaultTimeout)` + defer cancel; `delegate.SelectedWorker`; `worker.FindOrCreateContainer(ctx, owner, step.containerMetadata, containerSpec, delegate)`.
  9. **Process** (task_step.go:379–405): `delegate.Starting(logger)`; `process, err := attachOrRun(ctx, container, runtime.ProcessSpec{ID: agentProcessID, Path: "agent-runner", Dir: step.containerMetadata.WorkingDirectory, TTY: &runtime.TTYSpec{WindowSize: runtime.WindowSize{Columns: 500, Rows: 500}}}, sidecarProcessIO(delegate, containerSpec.Sidecars))` — `attachOrRun` (task_step.go:431) is what makes the step resume across web restarts under the jetbridge supervisor; `result, runErr := process.Wait(ctx)`.
  10. **Outputs registration** (task_step.go:715 pattern, including the `worker.ArtifactFromVolume` DaemonSet wrap): for each name in `plan.Outputs` plus `"flight"`, match `volumeMounts` by cleaned path and `repository.RegisterArtifact(build.ArtifactName(name), artifact, false)`.
  11. **Exit handling** identical to task_step.go:416–428: `context.DeadlineExceeded` → `delegate.Errored(logger, TimeoutLogMessage)`, return `(false, nil)`; other runErr → return it; else `delegate.Finished(logger, ExitStatus(result.ExitStatus))`, return `(result.ExitStatus == 0, nil)`.
- [ ] Add the exec-level runtime-seams specs (amendment 2026-07-09, F15/F21) to `atc/exec/agent_step_test.go` — these assert what the step PUTS ON the ContainerSpec; the jetbridge side is covered by Task 11B:

```go
	It("populates per-sidecar env and secret refs for MCP sidecars (F15)", func() {
		// plan.Env: AGENT_TICKET_ID=7, AGENT_PIPELINE_RUN_ID=42;
		// plan.Sidecars: platform + gateway; plan.BudgetSliceUSD: 2.5
		step.Run(ctx, state)
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.SidecarEnv["platform"]).To(ContainElements(
			"ATC_EXTERNAL_URL="+stepMetadata.ExternalURL,
			"BUILD_ID="+strconv.Itoa(stepMetadata.BuildID),
			"AGENT_TICKET_ID=7",
			"AGENT_PIPELINE_RUN_ID=42",
		))
		Expect(spec.SidecarSecretEnv["platform"]).To(HaveKeyWithValue(
			"AGENT_PRINCIPAL_TOKEN", vars.SecretRef{Name: "agent-run-42", Key: "principal-token"}))
		Expect(spec.SidecarEnv["gateway"]).To(ContainElement(HavePrefix("AGENT_BUDGET_SLICE_USD=")))
		Expect(spec.SidecarSecretEnv["gateway"]).To(HaveKeyWithValue(
			"AGENT_PRINCIPAL_TOKEN", vars.SecretRef{Name: "agent-run-42", Key: "principal-token"}))
		Expect(spec.SidecarSecretEnv["gateway"]).To(HaveKeyWithValue(
			"CLAUDE_CODE_OAUTH_TOKEN", vars.SecretRef{Name: "agent-run-42", Key: "anthropic-token"}))
	})

	It("emits no sidecar secret env without a pipeline-run id (pure CI)", func() {
		// plan.Env carries no AGENT_PIPELINE_RUN_ID
		step.Run(ctx, state)
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.SidecarSecretEnv).To(BeEmpty())
		Expect(spec.SidecarEnv["platform"]).To(ContainElement(HavePrefix("ATC_EXTERNAL_URL=")))
	})

	It("sets each unset MCP sidecar WorkingDir to the workspace mount (F21)", func() {
		// plan.Outputs includes "workspace"; sidecar config leaves WorkingDir unset
		step.Run(ctx, state)
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.Sidecars[0].WorkingDir).To(HaveSuffix("/workspace")) // <hashed-workdir>/workspace
	})

	It("leaves sidecar WorkingDir unset without a workspace artifact (jetbridge default)", func() {
		// plan has no input/output named "workspace"
		step.Run(ctx, state)
		_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
		Expect(spec.Sidecars[0].WorkingDir).To(BeEmpty())
	})
```

- [ ] Run `ginkgo --focus="AgentStep" ./atc/exec/` — expect pass.
- [ ] Run `ginkgo ./atc/exec/` — full package green.
- [ ] Commit: `git add atc/exec && git commit -m "feat(exec): agent step execution — env contract, sidecars, budget slice, resumable process"`

---

### Task 13: `exec.AgentStep` — server-side flight-recorder ingestion

The ingestion-before-GC guarantee: ingestion runs synchronously inside `Run` before the step returns, reading from the just-registered `flight` output volume via `Streamer.StreamFile` (the DaemonSet fabric path — same seam `LoadVarStep` uses at load_var_step.go:138). Tolerant of crashed agents: missing/malformed files or a stream without `step.end` produce a `status=error` row; ingest failures never fail the step; a fire-and-forget ledger record lands cost in `agent_cost_ledger`.

> **Amended 2026-07-10 (PARK-V2 seam delta §B6/§F).** Task 26 extends `ingestFlightRecorder` additively: `rm.SessionID` is read from `results.json`, and a stream ending in `step.park` with no `step.end` is the ONE sanctioned exception to the missing-`step.end`-is-error rule — it ingests as status `parked` (partial spend still ledgered through the normal F3 `inserted` gate; a continuation build has a new `build_id`, so its row never collides with the park-exit row). Implement this task as written; the parked branch lands in Task 26.

Three resume/timeout invariants this task must hold (design-review findings F3/F4, 2026-07-09; final-review finding F24, 2026-07-09):
- **F3 — ledger charged exactly once.** The metrics row is idempotent under `ON CONFLICT (build_id, plan_id)`, but `agent_cost_ledger` is append-only with no dedup key (§1.4), so a web-restart resume (which re-runs the whole `Step.Run`: re-attach → `process.Wait` → re-register outputs → re-ingest) would double-append the cost row and inflate spend against both the per-ticket budget and the global daily cap. The ledger append is therefore gated on a first-insert discriminator returned by the metrics upsert (`Store.UpsertReturningInserted`, Task 7/8), reusing the `(build_id, plan_id)` key as the single dedup authority — no ledger schema change, and the gateway's own `source='gateway'` rows for the same build/plan are untouched.
- **F4 — timed-out steps still measured.** Ingestion is called on the `DeadlineExceeded` path too, but the step's `ctx` is the timeout-scoped context (already expired), so reading through it would fail both `StreamFile` calls and record a zero-cost `status=error` row with no ledger entry — losing measurement on exactly the costliest (runaway) steps. `ingestFlightRecorder` therefore detaches from the deadline (`context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)`) before the reads. *(Amended 2026-07-09, finding F23: the detach alone is necessary but not sufficient — on deadline/transport-severed execs jetbridge skipped `uploadOutputsToArtifactStore`, so the flight volume had no locator and `StreamFile` failed regardless of context. Task 13B fixes the jetbridge side; the live readability assertion lives in Task 18.)*
- **F24 — degraded re-ingestion never clobbers a real row.** On a restart-resume of an already-completed step, `Attach` → `exitedProcess` never re-records outputs and the in-memory volume locator is ephemeral (artifact_locator.go:16–17), so the re-ingestion reads NO flight data and would degrade to a `{status: error, cost 0}` row — which, through the unconditional all-columns `DO UPDATE`, would destroy web-1's real row (scorecards and outcomes read it). Fix: track whether any flight file was actually read; when nothing was read, write via `Store.InsertIfAbsent` (`ON CONFLICT DO NOTHING`, Task 7/8) instead of the upsert — the degraded row lands only when no row exists yet (genuinely crashed agent), and an existing good row survives untouched. Known, accepted consequence (see §11): a **reaped-pod rerun** (reaper.go:87–101 deletes the pod; a fresh `Run` re-executes the whole claude session) reads real flight data for its second execution but hits `inserted=false` on the upsert, so the second run's cost is intentionally never ledgered — the `(build_id, plan_id)` dedup key cannot distinguish re-ingest from re-execute, and under-counting beats double-charging; reaper changes are deferred.

**Files:**
- Modify: `atc/exec/agent_step.go` (add `ingestFlightRecorder` + call it from `run` after output registration)
- Test: `atc/exec/agent_step_test.go` (ingestion contexts)

**Steps:**

- [ ] Add ingestion specs to `atc/exec/agent_step_test.go`, with `fakeMetricsStore *metricsfakes.FakeStore` passed via `exec.WithAgentMetricsStore`. `fakeStreamer.StreamFileStub` returns fixture readers keyed on the requested path:
  - `results.json` → `{"schema_version":"1.0","status":"pass","confidence":1,"summary":"done","artifacts":[]}`
  - `events.ndjson` → four NDJSON lines: `step.start` (`{"step_name":"write-spec","build_id":1,"plan_id":"p"}`), `tool.call` (`{"tool":"run_tests"}`), `cost.record` (`{"source":"agent_step","provider":"anthropic","model":"m1","input_tokens":100,"output_tokens":50,"cache_read_tokens":1,"cache_creation_tokens":2,"turns":9,"cost_usd":0.42}`), `step.end` (`{"step_name":"write-spec","status":"ok","summary":"done","wall_time_seconds":61,"cost_usd":0.42,"turns":9}`).

  Ingestion now calls `Store.UpsertReturningInserted` (Task 7), not `Upsert`, so assert against the counterfeiter fake's `UpsertReturningInserted*` methods. In the `BeforeEach`, default the fake to a fresh insert: `fakeMetricsStore.UpsertReturningInsertedReturns(true, nil)` — the ledger append is gated on that flag (finding F3), so the first `Run` in each spec must see `inserted=true`.

```go
	Context("flight-recorder ingestion", func() {
		BeforeEach(func() {
			// first ingestion of a (build_id, plan_id) is always a fresh insert
			fakeMetricsStore.UpsertReturningInsertedReturns(true, nil)
		})

		It("upserts a RunMetrics row before Run returns", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(Equal(1))
			rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
			Expect(rm.Status).To(Equal("ok"))
			Expect(rm.BuildID).To(Equal(stepMetadata.BuildID))
			Expect(rm.PlanID).To(Equal(string(planID)))
			Expect(*rm.TicketID).To(Equal(7))
			Expect(rm.Usage.InputTokens).To(Equal(int64(100)))
			Expect(rm.CostUSD).To(BeNumerically("~", 0.42, 1e-9))
			Expect(rm.Turns).To(Equal(9))
			Expect(rm.WallTimeSeconds).To(Equal(61))
			Expect(rm.EventCounts).To(HaveKeyWithValue("tool.call", 1))
			Expect(rm.EventsArtifact).ToNot(BeEmpty())
		})

		It("records an error row when events.ndjson has no step.end", func() {
			// events fixture truncated after tool.call
			step.Run(ctx, state)
			rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
			Expect(rm.Status).To(Equal("error"))
			Expect(rm.EventCounts).To(HaveKeyWithValue("tool.call", 1)) // partial counts kept
		})

		It("records an error row when the flight files are missing entirely", func() {
			// fakeStreamer returns an error for both files ⇒ no flight data was
			// read ⇒ DEGRADED ingestion goes through InsertIfAbsent (F24), never
			// the clobbering upsert. First run, no existing row ⇒ inserted.
			fakeMetricsStore.InsertIfAbsentReturns(true, nil)
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue()) // exit status still drives step success
			Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(BeZero())
			rm := fakeMetricsStore.InsertIfAbsentArgsForCall(0)
			Expect(rm.Status).To(Equal("error"))
			Expect(rm.Summary).To(ContainSubstring("flight recorder"))
		})

		It("never fails the step when the metrics upsert errors", func() {
			fakeMetricsStore.UpsertReturningInsertedReturns(false, errors.New("db down"))
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			// upsert failure ⇒ inserted=false ⇒ no ledger append (cannot prove first-insert)
			Expect(fakeChecker.RecordCallCount()).To(Equal(0))
		})

		It("records a fire-and-forget ledger entry when cost was incurred", func() {
			step.Run(ctx, state)
			Expect(fakeChecker.RecordCallCount()).To(Equal(1))
		})

		It("maps abstain results to failed", func() {
			// results.json fixture status "abstain"
			step.Run(ctx, state)
			Expect(fakeMetricsStore.UpsertReturningInsertedArgsForCall(0).Status).To(Equal("failed"))
		})

		// --- findings F3 + F24: web-restart resume neither re-charges the ledger
		// nor clobbers the real metrics row ---
		It("appends the ledger once and never clobbers across two Run invocations (resume)", func() {
			// First Run: flight data reads fine, fresh insert → ledger append fires.
			fakeMetricsStore.UpsertReturningInsertedReturns(true, nil)
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			// Web restart: Step.Run re-executes, re-attaches, re-ingests — but the
			// in-memory volume locator is gone (artifact_locator.go is ephemeral)
			// and exitedProcess never re-records outputs, so BOTH StreamFile reads
			// fail. The degraded re-ingestion must go through InsertIfAbsent
			// (ON CONFLICT DO NOTHING) so its zero-cost error row cannot destroy
			// web-1's real row (F24) — and inserted=false skips the ledger (F3).
			fakeStreamer.StreamFileReturns(nil, errors.New("no locator for volume"))
			fakeMetricsStore.InsertIfAbsentReturns(false, nil) // row already exists
			_, err = step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(Equal(1)) // full ingest only on web-1
			Expect(fakeMetricsStore.InsertIfAbsentCallCount()).To(Equal(1))          // degraded path on web-2
			Expect(fakeChecker.RecordCallCount()).To(Equal(1))                       // ledger charged ONCE
		})

		// --- finding F4: timed-out step still records pre-timeout cost + ledger ---
		It("still ingests cost/tokens and records the ledger when the step times out", func() {
			// runtimetest.ProcessStub.Err is a plain string, so use the Call hook
			// to return the sentinel context.DeadlineExceeded from Wait (matching
			// what MaybeTimeout's cancelled ctx surfaces). The flight volume stays
			// fully populated — the runner flushed before the kill. ctx into Run is
			// the expired timeout context, so ingestFlightRecorder must detach from
			// it (WithoutCancel) or both StreamFile reads fail.
			//
			// Fixed 2026-07-09 (finding F37): runtimetest's WithProcess is
			// copy-on-write — it RETURNS a new container, so calling it here and
			// discarding the result installs nothing (and a bare
			// ProcessSpec{ID: "agent"} would never DeepEqual-match the step's full
			// spec anyway). Mutate the harness-registered process definition's
			// stub in place instead:
			chosenContainer.ProcessDefs[0].Stub.Call = func(
				context.Context, *runtimetest.Process,
			) (runtime.ProcessResult, error) {
				return runtime.ProcessResult{}, context.DeadlineExceeded
			}

			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeFalse()) // timeout ⇒ step not successful

			Expect(fakeMetricsStore.UpsertReturningInsertedCallCount()).To(Equal(1))
			rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
			Expect(rm.CostUSD).To(BeNumerically("~", 0.42, 1e-9)) // pre-timeout cost preserved
			Expect(rm.Turns).To(Equal(9))
			Expect(rm.Usage.InputTokens).To(Equal(int64(100)))
			Expect(rm.EventCounts).To(HaveKeyWithValue("tool.call", 1))
			Expect(fakeChecker.RecordCallCount()).To(Equal(1)) // ledger entry recorded despite timeout
		})
	})
```

  The F4 spec depends on the streamer fixture ignoring context cancellation — the fake `StreamFileStub` returns its reader regardless of the passed `ctx`, which is exactly the point: the production `Streamer` would fail on the expired `ctx`, and the fix is that `ingestFlightRecorder` never passes it one. If the existing test harness wires `fakeStreamer.StreamFileStub` to honor `ctx.Err()`, have it return the fixture reader when the context is live and an error otherwise — the detached `ingestCtx` keeps it live, so the assertion still holds and the pre-fix code (which threaded the expired `ctx`) fails it.

- [ ] Run `ginkgo --focus="ingestion" ./atc/exec/` — expect failure (undefined `UpsertReturningInserted*`/`InsertIfAbsent*` fake methods until Task 7's fake is regenerated; then the new F3/F4/F24 specs fail against pre-fix behavior).
- [ ] Implement in `atc/exec/agent_step.go` and call it from `run` immediately after output registration (step 10 of Task 12), on every path where the container ran — including the `DeadlineExceeded` branch — before the exit handling returns. Pass the same timeout-scoped `ctx` you have in scope: `ingestFlightRecorder` **internally detaches** it (`context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)`) so a timed-out step still ingests its pre-timeout cost/tokens rather than reading through an already-cancelled deadline (finding F4). Do NOT pre-detach at the call site — the detach lives inside `ingestFlightRecorder` so both callers (normal + timeout) get it for free.

```go
// ingestFlightRecorder reads flight/results.json and flight/events.ndjson
// from the registered flight output and upserts an agent_run_metrics row.
// It runs synchronously before Run returns — the build cannot complete (and
// artifact-fabric retention cannot reap the events) until ingestion is done.
// It is tolerant by design: any missing/partial/corrupt input degrades to a
// status=error row; its own failures are logged, never returned.
func (step *AgentStep) ingestFlightRecorder(
	ctx context.Context,
	logger lager.Logger,
	wkr runtime.Worker,
	volumeMounts []runtime.VolumeMount,
	resolvedEnv map[string]string,
	wallTime time.Duration,
) {
	if step.metricsStore == nil {
		return
	}

	// Detach from the step deadline. `ctx` is the timeout-scoped context from
	// MaybeTimeout (Task 12 step 8); on the DeadlineExceeded path it is already
	// cancelled, and every StreamFile call threads ctx down to
	// http.NewRequestWithContext, so both reads below would fail instantly and a
	// timed-out step — the costliest, most measurement-critical case — would
	// record a bare status=error row with zero cost/tokens/event_counts and no
	// ledger entry. WithoutCancel keeps the request-scoped values (tracing,
	// lager) while dropping the deadline; the fresh 30s bound keeps ingestion
	// (which blocks the build from completing) from hanging on a wedged fabric.
	ingestCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	rm := schema.RunMetrics{
		BuildID:         step.metadata.BuildID,
		PlanID:          string(step.planID),
		StepName:        step.plan.Name,
		WallTimeSeconds: int(wallTime.Seconds()),
	}
	if id, ok := envInt(resolvedEnv, "AGENT_TICKET_ID"); ok {
		rm.TicketID = &id
	}
	if id, ok := envInt(resolvedEnv, "AGENT_PIPELINE_RUN_ID"); ok {
		rm.PipelineRunID = &id
	}
	rm.WorkflowName = resolvedEnv["AGENT_WORKFLOW_NAME"]
	if v, ok := envInt(resolvedEnv, "AGENT_WORKFLOW_VERSION"); ok {
		rm.WorkflowVersion = &v
	}
	rm.WorkflowHash = resolvedEnv["AGENT_WORKFLOW_HASH"]

	flightPath := ensureTrailingSlash(artifactPath(step.containerMetadata.WorkingDirectory, "flight", ""))
	var flightArtifact runtime.Artifact
	for _, mount := range volumeMounts {
		if filepath.Clean(mount.MountPath) == filepath.Clean(flightPath) {
			flightArtifact = wkr.ArtifactFromVolume(mount.Volume)
			rm.EventsArtifact = mount.Volume.Handle()
		}
	}

	rm.Status = schema.RunStatusError
	rm.Summary = "flight recorder output missing"

	// flightRead tracks whether ANY flight data was actually read. When it
	// stays false this is a DEGRADED ingestion (missing mount, ephemeral
	// locator on a restart-resume, reaped pod) and the write below must go
	// through InsertIfAbsent so it can never clobber a real row (finding F24).
	flightRead := false

	if flightArtifact != nil {
		// results.json
		if rc, err := step.streamer.StreamFile(ingestCtx, flightArtifact, "results.json"); err == nil {
			flightRead = true
			raw, readErr := io.ReadAll(io.LimitReader(rc, 5<<20))
			rc.Close()
			var results schema.Results
			if readErr == nil && json.Unmarshal(raw, &results) == nil && results.Validate() == nil {
				status, abstained := schema.ThreeWayStatus(results.Status)
				rm.Status = status
				rm.Summary = results.Summary
				rm.Results = json.RawMessage(raw)
				if abstained {
					logger.Info("agent-abstained")
				}
			} else {
				rm.Summary = "results.json missing or malformed"
			}
		}

		// events.ndjson: counts + cost rollup + step.end detection
		if rc, err := step.streamer.StreamFile(ingestCtx, flightArtifact, "events.ndjson"); err == nil {
			flightRead = true
			counts := map[string]int{}
			sawStepEnd := false
			reader := schema.NewEventReader(rc)
			for {
				event, err := reader.Read()
				if err != nil {
					break // io.EOF or malformed tail — keep partial counts
				}
				counts[string(event.Type)]++
				switch event.Type {
				case schema.EventCostRecord:
					var c schema.CostRecordData
					if json.Unmarshal(event.Data, &c) == nil {
						rm.Usage.InputTokens += c.InputTokens
						rm.Usage.OutputTokens += c.OutputTokens
						rm.Usage.CacheReadInputTokens += c.CacheReadTokens
						rm.Usage.CacheCreationInputTokens += c.CacheCreationTokens
						rm.Turns += c.Turns
						rm.CostUSD += c.CostUSD
						if c.Model != "" {
							rm.Model = c.Model
						}
					}
				case schema.EventStepEnd:
					sawStepEnd = true
					var e schema.StepEndData
					if json.Unmarshal(event.Data, &e) == nil && e.WallTimeSeconds > 0 {
						rm.WallTimeSeconds = e.WallTimeSeconds
					}
				}
			}
			rc.Close()
			rm.EventCounts = counts
			if !sawStepEnd {
				// crashed agent: a stream missing step.end is defined as error
				// (shared-contracts §5 ingestion rule)
				rm.Status = schema.RunStatusError
				if rm.Summary == "" || rm.Summary == "flight recorder output missing" {
					rm.Summary = "event stream ended without step.end"
				}
			}
		}
	}

	// Upsert reports whether THIS ingestion inserted the row (inserted=true) or
	// updated an existing one (inserted=false, i.e. a web-restart resume: the
	// whole Step.Run re-executes, re-attaches, and re-ingests). The metrics row
	// is idempotent under ON CONFLICT (build_id, plan_id), but agent_cost_ledger
	// is append-only with no dedup key (§1.4) — so the ledger append below is
	// gated on `inserted` to charge each step exactly once. On a metrics-store
	// error inserted is false, so a failed upsert also skips the ledger append
	// (we cannot prove first-insert), preserving "every dollar enters the ledger
	// exactly once" over "at least once".
	//
	// DEGRADED ingestion (finding F24): when no flight data was actually read —
	// restart-resume with an ephemeral locator, reaped pod, missing mount — rm
	// is a zero-cost status=error shell, and pushing it through the all-columns
	// DO UPDATE would destroy a real row written by an earlier ingestion
	// (scorecards and delivery-outcomes read that row). Write it insert-only
	// instead: it lands when no row exists (genuinely crashed agent) and is a
	// no-op when one does.
	var inserted bool
	var err error
	if flightRead {
		inserted, err = step.metricsStore.UpsertReturningInserted(&rm)
	} else {
		inserted, err = step.metricsStore.InsertIfAbsent(&rm)
	}
	if err != nil {
		logger.Error("failed-to-ingest-run-metrics", err)
	}

	if inserted && step.budgetChecker != nil && rm.CostUSD > 0 {
		entry := budget.LedgerEntry{
			// field names mirror agent_cost_ledger columns (contracts §1.4/§2.7);
			// align to the exact struct credentials-and-budgets landed.
			TicketID:            rm.TicketID,
			PipelineRunID:       rm.PipelineRunID,
			BuildID:             rm.BuildID,
			StepName:            rm.StepName,
			Source:              "agent_step",
			Provider:            "anthropic",
			Model:               rm.Model,
			InputTokens:         rm.Usage.InputTokens,
			OutputTokens:        rm.Usage.OutputTokens,
			CacheReadTokens:     rm.Usage.CacheReadInputTokens,
			CacheCreationTokens: rm.Usage.CacheCreationInputTokens,
			Turns:               rm.Turns,
			CostUSD:             rm.CostUSD,
		}
		if err := step.budgetChecker.Record(entry); err != nil {
			logger.Error("failed-to-record-cost-ledger", err) // fire-and-forget
		}
	}
}

func envInt(env map[string]string, key string) (int, bool) {
	v, ok := env[key]
	if !ok || v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
```

  Step success remains purely a function of the process exit status — ingestion never changes the return values.
- [ ] Run `ginkgo ./atc/exec/` — expect pass.
- [ ] Commit: `git add atc/exec && git commit -m "feat(exec): synchronous server-side flight-recorder ingestion — detached ingest ctx (F4), first-insert-gated ledger record (F3), non-destructive degraded re-ingestion (F24)"`

---

### Task 13B: jetbridge — record output locations on the severed-exec path (added 2026-07-09, finding F23)

Deadline- and transport-severed execs return through the non-`ExecExitError` branch of `execProcess.Wait` (process.go:899–902), which skips `uploadOutputsToArtifactStore` (:906) — the ONLY publisher of an output volume's locator entry in the DaemonSet artifact store. Without a locator, `WrapVolumeForLookup` yields `sourceNode==""` and `StreamOut` fails instantly (storage_daemonset.go:522–546, volume_daemonset.go:90–92). Consequence for this plan: on the step-timeout path, Task 13's ingestion fails BOTH `StreamFile` reads no matter how it detaches its context (the F4 `WithoutCancel` fix is necessary but not sufficient), so timed-out steps still record zero cost. The same gap breaks timed-out task steps' hook inputs. Fix: a best-effort, 15s-bounded `uploadOutputsToArtifactStore` call in that branch, on a detached context, with failures logged and never returned.

This is the one additional jetbridge edit sanctioned outside Task 11B's frozen runtime-seams scope (finding F23 is 07-owned; see REVIEW.md §8). Sequence after Task 11B (same file). The Task 13 unit specs structurally CANNOT catch this — the fake streamer bypasses artifact location entirely — so the end-to-end proof is the live readability assertion added to Task 18.

**Files:**
- Modify: `atc/worker/jetbridge/process.go` (:899–902 branch)
- Test: `atc/worker/jetbridge/process_test.go`; live readability assertion in `live_agent_resume_test.go` (Task 18)

**Steps:**

- [ ] Write the failing spec in `atc/worker/jetbridge/process_test.go`, on the existing fake-executor + fake-storage-backend scaffolding: the executor's exec returns a non-`ExecExitError` transport error (e.g. `errors.New("error dialing backend: EOF")`); assert that the storage backend's `RecordOutputs` is called exactly once before `Wait` returns, and that `Wait` still returns the wrapped transport error (the recording is best-effort, not a rescue):

```go
	It("records output locations even when the exec is severed by a transport error (F23)", func() {
		fakeExecutor.ExecInPodReturns(errors.New("error dialing backend: EOF"))

		_, err := process.Wait(ctx)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exec in pod"))

		Expect(fakeStorageBackend.RecordOutputsCallCount()).To(Equal(1))
	})
```

- [ ] Run `ginkgo --focus="severed" ./atc/worker/jetbridge/` — expect failure (`RecordOutputs` never called).
- [ ] Implement: in `execProcess.Wait`, the branch at process.go:899–902 becomes:

```go
		logger.Error("failed-to-exec-in-pod", err)
		fetchPodFailureContext(ctx, p.clientset, p.config.Namespace, p.podName, p.processIO.Stderr)

		// Best-effort output-location recording (finding F23). This branch is
		// reached on deadline-severed and transport-severed execs — the step
		// timeout included — and uploadOutputsToArtifactStore is the ONLY
		// publisher of the output volumes' locator entries in the DaemonSet
		// store. Without it, server-side flight-recorder ingestion (and
		// timed-out task steps' hook inputs) see sourceNode=="" and StreamOut
		// fails instantly, however the exec layer detaches its read context.
		// ctx may already be cancelled/expired here, so detach and bound the
		// call; failures are logged, never returned — the transport error
		// below stays the caller-visible result.
		uploadCtx, cancelUpload := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		if uploadErr := p.uploadOutputsToArtifactStore(uploadCtx); uploadErr != nil {
			logger.Error("failed-to-record-outputs-after-severed-exec", uploadErr)
		}
		cancelUpload()

		spanErr = err
		return runtime.ProcessResult{}, wrapIfTransient(fmt.Errorf("exec in pod: %w", err))
```

- [ ] Run `ginkgo ./atc/worker/jetbridge/` — expect pass (the `ExecExitError` and success paths already record outputs; this spec covers the third branch).
- [ ] Commit: `git add atc/worker/jetbridge && git commit -m "fix(jetbridge): best-effort output-location recording on severed-exec paths (F23) — timed-out steps keep readable outputs"`

---

### Task 14: Engine wiring — container type, CoreStepFactory.AgentStep, builder dispatch

`exec.NewAgentStep` and its options exist (Tasks 12–13); this task routes `Agent` plans to it. Amended 2026-07-09 (runtime-seams F18): `db.ContainerTypeAgent` is now **consumed, not added** — the constant and its parse case landed in Task 11B, where the same commit extends jetbridge's `supervised()` to cover it (the agent process MUST run under the in-pod supervisor for web-restart resume and the park protocol to exist). The rest of the engine wiring is unchanged.

**Files:**
- Consume: `db.ContainerTypeAgent` (added by Task 11B — this task no longer touches `atc/db/container_metadata.go`)
- Modify: `atc/engine/builder.go:20` (CoreStepFactory interface + `buildAgentStep` dispatch before `plan.Run` at :138)
- Modify: `atc/engine/step_factory.go:166` region (constructor; new option fields)
- Modify: `atc/engine/enginefakes/` (regenerate)
- Test: `atc/engine/builder_test.go` (mirror the RunStep case at :558)

**Steps:**

- [ ] Add a builder test case in `atc/engine/builder_test.go` next to the run-step context (:558): a plan built from `planFactory.NewPlan(atc.AgentPlan{Name: "write-spec", Prompt: "p"})`; assert `fakeCoreStepFactory.AgentStepCallCount()` is 1 and the received plan / stepMetadata / containerMetadata match (containerMetadata `Type: db.ContainerTypeAgent`, `StepName: "write-spec"`). Copy the surrounding `expectedPlan`/`ArgsForCall` assertions verbatim from the run case (:558–566), substituting names.
- [ ] Run `ginkgo ./atc/engine/` — expect compile failure (`AgentStep` not on `CoreStepFactory`, fake missing). (`db.ContainerTypeAgent` already exists — Task 11B.)
- [ ] In `atc/engine/builder.go`:
  - Add to `CoreStepFactory` (after `RunStep` :24): `AgentStep(atc.Plan, exec.StepMetadata, db.ContainerMetadata, DelegateFactory) exec.Step`
  - Add dispatch before `plan.Run` (:138):

```go
	if plan.Agent != nil {
		return factory.buildAgentStep(build, plan)
	}
```

  - Add after `buildRunStep` (:378):

```go
func (factory *stepperFactory) buildAgentStep(build db.Build, plan atc.Plan) exec.Step {
	containerMetadata := factory.containerMetadata(
		build,
		db.ContainerTypeAgent,
		plan.Agent.Name,
		plan.Attempts,
	)

	stepMetadata := factory.stepMetadata(
		build,
		factory.externalURL,
		false,
	)

	return factory.coreFactory.AgentStep(
		plan,
		stepMetadata,
		containerMetadata,
		factory.buildDelegateFactory(build, plan),
	)
}
```

- [ ] In `atc/engine/step_factory.go`:
  - Add fields to `coreStepFactory` (:18): `agentStepImage string`, `agentMetricsStore metrics.Store`, `agentBudgetChecker budget.Checker` (imports `"github.com/concourse/concourse/agent/api/metrics"`, `"github.com/concourse/concourse/agent/budget"`).
  - Add options following `WithCoreImageResolver` (:39):

```go
// WithAgentStepImage sets the main-container image for agent: steps
// (web flag --agent-step-image).
func WithAgentStepImage(image string) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.agentStepImage = image }
}

// WithAgentMetricsStore sets the run-metrics store for server-side
// flight-recorder ingestion.
func WithAgentMetricsStore(s metrics.Store) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.agentMetricsStore = s }
}

// WithAgentBudgetChecker sets the budget library used for step-slice
// resolution and fire-and-forget ledger records.
func WithAgentBudgetChecker(c budget.Checker) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.agentBudgetChecker = c }
}
```

  - Add the constructor after `RunStep` (:185), mirroring `TaskStep`'s working-dir hash (:193):

```go
func (factory *coreStepFactory) AgentStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	containerMetadata db.ContainerMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	sum := sha256.Sum256([]byte(plan.Agent.Name))
	containerMetadata.WorkingDirectory = filepath.Join("/tmp", "build", fmt.Sprintf("%x", sum[:4]))

	var agentOpts []exec.AgentStepOption
	if factory.imageResolver != nil {
		agentOpts = append(agentOpts, exec.WithAgentImageResolver(factory.imageResolver))
	}
	if factory.agentMetricsStore != nil {
		agentOpts = append(agentOpts, exec.WithAgentMetricsStore(factory.agentMetricsStore))
	}
	if factory.agentBudgetChecker != nil {
		agentOpts = append(agentOpts, exec.WithAgentBudgetChecker(factory.agentBudgetChecker))
	}

	agentStep := exec.NewAgentStep(
		plan.ID,
		*plan.Agent,
		factory.defaultLimits,
		factory.defaultRequests,
		stepMetadata,
		containerMetadata,
		factory.pool,
		factory.streamer,
		delegateFactory,
		factory.defaultTaskTimeout,
		factory.agentStepImage,
		agentOpts...,
	)

	agentStep = exec.LogError(agentStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		agentStep = exec.RetryError(agentStep, delegateFactory)
	}
	return agentStep
}
```

- [ ] Regenerate engine fakes: `go generate ./atc/engine/...`
- [ ] Run `ginkgo ./atc/engine/` — expect pass.
- [ ] Commit: `git add atc/engine && git commit -m "feat(engine): route agent plans to exec.AgentStep with image/metrics/budget options"`

---

### Task 15: `agent-runner` binary

Deterministic pod entrypoint: waits for declared MCP sidecars, invokes the claude CLI, writes the flight recorder. Lives in the main module (`agent/runner` package + thin `cmd/agent-runner`). CLI envelope parsing follows `ci-agent/llm/result.go` (cost_usd, usage, num_turns, is_error — with `total_cost_usd` fallback for newer CLIs).

> **Amended 2026-07-10 (PARK-V2 seam delta §§B/D/F/G).** Two of this task's decisions are superseded by the PARK-V2 tasks and must NOT be re-implemented later as written here: (1) step 4's `--output-format json` becomes `--output-format stream-json` teed line-by-line to stdout with a tee-only truncation guard, and step 5's "parse the last non-empty line" becomes "capture the terminal `result` stream event" — Task 23; (2) the runner additionally gains the `flight/park.json` watcher → SIGTERM → grace → SIGKILL → exit **86** park-exit path, session-JSONL capture to `flight/session.jsonl` at EVERY exit, and the `--resume` continuation mode (`AGENT_SESSION_ID`/`AGENT_SESSION_FILE`, frozen `runner.ContinuationPrompt`) — Task 24, gated on the Task 20 empirical pin. `cliEnvelope` below gains `SessionID string \`json:"session_id"\`` in Task 23. If this task is already landed, Tasks 23/24 amend it in place; if not yet started, still implement it as written first — the PARK-V2 tasks are deliberately expressed as diffs against this baseline.

**Files:**
- Create: `agent/runner/runner.go`
- Create: `agent/runner/envelope.go`
- Create: `cmd/agent-runner/main.go`
- Test: `agent/runner/runner_test.go`

**Steps:**

- [ ] Write `agent/runner/runner_test.go` (plain Go tests; stub `claude` via a shell script and stub MCP healthz via `httptest`):

```go
package runner_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/runner"
	schema "github.com/concourse/concourse/agent/schema"
)

func writeStubClaude(t *testing.T, dir, envelope string) string {
	t.Helper()
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\necho '" + envelope + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunWritesFlightRecorder(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)

	healthz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthz.Close()

	claude := writeStubClaude(t, dir,
		`{"type":"result","subtype":"success","result":"\"done\"","model":"m1","cost_usd":0.42,"num_turns":9,"is_error":false,"usage":{"input_tokens":100,"output_tokens":50}}`)

	cfg := runner.Config{
		Prompt:     "do it",
		Model:      "m1",
		MaxTurns:   9,
		FlightDir:  flight,
		WorkDir:    dir,
		StepName:   "write-spec",
		ClaudePath: claude,
		MCPServers: map[string]string{"platform": healthz.URL + "/mcp"},
	}
	exit, err := runner.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("expected exit 0, got %d", exit)
	}

	raw, err := os.ReadFile(filepath.Join(flight, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var results schema.Results
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	if results.Status != schema.StatusPass {
		t.Fatalf("expected pass, got %s", results.Status)
	}

	events, err := os.Open(filepath.Join(flight, "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	reader := schema.NewEventReader(events)
	var types []schema.EventType
	for {
		e, err := reader.Read()
		if err != nil {
			break
		}
		types = append(types, e.Type)
	}
	want := []schema.EventType{schema.EventStepStart, schema.EventCostRecord, schema.EventStepEnd}
	if len(types) != 3 || types[0] != want[0] || types[1] != want[1] || types[2] != want[2] {
		t.Fatalf("expected %v, got %v", want, types)
	}
}

func TestRunMapsCLIErrorToErrorStatus(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)
	claude := writeStubClaude(t, dir, `{"type":"result","is_error":true,"result":"\"boom\""}`)

	exit, err := runner.Run(context.Background(), runner.Config{
		Prompt: "p", FlightDir: flight, WorkDir: dir, StepName: "s", ClaudePath: claude,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exit != 2 {
		t.Fatalf("expected exit 2 (error), got %d", exit)
	}
}
```

- [ ] Run `go test ./agent/runner/` — expect compile failure.
- [ ] Write `agent/runner/envelope.go`:

```go
package runner

import "encoding/json"

// cliEnvelope is the claude CLI --output-format json envelope
// (parity with ci-agent/llm/result.go, plus total_cost_usd for newer CLIs).
type cliEnvelope struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype"`
	Result       json.RawMessage `json:"result"`
	Model        string          `json:"model"`
	CostUSD      float64         `json:"cost_usd"`
	TotalCostUSD float64         `json:"total_cost_usd"`
	NumTurns     int             `json:"num_turns"`
	IsError      bool            `json:"is_error"`
	Usage        struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func (e cliEnvelope) costUSD() float64 {
	if e.TotalCostUSD > 0 {
		return e.TotalCostUSD
	}
	return e.CostUSD
}
```

- [ ] Write `agent/runner/runner.go` with:
  - `type Config struct { Prompt, PromptFile, Model string; MaxTurns int; OutputSchema string; FlightDir, WorkDir, StepName, ClaudePath string; MCPServers map[string]string; Stdout, Stderr io.Writer }` (nil writers default to os.Stdout/os.Stderr; empty ClaudePath defaults to `"claude"`).
  - `FromEnv() Config` reading `AGENT_PROMPT`, `AGENT_PROMPT_FILE`, `AGENT_MODEL`, `AGENT_MAX_TURNS`, `AGENT_OUTPUT_SCHEMA`, `AGENT_FLIGHT_DIR`, `AGENT_STEP_NAME`, and every env var matching `^([A-Z]+)_MCP_URL$` (`DEV_MCP_URL` → key `dev`, etc., via `os.Environ()` scan); `WorkDir` = cwd.
  - `Run(ctx context.Context, cfg Config) (exitCode int, err error)`:
    1. Resolve prompt: `cfg.Prompt` inline, else read `filepath.Join(cfg.WorkDir, cfg.PromptFile)`; both empty → return `(2, errors.New("no prompt configured"))`.
    2. For each MCP server, poll `GET` on the URL with the `/mcp` suffix replaced by `/healthz` every 2s up to 60s (§8.5: every sidecar exposes `GET /healthz`); on timeout write an `error` event (`{"message":"sidecar <name> never became healthy"}`) plus `step.end` with status error, and return exit 2 (platform error, not agent failure).
    3. Open `events.ndjson` (create/truncate) in `cfg.FlightDir`; `w := schema.NewEventWriter(f)`; write `EventStepStart` with marshaled `schema.StepStartData{StepName: cfg.StepName}`.
    4. Build args: `-p <prompt> --output-format json`; `--model` when set; `--max-turns` when > 0; `--dangerously-skip-permissions`; when MCPServers is non-empty, write a temp file containing `{"mcpServers":{"<name>":{"type":"http","url":"<url>"}, ...}}` and pass `--mcp-config <path>`. Execute `exec.CommandContext(ctx, cfg.ClaudePath, args...)` with `Dir: cfg.WorkDir`, stderr → cfg.Stderr, stdout captured into a buffer AND teed to cfg.Stdout (build log visibility).
    5. Parse the last non-empty line of stdout as `cliEnvelope` (tolerating leading non-JSON output). Write `EventCostRecord` with `schema.CostRecordData{Source: "agent_step", Provider: "anthropic", Model: env.Model, InputTokens: env.Usage.InputTokens, OutputTokens: env.Usage.OutputTokens, CacheReadTokens: env.Usage.CacheReadInputTokens, CacheCreationTokens: env.Usage.CacheCreationInputTokens, Turns: env.NumTurns, CostUSD: env.costUSD()}`.
    6. Status: command error or `env.IsError` → `schema.StatusError` (exit 2); otherwise `schema.StatusPass` (exit 0). Write `results.json`: `schema.Results{SchemaVersion: "1.0", Status: status, Confidence: 1, Summary: <result string truncated to 500 chars>, Artifacts: []schema.Artifact{}}` (satisfies `Results.Validate`).
    7. Write `EventStepEnd` with `schema.StepEndData{StepName, Status: <ThreeWayStatus of status>, Summary, WallTimeSeconds: int(time.Since(start).Seconds()), CostUSD: env.costUSD(), Turns: env.NumTurns}`; close files; return the exit code.
- [ ] Write `cmd/agent-runner/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/concourse/concourse/agent/runner"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	exit, err := runner.Run(ctx, runner.FromEnv())
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-runner: %v\n", err)
		if exit == 0 {
			exit = 2
		}
	}
	os.Exit(exit)
}
```

- [ ] Run `go test ./agent/runner/ && go build ./cmd/agent-runner` — expect pass.
- [ ] Commit: `git add agent/runner cmd/agent-runner && git commit -m "feat(agent-runner): deterministic pod entrypoint invoking claude with flight-recorder output"`

---

### Task 16: `--agent-step-image` flag, atccmd wiring, and the agent-runner image CI job

**Files:**
- Modify: `atc/atccmd/command.go:218` region (flag), `:1990` region (CoreStepFactory options), `constructEngine` signature as needed
- Create: `deploy/agent-runner/Dockerfile`
- Modify: `deploy/concourse-pipeline.yml` (new `build-agent-runner-image` job)
- Test: `go build ./atc/...`; flag visible in `concourse web --help`

**Steps:**

- [ ] Add the flag next to `AgentReviewPublishToken` (command.go:218):

```go
	AgentStepImage string `long:"agent-step-image" description:"Container image for the agent: step's main container (must contain the claude CLI and agent-runner). Agent steps error at runtime when unset."`
```

- [ ] In the `engine.NewCoreStepFactory` call (command.go:1990), append options:

```go
				engine.WithCoreImageResolver(resolver),
				engine.WithAgentStepImage(cmd.AgentStepImage),
				engine.WithAgentMetricsStore(db.NewAgentRunMetricsFactory(dbConn)),
				engine.WithAgentBudgetChecker(agentBudgetChecker),
```

  where `agentBudgetChecker` is the `budget.Checker` instance credentials-and-budgets wired into `command.go` in wave 1 — locate it with `grep -n "budget\." atc/atccmd/command.go` and reuse it (thread it into `constructEngine` as a parameter alongside the existing ones, plus `dbConn` for the metrics factory if not already in scope; wave-1 code determines the exact constructor — the §2.7 `Checker` interface is the only binding surface).
- [ ] Run `go build ./atc/... && go run ./cmd/concourse web --help 2>&1 | grep agent-step-image` — expect the flag listed.
- [ ] Create `deploy/agent-runner/Dockerfile` (packaging per §8.5 as amended 2026-07-09, finding F25: pinned provider CLI; the image runs as **root** — §8.5's non-root convention is scoped to MCP sidecar images only; this Dockerfile also hosts `harvest-runner` per 09 Task 10, which inherits the same decision):

```dockerfile
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/agent-runner ./cmd/agent-runner

FROM node:20-bookworm-slim
# Pin to the claude-code version the live theborg review job currently runs
# (check a recent agent-review build log before bumping).
RUN npm install -g @anthropic-ai/claude-code@2.0.1 \
    && apt-get update && apt-get install -y --no-install-recommends git ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/agent-runner /usr/local/bin/agent-runner
# Root, like every other step image: jetbridge hostPath step volumes are
# kubelet-created root:root 0755 and fsGroup is ignored for hostPath, so a
# non-root user gets EACCES writing the flight recorder. IS_SANDBOX=1 lets
# the claude CLI accept --dangerously-skip-permissions when running as root
# (single-tenant, pod-isolated sandbox).
ENV IS_SANDBOX=1
ENTRYPOINT ["agent-runner"]
```

  (No `USER` directive — earlier drafts' `RUN useradd -u 1001 -m agent` + `USER agent` are DELETED: the first live run would EACCES on `results.json` against the root:root 0755 hostPath output volume, storage_daemonset.go:45–56, and nothing before the Task 19 dual-run would catch it. Task 18's flight-mount write tripwire now guards this permanently.)

- [ ] Add a `build-agent-runner-image` job to `deploy/concourse-pipeline.yml` copying the `build-image` job's DinD-builder pattern (deploy/concourse-pipeline.yml:226–352) but building `deploy/agent-runner/Dockerfile` with context `.` and pushing `registry.home/agent-runner:v<version>` and `ghcr.io/tdmtrader/agent-runner:v<version>`; `trigger: false` (manual — the image changes rarely), `passed: [unit-tests]`.
- [ ] Validate the pipeline config: `fly validate-pipeline -c deploy/concourse-pipeline.yml`.
- [ ] Commit: `git add atc/atccmd deploy && git commit -m "feat(web): --agent-step-image flag, engine wiring, and agent-runner image build job"`

---

### Task 17: Elm build-page rendering for the agent step

The plan decoder (`web/elm/src/Concourse.elm:666`) is a strict `oneOf` — a build containing an `agent` plan otherwise fails to render. Mirror the `BuildStepRun` precedent end to end.

**Files:**
- Modify: `web/elm/src/Concourse.elm` (:500 variant list — add `BuildStepAgent StepName`; :431 region — the variant case; :679 decoder list — add `Json.Decode.field "agent" …`; :750 region — `decodeBuildStepAgent`)
- Modify: `web/elm/src/Build/StepTree/StepTree.elm` (:92, :182, :1310/:1361, :1386/:1431 — the case sites where `BuildStepRun`/`BuildStepSidecar` are handled; render like `BuildStepTask` with the step name)
- Test: Elm compile (exhaustiveness) + existing elm test suite

**Steps:**

- [ ] Add `| BuildStepAgent StepName` to the `BuildStep` union (Concourse.elm:500 region) and satisfy every `case` the compiler flags — Elm's exhaustiveness checking is the failing test here.
- [ ] Add the decoder entry after the `run` field (:680):

```elm
                , Json.Decode.field "agent" <|
                    lazy (\_ -> decodeBuildStepAgent)
```

  and:

```elm
decodeBuildStepAgent : Json.Decode.Decoder BuildStep
decodeBuildStepAgent =
    Json.Decode.succeed BuildStepAgent
        |> andMap (Json.Decode.field "name" Json.Decode.string)
```

- [ ] In `Build/StepTree/StepTree.elm`, handle `Concourse.BuildStepAgent name` at each flagged site exactly as `Concourse.BuildStepTask name` is handled (init → leaf step with name; view label `"agent:"`).
- [ ] Compile + test + rebuild the bundle the way the repo does (see commit `6f16d19a` "rebuild frontend bundle" for the exact command; typically `cd web && yarn && yarn build`, elm tests under `web/elm` via `npx elm-test` if present). Expect clean compile.
- [ ] Commit: `git add web && git commit -m "feat(web-ui): render agent steps in the build plan view"`

---

### Task 18: Live theborg tests — sidecar-MCP wiring proof and agent-process resume

The wiring proof the three wave-3 sidecar workstreams assume: a main container reaches a stub MCP tool served from a sidecar over localhost in the pause-pod model, with startup-order tolerance and clean teardown. Plus the supervisor-resume proof for the well-known `agent` process ID. Plain Go tests, `//go:build live`, `kubeClient(t)` + throwaway namespace per CLAUDE.md.

Amended 2026-07-09 (final-review findings F18/F23/F25) — three binding additions: (1) the resume test MUST use `db.ContainerTypeAgent` at BOTH `FindOrCreateContainer` sites — a copy that keeps `ContainerTypeTask` passes vacuously against the pre-11B `supervised()` and is a review-rejectable defect; (2) the MCP test gains a flight-mount write tripwire so a hostPath-permission regression on output volumes fails this test instead of the first production run; (3) the resume test gains the F23 readability assertion — outputs must be streamable after a severed exec, before any re-attach.

**Files:**
- Create: `atc/worker/jetbridge/live_agent_mcp_test.go`
- Create: `atc/worker/jetbridge/live_agent_resume_test.go`
- Test: run against theborg (commands below)

**Steps:**

- [ ] Write `live_agent_mcp_test.go` modeled on `live_sidecar_test.go`, but through the jetbridge worker path (`setupLiveWorker(t, handle)` + `FindOrCreateContainer` with `runtime.ContainerSpec{Sidecars: …}` — the exact pod construction the agent step exec uses):

```go
//go:build live
// +build live

package jetbridge_test

// TestLiveAgentSidecarMCPWiring proves the wiring every MCP sidecar
// workstream assumes: a sidecar serving HTTP on a fixed localhost port
// (7781, the platform slot), a main container that waits for /healthz then
// POSTs a stub MCP tool call to /mcp — with the main container starting
// BEFORE the sidecar is ready (startup-order tolerance), in the pause-pod
// model, and torn down cleanly.
//
//	kubectl create ns jetbridge-agent-step-test
//	KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=jetbridge-agent-step-test \
//	  go test -tags live -run '^TestLiveAgentSidecarMCPWiring$' -v -count=1 \
//	  -timeout 5m ./atc/worker/jetbridge/
//	kubectl delete ns jetbridge-agent-step-test
func TestLiveAgentSidecarMCPWiring(t *testing.T) {
	handle := "live-agent-mcp-" + time.Now().Format("150405")
	ctx := context.Background()
	clientset, cfg := kubeClient(t)
	cleanupPod(t, clientset, cfg.Namespace, handle)

	worker, delegate := setupLiveWorker(t, handle)

	// Sidecar: python stub MCP server. Sleeps 10s BEFORE binding so the main
	// container demonstrably starts first and must wait — the startup-order
	// case. Serves GET /healthz (200) and POST /mcp (fixed JSON tool result).
	sidecarScript := `
import time, json
time.sleep(10)
from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.end_headers(); self.wfile.write(b"ok")
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0)); self.rfile.read(n)
        body = json.dumps({"jsonrpc": "2.0", "id": 1, "result": {"content": [{"type": "text", "text": "stub-tool-result-42"}]}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json"); self.end_headers()
        self.wfile.write(body)
HTTPServer(("127.0.0.1", 7781), H).serve_forever()
`

	containerSpec := runtime.ContainerSpec{
		TeamID:    1,
		ImageSpec: runtime.ImageSpec{ImageURL: "docker:///python:3.12-alpine"},
		Dir:       "/tmp/build/agent",
		Env:       []string{"PLATFORM_MCP_URL=http://127.0.0.1:7781/mcp"},
		// Output volume for the flight-write tripwire (finding F25): jetbridge
		// hostPath output volumes are kubelet-created root:root 0755, so this
		// mount is exactly what agent-runner must be able to write results.json
		// into. A permission regression fails HERE, not on the first live run.
		Outputs: runtime.OutputPaths{"flight": "/tmp/build/agent/flight/"},
		Sidecars: []atc.SidecarConfig{{
			Name:    "platform",
			Image:   "python:3.12-alpine",
			Command: []string{"python", "-c", sidecarScript},
		}},
	}

	container, _, err := worker.FindOrCreateContainer(
		ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeAgent},
		containerSpec, delegate,
	)
	if err != nil {
		t.Fatalf("creating container: %v", err)
	}

	// Main: wait for healthz (up to 60s), then POST a tool call — the exact
	// client behavior agent-runner implements.
	clientScript := `
import time, urllib.request, sys
base = "http://127.0.0.1:7781"
deadline = time.time() + 60
while True:
    try:
        urllib.request.urlopen(base + "/healthz", timeout=2); break
    except Exception:
        if time.time() > deadline: print("HEALTHZ-TIMEOUT"); sys.exit(1)
        time.sleep(1)
req = urllib.request.Request(base + "/mcp", data=b'{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"stub"}}', headers={"Content-Type": "application/json"})
print(urllib.request.urlopen(req, timeout=10).read().decode())
# Flight-mount write tripwire (finding F25): prove the main container can
# write the root:root 0755 hostPath output volume it runs on.
import os
os.makedirs("/tmp/build/agent/flight", exist_ok=True)
with open("/tmp/build/agent/flight/tripwire", "w") as f:
    f.write("ok\n")
print("flight-write-ok")
`

	var stdout, stderr bytes.Buffer
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "python",
		Args: []string{"-c", clientScript},
	}, runtime.ProcessIO{Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatalf("running client process: %v", err)
	}

	result, err := process.Wait(ctx)
	if err != nil {
		t.Fatalf("waiting: %v (stderr: %s)", err, stderr.String())
	}
	if result.ExitStatus != 0 {
		t.Fatalf("client exited %d: %s / %s", result.ExitStatus, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "stub-tool-result-42") {
		t.Fatalf("expected stub tool result in output, got: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "flight-write-ok") {
		t.Fatalf("flight-mount write tripwire failed — hostPath output volume not writable by the main container (finding F25): %s / %s",
			stdout.String(), stderr.String())
	}
}
```

  (Adjust `setupLiveWorker`/`cleanupPod`/`kubeClient` calls to their exact signatures in `live_test.go`/`live_worker_test.go` at execution time; teardown is the existing `t.Cleanup`-based pod deletion in those helpers.)
- [ ] Write `live_agent_resume_test.go`: copy `TestLiveTaskResume` (live_task_resume_test.go:33) wholesale, renamed `TestLiveAgentProcessResume`, with `ProcessSpec{ID: "agent", …}` on the web-1 `container1.Run` and the web-2 takeover using `container2.Attach(ctx, "agent", …)` — proving the `agent` process ID survives a severed exec session without the command restarting (the exec's `attachOrRun` contract from Task 12).

  **BINDING (amended 2026-07-09, finding F18):** the copy MUST pass `db.ContainerMetadata{Type: db.ContainerTypeAgent}` at **BOTH** `FindOrCreateContainer` sites — the web-1 site mirroring live_task_resume_test.go:61 and the web-2 takeover site mirroring :118. Supervision is gated on the container type (`supervised()`, Task 11B): a copy that keeps `ContainerTypeTask` exercises only the already-proven task path and passes **vacuously** — that is a review-rejectable defect, not a passing test. The test proves: one "started" line total across both webs, exit status recovered by web-2, pod survives web-1 death.
- [ ] Extend `TestLiveAgentProcessResume` with the F23 readability assertion (amended 2026-07-09): give the container spec an output (`Outputs: runtime.OutputPaths{"flight": …}`) and have the long-running command write a marker file into it before its sleep (`echo flight-data > <flight-mount>/tripwire; …`). After web-1's `Wait` returns severed (the test's deadline-cancelled/killed exec session) and **before** web-2 attaches, stream the marker back through the DaemonSet fabric and assert its contents:

```go
	// F23: the severed-exec branch must have recorded the output volume's
	// locator — otherwise WrapVolumeForLookup yields sourceNode=="" and this
	// StreamOut fails instantly, which is exactly the regression that made
	// timed-out agent steps unmeasurable.
	rc, err := flightVolume.StreamOut(ctx, "tripwire", baggageclaim.GzipEncoding)
	if err != nil {
		t.Fatalf("flight output not readable after severed exec (F23 locator missing): %v", err)
	}
	defer rc.Close()
	// un-tar and assert the file body contains "flight-data" (reuse the
	// stream-read helper from the existing live volume tests)
```

  (Adjust the volume-handle lookup and stream-read helper to the exact `live_test.go`/volume-test helpers at execution time; the assertion that matters is: readable after severance, before re-attach.)
- [ ] Compile-check without a cluster: `go vet -tags live ./atc/worker/jetbridge/`.
- [ ] Run both against theborg (throwaway namespace; NEVER cicd/concourse):

```
kubectl create ns jetbridge-agent-step-test
KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=jetbridge-agent-step-test \
  go test -tags live -run '^TestLiveAgent' -v -count=1 -timeout 10m ./atc/worker/jetbridge/
kubectl delete ns jetbridge-agent-step-test
```

  Expect both tests green; paste the pass output into the commit message body.
- [ ] Commit: `git add atc/worker/jetbridge && git commit -m "test(jetbridge): live sidecar-MCP wiring and agent-process resume proofs on theborg"`

---

### Task 19: Cutover — native agent-review job with dual-running verification

Replaces the build-from-source shell job in the live theborg/cicd `concourse` pipeline with a hand-written job using the `agent:` step. Prereqs at execution time: this branch released to theborg via the self-upgrade pipeline (the step type must be deployed before a pipeline can use it), the agent-runner image pushed (Task 16 job), web configured with `--agent-step-image=registry.home/agent-runner:v<version>` (deployment env `CONCOURSE_AGENT_STEP_IMAGE`), and the wave-1 scoped principal for review publishing (agent-identity's cutover) available as `((agent-review-publish-token))`.

**Files:**
- Modify: `deploy/concourse-pipeline.yml` (add `agent-review-native` job; later remove the `agent-review` job at :42–110)
- Test: dual-running verification on theborg (operator steps, recorded)

**Steps:**

- [ ] Add the `agent-review-native` job after the existing `agent-review` job (:110). The prompt body is the real review-phase prompt template `ci-agent/phases/prompts/review/findings.md` (the file `ci-agent/phases/review.yaml` references as `template: prompts/review/findings.md` — `review.yaml` itself contains no `prompt:` text). The phase-runner normally resolves that file's Go-template placeholders (`{{.Env.repo_dir}}`, `{{if eq .Env.diff_only "true"}}…`, `{{.Env.base_ref}}`, `{{.Env.score_threshold}}`) at runtime; the native `agent:` step does NOT run Go-template substitution, so inline the template with those placeholders resolved to this job's fixed values (`repo_dir` → `repo`, `diff_only` → `true` so the diff block is kept, `base_ref` → `main`, `score_threshold` → `7.0`) and change the output path to `review/review.json`:

```yaml
- name: agent-review-native
  plan:
  - get: repo
    trigger: false
    params: {depth: 25}
  - agent: review
    prompt: |
      You are a code review agent. Analyze the repository at repo for real defects (not style issues).

      Only review files changed in diff against base ref: main
      Use `git diff main...HEAD --name-only` to determine changed files.

      For each concern:
      1. Write a failing Go test that proves the defect
      2. Classify severity by what the test demonstrates

      Write a JSON object with this structure to review/review.json:
      {
        "schema_version": "1.0.0",
        "proven_issues": [
          {
            "id": "ISS-001",
            "severity": "critical|high|medium|low",
            "title": "short description",
            "description": "detailed explanation",
            "file": "path/to/file.go",
            "line": 42,
            "category": "security|correctness|performance|maintainability|testing",
            "test_code": "package ...\n\nimport \"testing\"\n\nfunc TestXxx(t *testing.T) { ... }",
            "test_file": "path/to/file_test.go",
            "test_name": "TestXxx"
          }
        ],
        "observations": [
          {
            "id": "OBS-001",
            "title": "short description",
            "file": "path/to/file.go",
            "line": 42,
            "category": "security|correctness|performance|maintainability|testing"
          }
        ],
        "score": {
          "value": 8.5,
          "max": 10.0,
          "pass": true,
          "threshold": 7.0
        }
      }

      If a concern cannot be proven with a test, include it in observations, not proven_issues.
      Run any generated tests to verify they actually fail against the current code before reporting.
    model: ((agent-model))
    max_turns: 50
    inputs: [repo]
    outputs: [review]
    env:
      BASE_REF: main
      REVIEW_DIFF_ONLY: "true"
      CLAUDE_CODE_OAUTH_TOKEN: ((claude-oauth-token))
    timeout: 30m
  - task: publish-review
    config:
      platform: linux
      rootfs_uri: docker:///registry.home/concourse-test-runner:v5
      inputs:
      - name: review
      params:
        AGENT_REVIEW_PUBLISH_TOKEN: ((agent-review-publish-token))
      run:
        path: sh
        args:
        - -ec
        - |
          echo "=== Publishing review to ATC ==="
          curl -sf -X POST \
            -H "Authorization: Bearer ${AGENT_REVIEW_PUBLISH_TOKEN}" \
            -H "Content-Type: application/json" \
            -d "{\"build_id\": ${BUILD_ID}, \"review\": $(cat review/review.json)}" \
            "http://concourse-web.cicd.svc.cluster.local:8080/api/v1/agent/reviews"
          echo "Published for build ${BUILD_ID}"
```

  Locked by contract: the agent pod holds only the anthropic token (var-source scaffolding per program decision — no push/publish credentials); the publish principal token lives only in the separate task step. `((claude-oauth-token))` is a new pipeline var the operator sets from a `claude setup-token` value; if only `((anthropic-api-key))` exists in the cicd vault at execution time, use env name `ANTHROPIC_API_KEY` instead — both are accepted by the claude CLI.
- [ ] Validate: `fly validate-pipeline -c deploy/concourse-pipeline.yml`.
- [ ] Commit: `git add deploy/concourse-pipeline.yml && git commit -m "feat(cicd): native agent-review job using the agent: step (dual-running with shell job)"`
- [ ] **Deploy + dual-run (operator steps, after this branch releases to theborg):**
  1. `fly -t cicd login` per memory `reference_theborg_cicd_live_concourse.md`; confirm the running web version includes this work (`curl .../api/v1/info`).
  2. Set `CONCOURSE_AGENT_STEP_IMAGE` on the concourse-web deployment; trigger the `build-agent-runner-image` job once; set the `((claude-oauth-token))` secret.
  3. `fly -t cicd set-pipeline -p concourse -c deploy/concourse-pipeline.yml`.
  4. For each of 5 runs (same or successive commits), trigger BOTH `agent-review` and `agent-review-native`; verify per run: both jobs green; `agent_reviews` has a row from each (`fly curl /api/v1/teams/main/agent-reviews`); `agent_run_metrics` has exactly one row per native run with non-zero `cost_usd`, `input_tokens`, and `event_counts` (`fly curl /api/v1/agent/tickets/…` is ticket-scoped, so query via psql on the cicd DB or `GetBuildAgentReviews` + direct SQL for metrics); review scores within ±2 of each other (parity signal).
  5. Record the 5 dual-green runs (dates + build numbers) as a new bullet in `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` §11 amendment log.
- [ ] **Retire the shell job (only after step 5 above):** delete the `agent-review` job block (deploy/concourse-pipeline.yml:42–110), `fly set-pipeline`, and commit: `git commit -am "chore(cicd): retire build-from-source agent-review shell job (dual-running verified)"`

---

## PARK-V2 — exit-and-respawn for long human-waits (added 2026-07-10)

**Provenance:** the frozen PARK-V2 seam delta (2026-07-10; amends the 2026-07-09 "SSE transport & park hardening" entry; implements FLOWS.md Part 2 §P2.5 recommendations #1–#4 — read that section first, it is the agent-model audit that motivates every task below; contracts decisions 30–32). Owner-approved: (1) exit-and-respawn hybrid for long parks with the non-terminal `awaiting_human` RUN state decided pre-freeze; (2) stream-json live watching; (3) session JSONL + session_id capture; (4) parked UI badge. **The TICKET enum is NOT reopened** — a parked ticket stays `running`; parked-ness surfaces via run state + open questions (delta §H, owned by 03/08).

**This plan owns the runner/exec/schema halves.** Co-signed halves live elsewhere and are NOT implemented here: `awaiting_human` run status + lifecycler entry/exit + park-timeout expiry (03-pipeline-runs, migration `1773106032`); the `--agent-short-park-max` web flag's primary definition (11-dispatch, Task 13); sidecar threshold timer + atomic `flight/park.json` writer + checkpoint-client exit 3 + `question_hash` find-or-create dedup (08-platform-mcp-hitl, migration `1773106072`); `reconcileAwaitingRuns` + principal/secret re-mint + continuation-build trigger (11-dispatch, Task 11c); `SecretAttacher.Attach` create-or-update + `RunPrincipalTimeout` (02-credentials-and-budgets); run-status API string + `fly runs` + Elm amber badge (03). The runner side treats `flight/park.json` as an opaque signal file with the delta §B1 payload; the exec side treats the OPEN `agent_run_questions` row as the platform's authority — the build status is only a carrier (delta §B5).

**Ordering and the gate:** Task 20 (the delta §I empirical pin) is the **FIRST deliverable of the PARK-V2 build and gates Tasks 24–27**. Tasks 21–23 (accommodations #1/#2: stream-json tee, session capture, additive schema) and Task 25 (additive, inert schema) land regardless of the pin's outcome. Pin RED ⇒ ship with `--agent-short-park-max=0` (pure PARK-V1), mark Tasks 24/26/27 deferred in this file, and record the red result in §11 — zero schema waste, everything landed stays additive and inert at 0.

---

### Task 20: PARK-V2 empirical pin — `TestLiveClaudeParkExitResume` (FIRST deliverable; gates Tasks 24–27)

The F13-style gating test (delta §I, mirrors 08 Task 18b's role): does the pinned claude CLI, SIGTERM'd mid-`ask_human`, leave a resumable session JSONL whose restored copy — under a fresh HOME/cwd, against a fresh answered stub sidecar — makes `claude -p --resume <id>` re-issue the pending tools/call and complete? Plain Go, `//go:build live_claude`, needs the pinned CLI on PATH + `CLAUDE_CODE_OAUTH_TOKEN` (or `ANTHROPIC_API_KEY`); **no cluster, no postgres**.

Mechanism assumptions are split VERIFIED-IN-DOCS vs NEEDS-EMPIRICAL-PIN in the delta; this test answers pins **P1** (stream-json flag shape / `--verbose` requirement / result-event field parity incl. session_id+cost), **P2** (cwd-slug derivation + incremental JSONL append — pending `tool_use` on disk before the call resolves), **P3** (SIGTERM behavior — prompt exit, envelope optional), **P4** (THE GATE — resume re-issues the pending tools/call; whether the resumed envelope's session_id equals or forks the original), **P5** (resumed envelope cost per-invocation vs session-cumulative), **P6** (tool availability on resume comes from the live `--mcp-config`, not the transcript).

**Files:**
- Create: `agent/runner/continuation.go` (frozen constants shared by test + Task 24)
- Create: `agent/runner/live_claude_resume_test.go`

**Steps:**

- [ ] Write `agent/runner/continuation.go`:

```go
package runner

import "strings"

// ContinuationPrompt is the FROZEN prompt for every `claude -p --resume`
// continuation invocation (PARK-V2 delta §D). Do not edit without a contracts
// amendment — the empirical pin (TestLiveClaudeParkExitResume) verifies this
// exact string makes the resumed model re-issue its pending tool call.
const ContinuationPrompt = "Your wait for a human has ended and the question has been answered. Re-issue your pending platform-mcp tool call to receive the answer, then continue your task. If your step's goal is already complete, finish now."

// ExitAwaitingHuman is the frozen agent-runner park-exit code (delta §B2):
// the step ended because a human-wait crossed --agent-short-park-max, not
// because the agent finished or failed. exec.AgentStep matches on it.
const ExitAwaitingHuman = 86

// CwdSlug derives the claude CLI's per-project session directory name for a
// working directory (~/.claude/projects/<slug>/<session-id>.jsonl).
// Empirical pin P2 verifies this rule at the pinned CLI version — if the
// pinned CLI derives it differently, fix THIS function (test and runner both
// call it, so they cannot drift apart).
func CwdSlug(dir string) string {
	return strings.ReplaceAll(dir, "/", "-")
}
```

- [ ] Write `agent/runner/live_claude_resume_test.go`:

```go
//go:build live_claude

package runner_test

// TestLiveClaudeParkExitResume is THE PARK-V2 gating pin (frozen delta §I).
// Needs the pinned claude CLI (the version in deploy/agent-runner/Dockerfile)
// on PATH and CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY in the env.
//
//	go test -tags live_claude -run '^TestLiveClaudeParkExitResume$' -v \
//	  -count=1 -timeout 15m ./agent/runner/
//
// GREEN ⇒ Tasks 24–27 proceed. RED ⇒ ship --agent-short-park-max=0 (pure
// PARK-V1); Tasks 21–23/25 land anyway (additive + inert). Either way paste
// the run output + the P1–P6 answers into this plan's §11 amendment log.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/runner"
)

// stubMCP is a minimal streamable-HTTP MCP server exposing one tool,
// ask_human. In parked mode a tools/call blocks with SSE heartbeats (the
// PARK-V1 short-park behavior); in answered mode it returns the answer
// immediately (the §E find-or-create fast path a fresh sidecar shows a
// resumed agent). The JSON-RPC-over-HTTP shape here is the P1/P4 measurement
// instrument, not a contract — adjust to what the PINNED CLI actually speaks
// if the initialize handshake fails (claude --debug shows the exchange).
type stubMCP struct {
	mu       sync.Mutex
	askCalls int
	answered bool
	answer   string
}

func (s *stubMCP) calls() int { s.mu.Lock(); defer s.mu.Unlock(); return s.askCalls }

func (s *stubMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	json.Unmarshal(body, &req)
	reply := func(result any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	switch req.Method {
	case "initialize":
		reply(map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "stub-platform", "version": "0.0.1"},
		})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		reply(map[string]any{"tools": []map[string]any{{
			"name":        "ask_human",
			"description": "Ask the human operator a question and wait for the answer.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"question": map[string]any{"type": "string"}},
				"required":   []string{"question"},
			},
		}}})
	case "tools/call":
		s.mu.Lock()
		s.askCalls++
		answered := s.answered
		s.mu.Unlock()
		if answered {
			reply(map[string]any{"content": []map[string]any{{"type": "text", "text": s.answer}}})
			return
		}
		// PARK: hold the call open, heartbeating, until the client
		// (SIGTERM'd claude) disconnects — delta §B3.
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Second):
				fmt.Fprint(w, ": heartbeat\n\n")
				if fl != nil {
					fl.Flush()
				}
			}
		}
	default:
		reply(map[string]any{})
	}
}

func writeMCPConfig(t *testing.T, dir, url string) string {
	t.Helper()
	path := filepath.Join(dir, "mcp.json")
	cfg := map[string]any{"mcpServers": map[string]any{
		"platform": map[string]any{"type": "http", "url": url},
	}}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// findSessionJSONL globs HOME/.claude/projects/*/*.jsonl and returns the
// newest file plus its session id (the basename). P2: the file must exist
// and grow incrementally while claude runs.
func findSessionJSONL(t *testing.T, home string) (string, string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", "*.jsonl"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no session JSONL under %s/.claude/projects (P2 RED): %v", home, err)
	}
	newest := matches[0]
	var newestMod time.Time
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.ModTime().After(newestMod) {
			newest, newestMod = m, fi.ModTime()
		}
	}
	return newest, strings.TrimSuffix(filepath.Base(newest), ".jsonl")
}

// assertPendingToolUse asserts the transcript's LAST assistant message
// carries an unresolved ask_human tool_use — the delta's transcript-safety
// claim (P2): the assistant message with the pending call is on disk BEFORE
// the MCP call resolves, so even SIGKILL cannot lose it.
func assertPendingToolUse(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lastAssistant string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, `"type":"assistant"`) || strings.Contains(line, `"role":"assistant"`) {
			lastAssistant = line
		}
	}
	if !strings.Contains(lastAssistant, "tool_use") || !strings.Contains(lastAssistant, "ask_human") {
		t.Fatalf("last assistant message lacks the pending ask_human tool_use (P2 RED):\n%s", lastAssistant)
	}
}

func TestLiveClaudeParkExitResume(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not on PATH")
	}

	stub := &stubMCP{answer: "ANSWER: option B — proceed."}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	// ---- (1) park: headless claude forced into ask_human against the stub ----
	home1, work1 := t.TempDir(), t.TempDir()
	prompt := "Call the ask_human tool with the question 'Which option should I take, A or B?'. " +
		"You MUST call the tool and wait for its answer before doing anything else. " +
		"After you receive the answer, reply with exactly the option letter it names and stop."

	cmd := exec.Command("claude", "-p", prompt,
		"--output-format", "stream-json", "--verbose", // P1: --verbose requirement measured here
		"--mcp-config", writeMCPConfig(t, work1, srv.URL),
		"--allowedTools", "mcp__platform__ask_human",
		"--dangerously-skip-permissions",
	)
	cmd.Dir = work1
	cmd.Env = append(os.Environ(), "HOME="+home1, "IS_SANDBOX=1")
	var out1 bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out1, &out1
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Minute)
	for stub.calls() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("claude never called ask_human:\n%s", out1.String())
		}
		time.Sleep(time.Second)
	}
	time.Sleep(5 * time.Second) // let the pending tool_use JSONL append flush (P2)

	// ---- (2) SIGTERM mid-call — the park-exit ----
	cmd.Process.Signal(syscall.SIGTERM)
	werr := cmd.Wait() // P3: exit code + final envelope deliberately NOT required
	t.Logf("P3: SIGTERM wait err=%v; stream tail:\n%s", werr, tail(out1.String(), 2000))

	sessionFile, sessionID := findSessionJSONL(t, home1)
	assertPendingToolUse(t, sessionFile)

	// ---- (3) fresh HOME/cwd + fresh ANSWERED stub; restore ONLY the JSONL ----
	stub.mu.Lock()
	stub.answered = true
	callsAtResume := stub.askCalls
	stub.mu.Unlock()

	home2, work2 := t.TempDir(), t.TempDir()
	dest := filepath.Join(home2, ".claude", "projects", runner.CwdSlug(work2), sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(sessionFile)
	if err := os.WriteFile(dest, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	resume := exec.Command("claude", "-p", runner.ContinuationPrompt,
		"--resume", sessionID,
		"--output-format", "stream-json", "--verbose",
		"--mcp-config", writeMCPConfig(t, work2, srv.URL), // P6: tools come from the LIVE config
		"--allowedTools", "mcp__platform__ask_human",
		"--dangerously-skip-permissions",
	)
	resume.Dir = work2
	resume.Env = append(os.Environ(), "HOME="+home2, "IS_SANDBOX=1")
	out2, err := resume.CombinedOutput()
	if err != nil {
		t.Fatalf("P4 RED — resume invocation failed (fall back to --agent-short-park-max=0): %v\n%s", err, out2)
	}

	// ---- (4) the gate: the pending tools/call was RE-ISSUED and answered ----
	if stub.calls() <= callsAtResume {
		t.Fatalf("P4 RED — resumed claude never re-issued ask_human:\n%s", out2)
	}

	// Terminal result event of the resumed stream (P1 field parity).
	var env struct {
		Type         string  `json:"type"`
		SessionID    string  `json:"session_id"`
		CostUSD      float64 `json:"cost_usd"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		NumTurns     int     `json:"num_turns"`
		IsError      bool    `json:"is_error"`
	}
	for _, line := range strings.Split(string(out2), "\n") {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &probe) == nil && probe.Type == "result" {
			json.Unmarshal([]byte(line), &env)
		}
	}
	if env.Type != "result" {
		t.Fatalf("P1 RED — no terminal result event in resumed stream:\n%s", out2)
	}
	if env.IsError {
		t.Fatalf("P4 RED — resumed claude ended in error:\n%s", out2)
	}
	if env.SessionID == sessionID {
		t.Logf("P4: resume PRESERVES the session id (%s)", sessionID)
	} else {
		t.Logf("P4: resume FORKS the session id (%s -> %s) — agent_run_step_state.session_id follows the LATEST envelope id", sessionID, env.SessionID)
	}
	t.Logf("P5: resumed envelope cost_usd=%v total_cost_usd=%v num_turns=%d — compare against phase-1 partial usage to classify per-invocation vs session-cumulative", env.CostUSD, env.TotalCostUSD, env.NumTurns)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
```

- [ ] Compile-check without the CLI: `go vet -tags live_claude ./agent/runner/` — expect clean (fix imports until it is).
- [ ] Run against the real pinned CLI (check `deploy/agent-runner/Dockerfile` for the pin; install that exact version locally if the local `claude --version` differs):

```
go test -tags live_claude -run '^TestLiveClaudeParkExitResume$' -v -count=1 -timeout 15m ./agent/runner/
```

- [ ] **Record the pin results in §11 of this plan** (paste the `t.Logf` P1–P6 answers verbatim): P1 whether `--verbose` was required and the result-event field parity; P2 the actual cwd-slug rule (fix `runner.CwdSlug` if it differed) and the pending-tool_use-on-disk confirmation; P3 SIGTERM exit behavior + whether any final envelope appeared; P4 re-issue confirmed + session-id preserve-or-fork; P5 cost semantics (if **cumulative**, Task 26's continuation ingestion must subtract the park-exit partial before appending the ledger row — flag it there); P6 tool availability from the live config.
- [ ] **GATE:** green ⇒ proceed with Tasks 21–27 in order. Red ⇒ still land Tasks 21–23 and 25 (additive/inert), set `CONCOURSE_AGENT_SHORT_PARK_MAX=0` in the theborg deploy values, mark Tasks 24/26/27 "deferred (pin red, <date>)" in this file, and record the failure mode in §11.
- [ ] Commit: `git add agent/runner && git commit -m "test(agent-runner): PARK-V2 gating pin — live claude park-exit/resume proof (delta sI, pins P1-P6)"`

---

### Task 21: Contract addendum + `agent/schema` PARK-V2 surface + metrics migration 1773106062

Accommodation #2's schema half (delta §F) plus the PARK-V2 env rows this plan owns. Lands regardless of the Task 20 outcome — all additive, inert until a sidecar ever writes `park.json`.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (flight-output/flight-prev/exit-code contract block + §11 log; the §8.1 env rows and §5 parked-ingestion exception are PRE-APPLIED by the frozen delta — verify, don't re-append)
- Modify: `agent/schema/results.go`, `agent/schema/status.go`, `agent/schema/event_payloads.go`, `agent/schema/metrics.go`
- Create: `atc/db/migration/migrations/1773106062_agent_run_metrics_parked.up.sql` / `.down.sql`
- Test: `agent/schema/results_test.go`, `status_test.go`, `event_payloads_test.go`, `metrics_test.go`

**Steps:**

- [ ] VERIFY (do not re-append) that the four §8.1 agent-step-owned env rows — `AGENT_PARK_EXIT_GRACE_SECONDS`, `AGENT_STREAM_LOG_MAX_LINE_BYTES`, `AGENT_SESSION_ID`, `AGENT_SESSION_FILE` — are already present in 00-shared-contracts.md §8.1: they were PRE-APPLIED by the frozen delta (2026-07-10). Re-appending them duplicates the table rows. The §5 ingestion-rule exception (a stream ending in `step.park` with no `step.end` ingests as status `parked`, not error) is likewise PRE-APPLIED in 00 §5 — verify it, don't re-add it.

- [ ] The 00 edits that DO still land in this task (none of these exist in 00 yet — `flight-prev` has zero mentions there): add the flight-output/exit-code contract block to 00-shared-contracts.md (flight-output conventions, beside §5/§8.1):

```markdown
- **Flight-output contract additions (PARK-V2 §F):** `flight/session.jsonl` (the claude session JSONL, copied by the runner at EVERY exit — normal or park) and `results.json.session_id` are additive members of the flight output. `flight/park.json` (written by the platform sidecar at threshold-crossing, delta §B1) rides the ingested flight artifact as park-exit provenance. `flight-prev` joins `flight` as a reserved artifact name (the continuation's restored-flight INPUT; a user-declared artifact named `flight-prev` is a validation error).
- **Exit-code registry:** agent-runner exit `86` = awaiting-human park-exit (frozen); checkpoint client exit `3` = parked-past-threshold (owned by 08).
```

  and log in §11 (recording the delta as a whole — the §8.1/§5 legs were pre-applied, this task lands the flight-output block): `- 2026-07-10 (agent-step, PARK-V2 seam delta): §8.1 gains AGENT_PARK_EXIT_GRACE_SECONDS / AGENT_STREAM_LOG_MAX_LINE_BYTES / AGENT_SESSION_ID / AGENT_SESSION_FILE; flight output gains session.jsonl + park.json + the flight-prev reserved name; §5 gains step.park/step.resume + the parked ingestion exception; §1.8 status CHECK gains 'parked' + session_id column (migration 1773106062); §1.14 agent_run_step_state (migration 1773106061). Consumers: dispatch renderer (continuation env), platform-mcp (park.json shape), scorecards (parked rows + (pipeline_run_id, step_name) cost aggregation note per decision 32).`
- [ ] Extend `agent/schema/status_test.go` (new entry in the existing table) and `agent/schema/results_test.go` (parked round-trip):

```go
			Entry("parked", schema.StatusParked, schema.RunStatusParked, false),
```

```go
	It("accepts status parked and round-trips session_id (PARK-V2 sF)", func() {
		r := schema.Results{
			SchemaVersion: "1.0", Status: schema.StatusParked, Confidence: 1,
			Summary: "awaiting human answer to question 7", Artifacts: []schema.Artifact{},
			SessionID: "abc-123",
		}
		Expect(r.Validate()).To(Succeed())
		data, err := json.Marshal(r)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(`"session_id":"abc-123"`))
	})
```

- [ ] Add payload specs to `agent/schema/event_payloads_test.go`:

```go
	It("marshals StepParkData and StepResumeData with snake_case keys (PARK-V2)", func() {
		data, err := json.Marshal(schema.StepParkData{
			StepName: "implement", QuestionID: 7, WaitSecondsAtExit: 1800, SessionID: "abc-123",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(string(data)).To(MatchJSON(`{"step_name":"implement","question_id":7,"wait_seconds_at_exit":1800,"session_id":"abc-123"}`))

		data, err = json.Marshal(schema.StepResumeData{StepName: "implement", SessionID: "abc-123", QuestionID: 7})
		Expect(err).ToNot(HaveOccurred())
		Expect(string(data)).To(MatchJSON(`{"step_name":"implement","session_id":"abc-123","question_id":7}`))
	})

	It("omits the new optional StepStart/StepEnd keys when unset (additive)", func() {
		data, _ := json.Marshal(schema.StepStartData{StepName: "s"})
		Expect(string(data)).ToNot(ContainSubstring("resumed"))
		Expect(string(data)).ToNot(ContainSubstring("replayed"))
		data, _ = json.Marshal(schema.StepEndData{StepName: "s", Status: "ok", Summary: "x"})
		Expect(string(data)).ToNot(ContainSubstring("session_id"))
	})
```

- [ ] Run `cd agent/schema && go test ./...` — expect compile failure.
- [ ] Implement, all additive:
  - `results.go`: add `StatusParked Status = "parked"` to the const block + `validStatuses`; add `SessionID string \`json:"session_id,omitempty"\`` to `Results`; extend the `Validate` error string to `"must be one of pass, fail, error, abstain, parked"`.
  - `status.go`: add `RunStatusParked = "parked"`; add `case StatusParked: return RunStatusParked, false` to `ThreeWayStatus`.
  - `event_payloads.go`: add constants `EventStepPark EventType = "step.park"` and `EventStepResume EventType = "step.resume"`; add to `StepStartData` the fields `Resumed bool \`json:"resumed,omitempty"\`` and `Replayed bool \`json:"replayed,omitempty"\``; add to `StepEndData` the field `SessionID string \`json:"session_id,omitempty"\``; add:

```go
// StepParkData is emitted by the agent-runner/exec at a park-exit (PARK-V2
// §B/§F): the step ended awaiting a human, the run enters awaiting_human.
type StepParkData struct {
	StepName          string `json:"step_name"`
	QuestionID        int    `json:"question_id"`
	WaitSecondsAtExit int    `json:"wait_seconds_at_exit"`
	SessionID         string `json:"session_id"`
}

// StepResumeData is emitted by the agent-runner at a continuation start.
type StepResumeData struct {
	StepName   string `json:"step_name"`
	SessionID  string `json:"session_id"`
	QuestionID int    `json:"question_id,omitempty"`
}
```

  - `metrics.go`: add `SessionID string \`json:"session_id,omitempty"\`` to `RunMetrics` (between `CostUSD` and `Results`).
- [ ] Run `cd agent/schema && go test ./...` — expect pass.
- [ ] Write `1773106062_agent_run_metrics_parked.up.sql` (agent-step migration block; the inline CHECK in 1773106060 gets the default constraint name `agent_run_metrics_status_check`):

```sql
ALTER TABLE agent_run_metrics DROP CONSTRAINT agent_run_metrics_status_check;
ALTER TABLE agent_run_metrics ADD CONSTRAINT agent_run_metrics_status_check
    CHECK (status IN ('ok','failed','error','parked'));
ALTER TABLE agent_run_metrics ADD COLUMN session_id TEXT NOT NULL DEFAULT '';
```

  and `.down.sql`:

```sql
ALTER TABLE agent_run_metrics DROP COLUMN session_id;
DELETE FROM agent_run_metrics WHERE status = 'parked';
ALTER TABLE agent_run_metrics DROP CONSTRAINT agent_run_metrics_status_check;
ALTER TABLE agent_run_metrics ADD CONSTRAINT agent_run_metrics_status_check
    CHECK (status IN ('ok','failed','error'));
```

- [ ] Extend the Task 8 factory: add `session_id` to the upsert column list (+ `EXCLUDED.session_id` in the DO UPDATE), the scan list, and one round-trip assertion in the factory suite (`rm.SessionID = "abc-123"` in, same out).
- [ ] Run `ginkgo ./atc/db/migration/ && ginkgo ./atc/db/` — expect pass.
- [ ] Commit: `git add docs agent/schema atc/db && git commit -m "feat(agent-schema): PARK-V2 surface — parked status, session_id, step.park/step.resume events (delta sF, migration 1773106062)"`

---

### Task 22: ci-agent session_id capture — `CallResult.SessionID` + `step.end` payload

Stop discarding the already-parsed envelope field (`ci-agent/llm/result.go:51` parses `session_id` into `cliEnvelope` and drops it). Lands regardless of the pin. Note the ci-agent module has its OWN schema import (agent/schema, post-Task 3) — no new types needed here.

**Files:**
- Modify: `ci-agent/llm/result.go`, `ci-agent/llm/result_test.go`
- Modify: `ci-agent/phaserunner/runner.go` (the `step.end` emitter — `grep -n "EventStepEnd\|step.end" ci-agent/phaserunner/runner.go` for the exact payload-construction site)

**Steps:**

- [ ] Add to `ci-agent/llm/result_test.go`:

```go
func TestParseCLIEnvelopeCapturesSessionID(t *testing.T) {
	res := ParseCLIEnvelope([]byte(`{"type":"result","result":"\"ok\"","session_id":"sess-42","cost_usd":0.1}`))
	if res.SessionID != "sess-42" {
		t.Fatalf("expected session id sess-42, got %q", res.SessionID)
	}
}
```

- [ ] Run `cd ci-agent && go test ./llm/` — expect compile failure (`res.SessionID` undefined).
- [ ] In `ci-agent/llm/result.go`: add to `CallResult`:

```go
	// SessionID is the claude session id from the CLI envelope (PARK-V2 §F).
	SessionID string
```

  and add `SessionID: env.SessionID,` to the `CallResult` literal in `ParseCLIEnvelope`.
- [ ] In `ci-agent/phaserunner/runner.go`, add `"session_id"` (from the phase's `CallResult.SessionID`) to the `step.end` event payload it emits — matching the additive `StepEndData.SessionID` key from Task 21. Extend the nearest phaserunner test fixture asserting the `step.end` payload.
- [ ] Run `cd ci-agent && go test ./... -count=1` — expect pass.
- [ ] Commit: `git add ci-agent && git commit -m "feat(ci-agent): capture claude session_id into CallResult and step.end (PARK-V2 sF)"`

---

### Task 23: `agent-runner` — stream-json tee + session JSONL capture (accommodations #1 + #2)

Delta §G + §F, runner half. Switch from `--output-format json` to `--output-format stream-json`: tee each NDJSON line to stdout (the existing build-event fabric — `fly watch` becomes a live transcript) with the `AGENT_STREAM_LOG_MAX_LINE_BYTES` truncation guard applied ON THE TEE ONLY; parse the stream to (a) accumulate best-effort usage/cost (the park-exit partial envelope, Task 24 consumes it) and (b) capture the terminal `result` event as THE envelope for `results.json`. Copy the session JSONL to `flight/session.jsonl` at EVERY exit. Lands regardless of the pin (pin P1 refines the flag shape — apply its recorded answer here).

**Files:**
- Create: `agent/runner/stream.go`
- Modify: `agent/runner/runner.go`, `agent/runner/envelope.go`
- Test: `agent/runner/runner_test.go`, `agent/runner/stream_test.go`

**Steps:**

- [ ] Write `agent/runner/stream_test.go`:

```go
package runner

import (
	"bytes"
	"strings"
	"testing"
)

func TestConsumeStreamTeesTruncatesAndCapturesEnvelope(t *testing.T) {
	long := strings.Repeat("x", 100)
	in := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-1"}`,
		`{"type":"assistant","message":{"usage":{"input_tokens":100,"output_tokens":50}},"session_id":"sess-1","pad":"` + long + `"}`,
		`{"type":"result","subtype":"success","result":"\"done\"","session_id":"sess-1","cost_usd":0.42,"num_turns":9,"is_error":false,"usage":{"input_tokens":120,"output_tokens":60}}`,
	}, "\n")

	var tee bytes.Buffer
	st := consumeStream(strings.NewReader(in), &tee, 80)

	if st.envelope == nil {
		t.Fatal("terminal result event not captured")
	}
	if st.envelope.SessionID != "sess-1" || st.envelope.costUSD() != 0.42 {
		t.Fatalf("envelope mismatch: %+v", st.envelope)
	}
	if st.sessionID != "sess-1" {
		t.Fatalf("session id not captured from stream: %q", st.sessionID)
	}
	if st.usage.InputTokens != 100 || st.usage.OutputTokens != 50 {
		t.Fatalf("partial usage not accumulated: %+v", st.usage)
	}
	// tee truncated the long line; the PARSER saw it whole (usage above proves it)
	if !strings.Contains(tee.String(), "…[truncated") {
		t.Fatalf("expected tee truncation marker, got:\n%s", tee.String())
	}
	// short lines pass through verbatim
	if !strings.Contains(tee.String(), `"subtype":"init"`) {
		t.Fatalf("short line not teed verbatim:\n%s", tee.String())
	}
}
```

- [ ] Run `go test ./agent/runner/` — expect compile failure.
- [ ] Write `agent/runner/stream.go`:

```go
package runner

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// streamEvent is one NDJSON line of `claude --output-format stream-json`.
// Only the fields the runner consumes; unknown keys are ignored. Pin P1
// records the exact shape at the pinned CLI version.
type streamEvent struct {
	Type      string `json:"type"` // system | assistant | user | result
	SessionID string `json:"session_id"`
	Message   struct {
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type streamState struct {
	sessionID string
	turns     int
	usage     struct {
		InputTokens              int64
		OutputTokens             int64
		CacheReadInputTokens     int64
		CacheCreationInputTokens int64
	}
	envelope *cliEnvelope // terminal result event; nil when claude died first (P3)
}

// consumeStream tees every NDJSON line to logw — truncating individual lines
// longer than maxLine ON THE TEE ONLY (large tool_results are the offender;
// PARK-V2 §G log-volume guard) — while the parser always sees the full line.
// It accumulates best-effort usage for the park-exit partial envelope and
// captures the terminal result event as THE envelope for results.json.
func consumeStream(r io.Reader, logw io.Writer, maxLine int) *streamState {
	st := &streamState{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 32<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if maxLine > 0 && len(line) > maxLine {
			fmt.Fprintf(logw, "%s…[truncated %d bytes]\n", line[:maxLine], len(line)-maxLine)
		} else {
			logw.Write(line)
			logw.Write([]byte("\n"))
		}

		var ev streamEvent
		if json.Unmarshal(line, &ev) != nil {
			continue // non-JSON noise stays teed, never parsed
		}
		if ev.SessionID != "" {
			st.sessionID = ev.SessionID
		}
		switch ev.Type {
		case "assistant":
			st.turns++
			st.usage.InputTokens += ev.Message.Usage.InputTokens
			st.usage.OutputTokens += ev.Message.Usage.OutputTokens
			st.usage.CacheReadInputTokens += ev.Message.Usage.CacheReadInputTokens
			st.usage.CacheCreationInputTokens += ev.Message.Usage.CacheCreationInputTokens
		case "result":
			var env cliEnvelope
			if json.Unmarshal(line, &env) == nil {
				st.envelope = &env
			}
		}
	}
	return st
}
```

- [ ] Amend `agent/runner/envelope.go`: add `SessionID string \`json:"session_id"\`` to `cliEnvelope` (parity with ci-agent/llm/result.go:51).
- [ ] Amend `agent/runner/runner.go`:
  - `Config` gains `MaxLogLineBytes int` (0 ⇒ default 16384) and `HomeDir string` (empty ⇒ `os.UserHomeDir()`; tests override); `FromEnv` reads `AGENT_STREAM_LOG_MAX_LINE_BYTES`.
  - Step 4's args become `-p <prompt> --output-format stream-json --verbose` (drop `--output-format json`; keep `--model`/`--max-turns`/`--dangerously-skip-permissions`/`--mcp-config` exactly as before; adjust `--verbose` per the recorded P1 answer). Wire `cmd.StdoutPipe()` into `st := consumeStream(pipe, cfg.Stdout, cfg.MaxLogLineBytes)` run on the calling goroutine between `cmd.Start()` and `cmd.Wait()`.
  - Step 5 becomes: `env := st.envelope`; when nil (claude died without a terminal event — SIGTERM/SIGKILL, pin P3) synthesize a partial envelope from `st.usage`/`st.turns` with `CostUSD: 0` and note `"no terminal result event"` — the runner MUST NOT require the envelope.
  - Step 6/7: `results.json` gains `SessionID: st.sessionID`; `step.end` event data gains `SessionID: st.sessionID`.
  - New helper, called at EVERY exit path (normal, error, and Task 24's park-exit) right before `results.json` is written:

```go
// copySessionJSONL copies the claude session transcript into the flight
// output (PARK-V2 §F): flight/session.jsonl rides the artifact fabric so a
// continuation build can restore it. Best-effort — a missing transcript is
// logged into the events stream, never fatal.
func copySessionJSONL(cfg Config, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("no session id observed on the stream")
	}
	src := filepath.Join(cfg.homeDir(), ".claude", "projects", CwdSlug(cfg.WorkDir), sessionID+".jsonl")
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cfg.FlightDir, "session.jsonl"), raw, 0o644)
}
```

- [ ] Update `agent/runner/runner_test.go`: the stub claude scripts now emit NDJSON streams (an `init` line with `session_id`, an `assistant` line, a `result` line) instead of a single envelope; each test pre-creates `<home>/.claude/projects/<CwdSlug(workdir)>/<session-id>.jsonl` and sets `cfg.HomeDir`; new assertions — `results.json` carries `session_id`, `flight/session.jsonl` exists with the fixture content, and the events `step.end` data carries `session_id`.
- [ ] Run `go test ./agent/runner/ && go build ./cmd/agent-runner` — expect pass.
- [ ] Commit: `git add agent/runner && git commit -m "feat(agent-runner): stream-json live tee with truncation guard + session JSONL capture (PARK-V2 sF/sG)"`

---

### Task 24: `agent-runner` — park-exit watcher, exit 86, and `--resume` continuation mode **[GATED on Task 20 green]**

Delta §B2 + §D, runner half. While claude runs, a watcher goroutine stats `flight/park.json` every 5s (sentinel file, atomically written by the platform sidecar — 08's half; it persists across sidecar crashes and rides the flight artifact as provenance). On appearance: SIGTERM claude → wait `AGENT_PARK_EXIT_GRACE_SECONDS` (default 30) → SIGKILL. Transcript safety is pins P2/P3: the pending `tool_use` is on disk before the MCP call resolves, and no final envelope is required. Then: copy the session JSONL, write `results.json` status `parked` + `session_id` + best-effort partial usage, emit `step.park`, exit **86**. Resume mode: `AGENT_SESSION_ID`/`AGENT_SESSION_FILE` install the JSONL under `~/.claude/projects/<cwd-slug>/` and invoke `claude -p --resume <id>` with the frozen `runner.ContinuationPrompt`, emitting `step.resume`.

**Files:**
- Create: `agent/runner/park.go`
- Modify: `agent/runner/runner.go`
- Test: `agent/runner/park_test.go`

**Steps:**

- [ ] Write `agent/runner/park_test.go`:

```go
package runner_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/runner"
	schema "github.com/concourse/concourse/agent/schema"
)

// stub claude that traps TERM and would otherwise run for a minute — the
// park-exit must come from the watcher, not the stub finishing. It emits an
// init stream event first so the runner observes the session id.
const parkStub = `#!/bin/sh
echo '{"type":"system","subtype":"init","session_id":"sess-park"}'
trap 'exit 143' TERM
sleep 60
`

func TestParkExitWritesParkedResultsAndExits86(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)
	home := t.TempDir()

	// pre-create the session transcript the runner must capture
	proj := filepath.Join(home, ".claude", "projects", runner.CwdSlug(dir))
	os.MkdirAll(proj, 0o755)
	os.WriteFile(filepath.Join(proj, "sess-park.jsonl"), []byte(`{"role":"assistant"}`+"\n"), 0o644)

	claude := filepath.Join(dir, "claude")
	os.WriteFile(claude, []byte(parkStub), 0o755)

	// the sidecar's threshold-crossing signal appears 2s in (delta §B1 payload)
	go func() {
		time.Sleep(2 * time.Second)
		park := map[string]any{
			"question_id": 7, "kind": "question", "step_name": "write-spec",
			"asked_at": "2026-07-10T12:00:00Z", "threshold_seconds": 1800,
			"crossed_at": "2026-07-10T12:30:00Z",
		}
		raw, _ := json.Marshal(park)
		tmp := filepath.Join(flight, ".park.json.tmp")
		os.WriteFile(tmp, raw, 0o644)
		os.Rename(tmp, filepath.Join(flight, "park.json")) // atomic, like the sidecar's mv
	}()

	exit, err := runner.Run(context.Background(), runner.Config{
		Prompt: "p", FlightDir: flight, WorkDir: dir, StepName: "write-spec",
		ClaudePath: claude, HomeDir: home, ParkExitGraceSeconds: 5,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if exit != runner.ExitAwaitingHuman {
		t.Fatalf("expected exit %d, got %d", runner.ExitAwaitingHuman, exit)
	}

	var results schema.Results
	raw, _ := os.ReadFile(filepath.Join(flight, "results.json"))
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	if results.Status != schema.StatusParked || results.SessionID != "sess-park" {
		t.Fatalf("expected parked/sess-park, got %s/%s", results.Status, results.SessionID)
	}
	if _, err := os.Stat(filepath.Join(flight, "session.jsonl")); err != nil {
		t.Fatalf("session.jsonl not captured at park-exit: %v", err)
	}

	// event stream ends in step.park with the question id — the sanctioned
	// missing-step.end exception (contracts §5)
	events, _ := os.ReadFile(filepath.Join(flight, "events.ndjson"))
	lines := strings.Split(strings.TrimSpace(string(events)), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, `"step.park"`) || !strings.Contains(last, `"question_id":7`) {
		t.Fatalf("expected terminal step.park event, got: %s", last)
	}
	if strings.Contains(string(events), `"step.end"`) {
		t.Fatalf("park-exit must not emit step.end:\n%s", events)
	}
}

func TestResumeModeInstallsSessionAndPassesResumeFlag(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	os.MkdirAll(flight, 0o755)
	home := t.TempDir()

	// restored flight-prev input carrying the prior session transcript
	prev := filepath.Join(dir, "flight-prev")
	os.MkdirAll(prev, 0o755)
	os.WriteFile(filepath.Join(prev, "session.jsonl"), []byte(`{"role":"assistant"}`+"\n"), 0o644)

	// stub claude that records its argv and emits a full happy stream
	argsFile := filepath.Join(dir, "argv")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n" +
		`echo '{"type":"system","subtype":"init","session_id":"sess-park"}'` + "\n" +
		`echo '{"type":"result","subtype":"success","result":"\"done\"","session_id":"sess-park","cost_usd":0.1,"num_turns":2,"is_error":false,"usage":{"input_tokens":10,"output_tokens":5}}'` + "\n"
	claude := filepath.Join(dir, "claude")
	os.WriteFile(claude, []byte(stub), 0o755)

	exit, err := runner.Run(context.Background(), runner.Config{
		Prompt: "ignored on resume", FlightDir: flight, WorkDir: dir, StepName: "write-spec",
		ClaudePath: claude, HomeDir: home,
		SessionID: "sess-park", SessionFile: filepath.Join(prev, "session.jsonl"),
	})
	if err != nil || exit != 0 {
		t.Fatalf("resume run: exit=%d err=%v", exit, err)
	}

	// transcript installed where --resume looks for it
	installed := filepath.Join(home, ".claude", "projects", runner.CwdSlug(dir), "sess-park.jsonl")
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("session file not installed: %v", err)
	}

	argv, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(argv), "--resume") || !strings.Contains(string(argv), "sess-park") {
		t.Fatalf("claude not invoked with --resume sess-park:\n%s", argv)
	}
	if !strings.Contains(string(argv), runner.ContinuationPrompt) {
		t.Fatalf("continuation prompt not passed:\n%s", argv)
	}

	// first event is step.resume
	events, _ := os.ReadFile(filepath.Join(flight, "events.ndjson"))
	if !strings.Contains(strings.SplitN(string(events), "\n", 2)[0], `"step.resume"`) {
		t.Fatalf("expected leading step.resume event:\n%s", events)
	}
}
```

- [ ] Run `go test ./agent/runner/` — expect compile failure (`ParkExitGraceSeconds`/`SessionID`/`SessionFile` undefined).
- [ ] Write `agent/runner/park.go`:

```go
package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// parkSignal is the sidecar-written flight/park.json payload (delta §B1).
// The sidecar writes it atomically (temp + mv), so a partial read is
// impossible; the file rides the ingested flight artifact as provenance.
type parkSignal struct {
	QuestionID       int    `json:"question_id"`
	Kind             string `json:"kind"` // question | checkpoint
	StepName         string `json:"step_name"`
	AskedAt          string `json:"asked_at"`
	ThresholdSeconds int    `json:"threshold_seconds"`
	CrossedAt        string `json:"crossed_at"`
}

// watchPark polls flight/park.json every 5s while claude runs (delta §B2).
// On appearance it stores the signal, SIGTERMs claude, and SIGKILLs after
// the grace period unless claude exited first (done closes).
func watchPark(ctx context.Context, done <-chan struct{}, flightDir string, proc *os.Process, grace time.Duration, out chan<- parkSignal) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-tick.C:
			raw, err := os.ReadFile(filepath.Join(flightDir, "park.json"))
			if err != nil {
				continue
			}
			var sig parkSignal
			if json.Unmarshal(raw, &sig) != nil {
				continue
			}
			out <- sig
			proc.Signal(syscall.SIGTERM)
			select {
			case <-done: // exited within grace
			case <-time.After(grace):
				proc.Kill()
			}
			return
		}
	}
}
```

- [ ] Amend `agent/runner/runner.go`:
  - `Config` gains `ParkExitGraceSeconds int` (0 ⇒ 30; env `AGENT_PARK_EXIT_GRACE_SECONDS`), `SessionID string` (env `AGENT_SESSION_ID`), `SessionFile string` (env `AGENT_SESSION_FILE`) — the latter two set only on continuation builds by the exec (Task 26).
  - **Resume mode** (before building args): when `cfg.SessionID != ""` — copy `cfg.SessionFile` to `filepath.Join(cfg.homeDir(), ".claude", "projects", CwdSlug(cfg.WorkDir), cfg.SessionID+".jsonl")` (MkdirAll first; a missing SessionFile is exit 2 with a clear error — the exec always supplies it); replace the prompt with `ContinuationPrompt` and prepend `--resume <cfg.SessionID>` to the args; write `EventStepResume` (`schema.StepResumeData{StepName: cfg.StepName, SessionID: cfg.SessionID}`) as the FIRST event; pass `Resumed: true` on the `StepStartData`.
  - **Watcher wiring** (around `cmd.Wait()`): `parkCh := make(chan parkSignal, 1)`; `done := make(chan struct{})`; `go watchPark(ctx, done, cfg.FlightDir, cmd.Process, cfg.grace(), parkCh)`; after `consumeStream` + `cmd.Wait()` return, `close(done)`.
  - **Park-exit path**: `select { case sig := <-parkCh: … default: }` — when a signal was consumed: write `EventCostRecord` from `st.usage`/`st.turns` (best-effort partial; nil envelope is EXPECTED — pin P3, never require one); write `EventStepPark` (`schema.StepParkData{StepName: cfg.StepName, QuestionID: sig.QuestionID, WaitSecondsAtExit: waitSeconds(sig), SessionID: st.sessionID}`) as the TERMINAL event (NO `step.end` — the sanctioned §5 exception); `copySessionJSONL(cfg, st.sessionID)`; write `results.json` with `Status: schema.StatusParked`, `SessionID: st.sessionID`, `Summary: fmt.Sprintf("awaiting human answer to question %d", sig.QuestionID)`; return `(ExitAwaitingHuman, nil)`. `waitSeconds` parses `sig.AskedAt`/`sig.CrossedAt` (RFC3339) and falls back to `sig.ThresholdSeconds` on parse failure.
  - `cmd/agent-runner/main.go` is unchanged — `Run` returns 86 with a nil error and `os.Exit(86)` propagates it as the pod's exit status (the jetbridge supervisor surfaces it as the process `ExitStatus` the exec matches on).
- [ ] Run `go test ./agent/runner/ && go build ./cmd/agent-runner` — expect pass.
- [ ] Commit: `git add agent/runner && git commit -m "feat(agent-runner): park-exit watcher (SIGTERM/grace/SIGKILL, exit 86) and --resume continuation mode (PARK-V2 sB2/sD)"`

---

### Task 25: Migration 1773106061 — `agent_run_step_state` + `db.AgentRunStepStateFactory`

Contracts §1.14 (delta §B6), DDL verbatim. The durable per-logical-step record the continuation build consults: `state='completed'` rows short-circuit-replay, `state='awaiting_human'` rows resume. Additive and inert at `--agent-short-park-max=0` (the table only gains rows once Task 26 lands) — lands regardless of the pin.

**Files:**
- Create: `atc/db/migration/migrations/1773106061_create_agent_run_step_state.up.sql` / `.down.sql`
- Create: `atc/db/agent_run_step_state_factory.go`
- Create: `atc/db/dbfakes/fake_agent_run_step_state_factory.go` (counterfeiter)
- Test: `atc/db/agent_run_step_state_factory_test.go`

**Steps:**

- [ ] Write `1773106061_create_agent_run_step_state.up.sql` (delta §B6 verbatim):

```sql
CREATE TABLE agent_run_step_state (
    id              BIGSERIAL PRIMARY KEY,
    pipeline_run_id INTEGER NOT NULL,
    step_name       TEXT NOT NULL,
    state           TEXT NOT NULL CHECK (state IN ('completed','awaiting_human')),
    build_id        INTEGER NOT NULL,            -- build that produced this state
    session_id      TEXT NOT NULL DEFAULT '',    -- latest claude session id for this step
    question_id     INTEGER,                     -- open agent_run_questions row at park-exit
    artifacts       JSONB NOT NULL DEFAULT '{}', -- {"workspace": "<fabric handle>", "flight": "<handle>", ...}
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (pipeline_run_id, step_name)
);
```

  and `.down.sql`: `DROP TABLE agent_run_step_state;`
- [ ] Write the Ginkgo factory suite (template-DB recipe, mirror `agent_reviews_factory` / Task 8's suite): upsert-inserts, upsert-updates-in-place on `(pipeline_run_id, step_name)` conflict (state flips `awaiting_human` → `completed`, artifacts replaced), `Find` returns `(state, true, nil)` / `(zero, false, nil)`, `question_id` NULL round-trip.
- [ ] Run `ginkgo --focus="AgentRunStepState" ./atc/db/` — expect compile failure.
- [ ] Write `atc/db/agent_run_step_state_factory.go` (squirrel + counterfeiter recipe, per `atc/db/agent_reviews_factory.go`):

```go
package db

import (
	"database/sql"
	"encoding/json"

	sq "github.com/Masterminds/squirrel"
)

// AgentRunStepState is one logical agent step's latest state within a
// pipeline run (PARK-V2 §B6, contracts §1.14). Continuation builds consult
// it keyed (pipeline_run_id, step_name): completed ⇒ zero-cost replay,
// awaiting_human ⇒ --resume, absent ⇒ cold run.
type AgentRunStepState struct {
	PipelineRunID int
	StepName      string
	State         string // completed | awaiting_human
	BuildID       int
	SessionID     string
	QuestionID    *int
	Artifacts     map[string]string // artifact name -> fabric volume handle
}

const (
	AgentStepStateCompleted     = "completed"
	AgentStepStateAwaitingHuman = "awaiting_human"
)

//counterfeiter:generate . AgentRunStepStateFactory
type AgentRunStepStateFactory interface {
	Upsert(state AgentRunStepState) error
	Find(pipelineRunID int, stepName string) (AgentRunStepState, bool, error)
}

type agentRunStepStateFactory struct {
	conn DbConn
}

func NewAgentRunStepStateFactory(conn DbConn) AgentRunStepStateFactory {
	return &agentRunStepStateFactory{conn: conn}
}

func (f *agentRunStepStateFactory) Upsert(s AgentRunStepState) error {
	artifacts, err := json.Marshal(s.Artifacts)
	if err != nil {
		return err
	}
	_, err = psql.Insert("agent_run_step_state").
		Columns("pipeline_run_id", "step_name", "state", "build_id", "session_id", "question_id", "artifacts").
		Values(s.PipelineRunID, s.StepName, s.State, s.BuildID, s.SessionID, s.QuestionID, artifacts).
		Suffix(`ON CONFLICT (pipeline_run_id, step_name) DO UPDATE SET
			state = EXCLUDED.state,
			build_id = EXCLUDED.build_id,
			session_id = EXCLUDED.session_id,
			question_id = EXCLUDED.question_id,
			artifacts = EXCLUDED.artifacts,
			created_at = now()`).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *agentRunStepStateFactory) Find(pipelineRunID int, stepName string) (AgentRunStepState, bool, error) {
	var (
		s         AgentRunStepState
		artifacts []byte
	)
	err := psql.Select("pipeline_run_id", "step_name", "state", "build_id", "session_id", "question_id", "artifacts").
		From("agent_run_step_state").
		Where(sq.Eq{"pipeline_run_id": pipelineRunID, "step_name": stepName}).
		RunWith(f.conn).
		QueryRow().
		Scan(&s.PipelineRunID, &s.StepName, &s.State, &s.BuildID, &s.SessionID, &s.QuestionID, &artifacts)
	if err == sql.ErrNoRows {
		return AgentRunStepState{}, false, nil
	}
	if err != nil {
		return AgentRunStepState{}, false, err
	}
	if err := json.Unmarshal(artifacts, &s.Artifacts); err != nil {
		return AgentRunStepState{}, false, err
	}
	return s, true, nil
}
```

  (`QuestionID *int` scans NULL directly; if the suite's `Scan` balks, use `sql.NullInt64` internally — match whatever `agent_reviews_factory` does for its nullable columns.)
- [ ] Regenerate fakes: `go generate ./atc/db/...`
- [ ] Run `ginkgo ./atc/db/migration/ && ginkgo --focus="AgentRunStepState" ./atc/db/` — expect pass.
- [ ] Commit: `git add atc/db && git commit -m "feat(db): agent_run_step_state table + factory (migration 1773106061, PARK-V2 sB6)"`

---

### Task 26: `exec.AgentStep` — exit-86 distinguished end, step-state upserts, continuation replay/resume, parked ingestion **[GATED on Task 20 green]**

Delta §B5/§B6/§D, exec half. No fifth Concourse BUILD status: exit 86 fails the build **as a carrier only**; the distinguished end is (1) exit 86, (2) the additive `awaiting_human` build event + the `step.park` flight event, (3) the `agent_run_step_state` row. The platform's authority is the OPEN park-policy question row (08/11's half) — this exec never reads or writes run status. A pod that dies while parked leaves the open row + step-state row, so the park now survives pod death (an improvement over PARK-V1).

**Files:**
- Modify: `atc/exec/agent_step.go`, `atc/exec/agent_step_test.go`
- Modify: `atc/exec/task_step.go` (:60 region — `TaskDelegate` interface gains `AwaitingHuman`)
- Modify: `atc/event/events.go`, `atc/event/types.go`, `atc/event/parser.go` (additive event type)
- Modify: `atc/engine/task_delegate.go` (implement `AwaitingHuman`), `atc/engine/step_factory.go` + `atc/atccmd/command.go` (wire `WithAgentStepStateFactory` / `engine.WithAgentStepStateFactory(db.NewAgentRunStepStateFactory(dbConn))`)
- Modify: `atc/step_validator.go` (reserve `flight-prev` alongside `flight`)
- Regenerate: `atc/exec/execfakes`, `atc/engine/enginefakes`
- Test: `atc/exec/agent_step_test.go`, `atc/event/` suite

**Steps:**

- [ ] Add the additive build event (`atc/event/events.go`, constant in `types.go`, registration in `parser.go` mirroring an existing 1.0 event):

```go
// AwaitingHuman marks an agent step that exited to await a human answer
// (PARK-V2 §B5). The build finishes failed as a carrier; this event is one
// leg of the distinguished end (with runner exit 86 and the
// agent_run_step_state row). Consumers derive "awaiting human" from run
// state + open questions, never from build status.
type AwaitingHuman struct {
	Time       int64  `json:"time"`
	Origin     Origin `json:"origin"`
	QuestionID int    `json:"question_id"`
	SessionID  string `json:"session_id,omitempty"`
}

func (AwaitingHuman) EventType() atc.EventType  { return EventTypeAwaitingHuman }
func (AwaitingHuman) Version() atc.EventVersion { return "1.0" }
```

  Verify the Elm event decoder tolerates unknown event types before shipping (unlike the PLAN decoder from Task 17, the event stream decoder is expected to skip unknowns — if it is strict, add a minimal ignore-case the way Task 17 added the plan case).
- [ ] Add `AwaitingHuman(logger lager.Logger, questionID int, sessionID string)` to the `TaskDelegate` interface (task_step.go:60 region), implement in `atc/engine/task_delegate.go` via `d.build.SaveEvent(event.AwaitingHuman{Time: time.Now().Unix(), Origin: …, QuestionID: questionID, SessionID: sessionID})` (mirror `Finished`'s save-and-log shape), regenerate `execfakes`/`enginefakes`.
- [ ] Write the failing specs in `atc/exec/agent_step_test.go` (fixtures: `fakeStepStateFactory *dbfakes.FakeAgentRunStepStateFactory` via new option `exec.WithAgentStepStateFactory`; plan env carries `AGENT_PIPELINE_RUN_ID=42`):

```go
	Context("PARK-V2", func() {
		It("appends PLATFORM_MCP_PARK_PATH to the platform sidecar env (delta §B1 producer, F15)", func() {
			// plan.Sidecars includes platform (Task 12 fixtures). The flight
			// output always exists (step 10 registers it unconditionally), so
			// the row is unconditional for agent steps; checkpoint pods never
			// get it (dispatch's renderer does not emit it) — unset = never
			// write, per 08 Task 9c.
			step.Run(ctx, state)
			_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
			Expect(spec.SidecarEnv["platform"]).To(ContainElement(
				SatisfyAll(HavePrefix("PLATFORM_MCP_PARK_PATH="), HaveSuffix("/flight/park.json"))))
		})

		It("upserts state=completed with output handles at a normal end", func() {
			_, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			s := fakeStepStateFactory.UpsertArgsForCall(0)
			Expect(s.State).To(Equal(db.AgentStepStateCompleted))
			Expect(s.PipelineRunID).To(Equal(42))
			Expect(s.StepName).To(Equal("write-spec"))
			Expect(s.Artifacts).To(HaveKey("workspace"))
			Expect(s.Artifacts).To(HaveKey("flight"))
		})

		It("treats exit 86 as the distinguished awaiting-human end", func() {
			// process stub exits 86; flight fixtures: results.json status
			// "parked" + session_id "sess-1"; events end in step.park;
			// park.json carries question_id 7
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeFalse()) // failed build = carrier only

			rm := fakeMetricsStore.UpsertReturningInsertedArgsForCall(0)
			Expect(rm.Status).To(Equal("parked")) // NOT error, despite no step.end
			Expect(rm.SessionID).To(Equal("sess-1"))
			Expect(fakeChecker.RecordCallCount()).To(Equal(1)) // partial spend ledgered (F3 gate)

			s := fakeStepStateFactory.UpsertArgsForCall(0)
			Expect(s.State).To(Equal(db.AgentStepStateAwaitingHuman))
			Expect(s.SessionID).To(Equal("sess-1"))
			Expect(*s.QuestionID).To(Equal(7))
			Expect(s.Artifacts).To(HaveKey("flight"))

			_, qid, sid := fakeDelegate.AwaitingHumanArgsForCall(0)
			Expect(qid).To(Equal(7))
			Expect(sid).To(Equal("sess-1"))
		})

		It("short-circuit-replays a completed step without selecting a worker", func() {
			fakeStepStateFactory.FindReturns(db.AgentRunStepState{
				State: db.AgentStepStateCompleted,
				Artifacts: map[string]string{"workspace": "vol-ws", "flight": "vol-fl"},
			}, true, nil)
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(fakePool.FindOrSelectWorkerCallCount()).To(BeZero()) // zero claude, zero cost
			Expect(fakeChecker.StepSliceCallCount()).To(BeZero())       // no admission for a replay
			_, found := state.ArtifactRepository().ArtifactFor(build.ArtifactName("workspace"))
			Expect(found).To(BeTrue())
		})

		It("resumes an awaiting_human step with session env and restored inputs", func() {
			qid := 7
			fakeStepStateFactory.FindReturns(db.AgentRunStepState{
				State: db.AgentStepStateAwaitingHuman, SessionID: "sess-1", QuestionID: &qid,
				Artifacts: map[string]string{"workspace": "vol-ws", "flight": "vol-fl"},
			}, true, nil)
			step.Run(ctx, state)
			_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
			Expect(spec.Env).To(ContainElement("AGENT_SESSION_ID=sess-1"))
			Expect(spec.Env).To(ContainElement(HaveSuffix("/flight-prev/session.jsonl")))
			Expect(spec.Inputs).To(ContainElement(HaveField("DestinationPath", HaveSuffix("/flight-prev"))))
			// StepSlice re-resolved at every execution (decision 32) — never skipped on resume
			Expect(fakeChecker.StepSliceCallCount()).To(Equal(1))
		})
	})
```

- [ ] Run `ginkgo --focus="PARK-V2" ./atc/exec/` — expect failure.
- [ ] Implement in `atc/exec/agent_step.go`:
  - `const agentExitAwaitingHuman = 86` (mirrors `runner.ExitAwaitingHuman`; defined locally so exec does not import the runner) and option `WithAgentStepStateFactory(f db.AgentRunStepStateFactory)`.
  - **Park-sentinel path env (delta §B1 producer, F15; follow-up 2026-07-10 — this exec is the `PLATFORM_MCP_PARK_PATH` producer, NOT dispatch's renderer, which cannot know the flight mount path at render time):** amend the Task 12 step-7 sidecar env assembly's `case "platform":` branch to append the sentinel destination row:

```go
			rows = append(rows, "PLATFORM_MCP_PARK_PATH="+
				artifactPath(step.containerMetadata.WorkingDirectory, "flight", "park.json"))
```

    Unconditional for agent steps — the `flight` output always exists (step 10 registers it), and the flight volume already reaches sidecars via jetbridge's `buildSidecarContainers` mount inheritance, so the path is valid inside the platform container. Checkpoint pods never get the row (the renderer does not emit it) — the legal unset = never-write shape (08 Task 9c). Landing this in the GATED Task 26 keeps the rollback story coherent: at a red Task 20 pin the env is never set and an `ask_human` threshold crossing degrades LOUDLY to the SSE park (08 Task 11b) — moot anyway under the fallback `--agent-short-park-max=0`. Matching §8.1 row added to 00-shared-contracts; 08's Task 1 addendum row corrected in place.
  - **Continuation consult** (new step 0 of `run`, after env interpolation resolves `AGENT_PIPELINE_RUN_ID`, before the budget slice): when `stepStateFactory != nil && runID > 0`, `row, found, err := stepStateFactory.Find(runID, plan.Name)`; lookup errors log and fall through to a cold run (never fail the step on a read).
    * `found && row.State == completed` ⇒ **short-circuit replay** (delta §D — REQUIRED, not optional: standard-dev's plan-approval checkpoint parking overnight is the common case; without this every resume re-runs `write-spec` cold): for each `(name, handle)` in `row.Artifacts`, locate the volume through the pool/fabric (`step.workerPool` + `wkr.ArtifactFromVolume` — the same lookup seam the streamer resolves handles with; verify the exact locate-by-handle signature on the `Pool` interface at execution time) and `state.ArtifactRepository().RegisterArtifact(build.ArtifactName(name), artifact, false)`; write the two §5 events as NDJSON lines to `delegate.Stdout()` (`step.start` with `"replayed": true`, then `step.end` — visible in `fly watch`; no pod, no flight stream, no metrics row); `delegate.Finished(logger, ExitStatus(0))`; return `(true, nil)`. Zero claude, zero cost, no `StepSlice` call.
    * `found && row.State == awaiting_human` ⇒ **resume**: restore `row.Artifacts["workspace"]` under its own name and `row.Artifacts["flight"]` under the reserved input name **`flight-prev`** (the fresh `flight` OUTPUT name stays free); append to the env assembly `AGENT_SESSION_ID=<row.SessionID>` and `AGENT_SESSION_FILE=<artifactPath(workdir, "flight-prev", "session.jsonl")>`; proceed through the NORMAL path (budget slice re-resolution included — decision 32: `StepSlice` is a resolution, the ledgered park-exit partial makes the re-resolved slice tighter automatically; **if pin P5 recorded session-cumulative costs, subtract the park-exit row's cost before the continuation's ledger append**).
    * not found ⇒ cold run (steps after the parked one; unchanged path).
  - **Parked ingestion** (`ingestFlightRecorder` amendments, all additive): `rm.SessionID = results.SessionID`; track `sawStepPark` alongside `sawStepEnd` in the events loop (also parse `StepParkData` to capture the question id back to the caller — return it, or hoist it into a small `ingestResult` struct); the missing-`step.end` error branch becomes `if !sawStepEnd && !sawStepPark { …error… }` — a stream ending in `step.park` keeps the results.json `parked` status (the ONE sanctioned exception, contracts §5). The ledger append is UNCHANGED (normal F3 `inserted` gate — the continuation build has a new `build_id`, so its later row never collides).
  - **Exit-86 branch** (step 11, checked before the generic exit handling): outputs were already registered by step 10 (including `flight` with `session.jsonl` — the F23 upload path already guarantees registration on non-happy exits) and ingestion already ran synchronously; when `result.ExitStatus == agentExitAwaitingHuman`: build `artifacts := map[string]string{}` from the step's registered output volume handles (the same `volumeMounts` loop ingestion uses); `stepStateFactory.Upsert(db.AgentRunStepState{PipelineRunID: runID, StepName: plan.Name, State: awaiting_human, BuildID: step.metadata.BuildID, SessionID: rm.SessionID, QuestionID: &questionID, Artifacts: artifacts})` (upsert errors: log + `delegate.Errored` — the open question row is the platform's authority, but losing the row breaks resume, so surface loudly); `delegate.AwaitingHuman(logger, questionID, rm.SessionID)`; `delegate.Finished(logger, ExitStatus(86))`; return `(false, nil)`.
  - **Normal-end upsert** (same branch structure, exit 0): `stepStateFactory.Upsert(… State: completed, SessionID: rm.SessionID, Artifacts: artifacts …)` — errors logged, never fatal.
- [ ] Reserve `flight-prev` in the Task 10 validator exactly as `flight` is reserved (a user-declared input/output named `flight-prev` is a validation error); extend the Task 10 validator test table.
- [ ] Wire the factory: `engine.WithAgentStepStateFactory(...)` option in `atc/engine/step_factory.go` (threaded into `AgentStep` as `exec.WithAgentStepStateFactory`), `db.NewAgentRunStepStateFactory(dbConn)` at the Task 16 call site in `atc/atccmd/command.go`; regenerate engine fakes.
- [ ] Run `ginkgo ./atc/exec/ ./atc/engine/ ./atc/event/ && go test ./atc/ -count=1` — expect pass.
- [ ] Commit: `git add atc && git commit -m "feat(exec): PARK-V2 agent step — exit-86 awaiting-human end, step-state replay/resume, parked ingestion (delta sB/sD)"`

---

### Task 27: Continuation-pin — step-state-referenced volumes exempt from orphan GC **[GATED on Task 20 green]**

Delta §B6 GC pin: artifact volumes whose handles appear in `agent_run_step_state.artifacts` of a run in (`running`, `awaiting_human`) are exempt from volume GC, so workspace/flight state "stays" across the zero-pod gap in the DaemonSet artifact cache. The single choke point is DB-side: `volumeRepository.GetOrphanedVolumes` (atc/db/volume_repository.go:542) is what feeds the GC collector that flips volumes to `destroying`, and the jetbridge reaper only ever acts on `destroying` volumes (reaper.go:176–183 cleans DaemonSet artifact entries from that same list) — so one query edit pins both the volume and its fabric locator, with **no jetbridge-side code change**. Bounded by construction: the pin dissolves when the run reaches a terminal status, and `--agent-park-timeout` (72h, lifecycler-enforced — 03's half) guarantees every run does.

**Files:**
- Modify: `atc/db/volume_repository.go` (:542 `GetOrphanedVolumes`)
- Test: `atc/db/volume_repository_test.go`

**Steps:**

- [ ] Write the failing specs in `atc/db/volume_repository_test.go` (existing orphaned-volumes context; fixtures need a `pipeline_runs` row — its table landed in wave 1 (03), so insert directly with the suite's raw-SQL helper):

```go
		Context("continuation-pinned volumes (PARK-V2 §B6)", func() {
			It("excludes volumes referenced by step-state rows of non-terminal runs", func() {
				// volume 'vol-ws' orphaned by every existing rule, BUT:
				// pipeline_runs row id=42 status='awaiting_human' and
				// agent_run_step_state(pipeline_run_id: 42, artifacts: {"workspace":"vol-ws"})
				orphaned, err := volumeRepository.GetOrphanedVolumes()
				Expect(err).ToNot(HaveOccurred())
				Expect(handlesOf(orphaned)).ToNot(ContainElement("vol-ws"))
			})

			It("releases the pin when the run reaches a terminal status", func() {
				// same fixtures, then UPDATE pipeline_runs SET status='errored'
				orphaned, err := volumeRepository.GetOrphanedVolumes()
				Expect(err).ToNot(HaveOccurred())
				Expect(handlesOf(orphaned)).To(ContainElement("vol-ws"))
			})
		})
```

- [ ] Run `ginkgo --focus="continuation-pinned" ./atc/db/` — expect failure (the pinned volume is returned).
- [ ] Implement — add one `NOT EXISTS` predicate to the `GetOrphanedVolumes` query builder (after the existing `Where` clauses at :561):

```go
		// PARK-V2 continuation-pin (§B6): volumes whose handles appear in
		// agent_run_step_state.artifacts of a non-terminal run must survive
		// the zero-pod awaiting_human gap so a continuation build can restore
		// them from the DaemonSet artifact cache. The pin dissolves when the
		// run reaches a terminal status; --agent-park-timeout (72h) bounds it.
		Where(sq.Expr(`NOT EXISTS (
			SELECT 1
			FROM agent_run_step_state ss
			JOIN pipeline_runs pr ON pr.id = ss.pipeline_run_id
			CROSS JOIN LATERAL jsonb_each_text(ss.artifacts) a(name, handle)
			WHERE pr.status IN ('running','awaiting_human')
			  AND a.handle = v.handle
		)`)).
```

  (`jsonb_each_text` over the artifacts object matches on the handle VALUES; the LATERAL stays cheap at this table's row counts — one row per (run, step), bounded by run retention. `pipeline_runs` landed in wave 1 (03), and its `awaiting_human` status value ships in 03's migration `1773106032` — sequence this task after that migration exists, or the fixture insert fails its CHECK.)
- [ ] Run `ginkgo ./atc/db/` — expect pass (the full suite guards the query against regressions in the seven existing orphan predicates).
- [ ] Commit: `git add atc/db && git commit -m "feat(db): PARK-V2 continuation-pin — step-state-referenced volumes exempt from orphan GC (delta sB6)"`

---

## Execution notes

**Full workstream test suite, in dependency order:**

```bash
pg_isready                                   # PostgreSQL required
cd /Users/tdmtrader/concourse/concourse
cd agent/schema && go test ./... && cd ../..                # nested module
cd ci-agent && go test ./... -count=1 -timeout 5m && cd ..  # make test-ci-agent
go test ./agent/api/metrics/ ./agent/runner/ -count=1
go test ./atc/ ./atc/builds/ -count=1                       # steps + planner testify suites
ginkgo ./atc/configvalidate/ ./atc/wrappa/ ./atc/api/accessor/
ginkgo ./atc/engine/ ./atc/exec/
ginkgo ./atc/db/ ./atc/db/migration/                        # ~90s; template DB
make test-unit                                              # full sweep (~3 min)
```

Never use `--race` with the parallel Ginkgo runs (CLAUDE.md — parallel compilation failures). If `atc/db` reports `database "testdb_template" already exists`, another test process is running — wait or kill it.

**Live tests (theborg):** plain-Go, `-tags live`, THROWAWAY namespace only (never `cicd`/`concourse`); Colima/Docker is usually down on this machine, so testcontainers is not an option. Commands are embedded in Task 18. The `k8s-live-tests` job in the cicd pipeline runs `go test -tags live ./atc/worker/jetbridge/...` against namespace `concourse` after self-upgrade — the two new live tests run there automatically once merged (they use the `kubeClient(t)`/namespace helpers, so they tolerate that namespace).

**PARK-V2 ordering (added 2026-07-10):** Task 20 (`TestLiveClaudeParkExitResume`, `-tags live_claude` — needs only the pinned claude CLI + a token, NO cluster/postgres) is the FIRST PARK-V2 deliverable and gates Tasks 24/26/27:

```bash
go test -tags live_claude -run '^TestLiveClaudeParkExitResume$' -v -count=1 -timeout 15m ./agent/runner/
```

Tasks 21–23 and 25 land regardless of the pin (additive; inert at `--agent-short-park-max=0`). Cross-plan sequencing: Task 26's continuation env consumes 08's sidecar `park.json` writer and 11's `reconcileAwaitingRuns` for an END-TO-END resume, but every Task 21–27 unit/DB suite here is self-contained; Task 27's DB fixture needs 03's `awaiting_human` status migration (`1773106032`) to exist first.

**Rollback notes for the risky diffs:**
- *StepVisitor interface change (Task 10)* is the widest compile surface; it is additive and revertible by dropping the `agent` detector from `StepPrecedence` (configs with `agent:` then fail validation with "no core step type declared" instead of executing — a safe failure mode).
- *NewHandler signature change (Task 9)*: expect mechanical rebase conflicts with whatever agent-identity landed in `atc/api/handler.go` — parameter appends only.
- *ci-agent schema switch (Task 3)* is the only cross-module diff; if it destabilizes the live review pipeline (which builds ci-agent from source at HEAD), revert is `git revert` of that single commit — the nested module (Task 2) stands alone.
- *Cutover (Task 19)*: the old shell job is not deleted until 5 dual-green runs are recorded; rollback at any point is `fly set-pipeline` with the previous YAML (the shell job block is preserved in git history).
- *Ingestion (Task 13)* is deliberately non-fatal end to end: a bad deploy degrades to missing metrics rows plus error-status rows, never failed builds.
- *Runtime seams (Task 11B) + severed-exec recording (Task 13B)* are jetbridge-wide but behavior-preserving for existing callers (nil seam maps are no-ops; the applySecretRefs append leg fires only for SecretEnv-only keys, which no pre-existing caller produces; the F23 upload is best-effort and never changes `Wait`'s returned error). The one behavioral change to watch is `supervised()` covering `ContainerTypeAgent` and the pause-loop command — both revert cleanly as isolated hunks of the Task 11B commit.

**Deferred/known risks:** `budget.LedgerEntry` field names are derived from §1.4 column names — align to the struct credentials-and-budgets actually landed (one-line fixes at Tasks 13/16). The claude CLI pin in the agent-runner image must match what the live review job verifies today. The prompt port in Task 19 trades exact ci-agent parity for the permanent step — the dual-running window is the verification mechanism, and score divergence beyond ±2 blocks retirement of the shell job.

**PARK-V2 rollback (added 2026-07-10):** the whole exit-and-respawn branch is behind `--agent-short-park-max` — setting it to `0` (or leaving `CONCOURSE_AGENT_SHORT_PARK_MAX` unset at `0`) restores pure PARK-V1 behavior end to end: the sidecar never writes `park.json`, the runner watcher never fires, exit 86 never occurs, `agent_run_step_state` stays empty, no run enters `awaiting_human`, and the Task 27 GC predicate matches nothing. All PARK-V2 schema is additive and inert at 0 — that is the designed escape hatch if the Task 20 pin is red OR if a live regression appears post-cutover. Per-workflow threshold override (`hitl.short_park_max_seconds`) is explicitly DEFERRED — global flag only in v1.

---

## §11 — Amendment log

- **2026-07-09 (design-review findings F3 + F4, `REVIEW.md` §2 majors 3–4).** Two ~2-line correctness fixes to the shared flight-recorder ingestion path (`ingestFlightRecorder`, Task 13), both defending the program's "every dollar / everything measurable" invariants:
  - **F3 — ledger double-charge on web-restart resume.** `budgetChecker.Record` was called unconditionally whenever `rm.CostUSD > 0`. Because the metrics row is idempotent under `ON CONFLICT (build_id, plan_id)` but `agent_cost_ledger` (§1.4) is append-only with no dedup key, a resume re-appended the cost row and inflated per-ticket and global-daily spend. Fix: `metrics.Store` (Task 7) gains an additive `UpsertReturningInserted(rm) (inserted bool, err error)`; the factory (Task 8) implements it via `RETURNING (xmax = 0) AS inserted` (fresh INSERT ⇒ `xmax = 0`; ON-CONFLICT UPDATE ⇒ `xmax <> 0`); Task 13 gates the ledger append on `inserted && rm.CostUSD > 0`. Reuses the `(build_id, plan_id)` key as the single dedup authority — no ledger schema change, and the gateway's own `source='gateway'` rows for the same build/plan are unaffected. `Upsert(rm) error` is retained (derived from the new method) so harvest-step (09) and delivery-outcomes (12) consumers and the `metricsfakes.FakeStore.Upsert*` methods are unchanged. New Task-13 spec asserts `Record` fires exactly once across two `Run` invocations (resume), and a Task-8 spec asserts `inserted` is true on first upsert and false on the ON-CONFLICT update.
  - **F4 — timed-out steps recorded zero cost/tokens.** Task 13 ingests on the `DeadlineExceeded` path too, but the step's `ctx` is the already-expired timeout context from `MaybeTimeout`, so both `StreamFile` reads failed immediately and a timed-out step recorded a bare `status=error` row with zero cost/tokens/`event_counts` and no ledger entry — losing measurement on the costliest steps. Fix: `ingestFlightRecorder` detaches from the deadline before the reads (`ingestCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second); defer cancel()`) and threads `ingestCtx` into both `StreamFile` calls. New Task-13 spec forces `process.Wait` to return `context.DeadlineExceeded` with a populated flight volume and asserts the metrics row still carries pre-timeout cost/turns/`event_counts` and that a ledger entry is recorded. *(Amended 2026-07-09, finding F23: the detach is necessary but NOT sufficient — deadline-severed execs also skipped jetbridge's `uploadOutputsToArtifactStore`, the only publisher of the flight volume's locator, so both reads failed regardless of context. Task 13B closes the jetbridge side; the F4 unit spec passes either way because its fake streamer bypasses artifact location, which is why Task 18 carries the live readability assertion.)*

  No shared-contract table/column/route/env names changed; the `Store` change is additive within the agent-step-owned `agent/api/metrics` surface (§2.4 `RunMetrics` and §1.8 `agent_run_metrics` are byte-for-byte unchanged). No cross-workstream sign-off required.

- **2026-07-09 (final-review findings, `REVIEW.md` §8 — runtime-seams package F15/F18/F20/F21/F25/F31-pause, owned here per the frozen 2026-07-09 seam delta, plus agent-step findings F23/F24/F37).**
  - **Task 11B (new) — jetbridge runtime seams.** Inserted after Task 11 as a hard prerequisite of Tasks 12/14/18; all wave-2 jetbridge/runtime Go changes land in this ONE task (consumers: 04 dev-mcp, 08 platform-mcp-hitl, 09 harvest-step, 11 dispatch, 00 registry). Contents: (F15) `runtime.ContainerSpec` gains `SidecarEnv map[string][]string` + `SidecarSecretEnv map[string]map[string]vars.SecretRef`, applied per-sidecar by `buildSidecarContainers(sidecars, mainMounts, defaultDir, sidecarEnv, sidecarSecretEnv)`; public YAML (`atc.SidecarConfig`/`SidecarEnvVar`) unchanged, no ValueFrom. (F20) `applySecretRefs(envList, secretEnv) []corev1.EnvVar` now RETURNS the slice and APPENDS sorted secretKeyRef-only EnvVars for SecretEnv-only keys — behavior-preserving for existing callers; the empty-placeholder workaround is forbidden. (F18) `supervised()` = `(ContainerTypeTask || ContainerTypeAgent) && Stdin == nil`; `db.ContainerTypeAgent = "agent"` (+ parse case) moved into Task 11B from Task 14, which now consumes it. (F31 pause leg) `pauseCommand` becomes `trap 'exit 0' TERM; while :; do sleep 86400 & wait $!; done` so parked pods survive past 24h; literal assertions at container_test.go:1517/:3186 and integration_test.go:101 updated.
  - **Task 12 (amended, F15/F21).** Step 7 now populates `SidecarEnv`/`SidecarSecretEnv` per §8.1 — common `ATC_EXTERNAL_URL`/`BUILD_ID` + identity rows for every `mcpSidecarPorts` sidecar; platform additionally gets the ASK_TIMEOUT rows and an `AGENT_PRINCIPAL_TOKEN` secret ref; gateway gets `AGENT_BUDGET_SLICE_USD` plus `AGENT_PRINCIPAL_TOKEN` and `CLAUDE_CODE_OAUTH_TOKEN` refs; refs derive from the deterministic §8.2 secret name `agent-run-<AGENT_PIPELINE_RUN_ID>` (keys `principal-token`/`anthropic-token`); no run id ⇒ no sidecar secret env (pure CI). Step 7 also normalizes each MCP sidecar's unset `WorkingDir` to `artifactPath(workdir, "workspace", "")` when the plan carries a `workspace` artifact (§8.5 CWD convention — sidecar images ship bare-binary ENTRYPOINTs, no hardcoded `/workspace`). New exec-level specs assert both.
  - **Task 13B (new, F23) — jetbridge severed-exec output recording.** The non-`ExecExitError` branch of `execProcess.Wait` (process.go:899–902) skipped `uploadOutputsToArtifactStore`, so deadline/transport-severed execs left outputs unlocatable and timed-out steps recorded zero cost despite F4. Fix: best-effort `uploadOutputsToArtifactStore` under `context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)` in that branch (logged, never returned; also repairs timed-out task steps' hook inputs), plus a process_test.go spec and a live readability assertion in Task 18 (the unit-level fake streamer structurally cannot catch this).
  - **F24 — degraded re-ingestion is now non-destructive; reaped-pod reruns intentionally under-count.** `metrics.Store` (Task 7) gains additive `InsertIfAbsent(rm) (bool, error)`; the factory (Task 8) implements it via `ON CONFLICT (build_id, plan_id) DO NOTHING` (sql.ErrNoRows ⇒ inserted=false, nothing written). Task 13's `ingestFlightRecorder` tracks whether any flight file was actually read; when nothing was read (restart-resume with the ephemeral locator gone, reaped pod, missing mount) it writes via `InsertIfAbsent` instead of the all-columns upsert, so a zero-cost `status=error` shell can never clobber a real row consumed by scorecards/outcomes. The F3 resume spec now feeds a failing streamer on the second `Run` and asserts the degraded path + single ledger charge. **Accepted trade-off, recorded here:** a reaped-pod rerun re-executes the whole claude session and its second run's real cost lands with `inserted=false` — never ledgered. The `(build_id, plan_id)` dedup key cannot distinguish re-ingest from re-execute; under-counting beats double-charging, and reaper changes are deferred.
  - **F37 — F4 timeout spec stub fixed.** The spec installed its `DeadlineExceeded` stub via `chosenContainer.WithProcess(...)` with the result discarded — runtimetest's `WithProcess` is copy-on-write, so the stub never took effect (and the bare `ProcessSpec{ID: "agent"}` would never DeepEqual-match). Replaced with the harness idiom: mutate `chosenContainer.ProcessDefs[0].Stub.Call` in place.
  - **Task 14 (amended, F18).** `db.ContainerTypeAgent` consumed, not added (moved to Task 11B); engine wiring otherwise unchanged.
  - **Task 16 (amended, F25).** `deploy/agent-runner/Dockerfile` drops `useradd`/`USER agent` and runs as **root** with `ENV IS_SANDBOX=1` (claude refuses `--dangerously-skip-permissions` as root otherwise): jetbridge hostPath step volumes are kubelet-created root:root 0755 and fsGroup is ignored for hostPath, so a non-root runner EACCESes writing the flight recorder on its first live run. §8.5's non-root convention is scoped to MCP sidecar images only (00-shared-contracts + `deploy/MCP_IMAGES.md` carry the matching scoping edits); the same decision covers `harvest-runner` (09 Task 10, same Dockerfile).
  - **Task 18 (amended, F18/F23/F25).** `live_agent_resume_test.go` MUST use `db.ContainerMetadata{Type: db.ContainerTypeAgent}` at BOTH `FindOrCreateContainer` sites (a `ContainerTypeTask` copy passes vacuously — review-rejectable) and gains the F23 post-severance output-readability assertion; `live_agent_mcp_test.go` gains the flight-mount write tripwire (`flight-write-ok`) so hostPath-permission regressions fail the live test instead of production.

  Cross-workstream: contract-visible names match the frozen seam delta exactly (`SidecarEnv`, `SidecarSecretEnv`, `applySecretRefs` append semantics, `db.ContainerTypeAgent`, the pause command string, `agent-run-<run-id>`/`principal-token`/`anthropic-token`, `IS_SANDBOX=1`). 00-shared-contracts §8.1/§8.2/§8.5/§11, 04 Tasks 12–13, and 09 Tasks 11–12 carry the co-signed consumer edits in their own files. F23/F24/F37 are agent-step-internal; the Task 13B jetbridge edit is the one sanctioned addition outside Task 11B's frozen scope.

- **2026-07-10 (PARK-V2 seam delta — exit-and-respawn for long human-waits; FROZEN 2026-07-10, amends the 2026-07-09 SSE-park entry; implements FLOWS.md Part 2 §P2.5 recommendations #1–#4; contracts decisions 30–32).** This plan gains the new "PARK-V2" section (Tasks 20–27) owning the runner/exec/schema halves; SHORT parks (< `--agent-short-park-max`, default 30m) keep the unchanged PARK-V1 SSE path, LONG parks exit-and-respawn. Nothing in F13/F31 legs 1–3 or the checkpoint seam is retracted — PARK-V2 sits above them and makes the >threshold branch of each moot.
  - **Task 20 (new, THE GATE)** — `agent/runner/live_claude_resume_test.go` `TestLiveClaudeParkExitResume` (`//go:build live_claude`, real pinned CLI, no cluster): SIGTERM claude mid-`ask_human` against a parking stub MCP server, assert the pending `tool_use` is on disk, restore ONLY the JSONL under a fresh HOME/cwd, `claude -p --resume` against a fresh ANSWERED stub, assert the pending tools/call is RE-ISSUED and completes. Answers pins P1–P6 (recorded here on execution). Green ⇒ Tasks 24–27 proceed; red ⇒ ship `--agent-short-park-max=0` (pure PARK-V1), zero schema waste. Also lands the frozen `runner.ContinuationPrompt`, `runner.ExitAwaitingHuman = 86`, and `runner.CwdSlug`.
  - **Task 21 (new, delta §F)** — contract addendum (§8.1 rows `AGENT_PARK_EXIT_GRACE_SECONDS`/`AGENT_STREAM_LOG_MAX_LINE_BYTES`/`AGENT_SESSION_ID`/`AGENT_SESSION_FILE`; flight output gains `session.jsonl` + `park.json` + reserved `flight-prev`; exit-code registry 86/3; §5 `step.park`-without-`step.end` ingestion exception) + `agent/schema` additions (`StatusParked`/`RunStatusParked` + `ThreeWayStatus` mapping, `Results.SessionID`, `RunMetrics.SessionID`, `EventStepPark`/`EventStepResume` + `StepParkData`/`StepResumeData`, `StepStartData.Resumed/.Replayed`, `StepEndData.SessionID`) + migration `1773106062` (metrics CHECK gains `'parked'`, column `session_id`).
  - **Task 22 (new, accommodation #2)** — `ci-agent/llm/result.go` `CallResult` gains `SessionID` (populated from the envelope field parsed at :51 and previously discarded); ci-agent's `step.end` payload gains `session_id`.
  - **Task 23 (new, accommodation #1, delta §G)** — agent-runner switches to `--output-format stream-json` teed line-by-line to stdout (`fly watch` = live transcript) with the 16KiB tee-only truncation guard (parser always sees full lines); the stream is parsed for best-effort usage accumulation + the terminal `result` event as THE envelope (never required — pin P3); session JSONL copied to `flight/session.jsonl` at EVERY exit; `results.json` gains `session_id`. Supersedes Task 15's `--output-format json` + last-line parse.
  - **Task 24 (new, GATED, delta §B2/§D)** — runner park-exit path: 5s `flight/park.json` stat watcher → SIGTERM claude → `AGENT_PARK_EXIT_GRACE_SECONDS` (30) → SIGKILL → partial `cost.record` + terminal `step.park` (no `step.end`) → `results.json` status `parked` + session_id → exit **86**; resume mode: `AGENT_SESSION_ID`/`AGENT_SESSION_FILE` install the transcript under `~/.claude/projects/<cwd-slug>/` and invoke `claude -p --resume <id>` with the frozen `ContinuationPrompt`, leading `step.resume` event.
  - **Task 25 (new, delta §B6)** — migration `1773106061` `agent_run_step_state` (contracts §1.14, UNIQUE (pipeline_run_id, step_name)) + `db.AgentRunStepStateFactory` (Upsert/Find) + fake.
  - **Task 26 (new, GATED, delta §B5/§B6/§D)** — exec.AgentStep: exit-86 distinguished end (no fifth build status — build fails as CARRIER; the triple is exit 86 + additive `atc/event.AwaitingHuman` build event + the step-state row; the platform's authority stays the open question row); outputs registered + parked partial ingestion (status `parked`, session_id, ledger via the normal F3 gate) + `awaiting_human` step-state upsert with artifact handles; continuation consult keyed (pipeline_run_id, step_name) — `completed` ⇒ zero-cost short-circuit replay by handle restore (REQUIRED: overnight plan-approval parks are the common case), `awaiting_human` ⇒ restore workspace + `flight-prev` inputs and set session env, absent ⇒ cold; `completed` upsert at every normal end; `StepSlice` re-resolved at every execution start (decision 32 — resolution not reservation; park-exit partial spend already ledgered makes it self-tightening; P5-cumulative caveat flagged inline); `TaskDelegate` gains `AwaitingHuman(logger, questionID, sessionID)`; validator reserves `flight-prev`. *(Follow-up 2026-07-10 — `PLATFORM_MCP_PARK_PATH` producer assigned:)* the frozen delta's §B1 sentinel-path env had NO producer — plan 11's renderer emits only `PLATFORM_MCP_SHORT_PARK_MAX_SECONDS` and cannot know the flight mount path at render time, and 08's Task 1 addendum wrongly attributed the row to the renderer, so every `ask_human` long-park would have degraded LOUDLY to the SSE park (the ask_human half of PARK-V2 never activating). Per F15 (sidecar rows for exec-owned steps are populated programmatically by the owning exec), Task 26 now appends `PLATFORM_MCP_PARK_PATH=<flight mount>/park.json` to `ContainerSpec.SidecarEnv["platform"]` in the Task 12 step-7 assembly (agent steps only; checkpoint pods stay unset = never write); co-signed edits: 08's addendum row + `Config.ParkPath` comments corrected, 00 §8.1 gains the row.
  - **Task 27 (new, GATED, delta §B6)** — continuation-pin: `GetOrphanedVolumes` (volume_repository.go:542) excludes volumes whose handles appear in `agent_run_step_state.artifacts` of runs in (`running`,`awaiting_human`) — one DB-side predicate pins both the volume and its DaemonSet fabric locator (the jetbridge reaper only acts on `destroying` volumes); dissolves at terminal run status, bounded by `--agent-park-timeout` (72h).
  - **Inline notes** added to Tasks 4/6/12/13/15 so already-landed baseline code is amended (never re-implemented) by the PARK-V2 tasks; execution notes gain the PARK-V2 ordering + rollback paragraphs.

  Cross-workstream (co-signed, NOT implemented here): 03-pipeline-runs owns `awaiting_human` run status (migration `1773106032`), lifecycler entry/exit, park-timeout expiry + `run.park_expired` notification, run-status API/`fly runs`/Elm badge; 08-platform-mcp-hitl owns the sidecar threshold timer + atomic `park.json` writer, checkpoint-client exit 3, `question_hash` find-or-create dedup (migration `1773106072`), ticket-page chip; 11-dispatch owns `reconcileAwaitingRuns` (Task 11c), principal/secret re-mint, continuation-build trigger (`created_by = "agent-dispatcher:resume"`); 02-credentials-and-budgets owns the `SecretAttacher.Attach` create-or-update amendment + `RunActive` counting `awaiting_human` as active; 13-scorecards notes the (pipeline_run_id, step_name) cost-aggregation rule. The TICKET state enum was deliberately NOT reopened — parked-ness is a derivation (run `awaiting_human` OR open questions), never a ticket state.
