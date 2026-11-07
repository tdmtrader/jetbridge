# JetBridge 0.4.5 (running notes)

JetBridge 0.4.5 follows jb-0.4.4 (cf122f8a09).

## Changes

- **A `job` step runtime, selectable per deployment.** `kubernetes.stepRuntime: job`
  runs each step as one suspended `batch/v1` Job whose pod carries the command in
  its own spec, started by unsuspending it, under the `jb-launcher` binary the
  jetbridge image now ships. The exit code is the main container's terminated
  status, logs come from `pods/log`, and a check, get or put's answer is written
  to the step's answer directory on the node and read back through the artifact
  daemon. Its request JSON reaches the pod as a step envelope the pod fetches with
  a pod-bound projected ServiceAccount token (`/runtime/v1/step-envelope`), never
  through a Kubernetes object. Nothing is exec'd into a job-runtime pod. Declared
  sidecars become native sidecars (Kubernetes 1.29 or later), so a step pod
  completes when its command does. The default stays `exec`; switch back by
  setting the value, with no other change.
- **Not yet on the job runtime:** a Run-owned exact execution (landing queue,
  review and implement workloads) still runs on a pause pod through exec, so
  `pods/exec` remains in the Role in both modes. `fly intercept` into a
  job-runtime pod still execs. Both go in a later release.

## Chart (0.2.4 → 0.2.5)

- `kubernetes.stepRuntime` (`exec` | `job`), passed to the web only as `job`: an
  exec render is byte-identical to 0.4.4's, so a chart ahead of its image still
  starts. The launcher is copied from the web's own image, which the web reads
  from its own pod (`CONCOURSE_KUBERNETES_WEB_POD_NAME` from the downward API), so
  it is always the web's own build whatever tag the pipeline's self-upgrade set.
  Under `job` the pod-manager Role
  gains `batch/jobs` and the web ClusterRole gains `tokenreviews create`; the web
  gets `--kubernetes-launcher-image` (its own image) and
  `--kubernetes-step-envelope-url` (its in-cluster Service).

## Operator notes

- No migrations. `job` needs Kubernetes 1.29 or later (native sidecars); the
  chart refuses to render it on an older cluster.
- Under `job`, step pods reach the web's Service on its HTTP port for their
  envelope. With `networkPolicy.enabled` or `taskEgressTo` set the chart opens
  that path itself (web ingress from every pod carrying the worker label, task
  egress to the web).
- Behaviour changes under `job`: an aborted or timed-out check, get or put's
  pod is deleted (the exec runtime kept it for `fly intercept`); an aborted
  task gets a 30 s SIGTERM grace instead of an immediate delete; a step pod
  deleted from outside fails the step and its Job is deleted rather than the
  command being run again.
- A step started under one runtime and resumed by a web running the other is
  not resumed: switch runtimes between builds.
