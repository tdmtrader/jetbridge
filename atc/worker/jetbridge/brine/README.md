# Brine tests

Behavioral tests for `atc/worker/jetbridge`, run through Brine as a
third-party runner (`cmd/brine-adapter-jetbridge`, contract 5,
document-native — see `local/.brine`'s header comment for what that means).
Scenarios share
one `WorkerReady` fixture: a real `envtest` Kubernetes API server + etcd (no
kubelet), real PostgreSQL, and a real `artifact-daemon` subprocess — no
recording doubles, enforced by an AST import guard and vocabulary guards in
`steps/`. Where kubelet/exec/scheduler fidelity is required, scenarios live
in the **live** tier (`live/main/.brine` and `live/recovery/.brine`) against a real cluster.

The fixture design, migration evidence, and historical coverage gaps are
recorded in the [campaign archive][campaign]. This file covers build/run mechanics.

## Prerequisites

Linux with user/network namespaces (the local tier is not viable on macOS —
it builds a namespace launcher via `cc`), BusyBox (`BRINE_BUSYBOX_BINARY` if
not on `PATH`), the module-pinned Go toolchain, a matching Brine CLI/engine,
PostgreSQL client tools on `PATH`, an OpenTelemetry Collector
(`BRINE_OTELCOL_BINARY` if not on `PATH`), and `kube-apiserver`/`etcd`
binaries selected by `KUBEBUILDER_ASSETS` for `envtest`.

On a developer Mac, the adapter tests (`cmd/brine-adapter-jetbridge`) need
only the Brine CLI and engine on `PATH` (`brine` and `brine-engine`, in
`~/brine-private/target/debug`). That includes the engine test
(`TestEngineReleaseDrainsRealDaemon`) now that `review-binaries`, like
`auth-binaries` and `span-capture`, starts only at its first step. Two
`steps` tests keep a prerequisite and fail rather than skip without it:
`TestBusyboxScriptOutcomeBoundary` needs `.build/busybox`, which
`scripts/build-private-network` installs on Linux only (or
`BRINE_BUSYBOX_BINARY`), and `TestTraceCaptureExportsAndDisposesRealCollector`
needs `otelcol` on `PATH` (or `BRINE_OTELCOL_BINARY`).

## Local tier

```sh
sh scripts/build-private-network
brine run local --no-engine --mode sync --format jsonl
```

`build-private-network` compiles `cmd/brine-adapter-jetbridge` and
`cmd/artifact-daemon`, verifies the BusyBox/namespace prerequisite, and
requires Linux. `local/.brine`'s `runner.binary` is `../scripts/run-private-network`,
a wrapper — not the adapter itself — that execs the built adapter inside a
private user+network namespace so this run owns its own loopback and ports
without touching the host.

## Live tier

```sh
export BRINE_KUBE_CONTEXT=your-authorized-test-context
(cd live && brine run --no-engine --mode sync --format jsonl)
```

There is no default context and no mock fallback. Each scenario creates and
tears down its own namespace (baseline pod security, a resource quota,
container limits). From the suite root, plain `brine run` discovers and runs
`local/.brine`, `live/main/.brine` and `live/recovery/.brine` together.
Plain `brine run live` still runs every live scenario. The coverage task selects
the two live manifests separately so their complete feature files can overlap.
Run only the complete local tier with `brine run local --no-engine --mode sync`.

The artifact-handoff hostPath/hostPort fixture needs explicit additional
approval and is not part of an ordinary live run:
`BRINE_ALLOW_HOSTPATH_TESTS=1`, `BRINE_ALLOW_HOSTPORT_TESTS=1`, at least
one node labelled `brine.dev/live-artifacts=approved` (or one named in
`BRINE_LIVE_ARTIFACT_NODE`), `BRINE_LIVE_ARTIFACT_DAEMON_PORT` (an unused port
in 49152–60999), and `BRINE_LIVE_ARTIFACT_DAEMON_BINARY` (an absolute path to
a current, stripped, static Linux `artifact-daemon` build — see
`features/live/artifact-handoff.feature` for the exact build command). CI
selects `BRINE_KUBE_CONTEXT=in-cluster` and supplies these as pipeline
variables. Historical execution evidence is in [V5-MIGRATION.md][migration].

### Cross-node tier

`live-multinode/.brine` runs the same fixture on every approved node at once:
a store and a daemon per node, one root path (`/var/lib/brine-artifacts/<namespace>.<namespace UID>`,
a per-node symlink into that node's anchor emptyDir, so the kubelet still
reclaims the data), and one service publishing every daemon. Its scenario
produces an artifact on one node and reads it from a task on another. It needs
two approved nodes up, so it is `discover: false`; `scripts/coverage` counts
the approved nodes that are Ready, schedulable and carry a ready artifact cache,
runs the tier when there are two, and prints that it did not run otherwise.
Run it by hand from `live-multinode/` with the live tier's environment.

## Disposable-kubelet tier

Scenarios that need a real kubelet *and* the Hangar output plane on its node
(a node carrying the output plane's ready labels, its artifact daemon serving
the capture routes) cannot run
in the live tier, which must not change the deployed cluster's nodes. They
live in `features/kubelet/` and run against a single-node K3s cluster that a
privileged CI task creates and destroys:

```sh
fly -t home execute -c deploy/run-kubelet-task.yml --privileged \
  -i repo=<git archive of the ref> -v github-token="$GITHUB_TOKEN"
```

`hack/test-run-kubelet` marks the node `brine.dev/run-kubelet=owned-ci` and
runs `kubelet/.brine` (Run cancellation). The steps refuse any cluster that is
not that loopback, single, marked node. Its CI home is the
`run-cancellation-kubelet` job in `deploy/k8s-e2e-pipeline.yml`
(live once that pipeline is set).

## Guard tests and vet

```sh
go vet ./...
BRINE_PROTOCOL_LAUNCHER="$PWD/scripts/run-private-network" \
  go run github.com/onsi/ginkgo/v2/ginkgo -r --timeout=5m
```

Runs the AST import guard (`steps/kubernetes_clients_test.go`, rejects any
fake Kubernetes client/reactor imports across the module and locally declared
client doubles in non-test sources), the vocabulary/wiring guards
(`steps/vocabulary_test.go`: no dead step definition, no unresolved or
ambiguous step phrase, and a nonempty active feature corpus), and the adapter's
own black-box protocol tests.

See [features/README.md](features/README.md) for feature tags.

## Full coverage gate

```sh
sh scripts/coverage
```

Requires `BRINE_KUBE_CONTEXT` and the live-artifact-handoff approvals above;
builds one instrumented adapter, runs the complete local, main live and recovery manifests
concurrently in separate process groups, and requires at least 50% Brine-only coverage
of `atc/worker/jetbridge` production statements — package-scoped, not
repository-wide. Each tier must independently pass its CLI verdict and full
manifest verification. Raw coverage is kept per tier and merged only after
verification; cross-node work follows all three under the same approvals.
The existing pipeline job retains all cluster exclusion locks. Restores the
normal adapter after all three processes finish.

The recovery manifest owns the compatibility, container-lifecycle, task-command,
pod-watch and startup-failure features. Its real eviction, OOM, replacement,
execution, watch and startup scenarios have their own databases and namespaces
and do not use the live artifact-daemon host port. The task's PostgreSQL sidecar
uses only a pod-local port; watch routes allocate local ports and permission
changes apply only to the scenario's namespace. Each manifest runs its scenarios
serially, keeping process-local metrics and tracing isolated. Their feature
contents, production waits and kubelet configuration are unchanged.

For a matched sequential baseline or rollback, run
`BRINE_COVERAGE_SCHEDULE=serial sh scripts/coverage`. This changes scheduling
only; both modes run the same manifests and coverage gate.

## Where to look next

- [Campaign archive][campaign] — migration decisions, per-test dispositions,
  mutation evidence, and historical coverage gaps. All nine campaign records
  live in the hearth beside the main checkout, under
  `../jetbridge-hearth/proposals/20261009T0940_codebase_proportionate/brine-campaign-records/`
  (relative to the checkout root, not an individual worktree).
- [V5-MIGRATION.md][migration] — the historical fixture and migration decision log.
- [features/README.md](features/README.md) — feature tags.

[campaign]: ../../../../../jetbridge-hearth/proposals/20261009T0940_codebase_proportionate/brine-campaign-records/
[migration]: ../../../../../jetbridge-hearth/proposals/20261009T0940_codebase_proportionate/brine-campaign-records/V5-MIGRATION.md
