# Agent Step Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a first-class `agent:` step type that runs the claude CLI in a jetbridge pod with declared MCP sidecars, ingests its flight recorder (results.json + events.ndjson) server-side into `agent_run_metrics`, proves sidecar-MCP wiring on a live theborg cluster, and cuts the live theborg/cicd agent-review job over to it with a dual-running verification period.

**Architecture:** The step follows the existing step-type recipe exactly (config union in `atc/steps.go`, plan union in `atc/plan.go`, planner visitor, validator visitor, engine builder + core step factory, `atc/exec` implementation modeled on `TaskStep` including its sidecar machinery and `attachOrRun` resume path). The pod's main container runs a new deterministic `agent-runner` binary that invokes the claude CLI and writes the flight recorder to an implicit `flight` output; the exec ingests that output into the DB synchronously before the step returns, so ingestion always beats artifact-fabric GC. The shared `agent/schema` package becomes its own nested Go module consumed by both the main module and ci-agent (spec open item 11).

**Tech Stack:** Go 1.25 (main module + `agent/schema` nested module + ci-agent module), PostgreSQL migrations (`atc/db/migration/migrations`), squirrel + counterfeiter factory recipe, Ginkgo/Gomega for atc packages, testify suites for `atc/steps_test.go` / `atc/builds/planner_test.go`, Elm for the build-page plan rendering, plain-Go `//go:build live` tests against theborg.

---

## Context

**Charter (workstreams.json `agent-step`, wave 2, size L).** Scope in: (1) full step-type recipe; (2) step config schema v1 with inline prompt, sidecar mounts, budget-slice env, artifacts in/out, resumable execution, no push credentials in the agent pod; (3) `agent_run_metrics` migration + factory + principal-authed ingest route with tolerant parsing and an ingestion-before-GC guarantee; (4) open item 11 — extract ONLY the results/events schema types from ci-agent into a shared module; (5) live theborg sidecar-MCP wiring proof; (6) cutover of the live theborg/cicd agent-review job with dual-running. Scope out: credential vaulting (v1 consumes ordinary var-source secrets), real MCP sidecar servers, harvest/push, scorecard views.

**Prior waves (assumed landed exactly as 00-shared-contracts.md defines):**
- **agent-identity**: `agent_principals` table (§1.2), `auth.CheckAgentPrincipalHandler(handler, rejector, scope)` wrappa tier, `CheckAgentAuthorizationHandler` for team-less `/api/v1/agent/*` authorized routes (§4.2 closing paragraph, decision 21), scope vocabulary including `metrics:write` (§4.1).
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

Follows the `agent/api/reviews` idiom exactly (types + `Store` + `memory_store.go` + `handler.go`, `r.FormValue(":ticket_id")` for rata params).

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
	s.mu.Lock()
	defer s.mu.Unlock()
	key := [2]any{rm.BuildID, rm.PlanID}
	if _, ok := s.rows[key]; !ok {
		s.seq++
		s.ord[key] = s.seq
	}
	s.rows[key] = *rm
	return nil
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

Implements `metrics.Store` with squirrel, upsert `ON CONFLICT (build_id, plan_id)`, epoch-seconds scan — the `agent_reviews_factory.go` recipe (atc/db/agent_reviews_factory.go:26 Upsert, :61 column scan).

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
		Expect(factory.Upsert(rm)).To(Succeed())

		rm.Summary = "second"
		rm.CostUSD = 0.43
		Expect(factory.Upsert(rm)).To(Succeed())

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
})
```

- [ ] Run `ginkgo --focus="AgentRunMetricsFactory" ./atc/db/` — expect compile failure.
- [ ] Write `atc/db/agent_run_metrics_factory.go`:

```go
package db

import (
	"database/sql"
	"encoding/json"

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
	var eventCounts, results any
	if rm.EventCounts != nil {
		b, err := json.Marshal(rm.EventCounts)
		if err != nil {
			return err
		}
		eventCounts = b
	}
	if len(rm.Results) > 0 {
		results = []byte(rm.Results)
	}

	_, err := psql.Insert("agent_run_metrics").
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
			event_counts = EXCLUDED.event_counts`).
		RunWith(f.conn).
		Exec()
	return err
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
- [ ] Commit: `git add atc/db && git commit -m "feat(db): AgentRunMetricsFactory implementing metrics.Store"`

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
  - Add `case atc.SubmitAgentRunMetrics:` to the principal-scope tier landed by agent-identity, with scope `metrics:write` (per contracts §4.1). The landed pattern is `newHandler = auth.CheckAgentPrincipalHandler(handler, rejector, "<scope>")`; match the exact helper signature that wave 1 landed (grep `CheckAgentPrincipalHandler` first and copy an existing principal case such as `SubmitAgentReview`).
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

### Task 12: `exec.AgentStep` — container spec, env contract, budget slice, process

The core execution path, modeled line-by-line on `TaskStep.run`. Reuses the `TaskDelegate` interface (it already has `EmitSidecarPlans`/`SidecarWriter`; `SetTaskConfig` is simply not called) via `TaskDelegateFactory` — both defined in `atc/exec/task_step.go:60–86`, produced by the engine's `DelegateFactory.TaskDelegate` (atc/engine/delegate_factory.go:65). No engine changes in this task; the step is constructed directly in tests. Flight-recorder ingestion is added in Task 13 — this task's `Run` does not yet write metrics rows.

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
  7. **Sidecars:** `sidecars, err := loadSidecarConfigs(ctx, logger, state.ArtifactRepository(), step.streamer, step.plan.Sidecars)`; `resolveSidecarImages(ctx, logger, state, step.imageResolver, sidecars)`; `containerSpec.Sidecars = sidecars`; when non-empty `delegate.EmitSidecarPlans(logger, sidecars)` (Task 11 helpers). Then for each sidecar name found in `mcpSidecarPorts`, append `strings.ToUpper(name)+"_MCP_URL=http://127.0.0.1:"+strconv.Itoa(port)+"/mcp"` to `containerSpec.Env` (after loading, so file-sourced sidecars count too).
  8. **Placement + timeout** (task_step.go:345–378): `tracing.Inject(ctx, &containerSpec)`; `owner := db.NewBuildStepContainerOwner(step.metadata.BuildID, step.planID, step.metadata.TeamID)`; `delegate.BeforeSelectWorker`; `step.workerPool.FindOrSelectWorker(ctx, owner, containerSpec, worker.Spec{TeamID: step.metadata.TeamID})`; `MaybeTimeout(ctx, step.plan.Timeout, step.defaultTimeout)` + defer cancel; `delegate.SelectedWorker`; `worker.FindOrCreateContainer(ctx, owner, step.containerMetadata, containerSpec, delegate)`.
  9. **Process** (task_step.go:379–405): `delegate.Starting(logger)`; `process, err := attachOrRun(ctx, container, runtime.ProcessSpec{ID: agentProcessID, Path: "agent-runner", Dir: step.containerMetadata.WorkingDirectory, TTY: &runtime.TTYSpec{WindowSize: runtime.WindowSize{Columns: 500, Rows: 500}}}, sidecarProcessIO(delegate, containerSpec.Sidecars))` — `attachOrRun` (task_step.go:431) is what makes the step resume across web restarts under the jetbridge supervisor; `result, runErr := process.Wait(ctx)`.
  10. **Outputs registration** (task_step.go:715 pattern, including the `worker.ArtifactFromVolume` DaemonSet wrap): for each name in `plan.Outputs` plus `"flight"`, match `volumeMounts` by cleaned path and `repository.RegisterArtifact(build.ArtifactName(name), artifact, false)`.
  11. **Exit handling** identical to task_step.go:416–428: `context.DeadlineExceeded` → `delegate.Errored(logger, TimeoutLogMessage)`, return `(false, nil)`; other runErr → return it; else `delegate.Finished(logger, ExitStatus(result.ExitStatus))`, return `(result.ExitStatus == 0, nil)`.
- [ ] Run `ginkgo --focus="AgentStep" ./atc/exec/` — expect pass.
- [ ] Run `ginkgo ./atc/exec/` — full package green.
- [ ] Commit: `git add atc/exec && git commit -m "feat(exec): agent step execution — env contract, sidecars, budget slice, resumable process"`

---

### Task 13: `exec.AgentStep` — server-side flight-recorder ingestion

The ingestion-before-GC guarantee: ingestion runs synchronously inside `Run` before the step returns, reading from the just-registered `flight` output volume via `Streamer.StreamFile` (the DaemonSet fabric path — same seam `LoadVarStep` uses at load_var_step.go:138). Tolerant of crashed agents: missing/malformed files or a stream without `step.end` produce a `status=error` row; ingest failures never fail the step; a fire-and-forget ledger record lands cost in `agent_cost_ledger`.

**Files:**
- Modify: `atc/exec/agent_step.go` (add `ingestFlightRecorder` + call it from `run` after output registration)
- Test: `atc/exec/agent_step_test.go` (ingestion contexts)

**Steps:**

- [ ] Add ingestion specs to `atc/exec/agent_step_test.go`, with `fakeMetricsStore *metricsfakes.FakeStore` passed via `exec.WithAgentMetricsStore`. `fakeStreamer.StreamFileStub` returns fixture readers keyed on the requested path:
  - `results.json` → `{"schema_version":"1.0","status":"pass","confidence":1,"summary":"done","artifacts":[]}`
  - `events.ndjson` → four NDJSON lines: `step.start` (`{"step_name":"write-spec","build_id":1,"plan_id":"p"}`), `tool.call` (`{"tool":"run_tests"}`), `cost.record` (`{"source":"agent_step","provider":"anthropic","model":"m1","input_tokens":100,"output_tokens":50,"cache_read_tokens":1,"cache_creation_tokens":2,"turns":9,"cost_usd":0.42}`), `step.end` (`{"step_name":"write-spec","status":"ok","summary":"done","wall_time_seconds":61,"cost_usd":0.42,"turns":9}`).

```go
	Context("flight-recorder ingestion", func() {
		It("upserts a RunMetrics row before Run returns", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(fakeMetricsStore.UpsertCallCount()).To(Equal(1))
			rm := fakeMetricsStore.UpsertArgsForCall(0)
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
			rm := fakeMetricsStore.UpsertArgsForCall(0)
			Expect(rm.Status).To(Equal("error"))
			Expect(rm.EventCounts).To(HaveKeyWithValue("tool.call", 1)) // partial counts kept
		})

		It("records an error row when the flight files are missing entirely", func() {
			// fakeStreamer returns an error for both files
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue()) // exit status still drives step success
			rm := fakeMetricsStore.UpsertArgsForCall(0)
			Expect(rm.Status).To(Equal("error"))
			Expect(rm.Summary).To(ContainSubstring("flight recorder"))
		})

		It("never fails the step when the metrics upsert errors", func() {
			fakeMetricsStore.UpsertReturns(errors.New("db down"))
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
		})

		It("records a fire-and-forget ledger entry when cost was incurred", func() {
			step.Run(ctx, state)
			Expect(fakeChecker.RecordCallCount()).To(Equal(1))
		})

		It("maps abstain results to failed", func() {
			// results.json fixture status "abstain"
			step.Run(ctx, state)
			Expect(fakeMetricsStore.UpsertArgsForCall(0).Status).To(Equal("failed"))
		})
	})
```

- [ ] Run `ginkgo --focus="ingestion" ./atc/exec/` — expect failure.
- [ ] Implement in `atc/exec/agent_step.go` and call it from `run` immediately after output registration (step 10 of Task 12), on every path where the container ran — including the `DeadlineExceeded` branch — before the exit handling returns:

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

	if flightArtifact != nil {
		// results.json
		if rc, err := step.streamer.StreamFile(ctx, flightArtifact, "results.json"); err == nil {
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
		if rc, err := step.streamer.StreamFile(ctx, flightArtifact, "events.ndjson"); err == nil {
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

	if err := step.metricsStore.Upsert(&rm); err != nil {
		logger.Error("failed-to-ingest-run-metrics", err)
	}

	if step.budgetChecker != nil && rm.CostUSD > 0 {
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
- [ ] Commit: `git add atc/exec && git commit -m "feat(exec): synchronous server-side flight-recorder ingestion with tolerant parsing and ledger record"`

---

### Task 14: Engine wiring — container type, CoreStepFactory.AgentStep, builder dispatch

`exec.NewAgentStep` and its options exist (Tasks 12–13); this task routes `Agent` plans to it.

**Files:**
- Modify: `atc/db/container_metadata.go:24` (add `ContainerTypeAgent ContainerType = "agent"` + parse case in the from-string function at :43 region)
- Modify: `atc/engine/builder.go:20` (CoreStepFactory interface + `buildAgentStep` dispatch before `plan.Run` at :138)
- Modify: `atc/engine/step_factory.go:166` region (constructor; new option fields)
- Modify: `atc/engine/enginefakes/` (regenerate)
- Test: `atc/engine/builder_test.go` (mirror the RunStep case at :558)

**Steps:**

- [ ] Add a builder test case in `atc/engine/builder_test.go` next to the run-step context (:558): a plan built from `planFactory.NewPlan(atc.AgentPlan{Name: "write-spec", Prompt: "p"})`; assert `fakeCoreStepFactory.AgentStepCallCount()` is 1 and the received plan / stepMetadata / containerMetadata match (containerMetadata `Type: db.ContainerTypeAgent`, `StepName: "write-spec"`). Copy the surrounding `expectedPlan`/`ArgsForCall` assertions verbatim from the run case (:558–566), substituting names.
- [ ] Run `ginkgo ./atc/engine/` — expect compile failure (`AgentStep` not on `CoreStepFactory`, fake missing).
- [ ] In `atc/db/container_metadata.go` add the constant and the `case "agent":` parse arm mirroring `run` (:31/:45).
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
- [ ] Commit: `git add atc/db/container_metadata.go atc/engine && git commit -m "feat(engine): route agent plans to exec.AgentStep with image/metrics/budget options"`

---

### Task 15: `agent-runner` binary

Deterministic pod entrypoint: waits for declared MCP sidecars, invokes the claude CLI, writes the flight recorder. Lives in the main module (`agent/runner` package + thin `cmd/agent-runner`). CLI envelope parsing follows `ci-agent/llm/result.go` (cost_usd, usage, num_turns, is_error — with `total_cost_usd` fallback for newer CLIs).

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
- [ ] Create `deploy/agent-runner/Dockerfile` (packaging per §8.5: pinned provider CLI, non-root):

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
RUN useradd -u 1001 -m agent
USER agent
ENTRYPOINT ["agent-runner"]
```

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
		Env:       []string{"PLATFORM_MCP_URL=http://127.0.0.1:7781/mcp"},
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
}
```

  (Adjust `setupLiveWorker`/`cleanupPod`/`kubeClient` calls to their exact signatures in `live_test.go`/`live_worker_test.go` at execution time; teardown is the existing `t.Cleanup`-based pod deletion in those helpers.)
- [ ] Write `live_agent_resume_test.go`: copy `TestLiveTaskResume` (live_task_resume_test.go:33) wholesale, renamed `TestLiveAgentProcessResume`, with `ProcessSpec{ID: "agent", …}` on the web-1 `container1.Run` and the web-2 takeover using `container2.Attach(ctx, "agent", …)` — proving the `agent` process ID survives a severed exec session without the command restarting (the exec's `attachOrRun` contract from Task 12).
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

- [ ] Add the `agent-review-native` job after the existing `agent-review` job (:110). The prompt is the `prompt:` text from `ci-agent/phases/review.yaml` copied verbatim (the same instructions the shell job feeds ci-agent), adapted only where it references phase-runner file paths (output goes to `review/review.json`):

```yaml
- name: agent-review-native
  plan:
  - get: repo
    trigger: false
    params: {depth: 25}
  - agent: review
    prompt: |
      # <verbatim copy of the review prompt from ci-agent/phases/review.yaml,
      #  with output instructions pointing at review/review.json>
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

**Rollback notes for the risky diffs:**
- *StepVisitor interface change (Task 10)* is the widest compile surface; it is additive and revertible by dropping the `agent` detector from `StepPrecedence` (configs with `agent:` then fail validation with "no core step type declared" instead of executing — a safe failure mode).
- *NewHandler signature change (Task 9)*: expect mechanical rebase conflicts with whatever agent-identity landed in `atc/api/handler.go` — parameter appends only.
- *ci-agent schema switch (Task 3)* is the only cross-module diff; if it destabilizes the live review pipeline (which builds ci-agent from source at HEAD), revert is `git revert` of that single commit — the nested module (Task 2) stands alone.
- *Cutover (Task 19)*: the old shell job is not deleted until 5 dual-green runs are recorded; rollback at any point is `fly set-pipeline` with the previous YAML (the shell job block is preserved in git history).
- *Ingestion (Task 13)* is deliberately non-fatal end to end: a bad deploy degrades to missing metrics rows plus error-status rows, never failed builds.

**Deferred/known risks:** `budget.LedgerEntry` field names are derived from §1.4 column names — align to the struct credentials-and-budgets actually landed (one-line fixes at Tasks 13/16). The claude CLI pin in the agent-runner image must match what the live review job verifies today. The prompt port in Task 19 trades exact ci-agent parity for the permanent step — the dual-running window is the verification mechanism, and score divergence beyond ±2 blocks retirement of the shell job.
