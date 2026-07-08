# Agentic Development Platform — End-State Design

- **Date:** 2026-07-07
- **Status:** Draft for review (pre-planning; workstream decomposition happens in a later session)
- **Scope:** End-state architecture for evolving the jetbridge Concourse fork into an agentic development platform for a small team, adjacent to and augmenting existing CI functionality.

## Vision

A human (or later, Jira) files a **ticket**. The platform dispatches it as a **pipeline run** of a versioned **workflow definition**, executing agent steps in cluster pods via jetbridge. The agent learns how to build and test each repo through that repo's **dev-mcp** sidecar, requests reviews from other agents (any provider) through an **agent-gateway** sidecar, and can only deliver work through a **gate-mcp** sidecar that holds the credentials and enforces preconditions. The agent pushes a branch; a rubric judge scores it; the ticket page shows the diff, proof-carrying review evidence, live plan progress, cost, and score. A human merges.

Every dollar is budgeted. Every workflow change is regression-gated against benchmarks mined from the team's own git history. Every human verdict feeds back into future runs.

## Guiding principles

1. **Augment, don't conflict.** New capability lands as additional tenants in existing seams (`/api/v1/agent/*` routes, migrations + factories, RunnableComponents, step types, the embedded MCP server). CI pipelines continue working unchanged.
2. **Workflows are data, not code.** The unit of iteration is a versioned, content-hashed workflow definition. The platform ships a library (starting from today's five ci-agent phases) but nothing about the sequence is privileged.
3. **Authority lives in sidecars, not agents.** Agents hold no credentials. Privileged actions (push, publish, phase completion) are MCP tools whose sidecar implementations verify preconditions first. Hard gates are capabilities the agent physically lacks, not instructions it is asked to follow.
4. **Code for mechanics, agents for judgment, humans for approval.** (Atium's lesson.) Deterministic gates run before LLM judges; LLM judges run before humans; main is always human-gated.
5. **Everything is measurable.** Every run emits a flight recorder (events + tokens/turns/time/cost per step). If a configuration change can't be evaluated, it can't be trusted.

## Constants vs. variables

The platform fixes the **contract around a workflow**, not the workflow itself.

**Constant (the contract):**
- Front: ticket → `spec.md` + `plan.md` (Superpowers-style), with plan tasks parsed into structured rows for tracking.
- Back: work product = pushed branch + patch manifest + review evidence + judge score.
- Throughout: flight-recorder event stream, cost accounting, gate results.

**Variable (the workflow definition):** step sequence, prompts, models, MCP sidecar mix, gate policy, subagent usage. All swappable, all versioned, all A/B-testable.

## Components

### 1. Tickets

New `agent_tickets` table + `/api/v1/agent/tickets` CRUD + web page with edit mode.

- Lifecycle: `draft → queued → running → needs-review → merged | closed | failed`.
- Fields include `origin` (e.g. `web`, `fly`, `jira`) from day one so the future Jira sync is just another writer to the same table — no schema redesign when it arrives.
- References a workflow definition (name + version) and a target repo.
- Carries a per-ticket budget (defaulted, overridable).

**Structured plan tracking:** `spec.md`/`plan.md` remain markdown artifacts, but plan tasks are additionally parsed into `agent_ticket_tasks` rows (id, title, status, ordering). The ticket UI shows live progress; workflow evolution cannot break tracking because the parser targets the artifact contract, not any specific workflow.

### 2. Workflow definitions

A versioned config artifact declaring: ordered agent steps, prompt set, model(s), MCP sidecar mix, gate policy, subagent tools available. Content-hashed for provenance (ci-agent's existing provenance mechanism generalizes).

- Stored in the DB (`agent_workflow_definitions`), editable via API/fly, importable from repo files.
- A definition may be marked **live** (eligible for ticket dispatch). Promoting a new version to live requires passing its benchmark regression gate against the incumbent (see §8).
- Today's five ci-agent phases (plan/implement/review/fix/qa) become the seed library, decomposed into reusable steps rather than a fixed chain.

### 3. Pipeline runs (general Concourse improvement)

A **pipeline run** is an instanced pipeline with a managed lifecycle — Concourse's instance-vars data model plus the missing run semantics (the Tekton Pipeline/PipelineRun split):

- `fly run-pipeline -p <template> -v key=val` or `POST /api/v1/pipeline-runs`: resolves the template with params, creates the instance, auto-triggers entry job(s).
- **Completion semantics:** when the run's builds finish, the run completes with an aggregate status.
- **Retention policy on the template:** keep last K runs and/or archive after N days. No manual archival hygiene.
- UI groups runs under their template (like build history, for whole pipelines).

Three consumers of the same machinery:
1. **Ticket dispatch** — a ticket becomes one run of its workflow definition's rendered template.
2. **Experiments** — an experiment is a batch of runs (cases × variants).
3. **General CI** — e.g. regression suites that today awkwardly use instanced pipelines for short-lived parameterized executions.

### 4. Dispatcher

A RunnableComponent (registrar/reaper pattern: polling + notifications, never notify-only) that:
- claims `queued` tickets with SQL claim/retry/timeout semantics,
- checks the budget ledger (per-ticket budget, global daily cap — over-cap dispatches remain queued, not failed),
- renders the ticket's workflow definition into a pipeline-run creation,
- transitions ticket state as the run progresses/completes.

### 5. Execution: native `agent:` step

A first-class step type (following the fork's fresh `run:`/sidecar step recipe) replacing the build-ci-agent-from-source shell scaffolding:

- Schema-visible in pipeline config; executed as a jetbridge pod.
- Declares: prompt/step reference from the workflow definition, MCP sidecars to mount, budget slice, artifacts in/out.
- Step results (results.json, events.ndjson) ingest server-side rather than only living as artifacts.
- Runs under jetbridge's existing supervisor for restart-resumability; artifacts flow through the DaemonSet fabric.

### 6. MCP sidecar architecture

Sidecars (jetbridge's existing sidecar support) are the platform's trust and capability boundary. Three standard roles:

**dev-mcp (per repo):** the typed contract for repo mechanics — `build`, `run_tests`, `run_focused_test`, `lint`, `repo_conventions`. The platform defines the interface; each repo ships its implementation. This is what makes the platform language-agnostic: proof-carrying review calls `run_tests` and never guesses shell commands.

**gate-mcp:** holds credentials and enforces the checklist. The agent container has no git credential; the only delivery path is `gate.push_branch()`, which the sidecar refuses unless preconditions hold (dev-mcp tests green, review published, judge invoked, budget not exceeded). Same pattern for `publish_review`, `mark_task_complete`. Gates are enforced by capability, not by prompt.

**agent-gateway-mcp:** provider-agnostic subagent access — `request_review(diff, rubric)`, `ask_agent(prompt, provider, model)`. Behind the tool contract sits an adapter layer (claude CLI, codex, cursor-cli, ...). The host agent is fully decoupled from subagent implementation.

- **v1 backend: in-sidecar execution** (gateway container bundles provider CLIs, runs calls in place). A platform-scheduled-pod backend can land later behind the same contract.
- The gateway is the **universal metering point**: every cross-agent call's tokens/turns/cost/latency lands in the flight recorder, enabling cross-provider comparison.

The sidecar mix is declared per workflow definition, making it part of the experiment surface (e.g. swap in an LSP mcp or a compact-context-format mcp and measure the delta).

### 7. Delivery

- Per-repo git credentials, held by gate-mcp only.
- Agent pushes `agent/ticket-<n>` branches.
- The ticket page is a lightweight PR view: diff, review panel (existing `Build/AgentReview.elm` machinery), judge score, plan progress, cost, six-verdict feedback buttons.
- Merging happens wherever the repo lives. Main is always human-gated. No auto-merge in the end state.

### 8. Observability & experimentation (atium absorbed)

The platform's evolutionary engine:

- **Flight recorder per run:** events.ndjson plus per-step tokens/turns/wall-time/cost, stored in the artifact fabric, ingested to queryable rows, surfaced on the run/ticket page. "Where did the turns go" is answerable for any run.
- **Benchmarks from history:** cases mined from the team's own repos ({ticket prompt, beforeRef, referenceRef}, atium's method), maintained via an interactive extraction skill + platform storage.
- **Experiments:** benchmark cases × workflow-definition variants, executed as a batch of pipeline runs. Results roll up into a comparison view scoring **quality** (deterministic gates → rubric judge with schema-constrained verdicts) × **efficiency** (tokens, turns, wall time, cost).
- **Judge on every ticket:** after gates pass, the rubric judge scores the produced branch as triage signal for the human merger.
- **Regression gate:** promoting a workflow definition version to live requires running its benchmark suite versus the incumbent and passing the delta policy.
- **Feedback consumption:** the collected six-verdict `agent_feedback` is injected at prompt-render time (suppress noisy categories, few-shot false positives) and tunes scoring thresholds per repo.
- Run statuses use atium's three-way taxonomy: **ok / failed / error** — "agent did badly" is distinguished from "platform broke".

### 9. Governance

- **Budgets:** per-ticket budget (default per workflow definition, overridable per ticket) + global daily cap. Enforcement at the gateway/gate sidecars (metering + cutoff) and at the dispatcher (admission).
- **Cost ledger:** `agent_cost_ledger` rows fed by the cost/usage data ci-agent already parses; rolls up per ticket/run/day/team; dashboard view.
- **Agent identity:** per-agent principals (extending `accessor/roles.go`) replace the single static publish token. Each sidecar/step authenticates as a scoped identity; the shared-token blast radius goes away.

## Data model sketch

New tables (migration + factory pattern, as `agent_reviews` did):

| Table | Purpose |
|---|---|
| `agent_tickets` | ticket lifecycle, origin, repo, workflow ref, budget |
| `agent_ticket_tasks` | parsed plan tasks (id, title, status, ordering) |
| `agent_workflow_definitions` | versioned workflow configs, content hash, live flag |
| `pipeline_runs` | run lifecycle over instanced pipelines, aggregate status, retention |
| `agent_run_metrics` | ingested flight-recorder rollups (tokens/turns/time/cost per step) |
| `agent_benchmark_cases` / `agent_experiments` | eval substrate |
| `agent_cost_ledger` | spend accounting |

Existing tables extended, not duplicated: `agent_reviews`, `agent_feedback`.

## Out of scope (end state)

- Interactive/reattachable agent sessions (pause-pod primitives remain available if wanted later).
- External inboxes (email/chat). Jira is phase 2, via the ticket `origin` seam.
- Auto-merge of any kind.
- Multi-namespace / per-agent K8s isolation beyond identity principals (revisit if the team grows).

## Failure handling

- Budget exhaustion → dispatch queues (daily cap) or run halts at the gateway with ticket state `needs-review` and a partial work product (per-ticket cap). Never silent truncation.
- Run timeout / platform fault → status `error` (not `failed`); dispatcher retry semantics with attempt caps.
- Web-node restarts → jetbridge supervisor resume semantics keep in-flight agent steps alive; pipeline-run state is DB-backed.
- Notification loss → all components poll (established fork lesson: never notify-only).

## Testing approach

- Unit: existing patterns (Ginkgo suites, counterfeiter fakes, `fake.NewSimpleClientset()` for K8s).
- Contract tests for the three MCP interfaces (dev-mcp, gate-mcp, gateway) so repo implementations and provider adapters can be validated independently.
- The platform tests itself: benchmark suites + regression gates apply to the platform's own workflow definitions before they go live on theborg.
- K8s behavioral coverage via the existing topgun/k8s_behavioral harness for the `agent:` step and sidecar mounting.

## Open items for the planning phase

1. Pipeline-run implementation depth: how much lifecycle lands in core vs. an agent-platform-owned layer over instance vars.
2. Workflow-definition schema details (step composition grammar, prompt packaging, sidecar declaration format).
3. dev-mcp interface finalization (tool list, error taxonomy, streaming semantics for long test runs).
4. Gate policy language (which preconditions, how expressed per workflow definition).
5. Judge rubric format and its relationship to the six-verdict feedback taxonomy.
6. Benchmark case storage location (in-repo `.platform/` dir vs. DB) and the extraction skill port.
7. Jira sync design (phase 2).
8. Consolidation strategy for the three codebases (ci-agent module, `agent/` package, atium concepts) — shared schema module vs. contract tests.
