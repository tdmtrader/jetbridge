# Agentic Development Platform — End-State Design

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../plans/2026-07-21-agentic-functions-program.md) are authoritative. This end-state design birthed the ticket-centric wave plans (`00`-`15`) that are now historical; it is retained as the original vision document, superseded in its particulars by the 2026-07-21 functions-over-snapshots rework.

- **Date:** 2026-07-07 (rev 2, after design challenge round)
- **Status:** Draft for review (pre-planning; workstream decomposition happens in a later session)
- **Scope:** End-state architecture for evolving the jetbridge Concourse fork into an agentic development platform for a small team, adjacent to and augmenting existing CI functionality.

## Vision

A human (or later, Jira) files a **ticket**. The platform dispatches it as a **pipeline run** of a versioned **workflow definition**, executing agent steps in cluster pods via jetbridge, attributed to and funded by the triggering user's vaulted credentials. The agent learns how to build and test each repo through that repo's **dev-mcp** sidecar, requests reviews from other agents (any provider) through an **agent-gateway** sidecar, asks the human questions mid-flight when genuinely blocked, and finishes by leaving committed work in its workspace. A deterministic **harvest step** then independently re-verifies the gates, runs the judge, pushes the branch, and updates the ticket. The ticket page shows the diff, proof-carrying review evidence, live plan progress, cost, and score. A human merges — and the platform watches what happens next.

Every dollar is budgeted and attributed. Workflow versions compete on scorecards, not gates. Review findings, agent friction, and post-merge outcomes feed a process-intelligence loop whose explicit goal is to migrate catches leftward — out of LLM review and into deterministic tooling.

## Guiding principles

1. **Augment, don't conflict.** New capability lands as additional tenants in existing seams (`/api/v1/agent/*` routes, migrations + factories, RunnableComponents, step types, the embedded MCP server). CI pipelines continue working unchanged.
2. **Workflows are data, not code.** The unit of iteration is a versioned, content-hashed workflow definition. The platform ships a library (seeded from today's five ci-agent phases, decomposed) but nothing about the sequence is privileged.
3. **Code for mechanics, agents for judgment, humans for approval.** (Atium's lesson.) The litmus test for whether something is an agent-facing tool: *does the agent act on the result?* Terminal mechanics (pushing, publishing, archiving) are platform steps, not tools. Deterministic gates run before LLM judges; LLM judges run before humans; main is always human-gated.
4. **Verify state, don't trust transcripts.** The platform never relies on an agent's claim that gates passed — the harvest step re-runs them against the final workspace state.
5. **Everything is measurable, including the process.** Every run emits a flight recorder (events + tokens/turns/time/cost per step). Review findings, agent friction, and merge outcomes are analyzed to improve the process itself.

## Constants vs. variables

The platform fixes the **contract around a workflow**, not the workflow itself.

**Constant (the contract):**
- Front: a ticket (whose structured spec + plan storage — structured envelope, markdown prose bodies — is *available to any workflow that wants it*, see §1; flows that work directly from the ticket body are equally first-class).
- Back: work product = pushed branch + patch manifest + review evidence + judge score + outcome tracking.
- Throughout: flight-recorder event stream, cost accounting, gate results.

**Variable (the workflow definition):** step sequence, prompts, models, MCP sidecar mix, gate policy, subagent usage, human checkpoints. All swappable, all versioned, all comparable.

## Components

### 1. Tickets, specs, and plans

New `agent_tickets` table + `/api/v1/agent/tickets` CRUD + web page with edit mode.

- Lifecycle: `draft → queued → running → needs-review → merged | merged-with-fixes | sent-back | abandoned | concluded | failed` (post-review states in §9). `concluded` is terminal, reached from `needs-review` by explicit human disposition, for flows with no merge intent (spike/research) — a positive sibling of `abandoned`.
- `origin` field (`web`, `fly`, `jira`, `retrospective`) from day one so the future Jira sync is just another writer — no schema redesign when it arrives.
- References a workflow definition (name + version), a target repo, a per-ticket budget (defaulted, overridable), and the triggering user (for credential attachment and cost attribution).

**Spec/plan storage — DB-resident, envelope + prose:** No `.md` files as source of truth, and no fully-schematized prose. A spec row = structured fields (title, acceptance criteria, links) + a markdown `body` column; plan tasks are structured rows (`agent_ticket_tasks`: id, title, status, ordering, optional markdown detail). Prose stays markdown because rationale and tradeoffs are load-bearing context for downstream agents and humans. The **write path** is `submit_spec`/`submit_plan` (schema-constrained tool calls, never markdown parsing). The **read path** is the granular platform-mcp read tools — `read_ticket` (envelope + spec), `list_tasks` (task skeleton), `get_task` (one task's detail on demand), `update_task_status` (write-back) — so structured storage is never flattened to a document by default and the agent loads only what it needs. A workflow definition MAY opt into read-only `spec.md`/`plan.md` file materialization (`spec_delivery: files`) for prompt-cache friendliness. The ticket UI shows live task progress.

### 2. Workflow definitions

A versioned config artifact declaring: ordered agent steps, prompt set, model(s), MCP sidecar mix, gate policy, subagent tools available, human checkpoints (e.g. plan approval). Content-hashed for provenance (generalizing ci-agent's provenance mechanism).

- Stored in `agent_workflow_definitions`, editable via API/fly, importable from repo files.
- A definition version may be marked **live** (eligible for ticket dispatch). **Promotion is a human decision** — no mandatory regression gate. The platform informs the decision with workflow scorecards (§8); benchmark suites and experiment matrices are opt-in tooling for controlled comparison, not gates.

### 3. Pipeline runs (general Concourse improvement)

A **pipeline run** is a one-shot, parameterized execution of a pipeline template — Concourse's instance-vars data model plus the missing lifecycle. The defining semantic: normal and instanced pipelines are *reactive* (they check resources forever and trigger on new versions); **a run executes once and completes**.

- **Template:** a regular pipeline config flagged `template: true`. It never schedules on its own, its resource checks don't run, and it declares a **params schema** (names, types, defaults, required) instead of relying on unchecked `((vars))`.
- **Creation:** `fly run-pipeline -p <template> -v key=val` or `POST /api/v1/teams/:team/pipelines/:name/runs`. Params are validated against the schema; the run gets a monotonic number (`regression-suite/runs/42`, like build numbers); entry jobs (those with no `passed:` upstream) are auto-triggered immediately; downstream jobs flow through `passed:` chains as normal.
- **During:** the run *is* an instanced pipeline underneath (`instance_vars: {run: N}` + params), so all existing UI views, API endpoints, and fly commands work unmodified. `trigger: true` resource semantics are disabled; versions are pinned at creation.
- **Completion:** when no builds are pending or running, the run completes with an aggregate status (worst-of: succeeded/failed/errored/aborted), computed by a lifecycle component and stored on a `pipeline_runs` row — "did the suite pass?" becomes a queryable fact.
- **Retention:** per-template policy (`keep_last: K`, `ttl: Nd`); the lifecycle component archives expired runs via existing pipeline-archival machinery.
- **UI:** the template page lists runs (status, params, duration) — build history, for whole pipelines.

Three consumers of the same machinery: **ticket dispatch** (a ticket = one run of its workflow definition's rendered template), **experiments** (a batch of runs), and **general CI** (short-lived parameterized executions like regression suites, which today misuse instanced pipelines).

### 4. Dispatcher

A RunnableComponent (registrar/reaper pattern: polling + notifications, never notify-only) that:
- claims `queued` tickets with SQL claim/retry/timeout semantics,
- checks the budget ledger (per-ticket budget, global daily cap — over-cap dispatches remain queued, not failed),
- resolves the triggering user's vaulted credential and attaches it to the run as an ephemeral K8s secret,
- creates the pipeline run and transitions ticket state as it progresses.

### 5. Execution: native `agent:` step + harvest step

**`agent:` step** — a first-class step type (following the fork's `run:`/sidecar step recipe) replacing build-from-source shell scaffolding:
- Schema-visible in pipeline config; executed as a jetbridge pod under the existing supervisor (restart-resumable); artifacts flow through the DaemonSet fabric.
- Declares: prompt/step reference from the workflow definition, MCP sidecars to mount, budget slice, artifacts in/out.
- Step results (results.json, events.ndjson) ingest server-side, not just as artifacts.
- The agent's job ends when its workspace contains committed work. **Agents hold no credentials and cannot push.**

**Harvest step** — a deterministic, non-LLM platform step that runs after the final agent step:
1. Takes the workspace's committed state.
2. **Independently re-runs the gates** — build + tests via the repo's dev-mcp implementation (invoked by platform code; MCP tools are callable by code, not only by agents). Affected-component tests first, full suite per gate policy.
3. Runs the rubric judge (§8).
4. If gates pass: pushes `agent/ticket-<n>` using per-repo credentials that exist **only in the harvest pod** — never co-resident with an agent process.
5. Updates the ticket (state, evidence links, scores) regardless of outcome.

This replaces the earlier "gate-mcp" concept. Hard gates are enforced by *independent re-verification*, which is stronger than sidecar-mediated tool calls: the platform trusts workspace state, not agent transcripts.

### 6. MCP sidecar architecture

Sidecars (jetbridge's existing sidecar support) are the agent's capability surface. Three standard roles — the litmus test for every tool: *the agent must act on the result*.

**dev-mcp (per repo):** the typed contract for repo mechanics. Five tools:
- `list_components()` → `[{id, description, paths[], kind}]` — component ids are opaque, repo-defined strings; single-component repos return one entry.
- `build(component?)`, `run_tests(component?, focus?)`, `lint(component?)` — omitted component means whole repo.
- `affected_components(changed_paths[])` — maps a diff to touched components, enabling fast targeted iteration for the agent and cheap first-pass gating for the harvest step.

The platform defines the interface; each repo ships its implementation. This is what makes the platform language- and layout-agnostic: nothing ever guesses a shell command.

**platform-mcp:** the agent's mid-flight interaction surface with the platform itself: `read_ticket`, `list_tasks`, `get_task`, `submit_spec`, `submit_plan`, `update_task_status`, `ask_human` (§10). Small by design; anything terminal is the harvest step's job.

**agent-gateway-mcp:** provider-agnostic subagent access — `request_review(diff, rubric)`, `ask_agent(prompt, provider, model)`. Behind the tool contract sits an adapter layer (claude CLI, codex, cursor-cli, ...). v1 backend: **in-sidecar execution** (gateway container bundles provider CLIs); a platform-scheduled-pod backend can land later behind the same contract. The gateway is the **universal metering point**: every cross-agent call's tokens/turns/cost/latency lands in the flight recorder, enabling cross-provider comparison.

The sidecar mix is declared per workflow definition, making it part of the comparison surface (swap in an LSP mcp or a compact-context-format mcp; read the scorecard delta).

### 7. Delivery

- Per-repo git credentials held by the harvest step only.
- Harvest pushes `agent/ticket-<n>` branches after independent gate verification.
- The ticket page is a lightweight PR view: diff, review panel (existing `Build/AgentReview.elm` machinery), judge score, plan progress, cost, six-verdict finding feedback, and ticket-level disposition (§9).
- Merging happens wherever the repo lives. Main is always human-gated. No auto-merge in the end state.

### 8. Observability & workflow scorecards

- **Flight recorder per run:** events.ndjson plus per-step tokens/turns/wall-time/cost, stored in the artifact fabric, ingested to queryable rows (`agent_run_metrics`), surfaced on the run/ticket page. "Where did the turns go" is answerable for any run.
- **Workflow scorecards:** every run is tagged with its workflow-definition version; the platform compares versions side-by-side on **objective** metrics (gate pass rate, merge rate, merged-untouched rate, sent-back rate, time-to-merge, cost per ticket, turns, review findings per ticket, human-touch delta) and **subjective** ones (judge rubric scores, human verdict distributions). This — not a gate — is how promotion decisions get made.
- **Judge on every ticket:** after harvest gates pass, a rubric judge (schema-constrained verdicts, atium's method) scores the branch as triage signal for the human merger.
- **Opt-in experiments:** benchmark cases mined from the team's own repos ({ticket prompt, beforeRef, referenceRef}, maintained via an interactive extraction skill), run as a batch of pipeline runs across workflow-definition variants when a controlled comparison is wanted.
- Run statuses use atium's three-way taxonomy: **ok / failed / error** — "agent did badly" is distinguished from "platform broke".

### 9. Outcome tracking (post-completion lifecycle)

Tickets don't end at `needs-review`. The platform watches the target repo natively (it already knows how to check repos — no SCM webhooks required for v1):

- **merged:** the agent branch's head becomes reachable from the default branch.
- **merged-with-fixes:** human commits landed on the agent branch before merge. The **human-touch delta** (lines the human changed post-agent) is recorded — the single most honest quality metric the platform collects.
- **sent-back / abandoned:** explicit disposition on the ticket UI with a small reason taxonomy + free text (the ticket-level analog of six-verdict finding feedback).
- **concluded:** explicit human disposition from `needs-review` for flows with no merge intent (spike/research) — "run finished, human reviewed, no merge intended" — a positive terminal sibling of `abandoned`, so finished spikes neither rot in `needs-review` nor count against merge-rate metrics.

Outcomes feed the scorecards (§8) and process intelligence (§10). Jira status sync rides the same seam in phase 2.

### 10. Process intelligence

The subsystem that closes the loop from "what happened" to "how the process improves" — the platform's version of the review/iterate cycle. Explicit optimization target: **catches migrate leftward** — review findings per ticket trend down while escaped defects stay flat, because catches move from LLM review into deterministic gates.

- **Finding analytics:** review findings (already category/severity-tagged) aggregated per repo and per workflow version. Recurring classes are automation candidates.
- **Calibration:** false-positive rate from six-verdict feedback; missed-issue rate from post-merge defects traced back to the ticket that should have caught them.
- **Friction mining:** flight-recorder signatures of agent pain — repeated failing test loops, tool errors, turn burn, context re-reads — aggregated per workflow version (mechanizing what forge cgx.md notes capture by hand).
- **Retrospective workflow:** a recurring agent job that reads findings, verdicts, friction, and outcomes, and proposes *concrete process changes* — a lint rule automating a recurring review catch, a prompt amendment, a dev-mcp gate addition, a workflow-definition edit — filed **as tickets** (`origin: retrospective`) into the normal human-merged loop. Process improvement is just more agent work.

**Human-in-the-loop:** the `ask_human(question, options)` platform-mcp tool parks the run (supervisor wait semantics; pause-pod model already supports this), surfaces the question on the ticket page plus notification, and resumes on answer. Workflow definitions may also declare checkpoint gates (e.g. plan approval before implementation). HITL is early scope, not deferred.

### 11. Governance, credentials, and identity

- **Credentials — vaulted per-user tokens:** `fly agent auth` walks each user through `claude setup-token` (verified: one-year token, headless via `CLAUDE_CODE_OAUTH_TOKEN`, subscription plans; no auto-refresh — the platform tracks expiry and nags). Tokens are vaulted encrypted per user; dispatch attaches the *triggering user's* token to the run's pods as an ephemeral K8s secret; the cost ledger attributes spend per person. Survives Jira-origin tickets (Jira user maps to platform user). Open empirical question: whether headless usage shares the user's interactive rate-limit windows — test early on theborg before large batches.
- **Budgets:** per-ticket budget (default per workflow definition, overridable) + global daily cap. Enforcement at the gateway (metering + cutoff) and the dispatcher (admission).
- **Cost ledger:** `agent_cost_ledger` rows from the usage data ci-agent already parses; rolls up per ticket/run/user/day; dashboard view.
- **Agent identity:** per-agent principals (extending `accessor/roles.go`) replace the single static publish token. Each sidecar/step authenticates as a scoped identity.

## Data model sketch

New tables (migration + factory pattern, as `agent_reviews` did):

| Table | Purpose |
|---|---|
| `agent_tickets` | lifecycle incl. outcome states, origin, repo, workflow ref, budget, triggering user |
| `agent_ticket_specs` / `agent_ticket_tasks` | structured envelope + markdown bodies; task status rows |
| `agent_workflow_definitions` | versioned workflow configs, content hash, live flag |
| `pipeline_runs` | run lifecycle over instanced pipelines: number, params, aggregate status, retention |
| `agent_run_metrics` | ingested flight-recorder rollups (tokens/turns/time/cost per step) |
| `agent_outcomes` | merge state, human-touch delta, disposition + reason |
| `agent_user_credentials` | vaulted per-user tokens, expiry tracking |
| `agent_benchmark_cases` / `agent_experiments` | opt-in eval substrate |
| `agent_cost_ledger` | spend accounting per ticket/run/user/day |

Existing tables extended, not duplicated: `agent_reviews`, `agent_feedback`.

## Out of scope (end state)

- Interactive/reattachable agent sessions (pause-pod primitives remain available if wanted later).
- External inboxes (email/chat). Jira is phase 2, via the ticket `origin` seam.
- Auto-merge of any kind.
- Mandatory regression gates on workflow promotion (scorecards inform; humans decide).
- Multi-namespace / per-agent K8s isolation beyond identity principals (revisit if the team grows).

## Failure handling

- Budget exhaustion → dispatch queues (daily cap) or run halts at the gateway with ticket state `needs-review` and a partial work product (per-ticket cap). Never silent truncation.
- Budget enforcement points differ by spend source. The main agent's *own* claude-CLI spend is bounded per-step by `--max-turns` and the step timeout, then reconciled against the budget at step-admission (the dispatcher's daily-cap check and the agent step's `StepSlice` resolution) and post-hoc when its usage envelope is ingested — **not** interrupted mid-call. Only *cross-agent* gateway calls receive a mid-call dollar cutoff; the gateway is the one place a live dollar ceiling halts an in-flight LLM call.
- Harvest gate failure → ticket `needs-review` with failing evidence attached; nothing is pushed.
- Run timeout / platform fault → status `error` (not `failed`); dispatcher retry semantics with attempt caps.
- `ask_human` timeout → configurable per workflow definition (park indefinitely vs. proceed-with-default vs. fail).
- Credential expiry mid-run → run halts as `error`; ticket notes credential owner; nag on expiry horizon.
- Web-node restarts → jetbridge supervisor resume semantics keep in-flight agent steps alive; pipeline-run state is DB-backed.
- Notification loss → all components poll (established fork lesson: never notify-only).

## Testing approach

- Unit: existing patterns (Ginkgo suites, counterfeiter fakes, `fake.NewSimpleClientset()` for K8s).
- Contract tests for the three MCP interfaces (dev-mcp, platform-mcp, gateway) so repo implementations and provider adapters validate independently.
- Harvest-step verification logic tested against fixture workspaces (gates pass / fail / flaky).
- Pipeline-run lifecycle: unit tests on completion/retention semantics + a topgun behavioral spec.
- The platform improves itself: scorecards and the retrospective workflow apply to the platform's own workflow definitions on theborg.

## Open items for the planning phase

1. Pipeline-run implementation depth: template flag + params schema representation; how completion detection interacts with in-flight aborts.
2. Workflow-definition schema (step composition grammar, prompt packaging, sidecar declaration, checkpoint declaration).
3. dev-mcp interface finalization (error taxonomy, streaming semantics for long test runs, component id conventions).
4. Harvest gate policy language (which checks, affected-vs-full suite policy, per workflow definition).
5. Judge rubric format and its relationship to the six-verdict feedback taxonomy.
6. Benchmark case storage (in-repo dir vs. DB) and the extraction-skill port.
7. Credential vault implementation (encryption at rest, K8s secret lifecycle, expiry nagging) and the shared-rate-limit empirical test on theborg.
8. Outcome watcher design (polling cadence, branch-tracking heuristics, human-touch delta computation).
9. Retrospective workflow v1 scope (which inputs, proposal format, cadence).
10. Jira sync design (phase 2).
11. Consolidation strategy for ci-agent module / `agent/` package / atium concepts (shared schema module vs. contract tests).

## Amendments

- **2026-07-09 (F12 — budget-honesty correction, Failure handling):** Added a line to §Failure handling clarifying that the main agent's own claude-CLI spend is bounded per-step by `--max-turns` / step timeout and reconciled against the budget at step-admission (dispatcher daily-cap check + agent step `StepSlice`) and post-hoc at usage ingestion — **not** mid-call; only cross-agent gateway calls receive a mid-call dollar cutoff. This corrects the earlier implication (rev 2 §11 "Enforcement at the gateway (metering + cutoff)") that the gateway caps *all* agent spend. No contract names changed; consistent with 00-shared-contracts.md §2.7 (`budget.Checker.StepSlice`), §2.8 (`AgentStep.MaxTurns`), and 10-gateway-mcp.md (gateway cutoff enforced only against `AGENT_BUDGET_SLICE_USD`).
- **2026-07-09 (E1 — flow decoupling, per `plans/agentic-platform/FLOWS.md`):** Reworded the "Constants vs. variables" Front constant from "ticket → spec + plan" to "a ticket (whose structured spec + plan storage is available to any workflow that wants it)" — the enforced front constant is the ticket envelope; spec/plan rows are optional per-workflow instrumentation, and ticket-body-driven flows are first-class (kills FLOWS.md couplings R1/R2 at the source). Added the terminal `concluded` state to the §1 lifecycle enum now, before the enum freezes ("run finished, human reviewed, no merge intended"; reached from `needs-review` by explicit human disposition; positive sibling of `abandoned`), plus a §9 outcome-tracking entry so concluded spikes don't rot in `needs-review` or depress merge-rate scorecards (FLOWS.md §3 spike-research gap, §4 harvest-knob row).
