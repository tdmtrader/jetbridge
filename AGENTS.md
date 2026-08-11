# Working in this repository

JetBridge is a Kubernetes-native fork of Concourse CI. This file records things that
are **expensive to rediscover** — traps where the code looks like it does one thing and
does another, and measured numbers that settle an argument.

`CLAUDE.md` covers how to run the test tiers. This file covers what will surprise you.

Every entry below was verified against this branch. If you find one that is no longer
true, delete it — a stale entry here is worse than no entry.

## Tests

**`go test -run TestSuite/Some.Describe` does not focus a Ginkgo `Describe` — it silently
runs the whole suite.** Ginkgo v2's `GinkgoTestingT` is `interface{ Fail() }`, so no Go
subtests exist and the pattern after the slash is discarded. Use
`-ginkgo.focus='Pod Cleanup'`, which is registered onto `flag.CommandLine`.

**Run database-backed suites with `ginkgo`, never `go test ./...`.**
`atc/postgresrunner/ginkgo.go` picks its port as `5433 + GinkgoParallelProcess()`, which
is always 5434 under plain `go test`. Two database-backed packages running concurrently
bind the same port and, through the shared `/tmp` socket, silently reach *each other's*
server — it surfaces as `database "testdb_template" already exists` or
`cannot drop a template database`, from a suite that did nothing wrong. `make test-unit`
uses `ginkgo -r -p`, which gives each suite its own postmaster.

**Nothing in `make test-unit` runs the two shell scripts injected into task images under
the shell they actually meet.** `supervisorScriptTemplate`
(`atc/worker/jetbridge/supervisor.go`) and `pauseCommand`
(`atc/worker/jetbridge/container.go`) must be POSIX-sh only, but the tests execute them
with the host `/bin/sh` — bash on macOS — and the only real busybox execution is behind
`//go:build live`, which the Makefile never compiles. After editing either, exec it in a
real busybox pod. The failure mode is a task that silently never runs while the suite
stays green.

**A pod `volumeMount` with no matching pod `Volume` passes the entire unit suite and
breaks every build in a real cluster.** `Container.buildPod` gets mounts and volumes from
separate functions with no cross-check, and `StorageBackend.BuildFetchInitContainers`
returns only `[]corev1.Container`, so it physically cannot add the Volume its mount needs.
When touching either, assert in the same test that every mount name resolves to a Volume.

**`metric.Counter.Delta()` and `metric.Gauge.Max()` are destructive reads** despite
accessor-shaped names — `Delta()` does `cur.Swap(0)`, `Max()` does `max.Swap(-1)`. A spec
asserting the same counter twice sees 0 the second time. The flip side: one call at spec
start is a free reset.

**There are eight OTel init functions in `atc/metric`, not one, and every `Record*`
nil-guards and returns silently.** Production calls all eight. Calling only
`InitOTelMetrics()` leaves seven metric families dark with no panic and no error.

**A test's URL pattern must match what the SUT emits in production.** Contrived artifact
URLs once masked a real peer-fallback 404 — the test used `/artifacts/{key}` where
production emits `/artifacts/steps/{key}`.

## Kubernetes runtime

**Production steps always run through `execProcess`; `Process`/`newProcess` is a
test-only fallback.** `atc/atccmd/command.go` wires `K8sExecutor` unconditionally and
`atc/worker/factory.go` is the only non-test `SetExecutor` caller, so the fallback never
fires — which makes `Process.Wait`'s delete-pod-on-cancel branch dead code. Read
`execProcess` when reasoning about pod lifecycle; `process.go` declares `Process` first
and reads as if it were the main path.

**Images are resolved in-process against the OCI registry — no check pod is ever
created.** This diverges from upstream Concourse. Every failure inside the lidar
resolvers logs and returns: no check build, no DB check error, nothing in the UI.
Diagnose from web logs (`resolve-resource`, `failed-to-resolve-digest`).

**Native resolution reads only `repository`, `tag`, `username` and `password`, and
silently ignores everything else — including `insecure` and `ca_certs`.** `atc.Source` is
an unvalidated `map[string]any`, so a pipeline setting `insecure: true` is accepted and
does nothing. The symptom is an x509 error from a resource whose YAML plainly handles it,
with no container to hijack.

**A task step's `vars:` block is ignored unless the step uses `file:`.** The inline
`config:` branch never reads `step.plan.Vars` and the validator does not warn. It
surfaces at build time as an unresolved-`((var))` error naming the variable, not the
ignored block.

**The `waiting-for-worker` build event is fully plumbed and has zero producers.** It is
declared in four delegate interfaces, registered in `atc/event/types.go`, and rendered by
both fly and Elm — but `atc/worker/pool.go` takes no delegate, so nothing can emit it. Do
not build UI or assertions on it.

**`component.Runner` with `Interval=0` never polls and wakes only on NOTIFY.** The
Coordinator wraps `Runnable.Run()` identically for `RunPeriodically` and
`RunImmediately`, so a component needs NOTIFY calls at the right DB mutation points
rather than runner-level tests.

**Task caches on hostPath are never reclaimed.** Any step with `caches:` auto-selects
hostPath whenever the artifact-daemon host path is set (it is, by chart default), and
nothing deletes those directories: the sweeper skips `/caches/`, there is no DELETE
route, and `atc/gc/task_cache_collector.go` only removes DB rows. Treat that node disk as
monotonic growth.

## Build, chart and deploy

**Task rootfs images are pulled `PullIfNotPresent`, so pushing a rebuilt image to the
same mutable tag is a silent no-op and CI keeps testing the old binary.** Changing a
runner image requires bumping its tag at *every* site in the pipeline plus a
`set-pipeline`. A `resource_config_scope` FK flake was once chased for six weeks against
guards CI had never actually executed.

**The chart documents `cacheStore` values the binary rejects.** `deploy/chart/values.yaml`
offers `artifact` and `pvc`; `atc/worker/jetbridge/config.go` accepts only `hostpath` and
`emptydir` and fails at web startup. This is real drift, not a doc nit.

## Local environment

**The K8s tiers are testcontainers-K3s, not KinD** — there are zero `sigs.k8s.io/kind`
imports in the tree. They are also **not viable on macOS**: containerd-in-Docker is
unstable under Colima at any memory size, measured identically at 8 GB and 16 GB. Run
that tier in CI.

**The ATC component intervals are hard-coded and live in the DB `components` table, not
in CLI flags.** Dropping the three scheduler-path intervals to 2s via SQL took a
five-test suite from 57.6s to 21.5s.

**`fly clear-resource-cache` hangs forever on piped stdin** — it is the only confirming
fly command with no `--non-interactive`. Call the API endpoint instead. Before automating
any fly command that prompts, grep it for `SkipInteractive`.

## Repo conventions

**Never constrain a "who consumes this?" grep by file extension.** This repo's dev
scripts are extensionless 755 executables — `atc/scripts/*`, `hack/*`. An
`--include="*.sh"` sweep once reported a package as dead when its only consumer was
`atc/scripts/create-migration`, the documented way to create a migration.

**Derivable facts get one declaration.** The version lives in `VERSION` and is asserted
against `versions.go` and the chart's `appVersion` by `version_consistency_test.go`. The
head migration is derived from the binary's own embedded set, not written down — an
earlier hardcoded copy outlived the migration it named and took five specs red with it.

**Every guard asserts it matched something.** A structural test that silently matches
zero files passes forever. Where a test scans the tree, it must fail when the scan is
empty, and it must not assert an exact count — that is an enumerated allowlist in
costume.
