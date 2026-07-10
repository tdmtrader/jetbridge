# Workflow Renderer + Dispatcher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render a live workflow-definition version into a `template: true` pipeline (golden-file validated) whose `agent:` steps and terminal `harvest:` step are fully self-contained, then ship the `dispatcher` RunnableComponent that claims `queued` tickets, admits them against budgets, attaches the filer's vaulted credential as an ephemeral K8s secret, creates the pipeline run, and walks the ticket through the single transition function.

**Architecture:** Two libraries in a new `agent/dispatch` package. (1) A pure `Renderer` turns a `workflow.Config` + a `tickets.Ticket` (+ its spec/plan) into an `atc.Config` with `Template: true`, one job of rendered `agent:` steps interleaved with checkpoint steps, and an implicitly-appended terminal `harvest:` step — matching contracts §2.8 / §2.8.1 exactly, resolving every workflow-table reference into literal step config (the render-time-resolution rule). (2) A `Dispatcher` (RunnableComponent, polling+notify via the component Coordinator's lock, never notify-only) that loops over queued tickets, uses `tickets.Store.Transition(queued→running)` as the atomic multi-node claim, admits via `budget.Checker`, resolves the credential via `credentials.Backend` + `credentials.SecretAttacher`, persists the rendered template via `team.SavePipeline`, and starts the run via `db.PipelineRunFactory.CreateRun`. Each `Run` pass ends with a **run-completion reconciler** (`reconcileCompletedRuns`, Task 11b; added 2026-07-09 per the frozen checkpoint seam delta §6, F17) that walks `running` tickets whose pipeline run completed and applies the frozen decision tree — rejected `send_back` checkpoint ⇒ `running→queued` re-dispatch, rejected `fail` checkpoint or checkpoint-free failure ⇒ `running→needs_review`, unanswered checkpoint on a dead run ⇒ `running→errored` (orphan rows released), succeeded-but-still-running ⇒ `needs_review` safety net. Dispatch owns **no new tables and no new migrations** — it is pure integration of six wave-1/2/3 contract surfaces.

**Tech Stack:** Go (`agent/dispatch`, `atc/db`, `atc/atccmd`), Ginkgo/Gomega for `atc/*` packages, plain `testing` for the stdlib-only renderer, squirrel/`goccy/go-yaml`, the component framework (`atc/component`), client-go fake clientset for secret-attach tests, and the theborg live-cluster pattern for the end-to-end dispatch proof.

---

## Context

**Charter (id: `dispatch`, size L, wave 4).** Goal: render workflow-definition versions into concrete pipeline templates (golden-file validated), then ship the RunnableComponent that claims queued tickets, admits against budgets, attaches vaulted credentials, and makes "queued" sufficient. Two milestones:
- **MILESTONE 1 — renderer library:** definition version → `template: true` pipeline config with fully-resolved `agent:` step config (steps never read workflow tables), sidecar mix, gate policy, checkpoint declarations, terminal harvest step; golden-file tests per definition version validated against `atc configvalidate`; rendered `spec.md`/`plan.md` materialized as run inputs; hand-dispatch via `fly run-pipeline` supported immediately (retires the wave-3 hand-written template).
- **MILESTONE 2 — dispatcher RunnableComponent:** SQL claim/retry/timeout semantics with attempt caps under multi-web-node deployments; budget admission (per-ticket + global daily cap — over-cap dispatches stay queued, never failed; platform faults → error, not failed); credential resolution `agent_user_credentials` → ephemeral K8s secret, credential-expiry mid-run → error with owner noted; ticket transitions `queued→running→needs_review/failed/errored` exclusively through ticket-core's transition function; **run-completion reconciliation** (added 2026-07-09, checkpoint seam delta §6, F17): the same Dispatcher pass walks `running` tickets whose run completed — rejected `send_back` checkpoints re-queue (`running→queued`, attempt_count++ via §2.1, loop capped by the existing MaxAttempts guard), rejected `fail` checkpoints and checkpoint-free failures go to `needs_review`, unanswered checkpoints on a completed run go to `errored` with the orphan question rows released.

**scope_out (do NOT implement here):** harvest logic (a rendered step — harvest-step owns it), ticket CRUD/UI (ticket-core, delivery-outcomes), experiment batch creation (process-intel-experiments reuses this renderer). This plan therefore produces zero SQL migrations and no new HTTP routes — it consumes existing factories in-process (contracts §4.1: `runs:create` scope was removed; run creation is in-process via `PipelineRunFactory`).

**Prior waves (assume LANDED, surfaces exact per the amended contracts doc + each plan file's addenda):**
- **agent-identity (w1):** `agent/api/principals` — `principals.Store` (`Create(CreateSpec) (Principal, token, error)`), `CreateSpec{Name,Description,Scopes,TeamName,CreatedBy,ExpiresAt}`, scope constants `ScopeTicketsRead`/`ScopeTicketsWrite`/`ScopeMetricsWrite`/`ScopeCostsWrite`/`ScopeQuestionsAnswer`; `db.NewAgentPrincipalsFactory(dbConn)` returns a `principals.Store`. Dispatch mints the per-run principal token here (§8.1 `AGENT_PRINCIPAL_TOKEN`).
- **credentials-and-budgets (w1):** `agent/budget` — `budget.Checker` (`TicketRemaining`/`GlobalDailyRemaining`/`StepSlice`/`Record`), `budget.NewChecker(ledger, budgets, cfg)`, `budget.TicketBudgets` seam (charter note: "real implementation arrives with ticket-core/dispatch" — **this plan supplies it**, Task 8), `budget.Remaining`, `budget.LedgerEntry`, source constants (`SourceAgentStep`…); `agent/credentials` — `credentials.Backend` (embeds `Store` with `Resolve(userID,kind) (*Credential,bool,error)`, `KindAnthropicOAuth`), `credentials.SecretAttacher` (`Attach(ctx,runID,cred,principalToken) (name,error)`, `Cleanup(ctx,runID)`), `credentials.NewK8sSecretAttacher(clientset, ns)`, `db.NewAgentUserCredentialsFactory(dbConn)` (impl `credentials.Backend`), `db.NewAgentCostLedgerFactory(dbConn)` (impl `budget.Ledger`); web flag `--agent-daily-budget-usd` (`cmd.AgentDailyBudgetUSD`) and the `budget.NewChecker` wiring already exist in `command.go`.
- **pipeline-runs (w1):** `db.PipelineRunFactory` (`CreateRun(templatePipelineID int, params map[string]any, createdBy string) (PipelineRun, error)`, `ErrNotATemplate`, `ErrTemplateNotFound`), `db.NewPipelineRunFactory(dbConn, lockFactory)`; `atc.Config` gains `Template bool` + `Params []ParamSchema` + `RunRetentionConfig`; the `pipeline_run_lifecycler` component (owns run completion + secret `Cleanup` on completion per §8.2). §7.1 addendum: reserved param name `run`; entry jobs auto-triggered by `CreateRun`; template flag stays true on instances.
- **workflow-store (w1):** `agent/workflow` — `workflow.Config` grammar (§6: `SchemaVersion`,`Name`,`Defaults`,`Budget{TicketUSD,JudgeUSD}`,`Sidecars map[string]Sidecar{Image,Role,Providers}`,`Prompts map[string]string`,`Schemas`,`Steps []Step{Agent,Prompt,Sidecars,BudgetSliceUSD,Model,MaxTurns,Inputs,Outputs,OutputSchema,Checkpoint,OnReject}`,`HITL{AskTimeout,AskTimeoutSeconds}`,`GatePolicy{Gates []Gate,OnGateFailure}`,`Gate{Gate,Scope,Focus,Timeout,Retries}`,`Judge{Rubric []RubricDimension,PassThreshold}`), `workflow.Parse([]byte)`, `workflow.Definition{ID,Name,Version,ContentHash,Live,Config,RawYAML}`, `workflow.Store` (`Live(name) (*Definition,bool,error)`, `Get(name,version)`), `db.NewAgentWorkflowsFactory(dbConn)` (impl `workflow.Store`).
- **ticket-core (w2):** `agent/api/tickets` — `tickets.Ticket` (§2.1), `State` constants (`StateQueued`,`StateRunning`,`StateNeedsReview`,`StateFailed`,`StateErrored`,`StateDraft`), `tickets.Store` (`Get`, `List(ListFilter)`, `Transition(id, from, to, TransitionMeta)`, `LatestSpec`, `ActivePlan`), `TransitionMeta{PipelineRunID *int, Branch string, ErrorDetail string}`, `ListFilter{State,Repo,Origin,Limit}`, errors `ErrStaleTransition`/`ErrTicketNotFound`/`ErrInvalidTransition`, render helpers `tickets.RenderSpecMarkdown(t, spec)` / `tickets.RenderPlanMarkdown(t, tasks)`, `db.NewAgentTicketsFactory(dbConn)`. Transition side effects (addendum §2.1): `→running` sets `dispatched_at`, `pipeline_run_id`; `→queued` from `running` bumps `attempt_count`; UPDATE guarded by `WHERE id=$id AND state=$from`; zero rows → `ErrStaleTransition`/`ErrTicketNotFound`.
- **agent-step (w2):** `atc.AgentStep` (§2.8: `Name`(`json:"agent"`),`Prompt`,`PromptFile`,`Model`,`MaxTurns`,`BudgetSliceUSD`,`OutputSchema`,`Sidecars []SidecarSource`,`Inputs`,`Outputs`,`Env map[string]string`,`Timeout`,`Limits`,`Requests`) registered in `StepPrecedence`; §8.1 agent-step addendum env vars (`AGENT_PROMPT`, `AGENT_PROMPT_FILE`, `AGENT_MODEL`, `AGENT_MAX_TURNS`, `AGENT_OUTPUT_SCHEMA`, `AGENT_FLIGHT_DIR`, plus identity keys `AGENT_TICKET_ID`/`AGENT_PIPELINE_RUN_ID`/`AGENT_WORKFLOW_NAME`/`AGENT_WORKFLOW_VERSION`/`AGENT_WORKFLOW_HASH`/`AGENT_BUDGET_SLICE_USD` and MCP-URL-by-sidecar-name derivation `dev`/`platform`/`gateway`→7780/7781/7782); web flag `--agent-step-image`.
- **harvest-step (w3):** `atc.HarvestStep` (§2.8.1: `Name`(`json:"harvest"`),`Workspace`,`Repo`,`TargetBranch`,`TicketID`,`PipelineRunID`,`Branch`,`Push`,`DevMCP *SidecarSource`,`GatePolicy harvest.GatePolicy`,`Judge *harvest.JudgeConfig`,`Timeout`) registered in `StepPrecedence`; `agent/harvest` — `harvest.GatePolicy{Gates []Gate{Gate,Scope,Focus,Timeout,Retries},OnGateFailure}`, `harvest.JudgeConfig{Rubric []RubricDimension{Name,Weight,Guidance},PassThreshold,Model,BudgetUSD}`; harvest exec reads `HARVEST_CONFIG` env + mounts `agent-harvest-git-<slug>` git creds on the main container only.

**Contract-surface sections this plan PRODUCES:** `renderer-library-and-golden-templates` (§2.8 emission target, §6 input, §7 output validation). One additive amendment (Task 1): the wave-4 renderer↔pipeline-runs↔ticket-core agreement on how the rendered template is persisted + which params the renderer's template declares, and the dispatch-owned `budget.TicketBudgets` implementation + the `concourse/ticket` secret label.

**Contract-surface sections this plan CONSUMES:** `ticket-tables-and-transition-function` (§1.7, §2.1), `workflow-definition-schema-and-hash` (§1.6, §2.2, §6), `pipeline-runs-api-and-lifecycle` (§1.5, §2.3, §7, §7.1), `agent-step-config-schema` (§2.8, §8.1), `harvest-terminal-step-schema-and-gate-policy` (§1.10, §2.8.1, §6.3, §8.3), `budget-library-and-cost-ledger` (§1.4, §2.7), `user-credential-vault-and-secret-helper` (§1.3, §2.6, §8.2), `agent-principal-auth` (§1.2, §4.1 — minting a per-run principal).

**Real-code seams verified on branch `jetbridge`:** `atc/component.go:1-32` (component name constants — add `ComponentAgentDispatcher`), `atc/atccmd/command.go:795-829` (component Runner + Coordinator lock wiring), `:1186-1258` (`backendComponents` slice), `:1300-1315` (K8s clientset construction — `jetbridge.NewClientset(k8sCfg)`), `:2340` (`RunnableComponent` struct), `atc/pauser/pipeline_pauser.go` (RunnableComponent recipe: `Run(ctx) error`), `atc/db/team.go:45-50` + `:619` (`SavePipeline(ref, atc.Config, from, initiallyPaused)`), `atc/config.go:20-28` (`Config` fields + `UnmarshalConfig`), `atc/sidecar.go:13-63` (`SidecarConfig`/`SidecarSource`/`SidecarEnvVar`), `atc/steps.go:14` (`Step` envelope), `atc/configvalidate/validate.go:42` (`Validate(atc.Config)`), `atc/component/runner.go:13` (`NotificationsBus`).

---

### Task 1: Wave-start contract addendum (renderer persistence + dispatch-owned seams)

The charter says schemas "agreed at wave start" must be written down, not assumed. The contracts doc names the renderer's emission target (§2.8) and inputs (§6/§7) but does not state (a) how dispatch persists the rendered template before calling `CreateRun`, (b) what params the renderer's `template: true` config declares, (c) the concrete `budget.TicketBudgets` implementation dispatch owns, or (d) the `concourse/ticket` label on the ephemeral secret (credentials-and-budgets' §8.2 note says "ticket label is dispatch's job"). Freeze all four now.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` (append a `### 2.8.2` subsection after §2.8.1; append to the §11 Amendment log at the end of the file)

**Steps:**

- [ ] **Step 1: Insert the renderer-persistence subsection** immediately after §2.8.1's closing fenced block (before `## 3.`):

````markdown
### 2.8.2 Renderer output + dispatch persistence — owner: **dispatch** (addendum, 2026-07-08; consumers: process-intel-experiments)

The renderer (`agent/dispatch.Render`) is a pure function `Render(RenderInput) (RenderOutput, error)` with no DB access. `RenderInput` carries the resolved `workflow.Config`, the `tickets.Ticket`, its latest `*tickets.Spec`, and active `[]tickets.Task`, plus the resolved image flags (`AgentStepImage`, and the per-sidecar images already inline in the workflow's `sidecars` map) and `ATCExternalURL`. `RenderOutput` carries the `atc.Config` (a `template: true` pipeline with exactly one job named `run`), the materialized run-input files (`map[string]string` of `spec.md`/`plan.md` → contents, produced via `tickets.RenderSpecMarkdown`/`RenderPlanMarkdown`, populated ONLY when `spec_delivery: files`), and the `params map[string]any` the dispatcher passes to `CreateRun`. `RenderInput` still carries Ticket + Spec + Tasks in both delivery modes — they are needed to materialize the files in `files` mode and to fill params.

- **Template shape:** one job `name: run`; its plan is the ordered `agent:`/checkpoint steps from the definition followed by the implicitly-appended terminal `harvest:` step (§6.1: harvest is never declared in `steps`). The job is an entry job (no `passed:`), so `CreateRun` auto-triggers it (§7.1 point 8).
- **Checkpoint steps render as `task:`, not `agent:` (F1, 2026-07-09; REWRITTEN 2026-07-09 per the frozen checkpoint seam delta — resolves F14/F15/F16/F28/F29/F36):** each `workflow.Step{Checkpoint:...}` becomes a bare `atc.TaskStep` named `checkpoint-<name>` — identical for both `on_reject` values, never wrapped in try/on_failure/ensure — whose `run` invokes `platform-mcp checkpoint --name <name>` resolved on PATH (the platform-mcp image installs the binary at `/usr/local/bin/platform-mcp` and is **shell-bearing** — alpine base with POSIX `sh` + `tail`/`mv`/`cat`/`sleep`/`mkdir`/`kill` — because it doubles as the task MAIN image under jetbridge's sh supervisor; F28, delta §8). `Config.ImageResource.Source` is built by the renderer helper `splitImageRef(image) (repo, tag)` and emits `{repository, tag}` with the tag **split out**; digest refs (`@sha256:`) pass whole into `repository` with no tag key — never a tag inside `repository` (the native resolver appends `:latest` unconditionally, so `repository: …:v1` is a fatal `name.ParseReference` error; F29). Task params carry `ATC_EXTERNAL_URL` / `AGENT_TICKET_ID` / `AGENT_PIPELINE_RUN_ID: ((run_id))` / `AGENT_STEP_NAME: checkpoint-<name>` / `PLATFORM_MCP_URL=http://127.0.0.1:7781/mcp` and **NO `AGENT_PRINCIPAL_TOKEN`** — the client authenticates to nothing (the pod boundary is the auth boundary; the old `((principal-token))` param was an undefined var that failed interpolation on every run; F16). Exactly one inline `platform` sidecar is mounted (Name `"platform"` — the fixed role key), carrying the same four identity rows as literal env plus `AGENT_PRINCIPAL_TOKEN` via `ValueFrom.SecretKeyRef {name: "agent-run-((run_id))", key: "principal-token"}` (F15, Task 4b seam) — the SIDECAR is the trust boundary: its `POST /checkpoint` handler files the `kind='checkpoint'` `agent_run_questions` row via the ATC API, long-polls until answered, and emits the §5 `checkpoint.wait`/`checkpoint.release` events; the client only POSTs it over pod-local loopback and exits 0 (approve) / 1 (reject or error) / 2 (usage). **Render-time guard (F36):** a checkpoint step in a workflow whose `sidecars` map has no `platform` entry (or an empty image) makes `Render` return `checkpoint %q requires a "platform" sidecar in the workflow definition` — never a zero-value sidecar. No timeout is set (checkpoints always park). `on_reject: fail`/`send_back` both render this identical bare failing step; the mapping is applied post-hoc by the dispatcher's **run-completion reconciler** (delta §6, Task 11b) from the ticket's frozen workflow config — the client and sidecar never see it. No `PLATFORM_MCP_CHECKPOINT*` env or LLM prompt is emitted.
- **Params schema declared by the rendered template:** exactly the run-identity params the dispatcher fills — `ticket_id` (number, required), `pipeline_run_id` is NOT a param (it is the run's own id, injected at exec time from `AGENT_PIPELINE_RUN_ID`, which the renderer sets to the literal `((run_id))` reserved var — `pipeline_runs.id`, allocated before materialization per the F30 delta (2026-07-09) — at BOTH renderer sites: agent-step env and the checkpoint task's params + sidecar env; interpolation covers the whole pipeline config including secretKeyRef secret names). The reserved names `run` and `run_id` (§7.1) are never declared. Rationale: keeping the template's declared params minimal avoids params-schema validation coupling; ticket/workflow identity travels as literal step `env`, not as params, per the render-time-resolution rule (§2.8).
- **Read model (default = MCP):** by default (`spec_delivery: mcp` or omitted) the renderer injects NO spec/plan bytes into any agent step. There is no `AGENT_SPEC_MD`/`AGENT_PLAN_MD` env key — those keys are DELETED from the emission target. The agent reaches spec/plan exclusively through the platform-mcp read tools (`read_ticket`, `list_tasks`, `get_task`; contracts §3.2, implemented by 08-platform-mcp-hitl over the ticket-core `Store` methods `Get`/`LatestSpec`/`ActivePlan`), loading only what it needs and working the plan through task handles. The DB stays the single source of truth; nothing is flattened into a monolithic blob. The workflow's first agent step prompt instructs the agent to begin by calling `read_ticket` and `list_tasks` (and `get_task` per task as it works) — a one-line seed-prompt convention.
- **spec.md / plan.md as run inputs (opt-in `spec_delivery: files`):** when the resolved `workflow.Config.SpecDelivery == "files"`, the renderer materializes read-only `spec.md`/`plan.md` (contents produced via `tickets.RenderSpecMarkdown`/`RenderPlanMarkdown`, which are KEPT) into `RenderOutput.RunInputs` (filename→contents). The dispatcher mounts them READ-ONLY as an artifact named `ticket` on the agent steps — a file mount, NOT env vars. In `mcp` mode `RenderOutput.RunInputs` is empty. In BOTH modes the platform-mcp sidecar is mounted and all its tools work; `files` mode is purely additive (a read-only mount) and does not disable the MCP read path. `SpecDelivery` is read off the resolved `workflow.Config` (grammar owned by 05-workflow-store; contracts §6): yaml key `spec_delivery`, Go field `SpecDelivery string`, values `"mcp"` (default when empty) | `"files"`, a normal hashed field.
- **Persistence:** the dispatcher persists the rendered `atc.Config` via `db.Team.SavePipeline(atc.PipelineRef{Name: "agent-ticket-<id>"}, config, 0, false)` to obtain the base template pipeline id, then calls `db.PipelineRunFactory.CreateRun(templateID, params, "dispatcher")`. One template pipeline per ticket (name `agent-ticket-<ticket-id>`); re-dispatch after send-back re-saves (bumping `ConfigVersion`) and creates a new run number under the same template.
- **`budget.TicketBudgets` implementation:** dispatch supplies `dispatch.TicketBudgets` resolving `tickets.budget_usd ?? workflow.Config.Budget.TicketUSD` via `tickets.Store.Get` + the resolved live `workflow.Definition` — this is the real implementation the wave-1 `budget.NoTicketBudgets` stub stood in for.
- **Ephemeral secret ticket label:** credentials-and-budgets' §8.2 left the `concourse/ticket` label to dispatch (the `SecretAttacher.Attach` seam has no ticket parameter and labels only `concourse/agent-run`). Dispatch owns a `dispatch.RunSecretLabeler` seam (K8s impl `dispatch.K8sRunSecretLabeler` over `kubernetes.Interface`) and, after `Attach` succeeds, does a follow-up strategic-merge `Patch` adding `concourse/ticket: "<ticket-id>"` when `ticket_id > 0`. This label is for operator filtering only — the reaper's safety-net GC keys off `concourse/agent-run` alone — so a labeling failure is logged, never fatal to a dispatched run.
- **Per-run principal:** the dispatcher mints a per-run `agent_principals` row named `agent-run-<run-id>` with scopes `[tickets:read, tickets:write, metrics:write, costs:write, questions:answer]` and `expires_at = now + --agent-run-timeout` (6h default) — EXCEPT when the rendered workflow contains any park-policy step (a checkpoint step, which always parks per the checkpoint seam delta, or `hitl.ask_timeout: park`), in which case `expires_at = now + --agent-park-timeout` (web flag, `time.Duration`, default `72h`, defined beside `--agent-run-timeout` in `atc/atccmd/command.go`; F31 principal-expiry leg, §8.1 `AGENT_PRINCIPAL_TOKEN` row / PARK-DURATION BOUNDS §3.2). Expiry is NOT NULL in either case — it stays the hard backstop, and a parked question outliving it fails loudly per the AwaitAnswer fatal-auth contract (plan 08). Minted via `principals.Store.Create`; the returned raw token becomes the secret's `principal-token` key. Revoked by the run-lifecycle cleanup path (best-effort; expiry is the hard backstop).
````

- [ ] **Step 2: Append to the §11 Amendment log** at the end of the file:

```markdown
- 2026-07-08 (dispatch wave-4 planning; affects: process-intel-experiments, credentials-and-budgets, ticket-core, pipeline-runs — additive only): added §2.8.2 (Renderer pure-function shape `Render(RenderInput)(RenderOutput,error)`; rendered template is a single `template: true` pipeline named `agent-ticket-<id>` with one entry job `run` = agent/checkpoint steps + implicit terminal harvest; declared params minimal (`ticket_id`); ticket/workflow identity travels as literal step env not params; persistence via `Team.SavePipeline` then `PipelineRunFactory.CreateRun`; dispatch-owned `budget.TicketBudgets` implementation `dispatch.TicketBudgets` (tickets.budget_usd ?? workflow default); `concourse/ticket` secret label applied by dispatch via a `dispatch.RunSecretLabeler` follow-up Patch after Attach (best-effort; GC keys off `concourse/agent-run` alone); per-run principal `agent-run-<run-id>` scopes + expiry). No existing rows changed.
- 2026-07-08 (frozen delta — spec/plan via granular platform-mcp read tools + optional file mount; affects: platform-mcp-hitl, dispatch, workflow-store, ticket-core-consumers — supersedes the prior "inline env copy under `AGENT_SPEC_MD`/`AGENT_PLAN_MD`" design in §2.8.2): the default read model is MCP — agents reach spec/plan ONLY via platform-mcp read tools (`read_ticket`, `list_tasks`, `get_task`; §3.2) and no spec/plan bytes are injected into any agent step by default. The `AGENT_SPEC_MD`/`AGENT_PLAN_MD` env keys are DELETED from the renderer emission target. New optional workflow-definition field `spec_delivery` (Go `workflow.Config.SpecDelivery string`, values `"mcp"` default | `"files"`, a normal hashed field; 05-workflow-store owns the grammar, §6 mirrors it): `files` mode materializes read-only `spec.md`/`plan.md` (via `tickets.RenderSpecMarkdown`/`RenderPlanMarkdown`, KEPT) into `RenderOutput.RunInputs`, mounted READ-ONLY as an artifact named `ticket` on the agent steps — a mount, NOT env vars; `mcp` mode leaves `RunInputs` empty. In both modes the platform-mcp sidecar is mounted. `read_ticket` returns envelope+spec only (tasks removed); `list_tasks`/`get_task` provide the task skeleton/detail; `update_task_status` is the write-back in both modes. No existing rows changed.
- 2026-07-09 (checkpoint seam — FROZEN normative delta; SUPERSEDES the F1 amendment of 2026-07-09; resolves F14, F15 (checkpoint leg), F16, F17, F28, F29, F36; co-signed dispatch + platform-mcp-hitl + ticket-core + shared contracts, noted to harvest-step (two-writers) and agent-step (shared sidecar-env seam)): ONE mechanism, ONE wire model — the plans' sidecar-POST model is ADOPTED; contracts §3.2's earlier client→ATC wording ("client inserts the row", "long-polls the ATC route", "reads reject-policy from argv", "NOT a call to a sidecar internal checkpoint endpoint") is RETRACTED. (1) WIRE MODEL (F14): the checkpoint CLIENT is a deterministic CLI talking ONLY to the pod-local platform sidecar over loopback (`POST /checkpoint`, endpoint derived by trimming `/mcp` from `PLATFORM_MCP_URL`); the SIDECAR is the trust boundary — it alone holds `AGENT_PRINCIPAL_TOKEN`, inserts the `agent_run_questions` row, fires the §8.4 notification, long-polls `GET /api/v1/agent/tickets/:id/questions/:qid`, and emits the §5 `checkpoint.wait`/`checkpoint.release` flight-recorder rows. Client exit codes: 0 approved, 1 rejected/non-200/bad-response/retries-exhausted, 2 usage error (missing `--name` or `PLATFORM_MCP_URL`). (2) RENDERED STEP: bare `atc.TaskStep` `checkpoint-<name>` (never wrapped); params = `ATC_EXTERNAL_URL`, `AGENT_TICKET_ID`, `AGENT_PIPELINE_RUN_ID: ((run_id))` (the F30 reserved var), `AGENT_STEP_NAME`, `PLATFORM_MCP_URL` — `AGENT_PRINCIPAL_TOKEN: ((principal-token))` is DELETED (F16: undefined var, failed interpolation; the client needs no auth); `Config.ImageResource.Source` emits `{repository, tag}` split by `splitImageRef` (digest refs pass whole; F29); exactly one inline `platform` sidecar carrying the four literal env rows + `AGENT_PRINCIPAL_TOKEN` via `ValueFrom.SecretKeyRef {agent-run-((run_id)), principal-token}` (F15, via the new atc.SidecarEnvVar ValueFrom seam gated by `--kubernetes-sidecar-secret-prefixes`); render-time guard errors when the workflow lacks a `platform` sidecar (F36). (3) IMAGE (F28): the platform-mcp image is BOTH sidecar and checkpoint task MAIN image, so it must be shell-bearing (alpine + POSIX `sh`/`tail`/`mv`/`cat`/`sleep`/`mkdir`/`kill` per jetbridge's pause command + sh supervisor); distroless bases are sidecar-only (§8.5 note). (4) TICKET WALKER (F17): dispatch's run-completion reconciler (Task 11b) walks `running` tickets whose run completed — latest answered checkpoint with `answer <> 'approve'` + `on_reject: send_back` ⇒ `running→queued` (attempt_count++ per §2.1, capped by the dispatch MaxAttempts guard); `fail`/empty/unknown-step ⇒ `running→needs_review`; unanswered checkpoint rows ⇒ `running→errored` (`checkpoint "<name>" unresolved: run completed <status> while parked`) with orphans released via `Answer(id, "", "dispatcher")`; run succeeded but ticket still running ⇒ `needs_review` safety net (harvest is the primary writer; two-writers recorded). `running→queued` needs NO ticket-core matrix change — only the semantic broadening of the edge annotation. No existing rows changed.
```

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md
git commit -m "docs(dispatch): wave-4 contract addendum - renderer output + dispatch persistence seams" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: `agent/dispatch` package skeleton + `RenderInput`/`RenderOutput` types

The renderer is a pure library (no DB, no k8s) so it can be unit-tested with plain `testing` and reused by process-intel-experiments. Define its input/output types first, referencing only already-landed types from `atc`, `agent/workflow`, and `agent/api/tickets`.

**Files:**
- Create: `agent/dispatch/render_types.go`
- Test: `agent/dispatch/render_types_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/dispatch/render_types_test.go`:

```go
package dispatch_test

import (
	"testing"

	"github.com/concourse/concourse/agent/dispatch"
)

func TestRenderInputZeroValueValidates(t *testing.T) {
	in := dispatch.RenderInput{}
	if err := in.Validate(); err == nil {
		t.Fatal("empty RenderInput must be invalid (no workflow, no ticket)")
	}
}

func TestRenderInputMinimalValid(t *testing.T) {
	in := dispatch.RenderInput{
		AgentStepImage: "ghcr.io/tdmtrader/agent-runner:v1",
		ATCExternalURL: "https://concourse.home",
	}
	in.Ticket.ID = 42
	in.Ticket.Repo = "tdmtrader/concourse"
	in.Workflow.Name = "standard-dev"
	in.Workflow.Steps = append(in.Workflow.Steps, workflowAgentStep())
	if err := in.Validate(); err != nil {
		t.Fatalf("minimal valid input rejected: %v", err)
	}
}
```

- [ ] **Step 2: Write the shared test helper** in the same file (used by later tasks too):

```go
import "github.com/concourse/concourse/agent/workflow"

func workflowAgentStep() workflow.Step {
	return workflow.Step{Agent: "write-spec", Prompt: "spec", Outputs: []string{"workspace"}}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/`
Expected: FAIL — `no required module provides package github.com/concourse/concourse/agent/dispatch`.

- [ ] **Step 4: Add the render-time `Version` pass-through to `workflow.Config`.** The renderer stamps `AGENT_WORKFLOW_VERSION` (§8.1) from `RenderInput.Workflow.Version`, but `workflow.Config` from workflow-store carries no `Version` field (version lives on `Definition`, §2.2 / 05-workflow-store.md). Add the field here — the very first task that imports `workflow` — so every later renderer task (Task 3 onward) compiles when it reads `in.Workflow.Version`. Verify absence first:

Run: `cd /Users/tdmtrader/concourse/concourse && grep -n "Version" agent/workflow/definition.go`

If `Config` lacks a `Version` field, add one after `Name` in `agent/workflow/definition.go`'s `Config` struct:

```go
	Version int `yaml:"-" json:"-"` // render-time pass-through; NOT part of the hashed YAML (hash covers RawYAML, not the marshaled struct)
```

It is excluded from YAML/JSON so it never affects the content hash or wire format. Confirm clean: `go build ./agent/workflow/`. If workflow-store already carries a version pass-through on `Config`, skip this step.

- [ ] **Step 5: Write `agent/dispatch/render_types.go`:**

```go
// Package dispatch renders workflow definitions into pipeline templates
// (Milestone 1) and dispatches queued tickets into pipeline runs
// (Milestone 2). Per contracts §2.8/§2.8.2, the renderer is a pure
// function: it resolves everything from the workflow definition into
// literal step config so a rendered pipeline is self-contained and never
// reads agent_workflow_definitions at run time.
package dispatch

import (
	"fmt"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

// RenderInput is everything the renderer needs, already resolved by the
// dispatcher (or a hand-caller). The renderer performs NO I/O.
type RenderInput struct {
	Workflow       workflow.Config // parsed, validated definition (§6)
	Ticket         tickets.Ticket  // §2.1
	Spec           *tickets.Spec   // latest spec, or nil
	Tasks          []tickets.Task  // active plan, or empty
	AgentStepImage string          // --agent-step-image value; empty is an error
	ATCExternalURL string          // ATC base URL for ATC_EXTERNAL_URL env
}

// RenderOutput is the self-contained result. Config is a template: true
// pipeline with exactly one entry job "run". RunInputs carries the ticket
// spec/plan markdown ONLY when the workflow opts into files delivery
// (workflow.Config.SpecDelivery == "files"); the dispatcher mounts it
// read-only as an artifact named "ticket" on the agent steps (§2.8.2). In
// the default "mcp" mode RunInputs is empty and no spec/plan bytes are
// injected — the agent reaches spec/plan via the platform-mcp read tools
// (read_ticket / list_tasks / get_task). Params is passed verbatim to
// PipelineRunFactory.CreateRun.
type RenderOutput struct {
	Config    atc.Config
	RunInputs map[string]string // filename -> contents (spec.md, plan.md); empty in mcp mode
	Params    map[string]any    // {"ticket_id": <id>}
}

// Validate checks the minimum a render needs. It does NOT re-validate the
// workflow grammar (workflow.Parse already did that at import time).
func (in RenderInput) Validate() error {
	if in.Workflow.Name == "" {
		return fmt.Errorf("render: workflow definition has no name")
	}
	if len(in.Workflow.Steps) == 0 {
		return fmt.Errorf("render: workflow %q has no steps", in.Workflow.Name)
	}
	if in.Ticket.ID == 0 {
		return fmt.Errorf("render: ticket id is zero")
	}
	if in.Ticket.Repo == "" {
		return fmt.Errorf("render: ticket %d has no repo", in.Ticket.ID)
	}
	if in.AgentStepImage == "" {
		return fmt.Errorf("render: agent step image is unset (--agent-step-image); cannot render agent steps")
	}
	return nil
}
```

- [ ] **Step 6: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add agent/dispatch/render_types.go agent/dispatch/render_types_test.go agent/workflow/definition.go
git commit -m "feat(dispatch): agent/dispatch package + RenderInput/RenderOutput types (+ workflow.Config.Version pass-through)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Render one `agent:` step from a `workflow.Step`

The core of the render-time-resolution rule: a `workflow.Step{Agent:...}` becomes an `atc.AgentStep` with the prompt text inlined from `workflow.Config.Prompts`, sidecars resolved to inline `atc.SidecarConfig` (image + role-name so agent-step's exec derives the MCP URL by name), budget slice + model + max-turns resolved with `Defaults` fallback, and the §8.1 identity/provenance env baked in as literal values. *(Amended 2026-07-09, F30 delta: `AGENT_PIPELINE_RUN_ID` is the `((run_id))` reserved var — `pipeline_runs.id`, allocated pre-materialization by `CreateRun` — NOT the per-template `((run))` number, whose values collide across tickets. The renderer injects `((run_id))` at both sites: here and in `renderCheckpointStep`.)*

**Files:**
- Create: `agent/dispatch/render_agent.go`
- Test: `agent/dispatch/render_agent_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/dispatch/render_agent_test.go`:

```go
package dispatch_test

import (
	"testing"

	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
)

func TestRenderAgentStepResolvesInlinePromptAndSidecars(t *testing.T) {
	cfg := workflow.Config{
		Name:     "standard-dev",
		Defaults: workflow.Defaults{Model: "claude-sonnet-4-5", MaxTurns: 80},
		Prompts:  map[string]string{"spec": "Read the ticket, submit a spec."},
		Sidecars: map[string]workflow.Sidecar{
			"dev":      {Image: "ghcr.io/tdmtrader/mcp-dev-concourse:v1", Role: "dev"},
			"platform": {Image: "ghcr.io/tdmtrader/mcp-platform:v1", Role: "platform"},
		},
	}
	step := workflow.Step{
		Agent: "write-spec", Prompt: "spec",
		Sidecars: []string{"dev", "platform"}, BudgetSliceUSD: 2.0,
		Outputs: []string{"workspace"},
	}
	in := dispatch.RenderInput{Workflow: cfg, AgentStepImage: "ghcr.io/tdmtrader/agent-runner:v1", ATCExternalURL: "https://concourse.home"}
	in.Ticket.ID = 42
	in.Ticket.Repo = "tdmtrader/concourse"
	in.Workflow.Version = 3
	in.WorkflowHashForTest = "abc123"

	got, err := dispatch.RenderAgentStep(in, step)
	if err != nil {
		t.Fatalf("RenderAgentStep: %v", err)
	}
	if got.Name != "write-spec" {
		t.Errorf("name = %q, want write-spec", got.Name)
	}
	if got.Prompt != "Read the ticket, submit a spec." {
		t.Errorf("prompt not inlined, got %q", got.Prompt)
	}
	if got.Model != "claude-sonnet-4-5" || got.MaxTurns != 80 {
		t.Errorf("defaults not applied: model=%q turns=%d", got.Model, got.MaxTurns)
	}
	if got.BudgetSliceUSD != 2.0 {
		t.Errorf("budget slice = %v, want 2.0", got.BudgetSliceUSD)
	}
	if len(got.Sidecars) != 2 || got.Sidecars[0].Config == nil {
		t.Fatalf("sidecars not resolved to inline configs: %+v", got.Sidecars)
	}
	if got.Sidecars[0].Config.Name != "dev" || got.Sidecars[0].Config.Image != "ghcr.io/tdmtrader/mcp-dev-concourse:v1" {
		t.Errorf("dev sidecar wrong: %+v", got.Sidecars[0].Config)
	}
	if got.Env["AGENT_TICKET_ID"] != "42" || got.Env["AGENT_WORKFLOW_NAME"] != "standard-dev" ||
		got.Env["AGENT_WORKFLOW_VERSION"] != "3" || got.Env["AGENT_WORKFLOW_HASH"] != "abc123" ||
		got.Env["AGENT_BUDGET_SLICE_USD"] != "2" || got.Env["ATC_EXTERNAL_URL"] != "https://concourse.home" {
		t.Errorf("identity/provenance env wrong: %+v", got.Env)
	}
	if got.Env["AGENT_PIPELINE_RUN_ID"] != "((run_id))" {
		t.Errorf("pipeline run id must be the ((run_id)) reserved var (pipeline_runs.id, F30), got %q", got.Env["AGENT_PIPELINE_RUN_ID"])
	}
	if len(got.Outputs) != 1 || got.Outputs[0] != "workspace" {
		t.Errorf("outputs wrong: %+v", got.Outputs)
	}
}

func TestRenderAgentStepMissingPromptKey(t *testing.T) {
	in := dispatch.RenderInput{Workflow: workflow.Config{Name: "w", Prompts: map[string]string{}}, AgentStepImage: "img:v1"}
	in.Ticket.ID = 1
	in.Ticket.Repo = "r/x"
	_, err := dispatch.RenderAgentStep(in, workflow.Step{Agent: "a", Prompt: "nope"})
	if err == nil {
		t.Fatal("missing prompt key must error")
	}
}
```

- [ ] **Step 2: Add the test-only hash carrier to `RenderInput`.** In `agent/dispatch/render_types.go`, add a field after `ATCExternalURL`:

```go
	// WorkflowHashForTest lets tests and the dispatcher supply the content
	// hash frozen at render time; the dispatcher sets it from the resolved
	// workflow.Definition.ContentHash. Named for clarity that it is a pass-
	// through provenance tag, not something the renderer computes.
	WorkflowHashForTest string
```

- [ ] **Step 3: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestRenderAgentStep`
Expected: FAIL — `undefined: dispatch.RenderAgentStep`.

- [ ] **Step 4: Write `agent/dispatch/render_agent.go`:**

```go
package dispatch

import (
	"fmt"
	"strconv"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

// RenderAgentStep resolves one workflow agent step into a self-contained
// atc.AgentStep (contracts §2.8, §8.1, §8.1 agent-step addendum). All
// workflow-table references (prompt key, sidecar names, defaults) are
// resolved to literal values here — the step never reads workflow tables.
func RenderAgentStep(in RenderInput, s workflow.Step) (atc.AgentStep, error) {
	prompt, ok := in.Workflow.Prompts[s.Prompt]
	if !ok {
		return atc.AgentStep{}, fmt.Errorf("agent step %q references unknown prompt %q", s.Agent, s.Prompt)
	}

	model := s.Model
	if model == "" {
		model = in.Workflow.Defaults.Model
	}
	maxTurns := s.MaxTurns
	if maxTurns == 0 {
		maxTurns = in.Workflow.Defaults.MaxTurns
	}

	sidecars, err := renderSidecars(in.Workflow, s.Sidecars)
	if err != nil {
		return atc.AgentStep{}, fmt.Errorf("agent step %q: %w", s.Agent, err)
	}

	// §8.1 identity + provenance env; AGENT_PIPELINE_RUN_ID is the ((run_id))
	// reserved var — pipeline_runs.id, allocated pre-materialization inside
	// the CreateRun tx (F30 delta, 2026-07-09) — resolved when the instanced
	// config is interpolated. NOT ((run)): that is the per-template NUMBER,
	// which resets per template and collides across tickets.
	env := map[string]string{
		"ATC_EXTERNAL_URL":       in.ATCExternalURL,
		"AGENT_TICKET_ID":        strconv.Itoa(in.Ticket.ID),
		"AGENT_PIPELINE_RUN_ID":  "((run_id))",
		"AGENT_STEP_NAME":        s.Agent,
		"AGENT_WORKFLOW_NAME":    in.Workflow.Name,
		"AGENT_WORKFLOW_VERSION": strconv.Itoa(in.Workflow.Version),
		"AGENT_WORKFLOW_HASH":    in.WorkflowHashForTest,
		"AGENT_BUDGET_SLICE_USD": strconv.FormatFloat(s.BudgetSliceUSD, 'f', -1, 64),
	}

	return atc.AgentStep{
		Name:           s.Agent,
		Prompt:         prompt,
		Model:          model,
		MaxTurns:       maxTurns,
		BudgetSliceUSD: s.BudgetSliceUSD,
		OutputSchema:   in.Workflow.Schemas[s.OutputSchema], // "" when unset
		Sidecars:       sidecars,
		Inputs:         append([]string(nil), s.Inputs...),
		Outputs:        append([]string(nil), s.Outputs...),
		Env:            env,
	}, nil
}

// renderSidecars maps workflow sidecar names to inline atc.SidecarConfig.
// The SidecarConfig.Name is set to the sidecar's role (dev/platform/gateway)
// so the agent-step exec derives DEV_MCP_URL/PLATFORM_MCP_URL/GATEWAY_MCP_URL
// by name (§8.1 MCP-URL-by-sidecar-name derivation).
func renderSidecars(cfg workflow.Config, names []string) ([]atc.SidecarSource, error) {
	out := make([]atc.SidecarSource, 0, len(names))
	for _, name := range names {
		def, ok := cfg.Sidecars[name]
		if !ok {
			return nil, fmt.Errorf("references unknown sidecar %q", name)
		}
		out = append(out, atc.SidecarSource{
			Config: &atc.SidecarConfig{
				Name:  def.Role, // role is the discovery name for MCP URL derivation
				Image: def.Image,
			},
		})
	}
	return out, nil
}
```

- [ ] **Step 5: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestRenderAgentStep`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/dispatch/render_agent.go agent/dispatch/render_agent_test.go agent/dispatch/render_types.go
git commit -m "feat(dispatch): render workflow agent step to self-contained atc.AgentStep" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Render the terminal `harvest:` step

The harvest step is never declared in `steps` (§6.1); the renderer appends it as the terminal step, resolving `workflow.Config.GatePolicy`/`Judge`/`Budget.JudgeUSD` into an `atc.HarvestStep` per §2.8.1. It reuses the definition's `dev` sidecar as `DevMCP` (harvest runs gates through the repo's dev-mcp).

**Files:**
- Create: `agent/dispatch/render_harvest.go`
- Test: `agent/dispatch/render_harvest_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/dispatch/render_harvest_test.go`:

```go
package dispatch_test

import (
	"testing"

	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
)

func fullWorkflow() workflow.Config {
	return workflow.Config{
		Name:   "standard-dev",
		Budget: workflow.Budget{TicketUSD: 15, JudgeUSD: 1.0},
		Sidecars: map[string]workflow.Sidecar{
			"dev": {Image: "ghcr.io/tdmtrader/mcp-dev-concourse:v1", Role: "dev"},
			// "platform" is required by the checkpoint render-time guard (F36,
			// 2026-07-09) — every render test that declares a checkpoint step
			// builds on this fixture; it matches the Task 2 fixture and the
			// Task 6 golden.
			"platform": {Image: "ghcr.io/tdmtrader/mcp-platform:v1", Role: "platform"},
		},
		GatePolicy: workflow.GatePolicy{
			Gates: []workflow.Gate{
				{Gate: "build", Scope: "affected"},
				{Gate: "test", Scope: "affected_then_full", Timeout: "45m", Retries: 1},
			},
			OnGateFailure: "needs_review",
		},
		Judge: &workflow.Judge{
			Rubric: []workflow.RubricDimension{
				{Name: "correctness", Weight: 3, Guidance: "Does it meet acceptance criteria?"},
			},
			PassThreshold: 6.5,
		},
	}
}

func TestRenderHarvestStep(t *testing.T) {
	in := dispatch.RenderInput{Workflow: fullWorkflow(), AgentStepImage: "img:v1"}
	in.Ticket.ID = 42
	in.Ticket.Repo = "tdmtrader/concourse"
	in.Ticket.TargetBranch = "main"

	h, err := dispatch.RenderHarvestStep(in)
	if err != nil {
		t.Fatalf("RenderHarvestStep: %v", err)
	}
	if h.Name != "harvest" || h.Repo != "tdmtrader/concourse" || h.TargetBranch != "main" {
		t.Errorf("harvest header wrong: %+v", h)
	}
	if h.TicketID != 42 || !h.Push || h.Branch != "agent/ticket-42" {
		t.Errorf("push/branch wrong: push=%v branch=%q ticket=%d", h.Push, h.Branch, h.TicketID)
	}
	if h.Workspace != "workspace" {
		t.Errorf("workspace input = %q, want workspace", h.Workspace)
	}
	if h.DevMCP == nil || h.DevMCP.Config == nil || h.DevMCP.Config.Image != "ghcr.io/tdmtrader/mcp-dev-concourse:v1" {
		t.Fatalf("dev_mcp not resolved: %+v", h.DevMCP)
	}
	if len(h.GatePolicy.Gates) != 2 || h.GatePolicy.Gates[1].Scope != "affected_then_full" || h.GatePolicy.Gates[1].Retries != 1 {
		t.Errorf("gate policy not copied (incl. retries): %+v", h.GatePolicy)
	}
	if h.Judge == nil || h.Judge.PassThreshold != 6.5 || h.Judge.BudgetUSD != 1.0 {
		t.Fatalf("judge not resolved (BudgetUSD from Budget.JudgeUSD): %+v", h.Judge)
	}
}

func TestRenderHarvestStepNoJudge(t *testing.T) {
	cfg := fullWorkflow()
	cfg.Judge = nil
	in := dispatch.RenderInput{Workflow: cfg, AgentStepImage: "img:v1"}
	in.Ticket.ID = 7
	in.Ticket.Repo = "r/x"
	h, err := dispatch.RenderHarvestStep(in)
	if err != nil {
		t.Fatalf("RenderHarvestStep: %v", err)
	}
	if h.Judge != nil {
		t.Errorf("no-judge workflow must produce nil judge, got %+v", h.Judge)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestRenderHarvestStep`
Expected: FAIL — `undefined: dispatch.RenderHarvestStep`.

- [ ] **Step 3: Write `agent/dispatch/render_harvest.go`:**

```go
package dispatch

import (
	"fmt"

	"github.com/concourse/concourse/agent/harvest"
	"github.com/concourse/concourse/atc"
)

// harvestWorkspaceInput is the conventional threaded artifact the agent
// steps output and the harvest step consumes (§6.1: "workspace" is the
// conventional threaded artifact name).
const harvestWorkspaceInput = "workspace"

// RenderHarvestStep builds the terminal harvest step from the workflow's
// gate-policy/judge slots (§2.8.1, §6.3, §6.4). Judge budget comes from the
// workflow's budget.judge_usd (§1.13, funded by the platform credential).
func RenderHarvestStep(in RenderInput) (atc.HarvestStep, error) {
	targetBranch := in.Ticket.TargetBranch
	if targetBranch == "" {
		targetBranch = "main"
	}

	var devMCP *atc.SidecarSource
	if len(in.Workflow.GatePolicy.Gates) > 0 {
		dev, ok := in.Workflow.Sidecars["dev"]
		if !ok {
			return atc.HarvestStep{}, fmt.Errorf("workflow declares gates but no 'dev' sidecar to run them through")
		}
		devMCP = &atc.SidecarSource{Config: &atc.SidecarConfig{Name: dev.Role, Image: dev.Image}}
	}

	gates := make([]harvest.Gate, 0, len(in.Workflow.GatePolicy.Gates))
	for _, g := range in.Workflow.GatePolicy.Gates {
		gates = append(gates, harvest.Gate{
			Gate:    g.Gate,
			Scope:   g.Scope,
			Focus:   g.Focus,
			Timeout: g.Timeout,
			Retries: g.Retries,
		})
	}
	onFail := in.Workflow.GatePolicy.OnGateFailure
	if onFail == "" {
		onFail = "needs_review"
	}

	var judge *harvest.JudgeConfig
	if in.Workflow.Judge != nil {
		dims := make([]harvest.RubricDimension, 0, len(in.Workflow.Judge.Rubric))
		for _, d := range in.Workflow.Judge.Rubric {
			dims = append(dims, harvest.RubricDimension{Name: d.Name, Weight: d.Weight, Guidance: d.Guidance})
		}
		judge = &harvest.JudgeConfig{
			Rubric:        dims,
			PassThreshold: in.Workflow.Judge.PassThreshold,
			BudgetUSD:     in.Workflow.Budget.JudgeUSD,
		}
	}

	return atc.HarvestStep{
		Name:         "harvest",
		Workspace:    harvestWorkspaceInput,
		Repo:         in.Ticket.Repo,
		TargetBranch: targetBranch,
		TicketID:     in.Ticket.ID,
		Branch:       fmt.Sprintf("agent/ticket-%d", in.Ticket.ID),
		Push:         true,
		DevMCP:       devMCP,
		GatePolicy:   harvest.GatePolicy{Gates: gates, OnGateFailure: onFail},
		Judge:        judge,
	}, nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestRenderHarvestStep`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/dispatch/render_harvest.go agent/dispatch/render_harvest_test.go
git commit -m "feat(dispatch): render terminal harvest step from workflow gate-policy/judge slots" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4b: Sidecar secretKeyRef env seam (jetbridge) — `atc.SidecarEnvVar.ValueFrom` + `--kubernetes-sidecar-secret-prefixes`

*(Added 2026-07-09 per the frozen checkpoint seam delta §3, F15. Co-signed with platform-mcp-hitl; reused by agent-step — 07's exec populates its §8.1 sidecar env programmatically THROUGH THIS SAME SEAM (literal Env + ValueFrom entries); no separate `runtime.ContainerSpec` sidecar-env maps are introduced.)*

The checkpoint's platform sidecar must receive `AGENT_PRINCIPAL_TOKEN` from the ephemeral run secret, but `atc.SidecarEnvVar` today carries only literal `Name`/`Value` pairs and jetbridge's `buildSidecarContainers` maps only literals — there is no secret-ref path for sidecars at all (F15: the rendered sidecar would get zero env and exit at startup). This task builds the seam ONCE at the runtime level, with a security gate: sidecar `secretKeyRef`s are rejected unless the secret name matches an operator-allowed prefix (default EMPTY = reject everything), keeping §8.2's secretKeyRef-only rule (token never in files/argv/DB) while closing the arbitrary-pipeline-YAML secret-exfiltration hole. Residual risk (same-worker pipelines referencing another run's `agent-run-*` secret) is accepted for v1 — per-run principal tokens are ticket-scoped and expire at run timeout (§8.1). Garden runtime is unchanged (it already rejects/ignores sidecars).

**Files:**
- Modify: `atc/sidecar.go` (`SidecarEnvVar.ValueFrom` + `SidecarEnvVarSource` + `SidecarSecretKeySelector` + `Validate` rules)
- Modify: `atc/sidecar_test.go` (Ginkgo, existing suite)
- Modify: `atc/worker/jetbridge/container.go` (`buildSidecarContainers` ValueFrom mapping + prefix gate)
- Modify: `atc/worker/jetbridge/config.go` (`Config.SidecarSecretPrefixes`)
- Modify: `atc/atccmd/command.go` (`--kubernetes-sidecar-secret-prefixes` flag, threaded into the jetbridge `Config`)
- Create: `atc/worker/jetbridge/sidecar_secret_env_test.go` (plain `testing`, in-package — jetbridge already mixes plain-Go tests with the Ginkgo suite)

**Steps:**

- [ ] **Step 1: Write the failing atc-side test.** In `atc/sidecar_test.go`, add to the existing `Describe("SidecarConfig")`:

```go
	Context("env valueFrom (secretKeyRef) — 2026-07-09 checkpoint seam delta §3", func() {
		It("parses a secretKeyRef env entry", func() {
			data := []byte(`
- name: platform
  image: ghcr.io/tdmtrader/mcp-platform:v1
  env:
  - name: AGENT_PRINCIPAL_TOKEN
    valueFrom:
      secretKeyRef:
        name: agent-run-((run_id))
        key: principal-token
`)
			configs, err := ParseSidecarConfigs(data)
			Expect(err).ToNot(HaveOccurred())
			Expect(configs).To(HaveLen(1))
			e := configs[0].Env[0]
			Expect(e.Name).To(Equal("AGENT_PRINCIPAL_TOKEN"))
			Expect(e.Value).To(BeEmpty())
			Expect(e.ValueFrom).ToNot(BeNil())
			Expect(e.ValueFrom.SecretKeyRef).ToNot(BeNil())
			Expect(e.ValueFrom.SecretKeyRef.Name).To(Equal("agent-run-((run_id))"))
			Expect(e.ValueFrom.SecretKeyRef.Key).To(Equal("principal-token"))
		})

		It("rejects value and valueFrom on the same entry", func() {
			sc := SidecarConfig{Name: "s", Image: "i", Env: []SidecarEnvVar{{
				Name: "X", Value: "literal",
				ValueFrom: &SidecarEnvVarSource{SecretKeyRef: &SidecarSecretKeySelector{Name: "n", Key: "k"}},
			}}}
			Expect(sc.Validate()).To(MatchError(ContainSubstring("cannot set both 'value' and 'valueFrom'")))
		})

		It("rejects a secretKeyRef missing name or key", func() {
			sc := SidecarConfig{Name: "s", Image: "i", Env: []SidecarEnvVar{{
				Name:      "X",
				ValueFrom: &SidecarEnvVarSource{SecretKeyRef: &SidecarSecretKeySelector{Name: "", Key: "k"}},
			}}}
			Expect(sc.Validate()).To(MatchError(ContainSubstring("requires both 'name' and 'key'")))
		})
	})
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./atc/ -run TestAtc 2>&1 | head -5`
Expected: FAIL — compile error, `SidecarEnvVarSource`/`SidecarSecretKeySelector` undefined (`ValueFrom` field does not exist).

- [ ] **Step 3: Extend `atc/sidecar.go`.** Replace the `SidecarEnvVar` type and add the two new types (mirroring corev1, consistent with `SidecarConfig`'s stated k8s-subset design):

```go
// SidecarEnvVar is an environment variable for a sidecar container,
// matching the Kubernetes EnvVar structure. Exactly one of Value or
// ValueFrom may be set (2026-07-09 checkpoint seam delta §3, F15).
type SidecarEnvVar struct {
	Name      string               `json:"name"`
	Value     string               `json:"value,omitempty"`
	ValueFrom *SidecarEnvVarSource `json:"valueFrom,omitempty"`
}

// SidecarEnvVarSource is the corev1.EnvVarSource subset sidecars support.
type SidecarEnvVarSource struct {
	SecretKeyRef *SidecarSecretKeySelector `json:"secretKeyRef,omitempty"`
}

// SidecarSecretKeySelector selects a key of a Secret in the pod's
// namespace. At pod build time jetbridge rejects any reference whose secret
// name does not match an allowed --kubernetes-sidecar-secret-prefixes prefix.
type SidecarSecretKeySelector struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}
```

And in `SidecarConfig.Validate()`, after the existing port loop:

```go
	for _, e := range sc.Env {
		if e.ValueFrom == nil {
			continue
		}
		if e.Value != "" {
			errors = append(errors, fmt.Sprintf("env %q: cannot set both 'value' and 'valueFrom'", e.Name))
		}
		if e.ValueFrom.SecretKeyRef == nil {
			errors = append(errors, fmt.Sprintf("env %q: 'valueFrom' supports only 'secretKeyRef'", e.Name))
		} else if e.ValueFrom.SecretKeyRef.Name == "" || e.ValueFrom.SecretKeyRef.Key == "" {
			errors = append(errors, fmt.Sprintf("env %q: 'secretKeyRef' requires both 'name' and 'key'", e.Name))
		}
	}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./atc/ 2>&1 | tail -3`
Expected: PASS.

- [ ] **Step 5: Write the failing jetbridge test** `atc/worker/jetbridge/sidecar_secret_env_test.go` (plain `testing`, in-package so it can call `buildSidecarContainers` directly):

```go
package jetbridge

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/atc"
)

func platformSidecarWithTokenRef() atc.SidecarConfig {
	return atc.SidecarConfig{
		Name:  "platform",
		Image: "ghcr.io/tdmtrader/mcp-platform:v1",
		Env: []atc.SidecarEnvVar{
			{Name: "AGENT_TICKET_ID", Value: "42"},
			{Name: "AGENT_PRINCIPAL_TOKEN", ValueFrom: &atc.SidecarEnvVarSource{
				SecretKeyRef: &atc.SidecarSecretKeySelector{Name: "agent-run-100", Key: "principal-token"},
			}},
		},
	}
}

func TestSidecarSecretEnvMapsToCoreV1ValueFrom(t *testing.T) {
	containers, err := buildSidecarContainers(
		[]atc.SidecarConfig{platformSidecarWithTokenRef()}, nil, "/w", []string{"agent-run-"})
	if err != nil {
		t.Fatalf("buildSidecarContainers: %v", err)
	}
	if len(containers) != 1 {
		t.Fatalf("want 1 container, got %d", len(containers))
	}
	env := containers[0].Env
	// Literal env first, secret refs appended after (delta §3).
	if env[0].Name != "AGENT_TICKET_ID" || env[0].Value != "42" {
		t.Errorf("literal env wrong: %+v", env[0])
	}
	last := env[len(env)-1]
	if last.Name != "AGENT_PRINCIPAL_TOKEN" || last.ValueFrom == nil || last.ValueFrom.SecretKeyRef == nil ||
		last.ValueFrom.SecretKeyRef.Name != "agent-run-100" || last.ValueFrom.SecretKeyRef.Key != "principal-token" {
		t.Errorf("secretKeyRef env not mapped to corev1 ValueFrom: %+v", last)
	}
}

func TestSidecarSecretEnvRejectedByDefault(t *testing.T) {
	// Default (empty allowlist) rejects EVERY sidecar secretKeyRef.
	_, err := buildSidecarContainers(
		[]atc.SidecarConfig{platformSidecarWithTokenRef()}, nil, "/w", nil)
	if err == nil {
		t.Fatal("empty --kubernetes-sidecar-secret-prefixes must reject sidecar secretKeyRefs")
	}
	want := `sidecar "platform": env "AGENT_PRINCIPAL_TOKEN" references secret "agent-run-100" outside allowed prefixes`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain the frozen text %q", err.Error(), want)
	}
}

func TestSidecarSecretEnvRejectedOutsidePrefix(t *testing.T) {
	sc := platformSidecarWithTokenRef()
	sc.Env[1].ValueFrom.SecretKeyRef.Name = "kube-root-ca"
	_, err := buildSidecarContainers([]atc.SidecarConfig{sc}, nil, "/w", []string{"agent-run-"})
	if err == nil {
		t.Fatal("secret outside the allowed prefixes must be rejected")
	}
}
```

- [ ] **Step 6: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./atc/worker/jetbridge/ -run TestSidecarSecretEnv 2>&1 | head -5`
Expected: FAIL — compile error (`buildSidecarContainers` takes 3 args and returns no error).

- [ ] **Step 7: Extend jetbridge.** In `atc/worker/jetbridge/config.go`, add to `Config` (after `ImagePullSecrets`):

```go
	// SidecarSecretPrefixes is the allowlist of K8s Secret-name prefixes a
	// sidecar env secretKeyRef may reference (--kubernetes-sidecar-secret-
	// prefixes). EMPTY (the default) rejects every sidecar secretKeyRef.
	// Agentic deployments set it to ["agent-run-"] so the platform sidecar
	// can mount the per-run principal token (checkpoint seam delta §3, F15).
	SidecarSecretPrefixes []string
```

In `atc/worker/jetbridge/container.go`, change `buildSidecarContainers` (container.go:510) to take the allowlist and return an error, and map `ValueFrom` after the literal env loop:

```go
func buildSidecarContainers(sidecars []atc.SidecarConfig, mainMounts []corev1.VolumeMount, defaultDir string, allowedSecretPrefixes []string) ([]corev1.Container, error) {
```

Replace the env loop body inside it:

```go
		for _, e := range sc.Env {
			if e.ValueFrom != nil {
				continue // secret refs are appended after literals below
			}
			c.Env = append(c.Env, corev1.EnvVar{Name: e.Name, Value: e.Value})
		}
		for _, e := range sc.Env {
			if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
				continue
			}
			ref := e.ValueFrom.SecretKeyRef
			// SECURITY GATE (delta §3): default-empty allowlist rejects every
			// sidecar secretKeyRef; only operator-allowed prefixes pass.
			if !secretNameAllowed(ref.Name, allowedSecretPrefixes) {
				return nil, fmt.Errorf("sidecar %q: env %q references secret %q outside allowed prefixes", sc.Name, e.Name, ref.Name)
			}
			c.Env = append(c.Env, corev1.EnvVar{
				Name: e.Name,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
						Key:                  ref.Key,
					},
				},
			})
		}
```

Change the final `return containers` to `return containers, nil`, and add the helper:

```go
// secretNameAllowed enforces --kubernetes-sidecar-secret-prefixes: an empty
// allowlist rejects every sidecar secretKeyRef (secure default).
func secretNameAllowed(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
```

Update the single call site (container.go:439) to propagate the error:

```go
	sidecarContainers, err := buildSidecarContainers(c.containerSpec.Sidecars, volumeMounts, dir, c.config.SidecarSecretPrefixes)
	if err != nil {
		return nil, err
	}
	containers = append(containers, sidecarContainers...)
```

- [ ] **Step 8: Add the web flag.** In `atc/atccmd/command.go`'s `Kubernetes` struct (after `ImagePullSecrets`, :175):

```go
		SidecarSecretPrefixes []string `long:"kubernetes-sidecar-secret-prefixes" env-delim:"," description:"Secret-name prefix a sidecar env secretKeyRef may reference (env CONCOURSE_KUBERNETES_SIDECAR_SECRET_PREFIXES, comma-separated). Empty (default) rejects every sidecar secretKeyRef. Agentic deployments set agent-run-. Can be specified multiple times."`
```

Thread it into the jetbridge `Config` literal wherever `cmd.Kubernetes.ImagePullSecrets` is already copied (grep `ImagePullSecrets:` in command.go for the construction site): `SidecarSecretPrefixes: cmd.Kubernetes.SidecarSecretPrefixes,`.

- [ ] **Step 9: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./atc/worker/jetbridge/ -run TestSidecarSecretEnv && go build ./atc/...`
Expected: PASS + clean build.

- [ ] **Step 10: Commit**

```bash
git add atc/sidecar.go atc/sidecar_test.go atc/worker/jetbridge/container.go atc/worker/jetbridge/config.go atc/worker/jetbridge/sidecar_secret_env_test.go atc/atccmd/command.go
git commit -m "feat(atc/jetbridge): sidecar env secretKeyRef ValueFrom, gated by --kubernetes-sidecar-secret-prefixes" -m "Checkpoint seam delta §3 (F15): one seam for checkpoint AND agent-step sidecar secret env; default-empty allowlist rejects all sidecar secret refs." -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: Assemble the full pipeline `atc.Config` (`Render`)

Combine the agent/checkpoint steps + terminal harvest into one `template: true` pipeline with a single entry job `run`. Checkpoint steps (`workflow.Step{Checkpoint:...}`) render to a `task:`-style checkpoint step that platform-mcp-hitl's rendered checkpoint recognizes (§3.2). *(Amended 2026-07-09 per the frozen checkpoint seam delta §2: `renderCheckpointStep` emits the delta shape verbatim — NO `AGENT_PRINCIPAL_TOKEN` param (F16), `((run_id))` at both env sites (F30), `ImageResource.Source` split into repository+tag via the new `splitImageRef` helper (F29), one inline `platform` sidecar with four literal env rows + the principal-token secretKeyRef ValueFrom (F15, Task 4b seam), and a render-time guard erroring when the workflow declares a checkpoint but no `platform` sidecar (F36).)* Per §2.8.2, the declared params are minimal (`ticket_id`). The default read model is MCP: NO spec/plan bytes are injected into any agent step and `RunInputs` is empty — the agent reaches spec/plan through the platform-mcp read tools (`read_ticket`/`list_tasks`/`get_task`). Only when the resolved `workflow.Config.SpecDelivery == "files"` does the renderer populate `RunInputs` with `spec.md`/`plan.md` (for the dispatcher to mount read-only as an artifact named `ticket`).

**Files:**
- Create: `agent/dispatch/render.go`
- Test: `agent/dispatch/render_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/dispatch/render_test.go`:

```go
package dispatch_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
)

func TestRenderProducesTemplatePipelineWithOneRunJob(t *testing.T) {
	cfg := fullWorkflow()
	cfg.Defaults = workflow.Defaults{Model: "claude-sonnet-4-5", MaxTurns: 80}
	cfg.Prompts = map[string]string{"spec": "s", "implement": "i"}
	cfg.Steps = []workflow.Step{
		{Agent: "write-spec", Prompt: "spec", Sidecars: []string{"dev"}, BudgetSliceUSD: 2, Outputs: []string{"workspace"}},
		{Checkpoint: "plan-approval", OnReject: "fail"},
		{Agent: "implement", Prompt: "implement", Sidecars: []string{"dev"}, BudgetSliceUSD: 10, Inputs: []string{"workspace"}, Outputs: []string{"workspace"}},
	}
	in := dispatch.RenderInput{Workflow: cfg, AgentStepImage: "img:v1", ATCExternalURL: "https://concourse.home"}
	in.Ticket = tickets.Ticket{ID: 42, Repo: "tdmtrader/concourse", TargetBranch: "main", Title: "Fix X"}
	in.Spec = &tickets.Spec{Title: "Fix X", Body: "do the thing"}
	in.Tasks = []tickets.Task{{Ordering: 1, Title: "step one", Status: tickets.TaskPending}}

	out, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !out.Config.Template {
		t.Error("rendered config must have Template: true")
	}
	if got := out.Params["ticket_id"]; got != 42 {
		t.Errorf("params ticket_id = %v, want 42", got)
	}
	if len(out.Config.Jobs) != 1 || out.Config.Jobs[0].Name != "run" {
		t.Fatalf("want one job named run, got %+v", out.Config.Jobs)
	}
	// 3 declared steps + 1 implicit harvest = 4 plan steps.
	if n := len(out.Config.Jobs[0].PlanSequence); n != 4 {
		t.Fatalf("plan should be 3 declared + terminal harvest = 4, got %d", n)
	}
	last := out.Config.Jobs[0].PlanSequence[3].Config
	if _, ok := last.(*atcHarvest); !ok {
		// resolved below in Step 2 helper
	}

	// The checkpoint (plan step index 1) MUST render as a task: step whose
	// container exit code natively drives success/failure — NOT an agent: step
	// (an AgentStep's hardwired agent-runner main process cannot run the
	// deterministic checkpoint client, so a rejected checkpoint would exit 0
	// and the run would proceed as if approved). Assert: it is a *atc.TaskStep;
	// its run invokes `platform-mcp checkpoint --name plan-approval`; it mounts
	// the platform sidecar; and it carries NEITHER stale PLATFORM_MCP_CHECKPOINT*
	// env var (those are deleted — the container's exit code is the signal).
	cp, ok := out.Config.Jobs[0].PlanSequence[1].Config.(*atcTask)
	if !ok {
		t.Fatalf("checkpoint step must be a task step, got %T", out.Config.Jobs[0].PlanSequence[1].Config)
	}
	if cp.Name != "checkpoint-plan-approval" {
		t.Errorf("checkpoint step name = %q, want checkpoint-plan-approval", cp.Name)
	}
	if cp.Config == nil || cp.Config.Run.Path != "platform-mcp" ||
		len(cp.Config.Run.Args) != 3 || cp.Config.Run.Args[0] != "checkpoint" ||
		cp.Config.Run.Args[1] != "--name" || cp.Config.Run.Args[2] != "plan-approval" {
		t.Fatalf("checkpoint run must be `platform-mcp checkpoint --name plan-approval`, got %+v", cp.Config.Run)
	}
	// F29: the image_resource source must carry the tag SPLIT out — never
	// "repository: …:v1" (the native resolver appends :latest unconditionally,
	// so a tag inside repository is a fatal name.ParseReference error).
	if cp.Config.ImageResource == nil || cp.Config.ImageResource.Type != "registry-image" ||
		cp.Config.ImageResource.Source["repository"] != "ghcr.io/tdmtrader/mcp-platform" ||
		cp.Config.ImageResource.Source["tag"] != "v1" {
		t.Fatalf("checkpoint image_resource must split repo/tag (F29), got %+v", cp.Config.ImageResource)
	}
	// F16: the client authenticates to NOTHING — the pod boundary is the auth
	// boundary. The old `AGENT_PRINCIPAL_TOKEN: ((principal-token))` param was
	// an undefined var that failed interpolation on every run.
	if _, present := cp.Params["AGENT_PRINCIPAL_TOKEN"]; present {
		t.Errorf("checkpoint main container must NOT carry AGENT_PRINCIPAL_TOKEN (F16)")
	}
	if cp.Params["AGENT_PIPELINE_RUN_ID"] != "((run_id))" {
		t.Errorf("checkpoint AGENT_PIPELINE_RUN_ID must be ((run_id)) (F30), got %q", cp.Params["AGENT_PIPELINE_RUN_ID"])
	}
	if cp.Params["PLATFORM_MCP_URL"] != "http://127.0.0.1:7781/mcp" {
		t.Errorf("client requires PLATFORM_MCP_URL (exit 2 if unset), got %q", cp.Params["PLATFORM_MCP_URL"])
	}
	// F15: exactly one inline platform sidecar — Name "platform" (the fixed
	// role key), the workflow's full image ref (sidecars are immune to F29:
	// jetbridge's own parseImageRef splits it), four literal env rows, and
	// AGENT_PRINCIPAL_TOKEN as a secretKeyRef ValueFrom (the sidecar is the
	// trust boundary; delivered via the Task 4b seam).
	if len(cp.Sidecars) != 1 || cp.Sidecars[0].Config == nil || cp.Sidecars[0].Config.Name != "platform" ||
		cp.Sidecars[0].Config.Image != "ghcr.io/tdmtrader/mcp-platform:v1" {
		t.Fatalf("checkpoint must mount the platform sidecar, got %+v", cp.Sidecars)
	}
	literals := map[string]string{}
	var tokenRef *atc.SidecarSecretKeySelector
	for _, e := range cp.Sidecars[0].Config.Env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			if e.Name == "AGENT_PRINCIPAL_TOKEN" {
				tokenRef = e.ValueFrom.SecretKeyRef
			}
			continue
		}
		literals[e.Name] = e.Value
	}
	if literals["ATC_EXTERNAL_URL"] != "https://concourse.home" || literals["AGENT_TICKET_ID"] != "42" ||
		literals["AGENT_PIPELINE_RUN_ID"] != "((run_id))" || literals["AGENT_STEP_NAME"] != "checkpoint-plan-approval" {
		t.Errorf("platform sidecar literal env rows wrong (F15): %+v", literals)
	}
	if tokenRef == nil || tokenRef.Name != "agent-run-((run_id))" || tokenRef.Key != "principal-token" {
		t.Fatalf("platform sidecar must carry AGENT_PRINCIPAL_TOKEN via secretKeyRef {agent-run-((run_id)), principal-token} (F15), got %+v", tokenRef)
	}
	if _, present := cp.Params["PLATFORM_MCP_CHECKPOINT"]; present {
		t.Errorf("stale PLATFORM_MCP_CHECKPOINT env must be deleted; the exit code is the signal")
	}
	if _, present := cp.Params["PLATFORM_MCP_CHECKPOINT_ON_REJECT"]; present {
		t.Errorf("stale PLATFORM_MCP_CHECKPOINT_ON_REJECT env must be deleted; on_reject is the dispatcher's refinement, not container env")
	}

	// §2.8.2 default read model = MCP: NO spec/plan bytes are injected. RunInputs
	// is empty and NO agent step carries AGENT_SPEC_MD/AGENT_PLAN_MD env — the
	// agent reaches spec/plan via the platform-mcp read tools (read_ticket /
	// list_tasks / get_task).
	if len(out.RunInputs) != 0 {
		t.Errorf("mcp (default) mode must leave RunInputs empty, got %+v", out.RunInputs)
	}
	first, ok := out.Config.Jobs[0].PlanSequence[0].Config.(*atcAgent)
	if !ok {
		t.Fatalf("first plan step is not an agent step: %T", out.Config.Jobs[0].PlanSequence[0].Config)
	}
	if _, present := first.Env["AGENT_SPEC_MD"]; present {
		t.Errorf("mcp mode must NOT inject AGENT_SPEC_MD env; got %q", first.Env["AGENT_SPEC_MD"])
	}
	if _, present := first.Env["AGENT_PLAN_MD"]; present {
		t.Errorf("mcp mode must NOT inject AGENT_PLAN_MD env; got %q", first.Env["AGENT_PLAN_MD"])
	}
	// No later agent step carries the copy either.
	if third, ok := out.Config.Jobs[0].PlanSequence[2].Config.(*atcAgent); ok {
		if _, present := third.Env["AGENT_SPEC_MD"]; present {
			t.Errorf("mcp mode injects no spec/plan env on any agent step")
		}
	}
}

func TestRenderFilesModeMaterializesRunInputs(t *testing.T) {
	cfg := fullWorkflow()
	cfg.SpecDelivery = "files" // opt in to the read-only spec.md/plan.md mount
	cfg.Defaults = workflow.Defaults{Model: "claude-sonnet-4-5", MaxTurns: 80}
	cfg.Prompts = map[string]string{"spec": "s"}
	cfg.Steps = []workflow.Step{
		{Agent: "write-spec", Prompt: "spec", Sidecars: []string{"dev"}, BudgetSliceUSD: 2, Outputs: []string{"workspace"}},
	}
	in := dispatch.RenderInput{Workflow: cfg, AgentStepImage: "img:v1", ATCExternalURL: "https://concourse.home"}
	in.Ticket = tickets.Ticket{ID: 42, Repo: "tdmtrader/concourse", TargetBranch: "main", Title: "Fix X"}
	in.Spec = &tickets.Spec{Title: "Fix X", Body: "do the thing"}
	in.Tasks = []tickets.Task{{Ordering: 1, Title: "step one", Status: tickets.TaskPending}}

	out, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// files mode materializes the read-only spec.md/plan.md the dispatcher mounts
	// as the "ticket" artifact. The bytes come from RenderSpecMarkdown/RenderPlanMarkdown.
	if out.RunInputs["spec.md"] == "" || out.RunInputs["plan.md"] == "" {
		t.Errorf("files mode must materialize spec.md/plan.md run inputs, got %+v", out.RunInputs)
	}
	// files mode STILL injects no spec/plan env — the file mount replaces env, and
	// the platform-mcp read tools remain available in both modes.
	first := out.Config.Jobs[0].PlanSequence[0].Config.(*atcAgent)
	if _, present := first.Env["AGENT_SPEC_MD"]; present {
		t.Errorf("files mode delivers spec via mount, never AGENT_SPEC_MD env; got %q", first.Env["AGENT_SPEC_MD"])
	}
}

// TestRenderCheckpointOnRejectFailFailsRun is the human-gate proof at render
// level: with on_reject: fail, the checkpoint renders as a BARE task step whose
// non-zero container exit propagates and fails the run. The client exits 1 on
// reject (§3.2), so the only way the gate holds is if nothing in the rendered
// plan swallows that exit — the step must NOT be wrapped in a `try:`, and must
// carry no on_success/on_failure/ensure hook that would mask the failure. If a
// future edit re-renders the checkpoint as an agent: step (main process
// hardwired to agent-runner, always exit 0), this assertion catches it: an
// agent: step cannot fail on reject, so the type assertion below fails.
func TestRenderCheckpointOnRejectFailFailsRun(t *testing.T) {
	cfg := fullWorkflow()
	cfg.Defaults = workflow.Defaults{Model: "claude-sonnet-4-5", MaxTurns: 80}
	cfg.Prompts = map[string]string{"spec": "s"}
	cfg.Steps = []workflow.Step{
		{Agent: "write-spec", Prompt: "spec", Sidecars: []string{"dev"}, BudgetSliceUSD: 2, Outputs: []string{"workspace"}},
		{Checkpoint: "plan-approval", OnReject: "fail"},
	}
	in := dispatch.RenderInput{Workflow: cfg, AgentStepImage: "img:v1", ATCExternalURL: "https://concourse.home"}
	in.Ticket = tickets.Ticket{ID: 42, Repo: "tdmtrader/concourse", TargetBranch: "main", Title: "Fix X"}

	out, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	step := out.Config.Jobs[0].PlanSequence[1]
	if step.Config == nil {
		t.Fatal("checkpoint plan step is empty")
	}
	// The checkpoint's StepConfig MUST be a bare *atc.TaskStep — the exit code
	// IS the run's success signal. atc.Step wraps failure-masking modifiers as
	// their own StepConfig types (TryStep, OnFailureStep, EnsureStep, …). If the
	// checkpoint were wrapped in any of those, or re-rendered as an *AgentStep
	// (main process hardwired to agent-runner => always exit 0), this direct
	// type assertion fails and the human gate is proven broken.
	switch step.Config.(type) {
	case *atc.TryStep, *atc.OnFailureStep, *atc.OnSuccessStep, *atc.EnsureStep:
		t.Fatalf("on_reject=fail checkpoint must not be wrapped in a failure-masking modifier; got %T", step.Config)
	}
	cp, ok := step.Config.(*atcTask)
	if !ok {
		t.Fatalf("on_reject=fail checkpoint must be a bare task step whose exit fails the run, got %T", step.Config)
	}
	// The client always exits 1 on reject; a task step's exit is unignored.
	if cp.Config == nil || cp.Config.Run.Path != "platform-mcp" {
		t.Errorf("checkpoint main process must be the platform-mcp client (exit 1 on reject), got %+v", cp.Config)
	}

	// The rendered plan (bare failing task step included) still validates.
	if _, errs := configvalidate.Validate(out.Config); len(errs) != 0 {
		t.Fatalf("configvalidate errors on rendered plan with fail-checkpoint: %v", errs)
	}
}

// TestRenderCheckpointWithoutPlatformSidecarErrors (F36, 2026-07-09): a
// checkpoint step in a workflow whose sidecars map lacks a "platform" entry is
// a render ERROR — the renderer must never emit a zero-value sidecar (which
// would fail SidecarConfig.Validate at configvalidate time and, worse, ship a
// checkpoint pod with no trust boundary).
func TestRenderCheckpointWithoutPlatformSidecarErrors(t *testing.T) {
	cfg := fullWorkflow()
	delete(cfg.Sidecars, "platform")
	cfg.Defaults = workflow.Defaults{Model: "claude-sonnet-4-5", MaxTurns: 80}
	cfg.Prompts = map[string]string{"spec": "s"}
	cfg.Steps = []workflow.Step{
		{Agent: "write-spec", Prompt: "spec", Sidecars: []string{"dev"}, BudgetSliceUSD: 2, Outputs: []string{"workspace"}},
		{Checkpoint: "plan-approval", OnReject: "fail"},
	}
	in := dispatch.RenderInput{Workflow: cfg, AgentStepImage: "img:v1", ATCExternalURL: "https://concourse.home"}
	in.Ticket = tickets.Ticket{ID: 42, Repo: "tdmtrader/concourse", TargetBranch: "main"}

	_, err := dispatch.Render(in)
	if err == nil {
		t.Fatal("checkpoint without a platform sidecar must be a render error, and must emit no config")
	}
	if !strings.Contains(err.Error(), `checkpoint "plan-approval" requires a "platform" sidecar in the workflow definition`) {
		t.Errorf("guard error text drifted from the frozen delta §2: %q", err.Error())
	}
}
```

This file's import block also gains `"strings"` alongside `configvalidate`; add both (the golden test in Task 6 also imports it, so if Task 6 lands first the import already exists — keep the two files' imports consistent):

```go
import "github.com/concourse/concourse/atc/configvalidate"
```

- [ ] **Step 2: Add the type-assert helpers** at the bottom of `agent/dispatch/render_test.go`:

```go
import "github.com/concourse/concourse/atc"

type atcHarvest = atc.HarvestStep
type atcAgent = atc.AgentStep
type atcTask = atc.TaskStep
```

- [ ] **Step 3: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run 'TestRenderProduces|TestRenderFilesMode|TestRenderCheckpointOnReject'`
Expected: FAIL — `undefined: dispatch.Render`.

- [ ] **Step 4: Write `agent/dispatch/render.go`:**

```go
package dispatch

import (
	"fmt"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

// PipelineNameForTicket is the base template pipeline name for a ticket.
func PipelineNameForTicket(ticketID int) string {
	return fmt.Sprintf("agent-ticket-%d", ticketID)
}

// Render turns a resolved workflow definition + ticket into a self-contained
// template: true pipeline (contracts §2.8, §2.8.2). Pure function, no I/O.
func Render(in RenderInput) (RenderOutput, error) {
	if err := in.Validate(); err != nil {
		return RenderOutput{}, err
	}

	plan := make([]atc.Step, 0, len(in.Workflow.Steps)+1)
	for _, s := range in.Workflow.Steps {
		switch {
		case s.Agent != "":
			as, err := RenderAgentStep(in, s)
			if err != nil {
				return RenderOutput{}, err
			}
			// §2.8.2 default read model = MCP: inject NO spec/plan bytes. The agent
			// reaches spec/plan via the platform-mcp read tools (read_ticket /
			// list_tasks / get_task). files mode delivers them as a read-only mount
			// (RunInputs below), never as env — so no step ever carries AGENT_SPEC_MD.
			plan = append(plan, atc.Step{Config: &as})
		case s.Checkpoint != "":
			cs, err := renderCheckpointStep(in, s)
			if err != nil {
				return RenderOutput{}, err // F36 render-time guard: no platform sidecar => no config emitted
			}
			plan = append(plan, atc.Step{Config: &cs})
		default:
			return RenderOutput{}, fmt.Errorf("step is neither agent nor checkpoint: %+v", s)
		}
	}

	hs, err := RenderHarvestStep(in)
	if err != nil {
		return RenderOutput{}, err
	}
	plan = append(plan, atc.Step{Config: &hs})

	config := atc.Config{
		Template: true,
		Params: []atc.ParamSchema{
			{Name: "ticket_id", Type: "number", Required: true, Description: "agent ticket id"},
		},
		Jobs: atc.JobConfigs{
			{
				Name:         "run",
				PlanSequence: plan,
			},
		},
	}

	// §2.8.2: only files mode materializes the read-only spec.md/plan.md the
	// dispatcher mounts as the "ticket" artifact. In the default mcp mode
	// RunInputs stays nil — the platform-mcp read tools are the delivery path.
	var runInputs map[string]string
	if in.Workflow.SpecDelivery == "files" {
		runInputs = map[string]string{
			"spec.md": string(tickets.RenderSpecMarkdown(in.Ticket, in.Spec)),
			"plan.md": string(tickets.RenderPlanMarkdown(in.Ticket, in.Tasks)),
		}
	}

	return RenderOutput{
		Config:    config,
		RunInputs: runInputs,
		Params:    map[string]any{"ticket_id": in.Ticket.ID},
	}, nil
}

// renderCheckpointStep renders a workflow checkpoint into a bare TASK step
// per the frozen checkpoint seam delta §2 (2026-07-09) — identical for both
// on_reject values, never wrapped in try/on_failure/ensure. The main
// container runs the deterministic `platform-mcp checkpoint --name <n>`
// client (binary on PATH; the platform-mcp image is shell-bearing per delta
// §8/F28 because it doubles as the task MAIN image under jetbridge's sh
// supervisor). It is NOT an agent: step — an AgentStep's main process is
// hardwired to agent-runner/claude with no command override, so it could not
// run the checkpoint client and a rejected checkpoint would exit 0 and let
// the run proceed as if approved, defeating the human gate.
//
// WIRE MODEL (delta §1, F14): the client talks ONLY to the pod-local platform
// sidecar over loopback — it trims /mcp from PLATFORM_MCP_URL, POSTs
// /checkpoint, blocks while parked, and exits 0 (approve) / 1 (reject or
// error) / 2 (usage). CLIENT AUTH: NONE — the pod boundary is the auth
// boundary, so the main container carries NO AGENT_PRINCIPAL_TOKEN (F16: the
// old ((principal-token)) param was an undefined var that failed config
// interpolation on every run). The SIDECAR is the trust boundary: it alone
// holds AGENT_PRINCIPAL_TOKEN (secretKeyRef ValueFrom via the Task 4b seam,
// F15), files the kind='checkpoint' agent_run_questions row, long-polls the
// ATC route, and emits the §5 checkpoint.wait/checkpoint.release events.
//
// on_reject mapping: at the STEP level BOTH `fail` and `send_back` fail the
// step on reject — the client always exits 1, and the renderer emits the SAME
// bare failing task step for either value. Exit 1 fails the task => build =>
// run completes failed; the dispatcher's run-completion reconciler (Task 11b,
// delta §6) then walks the ticket, reading on_reject from the ticket's frozen
// workflow config: send_back => running->queued re-dispatch (attempt_count++
// per §2.1); fail/empty/unknown-step => running->needs_review. The renderer
// only guarantees a failing task step so the reconciler has a failure to map.
//
// No timeout is set: checkpoints always park (F31 park-lifetime ceilings are
// a sibling delta, not handled here). The checkpoint has no free-text
// description in the workflow grammar (workflow.Step carries Checkpoint +
// OnReject only), so the client is invoked with --name alone.
func renderCheckpointStep(in RenderInput, s workflow.Step) (atc.TaskStep, error) {
	// RENDER-TIME GUARD (F36): never emit a zero-value sidecar.
	platform, ok := in.Workflow.Sidecars["platform"]
	if !ok || platform.Image == "" {
		return atc.TaskStep{}, fmt.Errorf("checkpoint %q requires a %q sidecar in the workflow definition", s.Checkpoint, "platform")
	}

	stepName := "checkpoint-" + s.Checkpoint

	// Main-container env. PLATFORM_MCP_URL is set literally (a TaskStep does
	// not get the agent-step MCP-URL-by-sidecar-name derivation) and is the
	// client's ONLY required var (exit 2 if unset); the other four rows are
	// provenance/logging only. AGENT_PIPELINE_RUN_ID is the ((run_id))
	// reserved var — pipeline_runs.id, allocated pre-materialization (F30).
	params := atc.TaskEnv{
		"ATC_EXTERNAL_URL":      in.ATCExternalURL,
		"AGENT_TICKET_ID":       fmt.Sprintf("%d", in.Ticket.ID),
		"AGENT_PIPELINE_RUN_ID": "((run_id))",
		"AGENT_STEP_NAME":       stepName,
		"PLATFORM_MCP_URL":      "http://127.0.0.1:7781/mcp",
	}

	// F29: repository and tag are SPLIT — never a tag inside "repository"
	// (imageresolver appends :latest unconditionally, so "repo:v1" would
	// become the fatal name.ParseReference input "repo:v1:latest").
	repo, tag := splitImageRef(platform.Image)
	source := atc.Source{"repository": repo}
	if tag != "" {
		source["tag"] = tag
	}

	// F15: the platform sidecar carries the same four identity rows as
	// literal env plus the principal token via secretKeyRef ValueFrom (Task
	// 4b seam; jetbridge gates the ref on --kubernetes-sidecar-secret-
	// prefixes). The secret name interpolates at run materialization —
	// ((run_id)) coverage includes secretKeyRef names (§8.2, F30 delta). No
	// PLATFORM_MCP_ASK_TIMEOUT_* rows (checkpoints always park; sidecar
	// defaults park/0), no BUILD_ID (not knowable at render; checkpoint
	// question rows carry build_id=0 in v1 — pipeline_run_id + step_name are
	// the join keys). MCP_LISTEN_ADDR defaults :7781.
	sidecarEnv := []atc.SidecarEnvVar{
		{Name: "ATC_EXTERNAL_URL", Value: in.ATCExternalURL},
		{Name: "AGENT_TICKET_ID", Value: fmt.Sprintf("%d", in.Ticket.ID)},
		{Name: "AGENT_PIPELINE_RUN_ID", Value: "((run_id))"},
		{Name: "AGENT_STEP_NAME", Value: stepName},
		{Name: "AGENT_PRINCIPAL_TOKEN", ValueFrom: &atc.SidecarEnvVarSource{
			SecretKeyRef: &atc.SidecarSecretKeySelector{Name: "agent-run-((run_id))", Key: "principal-token"},
		}},
	}

	return atc.TaskStep{
		Name:   stepName,
		Params: params,
		Sidecars: []atc.SidecarSource{
			// Name "platform" is the fixed role key (delta §2). Image is the
			// full ref incl. tag — sidecars are immune to F29 (jetbridge's own
			// parseImageRef splits it).
			{Config: &atc.SidecarConfig{Name: "platform", Image: platform.Image, Env: sidecarEnv}},
		},
		Config: &atc.TaskConfig{
			Platform:      "linux",
			ImageResource: &atc.ImageResource{Type: "registry-image", Source: source},
			Run: atc.TaskRunConfig{
				// Resolved on PATH: the image installs /usr/local/bin/platform-mcp.
				Path: "platform-mcp",
				Args: []string{"checkpoint", "--name", s.Checkpoint},
			},
		},
	}, nil
}

// splitImageRef splits an image ref into repository and tag, following
// exec.parseImageRef semantics (task_step.go:866): digest refs ("@sha256:")
// pass through whole with no tag (imageresolver.Resolve's digest
// short-circuit, resolver.go:54-56, handles them); otherwise split at the
// last ':' occurring after the last '/'. A ref with no tag returns tag == ""
// (the resolver defaults to latest; §8.5 import validation rejects untagged
// workflow images anyway, so that branch is dead in practice).
func splitImageRef(image string) (repo, tag string) {
	if strings.Contains(image, "@sha256:") {
		return image, ""
	}
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon > slash {
		return image[:colon], image[colon+1:]
	}
	return image, ""
}
```

(`render.go`'s import block gains `"strings"`.)

- [ ] **Step 4b: Add the in-package `splitImageRef` table test** (added 2026-07-09, F29; the helper is unexported per the frozen delta name, so the test lives in `package dispatch`) — create `agent/dispatch/render_internal_test.go`:

```go
package dispatch

import "testing"

func TestSplitImageRef(t *testing.T) {
	cases := []struct{ in, repo, tag string }{
		{"ghcr.io/tdmtrader/mcp-platform:v1", "ghcr.io/tdmtrader/mcp-platform", "v1"},
		{"registry:5000/img:v2", "registry:5000/img", "v2"}, // port colon sits before the last '/'
		{"registry:5000/img", "registry:5000/img", ""},      // port colon alone is NOT a tag
		{"alpine", "alpine", ""},                             // no tag: resolver defaults latest
		{"ghcr.io/x/y@sha256:abcd", "ghcr.io/x/y@sha256:abcd", ""}, // digest passes whole, no tag key
	}
	for _, c := range cases {
		repo, tag := splitImageRef(c.in)
		if repo != c.repo || tag != c.tag {
			t.Errorf("splitImageRef(%q) = (%q, %q), want (%q, %q)", c.in, repo, tag, c.repo, c.tag)
		}
	}
}
```

- [ ] **Step 5: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/`
Expected: PASS (all render tests, including `TestRenderProducesTemplatePipelineWithOneRunJob` asserting the frozen checkpoint shape — `*atc.TaskStep` invoking `platform-mcp checkpoint --name plan-approval`, `image_resource.source` split into `{repository, tag}` (F29), NO `AGENT_PRINCIPAL_TOKEN` param (F16), `AGENT_PIPELINE_RUN_ID: ((run_id))` (F30), the `platform` sidecar with four literal env rows + the principal-token secretKeyRef ValueFrom (F15), and no `PLATFORM_MCP_CHECKPOINT*` env; `TestRenderCheckpointOnRejectFailFailsRun` asserting the on_reject=fail checkpoint is a bare task step whose non-zero exit fails the run and the plan still passes `configvalidate`; `TestRenderCheckpointWithoutPlatformSidecarErrors` asserting the F36 guard error; and `TestSplitImageRef`).

- [ ] **Step 6: Commit**

```bash
git add agent/dispatch/render.go agent/dispatch/render_test.go agent/dispatch/render_internal_test.go
git commit -m "feat(dispatch): assemble full template pipeline (agent + checkpoint + terminal harvest)" -m "Checkpoint step per the 2026-07-09 frozen seam delta: no principal-token param (F16), repo/tag split (F29), platform-sidecar env + secretKeyRef (F15), render guard (F36), ((run_id)) (F30)." -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: Golden-file rendering test validated against `configvalidate`

The charter's honesty mechanism: golden-file tests per definition version, validated against `atc configvalidate`. Store a real workflow YAML fixture, render it, marshal the pipeline to YAML, assert it byte-matches a golden file, and assert `configvalidate.Validate` reports no errors. This test lives under `atc/` (not the pure `agent/dispatch` package) because `configvalidate` is an `atc` package with a Ginkgo suite — but it stays a plain `testing` test to keep the golden compare simple; verify the file's package matches the codebase idiom before landing.

**Files:**
- Create: `agent/dispatch/testdata/standard-dev.yml` (workflow definition fixture)
- Create: `agent/dispatch/testdata/standard-dev.golden.yml` (expected rendered pipeline)
- Create: `agent/dispatch/golden_test.go`

**Steps:**

- [ ] **Step 1: Write the workflow fixture** `agent/dispatch/testdata/standard-dev.yml` (the seed library definition from workflow-store, §6):

```yaml
schema_version: 1
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
    image: ghcr.io/tdmtrader/mcp-dev-concourse:v1
    role: dev
  platform:
    image: ghcr.io/tdmtrader/mcp-platform:v1
    role: platform
prompts:
  spec: |
    Read the ticket via platform-mcp read_ticket, explore the repo, then submit a spec.
  implement: |
    Implement the active plan task by task. Use dev-mcp run_tests after each task.
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
  sidecars: [dev, platform]
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
  pass_threshold: 6.5
```

- [ ] **Step 2: Write the failing golden test** `agent/dispatch/golden_test.go`:

```go
package dispatch_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/configvalidate"
	"sigs.k8s.io/yaml"
)

var update = flag.Bool("update", false, "update golden files")

func TestRenderGoldenStandardDev(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "standard-dev.yml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := workflow.Parse(raw)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	cfg.Version = 3

	in := dispatch.RenderInput{Workflow: *cfg, AgentStepImage: "ghcr.io/tdmtrader/agent-runner:v1", ATCExternalURL: "https://concourse.home", WorkflowHashForTest: "deadbeef"}
	in.Ticket = tickets.Ticket{ID: 42, Repo: "tdmtrader/concourse", TargetBranch: "main", Title: "Fix X"}
	in.Spec = &tickets.Spec{Title: "Fix X", Body: "context"}

	out, err := dispatch.Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// The rendered template MUST pass configvalidate with zero errors.
	warnings, errs := configvalidate.Validate(out.Config)
	if len(errs) != 0 {
		t.Fatalf("configvalidate errors on rendered template: %v (warnings: %v)", errs, warnings)
	}

	got, err := yaml.Marshal(out.Config)
	if err != nil {
		t.Fatal(err)
	}
	goldenPath := filepath.Join("testdata", "standard-dev.golden.yml")
	if *update {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("rendered pipeline drifted from golden.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
```

- [ ] **Step 3: Run to verify failure** (golden file does not exist yet)

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestRenderGolden`
Expected: FAIL — `read golden (run with -update to create): ... no such file`. If instead `configvalidate errors` fails, the rendered config is invalid — fix the renderer (e.g. a step is missing a core type) before generating the golden.

- [ ] **Step 4: Generate the golden file**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestRenderGolden -update`
Expected: PASS (writes `standard-dev.golden.yml`).

- [ ] **Step 5: Inspect the golden by eye** *(checklist updated 2026-07-09 to the frozen checkpoint seam shapes)* — open `agent/dispatch/testdata/standard-dev.golden.yml` and confirm: `template: true`; one job `run`; four plan steps (`agent: write-spec`, then a `task: checkpoint-plan-approval` whose `config.run.path: platform-mcp` with args `[checkpoint, --name, plan-approval]` — NOT an `agent:` step, carrying neither `PLATFORM_MCP_CHECKPOINT` nor `PLATFORM_MCP_CHECKPOINT_ON_REJECT`, then `agent: implement`, then `harvest: harvest`); the checkpoint's `image_resource` is `source: {repository: ghcr.io/tdmtrader/mcp-platform, tag: v1}` — repo/tag SPLIT, never a tag inside `repository` (F29); its `params` carry `ATC_EXTERNAL_URL`/`AGENT_TICKET_ID`/`AGENT_PIPELINE_RUN_ID: ((run_id))`/`AGENT_STEP_NAME: checkpoint-plan-approval`/`PLATFORM_MCP_URL` and NO `AGENT_PRINCIPAL_TOKEN` (F16); its single sidecar is `name: platform` with `image: ghcr.io/tdmtrader/mcp-platform:v1` (full ref — sidecars are immune to F29), the four literal env rows, and an `AGENT_PRINCIPAL_TOKEN` entry with `valueFrom: {secretKeyRef: {name: agent-run-((run_id)), key: principal-token}}` (F15); the harvest step carries `gate_policy` with three gates and a `judge` with `pass_threshold: 6.5` and `budget_usd: 1`; agent steps carry inline sidecars with role-names `dev`/`platform` and the resolved images; env includes `AGENT_PIPELINE_RUN_ID: ((run_id))` (F30). The fixture omits `spec_delivery` (default `mcp`), so NO agent step carries `AGENT_SPEC_MD`/`AGENT_PLAN_MD` env (§2.8.2 default read model); the agent reaches spec/plan via the platform-mcp read tools, seeded by the first step's prompt ("Read the ticket via platform-mcp read_ticket …").

- [ ] **Step 6: Run to verify pass** (golden now compares clean)

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestRenderGolden`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add agent/dispatch/testdata agent/dispatch/golden_test.go
git commit -m "test(dispatch): golden-file render of standard-dev validated against configvalidate" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: `RenderResolver` — resolve a ticket's live workflow definition into a `RenderInput`

The dispatcher does not read workflow tables inside the render (render-time-resolution rule) — it *resolves* the live/pinned definition first, then hands the renderer a fully-materialized `RenderInput`. This resolver consumes `workflow.Store` + `tickets.Store` and is the seam between the pure renderer and the DB.

**Files:**
- Create: `agent/dispatch/resolver.go`
- Test: `agent/dispatch/resolver_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/dispatch/resolver_test.go`:

```go
package dispatch_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
)

type fakeWorkflowStore struct {
	live map[string]*workflow.Definition
	byV  map[string]*workflow.Definition // "name/version" -> def
}

func (f *fakeWorkflowStore) Import(string, []byte, string) (*workflow.Definition, error) { return nil, nil }
func (f *fakeWorkflowStore) Get(name string, v int) (*workflow.Definition, bool, error) {
	d, ok := f.byV[keyNV(name, v)]
	return d, ok, nil
}
func (f *fakeWorkflowStore) Live(name string) (*workflow.Definition, bool, error) {
	d, ok := f.live[name]
	return d, ok, nil
}
func (f *fakeWorkflowStore) List() ([]workflow.Definition, error)              { return nil, nil }
func (f *fakeWorkflowStore) Versions(string) ([]workflow.Definition, error)    { return nil, nil }
func (f *fakeWorkflowStore) Promote(string, int, string) error                 { return nil }

func keyNV(n string, v int) string { return n + "/" + itoa(v) }
func itoa(v int) string            { return map[int]string{0: "0", 3: "3", 4: "4"}[v] }

func TestResolveLiveDefinition(t *testing.T) {
	def := &workflow.Definition{Name: "standard-dev", Version: 4, ContentHash: "hash4", Config: fullWorkflow()}
	def.Config.Steps = []workflow.Step{{Agent: "a", Prompt: "spec"}}
	def.Config.Prompts = map[string]string{"spec": "p"}
	store := &fakeWorkflowStore{live: map[string]*workflow.Definition{"standard-dev": def}}

	tkt := tickets.Ticket{ID: 42, Repo: "r/x", WorkflowName: "standard-dev"} // WorkflowVersion nil => live
	r := dispatch.NewRenderResolver(store, "img:v1", "https://c.home")
	in, err := r.Resolve(tkt, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if in.Workflow.Version != 4 || in.WorkflowHashForTest != "hash4" {
		t.Errorf("wrong resolved version/hash: v=%d hash=%q", in.Workflow.Version, in.WorkflowHashForTest)
	}
	if in.AgentStepImage != "img:v1" || in.ATCExternalURL != "https://c.home" {
		t.Errorf("resolver did not thread image/url: %+v", in)
	}
}

func TestResolveNoLiveVersionErrors(t *testing.T) {
	store := &fakeWorkflowStore{live: map[string]*workflow.Definition{}}
	r := dispatch.NewRenderResolver(store, "img:v1", "u")
	_, err := r.Resolve(tickets.Ticket{ID: 1, Repo: "r", WorkflowName: "missing"}, nil, nil)
	if err == nil {
		t.Fatal("no live version must be a dispatchable error")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestResolve`
Expected: FAIL — `undefined: dispatch.NewRenderResolver`.

- [ ] **Step 3: Write `agent/dispatch/resolver.go`:**

```go
package dispatch

import (
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/workflow"
)

// ErrNoLiveWorkflow is returned when a ticket names a workflow with no live
// version and no pinned version. The dispatcher treats this as a ticket
// fault (errored), not a platform fault.
var ErrNoLiveWorkflow = errors.New("no live or pinned workflow version for ticket")

// RenderResolver turns a ticket into a fully-materialized RenderInput by
// resolving its workflow definition (pinned version or live). It is the only
// component that touches workflow.Store; the renderer stays pure.
type RenderResolver struct {
	workflows      workflow.Store
	agentStepImage string
	atcExternalURL string
}

func NewRenderResolver(workflows workflow.Store, agentStepImage, atcExternalURL string) *RenderResolver {
	return &RenderResolver{workflows: workflows, agentStepImage: agentStepImage, atcExternalURL: atcExternalURL}
}

// Resolve looks up the ticket's workflow (pinned WorkflowVersion, else live),
// and returns a RenderInput carrying the resolved config, hash, spec and tasks.
func (r *RenderResolver) Resolve(t tickets.Ticket, spec *tickets.Spec, tasks []tickets.Task) (RenderInput, error) {
	if t.WorkflowName == "" {
		return RenderInput{}, fmt.Errorf("%w: ticket %d has no workflow name", ErrNoLiveWorkflow, t.ID)
	}

	var (
		def   *workflow.Definition
		found bool
		err   error
	)
	if t.WorkflowVersion != nil {
		def, found, err = r.workflows.Get(t.WorkflowName, *t.WorkflowVersion)
	} else {
		def, found, err = r.workflows.Live(t.WorkflowName)
	}
	if err != nil {
		return RenderInput{}, fmt.Errorf("resolve workflow %q: %w", t.WorkflowName, err)
	}
	if !found || def == nil {
		return RenderInput{}, fmt.Errorf("%w: %q", ErrNoLiveWorkflow, t.WorkflowName)
	}

	cfg := def.Config
	cfg.Version = def.Version

	return RenderInput{
		Workflow:            cfg,
		Ticket:              t,
		Spec:                spec,
		Tasks:               tasks,
		AgentStepImage:      r.agentStepImage,
		ATCExternalURL:      r.atcExternalURL,
		WorkflowHashForTest: def.ContentHash,
	}, nil
}
```

- [ ] **Step 4: Confirm the `workflow.Config.Version` pass-through exists.** The resolver stamps `cfg.Version = def.Version` (above), relying on the `Config.Version` field added back in Task 2, Step 4. Verify it is present:

Run: `cd /Users/tdmtrader/concourse/concourse && grep -n "Version int" agent/workflow/definition.go`

Expected: the `Version int \`yaml:"-" json:"-"\`` field is present on `Config`. If it is somehow absent (Task 2 skipped it because workflow-store already had one under a different name), reconcile before proceeding — `resolver.go` will not compile otherwise.

- [ ] **Step 5: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestResolve`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/dispatch/resolver.go agent/dispatch/resolver_test.go
git commit -m "feat(dispatch): RenderResolver resolves live/pinned workflow into a RenderInput" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

(`agent/workflow/definition.go`'s `Config.Version` field was already committed in Task 2, Step 7 — nothing new to stage here for it.)

---

### Task 8: `dispatch.TicketBudgets` — the concrete `budget.TicketBudgets` implementation

credentials-and-budgets froze `budget.TicketBudgets` as a seam and stubbed it with `NoTicketBudgets`; the charter assigns the real implementation to dispatch. It resolves `tickets.budget_usd ?? workflow default ticket_usd`.

**Files:**
- Create: `agent/dispatch/budgets.go`
- Test: `agent/dispatch/budgets_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/dispatch/budgets_test.go`:

```go
package dispatch_test

import (
	"testing"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
)

type ticketGetter struct{ rows map[int]tickets.Ticket }

func (g ticketGetter) Get(id int) (*tickets.Ticket, bool, error) {
	t, ok := g.rows[id]
	if !ok {
		return nil, false, nil
	}
	return &t, true, nil
}

func TestTicketBudgetsTicketOverride(t *testing.T) {
	budget20 := 20.0
	getter := ticketGetter{rows: map[int]tickets.Ticket{
		7: {ID: 7, WorkflowName: "standard-dev", BudgetUSD: &budget20},
		8: {ID: 8, WorkflowName: "standard-dev"}, // no ticket budget -> workflow default
	}}
	resolver := dispatch.NewRenderResolver(&fakeWorkflowStore{live: map[string]*workflow.Definition{
		"standard-dev": {Name: "standard-dev", Version: 4, Config: workflow.Config{Name: "standard-dev", Budget: workflow.Budget{TicketUSD: 15}}},
	}}, "img", "url")

	tb := dispatch.NewTicketBudgets(getter, resolver)

	got, found, err := tb.BudgetUSD(7)
	if err != nil || !found || got != 20.0 {
		t.Errorf("ticket-override budget: got=%v found=%v err=%v want 20", got, found, err)
	}
	got, found, err = tb.BudgetUSD(8)
	if err != nil || !found || got != 15.0 {
		t.Errorf("workflow-default budget: got=%v found=%v err=%v want 15", got, found, err)
	}
}

func TestTicketBudgetsUnknownTicketUncapped(t *testing.T) {
	tb := dispatch.NewTicketBudgets(ticketGetter{rows: map[int]tickets.Ticket{}},
		dispatch.NewRenderResolver(&fakeWorkflowStore{live: map[string]*workflow.Definition{}}, "i", "u"))
	_, found, err := tb.BudgetUSD(99)
	if err != nil || found {
		t.Errorf("unknown ticket must be uncapped (found=false), got found=%v err=%v", found, err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestTicketBudgets`
Expected: FAIL — `undefined: dispatch.NewTicketBudgets`.

- [ ] **Step 3: Write `agent/dispatch/budgets.go`:**

```go
package dispatch

import "github.com/concourse/concourse/agent/api/tickets"

// TicketGetter is the subset of tickets.Store the budget resolver needs.
type TicketGetter interface {
	Get(id int) (*tickets.Ticket, bool, error)
}

// TicketBudgets implements budget.TicketBudgets (credentials-and-budgets §2.7)
// with the real "tickets.budget_usd ?? workflow default ticket_usd" rule the
// wave-1 NoTicketBudgets stub stood in for.
type TicketBudgets struct {
	tickets  TicketGetter
	resolver *RenderResolver
}

func NewTicketBudgets(tg TicketGetter, resolver *RenderResolver) *TicketBudgets {
	return &TicketBudgets{tickets: tg, resolver: resolver}
}

// BudgetUSD returns the effective ticket budget. found=false means uncapped
// (unknown ticket or a workflow with no default). Matches the
// budget.TicketBudgets contract exactly.
func (b *TicketBudgets) BudgetUSD(ticketID int) (float64, bool, error) {
	t, ok, err := b.tickets.Get(ticketID)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil // uncapped
	}
	if t.BudgetUSD != nil && *t.BudgetUSD > 0 {
		return *t.BudgetUSD, true, nil
	}
	// fall back to the workflow default
	in, err := b.resolver.Resolve(*t, nil, nil)
	if err != nil {
		return 0, false, nil // cannot resolve => uncapped, dispatcher will error the ticket elsewhere
	}
	if in.Workflow.Budget.TicketUSD > 0 {
		return in.Workflow.Budget.TicketUSD, true, nil
	}
	return 0, false, nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestTicketBudgets`
Expected: PASS.

- [ ] **Step 5: Verify the interface satisfies budget.TicketBudgets.** Add a compile-time assertion to `agent/dispatch/budgets.go`:

```go
import "github.com/concourse/concourse/agent/budget"

var _ budget.TicketBudgets = (*TicketBudgets)(nil)
```

Run: `cd /Users/tdmtrader/concourse/concourse && go build ./agent/dispatch/`
Expected: clean build (proves the `BudgetUSD(int) (float64, bool, error)` signature matches).

- [ ] **Step 6: Commit**

```bash
git add agent/dispatch/budgets.go agent/dispatch/budgets_test.go
git commit -m "feat(dispatch): dispatch.TicketBudgets implements budget.TicketBudgets (ticket override ?? workflow default)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: `dispatch.Deps` + credential/principal resolution helpers

Milestone 2 begins. The dispatcher needs many collaborators; group them in a `Deps` struct so the loop body is testable with fakes. Add the credential-resolution + per-run-principal-mint helpers here (pure logic over the interfaces).

**Files:**
- Create: `agent/dispatch/deps.go`
- Test: `agent/dispatch/deps_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/dispatch/deps_test.go`:

```go
package dispatch_test

import (
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
)

type fakeCreds struct {
	cred  *credentials.Credential
	found bool
	err   error
}

func (f fakeCreds) Resolve(userID int, kind string) (*credentials.Credential, bool, error) {
	return f.cred, f.found, f.err
}

type fakePrincipals struct {
	lastSpec principals.CreateSpec
	token    string
}

func (f *fakePrincipals) Create(spec principals.CreateSpec) (principals.Principal, string, error) {
	f.lastSpec = spec
	return principals.Principal{ID: 5, Name: spec.Name}, f.token, nil
}

func TestResolveUserCredentialExpired(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	cr := &credentials.Credential{UserID: 3, UserName: "alice", Kind: credentials.KindAnthropicOAuth, Token: "tok", ExpiresAt: past.Unix()}
	_, err := dispatch.ResolveUserCredential(fakeCreds{cred: cr, found: true}, 3, time.Now())
	if err == nil {
		t.Fatal("expired credential must error (owner noted)")
	}
	if !contains(err.Error(), "alice") {
		t.Errorf("error must name the owner, got %q", err.Error())
	}
}

func TestResolveUserCredentialMissing(t *testing.T) {
	_, err := dispatch.ResolveUserCredential(fakeCreds{found: false}, 3, time.Now())
	if err == nil {
		t.Fatal("missing credential must error")
	}
}

func TestMintRunPrincipalScopes(t *testing.T) {
	fp := &fakePrincipals{token: "cap1.5.secret"}
	tok, err := dispatch.MintRunPrincipal(context.Background(), fp, 77, time.Hour)
	if err != nil || tok != "cap1.5.secret" {
		t.Fatalf("mint: tok=%q err=%v", tok, err)
	}
	want := []string{"tickets:read", "tickets:write", "metrics:write", "costs:write", "questions:answer"}
	if len(fp.lastSpec.Scopes) != len(want) {
		t.Fatalf("scopes = %v, want %v", fp.lastSpec.Scopes, want)
	}
	if fp.lastSpec.Name != "agent-run-77" {
		t.Errorf("principal name = %q, want agent-run-77", fp.lastSpec.Name)
	}
	if fp.lastSpec.ExpiresAt == nil {
		t.Error("per-run principal must have an expiry")
	}
}

// F31 principal-expiry leg (contracts §8.1 AGENT_PRINCIPAL_TOKEN row +
// §3.2 PARK-DURATION BOUNDS, 2026-07-09): park-policy workflows select
// --agent-park-timeout, everything else --agent-run-timeout.
func TestRunPrincipalTimeoutSelection(t *testing.T) {
	plain := workflow.Config{Steps: []workflow.Step{{Agent: "a", Prompt: "spec"}}}
	withCheckpoint := workflow.Config{Steps: []workflow.Step{
		{Agent: "a", Prompt: "spec"},
		{Checkpoint: "plan-approval", OnReject: "fail"},
	}}
	askPark := workflow.Config{
		Steps: []workflow.Step{{Agent: "a", Prompt: "spec"}},
		HITL:  workflow.HITL{AskTimeout: "park"},
	}

	if got := dispatch.RunPrincipalTimeout(plain, 6*time.Hour, 72*time.Hour); got != 6*time.Hour {
		t.Errorf("plain workflow = %v, want 6h (run timeout)", got)
	}
	if got := dispatch.RunPrincipalTimeout(withCheckpoint, 6*time.Hour, 72*time.Hour); got != 72*time.Hour {
		t.Errorf("checkpoint workflow = %v, want 72h (checkpoints always park)", got)
	}
	if got := dispatch.RunPrincipalTimeout(askPark, 6*time.Hour, 72*time.Hour); got != 72*time.Hour {
		t.Errorf("ask_timeout=park workflow = %v, want 72h (park timeout)", got)
	}
}

func TestMintRunPrincipalParkExpiry(t *testing.T) {
	fp := &fakePrincipals{token: "cap1.5.secret"}
	parked := workflow.Config{Steps: []workflow.Step{{Checkpoint: "ship", OnReject: "send_back"}}}
	before := time.Now()
	_, err := dispatch.MintRunPrincipal(context.Background(), fp, 78,
		dispatch.RunPrincipalTimeout(parked, 6*time.Hour, 72*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if fp.lastSpec.ExpiresAt == nil {
		t.Fatal("park-policy principal must still carry an expiry (NOT NULL — the backstop stays)")
	}
	got := time.Unix(*fp.lastSpec.ExpiresAt, 0)
	want := before.Add(72 * time.Hour)
	if got.Before(want.Add(-time.Minute)) || got.After(want.Add(2*time.Minute)) {
		t.Errorf("park-policy expiry = %v, want ~%v (now + 72h, --agent-park-timeout default)", got, want)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run 'TestResolveUserCredential|TestMintRunPrincipal|TestRunPrincipalTimeout'`
Expected: FAIL — `undefined: dispatch.ResolveUserCredential` (and `dispatch.RunPrincipalTimeout`).

- [ ] **Step 3: Write `agent/dispatch/deps.go`:**

```go
package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// CredentialResolver is the subset of credentials.Store the dispatcher uses.
type CredentialResolver interface {
	Resolve(userID int, kind string) (*credentials.Credential, bool, error)
}

// PrincipalMinter is the subset of principals.Store the dispatcher uses.
type PrincipalMinter interface {
	Create(spec principals.CreateSpec) (principals.Principal, string, error)
}

// RunSecretLabeler applies the dispatch-owned `concourse/ticket` label to the
// ephemeral run secret. credentials.SecretAttacher.Attach (§2.6) takes no
// ticket parameter and labels only `concourse/agent-run`; §8.2's full label
// set is satisfied jointly, with the ticket half applied here as a follow-up
// Patch (§2.8.2; credentials-and-budgets 02-...md §8.2 note: "ticket label is
// dispatch's job"). The reaper's safety-net GC keys off `concourse/agent-run`
// alone, so this label is for human/operator filtering, not GC correctness.
type RunSecretLabeler interface {
	// LabelTicket patches label `concourse/ticket: "<ticketID>"` onto secret
	// agent-run-<runID>. Called only when ticketID > 0. Idempotent.
	LabelTicket(ctx context.Context, runID, ticketID int) error
}

// PipelineSaver is the narrow SavePipeline seam. The real db.Team satisfies it
// structurally, and tests supply a tiny fake — no need to satisfy the whole
// db.Team interface.
type PipelineSaver interface {
	SavePipeline(atc.PipelineRef, atc.Config, db.ConfigVersion, bool) (db.Pipeline, bool, error)
}

// Deps are the dispatcher's collaborators. Grouped so DispatchOne is testable
// with fakes and the RunnableComponent wiring is a thin adapter.
type Deps struct {
	Tickets       tickets.Store
	Resolver      *RenderResolver
	Budget        budget.Checker
	Credentials   CredentialResolver
	Principals    PrincipalMinter
	SecretAttach  credentials.SecretAttacher
	SecretLabeler RunSecretLabeler // applies the concourse/ticket label (§2.8.2); nil-safe (skipped if nil)
	Runs          db.PipelineRunFactory
	Team          PipelineSaver // SavePipeline of the rendered template (real db.Team satisfies this)
	RunTimeout    time.Duration
	ParkTimeout   time.Duration // principal expiry for park-policy runs (--agent-park-timeout, default 72h; F31 leg, contracts §8.1 + §3.2 PARK-DURATION BOUNDS)
	MaxAttempts   int // attempt cap; over cap => errored, not re-queued
}

// runPrincipalScopes are the scopes a per-run agent principal holds (§2.8.2).
var runPrincipalScopes = []string{
	principals.ScopeTicketsRead,
	principals.ScopeTicketsWrite,
	principals.ScopeMetricsWrite,
	principals.ScopeCostsWrite,
	principals.ScopeQuestionsAnswer,
}

// ResolveUserCredential fetches the triggering user's anthropic OAuth token
// and rejects it if expired — with the owner named so the ticket's
// error_detail is actionable (§4 dispatcher: credential-expiry mid-run =>
// error with owner noted).
func ResolveUserCredential(cr CredentialResolver, userID int, now time.Time) (*credentials.Credential, error) {
	cred, found, err := cr.Resolve(userID, credentials.KindAnthropicOAuth)
	if err != nil {
		return nil, err
	}
	if !found || cred == nil {
		return nil, fmt.Errorf("no anthropic credential vaulted for user %d (they must run `fly agent auth`)", userID)
	}
	if cred.ExpiresAt != 0 && time.Unix(cred.ExpiresAt, 0).Before(now) {
		return nil, fmt.Errorf("anthropic credential for %s (user %d) expired %s; owner must re-run `fly agent auth`",
			cred.UserName, userID, time.Unix(cred.ExpiresAt, 0).Format(time.RFC3339))
	}
	return cred, nil
}

// MintRunPrincipal creates the per-run agent principal and returns its raw
// token (stored only in the ephemeral secret's principal-token key, §8.1).
func MintRunPrincipal(ctx context.Context, pm PrincipalMinter, runID int, timeout time.Duration) (string, error) {
	expires := time.Now().Add(timeout).Unix()
	_, token, err := pm.Create(principals.CreateSpec{
		Name:        fmt.Sprintf("agent-run-%d", runID),
		Description: fmt.Sprintf("per-run principal for pipeline run %d", runID),
		Scopes:      append([]string(nil), runPrincipalScopes...),
		TeamName:    "main",
		ExpiresAt:   &expires,
	})
	if err != nil {
		return "", fmt.Errorf("mint per-run principal: %w", err)
	}
	return token, nil
}

// RunPrincipalTimeout selects the per-run principal lifetime (F31
// principal-expiry leg; contracts §8.1 AGENT_PRINCIPAL_TOKEN row + §3.2
// PARK-DURATION BOUNDS, 2026-07-09): a run whose frozen workflow contains
// any park-policy step — a checkpoint step (checkpoints always park per the
// checkpoint seam delta; no timeout is rendered) or `hitl.ask_timeout: park`
// — is minted with expires_at = now + parkTimeout (--agent-park-timeout,
// default 72h) so a parked question is not killed by the 6h run timeout.
// Every other run uses runTimeout (--agent-run-timeout). The result is never
// zero-expiry-forever: expiry stays NOT NULL and remains the hard backstop —
// a park outliving it fails LOUDLY via the AwaitAnswer fatal-auth contract
// (consecutive-401/403 limit, plan 08), never as a silent forever-park.
func RunPrincipalTimeout(cfg workflow.Config, runTimeout, parkTimeout time.Duration) time.Duration {
	if cfg.HITL.AskTimeout == "park" {
		return parkTimeout
	}
	for _, s := range cfg.Steps {
		if s.Checkpoint != "" {
			return parkTimeout
		}
	}
	return runTimeout
}

// ticketLabel is the dispatch-owned §8.2 label key (credentials-and-budgets
// owns `concourse/agent-run`; the ticket half is dispatch's, §2.8.2).
const ticketLabel = "concourse/ticket"

// K8sRunSecretLabeler patches the concourse/ticket label onto the ephemeral
// run secret via a strategic-merge patch (creates-or-overwrites the one label,
// leaving the attacher's concourse/agent-run label and the secret data intact).
// It talks to the same worker namespace the SecretAttacher writes to.
type K8sRunSecretLabeler struct {
	client    kubernetes.Interface
	namespace string
}

func NewK8sRunSecretLabeler(client kubernetes.Interface, namespace string) *K8sRunSecretLabeler {
	return &K8sRunSecretLabeler{client: client, namespace: namespace}
}

func (l *K8sRunSecretLabeler) LabelTicket(ctx context.Context, runID, ticketID int) error {
	// Merge-patch only the label map — never touches secret data. Secret name
	// matches credentials.RunSecretName(runID) = "agent-run-<runID>".
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"labels": map[string]string{ticketLabel: strconv.Itoa(ticketID)},
		},
	})
	if err != nil {
		return fmt.Errorf("label run %d secret: marshal patch: %w", runID, err)
	}
	name := fmt.Sprintf("agent-run-%d", runID)
	_, err = l.client.CoreV1().Secrets(l.namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("label run %d secret %q with ticket %d: %w", runID, name, ticketID, err)
	}
	return nil
}

var _ RunSecretLabeler = (*K8sRunSecretLabeler)(nil)
```

- [ ] **Step 4: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run 'TestResolveUserCredential|TestMintRunPrincipal'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/dispatch/deps.go agent/dispatch/deps_test.go
git commit -m "feat(dispatch): Deps struct + credential/principal helpers + K8sRunSecretLabeler (concourse/ticket label)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 10: `DispatchOne` — claim, admit, resolve, render, persist, create run, transition

The heart of Milestone 2: dispatch a single queued ticket. The claim is `tickets.Store.Transition(id, queued→running)` guarded by `WHERE state='queued'` — `ErrStaleTransition` means another web node won (no double dispatch). Ordering (contract §4 + fork's never-notify-only lesson): admit BEFORE claiming so over-cap tickets stay `queued`; on platform faults after the claim, transition `running→errored`; the ticket transition records the `pipeline_run_id`.

**Files:**
- Create: `agent/dispatch/dispatch_one.go`
- Test: `agent/dispatch/dispatch_one_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/dispatch/dispatch_one_test.go` (drives the full path with fakes; the DB-backed integration lands in Task 12):

```go
package dispatch_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
)

func TestDispatchOneOverCapStaysQueued(t *testing.T) {
	h := newHarness(t)
	h.budgetGlobalRemaining = -1 // exhausted daily cap
	res := h.dispatch(ticketFor(7))
	if res != dispatch.OutcomeDeferred {
		t.Fatalf("over-cap ticket must defer (stay queued), got %v", res)
	}
	if h.transitions.count() != 0 {
		t.Errorf("over-cap must NOT transition the ticket, saw %v", h.transitions.log)
	}
}

func TestDispatchOneHappyPath(t *testing.T) {
	h := newHarness(t)
	res := h.dispatch(ticketFor(7))
	if res != dispatch.OutcomeDispatched {
		t.Fatalf("happy path outcome = %v, want dispatched", res)
	}
	if !h.transitions.saw(tickets.StateQueued, tickets.StateRunning) {
		t.Errorf("must claim via queued->running; saw %v", h.transitions.log)
	}
	if h.attached == 0 {
		t.Error("must attach the ephemeral secret")
	}
	if h.labeledTicket != 7 {
		t.Errorf("must patch the concourse/ticket label with the ticket id (§2.8.2), got %d", h.labeledTicket)
	}
	if h.createdRuns != 1 {
		t.Errorf("must create exactly one run, got %d", h.createdRuns)
	}
}

func TestDispatchOneLostClaimReapsOrphanRun(t *testing.T) {
	// The run is created BEFORE the claim (§2.1: the claim records the run id),
	// so a lost claim leaves an orphan run that DispatchOne must reap (Finish
	// aborted + Cleanup). The secret is attached only AFTER the claim, so it is
	// never created on this path.
	h := newHarness(t)
	h.claimErr = tickets.ErrStaleTransition
	res := h.dispatch(ticketFor(7))
	if res != dispatch.OutcomeLostClaim {
		t.Fatalf("stale claim => lost-claim outcome, got %v", res)
	}
	if h.createdRuns != 1 {
		t.Errorf("run is created before the claim, so exactly one run exists, got %d", h.createdRuns)
	}
	if h.attached != 0 {
		t.Error("secret is attached only after a won claim; lost claim must not attach")
	}
	if h.finishedAborted != 1 {
		t.Errorf("orphan run must be finished aborted, got %d", h.finishedAborted)
	}
	if h.cleaned != 1 {
		t.Errorf("orphan run's secret slot must be cleaned up, got %d", h.cleaned)
	}
}

func TestDispatchOneCredentialExpiredErrors(t *testing.T) {
	// Credential resolution happens BEFORE the claim, so an expired credential
	// errors a still-queued ticket (queued->errored), and no run is created.
	h := newHarness(t)
	h.credExpired = true
	res := h.dispatch(ticketFor(7))
	if res != dispatch.OutcomeErrored {
		t.Fatalf("expired cred => errored, got %v", res)
	}
	if !h.transitions.saw(tickets.StateQueued, tickets.StateErrored) {
		t.Errorf("expired cred is a pre-claim fault => queued->errored; saw %v", h.transitions.log)
	}
	if h.createdRuns != 0 {
		t.Errorf("pre-claim error must not create a run, got %d", h.createdRuns)
	}
}

func ticketFor(id int) tickets.Ticket {
	uid := 3
	return tickets.Ticket{ID: id, State: tickets.StateQueued, Repo: "r/x", WorkflowName: "standard-dev", UserID: &uid}
}
```

- [ ] **Step 2: Write the harness** `agent/dispatch/dispatch_one_harness_test.go` (fakes for every `Deps` collaborator; keeps the test file readable):

```go
package dispatch_test

import (
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
)

type transitionLog struct{ log []string }

func (l *transitionLog) record(from, to tickets.State) { l.log = append(l.log, string(from)+"->"+string(to)) }
func (l *transitionLog) count() int                    { return len(l.log) }
func (l *transitionLog) saw(from, to tickets.State) bool {
	want := string(from) + "->" + string(to)
	for _, e := range l.log {
		if e == want {
			return true
		}
	}
	return false
}

type harness struct {
	t                     *testing.T
	transitions           *transitionLog
	claimErr              error
	credExpired           bool
	budgetGlobalRemaining float64
	attached              int
	labeledTicket         int // ticket id the labeler was called with (0 = never)
	createdRuns           int
	finishedAborted       int
	cleaned               int
	queued                []tickets.Ticket
}

func newHarness(t *testing.T) *harness {
	return &harness{t: t, transitions: &transitionLog{}, budgetGlobalRemaining: 100}
}

func (h *harness) dispatch(tkt tickets.Ticket) dispatch.Outcome {
	deps := dispatch.Deps{
		Tickets:      &hTickets{h: h, tkt: tkt},
		Resolver:     dispatch.NewRenderResolver(hWorkflowStore(), "img:v1", "https://c.home"),
		Budget:       &hBudget{h: h},
		Credentials:   &hCreds{h: h},
		Principals:    &fakePrincipals{token: "cap1.5.secret"},
		SecretAttach:  &hAttacher{h: h},
		SecretLabeler: &hLabeler{h: h},
		Runs:          &hRuns{h: h},
		Team:          &hTeam{},
		RunTimeout:    time.Hour,
		ParkTimeout:   72 * time.Hour,
		MaxAttempts:   3,
	}
	return dispatch.DispatchOne(context.Background(), deps, tkt)
}

func hWorkflowStore() workflow.Store {
	def := &workflow.Definition{Name: "standard-dev", Version: 4, ContentHash: "h4", Config: workflow.Config{
		Name: "standard-dev", Budget: workflow.Budget{TicketUSD: 15},
		Prompts:  map[string]string{"spec": "p"},
		Sidecars: map[string]workflow.Sidecar{"dev": {Image: "i:v1", Role: "dev"}},
		Steps:    []workflow.Step{{Agent: "a", Prompt: "spec", Sidecars: []string{"dev"}, Outputs: []string{"workspace"}}},
		GatePolicy: workflow.GatePolicy{Gates: []workflow.Gate{{Gate: "build", Scope: "affected"}}, OnGateFailure: "needs_review"},
	}}
	return &fakeWorkflowStore{live: map[string]*workflow.Definition{"standard-dev": def}}
}
```

- [ ] **Step 3: Write the remaining fakes** `agent/dispatch/dispatch_one_fakes_test.go`:

```go
package dispatch_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type hTickets struct {
	h   *harness
	tkt tickets.Ticket
}

func (s *hTickets) Get(id int) (*tickets.Ticket, bool, error) { t := s.tkt; return &t, true, nil }
func (s *hTickets) Transition(id int, from, to tickets.State, _ tickets.TransitionMeta) error {
	if from == tickets.StateQueued && to == tickets.StateRunning && s.h.claimErr != nil {
		return s.h.claimErr
	}
	s.h.transitions.record(from, to)
	return nil
}
func (s *hTickets) List(tickets.ListFilter) ([]tickets.Ticket, error) { return nil, nil }
func (s *hTickets) Create(*tickets.Ticket) (int, error)               { return 0, nil }
func (s *hTickets) Update(int, tickets.Update) error                  { return nil }
func (s *hTickets) SubmitSpec(int, tickets.Spec) (int, error)         { return 0, nil }
func (s *hTickets) SubmitPlan(int, []tickets.Task) (int, error)       { return 0, nil }
func (s *hTickets) UpdateTaskStatus(int, int, int, tickets.TaskStatus) error { return nil }
func (s *hTickets) AppendTaskNote(int, int, int, string) error        { return nil }
func (s *hTickets) ActivePlan(int) ([]tickets.Task, error)            { return nil, nil }
func (s *hTickets) LatestSpec(int) (*tickets.Spec, bool, error)       { return nil, false, nil }

type hBudget struct{ h *harness }

func (b *hBudget) TicketRemaining(int) (budget.Remaining, error) {
	return budget.Remaining{RemainingUSD: 50}, nil
}
func (b *hBudget) GlobalDailyRemaining() (budget.Remaining, error) {
	return budget.Remaining{RemainingUSD: b.h.budgetGlobalRemaining, Exhausted: b.h.budgetGlobalRemaining <= 0}, nil
}
func (b *hBudget) StepSlice(int, float64) (budget.Remaining, error) {
	return budget.Remaining{RemainingUSD: 10}, nil
}
func (b *hBudget) Record(budget.LedgerEntry) error { return nil }

type hCreds struct{ h *harness }

func (c *hCreds) Resolve(userID int, kind string) (*credentials.Credential, bool, error) {
	exp := time.Now().Add(time.Hour).Unix()
	if c.h.credExpired {
		exp = time.Now().Add(-time.Hour).Unix()
	}
	return &credentials.Credential{UserID: userID, UserName: "alice", Kind: kind, Token: "tok", ExpiresAt: exp}, true, nil
}

type hAttacher struct{ h *harness }

func (a *hAttacher) Attach(_ context.Context, runID int, _ *credentials.Credential, _ string) (string, error) {
	a.h.attached++
	return "agent-run-" + itoa2(runID), nil
}
func (a *hAttacher) Cleanup(context.Context, int) error { a.h.cleaned++; return nil }

type hLabeler struct{ h *harness }

func (l *hLabeler) LabelTicket(_ context.Context, _ int, ticketID int) error {
	l.h.labeledTicket = ticketID
	return nil
}

type hRuns struct{ h *harness }

func (r *hRuns) CreateRun(templateID int, _ map[string]any, _ string) (db.PipelineRun, error) {
	r.h.createdRuns++
	return &hRun{id: 100 + templateID, h: r.h}, nil
}
func (r *hRuns) GetRun(int, int) (db.PipelineRun, bool, error)      { return nil, false, nil }
func (r *hRuns) ListRuns(int, int) ([]db.PipelineRun, error)        { return nil, nil }
func (r *hRuns) RunningRuns() ([]db.PipelineRun, error)             { return nil, nil }
func (r *hRuns) CompletedRunsWithNewActivity() ([]db.PipelineRun, error) { return nil, nil }
func (r *hRuns) RunsToArchive() ([]db.PipelineRun, error)           { return nil, nil }

// hRun embeds db.PipelineRun to inherit stubs for the methods DispatchOne
// never calls (the interface has getters like InstancePipelineID/Params that
// this test does not exercise); only ID/Finish are overridden.
type hRun struct {
	db.PipelineRun
	id int
	h  *harness
}

func (r *hRun) ID() int { return r.id }
func (r *hRun) Finish(status db.PipelineRunStatus) error {
	if status == db.PipelineRunAborted {
		r.h.finishedAborted++
	}
	return nil
}

// hTeam satisfies dispatch.PipelineSaver (the narrow SavePipeline seam
// Deps.Team is typed as — Task 9). The real db.Team also satisfies it.
type hTeam struct{}

func (h *hTeam) SavePipeline(ref atc.PipelineRef, _ atc.Config, _ db.ConfigVersion, _ bool) (db.Pipeline, bool, error) {
	return &hPipeline{}, true, nil
}

// hPipeline embeds db.Pipeline for the unused-method stubs; only ID/Template
// are exercised.
type hPipeline struct{ db.Pipeline }

func (p *hPipeline) ID() int        { return 1 }
func (p *hPipeline) Template() bool { return true }

func itoa2(i int) string { return map[int]string{100: "100", 101: "101"}[i] }
```

Note: `hRun`/`hPipeline` embed the large `db.PipelineRun`/`db.Pipeline` interfaces so only the methods `DispatchOne` calls need real bodies (embedding an interface satisfies the method set with nil-panicking stubs for the rest, which are never called on these paths). `hTeam` implements only `SavePipeline` because `Deps.Team` is the narrow `dispatch.PipelineSaver` interface (Task 9), not `db.Team`.

- [ ] **Step 4: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestDispatchOne`
Expected: FAIL — `undefined: dispatch.DispatchOne` / `dispatch.Outcome`.

- [ ] **Step 5: Write `agent/dispatch/dispatch_one.go`:**

```go
package dispatch

import (
	"context"
	"errors"
	"fmt"

	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// Outcome reports what happened to one ticket in a dispatch pass.
type Outcome string

const (
	OutcomeDispatched Outcome = "dispatched" // claimed, run created, ticket running
	OutcomeDeferred   Outcome = "deferred"   // over-cap; left queued
	OutcomeLostClaim  Outcome = "lost_claim" // another node claimed it first
	OutcomeErrored    Outcome = "errored"    // platform fault; ticket errored
)

// DispatchOne dispatches a single queued ticket. Ordering (contract §4 +
// ticket-core §2.1: the queued->running transition records pipeline_run_id
// from TransitionMeta, so the run must exist before the claim):
//  1. admit against budgets FIRST (over-cap defers, ticket stays queued — no
//     run is created and no claim is attempted);
//  2. resolve the credential + render the template (still queued; failures
//     here error the ticket without a run);
//  3. persist the template + create the run;
//  4. CLAIM via Transition(queued->running, {PipelineRunID}) — this is the
//     atomic multi-node guard AND records the run id in one write.
//     ErrStaleTransition => another node won: clean up the orphan run + secret.
//  5. mint the per-run principal (park-policy workflows — any checkpoint, or
//     ask_timeout: park — expire at now + ParkTimeout instead of RunTimeout;
//     F31 leg) + attach the ephemeral secret, then Patch the
//     dispatch-owned concourse/ticket label onto it (§2.8.2; best-effort — a
//     labeling failure never fails a dispatched run since GC keys off
//     concourse/agent-run alone).
// A platform fault after step 4 transitions running->errored.
func DispatchOne(ctx context.Context, deps Deps, t tickets.Ticket) Outcome {
	// (1) Budget admission. Over-cap defers (stays queued), never fails.
	daily, err := deps.Budget.GlobalDailyRemaining()
	if err != nil {
		return errorQueued(deps, t, fmt.Errorf("budget: %w", err))
	}
	if daily.Exhausted {
		return OutcomeDeferred
	}
	tr, err := deps.Budget.TicketRemaining(t.ID)
	if err != nil {
		return errorQueued(deps, t, fmt.Errorf("budget: %w", err))
	}
	if tr.Exhausted {
		// Ticket budget already spent (e.g. from a prior attempt): a ticket
		// fault, not a transient cap — error it so a human sees it.
		return errorQueued(deps, t, fmt.Errorf("ticket budget exhausted (spent %.2f of %.2f)", tr.SpentUSD, tr.LimitUSD))
	}

	// (2) Resolve credential + render — all while still queued, so a failure
	// errors the ticket without ever creating a run.
	if t.UserID == nil {
		return errorQueued(deps, t, fmt.Errorf("ticket has no triggering user; cannot resolve a credential"))
	}
	cred, err := ResolveUserCredential(deps.Credentials, *t.UserID, nowFor(deps))
	if err != nil {
		return errorQueued(deps, t, err)
	}
	spec, _, err := deps.Tickets.LatestSpec(t.ID)
	if err != nil {
		return errorQueued(deps, t, fmt.Errorf("load spec: %w", err))
	}
	plan, err := deps.Tickets.ActivePlan(t.ID)
	if err != nil {
		return errorQueued(deps, t, fmt.Errorf("load plan: %w", err))
	}
	in, err := deps.Resolver.Resolve(t, spec, plan)
	if err != nil {
		return errorQueued(deps, t, err)
	}
	out, err := Render(in)
	if err != nil {
		return errorQueued(deps, t, fmt.Errorf("render: %w", err))
	}

	// (3) Persist the rendered template + create the run.
	pipeline, _, err := deps.Team.SavePipeline(atc.PipelineRef{Name: PipelineNameForTicket(t.ID)}, out.Config, db.ConfigVersion(0), false)
	if err != nil {
		return errorQueued(deps, t, fmt.Errorf("save rendered template: %w", err))
	}
	run, err := deps.Runs.CreateRun(pipeline.ID(), out.Params, "dispatcher")
	if err != nil {
		return errorQueued(deps, t, fmt.Errorf("create run: %w", err))
	}
	runID := run.ID()

	// (4) Claim. The guarded UPDATE is the multi-node claim AND records the run
	// id (§2.1: TransitionMeta.PipelineRunID persisted on the ->running edge).
	if err := deps.Tickets.Transition(t.ID, tickets.StateQueued, tickets.StateRunning, tickets.TransitionMeta{PipelineRunID: &runID}); err != nil {
		if errors.Is(err, tickets.ErrStaleTransition) || errors.Is(err, tickets.ErrTicketNotFound) {
			// Another node won the claim. The run we created is an orphan:
			// best-effort clean up the ephemeral secret (none attached yet) and
			// finish the run as aborted so the lifecycler reaps it.
			_ = deps.SecretAttach.Cleanup(ctx, runID)
			_ = run.Finish(db.PipelineRunAborted)
			return OutcomeLostClaim
		}
		// Genuine DB error on the claim: error the ticket (it is still queued;
		// the orphan run is reaped as above).
		_ = deps.SecretAttach.Cleanup(ctx, runID)
		_ = run.Finish(db.PipelineRunAborted)
		return errorQueued(deps, t, fmt.Errorf("claim: %w", err))
	}

	// (5) From here on we hold the claim; platform faults => running->errored.
	// Park-policy workflows (any checkpoint step, or hitl.ask_timeout: park)
	// get the --agent-park-timeout expiry so a parked run's principal outlives
	// the 6h run timeout (F31 leg, contracts §8.1 + §3.2 PARK-DURATION BOUNDS).
	principalToken, err := MintRunPrincipal(ctx, deps.Principals, runID,
		RunPrincipalTimeout(in.Workflow, deps.RunTimeout, deps.ParkTimeout))
	if err != nil {
		return errorClaimed(deps, t, err)
	}
	if _, err := deps.SecretAttach.Attach(ctx, runID, cred, principalToken); err != nil {
		return errorClaimed(deps, t, fmt.Errorf("attach run secret: %w", err))
	}
	// Follow-up Patch of the dispatch-owned concourse/ticket label (§2.8.2/§8.2).
	// The reaper GC keys off concourse/agent-run alone, so a labeling failure is
	// non-fatal — log it, but do NOT error a successfully-dispatched run.
	if deps.SecretLabeler != nil && t.ID > 0 {
		if err := deps.SecretLabeler.LabelTicket(ctx, runID, t.ID); err != nil {
			lagerctx.FromContext(ctx).Error("label-run-secret-with-ticket", err,
				map[string]any{"run": runID, "ticket": t.ID})
		}
	}

	return OutcomeDispatched
}

// errorQueued errors a ticket that is still queued (no claim held). A budget
// arithmetic error or a pre-claim platform fault is errored so the ticket does
// not silently spin; the guarded queued->errored transition no-ops safely if
// another node already moved it.
func errorQueued(deps Deps, t tickets.Ticket, cause error) Outcome {
	_ = deps.Tickets.Transition(t.ID, tickets.StateQueued, tickets.StateErrored, tickets.TransitionMeta{ErrorDetail: cause.Error()})
	return OutcomeErrored
}

// errorClaimed errors a ticket we hold the running claim on.
func errorClaimed(deps Deps, t tickets.Ticket, cause error) Outcome {
	_ = deps.Tickets.Transition(t.ID, tickets.StateRunning, tickets.StateErrored, tickets.TransitionMeta{ErrorDetail: cause.Error()})
	return OutcomeErrored
}

func nowFor(deps Deps) time.Time { return timeNow() }
```

- [ ] **Step 6: Add the `timeNow` seam** at the bottom of `agent/dispatch/deps.go`:

```go
// timeNow is overridable in tests.
var timeNow = time.Now
```

And add `"time"` to the `dispatch_one.go` imports (used by `nowFor`).

- [ ] **Step 7: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestDispatchOne`
Expected: PASS (all four DispatchOne cases).

- [ ] **Step 8: Run the whole package**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add agent/dispatch/dispatch_one.go agent/dispatch/dispatch_one_test.go agent/dispatch/dispatch_one_harness_test.go agent/dispatch/dispatch_one_fakes_test.go agent/dispatch/deps.go
git commit -m "feat(dispatch): DispatchOne - claim, budget-admit, resolve, render, create run, error handling" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 11: `Dispatcher` RunnableComponent — the polling+notify loop with attempt caps

Wrap `DispatchOne` in the `Run(ctx) error` RunnableComponent shape (pauser recipe). It lists queued tickets via `tickets.Store.List(ListFilter{State: StateQueued})`, dispatches each, and enforces the attempt cap: a ticket whose `attempt_count >= MaxAttempts` errors instead of dispatching (prevents infinite retry of a poison ticket). The component framework's `Coordinator` lock already serializes across web nodes; the per-ticket `queued→running` guard handles the race inside a pass.

**Files:**
- Create: `agent/dispatch/dispatcher.go`
- Test: `agent/dispatch/dispatcher_test.go`

**Steps:**

- [ ] **Step 1: Write the failing test** `agent/dispatch/dispatcher_test.go`:

```go
package dispatch_test

import (
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
)

func TestDispatcherDispatchesEachQueuedTicket(t *testing.T) {
	queued := []tickets.Ticket{ticketFor(7), ticketFor(8)}
	h := newHarnessMulti(t, queued)
	d := dispatch.NewDispatcher(h.deps())
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.createdRuns != 2 {
		t.Errorf("expected 2 runs created, got %d", h.createdRuns)
	}
}

func TestDispatcherAttemptCapErrors(t *testing.T) {
	over := ticketFor(9)
	over.AttemptCount = 3 // == MaxAttempts
	h := newHarnessMulti(t, []tickets.Ticket{over})
	d := dispatch.NewDispatcher(h.deps())
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.createdRuns != 0 {
		t.Errorf("attempt-capped ticket must not dispatch, got %d runs", h.createdRuns)
	}
	if !h.transitions.saw(tickets.StateQueued, tickets.StateErrored) {
		t.Errorf("attempt-capped ticket must be errored; saw %v", h.transitions.log)
	}
}
```

- [ ] **Step 2: Extend the harness for multi-ticket listing** — append to `agent/dispatch/dispatch_one_harness_test.go`:

```go
func newHarnessMulti(t *testing.T, queued []tickets.Ticket) *harness {
	h := newHarness(t)
	h.queued = queued
	return h
}

func (h *harness) deps() dispatch.Deps {
	return dispatch.Deps{
		Tickets:       &hTicketsList{h: h},
		Resolver:      dispatch.NewRenderResolver(hWorkflowStore(), "img:v1", "https://c.home"),
		Budget:        &hBudget{h: h},
		Credentials:   &hCreds{h: h},
		Principals:    &fakePrincipals{token: "cap1.5.secret"},
		SecretAttach:  &hAttacher{h: h},
		SecretLabeler: &hLabeler{h: h},
		Runs:          &hRuns{h: h},
		Team:          &hTeam{},
		RunTimeout:    time.Hour,
		ParkTimeout:   72 * time.Hour,
		MaxAttempts:   3,
	}
}
```

The `queued []tickets.Ticket` field is already on the `harness` struct (Task 10). Add a listing ticket store in `agent/dispatch/dispatch_one_fakes_test.go`:

```go
type hTicketsList struct{ h *harness }

func (s *hTicketsList) Get(id int) (*tickets.Ticket, bool, error) {
	for _, t := range s.h.queued {
		if t.ID == id {
			tt := t
			return &tt, true, nil
		}
	}
	return nil, false, nil
}
func (s *hTicketsList) List(f tickets.ListFilter) ([]tickets.Ticket, error) { return s.h.queued, nil }
func (s *hTicketsList) Transition(id int, from, to tickets.State, _ tickets.TransitionMeta) error {
	s.h.transitions.record(from, to)
	return nil
}
func (s *hTicketsList) Create(*tickets.Ticket) (int, error)                    { return 0, nil }
func (s *hTicketsList) Update(int, tickets.Update) error                       { return nil }
func (s *hTicketsList) SubmitSpec(int, tickets.Spec) (int, error)              { return 0, nil }
func (s *hTicketsList) SubmitPlan(int, []tickets.Task) (int, error)            { return 0, nil }
func (s *hTicketsList) UpdateTaskStatus(int, int, int, tickets.TaskStatus) error { return nil }
func (s *hTicketsList) AppendTaskNote(int, int, int, string) error            { return nil }
func (s *hTicketsList) ActivePlan(int) ([]tickets.Task, error)                { return nil, nil }
func (s *hTicketsList) LatestSpec(int) (*tickets.Spec, bool, error)           { return nil, false, nil }
```

- [ ] **Step 3: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestDispatcher`
Expected: FAIL — `undefined: dispatch.NewDispatcher`.

- [ ] **Step 4: Write `agent/dispatch/dispatcher.go`:**

```go
package dispatch

import (
	"context"

	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/agent/api/tickets"
)

// Dispatcher is the RunnableComponent (contract §4; atc.ComponentAgentDispatcher).
// It fires on startup, on NOTIFY signals, and on its polling interval (never
// notify-only, per the fork's dropped-notification lesson). The component
// framework's Coordinator lock serializes Run across web nodes; the per-ticket
// queued->running guard in DispatchOne handles intra-pass concurrency.
type Dispatcher struct {
	deps Deps
}

func NewDispatcher(deps Deps) *Dispatcher {
	return &Dispatcher{deps: deps}
}

// Run dispatches every currently-queued ticket. Errors from a single ticket
// never abort the pass — each is isolated (a poison ticket must not starve the
// others). Run returns an error only on a listing failure.
func (d *Dispatcher) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("agent-dispatcher")
	logger.Debug("start")
	defer logger.Debug("done")

	queued, err := d.deps.Tickets.List(tickets.ListFilter{State: tickets.StateQueued})
	if err != nil {
		logger.Error("failed-to-list-queued-tickets", err)
		return err
	}

	for _, t := range queued {
		// Attempt cap: a ticket that has been re-queued MaxAttempts times is a
		// poison ticket — error it (platform gave up), never fail (it did not
		// run badly) and never dispatch again.
		if d.deps.MaxAttempts > 0 && t.AttemptCount >= d.deps.MaxAttempts {
			_ = d.deps.Tickets.Transition(t.ID, tickets.StateQueued, tickets.StateErrored,
				tickets.TransitionMeta{ErrorDetail: "exceeded dispatch attempt cap"})
			logger.Info("attempt-cap-exceeded", lagerData(t.ID, t.AttemptCount))
			continue
		}

		outcome := DispatchOne(ctx, d.deps, t)
		logger.Info("dispatched", map[string]interface{}{"ticket": t.ID, "outcome": string(outcome)})
	}
	return nil
}

func lagerData(ticketID, attempts int) map[string]interface{} {
	return map[string]interface{}{"ticket": ticketID, "attempts": attempts}
}
```

- [ ] **Step 5: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestDispatcher`
Expected: PASS.

- [ ] **Step 6: Run the whole package**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add agent/dispatch/dispatcher.go agent/dispatch/dispatcher_test.go agent/dispatch/dispatch_one_harness_test.go agent/dispatch/dispatch_one_fakes_test.go
git commit -m "feat(dispatch): Dispatcher RunnableComponent loop with attempt-cap poison-ticket handling" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 11b: Run-completion reconciler — `reconcileCompletedRuns` (F17, checkpoint seam delta §6)

*(Added 2026-07-09 per the frozen checkpoint seam delta §6. Milestone 2. F17: a rejected bare checkpoint halts the plan BEFORE the terminal harvest step, so nothing ever transitioned the ticket — `on_reject: fail` stranded it in `running` forever, and `send_back`'s →queued re-dispatch had no signal path at all.)*

The reconciler is a pass on the EXISTING `dispatch.Dispatcher` RunnableComponent (`atc.ComponentAgentDispatcher`; polling+notify, never notify-only) — **no new component constant**. Each `Run` pass, after the queued-dispatch loop, it calls `(d *Dispatcher) reconcileCompletedRuns(ctx, logger)`. Candidates: tickets in `StateRunning` with non-nil `pipeline_run_id` whose `pipeline_runs` row is COMPLETE (`completed_at` set; statuses per §1.5 — a PARKED run counts as running, so the reconciler can never fire mid-park). All ticket writes go through ticket-core's `Transition` (the single writer); `ErrStaleTransition`/`ErrTicketNotFound` are BENIGN — log and continue — harvest may have raced (harvest at 09:94 is the primary writer of the succeeded→needs_review edge; **TWO-WRITERS is now the recorded contract**, the reconciler is the later/backup writer). Ticket-core edge note (delta §7, owned by 06): `validTransitions` ALREADY contains `StateRunning: {StateQueued, …}` — NO matrix change here; the edge annotation broadens to `running → queued (retryable platform error OR rejected send_back checkpoint re-dispatch; attempt_count++)` and 06's matrix test gains a comment naming this reconciler as the second legitimate caller.

Decision tree (frozen, delta §6; keyed off `agent_run_questions.answer` — there is NO `approved` column):
- **(a)** Run succeeded but ticket still running → `Transition(running→needs_review)` (safety net).
- **(b)** Run failed/errored/aborted:
  1. Latest (max `asked_at`) `kind='checkpoint'` row ANSWERED with `answer <> 'approve'` (normative predicate; in practice `'reject'` given the ATC answer route's options validation): resolve the ticket's FROZEN workflow config (`workflow.Store.Get` via the ticket's pinned workflow name/version recorded at dispatch — the resolver's pinned-version path), strip the `checkpoint-` prefix off the row's `step_name`, match `workflow.Config.Steps[].Checkpoint` — `OnReject == "send_back"` → `Transition(running→queued, TransitionMeta{})` (§2.1 bumps `attempt_count`; the loop is capped by Task 11's EXISTING MaxAttempts guard — the reconciler requeues unconditionally); `OnReject == "fail"`, empty, or step not found → `Transition(running→needs_review)`.
  2. Else if ANY checkpoint row is UNANSWERED (`answered_at IS NULL`) — sidecar death, client retry exhaustion, abort while parked → `Transition(running→errored, TransitionMeta{ErrorDetail: 'checkpoint "<name>" unresolved: run completed <status> while parked'})`, AND release each orphaned row via `Answer(id, "", "dispatcher")` so the open-questions index and ticket banner clear (mirrors §3.2's "a timed-out row never stays open").
  3. Else (no checkpoint involvement — agent step crashed, gate blew up pre-harvest, usage-error exit 2, abort) → `Transition(running→needs_review)` (human triage).

**Files:**
- Create: `agent/dispatch/reconcile.go`
- Test: `agent/dispatch/reconcile_test.go`
- Modify: `agent/dispatch/deps.go` (`QuestionLister` seam + `Deps.Questions` + `FactoryQuestionLister` adapter)
- Modify: `agent/dispatch/dispatcher.go` (call the reconciler after the queued loop)
- Modify: `agent/dispatch/dispatch_one_fakes_test.go` (`hRuns.GetRunByID` stub — the factory interface grows)
- Modify: `atc/db/pipeline_run_factory.go` + test (additive `GetRunByID`, co-signed pipeline-runs)
- Depends on (NOT modified here): 08 Task 14b's additive `ListByRun` on `agent/api/questions` + `atc/db/agent_questions_factory.go` — plan 08 owns and fully lands that surface (newest-asked first)
- (Wiring of `Deps.Questions` lands in Task 13's `command.go` block, which runs after this task and was updated 2026-07-09 to include it)

**Steps:**

- [ ] **Step 1: Add the additive by-id run getter (co-signed pipeline-runs).** In `atc/db/pipeline_run_factory.go`, add to the `PipelineRunFactory` interface:

```go
	// GetRunByID fetches a run by its global pipeline_runs.id (additive,
	// 2026-07-09 checkpoint seam delta §6 — consumed by dispatch's
	// run-completion reconciler, which holds run ids from tickets.pipeline_run_id).
	GetRunByID(id int) (PipelineRun, bool, error)
```

Implementation mirrors `GetRun` exactly:

```go
func (f *pipelineRunFactory) GetRunByID(id int) (PipelineRun, bool, error) {
	run := newPipelineRun(f.conn, f.lockFactory)
	err := scanPipelineRun(run, pipelineRunsQuery.
		Where(sq.Eq{"r.id": id}).
		RunWith(f.conn).
		QueryRow())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return run, true, nil
}
```

And add a spec to the pipeline-run factory DB test:

```go
	It("gets a run by its global id (additive for dispatch's reconciler, 2026-07-09)", func() {
		run, err := factory.CreateRun(template.ID(), map[string]any{"ticket_id": 1}, "test")
		Expect(err).ToNot(HaveOccurred())

		got, found, err := factory.GetRunByID(run.ID())
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.Number()).To(Equal(run.Number()))

		_, found, err = factory.GetRunByID(999999)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})
```

Run: `cd /Users/tdmtrader/concourse/concourse && pg_isready && ginkgo --focus="gets a run by its global id" ./atc/db/`
Expected: PASS after the two edits (FAIL to compile before them).

- [ ] **Step 2: Confirm `ListByRun` is landed by 08 Task 14b — nothing to implement here.** The additive `ListByRun(pipelineRunID int) ([]questions.Question, error)` store surface is owned and FULLY landed by 08 Task 14b (co-signed platform-mcp-hitl): the `agent/api/questions` Store interface + `MemoryStore`, the SQL in `atc/db/agent_questions_factory.go`, the regenerated fake, and the tests (`TestListByRun` + the factory It-block). Its frozen ordering contract is **newest-asked first** (`ORDER BY q.asked_at DESC, q.id DESC`; 08's tests assert it — "ordering is part of the contract"). Do NOT re-implement or reorder it in this plan: the reconciler does not depend on store ordering — it filters to `kind='checkpoint'` rows and re-sorts locally by `AskedAt` in `reconcileOne` (Step 6 below), so newest-first at the store is fine.

Run: `cd /Users/tdmtrader/concourse/concourse && grep -n "func (f \*agentQuestionsFactory) ListByRun" atc/db/agent_questions_factory.go && grep -n "ListByRun" agent/api/questions/types.go`
Expected: both hits present. If either is missing, 08 Task 14b has not landed in this worktree — execute it first, then continue here.

- [ ] **Step 3: Write the failing reconciler tests** `agent/dispatch/reconcile_test.go`:

```go
package dispatch_test

import (
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/questions"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db"
)

// --- fakes -----------------------------------------------------------------

type rTickets struct {
	tickets.Store // nil-panic stubs for methods the reconciler never calls
	running       []tickets.Ticket
	log           []string
	metas         []tickets.TransitionMeta
	transErr      error
}

func (s *rTickets) List(f tickets.ListFilter) ([]tickets.Ticket, error) {
	if f.State == tickets.StateRunning {
		return s.running, nil
	}
	return nil, nil // the queued-dispatch loop sees nothing
}

func (s *rTickets) Transition(id int, from, to tickets.State, meta tickets.TransitionMeta) error {
	if s.transErr != nil {
		return s.transErr
	}
	s.log = append(s.log, string(from)+"->"+string(to))
	s.metas = append(s.metas, meta)
	return nil
}

type qAnswer struct {
	id         int
	answer, by string
}

type rQuestions struct {
	rows    []questions.Question
	answers []qAnswer
}

func (q *rQuestions) ListByRun(int) ([]questions.Question, error) { return q.rows, nil }
func (q *rQuestions) Answer(id int, answer, answeredBy string) error {
	q.answers = append(q.answers, qAnswer{id, answer, answeredBy})
	return nil
}

type rRun struct {
	db.PipelineRun
	id       int
	status   db.PipelineRunStatus
	complete bool
}

func (r *rRun) ID() int                      { return r.id }
func (r *rRun) Status() db.PipelineRunStatus { return r.status }
func (r *rRun) CompletedAt() (time.Time, bool) {
	if r.complete {
		return time.Unix(1, 0), true
	}
	return time.Time{}, false
}

type rRuns struct {
	hRuns // Task 10 stubs
	run *rRun
}

func (r *rRuns) GetRunByID(id int) (db.PipelineRun, bool, error) {
	if r.run != nil && r.run.id == id {
		return r.run, true, nil
	}
	return nil, false, nil
}

// --- fixtures ----------------------------------------------------------------

func intp(i int) *int { return &i }

// runningReconcileTicket pins the workflow version (recorded at dispatch) so
// the reconciler resolves the FROZEN config via workflow.Store.Get.
func runningReconcileTicket(id, runID int) tickets.Ticket {
	v := 4
	return tickets.Ticket{ID: id, State: tickets.StateRunning, Repo: "r/x",
		WorkflowName: "standard-dev", WorkflowVersion: &v, PipelineRunID: intp(runID)}
}

func checkpointRow(id int, answered bool, answer string) questions.Question {
	q := questions.Question{
		ID: id, TicketID: 7, PipelineRunID: intp(100), BuildID: 0,
		StepName: "checkpoint-plan-approval", Kind: "checkpoint",
		Options: []string{"approve", "reject"}, AskedAt: int64(10 + id),
	}
	if answered {
		q.AnsweredAt = int64(20 + id)
		q.Answer = answer
		q.AnsweredBy = "tdm"
	}
	return q
}

func reconcileWorkflowStore(onReject string) workflow.Store {
	def := &workflow.Definition{Name: "standard-dev", Version: 4, ContentHash: "h4", Config: workflow.Config{
		Name:    "standard-dev",
		Prompts: map[string]string{"spec": "p"},
		Sidecars: map[string]workflow.Sidecar{
			"dev":      {Image: "i:v1", Role: "dev"},
			"platform": {Image: "ghcr.io/tdmtrader/mcp-platform:v1", Role: "platform"},
		},
		Steps: []workflow.Step{
			{Agent: "a", Prompt: "spec", Sidecars: []string{"dev"}, Outputs: []string{"workspace"}},
			{Checkpoint: "plan-approval", OnReject: onReject},
		},
	}}
	return &fakeWorkflowStore{byV: map[string]*workflow.Definition{"standard-dev/4": def}}
}

func reconcileDeps(ts *rTickets, qs *rQuestions, run *rRun, onReject string) dispatch.Deps {
	return dispatch.Deps{
		Tickets:   ts,
		Resolver:  dispatch.NewRenderResolver(reconcileWorkflowStore(onReject), "img:v1", "https://c.home"),
		Questions: qs,
		Runs:      &rRuns{run: run},
	}
}

// --- specs (frozen decision tree, delta §6) ----------------------------------

func TestReconcileRejectedSendBackRequeues(t *testing.T) {
	ts := &rTickets{running: []tickets.Ticket{runningReconcileTicket(7, 100)}}
	qs := &rQuestions{rows: []questions.Question{checkpointRow(1, true, "reject")}}
	run := &rRun{id: 100, status: db.PipelineRunFailed, complete: true}
	d := dispatch.NewDispatcher(reconcileDeps(ts, qs, run, "send_back"))
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ts.log) != 1 || ts.log[0] != "running->queued" {
		t.Fatalf("rejected send_back checkpoint must requeue (running->queued; attempt_count++ is §2.1's side effect); saw %v", ts.log)
	}
}

func TestReconcileRejectedFailNeedsReview(t *testing.T) {
	ts := &rTickets{running: []tickets.Ticket{runningReconcileTicket(7, 100)}}
	qs := &rQuestions{rows: []questions.Question{checkpointRow(1, true, "reject")}}
	run := &rRun{id: 100, status: db.PipelineRunFailed, complete: true}
	d := dispatch.NewDispatcher(reconcileDeps(ts, qs, run, "fail"))
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ts.log) != 1 || ts.log[0] != "running->needs_review" {
		t.Fatalf("rejected fail checkpoint => needs_review; saw %v", ts.log)
	}
}

func TestReconcileUnknownStepDefaultsToNeedsReview(t *testing.T) {
	// Step not found in the frozen config (e.g. definition drifted) => fail path.
	ts := &rTickets{running: []tickets.Ticket{runningReconcileTicket(7, 100)}}
	row := checkpointRow(1, true, "reject")
	row.StepName = "checkpoint-ghost"
	qs := &rQuestions{rows: []questions.Question{row}}
	run := &rRun{id: 100, status: db.PipelineRunFailed, complete: true}
	d := dispatch.NewDispatcher(reconcileDeps(ts, qs, run, "send_back"))
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ts.log) != 1 || ts.log[0] != "running->needs_review" {
		t.Fatalf("unknown checkpoint step must default to needs_review; saw %v", ts.log)
	}
}

func TestReconcileUnansweredCheckpointErrorsAndReleasesOrphans(t *testing.T) {
	ts := &rTickets{running: []tickets.Ticket{runningReconcileTicket(7, 100)}}
	qs := &rQuestions{rows: []questions.Question{checkpointRow(1, false, "")}}
	run := &rRun{id: 100, status: db.PipelineRunFailed, complete: true}
	d := dispatch.NewDispatcher(reconcileDeps(ts, qs, run, "send_back"))
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ts.log) != 1 || ts.log[0] != "running->errored" {
		t.Fatalf("unanswered checkpoint on a completed run => errored; saw %v", ts.log)
	}
	want := `checkpoint "plan-approval" unresolved: run completed failed while parked`
	if ts.metas[0].ErrorDetail != want {
		t.Errorf("error_detail = %q, want the frozen text %q", ts.metas[0].ErrorDetail, want)
	}
	if len(qs.answers) != 1 || qs.answers[0] != (qAnswer{1, "", "dispatcher"}) {
		t.Errorf(`orphaned rows must be released via Answer(id, "", "dispatcher"); saw %v`, qs.answers)
	}
}

func TestReconcileSucceededSafetyNet(t *testing.T) {
	// Harvest (09:94) is the primary writer of this edge; the reconciler is
	// the backup. TWO-WRITERS is the recorded contract.
	ts := &rTickets{running: []tickets.Ticket{runningReconcileTicket(7, 100)}}
	qs := &rQuestions{}
	run := &rRun{id: 100, status: db.PipelineRunSucceeded, complete: true}
	d := dispatch.NewDispatcher(reconcileDeps(ts, qs, run, "fail"))
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ts.log) != 1 || ts.log[0] != "running->needs_review" {
		t.Fatalf("succeeded-but-still-running => needs_review safety net; saw %v", ts.log)
	}
}

func TestReconcileNoCheckpointFailureNeedsReview(t *testing.T) {
	ts := &rTickets{running: []tickets.Ticket{runningReconcileTicket(7, 100)}}
	qs := &rQuestions{} // no checkpoint involvement: agent crash, gate blowup, exit 2, abort
	run := &rRun{id: 100, status: db.PipelineRunErrored, complete: true}
	d := dispatch.NewDispatcher(reconcileDeps(ts, qs, run, "fail"))
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ts.log) != 1 || ts.log[0] != "running->needs_review" {
		t.Fatalf("checkpoint-free failure => needs_review (human triage); saw %v", ts.log)
	}
}

func TestReconcileSkipsIncompleteRun(t *testing.T) {
	// A PARKED run counts as running (§1.5): completed_at unset => untouched.
	ts := &rTickets{running: []tickets.Ticket{runningReconcileTicket(7, 100)}}
	qs := &rQuestions{rows: []questions.Question{checkpointRow(1, false, "")}}
	run := &rRun{id: 100, status: db.PipelineRunRunning, complete: false}
	d := dispatch.NewDispatcher(reconcileDeps(ts, qs, run, "send_back"))
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ts.log) != 0 {
		t.Fatalf("reconciler must never fire mid-park/mid-run; saw %v", ts.log)
	}
}

func TestReconcileStaleTransitionBenign(t *testing.T) {
	// Harvest raced us: ErrStaleTransition is benign — log and continue, the
	// pass still returns nil.
	ts := &rTickets{running: []tickets.Ticket{runningReconcileTicket(7, 100)}, transErr: tickets.ErrStaleTransition}
	qs := &rQuestions{}
	run := &rRun{id: 100, status: db.PipelineRunSucceeded, complete: true}
	d := dispatch.NewDispatcher(reconcileDeps(ts, qs, run, "fail"))
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("ErrStaleTransition must be benign, got %v", err)
	}
}
```

- [ ] **Step 4: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestReconcile`
Expected: FAIL — `Deps` has no field `Questions`; `dispatch.QuestionLister` undefined.

- [ ] **Step 5: Add the `QuestionLister` seam + adapter to `agent/dispatch/deps.go`:**

```go
import "github.com/concourse/concourse/agent/api/questions"

// QuestionLister is the narrow questions seam the run-completion reconciler
// needs (frozen 2026-07-09 checkpoint seam delta §6). ListByRun is additive
// on 08's questions store, landed entirely by 08 Task 14b (newest-asked
// first is that plan's frozen ordering contract; the reconciler re-sorts
// locally and does not rely on store order). Answer releases orphaned
// checkpoint rows — Answer(id, "", "dispatcher") — so the open-questions
// index and ticket banner clear.
type QuestionLister interface {
	ListByRun(pipelineRunID int) ([]questions.Question, error)
	Answer(id int, answer, answeredBy string) error
}

// questionsStore is the subset of 08's db.AgentQuestionsFactory the adapter
// wraps (its Answer is (ticketID, questionID)-keyed).
type questionsStore interface {
	ListByRun(pipelineRunID int) ([]questions.Question, error)
	Answer(ticketID, questionID int, answer, answeredBy string) error
}

// FactoryQuestionLister adapts 08's (ticketID, questionID)-keyed store to the
// frozen id-only QuestionLister surface. The reconciler only ever Answers
// rows it fetched via ListByRun in the same pass, so the adapter remembers
// each listed row's ticket id. The Dispatcher runs single-threaded per pass
// (component Coordinator lock), so no locking is needed.
type FactoryQuestionLister struct {
	store         questionsStore
	ticketsByQID  map[int]int
}

func NewFactoryQuestionLister(store questionsStore) *FactoryQuestionLister {
	return &FactoryQuestionLister{store: store, ticketsByQID: map[int]int{}}
}

func (l *FactoryQuestionLister) ListByRun(runID int) ([]questions.Question, error) {
	rows, err := l.store.ListByRun(runID)
	for _, q := range rows {
		l.ticketsByQID[q.ID] = q.TicketID
	}
	return rows, err
}

func (l *FactoryQuestionLister) Answer(id int, answer, answeredBy string) error {
	ticketID, ok := l.ticketsByQID[id]
	if !ok {
		return fmt.Errorf("answer question %d: not listed this pass", id)
	}
	return l.store.Answer(ticketID, id, answer, answeredBy)
}

var _ QuestionLister = (*FactoryQuestionLister)(nil)
```

And add to the `Deps` struct (after `Runs`):

```go
	Questions QuestionLister // run-completion reconciler seam (Task 11b); nil disables reconciliation with a logged warning
```

- [ ] **Step 6: Write `agent/dispatch/reconcile.go`:**

```go
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/agent/api/questions"
	"github.com/concourse/concourse/agent/api/tickets"
)

// reconcileCompletedRuns walks StateRunning tickets whose pipeline run has
// COMPLETED (completed_at set; a PARKED run counts as running per §1.5, so
// this can never fire mid-park) and applies the frozen decision tree
// (2026-07-09 checkpoint seam delta §6, F17). It runs at the end of every
// Dispatcher pass, after the queued-dispatch loop. All ticket writes go
// through ticket-core's Transition — the single writer; ErrStaleTransition /
// ErrTicketNotFound are BENIGN (harvest may have raced us: it is the primary
// writer of running->needs_review on success, we are the recorded second).
func (d *Dispatcher) reconcileCompletedRuns(ctx context.Context, logger lager.Logger) {
	logger = logger.Session("reconcile-completed-runs")
	if d.deps.Questions == nil {
		logger.Info("skipped-no-question-lister")
		return
	}

	running, err := d.deps.Tickets.List(tickets.ListFilter{State: tickets.StateRunning})
	if err != nil {
		logger.Error("failed-to-list-running-tickets", err)
		return
	}

	for _, t := range running {
		if t.PipelineRunID == nil {
			continue
		}
		run, found, err := d.deps.Runs.GetRunByID(*t.PipelineRunID)
		if err != nil {
			logger.Error("failed-to-get-run", err, lager.Data{"ticket": t.ID, "run": *t.PipelineRunID})
			continue
		}
		if !found {
			continue
		}
		if _, complete := run.CompletedAt(); !complete {
			continue // still running (or parked — parked counts as running)
		}

		if err := d.reconcileOne(t, string(run.Status())); err != nil {
			if errors.Is(err, tickets.ErrStaleTransition) || errors.Is(err, tickets.ErrTicketNotFound) {
				logger.Debug("benign-race", lager.Data{"ticket": t.ID}) // harvest won; nothing to do
				continue
			}
			logger.Error("failed-to-reconcile-ticket", err, lager.Data{"ticket": t.ID})
		}
	}
}

// reconcileOne applies the delta-§6 decision tree to one ticket whose run
// completed with the given status.
func (d *Dispatcher) reconcileOne(t tickets.Ticket, runStatus string) error {
	// (a) Run succeeded but ticket still running: needs_review safety net.
	if runStatus == "succeeded" {
		return d.deps.Tickets.Transition(t.ID, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{})
	}

	// (b) Run failed/errored/aborted: checkpoint rows decide the path.
	rows, err := d.deps.Questions.ListByRun(*t.PipelineRunID)
	if err != nil {
		return fmt.Errorf("list questions for run %d: %w", *t.PipelineRunID, err)
	}
	var checkpoints []questions.Question
	for _, q := range rows {
		if q.Kind == "checkpoint" {
			checkpoints = append(checkpoints, q)
		}
	}
	sort.Slice(checkpoints, func(i, j int) bool { return checkpoints[i].AskedAt > checkpoints[j].AskedAt })

	// (b.1) Latest checkpoint ANSWERED with answer <> 'approve' (normative
	// predicate — there is NO `approved` column on agent_run_questions; in
	// practice the answer is exactly 'reject' given the ATC answer route's
	// options validation for kind='checkpoint').
	if len(checkpoints) > 0 && checkpoints[0].AnsweredAt != 0 && checkpoints[0].Answer != "approve" {
		if d.onRejectFor(t, checkpoints[0].StepName) == "send_back" {
			// ->queued re-dispatch. §2.1's side effect bumps attempt_count;
			// the loop is capped by Task 11's EXISTING MaxAttempts guard —
			// the reconciler itself requeues unconditionally.
			return d.deps.Tickets.Transition(t.ID, tickets.StateRunning, tickets.StateQueued, tickets.TransitionMeta{})
		}
		// on_reject: fail, empty, or step not found in the frozen config.
		return d.deps.Tickets.Transition(t.ID, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{})
	}

	// (b.2) Any UNANSWERED checkpoint row — sidecar death, client retry
	// exhaustion, abort while parked. Error the ticket; release each orphaned
	// row so the open-questions index and ticket banner clear (mirrors §3.2's
	// "a timed-out row never stays open").
	var unanswered []questions.Question
	for _, q := range checkpoints {
		if q.AnsweredAt == 0 {
			unanswered = append(unanswered, q)
		}
	}
	if len(unanswered) > 0 {
		name := strings.TrimPrefix(unanswered[0].StepName, "checkpoint-")
		detail := fmt.Sprintf("checkpoint %q unresolved: run completed %s while parked", name, runStatus)
		err := d.deps.Tickets.Transition(t.ID, tickets.StateRunning, tickets.StateErrored, tickets.TransitionMeta{ErrorDetail: detail})
		for _, q := range unanswered {
			_ = d.deps.Questions.Answer(q.ID, "", "dispatcher")
		}
		return err
	}

	// (b.3) No checkpoint involvement — agent step crashed, gate blew up
	// pre-harvest, usage-error exit 2, abort: human triage.
	return d.deps.Tickets.Transition(t.ID, tickets.StateRunning, tickets.StateNeedsReview, tickets.TransitionMeta{})
}

// onRejectFor resolves the checkpoint's on_reject from the ticket's FROZEN
// workflow config — workflow.Store.Get via the ticket's pinned workflow
// name/version recorded at dispatch (the resolver's pinned-version path) —
// by stripping the "checkpoint-" prefix off the question row's step_name and
// matching workflow.Config.Steps[].Checkpoint. Unknown step or unresolvable
// config => "" (treated as fail => needs_review).
func (d *Dispatcher) onRejectFor(t tickets.Ticket, stepName string) string {
	in, err := d.deps.Resolver.Resolve(t, nil, nil)
	if err != nil {
		return ""
	}
	name := strings.TrimPrefix(stepName, "checkpoint-")
	for _, s := range in.Workflow.Steps {
		if s.Checkpoint == name {
			return s.OnReject
		}
	}
	return ""
}
```

- [ ] **Step 7: Call the reconciler from `Dispatcher.Run`.** In `agent/dispatch/dispatcher.go`, before the final `return nil`:

```go
	// 2026-07-09 (F17, checkpoint seam delta §6): after the queued-dispatch
	// loop, walk running tickets whose run completed — rejected/unresolved
	// checkpoints and pre-harvest failures would otherwise strand the ticket
	// in `running` forever. Never aborts the pass.
	d.reconcileCompletedRuns(ctx, logger)
```

And add the `GetRunByID` stub to `hRuns` in `agent/dispatch/dispatch_one_fakes_test.go` (the `db.PipelineRunFactory` interface grew in Step 1):

```go
func (r *hRuns) GetRunByID(int) (db.PipelineRun, bool, error) { return nil, false, nil }
```

- [ ] **Step 8: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/`
Expected: PASS (all eight reconcile specs + every earlier dispatch test — the harness tickets fake returns nothing for the `StateRunning` listing, so Tasks 10/11's specs are unaffected).

- [ ] **Step 9: Build check.** `Deps.Questions` is wired in Task 13's `command.go` block (that block was updated 2026-07-09 to include `agentQuestions := db.NewAgentQuestionsFactory(dbConn)` + `Questions: dispatch.NewFactoryQuestionLister(agentQuestions)` — Task 13 runs after this task, so nothing to wire here yet).

Run: `cd /Users/tdmtrader/concourse/concourse && go build ./agent/... ./atc/db/`
Expected: clean.

- [ ] **Step 10: Commit**

```bash
git add agent/dispatch/reconcile.go agent/dispatch/reconcile_test.go agent/dispatch/deps.go agent/dispatch/dispatcher.go agent/dispatch/dispatch_one_fakes_test.go atc/db/pipeline_run_factory.go atc/db/pipeline_run_factory_test.go
git commit -m "feat(dispatch): run-completion reconciler walks tickets whose run finished (F17)" -m "Checkpoint seam delta §6: rejected send_back => running->queued (attempt_count++, MaxAttempts-capped); rejected fail / checkpoint-free failure => needs_review; unanswered checkpoint => errored + orphan rows released; succeeded => needs_review safety net (two-writers with harvest)." -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 12: DB-backed integration test — real render → SavePipeline → CreateRun

Prove the render output actually saves as a valid pipeline and a run is created against real Postgres, using the real `db.NewPipelineRunFactory`, `db.Team.SavePipeline`, and `db.NewAgentWorkflowsFactory`. This exercises the seams the fakes stand in for. Ginkgo (this is an `atc/db`-adjacent test needing the DB fixtures).

**Files:**
- Create: `agent/dispatch/integration/dispatch_integration_test.go`
- Create: `agent/dispatch/integration/integration_suite_test.go`

**Steps:**

- [ ] **Step 1: Write the suite bootstrap** `agent/dispatch/integration/integration_suite_test.go` (mirror `atc/db`'s suite setup — open `atc/db/db_suite_test.go` and copy its `dbConn`/`lockFactory` `BeforeEach`/`AfterEach` wiring verbatim; the exact helper names must match that file):

```go
package integration_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDispatchIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Dispatch Integration Suite")
}
```

- [ ] **Step 2: Write the failing integration spec** `agent/dispatch/integration/dispatch_integration_test.go`:

```go
package integration_test

import (
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Dispatch render -> SavePipeline -> CreateRun", func() {
	It("renders a live workflow into a saved template and creates a run", func() {
		// dbConn, lockFactory, and a team fixture come from the copied suite
		// bootstrap (Step 1). Create a team the same way atc/db specs do.
		team := createTeamFixture() // helper copied from atc/db suite

		def := &workflow.Definition{
			Name: "standard-dev", Version: 1, ContentHash: "h1",
			Config: workflow.Config{
				Name:     "standard-dev",
				Budget:   workflow.Budget{TicketUSD: 15, JudgeUSD: 1},
				Prompts:  map[string]string{"spec": "p"},
				Sidecars: map[string]workflow.Sidecar{"dev": {Image: "i:v1", Role: "dev"}},
				Steps:    []workflow.Step{{Agent: "a", Prompt: "spec", Sidecars: []string{"dev"}, Outputs: []string{"workspace"}}},
				GatePolicy: workflow.GatePolicy{Gates: []workflow.Gate{{Gate: "build", Scope: "affected"}}, OnGateFailure: "needs_review"},
			},
		}
		store := &staticWorkflowStore{live: def}
		resolver := dispatch.NewRenderResolver(store, "ghcr.io/tdmtrader/agent-runner:v1", "https://c.home")

		tkt := tickets.Ticket{ID: 42, Repo: "tdmtrader/concourse", TargetBranch: "main", WorkflowName: "standard-dev"}
		in, err := resolver.Resolve(tkt, nil, nil)
		Expect(err).ToNot(HaveOccurred())
		out, err := dispatch.Render(in)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.Config.Template).To(BeTrue())

		pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: dispatch.PipelineNameForTicket(42)}, out.Config, db.ConfigVersion(0), false)
		Expect(err).ToNot(HaveOccurred())
		Expect(pipeline.Template()).To(BeTrue())

		runFactory := db.NewPipelineRunFactory(dbConn, lockFactory)
		run, err := runFactory.CreateRun(pipeline.ID(), out.Params, "dispatcher")
		Expect(err).ToNot(HaveOccurred())
		Expect(run.Number()).To(Equal(1))
		Expect(run.Status()).To(Equal(db.PipelineRunRunning))
	})
})

type staticWorkflowStore struct{ live *workflow.Definition }

func (s *staticWorkflowStore) Import(string, []byte, string) (*workflow.Definition, error) { return nil, nil }
func (s *staticWorkflowStore) Get(string, int) (*workflow.Definition, bool, error)         { return s.live, true, nil }
func (s *staticWorkflowStore) Live(string) (*workflow.Definition, bool, error)             { return s.live, true, nil }
func (s *staticWorkflowStore) List() ([]workflow.Definition, error)                        { return nil, nil }
func (s *staticWorkflowStore) Versions(string) ([]workflow.Definition, error)              { return nil, nil }
func (s *staticWorkflowStore) Promote(string, int, string) error                           { return nil }
```

- [ ] **Step 3: Run to verify failure**

Run: `cd /Users/tdmtrader/concourse/concourse && pg_isready && ginkgo ./agent/dispatch/integration/`
Expected: FAIL — compile error on `createTeamFixture`/`dbConn`/`lockFactory` until Step 1's bootstrap is fully copied from `atc/db/db_suite_test.go`. Fix by copying the exact fixture helpers (team creation, `dbConn`, `lockFactory`) from that suite into `integration_suite_test.go`.

- [ ] **Step 4: Implement the suite bootstrap** by copying the `dbConn`/`lockFactory`/team-fixture setup from `atc/db/db_suite_test.go` into `integration_suite_test.go`. Read that file first (`sed -n` its `var (` block and `BeforeEach`), then replicate the postgres connection + `db.NewTeamFactory(...).CreateTeam(...)` calls, exposing `dbConn`, `lockFactory`, and a `createTeamFixture()` returning a `db.Team`.

- [ ] **Step 5: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && pg_isready && ginkgo ./agent/dispatch/integration/`
Expected: PASS (the rendered template saves as `template: true` and a run is created).

- [ ] **Step 6: Commit**

```bash
git add agent/dispatch/integration
git commit -m "test(dispatch): DB-backed render -> SavePipeline -> CreateRun integration" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 13: `atc.ComponentAgentDispatcher` constant + web wiring

Register the dispatcher as a component (name constant + `RunnableComponent` entry in `command.go`) so it runs with polling+notify. It needs a K8s clientset for `SecretAttacher`, so it wires only inside the `cmd.Kubernetes.Namespace != ""` block (like the registrar).

**Files:**
- Modify: `atc/component.go:26` (add `ComponentAgentDispatcher`)
- Modify: `atc/atccmd/command.go` (construct deps + append `RunnableComponent` inside the K8s block, after the reaper at ~:1315)
- Test: existing `atc/atccmd` build + `go vet` (component wiring is exercised by the live test in Task 14; a unit test here would require standing up the entire ATC command, which the codebase does not do for other components)

**Steps:**

- [ ] **Step 1: Add the component constant.** In `atc/component.go`, after `ComponentK8sWorkerReaper` (:24):

```go
	ComponentAgentDispatcher            = "agent_dispatcher"
```

- [ ] **Step 2: Verify the constant compiles**

Run: `cd /Users/tdmtrader/concourse/concourse && go build ./atc/`
Expected: clean build.

- [ ] **Step 3: Confirm the team-factory + main-team lookup surface in `command.go`.** The dispatcher's `SavePipeline` needs the ticket's *team*; v1 pins all agent tickets to the `main` team (contracts §1.7 `team_name` default + decision-21's main-team authorization). Find how this function already gets a team factory and the main-team constant:

Run: `cd /Users/tdmtrader/concourse/concourse && grep -n "dbTeamFactory\|NewTeamFactory\|atc.DefaultTeamName\|FindTeam(" atc/atccmd/command.go | head`

Use whatever `dbTeamFactory` variable + `FindTeam` signature already exist. If no team factory is in scope in this function, construct one: `dbTeamFactory := db.NewTeamFactory(dbConn, lockFactory)` (match the exact constructor signature the grep shows).

- [ ] **Step 4: Wire the dispatcher in `command.go`.** Read the K8s block first to anchor the insertion:

Run: `cd /Users/tdmtrader/concourse/concourse && sed -n '1300,1340p' atc/atccmd/command.go`

Then, immediately after the `k8sReaper` `RunnableComponent` append (after the block ending near :1315, before the closing `}` of the `if cmd.Kubernetes.Namespace != ""` block), insert:

```go
		mainTeam, found, err := dbTeamFactory.FindTeam(atc.DefaultTeamName)
		if err != nil {
			return nil, fmt.Errorf("dispatcher: find main team: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("dispatcher: main team not found")
		}

		agentTickets := db.NewAgentTicketsFactory(dbConn)
		agentWorkflows := db.NewAgentWorkflowsFactory(dbConn)
		agentCredentials := db.NewAgentUserCredentialsFactory(dbConn)
		agentPrincipals := db.NewAgentPrincipalsFactory(dbConn)
		agentLedger := db.NewAgentCostLedgerFactory(dbConn)
		agentQuestions := db.NewAgentQuestionsFactory(dbConn) // reconciler seam (Task 11b, 2026-07-09)
		agentRunFactory := db.NewPipelineRunFactory(dbConn, lockFactory)

		dispatchResolver := dispatch.NewRenderResolver(agentWorkflows, cmd.AgentStepImage, cmd.ExternalURL.String())
		dispatchBudgets := dispatch.NewTicketBudgets(agentTickets, dispatchResolver)
		dispatchChecker := budget.NewChecker(agentLedger, dispatchBudgets, budget.Config{
			GlobalDailyCapUSD: cmd.AgentDailyBudgetUSD,
		})
		secretAttacher := credentials.NewK8sSecretAttacher(k8sClientset, cmd.Kubernetes.Namespace)
		secretLabeler := dispatch.NewK8sRunSecretLabeler(k8sClientset, cmd.Kubernetes.Namespace)

		components = append(components, RunnableComponent{
			Component: atc.Component{
				Name: atc.ComponentAgentDispatcher,
			},
			Runnable: dispatch.NewDispatcher(dispatch.Deps{
				Tickets:       agentTickets,
				Resolver:      dispatchResolver,
				Budget:        dispatchChecker,
				Credentials:   agentCredentials,
				Principals:    agentPrincipals,
				SecretAttach:  secretAttacher,
				SecretLabeler: secretLabeler,
				Runs:          agentRunFactory,
				Team:          mainTeam,
				Questions:     dispatch.NewFactoryQuestionLister(agentQuestions), // run-completion reconciler (Task 11b, 2026-07-09)
				RunTimeout:    cmd.AgentRunTimeout,
				ParkTimeout:   cmd.AgentParkTimeout, // park-policy principal expiry (F31 leg, contracts §8.1)
				MaxAttempts:   3,
			}),
			Interval: 10 * time.Second, // polling backstop; NOTIFY on ticket queue wakes it sooner
		})
```

Note: `db.NewAgentUserCredentialsFactory(dbConn)` returns a type embedding `credentials.Backend` (which embeds `credentials.Store`), so it satisfies the `dispatch.CredentialResolver` interface (`Resolve(int, string)`) directly. `db.NewAgentPrincipalsFactory(dbConn)` returns a `principals.Store`, satisfying `dispatch.PrincipalMinter` (`Create`). `mainTeam` (`db.Team`) satisfies `dispatch.PipelineSaver` via its `SavePipeline` method.

- [ ] **Step 5: Add the `--agent-run-timeout` and `--agent-park-timeout` flags.** In the `RunCommand` struct near the other agent flags (find with `grep -n "AgentStepImage\|AgentDailyBudgetUSD" atc/atccmd/command.go`), add both, adjacent (contracts §8.1 requires `--agent-park-timeout` defined beside `--agent-run-timeout`):

```go
	AgentRunTimeout  time.Duration `long:"agent-run-timeout" default:"6h" description:"Max wall-clock for one agent ticket run; the per-run principal token and ephemeral secret expire after this."`
	AgentParkTimeout time.Duration `long:"agent-park-timeout" default:"72h" description:"Principal expiry for runs whose workflow contains a park-policy step (any checkpoint, or ask_human with ask_timeout: park) — the hard backstop on how long a run may stay parked awaiting a human. Never NULL; a park outliving it fails loudly (F31)."`
```

- [ ] **Step 6: Add imports.** Ensure `command.go`'s import block has `"github.com/concourse/concourse/agent/dispatch"`, `"github.com/concourse/concourse/agent/budget"`, `"github.com/concourse/concourse/agent/credentials"`. Confirm which are already imported (credentials-and-budgets' wiring likely added `budget`/`credentials`):

Run: `cd /Users/tdmtrader/concourse/concourse && grep -n "agent/budget\|agent/credentials\|agent/dispatch" atc/atccmd/command.go`

Add only the missing ones.

- [ ] **Step 7: Build + vet**

Run: `cd /Users/tdmtrader/concourse/concourse && go build ./atc/... && go vet ./atc/atccmd/`
Expected: clean build. If `dispatch.Deps.Team` type mismatches (the field is `db.Team` but the narrow `PipelineSaver` seam was adopted in Task 10), set `Team: mainTeam` still satisfies `db.Team`; the `DispatchOne` type-assert to `PipelineSaver` succeeds because `db.Team` includes `SavePipeline`.

- [ ] **Step 8: Commit**

```bash
git add atc/component.go atc/atccmd/command.go
git commit -m "feat(atc): wire agent_dispatcher RunnableComponent (K8s-gated) with budget/credential/render deps" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 14: Live theborg end-to-end dispatch test

The charter's ships-value: setting a ticket to `queued` is the whole human action. This live test (gated `//go:build live`, theborg pattern per CLAUDE.md) creates a ticket, marks it queued, runs one `Dispatcher.Run` pass against a throwaway namespace, and asserts the ticket transitioned to `running`, a `pipeline_runs` row exists, and the ephemeral secret was created. Fake clientsets cannot exercise real `SavePipeline` + K8s secret creation together, so this needs a live cluster. *(Extended 2026-07-09 per the frozen checkpoint seam delta §9: Steps 4b–4c add BOTH checkpoint-reject cases — (a) `send_back` rejected → ticket observed back in `queued` with `attempt_count=1`, then re-dispatched; (b) `fail` rejected → ticket in `needs_review`.)*

**Files:**
- Create: `agent/dispatch/live_dispatch_test.go`
- Create: `agent/dispatch/integration/checkpoint_reject_live_test.go` (Steps 4b–4c; lives in the integration suite because it needs Task 12's real-Postgres bootstrap AND the theborg cluster)

**Steps:**

- [ ] **Step 1: Write the live test** `agent/dispatch/live_dispatch_test.go` (build-tagged; connects via the `kubeClient(t)`/throwaway-namespace pattern from `atc/worker/jetbridge/live_test.go`):

```go
//go:build live

package dispatch_test

import (
	"context"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/dispatch"
)

// TestLiveDispatchAttachesSecretAndCreatesRun proves the credential/secret
// half of dispatch against a real cluster: a real K8sSecretAttacher creates
// agent-run-<id> with the two keys, labeled for reaper GC. The render/run half
// is covered by the DB integration test (Task 12); this test isolates the
// K8s-only behavior a fake clientset cannot verify (real API server labels,
// idempotency, cleanup).
func TestLiveDispatchAttachesSecretAndCreatesRun(t *testing.T) {
	clientset := kubeClientLive(t) // copied from live_test.go kubeClient
	ns := ensureThrowawayNamespace(t, clientset)
	attacher := credentials.NewK8sSecretAttacher(clientset, ns)

	ctx := context.Background()
	cred := &credentials.Credential{UserID: 1, UserName: "livetest", Kind: credentials.KindAnthropicOAuth, Token: "sk-live-token"}

	name, err := attacher.Attach(ctx, 424242, cred, "cap1.9.principal-secret")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	t.Cleanup(func() { _ = attacher.Cleanup(context.Background(), 424242) })

	sec, err := clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if string(sec.Data["anthropic-token"]) != "sk-live-token" || string(sec.Data["principal-token"]) != "cap1.9.principal-secret" {
		t.Errorf("secret keys wrong: %v", sec.Data)
	}
	if sec.Labels["concourse/agent-run"] != "424242" {
		t.Errorf("missing reaper GC label: %v", sec.Labels)
	}

	// Idempotency: a second Attach must not error (dispatch retries safely).
	if _, err := attacher.Attach(ctx, 424242, cred, "cap1.9.principal-secret"); err != nil {
		t.Fatalf("re-Attach must be idempotent: %v", err)
	}

	// Cleanup on error paths must be tolerant of not-found.
	if err := attacher.Cleanup(ctx, 999999); err != nil {
		t.Fatalf("Cleanup of nonexistent must not error: %v", err)
	}

	_ = os.Getenv // silence unused import in some build configs
	_ = time.Second
	_ = tickets.StateQueued
	_ = dispatch.PipelineNameForTicket
}
```

- [ ] **Step 2: Add the live helpers** at the bottom of `agent/dispatch/live_dispatch_test.go` — copy `kubeClient`/namespace-creation from `atc/worker/jetbridge/live_test.go` (rename to `kubeClientLive`/`ensureThrowawayNamespace` to avoid collision), including `KUBECONFIG`/`~/.kube/config`/in-cluster resolution and `K8S_TEST_NAMESPACE` (default a random `dispatch-live-<ts>` throwaway ns, NEVER `cicd`/`concourse`). `t.Cleanup` deletes the namespace.

Read the source first: `sed -n '1,90p' atc/worker/jetbridge/live_test.go`.

- [ ] **Step 3: Confirm it compiles under the live tag (no cluster needed to compile)**

Run: `cd /Users/tdmtrader/concourse/concourse && go vet -tags live ./agent/dispatch/`
Expected: clean (compiles; does not run without a cluster).

- [ ] **Step 4: Run against theborg** (per CLAUDE.md live-test invocation; requires a throwaway namespace):

```bash
KUBECONFIG=~/.kube/config K8S_TEST_NAMESPACE=dispatch-live-$$ \
  go test -tags live -run '^TestLiveDispatchAttachesSecretAndCreatesRun$' -v -count=1 -timeout 5m ./agent/dispatch/
```

Expected: PASS against kube-context `theborg`. Verify the secret was labeled and cleaned up (`kubectl get secret -n dispatch-live-<pid>` shows nothing after the run).

- [ ] **Step 4b: Write the checkpoint-reject live spec** *(added 2026-07-09, checkpoint seam delta §9)* — `agent/dispatch/integration/checkpoint_reject_live_test.go`. It exercises dispatch's whole half of the reject seam with the REAL stores (tickets/workflows/questions/runs/team on Postgres) and the REAL theborg secret attacher; the parked-pod half (client POST → sidecar files the row → human answers) is proven by 08's own live checkpoint test, so this spec stands in for the failing checkpoint task by filing + answering the `kind='checkpoint'` row and finishing the run failed — exactly the DB state a rejected checkpoint leaves behind. Skips unless `DISPATCH_LIVE_K8S=1` (needs the cluster for the re-dispatch attach):

```go
package integration_test

import (
	"context"
	"os"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/api/questions"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// checkpointWorkflowYAML declares one agent step + one checkpoint whose
// on_reject is interpolated per case. The platform sidecar is REQUIRED by the
// F36 render guard.
const checkpointWorkflowYAML = `
schema_version: 1
name: ckpt-live
defaults: {model: claude-sonnet-4-5, max_turns: 10}
budget: {ticket_usd: 5.0}
sidecars:
  dev: {image: ghcr.io/tdmtrader/mcp-dev-concourse:v1, role: dev}
  platform: {image: ghcr.io/tdmtrader/mcp-platform:v1, role: platform}
prompts: {spec: "read the ticket"}
steps:
- {agent: write-spec, prompt: spec, sidecars: [dev, platform], budget_slice_usd: 1.0, outputs: [workspace]}
- {checkpoint: plan-approval, on_reject: %s}
`

type liveCreds struct{}

func (liveCreds) Resolve(userID int, kind string) (*credentials.Credential, bool, error) {
	return &credentials.Credential{UserID: userID, UserName: "livetest", Kind: kind, Token: "sk-live"}, true, nil
}

type livePrincipals struct{}

func (livePrincipals) Create(spec principals.CreateSpec) (principals.Principal, string, error) {
	return principals.Principal{ID: 1, Name: spec.Name}, "cap1.live.tok", nil
}

var _ = Describe("Checkpoint reject paths (live)", func() {
	BeforeEach(func() {
		if os.Getenv("DISPATCH_LIVE_K8S") != "1" {
			Skip("set DISPATCH_LIVE_K8S=1 (and KUBECONFIG for theborg) to run the checkpoint reject live spec")
		}
	})

	// dispatchAndReject drives: queued -> Dispatcher.Run (dispatch) -> file +
	// reject the checkpoint row (standing in for the parked pod, proven live
	// by 08) -> finish the run failed -> Dispatcher.Run (reconcile pass).
	dispatchAndReject := func(onReject string) (dispatch.Deps, *dispatch.Dispatcher, int, int) {
		team := createTeamFixture()
		ticketStore := db.NewAgentTicketsFactory(dbConn)
		workflowStore := db.NewAgentWorkflowsFactory(dbConn)
		questionStore := db.NewAgentQuestionsFactory(dbConn)
		runFactory := db.NewPipelineRunFactory(dbConn, lockFactory) // match 03's landed constructor (F27 added logger+checkFactory params)
		clientset := liveKubeClientset()                            // helper: same KUBECONFIG/theborg resolution as live_dispatch_test.go
		ns := liveThrowawayNamespace(clientset)

		def, err := workflowStore.Import("ckpt-live", []byte(fmt.Sprintf(checkpointWorkflowYAML, onReject)), "livetest")
		Expect(err).ToNot(HaveOccurred())
		Expect(workflowStore.Promote("ckpt-live", def.Version, "livetest")).To(Succeed())

		uid := 1
		tkt := &tickets.Ticket{Title: "ckpt live " + onReject, Repo: "tdmtrader/concourse", TargetBranch: "main",
			WorkflowName: "ckpt-live", UserID: &uid}
		ticketID, err := ticketStore.Create(tkt)
		Expect(err).ToNot(HaveOccurred())
		Expect(ticketStore.Transition(ticketID, tickets.StateDraft, tickets.StateQueued, tickets.TransitionMeta{})).To(Succeed())

		resolver := dispatch.NewRenderResolver(workflowStore, "ghcr.io/tdmtrader/agent-runner:v1", "https://concourse.home")
		deps := dispatch.Deps{
			Tickets:      ticketStore,
			Resolver:     resolver,
			Budget:       budget.NewChecker(db.NewAgentCostLedgerFactory(dbConn), dispatch.NewTicketBudgets(ticketStore, resolver), budget.Config{GlobalDailyCapUSD: 100}),
			Credentials:  liveCreds{},
			Principals:   livePrincipals{},
			SecretAttach: credentials.NewK8sSecretAttacher(clientset, ns),
			Runs:         runFactory,
			Team:         team,
			Questions:    dispatch.NewFactoryQuestionLister(questionStore),
			RunTimeout:   time.Hour,
			ParkTimeout:  72 * time.Hour, // checkpoint workflow => park-policy expiry (F31); zero here would mint an already-expired principal
			MaxAttempts:  3,
		}
		d := dispatch.NewDispatcher(deps)

		// Pass 1: dispatch.
		Expect(d.Run(context.Background())).To(Succeed())
		claimed, found, err := ticketStore.Get(ticketID)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(claimed.State).To(Equal(tickets.StateRunning))
		Expect(claimed.PipelineRunID).ToNot(BeNil())
		runID := *claimed.PipelineRunID

		// Stand-in for the parked checkpoint pod: file + reject the row.
		q := &questions.Question{TicketID: ticketID, PipelineRunID: &runID, BuildID: 0,
			StepName: "checkpoint-plan-approval", Kind: "checkpoint",
			Question: `Approve checkpoint "plan-approval"?`, Options: []string{"approve", "reject"},
			TimeoutPolicy: "park", TimeoutSeconds: 0}
		qID, err := questionStore.Ask(q)
		Expect(err).ToNot(HaveOccurred())
		Expect(questionStore.Answer(ticketID, qID, "reject", "livetest")).To(Succeed())

		// The rejected client exits 1 => task fails => run completes failed.
		run, found, err := runFactory.GetRunByID(runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(run.Finish(db.PipelineRunFailed)).To(Succeed())

		// Pass 2: reconcile.
		Expect(d.Run(context.Background())).To(Succeed())
		return deps, d, ticketID, runID
	}

	It("(a) send_back reject re-queues with attempt_count=1, then re-dispatches", func() {
		deps, d, ticketID, firstRunID := dispatchAndReject("send_back")

		after, _, err := deps.Tickets.Get(ticketID)
		Expect(err).ToNot(HaveOccurred())
		Expect(after.State).To(Equal(tickets.StateQueued), "rejected send_back checkpoint must land back in queued")
		Expect(after.AttemptCount).To(Equal(1), "§2.1 running->queued side effect must bump attempt_count")

		// Pass 3: the re-queued ticket re-dispatches under the same template.
		Expect(d.Run(context.Background())).To(Succeed())
		again, _, err := deps.Tickets.Get(ticketID)
		Expect(err).ToNot(HaveOccurred())
		Expect(again.State).To(Equal(tickets.StateRunning))
		Expect(again.PipelineRunID).ToNot(BeNil())
		Expect(*again.PipelineRunID).ToNot(Equal(firstRunID), "re-dispatch must create a NEW run")
	})

	It("(b) fail reject lands the ticket in needs_review", func() {
		deps, _, ticketID, _ := dispatchAndReject("fail")

		after, _, err := deps.Tickets.Get(ticketID)
		Expect(err).ToNot(HaveOccurred())
		Expect(after.State).To(Equal(tickets.StateNeedsReview), "rejected fail checkpoint => needs_review, never a silent approve")
		Expect(after.AttemptCount).To(Equal(0), "fail path must not bump attempt_count")
	})
})
```

Add `liveKubeClientset()`/`liveThrowawayNamespace()` helpers to the integration suite bootstrap (same `KUBECONFIG`/theborg resolution and throwaway-namespace pattern as Step 2's `kubeClientLive`/`ensureThrowawayNamespace`; `DeferCleanup` deletes the namespace), and `"fmt"`/`"time"` to the imports.

- [ ] **Step 4c: Run the reject cases against theborg + local Postgres**

```bash
cd /Users/tdmtrader/concourse/concourse && pg_isready && \
  DISPATCH_LIVE_K8S=1 KUBECONFIG=~/.kube/config \
  ginkgo --focus="Checkpoint reject paths" ./agent/dispatch/integration/
```

Expected: PASS — (a) ends `queued` with `attempt_count=1` after the reconcile pass and `running` with a NEW `pipeline_run_id` after the re-dispatch pass; (b) ends `needs_review`. Without `DISPATCH_LIVE_K8S=1` both specs skip (so `make test-integration` stays hermetic).

- [ ] **Step 5: Commit**

```bash
git add agent/dispatch/live_dispatch_test.go agent/dispatch/integration/checkpoint_reject_live_test.go agent/dispatch/integration/integration_suite_test.go
git commit -m "test(dispatch): live theborg secret-attach proof + both checkpoint reject cases" -m "Checkpoint seam delta §9: send_back reject => queued (attempt_count=1) => re-dispatched; fail reject => needs_review." -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 15: Retire the wave-3 hand-written template (documentation + verification)

The scaffolding decision retires exactly one artifact: the hand-written `template: true` pipeline that closed the loop in wave 3. Point the ops doc at `fly run-pipeline` on a dispatcher-rendered template, and add a smoke check proving hand-dispatch still works (the renderer output is `fly run-pipeline`-compatible, so the wave-3 manual path is preserved, just now auto-generated).

**Files:**
- Create: `agent/dispatch/README.md`
- Test: `agent/dispatch/render_test.go` (add a hand-dispatch compatibility assertion)

**Steps:**

- [ ] **Step 1: Add the hand-dispatch compatibility test** to `agent/dispatch/render_test.go`:

```go
func TestRenderedTemplateIsHandDispatchable(t *testing.T) {
	// The rendered template must be a valid pipeline a human can hand-dispatch
	// via `fly run-pipeline` — the wave-3 manual path, now auto-generated.
	raw, err := os.ReadFile(filepath.Join("testdata", "standard-dev.yml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := workflow.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	in := dispatch.RenderInput{Workflow: *cfg, AgentStepImage: "img:v1", ATCExternalURL: "u"}
	in.Ticket = tickets.Ticket{ID: 1, Repo: "r/x", TargetBranch: "main"}
	out, err := dispatch.Render(in)
	if err != nil {
		t.Fatal(err)
	}
	// A hand-dispatcher supplies ticket_id via -v; assert the param exists and
	// is required so `fly run-pipeline` prompts for it.
	var found bool
	for _, p := range out.Config.Params {
		if p.Name == "ticket_id" && p.Required {
			found = true
		}
	}
	if !found {
		t.Error("rendered template must declare a required ticket_id param for hand-dispatch")
	}
}
```

Add `"os"`, `"path/filepath"` to the test file imports if not already present (Task 6 added them in `golden_test.go`, a separate file — `render_test.go` needs its own).

- [ ] **Step 2: Run to verify pass**

Run: `cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/ -run TestRenderedTemplateIsHandDispatchable`
Expected: PASS.

- [ ] **Step 3: Write `agent/dispatch/README.md`:**

````markdown
# agent/dispatch — workflow renderer + dispatcher

Two libraries:

- **Renderer** (`Render`, `RenderResolver`): pure function turning a live
  workflow-definition version + a ticket into a self-contained `template: true`
  pipeline. Steps never read `agent_workflow_definitions` — everything resolves
  at render time (contracts §2.8, §2.8.2).
- **Dispatcher** (`Dispatcher`, `DispatchOne`): the `agent_dispatcher`
  RunnableComponent. Marking a ticket `queued` is the whole human action.

## Hand-dispatch (retires the wave-3 hand-written template)

Before wave 4 the loop was closed by a hand-written `template: true` YAML
dispatched with `fly run-pipeline`. That YAML is retired: the renderer emits an
equivalent, and a human can still hand-dispatch by rendering + saving one:

1. `fly set-pipeline` the dispatcher-rendered config (or let the dispatcher save it).
2. `fly run-pipeline -p agent-ticket-<id> -v ticket_id=<id>`

The only retired artifact is the pipeline YAML; no code path is removed. The
dispatcher automates exactly this call.

## Tests

- `go test ./agent/dispatch/` — pure renderer + dispatcher-loop unit tests + golden files.
- `ginkgo ./agent/dispatch/integration/` — DB-backed render → SavePipeline → CreateRun (needs Postgres).
- `go test -tags live -run TestLiveDispatch ./agent/dispatch/` — theborg secret-attach proof.
- Regenerate goldens: `go test ./agent/dispatch/ -run TestRenderGolden -update`.
````

- [ ] **Step 4: Commit**

```bash
git add agent/dispatch/README.md agent/dispatch/render_test.go
git commit -m "docs(dispatch): retire wave-3 hand-written template; hand-dispatch compatibility test + README" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Execution notes

**Full test suite for this workstream:**

```bash
# 1. Pure renderer + dispatcher-loop unit tests (fast, no deps):
cd /Users/tdmtrader/concourse/concourse && go test ./agent/dispatch/

# 2. DB-backed integration (needs local PostgreSQL — check `pg_isready`).
#    The checkpoint-reject live specs inside it self-skip unless
#    DISPATCH_LIVE_K8S=1 (Task 14 Steps 4b-4c), keeping this run hermetic:
pg_isready && ginkgo ./agent/dispatch/integration/

# 2b. Runtime-seam tests added 2026-07-09 (Task 4b — sidecar secretKeyRef env):
go test ./atc/ && go test ./atc/worker/jetbridge/ -run TestSidecarSecretEnv

# 3. atc build/vet after the command.go wiring (Task 13):
go build ./atc/... && go vet ./atc/atccmd/

# 4. Regenerate golden files after an intentional renderer change:
go test ./agent/dispatch/ -run TestRenderGolden -update
# then eyeball the diff before committing.
```

Do NOT use `--race` with ginkgo per CLAUDE.md (parallel compilation failures). The `agent/dispatch` package is plain `go test` (stdlib-only renderer + fakes); the `integration/` subpackage is Ginkgo because it needs the `atc/db` suite fixtures.

**Live-test requirements (theborg pattern, per CLAUDE.md / MEMORY.md):**
- `//go:build live`, run with `go test -tags live -run '^TestLiveDispatch...$' -v -count=1 -timeout 5m ./agent/dispatch/`.
- Connect via `KUBECONFIG=~/.kube/config` (kube-context `theborg` → https://theborg.home:6443). Create a THROWAWAY namespace (`K8S_TEST_NAMESPACE=dispatch-live-<pid>`), NEVER `cicd`/`concourse` (live workloads). `t.Cleanup` deletes the secret then the namespace.
- Colima/Docker is usually down on this machine, so testcontainers is not an option — use theborg.
- Task 14 isolates the K8s-only behavior (real API-server secret labels, idempotency, not-found-tolerant cleanup) that a fake clientset returns instantly and therefore cannot verify.

**Rollback notes for the risky diffs:**
- **Task 13 (`command.go` wiring)** is the only edit outside the new `agent/dispatch` package. It is additive and K8s-gated: the whole `RunnableComponent` append lives inside `if cmd.Kubernetes.Namespace != ""`, so a non-K8s web node is unaffected. Rollback = delete the appended block + the `ComponentAgentDispatcher` constant + the `--agent-run-timeout` and `--agent-park-timeout` flags. No migrations, no schema, no route changes to revert (dispatch owns none).
- **Task 1 (contract addendum)** is doc-only and additive to §11; rollback = drop the §2.8.2 subsection and the amendment-log line. Consumers of §2.8.2 (process-intel-experiments, wave 5) have not planned against it yet, so the blast radius is zero within wave 4.
- **Task 2 `workflow.Config.Version` field** (if added): it is `yaml:"-" json:"-"` so it never affects the content hash or wire format — safe. The field is added in Task 2 (Step 4) because Task 3's renderer is the first code to read `in.Workflow.Version`; Task 7 only relies on it. If workflow-store already carries a version pass-through, skip that edit.
- **Poison-ticket handling (Task 11):** the attempt cap errors a ticket rather than re-queuing forever. If a legitimate ticket trips the cap due to transient platform faults, a human re-queues it via `TransitionAgentTicket` (errored→queued is a valid edge, §1.7) — the cap is a safety valve, not a terminal wall.
- **Multi-node safety:** the dispatcher relies on the component `Coordinator` lock (single Run across nodes) AND the `queued→running` guarded transition (intra-pass claim). Both must hold; if the Coordinator wiring changes upstream, the guarded transition still prevents double dispatch, degrading to redundant-but-safe work.
