# Brine tests

Behavioral tests for `atc/worker/jetbridge`, run through Brine as a
third-party runner (`cmd/brine-adapter-jetbridge`, contract 5,
document-native — see `.brine`'s header comment for what that means).
Scenarios share
one `WorkerReady` fixture: a real `envtest` Kubernetes API server + etcd (no
kubelet), real PostgreSQL, and a real `artifact-daemon` subprocess — no
recording doubles, enforced by an AST import guard and vocabulary guards in
`steps/`. Where kubelet/exec/scheduler fidelity is required, scenarios live
in a separate **live** tier (`live/.brine`) against a real cluster.

**Why** the fixture model, the no-doubles rule, contract 5, the
private-network local tier and the live tier exist — and the current known
gaps, including the seven production fixes that were reverted for the audit
branch and have since been re-applied (their scenarios, once parked in
`features/live/pending/`, now run in the live tier) — is in
[V5-MIGRATION.md](V5-MIGRATION.md). This file is only build/run mechanics.

## Prerequisites

Linux with user/network namespaces (the local tier is not viable on macOS —
it builds a namespace launcher via `cc`), BusyBox (`BRINE_BUSYBOX_BINARY` if
not on `PATH`), the module-pinned Go toolchain, a matching Brine CLI/engine,
PostgreSQL client tools on `PATH`, an OpenTelemetry Collector
(`BRINE_OTELCOL_BINARY` if not on `PATH`), and `kube-apiserver`/`etcd`
binaries selected by `KUBEBUILDER_ASSETS` for `envtest`.

## Local tier

```sh
sh scripts/build-private-network
brine run --no-engine --mode sync --format jsonl
```

`build-private-network` compiles `cmd/brine-adapter-jetbridge` and
`cmd/artifact-daemon`, verifies the BusyBox/namespace prerequisite, and
requires Linux. `.brine`'s `runner.binary` is `scripts/run-private-network`,
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
both `.brine` and `live/.brine` together.

The artifact-handoff hostPath/hostPort fixture needs explicit additional
approval and is not part of an ordinary live run:
`BRINE_ALLOW_HOSTPATH_TESTS=1`, `BRINE_ALLOW_HOSTPORT_TESTS=1`,
`BRINE_LIVE_ARTIFACT_NODE`, `BRINE_LIVE_ARTIFACT_DAEMON_PORT` (an unused port
in 49152–60999), and `BRINE_LIVE_ARTIFACT_DAEMON_BINARY` (an absolute path to
a current, stripped, static Linux `artifact-daemon` build — see
`features/live/artifact-handoff.feature` for the exact build command). CI
selects `BRINE_KUBE_CONTEXT=in-cluster` and supplies these as pipeline
variables; the live tier itself has never run in CI (see V5-MIGRATION.md).

## Guard tests and vet

```sh
go vet ./...
BRINE_PROTOCOL_LAUNCHER="$PWD/scripts/run-private-network" \
  go run github.com/onsi/ginkgo/v2/ginkgo -r --timeout=5m
```

Runs the AST import guard (`steps/kubernetes_clients_test.go`, rejects any
fake Kubernetes client/reactor import under `steps/`), the vocabulary/wiring
guards (`steps/vocabulary_test.go`: no dead step definition, no unresolved or
ambiguous step phrase, every `hangar-*.feature` scenario cites a requirement
tag), and the adapter's own black-box protocol tests.

```sh
./wirecheck.sh    # every *Definitions() func in steps/ is registered in registry.go
./timecheck.sh    # each features/*.feature completes within BRINE_FEATURE_TIMEOUT (default 120s)
./pendingcheck.sh  # features/pending/ still type-checks statically, even though it never runs
```

See [features/README.md](features/README.md) for the `features/pending/`
mechanism (checked, never executed — for the Hangar output family, whose
production code lands after its steps do) and the feature tag conventions
(`@HOP-<n>`, family prefixes like `@RF-`, `@PW-`).

## Full coverage gate

```sh
sh scripts/coverage
```

Requires `BRINE_KUBE_CONTEXT` and the live-artifact-handoff approvals above;
builds both tiers, runs both, and requires at least 50% Brine-only coverage
of `atc/worker/jetbridge` production statements — package-scoped, not
repository-wide. Restores the normal adapter after measuring.

## Where to look next

- [V5-MIGRATION.md](V5-MIGRATION.md) — the decision log: why, dated
  checkpoints, and current known gaps.
- [DISPOSITION-jetbridge.md](DISPOSITION-jetbridge.md) — per-test disposition
  of every deleted or trimmed Go suite.
- [MIGRATION-EVIDENCE.md](MIGRATION-EVIDENCE.md) — mutation-tested evidence
  for what Go coverage remains necessary, and the measured ceiling for
  further migration.
- [features/README.md](features/README.md) — tags and `features/pending/`.
- `COMPLETION-AUDIT.md`, `DISPOSITION-gc-lidar.md`, `DISPOSITION-hangar.md` —
  earlier or adjacent campaign records.
