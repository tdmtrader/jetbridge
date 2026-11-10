# JetBridge 0.4.6 (running notes)

JetBridge 0.4.6 follows jb-0.4.5 (e610ff1acb). These are running notes, kept as changes land; they become the release notes when 0.4.6 is declared.

## Changes

### Step runtime

- **The reaper collects step Jobs under either step runtime, and one pod it cannot delete no longer stops a pass.** A deployment switched from `job` back to `exec` keeps the step Jobs left behind, and their pods reach the reaper's destroying path. Deleting their Jobs was forbidden under `exec`, and the reaper returned at the first pod it could not delete. Every pass then stopped there, so no other destroyed container was reaped and no artifacts were dropped. Now a pod that cannot be deleted is logged and left for the next pass. The rest of the pass goes on, and the pass ends in an error. Under `exec`, a Role that cannot list or delete step Jobs (a chart older than the web) is reported once per process at info (`step-jobs-not-collected`), not as an error on every pass. Under `exec` this also stops the `failed-to-list-step-jobs` error 0.4.5 logged every 10 s. Under `job` a refusal stays an error. A refused pause pod delete is an error under either runtime.
- **The reaper deletes a step pod's Job only if it is still the Job the pod's owner reference names** (a UID precondition). A Job recreated under the same name is left alone. The same holds for the step Job an aborted or failed step deletes.
- **The managed-input init waits out a busy or restarting artifact daemon.** It retried a 503 or no answer five times, two seconds apart. Two Runs' inputs queued on one node, or a daemon restart, outlasted that, and the step failed before its command ran. It now retries with capped, jittered backoff for 10 minutes. Any other status still fails at once, with the same message. A node whose artifact daemon is gone for good now fails a managed-input step after about 10 minutes rather than 10 seconds.
- **Admitting an exact execution dials a restarting artifact daemon again.** A refused, reset or closed connection, no route, or no answer within the client's timeout is retried with backoff for 2 minutes under the step's context. Any HTTP answer, and any TLS or certificate failure, is still final. A repeated admission is the same admission: the node's ledger already holds it.
- **The capture control init waits about 3 minutes for the daemon, not 1,** so it outlasts a restart the admission waits through.
- **A transient API failure while checking a step envelope's token is retried.** Under `job`, a TokenReview or pod read that failed for want of an answer was a 401, which the launcher treats as final, so the step's pod exited without running its command. It is now a 503, which the launcher retries. A refused token, or a pod that is gone, is still a 401.
- **The launcher records a command it could not start.** A missing or non-executable command exits 127 or 126 as before, and now also leaves its one-line termination record, saying the command did not run. A failure before the start (bad usage, the token, the envelope fetch: 125) still leaves none.

### Hangar

- **A managed read's timeout starts once it holds the artifact daemon's scratch slot.** Materializations and archive reads share one slot with input publication and seals. A read queued behind another spent its timeout in the queue and answered 503. Now the wait is bounded by the caller's own request, and a caller that gives up while queued holds nothing and spends no warrant.

### Privilege mode

- **A new privilege mode, `--kubernetes-step-privilege`, decides which step pods may run privileged. Its default, `any`, changes nothing.** Under `any` a privileged container is admitted as before, whatever step pod grants declare. Under `granted` the web refuses a privileged container outside a grant that declares `privileged=true` when it creates the Pod or Job, naming the owner and the missing grant. Builds that can hold no grant (instanced pipelines, other teams' one-offs, Runs, builds not found) and privileged checks outside a resource-type grant (below) fail closed; a failed build lookup fails the step. Opt in: run `hack/step-pod-audit pods`, add the grants it shows, then set `granted` (see `deploy/chart/MIGRATING.md`). A pod reused, or a step Job resumed after a web restart, is not re-checked yet. Web logs `step-privilege-mode` at startup.
- **No step pod may add a Linux capability or join a host namespace, in either mode and under every grant.** No step the runtime builds asks for either, so nothing that runs today changes.
- **Under `granted`, a step pod grant can cover a privileged resource type's checks.** Its owner is `<team>/<pipeline>/resource-type/<type>` (a lowercase identifier), and it sets `privileged=true` and no ServiceAccount. Each check pod has exactly one owner: the type its plan runs, which the engine now sets on the container spec. A check of a resource runs that resource's type. The check of a resource type, and the fetch of its image, run that type's own type, so a type built on a privileged type needs the privileged type's grant. A task's `image_resource` check runs the image resource's type. A grant binds the pipeline's name, as a job grant does. Only a privileged check is looked up. The pod keeps the default step pod identity and is annotated `concourse.dev/step-pod-grant`, which `hack/step-pod-audit pods` holds it to. A check of a base type, of an instanced or Run pipeline, or in a one-off build stays refused, and the refusal says why. Under `any` nothing changes.

## Chart

- **`kubernetes.stepPrivilege` (`any | granted`, default `any`) sets the privilege mode.** Only `granted` renders a flag, so an `any` render is byte-identical to 0.4.5's. The value budget rises by one (`maxValues` 177), an exception recorded in the ADR-0008 amendment of 2026-11-09.

- **The pod-manager Role grants `batch/jobs` `list` and `delete` under `exec` too,** so the reaper can collect step Jobs a `job` deployment left behind. `get`, `watch`, `create`, `update` and `patch` stay `job`-only. No new value.
