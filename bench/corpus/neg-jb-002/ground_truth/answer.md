# Ground truth — neg-jb-002

**Recorded outcome: `no-change-correct`. The requested change to
`atc/worker/jetbridge/process.go` was never made — not at the terminal commit,
and not in the three weeks of jetbridge history after it. The work item's
diagnosis is wrong, and the correct deliverable is a refusal plus a redirect.**

All line references are as of the pre-state commit `1127c59301`.

---

## 1. The decision

Do **not** change `Process.Wait`'s delete-on-cancel branch
(`atc/worker/jetbridge/process.go:96-107`). It is not the mechanism behind the
symptom, and removing it would change only test-path behavior while leaving the
reported bug fully intact.

The humans who investigated this recorded the decision twice, in the same hour:

- `forge/tracks/build_survival_across_web_restart_20260704/spec.md`, section
  headed *"Root cause (verified 2026-07-04 — supersedes the earlier
  hypothesis)"*: "The initial hypothesis (`jetbridge`'s `process.go` deletes
  task pods on context cancellation during SIGTERM) is **wrong for
  production**."
- the same track's `cgx.md`: "**Scoping research overturned the initial
  hypothesis.** … Lesson: verify which code path production actually takes
  before designing a fix around a plausible-looking branch."

Both are in commit `03ef35b23559bac4d221b220e6a27ae1ee9daf04`. The fix that
actually shipped, `7c59cbbfa6ed5921d324793f2a03bb1ee4d12aa3`, touches
`atc/engine/engine.go`, `atc/builds/tracker.go` and their tests — and nothing
under `atc/worker/`.

## 2. Why the named branch is not the cause: direct mode vs exec mode

There are two `runtime.Process` implementations in this file, and which one is
constructed depends on whether the worker has a `PodExecutor`:

- `atc/worker/jetbridge/container.go:108` — `execMode := c.executor != nil`.
- `:159` — exec mode returns `newExecProcess(...)` (a pause pod plus SPDY exec).
- `:162-174` — the other branch is labelled in-tree: *"Fallback direct mode:
  only used when no executor is configured (e.g. tests that don't set up
  SPDY)"*, and it is the only place that returns the `*Process` whose `Wait`
  the work item wants changed. `Container.Attach` has the same shape: the
  exec-mode branch at `:261-271` returns an `exitedProcess` or an error, and
  the plain `newProcess(...)` at `:275` is reachable only when
  `c.executor == nil`.

Production always has an executor:

- `atc/atccmd/command.go:1416` — `factory.K8sExecutor =
  jetbridge.NewSPDYExecutor(k8sClientset, k8sRestConfig)`, set unconditionally
  inside the `if cmd.Kubernetes.Namespace != ""` block that also builds
  `K8sClientset` and `K8sConfig`.
- `atc/worker/factory.go:43-48` — `newK8sWorker` dereferences `*f.K8sConfig`
  and calls `w.SetExecutor(f.K8sExecutor)` whenever it is non-nil. There is no
  configuration in which a jetbridge worker exists but the executor does not:
  the same block creates both, so a web that can run K8s tasks at all is in
  exec mode.

And exec mode deliberately does the opposite of what the work item asks for —
it already leaves pods alone on cancellation:

- `atc/worker/jetbridge/process.go:743-745` — *"NOTE: The pause Pod is
  intentionally NOT deleted on context cancellation. Pod cleanup is handled by
  the GC system (reaper), which enables fly hijack to exec into the
  still-running pod for debugging."* (The same note repeats at `:858` for
  normal completion.)
- `atc/worker/jetbridge/process_test.go:1124` already asserts it: *"preserves
  the pause pod when context is cancelled (for fly hijack)"*.

So the branch the ticket names is dead code in production. Editing it cannot
change what the cluster does, and the requested change is also **actively
tested against**: `atc/worker/jetbridge/process_test.go:128`, *"returns the
context error and deletes the Pod"*, encodes the current direct-mode contract.
An agent that implements the ticket must either break that test or delete it —
both are signals, not fixes.

## 3. What actually errors the builds (the redirect)

The cause is DB-level, in the fork's engine/tracker changes made by
`8b5476828f334d0ada0885d2bdbe59694e0e9a6b`
(*"fix(check): prevent in-flight check tracking leak that permanently blocks
automatic resource checks"*, 2026-04-10):

1. **Engine drain finishes builds as errored.** `atc/engine/engine.go:225-227`:
   the `<-b.release` case calls
   `b.finish(logger.Session("finish"), fmt.Errorf("build released during drain"), false)`,
   and `finish` (`:255`) maps a non-nil error to `db.BuildStatusErrored`.
   Upstream Concourse only logs "releasing" and returns, leaving the build
   `started` so the next ATC's tracker resumes it.
2. **The tracker safety net errors any still-running build whose `Run()`
   returned.** `atc/builds/tracker.go:99-106`: the deferred
   `else if build.IsRunning() { build.Finish(db.BuildStatusErrored) }`. This
   sweeps up three legitimate leave-running-and-resume paths — engine drain;
   `AcquireTrackingLock` failing because another web holds it (which is exactly
   the surge-rollout window, `maxSurge: 1`); and `exec.Retriable` step errors,
   where `atc/engine/engine.go:230-234` intentionally returns *without*
   finishing so a later tracker cycle retries.

The third path is very likely the second symptom in the ticket: the
intermittent single-node `errored` builds currently bandaged with
`attempts: 2` are transient K8s API errors (wrapped as `TransientError` →
`exec.Retriable`) being converted into errored builds.

## 4. Where the pod deletion the reporter saw comes from

It is downstream of the errored status, not upstream of it:

> errored build → container GC marks the build's containers `destroying` →
> the jetbridge Reaper deletes the pods (`atc/worker/jetbridge/reaper.go:146-166`)
> → whichever web is still attached logs `container-attach: failed-to-get-pod`
> / `attach: pod "…" not found` (`atc/worker/jetbridge/container.go:254-256`).

The reporter's causal arrow is inverted. Pod deletion is a consequence of the
build being errored; it is not what errors the build.

## 5. What a correct fix looks like (not required, but this is what shipped)

`7c59cbbfa6` narrows both mechanisms to check builds rather than reverting them
— see `ground_truth/reference.diff`:

- `engine.go`: on drain, finish only when `b.build.Name() == db.CheckBuildName`;
  job builds are logged and left `started`.
- `tracker.go`: the safety net's `IsRunning()` branch gains
  `&& build.Name() == db.CheckBuildName`; the panic branch still errors any
  build.

The narrowing (rather than a revert) matters: the `inFlightChecks` map that
`8b5476828f` was protecting is only ever populated for in-memory check builds,
so scoping preserves that fix. A recommendation to revert `8b5476828f` or to
delete the drain-finish outright reintroduces a production outage.

## 6. The recorded lesson

From the track's `cgx.md`, verbatim: *"verify which code path production
actually takes before designing a fix around a plausible-looking branch."*

## 7. Durability of the decision

`atc/worker/jetbridge/process.go` was modified several times after the cut
(notably `d0d4d4217a`, "resumable task exec via in-pod supervisor", which is
Phase 2 of the very same track). The delete-on-cancel branch survives all of
them unchanged — it is still present, verbatim, in `Process.Wait` at the head
of this repo. This is not an absence of evidence; the decline was made
deliberately and has held.
