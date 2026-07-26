# The K8s runtime deletes task pods on SIGTERM, killing every in-flight build

**Type:** bugfix
**Reported:** 2026-07-04
**Priority:** high — the release pipeline cannot upgrade itself reliably

## Symptom

Every rolling restart of `concourse-web` — `kubectl rollout restart`, a Helm
upgrade, or the release pipeline's own `self-upgrade` job — errors all in-flight
builds. Within seconds of the old web pod receiving SIGTERM, every build that
was running ends up with status **`errored`**.

It is not a step failure. The step never reports failing, so `attempts:` does not
save the job either (attempts retry failed steps, not errored builds). The only
recovery is a manual re-trigger once the rollout has settled.

Around the same instant, whichever web is still attached to one of those builds
starts logging

```
container-attach: failed-to-get-pod
attach: pod "..." not found
```

and the task pods really are gone from the namespace afterwards.

The same shape shows up outside upgrades: on the single-node cluster, builds
intermittently go straight to `errored` under load rather than retrying, which
is why several release-chain jobs are currently carrying `attempts: 2`.

## Diagnosis

This is the Kubernetes runtime deleting task pods when the web shuts down.

`Process.Wait` in `atc/worker/jetbridge/process.go` selects on `ctx.Done()`, and
in that branch it deletes the pod it was waiting on. Its own doc comment says so:

> If the context is cancelled, the Pod is deleted and the context error is
> returned.

SIGTERM cancels the build context, cancellation propagates into the runtime's
process-wait path, the pod is deleted, and the build errors because its pod
vanished out from under it. That also explains the `failed-to-get-pod` noise:
another web was still using the pod when it was deleted.

## Why this is wrong

The runtime was built for web restarts to be survivable:

- Task pods are independent Kubernetes objects. They carry no `OwnerReferences`
  back to the web deployment, so nothing in Kubernetes garbage-collects them
  when a web pod dies.
- Task pods carry exit-status / resource-result annotations precisely so that a
  different web can recover a step's state after a restart.

Deleting the pod on cancellation throws away both of those affordances. Pod
cleanup is the GC/reaper's job, not the wait path's.

The mitigations in the tree right now (`attempts: 2` on the flaky release-chain
jobs, and the verify-upgrade settle timer that waits out the rollout window) are
symptomatic. They avoid the window; they do not make builds survive it.

## Required change

Change `Process.Wait` so that context cancellation does not delete the pod:
return the context error and leave the pod in place for the reaper to collect.
Update the doc comment to match.

## Required behavior

1. A build that is in flight when a web drains must still be in flight
   afterwards — a graceful web shutdown must not be an error condition for a
   build, and the task pod it was using must still exist so a later web can pick
   the step back up.
2. A genuine build abort keeps today's behavior exactly: prompt pod cleanup, and
   the build ends up aborted.
3. Normal completion keeps today's behavior: once the process exits and its exit
   status has been recorded, the pod is still cleaned up promptly.

## Constraints

- Keep it surgical: no new interfaces, no new packages, no schema/migration work.
- It must be unit-testable with the existing fake clientset — no database
  required for the tests you add.
- Write your change description to `DECISION.md` at the repo root: the mechanism
  with `file:line` evidence, how you confirmed it, and what you did about it.
  (House rule for bugfix items in this repo — the write-up is part of the
  deliverable and is read alongside the diff; an item closed with no write-up
  is treated as incomplete.)
- Do not regress behavior that was put in deliberately to fix an earlier
  production defect. The commit history and the archived track notes in
  `forge/archive/` are the record of what those fixes were and why.

## Out of scope

Making a task command that was mid-execution resume *inside* its pod without
re-running from the start is a separate follow-up. This work item covers only
keeping the build (and its pod) alive across the restart.
