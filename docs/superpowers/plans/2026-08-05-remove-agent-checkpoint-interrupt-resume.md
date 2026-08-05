# Remove the agent checkpoint / interrupt / resume subsystem

**Date:** 2026-08-05
**Branch:** `claude/remove-agent-checkpoint-resume`, cut fresh from `origin/jetbridge` at `57fae3a5fd`.
**Status:** approved by the repo owner; this document is the execution plan, not a proposal.

**Base note.** The prior analysis branch (`claude/jetbridge-snapshot-interrupt-resume-78463b`) was a strict
ancestor of `origin/jetbridge` — 0 commits ahead, 16 behind — so nothing was lost in the re-cut. All
load-bearing citations were re-verified against `57fae3a5fd`: `Authorities: nil` is still
`atc/atccmd/command.go:2266`, `attachOrRunAgentAttempt` is still `atc/exec/agent_step.go:1005-1016`, and
`agent_workflow_run_evidence_factory.go:142` still reads `agent_run_attempt_metrics`. Commit `40ba137f96`
retired the agentic-foundations semantic-rebase track and removed its section from `CLAUDE.md`, so the
worktree/branch pinning that section imposed no longer applies to this work.

### Owner decisions (2026-08-05)

1. **Branch:** fresh off `origin/jetbridge` (above).
2. **Post-removal interruption behaviour: auto-retry — but bounded.** The owner chose to make agent steps
   consistent with the other eight step types rather than keep failing closed. Implementing that as
   literally specified would be unbounded; see the ruling in §4 Phase 2b below. Supersedes open question 1.
3. **Codex slice: undecided.** Proceed with removal; amend `ad1e70d27e` to record that the subsystem was
   removed on economic grounds and point at this plan, rather than restoring the old non-goal or
   preserving the requirement. Supersedes open question 9.

---

## 1. Decision and rationale

The checkpoint / interrupt / resume subsystem was built to survive GKE Spot preemption of an agent pod. The measured economics do not support it: agent model spend is **$15.64/agent-hour** (n=75) against **$0.378/hour** for a `c4d-standard-8` node, so the spot discount the whole subsystem protects is worth roughly **$57/year**. It is also completely inert — the live `concourse-web` Deployment on theborg/cicd carries twelve `--agent-snapshot-*` flags and **zero** `--agent-checkpoint-*` flags, so `composeAgentCheckpoints` early-returns at `atc/atccmd/command.go:2214` and nothing downstream is ever constructed. Worse, its presence is actively harmful: the per-attempt metrics table it introduced became the *only* source the workflow-run occurrence projection reads for cost, and because that table has no live writer, **every agent node in the frozen run record carries `cost_usd = 0`** while the real spend sits in `agent_run_metrics`. We are removing ~20k lines that cost us money to keep.

---

## 2. What survives a web restart today

**An in-flight agent workflow DOES survive an ATC web pod restart today, and that survival does not touch a single line of checkpoint code.** This was verified in code and against the live cluster. The mechanism is older, unconditional, and shared with `task` steps.

The chain, end to end:

**Pickup.** On startup `builds.Tracker.Run` calls `GetAllStartedBuilds()`, which is literally `WHERE b.status = 'started'` (`atc/db/build_factory.go:221-227`), and re-runs the entire build plan from the top (`atc/engine/engine.go:227`). There is **no per-step resume record in the database** — resumption happens entirely at the runtime layer.

**Shutdown.** `drainRunner` (`atc/atccmd/command.go:4190-4200`) → `Tracker.Drain` → `Engine.Drain` closes `release`; `engineBuild.Run` selects on it and deliberately leaves job builds in `started`:

> `atc/engine/engine.go:231-239` — *"In-memory check builds cannot resume across a restart, so finalize them… Job builds are left in `started` so the next web's build tracker re-attaches to them."*

Critically, drain does **not** cancel the step context. The tracker runs builds off `context.Background()` (`atc/builds/tracker.go:124`), and `engine.go:178-205` only wraps the context with `WithCancel` when an abort signal exists, with `cancel()` called only from the abort goroutine — there is no `defer cancel()`. The step goroutine is abandoned and dies with the process, with `ctx.Err() == nil`. That non-cancellation is **load-bearing**: `killAgentOnTerminalEnd` tears down the supervised agent process tree only when the context is already dead (`atc/worker/jetbridge/process.go:1159-1162`), so a timeout or abort kills the agent while a web restart leaves it running.

**Identity.** The container owner for an agent step is `db.NewBuildStepContainerOwner(buildID, planID, teamID)` (`atc/exec/agent_step.go:648`) — stable across restarts. The new web resolves the same DB container row and handle (`atc/worker/jetbridge/worker.go:114-142`), and therefore the same pod name (`podname.go:31`). `attachOrRun` (`atc/exec/task_step.go:461-467`) tries `Attach` first, then `Run`.

**Case A — step already completed.** `Container.Attach` reads the `concourse.ci/exit-status` pod annotation (`container.go:372-390`), returns an `exitedProcess` with that status, and republishes output locations. The annotation is written by `annotateExitStatus` (`process.go:1110-1125`, comment: *"so that Attach() can recover the result after a web restart"*) and is the only durable record of a step's result that survives a web restart. The reaper retains those pods while their build is still running (`reaper.go:107-136`, `:228-266`, fail-closed on a nil lookup or a failed query — commit `11a81ce802`).

**Case B — step still running.** No annotation exists, so `Attach` deliberately errors (`container.go:391-395`) and `attachOrRun` falls through to `Run`, which finds the existing `Running` pause pod, does **not** recreate it, and re-execs (`container.go:190-233`). Because `execProcess.supervised()` explicitly covers `db.ContainerTypeAgent`:

```go
// atc/worker/jetbridge/process.go:804-821
// Agent steps REQUIRE supervision — web-restart resume (attachOrRun) …
// built on the supervisor keeping the process alive across severed exec sessions
return t == db.ContainerTypeTask || t == db.ContainerTypeAgent
```

…the re-exec is wrapped in `supervisorCommand` (`process.go:894-899`), and the supervisor script refuses to restart a live command:

```sh
# atc/worker/jetbridge/supervisor.go:43-45
if [ ! -f "$S/exit" ] && ! alive; then
  rm -f "$S/child"
  ( trap '' HUP
```

A live pid is not restarted — the new session just `tail -n +1 -f`s the log and waits for the exit file (`supervisor.go:67-73`). `trap '' HUP` shields the agent from pty teardown. The supervisor state dir is byte-stable across webs: it hashes the process ID plus the command (`supervisor.go:84-94`), and the agent step uses the constant process ID `"agent"` with the bare command `agent-runner` and no args (`agent_step.go:38`, `:622-634`). `streamInputs` is a no-op on the DaemonSet backend (`process.go:1086-1088`), so a takeover does not re-stream inputs over the live agent's workspace.

**The agent attaches. No new agent, no token re-spend.**

Checkpoint code is entirely off this path. With `--agent-checkpoint-enabled` unset, `step.checkpointCapture` is nil, `checkpointIdentity` returns `enabled=false` (`atc/exec/agent_checkpoint_step.go:121-123`), the owner never becomes `NewAgentAttemptContainerOwner`, `attachOrRunAgentAttempt` calls plain `attachOrRun` (`agent_step.go:1005-1016`), and `agent_run_attempts` is never written. Only compile-time imports remain.

### Known gaps in that survival (pre-existing, not caused by the removal)

- **GAP 1 — silent full re-spend when the pod is gone.** If the pause pod is absent or terminal when a restarted web attaches, `Attach` errors, `Run` deletes the terminal pod and creates a fresh one (`container.go:208-219`), and the fresh pod's empty `/tmp` makes the supervisor start `agent-runner` from scratch. **It is worse than that:** the reused container is flagged `reused=true` (`worker.go:137`), which prepends a `cleanup-stale` init container that `rm -rf`s everything under the handle's hostPath (`storage_daemonset.go:557-585`, `container.go:627-630`). So the workspace is wiped *and* a brand-new agent starts, with no interruption event and no operator signal. This is exactly the scenario checkpoint was built for; the gap exists today because checkpoint is off. Out of scope here — file a ticket, and at minimum instrument it (see §7).
- **GAP 2 — the timeout clock restarts.** `MaybeTimeout` is applied on each run and `started` is a fresh local per `Run` (`agent_step.go:614-618`, `:644`), so a resumed step gets a full fresh timeout and re-fires `Initializing`/`Starting`.
- **GAP 3 — log replay.** The supervisor replays the entire log from line 1 on takeover (`supervisor.go:67`), duplicating agent output in the build event stream. Cost is *not* double-charged: the observer takes a per-field max (`agent_step.go:1289-1302`) and the ledger charges a delta against the previous metrics row (`agent_step.go:1379-1400`).

### What proves it in CI today

`make test-unit` already runs two specs that prove the mechanism end to end — `atc/worker/jetbridge/supervisor_script_test.go:66` ("takes over a still-running command without restarting it", real `sh`, asserts the marker appears exactly once) and `atc/worker/jetbridge/integration_test.go:142` ("task reattach after web restart", asserts `pods.Items` has length 1 and a byte-identical re-exec). **But both bind `db.ContainerTypeTask`.** The agent-specific binding — `supervised()` covering `ContainerTypeAgent` — is proven only by `atc/worker/jetbridge/live_agent_resume_test.go` (`//go:build live`, needs a real cluster). Closing that is Phase 1.

---

## 3. Scope

Three distinct things share the word "checkpoint" in this tree. Do not conflate them.

**(A) Agent snapshot — OUT OF SCOPE, KEEP ENTIRELY.** The sealed typed record: `agent_snapshots`, `agent/snapshot/`, `agent/resourcecapture/`, the capture-resource, `agent/hangar/` (`KindSnapshot`), `atc/api/snapshots`, the snapshot orphan sweep from `4d81908297`. Verified structurally independent: separate Hangar kind, separate inventory route, separate reclaim chain. `composeAgentSnapshots` (`command.go:2100-2204`) owns the shared mTLS `DaemonClient` that `composeAgentCheckpoints` merely borrowed; removing the borrower does not touch the owner.

**(B) Checkpoint / interrupt / resume — REMOVED.** Workspace capture, the attempt model, the effect journal, safe-boundary signalling, and recovery admission. `agent/checkpoint/`, `agent/runner/recovery.go`, `agent/runner/boundary_control.go`, `atc/exec/agent_checkpoint_*.go`, `atc/exec/agent_attempt_launch_test.go`, `atc/runtime/checkpoint_{capture,restore}*.go`, `atc/worker/jetbridge/checkpoint_*.go` + `safe_boundary*.go`, `cmd/artifact-daemon/checkpoint_*.go`, `atc/db/agent_run_{attempts,checkpoints,events}_factory.go`, `atc/metric/otel_agent_checkpoint*.go`, the seven CLI flags, the three Prometheus alerts, the operator runbook, and nine database tables.

**(C) Pod interruption classification — KEEP, UNCHANGED.** Commit `62c3ef0aa7` made pod deletion / eviction / node loss / scheduler preemption a typed `runtime.InterruptionError` implementing `RetryableError`, and all nine step types are wrapped in `exec.RetryError`. Keep `atc/runtime/types.go:21-66` verbatim, `atc/worker/jetbridge/process.go:392`/`:396-462`/`:1312-1313`/`:1377`, `atc/exec/retry_error_step.go`, and all nine wraps at `atc/engine/step_factory.go:232,257,289,310,372,413,435,455,477`.

> **Caveat on (C):** that commit is not clean at commit granularity. `62c3ef0aa7` also added `atc/db/container_owner.go:36-116` (`NewAgentAttemptContainerOwner`), whose only non-test caller is `agent_step.go:705` inside `if checkpointEnabled`. Delete the owner plumbing; keep the interruption work.

---

## 4. Phased plan

> **Sequencing amendment (2026-08-05, during execution).** The bounded auto-retry from owner
> decision 2 is split OUT of Phase 2b into its own phase after the drop migration. Removal and
> behaviour change should not land in the same commit: the removal is mechanical and provably
> behaviour-preserving, while auto-retry is new behaviour with its own migration, its own tests, and
> its own rollback story. Phase 2b therefore keeps the fail-closed branch with the message reworded,
> and Phase 5 introduces bounded retry against a tree that no longer has checkpoints in it.
>
> Verified during execution that this is safe: `step.ingestFlightRecorder(...)` is called
> unconditionally *before* the `if !interrupted { break }` check, so an interrupted agent does write
> an `agent_run_metrics` row. The `ingestion_seq` counter Phase 5 needs therefore does count
> interruptions, which was the open risk in the design.
>
> Revised order: 0 cost fix · 1 reattach guard · 2 excise · 3 delete · 4 drop migration ·
> **5 bounded auto-retry** · 6 verification sweep.

Commit ladder is dependency-ordered so `go build ./...` and the affected suites pass at every commit. Where a mixed file and the wholly-owned files it references are mutually dependent (`atc/exec`, `atc/worker/jetbridge`), the excision and the deletion **must land in the same commit** — this is called out per phase.

---

### Phase 0 — Fix the `cost_usd = 0` regression

**Goal.** Re-source node-occurrence cost, start and completion from `agent_run_metrics` instead of the always-empty `agent_run_attempt_metrics`. This is the only user-visible defect in the whole track, it is independently valuable, and it removes the last surviving reader of `agent_run_attempt_metrics` so the Phase 4 drop is inert.

**Evidence it is broken live.** `agent_workflow_run_node_occurrences` holds 9 rows, `sum(cost_usd) = 0.000000`. The same `plan_id`s in `agent_run_metrics` carry $0.22–$2.96 each ($127.78 across 105 rows). Chain: `atc/db/agent_workflow_run_evidence_factory.go:137-163` reads `agent_run_attempt_metrics` exclusively; its only writer is `agent_step.go:1351` behind `if attempt != nil`, which requires `checkpointEnabled`; so `len(metrics) == 0` and every agent node falls to `fallbackFromBuildStep` (`agent/workflowrun/occurrence/derive.go:170-175`), which sets **only** `Status` (`derive.go:200-207`). Both the freeze path (`freezer.go:97`) and the live read path (`reader.go:97`) call the same `Derive`, so the run graph and node detail show $0 today (`web/elm/src/AgentWorkflowRun/NodeDetail.elm:185`).

**Files touched.**
- `atc/db/agent_workflow_run_evidence_factory.go:137-163` — repoint the query, update the doc comments at `:22-23` and `:40-49`.
- `atc/db/agent_workflow_run_evidence_factory_test.go` — rewrite the attempt-metrics specs (`:253`, `:287`, `:339-351`) against `agent_run_metrics`.
- Comment-only: `agent/workflowrun/occurrence/occurrence.go:51`, `:75`, `derive.go:249`, `atc/db/agent_workflow_run_node_occurrences_factory.go:16`.

**The query. The obvious form is wrong — read this before writing it.**

`agent_run_metrics.created_at` is the **completion** timestamp, not the start. The row is written by `ingestFlightRecorder` after `process.Wait` returns, and the column is `DEFAULT now()` (`1773106060_create_agent_run_metrics.up.sql:24`). Verified live: build 662985 ended at 18:21:36.194 and its metrics row was created at 18:21:36.103. Synthesising `created_at AS started, created_at + wall_time AS completed` therefore shifts **both** timestamps forward by a full run duration. The correct direction is backwards:

```sql
SELECT
    plan_id,
    1                                                        AS execution_attempt,
    status,
    cost_usd::float8,
    created_at - (wall_time_seconds * INTERVAL '1 second')    AS created_at,
    created_at                                                AS updated_at
FROM agent_run_metrics
WHERE build_id = $1
ORDER BY plan_id
```

Keep the `AgentNodeAttemptMetric` struct shape (`:40-49`) so `occurrence.AttemptMetric` and `derive.go` need no change. `agent_run_metrics` is unique on `(build_id, plan_id)` with no attempt dimension, so `execution_attempt` is pinned to `1`; the projection's `PRIMARY KEY (workflow_run_id, node_id, retry_attempt, attempt)` still varies on `retry_attempt`, so no key collision results. **No schema change.** When `wall_time_seconds = 0` (degraded ingestion) the interval collapses to a point, which reads as "unknown duration" rather than as a wrong number — that is the honest degradation and is why this is preferred over adding a column.

**Acceptance criteria.**
- A build with an `agent_run_metrics` row produces an occurrence with non-zero `CostUSD`, `StartedAt` strictly before `CompletedAt` when `wall_time_seconds > 0`, and `DurationSeconds == wall_time_seconds`.
- A node with **no** metrics row still falls through to `fallbackFromBuildStep` and yields status-only (regression guard — do not break deterministic task nodes).
- `occurrence.Attempt == 1` for every agent node; the API still emits `attempt` and `retry_attempt` (`agent/api/workflowruns/graph.go:53-54`) and the Elm decoder is unchanged.

**Test command.** `ginkgo --focus="AgentWorkflowRunEvidence" ./atc/db/` then `go test ./agent/workflowrun/...`

**LOC (est.).** +15 / −10 production, +90 / −40 test. Net ≈ **+55**.

**Optional Phase 0b — repair the 9 bad rows.** The immutability trigger is `BEFORE UPDATE` only (verified live via `pg_get_triggerdef`), so `DELETE` is permitted, and all 8 source builds still exist. A delete plus re-derive would restore real costs. **UNVERIFIED:** whether `agent/workflowrun/reconciler.go` re-invokes `FreezeRun` for a run that already reached a terminal state. If it does not, this needs a one-off script; if that is not worth it, leave the rows and document the window (workflow runs 28–36).

---

### Phase 1 — Guarantee agent web-restart survival with a CI regression test

**Goal.** The one thing that must survive is currently proven only by a `//go:build live` test. Before we touch anything, put an agent-typed reattach spec into `make test-unit` so the removal is provably behaviour-preserving.

**Files touched.**
- `atc/worker/jetbridge/integration_test.go` — new spec, modelled directly on `:142-193` ("task reattach after web restart") but binding `db.ContainerTypeAgent` at both `FindOrCreateContainer` sites, process ID `"agent"`, command `agent-runner` with no args.

**Acceptance criteria.** After a simulated web restart (fresh worker/container objects, same owner):
1. `FindOrCreateContainer` resolves the same handle and therefore the same pod name — assert `pods.Items` has length **1** ("no second pod may be created on reattach").
2. The re-exec command is byte-identical to the first, i.e. `supervisorCommand` resolved the same state dir (`/tmp/concourse-task-agent-<sha256(cmd)[:8]>`).
3. The exit status is recovered exactly once.

**Do not** try to write this at the `atc/exec` layer first — there is no existing harness there for re-entering `AgentStep.Run` with no in-memory state, and building one is a larger job than the guarantee needs. The jetbridge-layer spec covers the actual mechanism.

**Test command.** `ginkgo --focus="reattach" ./atc/worker/jetbridge/`

**LOC (est.).** +120 test. Net ≈ **+120**.

---

### Phase 2 — Unwire and excise checkpoint from mixed files

**Goal.** Remove every reference to category (B) from files that survive, so the wholly-owned packages become an unreferenced island. Ordered leaf-consumer-first.

#### 2a. Composition root, chart, alerts, docs

- `atc/atccmd/command.go` (~200 lines): drop the `agent/checkpoint` import (`:38`); fields `agentCheckpointMu`/`agentCheckpointStepConfig`/`agentCheckpointReclaimer` (`:247-249`); the entire `AgentCheckpoints` flag group (`:353-361`, 7 flags); `metric.InitOTelAgentCheckpoint()` (`:845`); the `composeAgentCheckpoints` call (`:1000-1002`) and body (`:2206-2291`); `SetBuildLookup` stays (`:1678`); the reclamation components (`:1761-1765`, `:2293-2341`); `agentCheckpointCoreStepFactoryOptions` (`:2639-2647`); `validateAgentCheckpoints` (`:3193-3195`, `:3357-3400`); the option append (`:3593-3595`).
- `atc/component.go:29` — delete `ComponentAgentCheckpointGC`.
- Delete `atc/atccmd/agent_checkpoint_composition_internal_test.go`, `agent_checkpoint_reclamation_internal_test.go`; prune `atc/atccmd/export_test.go:14-16` (`ValidateAgentCheckpointsForTest`) and the ~39 matching lines in `command_test.go`.
- `deploy/chart/templates/prometheusrule.yaml:14`, `:156-182` — delete the three alerts (`ConcourseAgentCheckpointCaptureFailures`, `ConcourseAgentRecoveryManualReviewRequired`, `ConcourseAgentCheckpointRestoreFailures`) and fix the header comment. **Keep every Hangar alert.**
- `deploy/chart/templates/NOTES.txt:89`, `deploy/chart/tests/agentic_config_test.go:186-220`, `deploy/chart/tests/metrics_test.go:193-202`.
- Delete `docs/operations/agent-checkpoint-recovery.md`; add a forward note (not a rewrite) to `docs/agentic/V3_CUTOVER_DEPLOY.md:128`, `docs/migration/DATABASE-MIGRATION-RUNBOOK.md:165`, `docs/migration/migrate-preflight.sh:66`.

Verified: no chart template or values key emits `--agent-checkpoint-*` or `CONCOURSE_AGENT_CHECKPOINT_ENABLED`, so the `aad91a9911` unrecognized-flag boot failure does not apply within this repo. External overlays are still unaudited (§7).

#### 2b. `atc/exec` — excision **and** deletion in one commit

`atc/exec/agent_step.go` is 1,711 lines of which ~400 are checkpoint, across 12 discontiguous regions: `:21` import, `:88-90`, `:118-127`, `:157`, `:281-283`, `:578-611`, `:642-643`, `:647-935` (the whole `for {}` loop), `:1005-1029`, `:1044`, `:1225-1252`, `:1342-1373`.

- Collapse the `for {}` retry loop to a single pass. It only ever iterates once when checkpoint is off (`if !interrupted { break }`, and the interrupted path returns). Replace `attachOrRunAgentAttempt(...)` at `:826` with plain `attachOrRun(ctx, container, agentSpec, pio)` and delete `attachOrRunAgentAttempt`/`sameAgentCheckpointRecoveryAttempt` (`:1005-1029`). Replace every `failRecoveryLaunch(x)` with plain `x` (`:735, 744, 750, 754, 762, 772, 806, 816, 837`).
- `:883` — the interrupted-attempt output-registration guard becomes unconditional `step.registerLegacyOutputs(...)`. **Do not "salvage" it as `if !interrupted`.** The hazard its comment names (first `RegisterArtifact` wins silently) requires two `registerLegacyOutputs` calls inside *one* artifact scope, which only the internal loop produced: `retry_step.go:28` opens `state.NewArtifactScope()` per attempt, `across_step.go:120` opens `state.NewLocalScope()` per value, and `engine.go:385-387` clears the RunState on a `Retriable` re-pickup. Rewriting it would be a behaviour *change* — today, with checkpoint off, an interrupted step does register its outputs.
- `:1225-1252` — keep the else-branch `step.transcriptStore.Upsert(tr)` as the unconditional path.
- `:1342-1373` — delete the `if attempt != nil` block; fall through to the existing `UpsertReturningInserted`/`InsertIfAbsent` + budget-checker ledger path.
- `:281-283` — delete the `CONCOURSE_AGENT_RECOVERY` deny (see §7; recommendation is delete).

> **HIGHEST-RISK EDIT — `:903-933`. Behaviour CHANGES here; this is the one place the removal is not behaviour-preserving.**
>
> Today, with checkpoint disabled, an interruption observed by a live web is deliberately **swallowed**: `if !interrupted { break }` falls through the skipped `if checkpointEnabled` block to `delegate.Errored(logger, "manual_review_required: …")` and `return false, nil`.
>
> **Owner decision: auto-retry instead — bounded.** Let the `runtime.InterruptionError` escape so `exec.RetryError` wraps it `Retriable` and agent steps behave like the other eight step types.
>
> **Why a bound is mandatory, not a nicety.** `engine.go:244-246` returns *without* calling `finish` on a `Retriable` error, leaving the build `started`; `builds/tracker.go:105-108` then re-picks it up on the next cycle and re-runs the whole plan. There is **no retry counter, no backoff, and no cap anywhere on that path** — the engine's own check-build carve-out concedes the hazard ("if a check build drops into endless retry, there is no way to abort it"). For a task step this is benign because `attachOrRun` reattaches. For an agent step whose pod is *gone* — which is exactly the eviction case being retried for — each cycle triggers the `cleanup-stale` init container to `rm -rf` the workspace and starts a brand-new agent at full spend, forever, until a human aborts the build. At the measured $1.70/run that is a runaway.
>
> **Required design.**
> - Cap interruption-driven restarts per `(build_id, plan_id)` at a small default (**2**), operator-configurable. Above the cap, fall back to today's terminal `delegate.Errored(...)` + `return false, nil` with a message naming the cap.
> - The counter must be durable (the whole point is surviving a web that died). With the attempt model gone, add a monotonic **`ingestion_seq`** column to `agent_run_metrics`. This does double duty: it is also the cheapest fix for the ledger hole in §5, where a re-executed agent's abandoned spend currently goes invisible because the row is upserted `(build_id, plan_id)` and the ledger charges only a delta. One mechanism, two fixes — fold the §5 hole into this phase rather than deferring it.
> - **Budget backstop.** Refuse the restart when the run has exhausted its budget, regardless of count. The ledger and per-run budgets already exist; this bounds spend even if the counter is wrong.
> - Emit an interruption event/metric on every restart so the behaviour is observable rather than silent.
>
> The `DescribeTable` at `agent_step_test.go:2426-2442` (4 interruption reasons) **must be rewritten**, not preserved — it asserts the fail-closed behaviour being replaced. Its replacement must assert: retriable below the cap, terminal at the cap, and terminal when over budget.
>
> **Duplicate-effect safety** for the retry path is analysed in §5: `directgit`'s remote marker ref reconciles rather than re-pushes even against a wiped database, so a bounded retry does not double-push. PR mode has no such marker but cannot boot today.
>
> **Also note, and neither the brief nor the current code comments say this:** `return false, nil` is **not** inert under `attempts:`. `RetryStep.Run` continues to the next attempt when a step returns `ok == false` with no error (`atc/exec/retry_step.go:34-45`), so an interrupted agent step under `attempts: 3` already re-spends up to three full agents today. That is a pre-existing behaviour, unchanged by this removal, and it is worth a separate decision (§7).

Delete in the same commit: `atc/exec/agent_checkpoint_{capture,capture_metrics,execution,intents,provenance,recovery,recovery_metrics,step}.go` and their tests, plus **`atc/exec/agent_attempt_launch_test.go`** — it calls `attachOrRunAgentAttempt(...)` directly at `:15` and `:27` and breaks the instant that function is collapsed. (This file was missing from every earlier enumeration.)

Test surgery: `atc/exec/agent_step_test.go` (2,823 lines) — delete the `Context("with server-owned checkpoint capture configured")` block and the recovery contexts (`:353-~750`), the fakes at `:2733-2770`, the import at `:18`, and the `NewAgentAttemptContainerOwner` / `CONCOURSE_AGENT_RECOVERY` expectations at `:402, 426, 576, 588-591, 682, 850, 1285`. ≈700–900 lines.

#### 2c. `atc/worker/jetbridge` — excision **and** deletion in one commit

- `container.go` (~140 lines): import `:19`; `checkpointMu`/`checkpointActive` `:73-74`; the `CheckpointRestore` recreate guards `:181-186`, `:209-211`; the Attach materialization gate `:314-319`; `:328-345` → restore the unconditional historical fast path `return &exitedProcess{id, result}`; volume/mount/gate wiring `:607-613`, `:638-642`, `:656-659`; `checkpointRestoreGate` + its shell constant + the four name constants `:838-905` (**including the duplicate `checkpointRestoreGatesDirectoryName` at `:843`**); `checkpointSessionVolume` `:907-940`; drop `|| c.containerSpec.CheckpointRestore != nil` at `:1340`; the restore annotation `:1656-1658`. Also drop `checkpointCaptureHandle`, `matchesMaterializedRecoveryPod`, `requireMaterializedRecoveryPod`, `exitedProcessWithTerminalEvidence` once unreferenced. **The annotation path at `:372-390` must be untouched** — `container_test.go:2365` and `:2442` are its guards.
- `process.go` (~30 lines): the six `CheckpointRestore` guards at `:906-908`, `:1054`, `:1077-1085`, `:1199-1201`, `:1336-1338`, `:1410`; `annotateSupervisorState` `:1127-1135` and its call site `:911-914`; `supervisorStateAnnotationKey` (`container.go:39`) and the `stateDir`/`persistedExit` fields on `newExitedProcess` (`container.go:401-417`). **Keep `supervised()` at `:804-821` exactly as-is** — reword only the "checkpoint park protocol" phrase in its comment.
- `supervisor.go:44`, `:49-60`: the `$S/child` pid+starttime record is read only by `safe_boundary.go:300,:344` and `checkpoint_process_quiescence.go:109`. Safe to remove — `supervisorState` hashes the **command**, not the script, so changing the script text does not change the state dir and an in-flight upgrade still takes over (the new script's `alive()` reads `$S/pid` written by the old one). **Keep `$S/pid`, `$S/log`, `$S/exit`, and `trap '' HUP`.**
- `storage_daemonset.go:31`, `:69`, `:85-100` — `restoreClient`, its setter, `CheckpointSessionVolume`. **Keep `BuildCleanupInitContainer`.**
- `daemon_client.go:37` — the `checkpointEndpoints` field.

Delete in the same commit: `safe_boundary.go` + test, `checkpoint_{capture_client,object_store,preemption_client,preemption_process,process_quiescence,restore_client,restore_materializer}.go` + tests, `checkpoint_session_volume_test.go`, `terminal_checkpoint_capture_test.go`.

#### 2d. Shared runtime and engine

- `atc/runtime/types.go`: drop the `agent/checkpoint` import (`:10` — its only use is `Archive checkpoint.Archive` at `:268`), `ContainerSpec.CheckpointCapture`/`CheckpointRestore` (`:196-204`), `CheckpointRestoreDescriptor` (`:265-273`), `PreLaunchMaterializer` (`:274-279`). **Keep `:21-66` verbatim.**
- `atc/engine/step_factory.go:43`, `:157-165`, `:348-350` — the `agentCheckpointCapture` field, the `WithAgentCheckpointCapture` option, the opts append. **Keep `:372`.** Prune `step_factory_test.go:105-136`.
- Delete `atc/metric/otel_agent_checkpoint.go` + test; drop the init call at `atc/metric/testhelpers/otel_test_provider.go:39`. `RecordK8sPodFailure` lives in `otel_metrics.go:151` and is unaffected.

#### 2e. Database factories

- Delete `atc/db/agent_run_{attempts,checkpoints,events}_factory.go` + tests. Note: **no counterfeiter fakes were ever generated** for these three despite their `//counterfeiter:generate` directives, so this is a clean cut.
- `atc/db/agent_run_metrics_factory.go` — remove `metrics.AttemptStore` from the interface (`:23-26`), `UpsertExecutionAttempt` (`:41-147`) and every helper it owns (`durableMetricAttempt`, `readDurableMetricAttempt`, `validateExecutionAttempt`, `counterValues`, `counterDelta`, `readExecutionAttemptMetric`, `sameAttemptMetricIdentity`, `hasAttemptMetrics`, `finalizedAttemptMetric`, `upsertExecutionAttemptMetric`, `applyAttemptAggregateDelta`, `insertAttemptAggregate`, `insertExecutionAttemptLedger`, `mergeAttemptEventCounts`, `addEventCounts`, `hasLedgerCounters`). **Keep `UpsertReturningInserted`, `InsertIfAbsent`, `GetByBuild`, `ListByWorkflowRun`, `ListRecent`, `runMetricsColumns`.**
- `atc/db/agent_run_transcript_factory.go` — remove `AgentRunAttemptTranscript` (`:35-51`), the three `Err*` sentinels (`:53-57`), `UpsertExecutionAttempt` (`:66`, `:107-164`) and its helpers (`:166-263`). **Keep `Upsert`, `ListByWorkflowRun`, `AgentRunTranscript`** — those back the live transcript viewer.
- `atc/db/container_owner.go:36-116` — `NewAgentAttemptContainerOwner`, `agentAttemptHandlePrefix`, `agentAttemptContainerOwner`. At attempt 1 it is byte-for-byte the plain build-step owner (`:52-55`), so removal is identity-neutral. Prune `container_owner_test.go:15-60`.
- `agent/api/metrics/types.go` — remove `ExecutionAttempt*` types, `CounterDelta`, `AttemptStore`, `ValidExecutionAttemptAttribution`, `canonicalExecutionAttemptDimension` (`:12-92`). **Keep the `Store` interface (`:94-157`) verbatim** — its doc comment about web-restart-resume idempotency describes behaviour that must survive.
- Regenerate exactly two fakes with `go generate ./...`: `atc/db/dbfakes/fake_agent_run_metrics_factory.go`, `atc/db/dbfakes/fake_agent_run_transcript_factory.go` (plus `agent/api/metrics/metricsfakes/*`). Do not hand-edit.

#### 2f. `agent/` — runner, provider, hangar

- `agent/runner/runner.go` (~50 lines): `Config.RecoverySpec` (`:90-92`), the `os.Getenv(recoveryTransportEnv)` read (`:168`), `decodeRecoverySpec`/`appendRecoveryNotice` (`:356-363`), the adapter-match guard (`:515-517`), the native-resume branch (`:530-547`) — keep the else body `running, startErr = adapter.Start(ctx, startRequest, adapterControl)` as the unconditional path.
- Delete `agent/runner/recovery.go` + test, `agent/runner/boundary_control.go` + test. **The earlier UNVERIFIED flag on boundary_control is now closed:** a non-test grep for `USR1|USR2` across `atc/` and `cmd/` returns only `atc/worker/jetbridge/safe_boundary.go:27,92,324,333,355`; the in-pod supervisor script sends no signals (it only has `trap '' HUP`). Deleting the handler is safe.
- `agent/provider/adapter.go` — remove `RecoveryProof` (`:40-70`), `Boundary` (`:72-105`), `BoundaryControl` (`:107-111`), `ResumeRequest` (`:124-139`), `RecoveryAdapter` (`:168-174`), the recovery bits of `Capabilities`, and the `BoundaryControl` parameter on `Adapter.Start`. Rewrite the package doc at `:1-3`, which currently defines the package as the "safe-boundary seam". **Keep `Identity`, `StartRequest`, `Result`, `RunningSession`, `Adapter`.** Fix `agent/runner/legacy_adapter.go:16,37`, `agent/runner/provider_seam_test.go`, `agent/provider/adapter_test.go`, `agent/runner/runner_test.go` (all three break on the signature change even though they contain zero literal "checkpoint" matches).
- Delete `agent/checkpoint/` entirely (13 files, 3,352 lines).
- `agent/hangar/types.go:29` (`KindCheckpoint`), `keys.go:12`, `cmd/artifact-daemon/hangar_inventory.go:35`. **Safe, verified empirically, not by construction:** the live Hangar bucket contains only `hangar/v1/snapshots/sha256/` (37 objects, matching `agent_snapshots = 37`); there is no `checkpoints/` prefix and `agent_checkpoint_objects` is empty. Commit `631584637e`'s "every captured checkpoint is still in the store" describes a code defect in the abstract — that set is empty because the feature never ran. Update `keys_test.go:13,73`, `list_test.go:18,79`, `gcs_test.go:447-449`, `gcs_integration_test.go:173,220`, `hangar_inventory_test.go:93,210,220,309`. The `ConcourseHangarStore*` alerts use `max by (kind)`, so dropping a kind is safe.

#### 2g. `cmd/artifact-daemon`

- Delete `checkpoint_{capture,objects,restore}.go` + tests.
- `server.go:63-74`, `:226-232`, `:349-355` — the 8 checkpoint fields, their initializers, and all 7 `/checkpoints/v1/*` route registrations.
- `metrics.go:22-24`, `:74-88`, `:118-120`, `:171-187` — the three Prometheus collectors and `recordCheckpoint()`. No dashboard references them (the alerts used the OTel names).
- `security.go:84` — drop the `checkpointRestoreGatesDirectory` clause. **This is a hard compile break if missed**, since the constant lives in the deleted `checkpoint_restore.go:22`.
- `preemption.go` — **KEEP THE FILE.** Remove only `handlePreemptionNotice` (`:98-123`), `parsePreemptionNoticeAfter` (`:125-134`), `parsePreemptionNoticeWait` (`:136-145`), `maxPreemptionNoticeWait` (`:27`), and the `RecordPreemptionNotice` call inside `startPreemptionWatcher`. **Keep `DefaultPreemptionMetadataURL`, `startPreemptionWatcher`, `PreemptionWatcher`/`NewPreemptionWatcher`/`Run`/`poll`** — the watcher's surviving job is `mirror.Evacuate(ctx, budget)` (`mirror.go:502`), which closes the async-mirror window and has nothing to do with checkpoint. `--preemption-watch`, `--preemption-budget`, `deploy/chart/values.yaml:599-616`, `artifact-daemon-daemonset.yaml:116-118`, `artifact-daemon-networkpolicy.yaml:150-151` all stay.

**Acceptance criteria for Phase 2.** `go build ./...` and `go vet ./...` clean after **every** sub-commit. `grep -rln 'concourse/agent/checkpoint"'` returns only files inside the Phase 3 delete set. No behaviour change to interruption handling: the `manual_review_required` `DescribeTable` still passes.

**Test command.** `go build ./... && ginkgo -r ./atc/exec/ ./atc/worker/jetbridge/ ./atc/engine/ ./atc/runtime/ ./atc/atccmd/ ./agent/ && go test ./deploy/chart/tests/`

**LOC (est.).** ≈ **−1,900 production, −1,500 test**, plus the co-located deletions counted in Phase 3.

---

### Phase 3 — Delete the wholly-owned island

**Goal.** With no surviving references, delete the remaining category (B) packages. (`atc/exec/agent_checkpoint_*`, the jetbridge checkpoint files, `agent/checkpoint/`, `agent/runner/recovery*`, `boundary_control*`, and the daemon handlers land in their Phase 2 sub-commits for build-integrity reasons; what remains standalone here is bookkeeping.)

Verified delete-set size: **72 tracked files, ~20,021 lines** (`git ls-files | xargs wc -l`; earlier counts of 19,957 / 19,824 predate `agent_attempt_launch_test.go` and `boundary_control*`).

Remaining standalone deletions: `atc/db/migration/agent_run_{checkpoints,attempts,attempt_metrics,attempt_transcripts}_test.go` (648 lines) — these move to Phase 4 because they must be deleted in the same commit as the drop migration.

**Acceptance criteria.** `grep -rn -i 'checkpoint' --include='*.go' atc/ agent/ cmd/` returns only `atc/worker/jetbridge/supervisor.go` comment residue (fixed in 2c), `docs/`, and `bench/corpus/`. `deploy/chart/` returns nothing.

**LOC (est.).** ≈ **−20,000**.

---

### Phase 4 — Forward-only DROP migration

**Goal.** Drop nine tables, fourteen indexes, six triggers and **eight** PL/pgSQL functions.

**Strategy is settled by live evidence: forward-only, never an in-place amendment.** Migrations `1773106144/45/46/47` are all recorded `dirty=f, direction=up` at **2026-07-30 03:20:08 UTC** on theborg/cicd, and all nine tables exist. The precedent someone will cite for amending in place is itself a demonstrated failure: commit `5073a67eff` amended `1773106157/58` on the premise that they *"have never been applied to any durable database"*, but `migrations_history` shows both applied at 2026-08-02 06:03:52 UTC — **~23.6 hours before that commit was authored** — and the live `enforce_agent_workflow_run_node_occurrence_immutability` still carries the original unconditional `RAISE EXCEPTION` body, not the amended `wait_id` carve-out. That fix never reached production and never will. It is evidence *against* amend-in-place.

**Next free number is 1773106160** (head is `1773106159_admit_node_experiment_targets`).

**Files.**
- `atc/db/migration/migrations/1773106160_drop_agent_checkpoint_subsystem.up.sql` — drop in FK order: `agent_run_attempt_transcripts`, `agent_run_attempt_metrics`, `agent_run_attempt_fence_tokens`, `agent_run_attempts`, `agent_run_events`, `agent_run_effects`, `agent_run_checkpoints`, `agent_checkpoint_objects`, `agent_run_checkpoint_heads`. Then the **eight** functions — the earlier briefs said "six" and then listed eight; eight is right: `enforce_agent_run_checkpoint_head_identity`, `agent_run_checkpoint_head_cleanup_eligible(BIGINT)`, `enforce_agent_run_effect_transition`, `enforce_agent_run_event_append_only` (from 1773106144), `enforce_agent_run_attempt_insertion`, `enforce_agent_run_attempt_transition` (1773106145), `enforce_agent_run_attempt_metric_binding` (1773106146), `enforce_agent_run_attempt_transcript_identity` (1773106147). Indexes and triggers drop with their tables. Append a defensive, idempotent `DELETE FROM components WHERE name = 'agent_checkpoint_gc';` — `component_factory.go:63` upserts by name and the component was never registered, so this should be a no-op, but it costs nothing and closes the question permanently.
- `1773106160_drop_agent_checkpoint_subsystem.down.sql` — **ship a real one.** Copy the DDL from `1773106144-47` verbatim (~400 lines). The tables are provably empty, so a structural recreate is a complete rollback. Two `up`-only precedents exist (`1537546150`, `1554469235`) so a non-reversible down would not break the runner, but symmetry is the convention and the copy is mechanical. Note `1773106145` also creates `agent_run_checkpoints_head_id_generation` and `ALTER`s `agent_run_attempts` — the down must respect that ordering.
- **Do NOT delete or edit `1773106144-47`.** They are durably applied, and `atc/db/migration/experiment_resource_source_admissions_test.go:15` pins `beforeVersion = 1773106147`. Leaving them on disk means the migration chain and that test are untouched.
- New `atc/db/migration/drop_agent_checkpoint_subsystem_test.go` in the standard shape: migrate to `1773106159`, assert the nine tables and eight functions exist; migrate to `1773106160`, assert they are gone and that `agent_run_metrics`, `agent_run_transcripts`, `agent_snapshots`, `agent_workflow_run_node_occurrences` survive **with their rows**.
- Delete the four superseded migration tests (`agent_run_checkpoints_test.go`, `agent_run_attempts_test.go`, `agent_run_attempt_metrics_test.go`, `agent_run_attempt_transcripts_test.go`, 648 lines) in this same commit.

A pure-SQL migration is also the only form that complies with `atc/db/migration/migration_isolation_test.go:29-33`, which forbids anything under `atc/db/migration` from importing `agent/`, `atc/exec`, or `atc/worker`.

**Ordering constraint.** This must land **after** all writer code is gone (Phases 2–3) — otherwise a rolling deploy leaves an old web hitting a missing table.

**Acceptance criteria.** `ginkgo ./atc/db/migration/` green; the up/down round-trip test passes; `experiment_resource_source_admissions_test.go` still passes unmodified.

**Test command.** `ginkgo ./atc/db/migration/` then `ginkgo ./atc/db/`

**LOC (est.).** +450 SQL, +130 test, −648 test. Net ≈ **−70**.

---

### Phase 5 — Verification sweep

**Goal.** Prove the tree is clean and the surviving behaviour is intact before deploying.

- Full test tiers (§8).
- `grep -rn -i checkpoint --include='*.go' --include='*.yaml' --include='*.sql' . | grep -v docs/ | grep -v bench/corpus/` — expect zero.
- Diff the rendered chart before/after (`helm template`) and confirm no `--agent-checkpoint-*` appears in any rendered manifest.
- Manually run `atc/worker/jetbridge/live_agent_resume_test.go` (`go test -tags live ./atc/worker/jetbridge/ -run TestLiveAgentProcessResume`) against theborg/cicd once, as the belt-and-braces confirmation that the Phase 1 unit spec is measuring the right thing.
- On the live database, after deploy: confirm the run graph shows non-zero `cost_usd` for a freshly completed agent workflow run.

**LOC.** 0.

---

**Net across all phases: ≈ −23,300 lines.**

---

## 5. What we are knowingly giving up

### Duplicate external effects — protection exists outside the checkpoint code, and it is stronger than what we are deleting

The effect journal (`agent_run_effects`) is **write-never**. `BeginEffect` and `CommitEffect` (`atc/db/agent_run_checkpoints_factory.go:1047`, `:1106`) have no non-test callers; the single non-test consumer is `ListEffects` at `atc/exec/agent_checkpoint_recovery.go:260`, reading a table nothing writes. It protects nothing today.

Real duplicate-push protection lives in `agent/publisher/` in three independent layers:

1. **Content-keyed DB dedup.** `agent_publications.operation_key` is `UNIQUE` with a `^sha256:[0-9a-f]{64}$` check (`1773106110_create_agent_publications.up.sql`); the key is a sha256 over team, publisher, input snapshot, destination, mode, parameters and approval-policy version (`agent/publisher/types.go:384-410`). `Acquire` uses `ON CONFLICT (operation_key) DO NOTHING` then `SELECT … FOR UPDATE` (`atc/db/agent_publications_factory.go:281`).
2. **Wait-behind lease with terminal short-circuit.** `acquireForExecution` (`agent/publisher/acquire.go:16-47`) blocks on an in-flight lease and returns the recorded result without re-executing when the publication is already terminal (`agent/publisher/git.go:189-191`).
3. **Remote-side marker ref — the strongest, and database-independent.** The operation key is written as a ref **on the git remote** and looked up with `ls-remote --exit-code --refs` before every push (`agent/publisher/directgit/backend.go:100-134`, `:928-939`), created atomically under `--atomic --force-with-lease=<marker>:<zero>` (`:213-218`), and verified after the push with `ErrAtomicityViolation` (`:545-559`). Even against a wiped database, a second attempt reconciles instead of re-pushing. Merge mode adds a fourth gate: the merge is pinned to an exact base SHA and a moved destination is refused as `stale_base` (`backend.go:377-380`, `git.go:220-239`).

**Verdict: accepted loss, no replacement needed.** Record the reasoning in the removal commit message, because it depends on directgit's remote-marker design and would not hold for a publisher without one.

**The one residual gap: PR mode.** `PRStore` is deliberately separate from `Store` (`agent/publisher/store.go:97-104`) and has no marker-ref analogue. It costs nothing today — `atc/atccmd/agent_publisher.go:81-83` unconditionally fails web startup when PRs are enabled (`incompletePRAuthoritySpineError`). **Record as an open constraint:** whoever completes the PR authority spine must supply provider-side idempotency (search-by-operation-key in the PR body, or a branch-name marker), not assume a journal will exist.

### The Codex slice

Commit `ad1e70d27e` on branch `codex/agentic-platform-rebase` (**not an ancestor of HEAD**, so it does not block anything) reversed the prior explicit non-goal and now makes native Codex checkpoint recovery — *including a fenced begin/commit effect-journal producer* — mandatory for the Codex slice, with a checkpoint-recovery canary as the first live rollout gate. Left alone, the next Codex session will rebuild this entire subsystem from that document.

**Action:** amend it in the same PR as the removal. Restore the non-goal and the "Codex checkpoint/recovery is deliberately unsupported in this release" paragraph that `ad1e70d27e` deleted (recoverable verbatim from `git show ad1e70d27e^:<path>`). Separately, restate its one genuinely-surviving requirement — *"ATC restart is also a recovery case… a control-plane restart must never strand an attempt or invent a second active recovery"* — in terms of **container reattachment**, which is what §2 shows we actually have, rather than in terms of the attempt model.

### Per-attempt cost, transcript and timing analysis

`agent_run_attempt_metrics` and `agent_run_attempt_transcripts` are empty in production and cannot be retained independently: their identity triggers JOIN `agent_run_checkpoint_heads`, and their PK columns `REFERENCE agent_run_attempts(id) ON DELETE RESTRICT`. Per-attempt attribution becomes impossible. **Accepted** — the $/agent-hour work managed at n=75 without it.

### Quiet accounting hole worth naming, not fixing here

Post-removal, an interrupted-and-re-executed build overwrites one `(build_id, plan_id)` row in `agent_run_metrics`, and the ledger charges only `rm.CostUSD - prev.CostUSD` (`agent_step.go:1379-1400`). The abandoned attempt's real spend becomes invisible. `agent_run_attempt_metrics` was the only place per-attempt `cost_usd` ever existed. Cheapest fix without the attempt model: on an update where the existing row already carried a terminal cost, append the **full** new cost to `agent_cost_ledger` with a distinguishing reason instead of the delta; or add a monotonic `ingestion_seq` to `agent_run_metrics` so re-executions are countable. **Decide explicitly (§7); do not let it ride silently.** Note this hole is arguably larger than the `cost_usd = 0` freeze Phase 0 fixes.

### Also going, uncontroversially

- **Fence tokens** (`agent_run_attempt_fence_tokens`, `agent_run_attempts.fence_token`): with no effect producer and no second concurrent live attempt, they fence nothing.
- **`agent_run_events`**: 100% dead — `NewAgentRunEventsFactory` has zero production callers.
- **The "unavailability is not absence" contract** from `631584637e` survives independently on the snapshot side (`snapshot_durable_inventory.go` + `cmd/artifact-daemon/durable_inventory.go`, from `4d81908297`).

---

## 6. Risks and rollback

| # | Risk | Detection | Rollback |
|---|---|---|---|
| R1 | **The retry bound fails open** — `ingestion_seq` not persisted, counter reset by the upsert, or the budget backstop not consulted. `InterruptionError` escapes unbounded: build stays `started`, tracker re-runs the plan every cycle with a fresh agent, workspace wiped each time, spend unbounded until a human aborts. **This is now the single highest-consequence risk in the plan**, because Phase 2b deliberately opens the retry path that previously failed closed. | The rewritten `agent_step_test.go` table must assert *terminal at the cap* and *terminal over budget*, not just *retriable below it*. Live symptom: a build that never finishes, repeated `Starting` events, `agent_cost_ledger` climbing for one `(build_id, plan_id)`. | Revert the Phase 2b commit — that restores fail-closed. **Do not merge 2b without both the cap test and the budget-backstop test green.** Consider shipping 2b behind a flag defaulting to fail-closed for the first deploy. |
| R2 | **Web-restart survival regresses** because `supervised()` loses `ContainerTypeAgent`, or the owner/process-ID/command spec changes shape. Every restart starts a second agent at full spend. | Phase 1 spec fails (`pods.Items` length ≠ 1, or non-identical re-exec command). Live symptom: duplicated transcript, doubled cost. | Revert; the guarded lines are `process.go:815-821`, `agent_step.go:38`, `:622-634`, `:648`. |
| R3 | **Drain starts cancelling step contexts** as a side effect of some later refactor, making `killAgentOnTerminalEnd` fire on every web restart. | `process_test.go:1593` ("does not kill the agent on a transport sever with a live context") fails. | Restore `context.Background()` in `tracker.go:124` and the abort-only `cancel()` wiring in `engine.go:178-205`. |
| R4 | **Compile breaks from second copies of a constant.** `cmd/artifact-daemon/security.go:84` and `atc/worker/jetbridge/container.go:843` each hold a copy of `.checkpoint-restore-gates`. | `go build ./...` in the same commit. | Trivial. |
| R5 | **A missed test file.** `atc/exec/agent_attempt_launch_test.go` calls the collapsed function directly and was absent from every earlier enumeration. Assume there are others. | `go vet ./...` and a full `ginkgo -r` before the PR, not after. | Trivial. |
| R6 | **An external overlay passes `--agent-checkpoint-*`** and the new binary rejects the flag at boot — the exact failure mode fixed in `aad91a9911`. Verified absent from `deploy/chart` and from the live web Deployment's rendered args; **UNVERIFIED for ArgoCD overlays and CI pipelines**. | Web pod `CrashLoopBackOff` with an unrecognized-flag error immediately after rollout. | Roll back the image. Audit before deploying, not after. |
| R7 | **The drop migration runs against a database with rows.** All nine tables are empty on theborg/cicd; other deployments unverified. | Pre-flight query (below). | The `.down.sql` recreates structure but **not data**. |

### R7 is the one irreversible step. Capture this before running it:

```bash
# 1. Row counts — every one must be 0. Abort if not.
for t in agent_run_checkpoint_heads agent_checkpoint_objects agent_run_checkpoints \
         agent_run_effects agent_run_events agent_run_attempts \
         agent_run_attempt_fence_tokens agent_run_attempt_metrics \
         agent_run_attempt_transcripts; do
  psql -c "SELECT '$t', count(*) FROM $t"
done

# 2. Schema-only dump of the nine tables + eight functions, archived off-cluster.
pg_dump --schema-only -t 'agent_run_checkpoint*' -t 'agent_checkpoint_objects' \
        -t 'agent_run_effects' -t 'agent_run_events' -t 'agent_run_attempt*' \
        > checkpoint-schema-pre-1773106160.sql

# 3. Full migrations_history snapshot.
psql -c "SELECT version, dirty, direction, tstamp FROM migrations_history ORDER BY tstamp" \
        > migrations-history-pre-1773106160.txt

# 4. Hangar bucket listing — confirm no checkpoints/ prefix.
kubectl exec -n cicd deploy/hangar-fake-gcs -- find /data -path '*checkpoints*'
```

Everything else in this plan is a code change on a branch and is revertible with `git revert`.

**Deploy ordering.** Phases 0–3 can ship in one image. Phase 4's migration must run **with or after** that image, never before. Do not roll Phase 4 to a cluster still running an older web.

---

## 7. Open questions for the owner

These are decisions, not research gaps. Each needs an explicit answer before the corresponding phase merges.

1. ~~**Post-removal interruption behaviour.**~~ **DECIDED 2026-08-05: auto-retry, bounded.** See the header and the Phase 2b ruling. The unbounded-retry hazard was discovered while implementing this decision and is the reason for the cap, the `ingestion_seq` counter, and the budget backstop.
2. **`attempts:` interaction — now more urgent, given decision 1.** `RetryStep` re-runs on `ok == false` (`retry_step.go:34-45`), so an interrupted agent step under `attempts: 3` already burns up to three full agents today. With Phase 2b returning an *error* rather than `ok == false`, the interaction inverts: `RetryErrorStep` and `RetryStep` are different wrappers, and a step under `attempts:` will now compose the user's attempt budget with the new interruption cap **multiplicatively** (`attempts: 3` × cap 2 = up to 6 full agents). Decide whether the interruption cap should be global per `(build_id, plan_id)` — recommended, and what the `ingestion_seq` design gives you for free — or per attempt.
3. **GAP 1 policy.** When a restarted web finds the pause pod gone or terminal it (today) wipes the handle's hostPath via the `cleanup-stale` init container **and** starts a fresh agent, silently. Options: (a) leave as-is, (b) fail with `manual_review_required`, (c) fail loudly with a dedicated metric/log at `container.go:208-219`. The workspace-wipe detail makes (a) harder to defend than previously described. Recommend at minimum (c), as a separate ticket.
4. ~~**The ledger hole (§5).**~~ **RESOLVED by decision 1.** The bounded-retry design needs a durable per-`(build_id, plan_id)` restart counter, and `ingestion_seq` on `agent_run_metrics` serves both purposes. Fold the ledger fix into Phase 2b rather than deferring it — with auto-retry enabled, re-execution stops being hypothetical and the hole would start losing real money.
5. **Repair the 9 zero-cost occurrence rows?** UNVERIFIED whether `agent/workflowrun/reconciler.go` re-invokes `FreezeRun` for an already-terminal run. If not, repair needs a one-off script. Alternative: document workflow runs 28–36 as a known-bad window.
6. **`CONCOURSE_AGENT_RECOVERY` deny at `agent_step.go:281-283`.** Once the runner no longer reads the variable the guard protects nothing. Recommend delete; keeping it as a 3-line deny is cheap and preserves the platform-owned-env posture.
7. **Retire `occurrence.Attempt` entirely, or pin it to 1?** Retiring means a migration against `agent_workflow_run_node_occurrences` whose PK includes `attempt`, plus API and Elm changes. **Recommend pinning** (no-op); flagging only because the column now carries no information. Consider hiding the "attempt N" label in `NodeDetail.elm` when `attempt == 1`.
8. **Other JetBridge deployments.** Every live claim here — migration state, empty tables, empty Hangar bucket, absent CLI flags — is verified against **theborg/cicd only**. Per `MEMORY.md`, that is not the only deployment. Confirm before rollout.
9. ~~**Is the Codex slice actually shipping?**~~ **DECIDED 2026-08-05: undecided, flag and move on.** Proceed with removal. Amend `ad1e70d27e` to record that the checkpoint subsystem was removed on economic grounds, with a pointer to this plan — do not restore the old non-goal, and do not preserve the `EffectJournal` requirement. Whoever picks the Codex slice up inherits an accurate statement of what exists rather than a spec for a subsystem that was deliberately deleted. Note `ad1e70d27e` lives on `codex/agentic-platform-rebase`, which is **not** an ancestor of this branch, so the amendment is a separate cross-branch commit.

---

## 8. Verification

From `CLAUDE.md`'s test table. PostgreSQL must be running (`pg_isready`). Do **not** use `--race` — it causes parallel compilation failures.

**Per-phase (fast, run on every commit):**

```bash
go build ./... && go vet ./...
ginkgo ./atc/db/                      # Phase 0, 2e — largest suite, ~1300 specs, 2-3 min
ginkgo ./atc/worker/jetbridge/        # Phase 1, 2c — ~29 s
ginkgo -r ./atc/exec/                 # Phase 2b
ginkgo ./atc/engine/ ./atc/runtime/   # Phase 2d
ginkgo ./atc/atccmd/                  # Phase 2a
ginkgo -r ./agent/                    # Phase 2f
go test ./cmd/artifact-daemon/        # Phase 2g
go test ./deploy/chart/tests/         # Phase 2a — golden alert tests
ginkgo ./atc/db/migration/            # Phase 4
```

**Full sweep before the PR merges (~8 min, needs Postgres):**

```bash
make test-unit
```

This covers every package the removal touches: `atc/db`, `atc/db/migration`, `atc/exec`, `atc/worker/jetbridge`, `atc/atccmd`, `agent/*`, `cmd/artifact-daemon`.

**Full sweep before deploy — schedule these, they are slow:**

```bash
make test-integration          # ~12 s,  21 specs, needs Postgres
make test-fly-integration      # ~30 s,  576 specs
make test-k8s-integration      # ~23 min, KinD — MANDATORY: atc/runtime/types.go
                               #          ContainerSpec changes pod rendering
make test-k8s-behavioral       # ~2-3 hrs, 2 parallel KinD clusters — this is where
                               #          attach-after-restart is exercised end to end.
                               #          ~3/117 specs are known-flaky on GC timing.
```

**One manual live confirmation (needs a real cluster, cannot run in CI):**

```bash
go test -tags live ./atc/worker/jetbridge/ -run TestLiveAgentProcessResume
```

**Post-deploy smoke:** run one agent workflow to completion and confirm the run page shows a non-zero `cost_usd` per agent node — that is Phase 0's payoff and the single clearest signal that the evidence repoint landed correctly.
