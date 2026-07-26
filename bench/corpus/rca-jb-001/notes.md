# rca-jb-001 — curation record

## ⚠ Shares a terminal artifact with fix-jb-005 — read this first

`bench/corpus/fix-jb-005` is backed out of **the same terminal commit and the
same pre_state ref** as this case. That is deliberate and (I argue) legitimate —
one incident yielded both a fix and a written diagnosis, and the two cases grade
different capabilities under different rubrics — but it has consequences:

- **Results are not independent samples.** Never aggregate rca-jb-001 and
  fix-jb-005 into one score as if they were two data points about the same
  system. A roll-up in `corpus/INDEX.md` needs a "shares terminal with" column.
- **Never run them in the same session or the same agent context.** fix-jb-005's
  `task.md` states the architectural intent that this case's rubric asks the
  agent to *derive* ("upstream's engine releases in-flight builds on shutdown;
  the build stays started and the next ATC re-attaches"), and its `ground_truth/`
  contains this answer.
- **The exposure manifests differ.** They share the repository ref; the
  work-items are different files, written to different cuts (see
  §"Task derivation" below).

If the corpus later needs strict independence, this case is the one to keep
(diagnosis shape is otherwise unrepresented in v0) and fix-jb-005 is the one to
re-source.

## Provenance walk

Backed out of an incident on the `jetbridge` line of this repo. Every SHA below
was resolved and read; nothing was taken on the candidate's word.

| Role | SHA | Committer date | Subject |
|---|---|---|---|
| pre_state (parent of the fix) | `1127c59301e2f865b4d2420e909ae5344e05661f` | 2026-07-04T21:58:44-07:00 | `fix(ci): gate verify-upgrade behind a settle timer to survive ATC restart` |
| terminal artifact (fix) | `7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3` | 2026-07-04T22:35:47-07:00 | `fix(engine): release in-flight job builds on drain instead of erroring them` |
| terminal companion (the RCA prose = answer key) | `03ef35b23559bac4d221b220e6a27ae1ee9daf04` | 2026-07-04T22:36:36-07:00 | `chore(forge): create build_survival_across_web_restart track; Phase 1 complete` |
| causing commit (reachable at pre_state) | `8b5476828f334d0ada0885d2bdbe59694e0e9a6b` | 2026-04-10T05:27:18-07:00 | `fix(check): prevent in-flight check tracking leak…` |
| symptomatic bandaid #1 (in pre_state tree) | `c2a7d3bfcbf5992e9cee012aa959bd7b621c6066` | 2026-07-04T19:49:47-07:00 | `ci(release): add attempts:2 retry to flaky single-node build/test jobs` |

Verification performed:

- `git rev-parse 7c59cbbfa6^` → `1127c59301…`. Parent relationship confirmed.
- The fix commit's message states the candidate's claim verbatim: both
  mechanisms, their origin in `8b5476828f`, the three swept-up resume paths, the
  scoping to `Name() == db.CheckBuildName`, and the downstream pod-deletion
  cascade. `git show --stat` → exactly 4 files (2 source, 2 spec), no doc or
  version companions inside the fix commit itself.
- The RCA prose was read in full at `03ef35b235`
  (`forge/tracks/build_survival_across_web_restart_20260704/spec.md` §Root cause,
  and `cgx.md` §Origin). It contains the explicit
  *"supersedes the earlier hypothesis"* section that makes the decoy gradable.
- **Every causal link was re-verified against the pre_state source, not the
  prose:**
  - `1127c59301:atc/engine/engine.go:224-227` — unconditional
    `b.finish(…, "build released during drain", false)` on `<-b.release`; and
    `finish()` at :255-272 maps non-nil err → `StatusErrored`, writing the error
    text to the lager log only (`logger.Info("errored", …)`), never to the build
    event stream. This is what makes the evidence bundle's "no error text in the
    build log" observation true.
  - `1127c59301:atc/builds/tracker.go:93-107` — unscoped
    `else if build.IsRunning() { build.Finish(BuildStatusErrored) }`.
  - `1127c59301:atc/engine/engine.go:120-125` — the `if !acquired` early return
    (surge-rollout path), and `:230-234` — the `exec.Retriable` early return.
  - `1127c59301:atc/db/check_factory.go:163` — `onFinishBuild` is constructed in
    exactly one place, for in-memory check builds. This is the fact that makes
    "scope to check builds" correct rather than arbitrary.
  - Decoy: `1127c59301:atc/worker/jetbridge/process.go:96-107` — the
    delete-on-cancel branch is real, and its doc comment at :58-61 literally
    says *"If the context is cancelled, the Pod is deleted"*.
  - Decoy refutation: `container.go:159-174` (`newExecProcess` when an executor
    is set; *"Fallback direct mode: only used when no executor is configured"*),
    `atc/atccmd/command.go:1416` (`factory.K8sExecutor = jetbridge.NewSPDYExecutor(...)`,
    line number confirmed by grep at pre_state), and `process.go:743-745`
    (*"The pause Pod is intentionally NOT deleted on context cancellation"*).
  - Downstream: `atc/worker/jetbridge/reaper.go` — `FindDestroyingContainers` →
    `Pods().Delete`; and `container.go:226` (`Session("container-attach")`) +
    `:254` (`logger.Error("failed-to-get-pod", err)`) are the real emitters of
    the log line quoted in the evidence bundle.
- Durability: on `jetbridge` today, `engine.go` still carries
  `if b.build.Name() == db.CheckBuildName` in the release case and `tracker.go`
  still carries `build.IsRunning() && build.Name() == db.CheckBuildName`. The
  diagnosis was never overturned, so the ground truth is not a decision that was
  later reversed.
- Self-hosted-corpus caveat: `git ls-tree 1127c59301 -- bench/` is empty; the
  pre_state predates `bench/` by three weeks.

**Conclusion: the case holds under verification.** The candidate's claims were
accurate in every particular I checked.

## Task derivation

The real trigger was operational and pod-centric. Per `cgx.md` §Origin, the
investigation was prompted by the user's instinct — *"a web upgrade should not
kill task pods; the new web should take over running jobs"* — and **the first
analysis blamed `jetbridge/process.go`'s delete-pod-on-ctx-cancel branch** before
being overturned. `task/task.md` reproduces that framing: the symptom, the
architectural expectation *about pods* (no `OwnerReferences`, recovery
annotations), and the pod-deletion theory presented as a standing hypothesis to
confirm or refute. That is faithful to how the incident actually read at T, and
it is what sets the trap the rubric scores.

`task/evidence.md` is **reconstructed operator field notes, not a captured log
file** — this must not be misrepresented. The 2026-07-04 web logs were never
captured (the terminating pod is gone by the time anyone looks; fix-jb-005's
notes reached the same conclusion independently). Every element of the bundle is
traceable to something that provably existed at T:

| Evidence item | Provenance |
|---|---|
| errored-not-failed, no error text in build log, attempts don't help | `1127c59301`'s own commit body |
| `attempts: 2` YAML + comment | `deploy/concourse-pipeline.yml` at pre_state (added by `c2a7d3bfcb`, message quoted verbatim) |
| settle-timer YAML + comment | `deploy/concourse-pipeline.yml` at pre_state (added by `1127c59301`) |
| `container-attach…failed-to-get-pod` | real emitter: `container.go:226` (session) + `:254` (action) at pre_state |
| `attach: pod "<name>" not found` | real emitter: `container.go:256` at pre_state |
| task pods carry no `ownerReferences` | stated in the post-cut spec's Overview; verifiable in `createPod` |
| surge rollout / two webs alive | deployment shape, stated in the post-cut spec |

One correction was made to the humans' own record while assembling this. The
post-cut spec quotes the symptom as `container-attach: failed-to-get-pod` /
*"pod deleted externally"*. Grepping the pre_state tree for the second string
puts it in `(*Process).pollUntilDone` (`process.go:178`) — **direct mode**, the
same mode the decoy lives in, so it cannot be emitted by production. Either the
operator was recalling an older incident or the phrase was borrowed from the
code while writing up. It is dropped from the exposed evidence (an unreachable
log line is a fabricated observation, and this one would have been a free hint
toward the direct-vs-exec distinction), and `answer.md` §5 carries a note so a
judge does not require it. Lesson: verify that quoted log strings are reachable
from the production code path before sealing them into an evidence bundle.

Synthesising a "realistic" log dump instead would have been easy **and would
have leaked the answer**: the drain path writes
`logger.Info("errored", {"error": "build released during drain"})`, so any
plausible web-log capture contains the mechanism in plain text. The absence of
the draining web's logs is therefore load-bearing, not a gap — it is recorded in
`case.yaml` under `grading.environment.notes` so a later curator does not
"improve" the case by adding them.

## Leakage analysis

### Withheld from the task (present in the post-cut record)

- **The answer's file/function names.** `atc/engine/engine.go`,
  `atc/builds/tracker.go`, `<-b.release`, `Tracker.trackBuild`,
  `build.Finish(BuildStatusErrored)` — none appear in `task/` .
- **The word "drain"** in the mechanism sense. `evidence.md` §6 says only that
  the web "catches SIGTERM and runs its normal shutdown sequence"; an earlier
  draft said "drains before exiting" and was rewritten, because `Drain` is a
  grep-able symbol that points straight at `<-b.release`.
- **The correct-behavior essay.** fix-jb-005's task states that upstream
  releases builds on drain and the next ATC re-attaches. For a *diagnosis*
  deliverable that is the answer, so it is absent here. This is the single
  largest difference between the two tasks' cuts.
- **The requirements list.** fix-jb-005 enumerates lock contention, retryable
  errors, abort and panic as behaviors to preserve. Here those are rubric items
  R6 and the bonus — the agent must discover the blast radius, not be handed it.
- **The predicate.** `db.CheckBuildName`, `inFlightChecks`, `onFinishBuild`,
  `checkFactory`, and the check-build/job-build distinction are never mentioned
  in `task/`.
- **The causing commit.** `8b5476828f` is not named. Finding it is rubric R3.
- **The causal ordering.** `evidence.md` reports the *observed* ordering (the
  build is already errored when the pod errors appear) and explicitly says
  "we do not know whether that ordering is causal or coincidental". The
  conclusion (errored → GC → reaper → attach failure) is withheld and scored as
  R5. Reporting an observed ordering is evidence; asserting its meaning would
  have been the answer.

### Present at pre_state and deliberately LEFT EXPOSED

- **`forge/archive/check_scheduling_inflight_leak_20260409/{spec,plan,cgx}.md`.**
  This is the archived track for `8b5476828f` and it is the biggest
  difficulty-reducer in the case: it names both mechanisms explicitly
  ("Fix 1 — Tracker safety net… if the build is still running, call
  `build.Finish(BuildStatusErrored)`"; "Fix 2 — Engine release path… call
  `b.finish()`"), cites `engine.go ~line 224`, and explains the `inFlightChecks`
  leak. An agent that greps `forge/archive` for shutdown/drain lands on rubric
  items R1, R2 and R3 (30 of 100 points) almost immediately.
  **Left exposed on purpose:** it is genuine pre-state evidence and reading the
  history of the code you are diagnosing is the intended, virtuous path — this
  is a case about causal reasoning, not about hide-and-seek. It does **not**
  contain: the regression it would cause for job builds, the decoy refutation
  (R4, 15 pts), the causal ordering (R5, 10), the blast radius incl. the surge
  window (R6, 15), the `attempts: 2` connection (R7, 10), or the scoping
  predicate (R8, 15). 70 of 100 points remain unassisted.
  A leakage auditor should still weigh this; if it is judged too generous, the
  remedy is to add it to `withheld`, not to rewrite the task.
- **`forge/archive/build_tracker_behavioral_spec_20260331/spec.md` EX-12**
  (added by the leakage audit, confirmed present at pre_state): *"Drain releases
  without finishing — when the engine's release channel is closed (drain), the
  engine MUST return immediately from `Run()` without calling `build.Finish()`."*
  That is the **pre-regression contract**, so an agent that reads it can infer
  the current unconditional finish is a documented-behavior regression. Same
  ruling as the archived track: authentic pre-state history, kept, priced.
- The `attempts: 2` and settle-timer comments in
  `deploy/concourse-pipeline.yml`. Symptom, not mechanism; also quoted directly
  in the evidence bundle.
- `8b5476828f` itself, in git history. Reachable by `git log -S`, `git blame`,
  or the archive track. This is the discovery path R3 grades.

### Materialization hazard (applies to fix-jb-005 too)

The terminal artifact `7c59cbbfa6` is a **direct child** of the pre_state ref and
its commit message is a verbatim answer key — it names both mechanisms, the
causing SHA, the predicate and the downstream cascade. `git branch -a --contains
7c59cbbfa6` lists `jetbridge`, `main` and ~8 working branches, so a naive
"clone the repo and check out the SHA" exposes the entire answer to a one-line
`git log --all --oneline | head`.

But `git archive` (review-jb-001's remedy) is **not** usable here: reading
history is part of the intended solution path. `case.yaml` therefore specifies a
ref-stripping materialization (detach at the pre_state SHA, delete all refs,
expire the reflog, `gc --prune=now`) with a two-command verification:
`git cat-file -e 7c59cbbfa6^{commit}` must fail and
`git cat-file -e 8b5476828f^{commit}` must succeed. **That recipe is written
from first principles and has not been executed** — this worktree's git access
is read-only for this curation pass. Validating it is the first item in
§Validation below.

fix-jb-005 has the same hazard and does not mention it; its `pre_state` block
carries no `materialize` key. Worth a follow-up on that case.

### Memorization

`memorization_risk: none`. Both the fork-local defect (`8b5476828f`, 2026-04)
and its narrowing (2026-07) are private post-cutoff history of this repo. The
surrounding engine/tracker code is upstream and public, but the behavior being
diagnosed exists *only* in the fork — upstream logs `releasing` and returns.
A model reciting upstream Concourse from weights would, if anything, be nudged
toward the correct expectation and away from the pre_state code.

## Difficulty / quality

`difficulty: hard`. Mechanical proxies understate it (the implied change is 19
source lines in 2 files), but the diagnostic chain is five hops
(SIGTERM → drain/safety-net → DB status → container GC → reaper → attach error)
and runs *backwards* through the evidence: the most visible artifact
(`failed-to-get-pod`) is the last link, and the most visually plausible culprit
(a pod-deleting code path whose own doc comment advertises the behavior) is
production-unreachable for a reason that requires reading three files of wiring.
The agent must also reconstruct a three-month-old fix's rationale to avoid
recommending a revert that re-opens a check-scheduling outage.

`quality: 5`. Multi-hop causal chain; a decoy that is documented as having
actually fooled the humans; ground truth written in prose *after* T with an
explicit "supersedes the earlier hypothesis" section; a rubric that can score
both the named cause and the rejection of the decoy; no cluster, no database, no
network.

## Open questions

1. **Judge variance on prose.** This case has no mechanical anchor at all. The
   gate item (G1) should keep the floor honest, but the 100-point breakdown is
   untested — two judges may differ by a band on R6/R7. Consider running the
   rubric against three known submissions (the real spec = 100; a
   pod-deletion answer = 0; a "revert 8b5476828f" answer ≈ 45) to calibrate,
   and record the results here.
2. **Is the archived leak track too much help?** See §Leakage. My judgment is
   no; a second auditor should weigh it. If it is withheld, difficulty rises
   substantially and R3 becomes near-unreachable without `git log -S`.
3. **Should the standing hypothesis be an ablation?** A variant with the
   "standing hypothesis" section removed would measure how much of the failure
   rate is anchoring versus plain difficulty. Cheap to produce (delete one
   section from `task.md`) and it would make this the corpus's first paired
   ablation — but it must be a separate case id, not an edit to this one.
4. **Port types don't exist yet.** `evidence-bundle/v1` and `analysis/v1` are
   proposed, not implemented, and there is no runnable log-diagnosis workflow on
   the platform. Until both land this case is hand-run and measures a model
   rather than a product.
5. **Second symptom is unproven even in ground truth.** The real RCA says the
   `Retriable` path is "the likely true cause" of the intermittent errored
   builds — it was never confirmed by a controlled experiment. R7 therefore
   rewards matching the humans' inference, not a verified fact. Flagged so a
   judge does not treat a well-argued dissent on R7 as wrong.

## Validation

_Stub — to be filled by the validation stage._

- [ ] Execute the `pre_state.repository.materialize` recipe end to end. Confirm
      `git cat-file -e 7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3^{commit}` FAILS
      and `git cat-file -e 8b5476828f334d0ada0885d2bdbe59694e0e9a6b^{commit}`
      SUCCEEDS in the materialized tree. Also confirm
      `git log --all --oneline | head` shows nothing after 2026-07-04T21:58:44.
- [ ] Confirm `forge/tracks/build_survival_across_web_restart_20260704/` is
      absent from the materialized tree.
- [ ] Grep the materialized tree for the answer strings: `CheckBuildName` in
      `*.md`, `released during drain`, `build_survival` — expect only the
      already-analysed hits recorded above.
- [ ] Calibrate the rubric against the three reference submissions in
      Open question 1; record scores.
- [ ] Confirm the run's `RCA.md` delivery path works end to end (task.md now
      names it; the pre_state tree has no root-level `RCA.md`, verified with
      `git ls-tree 1127c59301`, so there is no collision).
- [ ] Record `validation.status` in `case.yaml`.

## Fixup 2026-07-25

Curator-fixup pass over the two leakage audits. Every audit item resolved into
one of four buckets; residual verdict **pass**.

### Dissolved by the exposure contract (no action, nothing renamed or retitled)

The solver sees only `pre_state` (minus `withheld`) plus `task/`. `case.yaml`,
`notes.md`, `ground_truth/` and the case id/path are harness-side. So:

- **sonnet, blocking item 1** — `pre_state.repository.materialize`'s comment
  names `8b5476828f` as the answer. Dissolved: that comment is in `case.yaml`.
  It also has to name the SHA — it is the verification command for the
  materialization recipe (`git cat-file -e 8b5476828f^{commit}` must succeed).
- **sonnet, blocking item 2** — `curation.learnings` states the upstream-release
  narrative and the exact refuting evidence ("direct-mode vs exec-mode wiring").
  Dissolved: same file, same reason. It is also the corpus-level lesson about
  per-case scrubbing, which is what `curation.learnings` is for.
- **opus, item 4** — the `title` answers deliverable 3 ("…the pod-deletion code
  everyone blames is unreachable in production"). Dissolved: titles may state
  the answer freely per the contract. Kept verbatim; a descriptive title is what
  makes the corpus index readable.
- Not raised, but noted for completeness: `notes.md` §Leakage and
  `ground_truth/rubric.md` also state the answer. Same dissolution.

Standing caveat, already in the schema and now doubly true for this case:
**anyone hand-running it must materialize `task/` into a neutrally-named
directory and must not let the solver see this directory.**

### Known leak channel (declared, not fixable here)

- **opus item 3 / sonnet item 3** — this machine's project auto-memory states
  the conclusion verbatim ("builds error on web restart because commit
  8b5476828f errors builds on drain + tracker safety net (DB-level, NOT
  jetbridge pod deletion)"), i.e. the gate, R1, R2, R3 and the R4 refutation in
  one sentence. `known_leak_channels: [project-auto-memory]` added to
  `case.yaml`; a hand-run of this case on this machine is invalid unless project
  memory, session context and conversation history are suppressed. Memory was
  not modified.

### Priced deflators — KEPT, guard added (real fix)

- **opus item 2** — `forge/archive/check_scheduling_inflight_leak_20260409/`
  and `forge/archive/build_tracker_behavioral_spec_20260331/` EX-12. Both
  re-read at pre_state this pass; opus's pricing is accurate — the archived
  track's §"Technical Approach" hands over Fix 1 (tracker safety net calling
  `build.Finish(BuildStatusErrored)`) and Fix 2 (engine `<-b.release` calling
  `b.finish()`) plus the `inFlightChecks` intent, so R1-R3 and a short path to
  the gate are available to a grep for `drain` in `forge/`.
  **Ruling: keep.** Authenticity wins — this is genuine pre-state history and
  reading the history of the code you are diagnosing is the intended solution
  path, not a cheat. Instead, `ground_truth/rubric.md` gained an
  "Evidence, not quotation" clause: R1-R3 credit requires causal reasoning tied
  to the pre-state code and to the observed rollout symptom, a doc restatement
  alone earns at most half on each, and quotation alone does not pass the gate.
  Both docs were grepped for R4-R8 content — `CheckBuildName`, direct/exec mode,
  executor wiring, reaper, `Retriable`, surge, `ownerReferences` — with **zero
  hits in all six files**, which is expected (they predate the narrowing).
  Neither doc collapses the whole task, so neither goes in `withheld`.
  Second deflator added to §Leakage above, which had only recorded the first.

### Real defect fixed

- **Missing delivery channel.** The deliverable is prose and nothing said where
  it goes — the same defect class the decline cases have. `task/task.md` now
  reads "A written root-cause analysis, filed as `RCA.md` at the root of the
  repository you are given"; `ground_truth/rubric.md` records that path as where
  a harness should look and explicitly says another channel/filename is not a
  scoring defect on its own (content is what is graded). Checked that no
  root-level `RCA.md` exists at pre_state. Nothing else in `task/` was touched:
  both auditors independently found the trigger honestly scrubbed, and the
  standing-hypothesis anchor is the deliberate, load-bearing trap.
- No grading collisions to fix: `rubric: judge`, `fail_to_pass`/`pass_to_pass`
  are empty, and `mechanical_corroboration` is explicitly out of the score.
  Manifest dates are internally consistent (`information_cut` =
  `1127c59301`'s committer date = the "Opened: 2026-07-04" in `task.md` and the
  "assembled 2026-07-04" in `evidence.md`).

### Difficulty

Reconfirmed **hard**, not recalibrated. Opus's "collapses deliverables 1-2" is
an argument that the floor is lower, not that the case is easy: the deflators
supply 30 of 100 points, while R4 (15, decoy refutation via direct-vs-exec
wiring), R5 (10, GC→reaper ordering), R6 (15, blast radius incl. the surge
window), R7 (10, the `Retriable`/`attempts: 2` connection) and R8 (15, the
`CheckBuildName` predicate rather than a revert) — 65 points, and everything
that separates the Good and Excellent bands — appear in neither doc. Rationale
recorded inline in `case.yaml` above the `difficulty` key.

### Files changed this pass

- `bench/corpus/rca-jb-001/task/task.md` — deliverable now names `RCA.md`.
- `bench/corpus/rca-jb-001/ground_truth/rubric.md` — delivery-path note +
  "Evidence, not quotation" deflator guard.
- `bench/corpus/rca-jb-001/case.yaml` — header banner replaced (BORDERLINE →
  resolved/pass with the dissolution reason), `known_leak_channels` added,
  difficulty rationale comment added, `curator-fixup` audit entry appended.
- `bench/corpus/rca-jb-001/notes.md` — second deflator recorded in §Leakage,
  this section, one validation checklist item.

No git state was touched (read-only `git ls-tree`/`git show` only), and no file
outside `bench/corpus/rca-jb-001/` was modified.
