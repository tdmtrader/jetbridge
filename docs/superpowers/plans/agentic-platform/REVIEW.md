# Agentic Platform — Design Review

**Reviewed:** 2026-07-08 · **Scope:** all 14 workstream plans (`01`–`14`) against `00-shared-contracts.md`, the end-state spec, and `ROADMAP.md` · **Reviewers:** 14 per-plan + 4 cross-cutting = 18 verdicts.

---

## 1. Verdict

**The program is fundamentally well-designed and, with a small set of targeted fixes, ready to execute.** Across the 18 reviewers the tally is: **6 sound**, **8 minor-concerns**, **0 needs-work** on the per-workstream axis, plus (cross-cutting) **2 sound** and **2 minor-concerns**. Counting only the 14 per-workstream verdicts: **5 sound**, **8 minor-concerns**, **1 needs-work** (dispatch). No workstream is fundamentally mis-designed. The architecture is coherent end-to-end — render-time resolution decouples the agent step from workflow tables, single-writer transition discipline prevents wave-4 races, "verify-state-not-transcripts" is real (independent gate re-run + push-by-sha), and scoping shows genuine YAGNI discipline. What holds it back from a clean bill of health is a **cluster of confirmed findings concentrated in the wave-4 dispatch renderer and the agent-step ingestion path**: two blockers (both the same underlying defect — the checkpoint step is rendered as an LLM agent step that physically cannot execute the deterministic checkpoint mechanism) and eight majors. Critically, **only one confirmed finding touches a wave-1 workstream** (workflow-store's seed prompts), and even the wave-1/2 agent-step ledger/timeout bugs are ~2-line fixes. The blockers are in wave 4, so wave 1 can begin now while these are scheduled. This is a mature plan set that needs surgical corrections, not a redesign.

---

## 2. Confirmed findings (ranked)

These survived adversarial verification. Two verified as **partly-confirmed** (severity should be downgraded); they are marked as such and placed with the majors.

| # | Sev | Workstream | Dimension | One-line problem |
|---|-----|-----------|-----------|------------------|
| 1 | **BLOCKER** | dispatch / e2e-lifecycle | sensibility/design | Checkpoint rendered as `atc.AgentStep` (LLM) cannot invoke the deterministic `platform-mcp checkpoint` client the owner built; reject→fail seam never connects. |
| 2 | **BLOCKER→doc** | dispatch | design | Ephemeral run secret attached *after* `CreateRun` — flagged as a pod-start race, but **verified partly-confirmed: no runtime race**, only stale §8.2 wording. |
| 3 | major | agent-step | design | Ledger `Record` double-charges on web-restart resume (asymmetric with the deliberately idempotent metrics upsert). |
| 4 | major | agent-step | design | Ingestion inherits the timed-out step context, so timed-out (costliest) steps record zero cost/tokens/metrics. |
| 5 | major | workflow-store | sensibility | Seed `standard-dev.yaml` prompts assume file-delivery but the seed defaults to `mcp` — spec.md/plan.md never exist. |
| 6 | major | delivery-outcomes | design | Send-back → re-dispatch → merge is never recorded; outcome row frozen in `closed_unmerged`, re-push refresh is dead code. |
| 7 | major | platform-mcp-hitl | sensibility | `timeout_policy: default|fail` with `ask_timeout_seconds=0` silently parks forever, defeating the policy. |
| 8 | major | scorecards | sensibility | `live` flag and authoritative content-hash never populated — no query joins `agent_workflow_definitions`. |
| 9 | major | process-intel-experiments | sensibility | Analytics `since/until` window is advertised and parsed but never applied to SQL (**partly-confirmed**: substance holds, §10 citation loose). |
| 10 | major | process-intel-experiments | design | Retrospective intel delivered via ticket body but the prompt tells the agent to read a non-existent `intel.md`. |
| 11 | major | credentials-and-budgets | design | Platform-credential syncer never deletes the stale K8s secret on vault removal (**partly-confirmed**: stale-data-plane, not live-token exposure). |
| 12 | major | agent-step / e2e | design | Main agent step's own dollar spend has no mid-flight cutoff (**partly-confirmed**: real honesty gap, but a between-step admission gate exists — turn count is *not* the only cap). |

---

### BLOCKERS

#### Finding 1 — Checkpoint step: dispatch renderer and platform-mcp-hitl disagree on step shape (CONFIRMED)
**Workstreams:** dispatch (owner of the defect) + platform-mcp-hitl (owner of the mechanism); surfaced independently by both the `dispatch` and `e2e-lifecycle` reviewers.
**Problem.** `renderCheckpointStep` (Task 5) emits an `atc.AgentStep` named `checkpoint-<name>` with an LLM prompt (`"Await human approval of checkpoint %q via platform-mcp."`) and two invented env vars `PLATFORM_MCP_CHECKPOINT` / `PLATFORM_MCP_CHECKPOINT_ON_REJECT`. But platform-mcp-hitl built the checkpoint as a **deterministic client**: the step's main container must run `platform-mcp checkpoint --name <n> [--description <d>]`, which POSTs `/checkpoint` and returns exit 0 (approve) / exit 1 (reject). An `atc.AgentStep`'s main process is hardwired to `agent-runner`/claude (Task 12 step 9, `Path: "agent-runner"`), and the struct has **no command/entrypoint/image-override field** (§2.8). So the renderer physically cannot emit the required client. Three verified consequences: (1) a checkpoint burns a full LLM turn on the user's token instead of a deterministic park; (2) the two env vars are read by **nothing** (they appear only at these two lines in the entire plan set); (3) `on_reject: fail` never becomes a non-zero exit — agent-runner never observes `/checkpoint`, so a **rejected checkpoint exits 0 and the run proceeds as if approved**, defeating the human gate. Dispatch's own prose (11-dispatch.md line 626) says checkpoints "render to a `task:`-style checkpoint step" — the prose is right, the code is wrong.
**Citations.** Edit `11-dispatch.md` Task 5 `renderCheckpointStep` (lines 842–860, confirmed verbatim above). Contradicts `08-platform-mcp-hitl.md:79` and Task 14 (:3735/:3740), `00-shared-contracts.md` §3.2 (:1065), §5 events (:1250–1251), and §2.8 (`atc.AgentStep` field list, no command).
**Recommended change.** Render the checkpoint as an `atc.TaskStep` (exists at `atc/steps.go:342`; has `Config *TaskConfig` for run-path/args/image and a `Sidecars []SidecarSource` field), **not** an `AgentStep`. The TaskConfig's run invokes `platform-mcp checkpoint --name <s.Checkpoint> [--description ...]` with the platform sidecar mounted, so the container's exit code natively drives step failure. Wire `on_reject` per 08:79: `fail` → let the non-zero exit propagate (run fails → ticket `needs_review`); `send_back` → map the reject outcome to the sent-back path. Delete the two `PLATFORM_MCP_CHECKPOINT*` env vars and the LLM prompt. **dispatch and platform-mcp-hitl must co-sign this as a §11 amendment-log addendum before wave 4** (and, if a new step type is introduced instead of reusing `TaskStep`, a §2.8 addendum).

#### Finding 2 — Ephemeral run secret attached after CreateRun (VERIFIED PARTLY-CONFIRMED → downgrade to doc fix)
**Workstream:** dispatch.
**Problem as filed.** `DispatchOne` (Task 10) orders CreateRun (step 3) before `SecretAttacher.Attach` (step 5), which the filer called a pod-start race contradicting the frozen §8.2 rule that the secret is created "before run creation."
**Verification outcome.** The **race is not real.** `CreateRun` → `job.CreateBuild` only inserts a *pending* build and bumps `requestSchedule` inside the transaction (verified against `atc/db/job.go:825/849`); pod creation is driven asynchronously by the separate `scheduler` + build-tracker components. `Attach` runs synchronously microseconds later in the same function with no I/O yield, so no pod can be scheduled before the secret exists. The create→claim→attach ordering is a *deliberate* decision (records `pipeline_run_id` on the won claim; avoids leaking secrets on a lost claim). What is real is only that §8.2's phrase "before run creation" is **stale wording** predating the §2.8.2 dispatch addendum.
**Recommended change.** **Downgrade from blocker to trivial doc fix.** Reword `00-shared-contracts.md` §8.2 (line 1465) from "during dispatch, before run creation" to "during dispatch, after the queued→running claim is won (which records `pipeline_run_id` per §2.1) and before the build tracker schedules the entry-job pods." Add a §11 amendment-log note that dispatch's §2.8.2 addendum supersedes the earlier credentials-and-budgets phrasing. No code or lifecycle change needed.

---

### MAJORS

#### Finding 3 — Ledger `Record` double-charges on web-restart resume (CONFIRMED)
**Workstream:** agent-step.
**Problem.** `ingestFlightRecorder` calls `budgetChecker.Record(entry)` unconditionally whenever `rm.CostUSD > 0`. On a web restart the whole `Step.Run` re-executes (re-attach, `process.Wait` returns, outputs re-register, ingestion re-runs). The metrics row is protected by `ON CONFLICT (build_id, plan_id)` — documented "idempotent across web-restart resumes" — but `agent_cost_ledger` is append-only with only a `BIGSERIAL` PK and no dedup key (§1.4). A resume double-appends the cost row, inflating spend against both the per-ticket budget and the global daily cap, and directly violating the design's own stated invariant "Every dollar enters the ledger exactly once." The authors hardened the metrics row against exactly this resume but left the ledger unguarded in the same function.
**Citations.** Edit `07-agent-step.md` Task 13 `ingestFlightRecorder` (lines ~1884–1905). Contract §1.4 (append-only ledger, `00-shared-contracts.md`:102–129), §2.7 Record.
**Recommended change.** Have `metrics.Store.Upsert` (Task 7/8) return an `inserted bool` (true only when ON CONFLICT did not fire — e.g. `xmax = 0` or a RETURNING discriminator), and gate `budgetChecker.Record` on `inserted && rm.CostUSD > 0`. Reuses the existing `(build_id, plan_id)` key as the single dedup authority; no new schema; avoids an ON CONFLICT on the ledger (awkward because the gateway writes its own rows for the same build/plan under `source='gateway'`). Add a Task-13 spec asserting `Record` fires exactly once across two `Run` invocations.

#### Finding 4 — Timed-out steps record zero cost/tokens/metrics (CONFIRMED)
**Workstream:** agent-step.
**Problem.** Task 13 says to call `ingestFlightRecorder(ctx, ...)` on every path "including the DeadlineExceeded branch." But `ctx` is the timeout-scoped context from `MaybeTimeout`; when the step times out it is already expired. Ingestion then uses that cancelled `ctx` for every `StreamFile` call (traced through to `http.NewRequestWithContext`), so both `results.json` and `events.ndjson` reads fail immediately — producing a bare `status=error` row with zero cost, zero tokens, empty `event_counts`, and (because `CostUSD==0`) no ledger entry. **Every** timed-out agent step loses all measurement — the runaway agent that burns its whole slice then times out is exactly the case where cost attribution matters most, and it is the case that silently loses it. Violates spec guiding principle 5 ("Everything is measurable").
**Citations.** Edit `07-agent-step.md` Task 13 (line 1764 and the two `StreamFile` calls, ~1817/1835). `atc/exec/task_step.go` `MaybeTimeout` at :363.
**Recommended change.** Ingest on a context detached from the step deadline: before the reads, `ingestCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second); defer cancel()` and pass `ingestCtx` into ingestion. ~2-line idiomatic change, no new dependency. Add a Task-13 spec that forces `process.Wait` to return `DeadlineExceeded` with a populated flight volume and asserts the metrics row still carries pre-timeout cost/turns/`event_counts` and a ledger entry is recorded.

#### Finding 5 — Seed `standard-dev.yaml` prompts contradict the seed's own delivery mode (CONFIRMED)
**Workstream:** workflow-store. *(The only confirmed finding in a wave-1 workstream.)*
**Problem.** The seed omits `spec_delivery`, so per the frozen rule it defaults to `mcp`, which injects **no** spec/plan bytes and materializes **no** files (agents read via platform-mcp `read_ticket`/`list_tasks`/`get_task`). Yet the seed's own `implement` prompt says "The approved spec and plan are in spec.md and plan.md," and `qa`/`review` embed `{{.Spec}}`/`{{.Tasks}}`. Those files never exist under the seed's effective mode, and the dispatch renderer performs **no** Go-template substitution (it inlines the prompt verbatim), so the `{{.Spec}}`/`{{.Tasks}}` literals would reach the agent unexpanded. This is the workstream's shipped reference definition — the exemplar every future import copies — and it is internally incoherent. `TestSeedStandardDevValidates` passes anyway because it only checks structure, never prompt/delivery coherence.
**Citations.** Edit `05-workflow-store.md` seed (:2603–2605, prompts at :2636/:2643/:2650) and its test (:2548). Frozen default rule at `00-shared-contracts.md` §6 (:1269–1276); dispatch no-substitution at `11-dispatch.md` (:350–388) and `07-agent-step.md`:2487.
**Recommended change.** Rewrite the `implement`/`qa`/`review` prompts to match the default `mcp` read model (preferred over setting `spec_delivery: files`, since the reference workflow should demonstrate the default path the rest of the design centers on): drop the `spec.md`/`plan.md` references and the bare `{{.Spec}}`/`{{.Tasks}}` tokens, and instruct the agent to read via `read_ticket`/`list_tasks`/`get_task` (as the `plan` prompt already does). Add one assertion to `TestSeedStandardDevValidates` tying prompt content to the resolved `spec_delivery` (when it resolves to `mcp`, no prompt body may contain `spec.md`/`plan.md` or `{{.Spec}}`/`{{.Tasks}}`) so the exemplar cannot silently regress.

#### Finding 6 — Send-back → re-dispatch → merge is never recorded (CONFIRMED)
**Workstream:** delivery-outcomes.
**Problem.** `sent_back` moves the outcome row to terminal `closed_unmerged`. The state machine allows `sent_back → queued → running → needs_review` (re-dispatch after edits), reusing the same branch `agent/ticket-<n>`. On the next tick the watcher's `seedRows` finds an existing row and `continue`s (verified above: `else if found { continue }`), so it never re-resolves the fresh `pushed_sha`/`base_sha` and never calls `Ensure` again — and even if it did, `Ensure`'s refresh fires only `WHERE merge_state='open'`, but this row is `closed_unmerged`. Result: after any send-back+re-dispatch, the row is permanently frozen with stale shas in a terminal state, and a subsequent human merge of the re-worked branch is **never detected**. The platform silently loses "the single most honest quality metric it collects" (spec §9) exactly on the re-work loop — a core platform flow. The addendum §1.11.1 "re-push refreshes in place" contract text and the entire open-row-refresh path are dead in production; the `L2611` comment points at an "elsewhere" that does not exist.
**Citations.** Edit `12-delivery-outcomes.md` Task 10 `seedRows` (~L2604–2620), Task 3 `SetDisposition` (L520–536), Task 4 SQL (L771–775). State machine `06-ticket-core.md`:332; contract §1.11.1 (:81).
**Recommended change.** In `seedRows`, replace the unconditional `continue` on a found row with a re-arm: when the ticket is back in `needs_review` but the row is `closed_unmerged AND disposition='sent_back'`, reset it to `open` and refresh branch/pushed_sha/base_sha — add a `Reopen`/`Rearm` store method, or broaden `Ensure`'s ON CONFLICT WHERE to also match `closed_unmerged AND disposition='sent_back'` and stop `continue`-ing. Also fix the plain `needs_review→queued` path (row stays `open` but shas are never refreshed because `seedRows` continues). Add a watcher spec covering `needs_review → sent_back → queued → running → needs_review(new sha) → merged` asserting the row ends `merged_with_fixes` with the new base/pushed. Fix the misleading L2611 comment.

#### Finding 7 — `timeout_policy: default|fail` with `ask_timeout_seconds=0` silently parks forever (CONFIRMED)
**Workstream:** platform-mcp-hitl.
**Problem.** `awaitWithPolicy` (Task 11) only computes a deadline when `TimeoutPolicy != "park" && TimeoutSeconds > 0`. Nothing forces `seconds > 0` for a non-park policy: `ConfigFromEnv` accepts `fail`/`default` with 0 (0 = "indefinite"), `questions.Validate()` never requires it, and workflow-store `Validate` checks only the enum and `>= 0`. So a workflow declaring `ask_timeout: fail` but leaving `ask_timeout_seconds: 0` (the schema default) produces a sidecar that never sets a deadline: the run parks indefinitely and the `fail`/`default` policy the author asked for never fires, with no error surfaced anywhere. §3.2 ties "no timeout (0)" specifically to `park`, so a declared `fail`/`default` that never fires silently defeats the author's intent.
**Citations.** Root fix in `05-workflow-store.md` `Validate` (L726–733); defense-in-depth in `08-platform-mcp-hitl.md` `ConfigFromEnv` (~L1920). Contract §6 hitl block (`00-shared-contracts.md`:1327).
**Recommended change.** Add one cross-field check rejecting `AskTimeout ∈ {default,fail}` with `AskTimeoutSeconds <= 0` at the cheapest upstream layer — **workflow-store `Validate`** — so the misconfiguration fails loudly at import before any run is dispatched. Mirror the check in `ConfigFromEnv` as defense-in-depth so a hand-set sidecar env also fails at startup. Add a `config_test` case for `fail`/`default` + 0.

#### Finding 8 — Scorecard `live` flag and authoritative content-hash never populated (CONFIRMED)
**Workstream:** scorecards.
**Problem.** `VersionColumn.Live` and the content hash are surfaced in the UI (Task 9 renders a "live" badge) and the Architecture/CONSUMES sections claim to aggregate `agent_workflow_definitions` (§1.6). But **none** of the three store methods (`metricsAndCost`, `evidence`, `outcomes`) reads that table. `Live` stays at its zero value (`false` → the badge **never lights**, even for versions that have run), and `ContentHash` is set only from `MAX(workflow_hash)` over `agent_run_metrics` — so a candidate version that has not run yet returns an all-zero, empty-hash, not-live column. That is precisely the promotion-comparison case (compare live vs. candidate before promoting) that spec §8 names as the scorecard's reason to exist ("This — not a gate — is how promotion decisions get made").
**Citations.** Edit `13-scorecards.md` Task 3 `metricsAndCost` (:456), and add a query called from `perVersion` (:441). The reads already exist in workflow-store (`Live(name)`/`Versions(name)`, `05-workflow-store.md`:1073/1349).
**Recommended change.** Add a `definition(name, version, col)` query (called from `perVersion` alongside the other three): `SELECT content_hash, live FROM agent_workflow_definitions WHERE name=$1 AND version=$2`, setting `col.Live` and `col.ContentHash` authoritatively (metrics-derived hash becomes at most a fallback). Makes the badge reflect real `live` state and renders a not-yet-run candidate as a real hash-bearing column. Add a store spec asserting `col.Live` is true for the promoted version and false otherwise.

#### Finding 9 — Analytics `since/until` window is parsed but never applied to SQL (VERIFIED PARTLY-CONFIRMED — substance holds)
**Workstream:** process-intel-experiments.
**Problem.** Routes advertise `?since=&until=`, `intel.Filter` carries `SinceUnix`/`UntilUnix`, and the handler parses them — but the Task-16 Queryer SQL never applies them. `MergedReviewCount`, `DefectLinkCount`, `VerdictCounts` have no time predicate at all; `LeftwardSeries`/`FrictionAggregates`/`FindingsByVersion` parameterize only repo/workflow. Every metric is all-time, and the retrospective trigger (Task 19) calls the analyzer with an empty `intel.Filter{}`, so it mines the entire history rather than a recent window — contradicting the plan's own Step-3 prose ("`since`/`until` compared against `created_at`/`occurred_at`") and ROADMAP line 65 ("mine a month of findings"). Calibration/friction signatures that never age out drift meaninglessly and cannot show improvement-over-time — the whole leftward-migration thesis. *(Partly-confirmed only because the finding loosely attributed "over the window" to spec §10; the windowing language actually lives in the plan and ROADMAP. Substance is fully confirmed.)*
**Citations.** Edit `14-process-intel-experiments.md` Task 16 (Queryer methods ~L3337–3368; `LeftwardSeries` ~L3281–3300) and Task 19 trigger (~L3959).
**Recommended change.** Add an `applyWindow(b, alias, f)` helper mirroring the existing `applyWorkflow`, and call it in every Queryer method against `created_at`/`occurred_at`; bind since/until into the two raw-SQL methods. Have the retrospective trigger construct a bounded filter (trailing 30 days) instead of `intel.Filter{}`. Matches the existing `GetAgentCostRollup` since/until precedent.

#### Finding 10 — Retrospective intel delivered as ticket body but prompt reads a non-existent `intel.md` (CONFIRMED)
**Workstream:** process-intel-experiments.
**Problem.** Task 18 builds `RenderIntelMarkdown` as "the read-only `intel.md` workspace input" and the seed prompt instructs "Read `intel.md` in your workspace," but nothing wires that output into an `intel.md` file. Task 19's trigger puts the snapshot in `Ticket.Body`, which reaches the agent as the dispatched run's `spec.md`. But the dispatch renderer materializes only `spec.md`/`plan.md`, and the default `mcp` mode materializes **no file at all**; moreover `Ticket.Body` ≠ the versioned `Spec` that `RenderSpecMarkdown` reads (Task 19 never calls `SubmitSpec`), so even in files mode the agent would get an empty spec. An agent following the prompt looks for a file no step produces.
**Citations.** Edit `14-process-intel-experiments.md` Task 18 (:3661/:3733), seed prompt (:3733–3746), Task 19 trigger (`Body: string(snapshot)`, :3976). Dispatch materialization at `11-dispatch.md`:191/57/68; `Ticket.Body` vs `Spec` at `06-ticket-core.md`:396 vs :424/:502.
**Recommended change.** Pick one delivery path and make prompt + seed + trigger agree. Simplest (no new dispatch mechanism): (a) amend the seed prompt to "Read the ticket via platform-mcp (`read_ticket`)," and (b) have Task 19 deliver the snapshot via `SubmitSpec` (not `Ticket.Body`) so `read_ticket` returns it. Correct Task 18's framing that describes an "intel.md workspace input" that does not exist.

#### Finding 11 — Platform-credential syncer never deletes the stale K8s secret on vault removal (VERIFIED PARTLY-CONFIRMED — downgrade framing)
**Workstream:** credentials-and-budgets.
**Problem.** `PlatformSecretSyncer.Run` (Task 15) only creates/updates the `agent-platform-credential` secret. When the credential is removed from the vault (`fly agent auth --platform --delete`, an explicitly supported operation), `Run` returns a silent no-op (`platform-credential-not-vaulted`). The now-withdrawn credential remains live in the K8s secret indefinitely, and harvest/retrospective pods keep mounting it — contradicting the contract's "kept in sync" promise. *(Partly-confirmed: deleting the vault row does NOT revoke the upstream Anthropic token — the vault is only Concourse's store of it — so this is a stale-data-plane / operational-surprise bug, not live-token exposure. Real design inconsistency worth fixing; major severity is slightly high.)*
**Citations.** Edit `02-credentials-and-budgets.md` Task 15 `Run` (lines 3712–3778). Reuse the NotFound-tolerant idiom already in `secret_attacher.go` Cleanup (lines 3549–3554). Amend `00-shared-contracts.md` §8.2 (line 1472).
**Recommended change.** In `Run`, replace the `!found` early return with a delete of any existing `agent-platform-credential` secret (tolerating NotFound). Add `TestSyncerDeletesSecretWhenCredentialUnvaulted`. Amend §8.2 to state the sync contract is bidirectional (vault deletion removes the long-lived secret). Note in the commit that this propagates admin intent to the data plane but does not revoke the upstream token.

#### Finding 12 — Main agent step's own dollar spend has no mid-flight cutoff (VERIFIED PARTLY-CONFIRMED — honesty gap, not "turn count is the only cap")
**Workstream:** agent-step / e2e-lifecycle.
**Problem.** The gateway enforces only its own metered slice (cross-agent calls). The main agent's own claude-CLI spend on `CLAUDE_CODE_OAUTH_TOKEN` never passes through the gateway; the runner doesn't read `AGENT_BUDGET_SLICE_USD` and claude is invoked with `--max-turns` only. Within a single running step, the main agent can overshoot `budget_slice_usd`. §8.1 annotates that env var in the main container as "gateway enforces" — false for the main container's own usage — and the spec's "run halts at the gateway / never silent truncation" is true only for the sub-agent path. *(Partly-confirmed: a real dollar gate DOES exist — between steps. The step calls `StepSlice` against the ledger before starting and errors "budget slice exhausted before start" if remaining is exhausted, so a runaway step's overrun is caught at the next admission. Blast radius is one step's overshoot, self-limited by turn/timeout caps — NOT the unbounded overrun the finding implies. So "turn count is the only cap" is refuted; the honesty/documentation gap is real.)*
**Citations.** `02-credentials-and-budgets.md`:1547–1554; `07-agent-step.md`:2212–2218; spec Failure-handling; §8.1 annotation (`00-shared-contracts.md`:1452).
**Recommended change (cheap fallback, not the expensive live cutoff).** (1) Add a line to the spec's Failure-handling section stating the main agent's own spend is bounded per-step by `--max-turns`/timeout and enforced against the budget at step-admission and post-hoc ingestion — not mid-call; only cross-agent gateway calls get a mid-call dollar cutoff. (2) Correct the §8.1 annotation for `AGENT_BUDGET_SLICE_USD` in the main container from "gateway enforces" to "gateway enforces for sub-agent calls; main-agent own spend is admission-gated + post-hoc reconciled, turn/timeout-capped within a step." Optionally derive `--max-turns` from `slice / expected-cost-per-turn` at render time. A live per-turn abort in the runner is not worth the complexity for a self-limiting one-step overrun.

---

## 3. Per-workstream verdicts

| # | Workstream | Wave | Verdict | One-line reason |
|---|-----------|------|---------|-----------------|
| 01 | agent-identity | 1 | **sound** | Cleanly generalizes the auth seam every later workstream extends; dual-accept cutover, correct layering, failure modes handled. |
| 02 | credentials-and-budgets | 1 | minor-concerns | Well-architected; two loose seams — syncer never propagates vault deletion (F11), daily-cap local-time vs UTC rollups. |
| 03 | pipeline-runs | 1 | **sound** | Reuses real seams; completion/reopen handles the reactive substrate correctly; parked-run contract tested; risky filters proven inert. |
| 04 | dev-mcp | 1 | **sound** | Scoped precisely to charter; clean seams, correct taxonomy, no scope leakage; only a minor drift-guard test gap. |
| 05 | workflow-store | 1 | minor-concerns | Store/grammar/inert-slot scoping sound; one defect — shipped seed prompts contradict the seed's own delivery mode (F5). |
| 06 | ticket-core | 2 | **sound** | Single-writer transition + optimistic concurrency, clean layering, full §9 lifecycle from day one; two minor doc/footgun nits. |
| 07 | agent-step | 2 | minor-concerns | Mirrors TaskStep precisely; two real correctness gaps in the shared ingest path — ledger double-charge (F3) and timeout ctx (F4). |
| 08 | platform-mcp-hitl | 3 | minor-concerns | Cleanly layered, spec-faithful; ask_human timeout seam parks forever on default/fail + 0s (F7). |
| 09 | harvest-step | 3 | minor-concerns | Clean, well-seamed, vision-aligned; two dangling judge-budget contract promises to resolve before consumers freeze. |
| 10 | gateway-mcp | 3 | **sound** | Clean bounded sidecar; fire-and-forget metering can never fail a build; honest cutoff heuristic; minor doc/param overclaims. |
| 11 | dispatch | 4 | **needs-work** | Renderer/dispatcher well-factored, but checkpoint step is a broken cross-workstream seam (F1) and the secret-attach ordering note (F2). |
| 12 | delivery-outcomes | 4 | minor-concerns | Sound architecture; send-back→re-dispatch→merge loop freezes the outcome row so re-worked merges never record (F6). |
| 13 | scorecards | 4 | minor-concerns | Clean read-only rollup; live flag + authoritative hash never populated, undercutting promotion comparison (F8). |
| 14 | process-intel-experiments | 5 | minor-concerns | Substrate well-factored; M2 analytics ignore their advertised window (F9) and the intel-delivery seam is inconsistent (F10). |

**Cross-cutting:**

| Scope | Verdict | Reason |
|-------|---------|--------|
| vision-coherence | minor-concerns | 14 workstreams deliver the north star coherently; one design-level ordering hazard (dispatch checkpoint) + two minor coherence drifts. |
| small-team-scoping | **sound** | Mature, well-decomposed, deliberate anti-over-engineering (rejected glue-dispatch/double-ingestion/stub-harvest; claude-only v1; linear grammar; inert slots). |
| e2e-lifecycle | minor-concerns | Connects end-to-end almost everywhere; checkpoint render is a broken seam (F1) and main-step dollar spend is unenforced mid-flight (F12). |
| simplification | **sound** | One-mechanism/one-primitive discipline already applied across every high-complexity area; no blocker/major simplification survived scrutiny. |

---

## 4. Minor polish (optional — low priority)

**Attribution / identity documentation (agent-identity, ticket-core, harvest):**
- Admin-token debug writes to principal routes land with empty `submitted_by`, indistinguishable from pre-migration rows — inject `UserNameFunc` or document the sentinel.
- `team_name` is minted/stored/defaulted but never consulted by verification — add one sentence to `agent-route-scopes.md` marking it informational-only in v1.
- Add an explicit Task-5 unit assertion pinning the static-token-reaches-handler invariant (currently only implicitly verified).
- Task-1 addendum overclaims `GetAgentTicket/TicketDetail` == `read_ticket`'s verbatim payload (the 2026-07-08 amendment removed tasks) — reword to "projection of."
- Judge ledger entry carries no platform-user attribution required by §1.13 — pin which side backfills `agent-platform` user_id.

**Budget / clock consistency (credentials, simplification):**
- Daily-cap window uses local time but rollups bucket by UTC — pick one "platform day."
- State §1.8 that the ledger is authoritative and `run_metrics.cost_usd` is a derived convenience copy, so two dollar figures can never diverge.

**Scoping / dead plumbing (agent-step, harvest, gateway, ticket-core, dispatch, workflow-store):**
- `output_schema` is fully plumbed but never applied — mark captured-but-unused in v1, or wire it.
- §6.4.1 promises judge-budget "overage is logged" but no task reads `JudgeConfig.BudgetUSD` — trim the promise or add the one-line log.
- `Adapter.maxCostUSD` is a dead parameter whose doc promises a v1-unimplementable hard-stop — mark advisory/unused.
- Gateway adapter-error path posts a $0 ledger row + `cost.record` for a call that spent nothing — gate on `CostUSD>0 || InputTokens>0`.
- `Store.Update` accepts workflow-ref/budget mutations in any state incl. running — restrict to pre-dispatch or drop the fields (YAGNI).
- List handler issues an N+1 `Live()` query per workflow — fold into a single query (deferrable at small scale).
- `list_tasks` + `get_task` as two round-trips is finer than v1 needs — consider inlining `detail_md` and deferring the third tool.

**Honesty / disclosure (pipeline-runs, delivery-outcomes, scorecards, vision-coherence):**
- "Versions pinned at creation" is weaker than the spec promises for shared resource-config scopes — soften the wording.
- `resolveShas` branch-head fallback can seed a stale/foreign `pushed_sha` — add a `base_provenance` flag so a fallback baseline isn't silently trusted.
- Time-to-merge measured from `agent_outcomes.created_at` (an unlabeled interval) — pin to dispatched/pushed time or rename to "time in review."
- Verdict-distribution query counts all ticket feedback, not just harvest findings as the prose claims — reconcile filter vs. prose.
- Outcome watcher introduces a third git-credential/repo-cache system the spec implied didn't exist — add an honesty note; ditto delivery-outcomes reinventing a bare-mirror git stack.

**Process-intel scoping:**
- Pre-commit the M1/M2 split (experiments vs. intelligence) as two declared wave-5 tracks; they can run in parallel.
- Runner budget admission is a coarse throttle, not the "load-bearing cap" the addendum claims — bound per-tick queueing or state the dispatcher is the true cap.

---

## 5. What is genuinely strong (do not touch)

- **The auth seam (agent-identity).** The dual-accept cutover with per-task rollback, stdlib-only `agent/` package, and factory in `atc/db` is the clean foundation every later workstream extends. Sound as designed.
- **Single-writer transition discipline (ticket-core / pipeline-runs).** Optimistic-concurrency transition functions and the guarded queued→running claim are what prevent the wave-4 races; the full §9 lifecycle enum shipped from day one avoids retrofits.
- **Render-time resolution (dispatch/agent-step decoupling).** The agent step never reads workflow tables — the renderer resolves everything at dispatch — which is why the agent step mirrors `TaskStep` so cleanly and stays layer-pure.
- **Verify-state-not-transcripts (harvest).** Independent gate re-run + push-by-sha + main-container-only SecretMounts is real, not aspirational — a genuinely strong integrity posture.
- **Disciplined YAGNI throughout.** Linear-only grammar, declared-but-inert slots, nullable outcome columns, claude-only gateway v1, one-YAML scaffolding, rejected glue-dispatch/double-ingestion/stub-harvest. The `simplification` and `small-team-scoping` reviewers both found nothing to cut — rare and worth preserving.

---

## 6. Recommended actions

**Fix before executing wave 1 (only one, and it is small):**
1. **F5 — workflow-store seed prompts** (`05-workflow-store.md`). Rewrite the `implement`/`qa`/`review` prompts to the default `mcp` read model and add the delivery-coherence assertion. This is the exemplar every import copies; it must ship coherent. Small edit, wave-1 blocker for that workstream only.

**Fix during their own waves (schedule now, not wave-1 blockers):**
2. **F3 + F4 — agent-step ingestion** (`07-agent-step.md`, wave 2). Two ~2-line fixes (idempotent ledger gate; detached ingest context) plus two specs. Both defend the "every dollar / everything measurable" invariants; do them together in the agent-step task.
3. **F7 — hitl timeout validation** (`05-workflow-store.md` `Validate` + `08` `ConfigFromEnv`, wave 3). One cross-field check.
4. **F1 — checkpoint step render** (`11-dispatch.md` Task 5, wave 4) — **the real blocker.** Re-render as a `TaskStep` invoking `platform-mcp checkpoint`; wire `on_reject`; delete dead env vars. **dispatch + platform-mcp-hitl must co-sign a §11 amendment before wave 4.** Add an e2e test that a rejected checkpoint fails the run.
5. **F6 — outcome re-arm** (`12-delivery-outcomes.md` `seedRows`, wave 4). Broaden the WHERE / add `Reopen`, remove the `continue`, add the send-back→merge spec.
6. **F8 — scorecard live/hash query** (`13-scorecards.md`, wave 4). Add the `agent_workflow_definitions` join.
7. **F9 + F10 — process-intel** (`14`, wave 5). Apply the `applyWindow` helper + bounded retrospective filter; fix the intel-delivery seam (`SubmitSpec` + prompt).

**Note and proceed (documentation/framing; do in-line with the touched files):**
8. **F2 — §8.2 wording** (`00-shared-contracts.md`:1465). Downgrade to a doc fix; reword ordering + amendment-log note. No code change.
9. **F11 — syncer delete + §8.2 bidirectional sync** (`02` Task 15). Real fix but low blast radius; note it does not revoke the upstream token.
10. **F12 — budget honesty** (spec Failure-handling + §8.1 annotation). Documentation correction; the between-step admission gate already contains the overrun — do **not** build the live per-turn cutoff.

**Defer (optional polish):** everything in §4. None gates execution; batch opportunistically when the relevant file is open.

---

## 7. Resolution log (2026-07-09)

All 12 confirmed findings (F1–F12) were applied to their owning plan/spec files. Each fix landed as a TDD-style edit (failing→passing test where a code contract exists; prose-only correction where the finding is doc-only), with a dated 2026-07-09 amendment/sign-off note recorded in the touched file.

| Finding | Status | Files touched | What changed |
|---------|--------|---------------|--------------|
| F1 | fixed | `11-dispatch.md`, `08-platform-mcp-hitl.md`, `00-shared-contracts.md` | `renderCheckpointStep` now emits an `atc.TaskStep` running `platform-mcp checkpoint --name <n>` (exit 0 approve / exit 1 reject gates the run); dead `PLATFORM_MCP_CHECKPOINT*` env vars + LLM prompt deleted; `on_reject` mapping documented; co-signed §11 amendment across dispatch + platform-mcp-hitl + contracts. |
| F2 | fixed | `00-shared-contracts.md` | §8.2 reworded from "before run creation" to "after the queued→running claim is won, before the build tracker schedules entry-job pods"; §11 note that the §2.8.2 dispatch addendum supersedes the earlier phrasing. Doc-only (no runtime race). |
| F3 | fixed | `07-agent-step.md` | Added additive `metrics.Store.UpsertReturningInserted`; `ingestFlightRecorder` gates the ledger `Record` on `inserted && CostUSD > 0`, so a web-restart resume no longer double-charges the append-only ledger. Test asserts `Record` fires exactly once across two `Run`s. |
| F4 | fixed | `07-agent-step.md` | Ingestion now runs on `context.WithoutCancel(ctx)`+30s timeout so timed-out steps still read `results.json`/`events.ndjson`; test forces `DeadlineExceeded` and asserts non-zero cost/turns/`event_counts` + a ledger entry survive. |
| F5 | fixed | `05-workflow-store.md` | Rewrote `implement`/`qa`/`review` seed prompts to the default `mcp` read model (`read_ticket`/`list_tasks`/`get_task`), dropping `spec.md`/`plan.md` and `{{.Spec}}`/`{{.Tasks}}`; `TestSeedStandardDevValidates` now fails any prompt that regresses to file-mode tokens. |
| F6 | fixed | `12-delivery-outcomes.md` | `Ensure` re-arms a `closed_unmerged AND disposition='sent_back'` row back to `open` with fresh shas; `seedRows` no longer `continue`s on a found row; watcher test drives send-back → re-dispatch → merge ending `merged_with_fixes` with the reworked shas. |
| F7 | fixed | `05-workflow-store.md`, `08-platform-mcp-hitl.md` | Cross-field check rejects `ask_timeout ∈ {default,fail}` with `ask_timeout_seconds <= 0` at import (`Validate`) and as defense-in-depth in `ConfigFromEnv`; `park`+0 stays legal. Tests cover both layers. |
| F8 | fixed | `13-scorecards.md` | New `definition(name, version, col)` query runs first in `perVersion`, setting `col.Live`/`col.ContentHash` authoritatively from `agent_workflow_definitions` (metrics `MAX(workflow_hash)` demoted to fallback); test asserts a live version lights the badge and a never-run candidate returns a real hash. |
| F9 | fixed | `14-process-intel-experiments.md` | Added `applyWindow` helper applied to all four squirrel Queryers + the three raw-SQL methods (half-open `[since,until)`, `0`=unbounded); retrospective trigger now passes a trailing-30-day filter instead of `intel.Filter{}`. Windowing test asserts fewer counts under a `SinceUnix` bound. |
| F10 | fixed | `14-process-intel-experiments.md` | Retrospective snapshot delivered via `tickets.Store.SubmitSpec` and seed prompt changed to read the ticket via platform-mcp `read_ticket`; removed all references to the non-existent `intel.md` workspace file; test asserts `SubmitSpec` called once with the snapshot body. |
| F11 | fixed | `02-credentials-and-budgets.md`, `00-shared-contracts.md` | `PlatformSecretSyncer.Run` `!found` branch now deletes the stale `agent-platform-credential` secret (NotFound-tolerant, mirrors `secret_attacher.go` Cleanup); §8.2 marked bidirectional; `TestSyncerDeletesSecretWhenCredentialUnvaulted` added. Noted it does not revoke the upstream token. |
| F12 | fixed | `2026-07-07-agentic-platform-end-state-design.md`, `00-shared-contracts.md` | Prose/annotation honesty fix: main-agent own spend is admission-gated (`StepSlice`) + post-hoc reconciled and turn/timeout-capped, not cut off mid-call; §8.1 `AGENT_BUDGET_SLICE_USD` annotation corrected to "gateway enforces for sub-agent calls only." Doc-only; no live per-turn cutoff built. |

**Verification.** The fixes were cross-file verified for coherence, not just per-finding completeness. The three highest-risk seams were checked end-to-end: **F1** — the co-signed checkpoint contract (`atc.TaskStep` + `platform-mcp checkpoint --name`, exit-code gating, deleted env vars, `on_reject` mapping) is now byte-consistent across `11-dispatch.md`, `08-platform-mcp-hitl.md`, and the already-frozen `00-shared-contracts.md` §11/decision-12; **F7** — the `ask_timeout`/`ask_timeout_seconds` cross-field rule matches between workflow-store `Validate` and platform-mcp `ConfigFromEnv`; **F3** — the additive `UpsertReturningInserted` change is confined to the agent-step-owned `Store` surface, leaving the `RunMetrics` (§2.4) and `agent_run_metrics` (§1.8) contracts and the `Upsert` calls in harvest-step (09) and delivery-outcomes (12) untouched. Contract-visible names (`agent-platform-credential`, `AGENT_BUDGET_SLICE_USD`, `StepSlice`, `read_ticket`/`list_tasks`/`get_task`, `spec_delivery: mcp|files`, `needs_review`, `on_reject: fail|send_back`) were confirmed identical to their defining sections across every touched file. No residual follow-ups remained after the pass: the verifier follow-up queue was empty, and each editor's self-consistency cleanup (e.g. removing the stale delivery-outcomes "handled elsewhere" comment and the redundant workflow-store import note) was applied in-line.
