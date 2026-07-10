# agent-gateway-mcp Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the `mcp-gateway` sidecar exposing `request_review(diff, rubric)` and `ask_agent(prompt, provider, model)` over a provider-adapter layer, meter every cross-agent call into the flight-recorder (`agent/schema` events) and the cost ledger (fire-and-forget `POST /api/v1/agent/costs`), and enforce a budget-slice cutoff via the credentials-and-budgets library that halts with a `budget cutoff:` `failed` signal instead of ever silently truncating.

**Architecture:** A new main-module package `agent/gatewaymcp` (binary `cmd/gateway-mcp`, image `ghcr.io/tdmtrader/mcp-gateway`) is a streamable-HTTP MCP server (`atc/api/mcpserver`, the same protocol layer platform-mcp uses — SSE-capable after 08 Task 9b's in-place upgrade: `NewServerWithHeartbeat` + the 3-arg `ToolHandler`; `request_review`/`ask_agent` are MUST-stream tools per the 2026-07-09 SSE delta because they routinely exceed the claude CLI's empirical 60s buffered-call abandonment) whose two tools dispatch through an `Adapter` interface. The v1 `claude` adapter runs the bundled `claude` CLI in-sidecar, parses the `--output-format json` envelope for cost/usage (re-implemented locally because ci-agent's `llm` package is a separate, non-importable module), and hard-stops the subprocess when the metered running cost would exceed the remaining slice. Each call emits `subagent.call`/`subagent.result`/`cost.record` events (constants already in `agent/schema` from agent-step) to an NDJSON event log and POSTs a `budget.LedgerEntry` to ATC using the run's `AGENT_PRINCIPAL_TOKEN` — both fire-and-forget so a metering failure never fails the caller.

**Tech Stack:** Go 1.25 (main module + the nested `agent/schema` module via the root `replace`), `atc/api/mcpserver` MCP protocol, `os/exec` for the claude CLI, plain-Go `testing` + `httptest` for `agent/gatewaymcp` and `cmd/gateway-mcp`, counterfeiter fakes, Docker-in-DinD image build on the existing theborg `cicd` pipeline (dev-mcp's `build-mcp-dev-image` job is the copyable template), plain-Go `//go:build live` tests against theborg.

---

## Context

**Charter (workstreams.json id `gateway-mcp`, wave 3, size M, `depends_on: [agent-identity, credentials-and-budgets, agent-step]`).** Scope in:
1. Gateway sidecar (packaged per dev-mcp's convention) with `request_review`/`ask_agent` tool schemas + contract tests.
2. Adapter layer, v1 in-sidecar execution: `claude` CLI adapter first (mirroring ci-agent invocation + cost parsing), interface ready for `codex`/`cursor` and a future platform-scheduled-pod backend behind the same contract.
3. Universal metering: tokens/turns/cost/latency per call emitted in the shared events.ndjson schema (flight recorder) and written fire-and-forget to `agent_cost_ledger`; cost normalization at the adapter boundary.
4. Budget-slice cutoff per the credentials-and-budgets library: halt with a partial-work-product signal surfacing as needs-review, never silent truncation — tested explicitly.
5. Sidecar authenticates as a scoped agent principal; provider calls use the run-attached user credential (or platform credential per policy).

**Scope-out (must NOT appear in this plan):** judge execution (harvest invokes the judge directly); global daily-cap admission (dispatch); cross-provider scorecard views (scorecards); the platform-scheduled-pod backend (later, same `Adapter` contract). No new DB table (gateway-mcp owns no migration block in §1.1) and no new `agent/schema` event constant (agent-step already landed all §5 constants + payload structs).

**Prior waves assumed landed (exactly as `00-shared-contracts.md` + upstream plan files define):**
- **agent-identity (wave 1):** `agent_principals` (§1.2); the `principal(<scope>)` wrappa tier via `auth.CheckAgentPrincipalHandler`; scope vocabulary including `costs:write` (§4.1). The gateway authenticates outbound to ATC with `AGENT_PRINCIPAL_TOKEN` (§8.1) minted by dispatch with `costs:write` for the run.
- **credentials-and-budgets (wave 1):** `agent_cost_ledger` (§1.4); the `agent/budget` package with `budget.Checker` (`StepSlice`, `Record`, `TicketRemaining`), `budget.LedgerEntry`, `budget.Remaining`, `budget.SourceGateway`, and `budgetfakes.FakeChecker` (§2.7); the `POST /api/v1/agent/costs` route (`SubmitAgentCostRecord`) whose body is a JSON `budget.LedgerEntry` (credentials plan Task, `agent/api/costs` handler) — this is the gateway's fire-and-forget metering sink. Per the credentials wave-1 addendum, metering writers set `metadata: {"workflow": "<name>@<version>"}` on ledger rows.
- **agent-step (wave 2):** `agent/schema` extracted as a nested stdlib-only Go module (`require`+`replace` in the root and ci-agent `go.mod`s); §5 event constants `EventSubagentCall`, `EventSubagentResult`, `EventCostRecord`, `EventBudgetWarn`, `EventBudgetStop` and their payload structs `SubagentCallData`, `SubagentResultData`, `CostRecordData`, `BudgetData` (agent-step Task 4); the merged `schema.Event{Timestamp string; Type EventType; Data json.RawMessage}` + `schema.NewEventWriter`/`EventWriter.Write` (auto-fills a missing timestamp); the proven sidecar wiring/env contract (§8.1 + the agent-step addendum): `GATEWAY_MCP_URL=http://127.0.0.1:7782/mcp`, and the gateway container receives `CLAUDE_CODE_OAUTH_TOKEN`, `AGENT_PRINCIPAL_TOKEN`, `ATC_EXTERNAL_URL`, `AGENT_TICKET_ID`, `AGENT_PIPELINE_RUN_ID`, `BUILD_ID`, `AGENT_BUDGET_SLICE_USD` (agent-step addendum explicitly names gateway as the `AGENT_BUDGET_SLICE_USD` reader).
- **dev-mcp (wave 1):** the §8.5 sidecar image packaging convention (`deploy/MCP_IMAGES.md`) and the copyable CI job `build-mcp-dev-image` in `deploy/concourse-pipeline.yml`.
- **platform-mcp-hitl Task 9b (wave-3 sibling; HARD ORDERING DEPENDENCY — added 2026-07-09, SSE delta):** the in-place SSE upgrade of the shared `atc/api/mcpserver` — `const DefaultHeartbeat = 15 * time.Second`, `NewServerWithHeartbeat(d time.Duration)` (d <= 0 → DefaultHeartbeat), the BREAKING 3-arg `ToolHandler func(ctx context.Context, args json.RawMessage, progress func(string)) (any, error)`, and the SSE tools/call path (gated on `Accept: text/event-stream` + `params._meta.progressToken`; coalescing-ticker `notifications/progress` heartbeat frames; the final JSON-RPC response as the LAST SSE frame; a byte-similar mirrored port of `ci-agent/devmcp`'s proven implementation) — MUST land before this plan's Task 7. The gateway acquires SSE purely by consuming that upgrade: NO gateway-local transport code. 08 Task 18b (the real-CLI >5-minute park pin test) gates this plan's Task 7 merge; the gateway does not duplicate the CLI pin.

**Contract surfaces this plan PRODUCES** (`00-shared-contracts.md`):
- **§3.3 agent-gateway-mcp** — `request_review`/`ask_agent` tool schemas, the `GatewayUsage` result embed, the `Adapter` interface (`agent/gateway/adapter.go` per the contract; this plan places it in `agent/gatewaymcp/adapter.go` — see the Task 1 addendum and `contract_deviations`), and the cutoff contract.
- **§5** — the gateway is the named producer of `subagent.call`/`subagent.result`/`cost.record` (and `budget.warn`/`budget.stop` on cutoff); it emits them using agent-step's constants/payloads (no new constant added).
- **§8.5 instantiation** — `ghcr.io/tdmtrader/mcp-gateway` image + `build-mcp-gateway` CI job, copied from dev-mcp's template.

**Contract surfaces this plan CONSUMES** (section headings):
- §1.2 / §4.1 agent-principal auth (`AGENT_PRINCIPAL_TOKEN`, scope `costs:write`).
- §1.4 `agent_cost_ledger` + §2.7 budget library (`budget.Checker`, `budget.LedgerEntry`, `budget.SourceGateway`) + the `POST /api/v1/agent/costs` route.
- §1.13 / §8.2 platform-credential policy (the run credential the gateway sends provider calls with is `CLAUDE_CODE_OAUTH_TOKEN`, sourced by dispatch from the per-run or platform credential secret — the gateway reads whatever env it is given, never resolves credentials itself).
- §5 flight-recorder event schema (`agent/schema` constants + payloads).
- §8.1 env-var contract (fixed port 7782, `GATEWAY_MCP_URL`, `MCP_LISTEN_ADDR` override, `AGENT_BUDGET_SLICE_USD`, and — per the 2026-07-09 SSE delta — `GATEWAY_MCP_PROGRESS_INTERVAL`, whose normative definition lives in the amended §8.1).
- §8.5 sidecar image packaging convention + the dev-mcp CI job template.

**Verified code seams (line anchors current on branch `jetbridge` HEAD `fb1c54fac2`; waves 1–2 will have extended shared files — anchor to named neighbors, not raw numbers):**
- `atc/api/mcpserver/server.go:24` `NewServer()`, `:31` `AddTool(name, description, schema, handler)`, `:42` `ServeHTTP`, `:179` `MustJSON`. *(Corrected 2026-07-09, SSE delta: this line previously read "Streamable HTTP, buffered JSON — no SSE/progress" — stale after 08 Task 9b upgrades the shared server in place: `NewServerWithHeartbeat(d)` beside the unchanged `NewServer()`, `DefaultHeartbeat = 15s`, the 3-arg `ToolHandler` carrying `progress func(string)`, and an SSE tools/call path gated on `Accept: text/event-stream` + `params._meta.progressToken` with the final JSON-RPC response as the last SSE frame. Buffered JSON remains the behavior when the client doesn't opt in.)* The gateway reuses this verbatim (main-module package, unlike ci-agent's dev-mcp server).
- `ci-agent/llm/result.go:40-83` `cliEnvelope` + `ParseCLIEnvelope` (the claude `--output-format json` envelope: `cost_usd`, `usage.{input_tokens,output_tokens,cache_read_input_tokens,cache_creation_input_tokens}`, `num_turns`, `model`, `duration_ms`, `result`) — the reference the gateway's local parser mirrors; NOT importable (ci-agent is module `github.com/concourse/ci-agent` with no main-module dep).
- `ci-agent/llm/client.go:38-60` `ClaudeClient.Call` (argv `-p <prompt> --output-format json [--model m]`, `cmd.Dir`, stdout/stderr capture, `ctx.Err()` timeout handling) — the reference for the claude adapter's subprocess call.
- `ci-agent/adapter/adapter.go:19-29` `rawFinding` (title/description/file/line/severity_hint/category) — the shape a review prompt asks the model to emit; the gateway maps it to §3.3 `findings`.
- `agent/schema/event.go` merged `Event`/`EventType`, `agent/schema/event_writer.go` `NewEventWriter`/`Write`, `agent/schema/event_payloads.go` (`SubagentCallData` etc., landed by agent-step Task 4).
- `agent/budget/budget.go` `Checker`/`Remaining`/`LedgerEntry`/`SourceGateway`, `agent/budget/budgetfakes.FakeChecker` (credentials-and-budgets).
- `agent/platformmcp/*` (platform-mcp, wave 3 sibling) — the exact sidecar shape this plan mirrors: `Config`/`ConfigFromEnv`, `Server` with `mcpserver.NewServer()` + `/mcp` + `GET /healthz`, `cmd/platform-mcp/main.go` serve-mode binary, `agent/platformmcp/contracttest`, `deploy/Dockerfile.platform-mcp`, and its `build-mcp-platform` CI job.
- `deploy/concourse-pipeline.yml` — the live `cicd` pipeline; `build-mcp-dev-image` (appended by dev-mcp Task 13) is the copyable image-build job template.
- `atc/worker/jetbridge/live_sidecar_test.go` — live sidecar wiring pattern (memory: SC-11 sidecar log-stream needs a live cluster; `kubeClient(t)` + throwaway namespace).

---

### Task 1: Wave-start contract addendum (gateway ↔ platform-mcp packaging + metering agreements)

Three things need cross-workstream agreement recorded in §11 BEFORE code lands: (a) the gateway's packaging instantiation and the fact that gateway + platform-mcp both extend the same cicd pipeline (wave-3 parallel branches — the CI-job additions must merge additively, exactly like the `fly agent` struct agreement in the credentials addendum); (b) the gateway's event-log path env var (platform-mcp added `PLATFORM_MCP_EVENTS_PATH`; the gateway needs the analogous `GATEWAY_MCP_EVENTS_PATH`); (c) the `Adapter` interface package path (contracts §3.3 says `agent/gateway/adapter.go`; this plan uses one package `agent/gatewaymcp`, matching platform-mcp's single-package layout). A fourth item was added 2026-07-09 by the frozen SSE delta: (d) the HARD wave-3 ordering dependency on 08 Task 9b (the in-place SSE upgrade of `atc/api/mcpserver`) — the gateway acquires its SSE progress-heartbeat transport entirely by consuming that upgrade, with no gateway-local transport code, and 08 Task 18b (real-CLI >5-min park pin) gates this plan's Task 7 merge.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (append to `## 11. Amendment log`, currently ends after the platform-mcp-hitl entry)

**Steps:**

- [ ] Survey the landed wave-1/2/3-sibling seams and record the results into the `<fill>` markers of the addendum below (paste real command output; the markers must not survive into the committed text):

```bash
# budget library surface the gateway consumes (Checker methods, LedgerEntry, SourceGateway)
grep -n "SourceGateway\|func.*StepSlice\|func.*Record(\|type LedgerEntry\|type Remaining" agent/budget/*.go
# the gateway event constants/payloads agent-step landed
grep -n "EventSubagentCall\|EventSubagentResult\|EventCostRecord\|EventBudgetWarn\|EventBudgetStop\|SubagentCallData\|SubagentResultData\|CostRecordData\|BudgetData" agent/schema/*.go
# the cost-record route + request body the gateway POSTs to
grep -n "SubmitAgentCostRecord\|/api/v1/agent/costs" atc/routes.go
# platform-mcp's cicd image-job name + dev-mcp's template job, so the gateway job merges additively
grep -n "build-mcp-dev-image\|build-mcp-platform\|build-mcp-gateway" deploy/concourse-pipeline.yml
# platform-mcp's events-path env precedent
grep -rn "PLATFORM_MCP_EVENTS_PATH" docs/superpowers/plans/agentic-platform/00-shared-contracts.md
```

- [ ] Append this entry to the end of §11 of `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (with the survey `<fill>` markers replaced by the observed values):

```markdown
- 2026-07-08 (gateway-mcp wave-3 addendum; owner: gateway-mcp; consumers notified: scorecards, process-intel-experiments, platform-mcp-hitl, dispatch):
  - **Adapter package path:** contracts §3.3 sketches the adapter at `agent/gateway/adapter.go`; the gateway ships as ONE main-module package `agent/gatewaymcp` (mirroring platform-mcp's single-package `agent/platformmcp` layout), so the `Adapter` interface lives at `agent/gatewaymcp/adapter.go`. The interface signature (`Name() string`; `Invoke(ctx, Request, maxCostUSD float64) (*Response, error)`) is unchanged from §3.3. No consumer imports the adapter type by path (it is sidecar-internal); the change is package-path-only.
  - **§8.1 new row — `GATEWAY_MCP_EVENTS_PATH`** | gateway | literal | NDJSON event-log path where the sidecar writes its `subagent.*`/`cost.record`/`budget.*` flight-recorder events; unset = stdout (pod logs). Mirrors platform-mcp's `PLATFORM_MCP_EVENTS_PATH`. The renderer (dispatch, wave 4) points it at the agent step's shared flight dir so the events land in the same stream agent-step ingests; hand-written wave-3 pipelines may leave it unset (events go to pod logs).
  - **Metering sink = the cost-record route, not direct DB:** the gateway is a sidecar with no DB access; it meters by POSTing a `budget.LedgerEntry` JSON body (§2.7) to `POST /api/v1/agent/costs` (`SubmitAgentCostRecord`) with `Authorization: Bearer <AGENT_PRINCIPAL_TOKEN>` (scope `costs:write`), `Source: budget.SourceGateway` ("gateway"), `Metadata: {"workflow": "<name>@<version>"}` when workflow env tags are present (per the credentials wave-1 addendum). This write is FIRE-AND-FORGET: a non-2xx or transport error is logged to the event log and never fails the tool call (matches §1.4 "ledger writes never fail a build").
  - **Cutoff budget source:** the gateway enforces the cutoff against the per-STEP slice from env `AGENT_BUDGET_SLICE_USD` (§8.1), NOT by calling `budget.Checker` (the sidecar cannot reach the DB). The slice is the remaining headroom the agent step already resolved via `budget.Checker.StepSlice` at step start (agent-step exec). The gateway tracks its own cumulative spend across all calls in one sidecar lifetime and cuts a call off when `cumulative_before_call + running_call_cost >= slice`. `AGENT_BUDGET_SLICE_USD` unset or `0` = uncapped (the "0 = uncapped" convention, §2.8/§2.7 `Remaining`). This closes the "cutoff must not double-count" concern in the charter: dispatch admission and the agent-step slice are the DB-backed accounting; the gateway only enforces the already-sliced ceiling and reports spend back to the same ledger.
  - **gateway packaging (§8.5 instantiation):** source `agent/gatewaymcp` (main module), binary `cmd/gateway-mcp`, image `ghcr.io/tdmtrader/mcp-gateway` from `deploy/Dockerfile.mcp-gateway`; the image bundles the `claude` CLI (pinned). CI job `build-mcp-gateway` copies dev-mcp's `build-mcp-dev-image` template. gateway-mcp and platform-mcp are parallel wave-3 branches BOTH appending a job to `deploy/concourse-pipeline.yml`; the additions are non-overlapping (different job names, no shared `serial_groups`) and MUST merge additively — neither edits the other's job.
  - **SSE transport + wave-3 ordering (added 2026-07-09, frozen SSE delta):** the gateway acquires the SSE progress-heartbeat transport by consuming the upgraded shared `atc/api/mcpserver` (08 Task 9b: `NewServerWithHeartbeat`, 3-arg `ToolHandler` with `progress func(string)`, `DefaultHeartbeat` 15s) — NO gateway-local transport code. HARD ORDERING: 08 Task 9b lands before gateway Task 7, and 08 Task 18b (the real-CLI >5-minute park pin) gates gateway Task 7's merge (the gateway does not duplicate the CLI pin). `request_review` and `ask_agent` are MUST-stream tools per the §3-preamble rule (any handler that can block > 30s): a review call routinely exceeds the claude CLI's empirical 60s buffered-call abandonment — the F13 failure class. Heartbeat override env `GATEWAY_MCP_PROGRESS_INTERVAL` (normative §8.1 row added by the SSE delta): Go duration, default 15s; a set-but-invalid value, a value <= 0, or a value > 30s is a fatal startup error — never clamp silently.
  - Landed-seam survey results (recorded at execution time): budget cost-record route path = `<fill>`; gateway event constants present in agent/schema = `<fill: yes/no>`; dev-mcp CI template job name/location = `<fill>`; platform-mcp events-path env = `<fill>`.
```

- [ ] Verify the entry landed: `grep -n "gateway-mcp wave-3 addendum" docs/superpowers/plans/agentic-platform/00-shared-contracts.md` — expect one hit in §11.
- [ ] Commit:

```bash
git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md
git commit -m "docs(gateway-mcp): wave-3 contract addendum — adapter path, events-path env, metering sink, cutoff source" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Sidecar config + env parsing (`agent/gatewaymcp`)

The sidecar's runtime config, read entirely from the §8.1 env contract. Modeled on `agent/platformmcp/config.go` (`ConfigFromEnv`). Per the 2026-07-09 SSE delta (D3), the config also carries the SSE heartbeat override `GATEWAY_MCP_PROGRESS_INTERVAL`: Go duration syntax, default `mcpserver.DefaultHeartbeat` (15s — half of contracts §3.1's 30s progress bound, 4x margin under the claude CLI's empirical 60s abandonment cliff); a set-but-invalid value, a value <= 0, or a value > 30s is a FATAL config error — never clamp silently (same parse-or-die pattern as dev-mcp's `DEV_MCP_PROGRESS_INTERVAL`).

**Files:**
- Create: `agent/gatewaymcp/config.go`
- Test: `agent/gatewaymcp/config_test.go`

**Steps:**

- [ ] Write the failing test `agent/gatewaymcp/config_test.go`:

```go
package gatewaymcp_test

import (
	"testing"
	"time"

	"github.com/concourse/concourse/agent/gatewaymcp"
)

func TestConfigFromEnvDefaults(t *testing.T) {
	t.Setenv("ATC_EXTERNAL_URL", "https://concourse.home")
	t.Setenv("AGENT_PRINCIPAL_TOKEN", "cap1.9.secret")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-tok")
	// leave MCP_LISTEN_ADDR / AGENT_BUDGET_SLICE_USD unset
	t.Setenv("MCP_LISTEN_ADDR", "")
	t.Setenv("AGENT_BUDGET_SLICE_USD", "")

	cfg, err := gatewaymcp.ConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenAddr != ":7782" {
		t.Errorf("ListenAddr = %q, want :7782", cfg.ListenAddr)
	}
	if cfg.BudgetSliceUSD != 0 {
		t.Errorf("BudgetSliceUSD = %v, want 0 (uncapped)", cfg.BudgetSliceUSD)
	}
	if cfg.PrincipalToken != "cap1.9.secret" {
		t.Errorf("PrincipalToken = %q", cfg.PrincipalToken)
	}
}

func TestConfigFromEnvParsesSliceAndIdentity(t *testing.T) {
	t.Setenv("ATC_EXTERNAL_URL", "https://concourse.home")
	t.Setenv("AGENT_PRINCIPAL_TOKEN", "cap1.9.secret")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-tok")
	t.Setenv("AGENT_BUDGET_SLICE_USD", "2.50")
	t.Setenv("AGENT_TICKET_ID", "42")
	t.Setenv("AGENT_PIPELINE_RUN_ID", "7")
	t.Setenv("BUILD_ID", "123")
	t.Setenv("AGENT_WORKFLOW_NAME", "standard-dev")
	t.Setenv("AGENT_WORKFLOW_VERSION", "3")
	t.Setenv("MCP_LISTEN_ADDR", "127.0.0.1:9999")

	cfg, err := gatewaymcp.ConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BudgetSliceUSD != 2.50 {
		t.Errorf("BudgetSliceUSD = %v, want 2.50", cfg.BudgetSliceUSD)
	}
	if cfg.TicketID == nil || *cfg.TicketID != 42 {
		t.Errorf("TicketID = %v, want 42", cfg.TicketID)
	}
	if cfg.WorkflowTag() != "standard-dev@3" {
		t.Errorf("WorkflowTag() = %q, want standard-dev@3", cfg.WorkflowTag())
	}
	if cfg.ListenAddr != "127.0.0.1:9999" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
}

func TestConfigFromEnvRequiresPrincipalToken(t *testing.T) {
	t.Setenv("ATC_EXTERNAL_URL", "https://concourse.home")
	t.Setenv("AGENT_PRINCIPAL_TOKEN", "")
	if _, err := gatewaymcp.ConfigFromEnv(); err == nil {
		t.Fatal("expected error when AGENT_PRINCIPAL_TOKEN is missing")
	}
}

// SSE delta D3 (2026-07-09): heartbeat default + override + hard bounds.
func TestConfigFromEnvProgressIntervalDefaultsTo15s(t *testing.T) {
	t.Setenv("ATC_EXTERNAL_URL", "https://concourse.home")
	t.Setenv("AGENT_PRINCIPAL_TOKEN", "cap1.9.secret")
	t.Setenv("GATEWAY_MCP_PROGRESS_INTERVAL", "")

	cfg, err := gatewaymcp.ConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ProgressInterval != 15*time.Second {
		t.Errorf("ProgressInterval = %v, want 15s (mcpserver.DefaultHeartbeat)", cfg.ProgressInterval)
	}
}

func TestConfigFromEnvProgressIntervalOverrideAndBounds(t *testing.T) {
	t.Setenv("ATC_EXTERNAL_URL", "https://concourse.home")
	t.Setenv("AGENT_PRINCIPAL_TOKEN", "cap1.9.secret")

	t.Setenv("GATEWAY_MCP_PROGRESS_INTERVAL", "10s")
	cfg, err := gatewaymcp.ConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ProgressInterval != 10*time.Second {
		t.Errorf("ProgressInterval = %v, want 10s", cfg.ProgressInterval)
	}

	// Set-but-invalid, <= 0, or > 30s are FATAL — never clamped (SSE delta D3).
	for _, bad := range []string{"banana", "0", "-5s", "31s", "45s", "1h"} {
		t.Setenv("GATEWAY_MCP_PROGRESS_INTERVAL", bad)
		if _, err := gatewaymcp.ConfigFromEnv(); err == nil {
			t.Errorf("GATEWAY_MCP_PROGRESS_INTERVAL=%q: expected fatal config error", bad)
		}
	}
}
```

- [ ] Run to verify it fails: `go test ./agent/gatewaymcp/` — expect `no Go files` / undefined `gatewaymcp`.
- [ ] Write `agent/gatewaymcp/config.go`:

```go
// Package gatewaymcp is the agent-gateway-mcp sidecar (shared contracts §3.3):
// provider-agnostic subagent access (request_review, ask_agent) over an
// adapter layer, with universal metering into the flight recorder and cost
// ledger and a budget-slice cutoff that never silently truncates. It serves
// MCP streamable HTTP on MCP_LISTEN_ADDR and, for metering, calls the ATC cost
// route with its per-run principal token.
package gatewaymcp

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc/api/mcpserver"
)

// Config is the sidecar's runtime configuration, read from the §8.1 env
// contract. ATCURL/PrincipalToken are required for metering; the rest are
// optional identity/provenance tags and the budget-slice ceiling.
type Config struct {
	ATCURL         string   // ATC_EXTERNAL_URL (required — metering sink base URL)
	PrincipalToken string   // AGENT_PRINCIPAL_TOKEN (required — scope costs:write)
	OAuthToken     string   // CLAUDE_CODE_OAUTH_TOKEN (passed to provider CLIs)
	TicketID       *int     // AGENT_TICKET_ID (nil = pure-CI)
	PipelineRunID  *int     // AGENT_PIPELINE_RUN_ID
	BuildID        int      // BUILD_ID (0 = none)
	WorkflowName   string   // AGENT_WORKFLOW_NAME (provenance tag)
	WorkflowVersion string  // AGENT_WORKFLOW_VERSION (provenance tag, string form)
	BudgetSliceUSD float64  // AGENT_BUDGET_SLICE_USD (0 = uncapped)
	ListenAddr     string   // MCP_LISTEN_ADDR (default :7782, §8.1)
	EventsPath     string   // GATEWAY_MCP_EVENTS_PATH ("" = stdout; Task 1 addendum)
	ClaudeCLI      string   // GATEWAY_CLAUDE_CLI (default "claude"; test override)
	// ProgressInterval is the SSE heartbeat cadence (GATEWAY_MCP_PROGRESS_INTERVAL,
	// SSE delta D3 2026-07-09). Default mcpserver.DefaultHeartbeat (15s).
	// Set-but-invalid, <= 0, or > 30s (§3.1 progress bound) is a fatal config
	// error — never clamped silently.
	ProgressInterval time.Duration
}

// WorkflowTag is the "<name>@<version>" ledger metadata value (Task 1
// addendum / credentials wave-1 addendum). Empty when either tag is unset.
func (c Config) WorkflowTag() string {
	if c.WorkflowName == "" || c.WorkflowVersion == "" {
		return ""
	}
	return c.WorkflowName + "@" + c.WorkflowVersion
}

func ConfigFromEnv() (Config, error) {
	cfg := Config{
		ATCURL:          os.Getenv("ATC_EXTERNAL_URL"),
		PrincipalToken:  os.Getenv("AGENT_PRINCIPAL_TOKEN"),
		OAuthToken:      os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"),
		WorkflowName:    os.Getenv("AGENT_WORKFLOW_NAME"),
		WorkflowVersion: os.Getenv("AGENT_WORKFLOW_VERSION"),
		ListenAddr:      os.Getenv("MCP_LISTEN_ADDR"),
		EventsPath:      os.Getenv("GATEWAY_MCP_EVENTS_PATH"),
		ClaudeCLI:       os.Getenv("GATEWAY_CLAUDE_CLI"),
	}
	if cfg.PrincipalToken == "" {
		return cfg, fmt.Errorf("AGENT_PRINCIPAL_TOKEN is required")
	}
	// ATCURL missing is tolerated (metering becomes a no-op that logs) so the
	// sidecar still serves tools in a hand-written pipeline without a run; but
	// warn by returning it empty — the caller decides. Everything else optional.
	var err error
	if cfg.TicketID, err = optIntEnv("AGENT_TICKET_ID"); err != nil {
		return cfg, err
	}
	if cfg.PipelineRunID, err = optIntEnv("AGENT_PIPELINE_RUN_ID"); err != nil {
		return cfg, err
	}
	if raw := os.Getenv("BUILD_ID"); raw != "" {
		if cfg.BuildID, err = strconv.Atoi(raw); err != nil {
			return cfg, fmt.Errorf("BUILD_ID must be an integer: %w", err)
		}
	}
	if raw := os.Getenv("AGENT_BUDGET_SLICE_USD"); raw != "" {
		if cfg.BudgetSliceUSD, err = strconv.ParseFloat(raw, 64); err != nil {
			return cfg, fmt.Errorf("AGENT_BUDGET_SLICE_USD must be a float: %w", err)
		}
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":7782"
	}
	if cfg.ClaudeCLI == "" {
		cfg.ClaudeCLI = "claude"
	}
	// SSE heartbeat (SSE delta D3): default 15s; hard-bounded below the §3.1
	// 30s progress mandate. Fatal on bad values — a silently clamped heartbeat
	// would mask the F13 60s claude-CLI abandonment class in production.
	cfg.ProgressInterval = mcpserver.DefaultHeartbeat
	if raw := os.Getenv("GATEWAY_MCP_PROGRESS_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return cfg, fmt.Errorf("GATEWAY_MCP_PROGRESS_INTERVAL must be a Go duration: %w", err)
		}
		if d <= 0 {
			return cfg, fmt.Errorf("GATEWAY_MCP_PROGRESS_INTERVAL must be > 0, got %s", d)
		}
		if d > 30*time.Second {
			return cfg, fmt.Errorf("GATEWAY_MCP_PROGRESS_INTERVAL must be <= 30s (contracts §3.1 progress bound), got %s", d)
		}
		cfg.ProgressInterval = d
	}
	return cfg, nil
}

// optIntEnv parses an optional integer env var. Empty = nil (not set).
func optIntEnv(name string) (*int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return &v, nil
}
```

- [ ] Run to verify pass: `go test ./agent/gatewaymcp/` — expect PASS.
- [ ] Commit:

```bash
git add agent/gatewaymcp/config.go agent/gatewaymcp/config_test.go
git commit -m "feat(gateway-mcp): sidecar env config" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Provider-adapter interface + counterfeiter fake

Produces §3.3's `Adapter` interface and the `Request`/`Response` types the tools dispatch through. Placed at `agent/gatewaymcp/adapter.go` per the Task 1 addendum (single-package layout). The interface is the seam that lets `codex`/`cursor`/a future pod backend land later without touching the tools.

**Files:**
- Create: `agent/gatewaymcp/adapter.go`
- Test: `agent/gatewaymcp/adapter_test.go`

**Steps:**

- [ ] Write the failing test `agent/gatewaymcp/adapter_test.go`:

```go
package gatewaymcp_test

import (
	"context"
	"testing"

	"github.com/concourse/concourse/agent/gatewaymcp"
	"github.com/concourse/concourse/agent/gatewaymcp/gatewaymcpfakes"
)

func TestAdapterFakeSatisfiesInterface(t *testing.T) {
	var a gatewaymcp.Adapter = new(gatewaymcpfakes.FakeAdapter)
	fake := a.(*gatewaymcpfakes.FakeAdapter)
	fake.NameReturns("claude")
	fake.InvokeReturns(&gatewaymcp.Response{Answer: "ok", Usage: gatewaymcp.Usage{Provider: "claude", Model: "m", CostUSD: 0.1, DurationMS: 5}}, nil)

	if a.Name() != "claude" {
		t.Errorf("Name() = %q", a.Name())
	}
	resp, err := a.Invoke(context.Background(), gatewaymcp.Request{Prompt: "hi"}, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Answer != "ok" || resp.Usage.CostUSD != 0.1 {
		t.Errorf("unexpected response: %+v", resp)
	}
}
```

- [ ] Run to verify it fails: `go test ./agent/gatewaymcp/` — expect undefined `gatewaymcp.Adapter` / missing `gatewaymcpfakes`.
- [ ] Write `agent/gatewaymcp/adapter.go`:

```go
package gatewaymcp

import "context"

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// Usage is the normalized per-call metering record embedded in every tool
// result as GatewayUsage (§3.3) and emitted as subagent.result + cost.record
// events (§5). Cost normalization happens at the adapter boundary: each
// adapter fills these fields in USD/token/turn units regardless of provider.
type Usage struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	// CacheReadTokens/CacheCreationTokens carry the claude envelope's
	// usage.cache_read_input_tokens / usage.cache_creation_input_tokens. They
	// are NOT part of §3.3 GatewayUsage or the subagent.result event (neither
	// lists cache tokens), but the cost.record event and the ledger row MUST
	// carry them: the gateway is the named producer of cost.record (§5), whose
	// payload "mirrors budget.LedgerEntry (§2.7): {…, cache_read_tokens,
	// cache_creation_tokens, …}", and agent_cost_ledger (§1.4) has both columns.
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	Turns        int     `json:"turns"`
	CostUSD      float64 `json:"cost_usd"`
	DurationMS   int     `json:"duration_ms"`
	// Partial is true when the adapter could only meter turns/latency (not
	// tokens/cost) for this provider — scorecards label these so they don't
	// lie (charter risk 2). Never emitted to §3.3 GatewayUsage; internal only.
	Partial bool `json:"-"`
}

// Request is one subagent invocation. Tool is "request_review" or "ask_agent".
// For request_review, Prompt is the assembled review prompt (diff + rubric +
// context) and OutputSchema requests the findings JSON shape; for ask_agent,
// Prompt is the raw prompt and OutputSchema is the optional caller schema.
type Request struct {
	Tool         string // request_review | ask_agent
	Prompt       string
	Model        string // provider-specific; "" = adapter default
	OutputSchema string // optional JSON schema the answer must satisfy ("" = free text)
}

// Response is one subagent result. Answer is the model's text (or a JSON
// string when OutputSchema was given). Usage carries normalized metering.
type Response struct {
	Answer string
	Usage  Usage
}

// Adapter runs one subagent call in-sidecar (§3.3). maxCostUSD is the
// REMAINING budget slice for this call (0 = uncapped); the adapter passes
// provider-native limits where they exist and MUST return a context-cancelled
// error if the caller cancels ctx (the gateway hard-stops on cutoff by
// cancelling ctx and reading Usage-so-far from the returned error's Response).
//
//counterfeiter:generate . Adapter
type Adapter interface {
	Name() string // "claude" | "codex" | "cursor"
	Invoke(ctx context.Context, req Request, maxCostUSD float64) (*Response, error)
}
```

- [ ] Generate the fake: `go generate ./agent/gatewaymcp/` (creates `agent/gatewaymcp/gatewaymcpfakes/fake_adapter.go`).
- [ ] Run to verify pass: `go test ./agent/gatewaymcp/` — expect PASS.
- [ ] Commit:

```bash
git add agent/gatewaymcp/adapter.go agent/gatewaymcp/adapter_test.go agent/gatewaymcp/gatewaymcpfakes/
git commit -m "feat(gateway-mcp): provider-adapter interface, Request/Response/Usage types, counterfeiter fake" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Claude CLI adapter — in-sidecar exec + envelope/cost parsing

The v1 `claude` adapter. Runs the bundled `claude` CLI (`-p <prompt> --output-format json [--model m]`) as a subprocess and parses the JSON envelope for cost/usage. Re-implements `ci-agent/llm.ParseCLIEnvelope`'s parse locally because ci-agent is a separate, non-importable module (see `contract_deviations`); the envelope field names are copied verbatim from `ci-agent/llm/result.go:40-52` so the two stay wire-compatible.

**Files:**
- Create: `agent/gatewaymcp/claude.go`
- Test: `agent/gatewaymcp/claude_test.go`

**Steps:**

- [ ] Write the failing test `agent/gatewaymcp/claude_test.go` (uses a fake CLI script so no real `claude` is needed):

```go
package gatewaymcp_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/gatewaymcp"
)

// writeFakeCLI writes a shell script that prints a fixed claude JSON envelope
// on stdout and returns its path.
func writeFakeCLI(t *testing.T, envelope string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude")
	script := "#!/bin/sh\ncat <<'EOF'\n" + envelope + "\nEOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClaudeAdapterParsesEnvelope(t *testing.T) {
	envelope := `{"type":"result","subtype":"success","result":"the answer","model":"claude-sonnet-4-5","cost_usd":0.1234,"duration_ms":850,"num_turns":3,"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}`
	cli := writeFakeCLI(t, envelope)
	a := gatewaymcp.NewClaudeAdapter(cli, "oauth-tok")

	if a.Name() != "claude" {
		t.Fatalf("Name() = %q", a.Name())
	}
	resp, err := a.Invoke(context.Background(), gatewaymcp.Request{Tool: "ask_agent", Prompt: "hi"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Answer != "the answer" {
		t.Errorf("Answer = %q", resp.Answer)
	}
	if resp.Usage.CostUSD != 0.1234 || resp.Usage.InputTokens != 100 || resp.Usage.OutputTokens != 50 || resp.Usage.Turns != 3 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Usage.CacheReadTokens != 10 || resp.Usage.CacheCreationTokens != 5 {
		t.Errorf("cache tokens = %+v", resp.Usage)
	}
	if resp.Usage.Provider != "claude" || resp.Usage.Model != "claude-sonnet-4-5" {
		t.Errorf("provider/model = %+v", resp.Usage)
	}
	if resp.Usage.DurationMS < 1 {
		t.Errorf("DurationMS not measured: %d", resp.Usage.DurationMS)
	}
}

func TestClaudeAdapterPropagatesCLIError(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "boom")
	// exits 1 with a stderr message
	if err := os.WriteFile(cli, []byte("#!/bin/sh\necho boom >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := gatewaymcp.NewClaudeAdapter(cli, "")
	if _, err := a.Invoke(context.Background(), gatewaymcp.Request{Prompt: "x"}, 0); err == nil {
		t.Fatal("expected CLI error")
	}
}

func TestClaudeAdapterRespectsContextCancel(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "slow")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := gatewaymcp.NewClaudeAdapter(cli, "")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := a.Invoke(ctx, gatewaymcp.Request{Prompt: "x"}, 0); err == nil {
		t.Fatal("expected cancellation error")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("Invoke did not honor ctx cancellation")
	}
}
```

- [ ] Run to verify it fails: `go test ./agent/gatewaymcp/` — expect undefined `NewClaudeAdapter`.
- [ ] Write `agent/gatewaymcp/claude.go`:

```go
package gatewaymcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// ClaudeAdapter runs the bundled claude CLI in-sidecar. The claude CLI reads
// its OAuth token from the CLAUDE_CODE_OAUTH_TOKEN env var (spec-verified
// headless variable), so the adapter injects it into the subprocess env.
type ClaudeAdapter struct {
	cli        string
	oauthToken string
}

// NewClaudeAdapter builds the claude adapter. cli defaults to "claude".
func NewClaudeAdapter(cli, oauthToken string) *ClaudeAdapter {
	if cli == "" {
		cli = "claude"
	}
	return &ClaudeAdapter{cli: cli, oauthToken: oauthToken}
}

func (a *ClaudeAdapter) Name() string { return "claude" }

// claudeEnvelope mirrors ci-agent/llm/result.go cliEnvelope (verified
// field-for-field). ci-agent is a separate module the main module cannot
// import, so this is re-declared locally; the JSON tags keep the two in sync.
type claudeEnvelope struct {
	Type       string          `json:"type"`
	Result     json.RawMessage `json:"result"`
	Model      string          `json:"model"`
	CostUSD    float64         `json:"cost_usd"`
	DurationMS int             `json:"duration_ms"`
	NumTurns   int             `json:"num_turns"`
	IsError    bool            `json:"is_error"`
	Usage      struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// Invoke runs one claude call. maxCostUSD is advisory for the CLI (claude has
// no native cost ceiling in v1), so the gateway's cutoff (Task 6) enforces it
// by cancelling ctx; the adapter surfaces ctx cancellation as an error and
// the wall-clock DurationMS regardless.
func (a *ClaudeAdapter) Invoke(ctx context.Context, req Request, maxCostUSD float64) (*Response, error) {
	args := []string{"-p", req.Prompt, "--output-format", "json"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}

	cmd := exec.CommandContext(ctx, a.cli, args...)
	cmd.Env = os.Environ()
	if a.oauthToken != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_CODE_OAUTH_TOKEN="+a.oauthToken)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	elapsedMS := int(time.Since(start).Milliseconds())
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("claude call cancelled after %dms: %w", elapsedMS, ctx.Err())
		}
		return nil, fmt.Errorf("claude cli error (%v): %s", err, stderr.String())
	}

	var env claudeEnvelope
	if uerr := json.Unmarshal(stdout.Bytes(), &env); uerr != nil || env.Type == "" {
		// Not a recognized envelope — treat the whole stdout as the answer,
		// meter only wall-clock (partial metering, labeled).
		return &Response{
			Answer: stdout.String(),
			Usage: Usage{
				Provider: "claude", Model: req.Model, DurationMS: elapsedMS, Partial: true,
			},
		}, nil
	}
	if env.IsError {
		return nil, fmt.Errorf("claude reported is_error: %s", stderr.String())
	}

	// The "result" field is a JSON string; unquote it to plain text.
	answer := string(env.Result)
	var s string
	if len(env.Result) > 0 && env.Result[0] == '"' && json.Unmarshal(env.Result, &s) == nil {
		answer = s
	}

	return &Response{
		Answer: answer,
		Usage: Usage{
			Provider:            "claude",
			Model:               env.Model,
			InputTokens:         env.Usage.InputTokens,
			OutputTokens:        env.Usage.OutputTokens,
			CacheReadTokens:     env.Usage.CacheReadInputTokens,
			CacheCreationTokens: env.Usage.CacheCreationInputTokens,
			Turns:               env.NumTurns,
			CostUSD:             env.CostUSD,
			DurationMS:          elapsedMS,
		},
	}, nil
}
```

- [ ] Run to verify pass: `go test ./agent/gatewaymcp/` — expect PASS.
- [ ] Commit:

```bash
git add agent/gatewaymcp/claude.go agent/gatewaymcp/claude_test.go
git commit -m "feat(gateway-mcp): claude CLI adapter with envelope cost/usage parsing" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: Metering — flight-recorder events + fire-and-forget ledger client

The universal metering point. `Meter` emits `subagent.call`/`subagent.result`/`cost.record` (and `budget.warn`/`budget.stop` on cutoff) to the NDJSON event log using agent-step's constants + payloads, and POSTs a `budget.LedgerEntry` to `/api/v1/agent/costs` fire-and-forget. All writes are best-effort: failures are logged to the event log, never returned to the tool.

**Files:**
- Create: `agent/gatewaymcp/meter.go`
- Test: `agent/gatewaymcp/meter_test.go`

**Steps:**

- [ ] Write the failing test `agent/gatewaymcp/meter_test.go`:

```go
package gatewaymcp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/gatewaymcp"
	"github.com/concourse/concourse/agent/schema"
)

func TestMeterEmitsEventsAndPostsLedger(t *testing.T) {
	var mu sync.Mutex
	var posted []budget.LedgerEntry
	atc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/costs" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer cap1.9.secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var e budget.LedgerEntry
		_ = json.Unmarshal(body, &e)
		mu.Lock()
		posted = append(posted, e)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer atc.Close()

	tid := 42
	cfg := gatewaymcp.Config{
		ATCURL: atc.URL, PrincipalToken: "cap1.9.secret",
		TicketID: &tid, BuildID: 123, WorkflowName: "standard-dev", WorkflowVersion: "3",
	}
	var eventBuf bytes.Buffer
	m := gatewaymcp.NewMeter(cfg, schema.NewEventWriter(&eventBuf), atc.Client())

	callID := m.CallStart("request_review", "claude", "claude-sonnet-4-5", 512)
	m.CallResult(callID, "request_review", "ok", gatewaymcp.Usage{
		Provider: "claude", Model: "claude-sonnet-4-5",
		InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10, CacheCreationTokens: 5,
		Turns: 3, CostUSD: 0.42, DurationMS: 850,
	}, intPtr(2))
	m.Flush() // block for the fire-and-forget POST in the test

	events := eventBuf.String()
	if !strings.Contains(events, `"subagent.call"`) {
		t.Errorf("missing subagent.call event: %s", events)
	}
	if !strings.Contains(events, `"subagent.result"`) {
		t.Errorf("missing subagent.result event: %s", events)
	}
	if !strings.Contains(events, `"cost.record"`) {
		t.Errorf("missing cost.record event: %s", events)
	}
	// cost.record mirrors budget.LedgerEntry (§5), so it MUST carry cache tokens.
	if !strings.Contains(events, `"cache_read_tokens":10`) || !strings.Contains(events, `"cache_creation_tokens":5`) {
		t.Errorf("cost.record missing cache tokens: %s", events)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(posted) != 1 {
		t.Fatalf("expected 1 ledger POST, got %d", len(posted))
	}
	e := posted[0]
	if e.Source != budget.SourceGateway {
		t.Errorf("Source = %q, want %q", e.Source, budget.SourceGateway)
	}
	if e.TicketID == nil || *e.TicketID != 42 || e.CostUSD != 0.42 || e.Turns != 3 {
		t.Errorf("ledger entry = %+v", e)
	}
	if e.CacheReadTokens != 10 || e.CacheCreationTokens != 5 {
		t.Errorf("ledger entry missing cache tokens: %+v", e)
	}
	if !strings.Contains(string(e.Metadata), `"workflow":"standard-dev@3"`) {
		t.Errorf("metadata missing workflow tag: %s", e.Metadata)
	}
}

func TestMeterLedgerFailureNeverPanics(t *testing.T) {
	atc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer atc.Close()
	var eventBuf bytes.Buffer
	m := gatewaymcp.NewMeter(gatewaymcp.Config{ATCURL: atc.URL, PrincipalToken: "t"},
		schema.NewEventWriter(&eventBuf), atc.Client())
	id := m.CallStart("ask_agent", "claude", "m", 10)
	m.CallResult(id, "ask_agent", "ok", gatewaymcp.Usage{Provider: "claude", CostUSD: 0.01}, nil)
	m.Flush() // must not panic despite the 500
}

func intPtr(i int) *int { return &i }
```

- [ ] Run to verify it fails: `go test ./agent/gatewaymcp/` — expect undefined `NewMeter`.
- [ ] Write `agent/gatewaymcp/meter.go`:

```go
package gatewaymcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/schema"
)

// Meter is the universal metering point (§5, charter scope-in 3). Every tool
// call emits subagent.call / subagent.result / cost.record events and POSTs a
// ledger row. All writes are fire-and-forget: a failure is recorded as an
// `error` event and never propagates to the tool result.
type Meter struct {
	cfg    Config
	events *schema.EventWriter
	http   *http.Client

	mu     sync.Mutex
	seq    int
	wg     sync.WaitGroup // tracks in-flight ledger POSTs (Flush waits on them)
}

// NewMeter builds a Meter. httpClient may be nil (defaults to a 30s client).
func NewMeter(cfg Config, events *schema.EventWriter, httpClient *http.Client) *Meter {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Meter{cfg: cfg, events: events, http: httpClient}
}

// CallStart records subagent.call and returns a call id used to correlate the
// later CallResult (and any budget.warn/stop for the same call).
func (m *Meter) CallStart(tool, provider, model string, promptChars int) string {
	m.mu.Lock()
	m.seq++
	id := "gw-" + strconv.Itoa(m.seq)
	m.mu.Unlock()
	m.emit(schema.EventSubagentCall, schema.SubagentCallData{
		CallID: id, Tool: tool, Provider: provider, Model: model, PromptChars: promptChars,
	})
	return id
}

// CallResult records subagent.result + cost.record and POSTs the ledger row.
// findingCount is nil for ask_agent (no findings).
func (m *Meter) CallResult(callID, tool, status string, u Usage, findingCount *int) {
	m.emit(schema.EventSubagentResult, schema.SubagentResultData{
		CallID: callID, Status: status, Provider: u.Provider, Model: u.Model,
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, Turns: u.Turns,
		CostUSD: u.CostUSD, DurationMS: u.DurationMS, FindingCount: findingCount,
	})
	m.emit(schema.EventCostRecord, schema.CostRecordData{
		Source: budget.SourceGateway, Provider: u.Provider, Model: u.Model,
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CacheReadTokens: u.CacheReadTokens, CacheCreationTokens: u.CacheCreationTokens,
		Turns: u.Turns, CostUSD: u.CostUSD,
	})
	m.postLedger(u)
}

// BudgetWarn / BudgetStop emit the §5 budget events (cutoff, Task 6).
func (m *Meter) BudgetWarn(limit, spent, remaining float64) {
	m.emit(schema.EventBudgetWarn, schema.BudgetData{
		Scope: "step", LimitUSD: limit, SpentUSD: spent, RemainingUSD: remaining,
	})
}

func (m *Meter) BudgetStop(limit, spent, remaining float64) {
	m.emit(schema.EventBudgetStop, schema.BudgetData{
		Scope: "step", LimitUSD: limit, SpentUSD: spent, RemainingUSD: remaining,
	})
}

// emit marshals a payload struct into the event Data and writes one NDJSON
// line. A write failure is best-effort logged to stderr via a fallback error
// event; it never returns.
func (m *Meter) emit(t schema.EventType, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.events.Write(schema.Event{Type: t, Data: data})
}

// postLedger POSTs a budget.LedgerEntry fire-and-forget. Errors are recorded
// as an `error` event, never returned.
func (m *Meter) postLedger(u Usage) {
	if m.cfg.ATCURL == "" {
		return // no run attached (hand-written pipeline without a ticket)
	}
	entry := budget.LedgerEntry{
		TicketID: m.cfg.TicketID, PipelineRunID: m.cfg.PipelineRunID, BuildID: m.cfg.BuildID,
		Source: budget.SourceGateway, Provider: u.Provider, Model: u.Model,
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CacheReadTokens: u.CacheReadTokens, CacheCreationTokens: u.CacheCreationTokens,
		Turns: u.Turns, CostUSD: u.CostUSD,
	}
	if tag := m.cfg.WorkflowTag(); tag != "" {
		entry.Metadata = json.RawMessage(fmt.Sprintf(`{"workflow":%q}`, tag))
	}
	body, err := json.Marshal(entry)
	if err != nil {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		url := m.cfg.ATCURL + "/api/v1/agent/costs"
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			m.emit(schema.EventError, map[string]string{"where": "gateway-ledger", "error": err.Error()})
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+m.cfg.PrincipalToken)
		resp, err := m.http.Do(req)
		if err != nil {
			m.emit(schema.EventError, map[string]string{"where": "gateway-ledger", "error": err.Error()})
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			m.emit(schema.EventError, map[string]string{"where": "gateway-ledger", "status": resp.Status})
		}
	}()
}

// Flush waits for all in-flight ledger POSTs (used by the binary on shutdown
// and by tests). Safe to call repeatedly.
func (m *Meter) Flush() { m.wg.Wait() }
```

- [ ] Run to verify pass: `go test ./agent/gatewaymcp/` — expect PASS.
- [ ] Commit:

```bash
git add agent/gatewaymcp/meter.go agent/gatewaymcp/meter_test.go
git commit -m "feat(gateway-mcp): universal metering — flight-recorder events + fire-and-forget ledger POST" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: Budget-slice cutoff

The cutoff library. `SliceGuard` tracks cumulative gateway spend across all calls in one sidecar lifetime against `AGENT_BUDGET_SLICE_USD` and decides whether a call may start and, on cutoff, produces the `budget cutoff:` `failed` signal (never a truncated `ok`). Enforcement is polling-based: the adapter runs under a context the guard cancels when the metered running cost of a call plus prior cumulative spend would cross the ceiling. Because the claude CLI reports cost only at the end of a call, v1 cutoff is *pre-call admission* (a call that starts with the slice already exhausted is refused) plus *post-call accounting* (the call that pushed cumulative over the ceiling is the last one allowed) — documented as the v1 heuristic in Execution notes.

**Files:**
- Create: `agent/gatewaymcp/cutoff.go`
- Test: `agent/gatewaymcp/cutoff_test.go`

**Steps:**

- [ ] Write the failing test `agent/gatewaymcp/cutoff_test.go`:

```go
package gatewaymcp_test

import (
	"testing"

	"github.com/concourse/concourse/agent/gatewaymcp"
)

func TestSliceGuardUncappedAlwaysAdmits(t *testing.T) {
	g := gatewaymcp.NewSliceGuard(0) // 0 = uncapped
	if !g.Admit() {
		t.Fatal("uncapped guard must admit")
	}
	g.Record(1000.0)
	if !g.Admit() {
		t.Fatal("uncapped guard must still admit after huge spend")
	}
}

func TestSliceGuardRefusesWhenExhausted(t *testing.T) {
	g := gatewaymcp.NewSliceGuard(1.00)
	if !g.Admit() {
		t.Fatal("fresh guard under cap must admit")
	}
	g.Record(0.60)
	if !g.Admit() {
		t.Fatal("still under cap (0.60 < 1.00) must admit")
	}
	g.Record(0.60) // cumulative 1.20 >= 1.00
	if g.Admit() {
		t.Fatal("over cap must refuse")
	}
	rem := g.Remaining()
	if !rem.Exhausted {
		t.Errorf("Remaining().Exhausted = false, want true (%+v)", rem)
	}
	if rem.SpentUSD != 1.20 || rem.LimitUSD != 1.00 {
		t.Errorf("Remaining() = %+v", rem)
	}
}

func TestSliceGuardWarnAt80Percent(t *testing.T) {
	g := gatewaymcp.NewSliceGuard(1.00)
	g.Record(0.50)
	if g.ShouldWarn() {
		t.Error("50% must not warn")
	}
	g.Record(0.35) // cumulative 0.85 >= 80%
	if !g.ShouldWarn() {
		t.Error("85% must warn")
	}
	if !g.ShouldWarn() { // idempotent read is fine; but Warned() latches
	}
	g.MarkWarned()
	if g.ShouldWarn() {
		t.Error("ShouldWarn must be false after MarkWarned (emit once)")
	}
}
```

- [ ] Run to verify it fails: `go test ./agent/gatewaymcp/` — expect undefined `NewSliceGuard`.
- [ ] Write `agent/gatewaymcp/cutoff.go`:

```go
package gatewaymcp

import "sync"

// CutoffPrefix marks the summary of a budget-cut-off tool result. The result
// status is "failed" (never "ok"), so the calling agent sees a partial work
// product surfacing as needs-review, never a silently truncated success
// (§3.3 cutoff contract, spec §Failure-handling "Never silent truncation").
const CutoffPrefix = "budget cutoff:"

// SliceGuard enforces the per-step budget slice (AGENT_BUDGET_SLICE_USD) as a
// cumulative ceiling across every gateway call in one sidecar lifetime. It
// does NOT reach the DB — the slice is the headroom the agent step already
// resolved via budget.Checker.StepSlice at step start (Task 1 addendum). The
// gateway only enforces that already-sliced ceiling, so there is no
// double-counting with dispatch admission.
type SliceGuard struct {
	limit float64 // 0 = uncapped
	mu    sync.Mutex
	spent float64
	warned bool
}

// NewSliceGuard builds a guard. limit <= 0 means uncapped.
func NewSliceGuard(limit float64) *SliceGuard {
	return &SliceGuard{limit: limit}
}

// Admit reports whether a new call may start. Uncapped guards always admit;
// otherwise a call is refused once cumulative spend has reached the ceiling.
func (g *SliceGuard) Admit() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.limit <= 0 {
		return true
	}
	return g.spent < g.limit
}

// Record adds a completed call's cost to cumulative spend.
func (g *SliceGuard) Record(costUSD float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.spent += costUSD
}

// ShouldWarn reports whether cumulative spend has crossed 80% of the ceiling
// and no warn has yet been emitted (budget.warn is emitted once, §5).
func (g *SliceGuard) ShouldWarn() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.limit <= 0 || g.warned {
		return false
	}
	return g.spent >= 0.8*g.limit
}

// MarkWarned latches the warn so ShouldWarn returns false afterwards.
func (g *SliceGuard) MarkWarned() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.warned = true
}

// Remaining reports the current headroom in the budget.Remaining shape
// (LimitUSD == 0 => uncapped).
func (g *SliceGuard) Remaining() Remaining {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.limit <= 0 {
		return Remaining{LimitUSD: 0, SpentUSD: g.spent}
	}
	rem := g.limit - g.spent
	return Remaining{
		LimitUSD: g.limit, SpentUSD: g.spent, RemainingUSD: rem, Exhausted: rem <= 0,
	}
}

// Remaining mirrors budget.Remaining (§2.7) without a cross-package import
// (the sidecar already depends on agent/budget only for LedgerEntry/Source).
type Remaining struct {
	LimitUSD     float64
	SpentUSD     float64
	RemainingUSD float64
	Exhausted    bool
}
```

- [ ] Run to verify pass: `go test ./agent/gatewaymcp/` — expect PASS.
- [ ] Commit:

```bash
git add agent/gatewaymcp/cutoff.go agent/gatewaymcp/cutoff_test.go
git commit -m "feat(gateway-mcp): budget-slice cutoff guard (never silent truncation)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: MCP server assembly — `request_review` + `ask_agent` tools

Wires the adapter, meter, and slice guard into the two MCP tools. Schemas are byte-for-byte §3.3. Invalid input (missing or unparseable arguments, validated inside the tool handler) is an MCP tool error (`isError=true`) — the shared `atc/api/mcpserver` maps every handler-returned error to a `tools/call` result carrying the error text with `isError=true`, NOT a JSON-RPC `-32602` error object (it only emits `-32602` for a malformed `tools/call` envelope, never for a handler's returned error; locked in by its committed `server_test.go` "returns error result when tool handler errors"). Everything else is expressed in the payload `status` (`ok`/`failed`/`error`). The cutoff path returns `status: "failed"` with `summary` prefixed `budget cutoff:` and full usage-so-far.

**MUST-STREAM (added 2026-07-09, frozen SSE delta D4 — depends on 08 Task 9b landing first):** `request_review` and `ask_agent` are enumerated MUST-stream tools. The in-sidecar claude CLI subprocess routinely runs multi-minute (>60s is the NORM for `request_review`), which is exactly the F13 failure class: the *calling* claude CLI silently abandons a buffered, progress-free tools/call at exactly 60s ("(completed with no output)"; `MCP_TOOL_TIMEOUT` does not prevent it). The server is therefore built with `mcpserver.NewServerWithHeartbeat(cfg.ProgressInterval)` and the handlers use the upgraded 3-arg `ToolHandler` (`func(ctx context.Context, args json.RawMessage, progress func(string)) (any, error)`). Handlers need NO explicit progress calls — the server's coalescing ticker emits `running request_review` / `running ask_agent` heartbeats regardless — but the adapter MAY feed status lines via `progress`. Clients that don't opt in (no `Accept: text/event-stream` + `_meta.progressToken`) still get the buffered one-shot JSON body, and their handlers receive a no-op `progress`.

**Files:**
- Create: `agent/gatewaymcp/server.go`
- Create: `agent/gatewaymcp/tools.go`
- Test: `agent/gatewaymcp/server_test.go`

**Steps:**

- [ ] Write the failing test `agent/gatewaymcp/server_test.go` (drives the assembled server over HTTP with a fake adapter; asserts schema shape, metering, and cutoff):

```go
package gatewaymcp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/gatewaymcp"
	"github.com/concourse/concourse/agent/gatewaymcp/gatewaymcpfakes"
	"github.com/concourse/concourse/agent/schema"
)

func callTool(t *testing.T, srv http.Handler, name string, args map[string]any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	result := out["result"].(map[string]any)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("tool payload not JSON: %v (%s)", err, text)
	}
	return payload
}

func newTestServer(t *testing.T, fake *gatewaymcpfakes.FakeAdapter, slice float64) *gatewaymcp.Server {
	t.Helper()
	var eventBuf bytes.Buffer
	cfg := gatewaymcp.Config{PrincipalToken: "t", BudgetSliceUSD: slice}
	meter := gatewaymcp.NewMeter(cfg, schema.NewEventWriter(&eventBuf), http.DefaultClient)
	return gatewaymcp.NewServer(cfg, fake, meter)
}

func TestAskAgentHappyPath(t *testing.T) {
	fake := new(gatewaymcpfakes.FakeAdapter)
	fake.NameReturns("claude")
	fake.InvokeReturns(&gatewaymcp.Response{Answer: "42", Usage: gatewaymcp.Usage{Provider: "claude", Model: "m", CostUSD: 0.1, DurationMS: 5}}, nil)
	srv := newTestServer(t, fake, 0)

	p := callTool(t, srv.Mux(), "ask_agent", map[string]any{"prompt": "meaning of life"})
	if p["status"] != "ok" {
		t.Errorf("status = %v", p["status"])
	}
	if p["answer"] != "42" {
		t.Errorf("answer = %v", p["answer"])
	}
	usage := p["usage"].(map[string]any)
	if usage["provider"] != "claude" || usage["cost_usd"].(float64) != 0.1 {
		t.Errorf("usage = %v", usage)
	}
}

func TestRequestReviewParsesFindings(t *testing.T) {
	fake := new(gatewaymcpfakes.FakeAdapter)
	fake.NameReturns("claude")
	fake.InvokeReturns(&gatewaymcp.Response{
		Answer: `[{"title":"nil deref","description":"x may be nil","file":"a.go","line":10,"severity_hint":"major","category":"correctness"}]`,
		Usage:  gatewaymcp.Usage{Provider: "claude", Model: "m", CostUSD: 0.2, DurationMS: 9},
	}, nil)
	srv := newTestServer(t, fake, 0)

	p := callTool(t, srv.Mux(), "request_review", map[string]any{"diff": "@@ -1 +1 @@"})
	if p["status"] != "ok" {
		t.Errorf("status = %v", p["status"])
	}
	findings := p["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("findings = %v", findings)
	}
	f := findings[0].(map[string]any)
	if f["severity"] != "major" || f["category"] != "correctness" || f["message"] != "x may be nil" || f["path"] != "a.go" {
		t.Errorf("finding = %v", f)
	}
}

func TestCutoffRefusesWhenSliceExhausted(t *testing.T) {
	fake := new(gatewaymcpfakes.FakeAdapter)
	fake.NameReturns("claude")
	fake.InvokeReturns(&gatewaymcp.Response{Answer: "x", Usage: gatewaymcp.Usage{Provider: "claude", CostUSD: 0.6, DurationMS: 1}}, nil)
	srv := newTestServer(t, fake, 1.00)

	// first call: 0.6 spent, still under cap → ok
	if p := callTool(t, srv.Mux(), "ask_agent", map[string]any{"prompt": "a"}); p["status"] != "ok" {
		t.Fatalf("call 1 status = %v", p["status"])
	}
	// second call: cumulative would be 1.2 >= cap; the guard admits it (still
	// < cap before the call), so it runs and pushes over → ok
	if p := callTool(t, srv.Mux(), "ask_agent", map[string]any{"prompt": "b"}); p["status"] != "ok" {
		t.Fatalf("call 2 status = %v", p["status"])
	}
	// third call: cumulative 1.2 >= cap → refused with budget cutoff, failed
	p := callTool(t, srv.Mux(), "ask_agent", map[string]any{"prompt": "c"})
	if p["status"] != "failed" {
		t.Fatalf("call 3 status = %v, want failed", p["status"])
	}
	summary := p["summary"].(string)
	if !bytes.HasPrefix([]byte(summary), []byte(gatewaymcp.CutoffPrefix)) {
		t.Errorf("summary = %q, want %q prefix", summary, gatewaymcp.CutoffPrefix)
	}
	if fake.InvokeCallCount() != 2 {
		t.Errorf("adapter invoked %d times, want 2 (3rd refused before adapter)", fake.InvokeCallCount())
	}
}

func TestMalformedInputIsMCPError(t *testing.T) {
	fake := new(gatewaymcpfakes.FakeAdapter)
	fake.NameReturns("claude")
	srv := newTestServer(t, fake, 0)
	// request_review requires "diff"
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "request_review", "arguments": map[string]any{}},
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	result := out["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("expected isError for missing diff: %v", out)
	}
	_ = context.Background()
}

// TestSSEProgressHeartbeatsOnSlowAdapter is the FROZEN SSE contract test (SSE
// delta D7, 2026-07-09): a 40s fake adapter MUST yield >= 2
// notifications/progress frames spaced < 30s apart before the final tools/call
// result frame. With the default 15s heartbeat that is frames at ~15s and
// ~30s, result at ~40s. This pins the F13 fix: without SSE heartbeats the
// calling claude CLI abandons the call at exactly 60s.
func TestSSEProgressHeartbeatsOnSlowAdapter(t *testing.T) {
	if testing.Short() {
		t.Skip("sleeps a real 40s (frozen by the SSE delta); skipped under -short")
	}
	fake := new(gatewaymcpfakes.FakeAdapter)
	fake.NameReturns("claude")
	fake.InvokeStub = func(ctx context.Context, _ gatewaymcp.Request, _ float64) (*gatewaymcp.Response, error) {
		select {
		case <-time.After(40 * time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &gatewaymcp.Response{Answer: "slow answer", Usage: gatewaymcp.Usage{Provider: "claude", Model: "m", CostUSD: 0.1, DurationMS: 40000}}, nil
	}
	srv := newTestServer(t, fake, 0) // cfg.ProgressInterval zero → DefaultHeartbeat 15s
	ts := httptest.NewServer(srv.Mux())
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": "ask_agent", "arguments": map[string]any{"prompt": "slow"},
			"_meta": map[string]any{"progressToken": "tok-1"}, // the SSE opt-in (08 Task 9b gating)
		},
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	var progressAt []time.Time
	var final map[string]any
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &msg); err != nil {
			t.Fatalf("bad SSE frame %q: %v", line, err)
		}
		if msg["method"] == "notifications/progress" {
			params := msg["params"].(map[string]any)
			if params["progressToken"] != "tok-1" {
				t.Errorf("progressToken = %v, want tok-1 echoed verbatim", params["progressToken"])
			}
			progressAt = append(progressAt, time.Now())
			continue
		}
		final = msg // the tools/call JSON-RPC response is the LAST SSE frame
		break
	}

	// Frozen assertion: >= 2 progress frames, each gap (incl. start→first) < 30s.
	if len(progressAt) < 2 {
		t.Fatalf("got %d notifications/progress frames, want >= 2", len(progressAt))
	}
	prev := start
	for i, at := range progressAt {
		if gap := at.Sub(prev); gap >= 30*time.Second {
			t.Errorf("progress frame %d arrived %v after the previous — must be < 30s", i, gap)
		}
		prev = at
	}
	if final == nil {
		t.Fatal("stream ended without the final tools/call result frame")
	}
	text := final["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("final payload not JSON: %v", err)
	}
	if payload["status"] != "ok" || payload["answer"] != "slow answer" {
		t.Errorf("final payload = %v", payload)
	}
}
```

- [ ] Run to verify it fails: `go test ./agent/gatewaymcp/` — expect undefined `NewServer`.
- [ ] Write `agent/gatewaymcp/server.go`:

```go
package gatewaymcp

import (
	"net/http"
	"time"

	"github.com/concourse/concourse/atc/api/mcpserver"
)

// Server assembles the gateway sidecar: the MCP endpoint at POST /mcp and the
// pod readiness probe at GET /healthz (§8.5). Tools dispatch through the
// Adapter; the Meter records every call; the SliceGuard enforces the cutoff.
// request_review/ask_agent are MUST-stream tools (SSE delta D4): the MCP
// endpoint serves SSE progress heartbeats to opted-in clients via the upgraded
// shared mcpserver (08 Task 9b).
type Server struct {
	cfg     Config
	adapter Adapter
	meter   *Meter
	guard   *SliceGuard
	mcp     *mcpserver.Server
	mux     *http.ServeMux
}

// NewServer builds a server. It registers request_review and ask_agent.
func NewServer(cfg Config, adapter Adapter, meter *Meter) *Server {
	s := &Server{
		cfg:     cfg,
		adapter: adapter,
		meter:   meter,
		guard:   NewSliceGuard(cfg.BudgetSliceUSD),
		// SSE progress heartbeats per 08 Task 9b; cfg.ProgressInterval <= 0
		// (e.g. a zero-value test Config) falls back to DefaultHeartbeat (15s).
		mcp: mcpserver.NewServerWithHeartbeat(cfg.ProgressInterval),
		mux: http.NewServeMux(),
	}
	s.registerTools()
	s.mux.Handle("/mcp", s.mcp)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return s
}

func (s *Server) Mux() *http.ServeMux { return s.mux }

// ListenAndServe serves the sidecar with the SSE delta's D4 server-timeout
// rule: WriteTimeout and IdleTimeout MUST be 0 — any nonzero WriteTimeout
// severs a long SSE stream mid-call (a multi-minute request_review would die
// silently). ReadHeaderTimeout alone guards the accept path.
func (s *Server) ListenAndServe() error {
	srv := &http.Server{
		Addr:              s.cfg.ListenAddr,
		Handler:           s.mux,
		WriteTimeout:      0, // D4: never sever long SSE streams
		IdleTimeout:       0, // D4
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}
```

- [ ] Write `agent/gatewaymcp/tools.go` (schemas verbatim §3.3; the review prompt assembly + finding mapping mirror `ci-agent/adapter/adapter.go`'s `rawFinding`; the cutoff path returns `failed` with the `budget cutoff:` summary; handlers are 3-arg `ToolHandler`s per 08 Task 9b — the ticker guarantees `running <tool>` heartbeats even if a handler never calls `progress`):

```go
package gatewaymcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/atc/api/mcpserver"
)

var providerEnum = map[string]bool{"claude": true, "codex": true, "cursor": true}

func (s *Server) registerTools() {
	s.mcp.AddTool("request_review",
		"Request a rubric-structured code review of a unified diff from a subagent (any provider).",
		mcpserver.MustJSON(map[string]any{
			"type": "object", "required": []string{"diff"},
			"properties": map[string]any{
				"diff":     map[string]any{"type": "string", "description": "unified diff to review"},
				"rubric":   map[string]any{"type": "string", "description": "markdown rubric/instructions; default = general correctness review"},
				"context":  map[string]any{"type": "string", "description": "optional extra context (spec excerpt, constraints)"},
				"provider": map[string]any{"type": "string", "enum": []string{"claude", "codex", "cursor"}, "default": "claude"},
				"model":    map[string]any{"type": "string", "description": "provider-specific model id; empty = adapter default"},
			},
			"additionalProperties": false,
		}),
		s.handleRequestReview)

	s.mcp.AddTool("ask_agent",
		"Ask a subagent (any provider) a prompt and return its answer.",
		mcpserver.MustJSON(map[string]any{
			"type": "object", "required": []string{"prompt"},
			"properties": map[string]any{
				"prompt":        map[string]any{"type": "string"},
				"provider":      map[string]any{"type": "string", "enum": []string{"claude", "codex", "cursor"}, "default": "claude"},
				"model":         map[string]any{"type": "string"},
				"output_schema": map[string]any{"type": "string", "description": "optional JSON schema the answer must satisfy"},
			},
			"additionalProperties": false,
		}),
		s.handleAskAgent)
}

// gatewayUsage is the §3.3 GatewayUsage result embed (Usage minus the internal
// Partial flag).
type gatewayUsage struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Turns        int     `json:"turns"`
	CostUSD      float64 `json:"cost_usd"`
	DurationMS   int     `json:"duration_ms"`
}

func toGatewayUsage(u Usage) gatewayUsage {
	return gatewayUsage{
		Provider: u.Provider, Model: u.Model, InputTokens: u.InputTokens,
		OutputTokens: u.OutputTokens, Turns: u.Turns, CostUSD: u.CostUSD, DurationMS: u.DurationMS,
	}
}

type finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Category string `json:"category"`
	Message  string `json:"message"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
}

type reviewResult struct {
	Status   string       `json:"status"`
	Summary  string       `json:"summary"`
	Findings []finding    `json:"findings"`
	Usage    gatewayUsage `json:"usage"`
}

type askResult struct {
	Status string       `json:"status"`
	Answer string       `json:"answer"`
	Usage  gatewayUsage  `json:"usage"`
}

// rawReviewFinding mirrors ci-agent/adapter.rawFinding (verified) — the shape
// the review prompt asks the model to emit.
type rawReviewFinding struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	File         string `json:"file"`
	Line         int    `json:"line"`
	SeverityHint string `json:"severity_hint"`
	Category     string `json:"category"`
}

// handleRequestReview validates then dispatches request_review. A validation
// error is returned plainly; the shared mcpserver surfaces it as a tools/call
// result with isError=true (its handler-error path — never a JSON-RPC -32602
// object, which it reserves for a malformed tools/call envelope).
//
// MUST-stream (SSE delta D4): a review routinely exceeds 60s, the empirical
// claude-CLI buffered-call abandonment cliff. progress is the 3-arg
// ToolHandler's status feed (no-op for buffered clients); the server's ticker
// emits "running request_review" heartbeats regardless, so the explicit
// progress calls below are informational (MAY), not load-bearing.
func (s *Server) handleRequestReview(ctx context.Context, args json.RawMessage, progress func(string)) (any, error) {
	var in struct {
		Diff     string `json:"diff"`
		Rubric   string `json:"rubric"`
		Context  string `json:"context"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err) // → MCP tool error (isError=true), not -32602
	}
	if in.Diff == "" {
		return nil, fmt.Errorf("diff is required") // → MCP tool error (isError=true), not -32602
	}
	if in.Provider == "" {
		in.Provider = "claude"
	}
	if !providerEnum[in.Provider] {
		return nil, fmt.Errorf("unknown provider %q", in.Provider) // → MCP tool error (isError=true), not -32602
	}
	if in.Provider != s.adapter.Name() {
		return reviewResult{Status: "error", Summary: fmt.Sprintf("provider %q not available in this sidecar (only %q)", in.Provider, s.adapter.Name())}, nil
	}

	rubric := in.Rubric
	if rubric == "" {
		rubric = "General correctness review: find bugs, missing tests, and scope creep."
	}
	prompt := buildReviewPrompt(in.Diff, rubric, in.Context)

	// Cutoff admission (Task 6).
	if !s.guard.Admit() {
		rem := s.guard.Remaining()
		s.meter.BudgetStop(rem.LimitUSD, rem.SpentUSD, rem.RemainingUSD)
		return reviewResult{Status: "failed", Summary: fmt.Sprintf("%s slice of $%.2f exhausted ($%.2f spent) before this review", CutoffPrefix, rem.LimitUSD, rem.SpentUSD)}, nil
	}

	progress(fmt.Sprintf("dispatching request_review to the %s adapter", in.Provider))
	callID := s.meter.CallStart("request_review", in.Provider, in.Model, len(prompt))
	resp, err := s.adapter.Invoke(ctx, Request{Tool: "request_review", Prompt: prompt, Model: in.Model, OutputSchema: "findings"}, s.guard.Remaining().RemainingUSD)
	if err != nil {
		s.meter.CallResult(callID, "request_review", "error", Usage{Provider: in.Provider, Model: in.Model}, nil)
		return reviewResult{Status: "error", Summary: "provider/adapter error: " + err.Error()}, nil
	}

	findings := parseFindings(resp.Answer)
	fc := len(findings)
	s.guard.Record(resp.Usage.CostUSD)
	if s.guard.ShouldWarn() {
		rem := s.guard.Remaining()
		s.meter.BudgetWarn(rem.LimitUSD, rem.SpentUSD, rem.RemainingUSD)
		s.guard.MarkWarned()
	}
	s.meter.CallResult(callID, "request_review", "ok", resp.Usage, &fc)
	return reviewResult{Status: "ok", Summary: fmt.Sprintf("%d finding(s)", fc), Findings: findings, Usage: toGatewayUsage(resp.Usage)}, nil
}

// handleAskAgent is a MUST-stream tool like handleRequestReview (SSE delta
// D4); same 3-arg ToolHandler contract and heartbeat guarantees.
func (s *Server) handleAskAgent(ctx context.Context, args json.RawMessage, progress func(string)) (any, error) {
	var in struct {
		Prompt       string `json:"prompt"`
		Provider     string `json:"provider"`
		Model        string `json:"model"`
		OutputSchema string `json:"output_schema"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err) // → MCP tool error (isError=true), not -32602
	}
	if in.Prompt == "" {
		return nil, fmt.Errorf("prompt is required") // → MCP tool error (isError=true), not -32602
	}
	if in.Provider == "" {
		in.Provider = "claude"
	}
	if !providerEnum[in.Provider] {
		return nil, fmt.Errorf("unknown provider %q", in.Provider) // → MCP tool error (isError=true), not -32602
	}
	if in.Provider != s.adapter.Name() {
		return askResult{Status: "error", Answer: "", Usage: gatewayUsage{Provider: in.Provider}}, nil
	}

	if !s.guard.Admit() {
		rem := s.guard.Remaining()
		s.meter.BudgetStop(rem.LimitUSD, rem.SpentUSD, rem.RemainingUSD)
		return askResult{Status: "failed", Answer: fmt.Sprintf("%s slice of $%.2f exhausted ($%.2f spent) before this call", CutoffPrefix, rem.LimitUSD, rem.SpentUSD)}, nil
	}

	progress(fmt.Sprintf("dispatching ask_agent to the %s adapter", in.Provider))
	callID := s.meter.CallStart("ask_agent", in.Provider, in.Model, len(in.Prompt))
	resp, err := s.adapter.Invoke(ctx, Request{Tool: "ask_agent", Prompt: in.Prompt, Model: in.Model, OutputSchema: in.OutputSchema}, s.guard.Remaining().RemainingUSD)
	if err != nil {
		s.meter.CallResult(callID, "ask_agent", "error", Usage{Provider: in.Provider, Model: in.Model}, nil)
		return askResult{Status: "error", Answer: "", Usage: gatewayUsage{Provider: in.Provider, Model: in.Model}}, nil
	}
	s.guard.Record(resp.Usage.CostUSD)
	if s.guard.ShouldWarn() {
		rem := s.guard.Remaining()
		s.meter.BudgetWarn(rem.LimitUSD, rem.SpentUSD, rem.RemainingUSD)
		s.guard.MarkWarned()
	}
	s.meter.CallResult(callID, "ask_agent", "ok", resp.Usage, nil)
	return askResult{Status: "ok", Answer: resp.Answer, Usage: toGatewayUsage(resp.Usage)}, nil
}

func buildReviewPrompt(diff, rubric, extraContext string) string {
	p := "You are a code reviewer. Review the following unified diff against the rubric.\n\n" +
		"## Rubric\n" + rubric + "\n\n"
	if extraContext != "" {
		p += "## Context\n" + extraContext + "\n\n"
	}
	p += "## Diff\n```diff\n" + diff + "\n```\n\n" +
		"Respond with ONLY a JSON array of findings, each: " +
		`{"title","description","file","line","severity_hint":"critical|major|minor|info","category"}. ` +
		"Return [] if there are no findings."
	return p
}

// parseFindings maps the model's raw review JSON to §3.3 findings. Unparseable
// output yields no findings (the summary still reports the review ran).
func parseFindings(raw string) []finding {
	var raws []rawReviewFinding
	if err := json.Unmarshal([]byte(raw), &raws); err != nil {
		return []finding{}
	}
	out := make([]finding, 0, len(raws))
	for i, r := range raws {
		sev := r.SeverityHint
		if sev != "critical" && sev != "major" && sev != "minor" && sev != "info" {
			sev = "info"
		}
		cat := r.Category
		if cat == "" {
			cat = "general"
		}
		msg := r.Description
		if msg == "" {
			msg = r.Title
		}
		out = append(out, finding{
			ID: fmt.Sprintf("f%d", i+1), Severity: sev, Category: cat,
			Message: msg, Path: r.File, Line: r.Line,
		})
	}
	return out
}
```

- [ ] Run to verify pass: `go test ./agent/gatewaymcp/` — expect PASS. (The frozen SSE heartbeat test sleeps a real 40s — that duration is pinned by the SSE delta D7 and must not be shortened; use `go test -short` only for inner-loop iteration. Verification sweeps run it in full.)
- [ ] Commit:

```bash
git add agent/gatewaymcp/server.go agent/gatewaymcp/tools.go agent/gatewaymcp/server_test.go
git commit -m "feat(gateway-mcp): MCP server with request_review + ask_agent tools and cutoff" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: `cmd/gateway-mcp` binary (serve mode)

The container entrypoint. Reads env, assembles the server with the claude adapter and an event log at `GATEWAY_MCP_EVENTS_PATH` (or stdout), and serves `/mcp` + `/healthz`. Modeled on `cmd/platform-mcp/main.go`. Per the 2026-07-09 SSE delta (D3/D4): the binary validates `GATEWAY_MCP_PROGRESS_INTERVAL` at startup (via Task 2's `ConfigFromEnv` — set-but-invalid, <= 0, or > 30s is a fatal exit, never a silent clamp) and serves through `Server.ListenAndServe`, which applies the D4 server-timeout rule (`WriteTimeout: 0`, `IdleTimeout: 0`, `ReadHeaderTimeout: 5s`) so long SSE streams are never severed.

**Files:**
- Create: `cmd/gateway-mcp/main.go`
- Test: `cmd/gateway-mcp/main_test.go`

**Steps:**

- [ ] Write the failing test `cmd/gateway-mcp/main_test.go` (spawns the built binary with a fake claude CLI and probes `/healthz` + `tools/list` — an end-to-end smoke of the exact container entrypoint):

```go
package main_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestServeModeSmoke(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "gateway-mcp")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}

	// a fake claude on PATH is not needed to serve tools/list.
	port := freePort(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"AGENT_PRINCIPAL_TOKEN=cap1.9.secret",
		"CLAUDE_CODE_OAUTH_TOKEN=tok",
		fmt.Sprintf("MCP_LISTEN_ADDR=127.0.0.1:%d", port),
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	var healthy bool
	for i := 0; i < 50; i++ {
		resp, err := http.Get(base + "/healthz")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			healthy = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !healthy {
		t.Fatal("healthz never came up")
	}

	resp, err := http.Post(base+"/mcp", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "request_review") || !strings.Contains(string(raw), "ask_agent") {
		t.Fatalf("tools/list missing gateway tools: %s", raw)
	}
}

func TestServeModeFailsFastOnBadEnv(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "gateway-mcp")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}
	cmd := exec.Command(bin)
	cmd.Env = []string{} // no AGENT_PRINCIPAL_TOKEN
	if err := cmd.Run(); err == nil {
		t.Fatal("expected non-zero exit without required env")
	}
}

// SSE delta D3: interval validation is a FATAL startup error, never a clamp.
func TestServeModeFailsFastOnBadProgressInterval(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "gateway-mcp")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}
	for _, bad := range []string{"45s", "0", "banana"} {
		cmd := exec.Command(bin)
		cmd.Env = append(os.Environ(),
			"AGENT_PRINCIPAL_TOKEN=cap1.9.secret",
			"CLAUDE_CODE_OAUTH_TOKEN=tok",
			"GATEWAY_MCP_PROGRESS_INTERVAL="+bad,
		)
		if err := cmd.Run(); err == nil {
			t.Errorf("GATEWAY_MCP_PROGRESS_INTERVAL=%q: expected non-zero exit", bad)
		}
	}
}
```

- [ ] Run to verify it fails: `go test ./cmd/gateway-mcp/` — expect `no Go files`.
- [ ] Write `cmd/gateway-mcp/main.go`:

```go
// gateway-mcp is the agent-gateway MCP sidecar (shared contracts §3.3):
// provider-agnostic subagent access (request_review, ask_agent) with universal
// metering and a budget-slice cutoff. It serves streamable-HTTP MCP on
// MCP_LISTEN_ADDR (default :7782) with GET /healthz for the readiness probe.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/concourse/concourse/agent/gatewaymcp"
	"github.com/concourse/concourse/agent/schema"
)

func main() {
	cfg, err := gatewaymcp.ConfigFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gateway-mcp: %s\n", err)
		os.Exit(2)
	}

	var eventSink io.Writer = os.Stdout
	if cfg.EventsPath != "" {
		f, err := os.OpenFile(cfg.EventsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gateway-mcp: cannot open GATEWAY_MCP_EVENTS_PATH: %s\n", err)
			os.Exit(2)
		}
		defer f.Close()
		eventSink = f
	}

	adapter := gatewaymcp.NewClaudeAdapter(cfg.ClaudeCLI, cfg.OAuthToken)
	meter := gatewaymcp.NewMeter(cfg, schema.NewEventWriter(eventSink), nil)
	srv := gatewaymcp.NewServer(cfg, adapter, meter)

	fmt.Fprintf(os.Stderr, "gateway-mcp: serving MCP on %s (provider %s, slice $%.2f, sse heartbeat %s)\n", cfg.ListenAddr, adapter.Name(), cfg.BudgetSliceUSD, cfg.ProgressInterval)
	// srv.ListenAndServe applies the SSE delta's D4 server-timeout rule
	// (WriteTimeout: 0, IdleTimeout: 0, ReadHeaderTimeout: 5s) — a nonzero
	// WriteTimeout would sever long SSE streams mid-call. Interval validation
	// (fatal on invalid/<=0/>30s) already happened in ConfigFromEnv above.
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "gateway-mcp: %s\n", err)
		os.Exit(1)
	}
}
```

- [ ] Run to verify pass: `go test ./cmd/gateway-mcp/` — expect PASS.
- [ ] Commit:

```bash
git add cmd/gateway-mcp/
git commit -m "feat(gateway-mcp): sidecar binary serve mode (/mcp + /healthz)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: Contract-test kit (`agent/gatewaymcp/contracttest`)

The spec's testing approach requires contract tests for all three MCP surfaces; dev-mcp's `agent/devmcp/contracttest` and platform-mcp's `agent/platformmcp/contracttest` set the style: `contracttest.Run(t, endpointURL)` validates any serving endpoint — in-process here, `docker run` in the CI image job (Task 10). The kit points the sidecar at a stub claude CLI (a bundled fixture script) via `GATEWAY_CLAUDE_CLI` so it never needs a real provider or ATC. Per the 2026-07-09 SSE delta, the kit also checks progress emission the same way platform-mcp's kit does: a tools/call sent with the SSE opt-in (`Accept: text/event-stream` + `params._meta.progressToken`) must come back as an SSE stream whose final frame carries the tools/call result (transport shape; heartbeat *timing* against a slow adapter is pinned by Task 7's frozen 40s test).

**Files:**
- Create: `agent/gatewaymcp/contracttest/contracttest.go`
- Create: `agent/gatewaymcp/contracttest/fakecli.go`
- Test: `agent/gatewaymcp/contracttest/contracttest_test.go`

**Steps:**

- [ ] Write the failing self-test `agent/gatewaymcp/contracttest/contracttest_test.go` (runs the kit against an in-process real server + fake adapter — proving both kit and server):

```go
package contracttest_test

import (
	"net/http/httptest"
	"testing"

	"github.com/concourse/concourse/agent/gatewaymcp"
	"github.com/concourse/concourse/agent/gatewaymcp/contracttest"
	"github.com/concourse/concourse/agent/schema"
	"bytes"
	"net/http"
)

func TestKitAgainstRealServer(t *testing.T) {
	cfg := gatewaymcp.Config{PrincipalToken: "t"}
	adapter := contracttest.NewFakeAdapter()
	var buf bytes.Buffer
	meter := gatewaymcp.NewMeter(cfg, schema.NewEventWriter(&buf), http.DefaultClient)
	srv := gatewaymcp.NewServer(cfg, adapter, meter)
	ts := httptest.NewServer(srv.Mux())
	defer ts.Close()

	contracttest.Run(t, ts.URL)
}
```

- [ ] Run to verify it fails: `go test ./agent/gatewaymcp/contracttest/` — expect undefined package.
- [ ] Write `agent/gatewaymcp/contracttest/fakecli.go` — a deterministic in-process adapter the kit drives (no real provider):

```go
// Package contracttest validates any agent-gateway-mcp endpoint against the
// §3.3 contract. Point it at a serving endpoint (in-process httptest here,
// docker run in the CI image job).
package contracttest

import (
	"context"

	"github.com/concourse/concourse/agent/gatewaymcp"
)

// FakeAdapter returns deterministic responses so the kit can assert schema
// shape and taxonomy without a real provider CLI.
type FakeAdapter struct{}

func NewFakeAdapter() *FakeAdapter { return &FakeAdapter{} }

func (FakeAdapter) Name() string { return "claude" }

func (FakeAdapter) Invoke(_ context.Context, req gatewaymcp.Request, _ float64) (*gatewaymcp.Response, error) {
	u := gatewaymcp.Usage{Provider: "claude", Model: "fake-model", InputTokens: 10, OutputTokens: 5, Turns: 1, CostUSD: 0.01, DurationMS: 3}
	if req.Tool == "request_review" {
		return &gatewaymcp.Response{
			Answer: `[{"title":"t","description":"d","file":"f.go","line":1,"severity_hint":"minor","category":"style"}]`,
			Usage:  u,
		}, nil
	}
	return &gatewaymcp.Response{Answer: "fake answer", Usage: u}, nil
}
```

- [ ] Write `agent/gatewaymcp/contracttest/contracttest.go`:

```go
package contracttest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Run validates a serving gateway endpoint (base URL WITHOUT /mcp) against
// the §3.3 contract: healthz, tools/list advertises both tools, request_review
// returns the findings+usage shape with an ok status, ask_agent returns
// answer+usage, malformed input is an MCP-level error, and the SSE opt-in path
// (SSE delta, 08 Task 9b) returns the tools/call result as the final SSE frame.
func Run(t *testing.T, endpointURL string) {
	t.Helper()

	rpc := func(method string, params any) map[string]any {
		t.Helper()
		pj := []byte("{}")
		if params != nil {
			pj, _ = json.Marshal(params)
		}
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, pj)
		resp, err := http.Post(endpointURL+"/mcp", "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("%s decode: %v", method, err)
		}
		return out
	}

	callTool := func(name string, args any) (map[string]any, bool) {
		t.Helper()
		out := rpc("tools/call", map[string]any{"name": name, "arguments": args})
		result, _ := out["result"].(map[string]any)
		if result == nil {
			t.Fatalf("tools/call %s: no result: %v", name, out)
		}
		content, _ := result["content"].([]any)
		if len(content) == 0 {
			t.Fatalf("tools/call %s: empty content", name)
		}
		text := content[0].(map[string]any)["text"].(string)
		isErr, _ := result["isError"].(bool)
		var payload map[string]any
		if !isErr {
			if err := json.Unmarshal([]byte(text), &payload); err != nil {
				t.Fatalf("tools/call %s: payload not JSON: %v (%s)", name, err, text)
			}
		}
		return payload, isErr
	}

	// healthz (§8.5).
	resp, err := http.Get(endpointURL + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: err=%v status=%v", err, resp)
	}
	resp.Body.Close()

	// tools/list advertises both tools.
	list := rpc("tools/list", nil)
	raw, _ := json.Marshal(list)
	for _, name := range []string{"request_review", "ask_agent"} {
		if !bytes.Contains(raw, []byte(name)) {
			t.Errorf("tools/list missing %q: %s", name, raw)
		}
	}

	// request_review returns ok + findings[] + usage with required keys.
	p, isErr := callTool("request_review", map[string]any{"diff": "@@ -1 +1 @@"})
	if isErr {
		t.Fatal("request_review reported MCP error on valid input")
	}
	if p["status"] != "ok" {
		t.Errorf("request_review status = %v", p["status"])
	}
	if _, ok := p["findings"].([]any); !ok {
		t.Errorf("request_review missing findings array: %v", p)
	}
	assertUsage(t, p["usage"])

	// ask_agent returns ok + answer + usage.
	p, isErr = callTool("ask_agent", map[string]any{"prompt": "hello"})
	if isErr {
		t.Fatal("ask_agent reported MCP error on valid input")
	}
	if p["status"] != "ok" || p["answer"] == "" {
		t.Errorf("ask_agent result = %v", p)
	}
	assertUsage(t, p["usage"])

	// malformed input (missing required diff) is an MCP-level error.
	if _, isErr := callTool("request_review", map[string]any{}); !isErr {
		t.Error("request_review without diff must be an MCP-level error")
	}

	// SSE opt-in (SSE delta 2026-07-09 / 08 Task 9b, mirroring platform-mcp's
	// kit): Accept: text/event-stream + params._meta.progressToken must switch
	// the response to an SSE stream whose LAST frame is the tools/call result.
	// The fake adapter is fast, so zero progress frames is acceptable here —
	// heartbeat timing is pinned by the gateway's 40s slow-adapter test.
	sseBody := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"ask_agent","arguments":{"prompt":"sse ping"},"_meta":{"progressToken":"kit-1"}}}`
	sseReq, err := http.NewRequest(http.MethodPost, endpointURL+"/mcp", bytes.NewReader([]byte(sseBody)))
	if err != nil {
		t.Fatalf("sse request: %v", err)
	}
	sseReq.Header.Set("Content-Type", "application/json")
	sseReq.Header.Set("Accept", "text/event-stream")
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("sse tools/call: %v", err)
	}
	defer sseResp.Body.Close()
	if ct := sseResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("sse opt-in: Content-Type = %q, want text/event-stream", ct)
	}
	var lastData string
	scanner := bufio.NewScanner(sseResp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
			lastData = strings.TrimPrefix(line, "data: ")
		}
	}
	var finalMsg map[string]any
	if err := json.Unmarshal([]byte(lastData), &finalMsg); err != nil {
		t.Fatalf("sse final frame not JSON: %v (%q)", err, lastData)
	}
	if finalMsg["result"] == nil {
		t.Fatalf("sse final frame is not the tools/call result: %v", finalMsg)
	}
}

func assertUsage(t *testing.T, v any) {
	t.Helper()
	u, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("usage not an object: %v", v)
	}
	for _, k := range []string{"provider", "model", "cost_usd", "duration_ms"} {
		if _, ok := u[k]; !ok {
			t.Errorf("usage missing required key %q: %v", k, u)
		}
	}
}
```

- [ ] Run to verify pass: `go test ./agent/gatewaymcp/contracttest/` — expect PASS.
- [ ] Commit:

```bash
git add agent/gatewaymcp/contracttest/
git commit -m "feat(gateway-mcp): contract-test kit for the request_review/ask_agent surface" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 10: Sidecar image packaging (§8.5) — Dockerfile + CI job

Instantiates the dev-mcp packaging convention for the gateway. The image bundles the `claude` CLI (pinned) plus the static gateway binary. The CI job copies dev-mcp's `build-mcp-dev-image` template with gateway substitutions and runs the Task 9 kit against the built image; it merges additively with platform-mcp's `build-mcp-platform` job (Task 1 addendum). *(2026-07-09 SSE delta D8: NO image-content change for SSE — the image picks up the upgraded `atc/api/mcpserver` on its normal binary rebuild; ports 7782, `/mcp`, `/healthz`, and the bundled adapter CLI are all unchanged. The claude CLI needed by 08 Task 18b's park pin test lives on the test host, not in this image.)*

**Files:**
- Create: `deploy/Dockerfile.mcp-gateway`
- Modify: `deploy/concourse-pipeline.yml` (append `build-mcp-gateway` job at end of file; the dev-mcp `build-mcp-dev-image` job is the copyable template)
- Modify: `deploy/MCP_IMAGES.md` (append the gateway row to the convention table — the doc dev-mcp created)

**Steps:**

- [ ] Write `deploy/Dockerfile.mcp-gateway` (per §8.5: static Go binary, serves on `MCP_LISTEN_ADDR`, `GET /healthz`, non-root; bundles the pinned claude CLI; `agent/schema` is resolved via the root `go.mod` replace):

```dockerfile
# ghcr.io/tdmtrader/mcp-gateway — the agent-gateway-mcp sidecar image.
#
# Packaging convention: 00-shared-contracts.md §8.5 (owned by dev-mcp, reused
# here). Bundles provider CLIs (v1: claude) so subagent calls run in-sidecar.
FROM golang:1.25-bookworm AS builder
WORKDIR /src
COPY . .
# agent/schema is a nested module resolved via the root go.mod replace.
RUN CGO_ENABLED=0 go build -o /gateway-mcp ./cmd/gateway-mcp

FROM node:20-bookworm-slim
# Pinned claude CLI — the v1 in-sidecar provider (charter risk 1: pin versions,
# automate rebuilds via the packaging pipeline). Bump this tag deliberately.
RUN npm install -g @anthropic-ai/claude-code@1.0.44 && \
    rm -rf /root/.npm

RUN useradd --uid 1000 --create-home gateway
COPY --from=builder /gateway-mcp /usr/local/bin/gateway-mcp

USER gateway
ENV MCP_LISTEN_ADDR=:7782
EXPOSE 7782

ENTRYPOINT ["/usr/local/bin/gateway-mcp"]
```

- [ ] Append the gateway row to the convention table in `deploy/MCP_IMAGES.md` (under the existing `Registry/name` row context — additive, do not rewrite the table):

```markdown

## Gateway image note (mcp-gateway)

- `ghcr.io/tdmtrader/mcp-gateway` bundles the pinned `claude` CLI (v1 sole
  provider). Non-claude adapters (codex/cursor) land behind the same `Adapter`
  interface later and may meter turns/latency only (labeled partial) until
  their CLIs expose cost. The CI job `build-mcp-gateway` runs
  `go test ./agent/gatewaymcp/contracttest/` against the built image with a
  stub `claude` on PATH (`GATEWAY_CLAUDE_CLI`), so it needs no real provider
  credential.
```

- [ ] Append the CI job to `deploy/concourse-pipeline.yml` (copy the `build-mcp-dev-image` job verbatim; substitutions: job name `build-mcp-gateway`, dockerfile `deploy/Dockerfile.mcp-gateway`, image `ghcr.io/tdmtrader/mcp-gateway`, no repo-workspace mount needed, and the contract-test invocation runs the gateway kit against the running container with a stub claude CLI). The job:

```yaml
- name: build-mcp-gateway
  plan:
  - get: repo
    trigger: false
    params: {depth: 1}
  - task: build-test-push
    privileged: true
    config:
      platform: linux
      rootfs_uri: docker:///registry.home/concourse-test-runner:v5
      inputs:
      - name: repo
      params:
        GITHUB_TOKEN: ((github-token))
      run:
        path: sh
        args:
        - -exc
        - |
          cd repo
          SHORT_SHA=$(git rev-parse --short HEAD)
          TAG=$(git describe --tags --exact-match 2>/dev/null || echo "${SHORT_SHA}")
          cd ..
          IMAGE="ghcr.io/tdmtrader/mcp-gateway"

          BUILDER_POD="mcp-gateway-build-$$"
          cleanup_builder() { kubectl delete pod -n cicd ${BUILDER_POD} --grace-period=0 --force 2>/dev/null || true; }
          trap cleanup_builder EXIT
          kubectl delete pod -n cicd -l app=mcp-gateway-build --grace-period=0 --force 2>/dev/null || true

          echo "=== Creating DinD pod ==="
          cat <<PODEOF | kubectl apply -n cicd -f -
          apiVersion: v1
          kind: Pod
          metadata:
            name: ${BUILDER_POD}
            namespace: cicd
            labels:
              app: mcp-gateway-build
          spec:
            containers:
            - name: dind
              image: docker:dind
              securityContext:
                privileged: true
              env:
              - name: DOCKER_TLS_CERTDIR
                value: ""
              command: ["dockerd", "--host=unix:///var/run/docker.sock"]
          PODEOF
          kubectl wait --for=condition=ready pod/${BUILDER_POD} -n cicd --timeout=120s
          for i in $(seq 1 30); do
            if kubectl exec -n cicd ${BUILDER_POD} -- docker info >/dev/null 2>&1; then break; fi
            sleep 2
          done

          echo "=== Copying repo into builder pod ==="
          kubectl cp repo cicd/${BUILDER_POD}:/build

          echo "=== Building ${IMAGE}:${TAG} ==="
          kubectl exec -n cicd ${BUILDER_POD} -- \
            docker build -f /build/deploy/Dockerfile.mcp-gateway \
              -t ${IMAGE}:${TAG} /build

          echo "=== Starting the built image (stub claude on PATH) ==="
          kubectl exec -n cicd ${BUILDER_POD} -- sh -c '
            printf "#!/bin/sh\ncat <<EOF\n{\"type\":\"result\",\"result\":\"ok\",\"model\":\"stub\",\"cost_usd\":0.01,\"num_turns\":1,\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\nEOF\n" > /tmp/claude && chmod +x /tmp/claude'
          kubectl exec -n cicd ${BUILDER_POD} -- \
            docker run -d --name gwmcp \
              -e AGENT_PRINCIPAL_TOKEN=cap1.0.contract \
              -e GATEWAY_CLAUDE_CLI=/tmp/claude \
              -v /tmp/claude:/tmp/claude \
              ${IMAGE}:${TAG}
          kubectl exec -n cicd ${BUILDER_POD} -- sh -c '
            for i in $(seq 1 30); do
              if docker run --rm --network container:gwmcp curlimages/curl:8.7.1 \
                   -fsS http://127.0.0.1:7782/healthz >/dev/null 2>&1; then exit 0; fi
              sleep 2
            done
            echo "gateway container never became healthy"; docker logs gwmcp; exit 1'

          echo "=== Running the contract-test kit against the image ==="
          kubectl exec -n cicd ${BUILDER_POD} -- \
            docker run --rm --network container:gwmcp \
              -v /build:/build -w /build \
              -e GATEWAY_MCP_ENDPOINT=http://127.0.0.1:7782 \
              golang:1.25-bookworm \
              go test ./agent/gatewaymcp/e2e/ -run TestLiveImageContract -v -count=1 -timeout 15m

          echo "=== Pushing on green ==="
          echo "${GITHUB_TOKEN}" | kubectl exec -i -n cicd ${BUILDER_POD} -- docker login ghcr.io -u tdmtrader --password-stdin
          kubectl exec -n cicd ${BUILDER_POD} -- docker push ${IMAGE}:${TAG}
          echo "=== Published ${IMAGE}:${TAG} ==="
```

- [ ] Write the tiny e2e wrapper `agent/gatewaymcp/e2e/live_image_contract_test.go` the CI job invokes (skips locally when `GATEWAY_MCP_ENDPOINT` is unset, so `make test-quick` never blocks on Docker):

```go
package e2e_test

import (
	"os"
	"testing"

	"github.com/concourse/concourse/agent/gatewaymcp/contracttest"
)

// TestLiveImageContract runs the §3.3 contract kit against a gateway endpoint
// named by GATEWAY_MCP_ENDPOINT (set by the build-mcp-gateway CI job against
// the running container). Skipped when unset (local runs / no Docker).
func TestLiveImageContract(t *testing.T) {
	endpoint := os.Getenv("GATEWAY_MCP_ENDPOINT")
	if endpoint == "" {
		t.Skip("GATEWAY_MCP_ENDPOINT unset — runs only in the image CI job")
	}
	contracttest.Run(t, endpoint)
}
```

- [ ] Verify the Dockerfile references only files that exist and the binary builds: `ls cmd/gateway-mcp/main.go deploy/Dockerfile.mcp-gateway && go build ./cmd/gateway-mcp && rm -f gateway-mcp` — all present, build succeeds. (An actual `docker build` is not possible on this machine — Colima is usually down; the image is built/verified/pushed exclusively by the theborg CI job.)
- [ ] Validate the pipeline config: `go build ./fly && ./fly validate-pipeline -c deploy/concourse-pipeline.yml && rm -f fly` — expect `looks good` (`validate-pipeline` works offline).
- [ ] Deploy and run once on theborg (requires the live `cicd` target; see memory `reference_theborg_cicd_live_concourse.md` for login):

```bash
fly -t theborg set-pipeline -p concourse -c deploy/concourse-pipeline.yml -n
fly -t theborg trigger-job -j concourse/build-mcp-gateway -w
```

Expect the job to build, pass `TestLiveImageContract`, and push `ghcr.io/tdmtrader/mcp-gateway:<short-sha>`. If the kit fails inside the job, the image or config is genuinely broken — fix and re-trigger; never push manually.

- [ ] Commit:

```bash
git add deploy/Dockerfile.mcp-gateway deploy/MCP_IMAGES.md deploy/concourse-pipeline.yml agent/gatewaymcp/e2e/
git commit -m "ci(gateway-mcp): mcp-gateway Dockerfile + build-mcp-gateway job (packaging convention)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 11: Live theborg wiring + cutoff proof

The end-to-end proof the charter's ships_value demands: an agent (or a plain test harness) calls `request_review` through the gateway and gets a rubric-structured review back; the call's tokens/cost/latency land in the flight recorder and ledger; and a deliberately tiny budget slice demonstrably halts the call with a clean, non-silent cutoff. The unit tests (Tasks 5–7) already prove metering and cutoff against fakes; this task proves the wiring in the real jetbridge pod model — the gateway sidecar reachable at `127.0.0.1:7782` from the main container, with a stub claude CLI so no real spend occurs.

**Files:**
- Create: `atc/worker/jetbridge/live_gateway_mcp_test.go`

**Steps:**

- [ ] Write `live_gateway_mcp_test.go` modeled on `live_sidecar_test.go` (memory: SC-11 sidecar log-stream genuinely needs a live cluster; fake clientset can't exercise localhost sidecar transport). Gate it `//go:build live`. It builds the gateway image path via the same `setupLiveWorker(t, handle)` + `FindOrCreateContainer` pattern the agent-step exec uses, mounts the gateway as a sidecar with a stub claude CLI baked into the container command, sets `AGENT_BUDGET_SLICE_USD` tiny, and asserts (a) `/healthz` is reachable from the main container, (b) `request_review` returns `status: ok` with `findings` and `usage`, (c) a second call after the slice is spent returns `status: failed` with the `budget cutoff:` summary:

```go
//go:build live

package jetbridge_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runtime"
)

// TestLiveGatewaySidecarWiringAndCutoff proves the gateway sidecar is
// reachable over localhost:7782 from the main container in the jetbridge pod
// model, meters a request_review, and cuts off once AGENT_BUDGET_SLICE_USD is
// spent — with a stub claude CLI so no real provider spend occurs.
func TestLiveGatewaySidecarWiringAndCutoff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	handle := "gateway-wiring-" + randSuffix()
	worker := setupLiveWorker(t, handle) // live_test.go helper

	// The gateway sidecar image is ghcr.io/tdmtrader/mcp-gateway:<tag> built by
	// Task 10's CI job; for the live wiring test we point at the pushed tag and
	// override the provider CLI with a stub baked into /tmp/claude via the
	// sidecar command, and set a tiny slice.
	stubCLI := `printf '#!/bin/sh\ncat <<E\n{"type":"result","result":"[{\"title\":\"t\",\"description\":\"d\",\"file\":\"f.go\",\"line\":1,\"severity_hint\":\"minor\",\"category\":\"style\"}]","model":"stub","cost_usd":0.60,"num_turns":1,"usage":{"input_tokens":1,"output_tokens":1}}\nE\n' > /tmp/claude && chmod +x /tmp/claude && exec gateway-mcp`

	spec := runtime.ContainerSpec{
		Env: []string{
			"AGENT_PRINCIPAL_TOKEN=cap1.0.livetest",
			"AGENT_BUDGET_SLICE_USD=1.00",
			"GATEWAY_CLAUDE_CLI=/tmp/claude",
			"MCP_LISTEN_ADDR=:7782",
		},
		Sidecars: []atc.SidecarConfig{{
			Name:    "gateway",
			Image:   gatewayImageRef(t), // reads GATEWAY_MCP_IMAGE env (pushed tag), t.Skip if unset
			Command: []string{"sh", "-c", stubCLI},
		}},
	}

	container, err := worker.FindOrCreateContainer(ctx, handle, spec)
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	t.Cleanup(func() { _ = worker.DestroyContainer(context.Background(), handle) })

	mcp := func(t *testing.T, method, params string) map[string]any {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
		// runInMain runs a curl inside the main container against the sidecar's
		// localhost port (the wiring under test); helper in live_sidecar_test.go.
		out := runInMain(t, container, "curl", "-fsS", "-XPOST",
			"-H", "Content-Type: application/json", "-d", body,
			"http://127.0.0.1:7782/mcp")
		var resp map[string]any
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("%s: bad json %q: %v", method, out, err)
		}
		return resp
	}

	toolPayload := func(t *testing.T, resp map[string]any) map[string]any {
		t.Helper()
		result := resp["result"].(map[string]any)
		text := result["content"].([]any)[0].(map[string]any)["text"].(string)
		var p map[string]any
		if err := json.Unmarshal([]byte(text), &p); err != nil {
			t.Fatalf("payload not json: %v", err)
		}
		return p
	}

	// healthz reachable from the main container (the wiring proof).
	if out := runInMain(t, container, "curl", "-fsS", "http://127.0.0.1:7782/healthz"); !strings.Contains(out, "") {
		t.Fatalf("healthz unreachable: %q", out)
	}

	// request_review returns ok + findings + usage; the stub spends 0.60.
	p := toolPayload(t, mcp(t, "tools/call", `{"name":"request_review","arguments":{"diff":"@@ -1 +1 @@"}}`))
	if p["status"] != "ok" {
		t.Fatalf("call 1 status = %v", p["status"])
	}
	if _, ok := p["findings"].([]any); !ok {
		t.Fatalf("call 1 missing findings: %v", p)
	}

	// second call pushes cumulative to 1.20 (> 1.00) → still runs (admitted < cap).
	p = toolPayload(t, mcp(t, "tools/call", `{"name":"request_review","arguments":{"diff":"@@ -2 +2 @@"}}`))
	if p["status"] != "ok" {
		t.Fatalf("call 2 status = %v", p["status"])
	}

	// third call is refused: cumulative 1.20 >= 1.00 → budget cutoff, failed.
	p = toolPayload(t, mcp(t, "tools/call", `{"name":"request_review","arguments":{"diff":"@@ -3 +3 @@"}}`))
	if p["status"] != "failed" {
		t.Fatalf("call 3 status = %v, want failed", p["status"])
	}
	if !strings.HasPrefix(p["summary"].(string), "budget cutoff:") {
		t.Fatalf("call 3 summary = %q, want budget cutoff prefix", p["summary"])
	}
}
```

  (Where `randSuffix`, `setupLiveWorker`, `runInMain`, and `gatewayImageRef` follow the existing `live_test.go`/`live_sidecar_test.go` helpers; `gatewayImageRef` reads `GATEWAY_MCP_IMAGE` and calls `t.Skip` when unset so the suite is a no-op without a pushed image. Match the actual helper names present after agent-step's wave-2 live tests landed — the memory note names `kubeClient(t)` and the `setupLiveWorker` pattern; adjust names to what exists.)

- [ ] Run against theborg (requires a pushed `ghcr.io/tdmtrader/mcp-gateway` tag from Task 10):

```bash
KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=<throwaway-ns> \
  GATEWAY_MCP_IMAGE=ghcr.io/tdmtrader/mcp-gateway:<short-sha> \
  go test -tags live -run '^TestLiveGatewaySidecarWiringAndCutoff$' -v -count=1 -timeout 5m ./atc/worker/jetbridge/
```

Expect: healthz reachable from the main container, calls 1–2 `ok`, call 3 `failed` with `budget cutoff:`. (Create a THROWAWAY namespace — never `cicd`/`concourse`; no pod-security label so the sidecar pod is allowed; `t.Cleanup` destroys the container.)

- [ ] Commit:

```bash
git add atc/worker/jetbridge/live_gateway_mcp_test.go
git commit -m "test(gateway-mcp): live theborg sidecar wiring + cutoff proof" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 12: Full verification sweep + workstream close-out

**Files:** none (verification only).

**Steps:**

- [ ] Run the full non-live suite (fastest-first):

```bash
go test ./agent/gatewaymcp/... ./cmd/gateway-mcp/    # plain Go, no DB, no Docker
go build ./cmd/gateway-mcp && rm -f gateway-mcp       # binary builds
go vet ./agent/gatewaymcp/... ./cmd/gateway-mcp/
```

Expect all PASS. (No `atc/db` migration or Ginkgo suite is touched — gateway-mcp owns no table and wires nothing into ATC beyond consuming the existing `/api/v1/agent/costs` route over the wire. Do NOT pass `-short`: the sweep must run Task 7's frozen 40s SSE heartbeat test in full.)

- [ ] Run the module-wide build to confirm nothing else broke: `go build ./...` — expect success.
- [ ] Confirm the pipeline config still validates: `go build ./fly && ./fly validate-pipeline -c deploy/concourse-pipeline.yml && rm -f fly` — expect `looks good`.
- [ ] Scope-in coverage self-check (all five charter items map to shipped tasks): sidecar + tool schemas + contract tests → Tasks 2,7,9; adapter layer + claude adapter → Tasks 3,4; universal metering (events + ledger) → Task 5; budget-slice cutoff (never silent) → Tasks 6,7; principal auth + run credential for provider calls → Tasks 2,4,5 (`AGENT_PRINCIPAL_TOKEN` on ledger POST; `CLAUDE_CODE_OAUTH_TOKEN` into the claude subprocess); packaging → Task 10; live proof → Task 11.
- [ ] Commit any final tidy-ups (none expected):

```bash
git status   # expect clean
```

---

## Execution notes

**Full workstream test suite** (fastest-first; no PostgreSQL and no Docker required for the non-live tiers):

```bash
go test ./agent/gatewaymcp/... ./cmd/gateway-mcp/   # unit + contract kit (plain Go)
go build ./cmd/gateway-mcp && rm -f gateway-mcp      # entrypoint builds
go vet ./agent/gatewaymcp/... ./cmd/gateway-mcp/
go build ./fly && ./fly validate-pipeline -c deploy/concourse-pipeline.yml && rm -f fly
make test-quick                                       # final gate (unit + ci-agent; unaffected but proves no regressions)
```

This workstream adds no `atc/db` migration and no ATC route, so `ginkgo ./atc/...` is not required for gateway changes; run `go build ./...` to confirm the main module still compiles after the new `agent/gatewaymcp` + `cmd/gateway-mcp` packages land.

**Image build (theborg only):** Docker/Colima is usually down on the dev machine — never attempt `docker build` locally. `ghcr.io/tdmtrader/mcp-gateway` is built, contract-tested (`TestLiveImageContract` with a stub `claude` on PATH), and pushed exclusively by the `build-mcp-gateway` job on the theborg `cicd` pipeline. Deploy it with `fly -t theborg set-pipeline -p concourse -c deploy/concourse-pipeline.yml -n` then `fly -t theborg trigger-job -j concourse/build-mcp-gateway -w` (login per memory `reference_theborg_cicd_live_concourse.md`). The DinD job runs on a single-node cluster; pod-loss mid-run manifests as an errored (not failed) build — re-trigger before debugging.

**Live-test requirements (theborg):** `KUBECONFIG=~/.kube/config`, kube-context `theborg` (https://theborg.home:6443), a THROWAWAY namespace via `kubectl create ns` (never `cicd`/`concourse` — live workloads), no pod-security label so the sidecar pod is allowed. Task 11's `TestLiveGatewaySidecarWiringAndCutoff` requires a pushed `GATEWAY_MCP_IMAGE` tag and uses a stub claude CLI baked into the sidecar command, so it costs nothing and needs no real Anthropic credential. `t.Cleanup` destroys the container; delete the namespace afterward. The wiring genuinely needs a live cluster — the fake clientset cannot exercise localhost sidecar transport (memory: SC-11).

**v1 heuristic — cutoff granularity (documented limit):** the claude CLI reports cost only at the end of a call, so the gateway cannot cut off *mid-call*. v1 cutoff is (a) pre-call admission — a call that starts with the slice already at/over the ceiling is refused with `status: failed` + `budget cutoff:`, adapter never invoked — plus (b) post-call accounting — the call that pushes cumulative over the ceiling is allowed to finish (its cost is metered), and the next call is refused. This slightly overshoots the slice by at most one call's cost; that is intentional (never silently truncate a call in flight) and is bounded by the per-call model max-turns. A future provider-native running-cost stream (or the platform-scheduled-pod backend) can tighten this behind the same `Adapter` contract without changing the tool surface.

**Manual end-to-end sanity (post-merge, wave-3 hand-written pipeline):** add the gateway sidecar to an `agent:` step (`sidecars: [gateway]`, image `ghcr.io/tdmtrader/mcp-gateway:<tag>`), set the step's `budget_slice_usd` small, and have the agent call `request_review`; verify the review returns with findings, a `cost.record` event appears in the flight-recorder stream, a `gateway`-source row lands in `agent_cost_ledger` (`fly -t theborg curl /api/v1/agent/costs?group_by=day`), and once the slice is spent the tool returns `budget cutoff:` `failed` (the run surfaces `needs_review`, never a truncated success).

**Rollback notes for the risky diffs:**
- *Sidecar/binary/kit (Tasks 2–9)* are net-new packages (`agent/gatewaymcp`, `cmd/gateway-mcp`) consumed by nothing in-tree; reverting them cannot break CI pipelines or the ATC. Rollback = delete the packages.
- *CI pipeline (Task 10)*: the `build-mcp-gateway` job has `trigger: false`, a unique name, and no shared `serial_groups`, so it cannot affect existing jobs (build/vet/unit/release or the sibling `build-mcp-platform`). It never force-pushes git refs. Rollback = `git revert` the commit and re-run `fly set-pipeline`; worst case is a stray `mcp-gateway-build-*` pod in `cicd` (cleaned by the trap; manually `kubectl delete pod -n cicd -l app=mcp-gateway-build`).
- *Metering (Task 5)*: the ledger POST is fire-and-forget — an ATC outage or a `costs:write` scope mismatch degrades to logged `error` events, never a failed tool call or a failed build (§1.4 rule). If ledger rows appear with the wrong `source`/`metadata`, they are append-only join-key data safe to leave; fix the writer and new rows are correct.
- *Cutoff (Task 6)*: enforced entirely against the env-provided `AGENT_BUDGET_SLICE_USD`; setting it unset/0 disables the ceiling (uncapped) for a hand-written pipeline while debugging, with no code change.
- *Dockerfile claude pin (Task 10)*: the bundled `@anthropic-ai/claude-code` version is pinned; a bad bump is reverted by restoring the prior tag and re-running `build-mcp-gateway` — the old image tag remains in GHCR and referenced workflow definitions keep working (import validation rejects `latest`, so nothing silently follows the bump).

**Amendment log (this plan):**
- 2026-07-08 (consistency fix; owner: gateway-mcp): corrected the Task 7 handler-error mechanism (pre-existing inconsistency, NOT introduced by the spec/plan-delivery change). The plan claimed invalid/malformed tool input produces a JSON-RPC `-32602` error object "via the mcpserver's handler-error path," but the gateway builds ON the shared `atc/api/mcpserver` (unlike dev-mcp/04, which ships its own server that genuinely emits `-32602`), and that shared server maps every tool-handler-returned error to a `tools/call` result with `isError=true` — it only emits `-32602` for a malformed `tools/call` envelope, never for a handler's returned error (locked in by its committed `server_test.go` "returns error result when tool handler errors"). Reworded the Task 7 intro plus the six `// -32602` code comments in `handleRequestReview`/`handleAskAgent` (Task 7 `tools.go`) to "MCP tool error (`isError=true`)", and added a doc comment on `handleRequestReview` naming the mechanism. Task 7's `TestMalformedInputIsMCPError` already asserts `result.isError=true` (never a top-level `-32602` object), so no test change was needed. Matches the same correction applied to 00-shared-contracts §3.2 and 08-platform-mcp-hitl during the platform-mcp read-tool work.
- 2026-07-09 (F13 / frozen SSE-transport delta, gateway legs D2-consumption + D3 + D4; owner: gateway-mcp; final-review Fable pass, REVIEW.md §8): the real claude CLI (v2.1.77, empirical) silently abandons a buffered, progress-free MCP tools/call at exactly 60s — `request_review`/`ask_agent` routinely exceed that, so without SSE progress heartbeats every long gateway call hits the F13 failure class. Applied:
  1. **Survey correction (Context, `atc/api/mcpserver` seam line):** "buffered JSON — no SSE/progress" was stale; after 08 Task 9b the shared server is SSE-capable in place (`NewServerWithHeartbeat`, `DefaultHeartbeat = 15s`, 3-arg `ToolHandler` with `progress func(string)`, SSE gated on `Accept: text/event-stream` + `params._meta.progressToken`, final JSON-RPC response as the last SSE frame — a mirrored port of `ci-agent/devmcp`'s proven implementation; module-boundary rule per the delta's D1).
  2. **Task 1 addendum (item d + new §11 bullet):** HARD wave-3 ordering — 08 Task 9b lands before this plan's Task 7; 08 Task 18b (real-CLI >5-minute park pin) gates Task 7's merge; the gateway acquires SSE purely by consuming the shared upgrade (no gateway-local transport code, no gateway CLI-pin test).
  3. **Task 2 (D3):** `Config.ProgressInterval` from `GATEWAY_MCP_PROGRESS_INTERVAL` — Go duration, default `mcpserver.DefaultHeartbeat` (15s); set-but-invalid, <= 0, or > 30s is a FATAL config error, never clamped. Tests added: `TestConfigFromEnvProgressIntervalDefaultsTo15s`, `TestConfigFromEnvProgressIntervalOverrideAndBounds`.
  4. **Task 7 (D4 + D2 consumption):** `request_review` and `ask_agent` declared MUST-stream; server built via `mcpserver.NewServerWithHeartbeat(cfg.ProgressInterval)`; both handlers moved to the 3-arg `ToolHandler` (the ticker guarantees `running <tool>` heartbeats; handlers/adapters MAY feed status lines via `progress`); `Server.ListenAndServe` now constructs an explicit `http.Server` with `WriteTimeout: 0`, `IdleTimeout: 0`, `ReadHeaderTimeout: 5s` (D4 server-timeout rule). Test added — the FROZEN D7 contract assertion: `TestSSEProgressHeartbeatsOnSlowAdapter` (40s fake adapter ⇒ >= 2 `notifications/progress` frames spaced < 30s apart, progressToken echoed verbatim, final SSE frame is the tools/call result).
  5. **Task 8:** binary logs the heartbeat interval and fails fast on a bad `GATEWAY_MCP_PROGRESS_INTERVAL` (validation in `ConfigFromEnv`; serve path inherits the D4 timeout rule from `Server.ListenAndServe`). Test added: `TestServeModeFailsFastOnBadProgressInterval`.
  6. **Task 9:** contract-test kit now checks progress emission the way platform-mcp's kit does — SSE opt-in tools/call must return `Content-Type: text/event-stream` with the tools/call result as the final SSE frame (transport shape; timing pinned by Task 7's 40s test).
  7. **Task 10 (D8):** image explicitly unchanged — the upgraded mcpserver arrives via the normal binary rebuild; the pin-test claude CLI lives on the test host, not in the image. Task 12's sweep notes `-short` must not be used.
