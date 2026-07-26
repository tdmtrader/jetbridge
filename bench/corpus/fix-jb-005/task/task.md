# In-flight builds must survive a `concourse-web` rolling restart

**Type:** bugfix
**Reported:** 2026-07-04
**Priority:** high — blocks the release pipeline from upgrading itself reliably

## Symptom

Every rolling restart of `concourse-web` errors all in-flight builds.

The release pipeline hits this on itself. The `self-upgrade` job rolls the web
deployment and exits; any build that is running at that moment ends up with
status **`errored`** within seconds of the old web pod receiving SIGTERM. It is
not a step failure — the step never reports a failure — so a job's `attempts:`
setting does not save it either (attempts retry failed steps, not errored
builds). The only recovery is a manual re-trigger once the rollout has settled.

The same shape shows up outside upgrades: on the single-node cluster, builds
intermittently go straight to `errored` under load rather than retrying, which
is why several release-chain jobs are currently carrying `attempts: 2`.

Frequently, right after one of these errored builds, whichever web was still
attached to the build's task pod starts logging

```
container-attach: failed-to-get-pod
... pod deleted externally
```

i.e. the pod is torn out from under a web that was still using it. That looks
like a downstream consequence of the build being marked errored, not a separate
bug — but confirm rather than assume.

## Why this is wrong

The runtime was built for web restarts to be survivable:

- Task pods are independent Kubernetes objects. They carry no `OwnerReferences`
  back to the web, so they are *not* garbage-collected when a web pod dies.
- Task pods carry exit-status / resource-result annotations precisely so that a
  different web can recover a step's state after a restart.
- Upstream Concourse treats a graceful shutdown as a *release*, not a failure:
  the build stays `started` and another ATC picks it up.

A web upgrade should be invisible to a running build. Today it is fatal to every
one of them.

The mitigations in the tree right now (`attempts: 2` on the flaky release-chain
jobs, and the verify-upgrade settle timer that waits out the rollout window) are
symptomatic. They avoid the window; they do not make builds survive it. Please
fix the underlying behavior.

## Required behavior

1. **The invariant:** a build that is in flight when a web drains must still be
   in flight afterwards. Graceful web shutdown must not be an error condition
   for a build. The build must be left in a state that a later web process can
   pick up and resume.
2. **Rollouts overlap.** During a surge rollout two webs are alive at the same
   time and both are doing work. Whatever you change has to be correct under
   that overlap — a build one web is handling must not be disturbed by the
   other.
3. **Transient failures stay transient.** Builds also error out on momentary
   infrastructure blips (a Kubernetes API call failing for a second, say) where
   the intended behavior is to try again. Those should stop being terminal too.
4. A genuine abort keeps today's behavior exactly: pod cleanup happens promptly
   and the build ends up aborted.
5. If the web genuinely crashes while running a build, that build must still end
   up errored. Do not trade this bug for builds that hang around forever.

## Constraints

- **No regressions.** Not all of the current behavior here is accidental; some
  of it is load-bearing. Satisfy yourself that you know why the code behaves the
  way it does today before you change it, and say in your change description
  what you concluded. Making this bug go away by undoing someone else's fix is
  not an acceptable outcome.
- Keep it surgical: no new interfaces, no new packages, no schema/migration
  work.
- It must be unit-testable with the existing counterfeiter fakes — no database
  required for the tests you add. Add tests that fail before your change.

## Out of scope

Making a task command that was mid-execution resume *inside* its pod without
re-running from the start is a separate follow-up. This work item covers only
making the build itself survive the restart so a later web can pick it up.
