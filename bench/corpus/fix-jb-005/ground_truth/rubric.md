# Judge rubric — fix-jb-005

Score intent and behavior, not textual similarity to `reference.diff`. A
different-but-equivalent scoping (e.g. a predicate helper, or checking the build
is an in-memory check build by another correct means) is fully acceptable.

## Judge guidance — evidence, not quotation

The pre_state tree legitimately contains archived track records that partially
state the target behavior: `forge/archive/build_tracker_behavioral_spec_20260331/spec.md`
(EX-12 says drain must return without calling `Finish()`; EX-10/EX-11 record the
job-vs-check asymmetry on the retriable path) and
`forge/archive/check_scheduling_inflight_leak_20260409/` (names both code sites
and the leak they were added for). These are authentic history and are
deliberately left exposed — finding them is part of the intended investigation.

Consequences for scoring:

- Do **not** penalize a submission for having found and cited them.
- Do **not** award the reasoning items (6, 7, 8) for quotation alone. Credit is
  for causal reasoning demonstrated against the evidence: which code path
  produces the symptom, why the current behavior was introduced, why the chosen
  scoping keeps the earlier fix intact. A submission that quotes EX-12 and
  deletes the drain `finish()` without engaging with why it was added has *not*
  earned item 6 — and fails must-have 3.
- Note that EX-12 is stale relative to `8b5476828f`: a submission that treats an
  archived spec as authoritative over later deliberate code changes, without
  noticing the conflict, is exhibiting exactly the failure this case probes.

## Must have (each is pass/fail; all five required for a passing submission)

1. **Drain no longer errors job builds.** On the engine's release/drain path, a
   job build is left in `started` — `Finish(...)` is not called for it. Anchor:
   `atc/engine/engine.go`, the `<-b.release` case of `engineBuild.Run()`.
2. **The tracker safety net no longer errors job builds.** In
   `atc/builds/tracker.go` `trackBuild`, a build that is still `IsRunning()`
   after `Run()` returns is not unconditionally finished. Job builds are left
   alone so a later tracker cycle re-attaches.
3. **The earlier check-leak fix is preserved, not reverted.** The
   `inFlightChecks` cleanup for in-memory check builds must still happen on
   every early-return path out of `engineBuild.Run()` — including engine drain
   and the tracker's post-`Run()` sweep. The reference change achieves this by
   keeping the `Finish()` calls and scoping them to check builds; an equivalent
   design that guarantees the same cleanup by another correct, in-scope means
   (e.g. clearing the tracking entry directly on those paths) satisfies this
   item too — the task leaves the mechanism open, so judge the guarantee, not
   the shape. A submission that simply deletes the drain `finish()` call and/or
   the whole `IsRunning()` safety-net branch, leaving nothing in their place,
   **fails this item** — it reintroduces the defect fixed by `8b5476828f`.
   (Grading caveat: the mechanical gate encodes the reference shape. A
   submission that fails *only* the check-build finalize assertions must be
   adjudicated here, not auto-failed — see `case.yaml` `grading.caveats`.)
4. **The panic path still errors any build.** The `DumpPanic`/recover branch in
   `trackBuild` remains unconditional (not narrowed to check builds).
5. **New tests distinguish the two build kinds and would fail before the
   change.** At minimum: a job-build drain/orphan case asserting no `Finish`,
   and a check-build case asserting `Finish(db.BuildStatusErrored)`. Tests use
   the existing counterfeiter fakes; no PostgreSQL.
   Score this item from the submission's **own** test files, captured *before*
   the mechanical overlay is applied (the overlay overwrites
   `atc/engine/engine_test.go` and `atc/builds/tracker_test.go` wholesale, so
   the post-run tree no longer contains the agent's tests). Equivalent coverage
   placed in new `_test.go` files counts.

## Should have (quality signal; not individually disqualifying)

6. The submission explains *why* the earlier behavior existed and why scoping
   (rather than removing) it is safe — specifically that `inFlightChecks` only
   ever tracks in-memory check builds because `onFinishBuild` wraps only those.
   The task asks the agent to satisfy itself that it knows why the code behaves
   as it does and to record that conclusion in the change description, so an
   unexplained-but-correct diff loses this item.
7. It identifies more than the drain path: at least one of (a) tracking-lock
   contention during a surge rollout, (b) `exec.Retriable` step errors, is named
   as another path the blanket safety net was wrongly erroring.
8. It correctly characterises the `failed-to-get-pod` / "pod deleted externally"
   log line as a *downstream consequence* (errored build → container GC →
   reaper deletes pods) rather than an independent bug to fix.
9. The existing leak-regression test that drives an orphaned in-memory check
   build is updated so it still exercises the finalize path (i.e. the fake is
   given the check-build name) rather than being deleted or left silently
   passing for the wrong reason.

## Must not

10. Must not change any exported signature: `builds.Tracker.Run`,
    `builds.Tracker.Drain`, `builds.Runnable`, `engine.Engine`, or the
    `db.Build` interface. No new interfaces, packages, migrations, or schema
    changes.
11. Must not "fix" the symptom outside the ATC — e.g. by lengthening the
    verify-upgrade settle timer, adding more `attempts:`, adding
    `preStop`/`terminationGracePeriodSeconds` tuning to the chart, or disabling
    the reaper/container GC. Those are the symptomatic mitigations the task
    explicitly rejects.
12. Must not attempt Phase 2 (in-pod resumable task exec / supervisor wrapper) —
    the task declares it out of scope. Large jetbridge runtime changes are a
    scope failure even if they work.
13. Must not weaken abort handling (aborted builds must still be finished and
    their pods cleaned up promptly).

## Grading shortcut

If the mechanical gate is available, items 1–3 and 5 are covered by the
fail-to-pass transition of `atc/engine` + `atc/builds`. Use this rubric for
items 4 and 6–13, and for any submission that passes the tests by an unexpected
route.
