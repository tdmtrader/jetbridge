# Evidence bundle — in-flight builds die on web rollout

Operator field notes, assembled 2026-07-04. This is everything we have. The
cluster is a single node (`theborg`); the web runs as a Deployment with a surge
rollout, so during an upgrade two web pods are briefly alive at once.

---

## 1. Observed behaviour

Reproduces on every rollout we have watched, in both of these forms:

- **`self-upgrade` job.** The release pipeline's `self-upgrade` job rolls the
  web Deployment and exits successfully. `verify-upgrade` used to trigger the
  instant `self-upgrade` passed; its task pod started on the web that was in the
  middle of restarting, and its build was **errored** as the old web pod
  terminated. It passed on a manual re-trigger once the ATC was stable.
- **Manual `kubectl rollout restart deployment/concourse-web`.** Any build in
  flight at that moment goes to `errored` within a few seconds.

Details that seem to matter:

- The final build status is `errored`, not `failed` and not `aborted`.
- The build's own event stream shows **no step failure and no error text** —
  from `fly watch` the build simply stops and is marked errored. Whatever
  errors it is not writing a message into the build log.
- Nobody aborted anything. No `fly abort-build`, no UI abort.
- `attempts:` on the job does not help. Attempts retry *failed* steps; an
  errored build is not retried.
- We have **not** been able to capture the terminating web's own logs. By the
  time anyone looks, that pod is gone. Everything below is from the web that
  survived the rollout.

## 2. Log lines from the surviving web

Repeatedly, immediately after one of these builds is errored, the *other* web —
the one that was still attached to that build's task pod — logs:

```
... "container-attach.failed-to-get-pod" ... pods "<task-pod>" not found
```

and the step surfaces

```
attach: pod "<task-pod>" not found
```

(quoted as they were pasted into the incident thread; only the trailing action
name was recorded from the log line, not the full session prefix)

Read plainly this says: the task pod was deleted while a web was still using it.
That is what put us onto the "the runtime deletes pods on shutdown" theory. Note
the ordering we actually observed, though — **the build is already marked
errored when these lines appear**, not before. We do not know whether that
ordering is causal or coincidental.

## 3. Task pods are not owned by the web

Checked directly with `kubectl get pod <task-pod> -o yaml`: task pods have **no
`ownerReferences`**. Kubernetes is therefore not cascade-deleting them when a
web pod goes away. Something is deleting them on purpose.

## 4. Related symptom: intermittent `errored` builds under load

Independent of upgrades, build/test jobs on this single node intermittently
error mid-run. This has been read as node contention losing the step pod. The
current mitigation is in the pipeline (see `deploy/concourse-pipeline.yml`):

```yaml
  - task: build-and-vet
    # Retry transient k8s-runtime pod disruptions (single-node contention);
    # this task errors (not fails) when its pod is lost mid-run.
    attempts: 2
```

added by commit `c2a7d3bfcbf5992e9cee012aa959bd7b621c6066`:

> ci(release): add attempts:2 retry to flaky single-node build/test jobs
>
> build-and-vet, unit-tests, k8s-runtime-tests and k8s-live-tests run their
> work as k8s-runtime pods on the single shared node (theborg). Under
> contention the step pod is occasionally lost mid-run, which Concourse
> reports as "errored" (not "failed") and leaves the job red — this has
> blocked every RC/release since June 10.

Whether this is the same mechanism as the rollout case or a genuinely different
one (real node pressure) is unresolved. It is suspicious that both produce
`errored` rather than `failed`.

## 5. The other mitigation already in the tree

`deploy/concourse-pipeline.yml` also carries an upgrade settle timer, added by
commit `1127c59301e2f865b4d2420e909ae5344e05661f` (the current tip):

```yaml
# Settle timer: gates verify-upgrade so its build starts on a timer tick rather
# than the instant self-upgrade finishes. self-upgrade restarts concourse-web
# (the ATC); a job triggered immediately runs a task pod on that restarting web
# and its build is errored when the old web pod terminates. Ticking every 2m
# means verify-upgrade starts after the rollout has settled, and if a tick ever
# races the restart, the next tick re-fires it (self-healing).
- name: upgrade-settle-timer
  type: time
  source:
    interval: 2m
```

Both mitigations are symptomatic: they avoid the window in which builds die.
Neither makes a build survive it. That is what this RCA is for.

## 6. Deployment shape (for reference)

- Web: Deployment, rolling update with surge — a new web pod is Ready before the
  old one finishes terminating, so two ATCs run concurrently for a short period.
- Workers: none in the traditional sense; steps run as Kubernetes pods created
  by the web process itself (the fork's `jetbridge` runtime).
- Shutdown is graceful: the web catches SIGTERM and runs its normal shutdown
  sequence within the pod's termination grace period rather than being killed
  outright.
