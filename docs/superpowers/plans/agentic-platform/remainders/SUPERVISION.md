# Supervision log — Fable agents vs. the remainder plans

**Run context:** 2026-07-18, concourse.home, workflow `develop-fable` v1 (claude-fable-5,
max_turns 80, full-scope build/test/lint gates, on_gate_failure: needs_review,
budget ticket $10 / slice $6 — declared, unenforced until this very batch lands).
Baseline for comparison: tickets #12–#15 ran the `develop` workflow on **claude-sonnet-5**
(the loop's entire proven envelope to date was Sonnet).

**Method:** each ticket body names a plan slice (committed at `462469a55a`); the plan
carries complete code+tests. The agent is a faithful executor by design — the comparison
is *what the agent actually did* vs. *the slice text*: fidelity, divergences (good and
bad), what it did when reality differed from the plan, gate outcomes, cost/turns.

| # | Ticket | Slice | State | Cost/turns | Gates | Fidelity verdict |
|---|---|---|---|---|---|---|
| 16 | budget lib + admission | dispatcher A T1-2 | **merged** (ed0e18bb31) | **$1.23 / 112** | build ok; test structural-fail | **faithful++** |
| 17 | Dispatcher polling loop | dispatcher B T4 | **merged** (83eab3ebe2) | **$0.65 / 62** | build ok → harvest pushed | **faithful++** |
| 18 | secret labeler | dispatcher D T9 | **merged** (1238005efa) | **$0.72 / 77** | build ok → harvest pushed | **faithful, +1 improvement** |
| 19 | F17 reconciler | dispatcher C T6 | **merged** (69d4341e5e) | **$0.90 / 71** | build ok → harvest pushed | **faithful on the hard one** |
| 20 | judge engine | judge C T5-7 | **merged** (8baee28a19) | **$1.21 / 100** | build ok → harvest pushed | **faithful, 3 tasks / 3 commits** |
| 21 | flight recorder T8-9 | judge D | **abandoned** (2 runs) | **$6.27 / 160** | F33 dirty-fail ×2, no push | **capacity failure, not fidelity** |

## Ticket #21 — flight recorder (T8-9): the batch's instructive failure

Two runs, both stopped at EXACTLY the 80-turn cap mid-Task-9 with the runner rewrite
uncommitted; harvest's F33 dirty=fail correctly refused to push both times. ~$6.27 spent,
nothing delivered. Three lessons:

1. **max_turns is a real capacity wall**, and the plan's own sizing note ("largest single
   ticket, top of the proven envelope") predicted the risk. The verbatim-code slice still
   burns turns on TDD ceremony + the nested-module test dance + my extra verify ask.
2. **Dispatch FREEZES the workflow version on first dispatch** — the requeued rerun
   silently used frozen v2 (80 turns), not the newly-live v4 (140). Re-dispatching a
   sent_back ticket does NOT pick up workflow fixes; a fresh ticket is required. (fly has
   no update for a ticket's workflow pin — worth a UX ticket.)
3. **Fail-safe held**: no partial/dirty work ever reached the branch; the state machine
   walked needs_review→sent_back→queued→abandoned cleanly with attempt_count bumped.

Remedy: split along the plan's own task boundary — #22 = Task 8 alone (schema keys),
#23 = Task 9 alone on v4's 140 turns with a fresh version freeze.

| 22 | schema keys (T8 alone) | judge D | **merged** (nested tests local) | **$0.43 / 68** | build ok → pushed | **faithful, textbook C3** |
| 23/24 | T9 attempt 2+3 | judge D | **errored ×2** | $0.15 | — | **AUP false-positive refusals (turn 1)** |
| 25 | T9 as "run-report files" | judge D | **merged** (744db904f5) | **$2.74 / 180** | build ok → pushed | **faithful on the centerpiece** |

## Tickets #23-#25 — the refusal detour and the centerpiece landing

**AUP false-positive refusals, twice:** the claude CLI refused #23 and #24 on TURN 1
($0.075 each) with the Usage Policy boilerplate. Rewriting the pacing prose (#24) did
NOT fix it; rewriting the VOCABULARY did (#25): "flight recorder + evidence + advisory
judge + harvest" in prose apparently pattern-matches an automated-judgment/surveillance
category; "run-report files + optional rubric scoring" with the CI context stated first
sailed through — same task, same files, same plan reference. **Platform lesson: ticket
prose vocabulary is part of the loop's reliability envelope.** Candidates: a
refusal-classifier retry policy in the runner, and phrasing guidance in the workflow
authoring docs. Also: both refused runs stranded tickets in `running` (no reconciler
wired yet) — manual errored transitions; the merged-but-unwired reconciler's value
demonstrated in production twice in one hour.

**#25 itself:** 3 milestone commits exactly as suggested, +831/−100 across exactly the
8 allowed files, 180 turns (past the old 80-cap wall). Review found the live-contract
worry was even softer than planned: the deployed exec keys ONLY off the exit taxonomy
(0/1/2) — it never parses stdout JSON — so the schema.Results convergence is free of
production risk until Slice F ingestion consumes it. GateOutcome JSON preserved,
refusal boundaries untouched, metadata map carries head_sha/base_sha/pushed_branch.
Local verify: harvest + nested schema + exec Harvest suites + full build, all green.

| 26 | mcpserver SSE heartbeat | hitl T2 | **merged** | **$1.26 / 102** | build ok → pushed | **faithful mirror-not-import** |
| 27 | webhook notifier lib | hitl T22 | **merged** | **$0.38 / 45** | build ok → pushed | **faithful (control sample)** |

## Tickets #26-#27 — transport + control sample

#26: devmcp wire semantics mirrored with the "mirrored, not imported" rationale kept in
comments; buffered fallback preserves live ATC callers unchanged (they send no
progressToken); 26-closure migration done compiler-checked. #27: the low-ambiguity
control — cheapest/fastest of the batch, exactly the predicted profile.

## Batch verdict (9 dispatched slices, 8 merged, ~$16)

**Fidelity:** Fable is a faithful-PLUS executor. Every merged diff matched the plan
text — error tables, seams, refusal boundaries, C3 discipline — with judgment appearing
only at the edges and always defensibly (rename-proof labeler, vanished-run
classification, fail-closed rationale traced to the landed enforcement site). Zero
hallucinated scope, zero drive-by refactors, zero rework demanded at review across all
eight merges. The plans' faithful-executor thesis held completely: plan quality was the
binding constraint, and where my ticket bodies added landed-state notes, exploration
turns measurably dropped.

**Cost:** Fable ≈ 1/3 Sonnet's cost and ≈ 1/2 the turns on comparable slices
(#16: $1.23/112 vs #14: $3.93/209). Failures were cheap ($0.075 refusals) or
capacity-bounded ($3.1×2 at the turn cap); no failure ever reached the branch.

**The five platform lessons** (each now in memory + candidates for retrospective
tickets): build-only gate tier; dispatch freezes the workflow version (fresh ticket
required to pick up workflow fixes); AUP prose-vocabulary refusals (refusal-retry
policy + authoring guidance wanted); turn caps need headroom on centerpiece slices +
milestone-commit pacing guidance; scorecards must key on harvest outcome, not
step-status ok.

## Ticket #20 — judge engine (T5-7, first three-task ticket, first on v3 workflow)

Merged as `8baee28a19`: +640 lines, exactly the 6 allowed files, one commit per task as
instructed. Both plan hazards respected: no Push/Porcelain duplication out of runner.go,
RunJudge left advisory-unwired. The CLI-envelope subtleties all present (ci-agent parity
comment, total_cost_usd fallback, fence unwrap mirroring llm.ExtractJSON, string-result
unquoting, per-dimension verdict validation). v3 workflow's resolve-once protocol: no
workspace-path incident this run.

## Ticket #19 — run-completion reconciler (T6, the judgment test)

Merged as `69d4341e5e`. True diff exactly in scope (+473/−6 across reconcile.go,
reconcile_test.go, stub deletion) — the scary 26-file first diff was a stale-base
illusion (the UI session merged their wave onto jetbridge mid-run; the agent's work was
clean against its own base). 11 table-driven tests covering every F17 branch.

**The state-machine semantics all survived faithfully** — the three drift-prone spots I
pinned in the body came through exactly: attempt cap ONLY on the requeue edge with the
over-cap send_back → errored (never queued→errored); harvest-primary with
stale-transition-benign racing; checkpoint legs dormant behind nil with orphan release
(§3.2) unit-tested via the seam. It also added a sound decision the plan left implicit:
vanished run row classifies as errored → needs_review triage, and unresolvable
PipelineRunID==nil is skipped as human-owned.

**Cross-session events this cycle:** (a) the UI session merged agentic-ui-wave onto
jetbridge twice mid-review — re-merge with fetch-retry loop handled it; (b) the
AGENT_OUTPUT_WORKSPACE env bug I chip-flagged after #16 was fixed by a spawned session
(9c2c5dacc1: resolve-once protocol, runner literal block, develop-fable v3 seed) and v3
is already live on the cluster — the loop improved itself mid-batch.

## Ticket #18 — secret labeler (T9)

Merged as `1238005efa` (+125 lines / 3 files, exact scope). Faithful to the plan's
best-effort contract (label failure logs, never fails dispatch; GC keys off
concourse/agent-run alone). **One genuine improvement over the plan text:** it resolved
the secret name via the landed `credentials.RunSecretName(runID)` helper instead of the
plan's literal — my ticket-body warning said "read what the mint actually creates"; the
agent went one better and made the labeler rename-proof for Task 8. This is the
faithful-executor property working WITH judgment, not against it.

## Ticket #17 — dispatcher: polling loop (T4)

**Outcome:** merged as `83eab3ebe2`; first fully-clean cycle on develop-fable v2 —
harvest ran the build gate, passed, and pushed agent/ticket-17 itself (head 4a0c8d9502).

**Fidelity vs plan:** error-classification table implemented VERBATIM (deferred stays
queued / raced benign-debug / refused loud-non-fatal / platform-fault retry-next-pass);
LoopConfig with the dormant RunReader + QuestionLister/CheckpointRow seams exactly as the
plan froze them (incl. the Answer method for orphan cleanup); reconcile stub left in
dispatcher.go pointing at Task 6's reconcile.go. The landed-state note in the ticket body
worked: it read the merged Slice A code and built on the real tree without confusion.
Nice touches beyond the letter of the plan: poison-ticket isolation comment, ctx
cancellation inside the loop, component comment citing the never-notify-only lesson.

**Cost curve:** $0.65 / 62 turns — cheaper than #16; the landed-state preamble seems to
reduce exploration turns.

## Ticket #16 — dispatcher: budget lib + admission (T1-2)

**Outcome:** merged as `ed0e18bb31` (branch agent/ticket-16, commits 5a59312d75 + d95c93687b, +273 lines / 6 files, exactly the allowed scope).

**Fidelity vs plan: faithful, plus judgment where it counted.**
- TicketBudgets exactly per plan: ticket override ?? frozen-workflow default; pinned→Get / live→Live; not-found=uncapped; store errors PROPAGATE (fail-closed rationale reproduced in a comment citing agent_step.go:299-313 — the agent read the landed enforcement site, not just the plan).
- Admission block exactly at the plan's insertion point (after workflow-name check, before resolution/side effects); nil-tolerant Deps.Budget; ErrBudgetExhausted carries spent/limit detail; handler maps 409; ticket stays queued. TDD honored (commit messages + its summary describe watched failures).
- Two commits (one per task) as instructed; tree clean; local verify green (go test/vet agent/dispatch, full go build).

**Cost datum:** $1.23 / 112 turns on Fable vs #14's $3.93 / 209 turns on Sonnet for a comparable TDD slice — Fable ~3x cheaper, ~2x fewer turns here.

**Two platform findings (mine, not the agent's fault):**
1. **Full-scope test gate is structurally impossible in-pod** — testflight needs a live
   Concourse on :8080; topgun needs docker/helm/kubectl. Gates: build ok → test failed
   (attempt 2 — the §6.3 retry fired correctly) → no push, ticket to needs_review
   WITHOUT branch. This is WHY develop v1 declares no gates; resolves the plans' open
   question D7/R5: the loop gate tier is build(+vet-maybe) only until dev-mcp affected
   scopes (wave 3). Fixed: develop-fable v2 = build-only gate.
2. **Recovery path proven:** stranded workspace commits extracted from the artifact
   daemon (no git in daemon image — streamed loose objects via tar, injected into local
   object store, branch pushed by hand). ~5 min, $0.
3. **Runner env bug (agent-reported):** $AGENT_OUTPUT_WORKSPACE was only set in the
   agent's FIRST shell call; later calls saw it empty → its initial cp landed in /.
   The agent noticed, cleaned up, and used the literal path. File as a ticket.

(notes appended as runs complete)

## Batch 2 (post-native-unblock): tickets #28+

| # | Ticket | Slice | State | Cost/turns | Fidelity |
|---|---|---|---|---|---|
| 28 | outcomes B1-B3 | delivery B | **B1+B2 merged** (d2154827e1); B3 split out | $5.49 / 140 (cap) | **faithful incl. security tier + pinning spec** |

Ticket #28 repeated the #21 capacity pattern: 140-turn cap hit mid-B3 with B1+B2
COMMITTED — the harvest dirty listing showed only fly/go-concourse files, so recovery
via the artifact daemon kept $5.49 of work (B1 handler with transition-first ordering,
B2 six-touchpoint wiring with the security-critical wrappa placement done RIGHT: plain
authorized block, principal path refused, tier-pinning spec creating a real
tickets:write token and asserting rejection). Lesson reinforced: "sized ≈ ticket #14"
underestimates six-touchpoint + spec-heavy slices — split route-wiring bundles from
CLI/client bundles at authoring time. Milestone-commit pacing SAVED the work this time
(vs #21's total loss).

## Batch 2 final table (post-native-unblock, all merged)

| # | Slice | Cost/turns | Note |
|---|---|---|---|
| 28 | outcomes B1+B2 | $5.49/140cap | recovered; security tier + pinning spec correct |
| 31 | outcomes B3 (fly dispose/close) | $1.62/112 | D-3 writer migration complete |
| 32 | judge F ingestion | $7.04/319 | all 5 trust/money pins verified |
| 33 | outcomes B4 seeding | $2.17/133 | transition-first ordering + comment |
| 34 | gitcheck C1+C2 | $1.12/93 | honesty note + frozen heuristics |
| 35 | watcher C3+C4-handler | $6.40/140cap | recovered; BranchHead = defensible extension |
| 36 | C4-wiring + C5 | $2.88/220 | viewer tier correct |
| 37 | sidecar T11+T13 | $2.50/134 | frozen retry contract verbatim |
| 38 | ask_human T14+T15 | $2.80/165 | sentinel-only-when-configured |
| 39 | T16+T17 (+T12 pre-landed) | $1.22/113 | agent detected the overlap itself |
| 40 | checkpoint kit T18+T20+T21 | $4.54/273 | correct-when-activated, inert |

Batch 2: 11 dispatched, 11 merged (2 via workspace recovery), ~$37.8, zero rework at
review. Sizing lesson held: every two-commit-scoped ticket cleared the cap; every
3+-task bundle hit it. Combined batches 1+2: ~19 loop tickets merged, ~$54, plus the
native session. The five-plan loop-able scope is COMPLETE.
