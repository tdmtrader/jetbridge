# V5 migration — decision log

This is a decision log, not a transcript. The migration ran as ~150 dated
checkpoints between 2026-09-09 and 2026-09-18, each with its own mutation
evidence and `/tmp` receipt paths; that raw history is not reproduced here.
For the current per-test disposition of every deleted or trimmed Go suite,
see [DISPOSITION-jetbridge.md](DISPOSITION-jetbridge.md). For build/run
commands, see [README.md](README.md).

## Where it stands (2026-09-20)

**CI has now run the full gate on the integrated tree and it is green.**
Pipeline job `brine`, build 875390 on `26defb2983`: 543 local + 134 live
scenarios, 677/677 passed, 2278/2961 = 76.9% Brine-only production-statement
coverage (target 50%), 34m52s for the whole job. Every owned namespace is
confirmed absent by the run's own sweep (a survivor counts in `run_end.failed`).
The night's performance work is recorded in the decision log below
("2026-09-20"): the local tier went from ~27 min to ~2m17s (the 48 Hangar
scenarios could never reach the shared emulator from inside the network
namespace), the live tier from ~45 min to ~26 min (the disposer no longer waits
for the namespace controller; expensive fixtures start on first use). The
per-run profile printed by `scripts/coverage` (`cmd/brine-profile-run`) is the
authority on where time goes now: kubelet-timed eviction and pause-pod steps,
and the one watch-expiry scenario that must wait out the apiserver's history
window (~89s).

Everything below this heading describes the tree as it stood on 2026-09-17/18
and is kept as history.

### Where it stood (2026-09-17/18)

The full local+live Brine gate last passed **589/589** (455 local, 134 live),
**2,035/2,471 = 82.36%** Brine-only production-statement coverage of
`atc/worker/jetbridge`, in 46m50s summed CLI time, with all 589 recorder
drains complete and all 133 owned namespaces independently confirmed absent.
**Those counts are historical** — they describe the tree before commit
`388692eb54`'s revert (see below), before the Hangar output family landed,
and before the 2026-09-18 fix round re-applied the fixes. On the integrated
tree (`core-brine-v5-fixes`, 2026-09-18), the actual `Scenario:`/`Scenario
Outline:` line and `Examples` row counts under `features/` are:

| Directory | `Scenario:` + `Scenario Outline:` lines | `Examples` tables | `Examples` data rows | Executable scenarios |
|---|---|---|---|---|
| `features/` (excluding `live/`) | 439 (391 + 48) | 50 | 152 | 543 |
| `features/live/` | 85 (64 + 21) | 21 | 70 | 134 |

The last column is what a run resolves to (one per `Scenario:`, one per
`Examples` data row) and is the `budgets.scenarios_min` floor each `.brine`
manifest declares.

Neither `features/pending/` nor `features/live/pending/` exists any more:
the Hangar output family landed into `features/`, and the eight parked live
scenarios were folded back into their `features/live/` files (several as
rows merged into an existing outline, which is why the live count is not the
old 78 + 9). See `features/README.md` for how the pending mechanism worked,
described generically. That is not a mergeable sign-off:

- CI has never executed this migration's `brine` job. The v11 runner image
  (adds gcc/busybox/otelcol) was published 2026-09-19 (manifest digest
  `sha256:1e1fa432a1d3…`, see `deploy/test-runner.md`) and every job in
  `deploy/concourse-pipeline.yml` pins it; the job is wired, not yet proven.
- The historical OOM-priority timeout (see "Known gaps" below) is mitigated,
  not explained.
- Two native daemon tests fail on the installed Go 1.25.12 toolchain
  (`TestPeerFetch_RestrictiveModesNormalized`,
  `TestStreamIn_RestrictiveModesNormalized`) and pass on 1.26.1 in a focused
  comparison; no toolchain change was applied, and direction on it is pending.
- On 2026-09-18, commit `388692eb54` ("checkpoint: revert out-of-scope
  production changes in atc/worker/jetbridge to origin/core") reverted every
  production fix this migration had made and approved, leaving
  `atc/worker/jetbridge/*.go` (excluding `brine/`) byte-identical to
  `origin/core`. Later the same day the fix round (`brine-v5-fix/prod-fixes`,
  merged into `core-brine-v5-fixes`) re-applied all seven as focused changes
  over current `core`, reconciled with core's own `confirmPodSurvivedExec`.
  **The seven fixes are present in this tree.** Checkpoints below dated
  before 2026-09-18 that describe a fix as landed are accurate again for the
  production source, but not for the intervening audit lineage; the table in
  "Known gaps" is the authoritative per-defect record.

This tree descends from `core-brine-rebased-20260918` via the integration
branch `core-brine-v5-fixes`; several checkpoints below (and the former
`features/live/pending/` files) cite a sibling review branch,
`core-brine-audit-20260918`, as where a fix would apply. Treat that as "the
audit lineage", not a claim about this exact ref. The seven fixes were
re-applied onto this tree on 2026-09-18 (see the last two checkpoints and
"Known gaps").

## The fixture model

Every local scenario shares one `WorkerReady` fixture (`steps/worker.go`)
built from two real resources, not stand-ins:

- **`real-cluster`**: `envtest`'s real `kube-apiserver` + `etcd`
  (`steps/envtest.go`). This is a real Kubernetes API server that admits,
  validates and stores objects exactly like a live cluster — but it ships
  **no kubelet, no scheduler, no controller-manager**. Pods get real specs,
  real names and real UIDs; nothing ever runs inside them, and nothing
  transitions their phase. A local scenario can prove "the pod submitted to
  the API has shape X" and cannot prove "the pod ran".
- **`jetbridge-db`**: real PostgreSQL, not an in-memory or mocked store.

Where the corpus needs kubelet/exec/scheduler fidelity — a container
actually running, a real SPDY exec stream, a real OOM kill, a real image
pull — those scenarios moved to the **live tier** (below) against a real
cluster, because `envtest` structurally cannot provide it (confirmed
repeatedly rather than assumed: see the 2026-09-11 "Goal blocked" checkpoint,
which found no Docker/Podman/containerd/K3s/rootless runtime available and
insufficient privilege for a local K3s testcontainers harness).

The artifact daemon is a **real subprocess**, not an HTTP handler faked in
Go: `steps/realdaemon.go` builds and runs the actual
`cmd/artifact-daemon` binary, and daemon-facing scenarios talk to it over a
real socket.

## The no-doubles rule

At the start of this migration (see "Starting double inventory", historical),
Brine's own step packages still contained: client-go fake clientsets
(`fixture.go`, `domain.go`), injected fake-client reactors for status/watch/API
failures (`process.go`, `closing.go`, `container_extra.go`, `podwatch.go`),
a host-command "local executor" standing in for Kubernetes remote exec, and
several HTTP fixture implementations sitting next to already-real daemon
processes. The rule adopted from that point on: real Kubernetes API (`envtest`
or a live cluster), real Postgres, real daemon subprocesses, real SPDY exec —
no recording doubles, no fabricated status, no host-process substitutes for
pod execution.

Two guards enforce it mechanically rather than by convention:

- **`steps/kubernetes_clients_test.go`** — a recursive AST import guard that
  fails if any file in the whole nested module (walked from `..`, skipping
  `vendor/` and hidden directories) imports a fake Kubernetes client or
  reactor package (including via an alias or dot import), plus a declaration
  guard (`TestKubernetesClientDeclarationGuard`) that flags any non-test
  type/func named like a fake clientset. The walk has a floor on the number of
  files it must see, so an empty walk cannot pass vacuously (an earlier
  `steps/`-only version once scanned zero files and passed).
- **`steps/vocabulary_test.go`** — a family of vocabulary/wiring guards:
  every step definition must be reachable from some scenario
  (`TestEveryStepDefinitionIsUsedByAScenario`), every scenario phrase must
  resolve to exactly one definition
  (`TestEveryScenarioStepResolvesToADefinition`,
  `TestNoStepLineMatchesTwoDefinitions`), and — specific to the Hangar
  requirement family — every `hangar-*.feature` scenario must cite a
  requirement tag (`TestEveryHangarScenarioCitesARequirement`). These guards
  walk every directory under `features/`, so a `features/pending/` directory,
  when one exists (see "The pending mechanism" below), gets everything but
  execution.

A third guard, `steps/temproot_test.go`'s
`TestEveryFixtureTempDirIsUnderTheAdapterRoot`, fails on any
`os.MkdirTemp("", …)`/`os.CreateTemp("", …)` in a non-test step file: every
fixture temp path must come from `AttributedTempDir`/`AttributedTempFile`,
which place it under the pid-carrying adapter root so a leak is attributable
and the stale-root sweep can reclaim it.

Since the 2026-09-18 fix round these guards are wired rather than
convention: `make test-brine-guards` (part of `make test-quick`) runs the
guard subset by name pattern, and both `scripts/coverage` and
`scripts/run-private-network` run the same subset before touching a daemon.

One exception is explicitly labelled rather than silently tolerated:
`steps/scan_images.go`'s `panicImageResolver`, used only by the
resolver-panic-recovery scenario, which needs to inject an actual panic (not
an HTTP/registry error) to prove recovery. It is recorded as an approved
exception, not evidence that other substitutes are permitted.

As of the last full inventory (`REMAINING-DOUBLES.md`, 2026-09-17), the
no-doubles goal is **not complete**: a handful of reported-lifecycle fixture
families and the documented resolver-panic exception remain open. See that
file (about to be folded into this one — see below) for the live list.

## Contract 5: the document-native adapter

`.brine` pins `runner: {contract: 5}`. Contracts 3 and 4 were Brine's
"flags era" — the CLI told the adapter what to run via `--tags`/`--only`
flags. `brine-dispatch` stopped emitting those flags on 2026-09-09; contract
5 means the adapter reads Brine's **execution document** directly (selection,
AST, directives and resource seeds all come from the shared document
library) instead of parsing CLI flags itself. `cmd/brine-adapter-jetbridge`
implements this; the old CLI feature/tag/line parser was removed from it.
The Go step-authoring model underneath is unaffected and stays at
authoring-version 3 — contract 5 is a wire-protocol pin, not a rewrite of how
steps are written.

`cmd/brine-verify-run` and `cmd/brine-census` are unrelated, pre-existing
tools (verify-run checks a JSONL run's recorder/namespace receipts;
brine-census is a historical fixed-scope tool for naming deleted files, not
a certifier of current consolidation state).

## The local tier: a private network namespace

Real daemon peers need real, reachable addresses to prove mirroring and
fallback — but the suite cannot bind to the host's network without every
concurrent test run colliding. The solution (prototyped 2026-09-10, then
promoted to `scripts/netns-exec.c` / `scripts/build-private-network` /
`scripts/run-private-network`) is a small C launcher that creates its own
Linux user+network namespace, configures two private IPv4 addresses on a
private loopback, and execs the adapter inside it. Two problems this solved
directly:

- PostgreSQL refuses uid 0. The launcher maps the calling root (inside the
  fresh user namespace) to uid 65534 (`nobody`), the identity Brine's
  Postgres runner already expects — no host identity or filesystem
  permission changes.
- `envtest`'s API server needs an explicit `--advertise-address` inside a
  namespace with no default route (automatic address selection fails with
  Kubernetes's own "no default routes" diagnostic); the launcher's first
  private address is used explicitly for this.

Neither private address is ever added to the host routing table, and both
disappear when the process tree exits. `.brine`'s `runner.binary:
scripts/run-private-network` is this wrapper, not the adapter itself.

## The live tier

`live/.brine` runs the same contract-5 adapter (`runner.binary:
../scripts/run-live-cluster`) against a real, explicitly authorized cluster
via `BRINE_KUBE_CONTEXT` — there is no default context and no mock fallback.
It exists because `envtest` cannot run anything: no kubelet, no scheduler, no
real exec, no real OOM/eviction. The live tier was authorized in stages as
specific fidelity gaps were found (2026-09-11 "Approved watch recovery and
bounded live-runtime validation" was the first grant, scoped to a temporary
namespace on the shared cluster; later grants added specific approvals for
hostPath/hostPort artifact-daemon fixtures and opt-in peer discovery).

Each live scenario creates its own namespace with baseline pod-security,
a resource quota and container limits, and deletes that namespace (with a
UID precondition, waiting for actual removal) on cleanup. No privileged
containers, no node changes, no cluster-wide RBAC. The one documented
exception is the artifact-handoff node-port/hostPath fixture, gated behind
explicit `BRINE_ALLOW_HOSTPATH_TESTS`/`BRINE_ALLOW_HOSTPORT_TESTS`/node/port
environment variables that are not themselves a grant of Kubernetes
permission — the fixture verifies ownership and port availability before
using them. Peer/mirroring discovery is opt-in (`--peer-discovery` without
`--node-name`) specifically so it does not also claim the label-ownership
side effects `--node-name` has in production; only the producer daemon gets
a scoped, namespace-local `list` Role on EndpointSlices.

**The live tier has never run in CI.** Source (a pinned runner build, a
stripped `artifact-daemon` build, four CI pipeline variables for the
approvals above) exists and passes shell/task-extraction checks, but no
image has been published and no pipeline deployed.

## The pending mechanism

Two distinct uses of "pending", both excluded from `.brine`'s `features:
"features/*.feature"` glob (and the live tier's equivalent) by directory:

**`features/pending/`** (local tier) exists for the Hangar output family,
where step definitions are written and registered ahead of the production
code that would make them pass (production lands across later phases of a
separate track). Without this directory, a family of step definitions with
no runnable scenario yet would be dead code
(`TestEveryStepDefinitionIsUsedByAScenario` would fail), while writing a
scenario that runs and stubs green would be exactly the "test that cannot
fail" defect this whole migration exists to eliminate. `features/pending/`
scenarios get every wiring/vocabulary guard except execution — see the table
in [features/README.md](features/README.md). `./pendingcheck.sh` proves the
static walk still type-checks (every step's input state matches what the
previous step produced) even though nothing runs; it temporarily repoints
`.brine`'s glob at `features/pending/`, runs `brine check`, and restores the
manifest on every exit path. This directory does not exist at present —
nothing is mid-flight in the local tier right now.

**`features/live/pending/`** was a second, distinct use introduced for this
audit branch: after commit `388692eb54` reverted every production fix, each
scenario that used to pass against the fixed source and then failed against
`origin/core` was moved there rather than deleted or silently left red in the
running corpus. Each file stated plainly: "NOT RUN YET. This scenario
documents a real defect in core and is red against it," named the exact
production change that would fix it, and (where applicable) the commit that
reverted it. The fixes have since been re-applied and every scenario moved
back up into `features/live/`; the directory is gone, and
`TestPendingHangarFeaturesAreNotRun` now asserts it stays gone and that no
running live feature carries the marker. See "Known gaps" for the record.

## Go test files deleted in this branch

Two different eras are easy to conflate; only the second is this branch's
work:

**Already on `core`, not part of this diff** — an earlier consolidation
(documented in `MIGRATION-EVIDENCE.md` and `COMPLETION-AUDIT.md`, dated
2026-08-29/09-08) deleted about nineteen whole Go test files
(`artifact_locator_test.go`, `config_test.go`, `behavioral_worker_test.go`,
`artifact_integration_test.go`, `behavioral_permutations_test.go`,
`integration_test.go`, `podname_integration_test.go`, `podname_test.go`,
`registrar_test.go`, `reaper_test.go`, `container_test.go`,
`secret_env_test.go`, `resource_test.go`,
`storage_daemonset_durable_test.go`, `watch_test.go`, `process_test.go`,
`volume_test.go`, `volume_daemonset_test.go`, `worker_test.go`) in favor of
features, with per-test both-red mutation evidence recorded in
`DISPOSITION-jetbridge.md`. None of that is this branch's diff — `origin/core`
already has these files gone.

**This branch's whole-file deletions** (`git diff --diff-filter=D
core...HEAD`, current tree):

| File | Replaced by |
|---|---|
| `daemon_client_test.go` | artifact-daemon.feature probes + daemon-mirroring.feature |
| `resource_cache_stub_test.go` | real cached-get association scenario |
| `brine/steps/local_executor_test.go` | the step module's own real-executor scenarios |

The audit lineage had also deleted `behavioral_runtime_spec_restored_test.go`,
`container_restored_test.go`, `integration_restored_test.go`,
`process_restored_test.go` and `volume_daemonset_restored_test.go` outright;
the 2026-09-18 fix round restored all five (`a71eae60c5`, "restore the ones
it does not"), so on this tree they are partial retirements: each keeps the
tests brine does not cover, and `DISPOSITION-jetbridge.md` names the scenario
or restored test for every one that left.

**This branch's partial retirements** (file kept, some tests removed from
inside it): `node_ip_resolver_test.go` (four `TestNodeIPResolver_*` tests
removed; one renamed pure-policy test, `TestNodeInternalAddressSelection`,
remains), `behavioral_volume_test.go` (four retry/raw-body tests removed),
`storage_daemonset_test.go`, `daemonset_integration_test.go`,
`executor_test.go`, plus small trims to `container_resources_test.go`,
`daemon_tls_test.go`, `resolve_capability_test.go` and several `live_*_test.go`
files. Full per-test mapping: `DISPOSITION-jetbridge.md`.

Note `volume_restored_test.go` is **not** in either list above — earlier
journal entries (and README/REMAINING-DOUBLES text carried over from before
2026-09-18) claimed it was retired and removed; the current tree has it
byte-identical to `origin/core`. Treat that specific removal claim as
unverified against this tree.

## Key decisions, with dates

- **2026-09-09** — `brine-dispatch` stops emitting flags; adapter moves to
  contract 5 (document-native).
- **2026-09-10** — Registrar, reaper and node-resolution families move onto
  the real `envtest` API + real Postgres, each with paired production-fault
  mutation evidence (first instances of the checkpoint pattern this whole log
  follows). The private-network-namespace launcher is prototyped, then
  promoted into `scripts/`.
- **2026-09-11** — Goal marked **blocked**: remaining fidelity work needs a
  real kubelet/exec/log environment `envtest` cannot provide, and no
  container runtime is available locally. User approves (a) fixing the
  `PodWatcher` expiry-recovery defect and (b) running resource-limited
  workloads in an owned temporary namespace on the shared cluster — this is
  the live tier's origin. Sidecar log-routing fix approved and migrated to a
  live fixture same day.
- **2026-09-12** — Resource-cancellation process-group fix approved and
  migrated (`resource_process.go`); real Git/time-resource/S3 fixtures begin
  replacing host-echo resource doubles.
- **2026-09-13** — First full v5 coverage gate passes (both tiers).
- **2026-09-14/15** — Bulk of the no-doubles retirement: native daemon
  stand-ins, node-address status writers, watch/recovery/failure-policy
  consolidation, the seven whole-file deletions above, and most of the
  partial retirements.
- **2026-09-16/17** — Final mirroring cases move to real peer daemons with
  opt-in discovery (last shared Node-status writer removed); an eviction
  false-negative (daemon binary too large for its own ephemeral-storage
  budget) is found and fixed by adopting the already-documented `-ldflags='-s
  -w'` stripped build; OOM-priority timeout investigated but not resolved.
  589/589 full gate passes 2026-09-17.
- **2026-09-18** — `core-brine-audit-20260918` requested as the review
  branch. Commit `388692eb54` reverts every production fix this migration
  made (watch.go, exec_status.go, resource_process.go, sidecar_logs.go,
  volume.go, podExitCode/process.go, process.go's `writePodDiagnostics`) back
  to `origin/core`, and the seven now-red scenarios move into
  `features/live/pending/` with the defect and the reverted fix each
  documented inline. This worktree, `core-brine-rebased-20260918`, carries
  that same reverted production state.
- **2026-09-18 (later)** — branch `brine-v5-fix/prod-fixes` re-applies the
  seven fixes onto this tree as focused changes over current `core`, which
  had meanwhile grown its own mid-command pod-destruction handling
  (`confirmPodSurvivedExec`, commits `4ee45fc936`/`d5433280d1`). The two are
  reconciled rather than one replacing the other: the status-checking exec
  transport turns a silent error-stream EOF into an error, and `Wait` then
  asks the Pod the same question core's nil path asks, so a Pod destroyed
  under its command is still named as such and still not retried. The eight
  parked scenarios move up into `features/live/` and the pending directory
  is removed.
- **2026-09-18 (integration)** — `core-brine-v5-fixes` merges five fix
  branches over `c96fac16a7`: `prod-fixes` (above); `test-restore` (24 Go
  tests deleted by `3822b69a56` with no covering scenario or with a
  `@live-kubernetes`-only replacement come back — StreamOut retry/peer
  fallback, the mount-builder family, and five Ginkgo leaves — each with a
  ledger row in `DISPOSITION-jetbridge.md`); `adapter` (the adapter disposes
  live resources on every exit path, including early `RunPlan` returns,
  SIGINT/SIGHUP and panics; recorder disposers record instead of panicking
  into a swallowed boolean; `brine-verify-run` now requires a declared
  scenario floor); `steps-hygiene` (attributable temp dirs, daemons killed as
  a process group with their last output on the error, race-free port
  selection, module-wide fake-client guard); `scripts-docs` (guard wiring:
  `make test-brine-guards`, cancel-safe `scripts/coverage`). The only merge
  conflict was this file.
- **2026-09-18 (integration, round 2)** — `core-brine-v5-fixes` merges five
  more branches over `718763bc57`, in order: `brine-v5-fix2/disposal`
  (scenario disposers tracked and drained on every exit path, SIGTERM
  replace-then-re-raise handover, `scripts/coverage` passes `NAME=PATH` to
  `brine-verify-run`); `brine-v5-fix2/ports` (shared-port daemon groups and
  otelcol retry on address-in-use, with a bind probe immediately before each
  daemon start); `brine-v5-fix2/restore-live-only` (nineteen more Go tests
  retired against `@live-kubernetes`-only or nonexistent scenarios come back
  verbatim into their original files); `brine-v5-fix2/ledger-resolve` (58
  DISPOSITION rows and the summary table resolved to real `Scenario:` lines);
  `brine-v5-fix2/prod-fix-tests` (local tests for the watch-expiry, sidecar
  fallback, status-checking executor and resource-cancel fixes, each shown red
  against the pre-fix source). The only conflict was
  `DISPOSITION-jetbridge.md`: restore-live-only and ledger-resolve had both
  restored the same thirteen specs; the verbatim originals were kept,
  `ledger_restored_test.go` was trimmed to the one spec only it restored
  (`JB-process-023`), and each row carries both passes' bullets. Verified on
  the merged tree: `go build ./...`, `go vet ./atc/worker/jetbridge/...
  ./cmd/artifact-daemon/...`, `go test ./atc/worker/jetbridge/` (ok, 169s),
  `ginkgo ./atc/worker/jetbridge/` (91 passed, 1 skipped), nested-module
  `go build`/`go vet`, `make test-brine-guards`, `sh -n scripts/coverage`,
  and `brine-verify-run` with the `NAME=PATH` shape (floor of 542 derived
  from the manifest, exit 1 — not an argument-shape rejection). Nested
  `go test ./...` fails only the five documented environment cases, with
  scenario errors identical to the `718763bc57` capture; no REAL failure.
- **2026-09-18 (integration, round 3)** — `core-brine-v5-fixes` merges three
  more branches over `d702d745ae`, in order: `brine-v5-fix3/disposers-all`
  (37 of the 53 `RegisterDisposer` sites in `steps/` become
  `TrackDisposer(rec, name, func() error)`; the 16 that remain are bare
  context cancellations, enforced by the new AST guard
  `TestGuardEveryRecorderDisposerReleasesOnlyAContext`; no disposer body
  panics any more, multi-release bodies `errors.Join`; `TrackDisposer`
  returns a release-once handle so a per-step fixture can also be drained at
  exit; the adapter's signal channel stays registered through the whole
  drain so a second SIGTERM/SIGINT no longer kills the process mid-release);
  `brine-v5-fix3/ledger-final` (every DISPOSITION row names its carrier —
  24 cells rewritten, 13 bullets annotated, count table 108 absent at
  `c96fac16a7` / 46 restored / 62 still absent with 58 unit-tier cited, 4
  Go-for-Go, 0 live-only, 0 open; `TestNodeIPResolver_Resolve` restored
  with a one-`Nodes.Get` assertion, `RcKeyHonorsLocator` carried by a new
  `artifact-recording.feature` Examples row that only CI can execute here);
  `brine-v5-fix3/comments` (two false test comments corrected; the hold
  test's failure dump prints the stderr tail and every error/scenario_end
  line, so the real factory error is visible). No merge conflicts. Verified
  on the merged tree: `go build ./...`, `go vet ./atc/worker/jetbridge/...
  ./cmd/artifact-daemon/...`, `go test ./atc/worker/jetbridge/` (ok, 90s),
  `ginkgo ./atc/worker/jetbridge/` (91 passed, 1 skipped), nested-module
  `go build`/`go vet`/`gofmt -l` (clean), `make test-brine-guards` (ok).
  Nested `go test ./...` fails only the five documented environment cases
  (otelcol absent, `.build/busybox` absent, auth binaries); no REAL failure.

## Known gaps and deferred items

**Production fixes identified during migration, reverted for the audit
branch, and re-applied to `atc/worker/jetbridge` on 2026-09-18.** Each was
specified in a `features/live/pending/*.feature` file while reverted; those
scenarios now run from `features/live/` and the table stays as the record of
what each one guards:

| Defect (reproduced against `origin/core` before 2026-09-18) | Live feature | Fix location |
|---|---|---|
| `client-go` treats an EOF on the v4/v5 exec error stream with no status bytes as exit 0 — a vanished pod or severed exec reports false success | `exec-interruption.feature`, `severed-artifact.feature` | `exec_status.go` (`statusCheckingUpgrader`/`execStatusStream`), wired into `SPDYExecutor.ExecInPod` |
| Cancelling a resource exec closes the SPDY stream but not the command's children (no terminal, so no signal reaches the process group) | `cancellation.feature` | `resource_process.go` (`cancellableResourceCommand`/`cancelResourceCommand`, `setsid` + PID/birth-time-guarded group kill) |
| `execProcess.Wait` streams a sidecar's log only when a dedicated writer exists; with none, the sidecar's output never reaches any build stream | `sidecar-logs.feature` | `sidecar_logs.go` fallback (`serializedLogWriter`/`prefixedLogWriter`) |
| `Volume.StreamIn` execs `tar xf - -C targetPath` against a directory nothing created, for the one caller that passes a non-`.` path | `volume-io.feature` | `volume.go` (`mkdir -p -- "$1" && exec tar xf - -C "$1"`) |
| `PodWatcher.Next` drops non-Pod `Status` events and reconnects at the same expired resource version forever instead of recovering | `pod-watch.feature` | `watch.go` (`IsResourceExpired`/`IsGone` handling) |
| `podExitCode` only switches on `PodSucceeded`/`PodFailed`/`PodRunning`; a pod stuck `Pending` behind an unpullable sidecar with a completed main never returns | `compatibility-process.feature` | `podExitCode` also handling `PodPending` |
| `writePodDiagnostics` only prints the `PodScheduled` condition when it's False or has a Reason, so a genuinely scheduled-then-pull-failed pod never gets the diagnostic line the build log promises | `startup-failure.feature` | `process.go`'s `writePodDiagnostics` |

Do not read any pre-2026-09-18 checkpoint above (or in `DISPOSITION-jetbridge.md`
/ `REMAINING-DOUBLES.md`) as describing current production behavior — those
checkpoints are accurate about what was true when they were written, not
about this tree.

**Other open items:**

- **`StreamIn` needs `sh` only for a nested destination**: the `volume.go`
  fix execs `sh -c 'mkdir -p -- "$1" && exec tar xf - -C "$1"'` for a path
  below the mount root, which is the one caller the defect was about; an
  upload to the root itself is still a bare `tar`, so a shell-less image
  used purely as a volume target keeps working. The nested form carries the
  same standing requirement the task supervisor and the resource process
  group already impose on every image that runs a step.
- **CI**: the `brine` job has never executed. v11 (adds gcc/busybox/otelcol,
  needed for the fetch-script and OTLP-tracing scenarios) was published
  2026-09-19 and every pipeline job pins it (`deploy/test-runner.md`); the
  first CI run of the job is still outstanding.
- **Live tier**: passes locally against an explicitly authorized cluster;
  has never executed inside CI.
- **Historical OOM-priority timeout**: a scenario asserting that an OOM kill
  is reported ahead of the crash loop it caused has timed out intermittently
  across several full runs. The 2026-09-17 fix addressed a *different*,
  confirmed bug (an unstripped daemon binary exceeding its own
  ephemeral-storage budget, so a real eviction was misread as a delayed
  observation), and that fix is verified. The original OOM-priority timeout's
  cause remains unexplained; passing reruns are not a diagnosis.
- **Every "genuine OOM" premise depends on the node's swap state** (CI build
  873827, five live scenarios: both `dead pause pod` OOM rows, `An
  interrupted exec reports the container's OOM kill`, `An OOM kill is
  reported ahead of the crash loop it caused`, and its crash-loop premise).
  The fixture (`steps/live_oom.go`) has the task allocate 256 MiB under a
  verified 64 MiB cgroup limit and then `exit 9`, expecting the kernel to
  kill it first. The task node runs cgroup v1 with 2 GiB of swap enabled,
  kubelet `failSwapOn=false`, and no swap accounting
  (`memory.memsw.limit_in_bytes` does not exist, so the kernel was not
  booted with `swapaccount=1`). A container at its memory limit therefore
  pages out instead of being killed whenever the node has free swap, and
  the allocator runs to completion: `Terminated{ExitCode:9, Reason:Error}`
  where the scenario needs `ExitCode:137, Reason:OOMKilled`. That is also
  the likeliest cause of the historical intermittency above: the premise
  held only while the node's swap happened to be full. The scenario cannot
  fix this from inside a pod: page locking needs `CAP_IPC_LOCK`, which the
  fixture namespaces' PodSecurity baseline refuses. Decision 2026-09-20:
  swap is turned off on the node (`swapoff -a`, fstab entry commented),
  and a move to cgroup v2 with an OS upgrade is a later host track. The
  allocator (`cmd/brine-oom-hog`) still locks pages where a node permits
  it and reports, not fails, where it cannot.
- **Go toolchain**: `TestPeerFetch_RestrictiveModesNormalized` and
  `TestStreamIn_RestrictiveModesNormalized` fail on installed Go 1.25.12 and
  pass on 1.26.1 in a focused comparison. No upgrade was applied; this needs
  a decision, not just a retry.
- **No-doubles**: not complete. `REMAINING-DOUBLES.md`'s content is folded
  into the "Other still-open double families" note below; that file is
  deleted as of this edit.
- Coverage (82.36%) and the 589/589 full-gate pass are both measurements of
  the *pre-revert* source. No fresh coverage run or full local+live gate has
  been taken against the integrated tree; the eight un-parked live scenarios
  have not been executed since the fixes were re-applied (they need an
  explicitly authorized cluster via `BRINE_KUBE_CONTEXT`). The Go-side
  evidence for the seven fixes is the unit/regression tests that landed with
  them (`go test ./atc/worker/jetbridge/` is green on the integrated tree).
- **Adapter test prerequisites on a developer Mac**: three
  `cmd/brine-adapter-jetbridge` tests (`TestDocumentOwnsSelection`,
  `TestEngineReleaseDrainsRealDaemon`,
  `TestHoldPreservesThenDrainsRealResources`) and one `steps` test
  (`TestTraceCaptureExportsAndDisposesRealCollector`) fail without `otelcol`;
  `TestBusyboxScriptOutcomeBoundary` fails without a built `.build/busybox`.
  The three adapter cases fail for ONE reason and it is not a cwd: the
  `span-capture` resource is `ScopeScenario`, `RequireAllForScope` acquires
  every definition at a scope rather than the ones a scenario's steps ask
  for, and its factory needs `otelcol` on PATH or `BRINE_OTELCOL_BINARY`.
  Every scenario in every adapter run therefore fails at acquisition
  ("Resource 'span-capture' factory failed"), before a hold or a release can
  be reached. `auth-binaries` is suite-scoped and walks up from the adapter's
  cwd looking for `skymarshal/dexserver/dexserver.go`; the two hold/selection
  tests leave that cwd inside the repository, so for them it is not
  implicated. `TestEngineReleaseDrainsRealDaemon` is the exception: it stages
  its scenario through the engine from a temp-dir scope, so the walk reaches
  `/` first and its scenario fails at `auth-binaries` ("cannot locate
  authentication source root") before `span-capture` is ever acquired — the
  same message at `718763bc57` and on the round-2 integrated tree.
  They fail identically at `c96fac16a7`; they are environment prerequisites,
  not regressions, but they fail rather than skip, which hides a real break
  behind a familiar red.
- **SIGTERM without a temp sweep**: closed. The adapter now takes SIGTERM
  itself, drains its tracked scenario disposers and its resource state,
  reports every disposal failure, sweeps its temp root, and only then
  installs `brine.InstallSigtermDrain` and re-raises, so the library's
  cancellation contract still emits the `recorder_drain` pair and still owns
  exit 143. Two handlers on one signal could not be ordered, which is why the
  library's is installed last rather than alongside.
- **`brine-verify-run` is stricter**: a bare manifest name with no
  `--min-scenarios` (or `NAME=COUNT` / `NAME=PATH/.brine`) is rejected.
  `scripts/coverage` IS an in-tree caller and was passing bare names, so the
  whole gate ran and then exited 2 on its argument shape; it now passes
  `NAME=PATH` for both manifests, which derives each floor from that
  manifest's own feature files. An out-of-tree CI invocation still needs a
  floor of its own.

### Other still-open double families (from REMAINING-DOUBLES.md, 2026-09-17)

- A handful of reported-lifecycle/observability fixtures still supply status
  rather than observing it for real, where the real path could not yet be
  proven without losing an existing distinguishing assertion (e.g. some
  repeated-OOM terminal-history detail).
- `steps/scan_images.go`'s `panicImageResolver` remains the one approved,
  explicitly labelled fault-injection exception (resolver-panic recovery
  needs an actual panic, not an HTTP/registry error).
- The partial-output-put fixture uses an approved executable fault (valid
  version JSON, then a real exit 4) in an otherwise fully real pod/pool/DB/SPDY
  path — an explicit, labelled boundary, not a silent double.

None of these are claimed complete; they are the residue the mechanical
guards (`kubernetes_clients_test.go`, `vocabulary_test.go`) cannot catch by
themselves, because each is a real client/transport used in a way that still
needs a judgment call about fidelity.
