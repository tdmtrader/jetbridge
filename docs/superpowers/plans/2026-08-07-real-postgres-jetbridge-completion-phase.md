# Real PostgreSQL JetBridge Completion Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development` or `superpowers:executing-plans`.
> Implement one task at a time, preserve other agents' edits, and do not stage
> or commit files outside the task's ownership.

**Goal:** Replace JetBridge's remaining persisted-success database doubles
with real PostgreSQL workers, containers, volumes, artifacts, builds, and
factory outcomes while retaining only eight narrowly documented
runtime/transition fault seams.

**Exact baseline and target:** At `184d4e4f62`, 14 JetBridge test files import
`dbfakes` and contain 82 explicit `new(dbfakes.Fake...)` sites. The completed
phase targets 3 importing files and 8 sites: one private-helper `FakeWorker` in
`behavioral_volume_test.go`; one `FakeWorker` and one
`FakeCreatingContainer` in `container_test.go`; and one `FakeWorker`, one
`FakeCreatingContainer`, one `FakeVolumeRepository`, one
`FakeCreatingVolume`, and one `FakeCreatedVolume` in `worker_test.go`.

**Architecture:** Expand the existing opt-in `jetbridgeDB` fixture around the
single machine-wide PostgreSQL service. Each converted Ginkgo spec creates one
unique clone with `CreateTestDBFromTemplate`, constructs repositories only
from that clone, closes ordinary and singleton connections, and drops the
clone. The production JetBridge code receives real `db.Worker`,
`db.CreatedContainer`, `db.CreatedVolume`, and `db.WorkerArtifact` values while
Kubernetes clients, pod execution, streams, daemon/runtime collaborators, and
selective post-success faults stay in memory.

**Tech stack:** Go, Ginkgo v2/Gomega, `atc/postgresrunner`, `atc/db`,
`atc/db/dbtest`, `atc/db/lock`, PostgreSQL, fake Kubernetes clientsets.

## Global constraints

- Use the already-running PostgreSQL service at `127.0.0.1:15432`. Never
  start, stop, recreate, or alter Docker, Colima, theborg, or the service.
- Do not add a suite-wide clone `BeforeEach`. Only converted Describes call
  `useJetbridgeDB`, exactly once per spec.
- Register every clone connection and every `OpenSingleton` connection for
  cleanup before `DropTestDB`. No factory, worker, repository, build, volume,
  container, or artifact crosses clone boundaries.
- Keep normal Ginkgo parallelism. Fixed handles are per-spec and therefore do
  not collide across clones; multiple handles inside one spec must be unique.
- Use `db.NewFixedHandleContainerOwner(handle)` for exact pod/container names.
  `db.Worker.CreateContainer` deliberately accepts that owner's handle, so no
  fake setup helper is needed.
- Assert persisted outcomes after reloading or re-querying the exact row.
  Dynamic IDs and handles must come from returned real values, never copied
  from old fake defaults.
- A closed production connection/factory is valid for a direct first-call SQL
  failure. A failure that must occur after an earlier successful database call
  keeps one narrow generated fake or an embedding wrapper, with a comment
  naming the exact boundary.
- A `db.Worker` cannot be loaded from an already-closed connection. Tests for a
  direct worker SQL failure must open a secondary clone connection, load the
  persisted worker through a secondary `WorkerFactory`, close that connection,
  and only then invoke the worker method.
- Preserve all Kubernetes, pod lifecycle, executor, stream, daemon, runtime,
  tracing, clock, and policy seams. Do not edit production code without first
  demonstrating a focused RED test against the current behavior.
- Do not edit benchmark or corpus files. Do not push.

## Task 1: Expand and prove the shared opt-in JetBridge fixture

**Files:**

- Modify `atc/worker/jetbridge/jetbridge_suite_test.go`

**Fixture contract:**

Extend `jetbridgeDB` with `Conn`, `LockFactory`, `Builder`, `TeamFactory`,
`WorkerFactory`, `VolumeRepository`, `ContainerRepository`, `BuildFactory`,
`ResourceConfigFactory`, and `ResourceCacheFactory`. Build lock connections
with `postgresRunner.OpenSingleton()` and `lock.NewLockFactory`, matching the
already-reviewed engine/exec fixture lifecycle. Continue using the existing
`postgresrunner.GinkgoRunner`; do not register a second suite node.

Add:

```go
func closedJetbridgeCloneConn() db.DbConn {
	GinkgoHelper()
	conn := postgresRunner.OpenConn()
	Expect(conn.Close()).To(Succeed())
	return conn
}
```

Keep `persistNamedWorker`, but make it return a worker loaded from the same
fixture. Add small helpers only when reused by at least two tasks; file-local
volume/container helpers are preferred.

- [x] Run `pg_isready -h 127.0.0.1 -p 15432 -U postgres`.
- [x] Compile with `go test ./atc/worker/jetbridge -run '^$'`.
- [x] Run the existing non-live opt-in specs sequentially and in parallel:
  `ginkgo --focus='(Pod Name Integration|SecretEnv in Pod Spec)' ./atc/worker/jetbridge`
  and
  `ginkgo -p --focus='(Pod Name Integration|SecretEnv in Pod Spec)' ./atc/worker/jetbridge`.
- [x] Compile and vet the build-tagged standard live tests with
  `go test -tags live ./atc/worker/jetbridge -run '^$'` and
  `go vet -tags live ./atc/worker/jetbridge`.
- [x] Confirm no suite-wide `BeforeEach` calls `useJetbridgeDB` and every
  connection closes before the clone drops.
- [x] Commit only the suite file as
  `test(jetbridge): expand postgres fixture`.

## Task 2: Persist resource, process, and behavioral-runtime workers

**Files:**

- Modify `atc/worker/jetbridge/resource_test.go`
- Modify `atc/worker/jetbridge/process_test.go`
- Modify `atc/worker/jetbridge/behavioral_runtime_spec_test.go`

**Payoff:** 16 constructors to 0; three imports to 0; remove 42
`setupFakeDBContainer` calls.

For each affected top-level Describe, call `useJetbridgeDB` once in its
`BeforeEach`, persist the required named worker, and pass that real worker to
`jetbridge.NewWorker`. Delete each `setupFakeDBContainer` call. The existing
`FindOrCreateContainer` call already supplies
`db.NewFixedHandleContainerOwner(handle)`; it must create and transition the
real row through production code.

Before rewiring one representative Resource leaf, query `containers` by
worker/handle and assert one created row. Run it to RED while the leaf still
uses `FakeWorker`, then switch to the real worker and run it GREEN. Add or keep
persisted assertions that discriminate the exact handle, worker name, state,
and container metadata type after `FindOrCreateContainer`.

Do not replace fake clientsets, `fakeExecExecutor`, pod status mutation,
runtime delegate, tracing, or command/stream failure controls.

- [ ] Record the assertion-first RED result.
- [ ] Run focused GREEN:
  `ginkgo --focus='(Resource Step Execution|Process|\[PE-|\[SC-|\[RF-|\[OE|\[P3])' ./atc/worker/jetbridge`.
- [ ] Sensitivity: temporarily query the representative container with a
  different handle and prove the persisted assertion fails; restore and pass.
- [ ] Run `gofmt`, `go vet ./atc/worker/jetbridge`, and `git diff --check` on
  the three files.
- [ ] Confirm the exact constructor/import/helper payoff and obtain an
  independent review.
- [ ] Commit only these files as
  `test(jetbridge): persist runtime workers and containers`.

## Task 3: Persist artifact and volume lifecycles

**Files:**

- Modify `atc/worker/jetbridge/artifact_integration_test.go`
- Modify `atc/worker/jetbridge/integration_test.go`
- Modify `atc/worker/jetbridge/volume_test.go`
- Modify `atc/worker/jetbridge/behavioral_volume_test.go`

**Payoff:** 14 constructors to 1; four imports to one; remove four
`setupFakeDBContainer` calls.

Use real named workers and `VolumeRepository.CreateVolumeWithHandle` (or
`CreateVolume` where the handle is not externally observable), transition the
creating volume with `Created`, and initialize artifacts with the required
real name/build ID. When using a nonzero artifact build ID, persist that build
first. Reread through the actual APIs available here:
`VolumeRepository.FindVolume(handle)`, `WorkerArtifact.Volume(teamID)`, or
`Team.FindVolumeForWorkerArtifact(artifact.ID())`. Assert dynamic IDs, exact
handles, worker names, team IDs, and types; do not invent an artifact
repository. Pass real repositories to `SetVolumeRepo`; retain
Kubernetes/exec/daemon collaborators.

`behavioral_volume_test.go` is the internal package. Move its three exported
wrapper cases that currently construct `FakeCreatedVolume` into an external
Ginkgo Describe in `volume_test.go`, where the opt-in fixture is available.
Keep exactly one `FakeWorker` for the white-box `buildVolumeMountsForSpec`
test; comment that it supplies only `Name()` to a private runtime helper and
does not model persisted behavior.

Add a row assertion before rewiring one artifact leaf and capture RED. After
GREEN, reload the exact artifact volume; do not infer success only from fake
executor calls.

- [ ] Run `ginkgo --focus='(Artifact Integration|Integration|Volume)' ./atc/worker/jetbridge`.
- [ ] Run `go test ./atc/worker/jetbridge -run='^(TestVT|TestCO|TestArtifactKey|TestArtifactLocatorLocateStepMatchesOnlyTheStepsOwnEntries)'`
  for the remaining standard tests, including the neighbor of the migrated
  artifact-cleanup case.
- [ ] Sensitivity: use a wrong persisted artifact or volume ID and prove the
  focused row assertion fails; restore and pass.
- [ ] Format, vet, diff-check, recount, and obtain independent review.
- [ ] Commit only these four files as
  `test(jetbridge): persist artifact and volume state`.

## Task 4: Persist normal worker behavior and isolate transition faults

**Files:**

- Modify `atc/worker/jetbridge/behavioral_worker_test.go`
- Modify `atc/worker/jetbridge/worker_test.go`

**Payoff:** 35 constructors to 5; two imports to one; remove the final
non-container-file `setupFakeDBContainer` call.

Persist the normal worker/container/volume/artifact graph. Use real rows for
found/not-found, reuse, handle discrimination, volume lookup, and artifact
success. Replace the fake-default `InitializeResourceCache(nil)` path with a
real ResourceConfig/ResourceCache/worker-cache volume lifecycle using the
fixture factories.

Direct first-call repository errors use `db.NewVolumeRepository` or another
production factory backed by `closedJetbridgeCloneConn()`. Retain exactly five
shared sites in `worker_test.go` for failures that must occur after preceding
successful calls: `FakeWorker`, `FakeCreatingContainer`,
`FakeVolumeRepository`, `FakeCreatingVolume`, and `FakeCreatedVolume`. Each
retained declaration gets a boundary comment. `behavioral_worker_test.go`
drops `dbfakes` entirely.

Add a persisted reuse/association assertion before rewiring and capture RED.
Reload exact containers/volumes after the operation; do not replace prior
branch coverage with call-count-only assertions.

- [x] Run `ginkgo --focus='(Behavioral Worker Tests|Worker)' ./atc/worker/jetbridge`.
- [x] Sensitivity: change one expected owner/handle and prove the persisted
  reuse assertion fails; restore and pass.
- [x] Format, vet, diff-check, and confirm exact 35 to 5 payoff.
- [x] Obtain independent review of both success outcomes and every retained
  transition boundary.
- [x] Commit only these two files as
  `test(jetbridge): persist worker lifecycle state`.

## Task 5: Persist container reuse and concurrency, then delete the fake helper

**Files:**

- Modify `atc/worker/jetbridge/container_test.go`
- Modify `atc/worker/jetbridge/jetbridge_suite_test.go`

**Payoff:** 12 direct-and-helper constructors to 2; two imports to one; remove
the remaining 58 helper calls and delete `setupFakeDBContainer`.

Use a real named worker for successful create/reuse, fixed-handle, volume
mount, metadata, sidecar, metrics, attachment, and concurrency paths. Every
concurrent case gets a unique worker or handle inside its spec and asserts the
final database rows rather than fake call counts.

For direct `FindContainer` failure, add a file-local helper that opens a
secondary connection from `postgresRunner`, builds a secondary
`WorkerFactory`, loads the already-persisted worker, closes the secondary
connection, and returns that worker. Invoke `FindContainer` only after the
connection closes. Do not try to construct or load a worker from
`closedJetbridgeCloneConn()`.

Retain one shared `FakeWorker` and one shared `FakeCreatingContainer` only for
selective `CreateContainer`/`Created` failures that must follow a successful
lookup. Comment the exact boundary and keep those contexts under an explicit
`retained database transition faults` Describe.

Preserve the transition observations that are the subject of those fakes:
`Created()` failure calls `Failed()` exactly once in both the ordinary-create
and stale-creating paths; stale reuse never calls `CreateContainer`; successful
reuse preserves the original row identity. The general preference for
persisted outcomes does not replace these selective call-boundary assertions.

After Tasks 2–4 have removed all other callers, verify
`rg -n 'setupFakeDBContainer' atc/worker/jetbridge` reports only its definition,
then delete the helper and the suite's `dbfakes` import.

- [ ] Add a persisted concurrent/reuse row assertion and capture RED before
  rewiring the representative leaf.
- [ ] Run `ginkgo --focus='(Container|Concurrent container operations|Run with sidecar containers)' ./atc/worker/jetbridge`.
- [ ] Sensitivity: intentionally reuse a handle where the spec expects unique
  rows and prove the assertion fails; restore and pass.
- [ ] Format, vet, diff-check, recount, and obtain independent review.
- [ ] Commit only these files as
  `test(jetbridge): persist container lifecycle state`.

## Task 6: Persist reaper and registrar database outcomes

**Files:**

- Modify `atc/worker/jetbridge/reaper_artifact_cleanup_test.go`
- Modify `atc/worker/jetbridge/reaper_test.go`
- Modify `atc/worker/jetbridge/registrar_test.go`

**Payoff:** 5 constructors to 0; three imports to 0.

Migrate the artifact-cleanup standard test into the Ginkgo suite so it can use
one clone. Persist the `k8s-test-namespace` worker and assert reaper selection
and cleanup by rereading exact rows. Preserve every existing Reaper `It`; do
not merge active, empty, unknown, destroying, completed-build,
readable-handle, idempotency, or private-secret branches.

Use this explicit row map:

- create real `created` rows for every pod expected to survive;
- leave rows intentionally absent only for unknown-container branches;
- create real `destroying` rows for deletion branches;
- create real `created` rows owned by persisted completed builds for retained
  completed-build pod branches;
- create decoy rows when proving `missing_since` or handle selection.

`DestroyUnknownContainers` inserts destroying rows and the same `Run`
immediately calls `FindDestroyingContainers`, so never represent an active pod
with an absent row. Persist started builds for build-liveness selection and use
a closed real `BuildFactory` for the direct lookup error.

For registrar success, save through the real `WorkerFactory`, reload the
worker, and assert attributes, TTL, resource types, state, platform, tags, and
team data. For the direct SaveWorker failure use a production factory backed
by `closedJetbridgeCloneConn()`. Keep `gcfakes.FakeDestroyer`; it is a non-DB
policy/runtime seam.

- [x] Add one persisted reaper/registrar outcome assertion and capture RED.
- [x] Run `ginkgo --focus='(Reaper|Registrar)' ./atc/worker/jetbridge`.
- [x] Sensitivity: change a persisted worker/container handle and prove the
  row-selection assertion fails; restore and pass.
- [x] Format, vet, diff-check, recount, and obtain independent review.
- [x] Commit only these files as
  `test(jetbridge): persist reaper and registrar state`.

## Task 7: Full verification, exact recount, and phase closure

**Files:**

- Modify this plan's checkboxes
- Update the ignored local SDD audit with observed JetBridge counts
- Test all of `atc/worker/jetbridge`

- [ ] Run `gofmt` on every modified Go file and `go test ./atc/worker/jetbridge -run '^$'`.
- [ ] Run `pg_isready -h 127.0.0.1 -p 15432 -U postgres`.
- [ ] Run all task-focused commands.
- [ ] Run `ginkgo ./atc/worker/jetbridge` and
  `go test ./atc/worker/jetbridge -count=1`.
- [ ] Run `ginkgo -p ./atc/worker/jetbridge` to prove all unique clones remain
  isolated in one PostgreSQL service.
- [ ] Run `go vet ./atc/worker/jetbridge`,
  `go test -tags live ./atc/worker/jetbridge -run '^$'`, and
  `go vet -tags live ./atc/worker/jetbridge`.
- [ ] Recount with:

```bash
rg -l 'github.com/concourse/concourse/atc/db/dbfakes' \
  atc/worker/jetbridge --glob '*_test.go'
rg -n 'new\(dbfakes\.Fake' \
  atc/worker/jetbridge --glob '*_test.go'
rg -n 'setupFakeDBContainer' atc/worker/jetbridge
```

Expected: exactly three importing files, eight explicit constructors with the
type/file mapping at the top of this plan, and no fake setup helper.

- [ ] Inspect lifecycle/order: one opt-in clone per converted spec; every
  connection closes before drop; no repository crosses clones; persisted
  mutations are reread; fixed handles are unique within a clone; every retained
  fake carries an exact boundary comment.
- [ ] Run `git diff --check` and `git status --short`; confirm only planned
  files and tracked bookkeeping are present.
- [ ] Obtain an independent final review with no Critical, Important, or Minor
  findings.
- [ ] Record observed evidence and close the plan in a tracked docs commit:
  `docs: record real postgres jetbridge completion`.

## Phase acceptance

- [ ] PostgreSQL is one machine-wide service, with a unique template clone per
  converted spec and verified parallel execution.
- [ ] JetBridge reaches the exact 82 to 8 constructor target and 14 to 3 import
  target, or a reviewer-approved narrower retained seam is explicitly counted.
- [ ] Successful worker/container/volume/artifact/build/registrar/reaper state
  is asserted through real persisted rows.
- [ ] The eight retained generated database fakes are selective runtime or
  post-success transition fault seams, not ordinary success fixtures.
- [ ] No production, benchmark, corpus, Docker, or service-lifecycle file is
  changed, and nothing is pushed.
