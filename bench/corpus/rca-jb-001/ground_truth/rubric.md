# Judge rubric — rca-jb-001

Score the submitted **analysis**, not a diff. The deliverable is prose; no code
change was requested. If the submission also contains a patch, the patch may be
used as corroborating evidence of what the author concluded, but a correct patch
does **not** substitute for a missing diagnosis (an agent can arrive at the
right predicate by test-fitting without understanding why).

Grade against `ground_truth/answer.md`. Line references there are as of the
pre-state commit `1127c59301`; accept equivalent references (function names,
quoted code) rather than requiring exact line numbers.

`task.md` asks for the analysis as `RCA.md` at the repository root; that is
where a harness should look for it. Grade whatever prose the run produced —
delivering the same analysis through another channel or filename is not a
scoring defect on its own.

**Evidence, not quotation.** The pre-state tree legitimately contains the
archived track for the causing commit
(`forge/archive/check_scheduling_inflight_leak_20260409/{spec,plan,cgx}.md`,
which describes both mechanisms and the `inFlightChecks` leak they fixed) and
`forge/archive/build_tracker_behavioral_spec_20260331/spec.md` EX-12, which
states the pre-regression contract ("drain releases without finishing"). Reading
project history is the intended, virtuous path and must not be penalized — but
credit on R1, R2 and R3 requires *causal reasoning tied to the pre-state code*
(the cited call sites, and why they produce `errored` for the reported symptom),
not a restatement of what a design doc says. A submission that quotes the
archived track and stops — no code citation, no link from the mechanism to the
observed rollout symptom — earns at most half on each of R1-R3, and does not
pass the gate on the strength of the quotation alone.

---

## Gate — the primary root cause (pass/fail)

**G1. The submission must locate the cause at the build-status level, in the
ATC, not in pod lifecycle management.** Concretely: something calls
`build.Finish(BuildStatusErrored)` on in-flight builds when a web shuts down.

A submission that fails G1 scores **0 overall regardless of other items**. The
two failing shapes to watch for:

- concludes the Kubernetes runtime deleting task pods is the root cause
  (`Process.Wait`'s delete-on-cancel branch, `atc/worker/jetbridge/process.go`);
- concludes the cause is node contention / resource pressure / OOM / eviction
  on the single node.

Both are the traps this case exists to detect. Hedged wording ("the pod deletion
may also contribute") does not rescue a submission whose *primary* named cause
is either of these; conversely, correctly identifying pod deletion as a
downstream *effect* is item R5 and is worth credit.

---

## Required items (10 points each unless noted)

**R1 — Mechanism A: engine drain.** Identifies that on drain the engine's
`<-b.release` case calls `b.finish(..., fmt.Errorf("build released during
drain"), false)`, which maps to `StatusErrored`
(`atc/engine/engine.go` ~224-227, `finish` at ~255-272). *(10)*

**R2 — Mechanism B: the tracker safety net.** Identifies the deferred
`else if build.IsRunning() { build.Finish(BuildStatusErrored) }` in
`Tracker.trackBuild` (`atc/builds/tracker.go` ~99-106) as a second, independent
path to the same outcome. *(10)*

> Naming only one of R1/R2 caps the submission at "partial": the rollout case is
> genuinely reachable through both, and a fix that addresses only one leaves the
> surge-rollout window broken. Award R1 and R2 independently.

**R3 — Provenance and intent.** Attributes both mechanisms to the same earlier
fix and explains *why* it exists: the `inFlightChecks` map in
`atc/db/check_factory.go` leaks when a check build exits `Run()` without
`Finish()`, permanently blocking automatic checks for that resource. Naming the
SHA `8b5476828f` is ideal; identifying the commit by subject/behavior
("the in-flight check-tracking leak fix") earns full credit. Merely saying "this
was added at some point" does not. *(10)*

**R4 — Refutes the standing hypothesis, for the right reason.** States that the
delete-on-cancel path is direct mode only and that production runs exec mode,
citing at least one of: the fallback comment in
`atc/worker/jetbridge/container.go` (~159-174), the wiring
`factory.K8sExecutor = jetbridge.NewSPDYExecutor(...)` in
`atc/atccmd/command.go` (~1416), or `execProcess.Wait`'s explicit "pause Pod is
intentionally NOT deleted on context cancellation" note. *(15)*

> Refuting the hypothesis with a wrong or hand-wavy reason ("pods have no
> OwnerReferences so they can't be deleted", "the code looks unreachable")
> earns at most half. The discriminating evidence is the direct-vs-exec wiring.

**R5 — Causal ordering of the pod errors.** Places the observed
`container-attach.failed-to-get-pod` / `attach: pod … not found` lines
*downstream* of the errored status, via container GC marking containers
destroying and the jetbridge Reaper deleting the pods. Full credit requires
naming the intermediate step (GC / destroying containers / reaper), not just
asserting "it's a symptom". *(10)*

**R6 — Blast radius.** Identifies that the safety net catches early returns
other than drain. Award 5 points per path, max 15:
 - tracking lock not acquired (`build-already-tracked`) — and, for full credit
   on this path, connects it to the surge rollout: two webs alive at once, the
   new web errors a build the old web is still running;
 - engine drain;
 - `exec.Retriable` / transient K8s errors, where the engine intentionally
   returns without finishing so the tracker can retry. *(15)*

**R7 — Connects the second symptom.** Concludes that the intermittent
single-node `errored` builds bandaged with `attempts: 2` are (at least partly)
the `Retriable` path being converted into errored builds rather than node
pressure. This is the highest-value insight in the real RCA and the one most
likely to separate strong from adequate submissions. *(10)*

**R8 — Recommends narrowing, not reverting.** States that the check-leak fix
must be preserved and that the correct fix scopes the two mechanisms rather than
removing them. Full credit additionally names the discriminator: the
`inFlightChecks` map only ever tracks in-memory check builds (`onFinishBuild` is
applied only to them), so the predicate is "is this a check build"
(`Name() == db.CheckBuildName`). *(15)*

> A recommendation to revert `8b5476828f`, or to delete the drain-finish and the
> safety net outright, scores 0 on R8 and should be called out in the judge's
> summary: it reintroduces a production outage.

**R9 — Epistemic discipline.** *(5)* Claims about code and history are cited
(`file:line`, SHAs) and verified-vs-inferred is distinguishable; the submission
does not invent cluster observations, log lines, or metrics it could not have
seen. Fabricated evidence caps the submission at "partial" no matter how correct
the conclusion.

Total: 100.

## Bonus (up to +5, cannot exceed 100)

- Notices that surviving the restart still leaves "resume" as "re-run": the new
  web's `Attach()` finds no exit-status annotation and falls through to `Run()`,
  which re-execs the command in the still-running pause pod, risking a duplicate
  concurrent process (answer.md §7).
- Notices that the panic branch of the safety net must keep erroring all builds.
- Notices that the drain path must still finalize in-memory check builds
  (they cannot resume across a restart), i.e. states the *positive* half of the
  scoping rule and not just the negative half.

## Bands

| Band | Meaning |
|---|---|
| **Excellent (85-100)** | Gate passed; R1-R5 and R8 all full; connects the retry symptom (R7). Equivalent to what the humans shipped. |
| **Good (65-84)** | Gate passed; both mechanisms named; hypothesis refuted with the right evidence; recommends narrowing. May miss R6/R7 detail. |
| **Partial (40-64)** | Gate passed but only one mechanism found, or refutes the hypothesis for the wrong reason, or recommends a revert. |
| **Fail (0-39)** | Gate failed (pod deletion or node pressure named as root cause), or fabricated evidence. |

## Notes for the judge

- Do not reward diff-similarity to `reference.diff`; it is included only so the
  judge can see what the accepted diagnosis implied in code.
- Do not penalize a submission for *additionally* proposing the fix, for
  proposing a different-but-equivalent predicate (e.g. "only finalize builds the
  check factory is tracking"), or for flagging Phase-2 concerns as follow-ups.
- Do penalize confident single-cause framing when the submission has only found
  Mechanism A: the surge-rollout window is a distinct failure the submission
  would not have covered.
