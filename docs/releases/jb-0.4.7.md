# JetBridge 0.4.7 (running notes)

JetBridge 0.4.7 follows jb-0.4.6 (ae366b2cf8). These are running notes, kept as changes land; they become the release notes when 0.4.7 is declared.

## Changes

### Step runtime

- **A task's `run.user` is honoured.** `root` or a decimal uid (0 to 2147483647, leading zeros accepted) becomes the main container's `runAsUser`, under both step runtimes and on a Run's exact executions. Before, it was ignored and the task ran as its image's user. A task with `user: root` on an image whose USER is non-root now runs as uid 0. A task that sets no `run.user` runs as its image's user, as before.
- **Breaking: a task that set a numeric non-root `run.user` now runs as that uid.** On hostPath storage (the artifact daemon) its working directory, outputs and caches are root-owned, so it cannot write them, and a task that was green may fail with `permission denied`. Drop `run.user`, or set it to `root`.
- **A user name, or `user:group`, is not resolved.** The task runs as its image's user, and the step's stderr and the web log (`warning-ignored-task-user`) say that `run.user` was ignored.
- **A signed or out-of-range uid fails the step before a pod or Job is made.** The error names the user and the range Kubernetes accepts.
- **A uid is not privilege.** The privilege mode judges the same things as before, whatever the uid: a task that runs as root keeps the unprivileged security policy, and a privileged task stays privileged. `fly intercept` into a step runs as the step's uid.
- **The launcher records an envelope it never received.** A launcher sent for a step envelope that exits without one leaves its termination record marked `envelope_never_received`: the fetch gave up after `--envelope-timeout` or was refused (125), the token or CA file could not be read (125), or the pod's termination signal arrived before the envelope did (while the token was read, during the fetch, or between attempts). That signal used to kill the launcher by default action. It now ends the wait, and the launcher exits 143 (128 plus the signal) with the record and runs nothing. A step's exit code is unchanged.

## Verification

**Not live-verified** (covered by unit and integration specs only):

- `run.user`: `root` and numeric uids reaching the main container under both step runtimes; a name or `user:group` keeping the image's user, with the line on the step's stderr and the web log; a signed or out-of-range uid refused before a pod or Job exists; the privilege policy unchanged by the uid. Nothing here has run on a cluster yet: not a Run's exact execution, not the permission failure of a non-root uid on hostPath storage, and not `fly intercept` as the step's uid.
- The launcher's never-received record: its exits (the envelope timeout, a refused fetch, an unreadable token or CA file, the termination signal before the envelope) are covered by the launcher's unit tests; the live-tier test that closes an undelivered exact job start (`live_job_runtime_test.go`) has not run on a cluster.
