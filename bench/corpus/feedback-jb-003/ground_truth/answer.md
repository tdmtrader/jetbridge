# Answer — feedback-jb-003

**WITHHELD.** This is the recorded outcome of the work item, taken from the
terminal artifact `8157023ae91960df4a3ca2208a87c2062d33c7f1`
(`docs(agentic-platform): apply all 12 design-review findings (F1-F12)`,
2026-07-09T07:51:25-07:00, 11 files, +933/-122).

The commit's own message and the `REVIEW.md` §7 section it appended are the
per-finding ground truth. §7 is reproduced verbatim below; the curator's
reading of what it discriminates is in `rubric.md` and
`expected_findings.yaml`.

## Shape of the correct answer, in one paragraph

Twelve findings, one commit, three shapes of response. Nine are corrected in
the plans' own scripts (F1, F3, F4, F5, F6, F7, F8, F9, F10) — eight of them
with the failing test the plan tells its implementer to write first. One
(F11) is corrected in the plan's script but with its *severity framing*
walked back in prose (stale data plane, not live-token exposure). Two (F2,
F12) are answered with documentation ONLY, because the review's own
verification pass refuted the mechanism each one claimed: there is no
pod-start race in `DispatchOne`, and turn count is not the only cap on the
main agent's spend. F1 is the only finding requiring three co-signing owners.
Nothing from §4 (optional polish) is applied, and none of the Go files the
findings cite is created — they do not exist yet.

---


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

---

## Commit message (terminal artifact, verbatim)

```
docs(agentic-platform): apply all 12 design-review findings (F1-F12)

Wave-4 blocker F1: checkpoint now renders as atc.TaskStep running the
deterministic 'platform-mcp checkpoint' client (exit 0 approve / 1
reject) so a rejected checkpoint fails the run — co-signed across
dispatch, platform-mcp-hitl, and contracts §11.

Wave-1 F5: seed prompts rewritten to the mcp read model (no phantom
spec.md/plan.md or {{.Spec}}/{{.Tasks}}) + coherence assertion.

Wave-2 F3/F4: ledger no longer double-charges on resume (additive
UpsertReturningInserted gate); timed-out steps ingest on a detached
context so cost/tokens are still recorded.

Wave-4/5 F6/F8/F9/F10: send-back->merge outcome re-arm; scorecard
live/hash join; process-intel window applied + retrospective delivery
via SubmitSpec. F7: ask_timeout cross-field validation (two layers).
F2/F11/F12: doc/contract corrections (secret timing, bidirectional
sync, budget-honesty annotation).

Cross-file verified (F1/F7/F3 coherence + per-finding completeness),
0 follow-ups. Resolution log in REVIEW.md §7.
```

## Files the terminal artifact touched (all of them)

| File | +/- |
|---|---|
| `docs/superpowers/plans/agentic-platform/00-shared-contracts.md` | 19 |
| `docs/superpowers/plans/agentic-platform/02-credentials-and-budgets.md` | 60 |
| `docs/superpowers/plans/agentic-platform/05-workflow-store.md` | 74 |
| `docs/superpowers/plans/agentic-platform/07-agent-step.md` | 180 |
| `docs/superpowers/plans/agentic-platform/08-platform-mcp-hitl.md` | 49 |
| `docs/superpowers/plans/agentic-platform/11-dispatch.md` | 170 |
| `docs/superpowers/plans/agentic-platform/12-delivery-outcomes.md` | 194 |
| `docs/superpowers/plans/agentic-platform/13-scorecards.md` | 103 |
| `docs/superpowers/plans/agentic-platform/14-process-intel-experiments.md` | 178 |
| `docs/superpowers/plans/agentic-platform/REVIEW.md` | 23 |
| `docs/superpowers/specs/2026-07-07-agentic-platform-end-state-design.md` | 5 |

Everything else in the tree is unchanged — see `expected_findings.yaml#not_findings`
for the six plan files that own §4 polish items and were deliberately left alone.

## Outcome

`merged` — the commit landed directly on `main` and the program proceeded to
its next review round (`2fd306913c`, 2026-07-09T17:37:38-07:00, "final Fable
review (§8) — 32 confirmed findings F13-F40"), which is a *new* review of the
corrected set, not a re-litigation of F1-F12. No follow-up commit revises any
of these twelve fixes.
