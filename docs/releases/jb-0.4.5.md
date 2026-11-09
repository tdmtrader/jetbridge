# JetBridge 0.4.5

JetBridge 0.4.5 follows jb-0.4.4 (cf122f8a09). It gates the landing queue on tests and an agent review, adds an opt-in step runtime that never execs into a pod, moves Run executions off full or gone nodes, and hardens the web, the chart and the artifact daemon. **Upgrading needs a values change and a drained output plane** (one artifact daemon key; see Operator notes).

## Changes

### Landing queue

- **Gates.** A queue may declare `gates:`, each a template with a `wait`. Every gate Run gets the compose Run's composition as `composition`, and the queue lands a candidate only when every gate passed. A failed gate ejects the entry. An errored, aborted, vanished or overdue Run is no verdict: the gate is retried once, then the queue pauses until `fly resume-landing-queue`. `fly landing-queue` shows each in-flight entry's gates. Migration 1789793170.
- **Gate templates.** `deploy/landing-gate-tests-template.yml` runs the pipeline's build-and-vet and unit-tests on the candidate. `deploy/landing-gate-review-template.yml` has the review worker review base..candidate and fails only on a finding of severity high or blocker. A review that cannot run or does not complete is no verdict.
- **`jb-review-worker`** gains `--auth-file` and `verdict --block-at <severity>`, which exits 0 (passed), 1 (a blocking finding) or 3 (no verdict).
- **Every commit lands.** The queue replays an entry's commits onto the trunk one by one, each with its author, date, message and an `original:` trailer. It never squashes, and refuses an entry holding a merge commit.
- **Run history purge.** `fly destroy-pipeline --purge-run-history` (and `purge_run_history` on `DELETE pipeline`) destroys a template with its Runs. It is refused while a landing queue depends on the template. A destroyed template's task ids stay reserved, so a reinstall needs new ones.

### Runs

- **A `run_pipeline` step may wait** for its child Run, following it to the end, and bind the child's inputs from an earlier waited call. A landing parent template composes compose and land in one build.
- **No Run is created without the Hangar output plane in service.**
- **A Run execution is admitted on a node it fits and can bind to.** One whose pod stays Unschedulable and never binds for two minutes moves to another node. Migration 1789793168. A step pod that stays Unschedulable moves when its grace runs out.
- **A sealed producer finishes before any other step work,** and a result's consumer must follow its producer.
- **A materialized Run input keeps its executable bits.** Inputs stay read-only (files 0444, executables 0555, directories 0555).

### Step runtime

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


### Security

- **One artifact daemon key** (`secrets.artifactDaemonKeySecret`) signs every resolve capability and Hangar warrant. It replaces the resolve-capability Secret and the Hangar warrant key. Resolve is always signed; the unsigned mode is gone.
- **Credentials are redacted** from a build's error events, from sidecar output, and from a task's command line in its initialize-task and start-task events (the "running ..." line).
- **Webhook tokens** are compared in constant time and never logged.
- **A secret path that climbs out of its team's prefix** is refused.
- **Signing keys** carry a nonce (migration 1789793169) and are read whether encrypted or plaintext. Encrypting them at rest ships switched off, so a 0.4.4 web can still read them if you roll back.
- **The artifact daemon never follows a symlink** when walking a source tree.
- **Pinned images.** The built-in resource types and the artifact helper image are pinned by digest.
- **Toolchain.** Go 1.27.3, and every module govulncheck found reachable is updated.

### Web

- **Dashboard sections.** A pipeline names its dashboard section with `display.section`. A team's pipelines split into collapsible sections, and a collapsed section still shows what is failing.

## Chart (0.2.4 → 0.2.5)

- **`secrets.artifactDaemonKeySecret`** (required, exactly 32 raw bytes under `artifact-daemon.key`) replaces `artifactDaemon.resolveCapability` and `artifactDaemon.hangar.keySecret`. The closed schema rejects the old keys.
- **The web ServiceAccount is narrower.** No `apps/deployments` or `apps/daemonsets`, and `secrets: get` only when the Kubernetes credential manager is on.
- **The database password and local users** reach the web from `<fullname>-web-credentials`, not pod args.
- **The artifact helper image** is pinned (`alpine:3.24.4@sha256:93b41e…`).
- **Task egress** admits NodeLocal DNSCache's default address.
- **The artifact daemon has no PodDisruptionBudget.**
- `kubernetes.stepRuntime` (`exec` | `job`, default `exec`), passed to the web
  only as `job`: under `exec` the chart renders nothing for the job runtime (no
  flag, no `batch/jobs` or `tokenreviews` grant), so a chart ahead of its image
  still starts. The launcher is copied from the web's own image, which the web reads
  from its own pod (`CONCOURSE_KUBERNETES_WEB_POD_NAME` from the downward API), so
  it is always the web's own build whatever tag the pipeline's self-upgrade set.
  Under `job` the pod-manager Role
  gains `batch/jobs` and the web ClusterRole gains `tokenreviews create`; the web
  gets `--kubernetes-launcher-image` (its own image) and
  `--kubernetes-step-envelope-url` (its in-cluster Service).


## Operator notes

- **Migrations 1789793168, 1789793169 and 1789793170.** Upgrade from 0.4.4, or from 0.4.3, 0.4.2 or 0.3.1 through it.
- **The key cutover needs a drained output plane.** Warrants signed under the old keys stop verifying at the switch. Follow MIGRATING's "One artifact daemon key": create the Secret, set `hangarOutput.webEnabled: false` and wait for no residue, switch values, then re-enable. Anything else that names the old Secret, such as RBAC `resourceNames`, needs the new one. While drained, no Run is admitted, so the landing queue lands nothing.
- **Before upgrading:**
  - Mirror the pinned helper and resource-type images if nodes cannot reach Docker Hub.
  - Give any workload that borrows the web ServiceAccount its own.
  - Check that the Kubernetes credential manager is detectable if you use it.
  - See MIGRATING's "Security hardening".
- **Update `fly`** (`fly sync`): an older `fly` cannot set a queue with gates or purge Run history.
- **Gates run the installed template, not the candidate's.** After changing a gate template, set it with `set-pipeline` before landing the change. The review gate needs the team's `codex-auth` secret, which goes stale as Codex rotates its refresh token. Refresh it when reviews stop authenticating.
- `job` needs Kubernetes 1.29 or later (native sidecars); the
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
- **Known issue (exec runtime):** the reaper also lists step Jobs, which the chart grants only under `job`. With the default `exec` the web logs `failed-to-list-step-jobs` on every reaper pass (every 10 s). It is harmless: reaping is otherwise unaffected.
