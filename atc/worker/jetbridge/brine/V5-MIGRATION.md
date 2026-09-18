# V5 migration — in progress

## Current closure status — 2026-09-17

The complete Brine gate now passes: **589/589** (455 local / 134 live),
**2,035/2,471 = 82.355322%** Brine-only production statements in
`atc/worker/jetbridge`. Summed CLI runtime is **46m50.212s**, excluding
compilation and final coverage processing. All 589 recorder drains complete;
all 133 owned namespaces are independently absent. Source and daemon binary
identities stayed unchanged throughout the run; the complete roster matches
the previous full run. Both eviction and OOM-priority cases pass.

This is **not yet mergeable sign-off**:

- Three adapter suites and default/live vet pass. The additional daemon native
  suite has two failures on installed Go 1.25.12: `TestPeerFetch_RestrictiveModesNormalized`
  and `TestStreamIn_RestrictiveModesNormalized`. Both reproduce with the
  pre-migration daemon source and pass in a focused Go 1.26.1 comparison.
  That is not full validation of a toolchain upgrade; no upgrade or archive
  fix was applied. User direction on including a compatibility fix is pending.
- The historical OOM timeout remains unexplained. New diagnostics, focused
  controls and passing full runs are evidence, not a claimed behavioral fix.
- The required CI run is unperformed: no fly target is configured here, and
  publication of a matching immutable runner is awaiting approval.
- The migration is packaged for audit on `core-brine-audit-20260918`, based
  on `core` at `3c787e9c7`. The user requested this new remote review branch
  on 2026-09-18. This is an audit candidate, not merge approval; no runner
  image was published and no pipeline was deployed. The unrelated local
  `AGENTS.md` edit is excluded.

Passing evidence: `/tmp/brine-closure-final.3sJT9C/evidence.json`; raw run/profile:
`/tmp/brine-coverage.J25mSS`. Source snapshot SHA256:
`5c1f68029463f3eba6c435afa85c1c70b50ea190751183dfaa1af5909cdb3f5b`.
Native baseline comparison: `native-baseline/evidence.json` in that evidence
directory. The following checkpoints are historical context, not competing
completion claims.


## Audit handoff — 2026-09-18

Review branch: `core-brine-audit-20260918`. This snapshot includes the v5
execution-document adapter and protocol guards; consolidated local/live
features with real Kubernetes and daemon fixtures; native-test dispositions;
and the production fixes exposed by those behavioral checks (exec recovery
and exit status, cancellation, pod failure priority, sidecar logs, watch
refresh, and volume handling). Peer discovery is opt-in and its live fixture
uses namespace-local read access. CI source includes the pinned v5 runner
and stripped live-daemon build, but neither image nor pipeline is deployed.

The passing measurements above apply to the unchanged runtime/fixture source;
only handoff documentation changed afterward. Raw evidence paths under
`/tmp` refer to the originating machine and are not included in this Git
snapshot. See [README.md](README.md) for reproduction prerequisites and
[DISPOSITION-jetbridge.md](DISPOSITION-jetbridge.md) for replacement coverage.

Current open-work inventory: [REMAINING-DOUBLES.md](REMAINING-DOUBLES.md).

Latest completed checkpoint (2026-09-17): the four mirroring cases use real
independent peers with namespace-local discovery access, and the final
explicit Node status writer is removed. Four live controls, all 455 local
cases, five paired production faults, three adapter suites and default/live
vet pass. All 10 owned namespaces are independently absent. No scenarios
were added; one unused phrase was removed (589 scenarios / 1,071 definitions).
See the inventory above for evidence and the remaining closure gates.

Subsequent acceptance attempt (2026-09-17): all 455 local cases passed, but
the first live handoff could not reach its daemon. The already-failed run was
stopped cleanly after 41 live cases to retrieve buffered diagnostics; all 41
owned namespaces are independently absent. A longer focused reproduction
confirmed a kubelet eviction: the 103.86 MiB executable exceeded the pod's
96 MiB ephemeral-storage limit. The daemon exited cleanly, leaving Succeeded
status despite the Evicted event; a Failed-only check missed that distinction.

The same source built without debug symbols is 73.42 MiB and passes both short
and 75-second-lifetime handoff controls. CI now uses the stripping flags
already documented locally. The fixture derives its binary/emptyDir guard
from the existing storage budget and rejects oversized ELFs before creating
a namespace, reserving 8 MiB for data/logs. No quotas or timeouts increase.
The current guarded fixture passes the exact handoff, and all six reproduction
namespaces are independently absent. A fresh complete gate is still required.
Evidence: /tmp/brine-merge-gate.1NcEzo/cancelled-evidence.json and
daemon-budget/evidence.json in that directory.

Latest full attempt (2026-09-17): 455/455 local and 133/134 live cases pass
in 47m45.152s summed CLI time. The OOM-priority case passes; the sole failure
is a fixture that required observing a pod Running before its real eviction.
The fixture now uses the same-UID volume-limit eviction and matching kubelet
event, retaining the bounded writer and all production-facing assertions.
The delayed-observation regression and all four affected live cases pass
with the correction. All 133 full-run namespaces and eight investigation
namespaces are independently absent. Diagnostic coverage is 2,025/2,471
(81.950627%); the failed full run is not an acceptance pass. A fresh full gate,
unresolved historical OOM cause, CI and review packaging remain open.
Evidence: /tmp/brine-oom-closure.yt4GPA/full-evidence.json and eviction/ evidence
files in the same directory. See the inventory for details.

Previous full attempt (2026-09-16): 588/589 scenarios pass; one OOM-priority
fixture times out. Diagnostic coverage is 2,030/2,471 (82.1529745%), but the
full gate correctly fails. All 129 namespaces are independently absent, and
177 native / 20 root cases pass. See the current open-work inventory above
for evidence, the 47m07s CLI runtime and validated-but-not-deployed CI source.
Two focused OOM reproductions pass, including with coverage instrumentation;
the original timeout is unresolved. Failure diagnostics now retain the last
real pod state, while the deadline and behavioral assertions remain unchanged.

Previous checkpoint (2026-09-16): real-node recording and lookup.
Four recording/lookup cases now use the actual named node and production
artifact daemon. The task/get layout cases write through their real producer
pod mounts; output handles, database identity, exact raw bytes and request
patterns remain asserted. One live setup and one task/get refinement phrase
reuse the existing recording checks; no scenario is added. The owned-storage
file writer is shared with DaemonPlan, preserving its containment checks.

All nine paired production faults fail at the same assertions before and after;
normal/inactive controls pass. All 459 local scenarios, three adapter suites
and default/live vet checks pass. All 14 owned live namespaces are independently
absent. Inventory: 589 scenarios (459 local / 130 live), 1,072 definitions.
The address-removal diagnostic passes 37 of 41 cases; only the four mirroring
cases still depend on supplied Node addresses. Production storage/volume code
is unchanged. This is not a fresh full-suite coverage measurement.
Evidence: `/tmp/brine-recorded-live-t1WmxB/evidence.json`.

The subsequent mirroring checkpoint removes that shared status writer.
OOM-priority reliability, fresh full coverage, CI acceptance and review
packaging remain open; this is not a migration completion claim.

Starting JetBridge commit: `3c787e9c70602baf2279c5ff92ad32253edcf68b`.
Current Brine CLI/engine/Go runner pin:
`6bc5870169332d91815b5626ee890c703f316b09` (`main-20260910`).
The upstream source checkout is d1c75ea9; its two later CI/docs-only commits
do not change the installed runtime or Go pin. See the sidecar checkpoint.
The manifest conformance pin is 5; authoring remains 3. Earlier checkpoints
used `6c66f53848578bd5b2e54a2427970e087b2d2387` or
`1e9345da594d6a8e6d6cc9899a7a73de74d99ec9`; their measurements are historical,
not runs of the refreshed pin. See the engine-release checkpoint below.

## Goal: closure to a mergeable state (2026-09-17)

Finish the existing Brine v5 migration as a reviewable, mergeable change:
remove the remaining mirroring fixture's synthetic Node-status dependency,
address the OOM-priority failure without weakening assertions, and pass the
complete local/live suite with at least 50% Brine-only production statement
coverage of `atc/worker/jetbridge`. This is package coverage, not repository
coverage. No further broad migration or consolidation campaign is in scope.

### Measurable acceptance criteria

- [x] Migrate the four remaining mirroring cases to real independent daemon
  stores and peer discovery; remove the remaining test-side Node status
  writer. Preserve delivery, multi-output, refusal and cache assertions,
  with targeted paired production-fault checks for these replacements.
  Same-node peer pods must not claim physical node-failure isolation.
- [ ] Investigate and address the OOM-priority timeout, retaining the exact
  OOM-versus-crash-loop distinction. Include focused coverage-instrumented
  validation; passing retries alone do not establish a fix.
- [x] Obtain a fresh zero-exit full local/live coverage run on the final
  candidate: every selected scenario passes, coverage is >=50%, recorder
  drains complete, and owned-resource cleanup is independently verified.
  Record source identity, roster, coverage and elapsed time. The failed
  588/589 run and diagnostic 82.15% profile do not satisfy this criterion.
  Satisfied by the 2026-09-17 589/589 run and independently verified 82.355322%
  profile recorded above; any subsequent runtime/fixture change needs revalidation.
- [ ] Pass adapter/protocol tests, vocabulary guards, affected native tests
  and build/vet checks. Reuse existing evidence for unchanged code; recheck
  only affected behavioral distinctions. Do not remove legacy assertions
  without identified replacements and paired distinguishing-fault evidence.
- [ ] Align CI with an available immutable runner matching the v5 Go/CLI
  revision and explicit live prerequisites. Pass the repository-required
  `hack/ci-check.sh <candidate-ref>` checks against the exact review candidate.
- [ ] Review tracked and untracked changes, preserve unrelated user work,
  and package a reviewable candidate with a concise production/test change
  summary and validation record. Identify its ref and commit/push status;
  an uncommitted working tree is not the completed handoff.

### Execution boundaries

Work only on mirroring, OOM reliability, final validation and review packaging.
No new coverage target, broad no-doubles audit, suite redesign or runtime
optimization project unless it resolves a concrete merge blocker.

The user explicitly approved the opt-in daemon peer-discovery mode and
namespace-local EndpointSlice read grant on 2026-09-17. Do not modify shared Node
labels or shared/cluster RBAC. Confirm destination and authority before
publishing runner images, deploying pipeline changes or pushing a review
branch. Source validation alone is not deployed CI.


## Implemented, not yet a completion claim

- The adapter consumes Brine's execution document for both run and check.
  Selection, AST reading, directives and resource seeds come from the shared
  Go library; the old CLI feature/tag/line parser is removed.
- Registry documents use the protected protocol writer, not redirected fd 1.
  The real resource registry, SIGTERM drain and close-on-exec protection stay.
- Two helper tests use the updated in-process `Pipeline.Run` signature.
- Black-box tests build the real adapter and use the real CLI to produce
  documents. They exercise missing selection, registry capabilities/stdout,
  tag skips, body-line selection, deleted source files, check-roster echo,
  and unknown-directive refusal. Specimen steps are undefined deliberately;
  these are protocol tests, not production coverage.

Before migration a v5 CLI check returned `valid` with zero features/scenarios.
After migration the same task-command check reports one feature/seven scenarios.
The first full v5 run passed 577/577 (121521ms summed scenario time, not a
wall-clock benchmark); log: `/tmp/brine-v5-first-run.log`.

Adapter mutation evidence is recorded under
`/tmp/brine-v5-mutations.MpjZnq`. These challenge protocol plumbing and do NOT
replace the required production-behavior mutations.

## Verified checkpoint (2026-09-10)

- Nested Go tests and vet pass, including the three vocabulary guards and
  the new real-binary protocol tests (`/tmp/brine-v5-go-final.log`).
- Upstream `runner verify --contract 5` reports conformant: 20 event
  goldens, 3 protocol invocations, 8 document-selection cases and 8
  document-fidelity cases. Report: `/tmp/brine-v5-conformance.json`.
  The report explicitly does not drive AST goldens or registry-dependent
  adapter obligations. Holding/cancellation needs our own passing specimen.
- Fresh coverage passes with 577 scenarios and 1,838/2,368 production
  statements, **77.618243%**, against the 50% gate.
  Log: `/tmp/brine-v5-coverage.log`; profile:
  `/tmp/brine-coverage.6LrYdQ/coverage.out`. The script restored the normal
  adapter after measuring. This is package coverage, not repository coverage.
- All three isolated adapter mutations compile and fail the intended
  assertions: dropping the execution plan (both selection cases), writing the
  registry document to redirected stdout, and rebuilding the supplied roster.
- One production mutation has paired current evidence: change only
  `Volume.StreamIn`'s execution destination to `missing-container`.
  The exact retained Go leaf `Volume StreamIn streams exact input bytes to
  the intended tar destination with stream-in metadata` passes its control
  and fails the mutant on container identity. The Brine handoff outline passes
  5/5 in control; its raw/gzip/S2 delivery rows fail the mutant on the missing
  consumer container, while the two pre-existing fault rows still pass.
  Evidence: `go-{control,mutant}.{log,json,status}` and
  `handoff-line-{control,mutant}.{log,status}` in the mutation directory.
  This proves that one fault is detected; no deletion is justified by it alone.

### Tooling findings excluded from mutation evidence

`--set runner.binary=...` does **not** replace a manifest's runner binary.
Brine's `core/src/manifest.rs:apply_overrides` inserts the literal key into
`manifest.config`. The initial `brine-mutant.log` therefore ran the normal
adapter and is invalid mutation evidence. Corrected runs use separate
manifests with explicit binaries, exact feature copies and wrappers that
enter the real module before execution.

V5's brief reporter treats every scenario status other than `passed` as a
failure, including `skipped`, and the CLI lets that override an otherwise
successful exit (`cli/src/brief_reporter.rs` and `cli/src/main.rs`).
The tag-filtered control selected and passed the five handoff rows but
reported its 19 excluded rows as failures. That run is not a production
failure. Exact line selection omits the siblings and produced the valid
control/mutant pair above. Do not suppress the adapter's required skip events
to work around a reporter defect. JSONL is now a verified workaround for
filtered runs; see the reporter checkpoint below.

## Remaining work

- Extend production mutation validation with each behavioral change and
  remeasure final coverage; resolve the requested coverage denominator.
- Update CI's CLI/engine image coherently with the Go dependency; the current
  v9 image contains the old CLI and is not v5-ready. Do not silently reuse a
  mutable image tag, or claim CI passed from local runs.
- Audit/consolidate the behavioral vocabulary and prove every removal.
- Replace or explicitly resolve the test doubles below; renaming one or
  calling it a working implementation is not removal.
- Use JSONL for filtered runs until upstream fixes brief/tag-skip reporting.

## Starting double inventory (historical; see later checkpoints)

The prior migration rejected recording doubles but intentionally retained
some working doubles and focused Go mocks. That is not evidence of a
double-free suite. At the start of this migration, Brine sources included:

- `fixture.go`, `domain.go` and many consumers: client-go fake clientsets.
  `envtest.go` already provides a real API server/etcd resource, but no kubelet.
- `process.go`, `closing.go`, `container_extra.go`, `podwatch.go`,
  `podwatch_fidelity.go`:
  injected fake-client reactors for status, watcher and API failures.
- `local_executor.go` / `process.go`: host command execution and a specialized
  severing executor instead of production Kubernetes remote exec.
- `step_execution.go`: runtimetest container/process substitutes.
- `daemon.go`, `daemon_mtls.go`, `artifact_recording.go`, `closing.go`,
  `volume_streaming.go`, `worker.go`: HTTP fixture implementations, alongside
  already-real artifact-daemon processes. Each handler needs a semantic audit;
  a real listening socket alone does not make a production daemon.
- `artifact_recording.go`, `volume_streaming.go`, `worker.go`: StubVolume use,
  which needs classification by actual behavior rather than by name alone.

Later checkpoints record scenario consolidation and individually validated
legacy Go retirements. The goal remains active.

## Real registrar checkpoint (2026-09-10)

`steps/registrar.go` no longer imports or constructs a fake client. Its
existing nine scenarios now use the real kube-apiserver/etcd resource and
real PostgreSQL. Pods have valid specs and API-assigned names/UIDs; the fixture
does not fabricate Running status. The registrar counts objects regardless
of phase, so the setup language now says they `exist`. This does not claim
that envtest runs containers.

The two ownership setup definitions became one parameterized definition,
removing one duplicate definition. The feature diff changes only the two
setup sentences: every case, example value, assertion and requirement tag
remains. The requested namespace stays literal, preserving the independent
assertion that the worker name is derived from it.

Each scenario registers deletion of its own created pods, with UID
preconditions, zero grace and a bounded cleanup context. Namespaces live
inside the suite-owned control plane until that resource is disposed; no
external cluster or unrelated pod is targeted.

Before/after evidence: `/tmp/brine-v5-registrar.ZRJVao`. Both controls pass
9/9. All four production-only mutations compile and preserve the exact
failure attribution, seven scenario/fault pairs in total:

| Production fault | Same failing cases before and after |
|---|---|
| Remove the worker label selector | Container-count outline row 4 (unlabelled bystander) |
| Report zero containers | Container-count rows 2, 3 and 4 |
| Extend the lease to 24 hours | Registered-worker identity and heartbeat cases |
| Ignore resource-image overrides | Operator-override case |

The old binaries and feature text were captured before editing. Each run has
an explicit manifest/wrapper naming its binary; `--set runner.binary` is not
used. No old Go test or behavioral scenario was deleted.

### V5 protocol boundaries

The document-refusal check is now one table covering unknown directives,
wrong document kind, future envelope version and malformed JSON. All must
refuse with exit 2, an informative diagnostic, and empty protocol stdout.

`TestHoldPreservesThenDrainsRealResources` runs the real registrar with an API
pod and database. It observes a passed scenario and `hold_ready` before any
recorder drain, sends SIGTERM, and requires the one real pod disposer to
drain without a partial result and the adapter to exit 143.
Two isolated mutations are detected: ignore `--hold`, or omit registration
of the real pod disposer. Logs and overlays are in the same evidence directory.

### Gates and CI boundary

- Vet and all nested tests pass. The authoritative run uses the module-pinned
  `go run github.com/onsi/ginkgo/v2/ginkgo -r --timeout=5m`, serially, matching
  the revised CI task. Both native Go test packages ran, including vocabulary
  guards and all adapter protocol tests (`go-pinned.log`).
- Fresh full coverage run: **577/577 pass**, **1,838/2,368 production
  statements = 77.618243%**, target at least 50%.
  `coverage.log` records 120973ms summed scenario time (not a wall benchmark).
  Profile: `/tmp/brine-coverage.Vl7Shq/coverage.out`.
- `deploy/Dockerfile.test-runner` now reads its Brine revision from this
  module's replace directive, checks out that revision from the v5 branch,
  and builds CLI/engine with the lockfile. The extraction resolves to the
  installed source revision, `1e9345da594d`.
- The CI task compares the image's `/etc/brine-commit` receipt with the module
  pin before building or testing. Parsing the actual YAML/task and running
  its guard accepts the current receipt and rejects old/empty receipts
  (`verify-ci.go` in the evidence directory).
- **The image is not built or published, and pipeline image references are
  still v9.** Docker is unavailable here. The new guard intentionally rejects
  that old image. A new immutable tag must be built/published, updated at all
  pipeline references, and tested in CI before this migration is CI-ready.

One attempted plain nested `go test ./...` command was rejected by auto-review
under the repository's database-suite rule; it did not execute. Tests were
run using Ginkgo instead. The first local Ginkgo executable reported a
2.27.3/2.27.4 mismatch, so the final run uses the module-pinned CLI above.
No CI run, image push, pipeline deployment or source push is claimed.

## Real reaper checkpoint (2026-09-10)

All 20 pod-reaping cases now use real Kubernetes objects and PostgreSQL.
The reaper family no longer constructs a fake client, installs reactors, or
fabricates Running status. Each scenario has a generated namespace inside the
suite-owned control plane; that plane's disposer removes its complete state.
This still does not claim to execute pods.

Two equivalent pod-creation definitions became one sentence with explicit pod
name and container handle. Every scenario, example row, requirement tag and
assertion remains. The feature's stale claim that the race and readable-name
cases were absent was corrected.

The two canned API errors were replaced:

- Mid-sweep deletion: a second PostgreSQL connection locks the containers
  table. The fixture starts the production sweep, waits for PostgreSQL to
  confirm that this transaction blocks it, deletes the actual pod using its
  API-assigned UID, verifies NotFound, then releases the sweep. The reaper's
  subsequent delete receives the real API's NotFound response.
- Listing failure: the reaper gets a real client pointed at a closed local
  TCP endpoint. Connection refusal comes from the kernel. The observation
  client still reaches the owned API server.

Race setup failures are separate from production errors, and cannot satisfy
the expected-error assertion. The transaction, competing connection and
goroutine are bounded and cleaned up on error. An initial control timed out
because postgresrunner limits a pool to one connection; the competitor now
owns a separate pool. That unsuccessful run is preserved, not counted as
mutation evidence.

Evidence: `/tmp/brine-v5-reaper.TilM1J`. Before and after controls each pass
20/20. Five production-only mutations compile and fail the same scenario
and assertion on both sides:

| Fault | Same failing scenario |
|---|---|
| Remove worker label selector | Another worker's pods in the same namespace are left alone |
| Omit retained pods from the active report | A pod kept for a running build keeps its container row too |
| Delete by container handle rather than pod name | A destroyed container's readable pod is deleted by its pod name |
| Treat delete NotFound as fatal | A pod deleted by someone else mid-sweep does not fail the reaper |
| Suppress pod-list errors | A reaper that cannot reach the cluster says so |

Each variant has an explicit manifest, adapter binary, log and exit receipt.
Production `reaper.go` matches the captured original byte-for-byte.
No legacy Go test was deleted.

Fresh final gates for this checkpoint:

- `go vet ./...` passes.
- Module-pinned Ginkgo passes both native packages, including all vocabulary
  guards and adapter protocol tests (`go-pinned.log`).
- Full Brine run passes **577/577**, with **1,838/2,368 = 77.618243%**
  production JetBridge statement coverage against the 50% gate. This is
  unchanged coverage, not an increase or repository-wide measurement.
  `coverage.log` records 122008ms summed scenario time, not wall time.
- Profile: `/tmp/brine-coverage.u3Xk0F/coverage.out`, SHA-256
  `4a72c5309f60c3e668e195d3eacf8854894cd87d3f7b785cdcad5603d673e124`.

## V5 filtered-run reporter checkpoint

Use `brine run --tags @TAG --format jsonl` for filtered runs. Tags match
literally, including `@`; omitting it selects nothing and exits 2.

A two-scenario probe using actual configuration steps confirms:

- Brief: selected scenario passes, excluded scenario is labelled FAIL,
  exit 1.
- JSONL: one passed, one skipped, no excluded step events, exit 0.
- JSONL negative control: changing the selected namespace assertion to a
  wrong value produces one failed/one skipped and exit 1.

Evidence is in `reporter/` and `reporter-failure/` under the reaper evidence
directory. The upstream reporter bug remains; this is a verified usage
workaround, not an adapter semantic change or upstream fix. These probes
add no scenarios to the repository's behavioral suite.

The overall goal remains active: other fake-backed families and CI image
publication/validation are unfinished.

## Real node-resolution checkpoint (2026-09-10)

The nine node-resolution cases in `step-integration.feature` now use the real
Kubernetes Nodes API. Their two fake-client constructors are gone. Nodes are
created through the API, addresses are published through UpdateStatus, and
each recorder disposes only the Node UID that scenario created. Empty-node
setup verifies the real inventory is empty, so leaked objects cannot silently
become another scenario's fixtures. Literal IP-shaped node names remain real
API objects in the poisoning case; they were not replaced with ordinary names.

The two IP-refusal definitions became one check retaining the stronger
predicate: an ErrNodeNameIsIP error and no returned addresses. No scenario,
example row or legacy Go test was removed. A new cache transition replaces
the existing positive scenario's unchanged-node reads, so the net number of
step definitions does not grow.

### A measured cache gap, now closed

Disabling the cache-hit branch in production `NodeIPResolver.Resolve` passes
all nine original Brine cases AND all four retained Go tests. Both suites
previously read an unchanged node twice, which proved repeatability but not
caching.

The same existing Brine scenario now resolves once, deletes the actual Node
with a UID precondition, verifies the API returns NotFound, and resolves
again through the same production resolver. Both returned addresses must
equal the internal IP, and exactly two must be present. The cache-bypass
mutation now fails this scenario with the API's genuine NotFound error.
An isolated fixture mutation that omits the second read also fails, saying
`expected two answers`. That prevents a one-read false green.

### Before/after and retained-Go evidence

Evidence directory: `/tmp/brine-v5-nodes.ScmHEu`. Both Brine controls pass
9/9. Every production variant compiles; the four previously detected faults
retain exactly the same failing scenario identities after migration.

| Production fault | Retained Go result | Brine before → after |
|---|---|---|
| Prefer ExternalIP | Resolve and NoInternalIP tests fail | same two cases fail |
| Hide missing-node error | NodeNotFound test fails | same missing-node case fails |
| Accept IP-shaped names | IPShapedInputRejected test/subcases fail | same six IP-refusal cases fail |
| Corrupt cached address | Resolve test fails | same internal-address case fails |
| Bypass cache | all four tests pass | all pass → cache case fails |

The Go runs use the module-pinned Ginkgo command with
`-test.run='^TestNodeIPResolver_' -test.v`, so logs identify actual native
tests and subtests, not a purported Ginkgo Describe selected by a slash.
`go-*.log`, `before-*.log` and `after-*.log` retain those identities, with
explicit binary manifests and exit-status receipts. Production resolver
source matches its captured original byte-for-byte.

The call-record-only assertion in the retained Go IP-input test remains an
explicit unresolved policy boundary. This checkpoint does not claim that
every integration fixture, or that retained Go file, is free of doubles.

### Final gates for the node checkpoint

- Vet passes; module-pinned Ginkgo passes both native test packages, including
  every vocabulary guard and adapter protocol test (`go-pinned.log`).
- Full Brine coverage run passes **577/577** and **1,838/2,368 production
  statements = 77.618243%**, above the 50% gate (`coverage.log`).
  Statement coverage is unchanged; the improvement is the previously
  undetected production cache fault. No repository-wide percentage is claimed.
- The 122123ms in that report is summed scenario time, not wall-clock runtime.
- Profile: `/tmp/brine-coverage.06pmhM/coverage.out`, SHA-256
  `e0782758e8f3027dd5f4583ebfa1d3daedc9858cd5dbea7fd49ed08001f272b5`.
- The coverage command exited successfully and restored the ordinary adapter,
  SHA-256 `85c506cf18ce9017b50ad36e296149cdf81415acee1c216a5273f1650ddf275c`.

The node changes remain local and uncommitted on `core`. Other fake-backed
families and the previously documented CI-image boundary remain unfinished;
the goal is not complete.

## Real daemon-discovery checkpoint (2026-09-10)

The ATC-side Kubernetes topology for all 25 `artifact-daemon.feature` cases
now comes from the real API, not a fake clientset. The feature is unchanged
byte-for-byte: no scenario, step definition or assertion was added or removed.
Reading and probing steps declare their real-cluster resource explicitly.

Each scenario gets an owned namespace. It publishes an IPv4 EndpointSlice
with the actual service label and creates any producer Nodes through the
real API/status subresource. Nodes and slices have bounded, UID-protected
recorder cleanup; the suite-owned control plane disposes the namespaces.
Node creation/status publication/cleanup is shared with the node-resolution
family in `real_discovery.go` rather than copied.

### A validation difference hidden by the fake

A standalone real-API probe rejected `127.0.0.1` in an EndpointSlice as a
loopback address and accepted the container's assigned private IPv4 address.
Evidence: `/tmp/brine-v5-discovery.Y6zftF/endpoint-validation.log`.

The real daemon already listens on all local interfaces. The fixture now
advertises an actual non-loopback local IPv4 address and sets AddressType,
instead of bypassing API validation or inventing a pod address. This tier
therefore needs a usable non-loopback IPv4 interface. A host with HTTP proxy
settings must exempt that local address from proxying.

The existing resolve-only HTTP stand-in is STILL a stand-in. Its listener is
now bound to an API-valid local address, but no response semantics changed.
Replacing daemon-side peer discovery remains unfinished; publishing its
ATC-side EndpointSlice through a real API does not make that server real.

### Mutation and shared-helper evidence

Evidence directory: `/tmp/brine-v5-discovery.Y6zftF`. Controls pass 25/25
before and after. All four production mutations compile and fail the same
scenario identities and assertion behaviors on both sides, nine pairs:

| Fault | Same failing cases |
|---|---|
| Wrong resource-cache probe route | Cached-resource delivery; durable capability learned from a miss |
| Wrong step-artifact probe route | Mirrored copy after producer departure; locator wiped; live peer beats unreachable peer |
| Suppress durable capability | Durable capability learned from a miss |
| Wrong peer-fetch route | The same three peer-delivery cases |

Each variant has an explicit adapter manifest/binary and JSONL log with
scenario outcomes and an exit receipt. `daemon_client.go` and
`volume_daemonset.go` match the captured production originals byte-for-byte.
Peer-fetch errors differ only in the actual address (loopback before,
assigned local IPv4 after), and remain genuine daemon 404 responses.

The shared Node helper was rechecked separately: all nine resolver controls
pass, and the production cache-bypass mutation still fails only the intended
cached-resolution case (`node-control.log`, `node-bypass-cache.log`).
No legacy Go test was deleted.

### Final gates for daemon discovery

- Vet and both module-pinned native Ginkgo packages pass, including all
  vocabulary and adapter protocol checks (`go-pinned.log`).
- Full Brine suite: **577/577 pass**, **1,838/2,368 = 77.618243%**
  production JetBridge statement coverage against the 50% gate. Coverage is
  unchanged, not a repository-wide measurement (`coverage.log`).
- The report's 122144ms is summed scenario time, not wall-clock runtime.
- Profile: `/tmp/brine-coverage.uHKgCw/coverage.out`; SHA-256
  `fa4a2d966cc0323df8e29f4b73ff2fb481fb32cd83197a0a6dd5b78283c9f23c`.
- The command exited successfully and restored the ordinary adapter:
  `bb7539dbcba1a208718dbbaff661788f55767c443845547ae978e624f157867a`.

This is another local checkpoint, not goal completion or a CI-ready claim.
The resolve-only HTTP stand-in, other fake-backed families, and the CI-image
publication/validation boundary remain. The real daemon's existing TLS flags
are a promising next replacement for the separate mTLS HTTP fixture.

## Real mTLS checkpoint (2026-09-10)

The three mTLS cases in `daemon-mtls.feature` now start the production
`artifact-daemon` binary with real CA/server/client certificate files. The
map-backed HTTP artifact handler and its TLS listener are removed. Artifacts
are real files registered through the daemon's actual `/register` route;
the production file-serving path returns their exact bytes. These cases do
not claim to test directory tar generation, which has separate scenarios.

HTTP and HTTPS fixtures now share the daemon launcher and one real
registration helper. HTTPS readiness uses a separately configured verifying
client, not the ATC TLS helper under test, and requires `/healthz` to answer
200. A TLS listener's plaintext HTTP 400 is no longer sufficient readiness.
Missing ATC credentials are named beneath the scenario's owned temporary
directory, rather than a shared fixed `/tmp/brine-absent` path.

### Authentication fidelity

The previous comments claimed a `--client-auth=require` deployment flag.
The real implementation uses `VerifyClientCertIfGiven` at the TLS layer
and client-certificate middleware on artifact routes, leaving health probes
exempt. No production authentication policy was changed.

The existing first case's authenticated-only Given is now checked with an
actual unauthenticated read of a held artifact, expecting 401. Unlike the
old second fixture, the real daemon requires client authentication in the
service-name case too. Its certificate still omits the dial IP, so removing
the ATC's ServerName still uniquely breaks that case.

Every scenario name, step sentence and expected value is retained: three
mTLS cases plus the untouched warm-ownership case, with no new cases and no
legacy Go test deletion. The warm-ownership HTTP virtual-host implementation
and fake clientset remain; this file is not yet double-free.

### Mutation evidence

Evidence directory: `/tmp/brine-v5-mtls.xhFIu5`. The pre-edit sources,
feature text, independently built binaries, explicit manifests/wrappers,
production overlays, JSONL logs and exit statuses are retained there.
Both controls pass all four cases.

| Production fault | Before | After |
|---|---|---|
| Omit the ATC client certificate | Authenticated-read case fails | Both successful-read cases fail with real HTTP 401 |
| Omit the ATC ServerName | Service-name case fails | Same case fails on missing IP SAN |
| Omit the trusted CA | Both successful-read cases fail | Same cases fail on unknown authority |
| Insecure fallback when certificate loading fails | Missing-certificate case fails because bytes arrive | Same case fails because generic HTTP 401 does not name the certificate |
| Remove real daemon route authentication | All four cases pass: the stand-in never exercises it | Authenticated-only premise fails on an actual unauthenticated HTTP 200 |

All four earlier production faults remain detected (five original
scenario/fault pairs). The real route policy additionally makes the second
read sensitive to a missing client certificate. The fifth mutation closes
a previously unmeasured production-authentication gap; it is not paired
red evidence for deleting a legacy Go test.

The route-authentication overlay reaches the real daemon's child `go build`
via the wrapper's GOFLAGS; the ATC TLS mutations are compiled into separate
adapter binaries. Production source files remain unchanged. Initial CLI
argument refusals ran no scenarios and are excluded from the evidence.

### Final gates

- Vet and both module-pinned Ginkgo native test packages pass, including
  adapter protocol/resource tests, daemon boot-failure detection and all
  three vocabulary guards (`vet.log`, `go-pinned.log`).
- Full Brine suite: **577/577 pass**, **1,838/2,368 = 77.618243%**
  production JetBridge statement coverage, against the 50% gate
  (`coverage.log`). This is unchanged package coverage, not repository
  coverage or a claim of a percentage increase.
- Summed scenario time is 122385ms, not wall-clock runtime.
- Profile: `/tmp/brine-coverage.V4PIO9/coverage.out`; SHA-256
  `7a0bd2b80f8637c276255cea27664516864922e69570d16cbd7dc8f66f30439f`.
- The coverage command completed successfully and restored the normal
  adapter. No suite process was restarted while its handle was still live.

The goal remains active and changes remain local/uncommitted on `core`.
Remaining doubles, the coverage-denominator question, and CI image
publication/validation are not resolved by this checkpoint.

The next fixture investigation also disproved old comments claiming that
daemon peer discovery lacks a kubeconfig option: current main supports
`--kubeconfig`, `--listen-address` and a filesystem durable store. Those
stale notes are corrected. Real peer-resolve and warm-ownership replacements
still need multiple reachable, API-valid daemon addresses; envtest does not
run pods, and replacing the existing successful-peer premise with a daemon
that has no peers would silently lose its discriminator.

## Real worker API checkpoint (2026-09-10)

`steps/worker.go` no longer constructs or imports a client-go fake. Its
`WorkerReady` uses the suite-owned real Kubernetes API and the existing
PostgreSQL database, with an API-generated namespace per scenario. The
namespace remains inside the owned control plane until suite disposal.

The fixture now creates valid pod specs, uses API-assigned resource versions,
and writes phase/conditions through `UpdateStatus`. Completing an intercept
pod updates annotations and status separately. Reaping uses zero-grace,
UID-protected deletion and verifies that a subsequent Get returns NotFound.
These are API state transitions, not evidence of kubelet/container execution.

Brine disposes all pods in the scenario's private namespace, including those
created by production `Container.Run`; each deletion names the observed UID.
The existing HTTP artifact stand-in now binds the host's real non-loopback
address and publishes an IPv4 EndpointSlice accepted by the real API.
Its four setup definitions pass their recorder through to a scenario-scoped
listener disposer, replacing the previous deliberate process-lifetime leak.
The duplicate manual host/port parser is replaced by the existing helper.

`features/worker.feature` is byte-identical to its pre-edit snapshot:
**31 cases**, with every step, expected value and requirement tag retained.
No scenario, step definition or legacy Go test was deleted.

### Before/after production mutations

Evidence: `/tmp/brine-v5-worker.1KUHVZ`. Both controls pass 31/31. All four
production mutants build and produce the same case-level failures before
and after:

| Production fault | Failing cases in both versions |
|---|---|
| Discard lookup container metadata | The three intercept cases |
| Omit failed-container cleanup after Created fails | Container left for the collector |
| Use the wrong resource-cache key | Readable cache hit; cache hit surviving wrapping |
| Ignore the exited-pod interception guard | Interception of an exited pod |

The seven matching case/fault detections are not all equally strong:
the lost-metadata/gone-pod pair stops while reading missing supervisor state,
and the wrong-key/wrapped-cache pair stops at the action's missing-volume
precondition. The other five reach behavioral Then checks. All four faults
therefore have direct assertion evidence, but the two precondition failures
are not credited as final-assertion equivalence or permission to delete Go
tests. The exited-pod guard mutation fails the existing reason assertion:
without the guard, execution reaches an invalid replacement-pod attempt
instead of reporting that the original pod has already exited.

Production `worker.go` and `container.go` are unchanged; their hashes match
the pre-edit snapshots. Overlays and separately built binaries isolate each
mutation; explicit manifests/wrappers select them. Logs are JSONL and each
run's exit status is retained.

### Final gates and boundaries

- Vet and both module-pinned Ginkgo native packages pass, including adapter
  protocol/resource checks and the three vocabulary guards
  (`vet.log`, `go-pinned.log`).
- Full Brine: **577/577 pass**, **1,838/2,368 = 77.618243%** production
  JetBridge statement coverage, against the 50% gate (`coverage.log`).
  The percentage is unchanged and is not repository-wide coverage.
- Summed scenario time is 122007ms, not a wall-clock benchmark.
- Profile: `/tmp/brine-coverage.GQrUGK/coverage.out`; SHA-256
  `d3fbd4b63b1f7980951ed92ef276074156b4d4a9293650220e888becfc98e427`.
- The coverage command completed and restored the ordinary adapter:
  `b549bc9465fc752b8d57ffd97d4f91a85f6ce0108c5ff6cb78b28bce3b0318c7`.

This removes the worker family's API fake, not all its doubles. Its HTTP
artifact handler, local execution substitutes, `stubResourceCache`, and
database fault decorators remain explicitly unmigrated. The next database
targets are concrete: the `containers.state` update, volume state transition,
and artifact-initialization transaction. The decorators' only source
consumers are in `steps/worker.go`; replacing them must still preserve the
real rows left behind on each failure.

Peer-resolve work was investigated but not replaced by a weaker local-only
case. The host advertises only `10.42.0.154`, and has no `ip`,
`slirp4netns` or `pasta` executable. An isolated user/network namespace
creation probe succeeds, however, so a dedicated real-network fixture is a
remaining avenue—not a proven environment blocker. A host-interface change
or a fake address/HTTP routing trick was not used.

The full goal remains active. The existing coverage-scope question, remaining
doubles, and CI image publication/validation remain unresolved. Changes are
local and uncommitted on `core`.

## Real worker database failures and cache objects (2026-09-10)

The worker family's database collaborators are now production implementations
throughout. Removed eight test-double types: `stubResourceCache` and the
seven decorator types implementing container-created, volume-created and
artifact-initialization failures. The `ContainerFault` flag and rebuild-time
database wrapping are gone.

The three failure Givens share `workerDatabaseRefusal`, which installs an
ordinary PostgreSQL CHECK constraint in the scenario's private database:

| Rejected operation | Constraint |
|---|---|
| Container becomes created | `brine_container_creation`: containers.state cannot be created |
| Volume becomes created | `brine_volume_creation`: volumes.state cannot be created |
| Artifact is linked to its volume | `brine_artifact_initialization`: volumes.worker_artifact_id stays NULL |

No method is substituted, no PostgreSQL error is constructed in Go, and no
trigger raises a canned message. The actual db.Worker and VolumeRepository
execute their SQL; PostgreSQL rejects the mutation. Its own server logs in
`after-control.log` identify all three constraint violations. Database
disposal removes the constraints with the scenario's database; the template
and other scenarios are untouched.

The three error assertions now name those real constraints instead of
fabricated messages such as `db connection lost`. The state assertions
remain: rejected containers are failed, rejected volumes remain creating,
and failed artifact initialization leaves no artifact row.

### Real legacy resource caches

The three cache scenarios now create a real build, resource configuration,
resource cache and cache-use row through production factories. They retain
their existing legacy `rc-42` contract: the fixture uses its private
database sequence to assign the requested ID, clears durable_key to model a
pre-durable-key row, checks that exactly the intended row changed, and reloads
the object with FindResourceCacheByID. No database interface is implemented
by a test stub. The feature now documents that these are legacy-key cases,
not content-addressed-key coverage.

Every scenario and step pattern remains: **31 worker cases**, with only the
three expected error strings and explanatory comments changed. No scenario,
step definition or legacy Go test was deleted.

### Mutation evidence

Evidence directory: `/tmp/brine-v5-worker-db.PGLKxl`. Both controls pass
31/31. Source snapshots, explicit manifests/wrappers, separate binaries,
overlays, JSONL logs and exit statuses are retained there. The final after
runs include both the constraint and persisted-cache replacements.

| Production fault | Before | After |
|---|---|---|
| Omit failed-container cleanup | Collector case sees creating instead of failed | Same assertion fails |
| Mark a rejected volume failed | Volume case sees failed instead of creating | Same assertion fails |
| Hide artifact initialization error | Initialization case sees success | Same assertion fails |
| Use the wrong resource-cache key | Two cache cases fail | Same cases fail |
| Commit the artifact before linking its volume | All 31 cases pass | Initialization case finds one orphan artifact |

The wrong-key/wrapped-cache case still stops at the action's missing-volume
precondition, not its final assertion; the direct cache-hit case supplies
the assertion-level discriminator. That precondition failure is not evidence
for deleting a legacy Go test.

The early-commit mutation exposes a genuine gap in the old decorator:
it returned before InitializeArtifact executed, so it could not test the
transaction. The real constraint rejects the volume link after the artifact
insert; correct code rolls the insert back. Committing that insert early
leaves an observable row and fails the unchanged `no artifact is recorded`
assertion. This is added behavioral protection without another scenario.
Production `atc/worker/jetbridge/worker.go` and `atc/db/volume.go` remain
byte-identical to their snapshots; only isolated overlays are mutated.

### Final gates

- Vet and both module-pinned Ginkgo native packages pass, including adapter
  protocol/resource tests and all vocabulary guards
  (`vet.log`, `go-pinned.log`).
- Full Brine: **577/577 pass**, **1,838/2,368 = 77.618243%** production
  JetBridge statement coverage, target at least 50% (`coverage.log`).
  This remains package coverage, not repository coverage; the stronger
  transaction assertion is not presented as a percentage increase.
- Summed scenario time: 123136ms, not wall-clock runtime.
- Profile: `/tmp/brine-coverage.9ObtH8/coverage.out`; SHA-256
  `cb1cf9bb72a9a60c50b3314bbdc94788cbf684a18e140e73d7487ef1692975b8`.
- The completed coverage command restored the normal adapter:
  `3a5bc11f6ee9b41db7f474c29bafb54c548a2e1d99038227972598a3469cf7b4`.

The worker's HTTP artifact handler and local executor substitutes remain.
Replacing its wildcard HTTP behavior requires storing real files under the
scenario's actual output keys, not making a production daemon answer any
key. Other fixture families also retain doubles. The full goal, coverage
scope question and CI-image publication/validation work remain open; these
changes are local and uncommitted on `core`.

## Worker production-daemon checkpoint (2026-09-10)

Evidence: `/tmp/brine-v5-worker-http.9PIUfz`.

The worker fixture no longer has an HTTP handler, `httptest.Server`, wildcard
response, or hand-written artifact/cache URL routing. All four daemon setup
steps start the production artifact-daemon, write real files beneath its owned
storage root, and register explicit keys through the production `POST /register`
route. The real EndpointSlice advertises the host's non-loopback IPv4 address;
the production daemon listens on that address as well as its readiness loopback.
Brine disposes each daemon and its owned storage root at scenario end.

For the newly-created artifact case, setup cannot know the generated key in
advance. After creation it reads the handle from the persisted volume row by
the returned database artifact ID, then registers that file. It deliberately
does not derive the expected key from the returned runtime volume's key.
Explicit-key/cache cases register only the keys named in their Given. An empty
daemon stays empty. Files carry raw bytes, not a claimed tar archive.

All 31 worker scenarios, step patterns, example values and assertions are
byte-identical to the pre-change feature snapshot. No legacy Go test or
behavioral scenario was deleted. The runtime production worker is also
byte-identical to its snapshot; faults are isolated Go build overlays.

### Paired production mutation evidence

Both controls pass 31/31. Every mutant compiles. Explicit before/after manifests
name each distinct binary; `matrix.json` records scenario names and failures.

| Fault | Before / after failing cases |
|---|---|
| Constant key when creating an artifact volume | 1 / 2: the existing two-artifact identity failure remains; the daemon-read scenario now also fails with a real artifact miss |
| Wrong key when wrapping a volume as an artifact | 4 / 4: cache-hit wrapping plus all three output-lifetime rows |
| Return the original volume instead of wrapping | 3 / 3: all output-lifetime rows fail their reads |
| Wrong key for daemon cache lookup | 2 / 2: direct cache hit and downstream wrapping |

This preserves all ten existing scenario/fault pairs and adds one detection:
the old wildcard handler let the daemon-read scenario pass with the wrong key.
The two-artifact identity scenario already caught that mutant, so this is
stronger observable coverage within an existing case, not a previously
undetected suite-wide fault. In the wrong-cache-key wrapping case the failure
is the action's `no volume came back` precondition, not a reached Then; it is
not standalone evidence for deleting a legacy assertion.

Vet and both module-pinned Ginkgo packages pass (29.306s), including the
vocabulary and adapter protocol/resource checks.

Full gate: **577/577 pass**, **1,838/2,368 = 77.618243%** production
JetBridge statement coverage against the 50% requirement. Summed scenario
time is 124118ms, not wall-clock runtime. Coverage did not increase.
Log: `/tmp/brine-v5-worker-http.9PIUfz/coverage.log`.
Profile: `/tmp/brine-coverage.9XcEhi/coverage.out`, SHA-256
`abd504b6bb228a6b85e10e732721456d80c48dfe2f2e5e9b01f576c9fc0e07e4`.
The coverage command completed with exit 0 and restored the normal adapter:
`2be9c7e9cd9d3f7000631170f368248ab6de75f2461a9e8b9e67ccd767eafb96`.
Final worker fixture SHA-256:
`a78d3c2cae3ae1bc36ad4d66ce72dda64fd383f5747be288c1a8cb6ba1b72d1c`.

### Remaining boundary

The worker's `localExecutor` still substitutes for Kubernetes remote exec.
Its `NewStubVolume` is the production placeholder type (also returned by
`Worker.newVolumeForMount` with no executor), not a newly written test fake;
its production reachability and policy treatment must be kept distinct from
the executor substitute. Neither this checkpoint nor passing HTTP reads
proves real kubelet execution. Other fixture families retain HTTP stand-ins
and fake clientsets. CI image publication and validation remain unfinished.

## Worker real producer-exec checkpoint (2026-09-10)

Evidence: `/tmp/brine-v5-worker-exec.rVKppM`.

The artifact-lifetime setup no longer flips `ProducerReaped` or injects
`localExecutor{failure: ...}`. It creates a valid pod in the scenario-owned
namespace, deletes that observed UID with zero grace, and requires a real
API NotFound response. Its mounted volume uses the production
`jetbridge.NewSPDYExecutor`, configured with the actual envtest REST config.
A fallback read therefore reaches the Kubernetes exec endpoint and receives
`exec stream: pods "producer-pod" not found`.

The existing intercept deletion step and the producer deletion step now share
one `reapPod` helper, including the UID precondition and absence check.
The real-cluster resource retains the REST config returned by envtest rather
than reconstructing client credentials in the fixture.

All 31 scenarios remain. The only behavioral feature change replaces the canned
error phrase in the no-daemon fallback case with checks for the actual pod name
and `not found`. The introductory prose now explicitly distinguishes the real
producer API path from the still-host-local intercept executor. The positive
rows still require the identical bytes after the
pod is deleted. The placeholder row still uses the production placeholder
volume, and no legacy Go test is deleted.

### Paired production mutation evidence

Both controls pass 31/31; every mutation compiles. Explicit manifests select
the matching before/after binaries and feature snapshots. `matrix.json`
records exact scenario attribution.

| Fault | Before / after failing cases |
|---|---|
| Bypass artifact wrapping | 3 / 3: every output-lifetime row; mounted rows now fail with the real API's missing-pod response |
| Wrong wrapped artifact key | 4 / 4: cache wrapping and all output-lifetime rows |
| Suppress errors from production SPDY exec | 0 / 1: the no-daemon fallback now detects an incorrectly successful empty read |

The last mutant changes only production `SPDYExecutor.ExecInPod` to return
nil for a failed stream. All old worker cases passed because their injected
executor never entered that code. The migrated negative-read case fails
`expected the read to fail, it returned ""`. This closes a demonstrated
coverage gap without adding a scenario. Production worker and executor
sources are byte-identical to their saved originals; mutations use overlays.

Vet and both module-pinned Ginkgo packages pass (29.623s), including the
vocabulary and real adapter protocol/resource checks.

Full gate: **577/577 pass**, **1,867/2,368 = 78.842905%** production
JetBridge statement coverage against the 50% requirement. The previous run
covered 1,838 statements. Profile comparison identifies 26 newly covered
statements in production `executor.go`, plus three in the daemon client's
unreachable-probe branch. The latter are incidental run coverage, not evidence
that this executor change adds a new deterministic daemon test.

Summed scenario time: 124046ms, not wall-clock runtime. Log:
`/tmp/brine-v5-worker-exec.rVKppM/coverage.log`.
Profile: `/tmp/brine-coverage.nKsNFA/coverage.out`, SHA-256
`aa424144c9d5d0a072f23815eea3cfe526edff369b693ad90cd6a6acfb0d0592`.
The command exited 0 and restored the normal adapter:
`3cdccd3cffa65ade655fb8396fc8026634d7cda2eb02a63522e41d2a257c9498`.
Final worker fixture SHA-256:
`dc79cf6ffc4b52181f53240eed93a9fbdeac4499be792e3a47db3bbc94a1e048`;
real-cluster fixture:
`794ae17456399a8494cab74e490c7d23e8e49bcb7ed025c9d8a17887b2d4e783`.

The worker's intercept setup still uses a host-local executor, and other
fixture families still contain fake clients and HTTP substitutes. No kubelet
runs in envtest: this checkpoint proves real API deletion and exec rejection,
not successful execution in a Kubernetes container. CI image publication and
validation, and the full no-doubles objective, remain unfinished.

## Volume-stream error checkpoint (2026-09-10)

Evidence: `/tmp/brine-v5-volume-errors.ocVFFa`.

The shared volume-error Given no longer injects
`localExecutor{failure: "exec failed: pod terminated"}`. It acquires the
real-cluster resource, creates a unique scenario namespace, verifies that the
named pod is absent, and constructs the production SPDY executor from the
cluster's actual REST config. Reads and writes reach the real Kubernetes exec
endpoint. The namespace belongs to the suite-owned control plane and is removed
when that resource stops; no external cluster or existing pod is touched.

The two duplicate cluster-failure scenarios are now one outline with two rows.
The read row uses the existing `volume "broken" is read from "."` action;
the write row uses the existing `a file is put into volume "broken"` action.
No alias action definitions were added. Both assert a real `exec stream:`
failure containing `not found`, rather than the old fabricated `exec failed`
message. The feature still expands to 24 cases.

### Mutation mapping

The original reader scenario maps to outline row 1; the original writer maps
to row 2. All other scenario identities and assertions are unchanged.
Before/after manifests point at separately compiled binaries and exact feature
snapshots. Only production code is mutated, through Go build overlays.

- Suppress `Volume.StreamIn`'s exec error: the writer case fails before and
  row 2 fails after. The existing handoff outline's write-refusal row also
  fails before and after because it detects the wrong failure stage.
- Close `Volume.StreamOut`'s pipe without its exec error: the reader case
  fails before and row 1 fails after.
- Suppress errors in production `SPDYExecutor.ExecInPod`: all 24 old cases
  pass; both new cluster-failure rows reject the incorrectly successful read
  or write. This is added error-propagation protection, not an added case.

Both controls pass 24/24. Vet and both module-pinned Ginkgo packages pass
(29.293s), including all vocabulary and real adapter protocol/resource tests.
All mutation processes completed: swallow-write fails 2/24 before and after,
swallow-read 1/24 before and after, and hide-exec-error 0/24 before versus 2/24
after. The exact failures and totals are in `matrix.json`.

Full gate: **577/577 pass**, **1,865/2,368 = 78.758446%** production
JetBridge statement coverage, target at least 50%. Profile comparison with
the preceding run adds one executor command-formatting statement and does
not hit the three incidental daemon-unreachable probe statements that ran
previously. This is not presented as a percentage increase.

Summed scenario time: 124321ms, not wall-clock runtime.
Log: `/tmp/brine-v5-volume-errors.ocVFFa/coverage.log`.
Profile: `/tmp/brine-coverage.JSnOqU/coverage.out`, SHA-256
`0caed3ea75af14a1d818496bc735e1269be15d5f11057cef7e76aa48b538100c`.
The coverage command exited 0 and restored the normal adapter:
`dc1024465ddd3743f21428bed2b51917ffe73c51a8be85489ab032f6f4550567`.
Final volume-stream fixture SHA-256:
`27cd7680f0c91d557f65a0d9878b75328be20822742ccecbd37d627eeb1a1dde`.
Production volume and executor sources match their saved snapshots exactly;
no mutation was applied to the worktree.

This does not complete the no-doubles goal. Successful direct-volume transfers
still use the local executor; daemon-peer scenarios in this feature retain
HTTP stand-ins and fake clientsets. The outline also still starts with the
old, unused "real" host-volume Given before refining the broken volume; removing
that unnecessary setup is a follow-up simplification, not a claim made here.
No legacy Go test was deleted, and no successful kubelet execution is implied.

## Failure-only volume setup checkpoint (2026-09-10)

Evidence: `/tmp/brine-v5-volume-setup.J9RXd4`.

Four failure cases no longer construct an unused host-backed volume named
`real`. The placeholder and cluster-error Givens now start from Brine's Empty
state and create only the volume under test. They do not attach a host
workspace or construct a local executor. A shared `newVolumeSet` initializer
owns the empty map/context setup; only the mounted-volume Given attaches a
workspace.

The two placeholder scenarios are consolidated into one @VT-05 outline with
read/write rows, using the existing actions and the unchanged `no executor`
assertion. Together with the cluster-error outline, four behaviors now have
two setup/assertion declarations. No action alias was added, no example was
removed, and no legacy Go test was deleted. This removes the unused setup
explicitly left for follow-up at the preceding checkpoint.

### Paired mutation evidence

Focused specimens copy the four affected cases from the original and final
features. They are not presented as whole-suite runs. Both controls pass 4/4.
The original placeholder reader/writer cases map to rows 1/2 of the new
placeholder outline; cluster-error rows retain their identities.

| Production fault | Before / after failing cases |
|---|---|
| Return an empty stream from a placeholder read | 1 / 1: reader case / placeholder row 1 rejects `open gzip: EOF` instead of the required no-executor refusal |
| Report success from a placeholder write | 1 / 1: writer case / placeholder row 2 rejects success |
| Suppress production SPDY exec errors | 2 / 2: both cluster-error rows reject success |

All three faults compile and preserve all four scenario/fault pairs;
`matrix.json` records exact failures. The empty-read mutant is detected on
the wrong failure reason, not on proof that an empty stream is usable.
Production volume and executor files remain byte-identical to their snapshots;
faults are isolated build overlays.

Vet and both module-pinned Ginkgo packages pass (30.616s), including vocabulary
and real adapter protocol/resource checks.

Full gate: **577/577 pass**, **1,865/2,368 = 78.758446%** production
JetBridge statement coverage against the 50% requirement, unchanged from the
preceding run. Summed scenario time: 124029ms, not wall-clock runtime.
Log: `/tmp/brine-v5-volume-setup.J9RXd4/coverage.log`.
Profile: `/tmp/brine-coverage.Mja6cI/coverage.out`, SHA-256
`6404e9382a00a9937d9e6a34807f2ba711aef197c2d2adadbf1ee7bec4bd7729`.
The coverage command exited 0 and restored the normal adapter:
`7bcaf783171ba33a062148e80d60c479c3420a81b9d1d5e85fcf41af5f70182f`.
Final volume-stream fixture SHA-256:
`acc53494515ef682d2556a079cc3c7cc8f7fac9a48362904de571a88815ce383`.

### Corrected remaining-work explanation

Artifact-recording comments and feature prose no longer claim the production
daemon lacks `--kubeconfig` or needs a new flag to discover peers. Current
`cmd/artifact-daemon/main.go` supports both `--kubeconfig` and
`--listen-address`. The unfinished fixture work is a production daemon pair,
real EndpointSlice discovery at distinct reachable addresses sharing the peer
port, and observation of asynchronous mirror completion. The existing HTTP
copy stand-ins are still doubles; correcting their explanation does not
remove them or prove production mirroring. These are documentation-only
changes, not altered scenario steps.

Successful direct-volume execution, peer fixtures, other fake-client families
and CI validation remain unfinished. The goal is still active; work is local
and uncommitted on `core`.

## Real peer topology proven, not yet integrated (2026-09-10)

Evidence and prototype sources: `/tmp/brine-peer-topology.MNf1Vs`.
This is a real infrastructure experiment that changes the next migration
action, not a claim that the peer stand-ins have already been removed.

A small C launcher creates its own Linux user and network namespaces, verifies
both namespace identities changed, and only then configures two private IPv4
addresses on its private loopback interface. It execs the probe in that
namespace. The addresses are actual kernel-owned, reachable addresses, not
fictional EndpointSlice values. Neither address appears in the host routing
table after the runs; the host address remains unchanged.

The final launcher maps a root caller to the existing non-root nobody identity
inside the private namespace (uid 65534 here). This matters because Brine's
PostgreSQL runner otherwise sees uid 0 and tries to switch to an unmapped
postgres uid. The probe acquires the ACTUAL `postgres` and `jetbridge-db`
resource definitions and persists a real worker before starting the daemon
experiment. Both control and mutant passed that prerequisite. No fake
database or alternate resource implementation was introduced.

The API server needs an explicit advertise address in a namespace without a
default route: `env.ControlPlane.GetAPIServer().Configure().Set(
"advertise-address", "10.203.0.1")`. Leaving it to automatic selection failed
with Kubernetes's explicit no-default-routes diagnostic. The corrected probe
starts a real API server and etcd, creates real Nodes and an IPv4 EndpointSlice,
and launches two current production artifact-daemon binaries:

- Addresses `10.203.0.1` and `10.203.0.2`, both on port 17880.
- Explicit `--listen-address`, `--kubeconfig`, `--namespace`, `--node-name`,
  `--mirror-replicas=2`, and each daemon's actual `POD_IP`.
- Independent owned storage roots; no HTTP handler or copy implementation in
  the probe.
- A real producer file at `steps/build-42/output/result.txt`, followed by a
  real `POST /mirror` returning 202.
- Bounded observation of the file physically arriving in the peer's root,
  BEFORE a peer HTTP read; this prevents read-through fetching from disguising
  an absent mirror.
- A final GET from the peer's production `/artifacts/steps/build-42/output`
  route, extracting its real tar and comparing exact file bytes.

### Control and production fault

`database-control/status` is 0. Its log records both successful real database
persistence and the exact mirrored-file/HTTP-tar check. Producer and receiver
logs show `mirror-complete` and `stream-in-complete`.

`database-no-trigger/status` is 1. A Go build overlay changes only the
production server's mirror trigger call to a no-op. Both daemons still start
and configure real peer discovery, and the real database prerequisite passes;
the probe then fails `peer file never arrived: context deadline exceeded`.
The observation budget is ten seconds. This rules out a control that passed
merely because the server accepted a request. Production server source still
matches its saved original exactly.

Prototype identities (SHA-256):

- `netns-exec.c`: `1b60d00b0f7188d03693bc3e8bc1d976358815cf778da07ea5d90b31b47fd0bd`
- `probe.go`: `2dce99537d0f927148fe9e3148bce5cbd694a22fb6050e8725e462a906d5ef79`
- Control daemon: `d05d1c4140399a97849ab5365201f7cd72948de74d411b87a4b0d039051c7f49`
- No-trigger daemon: `12cb5fc434a595217129ae5d7d7d092166210535f9cbccaec9fcf11fb86a59e9`

Earlier prototype failures are retained separately: parent-namespace inspection
through /proc was denied after entering a child user namespace, and automatic
API advertise-address selection failed without a route. The final launcher
compares its own namespace identities before/after creation instead.

### Next integration boundary

The existing peer/mirror cases can now move to a real daemon pair. Their API
server, daemons and system under test must share the private network namespace;
these addresses cannot be reached from the outer host namespace. Integrating
a launcher must preserve Brine's protocol file descriptors, holding and
cancellation/drain semantics, and avoid hidden online builds inside the
network-isolated process. User-namespace support and compiler availability
also need CI validation; this probe does not establish portability.

No Brine scenario or adapter code changed in this experiment, so the full
suite was not rerun. Its latest recorded checkpoint remains 577/577 and
78.758446% package coverage. The peer stand-ins, broader no-doubles audit and
CI work remain open. The goal is active.

## Optional private-network runner checkpoint (2026-09-10)

Evidence: `/tmp/brine-private-runner.AYhXEr`.

The proven namespace launcher is now repository-owned in
`scripts/netns-exec.c`, with `scripts/build-private-network` and
`scripts/run-private-network`. Builds happen before network isolation. The
launcher exports its two actual private addresses; envtest uses the first
as its explicit API advertise address. The daemon helper accepts a validated
absolute prebuilt binary path. The default `.brine` still runs the direct
adapter: peer fixtures have NOT yet been converted to the real daemon pair.

Existing black-box protocol tests can exercise this launcher through
`BRINE_PROTOCOL_LAUNCHER`; they still build and run the actual adapter. No
duplicate protocol suite was added. Final native controls passed both suites
on the host (27.479s) and through the private launcher (27.548s); vet passed.
The private full run passed **577/577**, with **1,865/2,368 = 78.758446%**
production Jetbridge coverage. Its 121054ms is summed scenario time, not
wall time. Profile SHA-256:
`20e9f760e261a2ce38b36c79346610a65b993c50befcfe179824c6e0486e0709`.

Two launcher faults are detected: closing protocol stdout breaks registry,
selection and roster assertions; dropping `--hold` makes the real-resource
hold test observe premature drain. The closed-stdout fault also exposed a
test-harness cleanup issue: CommandContext killed the adapter on timeout.
The hold test now sends SIGTERM and allows 45 seconds for process cleanup.
The repeated fault exits 143 rather than being killed; absent stdout still
correctly fails the protocol assertions. That exit alone is not proof of
drain events, which cannot be observed through a closed descriptor.

The initial fault run left two owned daemons. Exact environment-marker checks
identified etcd PID 1070580 and kube-apiserver PID 1070614. Both received
SIGTERM; the API server required SIGKILL afterward. Both are now terminated
(zombies awaiting their parent reaper). No unrelated process was stopped and
no material files were deleted. No additional live API/etcd process matched
the repeated fault-run marker at the final check.

The previous real-peer probe also passes through the repository launcher:
real PostgreSQL persists a worker as uid 65534, the real API accepts both
private addresses, and production daemons mirror exact bytes before a peer
HTTP tar read verifies them. This validates infrastructure, not replacement
of the three existing peer/mirror stand-in scenarios.

A fresh upstream fetch confirms `core`, `origin/core`, and
`origin/core-brine` all remain at `3c787e9c70602baf2279c5ff92ad32253edcf68b`.
Brine's latest dated remote branch and local checkout both remain at
`1e9345da594d6a8e6d6cc9899a7a73de74d99ec9`. The v5 migration changes are
local and uncommitted. Real peer-fixture integration, the broader no-doubles
audit, and CI image/cluster validation remain open. The goal is active.

## Artifact recording uses real peer mirroring (2026-09-10)

Evidence: `/tmp/brine-peer-migration.qCDkTY`. This supersedes the preceding
checkpoint's statement that the three artifact-recording peer cases still
use stand-ins.

Removed the map-backed `storeDaemon`, its HTTP register/mirror/tar handlers,
the fictional store-root constant, and the `nodeDisk` interface that existed
only to accommodate that double. Every artifact-recording daemon is now a
production process with a real owned filesystem. The three peer cases also
use real PostgreSQL and the suite's real kube-apiserver/etcd. The rest of this
feature still uses a fake Kubernetes client; no family-wide no-fakes claim
is made.

The real peer Given creates two actual Nodes, runs production daemons at
the private runner's two real IPv4 addresses sharing one daemon port, and
publishes a real EndpointSlice. Both daemons use the real kubeconfig and
their own POD_IP, roots and node names. Production does discovery, node
labeling, alias registration, and asynchronous copying. The check first
waits up to ten seconds for the exact file bytes to arrive on the peer's
disk, then verifies its HTTP tar. Reading HTTP first could cause production
read-through to hide a missing mirror.

Two setup sentences became one Given:
`a jetbridge worker whose outputs are mirrored to another node`.
All three scenario names, inputs, keys and final assertions remain. This
removes one step definition, not a behavioral case. Single-node and peer
fixtures share the same worker/backend/locator wiring. The daemon lifecycle
is shared too: configurable bind address, port and process environment,
with process reaping before root removal and safe repeated stop calls.

### Paired production faults

Each run uses an explicit manifest naming the actual adapter binary and
the exact three selected scenarios. Old binaries were built before the
fixture changed. Both controls pass 3/3. The machine-checked matrix is
`mutation-matrix.json` in the evidence directory.

| Production fault | Same failures before and after |
|---|---|
| Omit the ATC's actual mirror request | All three cases |
| Send the volume handle instead of the output's disk key | Both RecordOutputs cases; cache case still passes |
| Stop after the first output | Multi-output case only |

All three mutations compile and preserve six exact scenario/fault pairs.
Additionally, the isolated production daemon mutation accepts POST /mirror
but removes only the mirror-trigger call. The old fixture passes 3/3 under
that binary configuration because it never exercises a production daemon;
the new fixture fails all three on absent completed peer files. This adds
evidence the removed double could not supply. The daemon source/overlay
and binary remain in `/tmp/brine-peer-topology.MNf1Vs`; current production
server source matches that saved original byte-for-byte. No production
source or retained Go test was removed.

### Default runner and final gates

The default manifest now names `scripts/run-private-network`. The coverage
script builds the launcher, adapter and daemon before isolation, and restores
the normal adapter afterward. The CI task builds the same prerequisites and
runs its existing native protocol suite through the launcher. Its image
declares the C compiler dependencies. `README.md` documents Linux namespace
requirements and the standard commands; absent prerequisites fail explicitly,
never fall back to a fake topology.

- Vet passes. Both native suites pass through the launcher in 30.260s,
  including vocabulary resolution/ambiguity guards and real-resource hold.
- Full default-runner coverage passes **577/577**, **1,865/2,368 production
  Jetbridge statements = 78.758446%**, against the exact >=50% gate.
  The log reports 122427ms summed scenario time, not wall-clock runtime.
- Profile: `/tmp/brine-coverage.fFVsyw/coverage.out`, SHA-256
  `7b8ab0a27eee6385cf6cc132bde3d5a2019f282cb145561573e6f7bfc1202436`.
- All three real-peer control scenarios report six scenario disposers drained
  with `partial:false`. The observed full-run CLI, adapter, API and etcd
  processes have exited.
- Script syntax and `git diff --check` pass. The final adjacent feature and
  Go-comment edits correct historical claims about unobservable mirroring;
  they do not change executable steps.

The broader fake-client/executor audit and real CI image/cluster validation
remain incomplete. This checkpoint does not establish that CI permits user
namespaces, nor that the new image has been built or deployed. Work remains
local and uncommitted on `core`; the full goal stays active.

## Artifact-recording API consolidation (2026-09-10)

Evidence: `/tmp/brine-artifact-api.TiAu9I`. This supersedes the previous
checkpoint's remaining fake Kubernetes client in artifact-recording.

The single-node and peer Givens now share one real constructor, varying only
whether a second daemon is required. Removed the fake-backed `NewCluster`
call, the separately fabricated Node/EndpointSlice setup, the duplicate
single-node daemon launcher/getters, and the test-supplied readiness label.
The production daemon labels its actual Node through the real API. Both
worker and backend use that client and real PostgreSQL. Pod creation is
wired through production exec mode using the real SPDY executor; envtest
still has no kubelet, so this does not claim successful remote execution.

The same generated-namespace pod cleanup is now shared by the worker and
artifact families. It lists only scenario-owned pods, deletes each with an
API-assigned UID precondition and zero grace, and runs with a bounded context
independent of scenario cancellation. The 22 artifact-recording control
scenarios all report non-partial recorder drain.

### Production-fault preservation and new coverage

Old binaries, source snapshots, explicit manifests and mutation overlays
are retained in the evidence directory. All 22 artifact-recording scenarios
pass before and after; no scenario or step assertion was removed.
`mutation-matrix.json` machine-checks equal scenario names/statuses for:

| Production fault | Same failures before and after |
|---|---|
| Omit the artifact-locator record | Named readback, next-step key, and unknown-node fetch cases |
| Prefer the node holding fewer inputs | Multiple-input node-preference case |
| Require the wrong readiness label | Daemon-ready scheduling case |

These preserve five exact scenario/fault pairs. A fourth fault changes only
the production daemon's published Node label to `not-ready`. The old fixture
passes all 22 because it supplied `ready` itself; the real fixture fails
exactly the readiness scenario, against the real API's observed label.
This is additional behavior previously hidden by the fake setup.

### Shared handoff regression found and fixed

The first full-suite attempt passed 572/577. Its five failures were the
volume-handoff outline, which also consumes the migrated Given. The real API
rejected its ordinary Pod update changing `spec.nodeName`; the old fake
accepted that invalid operation. Evidence remains in `coverage.log` and
`/tmp/brine-coverage.15AUgk/brine.log`. This failed run is not a coverage gate.

The handoff now uses the real Pod binding subresource, reloads the resulting
object, and publishes Running through UpdateStatus. It deletes the producer
with its observed UID and zero grace, then requires an actual NotFound before
reading the collected artifact. This closes the difference between requesting
deletion and proving deletion on the real API.

Both old and corrected handoff controls pass all five outline rows. A
production-only mutation redirects Volume.StreamIn to `missing-container`.
The raw, gzip and S2 delivery rows fail on that exact missing container in both
fixtures; the two existing fault rows still pass. That preserves three more
scenario/fault pairs, eight in total for this checkpoint. The old handoff
source comes from HEAD, where it was unchanged before this turn; its old
artifact constructor snapshots are explicitly overlaid when building the
old mutant.

The handoff still replaces the exec transport with `localExecutor` and drives
Running status itself. Using real binding/status APIs does not turn that into
a real kubelet or establish a double-free execution test.

### Final gates and remaining work

- Final vet and both native suites pass; Ginkgo reports 28.911s.
- Final full default-runner coverage passes **577/577**, **1,865/2,368 =
  78.758446%** production Jetbridge coverage against the exact >=50% gate.
  `coverage-final.log` reports 122936ms summed scenario time, not wall time.
  Profile: `/tmp/brine-coverage.ntn1JP/coverage.out`.
- The final full-suite CLI, adapter, kube-apiserver and etcd processes exited.
  `git diff --check` passes.
- Remaining doubles in these consumers include StubVolume descriptors,
  `fetchShellPrelude`'s substituted wget/sleep, and the handoff host executor.
  The real GNU wget binary is available locally; the shell substitute is the
  next concrete removal target. It must execute the actual request and retry
  behavior, not replay a response captured before running the script.
- Other fake-client families, broader legacy-test mutation accounting and
  real CI image/cluster validation remain open. The goal stays active; all
  changes are local and uncommitted on `core`.

## Real fetch-script checkpoint (2026-09-10)

Evidence: `/tmp/brine-real-fetch.8UkVhC`. The two existing script-execution
scenarios now execute the production init command under real BusyBox
sh/wget/sleep against the production daemon. The Go pre-request, response
replay, shell wget function and no-op sleep are removed. Destination
directories are still prepared by the fixture: this is a script/daemon
boundary test, not evidence of a running kubelet.

The negative assertion now requires the actual daemon HTTP 500 refusal in
addition to a nonzero script exit. A command crash, missing tool or wrong
endpoint must not satisfy a missing-input scenario. The positive scenario
continues to verify the delivered file's contents, not just shell success.
No feature scenario, legacy test or existing assertion was removed.

### Real-tool and failure-boundary validation

- The pinned upstream musl BusyBox binary is SHA256
  `6e123e7f3202a8c1e9b1f94d8941580a25135382b99e8d3e34fb858bba311348`.
  Its source URL is recorded in `deploy/Dockerfile.test-runner`.
  The private-network build installs it as `.build/busybox`, preserving
  BusyBox's argv[0] dispatch, and checks an actual wget timeout-option
  invocation against an empty private namespace before building the suite.
- Debian bookworm's downloaded static and dynamic builds crashed with
  `wget -T` here, including outside the namespace. This is a measured
  option boundary, not a diagnosed libc cause. The original failed control
  remains in `new-control.log`; it is excluded from passing evidence.
- `runBusyboxScript` installs only real applet symlinks into its owned
  temporary directory, supplies no host-tool fallback, and cleans up the
  command's process group on cancellation. The shared host executor reuses
  the same group-stopping function.
- One native table tests real success, exit 7, a running script's deadline,
  and missing prerequisites. Deadline errors are not classified as script
  exit errors. All four rows pass. The build script separately refuses a
  missing BusyBox with exit 2 before executing scenarios.
- The corrected control passes 2/2 in `new-control-final.log`. The negative
  scenario takes 18.185s, including the production retry waits; its backoff
  is no longer accelerated.

### Exact production mutation comparison

`mutation-matrix.json` validates complete two-scenario rosters, exit codes,
failure attribution and binary hashes for both controls and four mutants.
Production code is changed only through isolated Go overlays.

| Production fault | Before shell removal | After shell removal |
|---|---|---|
| Omit the artifact-index record | Unknown-node fetch fails | Same scenario fails |
| Exit zero after failed delivery | Partial-delivery scenario fails | Same scenario fails |
| Send the request to a nonexistent endpoint | Both pass | Both fail on actual HTTP 404 |
| Send an empty batch instead of the pod payload | Both pass | Both fail: missing contents and false success |

The empty batch is accepted by the daemon with an empty successful result;
it is not an HTTP 400. The file-content assertion exposes the positive
case, and the zero-exit assertion exposes the negative case. This preserves
two scenario/fault pairs and adds four previously undetected pairs.
Initial `new-<fault>.log` attempts refused non-executable temporary wrappers;
they ran no adapter and are excluded. Corrected evidence uses
`new-<fault>-final.log`.

### Gates and remaining boundaries

- Final vet and both native suites pass; Ginkgo reports 30.781s.
- Full coverage passes **577/577**, **1,865/2,368 = 78.758446%** against
  the >=50% production JetBridge package gate. This is not repo-wide coverage.
  The report's 141053ms is summed scenario time, not wall time.
- Profile: `/tmp/brine-coverage.2Y9JtP/coverage.out`, SHA256
  `b3ff3cd8b896a7e336a3bfcc0936f90757702c3fe11c7746eac23d2f5a1e7de6`.
  The restored normal adapter matches the tested control byte for byte.
  The full run's CLI, adapter, API server and etcd processes have exited;
  `git diff --check` passes.
- StubVolume descriptors, host executors and other fake-client families
  remain. The image recipe now includes BusyBox, but no image was built,
  published or deployed; pipeline references still name the old v9 image.
  The broad no-doubles requirement and real CI validation are not complete.
  All changes remain local and uncommitted; the full goal remains active.

## Worker-created artifact volumes checkpoint (2026-09-10)

Evidence: `/tmp/brine-real-volumes.Htys0X`. All four direct NewStubVolume
construction sites are removed from `steps/artifact_recording.go`.
The producer's volumes now come from the real worker's
`FindOrCreateContainer`, with real PostgreSQL container state and the
production SPDY executor. Scratch mounts are retained alongside output
mounts, leaving output selection to production RecordOutputs.

The feature's declared handles are independent expectations checked against
the returned volumes, never constructor inputs. Task handles now name the
actual worker-generated identities; the get-step handle already did.
All 22 scenario names and existing behavioral assertions are retained.
Locally produced consumer inputs pass through Worker.ArtifactFromVolume;
other input references use the real backend's lookup wrapper. Neither path
constructs a placeholder volume.

One producer-preparation helper is shared by recording and pod-layout
inspection. The cache-mirroring scenario now describes an actual get step,
places its bytes through that pod's declared mounts, and passes the returned
working-directory volume's handle to cache registration. It no longer
describes a task output while independently inventing a get-volume handle.
No new behavioral scenario or step definition was added, and no legacy test
was deleted.

This does not claim a running kubelet. The API, worker, database, volume
objects and daemon paths are real; production container execution still
requires the execution-tier migration.

### Fault preservation and new identity coverage

Old source/feature snapshots, binaries and explicit runner manifests remain
in the evidence directory. Controls pass 22/22 before and after.
`mutation-matrix.json` and `cache-mutation-matrix.json` validate complete
rosters, exact failure attribution, exit codes and binary hashes.

| Production fault | Before and after |
|---|---|
| Omit the artifact-index entry | Same three readback/key/fetch scenarios fail |
| Exit zero after failed delivery | Same partial-delivery scenario fails |
| Derive the cache's directory as missing instead of dir | Same get-cache mirror scenario fails on rejected registration |

These preserve five scenario/fault pairs. An additional mutation gives every
output of one producer the same volume handle. The old fixture passes 22/22;
the new fixture fails exactly the two multi-output scenarios, which now
observe the real worker's conflicting identities.

A separate naming-only mutation changes the handle prefix. The old fixture
passes; eight new cases reject the changed independently expected identity.
This is reported as an identity assertion, not counted as additional
data-loss evidence. The duplicate-handle fault is the concrete collision
check.

### Gates and remaining work

- Vet and both native suites pass; Ginkgo reports 30.760s.
- Full Brine coverage: **577/577**, **1,866/2,368 = 78.800676%** production
  JetBridge package coverage against the >=50% gate. The 140101ms report is
  summed scenario time, not wall time.
- Profile: `/tmp/brine-coverage.KplwwH/coverage.out`, SHA256
  `ab1d59be2a94d68dc91d37af347b4fd457b2f32f0fc8c072b09b5b23ef82827e`.
  The restored normal adapter matches the final control byte for byte.
  The full run's CLI, adapter, API server and etcd exited.
- `git diff --check` passes. Changes are local and uncommitted on core.
- `volume_streaming.go` still contains a placeholder volume, fake client
  construction and host execution. Other fake-client families, the handoff
  host executor, broader legacy-test accounting and actual CI image/cluster
  validation remain unresolved. The full goal remains active.

## Real remote-artifact checkpoint (2026-09-10)

Evidence: `/tmp/brine-remote-volumes.HoooxU`. The six existing remote-artifact
scenarios no longer use the custom HTTP handler or nodeAndPeers fake client.
Real Kubernetes Nodes and EndpointSlices drive production node resolution
and peer discovery. Separate production artifact-daemon processes serve
producer and peer files on distinct private addresses at the common daemon
port. The forgotten-producer write case still deliberately has no daemon
or resolver; it exercises the real volume's refusal.

The fixture supplies owned artifact files, not HTTP responses or tar bytes
returned to the runtime. The source alias is registered through the real
daemon. The peer copy is seeded on the peer's own disk and served by that
daemon; this tests consuming an existing mirror, not creating one.

Faults now act at real boundaries:

- A TCP route drops the first requested connections, or every connection,
  after the client sends a byte. Other connections reach the real daemon.
  The route neither parses HTTP nor generates responses, and its drop count
  drives the fault rather than serving as an assertion.
- The refused-source case leaves the recorded source address without a
  listener and probes an actual empty peer daemon.
- The server-error case creates an unreadable artifact file in the owned
  daemon root. Setup verifies a real permission denial; the production
  daemon then returns its genuine HTTP 500. The control passes with that
  diagnosis, not a canned error body.

### Shared transport lifecycle

The pre-existing cross-node and mirroring route helper delegates to the
same TCP implementation. A route now owns cancellation and drains accepted
connections before Close returns. Forwarding in both directions remains
one shared implementation.

The new native TestDaemonRouteClosesLiveKeepAliveConnections first receives
a real daemon health response over a persistent connection, then requires
route closure to drain and close it. Removing route cancellation fails only
that native test. `route-mutation.json` verifies this attribution separately
from production coverage. The initial `route-no-cancel.log` contains only
a Ginkgo argument-order refusal; it is excluded, and corrected evidence is
`route-no-cancel-final.log`.

### Production mutation comparison

Both controls pass 6/6. `mutation-matrix.json` checks complete rosters,
exact scenario failure attribution, exit statuses and executable hashes.

| Production fault | Before and after |
|---|---|
| Remove retry attempts | Same recovering-daemon scenario fails |
| Disable peer fallback | Same peer-success and empty-search scenarios fail |
| Report HTTP 500 as a missing artifact | Same server-diagnosis scenario fails |

These preserve four scenario/fault pairs. Disabling the real daemon's
directory tar output passes all six cases with the old HTTP stand-in, but
fails both successful artifact-read scenarios with the new real daemons.
Those are two additional production-fault detections. No behavioral
scenario, requirement tag or existing assertion was removed.

### Gates and remaining work

- Vet and both native suites pass; Ginkgo reports 27.603s.
- Full Brine coverage: **577/577**, **1,866/2,368 = 78.800676%** production
  JetBridge package coverage against the >=50% gate. The 141872ms report is
  summed scenario time, not wall time.
- Profile: `/tmp/brine-coverage.2ry1ab/coverage.out`, SHA256
  `19f85302e9b0c392bca4de4f7465c663a84642a86059f500ff889468d3b90de6`.
  The restored normal adapter matches the final control byte for byte.
  The full run's CLI, adapter, API server and etcd exited.
- `git diff --check` passes. Changes remain local and uncommitted on core.
- Host execution, other fake-client families and actual CI image/cluster
  validation remain. The no-executor volume scenarios test a production
  refusal directly; their use of the production constructor must not be
  confused with the removed HTTP and Kubernetes fixture implementations.
  Broader no-doubles compliance is not yet established; the goal stays active.

## Majority-placement consolidation checkpoint (2026-09-10)

Evidence: `/tmp/brine-placement.p91xKl`. The two majority-placement cases
are now one scenario in artifact-recording.feature, retaining requirement
tag CO-10. Inputs use worker-created, persisted artifact volumes, actual
Kubernetes Nodes and owned production-daemon storage. The consuming pod
receives those same artifact references. This checks pod construction and
its preference, not execution by a real Kubernetes scheduler.

The shared assertion now requires exactly one positively preferred node:
all hostname expressions are inspected, with positive weight and operator
In. It no longer returns the first hostname while ignoring additional ones.

### Evidence before deletion

Both old cases and the candidate were built and run against controls and
five production mutations. `pre-removal-proof.json` was validated and
written before deleting the duplicate. Final binaries repeat the checks
after deletion; `mutation-matrix.json` verifies scenario rosters, statuses,
failure reasons, terminal run events and executable hashes.

| Production fault | Old cases | Consolidated case |
|---|---|---|
| Prefer the minority node | Both fail | Fails |
| Omit preference | Both fail | Fails |
| Name an additional preferred node | Only retired case fails | Fails |
| Refuse artifact-volume creation | Only retired case fails | Fails |
| Reverse preference from In to NotIn | Both pass | Fails |

Four prior fault types are preserved, including the two checks unique to
the retired fake-backed case. Reversed preference is newly detected.
Controls pass 2/2 before and 1/1 after. Artifact creation is a required
setup boundary; its failure is not claimed as a scheduling assertion.

The InputPlacement state and four dedicated definitions were removed,
along with the old locator-only setup sentence. Two shared real-artifact
sentences replace them. Adapter catalogs measure **1,081 -> 1,078** step
definitions; the full suite changes **577 -> 576** scenarios. No legacy
Go tests were removed. Deleted source is recoverable from git history
and the before-* snapshots in the evidence directory.

### Gates and remaining work

- Vet and both native suites pass; Ginkgo reports 28.748s. The registry
  guards find no unused, unresolved or ambiguous scenario vocabulary.
- Full Brine: **576/576**, **1,866/2,368 = 78.800676%** production JetBridge
  package statement coverage against the >=50% gate. The reported
  141942ms is summed scenario time, not wall time.
- Profile: `/tmp/brine-coverage.dYaqFd/coverage.out`, SHA256
  `72898c454844dca67465a9dc0a33f6eb555e4a30aa57e133e7568e40e59d1560`.
  The restored normal adapter matches the final control byte for byte.
- `git diff --check` passes. Changes remain local and uncommitted on core.
- Other fake-client families, host execution and actual CI image/cluster
  validation remain. The adjacent worker mount cases are a bounded next
  candidate; preserve their unique mutation detections before replacing
  fixtures or consolidating cases. The overall goal remains active.

## Deferred worker-mount checkpoint (2026-09-10)

Evidence: `/tmp/brine-worker-mounts.YW4skL`. Five pre-scheduling cases now
start from the existing real WorkerReady fixture: a real Kubernetes API,
PostgreSQL worker/team rows, persisted input artifacts and the production
SPDY executor. The shared deferred-container action preserves that transport
instead of replacing it with localExecutor. These cases inspect the returned
mounts before Run; they do not claim successful pod execution or streaming.

The real worker enters the existing ContainerDraft state through one new
transition. All input/output/cache refinements and mount assertions remain
shared. Client fields used only through the Kubernetes API now accept its
interface. The old process-lifecycle transition still requires fake-only
reactors and explicitly rejects a real-worker draft rather than casting it
unsafely. That lifecycle fixture is not migrated by this checkpoint.

The two overlap bodies are consolidated into one CO-05 outline, retaining
exact-path and trailing-slash rows. Thus one authored scenario body is
removed, but no executed case or assertion is deleted. The additional typed
worker-to-draft transition changes the adapter catalog from 1,078 to 1,079
definitions; this is not reported as a net step-vocabulary reduction.

### Paired production-fault evidence

Six cases were compared: the five migrated cases plus the unchanged
process-start/binding case, which still uses the legacy fixture. Both
controls pass 6/6. `fixture-comparison.json` verifies exact scenario names,
statuses and failure reasons before consolidation. `mutation-matrix.json`
repeats the comparison after consolidation with explicit outline-row
mapping, complete terminal rosters, exit statuses and executable hashes.

| Production fault | Same failures before and after |
|---|---|
| Disable input/output deduplication | Both overlap spellings |
| Stop normalizing the output path | Trailing-slash row |
| Leave cache path relative | Relative-cache case |
| Omit returned input mounts | Both overlap rows and all-paths case |
| Give returned volumes one shared handle | All-paths case |

All eight prior scenario/fault detections survive. No newly detected fault
is claimed. No legacy Go tests were removed; before-* snapshots retain the
previous local source, including the two original overlap bodies.

### Gates and remaining work

- Vet and both final native suites pass; Ginkgo reports 27.531s, including
  unused/unresolved/ambiguous vocabulary guards.
- Full Brine: **576/576**, **1,866/2,368 = 78.800676%** production JetBridge
  package statement coverage against the >=50% gate. The 141626ms report
  is summed scenario time, not wall time.
- Profile: `/tmp/brine-coverage.0jR3Re/coverage.out`, SHA256
  `4b4ca42731301a4aa9f5f1f58c3d06c5971085844e743107731d2f9ae02748b1`.
  The restored normal adapter matches the new control byte for byte.
  Full-run CLI, adapter, API server and etcd PIDs have exited.
- `git diff --check` passes. Changes remain local and uncommitted on core.
- The process-start/binding case still selects the host-executor fallback.
  Other lifecycle/watch fixtures, daemon doubles and actual CI image/cluster
  validation remain. No-mocks compliance is not established; the full goal
  remains active. The next lifecycle migration must replace the real missing
  kubelet/exec boundary, not merely change the clientset's type.

## Real warm-ownership checkpoint (2026-09-10)

Evidence: `/tmp/brine-warm-ownership.ryOYPH`. The rollingDaemons HTTP handler,
virtual-host addressing, map-backed node disks/durable store and fake client
are removed from daemon_mtls.go. The three existing mTLS cases are unchanged.
Warm ownership now lives in real_warm.go and uses two production daemon
processes, real Kubernetes Nodes/EndpointSlices and a filesystem durable store.

Both daemons start at distinct private IPv4 addresses on a common port. A
warm uses the production backend/client; the subsequent production probe's
owner must agree with exactly one daemon root containing the expected bytes.
The daemon DELETE API then removes each owned local copy, with absence
checked before replacement. Both daemon processes are stopped and restarted
on swapped addresses, and the real EndpointSlice is updated with its current
resourceVersion. The next warm must leave the same physical node owning the
cache. This exercises daemon replacement and address reassignment, not a
DaemonSet controller or kubelet. Local cache copies have already been removed;
this does not claim preservation of unrelated hostPath data across restart.

### Hidden fixture mismatch and vocabulary cleanup

The old fixture accepted `sha256:cafe`, which the production durable store's
key validator rejects. The scenario now uses `resource-caches/rc-cafe`, with
the cache alias remaining `rc-42`. A separate invalid-key run against the
corrected real fixture confirms that restoring the old spelling fails only
the ownership scenario. That run is diagnostic, not a permanent extra case.

The fixture seeds a real tar object under the owned filesystem-store root;
it does not implement a Store interface or an HTTP response. Production
performs key validation, restore, extraction, registration and probing.
The local-copy step no longer claims to drive the sweeper: it names the
reclaimed state and reaches it through real DELETE requests.

The separate "warm again" definition was removed. Both actions use the same
step, with recorder-owned disposal even on failure. Adapter catalogs measure
**1,079 -> 1,078** definitions. No executed scenario or legacy Go test was
removed. Before snapshots retain the old local source and feature.

Node-named daemons require real API credentials at boot to label their
Nodes. The initial `new-*.log` runs omitted those credentials and failed
during startup; they are excluded from mutation evidence. The corrected
fixture and artifact peers share one daemonKubeconfig helper and credential
lifecycle. No cloud SDK dependencies were added to the Brine module.

### Mutation comparison

`mutation-matrix.json` validates complete four-case rosters, exact failure
attribution, terminal events, exit statuses and adapter/daemon hashes.

| Production fault | Old fixture | Real fixture |
|---|---|---|
| Rank owners by address, not node | Ownership case fails | Same case fails |
| Skip warming | Ownership case fails | Same case fails |
| Request the wrong durable object | Ownership case fails | Same case fails |
| Daemon reads a missing durable object | All four pass | Ownership case fails |
| Daemon restores an empty directory | All four pass | Ownership case fails |

Controls pass 4/4. Three prior ATC detections survive, with two additional
daemon detections. In the empty-directory mutant, the production probe
reports an owner, but the independent disk check finds no payload owner;
an HTTP hit alone cannot satisfy the scenario.

### Gates and remaining work

- Vet and both native suites pass; Ginkgo reports 26.956s, including the
  typed vocabulary and v5 protocol guards.
- Full Brine: **576/576**, **1,866/2,368 = 78.800676%** production JetBridge
  package statement coverage against the >=50% gate. The 142310ms report
  is summed scenario time, not wall time.
- Profile: `/tmp/brine-coverage.2Bkkua/coverage.out`, SHA256
  `5d1b7fce0389014d2d2d3fd5b6c22442b63b79f7177dd6ce05f9ec9b4d14f761`.
  The normal adapter is restored and matches the final control byte for byte.
  Full-run CLI, adapter, API server and etcd PIDs have exited.
- `git diff --check` passes. Changes remain local and uncommitted on core.
- Closing/cache HTTP substitutes, lifecycle/watch doubles, host execution
  and actual CI image/cluster validation remain. The full goal stays active.

## Real durable-cache checkpoint (2026-09-10)

Evidence: `/tmp/brine-closing-cache.QxHN3v`. The ten durable-cache variants
in step-closing.feature now use production daemon processes, real Node and
EndpointSlice objects, and an owned filesystem durable store. The
closingDaemon HTTP implementation, map-backed storage behavior, canned tar
responses and fake discovery client are removed. Maps remaining in the
plan describe input files only; they implement no runtime interface.

The runtime's own metrics remain the assertion boundary for local hits,
warm hits, misses and suppression. An unavailable store is an actual
permission denial, verified before use. A capability-silent daemon runs
without its durable tier configured; the no-activity assertion catches an
unwanted restore attempt even though the real daemon refuses that request.
This does not claim to execute an old daemon binary.

### Real registration and fallback boundaries

Local files are registered through the daemon API. ATC registration passes
the actual owned path to the production RegisterAlias client. Positive
registration waits up to five seconds for the asynchronously uploaded object
and verifies its complete tar contents before reclaiming the source. No-key
registration observes the forbidden row-ID durable object for 500ms before
continuing. This is a bounded negative observation, not proof about all
future background work; the row-ID mutation is detected within that window
in both comparison runs. All daemon/storage cleanup is recorder-owned,
including errors before the final lookup.

Peer-fallback cases use a separate daemon root holding an unregistered
mirror. Only the primary is published during the initial probe/warm. After
the real DELETE API removes the primary's copy and absence is verified,
the peer is published through the real EndpointSlice. Standalone data-plane
daemons have no daemon-side peer resolver: the ATC client must perform the
fallback. Removing that client still fails both variants, proving that
daemon read-through or an early peer hit did not bypass the tested path.

### Consolidation and mutation evidence

The two fallback bodies are one outline with local-hit and durable-warm
rows. Both variants and every existing assertion remain. The registration
case is renamed to say what it proves: survival after local-copy reclamation,
not transfer to a different node. No legacy Go tests were removed.
The adapter catalog stays at **1,078** definitions; no new vocabulary was
added. Before snapshots preserve the original local source and feature.

`pre-consolidation-proof.json` was validated before consolidating the bodies.
`mutation-matrix.json` repeats the final comparison with explicit row/name
mapping, complete ten-case rosters, terminal events, failure reasons and
adapter/daemon/log hashes.

| Production fault | Same prior scenario detections |
|---|---|
| Remove negative-cache suppression | Repeated failed warm |
| Ignore daemon capability | Capability-silent daemon |
| Warm without a content key | No-content-key lookup |
| Drop the bound volume's peer client | Both fallback variants |
| Ignore endpoint readiness | Not-ready daemon |
| Drop the registration content key | Positive durable registration |
| Register under the row ID without a content key | No-key registration |

All eight prior scenario/fault pairs survive. Both controls pass 10/10.
Two additional production-daemon mutations pass all old cases but fail the
new fixture: an empty restore fails the durable-read and registered-cache
cases; omitting durable upload fails the registration case. These are
three additional scenario/fault detections across two daemon fault types.

### Gates and remaining work

- Vet and both final native suites pass; Ginkgo reports 27.049s, including
  the typed vocabulary and v5 protocol guards.
- Full Brine: **576/576**, **1,866/2,368 = 78.800676%** production JetBridge
  package statement coverage against the >=50% gate. The 144302ms report
  is summed scenario time, not wall time.
- Profile: `/tmp/brine-coverage.AQ5A1l/coverage.out`, SHA256
  `d958d42e3122103cbb370e44b73e021a90c0d211ebf09be22ec5df576f296092`.
  The restored normal adapter matches the new control byte for byte.
  Full-run CLI, adapter, API server and etcd PIDs have exited.
- `git diff --check` passes. Changes remain local and uncommitted on core.
- Whole-step/lifecycle/watch doubles, host execution, a remaining daemon
  HTTP fixture and actual CI image/cluster validation remain. The full
  no-mocks goal is not yet achieved and remains active.

## September 10 upstream refresh and recommendations

Fetching the configured single-branch remote was insufficient to discover
the next dated release. An explicit remote-head check found
`main-20260910` at `6c66f53848578bd5b2e54a2427970e087b2d2387`.
The clean Brine checkout now tracks that branch; the CLI and engine were
rebuilt with Cargo's lockfile. The nested Go module/checksums and Dockerfile
source branch now agree with this revision. No image was published.

The September 9 to September 10 diff contains no changes to the Go runner,
execution-document schema, conformance goldens or verifier. It adds browser
host capabilities (`posture: inside/outside`, absent for process runners)
and prevents an inherited `BRINE_GEN_KEY` from leaking through the engine.
JetBridge's Brine tests use neither browser posture nor that generation key;
no scenario or step-definition rewrite is required for this daily update.

The underlying v5 migration still requires document-owned run/check inputs,
selection directives and roster echoes; registry-document capability
declarations; and real hold/cancellation cleanup. Those are implemented in
the local adapter. Step authoring remains v3. Do not add another feature
parser, duplicate selection logic or a parallel vocabulary for v5.

Fresh evidence: `/tmp/brine-sep10.y3jZeT`.

- Vet passes. Both native suites pass in 27.164s, including protocol,
  hold/drain and vocabulary guards.
- V5 conformance passes 39/39 externally drivable cases. The verifier does
  not cover AST generation or registry-dependent adapter obligations.
  The initial invocation from JetBridge refused because the contract index
  lives in the Brine checkout; the corrected invocation there passed.
- Full Brine passes 576/576, covering 1,866/2,368 production JetBridge
  package statements (78.800676%). The 142346ms report is summed scenario
  time, not wall time. Coverage is not repository-wide.
- Profile: `/tmp/brine-coverage.tVekOc/coverage.out`, SHA256
  `f88c7422fe99ec7c03f96d51be9f9b971c3f69cb4fdecbf74b35a0b4c02655ca`.
  The coverage script restored the normal adapter at the new dependency
  pin. The full-run CLI, adapter, API-server and etcd PIDs have exited.

Recommended next release milestone: build and publish an immutable v5
runner image, replace all 11 v9 pipeline references, and pass the pinned
image receipt guard, native/protocol tests and complete Brine coverage gate
in CI. This needs publishing/deployment authority and was not performed.
The behavioral migration remains separate: replace remaining watch,
lifecycle and runtime doubles with production-backed fixtures, retaining
paired mutation evidence before consolidating or deleting cases. The
peer-only resolve fixture is still a hand-written HTTP server; it was
inspected but not changed in this refresh.

After fetching JetBridge, `core`, `origin/core` and `origin/core-brine` all
remain at `3c787e9c7`, containing the prior Brine migration. The current v5
changes, including this refresh, remain local and uncommitted. The user's
AGENTS.md edit is preserved. The full migration goal remains active.

## Real peer-only cache probe checkpoint (2026-09-10)

Evidence: `/tmp/brine-peer-probe.4G7fN5`. The final hand-written HTTP
stand-in in `steps/daemon.go` is removed, along with its unused
`hostAndPort` helper and httptest import. The single peer-only cache
scenario remains, with one parameterized Given replacing the old one.
The catalog is unchanged at 1,078 definitions. No legacy Go test is removed.

The fixture reuses the real two-daemon/Kubernetes discovery setup. Only
the peer receives files and a registered cache alias. The empty daemon
must successfully POST-resolve those files through real peer discovery;
both the response's peer provenance and exact delivered bytes are checked.
The demonstration copy is then reclaimed through the real DELETE API,
and its physical absence, absence of a local steps/<key> cache and local
HEAD miss are verified. A separate discovery namespace presents only the
empty daemon to the ATC probe, while daemon-side discovery retains the peer.
Thus the final miss cannot pass merely because no peer ever existed.

Both old/new controls pass. The saved matrix validates exact scenario
rosters, step failures, terminal events, exit codes, disposer drains and
binary/log hashes:

| Production mutation | Old fixture | Real fixture |
|---|---|---|
| Treat a HEAD cache miss as a hit | Fails final miss assertion | Same failure |
| Disable daemon-side peer resolution | Passes | Fails real-resolve premise (404) |
| Return success without copying peer bytes | Passes | Fails delivered-byte premise |

This preserves one paired ATC fault detection and adds two daemon-fault
detections. The paired mutation changes the HEAD status predicate; it does
not replay the historical POST /resolve fallback with its now-invalid
outside-storage destination. No claim of legacy-test replacement follows
from this matrix. All eight comparison runs complete their recorder drains
without a partial result; the real control drains eight owned disposers.

Final gates pass: vet, both native suites (27.164s), all 576 Brine scenarios,
and 1,866/2,368 production JetBridge statements (78.800676%, >=50% gate).
The 144390ms report is summed scenario time, not wall time. The profile is
`/tmp/brine-coverage.lGw034/coverage.out`, SHA256
`8713d2938ac1935da889aaac2371f8d174cc22c4e5bcbc1fe94e43e5aaab081f`.
The restored normal adapter matches the new control byte for byte; the
full-run CLI, adapter, API server and etcd PIDs have exited. `git diff --check`
passes. Changes remain local and uncommitted on core.

The full goal is still active: lifecycle/watch fake clients and reactors,
whole-step runtime substitutes, remaining StubVolumes/host execution and
actual CI image/cluster validation have not been completed by this change.

## Real watch cancellation and consolidation checkpoint (2026-09-10)

Evidence: `/tmp/brine-watch-cancel.u7ss74`. Cancellation no longer needs the
fake watcher. The real fixture first receives a Running update through the
production PodWatcher and real API, establishing a live HTTP watch. The
next Next call has an independent context: cancelling that read leaves the
watch stream open, so HTTP-stream closure cannot mask a missing inner-select
cancellation branch by waking the outer retry loop instead.

This tests the PodWatcher read-context contract, not executing a pod or a
whole build. Status is changed through the real API; envtest has no kubelet
or scheduler. The fixture observes an idle read for 100ms before cancelling
it. A failure to return within 3s closes the real stream and joins the read;
the tested fault fails without abandoning its goroutine. The obsolete
claim that a real API cannot exercise this branch is removed.

Before consolidation, both original cancellation cases were captured with
their binaries and source. Both candidate real cases passed. Production-only
overlays established the following comparison:

| Fault | Old real case | Old fake case | Both real candidates | Retained real case |
|---|---|---|---|---|
| Remove inner-select cancellation | Passes | Fails | Fail | Fails |
| Swallow cancellation errors | Fails | Fails | Fail | Fails |

The machine-validated `pre-consolidation-proof.json` was saved before deleting
the duplicate body. `mutation-matrix.json` records the final replay, exact
scenario mapping, terminal events, failures, complete recorder drains and
binary/log hashes. All three old scenario/fault detections map to the one
retained scenario across the two fault types. No legacy Go test was removed.
Original sources remain in the evidence directory and repository history.

The now-equivalent cancellation cases are one scenario. Its old fake action
and assertion definitions are removed rather than retained as aliases:
**576 -> 575 scenarios; 1,078 -> 1,076 definitions**. The remaining four
fake-watch scenarios cover reconnection, fallback, replay and error delivery;
this checkpoint does not claim the whole watch family is mock-free.

Final gates: vet passes; both native suites pass in 27.374s; full Brine passes
575/575 with unchanged production JetBridge coverage, **1,866/2,368 =
78.800676%**, against the >=50% gate. The 144498ms report is summed scenario
time, not wall time. Profile: `/tmp/brine-coverage.v9MVGI/coverage.out`, SHA256
`e75476308881c694f71bca5e8a62e55c6d40673fae31792e86b145b97097d0b4`.
`gates.json` independently checks those totals and links both proof files.
The restored normal adapter matches the tested candidate; the full-run CLI,
adapter, API server and etcd PIDs have exited. `git diff --check` passes.

Changes remain local and uncommitted on core. Remaining watch/lifecycle
doubles, runtime substitutes, host execution and CI image/cluster validation
keep the full migration goal active.

## Real watch reconnection and replay checkpoint (2026-09-10)

Evidence: `/tmp/brine-watch-replay.49Q9Hk`. Reconnection and replay now use
the real API and a transparent TCP route. The route forwards TLS bytes
unchanged and implements no HTTP responses, selector filtering or replay.
Closing it joins the actual socket forwarders before the fixture changes
the pod. Restoring the same address lets the production watcher reconnect.

The scenario first observes an API-assigned resource-version checkpoint
through its live watch. During the interruption it changes the pod's phase,
deletes that pod with a UID precondition, and verifies NotFound before
reconnecting. Only replay can deliver the missed phase: a fresh snapshot or
fallback Get cannot recover a pod that is gone. Retaining the older initial
version is also observable, because that replays the Pending checkpoint
instead of the requested phase. No fixture parses or invents resource versions.

The one-pod and two-pod Givens now share one real setup helper, including
owned pod cleanup, watch Stop and route disposal. The fake WatchReplay,
SecondFeed and replay reactors are removed. The remaining non-pod error
definition moved into podwatch.go, so podwatch_fidelity.go and its registry
wrapper are removed. Originals are saved in the evidence directory and Git
history. Repository-wide references to the removed file are historical
audit text, not execution dependencies. No legacy Go test was removed.

Both candidate bodies passed before consolidation. A validated
`pre-consolidation-proof.json` was saved before folding them into one outline,
with both Running and Succeeded rows retained. The final matrix checks exact
row mapping, terminal events, failure reasons, complete disposer drains and
adapter/log hashes:

| Production fault | Old failing replay cases | Final failing rows |
|---|---:|---:|
| Do not reconnect a closed watch | 2 | 2 |
| Omit the resource version | 1 | 2 |
| Keep the stale initial version | 0 | 2 |
| Substitute a Get for replay | 0 | 2 |

The shared-setup guards also preserve the missing-inner-cancellation and
missing-field-selector detections, one each. In total, five prior
scenario/fault detections survive and five additional detections are gained.
`mutation-matrix.json` records old, candidate and final comparisons.

Authored replay bodies: two -> one outline; expanded suite: **575 -> 575**;
catalog: **1,076 -> 1,074 definitions**. Vet and both native suites pass
(26.980s), followed by **575/575** in the full Brine run. Production JetBridge
package coverage is **1,869/2,368 = 78.927365%**, above the 50% gate.
The three newly covered statements are the daemon-client unreachable branch,
not new watch statements; this run does not attribute that increase to replay.
The 144250ms report is summed scenario time, not wall time.

Profile: `/tmp/brine-coverage.d3v7ZB/coverage.out`, SHA256
`c72bb2f812f78c8afbcbdf9380b691f83f6993032da54e9dab6bbdefd0b60899`.
`gates.json` independently validates totals and links both proof files.
The normal adapter was restored byte-for-byte to the tested candidate.
Full-run CLI, adapter, API server and etcd PIDs have exited, and
`git diff --check` passes. Changes remain local and uncommitted on core.

Only fallback and non-pod error delivery remain fake-backed in the watch
family. Those, other lifecycle/runtime doubles, host execution and actual
CI image/cluster validation keep the full migration goal active.


## Real watch fallback checkpoint (2026-09-10)

Evidence: `/tmp/brine-watch-fallback.RsoafF`. The existing fallback scenario
now establishes a real API watch under a namespace-scoped RBAC identity,
revokes only watch permission, verifies actual Watch requests are Forbidden
while Get remains available, and interrupts the established TCP stream.
After the real pod status changes, production PodWatcher must return the
current pod using its fallback Get. The fixture never fabricates a watch
response, resource version, or API error. Envtest has no kubelet; the status
update is test input to the real API, not evidence of container execution.

The Role and RoleBinding have UID-protected, recorder-owned cleanup. The
watch and transparent route are also owned and drained. A shared checkpoint
helper proves that the runtime consumed an API-assigned version through its
live stream before interruption; the replay scenarios use the same helper.
The two fake fallback definitions and their reactor/counter imports are
removed. Two real-access definitions replace them, and existing real phase
update/assertion language is reused: **575 scenarios and 1,074 definitions,
both unchanged**. No legacy Go test or behavioral scenario was deleted.

Preserved pre-edit binaries and exact feature copies provide the baseline.
The before/after matrix verifies explicit executable paths, SHA256 hashes,
exact scenario rosters, intended assertion failures and complete recorder
drains, not merely nonzero exits:

| Production fault | Old failures | Real replacement failures |
|---|---:|---:|
| Disable fallback Get | 1 | 1 |
| Return the cached initial pod | 1 | 1 |
| Get the wrong pod during fallback | 1 | 1 |
| Retain a stale watch version (replay guards) | 2 | 2 |
| Do not reconnect (replay guards) | 2 | 2 |

All controls pass; all seven prior scenario/fault detections are preserved.
The fallback failures distinguish an explicit error, stale Pending phase,
and NotFound for the wrong identity. Both replay rows still distinguish
stale versions and missing reconnection after checkpoint extraction.
`mutation-matrix.json` SHA256:
`f829c869b21932e7a960ce8a8a5a9b2103cc67bbac835a53bd116c543e01dc9e`.

Final gates: vet passes; both native suites pass in 27.821s; full Brine
passes **575/575**. Production JetBridge package coverage is **1,866/2,368 =
78.800676%**, above the 50% gate. The only covered/uncovered delta from the
previous full run is the three-statement daemon-unreachable branch at
`daemon_client.go:224.18,228.5`, not watch coverage. The 144602ms report is
summed scenario time, not wall time.

Profile: `/tmp/brine-coverage.pUgbOG/coverage.out`, SHA256
`4c502847d353b73d92653462faf0ce6d2243a8ea74905365a3fafd7739657b71`.
`gates.json` independently validates totals, profile and matrix hashes, and
that the restored adapter matches the tested candidate. The full-run CLI,
adapter, API server and etcd processes have exited. Changes remain local and
uncommitted on core.

Non-pod error delivery is now the sole fake-backed watch scenario. It,
other lifecycle/runtime doubles, host execution, and CI image/cluster
validation remain unfinished; the complete migration goal stays active.


## Watch expiry fidelity finding (2026-09-10)

The last fake-backed watch scenario cannot yet be claimed migrated. A real
API reproduction found a production recovery defect that its fabricated
event sequence masks. Evidence: `/tmp/brine-watch-expiry.Xk4ZAf`.

The existing exact scenario, "A watch error is stepped over, not mistaken
for a pod", passes all six steps in `old-control.log`. Its fake watcher
queues a Gone Status followed by a Pod on the same stream. Real Kubernetes
instead emits a terminal ERROR Status with reason Expired/code 410 and
closes the stream when the requested history has been compacted.

The standalone `probe.go` starts its own disposable envtest control plane
with watch caching disabled, creates a real pod and lets production
PodWatcher read its initial version. It changes the pod to Running and then
Succeeded, reads the actual etcd revision, and physically compacts only that
owned etcd instance. A raw production WatchPod call verifies the real
ERROR/Expired/410 event followed by channel closure. PodWatcher.Next then
times out after one second, while a direct API Get still returns the
Succeeded pod. This reproduced on two fresh control planes. The second
probe asserts the event type/reason/code, closure, deadline failure and
healthy direct read; its zero exit means "defect reproduced", not "runtime
recovery passed". Both owned control planes were stopped.

The source explains the failure: `watch.go:153` ignores the non-pod Status;
channel closure clears the watcher but retains the expired resource version.
Watch creation succeeds and resets the consecutive-error count before the
server emits its terminal stream error, so the existing creation-error
fallback does not recover this case.

Kubernetes's documented recovery is a fresh Get/List and restart from its
returned version:
https://kubernetes.io/docs/reference/using-api/api-concepts/#efficient-detection-of-changes

Checksums:

- `probe.go`: `28beec5cef8d8c0794962d023520edcfdec2c21b5f2eb20d540919d513a15031`.
- `verified-probe.log`: `3ba44717cf70f22c2cd632dcd8cd1b88bf54c1b8e20cc13c7d38560c088325eb`.
- `old-control.log`: `c0816b07b14d7508ef573bf55cc9123e78db70789731cb491b02249228afcf2a`.

No production source or behavioral test was changed in this investigation,
and the fake scenario has not been deleted or weakened. A production expiry
recovery fix needs approval before proceeding beyond test migration. Once
authorized, the replacement must assert fresh-state recovery, subsequent
watch continuity and detection of the old non-pod type-confusion fault as
well as the newly exposed expiry-recovery fault. The full goal remains
active; this finding is not a claim that all remaining work is blocked.


## Shared real container database failures checkpoint (2026-09-10)

Evidence: `/tmp/brine-container-db.PdFjKW`. Four database-failure/adoption
scenarios in container-run.feature now use the existing real WorkerReady,
container request and ContainerOutcome assertions instead of a separate
RunExtraDBOutcome fixture. Their scenario names and distinguishing checks
are retained. Three small WorkerReady refinements express a closed worker
connection, another worker's existing handle, and a row left in creating.

The lost-connection case still closes a real PostgreSQL connection after
loading the worker; its connection is closed even if lookup fails. Duplicate
ownership still fails against the real unique constraint. Failed adoption
now uses the existing real CHECK-constraint vocabulary to prevent a created
transition while permitting the failed transition. The scenario-owned DB
resource drops that constraint with the database. No database method or
error response is substituted.

Removed: runExtraStaleCreatedFails, runExtraCreatedFails, the fake clientset
in runExtraRequest, RunExtraDBOutcome, its duplicate request/row assertions,
and the obsolete setup helpers. A repository-wide consumer scan found no
remaining references to those symbols. Before sources are preserved in the
evidence directory. The family reuses the same real API worker setup as
worker.feature; these assertions concern database behavior, not kubelet
execution. Other container runtime/metric doubles remain separate work.

Eight old definitions are replaced by three refinements: **1,074 -> 1,069
catalog definitions**. The two changed Go fixture files shrink by 188 lines
net relative to the preceding checkpoint. **All 575 scenarios remain**; no
legacy Go test or behavioral scenario was deleted.

The five-case selection includes the existing fresh-container failure case
as a guard of the shared vocabulary. Preserved pre-edit binaries and exact
feature copies were run before and after against these production faults:

| Production fault | Old failing cases | New failing cases |
|---|---:|---:|
| Mislabel a lookup failure | 1 | 1 |
| Mislabel an insert failure | 1 | 1 |
| Refuse adoption of an existing creating row | 2 | 2 |
| Omit marking failed completion for collection | 2 | 2 |
| Write creating instead of created in the real DB transition | 2 | 3 |

All controls pass. Eight prior scenario/fault detections are preserved;
the additional detection is the failed-adoption case now exercising the
real DB transition instead of the wrapper's unconditional error. The
validator checks exact scenario rosters, failure messages and steps,
terminal totals, explicit binary paths, SHA256 hashes and complete recorder
drains. `mutation-matrix.json` SHA256:
`92b3b2874ab3a672992f1a49d54b6f26ce87e8508c70fe8a267a3cebddaae999`.

Final gates: vet passes, both native suites pass in 28.284s, and full Brine
passes **575/575** with **1,866/2,368 production JetBridge statements =
78.800676%**, above the 50% gate. The 144301ms report is summed scenario
time, not wall time. Profile: `/tmp/brine-coverage.AmdE5B/coverage.out`,
SHA256 `81f8ed6f0456bcc9484d2b4f7877d4e2e87ac694a35712beaa448807ec0c0c0d`.
`gates.json` independently validates totals and proof hashes; the restored
normal adapter matches the tested candidate. Full-run CLI, adapter, API
server and etcd processes have exited; git diff --check passes.

Changes remain local and uncommitted on core. Production watch.go remains
untouched pending approval for expired-watch recovery. Remaining doubles,
host execution, and actual CI image/cluster validation keep the complete
migration goal active.


## Real pod-creation metrics checkpoint (2026-09-10)

Evidence: `/tmp/brine-container-metrics.78tkbN`. Container creation metrics
now use the existing real WorkerReady, real API admission, and actual pod
observations. The client-go creation reactor is gone from container_extra.go,
along with its testing/runtime imports. Exec mode uses ProducerExecutor
(the production SPDY implementation), not localExecutor. Run returns before
Wait; these cases prove pod admission and metrics, not kubelet execution.
Direct mode remains explicit to preserve the existing fallback contract.

For refusal, the fixture deletes only its generated namespace with an
API-assigned UID precondition and verifies its deletion timestamp before
requesting a pod. Kubernetes NamespaceLifecycle admission rejects creation
in that terminating namespace. No API error response is fabricated. The
existing recorder cleanup removes scenario pods; the suite-owned API server
owns the namespace's final disposal. Success assertions require a real
API-assigned pod identity. Both outcomes also compare the counters with the
actual pod count and returned Run error, rather than counters alone.

Three metric actions become one parameterized action. The original three
case bodies were first run separately against the real fixture, alongside
one new exec-refusal case. Paired proof was saved before consolidating them
into one four-row outline, "Pod creation counters describe the API outcome".
All original combinations remain; the new fourth row fills a real gap:
exec-mode creation failure was previously absent from this metric family.
Catalog definitions: **1,069 -> 1,067**. Expanded suite: **575 -> 576**;
authored metric bodies: **three -> one outline**.

Each counter fault is isolated in production Container.Run, with explicit
binary paths and old/candidate/final row mapping:

| Production fault | Old failing cases | Final failing rows |
|---|---:|---:|
| Omit direct success increment | 1 | 1 |
| Omit exec success increment | 1 | 1 |
| Omit direct failure increment | 1 | 1 |
| Omit exec failure increment | 0 | 1 |
| Count direct failure as creation | 1 | 1 |

All controls pass. Four prior scenario/fault detections are preserved and
one is added. The validator checks exact rosters, all three step events,
intended Then failures, complete recorder drains, terminal totals and
binary/log hashes. Pre-consolidation proof SHA256:
`6d32a4bd47b737d9cd7154988abc10792b8216e5f2e2b476b2b0385876d51a9f`.
Final matrix SHA256:
`7f4d467d74aa0dc0cb42786ea9e4602cb171d4da1a8bb181860255d02d10630b`.

Final gates: vet passes, both native suites pass in 27.829s, and full Brine
passes **576/576**. Coverage is **1,869/2,368 production JetBridge statements
= 78.927365%**, above the 50% gate. The three added statements are exactly
`container.go:172.18,176.5`, the exec-mode creation failure path. This is
not the daemon-unreachable coverage variation seen in earlier checkpoints.
The 144549ms report is summed scenario time, not wall time.

Profile: `/tmp/brine-coverage.vgkrLq/coverage.out`, SHA256
`cb75ee57b4ff315b18bda129c7421aad9436cc0454b301ce260b9f48ef5cbcb9`.
`gates.json` independently validates coverage, suite totals, proof hashes
and the restored normal adapter's identity. Full-run CLI, adapter, API server
and etcd processes have exited; git diff --check passes.

No legacy Go test was deleted. In particular, the retained leaf "Container
Run metrics when pod creation fails increments FailedContainers"
(JB-container-052) also asserts a non-nil Run error. Its historical pairing
was refuted. The new Brine assertion now covers that requirement, but
removal still needs an exact paired Go/Brine replay including a fault that
swallows the returned error without changing either counter. The current
counter-only matrix does not establish that deletion proof.

Changes remain local and uncommitted on core. The expired-watch production
fix still awaits approval. Remaining runtime/test doubles and actual CI
image/cluster validation keep the full migration goal active.


## Per-test metric retirement checkpoint (2026-09-10)

Evidence: /tmp/brine-metric-retirement.8xzjsI. Removed exactly the retained
Go leaf "Container Run metrics when pod creation fails increments
FailedContainers" (JB-container-052), plus its three unused metric/reactor
imports. No production source, Brine definition or feature changed.

Before removal, both the exact Go leaf and Brine's existing "Pod creation
counters describe the API outcome (row 3)" passed control and failed each
of three independent production faults in Container.Run's direct creation
failure branch:

| Fault | Distinguishing assertion in both suites |
|---|---|
| Remove FailedContainers.Inc | Failure delta must be one |
| Add ContainersCreated.Inc | Creation delta must remain zero |
| Return nil error with both counters unchanged | Run must report the failure |

The error-only fault closes the historical refutation: counters alone had
not proved the Go leaf's returned-error obligation. Brine now derives the
refusal from real Kubernetes admission in a terminating, suite-owned
namespace, and compares real pod observations, counters and the returned
error. No fake creation response is used in the replacement.

The saved pre-deletion validator requires one selected Go leaf, exact
hierarchy, intended leaf failures, four exact Brine rows, only row 3 failing
for each mutant, successful setup steps and complete recorder drains.
Production overlays touch only container.go; binaries are named explicitly.
Proof SHA256:
`af4b33a031412b4cecadac5956260721855680ebcd5bddc02f7b017eb0413117`.
All referenced binary, source and report hashes were rechecked after removal.

Initial anchored Ginkgo filters omitted the suite description and selected
zero tests. Those go-* reports are INVALID and excluded. Corrected
`go-fixed-*` runs use fail-on-empty and the full suite-prefixed name,
selecting exactly one leaf. The first post-removal compile then exposed two
unused reactor imports; it is not a passing run. Those imports were removed,
and the corrected full-package run passed, exit 0: 98/98 Ginkgo specs,
28.262 seconds Ginkgo runtime, 1m1.947s total command runtime. The report
`after-go-fixed.json` has exactly the previous 99-spec roster minus this
leaf. AST verification independently confirms all 36 remaining test bodies
in container_restored_test.go are unchanged (37 before removal).

No full Brine rerun was claimed for this Go-only cleanup. The normal adapter
still matches the metric checkpoint's verified binary SHA256
`8e7c5e577df70923c6ddb9c95562db0be7e1eaf4d4fafb363082846fc4f407b0`;
that checkpoint's 576/576 and 1,869/2,368 statement coverage remain historical
evidence, not new measurements. DISPOSITION-jetbridge.md now marks the leaf
DELETED and preserves the earlier refutation/restoration history.

Brine source was separately fast-forwarded to 42ae8316; its Go SDK,
CLI/core/engine/dispatch, conformance goldens and execution-document contract
are unchanged from the installed 6c66f538 pin. The dependency/image pin has
not been silently advanced. All migration changes remain local on core.
Remaining behavioral doubles, the unresolved expired-watch defect and CI
image/real-cluster validation still prevent completion of the full goal.


## Real container-spec checkpoint (2026-09-10)

Evidence: /tmp/brine-container-spec.U0Azyw. All seven expanded container-spec cases now
use the existing real Kubernetes/PostgreSQL WorkerReady setup and the existing
"the worker prepares task" transition. Their old fake-cluster setup is gone
from this feature. No new step definition, scenario, example row or assertion
was added or removed. The feature diff changes only its four setup pairs.
The shared run step now uses the carried TeamID rather than the literal 1.

These cases preserve their direct-mode contract: they construct a pod through
Container.Run and inspect what the real API stored. No executor is installed
on the worker, no Wait is called, and this does not claim that envtest executes
containers. The worker setup creates a unique namespace and registers actual
pod cleanup; every new control and mutant records a complete disposer drain.

Both controls pass 7/7. Six isolated mutations of production container.go
preserve all 15 scenario/fault detections, including identical assertion text
and errors before and after:

| Fault | Same failing cases before and after |
|---|---:|
| PullAlways instead of PullIfNotPresent | 1 |
| Wrong main-container name | 7 |
| Leave image prefixes intact | 3 |
| Omit the container environment | 1 |
| Omit the process environment | 2 |
| Reverse environment precedence | 1 |

The validator checks exact seven-case rosters, process exits, run totals,
successful setup/action steps, intended assertion failures, complete drains,
explicit binary wrappers and production-only overlays. It also verifies that
the feature changed only in setup and the step implementation only in TeamID.
Matrix SHA256: `5ebf6008323e9141652b8a5ff50043d0414420ad434cf1e08be81d379e6e10fa`.
The initial attempt to create all evidence files in one patch exceeded the
process argument limit and did not run tests; per-file patches succeeded.

Final vet passes. Both native suites pass in 27.755s. The full suite passes
576/576; coverage is 1869/2368 production JetBridge statements
(78.927365%), unchanged and above the 50% gate. The reported
144618ms is summed scenario time, not wall-clock duration. Profile:
`/tmp/brine-coverage.cO7dFT/coverage.out`, SHA256
`99c8c7e69dd83854d39e4a0977f3be51985abde5a76dfc1fa417166a540d2b30`.
The normal adapter was restored and matches the new-control mutation binary:
`4252a0af874717820d9f7d59161b2b58b526b76abe27e87494d5782127a017e4`.
The independent gates.json validates coverage and report/binary hashes.

No legacy Go test was removed in this checkpoint. The fake fixture remains
for other families, so this is seven migrated consumers, not a claim that the
shared fake infrastructure is gone. NewStubVolume also needs semantic rather
than name-based treatment: volume.go constructs the actual production Volume
placeholder, and worker.go's no-executor path returns it. Its own I/O-refusal
contract is not an injected fake response; uses substituting it for a working
artifact volume must still be examined separately.

Changes remain local and uncommitted on core. Remaining behavioral doubles,
expired-watch recovery and CI image/real-cluster validation remain unresolved.


## Real direct-run and shared-spec checkpoint (2026-09-10)

Evidence: /tmp/brine-direct-run.YHdTo2. Four container-run setups now use the existing
real Kubernetes/PostgreSQL worker and task-draft vocabulary: direct command,
seccomp, absent workspace, and post-Run volume binding. No scenario, example
row, assertion or step definition was added or removed. The first three keep
the direct-mode fallback contract; the binding case installs production SPDY.
None waits for a process or claims that envtest executes containers.

All five authored callers of "the container is created but not yet run" now
provide a production transport (one caller is a two-row outline). The action's
localExecutor fallback is removed; absent transport is an explicit error.
The nine-case control includes those callers and the three direct-mode cases,
so the migration and the shared action's other callers are exercised together.
Scenario-owned pod cleanup drains completely in all new runs.

Two copies of draft-to-ContainerSpec construction became one helper,
containerSpecFromDraft, used by three actions. The original complete builder
is preserved after indentation/blank-line normalization and the necessary
helper return-type/result adaptation. The removed copy had hardcoded TeamID=1
and omitted both disk-limit fields; all actions now use the actual TeamID and
the complete limits. The two step files shrink by 27 net lines. This is a
fixture consolidation, not a production-code change or a new coverage claim.

Both old and new controls pass 9/9. Six independent production mutations each
fail the same one scenario at the same assertion, with identical diagnostics:
wrong direct command, wrong working directory, Unconfined seccomp, a valid
but unrequested volume, empty direct-process identity, and missing exec-mode
volume binding. The extra-volume mutant remains API-admissible; no mutation
is counted merely because setup failed. The validator checks exact rosters,
setup/action success, assertion failure attribution, exits, complete resource
drains, explicit binary wrappers and production-only overlay targets.
Matrix SHA256: `f000af1d8e5a4ee60f77462cd076dffba637a79a891a06ac47ae58a53433f765`.

The first native run caught an overbroad textual replacement in two unrelated
DB-crash setup sentences. Those sentences were restored, the vocabulary guards
were kept intact, and the corrected native run passes both suites in 26.982s.
The focused mutation specimen never contained those DB-crash scenarios; it
needed no alteration. An initial evidence-validator comparison also treated a
formatting-only blank line as code and guessed expanded outline titles;
corrected validation ignores blank lines and uses the runner's literal-title
plus row-index convention, while retaining exact nine-case roster checks.

Vet passes. The corrected full suite passes 576/576 and covers
1869/2368 JetBridge production statements (78.927365%),
above the 50% gate. The 144602ms report is summed scenario time, not wall time.
Profile: `/tmp/brine-coverage.FmCL4A/coverage.out`, SHA256
`c6b7e83dcf9995ec5134ef995dc7fe1e347d39ba452e18f55d495a34c01f454e`.
The normal adapter is restored and equals the paired new-control binary:
`1812bde39b225eb5b97bb928b5117501415ba1f78f5d5fa9763de426d0a88bef`.
The independent gates.json validates totals and report/binary hashes. All run
handles are terminal; no recent active Brine/API-server/etcd process remained
in the scoped process check. git diff --check passes.

No legacy Go test was deleted here. The shared fake client remains for other
families, and task-command execution still has separate substitute fixtures.
The full goal remains active; changes are local and uncommitted on core.


## Real pod configuration and API-owned QoS checkpoint (2026-09-10)

Evidence: /tmp/brine-pod-spec.uP7JC6. Twenty generic-fake-backed pod-spec cases now
use the existing real Kubernetes/PostgreSQL worker and task-draft vocabulary.
The two sidecar-log execution cases and custom cache/registry configuration
fixtures are intentionally unchanged and still require migration. No scenario,
example row or assertion was removed or added. No step definition was added.

The hand-written qosClassOf helper is deleted. It classified only the main
container; the real API assigns status.qosClass on creation, accounting for
the pod as a whole. The four QoS sentences now say "the API assigns the pod
QoS class" rather than claiming a scheduler ran. Their getter requires an
API-assigned UID, resource version and nonempty QoS status. Missing evidence
fails explicitly; there is no fallback calculation. Kubernetes's own
[Pod creation strategy](https://github.com/kubernetes/kubernetes/blob/master/pkg/registry/core/pod/strategy.go#L81-L98)
confirms this boundary, and the actual envtest controls verify it here.
The step file shrinks by 24 net lines. This is API admission/status evidence,
not proof of container execution, actual scheduling or eviction.

Both controls pass 20/20. Fourteen separate, compiling production container.go
mutations preserve all 22 previous scenario/fault detections:

| Production fault | Old detections | New detections |
|---|---:|---:|
| Extra unrequested volume | 4 | 4 |
| Relative scratch resolved from root | 1 | 1 |
| Persistent rather than ephemeral scratch | 1 | 1 |
| Doubled CPU limit | 2 | 2 |
| Doubled independent CPU request | 2 | 2 |
| Doubled disk limit | 1 | 1 |
| Phantom request on a resource-free pod | 1 | 1 |
| Unprivileged task allowed escalation | 1 | 1 |
| Privileged task denied privilege | 1 | 1 |
| RestartAlways | 1 | 1 |
| Extra unbounded sidecar | 3 | 4 |
| Incorrect sidecar working directory | 2 | 2 |
| Sidecar mounts omitted | 1 | 1 |
| Unrequested image credential | 1 | 1 |

The extra detection is substantive: the Guaranteed case now sees the API's
Burstable classification after an unbounded sidecar is injected. The old
main-only calculation stayed green. The three original container-count
failures remain. This adds discrimination without adding a scenario.
All mutations pass setup and Run; failures occur at the intended assertions,
not because an invalid pod was rejected during fixture setup. New runs have
complete scenario pod-cleanup drains inside the suite-owned control plane.

The validator checks exact 20-case rosters, exit codes, run totals, intended
assertion mapping (normalizing only the renamed QoS sentence), complete drains,
explicit binaries and production-only overlays. It also verifies that the
feature changed only for the twenty named setup pairs and the four QoS lines.
Unordered mount diagnostics are not required to name the same first missing
path; the failing scenario and assertion are exact. Matrix SHA256:
`47aaed8af59971745f0865c8b4fce3ca92f2a2cbae8dc50d83883dcf298cb309`.

Vet passes. Both native suites pass in 33.281s. The full suite passes 576/576;
coverage is 1872/2368 JetBridge production statements
(79.054054%), above the 50% gate. The 145499ms report is summed
scenario time, not wall-clock runtime. The three newly hit statements are
exclusively daemon_client.go:224.18,228.5; none is lost. This is the same daemon
error-path variation observed earlier, not credited as new pod-spec coverage.

Profile: `/tmp/brine-coverage.qXywO9/coverage.out`, SHA256
`79c97ff607dbbcc3431b6c49ff76fbb337a8a0c7ede704864e8344c6d94989c2`.
The normal adapter is restored and matches the new-control binary:
`301051754731707ec131695952b7f3c560ed8e3213c3c7723008a510de29bc1d`.
The gates.json file independently verifies coverage and report/binary hashes,
and records the exact covered-block difference. All run handles are terminal;
no recent active Brine/API-server/etcd process appeared in the scoped check.
git diff --check passes. No legacy Go test was deleted.

The no-fakes goal is not complete: remaining execution/configuration fixtures,
the expired-watch defect and CI image/real-cluster validation still need work.
All migration changes remain local and uncommitted on core.


## Real configurable pod fixtures checkpoint (2026-09-10)

Twenty authored cases (21 expanded scenarios) in container-pod.feature now
use the existing real Kubernetes worker/database setup. Five whole-worker
configuration definitions became five WorkerReady refinements for pull
secrets/service account, private registry, standalone caches, cache mode and
artifact storage. Task/check/get drafts share workerContainerDraft, carrying
the real client, namespace, team and production execution transport.
No scenario, example row or assertion was removed; no definition was added.
The legacy newConfiguredWorker helper moved unchanged beside the shared
legacy fixture: two process-test callers still need it. It is not eliminated.

Evidence: /tmp/brine-pod-config.nWYFfa. Before/after controls pass 21/21.
Fourteen independent container.go mutations preserve all 24 scenario/fault
pairs, with identical failing assertion text and diagnostics: service account
(1), operator secrets (2), missing registry secret (1), duplicate registry
secret (1), ephemeral caches (4), wrong cache root (2), persistent check (1),
ephemeral step storage (4), omitted cleanup (2), omitted fetch (2), missing
node affinity (1), wrong overlapping input/output path (1), wrong output path
(1), and fetch-before-cleanup (1). All setups and actions pass; failures are
at assertions. All scenario cleanup drains finish without a partial result.

The validator checks exact rosters, terminal totals/statuses, assertion
attribution, drains, explicit runner binaries, production-only overlays,
unchanged feature content outside the named setup transformations, and the
unchanged moved legacy helper. Matrix SHA256:
`ad922a2c60d49906bb2530f0212a0cecdf6b45222555a4efa8ae379c8b0dd750`.

Vet passes; both native suites pass in 30.486s. The full suite passes 576/576,
with 1872/2368 production statements covered (79.054054%). The covered blocks
are identical to the preceding checkpoint. The reported 144640ms is summed
scenario time, not wall-clock runtime. Coverage profile:
/tmp/brine-coverage.orFd1A/coverage.out, SHA256
`0c2655ad78d0b51ac9cca722f784300777d3d84b7795e415b60f57240e394314`.
The restored normal adapter matches the new-control binary, SHA256
`391715c50e92ee3babc0f3ee1bd2fe6c68c5e64dc53b9f858369ae556c95665a`.

These cases verify API-accepted pod construction, not image authentication,
hostPath execution or actual scheduling. Existing irrelevant cache mode
"node" values in artifact-only cases were preserved; they do not establish
that production CLI validation accepts that value.

Fresh remote fetches confirm core, origin/core and origin/core-brine remain
at 3c787e9c70602baf2279c5ff92ad32253edcf68b. The Brine source checkout is
up to date at 42ae831675c4c3e02a4e7b0dafdd566ebb70c535. Its changes since
the installed 6c66f538 pin do not touch the Go runner or CLI/core/engine code.
The dependency and installed binaries remain at the validated 6c66f538 pin.
All current v5 changes remain local and uncommitted. The remaining doubles,
expired-watch recovery defect and old CI image still prevent completion.


## Real container recovery checkpoint (2026-09-10)

All five authored cases (seven expanded scenarios) in container-lifecycle.feature
now refine the shared real Kubernetes worker/database. Five independent
fake-backed setups became five WorkerReady transitions. Properties and
reattachment share recoveryContainer; pod inputs share recoveryPod. This
family no longer creates fake clients or local executors. The source file is
net 33 lines shorter. No scenario, row or definition was added or removed.
The sidecar-log family below it is byte-unchanged and remains legacy.

Check replacement now persists a valid pod, reports its terminal phase through
the real status API, and requires a replacement with a different API-assigned
UID. Its observation is a new unfinished pod, not a live pod: there is no
kubelet or command execution. Annotation-recovery inputs retain the API's
Pending phase rather than fabricating Running. These are actual persisted
inputs to production recovery, not evidence that Kubernetes ran a task.
The production SPDY executor is wired; successful Attach returns an exited
process without executing a command.

Evidence: /tmp/brine-container-recovery.CfRyHj. Controls pass 7/7 before and
after. Seven production-only container.go mutations preserve nine exact
scenario/fault pairs: retain Succeeded pod (1), retain Failed pod (1), corrupt
property read (1), ignore remembered exit (1), ignore exit annotation (2),
corrupt annotated exit (2), accept unfinished exec pod (1). Setup/actions
complete; all failures are at assertions, with identical diagnostics. Only
the replacement assertion's renamed sentence is normalized. Every scenario
resource drain finishes without a partial result.

The validator checks exact rosters, terminal codes/totals, assertion mapping,
drains, explicit binaries, production-only overlays, feature transformation,
and the unchanged sidecar-log suffix. Matrix SHA256:
55331f7a8d0b4f503717c17dd925caa40c275409a6b474087ef4b4f9d974a0f6.

Vet and both native suites pass (31.665s). The full suite passes 576/576 with
1869/2368 JetBridge production statements covered (78.927365%), above the 50%
gate. The 145505ms report is summed scenario time, not wall-clock runtime.
The only covered-block difference is three statements in
 daemon_client.go:224.18,228.5, the previously observed daemon error-path
variation; no recovery block lost coverage. Profile:
/tmp/brine-coverage.iEkGpz/coverage.out, SHA256
ea8767b7ad11ae5fe28dc8576c49e14ef05c62f75e9bd48b9af5b936c1c1ebb6.
The restored normal adapter matches new-control, SHA256
3aa427041eb7687ec31eb08a33d7f8de36612b8045f63f2154fe4ef25fd17ef0.
Gate hashes are recorded in gates.json. All run handles are terminal;
git diff --check passes.

No Go test was deleted. JB-container-055 remains retained: its exact assertion
requires "no completion status", while Brine accepts any Attach error. Its
hand-created unset-phase fake pod also differs from the real API's Pending
pod. The accept-unfinished fault does not close either historical fidelity
gap; narrower paired evidence is still required.

The no-fakes goal remains active. Process/watch/exec substitutes, the known
expired-watch defect, and CI image/real-cluster verification remain unfinished.
Docker and local Docker/containerd sockets were rechecked and are unavailable.
All migration changes remain local and uncommitted on core.

## Exact recovery refusal and legacy retirement (2026-09-10)

The retained JB-container-055 gap is closed. A probe against an owned real
kube-apiserver confirmed that creation defaults a pod to Pending and a status
update to the empty phase is accepted and read back unchanged. The negative
recovery case is now one two-row outline: Pending and unreported. It verifies
the persisted phase and UID before invoking production Attach. The original
Pending behavior remains; the added row covers the legacy fixture's actual
phase without a fake client. No new step definition was added.

The assertion now requires refusal from Attach itself containing
"no completion status". A correctly worded error from a later Process.Wait
cannot satisfy it. Positive recovery behavior remains unchanged.

Evidence: /tmp/brine-recovery-refusal.s8BAjP. Before mutation, the exact Go
leaf and new Brine rows pass. Both the Go leaf and the unreported-phase row
fail all five independent container.go faults:

- Accept the unfinished pod instead of refusing it.
- Refuse with an unrelated error instead of the missing completion status.
- Accept only the unreported-phase pod.
- Return an unrelated error only for that phase.
- Return a process from Attach and postpone the correctly worded refusal until Wait.

Before strengthening, Brine missed faults two, three and four; its old control
passed 7/7. The new control passes 8/8; broad faults fail both negative rows,
phase-specific faults only the unreported row. No old-Brine run is claimed for
fault five. All failures occur at the intended assertions; setup and actions
pass. All Brine resource drains complete without partial results.

The pre-deletion validator requires one exact Go leaf per run, fail-on-empty,
non-dry-run execution, correct failure locations, exact Brine rosters and
assertions, real phase-probe results, explicit binaries and production-only
overlays. Proof saved before removal, SHA256:
5fd523d6219e4e28e8aec0cdac1641925fdb37177f80105804ddc089aea318b0.

Only JB-container-055 and its private enclosing Attach setup were removed.
The full JetBridge package suite passes 97/97 Ginkgo specs (28.117s; command
1m1.844s). Its roster is exactly the previous 98 minus this leaf. AST comparison
preserves all 35 other leaf bodies in container_restored_test.go. The removed
source remains in Git and before-container_restored_test.go. DISPOSITION now
marks the row DELETED while retaining the historical refutation/restoration.

Vet and both native Brine suites pass (28.430s). Full Brine passes 577/577;
coverage remains 1869/2368 JetBridge production statements (78.927365%), above
the 50% gate. Every covered block is identical to the previous profile.
The 145792ms report is summed scenario time, not wall-clock runtime.
Profile: /tmp/brine-coverage.l6X8ea/coverage.out, SHA256
43c45356e8399c2c93517fd0bb29df43d738fc9c6e819319043d3773c5b848a4.
The restored normal adapter matches the new control, SHA256
ff86d374139ba3cf6b1c0e666af788cdb71285547cc62500ac559563edde1270.
after-proof.json verifies roster/source preservation and gate hashes. All run
handles are terminal. git diff --check passes. No production source was edited.

The wider goal remains active: remaining process/watch/exec substitutes,
expired-watch recovery and CI image/real-cluster validation are unfinished.
All v5 migration changes remain local and uncommitted on core.

## Failure-test fidelity and duplicate-row consolidation (2026-09-11)

Two false-green premises were reproduced before editing:

- The deletion case deleted before Process.Wait opened its watch. Its error
  was three consecutive API errors during initial sync, not a deletion
  diagnostic. Its broad substring assertion passed because the pod handle
  itself contained "deleted". Renaming only that handle to vanished-pod made
  the same control fail at Then.
- The image-pull priority case supplied Pending with no terminal exit path.
  Moving exit handling ahead of failure handling left it green. Changing only
  its phase to Failed supplied the competing fallback exit code: the control
  passed and that same production mutation failed at the intended assertion.

The priority case now retains Failed. The deletion case uses the existing
real WorkerReady/API/database fixture and a neutral handle. A transparent
RoundTripper forwards every request/response unchanged and exposes only the
successful, resource-versioned watch handshake for this namespace and pod.
That barrier orders an actual UID-preconditioned, zero-grace deletion after
Process.Wait establishes its watch. The API confirms NotFound, the waiter is
joined, and the assertion requires the external-deletion diagnostic prefix.
No API response, watch event, error, pod status or execution is synthesized.
The pod remains Pending. This exercises the direct compatibility watcher,
not production execProcess or actual Kubernetes command execution.

The duplicate three-row cannot-start outline was merged into the five-row
terminal-waiting outline. Its Given/When/Then text is identical after handle
normalization; all three slug/reason pairs already exist in the larger table.
The validator checks those exact relationships and preserves the union of
RF-04/RF-01/RF-02/RF-03 tags. One authored outline and three expanded rows are
removed; no step definition is added. Removed feature text is recoverable
from Git and old-cases.feature in the evidence directory.

Evidence: /tmp/brine-failure-fidelity.1mIBct. Full-family controls pass 18/18
before and 15/15 after. Four production-only process.go mutations show:

- Exit-first: both prior OOM/eviction failures remain; corrected image-pull
  priority adds one failure.
- Unclassified waiting reason: all nine old failures map to six retained
  failures, with the three duplicate rows mapping to the same reason rows.
- Wrong deletion diagnostic: old case green, real replacement fails.
- Swallowed deletion: old case green, real replacement fails.

Every old scenario/fault pair has a retained mapping (11 pairs, eight distinct
after duplicate mapping); three new discriminating pairs are added. Setup and
actions pass; faults fail at the intended assertions. All resource drains are
complete. Matrix SHA256:
d3d6b601fee7c208ffb00115edb9e71fef256cb277a2d0f3f9c3843ab5426dc4.
The validator also checks exact rosters, unchanged remaining steps/tables,
explicit runner binaries, production-only overlays and all six counterexample
runs. Historical disposition rows JB-behavioral_runtime_spec-017 and
JB-process-009 now flag the corrected Brine evidence; their deleted Go leaves
were not replayed or freshly revalidated in this checkpoint.

Vet and both native suites pass (27.590s). Full Brine passes 574/574; coverage
is 1873/2368 JetBridge production statements (79.096284%), above the 50% gate.
No previously covered block is lost. The four added statements are exactly
process.go:175.51,182.5 (external deletion diagnostics/return, three statements)
and process.go:635.2,635.17 (nonterminal exit check, one statement).
The 145646ms report is summed scenario time, not wall-clock runtime.
Profile: /tmp/brine-coverage.oHn5uq/coverage.out, SHA256
bbcd005583e20a9dcb35f785c4592259b75554f17902b865c5537f43c7daa39a.
The restored adapter matches new-control, SHA256
b8a5a73079cf03e35fd7b36c373449e6829b859b9da06c17bc80158ecf50ddbb.
All run handles are terminal; the scoped process check finds no recent active
Brine/API-server/etcd process. git diff --check passes. No production source
or legacy Go test was changed in this checkpoint.

Other failure/process/exec fixtures still use doubles; their real-API migration
remains unfinished, alongside expired-watch recovery and CI/real-cluster
validation. The goal remains active, with all work local and uncommitted.

## Direct failure-state fixtures use the real API (2026-09-11)

Twelve expanded failure-priority cases (eight authored setups) now use the
existing real WorkerReady/API/database fixture. Together with the preceding
real deletion case, thirteen of this feature's fifteen cases use the real
API. The scheduling and severed-exec cases still use their legacy fixtures.
The migrated cases report pod status through the actual status subresource;
they do not claim that envtest runs a kubelet, causes an actual OOM/eviction,
or executes a container. They exercise the direct compatibility Process.Wait
path; production task execution uses execProcess.

StepRunning and ProcessOutcome now carry kubernetes.Interface. The existing
described-container transition reuses containerSpecFromDraft, including the
real team ID, instead of maintaining a second partial spec builder and
requiring a fake client. Three remaining reactor callers explicitly require
the legacy fake client; real API state cannot silently fall back to a fake.
The common settlePod helper reuses updateTaskPodStatus, and the waiting-state
transition reuses settlePod rather than duplicating update/wait/report logic.
The sidecar case now declares the sidecar in the actual pod spec as well as
reporting its status. No step definition, scenario, example row or assertion
was added or removed, and no legacy Go test was deleted.

Evidence: /tmp/brine-real-failures.tNH85N. The previously launched old/new
matrices were recovered as completed runs, not restarted. Both full-family
controls pass 15/15. Ten production-only process.go overlays preserve the
same eighteen scenario/fault pairs, including identical failed assertion
text and diagnostics:

- Exit handling before failure detection: image-pull priority, current OOM,
  and eviction (three cases).
- Lost terminal-waiting reason: image-pull priority and five waiting rows
  (six cases).
- Waiting before OOM, or ignoring prior OOM: the OOM-versus-crash case
  (one case for each independent fault).
- Wrong OOM detail and wrong eviction detail: one case each.
- Missing diagnostics: current OOM and eviction (two cases).
- Wrong succeeded/failed fallback: the corresponding no-status case
  (one case for each independent fault).
- Taking the sidecar's exit code: the main-container exit case (one case).

validate.js checks exact old/new scenario rosters, the complete allowed
feature transformation, unchanged definitions, explicit manifest/binary
selection, production-only overlays and identical assertion failures.
All setup/action steps pass; the faults fail at Then/And assertions, not API
validation. All recorder drains complete without partial results, and each
of the twelve migrated cases drains a real disposer. Matrix SHA256:
cafefbca887b209743a9eea156de898e864d811e5795cb8724faedcea0b1f5bc.

Fresh vet and both native suites pass (27.728s). The full suite passes
574/574. Brine-only JetBridge production coverage is 1873/2368 statements
(79.096284%), above the 50% gate. The exact covered block set is unchanged
from the preceding checkpoint. The 145740ms report is summed scenario time,
not wall-clock runtime. Profile: /tmp/brine-coverage.h7vktW/coverage.out,
SHA256 63c520125644b363ef030eaac69a16f29af614b3bdb6a0033b2441fdfdaa3f61.
The coverage script restored the same normal adapter as the new control,
SHA256 a724f82b6ac30623a083f8bfe100f1029878fefaf796ccd7acbc575862ab0e0f.
verify-gates.js and gate-proof.json preserve those checks. All run handles
are terminal; the scoped process check found no recent active Brine/API/etcd
process. git diff --check passes. No production source was changed.

This is not a double-free or CI-ready completion claim. The broader legacy
process/watch/exec substitutes, expired-watch recovery and matching CI image
with real-cluster validation remain unfinished. The goal stays active; all
v5 work remains local and uncommitted on core.

## Real scheduling and startup timeout fixtures (2026-09-11)

Both timeout cases now use the existing real WorkerReady/API/database
setup and the production SPDY executor. Their pods never reach Running,
so no command is executed and no fake/host executor supplies a result.
The real API stores the explicit reported Scheduled or Unschedulable status;
this is a runtime deadline test, not evidence that envtest runs a scheduler
or kubelet. The old scheduling action no longer constructs NewCluster, and
the old impatient-worker setup no longer installs localExecutor.

One shared WorkerReady refinement declares startup/scheduling budgets:
200ms/200ms for startup, 2000ms/3000ms for scheduling, unchanged from the
old fixtures. It uses the real worker's team ID and production transport.
An independent fixture context (the larger configured duration plus five
seconds) is registered for disposal; caller cancellation cannot substitute
for the required runtime-timeout message. The startup case reuses the
common container draft/start chain. No case or example row is removed,
and the net step-definition count is unchanged. The obsolete RF-07-not-
migrated note and inaccurate timer-poll description were removed.

Evidence: /tmp/brine-real-timeouts.SS5nlt. Before editing, dropping the
waiting warning or replacing the detailed scheduling reason with an
unspecified capacity issue left both old cases green. Three existing
StepOutcome checks now require the detailed reason and both warning
phrases, waiting up to and cluster resources. No new assertion helper
is needed. Both old and new controls pass 2/2. Five production-only
process.go mutations preserve three prior pairs and add two:

- Wrong scheduling classification: scheduling fails before and after.
- Wrong startup classification: startup fails before and after.
- Missing diagnostics: startup fails before and after.
- Missing waiting warning: old scheduling passes, new scheduling fails.
- Missing scheduling detail: old scheduling passes, new scheduling fails.

Every pre-existing failure has identical assertion text and diagnostics
after migration. New failures occur at the added assertions, with preceding
setup/actions passing. All drains complete without partial results; each
new case drains real pod cleanup and context cancellation. validate.js
checks both selected cases against their full feature files, all neighboring
scenario steps/tables, unchanged rosters, explicit binaries/manifests,
production-only overlays and retained Go source equality. Requirement tag
sequences are also unchanged. Matrix SHA256:
2d67a49c1a9683fa54dfc1c4a9da5d9938bce130d822dbe6745e09d4daf2b67d.

The exact retained Go leaf, Process (restored) execProcess failure state
detection waits for Unschedulable pod and times out (JB-process-023), ran
with the module-pinned Ginkgo CLI, anchored focus and fail-on-empty. Its
control passes; wrong-scheduling and no-warning each fail at that It's own
assertion. Each report selects exactly one leaf of 97, not an empty or
whole-suite substitute. The disposition records the warning-gap closure,
but RETAIN stands: two paired faults are not a complete replacement audit,
and the Go fixture's ResourceType=git differs from Brine's ImageURL=busybox.
No legacy Go test was changed or deleted.

The first temporary wrong-scheduling overlay failed compilation because its
replacement left msg unused. It was corrected before any baseline tests
ran; only corrected compiled binaries and terminal logs are evidence.

Fresh vet and both native suites pass (28.469s). Full Brine passes 574/574;
coverage is 1873/2368 JetBridge production statements (79.096284%), above
50%. No covered block is gained or lost against the preceding checkpoint.
The 145871ms figure is summed scenario time, not wall runtime. Profile:
/tmp/brine-coverage.C4bPFi/coverage.out, SHA256
0a5019a30487114dfdbfbcac3a16093effe41b8e8e6a83ffe989472dba00226d.
The restored normal adapter matches new-control, SHA256
66aab3c3742408fdb32d58aa98d167565177ca7aee7369ecb9c94b48fea45858.
verify-gates.js and gate-proof.json preserve the final checks. All handles
are terminal, the scoped process check is clear, and git diff --check passes.

Fourteen of failure-priority.feature's fifteen cases now use the real API;
the severed-exec case is its remaining legacy fixture. Other process/watch
and runtime substitutes, expired-watch recovery and CI image/real-cluster
validation still prevent goal completion. No production source was edited,
and all v5 work remains local and uncommitted on core.

## Real pod-lifecycle state and diagnostics (2026-09-11)

Twenty-six more expanded cases in pod-lifecycle.feature now use the real
WorkerReady/API/database setup. Fourteen authored setups change; all nineteen
scenarios/outlines and thirty-two expanded cases remain. Together with the
already-real startup-timeout case, twenty-seven use the real API. The three
transient-read rows and two severed-exec cases retain explicit legacy
fixtures. Their behavior remains in the whole-family validation.

The old fake-cluster execution-mode factory is replaced by a WorkerReady
refinement. Production selects the real SPDY executor; direct compatibility
selects no executor, preserving the distinct Process.Wait path. Both retain
an independently bounded context with registered cancellation. The regular
container setup now reuses workerContainerDraft and the described-container
start transition; the requested image, working directory, sidecars, failure
inputs and assertions are preserved. No step definition or case is added
or removed. The API-error rows explicitly select the existing fake-cluster
Given instead of borrowing the now-real mode-selection step.

The two node fixtures use createRealNode with UID-preconditioned cleanup,
then separate Node Update and UpdateStatus calls. The old fake client had
accepted initial status on Create. The real fixtures persist the same spot
label, cordon flag, Ready/DiskPressure conditions and messages through their
actual API surfaces.

The create-pod reactor is removed from settleProcessOnEveryPod. Its real
replacement reads the API pod, verifies UID/resource version, binds the
requested node through the Binding subresource and reads that assignment
back on the same UID. It then reports failure through UpdateStatus and
verifies the returned identity and phase. Each observed UID is updated once.
A bounded loop discovers replacement pods created by production; the fixture
never creates them, substitutes an API response, or fabricates watch events.
It joins Process.Wait on success and after cancellation/error. This also
fixes the old fixture's spec-via-UpdateStatus shortcut, which a real API does
not persist. The two remaining get-error reactors stay explicitly legacy.

These are reported-state tests: envtest runs no kubelet or scheduler, and
no container command executes. Production's replacement-pod path now meets
actual API creation, binding and status persistence. The tests assert the
same results, diagnostics, metrics and cleanup as before; they do not claim
an independent replacement-count contract or real Kubernetes execution.
The existing compatibility cancellation case still cancels before calling
Wait; this migration does not claim to fix its previously documented race.

Evidence: /tmp/brine-real-lifecycle.hkvt7M. The complete old/new controls and
an initial real-control probe pass 32/32. Ten production-only process.go
mutations preserve all forty-four scenario/fault pairs:

- Wrong exit code: two cases.
- Missed completion deletion: three cases.
- Missed cancellation deletion: one case.
- Lost terminal-waiting reason: ten cases.
- Missing pod diagnostics: twelve cases.
- Missing node diagnostics: six cases.
- Missing pod node name: five cases.
- Missing restart count: two cases.
- Missing image-pull counter: two cases.
- A late sidecar failure overriding main completion: one case.

validate.js checks the full allowed feature transformation, unchanged
scenario/example/tag rosters, identical failing assertions, explicit runner
manifests/binaries, production-only overlays, and the remaining reactor
boundary. Diagnostic comparisons normalize only generated worker namespace
prefixes before a slash to the old test-namespace prefix. No other message
content is normalized. Every setup/action passes; mutations fail at the
intended assertions. All recorder drains finish without partial results;
each newly migrated case drains its real pod cleanup and cancellation, with
node cleanup included where needed. Matrix SHA256:
86651afbf1c439c342eaccc313db43a0cf94d397923e15b97fe329fa9fb5c422.

Fresh vet and both native suites pass (30.094s). Full Brine passes 574/574;
coverage is 1873/2368 JetBridge production statements (79.096284%), above
the 50% gate. The covered block set is unchanged. The 146928ms report is
summed scenario time, not wall runtime. Profile:
/tmp/brine-coverage.wLLMUI/coverage.out, SHA256
60670c58f61428f0b73d3cb1b39933478ecacb93319ad5c54bf3b93a16c0cee3.
The restored normal adapter matches new-control, SHA256
8f94e5962c1243f81c8f5c326ab4d858dfd2ff774f2322931438fca22e910ebc.
verify-gates.js and gate-proof.json preserve these checks. All run handles
are terminal; no recent active Brine/API/etcd process was found, and
git diff --check passes.

No production source or legacy Go test was changed. The remaining five
legacy cases in this feature, other runtime/test substitutes, expired-watch
recovery and matching CI image/real-cluster validation are unfinished. The
goal stays active, and all v5 work remains local and uncommitted on core.


## Real initial pod-read failures and exact retry boundary (2026-09-11)

The three RF-12/RF-13 cases now use a real API server, real pods and real
namespace-scoped RBAC denials. Both fake Get reactors and the now-unused
legacyProcessClient helper are removed from steps/process.go. The feature
still has 19 authored bodies and 32 expanded cases: 30 use real API fixtures;
only the two severed-exec cases retain legacy fixtures. Other families still
contain doubles, so this is not a double-free-suite claim.

The read and watch fixtures share newPodAccess, which creates a real Role,
RoleBinding and impersonated client and registers UID-preconditioned cleanup.
The existing watch identity, permissions and readiness probe are preserved.
For transient reads, the real pod's Succeeded status is persisted first, Get
permission is revoked, and access is restored after one or two actual 403
responses. deniedReadTransport forwards the original request and response
unchanged; it observes the fault boundary only to order the real Role update.
The restoration probe uses an unwrapped client with the same identity. No
response, error or watch event is fabricated, and no assertion reads the
observer's request history. Persistent denial leaves the real pod Pending.
The process outcome is read from compatibility Process.Wait; envtest runs no
kubelet and these cases do not execute a task command or prove production
execProcess behavior.

The initial replacement assertion had a measured false green: searching for
"3 consecutive API errors during initial sync" also accepted "13 consecutive
API errors during initial sync". The contains-limit-thirteen control in
/tmp/brine-real-reads.oo0gNz passes all three cases despite that production
retry-limit mutation. Its binary, feature snapshot, manifest, log and exit
status are preserved separately. It is a counterexample against the already
real fixture, not a claim that the original fake fixture was tested at 13.

The corrected assertion, "the initial pod read exhausts 3 attempts", requires
an error whose diagnostic starts with the exact count and classification,
including the colon boundary. It adds one definition; the two action
definitions replace their legacy equivalents one-for-one. No case is removed.
The same limit-13 mutation now fails only the persistent-denial assertion,
with both transient rows still passing. This closes the measured false green.

Before/after evidence: /tmp/brine-real-reads.oo0gNz. The original fake control
and final real control pass 3/3. Five production-only faults preserve all six
previously detected scenario/fault pairs and add three detections:

| Production fault | Original failing cases | Final failing cases |
|---|---|---|
| Retry limit 1 | Transient rows 1 and 2 | Both transient rows and persistent denial |
| Retry limit 2 | Transient row 2 | Transient row 2 and persistent denial |
| Retry limit 4 | None | Persistent denial |
| Wrong initial-error classification | Persistent denial | Persistent denial |
| Wrong recovered exit code | Transient rows 1 and 2 | Transient rows 1 and 2 |

The separate limit-13 counterprobe adds one more detected fault/case pair.
All setups/actions pass and mutations fail at their intended Then assertion.
Every recorder drain completes without partial results, including pod and
RBAC cleanup. validate.js verifies the unchanged feature/tag/example roster,
explicit binary selection, production-only overlays, failure attribution,
real Forbidden causes and unchanged production source. Runtime count and
classification prefixes remain comparable; the old fake error causes are
replaced by actual API authorization diagnostics, not claimed byte-identical.
Matrix SHA256:
270aff454dd59bd622ef8ffe2eb3b5811b2e60f3351b1dcb47f1085a2d8a2161.

Fresh vet and both native suites pass (28.450s). Full Brine passes 574/574;
coverage is 1873/2368 JetBridge production statements (79.096284%), above the
50% gate. The covered block set is unchanged. The 146791ms report is summed
scenario time, not wall runtime. Profile:
/tmp/brine-coverage.W9QcYx/coverage.out, SHA256
64167df46f5b4e550610995665cb11c848847894c0fd9f824b0161d35a536a05.
The restored normal adapter matches final new-control, SHA256
2895c528fc38b5370076e5c37fd34055248f5a01ad3b80ef6d20a24948c870e4.
verify-gates.js and gate-proof.json check the current profile, source hashes,
normal adapter, native results and mutation matrix. All run handles are
terminal. No production source or legacy Go test was changed in this step.

These runs still use the installed CLI/engine/Go pin 6c66f5384857. The upstream
source checkout was separately pulled to 6bc5870169332d91815b5626ee890c703f316b09;
its Go SDK and v5 contract files are unchanged, but its engine now judges
release success from drain events after hold_ready. Updating the pinned
binaries and adding real engine stage/release validation remain work, as do
the immutable CI image, remaining doubles and expired-watch recovery.
The migration remains active and uncommitted on core.


## Real repeated eviction and shared diagnostic vocabulary (2026-09-11)

The existing step-closing scenario "An evicted step is a retryable
interruption, not a failed build" now uses a real database-backed Kubernetes
worker and production SPDY executor wiring. The fake pod-create reactor is
removed from closing.go. The action preserves the task handle get-evicted,
busybox image, /workdir, task container kind, workspace-derived process ID,
/bin/sh -c "echo unreachable", nil stdin, combined stdout/stderr, and exact
Failed/Evicted status message. The real worker supplies its persisted TeamID
instead of the old fake fixture's hardcoded 1.

The action reuses workerContainerDraft/containerSpecFromDraft and the existing
settleProcessOnEveryPod helper. That helper reports failure through the real
UpdateStatus API for each actual UID the runtime creates and joins Wait; it
never creates a replacement itself or injects an API response. The scenario
uses production execProcess, not the direct compatibility fallback. Its pods
never reach Running: envtest has no kubelet and no command executes. As before,
this case asserts the typed interruption and diagnostics; it does not add an
independent assertion counting replacement pods.

The typed runtime.InterruptionError/reason assertion is unchanged except for
its input state, ProcessOutcome instead of TaskOutcome. Its two diagnostic
checks now use the existing "the build log shows" vocabulary. The removed
TaskOutcome diagnostic definition had no other consumers. The preceding typed
check already requires a non-nil error, preserving the old diagnostic check's
failed-Wait precondition. Net definitions: minus one. No scenario is removed
or added and the remaining feature text is unchanged apart from its explicit
fixture description.

Eviction was also the sole caller of runTask's optional fault argument. That
parameter and its now-unreachable branch are removed. The normal startup
sequence, cancellation, 25ms delay, result capture and joined startup report
remain unchanged. task-cleanup.js verifies the exact allowed transformation.
This does not remove the remaining localExecutor/fake-client task fixtures.

Evidence: /tmp/brine-real-eviction.KviUJt. The old and final new controls each
pass the selected scenario. Four production-only process.go mutations fail
at the same assertions before and after migration:

- Replacing the typed interruption with an ordinary error: typed-error check.
- Reporting node_lost for Evicted: interruption-reason check.
- Removing "Failure" from the diagnostic heading: first build-log check.
- Replacing the diagnostic reason with "unknown": second build-log check.

All setup/action steps pass. Every recorder drain completes without partial
results, including the real fixture's pod cleanup and bounded cancellation.
validate.js checks the unchanged case, command and status inputs, exact typed
assertion body, removed reactor/imports, shared lifecycle helper, definition
reduction, explicit per-run binaries and production-only overlays. Diagnostics
are compared byte-for-byte after normalizing only the generated worker-*/
namespace prefix and the renamed assertion subject (task diagnostics to build
log). No other error content is normalized. Matrix SHA256:
4b27b6ce29248e39c29a8ed77abcec6d7be4340b916af7520001f8b056e37c87.

An initial patch attempt was rejected because its hunks were out of order;
source inspection confirmed it made no repository edit. The script's resulting
missing-feature runs were not evidence. After correction and removal of the
unused helper branch, all new binaries were rebuilt and the complete final
matrix rerun; the hashes in matrix.json identify those authoritative runs.

Fresh vet and both native suites pass (27.629s), including vocabulary guards.
Full Brine passes 574/574; coverage remains 1873/2368 JetBridge production
statements (79.096284%), above the 50% gate, with no covered block gained or
lost. The 146635ms report is summed scenario time, not wall runtime. Profile:
/tmp/brine-coverage.1HFD51/coverage.out, SHA256
cda238b2aed3832ef7388b2003e64bc35f7caeab490d7060e05e7db6922b70d8.
The restored normal adapter matches final new-control, SHA256
84e7a720906e79a8c3486881efff4565a23519d8387e467354639c8156a92cfb.
verify-gates.js and gate-proof.json verify the profile, source identities,
normal binary and matrix. All run handles are terminal and no recent active
Brine/API/etcd process remains. No production source or legacy Go test changed.

The only remaining explicit fake-client reactor in the Brine steps is the
watch reactor in podwatch.go; central fake clientsets, local/severing execution
and runtime test substitutes still remain. This is not a double-free-suite
claim. The Brine pin is still 6c66f5384857; the already-pulled 6bc58701 engine
release changes need coherent binary/dependency alignment and a real engine
stage/release test. CI publication/validation and expired-watch recovery also
remain open. The full goal remains active, with changes uncommitted on core.


## Aligned Brine toolchain and real engine release (2026-09-11)

The local CLI, engine executable and Go module now use the pulled revision
6bc5870169332d91815b5626ee890c703f316b09. The module pseudo-version is
v0.0.0-20260911001831-6bc587016933; its fetched archive checksum is
h1:lJiFBcb8Uh1x6Fzzdls0ZqqjtgeCyZFMGqQJiHH7VuQ=.
The SDK source, contract goldens/schemas and Cargo.lock are unchanged from
6c66f5384857. The material upstream change is engine release witnessing:
a drain-start/drain-complete pair must follow hold_ready before release is
reported as honoured. A leader exit code alone does not establish cleanup.

Both Rust executables were built with --locked. The Go dependency and its two
checksum entries are the only module changes; validate.js reverses those
changes and checks the original checksum-file digest. The restored adapter's
Go build metadata names the new pseudo-version. README now explicitly requires
both matching CLI and engine binaries for native protocol tests.

TestEngineReleaseDrainsRealDaemon adds one native integration test, using the
existing production artifact-daemon vocabulary and no substitute registry or
runtime. It owns a scratch engine on an OS-assigned loopback port, an isolated
BRINE_HOME, and BRINE_ENGINE_MACHINE=0. Commands explicitly select that owned
port and the same CLI binary. Cleanup signals and joins the owned engine;
it does not resolve or stop another engine session.

The staged scenario serves known bytes through the actual daemon before
holding. The test independently reads those bytes from the daemon's actual
storage root while held, then calls the real CLI release command. It requires
physical removal of that root, the persisted complete/released projection,
ordered hold/drain/release events, exactly one completed non-partial recorder
disposer, and a second release reporting already released. The existing direct
adapter hold/pod-disposal test remains unchanged and still passes. There are
no new behavioral step definitions or feature scenarios; the full suite stays
at 574 cases.

Evidence: /tmp/brine-engine-release.GRWeZV. Two isolated lifecycle mutations
are detected after a passing control:

- Suppress only recorder_drain_completed at the adapter's protocol writer.
  Actual disposal still runs, but the new engine refuses release with exit 1
  and its missing-drain-pair explanation. The new integration test rejects
  that outcome; the existing direct hold test also detects the missing event.
- Leave the real daemon's storage directory after stopping its process.
  The protocol can report release, but the new test fails because the actual
  storage root remains. Test cleanup removes only its owned scratch directory.

These are adapter/engine lifecycle mutations, not production JetBridge fault
coverage and not grounds to retire a legacy Go behavior test. In particular,
the event-suppression mutation deliberately demonstrates the engine's protocol
witness, not an independent engine inspection of physical resources. The
filesystem assertion supplies the independent cleanup check.

The first missing-drain overlay targeted GOMODCACHE and Go rejected it before
compilation; missing-drain-cache-rejection.log preserves that tooling failure.
It is excluded from mutation evidence. The final overlay targets our adapter
writer instead and compiles/runs the real engine. A forwarded Go test filter
did not narrow Ginkgo's native-package invocation, so it was removed and the
complete native adapter package was rerun for control and both faults.
validate.js checks the exact failing tests, intended diagnostics, overlay
paths, unchanged existing protocol tests and current source identities.
Matrix SHA256:
9c20ab4fa3749b24285fc6705d60264b5bd41493b58fb400ce728ee50a5e5976.

Final vet and both native suites pass (33.751s). Full Brine passes 574/574,
covering 1873/2368 JetBridge production statements (79.096284%), above the 50%
gate. No covered block was gained or lost. The 146766ms report is summed
scenario time, not wall runtime. Profile:
/tmp/brine-coverage.wNqUnO/coverage.out, SHA256
4ac4b471378fc02f55bc4d4fa9afdc2992c2bd7981756d2fa8737b261eb28be8.
Adapter SHA256:
4d3893387406dafab92751abe38bd36a2b4795435c8ccd10d11dc38d26098410.
CLI SHA256:
d1ecd37bd6fb320dd6656e6526764176213f85999fe2fce507dd08d794ed6067.
Engine SHA256:
e5109b0e9ccb80085b52d4015d2d8155e25ea885574b9e1eaff0b27c6146b82f.

Fresh upstream runner verify --contract 5 is conformant: 20 event goldens,
3 protocol invocations, 8 document-selection cases and 8 document-fidelity
cases, all passing. As its report says, this does not drive AST goldens or
registry-dependent adapter obligations. conformance.json SHA256:
b553629a6debbfe9199385065fb197680f327cf0627b77d8272db653edfbfe96.
verify-gates.js/gate-proof.json bind these results to the module, sources,
CLI, engine and adapter. All process handles are terminal; no recent active
Brine/API/etcd processes remained after validation.

The CI Dockerfile already derives its Brine revision from go.mod, and the
pipeline checks the image build receipt against that same pin. Its eleven
runner image references still use v9: no new image was published, no pipeline
was set, and no CI pass is claimed. Remaining doubles, consolidation,
expired-watch recovery and CI publication/validation keep the full goal open.
All migration changes remain uncommitted on core.


## Sidecar startup outline consolidation (2026-09-11)

The two sidecar-startup outlines now share one body in pod-lifecycle.feature.
All six expanded cases remain: the original my-sidecar/bad-sidecar:latest,
redis-sidecar/redis:bad-tag and bad-image/nonexistent:latest fixtures, each on
production and direct compatibility execution. Task handles, setup, actions
and original assertions are preserved. The two former failure-only cases now
also require the sidecar name and image in the build log, using existing steps.
The feature goes from 19 authored scenarios/outlines to 18, still expanding to
32 cases (30 real-API cases and two legacy severed-exec cases). SC-09 late
failure and SC-10 completion remain separate; neither behavior was merged.
No Go source, step definition, production implementation or legacy Go test
changed. This is consolidation, not another double-removal claim.

Evidence: /tmp/brine-sidecar-outline.iVSnUS. The original six-case control and
four production mutants were already terminal when work resumed; their logs
and individual statuses were recovered before editing. The new feature ran
against the EXACT SAME five binaries, without rebuilding them. Both controls
pass 6/6. Production-only process.go overlays show:

| Fault | Before | After |
|---|---|---|
| Wrong waiting reason | Six cases fail | Same six fail |
| Suppress diagnostics | Four diagnostic cases fail | All six fail |
| Omit sidecar name | Four diagnostic cases fail | All six fail |
| Omit image detail | Four diagnostic cases fail | All six fail |

All 18 prior scenario/fault detections are preserved, with six additional
pairs from the strengthened diagnostic assertions. validate.js expands the
literal feature rows, compares the original assertions and unaffected cases,
checks inherited requirement tags, verifies all steps/cmd Go source hashes and
exact production overlays, and validates each scenario's completed non-partial
disposal. Preserved failures have identical assertion text and diagnostics,
normalizing only the generated worker namespace prefix. Matrix SHA256:
aab6e4d88e1a421f4d74c92f763062860514e406092c2a0dfd8c258bdda88820.

Vet and both native suites pass (32.353s), including real
engine release and direct adapter hold/disposal. Full Brine passes 574/574;
coverage remains 1873/2368 JetBridge production statements (79.096284%), with
no covered blocks gained or lost, above the 50% package gate. The reported
146690ms is summed scenario time, not wall runtime. Profile:
/tmp/brine-coverage.RolLNy/coverage.out, SHA256
ef2cfd4b97cb3c3a132df28caec390ec50b699404cee26f3cbbf4f15c0ff264f.
The restored normal adapter matches the same control binary, SHA256
4d3893387406dafab92751abe38bd36a2b4795435c8ccd10d11dc38d26098410.
verify-gates.js and gate-proof.json bind these results to the feature, matrix,
source identities and tool binaries. No new upstream conformance run was
needed for this feature-only change; the prior 39/39 result is historical,
not relabeled as a fresh run.

The upstream source checkout was fast-forwarded to d1c75ea958f299db1aee754b65d9da2b456a03ac.
Its two commits since the installed 6bc58701 runtime change only CLAUDE.md and
ci/jetbridge documentation/pipeline configuration. Runtime, SDK and contract
sources are unchanged; installed CLI/engine and the Go pin remain 6bc58701.
This checkpoint verifies those binary identities rather than claiming the
source pull rebuilt them. All test processes have terminated.

Remaining central fake clientsets, local/severing execution, runtimetest
substitutes, expired-watch recovery and immutable CI image publication and
validation keep the full goal active. Changes remain uncommitted on core.


## Task-command pod state on the real API (2026-09-11)

All eight task-command consumers (seven in task-command.feature, one failed
pod-retention case in container-run.feature) now use the shared WorkerReady
setup and actual kube-apiserver/etcd pod state. The old fake-cluster setup is
replaced by the existing named-worker Given followed by an explicitly named
host-command step. TaskCluster embeds WorkerReady, uses its real team ID and
inherits its registered pod cleanup. runTask requires the API-assigned UID
and resource version before observing startup. No command, assertion, case,
example value or requirement tag was removed; the definition count is unchanged.
The duplicate NewCluster construction in task_command.go is gone.

This removes the fake API layer, NOT the host executor. localExecutor still
runs the production supervisor on the host, with the same workspace mapping,
nil stdin, command ID, 25ms status-report delay, Wait and startup-gauge reset.
The fixture explicitly reports Running input to the real API; it is not a
kubelet. The feature and step language now say this. The exec-target and
sidecar-log tests still need actual pod execution/logging; envtest alone is
not their replacement. No production source or legacy Go test changed.

Evidence: /tmp/brine-task-api.wOWipm. Old/new controls pass 8/8. Four isolated
production mutations preserve five exact scenario/fault detections:

- Zero startup duration: the startup/log case rejects 0ms.
- Report successful exit for a failed command: exit-3 and exit-42 cases fail.
- Persist successful status as 19: the completed-container property check fails.
- Remove command hash from supervisor-state identity: the changed-command case
  observes one execution instead of two and fails.

validate.js verifies unchanged literal commands/assertions and unaffected
cases, exact overlays and source scope, explicit binary manifests, identical
failure text/diagnostics (no normalization), and complete non-partial recorder
drains. The old fixture had zero recorder disposers; the real worker adds its
pod cleanup. All Go files outside task_command.go/domain.go remain unchanged.
The first timing mutation failed compilation because startTime became unused;
it ran no tests and is excluded. The corrected mutation retains the expression
and multiplies its result by zero. Final matrix SHA256:
71b3b30d7838a593345d65fc2a6076010ce881fd54a617f61bbb125b1c0f6cad.

Vet and both native suites pass (32.752s), including real engine
hold/release and direct adapter disposal. Full Brine passes 574/574; coverage
remains 1873/2368 JetBridge production statements (79.096284%), with no covered
blocks gained or lost. The 147168ms report is summed scenario time. Profile:
/tmp/brine-coverage.tX30gC/coverage.out, SHA256
88b97d129fb8abde01e2729a9bd3a89be6209df6712252206dd607cf476c23ca.
The restored normal adapter matches new-control, SHA256
82fd8c563ae6601688982b4290af1405bad53377db88c6a713562c8899f45abf.
verify-gates.js and gate-proof.json bind the matrix, sources, features, profile
and runtime identities. The installed Brine CLI/engine/SDK remain 6bc58701;
the pulled d1c75ea9 changes only upstream CI/docs. All run handles are terminal.

The full goal remains active. Central fake clients, host/severing execution,
runtimetest substitutes, expired-watch recovery, and immutable CI publication
and validation remain unfinished. Changes are uncommitted on core.


## Cancellation pod lifecycle on the real API (2026-09-11)

The existing four-row cancellation outline now starts with shared WorkerReady
setup instead of NewCluster's fake client. Both resource/task kinds and both
before-start/running timings remain, with every original handle, command and
assertion unchanged. No case or step definition was added or removed. Only
steps/closing.go and features/step-closing.feature changed for this migration;
production source and legacy Go tests are untouched.

The fixture checks API-assigned pod UID/resourceVersion. Running rows create
an owned real Node and bind the same pod through the API before reporting
Running status. This matters: an unscheduled pod can be removed immediately
even when a nonzero grace period is requested, so it cannot distinguish the
immediate-delete contract. The existing UID-aware pod and Node cleanup helpers
own disposal, using independent contexts after scenario cancellation.

Commands still use the existing host executor. Both running rows observe a
real child PID and retain the assertion that it stopped, but its termination
is host process-group cleanup, not proof that Kubernetes killed a container.
The feature says so explicitly. Removing this API double does not finish the
remaining real-execution migration.

Evidence: /tmp/brine-cancel-api.GXArXF. Old/new controls pass 4/4. Four isolated
production-only process.go mutations show:

| Fault | Old fake API | New real API |
|---|---|---|
| Retain cancelled task pods | Both task rows fail | Same two fail |
| Delete cancelled resource pods | Both resource rows fail | Same two fail |
| Cancel the cleanup context before deletion | All pass | Both task rows fail |
| Request 30s deletion grace instead of zero | All pass | Running-task row fails |

All four prior scenario/fault detections are preserved, with three added.
Every mutant still passes the command-stop assertion; failures are specifically
at the final retained/removed pod observation, never setup. Preserved failure
text and diagnostics are identical without normalization. The fake client
ignored cancelled contexts and deletion grace, masking both additional faults.
No production defect is being claimed here: these are deliberate mutations of
otherwise passing production code.

validate.js checks the exact feature edit and unchanged example rows, unchanged
assertion/run-body code apart from real identity/binding and real team ID,
all other Go-source hashes, exact overlays, explicit runner binaries, individual
exit statuses and completed non-partial cleanup. The initial migration-script
quoting error happened before any edit; the direct corrected patch and final
runs are authoritative. Matrix SHA256:
967d0b484e0dc05feba557859547a76d7016a4e85879b06c806217527592d8b1.

Vet and both native suites pass (33.159s). Full Brine passes 574/574,
covering 1873/2368 JetBridge production statements (79.096284%); no covered
blocks were gained or lost. The 146964ms report is summed scenario time.
Profile: /tmp/brine-coverage.9ewrVS/coverage.out, SHA256
7567b72b62f74189161ea3efe36245e8476cb7c5d2ce7a18bdd7cef8bdd12b30.
Restored adapter matches new-control, SHA256
1e6150086bfa19a954750613dab3aa45f345c6e3990770f7ff4ec53359a1b7f8.
verify-gates.js/gate-proof.json verify these identities and the unchanged
6bc58701 CLI/engine/Go runtime. All run handles are terminal. The full goal
remains active: other fake clients, host/severing execution, runtimetest,
expired-watch recovery, and CI publication/validation remain unfinished.
Changes are uncommitted on core.


## Terminal outline and real API pod state (2026-09-11)

The terminal/no-terminal cases are now one two-row outline in
container-run.feature. The feature goes from 15 authored scenarios/outlines
to 14, still expanding to 18 cases. Catalogs verify 1067 definitions became
1066: the two fixed terminal setup definitions became one parameterized
transition, and every other definition and resource declaration is unchanged.
Both one/none modes are explicit; unknown mode strings fail setup.

The outline uses the shared real WorkerReady setup. tty.go no longer calls
NewCluster. Its probe checks API-assigned pod UID/resourceVersion and reports
Running input through the real client. Both original handles, get-step kind,
image, working directory, shell command, non-nil stdin, TTY selection and output
assertions remain. A real team ID replaces the old constant. Command execution
still uses the host executor's actual PTY or pipe; the feature and helper say
this is NOT Kubernetes remote-exec coverage. No production code or legacy Go
test changed, and only tty.go changed among the Go sources.

Evidence: /tmp/brine-tty-api.2S5Qsk. Controls pass 2/2 before and after. Three
production process.go mutations preserve four exact scenario/fault detections:
never allocate TTY fails the terminal row, always allocate TTY fails the pipe
row, and discard stdout fails both. Old scenario names map in order to rows
1/2 of the consolidated outline. Failure assertions and diagnostics are
identical without normalization. All setup/actions pass; recorder cleanup
completes without partial disposal.

validate.js checks the original probe/assertion bodies, unchanged surrounding
feature cases, retained requirement tag and example values, exact overlays,
explicit binaries, source hashes and catalog difference. Each catalog's binary
closure is checked against its own compiled executable, not compared as if a
rebuild should retain the old binary hash. Matrix SHA256:
66b971ea21a7a41ef682544a33cc8cdbdd5f6af87156ae26f51f2bef2644ed51.

Vet and both native suites pass (33.130s). Full Brine passes 574/574,
covering 1873/2368 JetBridge production statements (79.096284%), with no
covered blocks gained or lost. The 146846ms report is summed scenario time.
Profile: /tmp/brine-coverage.P3F3Av/coverage.out, SHA256
d993fa82519e7a574b556118ecfe4b81272babaf80198720d45cec94d3fa69fc.
The restored normal adapter matches new-control, SHA256
e78ba9562526380abf495b240ce1c1804d921dc48cf1914b81b0dabbed2e6736.
verify-gates.js/gate-proof.json bind this result to the matrix, source and
feature hashes, profile and unchanged installed 6bc58701 Brine toolchain.
All run handles are terminal. The full goal remains active: other fake clients,
host/severing execution, runtimetest substitutes, expired-watch recovery and
CI publication/validation remain unfinished. Changes are uncommitted on core.


## Observability lifecycle inputs on the real API (2026-09-11)

All nine observability cases now start from the shared real WorkerReady
resource. observability.go no longer calls NewCluster; ExecStepRunning carries
kubernetes.Interface rather than a fake clientset. The setup explicitly names
host-executed commands. The scenario names, handles, requirement tags, command,
stdin, image, event/node/metric assertions and init-failure-name assertion remain.
The real database team ID replaces the constant. No production source or legacy
Go test was changed or removed in this checkpoint.

The duplicated asynchronous status paths now share real_observability.go.
Each update reads a fresh resourceVersion and checks errors and API identity.
Scheduling creates an owned Node and uses the binding endpoint, not an attempt
to mutate spec through UpdateStatus. A transparent watch-handshake transport
forwards real requests/responses unchanged; it gates transitions until Wait
has processed initial state. The original 20ms startup interval remains.
Wait is bounded, cancelled and joined before scenario cleanup. Node and pod
cleanup completes with UID preconditions and no partial drain.

The repeated-condition case now sends a genuinely distinct Pending revision
after the watch opens, before Running. The extra status message forces a real
API update without changing the scheduled condition. Previously, two identical
fake updates happened before Wait and only the last object was initially read.
The no-dedup production mutation therefore records three scheduled events now,
versus two before. The unchanged exactly-once assertion detects both; the
validator checks both exact diagnostics instead of normalizing their counts.

Evidence: /tmp/brine-observe-api.IKXeqQ. Controls pass 9/9 before and after.
Nine production-only process.go mutations preserve ten scenario/fault pairs:
omit each of six lifecycle events, report the wrong node, repeat scheduling,
and zero startup duration. All other mutation failure diagnostics are identical.
RF-14's init-name assertion is preserved but has no targeted mutation in this
matrix; these results do not claim every behavior is mutation-covered.

validate.js checks the exact feature transformation, unchanged assertion and
trace-resource bodies, source hashes, explicit binary wrappers, production
mutation overlays and complete resource drains. Catalogs remain 1066 definitions;
only the setup sentence and corresponding state schemas change. All unrelated
step/resource declarations match. The compiled binary closures are verified
separately. Matrix SHA256:
1e1d0b79ea8fa35364a47719a00cee2811d673fbd94b0cd5960665b2325ff72a.

Initial new runs exposed an overly strict fixture guard: reporting Pending on
an already-Pending real pod is a valid no-op. Those logs/binaries are retained
under initial-new and excluded from valid mutation evidence. The final helper
requires a distinct revision specifically for the repeated-observation input.
A partial patch attempt also produced an unused-reference build failure
(partial-edit-build.log); it ran no scenarios. The evidence validator was fixed
to account for fail-fast execution after a failed assertion, not to demand
subsequent assertion events. Neither issue is counted as mutation detection.

Vet and both native suites pass (38.169s), including v5 protocol and real
engine-release/drain tests. Full Brine passes 574/574 with 147274ms summed
scenario time, not a wall-clock benchmark. Coverage is 1872/2368 production
statements = 79.054054%, above the unchanged 50% gate. Profile:
/tmp/brine-coverage.WhDt2N/coverage.out, SHA256
21e2b36f94202cccf0b1c73b4b77457ca32669b2db24138d2db435aaaa35789a.
The restored normal adapter matches new-control, SHA256
ba05705f4d4278b98556f7138a1b5f0e65c136a2e84948242b2234dac210061f.

There is exactly ONE lost statement, process.go:1420.66,1422.8: appending
successfully fetched init logs to the diagnostic. client-go's fake GetLogs
returns HTTP 200 with literal "fake logs"; the real API has no kubelet to serve
logs. RF-14 asserts the failed container's name, not these canned bytes.
No other covered block was lost or gained. verify-gates.js records this exact
gap rather than claiming unchanged coverage or adding a fake log server.
Real init-log delivery remains work for the actual Kubernetes execution tier.

All run handles are terminal with no new live Brine/API/etcd processes.
The goal remains active. Host execution and tracetest capture remain explicit
limitations in this family; other fake clients, runtimetest substitutes,
expired-watch recovery and CI publication/validation are still unfinished.
All changes remain local and uncommitted on core.


## Real OTLP export replaces trace-test capture (2026-09-11)

Brine no longer imports sdk/trace/tracetest or uses SpanRecorder or an
InMemoryExporter. Its scenario-scoped span-capture resource now starts an
official OpenTelemetry Collector, calls production tracing.Config.Prepare
with OTLP, and reads the collector's OTLP JSON file after provider ForceFlush.
The reader implements neither an exporter nor a receiver: assertions examine
bytes that went through the actual production exporter, gRPC and collector.
No feature sentence, case, requirement tag or registry declaration changed.

The collector has a private temporary root, a loopback listener and no batch
processor or sending queue. Its file exporter uses rotation mode, which
upstream documents as unbuffered. Readiness and export waits are bounded;
export/read/decode errors fail assertions rather than become empty captures.
Resource disposal shuts down the SDK provider, restores the prior provider,
propagator and Configured flag, stops and joins the owned collector, and removes
its files. A timeout kills only that owned process and is reported as failure.

The runtime dependency is official otelcol 0.160.0, verified against GitHub's
release asset digest. deploy/Dockerfile.test-runner pins its Linux/amd64 archive:
5415b8daf782f17cc463c3e46816abf181a68a04b7bcf98c273c3c204096c743.
The tested binary SHA256 is
abe338fa33865e54412566db5cea4adc82374594b65b49c1099048cdf8cf2451.
README documents otelcol on PATH or absolute BRINE_OTELCOL_BINARY. Nothing is
automatically downloaded by an adapter or replaced by a fallback. This changes
the Dockerfile only; no runner image was built/published or pipeline updated.
The Go dependency versions and go.sum are unchanged; two existing OTel module
requirements are now direct imports.

Sources: the official 0.160.0 release and pinned file-exporter documentation:
https://github.com/open-telemetry/opentelemetry-collector-releases/releases/tag/v0.160.0
https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.160.0/exporter/fileexporter/README.md

Evidence: /tmp/brine-trace-export.Jt4TH6. Nine-case controls pass before/after.
The same nine production mutations preserve all ten scenario/fault pairs and
exact failure diagnostics without normalization, including three observations
for the no-dedup mutant. validate.js binds the saved prior binaries to the
previous checkpoint, verifies the production overlays and explicit wrappers,
all unchanged feature bytes, the assertion-body adaptations, source hashes,
complete API cleanup and identical catalogs (1066 definitions/resources).
Matrix SHA256:
f0011f0910b2474813fa8e6f878517dcf586ca0e8ff38de74783e94282cbffea.

TestTraceCaptureExportsAndDisposesRealCollector additionally checks two
successive captures: exact production span IDs, node attributes, both duplicate
events, the production service resource, scenario isolation, successful collector
exit, file removal and global restoration. Two isolated capture-helper mutations
are rejected: discard decoded events and retain the exported files. Those are
fixture checks, not additional production-fault coverage or legacy retirement.
The retained-file mutant's actual OTLP bytes stay under fixture-scratch as evidence.
fixture-proof.json SHA256:
105844248b7faac8e25b0876dd442197b9e2523d930b7464b6c011f23f208f32.

Vet and both native suites pass (37.215s). Full Brine passes 574/574 with
1872/2368 JetBridge production statements = 79.054054%, above the 50% gate;
no covered blocks were gained or lost. Summed scenario time is 146533ms, not
wall time. Profile /tmp/brine-coverage.i398eY/coverage.out SHA256:
050a71f41ebdc8284758eb61a725a70e331e874763d13cc723e163459cdd6dbf.
Restored normal adapter matches new-control, SHA256:
e4380f417280416d3044ae1717c363050a8391c73d479c2b7a1982b8d46688d1.
verify-gates.js/gate-proof.json bind the result to that binary, unchanged Brine
CLI/engine/SDK pin and exact collector. All run handles are terminal.

The goal remains active. Host execution, remaining Kubernetes fake clients,
runtimetest substitutes, real init-log delivery, expired-watch recovery and CI
delivery are not solved by replacing trace capture. Legacy tracing Go tests
outside this nested Brine module were not changed. Work remains uncommitted
on core; no production source or legacy Go case was changed in this checkpoint.


## Integration cases share the real API worker (2026-09-11)

IntegrationCluster now refines WorkerReady rather than constructing another
fake Cluster. Twenty-nine cases reuse the real API-server/PostgreSQL worker,
then explicitly opt into host execution. Their former namespace literals were
setup-only values; the independent secret-reference namespace remains unchanged.
Worker identity, team, volume repository and five-second timeouts are preserved.
The helper checks that each observed pod has an API-assigned UID and resource
version. The duplicate constructor is deleted. The entire feature still runs
39 cases: 29 migrated here, nine previously real node cases, and one remaining
legacy container-target case. No legacy Go leaf was retired.

The production omission mutation exposed a vacuous check: the long-pipeline
case accepted a missing pipeline label because it only iterated label values.
That case now reuses the existing exact-label assertion with the expected
63-character prefix. The real API enforces label validity; the assertion also
requires presence and exact content. The now-unused length-only definition is
removed, reducing the catalog from 1066 to 1065 definitions/resources without
removing a case or requirement tag.

Evidence: /tmp/brine-integration-api.fQi0Al. The original and first-pass runs were
confirmed terminal before continuing; their binaries/logs remain intact.
old-* is the original fixture, new-* the first real-API pass, and final-* the
consolidated assertion. All three controls pass 39/39. Eight original production
mutations preserve all 14 exact scenario/fault pairs and diagnostics: pod naming,
volume binding, pipeline labels, secret literal/reference handling, secret key,
process identity, volume ownership and failure exit status. The final label
case adds a missing-label detection. A ninth mutation truncates labels to 62
rather than 63 characters: the original case passes, the final case fails on
its exact-label assertion. That is two added detections, not two new scenarios.
For this additional old binary only, the overlay restores the saved original
integration fixture alongside the single production fault; the validator checks
that all other Go source hashes match the original snapshot.

validate.js verifies exact production overlays, unchanged production sources,
feature behavior/tags except the stated setup and assertion changes, source
scope, catalog changes, explicit binary wrappers, complete scenario cleanup,
and all failure attributions. The new namespace disposer drains once per
migrated case, including failed cases. matrix.json SHA256:
a03a482d527c9414f298ce3c484279ae80677c03be2246294f0445cde43565d1.
An initial oversized patch was rejected before any edit; it ran no tests and
contributes no mutation evidence. Smaller patches applied successfully.

Vet and both native suites pass (37.290s), including vocabulary,
v5 protocol, hold/drain and real engine-release checks. Full Brine passes
574/574; coverage remains 1872/2368 production statements = 79.054054%,
above the unchanged 50% package gate. No covered blocks were gained or lost.
Summed scenario time: 146811ms, not a wall-clock benchmark.
Profile /tmp/brine-coverage.U0oSWX/coverage.out, SHA256:
9c283ac1b05393c3f03dcc77681160093927b9e6318ade3ae66b586206e8ef61.
Restored normal adapter matches final-control, SHA256:
55976bb150abb69b4c9d6336ac556c4d0e146ec6bcf7814b7749db39050736bc.
verify-gates.js/gate-proof.json bind these results to the same CLI/engine/SDK
and collector as the preceding checkpoint. All run handles are terminal;
no new live Brine/API/etcd/collector processes remain.

This removes fake API setup, not Kubernetes execution substitutes. Host commands
and installed resource scripts remain; mount/command assertions in this family
do not prove real artifact byte delivery. The container-target fixture, injected
exec failures, canned sidecar logs, runtimetest substitutes, expired-watch
recovery and CI delivery remain unfinished. No shared-cluster writes, image
publication, commit or push occurred. The goal stays active; work is local on core.


## Container-target obligation joins the resource case (2026-09-11)

The standalone main-container case used localExecutor.present, a hand-written
map unrelated to the pod created by the worker. The integration host executor
now resolves its namespace, pod and container from actual API objects, reusing
its existing client-backed destination resolution. Both default and installed
resource-script executors receive the real client. No new executor is introduced.

The existing get-resource case now also inspects its observed API pod and reuses
"the pod runs {int} containers" to require exactly one. Its existing image check
resolves main; its existing response, exit-status and pause-command assertions
remain. This single case therefore owns the old target obligation. The standalone
case and its two specialized definitions are removed. sortedKeys, which has
other callers, moves unchanged from exec_target.go to keys.go; the presence map
and its synthetic container-not-found error are deleted. The old fixture is
recoverable from Git and the saved before-steps_exec_target.go evidence snapshot.
No legacy Go test was retired.

Brine's nominal state boundary stays explicit: "the step's pod is inspected"
maps StepRan to the existing PodCreated state. That adds one reusable transition
while removing two bespoke definitions. The feature shrinks from 39 to 38 cases,
the full suite from 574 to 573, and the catalog from 1065 to 1064. Existing tags
and behavioral sentences are unchanged except the retired case and the added
inspection/count sequence. Historical comments claiming working doubles were
sufficient have been replaced with the actual host-execution limitation.

Evidence: /tmp/brine-exec-target.m3imIr. Controls pass 39/39 before and 38/38
after. Four production-only mutations change the step-command exec destination
container, pod, namespace, or command. The wrong-container mutant fails the old
standalone case and its explicitly named get-resource replacement. Five
wrong-command scenario/fault pairs retain identical diagnostics. Wrong pod and
namespace passed all old cases; they now fail six execution cases each through
real pod lookup. The wrong-container mutation also now fails six execution
cases rather than the one map-backed case. Across this matrix there are six
old and 23 final detections: five exact pairs preserved, one retired obligation
mapped to its replacement, and 17 additional detections after that mapping.
These are host-executor destination checks, not proof of Kubernetes remote exec.

validate.js checks production overlay contents and unchanged production source,
baseline source/binary identity, complete case rosters, terminal status totals,
exact failure attribution and the mapped obligation, unchanged surviving
vocabulary, feature transformation and complete namespace cleanup. matrix.json:
a0683dc627847ade615784e45f9c4afddea8c70d99b242571081aab763965a79.

The first-pass new-* artifacts are retained but excluded from valid evidence.
An attempted pod-only struct projection was unsatisfied at runtime: "type
mismatch: expected steps.PodSnapshot, got steps.StepRan". Vocabulary guards
check matching, not runtime nominal compatibility, so their pass did not prove
that transition. The full control run caught it; the structural shortcut was
removed and replaced by the explicit map. final-* is the corrected matrix.

Vet and both native suites pass (34.753s), including v5 protocol,
hold/drain and real engine-release checks. Full Brine passes 573/573; observed
coverage is 1875/2368 production statements (79.180743%), above the unchanged
50% package gate. No covered block was lost. The three newly hit statements
are only daemon_client.go:224.18,228.5, the previously observed daemon-probe
cancellation variation. The full log names brine-warm-roll, rc-42 and "context
canceled". This is not credited as deterministic new exec coverage. The initial
comparison verifier rejected the unexpected total; it now binds this measured
profile to that exact block and diagnostic rather than accepting arbitrary drift.
Summed scenario time: 145979ms, not wall-clock runtime.
Profile /tmp/brine-coverage.v8LYAg/coverage.out, SHA256:
826b003293f10241c64ec97bb036c04a0d00db08e2c86bc3acbdbf8a9c8979a2.
Restored normal adapter matches final-control, SHA256:
6d334aa0362a6e68b2a442969d341d90755749b6a667b156d9131e7323e13451.
verify-gates.js/gate-proof.json verify the unchanged CLI/engine/SDK/collector
and the complete evidence. All run handles are terminal with no new active
Brine/API/etcd/collector processes; git diff --check passes.

Host execution, installed resource scripts, injected failure strings (including
the handoff write-refused branch), severing executors, canned sidecar logs,
remaining fake clients and runtimetest substitutes still violate the final
no-doubles objective. Real remote-exec/log delivery, expired-watch recovery and
CI delivery remain unfinished. No shared-cluster writes, image publication,
commit or push occurred. The goal remains active; changes are local on core.


## Handoff write refusal comes from real extraction (2026-09-11)

The handoff outline's write-refused row no longer sets localExecutor.failure.
It uses the same client-backed host executor as the successful transfers.
After the real worker creates its returned input volume, the fixture places a
nonempty directory where the incoming regular file belongs. Real GNU tar
cannot replace that directory, including when running as root, so the actual
extraction exits 2 with "result.json: Cannot open: File exists".

The collision name must be local, its destination must be inside the owned
node store, and that destination must not already exist. The existing node
writer creates the directory and its marker file. After StreamIn, the fixture
checks the marker contents survived and requires a real ExecExitError with
code 2 and the tar write diagnostic. A missing pod, API refusal or wrong
container cannot pass as the intended write failure. The existing Brine
assertion still requires both "stream into returned input volume" and
"stream in via exec" error context. The artificial "transfer refused" suffix
is removed from that one expected outcome; all rows, tags, other outcomes and
step definitions are unchanged. This remains host tar, not kubelet execution.

Evidence: /tmp/brine-handoff-write.a4FvYj. The five-row controls pass before/after.
All five production-only mutations compile. The same ten scenario/fault pairs
are retained across error swallowing, lost error context, wrong input container,
missing decompression and skipped extraction. Eight failure diagnostics remain
identical; the write-refused row's two diagnostics now describe a real tar
failure instead of the removed injected error. Two detections are added: that
row also rejects the wrong-container and skipped-extraction mutants. Both gzip
and S2 rows reject skipped decompression on this host's GNU tar.

A separate fixture-only mutation omits the destination collision. Four rows
still pass, and write-refused fails because the actual handoff now delivers the
exact artifact. This proves the failing input matters; it is not counted as a
production mutation or legacy-test retirement. The preliminary filesystem
probe also records exit 2 and preserved directory contents. The captured
lost-error-context mutant exposes the actual tar diagnostic in its assertion
failure; a passing control does not otherwise print its expected error.

validate.js verifies source scope, exact production overlays, baseline binary
identity, feature transformation, the unchanged complete catalog, all five
case rosters, failure attribution and complete five-disposer drains, plus the
fixture-only mutation and filesystem probe. Matrix SHA256:
c22869b9aa7464e9d2b8b2b6cb74f28b86d24fdc30722699513d4a6372faa81c.
Two feature patch attempts were rejected before any new tests ran; the final
feature transformation is checked byte-for-byte by the validator. No failed
patch attempt is counted as test or mutation evidence.

Vet and both native suites pass (33.933s), including v5 protocol and real
hold/release checks. Full Brine passes 573/573; the catalog remains 1064.
Coverage is 1872/2368 production statements (79.054054%), above the unchanged
50% package gate. The only block absent relative to the preceding profile is
github.com/concourse/concourse/atc/worker/jetbridge/daemon_client.go:224.18,228.5:
the three-statement daemon-probe cancellation variation already documented,
not a handoff regression. All other covered blocks are unchanged; the stable
covered count remains 1872. Summed scenario time is 146899ms, not wall time.
Profile /tmp/brine-coverage.IohhKI/coverage.out, SHA256:
d83d2262ff4d19ecbeaa944954b9405533f09e019a6581fea589941ca6b48d2b.
Restored normal adapter matches new-control, SHA256:
355e5efe7eb0d43f4dafc8bc1bd327e42ec0d70930872c5af3c9fd20c25635ab.
verify-gates.js/gate-proof.json bind the result to the unchanged Brine
CLI/engine/SDK and collector. All run handles are terminal, git diff --check
passes, and no new live Brine/API/etcd/collector processes remain.

The injected localExecutor.failure option still has one consumer: F23's
severed-connection fixture in pod_failure.go. That case does not actually run
a writing task or sever a connection. Host executors, installed resource
scripts, severing executors, canned sidecar logs, fake clients and runtimetest
substitutes still prevent a no-doubles completion claim. Real cluster execution,
expired-watch recovery and CI delivery remain unfinished. No production source
or legacy Go test changed, and no shared-cluster write, publication, commit or
push occurred. The goal remains active; work is local on core.


## Last fake watch replaced by a real, currently failing expiry case (2026-09-11)

The watch feature no longer uses client-go fakes or a fabricated Status-then-Pod
stream. Its last case now starts an owned kube-apiserver/etcd with watch caching
disabled, reads the real initial pod/version through production PodWatcher,
updates that same pod to Running then Succeeded, and physically compacts only
that owned etcd instance at its actual revision.

An independent production WatchPod call must receive ERROR/Expired/410 and then
channel closure. Only after proving that premise does the fixture call the
runtime's existing watcher. A subsequent fresh API Get must still find the same
UID, current resource version and Succeeded phase. The shared phase assertion
requires successful recovery: deadline expiry is a failure, never a passing
expected-error outcome. Production panics become named failures, not a dead
adapter. No synthetic event or API response is supplied to PodWatcher.

The new fixture reuses RealWatch, its pod/route/watch ownership constructor and
the existing phase assertion. Six fake-only definitions and the WatchedPod and
WatchObservation states are removed; two real setup/action definitions replace
them. The complete catalog decreases from 1064 to 1060. All nine expanded watch
cases remain. The shared resource roster is unchanged: Brine eagerly acquires
scenario resources, so the dedicated expiry control plane is started on demand
by its setup step and stopped by a recorder disposer, not registered as a new
resource that would start for every unrelated scenario. Shared envtest startup
is extracted without changing its normal watch-cache setting.

Evidence: /tmp/brine-watch-compaction.8mtMVF. The first attempted baseline lacked
BRINE_OTELCOL_BINARY and failed setup; old-missing-collector.log is retained and
excluded. With the required collector path, the captured old binary passes 9/9.
The migrated binary runs twice against fresh control planes: both runs pass the
same eight unchanged cases, pass all three expiry setup/action steps, then fail
only the final phase assertion with "context deadline exceeded". The expiry
fixture drains all four owned disposers, with partial=false, on both runs.

Vet and both native suites pass (37.256s), including vocabulary and v5 protocol/
hold-release checks. validate.js checks the exact common step outcomes, the
real-case failure location, disposal counts, resource roster, catalog changes,
unchanged production watch.go, and the rebuilt normal adapter's identity. Its
source snapshots contain one extra trailing newline from capture; the validator
accounts for that single byte explicitly, not by trimming arbitrary changes.
Evidence JSON SHA256:
895aca797f904762595f5749c8709a8c3ba609e0162e54caa17248e1fe716694.
All run handles are terminal; no recent live Brine/API/etcd/collector processes
remain. git diff --check passes. The normal .build adapter now matches this
source, so the old fake-backed green binary is not left as the default runner.

This checkpoint intentionally leaves a real regression red. The production
recovery fix is still awaiting authorization; no production file was edited.
A passing real-recovery control and paired production mutation validation are
still required before this watch migration can be called complete. No legacy
Go test was retired. The full suite and coverage gate were not rerun in this
checkpoint; the preceding 573/573 and 79.05% measurements describe the earlier
source, not a current all-green claim. Other runtime doubles and CI image/
cluster delivery remain unfinished. No shared-cluster writes, commit, push,
image publication or deployment occurred. The goal remains active.


## Real OCI registry foundation for scanner migration (2026-09-11)

A current-state audit found an additional unresolved double beyond the
Kubernetes runtime inventory: resource_checking.go's imageRegistry implements
imageresolver.Resolver with in-memory digest/credential maps and an injected
panic. This is not a real OCI registry. Its comment claiming that dependency
changes were outside migration authority was stale and is corrected. The
scanner scenarios still use that double; no removal is claimed in this checkpoint.

steps/image_registry.go now provides a tested replacement service. It serves
go-containerregistry's OCI registry over owned loopback TLS, publishes actual
images with distinct configuration labels, hashes each real manifest before
publication, and verifies the registry retained its exact bytes. Resolution
uses production imageresolver.NewResolver, not a fixture implementation of its
interface. Private registries authenticate through Docker Distribution's actual
htpasswd access controller and bcrypt file, not a test-written comparison.
Fixture credentials are synthetic local values, stored with mode 0600 in an
owned temporary directory. The production empty multi-keychain avoids reading
host credentials, and the TLS client trusts the fixture certificate.

The native integration test exercises public and private registries, two
repositories/tags with different digests, default latest, real missing-image/tag
404s, real missing/wrong-user/wrong-password 401s, successful resolution after
refusals, tag movement, listener closure, and credential-directory removal.
This creates a real local path for the remaining scanner migration; it does not
require a kubelet or any shared-cluster writes.

Evidence: /tmp/brine-oci-registry.towNXL. Vet and both native suites pass
(48.038s; the public/private registry check itself took about 0.05s). Two
production-only resolver mutations compile and fail the intended checks:
return a wrong digest (both public/private) and drop explicit authentication
(private only, with a real 401). A separate fixture mutation makes both images
identical and fails both subcases; this is fixture fidelity, not production
coverage or legacy-test retirement. The control passes both subcases.
validate.js checks source overlays, test outcomes, production source unchanged,
and module/checksum changes. Evidence JSON SHA256:
0eccc1de4a79042e940a39c1a830ebf917272b24a339fdd450d78af7b52bb4be.

No dependency version was upgraded. Docker Distribution, go-containerregistry,
and x/crypto were already pinned and are now direct imports. The nested module
adds mux v1.8.1 and its two checksums, matching the root module's existing pin.
An oversized snapshot patch was rejected before changes; the successful smaller
patch preserved the pre-edit module/hash evidence. The first test compile used
a nonexistent remote.TransportError name; it was corrected to transport.Error
before the passing runs. Failed setup/compile attempts are not counted as tests.

Next: adopt the service in resource-checking scenarios, replace invented digest
strings with independently hashed image identities, preserve per-repository
credentials and tag changes, and pair the scanner's distinguishing production
mutations. Do not replace the panic-isolation obligation with an HTTP failure:
a server failing a request is not a panic in the scanner's goroutine. That
remaining fault-injection case needs its own explicit disposition.

No Brine scenario or legacy Go leaf was changed or retired here. Catalog count
remains 1060, and the previous real expired-watch regression remains red pending
authorized production recovery work. The full Brine/coverage gate was not rerun;
the historical 79.05% remains a measurement of the earlier source, not a claim
that the current complete suite is green. All native/mutation handles are
terminal and no recent test/API/etcd/collector processes remain. Changes remain
local and uncommitted; no external publication or deployment occurred. The goal
remains active.


## Scanner scenarios resolve real published OCI images (2026-09-11)

The resource-checking feature now uses the production imageresolver against real
TLS OCI registries. The former imageRegistry/registryImage digest and credential
maps, Resolve implementation, and invented MANIFEST_UNKNOWN/UNAUTHORIZED replies
are removed. scanImages maps select fixture inputs and expected manifest hashes;
the scanner's ordinary resolver does not read those maps. Private repositories
have separate Distribution-authenticated registry endpoints, so resource and
resource-type credentials remain independently distinguishable. No host
credentials or external registry is used.

The existing scenarios publish named images instead of claiming arbitrary strings
such as sha256:private-app are digests. Expected hashes come from the actual image
manifests before publication; publication also verifies their bytes over HTTP.
The source supplied to the scanner carries the owned registry address and the
original repository/tag. An omitted tag remains omitted for production to default.
The pinned-pull assertion independently combines the fixture's requested repository
and published image digest; it does not read the SUT's source to construct its
expected value. Both resource/type digest checks share resolvedScanImage.

Seven step patterns change, with no new or removed scenario or definition:
23 expanded scanner cases remain, and the complete catalog stays at 1060. The
catalog's requires/provides shapes remain equivalent after the registry state
change; three former Refine steps now use fallible Transform for real operations.
The native vocabulary guards and v5 protocol tests pass.

One resolver substitute remains explicit: panicImageResolver, installed only by
"a resolver panic is injected for ...". Before injection, the step requires that
the real image resolves. The normal scanner does not pass through this wrapper.
An HTTP server failure would not panic in the scanner's goroutine, so replacing
that obligation with an HTTP refusal would be a different test. Its final
no-doubles disposition remains unresolved, not declared an exception or complete.

Evidence: /tmp/brine-scanner-registry.t5PLAt. Old/new controls both pass 23/23.
Six scanner mutations retain all 18 scenario/fault pairs for resource/type auth,
resource/type saved digests, type intervals, and resource check-never. A seventh
paired production mutation changes ResolvedImage's @ separator to :, and the
pinned-pull case fails before/after: 19 preserved pairs in total. Three diagnostics
are identical; sixteen now report real image hashes or the real registry address.
An additional production OCI-resolver mutation returns a wrong digest: the old
suite detects none, while the migrated suite detects it in 12 scenarios.

The additional old pull-reference run reconstructs the captured old fixture via
Go overlay and deletes the new helper from that build. Its restored control also
passes 23/23. Every mutation uses an explicit binary/manifest, not a runner.binary
configuration override. A fixture-only mutation disables the explicit panic:
only the panic case fails, because the published image is actually resolved and
attached. This is fixture validation, not a production mutation or test retirement.

validate.js verifies the executable feature transformation, complete catalogs,
all case rosters, exact executed step prefixes through each intended failure,
source overlays, production sources unchanged, and complete recorder drains.
Public registry cases add two disposers; the private case adds four. The initial
validator incorrectly expected steps after a failing assertion; it was corrected
to verify Brine's exact fail-fast prefix, with all preceding steps passed and the
last step failed. No test result was changed or rerun to accommodate that correction.
Matrix SHA256:
f1e548dd31d89e3f973f9556c8cab06bb9d4d6102ffaa5e172c9e2b7a4fbbd9f.

Vet and both native suites pass (42.998s). The unchanged full coverage gate was
run and correctly exits 1: 572 scenarios pass and only the already-known real
expired-watch case fails with context deadline exceeded. Summed scenario time is
160453ms, not a wall-clock benchmark. This is NOT a green full-suite checkpoint.
The gate retains raw coverage on failure; separate diagnostic conversion yields
1872/2368 JetBridge production statements, 79.054054%, with the exact same covered
blocks as the earlier IohhKI profile. This does not override the failed gate.
Profile: /tmp/brine-coverage.sIKY40/failed-suite-coverage.out, SHA256:
9c75fb927e4cae7a1659bbd1603ecb4ff8e38bd19d2b083034ce101aea695fa3.
verify-gates.js/gate-proof.json bind the failed full run, profile, matrix, restored
normal adapter and CLI/engine/collector/module hashes. The normal adapter matches
the new control. All run handles are terminal; no recent live Brine/API/etcd/
collector processes remain, and git diff --check passes.

No production source or legacy Go test was edited. The watch recovery fix still
awaits authorization; runtime execution doubles, the isolated resolver panic and
CI image/cluster delivery remain unfinished. No shared-cluster write, commit,
push, image publication or deployment occurred. Work remains local on core and
the goal remains active.


## Goal blocked pending authority/runtime access (2026-09-11)

After the scanner checkpoint, the current source and retained matrix/gate proofs
were revalidated byte-for-byte. The goal is now marked blocked, not complete;
its full objective is unchanged. No test is still running or being monitored.

The unanswered production-watch-fix and cluster-access boundaries have persisted
through more than three consecutive goal turns. Safe local migrations continued
while available, including real watch expiry and real OCI resolution. Remaining
runtime fidelity needs a real kubelet/exec/log environment: no Docker, Podman,
containerd, K3s, rootless runtime tools or runtime socket is available here. The
process lacks CAP_SYS_ADMIN and its cgroup controller mounts are read-only, so
a local privileged K3s testcontainers harness is not an available substitute.
Envtest supplies only the API server and etcd.

Resume requires direction on the production PodWatcher recovery fix and an
authorized runtime test environment (for example, resource-limited test pods in
an owned temporary namespace on the shared cluster, with cleanup). CI still
needs a newly built/published immutable runner image and updated pipeline refs;
no publication/deployment is authorized or claimed. The explicit resolver-panic
injector also remains an unresolved no-doubles obligation, not an accepted
exception. Current full result remains 572/573 with the expiry regression red;
79.05% package coverage does not make that gate green. Changes remain local and
uncommitted on core; no production fix, cluster write, commit or push occurred.


### Approved watch recovery and bounded live-runtime validation (2026-09-11)

The user approved fixing PodWatcher and running resource-limited workloads in
an owned temporary namespace on theborg. Work resumed under that scope; no
commit, push, image publication, pipeline update, privileged pod, hostPath
access, or mutation of unrelated cluster resources was performed.

PodWatcher now recognizes Kubernetes Expired/Gone failures both when opening
a watch and when receiving a terminal watch.Error Status. It stops/clears an
expired stream, reads the current pod, and resumes future watches from that
fresh resource version. Ordinary disconnects still replay from the previous
version; they do not get converted into current-state reads.

The existing real-expiry scenario now also publishes another annotation
version after recovery and deletes the pod before the next read. The watcher
must replay the exact annotation version with the same UID. A second fallback
Get cannot pass this assertion because the pod is already absent. No scenario
or step definition was added; all nine watch cases remain.

Evidence: /tmp/brine-watch-recovery.XhWrBd. The fixed control passes 9/9;
three independently built production fault variants each fail only the expiry
case, with all eight other watch cases passing:

- Original unfixed watcher: context deadline exceeded.
- Fallback Get does not update lastResourceVersion: subsequent Get is NotFound.
- Unsafe Pod assertion before terminal-Status handling: named conversion panic.

Explicit runner manifests identify every executable. validate.js checks the
complete case roster, unchanged step sequences, intended failure messages,
source snapshots, exit statuses, complete/non-partial recorder disposal, and
production-only coverage. No legacy Go test retirement is claimed here.
Evidence SHA256:
5e6c540020b66d2b7957837059ab334387de6122f395daaff569b42138846c08.

Vet passes; both native suites pass in 36.997s. The full coverage gate now
passes 573/573, with 1881/2379 production statements covered (79.066835%).
This is Brine-only coverage of atc/worker/jetbridge, not repository-wide
coverage. The suite reports 151411ms summed scenario time, not wall time.
Profile: /tmp/brine-coverage.VeG2VD/coverage.out, SHA256
 d537e238c70a45a2f66601e928004c72142974a23e8d1c602d43eb3e5cc4f305.
The script restored the normal adapter after coverage, SHA256
 e1074e5907bf6156c378bcfd7edf3a5ef31847c83c6d79fad2a156f11995719d.

A separate live probe (/tmp/brine-live-sidecar.JDh1SR/probe.go) uses the real
Brine database factories, Worker, Container, PodWatcher, supervisor and
SPDYExecutor. Its two BusyBox tasks run on theborg in the generated namespace
brine-review-k2xr5 (UID f5ddba86-b950-4baa-8485-c7bd8e236227). Namespace
admission enforces baseline pod security, a two-pod quota, combined limits of
one CPU/512Mi memory/256Mi ephemeral storage, and per-container defaults of
250m CPU/64Mi memory/64Mi storage. No hostPath or privileged containers are
requested. The local PostgreSQL runs as nobody inside an owned user namespace.

The dedicated-stream control passes: the unique sidecar marker arrives in its
writer and not main stdout. The fallback expectation fails: Kubernetes has
the exact unique sidecar marker, but production stdout contains only the main
command's line. Both main commands exit zero and both sidecars terminate zero.
Thus this is lost routing, not failed task execution or an empty source log.
The observed BusyBox digest is
sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0.

Probe cleanup deleted only its own namespace with a UID precondition and
waited for NotFound; a separate kubectl read confirms it is absent. Production
process.go and the two fake-backed SC-07 cases remain unchanged. Those cases
exercise the test-only Process path, whose fallback does exist; production
execProcess starts log streams only when a dedicated writer is provided.
Authorization was requested for this additional production fix and real-cluster
Brine replacement. The no-doubles objective remains incomplete, regardless of
the passing coverage threshold and current full-suite result.


### Approved sidecar fallback fix and real execution tier (2026-09-11)

The user approved fixing the confirmed production log-routing defect and
replacing its fake-backed Brine tests. execProcess now starts a log stream
for every configured sidecar with an available destination: a dedicated
writer takes precedence, otherwise output is labelled [name] in shared
stdout. Main exec and fallback writers share a mutex. Prefixing preserves
blank lines, fragmented network reads, long lines and unterminated final
lines without buffering a whole line. The existing five-second drain limit
is unchanged; a child context cancels remaining log readers when Wait exits.

The two SC-07 cases are now one outline with two rows under
features/live/sidecar-logs.feature. The live fixture uses real Brine database
factories, Worker, Container, supervisor, SPDYExecutor and kubelet logs. It
requires successful main execution, an actual scheduled pod/container ID,
successful sidecar termination, and an exact independent kubelet source log.
The payload contains a namespace-UID marker, a blank line, and an unterminated
tail. Assertions check every output byte and that dedicated output does not
leak into main stdout. There is no fake client, host executor, injected pod
status or canned log response in this path.

Six old step patterns were removed and three shared ones added. The catalog
shrinks from 1060 to 1057 definitions; unchanged definitions and resource
catalogs compare exactly. The fake-worker and fake-task-draft setup sentences
had no remaining scenario consumers and were removed. Total expanded Brine
case count stays at 573: 571 local plus two live. No legacy Go test was retired.
Vocabulary guards now scan the entire feature tree, including the live tier.

Live execution requires explicit BRINE_KUBE_CONTEXT. Each scenario creates
its own generated namespace, baseline pod-security policy, two-pod quota,
aggregate one-CPU/512Mi-memory/256Mi-ephemeral-storage limits, and bounded
per-container defaults. Cleanup uses a namespace UID precondition and waits
for NotFound. It never adopts a namespace, alters nodes, grants RBAC, requests
privilege, or mounts hostPath. The live launcher keeps the host network route
but maps root to nobody in a private user namespace for owned PostgreSQL.

The CI Brine task explicitly selects in-cluster service-account credentials.
It must already have namespace and namespaced test-resource permissions;
none were granted here. The previously required matching runner-image rebuild
and tag bump remain outstanding. No pipeline was applied, image published,
commit created or branch pushed by this work.

Brine discovers nested live/.brine automatically from the suite root. An
initial coverage run redundantly invoked the live tier again; that invocation
was removed. A subsequent verifier exposed another reporter trap: brief
results are printed after manifest headers, not within each header's section.
The final gate therefore uses JSONL and cmd/brine-verify-run to require actual
passed scenario_end records naming each required manifest and a run_end.
Native tests reject missing/empty tiers, unfinished runs, failed/skipped cases,
unexpected manifests and failed summaries. The final full gate has one CLI
invocation and merges the instrumented counters from both adapters.

Evidence: /tmp/brine-sidecar-migration.TfaLnQ. Restored old control and final
new control both pass their two cases. Two production fault pairs preserve
the old obligations: discarded dedicated bytes and a wrong fallback label.
Two additional production faults are caught by the new fallback row: the
original missing production fallback, and dropping the empty payload line.
Each new fault fails only its intended Then; all setup/execution steps pass.
Explicit runner manifests select each overlay-built binary. validate.js checks
case rosters, executed step sequences, expected failing rows, catalog changes,
source snapshots and complete recorder/namespace cleanup.
Mutation evidence SHA256:
7abf9d8f72143dc32a2207c7515953184ed241d7c9059d802705ba9ce964e3c7.

Final validation:

- Vet passes; all three native suites pass in 36.574s.
- The formatting/shared-output native test passes (2.332s standalone).
- Retained sidecar Ginkgo specs pass 9/9; no Go retirement is claimed.
- Final JSONL full gate: 573 passed, zero failures, exactly 571 local + two live.
- Brine-only production JetBridge coverage: 1885/2417 = 77.989243%, above 50%.

The lower percentage versus the prior fake-backed suite is not concealed:
the fake-only legacy log path no longer contributes those executions, while
the real production path and new formatting implementation are measured.
This is package-scoped Brine-only coverage, not repository-wide coverage.
Final profile: /tmp/brine-coverage.ZWA0Pw/coverage.out, SHA256
c7ec6b4a239e5be0c2573e4116ff668eab8e200dbbb442a7036bc09bb92ce4d3.
Structured gate proof SHA256:
039577162aa86a0ae500ef461a0551aca1b7e4405df158db0987b7d0f4391f4c.
Restored normal adapter SHA256:
ef6caf2e71424552c7fb267c421b4fb793cd50af88874043c11c11711e2a1899.

The final live namespaces brine-runtime-58hpf and brine-runtime-tqfw9 were
removed; recorder logs verify their exact UIDs and a separate label-scoped
kubectl read finds no surviving Brine-runtime namespaces. All mutation
namespaces likewise have matched create/remove receipts. No recent test
adapter, API server, etcd or collector processes remain.

The larger no-doubles goal is still incomplete. NewCluster remains for F23's
injected exec failure and the two severingExecutor OOM/vanished-pod cases;
other host executors, step-execution stubs and panicImageResolver also remain.
The owned live fixture is available for migrating those runtime obligations
without adding another parallel fixture vocabulary.


## Real vanished-pod exec and approved status fix (2026-09-11)

Replaced the RF-15 vanished-pod fake factory with a real task, production
worker and SPDY executor in the existing bounded live tier. The feature moved
from pod-lifecycle.feature to live/exec-interruption.feature. One obsolete
Given was removed and two live Given/When definitions added (1057 to 1058);
the scenario count stays 573. Existing ProcessOutcome assertions are reused.
The OOM severingExecutor remains explicitly labelled as a diagnostic double.

The fixture observes actual task stdout, cuts a transparent TCP route, proves
with an independent real exec that the child survived, then deletes the exact
pod UID and waits for API NotFound. Output-writer backpressure ensures runtime
diagnostics happen after deletion. No executor error, exit status, Kubernetes
response or pod status is manufactured. The independent probe also checks
real exit zero and exit seven without another pod or extra scenarios.

This exposed a production bug: client-go accepts an empty error stream as
success when the real SPDY socket closes. The restored fake case passed; two
real runs failed at the existing step-failure assertion with exit zero and no
error. A full pre-fix run confirmed exactly 572 passed and that one failure
(/tmp/brine-coverage.2T3H8e); this failed gate did not establish passing coverage.
Initial route setup (missing default HTTPS port) and missing collector-path
attempts were fixture/environment failures, excluded from regression proof.

After approval, executor.go uses the standard client-go transport with a
small delegated status-stream check in exec_status.go. For negotiated
Kubernetes exec v4/v5 only, EOF before any remote status bytes is an error;
client-go still decodes genuine statuses and nonzero exits. Older protocols
retain their legitimate empty-success behavior. These Kubernetes protocol
versions are unrelated to Brine's v5 adapter contract. The runtime can now
enter its existing missing-pod diagnostic path. process.go was unchanged in
this increment; its earlier approved sidecar edits are preserved.

Evidence: /tmp/brine-live-exec.JqDKLe. Fixed live control passes all three
cases. An overlay removing only the production fix fails only the vanished
case at the intended Then while both sidecar cases pass. A separate paired
mutation removes the missing-pod diagnostic wording: both restored old and
new cases fail only their build-log assertion, preserving the old obligation.
No Go test is retired. All mutation overlays and binaries are outside the
working source; the final normal adapter is restored by the coverage script.

Final validation:

- Build and vet pass; all three native suites pass in 39.707s.
- Full JSONL gate: 573 passed, zero failed (570 local and three live).
- Brine-only production JetBridge coverage: 1904/2439 = 78.064781%, above 50%.
- Profile: /tmp/brine-coverage.rP6TEG/coverage.out, SHA256
  8548315dbd2dc6df24c21c79159b3e8b57489f5be1d6d12e2d7e1c8dd15b67bd.
- validate-fixed.js checks controls, exact failing assertions, complete
  recorder drains, namespace UID receipts, full manifests and coverage.
  fixed-evidence.json SHA256:
  c922e2aefc80814ae444afc65f79f00e01394cb6738b60cc1d6a17db0ac7343c.
- Independent final reads find no remaining Brine-runtime namespaces or
  recent adapter/API-server/etcd/collector processes. No nodes, hostPath
  volumes, privileged workloads or cluster-wide RBAC were changed.

The no-doubles goal remains incomplete: NewCluster is still used for F23's
injected exec failure and the OOM diagnostic case. Host-command executors,
step-execution stubs and panicImageResolver also remain. Next migrate those
remaining runtime obligations through the shared owned live fixture, with
real failure premises and mutation proof. No commit, push, image publication
or pipeline application was performed in this increment.


## Real OOM diagnostics and shared interruption vocabulary (2026-09-11)

Removed the remaining severingExecutor, its OOM factory and the now-orphaned
newSeveringWorker/newConfiguredWorker setup. The RF-15 OOM case moves from
pod-lifecycle.feature into live/exec-interruption.feature. Both interruption
cases share one Given and one parameterized When. Their distinct diagnostic
assertions remain explicit; no new expanded scenarios were added.

The native unused-definition guard found two additional orphaned steps after
that move: the fake task-running setup and the reported-running/exec trigger.
Both were removed, not exempted. Final vocabulary is 1056 definitions versus
1058 before this increment (five removed patterns, three added). Resources
are unchanged. ProcessOutcome gains the actual NodeName observed before
waiting completes; catalog changes on retained definitions are exclusively
that shared state's requires/provides field. Other retained contracts match.

The OOM premise is real. The runtime reuses an owned, resource-limited pod
whose PID 1 waits for an arming file and then allocates beyond its memory
limit. The task executes through the production worker/executor. The fixture
observes actual task output, cuts the transparent exec connection, proves the
task survived that cut and verifies genuine zero/nonzero remote exit handling.
Before arming PID 1 it reads the actual 64 MiB cgroup limit, supporting v1 and
v2 paths. It then requires the same pod UID, a real container ID, and kubelet
termination reason OOMKilled with exit 137. Only after this does it release
output backpressure so production diagnoses the interrupted connection.
The build-log node assertion uses the observed scheduled node, not a fake
fixed node name. No node, cgroup configuration, signal or status is modified.

Using PID 1 establishes container death even on runtimes without group OOM
killing; an OOM of only an exec child would leave a pause container alive.
Initial v2-only/group-OOM and v2-only/PID-1 prerequisite checks failed before
arming; these are excluded from behavioral proof. The final v1/v2 limit
check passes. No production source changed during this increment.

Evidence: /tmp/brine-live-oom.oNdMTd. The restored old OOM control passes
one case; the real interruption control passes both cases. Paired production
mutations replace the printed OOM reason and print the wrong node. Each old
case fails its intended log assertion; each new run fails only the OOM case's
corresponding assertion while the vanished-pod case still passes. Real OOM
identity/status receipts and complete disposers remain present in fault runs.
No Go test is retired. Overlays and mutation binaries remain outside source.

An initial full run passed 573 cases, but the native guard still reported the
two unused definitions. After deleting them, both native suites and the full
coverage gate were rerun against the final source:

- Build and vet pass; all three native suites pass in 36.819s.
- Final JSONL gate: 573 passed, zero failures (569 local, four live).
- Brine-only production JetBridge coverage: 1891/2439 = 77.531775%, above 50%.
- The decrease from 78.064781% is retained in the report: fake-only execution
  paths no longer contribute coverage. Production denominator is unchanged.
- Final profile: /tmp/brine-coverage.1lgZQG/coverage.out, SHA256
  8d14524a818ee42b1aedab7b44f730b9fc1d725e9128ffdbfb5cb29871c4fe0b.
- validate.js verifies controls, mutation assertion locations, real OOM
  receipts, catalogs, source snapshots, complete drains, exact namespace UID
  create/remove pairs, both full manifests and the coverage denominator.
  evidence.json SHA256:
  05897de601e531f9b64c1e6df9a25dadd8275a43a61ddb6531752e9717075222.
- The normal adapter was restored; independent final checks find no owned
  live namespaces or recent test adapter/API-server/etcd/collector processes.

COMPLETION-AUDIT.md is now clearly labelled as the historical 2026-09-08
checkpoint, not proof of the active v5/no-doubles goal. That goal remains
incomplete. NewCluster's only remaining scenario consumer is F23's injected
exec/artifact failure; host-command executors, step-execution stubs and the
panic resolver also remain. The next runtime migration is F23, requiring a
real mid-write disconnect and real artifact non-publication. No commit,
push, image publication, pipeline application or cluster-wide grant occurred.


## F23 storage boundary: approval required (2026-09-11)

Current-source inspection confirms F23 is the last NewCluster scenario
consumer. Its existing fixture injects an exec error before running the
command (which is only echo hi); no half-written output exists. A faithful
replacement must observe real bytes, disconnect while the task remains alive,
and test non-publication through the production storage backend, with a
successful publication control and paired mutation checks.

There is only one production StorageBackend implementation: DaemonSetBackend.
Its StepVolume and ArtifactStoreVolume use hostPath, and the output-recording
path assumes those bytes already exist there. The nil-backend emptyDir path
returns before RecordOutputs, so it cannot establish the required publication
obligation. Replacing backend volumes with emptyDir would substitute storage
semantics rather than test the production path. No Docker, Podman, nerdctl or
runc executable is available locally as an alternate real runtime.

The previously authorized live fixtures use baseline pod-security namespaces
and no hostPath. This increment therefore made no cluster or test-code
changes. Approval is needed for a newly owned hostPath test directory on
theborg, a policy exception confined to the generated test namespace, bounded
artifact writes by non-privileged pods, and verified cleanup of only that
owned directory. No existing application storage or node-wide policy should
be changed. Until approval, retain the F23 double visibly; do not replace it
with a nil-backend test or call the no-doubles goal complete.


## Real set_pipeline artifact consumption (2026-09-11)

HostPath approval for F23 has not been received. That boundary was left
untouched. Independent progress removes the stub volume from the five
set_pipeline cases in step-execution.feature; this is consumption of an
already-existing artifact, not a substitute for F23's producer publication.

runSetPipeline now uses the existing startRealDaemon, writeArtifactFile and
registration helpers to serve actual owned local files through the production
artifact-daemon and NewDaemonSetVolumeFromIP. The real SetPipelineStep and
Streamer consume the requested pipeline.yml through HTTP, tar filtering and
gzip. A valid a-decoy.yml sorting before it makes ignoring the requested path
observable. The recorder stops each daemon and removes its exact owned root;
a bounded context covers artifact registration and the real step execution.
No production source changed, no helper vocabulary or scenarios were added,
and the get/put/task substitutes elsewhere in step_execution.go remain visible.

Evidence: /tmp/brine-pipeline-artifact.A23rvn. Old and new controls both pass
all five identical scenarios. Paired production mutations disable the no-diff
branch and the cross-team permission check; old and new each fail only the
same intended assertion and pass the other four cases. An additional real
reader mutation skips tar subpath filtering: the decoy is consumed, reddening
the no-change, invalid-config and authorized-cross-team cases. The older-build
protection and unauthorized-team cases still pass. Thus the new file read is
not inert setup. All Given/When steps pass in these mutation runs.

The v5 catalog's steps, resources, capabilities and all non-closure fields
are identical: 1056 definitions. Its binary closure necessarily differs by
executable path, byte size and digest; validate.js verifies each entry against
the actual selected old/new binary rather than ignoring that difference.
No Go test is retired. All mutation sources and binaries remain outside the
working production tree.

Final validation:

- Build/vet pass; all three native suites pass in 38.492s.
- Full JSONL gate: 573 passed, zero failed (569 local, four live).
- Brine-only production JetBridge coverage: 1895/2439 = 77.695777%, above 50%.
- Profile: /tmp/brine-coverage.Fb0Kph/coverage.out, SHA256
  4d623fff223a99e37e4df59b9e7255d4abdc8f36e9aa7c23c1d506f31e1394d9.
- validate.js verifies paired rosters/assertions, real-daemon start/stop
  receipts and removed roots, complete recorder drains, the full manifests,
  exact live namespace UID cleanup, catalog contracts and binary hashes.
  evidence.json SHA256:
  cd7470ba481a1162db93851d6fd88a827138ccd9806d043a400aee409bba475a.
- The restored normal adapter matches the tested new-control binary:
  ce999dde9009a1c433f6e7ae6e6cc5b342a904a9289abd08add843ec0684fdc6.
- Independent final checks find no owned live namespaces or recent test
  adapter/API-server/etcd/collector processes. All pipeline daemon roots are
  absent. Existing live fixtures used only their authorized namespace scope.

The active goal remains incomplete: F23 requires the outstanding hostPath
permission; resource get/put processes and their pool, task fixtures,
host-command executors and the panic resolver still need migration or a
faithful disposition. No hostPath access, namespace policy exception, commit,
push, image publication or pipeline application was performed.

## Real task preflight dependencies (2026-09-11)

The missing-image and missing-input task cases now supply the production Pool
and DefaultFactory, real DB factories and a worker persisted in that database.
The declared real-cluster resource supplies the local envtest API. An
independent selection checks that the pool can select that worker before the
production TaskStep runs. The step rejects the invalid task before execution;
this does not claim a task ran in envtest.

The present input now contains real bytes, served by the production artifact
daemon and verified through Streamer before registration. Pipeline and task
fixtures share serveExecArtifact, replacing duplicated artifact setup without
new scenario vocabulary. Each recorder stops its daemon and removes its owned
directory. Task namespaces are local envtest objects; their pods are cleaned
by the recorder and the owned API-server teardown removes the namespace state.
No hostPath or live-namespace security exception was used.

Evidence: /tmp/brine-task-preflight.znf9UH. Old and new controls both pass
the same seven cases (five pipeline, two task). Three isolated production
mutations alter the missing-image diagnostic, treat an optional input as
required, and report the unmapped rather than mapped input name. Each old/new
pair passes six cases and fails only the same intended assertion; all
Given/When steps pass. The real pool selects its worker in both new task cases,
and all six new artifact daemons have paired shutdown receipts and absent
owned roots. Production task_step.go matches its pre-mutation snapshot.

The catalog remains at 1056 definitions. Its only non-binary changes are the
task action's explicit real-cluster dependency and the artifact action's source
location after moving from Refine to DefineMap. Patterns, scenario count and
the other contracts are unchanged. Binary closures were verified against the
actual old/new executables. No Go test was retired.

Final validation:

- Build/vet pass; all three native suites pass in 40.729s.
- Full gate: 573 passed, zero failed (569 local, four live); manifest execution
  totals 285.074s, excluding compilation and coverage processing.
- Brine-only production JetBridge coverage: 1893/2439 = 77.613776%, above 50%.
- Profile: /tmp/brine-coverage.K571vi/coverage.out, SHA256
  458d69df3be8b0c24c7873ea2f563c83e638090ab35ea336a73d4764f7b6c3bd.
- validate.js checks rosters, precise mutation assertion failures, complete
  recorder drains, daemon cleanup, worker selection receipts, catalog changes,
  both full manifests, namespace UID cleanup, source and executable hashes.
  evidence.json SHA256:
  6e1ecf23d61b285bdfeeb9f66ef654b6a2bc5e0c4e528f8c721fe81b53219de8.
- The restored normal adapter equals the tested new-control binary:
  6aeaf3e5447c66259dbb81e646f0668f26e11f44755dc784da1598f26437fb1b.
- Independent checks find no owned live namespaces or recent test adapter,
  API-server, etcd or collector processes.

The broader goal remains active. F23 still needs the outstanding hostPath
permission; get/put stubs, host-command execution fixtures and the panic
resolver remain. This increment is uncommitted on core. No push, image
publication, pipeline application or cluster-wide grant occurred.

## Real retry-classification failures (2026-09-12)

The three RetryError cases no longer use execStepPool.selectErr or a
manufactured url.Error. They run the production PutStep, engine delegates and
database-backed Pool/DefaultFactory. Task preflight and retry cases share the
real pool setup. Remaining get/put script doubles are initialized only when
those cases request them; the retry action rejects a populated substitute
worker or pool. This is not removal of the other resource-script doubles.

For the transient case, an owned transparent TCP route first reaches the real
envtest API, then closes. The actual pod-create request fails with the kernel's
ECONNREFUSED, wrapped by the HTTP client. Both the syscall error and url.Error
identity are verified. No API response or worker error is supplied by a stub.

Two old premises needed correction:

- The Kubernetes worker treats an unmapped resource type as an image name; it
  does not emit the old fixture's "unknown resource type" error. The permanent
  pipeline-error row now requires a missing input and observes the real
  PutInputNotFoundError. This preserves the classification obligation, not a
  claim that the worker validates unknown resource-type names.
- Pre-canceling the request produces plain context.Canceled before transport.
  That error is not otherwise retryable, so removing the abort guard would
  leave such a test green. The final case aborts at the real failed pod-create
  request. A forwarding transport observer calls the actual transport first,
  cancels only after its POST returns ECONNREFUSED, and returns that exact
  response/error unchanged. The real retryable failure survives, while the
  wrapper sees a canceled build. The Given now names that checkpoint.

This tests classification and cancellation, not resource-process execution or
artifact publication. All API activity is local envtest; no hostPath, live
namespace policy exception, fabricated Kubernetes status or manufactured
process result is involved. The original Go tests remain.

Evidence: /tmp/brine-retry-real.0sd1Se. Old/new controls pass the same 22 case
names. Four paired production mutations disable retry classification, classify
all errors as retryable, remove the build-log retry notice, and remove the abort
guard. Every old/new mutant passes 21 cases and fails only its intended
assertion; all Given/When steps pass. In particular, the final abort mutant
fails the "not marked for retry" assertion, not setup. Earlier failed premise
experiments and missing-collector runs are retained separately, not counted as
successful evidence.

Source and feature introductions now describe the remaining doubles plainly,
replacing repeated historical claims that stateful substitutes were "working"
production implementations. No cases or definitions were added: 573 scenarios
and 1056 definitions remain. Catalog changes are exactly two corrected Given
patterns and the retry action's declared real-cluster dependency; all other
non-binary fields match. Binary closure hashes match the selected executables.

Final validation:

- Build/vet pass; three native suites pass in 41.414s.
- Full gate: 573 passed, zero failed (569 local, four live); manifest execution
  totals 284.840s, excluding compilation and coverage processing.
- Brine-only production JetBridge coverage: 1897/2439 = 77.777778%, above 50%.
- Profile: /tmp/brine-coverage.PP5bFo/coverage.out, SHA256
  1a60e4066492b075c3663ea345c2878d46827effaa1829b25043e222fa08601f.
- validate.js checks both controls, eight mutants, exact assertion failures,
  complete drains, real route/abort receipts, daemon roots, namespace UIDs,
  catalogs, both manifests and coverage. evidence.json SHA256:
  96bd71c5f367ee1800edaaff7513126757bf43c4ee7eff858aa428c24c8b4b6d.
- Restored normal adapter equals the tested new-control binary:
  bff888a96f127359cbaf2a94b776ef021bb81ce52fdecfe22f0c6bc3d27c7897.
- Production retry_error_step.go matches its snapshot; git reports no edits
  to that file or put_step.go. Independent checks find no owned live namespaces
  or recent adapter, API-server, etcd or collector processes.

The goal remains active: F23's storage permission is outstanding, and get/put
processes, hook/attempt fixtures, host-command executors and the panic resolver
still require migration or faithful disposition. Work remains uncommitted on
core; no push, image publication or pipeline application occurred.

## Real cached-get association and bytes (2026-09-12)

The cache-hit scenario no longer answers from execStepPool's version-keyed
map or runtimetest.NewVolume. Its Given creates a resource cache owned by a
separate producer build, a real worker base-resource-type association, and a
created resource volume initialized as that cache in PostgreSQL. The actual
version file is served by the production artifact daemon, discovered through
a real envtest EndpointSlice. The get uses the production Pool, DefaultFactory,
GetStep and engine delegate.

Task/retry/cache cases share the same real-pool setup. Pipeline/task/cache
artifacts share daemon lifecycle and file registration helpers. The cache map,
its mutex and holdsCacheOf method are removed. The remaining resource-script
pool explicitly reports no cache; the real cache action rejects any substitute
worker or pool being present.

Before running the get, the fixture independently resolves the DB association
through the production pool and reads the cached bytes. The existing provenance
assertion now checks fromCache, reads the returned artifact's version file
through the real Streamer/daemon, and verifies that no resource pod exists.
Thus it checks usable cached data, not only a boolean next to an artifact name.
No scenarios or step definitions were added.

This is consumption of an existing cache, not execution of its producer or
proof of resource-script execution. The daemon owns only a temporary local
directory. Its path configures the production storage backend, but no hostPath
is mounted and no live-cluster policy exception is used. On a cache-miss
mutation, envtest can accept the pod object but cannot execute it; an eight-
second deadline bounds that path, and recorder cleanup removes the object.
The passing control requires no resource pod.

Evidence: /tmp/brine-cache-get.yZhkjQ. Old/new controls both pass the same
22-case step-execution feature. Paired production mutations:

- Ignoring the version pin fails exactly the ordinary pinned-get and cached-get
  success assertions in both versions (20 pass, two fail).
- Mislabeling a cache hit as freshly fetched fails only the cache provenance
  assertion in both versions (21 pass, one fail).
- Bypassing the cache fails only cached-get success in both versions
  (21 pass, one fail).
- Registering a nil artifact passes all 22 old cases, but fails the new cached-
  artifact assertion with "cached get registered no readable artifact"
  (21 pass, one fail). This is an additional discriminator, not equivalence
  claimed for an old assertion that never checked the artifact's usability.

All Given/When steps pass in every mutation run. Production get_step.go,
version_source.go and worker/pool.go match their snapshots. No Go test was
retired. The v5 catalog remains at 1056 definitions; its only non-binary changes
are the cache Given's explicit real-cluster dependency and source location.

Final validation:

- Build/vet pass; three native suites pass in 41.076s.
- Full gate: 573 passed, zero failed (569 local, four live); manifest execution
  totals 287.494s, excluding compilation and coverage processing.
- Brine-only production JetBridge coverage: 1897/2439 = 77.777778%, above 50%.
- Profile: /tmp/brine-coverage.ZnHop7/coverage.out, SHA256
  07ade3acf4ba1f5541639125a978f66a437bdbe340a263559033dcf4e27a73f0.
- validate.js checks exact control/mutation rosters and assertion failures,
  cache-read receipts, real retry checkpoints, complete drains, seven daemon
  start/stop pairs and absent roots, live namespace UIDs, catalog/binary
  closures, both manifests and coverage. evidence.json SHA256:
  df3e4a2cdb74357febfec235551db31da0c44db981ff86d2741bea3416b9679d.
- Restored normal adapter equals the tested new-control binary:
  92e26ed2747534dd53cca69a8482b3c635706cf3d7c72905a0fa9505ae42555d.
- Independent checks find no owned live namespaces or recent adapter,
  API-server, etcd or collector processes.

The broader goal remains active: uncached get/put scripts and their worker/
process/volume substitutes, hook/attempt fixtures, host-command executors and
the panic resolver remain. F23 still awaits its separate storage permission.
Changes are uncommitted on core; no push, image publication or pipeline
application occurred.

## Real volume I/O and remote terminals (2026-09-12)

Four direct-volume cases and the two terminal outline rows moved to the live
tier without adding scenarios or definitions. Volume fixtures create bounded,
non-root BusyBox 1.37.0 pods with emptyDir storage, verify actual kubelet
container identity and mounts, and independently check the exec hostname.
Their production deferred volumes use the real SPDY executor. They neither
rewrite paths nor create extraction subdirectories. This is direct volume
API behavior, not daemon-backed producer publication.

Terminal fixtures now use the actual worker, SPDY transport and kubelet
status. The probe keeps stdin open until command completion; immediate EOF
in the original port lost terminal output. This was corrected in the fixture,
not production. A non-nil input bypasses supervisor log replay, as before.
The TTY action's v5 input contract shrank from WorkerReady to the database
dependency; VolumeSet no longer carries TaskWorkspace. The identity-only
volume case also uses the real executor/API resource, without claiming I/O.

Evidence: /tmp/brine-live-io.uNNEKI.

- Original seven-case control: seven passed. Initial live port: four passed,
  two nested-upload setup failures, one terminal-output failure.
- Corrected old/new terminal controls: both two passed. Forced TTY=false
  fails only the terminal Then; forced TTY=true fails only the pipe Then,
  in both versions. All Given/When steps pass in those four mutation runs.
- The nested failures reveal a masked production defect: StreamIn executes
  tar with a target directory that does not yet exist. localExecutor used
  to create it implicitly. An isolated Go overlay changes the production
  command to POSIX sh with a quoted positional path argument, mkdir -p,
  then exec tar. All seven live focused cases pass (213.696s).
- That candidate has NOT been applied to production. The actual volume.go
  and process.go match their pre-migration snapshots. The two nested cases
  therefore remain known failures in the current working tree; approval
  for the production fix is outstanding.
- Build/vet pass; all three native suites pass in 38.625s.
- The v5 catalog remains at 1056 definitions. The validator accepts only
  the intended Workspace removal, mounted-volume resource removal,
  identity resource addition, and terminal action rename/input reduction.
- validate.js verifies exact control/mutation rosters, intended failing
  steps, complete recorder drains, paired namespace UID cleanup receipts,
  binary catalog closures, unchanged production, and native results.
  evidence.json SHA256:
  f9c13e35c95f019a9dd3e37e7633340f1fe2bb7b743c6556b150b45540fbc85f.
- Independent checks find no owned live namespaces or recent adapter,
  API-server, etcd or collector processes. Only owned test resources were
  removed; evidence remains in the directory above.

No Go tests were retired. In particular, the retained nil-stdin/raw-TTY
argument tests are not replaced by this non-nil-stdin behavioral probe.
No full coverage run is claimed for this known-red working tree. The last
green result remains the preceding cache-get increment, not this increment.
The broader goal remains active, including remaining resource-process and
host-executor substitutes and F23's separate, unanswered hostPath permission.
Changes remain uncommitted on core; no push, image publication or pipeline
application occurred.

## Real Kubernetes interception (2026-09-12)

The four existing interception cases now live together in
features/live/interception.feature. They use production LookupContainer,
Container.Run, the supervisor and SPDY against real BusyBox 1.37.0 pods.
worker.go no longer installs localExecutor. Both interception steps dropped
task-workspace; setup reuses the live database preamble and namespace policy.
There are no additional scenarios or definitions (catalog remains 1056).

The fixture persists the original container metadata, creates existing pods
with bounded emptyDir scratch storage, and verifies each actual hostname,
container identity and matching mount/volume. This tests interception of
existing pods, not creation of an original task pod by the worker. A real
PID 1 finishes on request; the fixture waits for kubelet-reported termination
and exit zero before supplying the saved completion annotation. It never
writes pod status. The completion case tests preservation of that annotation,
not the production annotation writer. Successful interceptions inspect the
supervisor's actual files in the expected pod, with a nonempty-match guard.

Evidence: /tmp/brine-live-intercept.znsUeE.

- Old/new controls: the same four cases pass; live control takes 60.927s.
- Four paired faults each leave three cases passing and fail exactly one
  intended assertion: missing-pod refusal, completed-pod refusal, exit-code
  propagation, and stdout delivery. All Given/When steps pass.
- A separate, narrow pod-name fault routes LookupContainer to the raw handle
  while preserving its metadata/type. All four cases fail in both versions.
  Three fail the same Then; the decoy case fails the old refusal Then but
  fails the new When's expected-pod supervisor-state guard. That is not
  claimed as Then-for-Then equivalence or as justification to retire a Go test.
- The earlier routing probe removed metadata AND container type, confounding
  routing with supervisor selection. Its logs are retained but excluded from
  the accepted mutation matrix; pod-name-fault.go is the accepted narrow fault.
- Build/vet pass. Three native suites pass in 41.071s.
- Entire remaining local tier: 559 passed, zero failed (222.361s).
  This is explicitly local-only validation, not the full coverage gate.
- validate.js checks exact rosters/failing steps, complete recorder drains,
  real-pod/termination/supervisor receipts, namespace UID cleanup, binary
  catalog closures, only the intended two catalog changes, and unchanged
  production worker.go/container.go/process.go. evidence.json SHA256:
  ceccf6ae1ad746ead1f617324120d50c17cf25492640714e244bf8c85e84e3c6.
- Independent namespace inspection finds no owned live namespaces.
  No Go tests were retired. No production changes were made this increment.

The previous increment's two nested-upload failures remain: the StreamIn
candidate is still unapplied pending approval. The full coverage gate has not
been rerun or claimed green. The broader migration remains active, with
resource process/worker substitutes, remaining host-execution families and
F23's separate storage permission still outstanding. Changes remain
uncommitted on core; nothing was pushed, published or deployed.

## Real supervised tasks and shared live runtime helpers (2026-09-12)

All eight existing supervised-task cases now live in
features/live/task-command.feature, including failed-task pod retention moved
from container-run.feature. The former local task-command.feature was removed,
not duplicated. Case count and the 1056-definition catalog are unchanged.

Setup is one Given rather than a generic worker plus a host-execution modifier.
TaskCluster no longer carries TaskWorkspace. Commands execute through the
production worker, pause pod, SPDY transport and supervisor in BusyBox 1.37.0.
The pod's /tmp filesystem replaces the host workspace; stable process IDs need
no host-path prefix. The kubelet supplies actual startup and container identity.
The fixture no longer sleeps and writes Running status to manufacture startup
time. It checks pod UIDs before/after execution and across recovery.

Tasks and interception now share newLiveRuntimeWorker and
requirePodSupervisorState in live_kubernetes.go. Namespace security, quotas,
limits and UID-safe disposal remain shared. The supervisor-state guard reads
actual pid/log/exit files inside the expected pod and rejects an empty match.
Legacy host workspace/status helpers still serve other, unmigrated families;
they are not claimed removed globally.

Recovery uses fresh runtime container objects backed by the same DB and pod,
not a killed/restarted ATC process. The missing-record case deliberately clears
the completed pod's annotations to exercise recovery without a completion
record. It is not evidence of a real web crash at that exact timing boundary.

Evidence: /tmp/brine-live-tasks.iuynHV.

- Eight-case old control passes. The initial live control failed because the
  new UID guard treated the empty object returned with NotFound as an existing
  pod. Checking the lookup error before comparing UIDs fixes the fixture.
  Corrected live control: eight passed (142.922s).
- Five paired mutations run focused copies of the affected existing cases:
  forcing re-execution breaks the one-run assertion; dropping the command hash
  breaks the different-command assertion; suppressing ExecExitError status
  breaks both nonzero-exit cases; corrupting the saved annotation breaks the
  recovered exit assertion while the direct exit assertion still passes;
  bypassing supervision breaks the state-ownership assertion.
  All Given/When steps pass in the accepted mutation runs. The state assertion
  is renamed from host workspace ownership to task-pod ownership.
- All eleven live task executions report real pod/container readiness; the
  three survivor executions and two changed-command executions retain their
  respective pod UIDs. No production process/supervisor/container source changed.
- Shared interception regression: four passed (56.597s).
- Remaining local tier: 551 passed, zero failed (189.741s).
- Build/vet pass; three native suites pass in 39.855s.
- validate.js checks exact rosters and failure points, direct-versus-recovered
  exit discrimination, stable recovery UIDs, complete recorder drains, namespace
  UID cleanup receipts, binary catalog closures, only the intended setup/state
  contract changes, and unchanged production. evidence.json SHA256:
  be55698653c1a6a0315c608dc26fffec453ea8fe1a82da4786d47516f87ceac5.
- Independent checks find no owned live namespaces or recent adapter,
  API-server, etcd or collector processes. No Go tests were retired.

No full coverage pass is claimed. The two nested-upload failures remain until
the separately requested StreamIn fix is approved. Other resource-process,
host-execution and reported-status fixtures remain; F23 still needs its
separate storage permission. The broader goal is active. Changes remain
uncommitted on core; nothing was pushed, published or deployed.

## Real cancellation and the approved resource fix (2026-09-12)

The four cancellation cases moved from step-closing.feature into
live/cancellation.feature. WorkerReady replaces the host TaskWorkspace; the
action drives actual Run/Wait/SPDY. Running task termination is observed from
the kubelet while an owned finalizer holds the API object, then that finalizer
is removed and pod disappearance is awaited. A disposer also removes it on
failure. Resource termination is read from the retained pod's /proc. Neither
helper reports pod status or signals host processes.

Baseline receipt: /tmp/brine-live-cancel.Q4nUKg/evidence.json, SHA-256
e0e46ef37c2d43d894eddce7930ff3169b97f111959e6a2e8aed9c9f8bd75926.
The old four cases passed in 6.066s. Both real controls had three passes and
one failure (56.306s, 57.125s): the running get's child remained alive in state
S after cancellation. All Given/When steps passed. Production was unchanged.
The move retained 1,056 definitions and added/retired no cases or Go tests.
All 547 local cases and three native Brine suites passed (189.913s, 38.415s).

Paired baseline mutations preserved resource retention and cancellation-error
assertions. Without task deletion, both old task rows failed pod-removal
checks; the new running-task row failed physical-stop verification because
the kubelet still reported Running without a deletion timestamp. This is
stronger observation, not Then-for-Then equivalence. No retirement is inferred.

Following user approval, resource_process.go and execProcess.Wait now stop an
invocation-scoped group on cancellation while retaining the pod. Cleanup uses
an independent five-second context; its errors are joined with cancellation.
A random state path, atomic PID/birth record and retained cancel marker cover
cleanup arriving before process startup. Only get/put/check containers are
wrapped; looked-up hijack containers are excluded. Normal exit removes the
state directory. The wrapper preserves the umask, positional arguments, stdin,
separate stdout/stderr and exit status.

Resource images need setsid, sh, mkdir, mv, rm and rmdir. BusyBox setsid lacks a
portable wait flag: a non-job-controlled background child is waited on
explicitly, preserving stdin through a descriptor. Actual BusyBox 1.37.0 pods
validate this. Deliberately detached sessions escape a process group; this is
not per-command cgroup containment. No hostPath, pod security, node or RBAC
policy changed.

The same cancellation outline adds running put/check and verifies a
TERM-resistant child stops while an independent process in the same pod
survives. A three-row protocol outline checks get/put/check, exact stdin/stdout,
separate stderr, literal shell-looking arguments and exits 0/17/42. The five
added cases and two definitions give 547 local + 31 live = 578 cases and 1,058
definitions. One real-shell Ginkgo spec covers cancellation before the PID
record. No Go tests were retired.

Current evidence: /tmp/brine-resource-cancel.ujcGd8. Setup mistakes are retained:
first.log and cancellation.log lacked the collector; native.log lacked Brine
on PATH. These failed before meaningful execution and are not behavioral
evidence. An initial BusyBox cleanup used a nonportable kill separator: the
child stopped but cleanup returned an error. After correction to kill -s KILL
with the negative group ID, all nine expanded controls passed in 111.391s
without cleanup errors.

The first local rerun passed 542/547: the host executor only rewrote the outer
executable and could not find resource scripts inside the wrapper. That
fixture now supplies explicit host script paths; implicit image/root
remapping was removed from localExecutor. No wrapper-shaped special case was
added. This does NOT claim that the remaining integration fixture uses a
kubelet. The corrected local suite passes all 547 cases in 193.760s.

Brine vet passes and all three native suites pass in 40.844s. The full
production package's native tests and 98 Ginkgo specs pass (Ginkgo 28.849s).
The new startup spec passes; removing the cancel-marker guard fails that spec.
An initial production-binary run used the Brine working directory and its
empty-scan guard correctly refused it. Accepted runs use the production
package directory with an isolated network namespace.

Final receipt: /tmp/brine-resource-cancel.ujcGd8/evidence.json, SHA-256
8ef10dfd9d40c19d23a5d6f704da1c5f67e44a8080b4efbc6f3d37b8e8fd8323.
The validator checks exact failure locations, complete recorder drains,
namespace/finalizer UID pairs, native results, catalog closure, source hashes
and restoration of the normal adapter.

| Isolated fault | Required detection |
| --- | --- |
| Omit resource cleanup | All three resource kinds fail physical-stop assertion |
| Kill parent, not group | Surviving child fails physical-stop assertion |
| Drop the stdin descriptor | All three exact-stream assertions fail |
| Replace exit status with zero | The 17/42 rows fail; genuine zero still passes |
| Remove startup cancel guard | The real-shell pre-launch cancellation spec fails |

All mutation Given/When steps pass. The initial leader-only check row reached
its observation deadline instead of reporting the last child state. It is
not counted as direct surviving-child proof: a separate check-only repeat
confirms state S at the intended Then (28.816s). Both runs are retained, and
the evidence validator requires the successful repeat of the mutation.

The final full live tier passes 29/31 cases in 560.225s. Its only failures are
the two pre-existing nested-directory uploads, both at StreamIn. Together
with the final local run this is 576 passing and two failing cases, not a
green full-suite coverage gate. No coverage percentage was remeasured.
The real cancellation/protocol, task, interception, terminal and interruption
regressions all pass. The current catalog has 1,058 definitions.

Normal adapter restored and hash-matched to final-control:
41fa05822220f0e1e9df9973af1f919bfb5e48dd5a75f439c5fdd5422799b734.
Independent cleanup checks find no owned live namespaces, recent test
processes or command-state directories. Six verified-empty directories from
the early pre-cleanup build were removed with explicit-path rmdir.

The separate StreamIn directory fix remains unapplied and approval was
requested again. Other execution substitutes and the F23 storage permission
remain open; the broader goal is active. Changes are uncommitted on core.
No push, image publication or pipeline application occurred.

## Actual Git resource consolidation — 2026-09-12

Six host echo-resource scenarios in step-integration.feature are replaced by
four rows in live/git-resource.feature: actual get, put, check and invalid
source rejection. They reuse the live worker setup and add only three shared
definitions. Seven obsolete definitions are removed, together with the unused
StepRan.ProcessID field. One row added to the existing container-spec image
outline preserves default-image fallback coverage. Net: one fewer scenario
and four fewer definitions; 542 local + 35 live = 577 cases, 1,054 definitions.
No production source changes or Go-test retirements occurred in this increment.

The actual upstream image is pinned to:
concourse/git-resource@sha256:6ae5106a362ec97b719276d4b0560526c385afac87444b24736ec5b3660d0175.
Docker registry v2 resolved this index on 2026-09-12; its linux/amd64 manifest
is sha256:b668284d1115cd4943b9c0b8a55931c05adddfd38a50ea0ed0be457f735b0cc9.
The image receipt is in /tmp/brine-real-git.rh8adQ/image.json.
Upstream assets were inspected at
https://github.com/concourse/git-resource/tree/master/assets; the pinned
image's actual scripts, not downloaded replacements, execute in these tests.

Each case owns a real Kubernetes namespace and pause pod, creates a real Git
repository in that pod, and calls the production resource process once through
SPDY. Get verifies both response ref and checked-out bytes. Put consumes a
real mounted checkout and verifies the pushed commit and bytes independently
in the bare remote. Check verifies its version list. Invalid get verifies
the real upstream error, empty stdout and exit 1. The image also exercises the
new cancellation wrapper's normal path; cancellation itself retains its
separate nine-case regression family.

Put populates the actual input mount through production Volume.StreamOut and
StreamIn. This is direct streaming within the owned pod, not DaemonSetBackend
publication, automatic init fetching, or proof of F23. No hostPath, RBAC,
node policy, deployment or external Git repository is changed.

The final contract check preserves the original creation-time DB assertion:
it captures checkContainerRow immediately after FindOrCreateContainer, before
Run, and retains the private error through the transformation. It additionally
checks the final row, unchanged pod UID, actual image, pause command, main
container, process ID and volume/mount cross-consistency. Empty mount scans
fail. Put also checks the mount inside the running container.

Default image protection needed its own correction: the existing dictionary
check alone does not exercise Container.resolveImage's nil-map fallback.
The base-git row in the existing image-resolution outline does. This uses
local pod construction, not an unpinned live image. The configured pinned
ResourceType path and the default ImageURL path therefore both remain covered.

Evidence directory: /tmp/brine-real-git.rh8adQ.
The original six-case control passes in 5.605s. Final creation-control.log
passes all four real Git cases in 59.723s. local-final.log passes all 542 local
cases in 188.827s. cancellation-regression.log passes all nine existing
cancellation/protocol cases in 113.208s. Final Brine vet passes; all three
native suites pass in 40.343s, including vocabulary closure, unique matching,
execution-document handling and real recorder/collector cleanup.

Every mutation runs against the exact corresponding control roster. The
creation-* variants use the final creation-time-checkpoint source. All setup
and When steps pass; failures occur at the stated Then/And assertions.

| Deliberate production fault (overlay only) | Original six cases | Final replacement |
| --- | --- | --- |
| Force resource exit to zero | 1 failure | Invalid get rejects false success; 1 failure |
| Drop stdin | 3 failures | Actual get/put/check reject absent source; 3 failures |
| Target missing-main | 4 failures | Real SPDY target errors; 4 failures |
| Wrong default process ID | 1 failure | Check contract fails; 1 failure |
| Persist task metadata instead | Creation-row assertion fails | All 4 final contract assertions report the captured pre-Run metadata error |
| Change default Git image | Image assertion fails | Dictionary plus base-git pod-image assertions fail |
| Remove nil-map image fallback | Image assertion fails | Only base-git pod-image assertion fails; dictionary still passes |

default-control.log passes its six focused image cases in 6.326s. These last
two new image mutants predate only the unrelated live creationErr addition;
their feature roster and construction code are unchanged. The fallback fault
is important evidence that the construction row adds protection the dictionary
case cannot supply. No assertion equivalence is inferred for unrelated behavior.

Earlier exploratory runs are retained under new.log, consolidated.log and
final.log, but final live claims above use creation-control.log and its paired
variants. vocabulary-first.log used invalid Ginkgo flag placement;
vocabulary.log then correctly found two obsolete definitions, now removed.
Neither failed setup is counted as behavioral validation.

Receipt: /tmp/brine-real-git.rh8adQ/evidence.json, SHA-256
478cc9a9978df1bd108d5af25351d15f08e21cc37e53afc8609a3ec6177b3c3e.
validate.cjs checks exact control/mutant case identity, failure locations,
complete recorder drains, namespace UID create/delete pairs, real Git
receipts, native results, the 1,058-to-1,054 catalog delta, unchanged prior
production cancellation sources, and normal-adapter restoration.
Normal adapter SHA-256:
f2a7099652e354979d97facefea4253a239d9af49a46cda26832b8d3ca4eea13.
Independent checks find no owned live namespaces or recent active test
processes; git diff --check passes.

The most recent full live run is still the preceding increment's 29/31:
two nested StreamIn uploads fail. No current full green coverage result is
claimed. The production directory fix and F23 hostPath permission remain
pending; neither is inferred from goal continuation. Remaining host pipeline
helpers, uncached exec-step substitutes and reported-status fixtures still
require migration. The goal remains active. Work is uncommitted on core;
nothing was pushed, published or deployed in this increment.

## Real uncached get steps — 2026-09-12

The pinned-version and refused-version scenarios now live in
features/live/get-step.feature. Unlike the preceding worker-level Git tests,
these call the actual exec.GetStep with a production worker Pool and
DefaultFactory, real PostgreSQL/engine delegates, the upstream resource image,
SPDY, cache initialization and the downstream artifact streamer. The new Given
constructs no version catalogue, runtime fake worker, container, process or
volume. Other get/put, timeout, attempt and hook fixtures still need migration.

A Git init container creates two distinct commits and a bare repository.
BusyBox serves its files read-only from a separate pod. This is Git's real
HTTP transport, with auxiliary files generated by git update-server-info:
https://git-scm.com/docs/git-update-server-info.
The fixture reads the real refs and HTTP index before running the get.
It uses the existing pinned Git resource image and BusyBox 1.37.0, within the
existing two-pod namespace quota. No services, hostPath mounts, RBAC grants or
node changes are required. The server alone has a one-second termination grace;
the production resource pod's lifecycle is unchanged.

The successful get explicitly pins the FIRST commit, while main points at the
second. Assertions independently read the finish event, build-owned cache row,
fresh-versus-cached flag and payload.txt through the production artifact
streamer. The actual pod's persisted exit status must agree exactly with the
build event. A missing forty-zero ref produces Git exit 128, a failed rather
than errored get, a diagnostic naming that ref, and no registered artifact.
This preserves exact status forwarding without retaining the old mock's
arbitrary exit 1. Direct exec-backed artifact reading is not daemon publication
or proof of independence from the producer pod; F23 remains separate.

Two scenarios moved; none were added. Three shared live definitions replace
three now-unused assertions. The cached-only provenance helper drops its dead
fresh-artifact branch. Inventory from Brine's own parser: 540 local + 37 live =
577 cases. Catalog: 1,054 definitions, unchanged. No Go tests were retired.

Evidence directory: /tmp/brine-live-get.iwjng9.
Original two-case control: 2/2 in 5.622s.
Final closed-vocabulary get control: 2/2 in 40.338s.
Shared Git-resource regression: 4/4 in 60.310s.
Complete local tier: 540/540 in 188.412s.
Brine vet passes; all three native suites pass in 40.501s.

| Production fault, applied only through build overlays | Original cases | Real replacement |
| --- | --- | --- |
| Ignore the planned version | Pinned get fails its success assertion | Real Git fetches main; wrong finish ref fails, and the formerly missing get incorrectly succeeds (2 failures) |
| Store a different cache ref | Cache membership fails | Build cache must contain only the actual pinned commit |
| Mark the fresh artifact cached | Provenance assertion fails | Shared outcome detects false cached provenance |
| Force finish event exit to zero | Expected exit 1 assertion fails | Event zero disagrees with actual pod exit 128 |
| Register artifacts even after failure | No-artifact assertion fails | Failed Git get must expose no downstream artifact |

All mutation setup/When steps pass, and exact assertion locations are checked.
The original-case mutant binaries retain the three legacy assertions; the
replacement mutants precede only their removal and the cached-only helper
simplification. live_get.go is unchanged between the accepted final-control
and closed-control stages. Final native/local runs validate the simplification.

Exploratory control.log and diagnostic.log are not accepted behavioral
evidence: the upstream resource image does not include git-daemon. Its actual
startup logs established that cause, and the fixture changed to real Git HTTP.
http.log then passed before setup consolidation and bounded server shutdown.
native-first.log correctly identified the three unused definitions now removed.
No failed setup run is treated as a successful mutation detection.

Receipt: /tmp/brine-live-get.iwjng9/evidence.json, SHA-256
d5e7324794ab8bfcf476593d579b4bd06951b9a0fc018fa323059a2d5826e0ab.
validate.cjs verifies exact old/new rosters and failure sites, complete recorder
drains, namespace UID cleanup pairs, actual Git server/commit receipts,
exit/artifact observations, native results, the closed catalog, parsed counts,
unchanged production get/version sources and restored normal adapter.
Normal adapter SHA-256:
c274bc734cfc67c20ec39d319794249c19930af56a5790d96958c3512b59183d.
Independent checks found no owned test namespaces or recent active test
processes. git diff --check passes.

No fresh full-suite coverage result is claimed. The two nested StreamIn
failures, pending production directory fix, pending F23 permission and other
runtime substitutes remain. This increment changes tests and documentation
only. Work is uncommitted on core; nothing was pushed or deployed. The goal
remains active.

## Real get timeout and dead-fixture removal — 2026-09-12

The remaining standalone get-timeout case moved into live/get-step.feature.
It reuses the real Git build/pool fixture. The owned HTTP server now runs as a
child of its container's shell so the test can pause that specific process,
rather than trying to signal namespace PID 1. A real /proc observation must
show the server stopped (T). No Kubernetes status, resource reply, process
result or deadline error is injected.

The get uses an actual 15-second plan timeout. While it runs, the test must
observe git-remote-http with the owned repository URL in the resource pod,
with a live process state and actual pod UID. A timeout during pod startup
alone is not accepted. The real child was observed sleeping (S) and was gone
after the production timeout cleanup. The owned Git server is resumed and
its real HTTP refs are read again before the action returns. The resource pod
must retain its UID, and no artifact may be handed downstream.

The original failed-versus-errored, timeout diagnostic and no-finish assertions
remain unchanged. The shared Git outcome assertion adds the physical execution,
child-stop and pod-retention checks. The existing cancellation observation
helpers are reused unchanged.

Removed scriptStalls, the stall/getTimeout fields, three unused setup
definitions, and the fake uncached branch of the ordinary pinned-get action.
That action now serves the existing real cached-get case only. The separate
put-to-get workflow, puts, attempts and hooks still contain runtime doubles;
the removal does not claim those have migrated.

One case moved, none added: 539 local + 38 live = 577. One new shared definition
replaces three obsolete ones, reducing the catalog from 1,054 to 1,052.
Brine's own parser supplies the inventory. No Go tests were retired and no
production files were changed.

Evidence directory: /tmp/brine-get-timeout.A8gPCL.
Original one-case control passes in 4.851s.
The initial three-case live get control passes in 66.989s.
After legacy fixture removal, all three live get controls pass in 68.318s,
all 539 local cases pass in 186.946s, vet passes, and the three native suites
pass in 39.433s. The normal pinned and missing gets remain regression controls
for the shared server change.

| Isolated get-step fault | Same old/new failure site |
| --- | --- |
| Omit timeout error event | Build log must record “timeout exceeded” |
| Emit a finish event on timeout | Build must never report the get finishing |
| Return the deadline as an infrastructure error | Step must fail rather than error |

Each old mutant fails its single case; each new mutant fails only the timeout
case while both ordinary Git cases pass. Setup and When steps pass in every
accepted mutation run. The real server pause, running HTTP child and stopped
child are witnessed for every live timeout, including the mutants. These
mutant binaries predate only legacy-code removal; live_get.go is unchanged
between the accepted control and closed-control source.

Receipt: /tmp/brine-get-timeout.A8gPCL/evidence.json, SHA-256
d11ecaf6dbd5c2a50c5984fe4bf6955095b09d0765c94ad417e35ab7f578b7ab.
validate.cjs checks exact fault sites/rosters, positive process observations,
complete recorder drains, namespace UID cleanup pairs, native results,
definition removal, parsed counts, unchanged production sources and normal
adapter restoration. Normal adapter SHA-256:
33760e6173f18d66a8b269593a66318aae53dbe6f0f00c2faddf142d60ecf90e.
Independent checks find no owned namespaces or recent active test processes;
git diff --check passes.

The two nested-upload failures and their pending production fix, F23 storage
permission and other runtime substitutes remain open. No fresh full green
coverage measurement is claimed. Work remains uncommitted on core; nothing
was pushed, deployed or published. The goal remains active.

## Real time-resource publication and successful retry — 2026-09-12

Two existing scenarios moved to live/time-resource.feature with their names
preserved: publication followed by a dynamic get, and stopping retry at the
first successful attempt. Both use the real database, worker pool, factory,
engine delegates and Kubernetes SPDY execution. Three shared definitions
replace three obsolete definitions: 1,052 definitions remain, and Brine's
parser counts 537 local + 40 live = 577 cases. No Go tests were retired.

The upstream time resource generates the actual timestamp version and writes
its actual input and epoch files. The immutable image is:
concourse/time-resource@sha256:5ee278f0e12aada4734b1a56a4ac6f04980236841d9e0b7e0d619e80d4167dc1.
The put/get case compares the real put finish, published database version,
dynamic get finish, cache row and streamed artifact contents. No version reply,
resource script, process result or Kubernetes status is fabricated.

Retry supplies a real invalid interval to attempt 1, which exits 1 with the
upstream parse-error diagnostic. Attempt 2 succeeds and publishes its actual
timestamp. Attempt 3 is valid and armed, but must not execute in the control.
This family creates a three-pod namespace quota so a faulty extra attempt can
actually run. Existing callers retain two pods. Aggregate CPU, memory and
ephemeral-storage caps, baseline security policy and RBAC are unchanged.
The initial control used quota Get/Update; accepted final controls and mutants
use the creation-time budget and require no additional quota API permissions.

The fake get process, in-memory version catalogue and three unused definitions
are removed. The remaining mocked put helper is explicitly named
scriptCreatesVersion; it is not presented as a real resource. Real worker-DB
construction, put-step construction and full-version cache observations are
shared instead of copied. Partial-output put, aborted retry and hook fixtures
still use runtime doubles. A real parse-error put would not faithfully replace
the partial-output case's version-then-failure contract.

Evidence directory: /tmp/brine-time-resource.doQX5V.
Original two-case control: 2/2 in 6.285s.
Initial real control: 2/2 in 37.108s.
Final fixture control: 2/2 in 34.779s.
After legacy removal: 2/2 in 36.176s; the three live Git get regressions pass
in 67.885s; all 537 local cases pass in 186.136s. Vet passes and all three
native suites pass in 38.797s. Control logs include actual pod UIDs and exit
annotations, and both resource artifacts and actual publications are observed.

| Isolated production fault | Old/new detection |
| --- | --- |
| Discard dynamic version | Old fake get fails; real get returns a different timestamp and fails the version assertion |
| Skip publication | Both workflows reject the missing database output |
| Omit stored put result | Both old/new put-get cases reject the missing previous-step version |
| Continue retry after success | Both reject attempt 3; the real test observes two distinct published timestamps |

Each dynamic/result/retry mutant fails only its intended case; the publication
mutant fails both. All setup and action steps pass. Production faults are Go
build overlays only: version_source.go, put_step.go and retry_step.go match
their before-run snapshots. The earlier approved resource cancellation fix is
unchanged. Mutation binaries predate only legacy catalogue/definition removal;
the accepted real fixture bodies are those used by the closed control.

Receipt: /tmp/brine-time-resource.doQX5V/evidence.json, SHA-256
52fad09390499175bce729d52ca4851a16999fdd17419ecc855fbff7bd5877cd.
validate.cjs checks exact case rosters and failure sites, the actual third
publication, complete recorder drains, namespace UID cleanup pairs, positive
control pod receipts, native results, parsed counts, vocabulary replacement,
removed fake symbols, unchanged production sources and normal adapter restore.
Normal adapter SHA-256:
0eba0c99da11438302ed2a34b5227000009ef59eb27f8bdb203e47eb85bcc1a4.
An independent namespace check finds no owned namespaces; git diff --check passes.

This is input-free resource publication, not artifact-daemon publication or
automatic input fetching. F23's hostPath exception is still pending, as is the
separate production fix for the two nested-directory upload failures. No fresh
full green coverage measurement is claimed. Work is uncommitted on core;
nothing was pushed, deployed or published. The goal remains active.

## Aborted retry with a real in-flight resource — 2026-09-12

The existing “An aborted build does not spend its remaining attempts” scenario
now shares live/time-resource.feature with publication and successful retry.
Its first attempt is a real pinned Git get against the owned HTTP server.
The server is paused, and the actual git-remote-http child, pod UID and live
process state must be observed before cancelling the retry's run context.
The production cancellation path stops that child while retaining the pod.
The independent observation context stays alive for cleanup, and the real
server is resumed and probed afterward.

Attempt 2 is an ordinary input-free put using the pinned upstream time resource
and a real pipeline output resource. No fake worker, process, protocol reply,
result or Kubernetes status is involved. The fixture allows three pods under
the existing aggregate caps, leaving room for the armed remaining attempt.
The same real pool and engine delegates are used for both attempts.

A fidelity trap matters here: the original mocked second attempt could publish
despite cancellation; a real worker correctly refuses a cancelled context.
Therefore no publication alone would not detect an incorrectly entered retry.
The new assertion reads durable initialize-get and initialize-put events: the
first must identify attempt-1, and there must be no put initialization. It also
requires no output, downstream artifact or finish event, the stopped child and
the original resource pod's UID/image. This preserves “do not spend remaining
attempts” without making the real worker ignore cancellation to mimic the fake.

Paired production fault: remove RetryStep's context cancellation guard.
The original control passes in 4.811s and its mutant fails in 5.429s at the
cancelled-outcome assertion because the fake second attempt succeeds.
The initial real control passes in 22.930s; its mutant fails in 22.701s at
“the aborted retry never enters its remaining attempt”, observing the actual
initialize-put origin attempt-2. The genuine worker error still wraps context
cancellation. On the final source, the same fault is detected at the same
initialization assertion in 24.101s. Setup and action steps pass in all accepted
mutant runs. No quota/startup failure is used as the test's failure site.

The mock attempt list/type, two setup definitions and old retry action are
removed. Three new definitions replace those three: catalog size remains
1,052. One existing case moved; Brine's parser reports 536 local + 41 live =
577. No one-case feature file was retained and no Go tests were retired.
Shared HTTP-child observation and server resume/probe helpers now serve both
deadline and explicit-abort cases; ordinary gets retain their two-pod budget.

Final verification:
- Three consolidated publication/retry cases: 3/3 in 52.819s.
- Three Git get regressions, including the actual 15-second timeout: 3/3 in 70.256s.
- Entire local tier: 536/536 in 190.695s.
- Vet passes; three native suites pass in 38.775s.
- All recorder drains complete; namespace UID cleanup pairs match.
- Independent checks find no owned namespaces or recent active test processes.
- Normal adapter restored; git diff --check passes.

Evidence: /tmp/brine-abort-retry.1wtS22/evidence.json, SHA-256
88880ab33e146db9d8f59e97b42732cdbb689308e9c4687075ba965694b24087.
validate.cjs verifies exact rosters/failure sites, positive process observation,
child cleanup, final catalog closure, counts, removed vocabulary, source hashes
and normal binary restoration. Its first exact-string cancellation check was
corrected to allow the genuine wrapped worker error; the runtime assertion
itself uses errors.Is(context.Canceled) and was not weakened.
Normal adapter SHA-256:
c90a756ac7ea5eb1f24138264ce30c295ff2fa2128b13747504401e28efeeb83.

No production files were changed. Partial-output put and hook mocks, other
host/status fixtures, the pending nested-upload production fix and F23 storage
permission remain open. No fresh full green coverage result is claimed.
Work remains uncommitted on core; nothing was pushed, deployed or published.
The goal remains active.

## on_abort with four real resource outcomes — 2026-09-12

All four existing outline rows now share live/time-resource.feature with
publication and retry. The Given is generalized to “a build using actual Git
and time resources” and reused by aborted retry. The guarded step is a real
Git get; the hook is a real input-free time-resource put, using the existing
immutable images, real worker pool, factory, database and engine delegates.

The four outcomes remain distinct:
- Abort: pause the owned Git HTTP server, observe the running HTTP child,
  cancel the guarded run, and require a generated hook timestamp in the real
  build output and successful finish event.
- Infrastructure error: after the same positive process observation, close an
  owned transparent TCP relay carrying only guarded exec traffic. A direct
  probe proves the child survives the socket cut. The SUT returns the actual
  missing-remote-exit-status/EOF error, not cancellation or an injected error.
- Resource failure: request a nonexistent Git commit; the resource exits
  nonzero, without an infrastructure error or downstream artifact.
- Success: fetch the pinned real commit and read its actual artifact bytes.

The hook retains a healthy direct execution route even when guarded exec is
cut. This is essential: a wrongly selected hook must be able to publish,
rather than being hidden by the same broken transport. The server resumes
and its protocol is probed; resource children stop before cleanup. Assertions
also verify real initialization/finish events, original pod identity, image
digests and the time-resource hook's successful exit annotation.

The real Git/time build setup and output-put constructor are shared with
aborted retry. The transparent relay and Kubernetes configuration loader are
extracted from existing live setup; the pod-deletion and real OOM-diagnostic
cases retain that same relay implementation and were rerun.

Four paired production mutations preserve the old outline's discriminators:

| Fault | Old and new rows that fail |
| --- | --- |
| Never select the hook | Abort |
| Select on any error | Infrastructure error |
| Select on any unsuccessful result | Infrastructure error and resource failure |
| Always select the hook | Error, failure and success |

All accepted setups and actions pass. Every new mutant fails at “the real
hook runs only for cancellation”: the never fault has no output; the other
faults expose actual unwanted timestamp publications. These are not transport,
admission or startup failures. The production changes are Go build overlays
only; on_abort.go remains byte-identical to its snapshot.

Removed the hook fate fields, six hook-only definitions and the four
scriptCreatesVersion/scriptRefuses/scriptUnreachable/scriptAbortsTheBuild
helpers. Two newly orphaned string-version publication assertions and their
ref-only reader are also removed. The partial-output mock is retained
explicitly; its remaining action rejects any attempt to use it as a generic
successful put. Its original regression remains in the local tier.

Two new hook definitions replace eight obsolete definitions, and one existing
Given is renamed/generalized. Catalog diff: nine removed patterns, three added;
1,052 -> 1,046 definitions. All four rows moved, none added or dropped:
532 local + 45 live = 577 cases. No separate one-case feature was introduced
and no Go tests were retired.

Validation:
- Original four-case control: 5.289s.
- Initial real four-case control: 74.850s.
- Final fixture four-case control: 77.398s.
- After cleanup, all seven consolidated cases pass in 122.216s.
- All three Git get regressions pass in 68.650s.
- Both real transport-interruption regressions pass in 25.386s.
- All 532 local cases pass in 188.390s.
- Vet passes; all three native suites pass in 41.590s.

Mutation run times (old/new): never 5.035s/73.558s; errors 6.281s/77.545s; unsuccessful 7.067s/83.598s; always 6.153s/81.204s.

Receipt: /tmp/brine-live-hooks.bJAre9/evidence.json, SHA-256
59adec27e5a02dc66faa5da2c56b5aa4ec3d63b468840a3f8d04938b52ce565c.
validate.cjs checks exact case rosters and fault sites, real unwanted
publications, positive process/cut observations, child-stop and namespace UID
pairs, complete recorder drains, native results, vocabulary/source cleanup,
parsed inventory, unchanged production sources and normal adapter restoration.
Accepted mutants use the final real fixture bodies before legacy cleanup;
the closed control uses the final complete source. One relay-handle compile
error and one patch-anchor rejection were corrected before accepted runs;
neither is counted as behavioral evidence.

Normal adapter SHA-256:
cfbd0589320b0dba012a84569ca9bbf8da70c82fd4709672defe0c9e7a4f4b52.
Independent checks find no owned namespaces or recent active test processes,
and git diff --check passes. No production code changed in this increment.

Partial-output put, host executor/status fixtures and injected resolver panic
remain. The two known nested-upload failures and F23 storage permission are
still open. Explicit approval for the nested-upload production fix was requested
again during this increment; it has not been applied. No fresh full green
coverage result is claimed. Changes remain uncommitted on core; nothing was
pushed, deployed or published. The goal remains active.

## Six startup trace cases, one real pod — 2026-09-12

Six local successful-startup scenarios are consolidated into one scenario in
live/task-command.feature, alongside the eight existing supervised-task cases.
The real runtime reuses a pre-created BusyBox pod with a running init-process
file gate and a normal sidecar. The fixture waits for actual Pending,
Scheduled=True, a node and a running init container, then probes the gate by
SPDY. It releases the init process only after the production watch's successful
handshake. No Kubernetes status, node, watch response or process result is
fabricated. The existing transport forwards requests and responses unchanged.

The first production Wait consumes Pending -> Running and executes the task
through the in-pod supervisor. Independent reads require the same UID, changed
resourceVersion, unchanged actual node, successful init termination, a running
sidecar and the task's actual stdout. A second Wait on the same process observes
an already-running first snapshot and replays the completed task output.
Both real exported OTLP wait spans must contain each lifecycle fact exactly
once and the actual scheduling node. This second observation preserves the
old first-snapshot premise; a watched transition alone would not replace it.

| Retired local scenario | Preserved assertion in the consolidated case |
| --- | --- |
| Pod initialization is recorded on the wait span | pod.initialized, including the already-running first snapshot |
| The trace records which node the step landed on | pod.scheduled names the node read from the actual pod |
| A successful init container is recorded | init.container.completed after an actual zero-exit init |
| A sidecar coming up is recorded | sidecar.started with the actual sidecar Running |
| A condition seen twice is recorded once | pod.scheduled exactly once despite real repeated observations |
| An ordinary startup is timed and its phases recorded | Pending/Running phase events and a positive startup gauge |

Eight paired production faults remove/rename initialization, scheduling,
init completion, sidecar start or phase events; corrupt the node; defeat
scheduling deduplication; or omit the startup gauge update. A ninth fault skips
lifecycle tracking only on an already-Running first observation. It fails the
original initialized case and the new second-observation check, while retaining
the watched-startup events. Every accepted new Given/When passes; each mutant
fails its intended assertion, not setup or transport.

The old snapshot-only binary reconstructs the original test definitions and
helper using test-source overlays. Its wait helper accepts an unused optional
timeout only so the new live file compiles; the old cases retain their original
15-second behavior. Production faults are build overlays, never source edits.

The first live initialization mutant is excluded: its inherited 15-second wait
expired, task cancellation deleted the pod, and a subsequent Get masked the
timeout with a 404. The fixture now reports WaitErr before reading the pod and
uses bounded 45-second startup/scheduling waits. All nine final live mutants
and controls use that same final, two-observation fixture. An initial phase
overlay targeted compatibility Process instead of production execProcess and
was discarded after source inspection. A gauge overlay initially failed to
compile because startTime became unused; that compile error is not behavioral
evidence.

Six old definitions are removed and three shared live definitions added:
1,046 -> 1,043. Six cases become one: 577 -> 572, comprising 526 local and
46 live. No standalone one-case feature or additional Go test is introduced;
no Go tests are retired. Removed unused synthetic-node binding/creation and
repeated-status branches from the remaining local helper. Existing generic
trace/exit/gauge assertions remain shared.

This fixture tests production runtime observation, pod reuse and actual
execution, not production pod assembly or automatic artifact input staging.
Three observability cases remain local and explicitly use reported status:
image-pull completion, failed input staging and failed init diagnostics.
Their unusual failure premises need separate fidelity work, not silent
replacement by this successful-startup scenario.

Validation on the final source:

- Original six-case control: 6.316s.
- Final real consolidated case, including both observations: 24.592s.
- All nine live task cases: 160.038s.
- All 526 local cases: 186.882s.
- All three native suites: 47.672s; vet passes.
- All nine paired faults detected by both old and new cases.

Mutation durations (old/new, seconds): initialized 7.035/25.227;
scheduled 6.662/24.931; node 7.026/25.086; init 6.774/25.034;
sidecar 7.846/26.100; dedup 7.193/25.136; phase 21.778/24.083;
gauge 5.947/25.117; first-snapshot-only 7.174/25.100.
The consolidated real case is slower than the six host/status cases; fewer
cases here means shared lifecycle evidence and less fixture vocabulary, not
a claimed runtime speedup.

Receipt: /tmp/brine-live-observe.kgPinM/evidence.json, SHA-256
d2e96a3af41a0a66f4be5f8a21097f48926de607fa0f337f765d206b6879a64c.
validate.cjs checks exact old rosters and all fault sites, actual pod UID/RV,
init/sidecar/task output and second-observation receipts, complete recorder
drains, namespace UID cleanup pairs, final local/native controls, catalog and
parsed inventory, removed helper paths and unchanged production sources.
Normal adapter restored and closure hash verified:
abee37634f22999ad6f847ab811f98970654b8b2505e0687ad6d660e3f22aec0.
Independent cleanup checks find no owned namespaces or recent active test
processes. The prior approved resource-cancellation source remains unchanged.

Partial-output put, other host executor/status fixtures and injected resolver
panic remain. The two known nested-upload failures and F23 storage permission
are still open; no fresh full green coverage result is claimed. No production
code changed in this increment. Changes remain uncommitted on core; nothing
was pushed, deployed or published. The goal remains active.

## Failed-input diagnostics from an actual init failure — 2026-09-12

RF-14, “A step whose inputs could not be staged says so”, moves from the local
observability feature into the existing live/task-command.feature. Its original
name assertion remains. The old fixture wrote PodSucceeded with a failed init
status; the new fixture observes a real PodFailed under RestartNever.

The successful-startup helper now also provides a real input-unpack init.
After its observed gate is released, BusyBox tar tries to unpack a missing
archive from an owned emptyDir and fails naturally. The fixture observes the
same pod UID, a new resourceVersion, nonzero init termination and actual
Kubernetes init logs before calling the production process Wait. This ordering
preserves the failure evidence even if a faulty runtime replaces the pod.

The assertion requires the init name and, for this live case, the actual exit,
reason and complete log bytes in the returned error. The same failed pod must
remain available, and the task must produce no stdout. No process result,
Kubernetes status or init log is injected. The existing production
failedInitContainer guard already prevents replacement; no production fix is
needed for this case. The obsolete reported-status RF-14 action is removed.

This is an actual input-unpack failure in a pre-created init container, not the
production artifact-daemon input-fetch protocol. It does not establish F23
publication or automatic staging. The separate failed-init event case remains
local: its repeated-observation deduplication premise is not established by a
single terminal failed-pod snapshot. Image-pull completion also remains local.

Two paired production faults retain the original name assertion's
discriminators:

- Rename the init in the returned diagnostic: old and new fail at the original
  name assertion with the substituted name.
- Disable the failed-init replacement guard: the old fake-status case loses
  the init name and times out; the real case replaces the failed pod and
  incorrectly succeeds, which the original assertion rejects.

A third, new-only fault drops init log bytes. The strengthened live diagnostic
assertion detects the missing actual logs. All accepted Given/When actions pass;
only the intended Then fails. Faults are isolated Go build overlays, and the
production process source remains byte-identical to the before snapshot.

One case moved, none added or removed: 525 local + 47 live = 572.
Two live definitions replace one obsolete action, so vocabulary grows by one,
1,043 -> 1,044. Both live startup cases share database/trace resources, namespace,
gated-pod setup and production runtime construction. There is no new feature
file and no Go-test retirement.

Validation:

- Original one-case control: 17.347s.
- Final real one-case control: 29.215s.
- Name mutation old/new: 5.910s / 28.515s.
- Replacement mutation old/new: 22.621s / 46.112s.
- New-only missing-log mutation: 30.354s.
- All ten live task cases: 179.490s.
- All 525 local cases: 231.873s.
- Vet passes; all three native suites pass in 83.266s.

Receipt: /tmp/brine-live-init.m9fsai/evidence.json, SHA-256
5f0759fbc2a5865c6dc91ddcf87ebf5f45f16757da28b628e3eacbb0dab4baa3.
validate.cjs checks exact rosters and fault sites, actual init identity/status/
log receipts, namespace UID cleanup pairs, complete recorder drains, source
and vocabulary changes, parsed inventory and normal adapter restoration.
The initial 20.328s control saved JSON stdout only; its physical receipts went
to terminal stderr. It is not used for physical/cleanup proof. Final controls
and mutants save both streams, and the validator enforces their physical proof.

Normal adapter SHA-256:
3de18fc691b349528c045ef8d4f2c7ecd15db8d9bb2cb7d69d8920239cc9dfd1.
Independent checks find no owned namespaces or recent active test processes.

Partial-output put, host executor/status fixtures, injected resolver panic,
the two known nested-upload failures and F23 storage permission remain open.
No fresh full green coverage result is claimed. Changes remain uncommitted on
core; nothing was pushed, deployed or published. The goal remains active.

## Image-pull observation from a real scheduled pod — 2026-09-12

OE-04, “The end of an image pull is recorded on the wait span”, moves into
the existing live/task-command.feature. Its original exit-zero and
image.pulled assertions remain unchanged. The synthetic ContainerCreating ->
Running action is removed.

A pre-created BusyBox pod has one scheduling gate and PullAlways on its main
container. The fixture first observes actual Pending/SchedulingGated status,
no assigned node and no container status. The runtime's successful watch
handshake must precede removal of that owned pod's gate. This is an ordinary
pod-spec update, not a fabricated status, forced node assignment or change to
the scheduler, registry or cluster policy.

A separate, unmodified Kubernetes watch starts before release and must observe
the same pod UID transition through ContainerCreating to Running with a
nonempty image ID. The fixture also reads real kubelet Pulling and Pulled
Events for that exact pod UID and main container, and requires the task's
actual stdout through production execution. ContainerCreating alone can mean
other setup, so it is not accepted as proof of an image pull. PullAlways
requests a real pull cycle but can reuse cached layers; no uncached-download
or byte-transfer claim is made.

The independent watcher uses the original client. Only the separately wrapped
runtime client can signal the watch handshake, so opening the observer cannot
release the pod prematurely. Both wrappers/observers leave Kubernetes requests
and responses unchanged. Shared helpers now own runtime construction and the
bounded watch/release/Wait sequence used by successful startup and image pull.
Failed-input diagnostics reuse that runtime constructor as well.

Two paired production mutations preserve the original discriminator:

- Rename image.pulled: both old and new fail at the original event assertion.
- Stop remembering ContainerCreating: both old and new fail at the same
  assertion because no image.pulled event is emitted.

All accepted Given/When actions pass. New mutants still independently observe
actual ContainerCreating/Running, kubelet Pulling/Pulled and real task output.
Their failures are trace regressions, not registry, scheduling or setup errors.
Production faults use build overlays only; process.go remains unchanged.

One case moved, none added or removed: 524 local + 48 live = 572.
Two live definitions replace one synthetic action: 1,044 -> 1,045 definitions.
No new feature file or Go-test retirement. The remaining local observability
case still fabricates failed-init states to exercise repeated-observation
deduplication; the single terminal failed-pod case does not replace that premise.

Validation:

- Original one-case control: 5.386s.
- Initial real control: 22.833s.
- Repeated final real controls: 28.750s and 22.102s.
- Event mutation old/new: 5.875s / 21.267s.
- Tracking mutation old/new: 5.788s / 20.866s.
- All eleven live task cases: 187.250s.
- All 524 local cases: 187.691s.
- Vet passes; all three native suites pass in 41.869s.

Receipt: /tmp/brine-live-pull.U35uP8/evidence.json, SHA-256
c4eea83bd976591d2310c170d487aec4ffad811613b56f38f3bfd5cc15e4056f.
validate.cjs checks exact case rosters and fault sites, pod UID/RV/image
receipts, physical kubelet pull evidence, complete drains, namespace UID
cleanup pairs, unchanged production sources, parsed inventory and catalog.
It also requires the prior successful-startup and failed-input physical
receipts in the full task regression. Local cases may have multiple When
actions; their 540 successful When records span 524 cases.

Normal adapter restored and closure hash verified:
0630b622ed85d47941aa9eeeebe0dcc7d328fa948d2205b84793945af867da2c.
Independent cleanup checks find no owned namespaces or recent active test
processes; git diff --check passes. The README now calls out scheduling-gate
support and read-only Event listing for the live test identity. No permission
grants, deployment changes or production changes were made.

Partial-output put, other host executor/status fixtures, injected resolver
panic, the two known nested-upload failures and F23 storage permission remain.
No fresh full green coverage result is claimed. Work remains uncommitted on
core; nothing was pushed, deployed or published. The goal remains active.

## Integration setup without unused host doubles — 2026-09-12

The integration feature previously installed a host executor and requested a
task workspace in 23 scenarios. Only two of those scenarios actually execute
commands. The others inspect persisted rows, accepted API pod construction,
names, labels, secret references, or a recorded completion state.

The default integration refinement now uses WorkerReady's existing production
SPDY executor and real database/API dependencies, with no task-workspace
resource request. This preserves exec-mode pod construction without pretending
to execute anything. The former “the step's container runs” action is now
“the step's pause pod is created”: it still calls Container.Run and inspects
the real API pod without calling Process.Wait. Its implementation and all
existing pod/row assertions are preserved.

The two artifact-chain scenarios explicitly opt into the host executor and
workspace afterward. They remain migration work, not evidence of kubelet
execution or real artifact publication. Both host execution and resource-script
installation reject missing explicit host setup, before fabricating Running
status or writing scripts. Two temporary negative fixture checks remove that
opt-in and fail at the intended guard messages. These are guard checks, not
production mutations or new permanent scenarios.

Consolidated “A cache hit is served from the database without scheduling
anything” into “A persisted volume comes back naming the worker that holds it”.
Their setup and action were identical after normalizing the handle string:
create the same artifact-type database volume and look it up twice. The
retained case keeps its handle/worker/row assertions and now also requires that
lookup creates no pod. The distinct resource-cache association and locator
cases remain separate.

Two paired production faults preserve the contracts:

- Create a real API pod during volume lookup: the old cache-hit case fails
  its no-pod assertion; the consolidated persisted-volume case now fails the
  exact same assertion.
- Return an incorrect worker from the volume: both the persisted-volume and
  locator cases fail their original worker-identity assertion, before and after.

All accepted mutation setup/actions pass. Faults are build overlays only;
worker.go and volume_daemonset.go are unchanged. No production code changed.
The shared live Git container-row assertion and the rest of the integration
helper suffix remain byte-identical.

The feature drops from 32 to 31 cases. Twenty retained cases no longer request
host setup; two explicitly retain it, and the removed duplicate accounts for
the other old use. Overall inventory is 523 local + 48 live = 571 cases.
There is one new general setup definition and one renamed action:
1,045 -> 1,046 definitions. No feature file or Go tests are added or retired.

Validation:

- Original 32-case control: 15.238s.
- Initial cleaned-up 31-case control: 17.266s.
- Final 31-case control: 15.695s.
- Unexpected-pod mutation old/new: 16.194s / 16.710s.
- Wrong-worker mutation old/new: 15.545s / 16.298s.
- Both missing-host-opt-in checks fail as intended in 5.562s.
- All 523 local cases pass in 184.629s.
- Vet passes; all three native suites pass in 39.475s.

Receipt: /tmp/brine-integration-clean.1fWWMY/evidence.json, SHA-256
b4ab86053dea027a585a90f52644aee488a3920bd2c0943e7ef1aad389ccd10a.
validate.cjs compares every executed original step in all 31 retained cases,
normalizing only the declared setup/action wording changes and the moved
assertion. It checks the merged cases' identical premise, exact mutation and
guard sites, complete recorder drains, explicit host-use counts, parsed
inventory, unchanged production/live sources and normal adapter restoration.
The initial pod overlay lacked imports; its compile failure is not evidence.
An initial patch-helper variable collision happened before edits; control.log
is the ensuing unchanged-source run and is not migration evidence. A validator
syntax typo was corrected before the accepted audit.

Normal adapter SHA-256:
b1aef9eee3f0ef8484579cd8456ad13f0ed5fa286b46568d9534cd31f7ff8eb0.
No recent active test processes remain, and git diff --check passes. No live
cluster run was needed for this local API/setup increment; the last eleven-case
live task regression belongs to the preceding image-pull increment.

Partial-output put, the two host integration chains, other host/status fixtures,
seeded completion-state fixtures, injected resolver panic, the two known
nested-upload failures and F23 storage permission still need attention. No
fresh full green coverage result is claimed. Changes remain uncommitted on
core; nothing was pushed, deployed or published. The goal remains active.

## Completed-task recovery: replace seeded state and consolidate (2026-09-12)

The local case "A restarted web resumes a step that had already finished"
manually created a pod and set concourse:exit-status on the current container.
Despite its title, its Attach call used that same object and exercised only
the in-memory completion branch. It did not reconstruct a runtime container.

Its completion assertion is now part of the existing successful live task
case. BusyBox executes echo hello world through production SPDY and the
supervisor. The original startup, output, status/property, worker-label,
supervisor-state, placeholder and pod-survival assertions remain. The case then
reattaches to the current container and reconstructs a fresh runtime container,
asserting exit 0 after each. No completion property, annotation or pod status
is seeded for this case. This models runtime recovery, not a full ATC restart.

Preserved metadata: task type, pipeline my-pipeline, job unit-test, build 42,
empty step name, handle 550e8400-e29b-41d4-a716-446655440000. Real execution and
both Attach receipts name my-pipeline-unit-test-b42-task-550e8400 and the same
pod UID. Task helpers now derive Kubernetes pod names from metadata while
retaining the opaque handle for database ownership and process IDs. Metadata
is private fixture state; shared Attach capture reports business failures to
the existing exit-status assertion. The local missing-pod diagnostic remains.

Two obsolete definitions were removed and two reusable refinements/actions
added. Inventory: 1,046 definitions, 570 cases = 522 local + 48 live; the live
task feature still has eleven cases. No production source changed and no Go
tests were retired in this increment.

Evidence: /tmp/brine-task-recover.Qcuyu6/evidence.json, generated by validate.cjs.
SHA-256: 437dba1fbb907802d9c60b15e82808cdd63aa4eb5a5a910fd1b32890364fbf60.
The audit checks every retained original task assertion, unchanged remaining
live scenarios, the exact local roster minus the retired case, catalog deltas,
complete disposer drains, same-UID live receipts, and unchanged production
container.go/process.go. Full stdout and stderr, baseline sources, adapters,
and isolated build overlays are retained beside it.

Validation:

- Original focused case: 1/1 passed, 5.238s.
- Consolidated real-pod control: 1/1 passed, 21.261s.
- Paired cached-status +1 fault: original failed its resumed-status Then;
  replacement failed the current-container exit-status Then. All prerequisites
  passed; 5.511s / 21.258s.
- Additional annotation-reader +1 fault: initial execution and cached
  reattachment passed, fresh-container recovery failed its exit-status Then,
  21.327s. This is new-path evidence, not a paired claim for the seeded case.
- Full local tier: 522/522 passed, 188.051s.
- Full live task feature: 11/11 passed, 185.886s.
- Native Ginkgo suites: 3/3 passed, 43.495s; go vet passed.

The first edit stopped on a nonunique feature anchor before writing that
feature or building. Scoping it to the first case resolved the edit; no
partially edited adapter run is counted. Final normal adapter SHA-256:
5fafc07770356847665af8692fb6152d4ac3aa00201ae0e153c5fe895d39446f.
All commands terminated; no owned live namespaces or recent active test
processes remain. git diff --check passes.

The overall goal remains active. Remaining host/status and seeded fixtures,
partial-output put, injected resolver panic, known nested-upload failures and
F23 storage authorization still need attention. This increment does not claim
a fresh full-green coverage result. Changes remain uncommitted on core;
nothing was pushed, deployed or published.

## Interception completion: production writes the saved status (2026-09-12)

The previous increment's consolidation was verified progress. Inspection of the
remaining host artifact chains confirmed they still persist empty artifact
records and execute echo scripts, not verified byte handoff. They were not
weakened or relabelled as migrated here.

The terminal interception case already finished a real PID 1 and waited for
the kubelet's terminal status, but finishInterceptPod manually wrote the
completion annotation. It now reuses runTask with the recorded step metadata
and opaque handle to run an actual supervised exit command first. Production
execProcess writes completion. The fixture checks the actual process result
and remote supervisor state, then asks PID 1 to finish and observes its actual
terminal status. No annotation or pod status is fabricated.

The feature, all four cases, every original assertion, and the entire step
catalog are unchanged. The terminal case still requires a polite interception
refusal and the saved status to remain on the metadata-named pod. The helper
retains its original UID check. Production completion and actual pod exit
receipts identify the same pod UID; the command and PID 1 both exit 0.

Evidence: /tmp/brine-intercept-completion.lPnIR5/evidence.json, produced by
validate.cjs. SHA-256:
de53d211151fa405e7f885cdd71f242bc403f0f655de704a75215a81fb1e016f.
The directory retains before-source snapshots, both output streams, isolated
production build overlays and the old/new fault adapters. The audit verifies
identical feature steps, unchanged catalog, exact regression rosters, complete
disposer drains, same-UID receipts and unchanged production source.

Validation:

- Original interception controls: 4/4 passed, 63.306s.
- Updated interception controls: 4/4 passed, 66.908s.
- Paired annotation-erasure fault in the terminal refusal branch: original and
  replacement both fail the existing saved-status assertion (got empty rather
  than 0), 15.301s / 19.230s. Setup and refusal checks pass.
- Production annotateExitStatus writing exitCode+1: original focused case
  stays green, 16.803s, because its fixture supplies the saved value. The
  replacement fails the existing saved-status assertion with 1 rather than 0,
  18.726s, after the real command and PID 1 both exit 0. This demonstrates an
  additional fault detected, not just a different way to seed the same state.
- Local tier: 522/522 passed, 188.335s.
- Shared live task regressions: 11/11 passed, 186.870s.
- Native Ginkgo suites: 3/3 passed, 41.526s; go vet passed.

Inventory remains 1,046 definitions and 570 cases: 522 local + 48 live.
Normal adapter SHA-256:
a775f0bd87f2f3fd857fb74b14fcd3e1d942f62d9bdb78522080055a4254de75.
All sessions terminated and no owned live namespaces or recent active test
processes remain; git diff --check passes. No production source changed and
no Go tests were retired in this increment.

A specific approval question was sent again for the previously isolated
Volume.StreamIn directory-creation fix. The current source still passes a
possibly absent destination directly to tar -C; that production fix remains
unapplied pending an answer. The two known upload failures, remaining host
artifact chains, partial-output put, other reported-status fixtures, resolver
panic injection and F23 storage authorization remain outside this completed
increment. No fresh full-green coverage result is claimed. Changes remain
uncommitted on core; nothing was pushed, deployed or published. Goal active.

## Current-OOM diagnosis: consolidate reported state into a real OOM (2026-09-12)

The previous turn removed seeded interception completion with verified mutation
evidence, so it was progress. The remaining artifact-handoff fixture was
inspected next: it still uses host execution, reported status/binding and
daemon host storage. It cannot be migrated faithfully by substituting an
emptyDir-only handoff that omits post-pod-deletion publication. That work and
the production upload fix remain approval-dependent.

The local "A step killed for using too much memory is told so" case is now
consolidated into the existing live exec-interruption OOM case. Its removed
setter manufactured a Running pod with current main OOMKilled/137 and no
restart history. The replacement retains those exact salient inputs:
the established PID 1 workload exhausts its independently verified 64 MiB
cgroup limit, while a 16 MiB-limited sidecar keeps the real pod Running.

All original production exec-interruption checks run first, unchanged. An
explicitly executor-free worker then looks up the same persisted container;
Attach returns the concrete direct compatibility Process, which diagnoses the
same pod. The fixture requires matching UID, Running phase, current main
OOMKilled/137, no previous termination or restart, and a running sidecar.
No pod status, signal, node setting or container result is manufactured. The
cgroup verification/arming helper is byte-for-byte unchanged. Main OOM and
compatibility receipts identify the same actual pod and container.

The three retired assertions are appended unchanged: OOMKilled in the error,
the exceeded-memory-limit explanation, and Pod Failure Diagnostics in stderr.
One obsolete setter is replaced by one compatibility action; no net vocabulary
growth or new live case. Inventory: 1,046 definitions and 569 cases =
521 local + 48 live. Other OOM/restart-priority reported fixtures remain;
this does not claim their repeated-restart or CrashLoopBackOff premises.

Evidence: /tmp/brine-oom-consolidate.lh1E2z/evidence.json, generated by validate.cjs.
SHA-256: d6157607dc4a33d61e69736ce95fcf748912811fe1863023cda6a25a098f47d2.
Before sources, adapters, both output streams and isolated production build
overlays are retained alongside it. The audit checks retained feature steps,
exact failure messages/sites, real same-UID receipts, unchanged OOM safety
helper, catalog deltas, local roster and complete disposer drains.

Validation:

- Original reported case: 1/1 passed, 5.449s.
- Original two real exec-interruption cases: 2/2 passed, 24.332s.
- Updated two real exec-interruption cases: 2/2 passed, 29.892s.
- Paired current-termination detector fault: both fail the OOMKilled assertion
  because the compatibility watcher reports success; 5.341s / 18.431s.
- Paired compatibility explanation fault: both fail the exceeded-memory-limit
  assertion with the same error text; 5.314s / 22.679s.
- Paired compatibility diagnostic omission: both fail the diagnostic-log
  assertion with empty stderr; 4.890s / 23.149s.
- All new fault prerequisites and original production exec checks pass; failures
  occur in the retained Then/And assertions, not in the real-OOM setup.
- Full local tier: 521/521 passed, 187.603s.
- Native Ginkgo suites: 3/3 passed, 41.463s; go vet passed.

Normal adapter SHA-256:
a7288f8617868ba9d2adc8cdfb39d7a7847871639cc2ee4687ad0e3e55241350.
All command sessions terminated. No owned live namespaces or recent active
test processes remain; git diff --check passes. No production source changed
and no Go tests were retired in this increment.

The goal remains active. Remaining host/status fixtures, partial-output put,
resolver panic, upload failures and storage authorization are not resolved by
this consolidation. The upload-fix approval question remains unanswered; no
production fix was applied. No fresh full-green coverage result is claimed.
Changes remain uncommitted on core; nothing was pushed, deployed or published.

## Kubelet startup refusals: replace two reported waiting rows (2026-09-12)

The preceding OOM consolidation was verified progress. Two more reported rows
could be replaced without hostPath, node changes or additional cluster grants:
InvalidImageName and CreateContainerConfigError. They now share the new
live/startup-failure.feature outline and one action. The remaining
ImagePullBackOff, ErrImagePull and CrashLoopBackOff rows retain their existing
reported-state premises and assertions; only their outline positions shift.

The new fixture uses production FindOrCreateContainer/Container.Run with an
explicitly executor-free worker, preserving the original direct compatibility
Process path. The invalid-image row supplies "not a valid image". The config
row supplies an ordinary required SecretEnv reference and confirms that its
named Secret is absent from the newly owned namespace. Nothing creates a
Secret or fabricates a registry response, pod status or process result.

Before Process.Wait, the fixture requires actual kubelet Pending status, the
expected current waiting reason, an actual node and stable pod UID, a nonempty
diagnostic message, and no prior termination or restart. An unexpectedly
running/terminated command is rejected. A five-second independent Wait bound
contains a broken classifier; normal refusals are immediate. This does not
claim actual task command execution or production use of the compatibility
fallback. Work-directory and sparse metadata premises remain unchanged.

The original reason assertion is reused verbatim. Catalog: 1,047 definitions
(one added action, none removed); cases: 569 total = 519 local + 50 live.
No scenario growth. The additional action is shared across both real faults.

Evidence: /tmp/brine-startup-errors.yV2YQG/evidence.json, generated by validate.cjs.
SHA-256: 256bc0ed3d4c0549655e9c91c119fe0d44da2df4d5481cb3ee492b8866eee7e3.
The audit checks every remaining local step against the prior run, normalizing
only the three surviving outline row numbers; retained assertions; expected
real Pending/no-restart receipts; exact old/new mutation errors; catalog delta;
unchanged existing live helpers except registration; and complete cleanup.
Both output streams, before sources, adapters and build overlays are retained.

Validation:

- Original focused reported rows: 2/2 passed, 5.636s.
- Real kubelet rows: 2/2 passed, 31.823s.
- Paired diagnostic-identity loss: both original/new rows fail the original
  reason assertion, 5.017s / 30.964s. The fault deliberately discards BOTH
  waiting reason and message: the old synthetic messages themselves repeated
  the reason, so changing only the reason field would not prove this property.
- Paired false success in compatibility failure handling: both original/new
  rows fail because the step succeeded, 4.863s / 21.557s.
- All real fault premises and When steps pass before those Then failures.
- Full local tier: 519/519 passed, 186.442s.
- Native Ginkgo suites: 3/3 passed, 40.377s; go vet passed.

Real messages identify an invalid reference format and the missing named
Secret. No external registry was needed to produce either refusal. Normal
adapter SHA-256:
aa7f31440bc479e572b6f9d1b34ea0c25680d6842d1b807794233fe9804bbbe2.
All sessions terminated; no owned live namespaces or recent active test
processes remain; git diff --check passes. No production source changed and
no Go tests were retired in this increment.

Remaining host/status fixtures, partial-output put, injected resolver panic,
the known upload failures and storage authorization still need attention.
The production upload fix remains unapplied pending approval. No fresh full
green coverage result is claimed. Changes remain uncommitted on core; nothing
was pushed, deployed or published. The goal remains active.

## Remove the last fake Kubernetes clientset factory (2026-09-12)

The preceding real startup-refusal migration was verified progress. A
repository-wide consumer check found that only F23 still called NewCluster;
the old ClusterReady state, Ready conversion and configuration options had no
other consumers. F23 now starts with the existing WorkerReady real-API Given,
derives its team and namespace from that state, and keeps the original
executor/volume-repository/locator semantics. The fake factory file,
Cluster/ClusterReady types and options were deleted. The tracked file is
recoverable from Git and its before-copy is retained with the evidence.

This is explicitly not F23's full live migration: localExecutor still injects
exec EOF, Running is still reported through the API, and no command writes an
artifact here. The real API now supplies validation, UID/resourceVersion and
persistence. The feature and source both state the remaining substitute.

The first real-API run exposed a fixture defect hidden by the fake client:
spec.volumes[2].hostPath.path was empty. The focused case and initial local run
failed that validation (1 failure out of 519), rather than exercising either
business assertion. The correction supplies an existing owned task-workspace
directory as ArtifactDaemonHostPath. This is an API-only local fixture; no
live Kubernetes node mounts a volume, no cluster authorization was expanded,
and no production source changed. The final run records a real UID and
resource version for the same reported-Running pod.

A new native AST-import guard recursively scans step Go sources and rejects
fake clientset/dynamic/metadata/controller-runtime imports and client-go
reactors. It rejects an empty scan rather than pinning a source-file count.
Its tests check unaliased, named, blank and dot imports, permit real client/
envtest imports and ignore documentation/test strings. A build overlay that
disables detection makes all five positive import-family tests fail in their
assertions, not compilation.

Evidence: /tmp/brine-last-client-fake.LXhIDW/evidence.json, generated by
validate.cjs. SHA-256:
b7ed42e3b96cf832f1e0d2bcc1e4cdcd41971d71e24a876e30bf7d56f6b9b4af.
The audit verifies every original local step, normalizing only F23's new
shared setup and Given-to-When wording; original assertions and exact paired
fault errors; real API receipts; catalog/pattern stability; guard controls and
disabled-detection failure; unchanged production source; and complete drains.
Before sources, tested binaries, both output streams and overlays are retained.

Validation:

- Original fake-client F23 control: 1/1 passed, 4.950s.
- Corrected real-API F23 control: 1/1 passed, 4.822s.
- Paired false-success fault: original/replacement fail the same failure
  assertion, 5.475s / 5.358s.
- Paired erroneous publication on exec failure: original/replacement fail the
  same absent-location assertion for severed-handle-output-out, 5.097s / 4.976s.
- Initial real-API local run: 518/519 passed, 184.723s; the sole failure was the
  invalid empty hostPath. It is retained as failed evidence, not a green run.
- Corrected full local run: 519/519 passed, 181.872s.
- Final native Ginkgo suites: 3/3 passed, 38.128s; final go vet passed.
- Focused non-DB import guard tests pass; disabled-detection mutation fails.

No Brine scenario or definition was added: 569 cases = 519 local + 50 live,
with 1,047 step definitions. Only F23's required input state/resource wiring
changes in the catalog. No Go tests were retired. Normal adapter SHA-256:
0134caf4265f9abf6be9601f2addc8166dede461cc396fde7cd0031cb590e722.
All sessions terminated and no recent active test processes remain;
git diff --check passes. No live cluster run was needed for this local setup
change. F23's live transport/storage premise, other runtime/status substitutes,
partial-output put, resolver panic and the approved-scope questions remain
unfinished. The production upload fix remains unapplied pending approval.
No fresh full-green coverage result is claimed. Changes remain uncommitted
on core; nothing was pushed, deployed or published. The goal remains active.

## Real OOM-versus-crash-loop priority (2026-09-12)

The last-fake-client removal was verified progress. The next reported-status
case, “An OOM kill is reported ahead of the crash loop it caused,” now runs in
live/oom-priority.feature. Its name and both original assertions are unchanged:
the error names OOMKilled and does not mention CrashLoopBackOff. There are still
569 scenarios: 518 local + 51 live. One shared action is added, bringing the
catalog to 1,048 definitions; all existing definitions remain unchanged.

The fixture shares the existing live OOM pod and arming helpers. A real main
container allocates beyond its independently verified 64 MiB cgroup limit.
For this case only, RestartPolicy=Always and an owned emptyDir retain the
arming gate across restarts; each restarted container verifies its own limit
before allocation. A bounded sidecar keeps the pod Running. Before diagnosis,
the test requires the same API UID, a scheduled node, current
CrashLoopBackOff with a real message, previous OOMKilled/137 with a container
identity, at least one restart, and a running sidecar. No status, process error
or signal is fabricated. No hostPath or cluster policy permission is added.

An explicitly executor-free worker supplies the direct compatibility Process
for the priority assertion; this is not a claim that production task execution
uses that fallback. The existing production exec-interruption and current-OOM
cases retain every step and the same original pod spec, cgroup probe and
allocation command. No production source was changed in this increment.

Validation:

- Original reported-state control: 1/1 passed, 4.855s.
- Real OOM/crash-loop control: 1/1 passed, 35.772s.
- Ignore last OOM termination: old/new both fail the OOM-naming assertion,
  4.949s / 35.263s. The new diagnostic includes actual kubelet pod identity.
- Append CrashLoopBackOff to the OOM error: old/new fail the negative assertion
  with identical errors, 5.166s / 40.807s.
- Existing live exec-interruption/current-OOM cases: 2/2 passed, 32.686s.
- Full local tier: 518/518 passed, 184.806s; every retained step is unchanged.
- Native Ginkgo: 3/3 suites passed, 39.462s; go vet passed.

Evidence: /tmp/brine-oom-priority.yy29Dg/evidence.json, generated by validate.cjs.
SHA-256: 757a125260421d87ca263ec56943b01448de34b0d65c1bda4ea48d145d2adef4.
Normal adapter: bfb07503054ced0edc9817ff18acc0307fd3c79e043c086c5fc696767d4c7dea.
The audit checks paired failures, original assertions/retained local steps,
shared-helper fidelity, catalog changes, real runtime receipts, complete
recorder drains and matched namespace UID creation/removal. All sessions
terminated; independent checks found no owned live namespaces or recent
active test processes. git diff --check passes.

Other reported-state and host-runtime substitutes, partial-output put and
injected resolver panic remain. The known nested-upload production fix and
F23 live storage authorization remain pending; neither was applied here.
No fresh full-green coverage result or Go-test retirement is claimed. Changes
remain uncommitted on core; nothing was pushed, deployed or published.
The goal remains active.

## Real direct-compatibility completion (2026-09-12)

The real OOM-priority migration was verified progress. The remaining repeated-
OOM diagnostic outline cannot simply merge into it: that outline also requires
a Failed phase, current and previous OOM terminations, an exact restart count,
and a termination message on both execution paths. Those premises remain
unmigrated; the Running/last-OOM priority fixture does not establish them.

Instead, the two ordinary compatibility exit scenarios now run real BusyBox
commands through production pod construction and the explicit executor-free
Process fallback. Their names and all original assertions remain unchanged.
The kubelet supplies Succeeded/0 or Failed/1, with a real scheduled node,
container identity, start/finish times and no restart history. Only then does
Process.Wait interpret the terminal state and perform its normal cleanup.
This is explicitly not production execProcess or supervised task execution.

One shared live action replaces the reported-status action; the two cases use
the shared live database/worker setup. No scenario or net definition is added:
569 cases = 516 local + 53 live; 1,048 definitions. All other definitions and
all retained local steps are unchanged. The deleted status setter and original
features are retained in the evidence snapshots and recoverable from Git.

The initial live run failed both setup actions because the kubelet briefly
reported container termination while the pod phase was still Pending. The
corrected fixture waits for the real terminal phase; it does not set status
or substitute a weaker premise. The failed run remains in new.log (22.018s).
The corrected control began after the normal adapter build completed.

Validation:

- Original reported-state cases: 2/2 passed, 5.505s.
- Corrected real completion cases: 2/2 passed, 26.141s.
- Wrong exit status: old/new fail the same nonzero-exit assertion,
  6.250s / 27.404s.
- Skip completion cleanup: old/new fail the same pod-absence assertion,
  5.372s / 26.472s.
- Misclassify a nonzero exit as a runtime error: old/new fail the same
  exit-result assertion, 5.588s / 24.754s.
- Every paired failure message is identical; setup/actions pass first.
- Full local tier: 516/516 passed, 185.148s.
- Previous real OOM-priority regression: 1/1 passed, 35.406s.
- Native Ginkgo: 3/3 suites passed, 37.967s; go vet passed.

Evidence: /tmp/brine-compat-exit.DEym2I/evidence.json, generated by validate.cjs.
SHA-256: 567a372190ad912eb451d46336b75c7839a92ab51d3a1f3384171ee668a2f156.
Normal adapter: 79c5fba776e4203ad69989a4780d0d6bf735b7f22ae68210f278ae6b5218ed2b.
The audit checks original assertions, all retained local steps, exact paired
failures, real container receipts, catalog/count stability and complete
recorder/namespace drains. All sessions terminated; independent checks found
no owned live namespaces or recent active test processes. git diff --check
passes. No production source changed in this increment.

Other host/reported-state substitutes, partial-output put and injected
resolver panic remain. The nested-upload production fix and F23 live storage
authorization remain pending. No fresh full-green coverage or Go-test
retirement is claimed. Changes remain uncommitted on core; nothing was pushed,
deployed or published. The goal remains active.

## Real compatibility sidecar completion (2026-09-12)

The ordinary compatibility-exit migration was verified progress. Both SC-10
rows now share live/compatibility-process.feature and its completion fixture.
Their original handles, PostgreSQL/Redis images, sidecar names, exit codes
(0 and 42), outline title and assertion text are unchanged. The reported
Running/main-terminated/sidecar-running setter is removed. There are still
569 cases and 1,048 definitions: 514 local + 55 live, no net additions.

Production pod construction launches the actual services. PostgreSQL receives
test-only authentication configuration; no Service or ingress is exposed.
Sidecar CPU/memory limits are 250m/256Mi, within the unchanged namespace caps.
Main remains behind an in-pod file gate until the sidecar's actual readiness
command responds. After main exits, the test requires the same pod UID,
Running phase, a real main termination with no restart history, and the same
still-running sidecar container responding again. Only then does the explicit
direct compatibility Process interpret and clean up the pod. This does not
claim production task execution uses that fallback.

The first live run passed its exit assertions but failed both immediate
absence checks: Kubernetes removes running sidecars asynchronously. The
cleanup assertion now requires every remaining pod to already have a deletion
timestamp; an untouched pod still fails immediately. It then waits at most
15 seconds for actual API absence. A deletion request alone is never success.
This is an explicit observation-timing change, not a fabricated response or
an assertion that terminating means removed.

An isolated fixture overlay uses the existing UID-safe finalizer helper to
hold real deletion. Both cleanup assertions fail at exactly 15 seconds; only
the recorder disposer then releases the owned finalizer. Both namespaces drain
completely. No finalizer fault or production fault is committed to the fixture.

Validation:

- Original reported-state outline: 2/2 passed, 7.072s.
- Initial live outline: both cleanup assertions failed, 43.696s; retained.
- Corrected live outline: 2/2 passed, 33.449s.
- Ignore main termination while the pod is Running: old/new both rows fail
  their exit-result checks, 45.492s / 47.071s.
- Lose the nonzero main exit: old/new only the 42 row fails,
  5.760s / 30.728s.
- Skip runtime deletion: old/new both absence checks fail,
  5.613s / 39.316s; the new checks reject untouched pods without polling.
- Each paired production fault has identical old/new assertion errors.
- Hold real requested deletion: both checks fail at 15s, 59.673s total.
- Ordinary compatibility exits: 2/2 passed, 24.619s.
- Full local tier: 514/514 passed, 185.654s.
- Native Ginkgo: 3/3 suites passed, 40.874s; go vet passed.

Evidence: /tmp/brine-compat-sidecars.tFajdB/evidence.json, generated by
validate.cjs. SHA-256:
72061196ec1394dd156f939a0aed024791f9b14aa7c7445e82b9325d897b95b9.
Normal adapter: 8f52014ec362d94676056b3ac89fb4239132149f769d99e3e9a10bad4c31fcdc.
The audit verifies all retained local steps, original Examples and checks,
the repository feature's exact correspondence with executed slices, paired
faults, actual service/container receipts, bounded held-deletion failures,
catalog/count stability and complete recorder/namespace drains. Independent
checks find no owned live namespaces or recent active test processes; all
sessions are terminal and git diff --check passes.

No production source or cluster policy changed. The remaining host/status
substitutes, partial-output put, injected resolver panic, pending nested-upload
fix and F23 storage authorization remain open. No fresh full-green coverage
result or Go-test retirement is claimed. Work is uncommitted on core; nothing
was pushed, deployed or published. The goal remains active.

## Real image-pull refusals and truthful task setup language (2026-09-12)

The sidecar migration was verified progress. The remaining ErrImagePull and
ImagePullBackOff rows now extend live/startup-failure.feature's existing
action and outline. Their original failure-name assertions are unchanged.
There are still 569 scenarios and 1,048 definitions: 512 local + 57 live.
The remaining Pending/CrashLoopBackOff/no-history row is a plain scenario
instead of a one-row outline, with every expanded step unchanged. It is not
claimed covered by the real Running/last-OOM priority case.

The kubelet attempts valid, scenario-unique BusyBox tags. The fixture requires
the actual Pending/current-waiting reason, a scheduled node, nonempty message
naming the requested image, and no termination or restart history. Independent
Warning/Failed kubelet events must identify the same pod UID, main container
and image. Every accepted control and mutation observed a genuine missing-tag
error. No registry response, Kubernetes status or command result is fabricated.
The original invalid-image and missing-Secret cases retain their setup/actions
and assertions in the same four-row live outline.

The shared Given is now “a task using Kubernetes,” not “a task running on
Kubernetes”: it prepares task state rather than starting a container. A
repository-wide consumer search found 16 occurrences across the definition
and eight live feature files. Every other consumer differs only by that exact
phrase. The catalog's typed resource contract is unchanged; no alias or new
definition is added. Native vocabulary guards parse all features and check
resolution, ambiguity and unused definitions.

Validation:

- Original reported pull-failure rows: 2/2 passed, 6.129s.
- Original live invalid-image/missing-Secret baseline: 2/2 passed, 31.006s.
- Expanded real startup suite: 4/4 passed, 70.958s.
- Drop both waiting reason and message: old two rows / new four rows fail
  their reason assertions, 4.768s / 69.330s.
- Report startup refusal as success: old two rows / new four rows fail
  their failure assertions, 5.587s / 50.969s.
- Both migrated rows retain identical old/new mutation error messages;
  all physical setup and action steps pass first.
- Full local tier: 512/512 passed, 185.168s.
- Native Ginkgo: 3/3 suites passed, 40.310s; go vet passed.

Evidence: /tmp/brine-startup-pulls.JDxgx9/evidence.json, generated by
validate.cjs. SHA-256:
6ac7177a2af1bf88a5fd556f9250dbed84a71c64b55491556842080d4d096d50.
Normal adapter: b34858ce1416e9eb3135f00460453db08a8094d713059b2221cba9ac2dc4ceff.
The audit checks all retained local steps, the plain crash-loop scenario,
existing live rows, paired errors, actual kubelet/pull-event receipts,
wording-only diffs, catalog/count stability and complete resource drains.
All sessions are terminal; independent checks find no owned live namespaces
or recent active test processes; git diff --check passes.

No production source or cluster policy changed. Other host/status substitutes,
partial-output put and injected resolver panic remain. The known nested-upload
fix and F23 storage authorization remain pending. No fresh full-green coverage
result or Go-test retirement is claimed. Changes remain uncommitted on core;
nothing was pushed, deployed or published. The goal remains active.

## Real image-pull scheduling diagnostic probe (2026-09-12)

Before combining the two diagnostic and two metric cases, a real-pod probe
reused the existing live startup action and required the original substring
Condition: PodScheduled=True. The normal adapter fails only that final log
assertion; actual Pending/ImagePullBackOff setup and failure classification
pass. writePodDiagnostics filters out true conditions without a reason; the
reported fixture supplies an artificial Scheduled reason and masks the gap.

An isolated Go overlay includes PodScheduled conditions in that filter. The
same real-pod probe passes with this candidate. Baseline: 0/1, 28.976s;
candidate: 1/1, 32.181s. Both runs have real kubelet missing-tag pull receipts,
complete recorder drains and matched namespace create/remove UIDs. The normal
adapter and production process.go remain unchanged. No suite scenario or
definition was added, removed or weakened; the proposed consolidation is not
claimed complete. Independent namespace and git diff --check checks pass.

Evidence: /tmp/brine-scheduling-diagnostic.JlB4ra/evidence.json. SHA-256:
e01fc81bccba2feca561a240ed411d9e0a07e908afcb8a817a2aab42d2b8e9ea.
The overlay, regression feature, binaries and both run logs are retained in
that directory. Applying this production change and the previously verified
nested-upload change awaits explicit scope confirmation. No new full-suite
or coverage result is claimed; the migration goal remains active.

## Real sidecar-first exit ordering (2026-09-12)

The clean-sidecar/failing-main case moves from failure-priority.feature to
the existing live compatibility feature. Its action wording and final exit-1
assertion are unchanged. The action now consumes LiveTaskPlan and shares
completeLiveCompatibility; the old status setter is removed. No aliases or
extra cases are introduced: 1,048 definitions, 569 scenarios (511 local,
58 live). The four existing compatibility cases retain their exact feature
text and are included in the five-case live regression.

A real BusyBox log-shipper command exits 0 while main waits at the existing
file gate. The fixture observes that termination, container identity, finish
time and no restart history before releasing main by real SPDY. After main
exits 1, actual kubelet status must be Failed and retain both results, with
the sidecar listed before main. That array ordering is essential: otherwise
a mutation selecting the first terminated container would escape detection.
No Kubernetes status, execution result or client response is manufactured.

Validation:

- Original reported case: 1/1 passed, 4.823s.
- Shared live compatibility feature: 5/5 passed, 67.297s.
- Select the first terminated container rather than main: original and live
  replacement fail identically (expected exit 1, got 0), 5.046s / 16.647s.
  All physical setup/action steps pass before the assertion rejects the fault.
- Full local tier: 511/511 passed, 184.660s.
- Native Ginkgo: 3/3 suites passed, 39.017s; go vet passed.

Evidence: /tmp/brine-sidecar-order.D9Mb1n/evidence.json, checked by
validate.cjs. SHA-256:
13f2fc6d4c7d5120ce9e5cd3e73c837967797ca1d1eec9ba99ec71a4eb432161.
Normal adapter: 552f47eff1b81af45a10b79589e4969cd1e5c43a162cf88a3d068f32d6cad648.
The validator compares all retained local step sequences, the unchanged
output contract, both exact mutation errors, actual pod receipts, matched
namespace UIDs, complete drains and unchanged production source. All sessions
are terminal; independent cleanup and git diff --check checks pass.

No production source or cluster policy changed. The scheduling-diagnostic and
nested-upload fixes still await approval; remaining test doubles and fresh
full-green coverage remain open. No Go tests were retired. Changes remain
uncommitted on core, with nothing pushed, deployed or published.

## Real production startup timeout through the shared init gate (2026-09-12)

RF-08 moves from pod-lifecycle.feature to the existing live startup feature.
The action phrase, timed-out assertion and failure-diagnostics assertion are
unchanged; the action consumes LiveTaskPlan instead of reported StepRunning
state. The status-writing action is removed. Inventory stays at 1,048
definitions and 569 scenarios: 510 local and 59 live.

The existing observability init setup is extracted into prepareLiveInitPod.
Its pod spec, real scheduling/kubelet observation and SPDY gate-file check
are unchanged, including both successful-init and missing-input consumers.
The timeout case reuses that physical gate and keeps the original 200 ms
startup and scheduling deadlines. It verifies the production execProcess
type, then observes the same pod Pending and same init container Running
after Wait, with no regular container execution. The control Wait took
200.90719 ms. No Kubernetes status or command result is manufactured.

Validation:

- Original reported timeout: 1/1 passed, 5.730s.
- Live startup feature plus both existing init-gate consumers: 7/7 passed,
  117.733s. All four prior compatibility refusal rows remain unchanged.
- Remove generic startup-timeout diagnostics: old and new timeout cases fail
  the same final log assertion with the same empty-log error, 5.267s / 18.579s.
  The physical setup and timeout-classification assertion pass first.
- Full local tier: 510/510 passed, 184.098s.
- Native Ginkgo: 3/3 suites passed, 39.404s; go vet passed.

An initial live invocation supplied a feature directory rather than a glob;
the runner rejected it before any scenario. That preflight log is retained.
The corrected glob executes all seven intended cases; no failing scenario
was filtered out or silently omitted.

Evidence: /tmp/brine-startup-timeout.TMeAlF/evidence.json, checked by
validate.cjs. SHA-256:
9e54eaf14e42570768803ffe25cdb6b8923833514743d352ba002e192925902e.
Normal adapter: 526c10bd6f29612fed4a4c4b2cbf6c9fe13bb17ad65e35630bfc66a1966ce27e.
The audit checks the unchanged extracted physical setup, all retained local
step sequences, both original assertions, paired errors, real timeout
receipts, complete drains, matched namespace UIDs and unchanged production
source. All sessions are terminal; independent cleanup and git diff --check
checks pass.

The scheduling-log and nested-upload production changes remain pending.
Other test doubles and fresh full-green coverage remain open. No production
source, cluster policy or retained Go tests changed. Nothing was committed,
pushed, deployed or published; the goal remains active.

## Real completion before transient pod-read failures (2026-09-12)

Both RF-12 rows move from pod-lifecycle.feature to the existing live
compatibility feature. Their action, denial counts and exit-zero assertion
are unchanged. No alias or extra case is introduced: 1,048 definitions and
569 scenarios remain, now 508 local and 61 live.

The shared observeInitialReadFailures fixture now waits for an actual BusyBox
/bin/true command to exit. The kubelet must report Succeeded with main exit 0,
nonempty container identity and timestamps, a scheduled node and no restart
history before reads are revoked. Its former UpdateStatus call is removed.
The owned namespace Role no longer grants pods/status, update or patch.

The existing denial/restoration mechanism is retained: actual HTTP 403
responses are forwarded unchanged, and the real Role is restored after one
or two denials. An independent read probe verifies authorization propagation
before the triggering response reaches the runtime. No watch response, error
or process result is supplied by a double. The permanent-denial case remains
API-only, uses the same narrowed Role and keeps its original three-attempt
assertion. No cluster-wide role or policy is created or changed.

Validation:

- Original two reported-completion controls: 2/2 passed, 4.938s.
- Shared live compatibility feature: 7/7 passed, 89.702s, including all five
  previous cases with unchanged feature text.
- Stop after two read failures: both old and new runs pass row 1 and fail
  row 2 at its exit assertion, 5.445s / 25.127s. The error text matches after
  normalizing only the generated namespace; it reports two real API denials.
- Full local tier: 508/508 passed, 185.829s, including permanent denial.
- Native Ginkgo: 3/3 suites passed, 39.846s; go vet passed.

Evidence: /tmp/brine-read-completion.FSfV9Y/evidence.json, checked by
validate.cjs. SHA-256:
20bb8d002381148478017b24200f8ee57063af818566b43592b55e59637d5b22.
Normal adapter: aae83e8532965f11c354dfc996483178dd01a54ce42a683a2cba10e5df6ad323.
The audit verifies identical actions/assertions, the distinguishing mutation,
real completion preceding each denial sequence, all retained local steps,
unchanged prior live cases, complete drains, matched namespace UIDs and
unchanged production process/watch source. All sessions are terminal, owned
namespace/process cleanup checks are empty and git diff --check passes.

Pending production fixes, other test doubles and fresh full-green coverage
remain open. No Go tests were retired; no production code or cluster-wide
policy changed. Work remains uncommitted on core; nothing was pushed,
deployed or published. The goal remains active.

## Real completed-main / failed-sidecar phase probe (2026-09-12)

The remaining SC-09 reported fixture sets phase Running alongside a completed
main and a sidecar waiting in ImagePullBackOff. A temporary real-pod probe
shows that the kubelet instead leaves this combination Pending. Main really
exits 0; a scenario-unique missing BusyBox tag causes the sidecar refusal,
independently witnessed by the actual kubelet Failed pull event. Pod UID,
node, main container identity and termination timestamps are observed.

The unchanged direct compatibility Process ignores the sidecar failure once
main terminates, but podExitCode does not inspect Pending. It therefore fails
the probe at its independent five-second deadline instead of returning main
exit 0. An isolated Go overlay adds Pending to the existing Running/main-exit
branch; the same probe passes with a nil error and exit 0. This is a retained
compatibility contract, not the production task execProcess path.

Baseline: 0/1 passed, 32.734s total; Wait reached 5.006634067s and returned
context deadline exceeded. Candidate: 1/1 passed, 26.238s total; Wait returned
exit 0 without error in 602.619446ms. Both runs show actual Pending/main-exit-0/
sidecar-pull-failure state, complete drains and matched namespace cleanup.
The latest full local run already passed the old reported SC-09 case, which
confirms the masking premise without repeating the full suite.

Evidence: /tmp/brine-late-sidecar-probe.LaCcTv/evidence.json. SHA-256:
05a198ac2b49ef4d37fc80b9cdf569b77818df7b093d5e3de90e80ae2da48fb5.
Probe/production overlays, binaries and both logs are retained there. The
normal adapter remains aae83e8532965f11c354dfc996483178dd01a54ce42a683a2cba10e5df6ad323;
production process.go and the repository startup helper are unchanged.
All jobs are terminal; independent namespace/process checks are empty and
git diff --check passes. No suite scenario or definition changed.

The candidate is neither applied nor broadly verified, and no completed
SC-09 migration or new coverage result is claimed. Applying it requires
scope confirmation alongside the pending nested-upload and scheduling-log
changes. Other doubles and coverage verification remain open. The goal
remains active; nothing was committed, pushed, deployed or published.

## Approved runtime fixes and full-suite checkpoint (2026-09-13)

The user approved the three pending production changes. They are now applied:

- Volume.StreamIn creates its extraction directory before unpacking. The
  destination is a quoted positional shell argument, never interpolated shell
  source. The remaining host executor translates that argument but no longer
  creates directories implicitly. Its exact-command Go assertions are updated.
- Failure diagnostics include PodScheduled even when its reason is empty.
- The retained compatibility podExitCode path reads main completion in Pending
  as well as Running. Main owns the result; an earlier sidecar entry cannot
  supply it. This is not the production task execProcess path.

New pure regression tests cover reasonless successful scheduling and Pending
main exits 0/42 with a sidecar first, plus main-not-finished. They pass with the
fix and fail at the intended assertions against the pre-fix process source.
No fake client is used by those tests. All 98 retained root Ginkgo specs pass
(28.567s execution; 64.329s including build/run). The focused volume group
passes 17/17. No Go tests are retired.

Full Brine execution: local 508/508 passed in 181.445s; live 60/61 passed in
1,043.405s. Combined execution is 20m24.850s. Both previously failing nested
uploads now pass. All 569 recorder drains complete, and all 61 live namespace
creation/removal receipts match by UID. Diagnostic-only production coverage is
1,952/2,466 statements (79.156529%). The normal coverage script correctly exits
unsuccessfully because one assertion failed; the profile was extracted
separately for diagnosis and is not a successful full coverage gate.

The sole failure was RF-08's empty startup-timeout log. The real init remained
Running and main never ran, but Wait returned after 200.570ms without having
received its first pod state, leaving no state to print. An unchanged
instrumented focused control reproduced the empty log in 1/3 runs (45.817s).
Setup polling and runtime execution shared the same rate-limited client.
A fixture-only candidate gives runtime execution its own normal real client,
separating those request budgets without altering the 200ms timeout, API
responses, production code or assertions. It passes 3/3 repeats (40.778s).
This is consistent with setup-client throttling contributing to the flake;
the failed request's underlying transport error was not separately captured.

That fixture-only correction is now applied. All five existing startup cases
pass against the final normal adapter (83.492s). Removing the production
timeout diagnostic still fails all three focused assertions with their exact
empty-log error (41.680s); setup and action steps pass. Final native Ginkgo
passes 3/3 suites (36.881s), and go vet passes. A fresh full coverage run after
this fixture correction remains outstanding.

Three separate real-pod probes pass (87.978s): actual reasonless scheduling,
Pending/main-exit-0 with a kubelet-refused sidecar image, and upload/readback
through a nested path containing spaces, an apostrophe, dollar sign and
semicolon. The latter verifies literal path handling in real BusyBox. These
temporary probes do not add permanent scenarios or definitions.

Evidence: /tmp/brine-approved-fixes.4YEhCb/evidence.json, independently checked
by validate.cjs. SHA-256:
a723fa1609b21a8d1eecbb1a2702ace680aab9e2ab7a6723701827eeaf785570.
The full log/profile are retained under /tmp/brine-coverage.Q9K192.
Final normal adapter:
c151fa6465e15f72df2421e15318cd60b119b1424ae45b14dfbf868da6f758a4.
Its catalog closure matches; inventory remains 1,048 definitions and 569
scenarios (508 local + 61 live). All jobs are terminal, independent owned
namespace/process checks are empty, and git diff --check passes.

Next: obtain a fresh full-green coverage gate, then use the approved fixes to
migrate and consolidate the remaining image-pull diagnostic/metric and SC-09
reported-state cases without dropping assertions. Other host/runtime/status
substitutes remain, including F23 publication, which still needs separate
hostPath authority. This approval did not authorize that policy change.
The broader no-doubles goal is incomplete. Work remains uncommitted on core;
nothing was pushed, deployed or published.

## SC-09 moves to real compatibility completion (2026-09-13)

The remaining late-sidecar case now shares completeLiveCompatibility with
ordinary exits, running services and sidecar-first completion. Its original
exit-zero assertion is unchanged. One scenario moves from local to live:
507 local + 62 live = 569 total; 1,048 definitions remain. The old action and
its Running/status setter are removed. The new action describes the image-pull
failure without accepting an invented image message as a fixture parameter.

The production worker constructs a BusyBox main that actually exits 0 and a
sidecar with a scenario-unique unavailable BusyBox tag. The fixture waits for
the kubelet's actual Pending/main-terminated/current-ImagePullBackOff state,
matching main identity, timestamps and no restart history. A separate kubelet
Failed pull event must match the same pod UID, sidecar and requested image.
Only then does the unchanged direct compatibility Process interpret the state.
Shared pod construction, completion validation, Wait and resource cleanup are
reused; no new standalone probe fixture is installed in the suite.

Validation:
- Original reported-state control: 1/1 passed, 5.266s.
- All eight shared live compatibility cases: 8/8 passed, 109.135s.
- Wrong main exit: old/new fail the original assertion with identical text,
  4.982s / 26.143s.
- Sidecar failure overriding main: old/new fail the same assertion with
  ImagePullBackOff, 5.043s / 19.657s. Actual kubelet text is intentionally
  different from the former invented waiting message; no exact full-message
  equivalence is claimed.
- Ignore Pending completion: the new case fails the exit assertion at its
  independent ten-second Wait deadline, 36.063s total. This guards the real
  defect the former reported Running premise had hidden.
- Native Ginkgo: 3/3 suites passed, 37.017s; go vet passed.

Evidence: /tmp/brine-sc09-live.6E0cIY/evidence.json, independently checked by
validate.cjs. SHA-256:
c442911a0b75334f25cc691e70345972307a3be23119085191bfcdf9dcfc4d96.
Normal adapter:
61e394725e5dfecf5a4f1e08c20cbe8e22293468a2817f7854331eb92cc2a9da.
The audit verifies matching original assertions, unchanged other definitions,
unchanged seven prior live case texts, complete drains, matched namespace UIDs,
and byte-identical production process.go. No Go tests were retired.
Focused jobs are terminal and owned namespace cleanup is empty.

A fresh full two-tier coverage run is now in progress; its outer log is
/tmp/brine-sc09-live.6E0cIY/coverage.log. Do not interpret the earlier
79.156529% diagnostic result as a successful current coverage gate. Other
reported-state and host/runtime substitutes remain. Image-pull diagnostic/
metric consolidation is next; F23 publication still needs separate hostPath
authority. Nothing was committed, pushed, deployed or published.

## Full v5 coverage gate passes (2026-09-13)

The unchanged source from the SC-09 checkpoint now passes the full coverage
script, exit status 0. The required local manifest passes 507/507 cases in
179.681s; the required live manifest passes 62/62 in 1,068.982s. Combined
scenario time is 20m48.663s, excluding builds/reporting. No scenario failed,
and neither tier is empty or omitted.

Brine-only production coverage is 1,950/2,466 statements (79.075426%), above
the exact 50% gate. The denominator includes only atc/worker/jetbridge, not
adapter code or native Go test runs. This supersedes the earlier failed-run
79.156529% diagnostic result. Both nested uploads, the 200ms timeout diagnostic
and actual Pending/main-completed/sidecar-pull-failure case pass together.

Independent validation checks all 569 scenario ends and complete recorder
drains, matching creation/removal UIDs for all 62 owned live namespaces,
the coverage profile and gate output, and restoration of the normal adapter.
No owned namespace or recent Brine/API-server/etcd/collector process remains;
all sessions are terminal and git diff --check passes.

Evidence: /tmp/brine-coverage.ydG0c5/evidence.json, checked by validate.cjs.
SHA-256:
eaa70b2dbbf73fcf4de8a3813344c516ede28586d8753bb65dac42a78c280779.
Outer gate log: /tmp/brine-sc09-live.6E0cIY/coverage.log.
Normal adapter:
61e394725e5dfecf5a4f1e08c20cbe8e22293468a2817f7854331eb92cc2a9da.
Inventory is still 569 scenarios (507 local + 62 live), 1,048 definitions.

During the full run, the next image-pull consolidation was prepared entirely
in temporary overlays, without changing its source inputs or running competing
live probes. Four original diagnostic/metric controls pass (5.990s). All five
baseline mutations fail their intended assertions: remove scheduling details,
remove image identity, omit the exact counter increment, change the failure
reason, and misidentify the production execution path. Setup/action steps
pass and all local disposer drains complete.

The compiled candidate reuses the existing real startup helper for two modes,
adds an exact requested-image assertion, and retains scheduling, failure,
counter and execution-path assertions. Its catalog still has 1,048 definitions.
It remains unapplied and has not run live. If validated, it would replace four
local cases with two live cases (503 local + 64 live = 567 total).
Prepared evidence and source/binary hashes:
/tmp/brine-pull-consolidation.JNgeAd/prepared.json. SHA-256:
1e9d0d8823a6e4f4c3ae4fd61b1343608db569a0b1fb28b86096fce1566e1d41.

The 50% coverage requirement is met; the complete goal is not. Remaining
reported-state, host-execution, partial-output put and resolver-panic doubles
must still be removed or resolved without weakening their contracts. F23
publication continues to need separate hostPath authority. Nothing was
committed, pushed, deployed or published.

## Image-pull diagnostics and metrics consolidated on real pods (2026-09-13)

Four reported-state cases now share two real image-pull cases, one each for
production execProcess and direct compatibility Process. Both use the existing
real startup-refusal helper. Their main image is a scenario-unique unavailable
BusyBox tag; the helper checks the constructed pod's requested image, actual
Pending/current-waiting/no-restart state, successful scheduling, and a matching
kubelet Failed pull event before returning the runtime result.

The consolidated cases retain all original consumer guarantees: failure names
ImagePullBackOff, the build log names the exact requested image and successful
PodScheduled condition, the image-pull counter increases by exactly one, and
the error comes from the requested execution path. The destructive counter is
reset before Wait and read once after it. The image assertion reads the real
failed pod's main-container spec; it does not use an invented waiting message.

An initial candidate passed both live controls but added RequestedImage to the
shared ProcessOutcome, needlessly changing unrelated Brine requirements. It
was not applied. The final candidate derives the expected image from the API
and leaves every unrelated step contract exactly unchanged. Two old actions
and their pod-status setters are removed; one shared action and one assertion
replace them. No alias or net definition is added.

Validation:
- Four original controls: 4/4 passed, 5.990s.
- Final two-case live control: 2/2 passed, 59.841s.
- All seven live startup cases after application: 7/7 passed, 138.306s,
  including the four previous classifier rows and the unchanged 200ms timeout.
- Full retained local tier: 503/503 passed, 184.084s. Every remaining scenario's
  ordered steps match the previous full run; only the four consolidated rows
  are removed.
- Native Ginkgo: 3/3 suites passed, 40.684s; go vet passed.

All five paired mutations fail the intended consumer assertions after setup
and action steps pass. Remove scheduling: old 2/4 fail, new 2/2 fail; remove
image identity: old 2/4, new 2/2; omit the counter: old 2/4, new 2/2; change
failure reason: old 4/4, new 2/2; misidentify production execution: old 2/4,
new 1/2, with the compatibility row remaining green. Counter and execution-path
errors match exactly. Other errors contain real pod/kubelet detail instead of
the old invented message, so no full-text equivalence is claimed.

Evidence: /tmp/brine-pull-consolidation.JNgeAd/evidence.json, independently
checked by validate.cjs. SHA-256:
10811cae4e9e7a4feef52cbc4a50ca372d42db7111c8ee6c8b61bc04a0a077de.
Normal adapter:
5cf548e4323122c0720c13d2d1c8c723158458b59a8bb9d94b1ad57914d675b1.
The catalog closure matches. Inventory is 567 scenarios (503 local + 64 live),
down two, with 1,048 definitions unchanged. The audit checks all retained local
steps, unchanged prior startup steps and unrelated contracts, complete drains,
matched namespace UIDs, and byte-identical production process.go. All jobs
are terminal, independent namespace/process cleanup is empty, and git diff
--check passes. No Go tests were retired.

The last full coverage gate, immediately before this test-only consolidation,
passed 569/569 at 1,950/2,466 production statements (79.075426%). This increment
does not claim a new full-suite coverage measurement. The broader goal remains
incomplete: sidecar-startup and other reported-state fixtures, host execution,
partial-output put and the injected resolver panic remain. F23 publication
still needs separate hostPath authority. No production code or cluster-wide
policy changed; nothing was committed, pushed, deployed or published.

## Failure-only tracing shares the real missing-input case (2026-09-13)

The existing real missing-input scenario now also requires exactly one
init.container.failed event and zero init.container.completed events on the
production wait-for-running span. It still checks the actual failed init's
name, exit, logs, retained pod identity and absence of task execution first.
The real OTLP collector supplies the events; no trace or pod status is seeded.

The existing event-count assertion now takes an integer instead of baking
"once" into its pattern. Both former callers retain count 1. The same definition
serves the new absence check, and still requires the named span to exist even
when the expected count is zero. Every other catalog contract is unchanged.
There are still 567 scenarios (503 local + 64 live) and 1,048 definitions; no
scenario, alias or net definition was added. A stale duplicate inventory
declaration was removed from README.

This does not remove the remaining reported-state OE-06 case. That case
requires repeated failed-init observations in one Wait, whereas the real
missing-input case observes a terminal failure once. Production explicitly
refuses to recreate a pod whose init failed. No faithful real replacement for
the repeated-observation premise was established in this increment, and its
existing assertion was not weakened or retired.

Validation:
- Preserved original reported-state control: 1/1 passed, 5.351s.
- Both real startup/failed-input controls: 2/2 passed, 36.011s.
- Entire local tier: 503/503 passed, 183.756s.
- Native Ginkgo: 3 suites passed, 38.523s; go vet passed.
- Remove failed-init deduplication: old and new fail with exactly two events,
  5.444s / 5.533s. Error text differs only in the generalized count wording.
- Omit the failure event: real failed-input assertion detects zero events;
  unrelated startup control stays green (36.320s).
- Add a false completion event alongside failure: the new zero-count assertion
  detects it; unrelated startup control stays green (37.411s).
- Remove scheduling deduplication: the original successful-startup contract
  detects three actual scheduled observations; failed-input stays green
  (36.229s). The count is observed, not assumed to be exactly two.

Evidence: /tmp/brine-init-trace.CZnhud/evidence.json, checked by validate.cjs.
SHA-256: 69b2abcb0b1151d295765a0c7709172faf47cfd2c6f8758c3ff7f855654b54fa.
Normal adapter:
41431a538b4cdacaf04bf3e23002f969e997f40cc2b9a0dafd3afda89892240f.
The catalog closure matches. The audit verifies all controls, intended
assertion failures after successful setup/actions, complete resource drains,
all eight live namespace create/remove UID pairs, retained local OE-06 steps,
and byte-identical production process.go. All test/build jobs are terminal.

The last full coverage gate remains the earlier 569/569 run at 79.075426%;
this scoped test-only increment does not claim a fresh full-suite measurement.
Host execution, other reported-state fixtures, partial-output put and resolver
panic substitution remain. F23 publication still needs separate hostPath
authority. No production code changed and no Go tests were retired. Nothing
was committed, pushed, deployed or published.

## Approved live artifact-storage prerequisite (2026-09-13)

The user approved temporary hostPath storage and a security-policy exception
limited to the owned test namespace for the five artifact-handoff cases. The
goal is active again. Those cases are not migrated yet: the storage prerequisite
below is verified, while production-daemon execution and preservation of all
five existing outcomes remain required.

Added live_artifact_store.go and one opt-in, live-tagged native prerequisite
check. A real BusyBox anchor owns a size-limited emptyDir. The test mounts its
exact kubelet-owned storage directory through hostPath into a writer and an
independent observer. A matching UID marker is read both through the data mount
and through a read-only mount of the anchor's own pod directory. HostPath type
Directory refuses an incorrect assumed kubelet path rather than creating it.

The writer produces actual bytes, and production Volume.StreamOut returns
them through gzip together with the ownership marker. After the writer's
UID-preconditioned deletion and actual API absence, the independent observer
still reads the same bytes. No status, executor result, artifact reply or tar
payload is fabricated.

Cleanup deletes the anchor, observes the actual storage path disappear through
the parent-directory mount, then deletes the observer and namespace. Looking
only through the data bind mount would be insufficient: that mount can remain
accessible after the directory is removed. No broad kubelet root, other pod's
directory, or existing artifact store is mounted.

The fixture requires BRINE_ALLOW_HOSTPATH_TESTS=1 before creating any resources.
Only a new, UID-verified namespace receives the admission exception. Pods have
read-only root filesystems, no service-account credentials, no added
capabilities or privilege escalation, and bounded resources. This increment
creates no daemon, host port, host-network pod, node label, or RBAC grant.
The default kubelet root /var/lib/kubelet was verified against actual ownership
markers on theborg; BRINE_KUBELET_ROOT permits an explicit alternate root.

Final-source validation:
- Storage control: 1/1 passed, 20.07s.
- Omit anchor deletion: the ten-second storage-cleanup assertion fails as
  intended, 30.48s overall. Independent anchor removal and recorder cleanup
  then verify storage reclamation and namespace removal even on this failed run.
  This challenges the fixture cleanup guard, not a production-code mutation.
- Missing opt-in: fails with the explicit approval requirement before any
  namespace is created.
- Native Ginkgo: all three suites passed, 52.914s.
- Default and live-tagged vet passed.
- Rebuilt normal adapter: 0b3d93c6d088e44997208abaed63d1b06bc82996e0aa1a41fad716825081e26b.
  Its closure matches; all 1,048 step contracts and resource declarations are
  byte-equivalent to the prior catalog. Inventory remains 567 Brine scenarios
  (503 local + 64 live). No Brine case or definition was added or retired.

Evidence: /tmp/brine-live-handoff.P8npld/evidence.json, checked by validate.cjs.
SHA-256: 602e6cf5f0b7cd67359b4b14e59bd2927c23ef7aa492ab524e35ea19e049f039.
It records source hashes, actual anchor/writer/observer UIDs, matching storage
and namespace cleanup receipts for both control and challenged runs, opt-in
refusal, native results, and unchanged catalog contracts. All jobs are terminal;
independent live-namespace and process checks are empty, and diff --check passes.

Next: use this owned storage for the production daemon and real producer and
consumer runtime paths, retaining the raw/gzip/S2 byte checks, direct returned
volume read, post-producer-deletion read, write-refusal collision and daemon
outage outcomes. The five current host-executed cases remain untouched until
their faithful replacements and mutation checks pass. No new full Brine
coverage result is claimed; the last full gate remains 79.075426%.
No production source changed. Nothing was committed, pushed or published.

## Real production-daemon prerequisite (2026-09-13)

Extended the same opt-in native storage probe; no Brine scenarios, definitions
or native test cases were added or retired in this increment. The five existing
host-executed handoff cases remain unchanged.

The current static production artifact-daemon binary runs as PID 1 in an owned
BusyBox pod on the anchor's actual node. Its 76,984,504 bytes are compressed to
22,052,832 bytes for SPDY upload only; real BusyBox decompresses the executable
and its SHA-256 must match before startup. ELF architecture must match the
actual node. An in-pod health check precedes the localhost-only port-forward,
and the probe verifies the actual PID 1 executable. No hostPort, hostNetwork,
node label, RBAC grant, mirroring, or existing daemon configuration is changed.
The pod has no Kubernetes credentials. Storage remains limited to the approved
owned path and namespace exception; the namespace pod quota is now four.

After writer deletion and an independent observer read, production
DaemonSetVolume reads the exact artifact bytes and ownership marker from this
real daemon. The probe then verifies daemon deletion, refusal of new loopback
connections, actual kubelet storage reclamation and namespace removal. Created
pod UIDs are retained on readiness failure; actual pod status/events are read
before cleanup. This does not exercise production's node-IP resolution,
RecordOutputs registration, consumer input-init, or complete worker handoff.

Validation on final source:

- Controls passed in 25.17s and 23.43s.
- Production-only overlay changing the direct-IP artifact URL to
  /artifacts/missing/... failed the post-writer-deletion daemon read as intended
  (86.03s); recorder cleanup reclaimed storage and removed the namespace.
- Fixture-only overlay omitting anchor deletion failed the explicit storage
  cleanup assertion (63.69s); independent removal and recorder cleanup recovered.
- The old-probe-body comparison with the URL mutation is inconclusive: it
  failed its ten-second cleanup bound (35.82s), then verified reclamation during
  recovery. It is not counted as a surviving mutation or a passing comparison.
- Three native Ginkgo suites passed in 48.540s; default and live-tagged vet passed.
- Missing hostPath opt-in failed before namespace creation.
- Rebuilt adapter d593a3256ba2ee5654c01b1a880f9dc8ecfd40087b85ddb73db7119e6072717c
  has a matching closure. All 1,048 step contracts and resource declarations
  match the prior catalog; inventory remains 567 scenarios (503 local + 64 live).
- Independent final checks found no owned test namespaces, no matching active
  test processes younger than two hours, and connection refusal on all four
  recorded validation tunnel ports. Older unrelated processes were not modified.

Three development controls also failed and are excluded from passing evidence:
observer startup/namespace-cleanup deadlines (the namespace was later confirmed
absent); opening a tunnel before the daemon bound its port; and writer startup
exhausting the setup deadline. The latter two verified cleanup. In-pod readiness
fixed the observed tunnel race; compressed upload reduced transfer size. These
changes do not establish a cause for the unrelated startup or reclamation delays.
The setup limit remains three minutes and explicit storage cleanup ten seconds;
the eight-minute outer limit permits bounded diagnostics and recovery cleanup.

Evidence: /tmp/brine-live-daemon.c6zTnX/evidence.json, checked by validate.cjs.
SHA-256: da978dd538a366e4cb89a702afc057a80a0a5420c82f11a0f3b730b3b3240305.
The evidence records source/binary hashes, real pod/namespace UIDs and storage
paths, intended failures, cleanup receipts, unchanged catalog and comparison
limitations. Production volume_daemonset.go matches its pre-increment snapshot.
No production source changed in this increment and no full Brine run was added;
the historical full-suite production coverage result remains 79.075426%.

Next requires approval to bind one temporary high TCP hostPort on theborg's
InternalIP for these five cases, removing it during cleanup and leaving existing
port 7780 untouched. That approval was requested but has not been received.
Do not substitute the localhost tunnel for production's node-IP/input-init path.
After approval, preserve every existing raw/gzip/S2, persistence, exact-files,
write-collision and daemon-outage outcome before retiring the five host cases.
The goal remains active and incomplete. Nothing was committed, pushed or published.

## Approved real node-port handoff migration (2026-09-14)

The user approved one temporary high TCP host port on theborg for the five
handoff cases. The goal is active again. This increment selects 49179 on the
actual InternalIP 192.168.1.133; it does not bind the existing node port 7780.
Both hostPath and hostPort opt-ins are required. A missing hostPort opt-in
was verified to fail before namespace creation.

The daemon fixture now uses the real node endpoint instead of a localhost
port-forward. It rejects a pre-existing Kubernetes port allocation or listening
TCP endpoint, checks health from an independent real pod, and verifies that
UID-preconditioned daemon deletion closes the node port. No host networking,
node labels, cluster-wide policies or RBAC grants are changed. The support
pods remain confined to the owned storage path and namespace. The daemon's
ephemeral-storage request/limit is 96Mi, enough for the verified 76,984,504-byte
static executable while allowing a task within the existing namespace quota.
The single-node, existing artifact-cache=ready premise is checked explicitly;
production task pods use the actual scheduler and their generated pod specs.

The existing five outline rows moved into live/artifact-handoff.feature.
There are still 567 scenarios and 1,048 definitions, now 498 local + 69 live.
The When consumes the shared LiveTaskPlan, not ArtifactCluster/task-workspace.
The host executor, supplied scheduler binding, supplied Running status,
host-path translation and host filesystem writes are removed from handoff.
The actual PostgreSQL team/worker and production Worker create the tasks;
SPDY executes them. The production worker records output locations and wraps
the returned output as an artifact served after the producer pod is deleted.

All five rows preserve the direct returned-output gzip checkpoint, exact
requested file, manifest.txt, output.txt=hello-from-the-step and decoy exclusion.
Raw/gzip/S2 retain the independent codec check. The consumer additionally
requires an actual successful fetch-inputs init and exact delivered files
before StreamIn. Those files are then cleared through the actual pod mount,
so init delivery cannot mask a broken public StreamIn. The consumer's actual
task reads the final files through its own mount.

The real BusyBox tar collision returns exit 1; production StreamIn passes nil
stderr to SPDY, so it does not return GNU tar's host diagnostic. The replacement
requires the real typed exit, unchanged collision contents and exact partial
extraction of the other archive members. It cannot pass on a generic exec
failure or a command that never extracted anything. The outage row checks
the actual producer node name plus the any-peer error, not a fabricated node-1.

Initial node-route native control: passed in 24.60s, including storage and
node-port cleanup. Original five-case baseline: 5/5 passed; root run 34.978s,
including tag-skipped live manifest 39.447s. The final live control v3 passed
all five rows. Development v1 failed because the fresh database had no main
team; setup now creates the team through the real API. Development v2 passed
four rows and rejected the unavailable-stderr assumption in the collision
check; the filesystem-and-exit check above replaced it. Those failed runs are
not counted as passing controls.

Final verification:

| Production-only mutation | Original five cases | Real five cases |
| --- | --- | --- |
| Consume input without extracting it | Transfer rows 1–4 fail; outage passes | Same rows and intended checks |
| Return output.txt without the other artifact members | All five fail the returned-output checkpoint | Same checkpoint fails in all five |
| Suppress the real StreamIn error | Only write-refusal row fails | Same row fails on the missing real error |
| Skip production fetch-inputs work | All five pass: host fixture never ran init | Rows 1–4 fail exact pre-StreamIn delivery; outage passes |

The accepted incomplete-output live comparison is the unchanged repeat.
Its first run reached the intended assertions but row 3 exceeded cleanup
limits (180.345s drain, partial=true); it is excluded from clean evidence.
The later namespace check was empty. A separate temporary, read-only probe
then verified that the exact old storage directory for anchor UID
40cda470-1611-41cc-b2a3-9f8c0aa3394f was absent: kubelet refused its Directory
hostPath mount. That 15.27s probe and its own cleanup passed. It introduced
no repository test case and could not create or write the old directory.
The cause of the cleanup delay remains unresolved; no timeout was relaxed,
no failure was suppressed, and no runtime source changed for the repeat.

Final live control: 5/5, 234.540s. That observed run overlapped
isolated mutant compilation; it is not an uncontended runtime benchmark.
All 498 local cases passed in 151.475s (plus 5.807s for the tag-skipped live
manifest). Native Ginkgo: all three suites passed in 39.786s. Default and
live-tagged vet passed. Every accepted live run has five complete recorder
drains, matching created/removed namespace UIDs and exact storage-reclamation
receipts. Final independent checks found no owned namespaces or recent test
processes, and node port 49179 refused connections.

Catalog resources and all unrelated step contracts are unchanged; only the
two handoff contracts changed. The normal adapter closure matches binary
7fd36f24fbe340fe3e92c1e3c5d495fb280d585c8a78f1c47b84925c0d438150.
Production volume.go and storage_daemonset.go match their pre-mutation snapshots.

Evidence: /tmp/brine-node-handoff.JVEY7l/evidence.json, checked by validate.cjs.
SHA-256: 02d12d3871d1de2b9a98f79c2973ff07f6153d4c67dbb118228ff3e5a0c990a6.
The evidence preserves the excluded timeout, independent old-path absence
check, exact per-row old/new outcomes, cleanup receipts and source hashes.
No fresh full-suite coverage claim is made: the historical gate remains
1,950/2,466 production statements (79.075426%).

Production source is unchanged. No Go tests were retired. The broader
no-doubles work remains incomplete: other host/status fixtures, partial-output
put and resolver panic remain. F23 storage authority is separate from this
five-case approval. No commit, push, deployment or image publication was made.
## Repeated-OOM terminal-history feasibility probe (2026-09-14)

A temporary Go overlay tested RF-10's exact terminal-state premise without
editing repository tests or writing Kubernetes status. It used one owned,
non-root BusyBox pod, a 512Mi request/limit inside the unchanged namespace
quota, no service-account credentials, an emptyDir arming gate and the existing
UID-checked observation finalizer. The actual cgroup limit was independently
read as 536870912 before arming and checked by each restarted process.
No hostPath, host port, node changes or additional RBAC were used.

Three distinct container IDs were observed genuinely OOMKilled with exit 137,
at restart counts 0, 1 and 2. The workload wrote its termination-file message
before allocation; the message was task-authored data transported by kubelet,
not a fabricated status or claimed kernel diagnostic. After the third death,
UID-preconditioned deletion with a one-second grace period retained the pod's
API object behind the owned finalizer. Kubelet reported Failed, current
OOMKilled/137, restart count 2 and the exact message, but cleared
LastTerminationState.Terminated. Thus this route did NOT preserve the old
simultaneous current-and-previous OOM assertion. It is not a passing replacement
for either RF-10 row, nor proof that every possible real route is impossible.

The probe failed its explicit fidelity assertion in 56.42s. Its finalizer and
namespace cleanup completed with matching UIDs. Namespace brine-runtime-bbj8p
UID cecaeea4-9ac3-4faf-9260-08bb8cd1874c and pod repeated-oom
UID b550198d-0231-4347-9676-76e096f1681b were removed. No scenario was added,
retired or weakened; production and adapter source were unchanged. Counts remain
567 scenarios (498 local + 69 live) and 1,048 definitions. This is a contract
fidelity result, not a denied permission. Production execProcess also has a
separate failed-pause-pod replacement behavior; a compatibility probe cannot
stand in for that production row.

Probe source and Go overlay: /tmp/brine-repeated-oom.DSZxG6/.
Raw result: probe.log (SHA-256: ea4e751a850819cfbeac1bac6ae4be93329fd38e60d73f08d4a81974ae995a41).

## Failure assertion vocabulary consolidated (2026-09-14)

Removed the duplicate StepOutcome assertion "the failure explains {string}";
its three feature uses now say "the step fails naming {string}". Both required
a non-nil Err and matched a substring of Message. The canonical definition is
byte-for-byte unchanged. Scenario text differs only in that phrase: all expected
values and all 567 cases remain (498 local + 69 live). Definitions fall from
1,048 to 1,047. No production code, timeout or failure-state fixture changed.

A temporary real-Brine-pipeline probe compared both original definitions over
six states: matching substring, missing substring, nil error, empty message,
case mismatch and a late substring beyond diagnostic truncation. All before/after
verdicts agree. Isolated assertion mutations reading Stderr instead of Message
and removing the nil-error guard each fail the intended probe. The probe adds
no repository scenario; its explicit StepOutcome values test a pure assertion,
not a replacement worker or Kubernetes failure source.

Verification: all 498 local cases passed in 260.083s; both selected live
exec-interruption cases passed in 139.643s; all three native Ginkgo suites passed
in 75.864s. The initial overlapping native run hit two 30s document-selection
timeouts, and the initial live OOM run timed out before its stdout checkpoint
while Pending. They are preserved as failures, not accepted controls. Unchanged
isolated repeats passed without extending deadlines. The OOM repeat's actual
kubelet events showed approximately 54s startup, then main OOMKilled/137 with
the sidecar Running; this does not establish the first timeout's root cause.

The native timeouts orphaned four local envtest processes belonging to the unique
adapter build /tmp/jetbridge-adapter-contract.651526819/adapter. Ownership was
checked through their exact executable and BRINE_ADAPTER_BINARY environment.
SIGTERM stopped both etcd processes; the two API servers required SIGKILL after
graceful shutdown did not finish. This reveals remaining native-test timeout
cleanup work; it is not claimed fixed. Final checks found no owned namespaces
or active recent verification processes. No repository data was deleted.

Evidence: /tmp/brine-failure-vocabulary.KGYgmv/evidence.json, checked by verify.cjs.
SHA-256: 2ec37329fa64f67099ab796ce398b4c084981d387cf4b0a611d4c4250e3985e8.
The goal remains active. Expanded storage authority for the other three flows
is still pending; this increment used none of it. No fresh full-coverage claim,
commit or push was made.

## Native adapter deadline requests the real drain (2026-09-14)

The earlier document-selection timeouts used exec.CommandContext's default
immediate kill. That bypassed Brine's installed SIGTERM drain and orphaned
envtest servers, which start in separate process groups. Ordinary native
invocations now share drainingCommand with the existing held-resource test:
SIGTERM first, then a bounded 45s WaitDelay before forced exit. The ordinary
30s execution deadline is unchanged and still fails the test. Timeout failures
now retain bounded stdout/stderr and the cleanup wait result.

The existing hold test now cancels its context after hold_ready, exercising
the same cancellation hook as ordinary invocations. It still requires the
real registrar's complete disposer events and controlled exit 143; a deadline
expiration cannot pass as the intended context cancellation. It additionally
requires nonempty owned envtest working directories before cancellation and
their removal afterward. Its scratch-directory setup is shared with the engine
release test. No Brine case or definition was added, and no production or
adapter-runtime source changed.

Verification: all three native suites passed in 78.807s; go vet ./... passed.
An isolated test-only mutation restoring immediate killing failed with
held=true, drained=false, signal: killed and context canceled. The mutation
runner's initial cleanup stopped on an unexpected but uniquely marked collector.
Recovery cleanup was run without repeating the mutation; its surviving API
server required SIGKILL. Its post-cleanup process-kind expectation also failed
because etcd had already exited, although remaining was empty. These cleanup
runner failures are retained rather than counted as passing checks. Final
independent marker and PID checks found no active mutation processes.

A temporary overlay also tested the original startup timing: it observed an
actual owned kube-apiserver process during suite-resource acquisition, cancelled
before any feature started, required exit 143, and verified removal of the owned
directories. That probe passed in 4.82s; the containing native suite passed in
32.627s. Final independent checks found no active startup-probe processes.
It adds no permanent test scenario. The normal adapter binary is unchanged.

This fixes the bypass of graceful cleanup, not every possible teardown failure:
a drain that exceeds the grace period can still be forcibly interrupted, and
partial resource-factory startup remains a separate failure boundary.
Evidence: /tmp/brine-native-drain.bBB205/evidence.json.
SHA-256: 1912dbeb7045dbb179239ab5a33b23259630db326ab7d6e7bbe264530ae1c215.
The full migration goal remains active; other doubles and the pending storage
scope are unchanged. No commit, push, deployment or live-cluster change was made.

## Approved boundaries and real partial-output put (2026-09-14)

The user's "Allowed on both" approves two specifically bounded scopes:

- Reuse temporary owned hostPath storage, the owned namespace's pod-security
  exception and an unused high TCP port on theborg for persisted-input integration,
  task-output-to-put integration and F23 severed-exec publication. Verify cleanup;
  do not touch existing port 7780 or node, cluster or RBAC configuration. These
  three migrations remain pending; this increment exercised none of that storage.
- Retain explicit, labelled fault injection for valid version JSON followed by
  actual exit 4, and for resolver-panic recovery. This is not permission to replace
  ordinary workers, pools, executors, resolver results or lifecycle state.

The partial-output put now runs through the production database-backed pool and
factory, worker, owned BusyBox pod and SPDY exec. Its intentionally installed
resource executable verifies the actual request bytes, writes valid version JSON
and exits 4. An independent real-exec preflight verifies that fault, and its
receipts are removed before the production put runs. The production run must
leave fresh request/argument/reply receipts and an actual runtime-written exit-4
annotation on the original pod. No cached completion is installed by setup.
The same original three assertions read step outcome, build events and publication
from real production state.

Removed execStepPool, execResourceStub, the runtimetest worker/container/process
setup and its now-unused fields and guards. The original scenario moved unchanged
to features/live/partial-put.feature: 567 total scenarios, now 497 local + 70 live,
and 1,047 definitions. No extra scenario or vocabulary alias was introduced.
The resolver panic injector is now documented as an explicitly approved exception;
ordinary resolution and authentication still use the real resolver.

Before/after validation used isolated build overlays, leaving both production
source files unchanged:

- Old control: 1/1 passed, 5.716s. Real-pod control: 1/1 passed, 18.077s.
- Removing the put-step nonzero-exit guard: old and new both fail the original
  failed-rather-than-errored check because the step incorrectly succeeds.
- Removing only the resource's nonzero-exit decode guard: old and new both pass;
  the put guard still prevents publication.
- Removing both guards: old and new both fail the original first check.
- Since execution stops at a failed assertion, temporary diagnostic documents
  independently exercised the other original checks, with their definitions
  unchanged. Removing the put guard makes both finish-event checks report
  expected exit 4, got 0. Removing both guards makes both publication checks
  find the incorrectly published resource version v3. These diagnostic documents
  add no repository cases and do not weaken the retained scenario.

The first live setup failed because the initially chosen image lacked jq; its
owned namespace was removed. That failed run is retained, not counted as a
control. The accepted BusyBox implementation needs no jq. All accepted controls,
mutations and diagnostic probes have complete recorder drains and matching owned
namespace creation/removal receipts.

Broader verification: all 497 local cases passed in 157.175s (plus 4.998s for the
all-skipped live manifest); all three native suites passed in 39.107s; default
and live-tag vet passed. Vocabulary guards passed. Final independent checks found
no owned live namespaces or active recent verification processes. No production
source change, commit, push, deployment or image publication occurred.

Evidence: /tmp/brine-partial-put.P58D3F/evidence.json, checked by verify.cjs.
SHA-256: 515b36f95279f92d84f76809a0fa4f31e7ae5a0c75e46e7c59adbfea9f1e11d2.
Normal adapter SHA-256:
cb32da93df102e91c3ab1039d93d2b75723e737629bc535007e10ace6a5c3692.

The goal remains active. No fresh full-suite coverage measurement is claimed;
79.075426% (1950/2466) remains historical evidence, not current-tree coverage.
Next: the now-authorized real mid-write F23 interruption/publication flow, then
the two remaining artifact integrations. Reported lifecycle fixtures still need
faithful real-state replacements; node-level disruption is not authorized here.

## F23: real mid-write disconnect without artifact publication (2026-09-14)

Previous goal turn: progress, not a blocked or unchanged-state turn. Its
partial-output put replacement and old/new mutation evidence were complete.
This increment uses the newly approved owned-storage scope for F23 only.

The original severed-exec scenario moved to live/severed-artifact.feature and
reuses the existing "a task using Kubernetes" Given. Its action now creates
real production workers/containers against the real database and live Kubernetes,
with the shared owned hostPath store and current production artifact daemon.
Only the scenario-owned transparent TLS route is closed. No executor result,
EOF, Running status, artifact location or exit status is supplied by the test.
Removed the old API status update and the last localExecutor.failure field and
error-injection branch.

The fixture first establishes that publication works: a real task finishes,
its actual returned volume handle is recorded, its pod is deleted, and its exact
output is read through the node daemon. It then runs the writing task, waits
for actual stdout, cuts the exec connection and waits for the production
runtime to return. Both subsequent file-growth reads occur strictly after Wait
returns, through the separate storage observer. A fresh direct exec verifies
that the task's PID is still alive and the same kubelet-owned pod/main container
is Running. Writes are stopped only after that premise is proved.

The original failure and locator-absence assertions remain. The latter uses
the actual returned output handle and additionally requires the reachable real
daemon to refuse the unregistered artifact through ArtifactFromVolume/StreamOut.
An offline daemon or a successful artifact read cannot satisfy that check.
The positive publication prerequisite prevents a disabled/misconfigured
publication path from making the negative check pass.

Old control: 1/1 passed in 5.128s. Final live control: 1/1 passed in 34.094s.
The original two regressions were applied only through temporary production
build overlays, with process.go unchanged on disk:

- Publishing output despite the transport error fails the original location
  assertion in both old and new tests. The final live run fails at that intended
  assertion, not at fixture setup, and still proves continued writing.
- Swallowing the transport error and returning success fails the original
  failure assertion in both old and new tests. The final live run likewise
  reaches that assertion with its still-running writer premise satisfied.

The first live control and mutation pair also completed, but their growth
interval began before Wait returned. Review tightened that ordering; these
initial logs are retained but excluded from final post-Wait fidelity evidence.
The rebuilt final control and both live mutations all prove growth with both
observations after Wait. The final control observed 5 to 8 lines; the publication
mutant 16 to 19; the swallowed-error mutant 15 to 17.

Every live run has a complete recorder drain, a matching owned namespace
creation/removal receipt, verified kubelet reclamation of its exact anchor
storage directory, and verified closure of its temporary node TCP port 49179.
The daemon was built from current source, uploaded only to owned pods and
checksum-verified there. Existing port 7780 and node/cluster/RBAC settings were
not changed. Final independent checks found no owned namespaces or recent
active verification processes.

Inventory remains 567 cases and 1,047 definitions, now 496 local + 71 live.
All 496 local cases passed in 149.829s, plus 4.664s for the skipped live manifest.
All three native suites passed in 37.392s, including vocabulary guards.
Default and live-tag vet passed. No production code or original Go test changed;
no commit, push, deployment or image publication was made.

Evidence: /tmp/brine-f23-live.EDbpwb/evidence.json, checked by verify.cjs.
SHA-256: 0b7b3c5f9e21722387c2dda887140fdd0d50e446c931949993961ce367d384fa.
Normal adapter SHA-256:
d7a87fab6976bb68e41a707ca41fcc4083a5cab85f3c28e6aeb02dfc4b3e4d7b.

The goal remains active. No fresh full-suite coverage measurement is claimed.
Next are the two authorized host integrations: persisted input consumption and
task-output-to-put publication. The latter's current resource fixture merely
echoes a non-JSON request; its replacement needs real resource protocol and
actual artifact consumption, not another echo executable. Reported lifecycle
and observability fixtures remain separate fidelity work.

## Artifact integrations use real tasks, discovery and resource protocol (2026-09-14)

Previous goal turn: verified progress on F23. This increment migrates the last
two host-command artifact integrations under the approved owned hostPath,
owned-namespace pod-security exception and temporary high-port scope.

Both original scenario names and their mount, exit-status and container-row
checks remain. They now share the existing task-using-Kubernetes Given and a
live setup. The persisted-input case uploads actual bytes through
CreateVolumeForArtifact/StreamIn, looks the volume up anew through the real
database, and requires production fetch-inputs to deliver those bytes before
a real task reads them. The prior command only echoed "artifact data received";
the new output comes from the artifact file.

The publication case builds a real Git commit from the persisted input, records
the task's created container row, deletes the producer pod, and lets the
unmodified pinned Git resource consume the output via production fetch-init.
The resource's actual JSON reply, published commit and committed file contents
are compared. Its source is a real bare repository inside the owned put pod,
using file://. This does not claim external S3 or network Git publication:
the old incidental s3 type used an installed shell that merely echoed a
non-JSON request. The generic resource-protocol assertion is now meaningful,
and the task-output handoff is real.

Removed installed echo scripts, the host integration workspace, host-execution
and request-echo actions, the unused named-artifact setup phrase, and runStep's
host execution/reported-Running branch. API-only runStep now only observes pod
construction. The local executor's unused API-client/host-mount translation
branch is gone; its remaining scenario consumer is failed-init observability.
Seven obsolete definitions were replaced by three behavior-level definitions:
1,047 -> 1,043. The same 567 cases remain, now 494 local + 73 live.

Shared fixture correction: after proving the daemon listener through the actual
node IP, setup now creates an owned headless Service and EndpointSlice tied to
the real daemon pod UID and verified node endpoint. Production discovery can
therefore upload and find persisted volumes. Previously F23's locator-absence
check was valid, but its additional no-source-node fallback could fail without
discovering any daemon. The new discovery configuration strengthens that
downstream-refusal check; F23 and all five handoff cases were rerun.

Old/new production-only overlays established:

| Mutation | Old cases | New cases |
| --- | --- | --- |
| Disable input fetching | Both pass: host commands never consumed fetched data | Both fail at missing real fetch-inputs |
| Corrupt input mount destination | Both fail their mount assertion | Both fail earlier because real input delivery cannot complete |
| Store check instead of task/put container metadata | Both fail the original row assertion | Input case fails that same row assertion; publication case detects its producer's wrong row before proceeding |
| Drop resource stdin | Input passes, echo-response assertion fails | Input passes, actual resource JSON/publication assertion fails |

Old control: 2/2 passed in 11.725s. Initial accepted new control: 2/2 in
64.066s. Final integration control: 2/2 in 67.082s. The initial build had an unused
import; an accidentally launched older adapter and the next run both encountered
backslash-escaped apostrophes in the new feature text and reported undefined
steps. They are retained and excluded, not counted as passing controls.

The first shared handoff regression run had four passes and one failure:
the producer was absent from the API, but quota admission still counted four
pods and rejected the consumer. All five cases cleaned completely. The shared
deletion helper now waits until its owned quota no longer counts more pods than
the namespace contains, without raising any limit or changing quota status.
The final repeat passed all five cases in 163.458s. No final run entered that
wait branch, so this is a guard for the observed race, not proof that every
quota/admission-cache timing failure is eliminated. The original failure is
preserved.

Final verification: all eight affected live cases passed (two integrations,
five handoffs, F23); F23 took 36.110s. All 494 local cases passed in 142.988s,
plus 5.287s for the skipped live manifest. All three native suites passed in
37.085s, including complete vocabulary resolution/use/ambiguity checks.
Default and live-tag vet passed. Every live run, including excluded setup and
quota failures, has complete drains and matching owned namespace, daemon-port
and storage-reclamation evidence. Final independent checks found no owned
namespaces or recent active verification processes.

Evidence: /tmp/brine-live-integrations.n4f7G8/evidence.json, checked by verify.cjs.
SHA-256: 4a1f7d5faf09df3c6aa84d49b4fe0144936bc937734163b30d8d4eb3e7e1e881.
Normal adapter SHA-256:
a382e72c85620aff1de5ead530c4c5bd1a325b7d44b48226df478f526668c69c.

Production mutation targets are unchanged on disk. Existing node port 7780,
node configuration and cluster/RBAC settings are untouched. No commit, push,
deployment or image publication was made. The existing production daemon
executable was reused and checksum-verified in each owned pod.

The goal remains active. No fresh full-suite coverage measurement is claimed.
Next: failed-init observability and remaining reported lifecycle fixtures,
preserving repeated-observation and state-sequence contracts rather than
replacing them with simpler failure checks.

## 2026-09-14 — OE-06 real init recovery; last host executor removed

The existing failed-init scenario now lives in live/task-command.feature.
Its original event-membership and exactly-once assertions are unchanged.
It additionally requires zero completion events through the existing generic
event-count assertion; no new check language or scenario was introduced.

A pre-existing, scenario-owned BusyBox pod uses an explicit OnFailure restart
policy and an emptyDir. Its real init tries to read a missing file twice,
actually exiting 1 on both attempts. A separate real API watch must observe
two distinct current terminated container IDs on the same pod UID before the
third attempt is released by creating that file. The kubelet then reports a
successful init and the production SPDY task emits startup-observed.
The runtime opens its real watch before the first attempt is released.
No pod status, watch reply or executor result is fabricated.

This is a lifecycle-observation contract for a pre-existing OnFailure pod.
It does not claim production's default RestartNever pod retries failed inits,
and it does not substitute for the real artifact-fetch integration contracts.

Old/new production-only mutation comparison:

- Removing the completed-init deduplication guard makes both original and live
  cases fail their original exactly-once assertion: each observes two failures.
- Renaming away the failed-init event makes both fail their original event
  membership assertion.
- Classifying nonzero init exits as completion likewise makes both fail their
  original event membership assertion.

All six mutant runs completed with the intended assertion failure, not setup
failure. All three live mutants still witnessed real recovery and task output.
Every owned namespace create/remove UID pair matched and every drain completed.

Removed the last localExecutor implementation, its supervisor-path mapping,
exit translation and process-inspection helpers, the reported OE-06 status
writer, ExecClusterReady, and the unreferenced requireSupervisorState method.
Five native tests solely for the retired executor were removed with it.
The independent workspace disposal/ownership contract is retained as
TestTaskWorkspaceDisposalIsIsolated, including refusal of an unowned directory,
isolation of another workspace and protection against a changed public Dir.
No production Ginkgo tests were retired. Before-images of all step files remain
in /tmp/brine-init-recovery.CFb5nC/before-steps for recovery and comparison.

Three obsolete setup/action phrases became two live transitions:
1,043 -> 1,042 definitions. Scenarios remain 567, now 493 local + 74 live.
Across the affected step/test files, including new files, raw lines decrease
from 2,454 to 2,083 (371 fewer); this is not a production coverage metric.

Validation:

- Original control: 1/1, 4.939s. Initial live probe: 1/1, 39.122s.
- Promoted normal-adapter control: 1/1, 39.365s.
- Adjacent normal-adapter startup regressions: 4/4, individually 24.809s
  (OE-01), 20.998s (OE-04), 38.212s (OE-06), 15.130s (RF-14).
- All 493 local cases pass in 141.675s; the excluded-live manifest takes
  another 5.162s and skips all 74 live cases.
- All three native suites pass in 36.407439165s, including vocabulary guards
  and the retained workspace-disposal test. Default and live-tag vet pass.
- Final independent namespace and recent-process checks are empty.
  Production process.go is byte-identical to the before-image.
- The first promotion build found an unused filepath import after deleting
  the last helper consumer. It was removed before the normal control.
  An unsupported literal "or" tag expression was rejected as empty selection
  before starting tests; that log is retained separately, not counted as a pass.
  Individual supported tag selections supplied the four regression results.

Checked evidence: /tmp/brine-init-recovery.CFb5nC/evidence.json
SHA256: c5dbfff1e85b4f77c8113f04d0654e9179d649e8d1f4cd272409be794c3f04fb
Normal adapter SHA256:
1a1028bae0efb6b2e76d96af4754a6f85aa3d171a32dde9b72e870619e6eadcf

No production source, cluster/node policy, images, commits or remote refs were
changed in this increment. No fresh full-suite coverage is claimed.
Historical 1,950/2,466 = 79.075426% is not current-tree coverage.

The goal remains active: other reported-status fixtures still need faithful
physical replacements, especially early sidecar states and repeated OOM
current/last-termination evidence. The prior OOM probe did not preserve that
premise and is not credited as a replacement. Node eviction/drain/pressure/spot
work remains outside the owned-pod approval; no node disruption is authorized.

## 2026-09-14 — Real no-status failure; bounded waits and shared finalizers

The Failed/no-container-status case moved from failure-priority.feature to
live/compatibility-process.feature. Its scenario name, RF-09 requirement tag,
and original "the step's exit status is 1" assertion are retained.

A real API pod is created with a scheduling gate, so no node or container
starts. An owned finalizer holds deletion while the real PodGC controller
marks the unscheduled terminating pod Failed. The fixture verifies the same
UID, no node assignment, no container/init statuses, a deletion timestamp,
and no active DisruptionTarget condition. The real compatibility Attach path
returns *jetbridge.Process; Wait interprets that actual state and returns
exit 1 with a nil error. There is no UpdateStatus, substituted API reply,
executor, or supplied runtime result in this replacement.

The matching upstream implementation was inspected at
https://github.com/kubernetes/kubernetes/blob/v1.34.3/pkg/controller/podgc/gc_controller.go.
The live server reports v1.34.3+k3s1. The unscheduled-terminating path is
deliberate: orphan-node GC adds DeletionByPodGC, which this runtime classifies
as a node-loss interruption instead of the phase fallback. No node was
created, changed, drained or removed.

The first probe used Run on a pre-created pod. Compatibility Run always
creates, so the one-pod quota correctly rejected the duplicate request.
That rejected run cleaned up its finalizer, pod and namespace; no quota was
increased. Switching to the real Attach path preserved the Process.Wait
subject while avoiding duplicate creation.

Two production-only mutations discriminate the old and new cases:

- Returning zero for Failed/no-status fails the original exit-status assertion
  in both cases.
- Refusing to finish on Failed/no-status fails that same assertion with a
  deadline error in the bounded old and live cases.

The non-completion mutant exposed an existing harness defect. The old
settlePod helper had no deadline and remained in Wait for 216 seconds before
the exact owned adapter was sent SIGTERM. Complete drains were recorded, but
there is no run_end: this interrupted run is explicitly NOT credited as an
assertion detection. settlePod now has a 15-second context deadline. Its
unchanged control passes; the bounded old mutant reaches and fails the
original assertion after 15 seconds. The live terminal observation independently
bounds Wait at five seconds. Production code was not changed.

Finalizer management was consolidated with the existing cancellation support.
holdLivePodDeletion in live_kubernetes.go is the sole implementation of adding
and releasing the owned observation finalizer. It retains UID checks,
conflict retries, preservation of other finalizers, and cleanup registration
before the write. The evidence verifier compares the moved body against its
before-image: only helper/constant names and diagnostic labels differ.
The controller case additionally deletes/verifies its exact pod after release.
Both normal and mutation runs verify finalizer and namespace cleanup.

Validation:

- Original control: 1/1 in 4.559s; bounded old control: 1/1 in 4.857s.
- First valid live probe: 1/1 in 21.517s; promoted control: 1/1 in 32.958s.
- Final shared-helper control: 1/1 in 19.842s.
- Final live false-success and non-completion mutations fail at the original
  assertion in 19.802s and 23.237s. The bounded old non-completion run takes
  20.396s including startup/cleanup.
- Two adjacent compatibility completion cases pass in 24.340s.
- All six real cancellation cases pass in 72.357s with the shared finalizer.
- Final local suite: 492/492 in 141.690s, plus 5.126s for the manifest that
  excludes all 75 live cases.
- Final three native suites pass in 36.302213995s, including vocabulary guards.
  Default and live-tag vet pass.
- Final independent owned-namespace and recent-process checks are empty.
  Production process.go matches the before-image.
- Moving the helper left an unused corev1 import; the build rejected it before
  any final-consolidation tests ran. It was removed before the successful
  final sequence. The rejected build is not represented as a test pass.

Inventory remains 567 expanded scenarios, now 492 local + 75 live.
One behavior-level definition was added: 1,042 -> 1,043. The new case uses the
existing Kubernetes setup and original outcome check. No production Ginkgo
tests were retired, and no additional scenario was introduced.

Checked evidence: /tmp/brine-orphan-fallback.RXizWY/evidence.json
SHA256: 2d7665cc295904209b9d5eb994ee39aed23a88890687b8db962afaf510a69d33
Normal adapter SHA256:
1859713347a7c9bb37e3014b71a6b423f5d9cf49720dce4808ab0f539d882dfd

### Rejected early-sidecar replacement — contract retained

Before the controller work, an owned-pod probe tried to preserve main
ContainerCreating plus sidecar ImagePullBackOff by first keeping main
unstarted with an invalid image, then repairing only its image. The real
kubelet retained main's InvalidImageName state and then reported Running,
without the required ContainerCreating observation. The probe correctly failed
its premise in 31.595s and removed its owned namespace. This disproves that
attempted fixture, not every possible physical realization of the contract.

Evidence: /tmp/brine-early-sidecar.4tGTJX/probe.log, also hashed and verified by
the controller evidence record. All six original sidecar cases remain, with
their main-not-started distinction and exact sidecar/image fixtures intact.

The goal remains active. Other reported-state fixtures remain, including
Succeeded/no-status, repeated OOM history, early sidecar startup, and terminal
check-pod reuse. The two terminal check-pod reuse rows are the next practical
candidate for real completed-pod fixtures. No fresh full-suite coverage is
claimed: historical 1,950/2,466 = 79.075426% is not current-tree coverage.
No image, commit, push, node policy or deployment change was made.

## 2026-09-14 — Terminal check-pod reuse uses actual completed pods

The existing two-row outline moved from container-lifecycle.feature to
live/container-lifecycle.feature. Both original Succeeded/Failed rows,
the handle aaaa1111-bbbb-cccc-dddd-eeee2222ffff, check metadata my-time,
and the three existing step phrases remain. PE-01 and PE-11 requirement tags
are retained. No scenario or definition was added.

The setup now consumes the common LiveTaskPlan and creates an owned live worker.
A real BusyBox main process prints a marker and exits 0 or 1. Before the
production replacement action runs, the helper independently requires the
same API UID, changed resource version, actual node, container ID, nonzero
start/finish timestamps, expected exit/phase, no restart, and actual kubelet
log bytes. It never writes pod status or supplies an executor response.

The production Run action and the original replacement assertion are preserved
byte-for-byte. The assertion still rejects errors, terminal pods, missing API
identity and reuse of the previous UID. This is a Container.Run replacement
contract: the subsequent /opt/resource/check command is not Waited, and its
execution or resource protocol is not claimed.

Production-only container.go mutation comparisons:

- Ignore Failed: only the Failed row fails in both old and real cases.
- Ignore Succeeded: only the Succeeded row fails in both.
- Skip deletion: both rows fail in both with AlreadyExists instead of a new pod.

All eight failing executions reach the original assertion. The four paired
old/new comparisons have identical scenario names and error text. Every live mutation still creates and
observes both actual prior completions; none fails its setup. All disposals
complete and every owned namespace create/remove UID pair matches.
No production fix was needed.

The verifier also formats the probe source without modifying it and compares
it with the promoted file, ignoring only the added explanatory comment.
The final executable fixture/definition code therefore matches the code used
for the paired mutation comparisons.

Validation:

- Original control: 2/2 in 5.169s.
- Real probe: 2/2 in 35.087s.
- Promoted normal-adapter control: 2/2 in 34.992s.
- All 490 local cases pass in 141.810s, plus 5.073s for the manifest that
  excludes all 77 live cases.
- All three native suites pass in 37.074280926s, including vocabulary guards.
- Default and live-tag vet pass. Final independent namespace and recent-process
  checks are empty. Production container.go is byte-identical to the before-image.

Inventory remains 567 expanded scenarios and 1,043 definitions, now
490 local + 77 live. One direct status-writing site was removed. The other
recovery/property fixtures in container_lifecycle.go are unchanged and are
not represented as newly kubelet-backed.

Checked evidence: /tmp/brine-check-reuse.DnWvtb/evidence.json
SHA256: 7a0ed215017f6632848dee34c87e7b374ca58134a261f46f81d52e8d9ff60699
Normal adapter SHA256:
643e6cc5af853d8b8588694d8f938bcde2529c9a87d0a8f3a78e7708c48f6422

The goal remains active: other reported lifecycle fixtures, including early
sidecar, repeated OOM and Succeeded/no-status, still need resolution.
No fresh full-suite coverage is claimed; historical 1,950/2,466 = 79.075426%
is not current-tree coverage. No production Ginkgo tests were retired and
no images, commits, pushes, node policy or deployment changes were made.


## 2026-09-14 — Worker presence no longer writes pod status

The existing orphan-pod lookup scenario moved to live/interception.feature,
retaining RC-01, its name, lookup action and original clean-miss assertion.
It uses the existing live worker and createInterceptPod fixture unchanged:
the kubelet must report Running with a ready main container and real container
identity, and actual SPDY exec verifies the pod's hostname before lookup.
No additional fixture helper, scenario or step definition was introduced.

The other caller of WorkerReady.createPod only needed a real API object followed
by deletion. Those four artifact-lifetime cases now create the pod directly and
retain the existing UID-scoped reapPod deletion and NotFound verification.
Their unused supplied Pending/Ready state is gone. They remain API-only
fixtures: they do not claim that the reaped producer executed. The separate
live artifact cases cover actual production and consumption of outputs.

The shared worker status-writing helper was removed. All original lookup and
artifact assertions, artifact feature rows, production worker.go, and reused
live helpers are byte-identical to their before-images. The worker feature's
stale claim that interception runs on the host was corrected.

Three isolated production-only faults were checked against old and new fixtures:

- Treat an existing pod with no DB row as a container: both fail the original
  clean-miss assertion with the same unexpected-found message.
- Turn a missing DB row into an error: both fail the same assertion with the
  same clean-miss/error message.
- Skip artifact wrapping: all three daemon-backed rows fail their original
  read assertion in both versions; the no-daemon row still passes.

These are five paired old/new assertion failures, not setup failures. Both
live mutation runs independently establish the real Running/exec premise.
All scenario drains complete and owned namespace create/remove UID pairs match.

Validation:

- Original five-case control: 5/5 in 6.831s.
- Migrated artifact control: 4/4 in 6.320s.
- Migrated live orphan control: 1/1 in 18.908s.
- Full live interception file: 5/5 in 73.107s.
- All 489 local cases pass in 142.209s, plus 4.953s for the manifest that
  excludes all 78 live cases.
- All three native suites pass in 38.31514128s; default and live-tag vet pass.
- Final independent namespace and recent-process checks are empty.

Inventory remains 567 scenarios and 1,043 definitions, now 489 local + 78 live.
One direct status-writing site and its helper were removed.

Checked evidence: /tmp/brine-worker-presence.90w0TK/evidence.json
SHA256: 14c4eda71fdf60a82d34bdef8e71fc52ca2c19b55bca8a48be3b02251f79d22b
Normal adapter SHA256:
408f754505daa0f65205e0bc7f0e8c0d4018bccaae4a3f597aa6a7ceb3a85a79

The goal remains active. Other reported lifecycle fixtures still require
resolution, including early sidecar, repeated OOM and Succeeded/no-status.
No fresh full-suite coverage is claimed; historical 1,950/2,466 = 79.075426%
is not current-tree coverage. No production Ginkgo tests were retired and
no images, commits, pushes, node policy or deployment changes were made.


## 2026-09-14 — Selector scoping uses actual neighbouring pod lifecycles

PW-03 moved from the former pod-watch-real.feature prototype to
live/pod-watch.feature. The scenario name, three existing step phrases,
requirement identifier and original identity/phase assertion remain.
No scenario, step definition or new state type was added.

Two pods start behind owned scheduling gates in the existing bounded live
namespace. The runtime first reads its unscheduled Pending pod. Only the
neighbour's gate is released; the fixture waits for an actual Failed pod,
real main-container identity, exit 1, start/finish timestamps, no restarts
and the expected kubelet log. Only then does it release the watched pod,
whose actual Running/ready/container identity is checked by the unchanged
shared awaitLivePod helper. No phase, container status or API event is supplied.

Unlike the former status setter, a real scheduler/kubelet produces intermediate
Pending updates. The action consumes only Pending events belonging to the
watched name. Any neighbour event, error, missing pod or unexpected phase goes
straight to the original assertion. It never filters away an incorrect pod
identity to make selector scoping pass. Reads have a fixed 20-second deadline.

The assertion is byte-identical to its before-image. The rest of the existing
watch fixture implementation is also byte-identical; it has not been silently
converted or claimed as kubelet-backed. One direct status-writing closure was
removed. Stale prototype text and three references to the relocated feature
were corrected; the daemon cases still share the local API server with
pod-watch.feature.

Production-only watch.go faults, checked against old and live fixtures:

- Remove the field selector: both fail the original identity assertion.
- Select every name except the watched name: both fail the identity assertion.
- Corrupt a returned Running phase to Failed: both fail the phase assertion.

All three paired failures have identical scenario, assertion and error text.
Each live mutant independently establishes the actual neighbour failure and
subsequent watched-pod startup before the assertion fails. The evidence verifier
matches original pod UIDs and advancing API versions across these observations.
Production watch.go is unchanged.

Validation:

- Original control: 1/1 in 5.018s.
- Initial live control: 1/1 in 21.622s.
- Final normal-adapter control: 1/1 in 21.164s.
- All 488 local cases pass in 141.582s, plus 5.083s for the manifest that
  excludes all 79 live cases.
- All three native suites pass in 37.07796293s; default and live-tag vet pass.
- Every scenario drain completes and every owned namespace create/remove UID
  pair matches. Final independent namespace and recent-process checks are empty.

Inventory remains 567 scenarios and 1,043 definitions, now 488 local + 79 live
across 32 local and 19 live feature files.

Checked evidence: /tmp/brine-watch-scope.FxoRbZ/evidence.json
SHA256: f3cc8b8728ad188a38a009cdb5aa07bb0533a9aa3bae2d361554e95e5cf4f145
Normal adapter SHA256:
403d778f8e0aab955bfd18ccebd03135e70cba646492f4825bd475ecb54e0808

The goal remains active: other reported watch/recovery/lifecycle fixtures,
including early sidecar, repeated OOM and Succeeded/no-status, still need
resolution. No fresh full-suite coverage is claimed; historical
1,950/2,466 = 79.075426% is not current-tree coverage. No production Ginkgo
tests were retired and no images, commits, pushes, node policy, RBAC or
deployment changes were made.


## 2026-09-14 — One observer for actual fixture-pod completion

The completed-check and selector fixtures now share awaitLivePodExit instead
of duplicating terminal polling and validation. The shared observer requires
the original API identity, an advancing resource version, terminal phase
derived from the expected exit, a real main-container identity, the expected
exit without restarts, nonzero start/finish timestamps, and exact kubelet logs.
It returns the actual observed pod and log, not a reconstructed result.

Pod creation remains in each fixture. Selector gate ordering and Pending-event
handling are unchanged, as are both original production assertions. All other
shared live helpers and watch implementations are unchanged. The three affected
Go files shrink from 1,266 to 1,255 lines; there is one completion-check
implementation and no new scenario, definition, or state type.

Before/after live controls:

- Completed-check rows: 2/2 before in 35.279s; 2/2 after in 35.489s.
- Selector case: 1/1 before in 20.742s; 1/1 after in 19.656s.

All six established production faults were rerun. Ignore-Failed,
ignore-Succeeded, skip-deletion, missing-selector, wrong-selector and corrupted
phase reproduce seven original assertion failures with identical error text.
The verifier checks the prior evidence/log hashes and confirms that the
pre-refactor consumer source matches the source used for those prior results.
Both production files are unchanged.

A separate fixture-only overlay changes the actual pod commands to emit an
unexpected log line. Both check rows and the selector case reject the real
bytes at the shared log check before reaching the production assertion.
These are three fixture-premise rejection checks, not additional suite cases
or claimed production-mutation kills. All owned resources are cleaned.

Validation:

- All 488 local cases pass in 142.238s, plus 4.401s for the manifest excluding
  the 79 live cases.
- All three native suites pass in 37.116984218s.
- Default and live-tag vet pass.
- All recorder drains complete; namespace create/remove UID pairs match.
  Final independent namespace and recent-process checks are empty.

Inventory remains 567 scenarios (488 local + 79 live) and 1,043 definitions.

Checked evidence: /tmp/brine-live-completion.wHF3SI/evidence.json
SHA256: c244d78b19233827171342159997c8470e0ce4601f102c0ecf7ed00dec7f2488
Normal adapter SHA256:
abb474de43ba85e4951937c24dc3b5bca75dd3c193c7cec8ca6740efa4aebf18

The goal remains active. Other supplied lifecycle fixtures still require
resolution, and the >=50% full coverage gate needs a fresh current-tree run.
Historical 1,950/2,466 = 79.075426% is not current-tree coverage.
The existing coverage script requires both manifests and checks actual passing
cases in each; a local-only measurement must not replace that full gate.
No production Ginkgo cases, images, commits, pushes, node policy, RBAC or
deployment changes were made.


## 2026-09-14 — Fresh full v5 coverage gate

The unchanged current core worktree passed the full scripts/coverage gate:

- 488/488 local cases across 32 feature blocks: 142.114s.
- 79/79 live cases across 19 feature blocks: 1,486.154s.
- 567/567 total; no failures, skipped cases or undefined cases.
- Combined scenario/setup time: 1,628.268s = 27m8.268s, excluding prerequisite
  builds, report generation and final normal-adapter rebuild.
- Brine-only production JetBridge coverage: 1,963/2,466 statements =
  79.602595%, exceeding the >=50% gate.

This supersedes the historical 1,950/2,466 measurement. The denominator remains
the production atc/worker/jetbridge package only; adapter and other package code
are excluded. The independent verifier recalculates the exact fraction from
the atomic profile, rejects duplicate blocks and unexpected files, and matches
all 1,598 reported blocks to the compiled production file set (25 files with
coverage blocks). It does not accept the rounded go-tool percentage as the gate.

The SDK independently parses every feature and expands every outline. The audit
matches the full feature/scenario inventory against the actual event stream,
including source path, feature name, scenario name, source line and step count.
It also requires every step and scenario to pass and all 567 recorder drains
to complete without partial disposal. All 1,043 definitions remain registered.

A fingerprint of all 2,794 versioned/nonignored worktree entries is identical
before and after the gate, at core HEAD 3c787e9c70602baf2279c5ff92ad32253edcf68b.
The dirty worktree was preserved. Only this report and README were updated
after measurement; no implementation or feature change was made during the run.

The CLI checkout is d1c75ea958f299db1aee754b65d9da2b456a03ac and is clean.
The Go SDK remains pinned to 6bc587016933. Its Git tree is identical at both
revisions: c2ae944f50060b61d57a7693b30e58927026f583. The intervening commits
change CI/documentation, not that SDK. Exact CLI and instrumented-binary hashes
and Go build metadata are retained with the evidence.

The approved storage tier used a freshly built static production daemon on
the approved node and high TCP port 49179; it did not use the production port
7780 or change node policy/RBAC. Cleanup verification matches:

- All 79 created live namespace names and UIDs to removal receipts.
- All eight owned storage roots/anchor UIDs to independent kubelet-removal checks.
- All eight daemon UIDs/endpoints to verified connection-refusal checks.
- Empty final independent namespace and recent-process listings.

There are nine closure receipts for eight daemons: the producer-offline case
closes its daemon in the action, then the registered disposer verifies closure
again. The first independent verifier incorrectly required one receipt per
daemon. After inspecting the matching UID and both production fixture call
sites, it was corrected to require exact equality of owned/closed UID-endpoint
sets while recording repeated receipts. It still rejects missing or foreign
closures. No suite rerun or test change was needed.

The gate's exit trap restored the normal adapter byte-for-byte:
abb474de43ba85e4951937c24dc3b5bca75dd3c193c7cec8ca6740efa4aebf18
The restored build has no coverage instrumentation.

Checked evidence: /tmp/brine-full-current.XjQHMk/evidence.json
SHA256: dd0a2fa734988ac96620c7c25458388c1ed826be3d9b4d7aee47f63957ba8d29
Raw run and counters: /tmp/brine-coverage.VKuLqr
Coverage profile SHA256:
530bafc294b017d4b61b261f55d7a94f4caca3f1fb4d67a4b90525b8d8318d0e
Measured worktree fingerprint SHA256:
5397623a457e52dc3c71b5722143d57254538afc7420820e4f8335739839cd0a

The >=50% coverage requirement now has fresh full-suite evidence, but the goal
remains active. Supplied lifecycle fixtures still exist. Read-only follow-up
audit also found the no-op BuildStepDelegate: production FindOrCreateContainer
does not read that parameter, so the dummy should be removed rather than
expanded. The nested PTY requirement may also outlive the retired host executor;
the root fly/pty consumer is real and must not be removed. These follow-ups
were not implemented during the frozen coverage run.

No production Ginkgo cases were retired and no images, commits, pushes or
deployments were made. Obsolete historical inventory paragraphs were removed
from README; their evidence remains in this journal.


## 2026-09-14 — Remove the unused dummy delegate and nested PTY requirement

An exhaustive AST audit found 34 empty noopDelegate constructions across
26 Brine step files. Every construction was the fifth, direct argument to
FindOrCreateContainer. The production method's body neither reads nor forwards
its delegate parameter. All 34 arguments now pass nil; the dummy type, its
zero-timestamp BuildStartTime method and the now-unused time import are gone.
Real engine delegates are unchanged.

This is removal of an unused collaborator, not a replacement implementation.
The post-edit AST audit finds no dummy identifiers and rechecks the production
parameter's non-use. The source verifier compares every step file with its
before-image and allows only these mechanical replacements and declaration
removals. All assertions, feature inputs, and production sources are unchanged.
Previously deleted feature files remain absent.

Go reports that the nested main module does not need github.com/creack/pty,
which outlived the retired host executor. Its direct nested requirement was
removed. All 636 selected module paths/versions/replacements are byte-identical
before and after, as are the nested go.sum and root go.mod. Fly's real PTY
consumer and root requirement are retained. No broad tidy or dependency
upgrade was performed.

Validation:

- Four representative live cases pass in 65.115s: both completed-check rows,
  the real worker-pool partial-put path, and supervised task completion plus
  current/fresh-container recovery.
- All 488 local cases pass in 141.519s, plus 4.359s for the manifest excluding
  the 79 live cases.
- All three native suites pass in 36.730578908s.
- Default and live-tag vet pass.
- Catalog patterns are identical; inventory remains 567 scenarios
  (488 local + 79 live) and 1,043 definitions.
- All four live namespace create/remove UID pairs match, all scenario drains
  complete, and final namespace/recent-process checks are empty.

The formatting audit found one pre-existing extra blank line in
container_spec.go. The before-image has the same formatting finding, and it
was preserved rather than folded into this mechanical change. The first
source audit also tried to read a previously deleted feature from the baseline
fingerprint; it was corrected to require recorded deletions to remain absent.
Neither finding required a test rerun or change to an assertion.

The exhaustive unread-argument proof and exact source transformation establish
that this cleanup preserves the existing test logic; no new production
mutation run or full coverage measurement is claimed. The latest full-gate
baseline remains 1,963/2,466 = 79.602595%; its production source is unchanged.

Checked evidence: /tmp/brine-noop-cleanup.2fGERF/evidence.json
SHA256: 8830595025941099388e62f70c594c3492e7472413bfe30c073ad311d34db553
Normal adapter SHA256:
70336e3a9e5a642cb50d48efc77d676e416fc4f0353af0034aa1e7001da722d3

The goal remains active because reported lifecycle fixtures still remain.
No production Ginkgo cases were retired and no images, commits, pushes,
node policy/RBAC changes or deployments were made.

## 2026-09-14 — Watch cancellation establishes its stream without supplied status

The cancellation scenario no longer writes a fictitious Running phase to its
API-only pod. It calls the existing watchCheckpoint helper instead: a real
persisted annotation update must be observed by the production PodWatcher at
the exact API-assigned resourceVersion. This proves that the stream is
established without claiming pod execution or a kubelet transition.

The HTTP watch still has an independent context from the next read. The test
observes that read idle for 100ms, cancels only the read, and requires its
cancellation error. A missing cancellation branch is bounded to 3s, then the
real stream is stopped and the blocked goroutine joined. Closing the HTTP
stream therefore cannot mask the missing branch by waking the outer loop.

The original scenario, action language and final assertion are unchanged.
Only the setup block inside cancelEstablishedRead and one feature comment
changed. No helper, scenario or definition was added. Other watch fixtures
and their reported transitions were not modified. The README also drops a
stale claim that OE-06 init retries still use reported status; that scenario
already uses real execution.

Production-only Go overlays, built separately against before/after fixture
sources, prove both original faults are still detected:

| Production fault | Before | After |
|---|---|---|
| Remove the inner blocking select's cancellation branch | Original assertion fails after bounded wait | Identical assertion/error |
| Return nil instead of cancellation errors | Original assertion fails on absent error | Identical assertion/error |

Both normal controls pass (4.848s before, 4.743s after, including setup).
All six runs complete their recorder drains; the four expected mutant
failures exit 1 and fail only the unchanged final cancellation assertion.
The production watch source was never edited; overlays live in the evidence
directory, and the workspace adapter is a normal build.

Validation:

- 488 local scenarios pass in 142.342s; the excluded-live manifest takes
  another 5.217s and explicitly skips its 79 cases.
- Three native suites pass in 36.201378934s; default and live-tag vet pass.
- Inventory and catalog patterns are unchanged: 567 scenarios, 1,043 definitions.
- The independent verifier checks the exact source transformation, unchanged
  scenario and production sources, paired failures, every selected scenario
  status and complete drain, and empty final namespace/process checks.
- git diff --check passes. No live pods were created by this increment.

Checked evidence: /tmp/brine-watch-checkpoint.LjkzvX/evidence.json
SHA256: 0fcd0151e1c26811b78a4c1fe77db735225980605848db9fbfb095fca1df32fe
Normal adapter SHA256:
ef7352fd116a843ac1d83633beeb0812cbd79fa6d1bd3455ead9843c14806b9c

The latest full coverage measurement remains 1,963/2,466 = 79.602595%;
this increment did not rerun full coverage. Other reported-status fixtures
and the broader migration completion audit remain, so the goal is active.
No production Ginkgo cases were retired, and no commits, pushes, image
publication, deployments or node/cluster policy changes were made.

## 2026-09-14 — Consolidate the duplicate subsequent-watch-change case

The reported-status scenario "Subsequent changes arrive as they happen" is
retired into the already-existing live selector scenario "A step is never
told about somebody else's pod". The retained fixture reads its own gated
Pending pod, observes the neighbour's real exit 1, releases its own pod and
independently verifies that the same UID actually reaches Running. Its final
assertion checks both pod identity and the expected Running phase.

This covers the removed case's initial-read/subsequent-phase contract with
an additional identity requirement. Neither case measures event latency.
The initial-read-only, reconnect/replay, fallback, cancellation, deletion,
burst and expiry scenarios remain distinct and unchanged. In particular,
the reported phase action remains necessary for the fallback case; no
claim that all watch status writes are gone follows from this consolidation.

Before deleting the duplicate, the unmodified fixtures and normal adapter
were captured and compared using two production-only Go overlays:

| Fault | Removed case's original assertion | Retained live assertion |
|---|---|---|
| Keep reporting the initial phase on later watch events | Rejects Pending instead of Running | Rejects failure to deliver Running within the bounded read deadline |
| Report Failed instead of Running | Rejects Failed | Rejects Failed with the same expected phase |

Both controls pass (4.893s local, 21.538s live). All four expected mutant
failures exit 1 and reach the respective final assertions, not failed setup.
All three live comparison runs prove actual neighbour/watched UID transitions
and complete owned namespace cleanup. The pre-retirement proof was validated
and saved before deleting the duplicate:
  /tmp/brine-watch-consolidate.cdcVlH/pre-retirement-proof.json
  SHA256 b43a0a2e5fb42a7e7b6b035f32765973cd31089d92f4e1d3103afd1edf8bfb0d

The local case is removed, and PW-02 is explicitly attached to the retained
live feature alongside PW-03. Only feature comments/tags and README accounting
change besides the scenario deletion. No Go source, retained assertion,
helper, step definition, production Ginkgo case or binary changes.

Post-consolidation validation:

- The actual live manifest selected by PW-02 passes the one retained case
  in 20.620s and explicitly skips its 78 unrelated cases.
- All 487 remaining local cases pass in 141.576s; the excluded-live manifest
  takes another 5.203s and explicitly skips its 79 cases.
- All three native suites pass in 36.27394417s.
- Inventory is 566 scenarios (487 local + 79 live), down from 567.
  Catalog patterns are unchanged at 1,043 definitions; no helpers were added.
- The audit compares the actual local case roster against the preceding
  passing run and requires exactly the retired case to be missing.
- It also checks exact feature transformations, unchanged fixture and
  production sources, original/retained assertion mapping, all selected
  scenario outcomes and complete drains, and unchanged normal adapter bytes.
- All four owned live namespace UID pairs across comparison/final runs were
  removed. Final namespace and recent process listings are empty.
  git diff --check passes.

Checked evidence: /tmp/brine-watch-consolidate.cdcVlH/evidence.json
SHA256: 4044b9844356d4b3e5e3f733f3d2842e4d7ad195c03681210355c0b884564672
Normal adapter SHA256 (unchanged):
ef7352fd116a843ac1d83633beeb0812cbd79fa6d1bd3455ead9843c14806b9c

The last full coverage measurement remains the 567-case baseline,
1,963/2,466 = 79.602595%. Coverage was not remeasured after reducing the
inventory to 566; no claim of current-tree full coverage follows from the
focused live/local checks. Reported-state fixtures and the broader migration
completion audit remain, so the goal is active.

No commits, pushes, images, deployments or node/cluster policy changes
were made. Original scenario source is preserved in the evidence directory.

## 2026-09-14 — A real kubelet supplies the watch burst

The existing burst scenario moved to live/pod-watch.feature. Its initial
runtime read, Running/Succeeded action wording, update-draining behavior and
final Succeeded assertion are preserved. Only the Given changes to an explicit
live cluster with a gated pod. One setup definition was added; no scenario
or state type was added.

A real scheduling gate keeps rapid-pod Pending until the runtime reads it.
After removing that gate, the shared readiness observer must see the original
UID and a real Running main container on a node. That main waits for a file;
a real SPDY exec creates the file, allowing the actual process to print its
marker and exit 0. The shared completed-pod observer verifies advancing API
version, the original UID, exact logs, exit code, timestamps and zero restarts.
The Running and terminal observations must identify the same container.

Only after both independently observed transitions finish does the unchanged
consumer loop start draining the production PodWatcher to the final phase.
The test supplies neither pod status nor a watch event/result. The explicit
file and scheduling gates control ordering, not the reported outcome.

The selector's UID-checked scheduling-gate release closure is extracted into
a shared method and reused by the burst. Its body is unchanged, including
RetryOnConflict and exact owned-gate checks. Both existing Running and
completion observers are reused. The final live regression runs both callers.
Other API-only watch fixtures and all existing assertions are unchanged.

Before/after production-only overlays validate two faults:

| Production fault | Reported-state fixture | Real burst fixture |
|---|---|---|
| Report Running for terminal Succeeded events | Original final assertion fails at read deadline | Identical assertion/error |
| Discard terminal Succeeded events | Original final assertion fails at read deadline | Identical assertion/error |

Both normal controls pass (4.556s before, 16.064s live). All four expected
mutant failures exit 1 at the unchanged final assertion, not at setup.
The three live comparison runs verify real Running/Succeeded UID,
resourceVersion, node and container identity receipts and complete cleanup.
Faults are isolated build overlays; production sources are unchanged.

Validation after moving the feature:

- Both live watch cases pass through the actual manifest in 30.773s, with
  78 unrelated live cases explicitly skipped. The selector still observes
  its neighbour's exit before releasing its own pod.
- All 486 local cases pass in 141.697s; the excluded-live manifest takes
  another 5.316s and explicitly skips 80 live cases.
- All three native suites pass in 38.591290659s.
- Default and live-tag vet pass.
- Total inventory remains 566 scenarios: 486 local + 80 live.
  The catalog changes only by the explicit live setup phrase, to 1,044 definitions.
- The independent verifier reconstructs the exact watch source transformation,
  checks the unchanged extracted gate body and original assertions, exact
  feature move, production hashes, local roster minus the moved case, complete
  scenario drains and all paired mutation failures.
- All five owned live namespace UID pairs across comparisons and final controls
  were removed. Final namespace and recent process listings are empty.
  git diff --check passes.

The first edit-script attempt had a JavaScript quoting error before executing
any edit. It was corrected; no test failure or source rollback was involved.

Checked evidence: /tmp/brine-watch-burst.VLwC8u/evidence.json
SHA256: 4b2443b9003355339c420598636d87b846a91bcf4bf1cd2f7461c9d24fd48ba0
Normal adapter SHA256:
844fdf4bfe61b80edbba196454105ba512423c14e8c4c0e2ad57f879bd51a724

The last full coverage measurement is still the 567-case baseline,
1,963/2,466 = 79.602595%; this increment did not rerun full coverage.
Other reported-state fixtures and the broader migration completion audit
remain, so the goal is active. No production Ginkgo cases were retired.
No commits, pushes, image publications, deployments or node/cluster policy
changes were made.

## 2026-09-14 — First expiry recovery must preserve the current phase

An isolated metadata-only expiry prototype exposed a missing assertion in the
current fixture. The production mutation returned the fresh pod UID and
resourceVersion from fallback Get but replaced its phase with the initial
pod's cached Pending phase. The original test passed: it checked the first
recovered object's UID/version, then checked phase only after a later replay
returned Succeeded and hid the corrupted first result.

The working fixture now checks that the first recovered phase equals the
independent healthy API read, alongside UID and resourceVersion. A mismatch
is retained as the scenario error before publishing/replaying the later
checkpoint. The existing final assertion therefore detects the first-recovery
failure instead of allowing a later correct event to repair the observation.

Only one predicate and its diagnostic change in steps/podwatch.go. All
feature inputs, phase transitions, existing identity/version checks, history
compaction, independent Expired/410/closure verification, post-deletion replay,
and the final Succeeded assertion are retained. No production source, case,
step definition or helper was added or removed.

The comparison deliberately included a prototype using two persisted annotation
updates instead of Running/Succeeded status updates. Its final phase expectation
was the unchanged API-default Pending phase. It passed both before and after
adding the same first-recovery check, including with the cached-phase fault.
Thus it cannot exercise changed-phase recovery. It was rejected, not installed
as a smaller but incomplete no-status replacement. Its source, binaries and
logs remain in the temporary evidence directory only.

| Fixture | Normal control | Fresh version with cached initial phase |
|---|---|---|
| Original working fixture | Pass | Pass: previously missed fault |
| Strengthened working fixture | Pass | Fails original final assertion with first-recovery identity/version/phase mismatch |
| Metadata-only prototype, original guard | Pass | Pass |
| Metadata-only prototype, strengthened guard | Pass | Pass: unsuitable replacement |

The three original recovery faults were also replayed against the strengthened
working fixture. Their historical production sources match current production
exactly before mutation:

- Unfixed expiry handling: original assertion fails with context deadline exceeded.
- Fallback Get leaves the old resume version: original assertion fails with
  post-deletion NotFound.
- Unsafe Pod cast before Status handling: original assertion reports the named
  conversion panic.

The current strengthened case therefore detects four production faults:
the three retained faults and one newly exposed phase-preservation fault.
The strengthened normal control passes in 9.353s. Across the eleven focused
runs, four intended assertion failures and seven passes match the matrix;
the prototype passes are evidence of inadequacy, not fault-detection credit.
Every run completes its recorder drain, including the expected failures.

Three native suites pass in 36.351520378s; default and live-tag vet pass.
Catalog patterns and inventory are unchanged: 566 cases (486 local + 80 live)
and 1,044 definitions. The independent audit checks the exact two source
replacements, unchanged feature inputs and production hashes, matched historic
fault sources, all focused case identities/outcomes/drains and empty final
namespace/process checks. git diff --check passes.

No full local/live suite or coverage rerun was needed for this assertion-only
change. The preceding burst increment remains the latest full-local/live-watch
regression evidence. The latest full coverage is still the 567-case baseline,
1,963/2,466 = 79.602595%; it is not a new measurement of this source.

Checked evidence: /tmp/brine-expiry-metadata.UzcDG3/evidence.json
SHA256: 65c375305dbf520d00633a8918570ee5b32dade265aa06e8add63cefc6fc6fb9
Normal adapter SHA256:
f178b15e1873cfe00050671d8154b2da554775a79c0d4201f682d815d472985d

The goal remains active. The expiry fixture still reports lifecycle states;
a future no-status replacement must preserve changed-phase recovery as well
as current UID/version and post-deletion replay. Other reported-state fixtures
and the broader completion audit also remain. No live pods, production Ginkgo
retirements, commits, pushes, images, deployments or node/cluster changes were
made in this increment.

## 2026-09-14 — Live startup after watch revocation exercises fallback

PW-06 now uses the existing gated live-pod setup instead of writing Running
through the status API. Its scenario moves to live/pod-watch.feature with
only the Given changed. The real permission grant/revocation, initial read,
established-watch checkpoint, TCP interruption and final Running assertion
retain their original wording and checks.

The fixture creates an owned namespace-scoped Role/RoleBinding and verifies
actual Get/Watch access using its restricted identity. It establishes the
production watch, revokes only watch permission, verifies Watch Forbidden
while Get still works, closes the established TCP stream and reopens the
same route address. Only then does it release the pod's scheduling gate.

The shared startup observer must see the original pod UID advance to a real
Running main container, with node placement, container ID and zero restarts.
The main remains behind its existing file gate until cleanup. Production
PodWatcher must recover Running by its fallback Get; no status, API error,
watch response or runtime result is supplied.

Shared fixture changes:

- The burst constructor is now named newLiveGatedWatch and serves both cases.
- startLiveWatchedPod extracts the burst's existing gate release/readiness/
  identity checks; the burst's terminal observation and draining stay unchanged.
- withWatchRoute extracts the API-only watch route and serves both local
  and live callers. Admin and restricted clients remain separate.
- apiRouteAddress shares the existing live exec route's explicit/default
  HTTP(S) port handling with watch routes.
- Existing checkpoint and RBAC revocation bodies are unchanged. No new
  scenario, definition, state type or alternative executor was added.

The first live control failed in the permission-setup step, before the final
runtime assertion. The selected Kubernetes URL is HTTPS without an explicit
port; the API-only raw-TCP route had passed its bare host to a dialer requiring
host:port, producing EOF. Existing live exec code already handled this.
Extracting/reusing that address logic corrected the route without broadening
permissions or changing server responses. The failed run is preserved as
excluded-missing-port.log, with complete namespace cleanup, and is not counted
as mutation evidence.

The corrected control passes (17.560s; API-only baseline 5.440s). All three
production-only faults preserve the exact original final assertion and error:

| Fault | Before and after failure |
|---|---|
| Disable fallback Get | watch fallback disabled |
| Return the cached initial pod | expected Running, got Pending |
| Get the wrong pod during fallback | missing-fallback-pod NotFound |

The four live comparison runs independently observe Running after the
permission-revocation action; all six intended mutant failures exit 1 at the
unchanged final assertion. Production sources remain untouched.

Final validation:

- The actual live watch feature passes all three cases in 44.360s: selector,
  burst and fallback, exercising the shared startup/gate/route callers.
- All 485 local cases pass in 141.087s, including remaining replay, cancellation
  and expiry paths. The excluded-live manifest takes another 5.089s and
  explicitly skips its 81 cases.
- Three native suites pass in 35.886433041s; default and live-tag vet pass.
- Inventory remains 566 cases (485 local + 81 live), with unchanged catalog
  patterns and 1,044 definitions.
- The independent audit reconstructs all four Go fixture transformations,
  verifies unchanged extracted checks, exact feature move and original
  assertion/fault mappings, and checks the full case roster with only the
  fallback case moving tiers.
- All eight owned live namespace UID pairs were removed, including the
  excluded setup run. All scenario drains complete; final namespace and
  recent process listings are empty. git diff --check passes.

Checked evidence: /tmp/brine-live-fallback.HVTj7F/evidence.json
SHA256: 8d0ef13abb439965a1d6c4ba87a6607a1c4078417644b611b9a2d9ea9da19083
Normal adapter SHA256:
34d950631dec5ab17804efe5cee81f4be5350c313a6b1e5da3f276d6426b3842

The latest full coverage measurement remains the 567-case baseline,
1,963/2,466 = 79.602595%; this increment did not rerun full coverage.
Reported replay/expiry and other lifecycle fixtures, plus the broader
completion audit, remain. The goal is active. No production Ginkgo cases
were retired; no commits, pushes, image publications, deployments, node
changes or cluster-wide RBAC/policy changes were made. Only the scenario's
owned namespaced read/watch grants were created, changed and removed.

## 2026-09-14 — Real lifecycle history is replayed after pod deletion

Both reconnect/replay rows moved to live/pod-watch.feature. Their names,
Running/Succeeded examples, initial-read step, interrupted-watch action
wording and final phase assertions are unchanged. Only the Given now uses
the existing gated live-pod setup.

The gated constructor creates its production PodWatcher through the shared
transparent TCP route before the initial read. The same watcher observes
its exact API-assigned checkpoint before that route closes. The original
admin configuration is kept separately, so actual startup and real SPDY
completion can proceed while the runtime stream is disconnected.

The existing startup observer verifies the original UID, Running main
container, node and zero restarts. The Succeeded row then releases the real
main's file gate and uses the shared exit observer to verify actual exit 0,
exact logs, timestamps, advancing version and the same container identity.
The burst now shares this completion operation; no alternate command runner
or status setter was added.

The fixture requests UID-scoped pod deletion with one second of grace, then
polls for NotFound before reopening the same TCP route address. A fresh Get
cannot satisfy either row because the pod is already absent. The setup bound
is increased from the API-only 15s to 60s for real lifecycle/deletion work,
within the live scenario's existing 90s lifetime. The post-reconnect read
remains bounded to five seconds.

Real kubelet history includes intermediate Pending updates, and Running
updates before Succeeded. The reader consumes only those legitimate phases
for the original pod. Crucially, it explicitly rejects the already-consumed
initial/checkpoint resource versions before skipping intermediates. Otherwise
a stale resume version could be hidden by draining its Pending checkpoint.
Unexpected identities, phases and errors reach the original final assertion.

RealWatch.setPhase had no remaining callers and is removed. No status write
remains in podwatch_real.go. The separate expiry fixture still reports its
lifecycle phases and remains migration work.

Production-only before/after overlays verify both rows against three faults:

| Fault | API-only rows | Real lifecycle rows |
|---|---|---|
| Keep the initial resume version | Both final assertions reject Pending | Both final assertions reject an already-consumed checkpoint |
| Do not reconnect after closure | Both report watch closed without reconnection | Identical failures |
| Substitute fallback Get for reconnection | Both report reconnect-pod NotFound | Identical failures |

All six paired scenario/fault detections are preserved. Stale-version
diagnostics intentionally become more precise; the original final assertions
remain unchanged. Both controls pass, and all twelve intended mutant
executions fail at those assertions rather than setup. Production sources
were never edited. No scenario or step definition was added.

Final validation:

- All five live watch cases pass through the actual feature in 68.076s:
  selector, burst, fallback and both replay rows.
- All 483 local cases pass in 141.788s. The excluded-live manifest takes
  another 5.146s and explicitly skips its 83 cases.
- Three native suites pass in 37.536640734s; default and live-tag vet pass.
- Inventory remains 566 cases (483 local + 83 live), with unchanged catalog
  patterns and 1,044 definitions.
- The independent audit reconstructs the exact two Go-file transformations,
  verifies unchanged assertions/examples and shared completion checks,
  proves each actual target version follows its consumed checkpoint, and
  matches original pod UIDs through confirmed absence before reconnection.
- The full scenario roster is unchanged; exactly the two replay rows change
  tiers. Every selected scenario has a complete recorder drain.
- All thirteen owned live namespace UID pairs across comparisons and final
  controls are removed. Final namespace/recent-process listings are empty.
  git diff --check passes.

Checked evidence: /tmp/brine-live-replay.t6FCst/evidence.json
SHA256: e3b798f5f26d5ddbaf73601a7358d0ccb8aa3affaafe2bb89bfed7d462c857e2
Normal adapter SHA256:
59bde3bf4ee3b5c28e0dcf7658ffe186270f951774cccf21d27ea4f42e80a7cc

The latest full coverage measurement remains the 567-case baseline,
1,963/2,466 = 79.602595%; no new full coverage run is claimed. Reported
expiry and other lifecycle fixtures and the broader completion audit remain,
so the goal is active. No production Ginkgo cases were retired. No commits,
pushes, image publications, deployments, node changes or cluster-wide
RBAC/policy changes were made.

## 2026-09-14 — Consolidate annotation recovery; preserve memory-only discrimination

The two local "After a restart the pod's own record is enough" rows (exit 0/3)
seeded completion annotations on pods that never executed. Their obligations
now live in the existing successful/failing supervised-task scenarios. BusyBox
runs the actual commands through production SPDY; a fresh runtime container
reads the completion annotation production wrote on the same surviving pause
pod. This models runtime-handle reconstruction, not a full ATC process restart.

No live step or assertion was removed, added or changed. The retired rows'
PE-11/PE-12 tags are preserved on both live cases. The annotation-seeding step
and its optional annotation input are removed. The remaining unfinished-pod
helpers collapse into recoverUnrecordedPod, preserving the exact Pending and
unreported phase inputs, status API writes/readback, identity check and original
Attach-refusal assertions. No phase-fidelity improvement is claimed for those
remaining negative cases.

The memory-only case is intentionally retained unchanged. Its pod does not
exist; only the property store can supply completion. A production-only
ignore-memory mutation fails its original Then with pod NotFound, while BOTH
existing real task cases pass using their production-written annotations.
That surviving fault proves those live cases cannot replace this distinct
memory-first obligation. Passing the earlier corrupt-memory-value fault was
not enough evidence to retire it.

Paired current-source controls and mutations:

- Old control: three seeded cases pass; live control: two existing tasks pass.
- Ignore annotation reader: both old exit-code rows and both live tasks fail
  their recovery exit assertions with missing completion status.
- Corrupt annotation exit by +1: both old rows and both live tasks fail their
  recovery exit assertions, observing 1/4 instead of 0/3.
- Every intended fault reaches the original Then after passing its prerequisites.
  Two faults preserve four paired scenario/fault detections. The separate
  ignore-memory comparison has the one intended old failure and two live
  survivors; those survivors are recorded, not credited as detections.
- Same-pod UID receipts independently identify command execution, current-handle
  recovery where present, and fresh-handle recovery.

Final inventory: 564 scenarios = 481 local + 83 live; 1,043 definitions.
This removes two scenarios and one definition, adding none.

Validation: all 481 local cases pass (140.353s; excluded-live manifest 4.707s),
all twelve cases in the actual live task feature pass (220.262s), all three
native suites pass (36.102074516s), and default/live vet pass. Every selected
scenario drains completely. All twenty owned live namespace UID pairs from
the matrix and final live feature are removed; final namespace/recent-process
lists are empty. git diff --check passes.

Checked evidence: /tmp/brine-recovery-consolidate.K5tEeW/evidence.json
SHA256: 10cbd33e3aa69dacb906d9e1250996f58ce43e18a1a204b8f36c6ad4c2b7d125
Normal adapter SHA256:
5f01953ac2e0bac999e6e5394408e61ba53e67c6a4e216e43d5315e9bdf0894d

The verifier checks exact source transformations, the catalog delta, all
unchanged live executable steps, the exact local roster minus the two rows,
production-only mutation overlays, fault locations and cleanup. An initial
audit newline expectation was corrected to match the actual patch operation;
no test behavior was changed to satisfy the audit. The final feature comment
says completed task, not completed pod: its pause pod survives.

The latest full coverage measurement remains the historical 567-case baseline,
1,963/2,466 = 79.602595%; no fresh full coverage measurement is claimed.
Memory-only seeded completion, reported expiry/lifecycle inputs and the broader
completion audit remain. The goal is active. No production source or Ginkgo
case changed. Work remains uncommitted on core; no push, publication,
deployment, node change or cluster-wide RBAC/policy change occurred.

## 2026-09-14 — Real completion replaces seeded memory-only recovery

The previous consolidation correctly retained the memory-only case: the live
task's annotation masked an ignored property-store read. That gap is now closed
without dropping its no-pod requirement.

The existing successful live task retains its actual runtime container and
API-assigned pod UID across fresh-container recovery. After all original
assertions pass, one added action UID-deletes that exact owned pause pod with
a one-second grace, observes real NotFound, calls production Attach/Wait on
the original runtime object, and verifies that the pod remains absent.
The existing task-exit assertion then requires exit 0. Completion comes from
the actual BusyBox command and production Wait, never a supplied property or
annotation. The original pod-survival check still happens before deliberate
deletion. Fresh-handle recovery remains a runtime-object model, not a claim
that a full ATC process was restarted.

Attach result capture is shared between the existing present-pod recovery
and the new absent-pod path. It still delivers business errors to the existing
exit assertion. Two private TaskOutcome fields retain the real container and
UID; no fixture result, client response or executor is substituted.

The seeded "A step the runtime still remembers is not run again" scenario
and its setup/assertion definitions are removed. The exact Pending/unreported
missing-record cases and arbitrary-property readback are unchanged. The live
scenario gains two steps, not a separate case. Net inventory: 563 scenarios
(480 local + 83 live), 1,042 definitions — one fewer scenario and one fewer
definition. JB-container-053's disposition now points at the final live
no-pod recovery assertion; no additional Go test is retired.

Both old/new controls pass. Four production-only faults fail both versions:

- Ignore memory: the replacement now fails its final exit assertion with real
  pod NotFound, rather than surviving through annotation fallback.
- Require pod existence before using memory: same final NotFound failure.
- Corrupt cached exit only when the pod lookup fails: reaches the final
  assertion and reports 1 instead of 0.
- Corrupt every cached exit: the replacement fails its earlier current-container
  exit assertion. It does not reach deletion; this is explicitly distinguished
  from the three final-assertion detections above.

All fault prerequisites pass, and each run fails at its intended Then.
UID receipts correlate actual command completion, present-pod reattachment,
confirmed absence, and the final memory-only result. The source audit verifies
every original live step/assertion remains, the precise local roster minus
the one retired case, the catalog delta, and unchanged production sources.

Final validation: 480/480 local cases pass (141.980s; excluded-live manifest
5.115s), the actual live task feature passes 12/12 (217.650s), all three native
suites pass (36.247791677s), and default/live vet pass. Every selected scenario
drains completely. All seventeen owned live namespace UID pairs across the
matrix and final feature are removed. Final namespace/recent-process listings
are empty; git diff --check passes.

Checked evidence: /tmp/brine-memory-real.k6UCQB/evidence.json
SHA256: 93039333d35613f1ec2e7e506d6de6b9feec09b98c253509d15f8661dcd1759c
Normal adapter SHA256:
6589a374400ae97f68ff07c6d1219b6e5476f33dbcfa5c901332ed7555e97632

The full coverage measurement remains the historical 567-case baseline,
1,963/2,466 = 79.602595%; no new full coverage measurement is claimed.
Reported missing-completion/expiry/lifecycle inputs and the broader completion
audit remain. Read-only inspection also confirmed active GC repository doubles:
noOrphanLookup, noFailedDestroy and noMissingDelete in gc_containers.go, and
faultedPipelines/faultedJobs and error-returning wrappers in gc_pipelines.go.
Those are a concrete next no-fakes target; they were not changed here.

The goal remains active. No production source, commit, push, image publication,
deployment, node or cluster-wide RBAC/policy change occurred. Work remains
local and uncommitted on core.

## 2026-09-14 — Container-GC errors come from real PostgreSQL

All three container-GC repository error-returning doubles are removed:
noOrphanLookup, noFailedDestroy and noMissingDelete, along with failOneGCStep
and errGCStepUnavailable. The earlier comment claiming PostgreSQL could not
isolate these failures was incorrect and is replaced.

The existing scenarios now use ordinary production db.ContainerRepository
objects on separate, one-session connections to the owned scenario database:

- Orphan lookup: an owned NOLOGIN role has SELECT revoked on builds.
  The preceding dirty-in-memory cleanup and other stages retain their access.
- Missing-container deletion: another owned role has DELETE revoked on
  containers; the other three stages can still SELECT/UPDATE their rows.
- Failed-container cleanup: the feature names one actual failed container.
  A competing PostgreSQL transaction holds that row FOR UPDATE; the collector's
  250ms lock timeout fails its failed-row update while other rows remain writable.

Independent zero-row SQL probes verify real permission errors (42501), and
an update of the exact held row verifies actual lock timeout (55P03) between
distinct backend PIDs. The collector itself remains unwrapped. A three-second
statement timeout bounds each dedicated connection. Recorder disposal closes
connections, drops only the fresh role's owned privileges and role, or rolls
back the row lock; each role's absence and each lock's release are checked.

Every original scenario and row-outcome assertion remains. The failed-stage
case gains one existing container-creation step and replaces its generic
failure phrase with a competing-transaction phrase naming that row. No new
scenario or definition is added: 563 scenarios = 480 local + 83 live;
1,042 definitions. The shared failure assertion now also requires the real
database SQLSTATE instead of accepting any unrelated non-nil error.

Validation against the captured wrapper-based fixture:

- Both complete ten-case controls pass: old 6.553s, new 6.740s.
- Three production-only early-return faults preserve their position-specific
  failures: failed-container destruction, missing-container deletion, and
  excess-check destruction respectively.
- Swallowing the accumulated error preserves all three failure-reporting
  assertion failures. These four faults preserve six paired scenario/fault
  detections. All failure prerequisites pass.
- An additional cause-loss fault returns context.Canceled instead of the actual
  database error. Old Brine passes all ten cases; new Brine fails exactly the
  three SQLSTATE assertions. These three detections are new evidence, not
  paired detections or a claimed per-Go-leaf replay.

Diagnostic row order follows generated handles, so the verifier compares exact
row sets in the two affected diagnostics. Only the intentional added
locked-failed-container is excluded from the old/new failed-stage diagnostic
comparison; both complete sets are checked first. No business assertion was
changed to accommodate the faults.

Final validation: 480/480 local cases pass (141.295s; excluded-live manifest
4.949s), the actual GC feature passes 10/10 (6.709s), all three native suites
pass (36.998258741s), and default/live vet pass. Every selected scenario drains
completely. Across new controls, faults, full local and final GC, all sixteen
owned NOLOGIN roles and eight held row locks are released with verification.
No recent active test/API/etcd/collector processes remain. git diff --check passes.
An initial nonunique source-edit anchor stopped before the field edit or any
build; narrowing it to ContainerGCReady resolved it without changing VolumeGCReady.

Checked evidence: /tmp/brine-gc-real-errors.k5B2jy/evidence.json
SHA256: 35752176d8dbbd81203cca015b6a5b633faa7b3d18b7419dfd957e1b78944e07
Normal adapter SHA256:
716337571de50b6c7b9511dafcfff6615ee38a76892d418e6191485613d0efde

The verifier checks exact source transformations, all retained feature steps,
the unchanged local scenario roster, the one-for-one vocabulary change,
production-only overlays, additional baseline-fixture overlay for cause loss,
failure locations, SQLSTATE/role/lock receipts and cleanup. README and
DISPOSITION-gc-lidar.md document this replacement without changing the
REFUTED legacy statuses: exact cause-text and per-Go-leaf retirement evidence
remain separate obligations. No production source or legacy Go test changed.

The latest full JetBridge coverage remains the historical 567-case baseline,
1,963/2,466 = 79.602595%; no fresh full coverage or live-suite run is claimed.
Pipeline-GC wrappers, reported lifecycle inputs and the broader completion
audit remain. The goal is active. Changes remain uncommitted on core; no push,
publication, deployment, shared-cluster, node or cluster-wide RBAC change occurred.

## 2026-09-14 — Pipeline log writes fail through real contention

The two write-error wrappers undeletableEvents and unmovableCursor are removed.
Their existing outline rows now use ordinary production pipeline factories and
database objects. No feature sentence, example row, assertion or definition
changes; only the explanatory feature comment is updated.

After sweep initializes the job cursors, the new helper uses the shared bounded
GC connection setup and a separate competing transaction:

- Event deletion takes a SHARE lock on the first pipeline's actual
  pipeline_build_events_<ID> table. Readers remain available, but the production
  DELETE cannot obtain its conflicting table lock.
- Cursor advancement takes a NO KEY UPDATE lock on the first job row. This
  blocks its cursor update while permitting unrelated foreign-key checks and
  the second pipeline's writes.

Zero-row deletion and same-value cursor-update probes independently establish
real SQLSTATE 55P03 refusals between distinct PostgreSQL backends. These probes
do not supply repository results. The original collector then executes using
its real factory/session with 250ms lock and three-second statement limits.

A production lager writer sink records the actual swallowed errors. The audit
requires failed-to-delete-build-events or failed-to-update-first-logged-build-id,
respectively, with the real lock-timeout SQLSTATE. This is recorded-run evidence,
not a newly added feature-level logging assertion or a claim of per-Go-leaf
retirement. All original event-fate, cursor and healthy-neighbour assertions
remain exactly as written.

Both four-row controls pass. Three production-only faults retain seven paired
scenario/fault detections: five on the two migrated write paths and two on the
unchanged build-history read-error row.

- Propagating a per-job error from the whole sweep fails its successful-sweep
  assertion. For the migrated rows the cause is now PostgreSQL's lock timeout,
  not the old supplied sentinel.
- Silently returning from the whole sweep leaves other-older unreaped, failing
  the original healthy-neighbour assertion.
- Advancing the cursor after failed event deletion reports newer instead of
  older at the original cursor assertion.

All intended failures occur at the original assertions after their prerequisites
pass. Exact old/new feature-step sequences and the complete catalog are equal.
Inventory stays 563 scenarios = 480 local + 83 live, with 1,042 definitions.

Final validation: 480/480 local cases pass (142.692s; excluded-live manifest
4.865s), the complete actual pipeline feature passes 25/25 (10.858s), all three
native suites pass (37.652611399s), and default/live vet pass. Every selected
scenario drains completely. All twelve new table/job-row locks across controls,
mutants, full local and final pipeline feature are rolled back and release is
checked via pg_locks. No recent active test/API/etcd/collector processes remain.
git diff --check passes.

Checked evidence: /tmp/brine-log-contention.WAgVeF/evidence.json
SHA256: 83b349e1f6b3d8af0d78b553bc02f9f38cdb08f1609812f2fe2eae1042e0df4a
Normal adapter SHA256:
7b61fa68729080bed712c6d44520c16e398e80e1ab5e492278a0caa6a04657fa

The verifier checks exact source transformations, unchanged shared GC connection
code, all executable feature steps and the full local roster, unchanged
production sources, overlays, failure locations, operation-specific production
error logs, lock identities and cleanup.

unlistableJobs and unlistableBuilds still return supplied errors through
faultedPipelines/faultedJobs; they remain explicit migration work. Read-only
inspection confirms AllPipelines reads pipelines ordered by team/ordering,
while Pipeline.Jobs reads jobs filtered to its pipeline. Any transient-lock or
query-cancellation replacement must preserve the exact failing read boundary
and restore access for the healthy neighbour, not move the error to an earlier
factory/configuration call.

Reported lifecycle fixtures and the broader legacy-test audit also remain.
The full JetBridge coverage measurement is still the historical 567-case
baseline, 1,963/2,466 = 79.602595%; no fresh full coverage or live-suite run is
claimed. The goal is active. Work remains uncommitted on core. No production
source or Go test changed; no commit, push, publication, deployment, shared-cluster,
node or cluster-wide RBAC change occurred.

### Pipeline log reads fail through real PostgreSQL cancellation

The two remaining pipeline read-error wrappers, unlistableJobs and
unlistableBuilds, and their faultedPipelines/faultedJobs decorators are removed,
along with errLogFault. All four rows of the existing failure-isolation outline
now use plain production database factories and real PostgreSQL errors.
No executable feature step, example, scenario or definition was added or changed.

After cursor initialization, the read fixture opens distinct owned collector
and lock-holder sessions on the scenario's private database. It holds an ACCESS
EXCLUSIVE lock on jobs or builds, then runs the actual production collector.
An independent admin session identifies the active collector query waiting for
that exact relation lock and confirms that the owned holder is its blocker.
It revalidates backend PID, database, query start time, captured SQL and blocker
atomically before issuing pg_cancel_backend. It waits for that original query
to end before releasing the lock, allowing the healthy pipeline to continue.

The first control found a safe fixture-guard failure: pg_stat_activity truncates
the long build SELECT at its default 1KB, before FROM builds. That failed run is
retained as rejected-truncated-query-guard.log. The final guard identifies the
build projection plus the exact ungranted builds relation lock. Atomic query
identity checks remain; the runner and production sources were not modified
to change PostgreSQL's startup configuration.

The collector uses a ten-second statement timeout and waits for lock restoration;
coordination and cleanup are bounded. Recorder cleanup is registered immediately
after opening the holder transaction, and idempotent release verifies no remaining
relation/transaction locks for its PID. Fixture coordination failures are returned
separately from the production result, so original assertions still decide whether
the collector continued correctly. No helper implements a repository interface.

Both four-row controls pass; the new control takes 6.543s. Actual production
logs identify failed-to-get-dashboard and failed-to-get-job-builds-to-delete
with SQLSTATE 57014, canceling statement due to user request. The unchanged
event-delete and cursor-write rows still produce real lock timeouts, 55P03.
These operation/error logs are verified in the evidence audit, not newly added
feature-level assertions or a claim of legacy Go test retirement.

Three production-only mutations retain seven paired scenario/fault detections:
three on the migrated read paths and four on unchanged write paths. Stopping
after a pipeline read failure leaves the healthy pipeline unreaped. Propagating
a per-job error fails the original successful-sweep assertion; returning cleanly
instead fails the healthy-neighbour assertion. Old and new failures occur at
the same existing assertions after their prerequisites pass. The propagated
history error is now PostgreSQL's cancellation rather than the supplied sentinel.

Final validation: 480/480 local scenarios pass in 144.104s, with the excluded-live
manifest taking 4.728s; the actual complete pipeline feature passes 25/25 in
10.823s. All three native suites pass in 36.205331733s, and default/live vet pass.
Inventory remains 563 scenarios = 480 local + 83 live, with 1,042 definitions.
The complete catalog, executable feature steps and local roster are unchanged.

All twelve cancelled real reads and twelve unchanged write locks across the
new control, three mutants, full local suite and final pipeline run are released
and checked. Recorder drains complete; no recent active test/API/etcd/collector
processes remain. git diff --check passes.

Checked evidence: /tmp/brine-log-read-errors.8j0gcU/evidence.json
SHA256: 1ff783320f99b1537881353d110a3a28b742005520d4d9207fad69c34aa84b1f
Normal adapter SHA256:
b48a3d0855c63d257dd97c4f57ab49a445cc5a68e4c97ef990fe9ebd3c278504

Reported lifecycle fixtures and the broader legacy-test audit remain. The full
JetBridge coverage figure is still the historical 567-case baseline,
1,963/2,466 = 79.602595%; no fresh full coverage or live-suite run is claimed.
The goal remains active. Work is uncommitted on core. No production source or
Go test changed; no commit, push, publication, deployment, shared-cluster,
node or cluster-wide RBAC change occurred.

### RF-07 uses a real scheduler CPU refusal

The scheduling-timeout scenario moves from failure-priority.feature to the
existing live/startup-failure.feature. It no longer writes a PodScheduled
condition through updateTaskPodStatus. A real scheduler now rejects an owned
Pending pod, and the production resource execProcess handles that refusal.

The fixture reads actual node identities and allocatable CPU. One pod requests
1 CPU more than the largest observed node's entire allocatable capacity
(13 CPUs against theborg's 12 in these runs). Required node affinity restricts
eligibility to those observed names. This is not a capacity-saturation load:
the pod cannot fit even on an empty eligible node and never binds or executes.
Only its disposable namespace's CPU admission cap is raised; the one-pod and
existing memory/storage quotas remain. The fixture waits for quota-controller
status to publish the new cap before creating the pod.

The first probe attempted PreemptionPolicy Never directly, which priority
admission rejected because that policy must come from a matching PriorityClass.
The cluster has no non-preempting class. No class was created or modified.
Instead the final fixture uses default priority and makes every eligible node
infeasible even after hypothetical removal of all other pods. Kubernetes
excludes those nodes from preemption:
https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/#inter-pod-affinity-on-lower-priority-pods

The actual condition and UID-matched FailedScheduling event must agree exactly
on an Insufficient cpu refusal with no feasible preemption outcome. The pod
must retain its UID, unbound Pending phase, no container status and no nominated
node before and after Wait. The observed scheduler text in all final runs was:
0/1 nodes are available: 1 Insufficient cpu. no new claims to deallocate,
preemption: 0/1 nodes are available: 1 Preemption is not helpful for scheduling.

The original 2-second startup and 3-second scheduling budgets remain. The
fixture prepares the scheduler refusal before starting the runtime deadline;
an independent context bounds Wait at the larger budget plus five seconds.
Actual Wait durations in the control, mutants and final feature are at least
3 seconds and below that independent bound. The measured duration is evidence,
not a newly introduced feature assertion.

All original assertions are retained except the old literal invented
0/3-nodes message, which is replaced by an exact comparison with the real
scheduler's complete refusal. Both Unschedulable and pod scheduling timeout
classification checks and both waiting-warning phrases remain. Two old
definitions (the supplied-status action and its sole-consumer budget refinement)
are replaced by two definitions (the parameterized live action and full-refusal
assertion). No scenario or net step-definition increase results.

Both controls pass. Three production-only faults retain three paired
scenario/fault detections: wrong scheduling classification, omitted waiting
warning, and replaced detailed refusal. All intended failures occur at
Then/And assertions after live setup succeeds. The first two retain their
exact assertion text; the detail fault maps from the old literal-message check
to the new complete-real-refusal check. The production source is unchanged.

Both rejected setup probes remain separately recorded: the priority-admission
refusal and the quota-controller publication race. Neither is counted as a
mutation detection. Their namespaces and recorder disposers also cleaned up.
A cleanup audit initially expected JSON for an all-absent multi-name kubectl
query; name-output verification now records successful empty output instead.

Final validation: 479/479 local scenarios pass in 140.205s; the excluded-live
manifest takes 4.878s. The complete actual live startup feature passes 8/8 in
144.405s, including its seven unchanged neighboring cases. Three native suites
pass in 36.515707636s, and default/live vet pass. Inventory remains 563 scenarios
and 1,042 definitions, now split 479 local + 84 live. All other catalog entries,
neighboring feature contracts and the local roster are preserved.

All fourteen created live namespaces, including the rejected probes, have
matching UID cleanup receipts and a fresh exact-name absence check. Recorder
drains are complete; no recent active test/API/etcd/collector processes remain.
git diff --check passes.

Checked evidence: /tmp/brine-real-scheduling.cOw1W3/evidence.json
SHA256: 142f9b9ee9ca322cb01765a51b575c95c2e8d2ceed3bdeea2a78475d7e71eab8
Normal adapter SHA256:
f900b3703feac0d0ab8e1b346da3b904596f99e5fe33899e0a817f9328b7b3db

The pre-existing Pending pod deliberately exercises production resource
container reuse and waiting, not fresh production pod construction. No
retained Go leaf is retired by this slice; reported lifecycle fixtures and
the broader migration audit remain. Full JetBridge coverage is still the
historical 567-case baseline, 1,963/2,466 = 79.602595%; no fresh full coverage
or complete live-suite pass is claimed. The goal stays active on core.
Changes are uncommitted and unpushed. No production source or Go test changed;
no node, PriorityClass or cluster-wide RBAC write, publication or deployment
occurred. Shared-cluster mutations were confined to the owned test namespaces.

### RF-05 uses actual pod-local kubelet eviction

The basic eviction scenario moves from failure-priority.feature to
live/compatibility-process.feature. Its action no longer sets Failed/Evicted
status. A fixed BusyBox command writes 24MiB into a disk-backed emptyDir whose
sizeLimit is 16MiB, and the actual kubelet evicts that pod for exceeding the
volume limit. This does not manufacture node pressure or fill disk indefinitely.

The fixture owns one namespace and one pod with the existing namespace quotas,
250m/64Mi CPU/memory limits and a 32Mi ephemeral-storage request/limit. The
16Mi volume and fixed 24Mi write deliberately distinguish the volume limit from
the larger pod storage limit. It observes the actual Running writer, its API
UID, node, container ID and exact scratch-bytes=25165824 output. It then requires
Failed/Evicted on that same pod plus a UID-matched Warning/Evicted kubelet event
whose message exactly matches the pod's volume-specific reason:
Usage of EmptyDir volume "scratch" exceeds the limit "16Mi".

The existing production compatibility Container.Attach returns Process, and
Process.Wait consumes that actual eviction state. This is the explicit legacy
compatibility path, not a claim that normal production task execution uses it.
The original assertions remain verbatim: the error names evicted and the build
log explains Evicted. The older RF-05 specification's failure-vs-interruption
wording drift is preserved in the moved feature comment.

The first real control passed, but one initial wrong-reason mutant failed in
setup on an incidental RestartCount==0 guard. That is not counted as a mutation
detection. Inspection of the matching Kubernetes v1.34.3 kubelet source explains
that a missing formerly Running container can become ContainerStatusUnknown
with an incremented restart counter; it does not establish another command run:
https://github.com/kubernetes/kubernetes/blob/v1.34.3/pkg/kubelet/kubelet_pods.go#L2364-L2375

The final fixture checks zero restarts during the observed initial Running
execution and records terminal container status separately. Its fresh control
actually observed LastTerminationState=ContainerStatusUnknown, exit 137 and
RestartCount=1 after eviction, while the pod reason and event remained the real
volume-limit eviction. No status is rewritten to make this pass.

Fresh old/new controls pass. Three production-only faults retain three paired
scenario/fault detections, with identical original assertion failures:
taking the exit code before eviction classification, reporting node_lost instead
of evicted, and omitting eviction diagnostics. Setup and action prerequisites pass
for every counted mutation; no fixture failure substitutes for a detection.
The initial control/matrix/full-feature logs are retained separately.

One definition replaces one, with no added cases or definitions. Inventory
remains 563 scenarios and 1,042 definitions, now split 478 local + 85 live.
All other catalog entries and neighboring executable feature contracts are
unchanged. The inherited RF-09 requirement tag is retained on the moved RF-05
case and restored on RF-07, whose preceding move had dropped that parent tag.
The local feature's stale reported-scheduling introduction is also corrected.

Final validation: 478/478 local scenarios pass in 139.876s, with the excluded-live
manifest taking 5.266s. The full actual compatibility feature passes 10/10 in
143.001s. Three native suites pass in 37.049102979s; default and live vet pass.
All twenty-eight namespaces from the initial and corrected runs have matching
UID cleanup receipts and a fresh exact-name absence check. Final recorder drains
complete, no recent active test/API/etcd/collector processes remain, and
git diff --check passes.

Checked evidence: /tmp/brine-live-eviction.xSPwW3/evidence.json
SHA256: 60451cbed59592935e9d1d3d8cf7c6f509a8941298397df8bb9b6451eb075d1e
Normal adapter SHA256:
aa20b3bce4b78a7b80fd2fdb34a7e109c870e28d47355332c5722d66756e9f68

The separate node-pressure, spot/drain and other reported lifecycle fixtures
remain migration work; a pod-local volume eviction is not substituted for those
distinct contracts. No retained Go test is retired. Full JetBridge coverage
remains the historical 567-case baseline, 1,963/2,466 = 79.602595%; no fresh
full coverage or complete live-suite pass is claimed. The goal remains active.
Work is uncommitted and unpushed on core. No production source or Go test changed,
and no node, cluster-wide RBAC, publication or deployment change occurred.
Shared-cluster writes were confined to the owned test namespaces.

### Pending recovery uses the API default; image-pull eviction probe rejected

Unfinished Pending recovery no longer calls UpdateStatus. The pod is created
through the real owned API, and the fixture reads back its actual Pending phase,
same UID and unchanged resource version before Attach. The separate unreported
phase still explicitly clears status and is not claimed migrated. Both original
refusal cases, assertions and executable feature steps remain unchanged.

The unused markPodRunning helper is removed after a whole-repository consumer
search found only its declaration. It contributed no scenario coverage and no
step definition, so removing it does not change the vocabulary or case inventory.

Two production-only false-success mutations discriminate the two inputs:
assuming Pending is complete fails only the Pending row, and assuming an empty
phase is complete fails only the unreported row. Each fault has the same original
assertion failure before and after this change. Both two-row controls pass.
Five new Pending receipts across control, mutants, full local and complete
recovery feature identify the API-created UID and unchanged resource version.

Final validation: 478/478 local scenarios pass in 141.532s; the excluded-live
manifest takes 4.479s. The complete actual recovery feature passes 3/3 in 5.206s.
All three native suites pass in 37.039557814s, and default/live vet pass.
Inventory remains 563 scenarios = 478 local + 85 live, with 1,042 definitions.
The full catalog, local roster and executable recovery feature steps are unchanged.
All recorder drains complete, no recent active test/API/etcd/collector processes
remain, and git diff --check passes.

Separately, two temporary Go-overlay probes tested whether the bounded volume
eviction could replace the remaining Failed/ImagePullBackOff priority fixture.
An auxiliary BusyBox writer performed the same fixed 24MiB write into a 16MiB
volume while main requested a scenario-unique nonexistent image tag. In both
runs the observer genuinely saw Failed/Evicted with main still waiting in
ImagePullBackOff and with no termination history. But Process.Wait then reported
pod interrupted: evicted instead of the original image-pull failure.

The second probe recorded the production diagnostics, which show the reason:
by the runtime's own initial read, main was Terminated/ContainerStatusUnknown
(exit 137), not Waiting/ImagePullBackOff. isPodFailedFast deliberately defers
to the exit/interruption path when main has terminated. Thus the observer's
earlier snapshot does not establish the input the production reader consumed.
No production bug or faithful migration is inferred from that mismatch.

This replacement is rejected. The original image-pull-priority case remains
untouched; no status is frozen, no older response is injected and no weaker
assertion substitutes for it. Both probe namespaces have matching UID cleanup
receipts and a fresh exact-name absence check. Repository live_eviction.go is
byte-identical to the before-probe snapshot; the probe adapter and manifests
remain isolated under /tmp/brine-live-pull-priority.IOKtsK.

Checked evidence: /tmp/brine-pending-default.s8U0FM/evidence.json
SHA256: e1e0cc0300793f409a6979191c40a91130a9a965bc1742ff039e6df6f923e4f8
Normal adapter SHA256:
e5a3d1c6ff5f1d164581ab8cefe3243646d6bfe333514812372f7f8340cfd3c6

Explicit unreported recovery, image-pull priority, expired-watch phase input,
node diagnostics and other reported lifecycle fixtures remain, along with the
broader legacy-test audit. No Go test is retired. Full JetBridge coverage is
still the historical 567-case baseline, 1,963/2,466 = 79.602595%; no fresh full
coverage or complete live-suite pass is claimed. The goal remains active.
Changes are uncommitted and unpushed on core. No production source or Go test
changed; no node, cluster-wide RBAC, publication or deployment change occurred.
The only shared-cluster writes were inside the two owned probe namespaces.

### Deadline image-pull priority probe: possible state, unreliable replacement

A bounded owned-pod experiment tested ActiveDeadlineSeconds as a replacement
for RF-09's reported Failed/ImagePullBackOff/no-termination-history input.
It used one pod with a scenario-unique missing BusyBox tag and a second,
resource-limited BusyBox container. The pod deadline was 30s; the helper
handled SIGTERM, waited 20s and exited within the 25s grace period. Namespace
quota and container limits were unchanged. No node disruption, status write,
volume-filling operation or cluster-wide policy change was used.

The isolated Go-overlay probe passed in 63.646s including setup and cleanup.
Unlike the preceding eviction probe, production Process.Wait's own diagnostics
really did show Failed/DeadlineExceeded with main Waiting/ImagePullBackOff;
its error named ImagePullBackOff. The matching UID's kubelet events confirmed
both the pull refusal and deadline. This establishes that the required state
can occur, not that the fixture is reliable.

A subsequent control integrated the mechanism into the existing shared
startup-failure helper and reused its registered step, temporarily moving the
original scenario without adding a case or definition. That control failed
in 63.452s: before runtime evaluation, the kubelet supplied main
Terminated/ContainerStatusUnknown (137), not the required waiting state.
The prerequisite guard rejected it. The successful probe therefore does not
justify retiring the original test or relying on this timing window.

The integration was withdrawn. Both feature files and the shared fixture are
byte-identical to their pre-turn snapshots. The normal adapter was rebuilt
and its SHA256 is also identical to the previous checkpoint:
e5a3d1c6ff5f1d164581ab8cefe3243646d6bfe333514812372f7f8340cfd3c6.
Temporary probes, rejected source snapshots and overlays remain under /tmp.

The restored original case passes. Two production-only mutations independently
make its original Then fail: exit-code-before-waiting priority returns false
success; loss of the waiting reason loses ImagePullBackOff. Four old/new
mutation adapters were compiled, but only the original-case mutations were
executed after rejecting the integration. No paired live mutation credit is
claimed. The entire restored priority feature passes 4/4 in 5.154s.

Both owned namespace UIDs have cleanup receipts and a fresh exact-name
absence check. All recorder drains complete. The catalog is unchanged at
1,042 definitions; inventory remains 563 = 478 local + 85 live. No production
source or Go test changed, and no case or double was retired.

Checked evidence: /tmp/brine-live-deadline-priority.yibziJ/evidence.json
SHA256: 4439b9ca06be6704db84b6e80b5a3f0b6e67e0b755a54d6c7bf858192911fd27

This was a fixture-fidelity failure, not a permission refusal. The ordinary
sandbox still failed to launch, but scoped escalation allowed the work.
No full-suite or coverage rerun occurred; 79.602595% remains the historical
full-run measurement, not a fresh result. The broader goal remains active,
and all repository work remains uncommitted and unpushed on core.

### Retire the redundant scheduling-timeout Go leaf with per-assertion evidence

The real scheduler fixture now supplies ResourceType=git to the production
worker, leaves the container working directory unset as in the original,
and derives its pod image from the declared default git resource mapping.
An empty mapping fails setup. It retains get execution, /opt/resource/in,
the /tmp/build/get argument, JSON stdin, and startup=2s / scheduling=3s.
The feature steps, assertions, scenario inventory and catalog are unchanged.

The previously restored JB-process-023 leaf is removed from
process_restored_test.go. Its earlier REFUTED/RETAIN findings are preserved
in DISPOSITION-jetbridge.md as history; the new per-test entry supersedes
them for this leaf only. The separate core cancellation leaf, including its
exact zero-grace deletion assertion, remains byte-for-byte unchanged.
The verifier reconstructs the final Go file from the exact old source minus
only this leaf, its two unused imports and the updated header.

Both the exact Go leaf and the real-scheduler Brine case pass their controls.
Five isolated production-only mutations cover all five outcome assertions:

| Fault | Original Go assertion line | Existing Brine check |
| --- | --- | --- |
| Swallow the wait-for-running error | 121: error must occur | the step fails naming Unschedulable rejects success |
| Drop the scheduling-timeout prefix | 122 | the step fails naming pod scheduling timeout |
| Drop the Unschedulable classification | 123 | the step fails naming Unschedulable |
| Drop the waiting notice | 124 | the build log explains waiting up to |
| Drop the cluster-resources notice | 125 | the build log explains cluster resources |

Every mutation selects exactly one Go It and fails at its intended original
assertion, not at setup. Every matching Brine run fails only the corresponding
Then/And. These are five per-leaf pairs, not a file-level both-red claim.
The real scheduler supplies the same-UID unbound Pending refusal and matching
event, and the resource never executes. No fake executor/client or supplied
PodScheduled condition is used by the Brine case.

The fixtures are not identical: the legacy fake test creates its pod through
Run, whereas the live fixture supplies a pre-created pod that cannot fit any
eligible node. This retirement covers the five asserted scheduling outcomes,
not universal mutation equivalence or identical construction setup. The
separate container-spec.feature image-construction contract remains present
and passes in the full local run. The real production pod-construction path
would be a useful further consolidation of the scheduling fixture.

Final validation:
- The remaining JetBridge Go suite passes 97/97 Ginkgo specs in 61.444946269s
  including its ordinary Go tests; the exact Ginkgo roster is the prior
  roster minus this one leaf.
- The affected actual startup feature passes 8/8 in 151.281s.
- Full local Brine passes 478/478 in 139.307s; the excluded-live manifest
  takes 4.287s and skips 85 cases.
- All three nested native suites pass in 35.835485681s; default/live vet pass.
- Catalog remains 1,042 definitions; inventory remains 563 = 478 local + 85 live.
- All fourteen owned namespaces have matching UID cleanup receipts and fresh
  exact-name absence checks. All scenario recorder drains complete.

Two harness errors were corrected without test-result credit: duplicate
canonical overlay paths prevented the initial mutation compilation, and the
first post-deletion compile found an unused Kubernetes status import.
Neither was a runtime regression or permission blocker.

Checked evidence: /tmp/brine-scheduling-retirement.sE3VjA/evidence.json
SHA256: f84238af9b73c8fe5efed1370f0bbbff00743c9588727239268ffd307e75ef90
Normal adapter SHA256:
ac229ec1191f6611d838c7d4ff26fa56a748ca6d0cb93fb6c1381c3e57f2e26b

No production source changed. One mock-backed legacy Go leaf is retired;
the remaining lifecycle status fixtures and broader retention audit are still
work. No fresh full-suite coverage or complete live-suite pass is claimed.
The historical coverage measurement remains 1,963/2,466 = 79.602595%.
The goal remains active. Changes are uncommitted and unpushed on core.

### Scheduling refusal now starts with a production-created resource pod

The scheduling fixture no longer creates its own PodSpec. It passes the
bounded CPU/memory values through runtime.ContainerLimits, selects
ResourceType=git, and lets the real worker's Run create the pod. It then
reads that actual pod and verifies its identity, image, CPU requests/limits
and memory requests/limits before observing the scheduler refusal. The CPU
quantity has one declaration; the runtime limit is derived from it.

This closes the construction distinction explicitly excluded by the preceding
retirement audit. A production-only get-container buildPod failure has three
measured outcomes:
- The original restored Go leaf, supplied only through a temporary Go overlay,
  fails its Run error assertion at original line 103.
- The old pre-created-pod Brine fixture passes.
- The current Brine fixture fails at Run with the same construction error.

The original Go control passes with exactly that one Ginkgo leaf selected.
The retired test was not reintroduced into the worktree. The five scheduling
outcome mutations also retain their original Brine assertion failures.
All six mutations were rebuilt and replayed against the final fixture after
centralizing the CPU quantity. No production source changed.

Production does not expose the former test-only node-name affinity. The new
fixture therefore requires stable node membership and allocatable CPU during
the run, verifies both after Wait, and is documented against use during node
addition/capacity changes. Its request exceeds every observed node even when
empty. On this authorized cluster, the unchanged node has 12 CPUs and every
runtime-built test pod requests/limits 13 CPUs and 64MiB memory. The existing
one-pod quota and memory/storage bounds remain. No node, PriorityClass or
cluster-wide policy is changed. The scheduler's actual event and refusal,
unbound Pending identity and absence of command execution are still required.

The original feature text, all assertions, the local scenario roster and the
complete catalog remain unchanged: 563 cases = 478 local + 85 live, with
1,042 definitions. No additional Go test or Brine case was added or removed.
README and the per-test disposition now distinguish the former construction
gap from the current measured replacement.

Final validation:
- Actual startup feature: 8/8 passed in 141.600s.
- Full local Brine: 478/478 passed in 139.254s; excluded-live manifest:
  85 skipped in 4.554s.
- Three native suites passed in 36.46680386s; default/live vet passed.
- All recorder drains completed. Twenty-two owned namespaces across controls,
  initial/final mutations and final feature validation have matching UID
  cleanup receipts and fresh exact-name absence checks.
- No recent active test processes remained; git diff --check passed.

Checked evidence: /tmp/brine-scheduling-construction.UUCyyx/evidence.json
SHA256: 75be05f0635061fd9fbb466e47bc49240cd34159ae3b5b8282ed47c580541bc8
Normal adapter SHA256:
a27bebbf6745637c8f5626682302fa35a37547a24392802d132c2f8a22a78b31

Reported lifecycle inputs and the remaining legacy-test audit are still work.
The core zero-grace cancellation Go leaf remains retained. No fresh full-suite
coverage or complete live-suite pass is claimed; 79.602595% remains the
historical full-run measurement. The goal remains active. Changes remain
uncommitted and unpushed on core.

## 2026-09-14 — retire the core zero-grace cancellation fixture

The remaining restored process leaf JB-kept-002 is now replaced by the existing
live task cancellation cases. The observer reads an independent copy of the
actual Kubernetes DELETE body, decodes JSON or protobuf using Kubernetes codecs,
and forwards the original request, response and transport error unchanged.
It requires exactly one successful HTTP attempt with an explicit nonnil zero
grace period. Existing cancellation, process-stop/isolation, UID and physical
pod-removal assertions remain. HTTP retries are counted, not collapsed into a
logical client call; this is a stricter wire-level count in the live fixture.

The first two-row control exposed an observer bug: assuming JSON failed on real
protobuf requests. That failed run is preserved as initial-json-decoder.log;
its namespaces were cleaned. After using the Kubernetes decoder, both real task
rows passed (20.156s). The previous unmodified fixture also passed both rows
(20.428s).

Five production-only overlay faults fail the original focused Go leaf and the
strengthened Brine case at their intended assertions:

- Lost cancellation: original Go line 109; Brine's cancellation Then.
- Missing deletion: original Go line 113; Brine observes zero DELETEs.
- Duplicate deletion: original Go line 115; Brine observes two DELETEs.
- Omitted grace: original Go line 116; Brine rejects a nil grace period.
- Nonzero grace: original Go line 117; Brine rejects grace 1.

The previous Brine case passed duplicate deletion, establishing a measured gap.
It already rejected omitted and nonzero grace indirectly because the pod remained
in those runs; no claim is made that those two faults previously escaped.
The faults never modified the working production file, whose hash still matches
the pre-pass snapshot.

The sole remaining 120-line process_restored_test.go fixture is removed. Its exact
recoverable source is /tmp/brine-zero-grace.pSvGpa/before-process_restored_test.go.
The complete remaining Ginkgo It roster equals the previous roster minus only
JB-kept-002: 96/96 passed (full package command 60.932496108s). The scheduling leaf
was retired separately in the preceding pass. Disposition records preserve that
history and now mark JB-kept-002 DELETED.

Final verification:

- Full actual cancellation feature: 9/9 passed in 113.120s.
- Full local Brine: 478/478 passed in 140.138s;
  85 live cases excluded in 4.629s.
- Three native Brine suites passed in 37.167338307s; default and live vet passed.
- Inventory unchanged: 563 cases (478 local + 85 live), 1,042 definitions;
  catalog patterns, modes and input/output shapes match the prior checkpoint.
- All recorder drains completed. All 23 owned live namespaces, including failed
  controls and mutation runs, have matching UID cleanup receipts and fresh
  exact-name absence checks. git diff --check passed.

Checked evidence: /tmp/brine-zero-grace.pSvGpa/evidence.json
SHA256: a39925b5d3ab6112b37a205a13b19c3e12596f8db509a5ac53d1fa0bb178406d
Normal adapter SHA256: c2d0abd8d9b5ef84fbadc861ad18bfcba7f5c3fda3252bd0256bf06faa13384e

No new full live-suite or coverage measurement is claimed. The historical
79.602595% full-run coverage result is not a current whole-suite certification.
Reported Kubernetes lifecycle inputs and other retained legacy fixtures remain
migration work. Goal active; changes uncommitted and unpushed on core.

## 2026-09-14 — retire the running-cancellation integration leaf

JB-kept-001 is now retired independently of the preceding before-start audit.
The original integration leaf waits for its fake executor to start, cancels the
step context, requires cancellation to return within ten seconds, lists an empty
namespace and requires one DELETE with explicit zero grace. The existing live
running-task row already observes a live child and supervisor, waits for runtime
cancellation and requires kubelet-confirmed termination before pod removal. Its
existing task-removal assertion now also lists the real owned namespace and
requires zero remaining pods. No step definition or scenario was added.

The original focused Go control and real running-command control passed (the
latter in 14.225s). Five production-only overlay faults fail both the original
leaf and the live row:

- Omitted grace: original Go line 153; actual DELETE has no grace field.
- Nonzero grace: original Go line 154; actual DELETE requests grace 1.
- Duplicate deletion: original Go line 152; two real HTTP DELETE attempts.
- Missing deletion: original Go line 149 requires an empty pod list; Brine fails
  because the real task remains Running with no deletion timestamp. This failure
  is the existing termination assertion, not the newly added namespace list.
- Lost running cancellation: original Go line 144; Brine receives nil instead
  of context cancellation. This mutates the post-exec cancellation branch, not
  the earlier wait-for-running branch used for JB-kept-002.

The single integration It and three unused imports are removed: 73 lines removed,
three explanatory lines added. The other three integration test bodies are
byte-for-byte unchanged. Recoverable source: /tmp/brine-running-cancel.t28y8n/before-integration.go.
Production process.go is unchanged from the pre-pass snapshot.

Final validation:

- Root JetBridge package: 95/95 specs, 61.097176438s total. The complete It roster
  equals the previous 96-leaf roster minus only JB-kept-001.
- Actual full cancellation feature: 9/9, 113.434s.
- Local Brine: 478/478, 139.409s; 85 live cases excluded in 5.115s.
- Three native suites passed in 35.80233143s; default and live vet passed.
- Inventory remains 563 cases (478 local + 85 live), 1,042 definitions. Catalog
  patterns, modes and input/output shapes match the previous checkpoint.
- All recorder drains completed. Fifteen owned live namespaces have matching
  UID creation/removal receipts and fresh exact-name absence checks.
- git diff --check passed; all test command handles finished successfully.

Checked evidence: /tmp/brine-running-cancel.t28y8n/evidence.json
SHA256: a4b89f3242b1a1c7d368a6e8b03263af9e03a874c7632f9b3bfa39b4460c68a4
Normal adapter SHA256: 478d37c5547fd02e16e134c3a92530b6188dfb3778c26a9ea1f305016dc50d7c

The remaining core hijack leaf JB-kept-000 is deliberately KEPT. Inspection
confirmed that live resource retention exercises fresh get/put/check containers,
not a looked-up task; the different-command restart case completes normally,
without cancelling a hijack session. Neither proves the lookedUp exclusion.
The concrete next cancellation migration is a real looked-up task session whose
cancellation preserves the original pod and task, with per-leaf mutations before
retirement. The disposition now records that gap explicitly.

Reported Kubernetes lifecycle inputs and other legacy fixtures also remain.
No current complete live-suite or full coverage result is claimed; 79.602595%
is still historical evidence, not certification of today's complete suite.
Goal active; changes uncommitted and unpushed on core.

## 2026-09-14 — real hijack-session cancellation and core leaf retirement

JB-kept-000 now has its own real replacement. The prior fresh-resource retention
rows and normally completed different-command replay case did not exercise an
operator session's cancellation on a looked-up task container. One new scenario
and two definitions cover that missing contract in the existing cancellation
feature. Shared task construction, worker setup, process-state reads and passive
DELETE observation are reused; no pod status, fake executor or API response is
supplied.

The original task is created and run through production with an on-pod release
gate. A separately looked-up container starts a second real command, and distinct
live PIDs in the same pod establish that both commands started. Only the session
context is cancelled. The outcome requires context cancellation, zero real DELETE
attempts, the original sole Running pod UID with no deletion timestamp, and the
original command still alive. After releasing its gate, the original runtime
process must finish successfully with exact stdout. Both waiters are bounded and
drained; the owned namespace is removed. The initial control passed in 20.094s.

Three independent production-only faults fail the original focused Go leaf and
the new Then assertion:

- Removing the lookedUp deletion exclusion: original Go line 966 loses its pod;
  live Brine observes one unwanted DELETE.
- Clearing the LookupContainer flag: original Go line 966 loses its pod;
  live Brine observes one unwanted DELETE.
- Suppressing looked-up cancellation errors: original Go line 964 receives nil;
  live Brine requires context cancellation and rejects nil.

The complete previous nine-case cancellation feature PASSES under the same
missing-guard mutation (112.732s). That is a measured coverage gap, not a claim
that the new case merely resembles an existing one. The new case rejects it.
Production process.go and worker.go remain identical to their pre-pass snapshots;
all fault injection used Go overlays.

The exact original hijack leaf and its now-unused time import are removed;
all other container-restored source is byte-for-byte unchanged after that known
replacement. The file is 34 lines shorter. Snapshot:
/tmp/brine-hijack-cancel.28Zhxs/before-container-test.go.
The first final package command found the unused import at compile time; its log
is saved as initial-root-compile.log. Removing the import allowed the subsequent
full package run. No tests ran or received credit for that failed compile.

Final verification:

- Root JetBridge: 94/94 specs, 60.815292086s command duration. The complete It roster
  equals the previous 95-leaf roster minus only JB-kept-000.
- Expanded actual cancellation feature: 10/10, 128.107s.
- Local Brine: 478/478, 138.961s; 86 live cases excluded in 5.207s. The local
  scenario roster is unchanged. The audit initially included the separately
  logged skipped-live scenario starts; it now compares the first/local run only.
- Three native suites passed in 36.36910638s; default/live vet passed.
- Inventory: 564 cases (478 local + 86 live), 1,044 definitions. Removing the
  one new scenario restores the previous feature byte-for-byte; removing the
  two new catalog patterns restores all previous modes and input/output shapes.
- All recorder drains and original-task waiters completed. Twenty-three owned
  namespaces have matching UID creation/removal receipts and fresh exact-name
  absence checks. All test handles are terminal; git diff --check passed.

Checked evidence: /tmp/brine-hijack-cancel.28Zhxs/evidence.json
SHA256: b2f3120e48e6f633d8191e521eacfcbe8c997ae2e8b0aebf2a0543ff66ec173e
Normal adapter SHA256: e6eeb6674ee7452d263c507cfdaaefc1d8dccaa2e1d1601e28d5be59c4926d95

Together with the preceding two audits, all three post-rebase core cancellation
leaves JB-kept-000/001/002 now have independently measured real replacements.
This does not retire other legacy tests or remove the remaining reported
Kubernetes lifecycle inputs. No fresh complete-live-suite or full coverage result
is claimed; the historical 79.602595% is not current whole-suite certification.
Goal active; changes uncommitted and unpushed on core.

## 2026-09-15 — remove fabricated Nodes from cache-closing fixtures

The remaining status-fixture audit found an unnecessary dependency in
real_closing_cache.go: each standalone cache daemon also created a Node and
supplied its InternalIP. The cache backend is deliberately configured with a nil
node resolver and uses EndpointSlice pod IPs; these daemons run without a node
labeler. The Node objects and status addresses are not inputs to this family.
Their creation/publication/deletion is now removed, with all cache actions and
assertions unchanged. The endpoint identity and real daemon addresses remain.

This does not generalize to every createRealNode caller. Warm-roll and peer
daemons use --node-name and real startup labelling, whose failure terminates the
production daemon. Other volume/discovery contracts actually resolve Node
addresses. Those fixtures remain; removing their Nodes would alter the contracts.
The audit traced those consumers rather than treating all Node setup as dead.

Fresh old/new ten-case controls passed in 8.284s and 7.899s. Nine previously
recorded production faults were replayed against both fixtures, preserving all
eleven scenario/fault detections at identical steps with identical errors:
readiness filtering, negative-cache suppression, peer-client wiring (two rows),
durable capability, empty-key silence, content-key propagation, forbidden row-ID
storage, missing durable upload, and empty restoration (two rows).

An additional production-only mutation requires a Node lookup to succeed before
serving a discovered local cache. The old fixture passes all ten cases (8.361s),
because its fabricated Nodes satisfy that unnecessary prerequisite. The new
fixture fails the original local-hit and local-hit/peer-fallback Then assertions.
Thus removing the fabricated inputs both preserves the old detections and exposes
a previously hidden Node-object dependency without adding an assertion or case.

The control recorder traces have one fewer disposer for each of eight ordinary
cases and two fewer for each of two peer cases: twelve redundant Node lifecycles
per ten-case matrix. All control and fault-run recorder drains completed without
partial cleanup. The only test-code change is deletion of the Node setup and a
comment explaining why; the feature is byte-for-byte unchanged. No Go leaf was
retired, no vocabulary changed, and all production files match their snapshots.

Final validation:

- Full local Brine: 478/478, 139.197s; 86 live cases excluded in 4.753s.
- Three native suites passed in 37.214140707s; default and live vet passed.
- Local scenario roster and full catalog patterns/modes/input/output shapes match
  the preceding checkpoint: 564 scenarios (478 local + 86 live), 1,044 definitions.
- All local recorder drains completed; no audit-specific adapter/daemon processes
  remained. Test handles are terminal and git diff --check passed.
- Runs used private local API servers and real daemon processes. No shared-cluster
  namespace, node, label or cluster-wide policy was changed.

Checked evidence: /tmp/brine-cache-discovery.zZ2G0P/evidence.json
SHA256: 62e64174a647e8726a4a9b6d3b45550987413586fbc109cba6727f0dbb9a570d
Normal adapter SHA256: 429c3b37836512cfd38196983c7f8c77026641607f283c2eb27efb84b49d97f9
Recoverable fixture snapshot: /tmp/brine-cache-discovery.zZ2G0P/before-real_closing_cache.go

Reported Kubernetes lifecycle fixtures and the remaining legacy-test audit are
still work. No complete live-suite run or fresh full coverage measurement is
claimed; the historical 79.602595% remains historical. Goal active; changes
uncommitted and unpushed on core.

## 2026-09-15 — fresh full v5 coverage and remaining-state checkpoint

The complete coverage gate passed on the unchanged core worktree at
3c787e9c70602baf2279c5ff92ad32253edcf68b. No source, feature, definition or
test was changed during this measurement. The gate process exited 0, including
its restoration of the normal adapter.

- Local manifest: 478/478 across 32 features, 138.451s.
- Live manifest: 86/86 across 19 features, 1,586.397s.
- Total: 564/564, no failures or skips; 1,044 catalog definitions.
- Combined scenario/setup runtime: 1,724.848s (28m44.848s), excluding build/report.
- Brine-only production JetBridge coverage: 1,966/2,466 statements,
  79.724249797%; unrounded 50% gate passed. Profile: 1,598 unique atomic blocks
  in 25 production files; adapter and test code are outside the denominator.
- Every expanded feature/scenario identity, line and step count matched the
  parsed source inventory. All 564 recorder drains completed without partial cleanup.
- All 86 owned namespace creation/removal UID receipts matched; fresh API reads
  found none remaining. All eight owned storage roots had kubelet-removal
  receipts, and all eight daemon UID/port pairs had closure receipts.
  The producer-offline path has one legitimate repeated closure receipt.
- No newly started matching adapter, API-server, etcd, PostgreSQL or collector
  process remained. Older unrelated processes were not touched.
- The 2,802-entry source fingerprint matched before/after. The instrumented
  adapter carried -cover=true; the restored normal adapter does not and matches
  the preceding cache-discovery checkpoint byte-for-byte.
- Brine CLI d1c75ea958f299db1aee754b65d9da2b456a03ac was clean; its Go runner
  subtree matches pinned SDK 6bc587016933 (tree
  c2ae944f50060b61d57a7693b30e58927026f583).

Checked evidence: /tmp/brine-full-refresh.FG9A2U/evidence.json
SHA256: aa509aa957264033794309d2981f6c4f997b5affec50066ed1289483291e5000
Coverage profile: /tmp/brine-coverage.lx4eKu/coverage.out
Profile SHA256: 851c529714dd99910cbd9a2c776e271bc375cb49ecf4713f0922ee450b531bd0
Normal adapter SHA256: 429c3b37836512cfd38196983c7f8c77026641607f283c2eb27efb84b49d97f9
Source fingerprint SHA256: fb7dce291ceb517e3219b3d596dcc8a874c4038bcca4a577e54ff6ea78ece9db

This supersedes the September 14 full coverage measurement, not the separate
mutation-pair evidence for individual migrations. No mutation campaign or native
suite was rerun for this checkpoint; no test code changed after their preceding
validation. Documentation was updated only after fingerprint verification.

### Remaining supplied-state milestone

A source-derived expanded-scenario ledger is preserved as state-inventory.json,
with its reproducible state-inventory.go beside the evidence. Its rules must
each match a nonempty family. It audits the explicit lifecycle status-writing
paths and direct Node-address specifications, not every discovery object or
every possible test double.

| Family | Expanded cases | Premise that a replacement must preserve |
| --- | ---: | --- |
| Node eviction and diagnostics | 9 | Eight execution-path/diagnostic rows plus repeated pre-command eviction |
| Early sidecar pull failure | 6 | Main still ContainerCreating when the sidecar cannot pull |
| Repeated OOM diagnostics | 2 | Current and previous OOM termination with restart history |
| Terminal waiting priority | 2 | Failed/ImagePullBackOff or Pending/CrashLoopBackOff, without termination history |
| Empty terminal status | 1 | Succeeded with no container status |
| Compacted phase history | 1 | Real expired watch recovers current Succeeded phase |
| Unreported recovery | 1 | Empty phase and no recorded completion |
| Direct Node-address input | 3 | Internal-address/cache, absent internal address, and IP-shaped node-name refusal |

The first seven rows total 22 lifecycle cases. The direct address cases are
separate; manually published discovery objects and remaining legacy-test
equivalence still require audit. Six UpdateStatus sites remain in five step
files, including the shared Node-address publisher. Removing fake-client
imports alone does not remove these supplied inputs.

Next measurable milestone: reduce the 22 supplied lifecycle cases to zero using
observed behavior, preserving their stated premises and paired production-fault
detections; remove each unused setter/definition when its final consumer moves.
Prefer existing live scenarios/helpers, and justify every net new case or
definition by a distinct contract. Then resolve the three direct Node-address
inputs and finish the broader discovery/legacy audit. Keep both manifests green
and Brine-only production coverage >=50%; a coverage percentage alone never
completes the no-doubles goal.

Nine eviction cases need an isolated-node strategy: pod-local emptyDir eviction
does not produce their node-resource-pressure messages, DiskPressure/spot or
cordon diagnostics. No node pressure, cordon, shared labels or cluster-wide
changes were attempted on theborg. No local Docker/Podman/K3s/containerd command
or standard Docker/containerd socket was found in the inspected environment;
no runtime was installed. These environment constraints do not invalidate the
completed full-suite run or authorize weakening the remaining assertions.

Goal remains active. Changes are uncommitted and unpushed on core.

## 2026-09-15 — consolidate watch vocabulary and identity checks

The scoped and phase-only watch assertions now share one CheckString definition:
“the runtime is told its pod is {string}”. It rejects errors, nil pods, a
different pod name and the wrong phase. The scoped case retains both checks;
former phase-only consumers gain identity checking. The duplicate custom
definition is removed, not retained as an alias.

All watch-feature consumers use the common vocabulary without the migration-only
“really” wording. The local Given now describes an API-only Pending pod rather
than claiming it runs; the live two-pod Given describes scheduling-gated pods.
Actions and lifecycle helpers are unchanged. Scenario names, lines, step counts
and expanded inventory match the preceding full checkpoint exactly.

Validation against recoverable old/new fixtures and binaries:

- Old control: four local cases in 10.527s; five live cases in 67.748s, all passed.
- New control: four local cases in 9.118s; five live cases in 69.225s, all passed.
- Three prior production-only faults fail both versions at the original scoped
  Then: missing selector, inverted selector and returned Running phase corrupted
  to Failed. Selector faults name noisy-neighbour instead of watched-pod.
- An additional production-only fault changes the initial returned pod name to
  different-pod without changing Pending phase. The old initial-read case passes
  (4.896s); the new case fails at its Then (5.317s), naming different-pod and
  watch-pod. No new scenario or test double is added.
- Full local Brine: 478/478, 139.693s; 86 live cases excluded in 5.408s.
- Three native suites passed in 40.022075529s; default/live vet passed.
- Catalog comparison verifies pattern/mode/input/output metadata: exactly one
  definition removed (1,044 -> 1,043), and only the recorded wording changes
  otherwise. Inventory stays 564 (478 local + 86 live), with no added case.
- All control/fault recorder drains completed. All 16 owned live namespace
  creation/removal UID receipts matched; fresh API reads found none remaining.
  No newly started matching test processes remained.
- Production watch.go and runtime watch helpers are unchanged. Faults used Go
  overlays only; the normal adapter matches the new control. No legacy Go test
  was retired. git diff --check passed.

Checked evidence: /tmp/brine-watch-language.WEJHmp/evidence.json
SHA256: d691ad6c6223f8f69940daa00d02b69c3f9280a7cc401cde48b1161568010659
Normal adapter SHA256: 4c09ab00db3a61a5a37245bf32f909d926445549fe6b1d4d3a382cd2fa097f59
Recoverable old source/features: before-podwatch_real.go, before-local.feature
and before-live.feature in that directory. run-audit, run-identity-gap and
verify.cjs reproduce the comparison and evidence checks.

The preceding full coverage result is 1,966/2,466 (79.724249797%), not remeasured
after this consolidation. The 22 supplied lifecycle cases, three direct Node
address cases, broader discovery audit and retained legacy-test equivalence
remain. This is consolidation and stronger detection, not completion of the
no-doubles goal. Goal active; changes uncommitted and unpushed on core.

## 2026-09-15 — retire the two restored DBVolume identity tests

JB-volume-002/003 are now covered by the existing real-database volume identity
scenario. It retains the exact created db.CreatedVolume object and owning team
as state, checks object identity for both exec-backed and daemon-backed volumes,
and checks the daemon row's handle, worker, team and artifact type. The handle
is declared once as vol-handle-123, preserving the restored test's exact handle
contract rather than deriving the expected value from the returned row.

No scenario or definition was added. Existing handle/source/non-nil checks remain.
The two definition shapes gain only DBVolume and TeamID; all other catalog
patterns, modes and shapes are unchanged. The Kubernetes executor is real and
not exercised by this identity-only scenario; no replacement mock was introduced.

Deletion followed independent original-leaf evidence, while both Go tests still
existed unmodified:

- Both original focused Go controls passed, with exactly the intended It selected.
- Old/new Brine controls passed in 5.763s and 5.034s.
- Direct DBVolume returning nil fails the original Go leaf at line 107 and both
  Brine versions at the existing missing-row assertion.
- Omitting the daemon constructor's dbVolume assignment fails its original Go
  identity assertion and both Brine versions at the missing-row assertion.
- For each kind, returning a distinct shallow copy of the real database object
  fails the original Go identity assertion. Both copies preserve the underlying
  concrete database type and all field values; these are production-only Go
  overlays, not replacement test clients or row doubles.
- Both clone faults pass the old Brine checks and fail the strengthened Then,
  naming the exec-backed or daemonset object replacement respectively. This
  closes the two precise gaps recorded in the September 8 retention notes.
- The pre-retirement pairing verifier checked all four original-Go/new-Brine
  pairs, exact selected leaves, failure locations and intended Brine diagnostics.

Only the two DBVolume Its and their empty Describe block were removed.
The rest of volume_restored_test.go is unchanged apart from the retirement
comment and blank-line normalization; its other cases and shared executor
fixture remain. Production volume.go and volume_daemonset.go are unchanged.

Final validation:

- JetBridge Ginkgo inventory: 94 -> 92; all 92 remaining cases passed in
  27.958856051s. Exact title comparison proves only JB-volume-002/003 disappeared.
- Full local Brine: 478/478, 139.005s; 86 live cases excluded in 4.694s.
- Three native suites passed in 41.47858752s; default/live vet passed.
- Inventory unchanged: 564 scenarios (478 local + 86 live), 1,043 definitions.
- All recorded resource drains completed; no newly started matching test
  processes remained. No shared-cluster operation was performed.
- Normal adapter matches the tested control; git diff --check passed.

Pre-retirement proof: /tmp/brine-volume-db-identity.RrbR7w/pairing.json
SHA256: ee8db3fc293b9ad83a8955c97b0e459d204e19bc7dfc996e53bceecd81c38e62
Final evidence: /tmp/brine-volume-db-identity.RrbR7w/evidence.json
SHA256: 8234fb0cc29d3c2a32cfc7f3dd61a9f2e918c1c09c14f4bd98f70e9f1ab46424
Normal adapter SHA256: 860213827bca206040b9bf7d2f908d454e68cd063d1497a9a70cc0603ec2ea84
Recoverable legacy source: before-volume_restored_test.go in that directory;
retired-block.txt records the exact removed block.

The prior full coverage gate remains 1,966/2,466 (79.724249797%), not rerun for
this local-only identity change. Other restored Go cases, supplied Kubernetes
lifecycle/address inputs and the broader discovery audit remain. This does not
establish completion of the no-doubles goal. Goal active; uncommitted/unpushed
on core.

## 2026-09-15 — consolidate two persisted volume identities and retire handle tests

JB-volume-000/019 now share the existing real-database identity scenario with
JB-volume-002/003. The Given constructs two independently named persisted volumes,
vol-handle-123 and vol-handle-456, initializes their real artifacts and reloads
their database rows. A collection of volume/row/expected-handle/artifact records
keeps the comparison together. The daemon-backed volume retains the first row.

The one assertion preserves exact handles, source worker, non-nil and identical
constructor-supplied row objects, and daemon row handle/worker/team/type. It also
requires two distinct runtime handles and resolves both real artifacts back to
their volumes. Each expected handle is declared once in the construction inputs;
it is not derived from the observed runtime handle.

The existing scenario is renamed “Volumes retain distinct database identities”,
and its Given becomes “two persisted volumes on this worker”. The assertion
phrase is unchanged. No scenario or definition is added and no old alias remains.
Both identity-definition shapes now carry the collection; all other catalog
patterns, modes, shapes and resource requirements remain unchanged.

Before deleting either original Go test:

- Old single-volume and new two-volume controls passed in 5.522s and 5.284s.
- Exact focused Go controls selected and passed each intended original It.
- Empty handle and a corrupted vol-handle-123 both fail the original handle
  assertion and both Brine versions.
- Collapsing all runtime handles to vol-handle-123 fails the original uniqueness
  comparison and the new Brine check for the second expected handle. It passes
  the old single-volume Brine scenario.
- Returning a different, still-distinct handle for the second volume fails the
  original artifact/runtime-handle equality and the new Brine identity check.
  It also passes old Brine.
- A separate production-only mutation in db.WorkerArtifact.Volume redirects the
  second artifact to the first artifact's row. Both runtime handles stay intact.
  The original Go artifact equality and the new Brine artifact lookup assertion
  fail; old Brine passes. The new failure explicitly reports vol-handle-123
  versus vol-handle-456, not a missing-row/setup failure.
- All five original-Go/new-Brine pairs reach their intended assertions. The final
  verifier compares each Go failure's source line with the original exact
  Expect expression, excluding failures in setup or unrelated checks.
- All four prior nil/clone database-object faults were rerun against old and new
  Brine fixtures and retained identical expected error messages. The earlier
  JB-volume-002/003 detections were not weakened by the collection refactor.

Only the two handle/uniqueness Its were removed. Other legacy test bodies remain
byte-for-byte unchanged, apart from retirement comments/blank-line normalization.
All mutations used Go overlays; volume.go, volume_daemonset.go and
db/worker_artifact.go remain unchanged.

Final validation:

- JetBridge Ginkgo: 92 -> 90 cases, all 90 passed in 29.007410538s. Exact title
  comparison confirms only the two intended leaves disappeared.
- Full local Brine: 478/478, 139.801s; 86 live cases excluded in 4.282s.
- Three native suites passed in 41.858779329s; default/live vet passed.
- Inventory remains 564 scenarios and 1,043 definitions. One scenario was renamed;
  every other expanded name, line and step count is unchanged.
- All recorder drains completed; no newly started matching test processes
  remained. This turn performed no shared-cluster operation.
- The normal adapter matches the tested new control; git diff --check passed.

Pre-retirement proof: /tmp/brine-volume-uniqueness.H3AyYr/pairing.json
SHA256: 2ed9a89f6c66d1ac1112ec3f3558029345dc5269c74c06fe25501e95dd229a5c
Final evidence: /tmp/brine-volume-uniqueness.H3AyYr/evidence.json
SHA256: 4383d0179f170a3ebfe6ec9d0cb8a3b9ee21f715b5d583dcb0cba33371074dad
Normal adapter SHA256: 2c8f0ae08bb7759567b5c6a395ad21dd2aa590f81b8738b7ea4d7e73dc299a44
Recoverable legacy source: before-volume_restored_test.go in that directory;
retired-handle.txt and retired-uniqueness.txt contain the exact removed blocks.

The prior full coverage result remains 1,966/2,466 (79.724249797%), not remeasured
after this local identity consolidation. Other mock-based legacy tests, supplied
lifecycle/address inputs and the broader discovery audit remain. Goal active;
changes uncommitted and unpushed on core.

## 2026-09-15 — migrate placeholder refusal and executor-reporting contracts

The existing two-row placeholder outline now exercises the actual production
NewStubVolume returned by worker.go, not a replacement executor or fake volume.
One row reads and one writes; each calls the real method with nil compression
and with gzip. The raw write preserves the original Go test's data bytes; the
gzip write preserves the former Brine probe archive. Refusal is observed before
any archive-decoding assertion can substitute for the method's error.

A focused Given/action/assertion trio replaces the former stub Given. The action
records actual errors and closes any unexpected returned stream. The assertion
calls HasExecutor on the same object, requires both raw/gzip observations, rejects
successful calls, and checks case-sensitive “cannot stream in/out” and “no
executor” fragments. There is no status/error injection in the fixture. Normal
volume streaming helpers and the generic case-insensitive diagnostic assertion
remain unchanged for their other consumers.

The outline still has two expanded cases. Three cohesive definitions replace one
old setup definition: two net definitions are added (1,043 -> 1,045), with no
aliases and no additional scenario. This isolates the placeholder's two-codec
refusal contract instead of adding compression flags to unrelated volume state.

Before deleting JB-volume-015/016/017, all original Go tests remained unchanged
and each was selected independently:

- All three original Go controls and old/new read/write Brine controls passed.
- Five faults per I/O direction fail the original raw Go assertion and the new
  Brine assertion: false success, missing operation-specific text, missing
  “no executor”, a raw-only diagnostic failure, and incorrect diagnostic case.
- Inverting HasExecutor fails its original Go assertion and both new Brine rows;
  both old Brine rows pass that fault.
- Two gzip-only diagnostic faults fail old and new Brine while the raw-only Go
  tests pass. This separately proves the compressed paths were preserved.
- The audit checks all eleven original-Go failure pairs against the exact
  original Expect source lines, not just a red suite. All new failures occur at
  the intended Then with the expected operation/mode.
- Seven unique mutations pass the old Brine cases but fail the new ones:
  operation-text, raw-only and case faults in each direction, plus the executor
  getter fault (detected by both rows).
- The old false-success read failure came from decoding an empty gzip stream.
  The new failure observes the method's nil error directly; it cannot mistake
  an archive-decoding error for a successful refusal.

Only the three placeholder Its and their empty Describe/setup block were removed.
Other legacy test bodies are unchanged apart from the retirement comment and
blank-line normalization. The normal production volume.go is unchanged; every
fault was built through a Go overlay. Remaining test executors elsewhere in the
legacy suite were not removed or relabelled as real.

Final validation:

- JetBridge Ginkgo: 90 -> 87 cases; all 87 passed in 28.115246084s. Exact title
  comparison confirms only the three intended leaves disappeared.
- Full local Brine: 478/478, 139.503s; 86 live cases excluded in 4.587s.
- Three native suites passed in 39.993573314s; default/live vet passed.
- Inventory: 564 scenarios (478 local + 86 live), 1,045 definitions.
  Only the two outline names changed; all other names, lines and step counts match.
- Catalog comparison verifies removal of one obsolete Given and addition of the
  three placeholder definitions, with every other pattern/mode/shape unchanged.
- All recorded drains completed; no newly started matching test processes
  remained. No shared-cluster operation was performed.
- The normal adapter matches the tested control; git diff --check passed.

Pre-retirement proof: /tmp/brine-placeholder-io.24Wd0x/pairing.json
SHA256: 0be4949e630713d21f88a035c216669e4fd4df4dbe209f81da2c194a06e12810
Final evidence: /tmp/brine-placeholder-io.24Wd0x/evidence.json
SHA256: 8dda1591e7da97054f6c5c42a4546032a6186c6dd67cc1bc1bcabbbb942e1f5a
Normal adapter SHA256: a889fcda4a0496bcd563466a00f7cfa5d5511c95aff59b13c33a401997deb8d9
Recoverable legacy source: before-volume_restored_test.go in that directory;
retired-block.txt preserves the exact removed Describe.

The prior full coverage result remains 1,966/2,466 (79.724249797%), not remeasured
after this local migration. Other legacy mock-based tests, supplied Kubernetes
lifecycle/address inputs and the broader discovery audit remain. Goal active;
changes uncommitted and unpushed on core.

## 2026-09-15 — migrate nested upload routing and shorten disposable-pod cleanup

The legacy JB-volume-006 assertion pinned the exact subdirectory upload argv,
not merely the resulting artifact. Existing live cases proved file contents but
missed equivalent-command changes. The upload fixture now observes the actual
production SPDY transport: it forwards each original request, response and error
unchanged, and checks one POST to the independently recorded pod/container,
HTTP 101, stdin and the exact extraction command/destination. It never implements
an executor, supplies a response, rewrites a command or sets Kubernetes status.

Two separate nested-path scenarios are consolidated into one outline. Both
original gzip rows remain (read from the nested path and from the volume root),
and one raw row exercises the nil-compression path used by the legacy Go test.
The existing upload phrase gains an explicit encoding parameter in place;
there is no compatibility alias. One reusable upload-route assertion is added.
The root round trip and cross-pod handoff remain. Net inventory change:
one additional live case and one definition, not another test family.

Original Go tests stayed byte-identical until the pre-retirement audit passed.
Four independent production overlays fail the exact original command Expect:
a changed shell argument label, a destination ending in /., the same destination
change only for raw input, and a genuinely wrong subdirectory. The first three
pass both old gzip Brine cases. New routing checks detect all four; under the
raw-only fault, both gzip rows pass and only the raw row fails. The old cases
still detect the genuinely wrong path through their artifact assertion.
All new failures are at the intended upload-route Then, not setup/compilation.
Only JB-volume-006 was removed; the other legacy bodies and production volume.go
are unchanged.

The comparison was unnecessarily expensive: the live I/O pods inherited a
30-second termination grace, and old/new variants ran serially. After the
mutation binaries had been built, the disposable fixture was changed to request
and verify one-second shutdown grace. No production pod lifecycle setting was
changed. The same five passing live controls fell from 231.625s to 64.644s
(about 72% less elapsed time). A diagnostic printf formatting error found in
the first controls was also fixed before the final live run and vet.

Final validation:

- JetBridge Ginkgo: 87 -> 86 leaves; all 86 passed. Reported suite execution
  24.025363766s (CLI elapsed 28.937670239s); title comparison proves only the
  intended leaf disappeared.
- Full local Brine: 478/478 in 139.025s; 87 live cases excluded in 4.917s.
- Complete direct-volume live feature: 5/5 in 64.644s.
- Three native suites passed in 40.658112696s; default/live vet passed.
- Current inventory: 565 scenarios (478 local + 87 live), 1,046 definitions.
  Every unrelated feature inventory and definition shape/pattern is unchanged.
- All drains completed. All 32 owned namespaces have matching UID cleanup
  receipts and were freshly confirmed absent. No new matching test processes
  remain. Node UID, labels, spec and allocatable resources are unchanged and
  the node remains Ready.
- Normal adapter matches the tested final binary; git diff --check passed.

Pre-retirement pairing: /tmp/brine-upload-route.q7aEMC/pairing.json
SHA256: 496f5857961ad8a61f20d2b86fcd030392bba1f19a510294b4b1eb77d73101b9
Final evidence: /tmp/brine-upload-route.q7aEMC/evidence.json
SHA256: 0f5d0f8f74decac163df9787efa80f721c1efa8cff6824337753a7040c9b1203
Normal adapter SHA256: a980187e711d81ae00460ca18be847dad35f9e7b7bf91d6b2bbffc680d895701
Recoverable legacy source: before-volume_restored_test.go and retired-block.txt
in that audit directory. Mutation-tested observer/live fixture snapshots preserve
the pre-formatting-fix/default-grace variants.

The prior full coverage checkpoint is still 1,966/2,466 (79.724249797%); this
batch did not rerun the entire live suite or coverage gate. The README now has
one current-inventory statement, fixing an inconsistent duplicate count.
The 25 supplied-status cases, broader discovery audit and other legacy mocks
remain. Next priority is grouped migration of the container-spec shared fake
client fixture against existing real-API Brine cases, retaining per-leaf
mutation evidence rather than inferring equivalence from a passing file.
Goal active; work remains uncommitted and unpushed on core.

## 2026-09-15 — migrate the ephemeral working-set group

Five restored Go leaves now share the real-API Brine mount contract:
JB-container-002/004/005/009/010 (working directory, two inputs, two outputs,
cache and scratch). The existing input/scratch cases are folded into the group;
three missing fixture combinations are added. Five Go cases are removed and
three Brine cases added, reducing the combined count by two.

One exact-set table assertion replaces the older “every volume is ephemeral”
definition. It requires a nonempty, single-column table of distinct absolute
paths; exact volume and main-container mount cardinalities; emptyDir backing;
valid mount-to-volume bindings; distinct volumes for independent directories;
and precisely the declared paths. Counts derive from the table, not a duplicated
integer. Existing real worker/database/artifact setup and draft refinements are
reused. The assertion reads the pod returned by the real API and never changes
pod status or substitutes a client, artifact, executor or response.

The initial outline controls exposed a runner limitation: Examples values are
expanded in step text but not attached data-table cells. The pinned Go SDK's
parser.go expandOutline (around lines 487–511) clones DataTable unchanged;
the clean CLI checkout's gherkin/src/lib.rs expand_outline (around 1261–1285)
does the same. The failing control shows literal <first>/<path> reaching the
assertion. An initial scratch-step wording typo was also corrected. No mutant
or retirement was accepted from these failed controls. Five explicit tables
preserve all five fixture combinations without a custom interpolation layer,
additional language aliases, or a runner/dependency edit.
Reproduction: initial-outline.feature and initial-outline-control.log in the
audit directory below.

All original legacy source stayed byte-identical until the pairing audit passed:

- Original Go controls: all five exact leaves pass.
- Old Brine controls: 2/2 in 5.367s; new controls: 5/5 in 5.479s.
- Missing volume, extra mount, wrong destination and persistent backing each
  fail all five original Go leaves at their exact expected Expect line and
  all five new Brine cases at the intended table Then (20 pairs).
- An outputs-only fault applies only when there are two outputs. It fails the
  original output leaf and the new output case; the other four controls remain
  green (one additional pair). Both old Brine cases pass this fault.
- The extra-mount fault also passes both old Brine cases. The old three other
  fault detections are retained by the new checks.
- All mutations are production container.go overlays. The API-only hostPath
  mutation changes a stored spec; no kubelet executes it or mounts a host path.

Only the workspace It and the four-row legacy table were removed. Their shared
fake client/artifact helpers still serve other restored cases and remain clearly
identified; this is not a claim that the entire container fixture is migrated.
All other legacy bodies and production container.go are unchanged.

Final validation:

- JetBridge Ginkgo: 86 -> 81 leaves; all 81 passed. Reported execution
  24.612574683s, CLI elapsed 28.178242183s. Exact title comparison confirms
  only the five intended leaves disappeared.
- Full local Brine: 481/481 in 139.825s; 87 live cases excluded in 5.154s.
- Three native suites passed in 41.182227479s; default/live vet passed.
- Inventory: 568 scenarios (481 local + 87 live), 1,046 definitions, unchanged
  definition count. No unrelated feature case or definition changed.
- All recorder drains completed, no newly started matching test processes
  remain, and the normal adapter matches the tested control.
- No shared-cluster resources or production files were changed.
- git diff --check passed. The obsolete workspace cross-reference was updated.

Pre-retirement proof: /tmp/brine-ephemeral-mounts.jpc0HJ/pairing.json
SHA256: 5b6e417f26c54e9ebe7436d2b920d8fdbca1d30b2d7ae4c56ec6e45b3f424b8c
Final evidence: /tmp/brine-ephemeral-mounts.jpc0HJ/evidence.json
SHA256: 255a3059bb212ee1d21e44bf578471790201994018c163a7e3bb340012a30a28
Normal adapter SHA256: 0e4a13d53ea84d494aba4a4738d23d470eee86d9c7339960c2b999ce7e9c24c3
Recoverable originals: before-container_restored_test.go and retired-block.txt
in the audit directory.

The previous full coverage result remains 1,966/2,466 (79.724249797%); no full
live-suite/coverage rerun is claimed here. Other shared legacy mocks, the 25
supplied-status cases and broader discovery audit remain. Continue grouped
container-spec migration (overlap, resource and security contracts) with
per-leaf evidence rather than file-level inference. Goal active; changes
uncommitted and unpushed on core.

## 2026-09-15 — migrate shared-input and separate-output mount contracts

The existing exact-set mount assertion now accepts an optional volume-prefix
column. It still requires every expected path, exact volume/mount counts,
emptyDir backing and distinct valid bindings. A prefix is checked on the actual
mount at the required path; absence cannot make the identity check pass.

The overlap scenario becomes a two-row outline: exact output path and trailing
slash. Its table is fixed, so it does not rely on the unsupported interpolation
of table cells. Both rows require the input-prefixed volume at the shared path.
The separate-output case uses the original legacy source/binary paths, including
the trailing slash, and the same exact-set assertion without a prefix column.
No definition is added. One Brine case is added and three legacy Go cases are
removed, reducing the combined count by two.

Before retiring JB-container-006/007/008, original Go source remained unchanged.
The audit proves eight original-Go failure pairs at exact Expect source lines:

- Removing a volume fails both count cases; the legacy prefix-only case passes
  vacuously. New Brine requires the complete set and fails all three rows.
- Adding a valid extra mount fails both original mount-count checks. Both old
  Brine cases pass; all three new rows reject the extra mount.
- Renaming input volumes fails the original input-prefix assertion. Old Brine
  passes; both new overlap rows fail the prefix check, while separation passes.
- Omitting output-path normalization fails both the original overlap count and
  prefix assertions: the extra trailing-slash output mount is also inspected
  by the original prefix loop. Both old Brine cases pass. Only the new
  trailing-slash row fails; exact overlap and separation remain green.
- Duplicating a mount path without changing counts fails the original uniqueness
  Expect. Both old and new real-API Brine fixtures fail at container Run with
  the precise spec.containers[0].volumeMounts[index].mountPath “must be unique”
  admission error naming the duplicated working-directory path. This is an
  explicitly verified production-pod rejection, not a Then failure or an
  unrelated setup failure.

All five preceding working-set mutations were rebuilt against the extended
assertion. Their 21 failure observations are unchanged, including exact
scenario, step and error. The optional prefix column therefore adds identity
coverage without weakening the single-column tables.

Only the two overlap Its and the separate-output It were removed. All other
legacy bodies, shared fake helpers and production container.go are unchanged.
The wider no-mocks goal is not claimed complete.

Final validation:

- JetBridge Ginkgo: 81 -> 78 leaves; all 78 passed. Execution 23.375265768s,
  CLI elapsed 27.841677679s. Exact title comparison confirms the removed leaves.
- Full local Brine: 482/482 in 140.092s; 87 live cases excluded in 5.018s.
- Three native suites passed in 40.064921404s; default/live vet passed.
- Inventory: 569 scenarios (482 local + 87 live), 1,046 definitions.
  The entire definition catalog's patterns, modes and shapes are unchanged.
- Every unrelated feature case and step count is unchanged.
- All drains completed; no newly started matching test processes remain.
  No shared-cluster resource was created or changed.
- Normal adapter matches the tested control; git diff --check passed.

Pre-retirement proof: /tmp/brine-overlap-mounts.HB5vvX/pairing.json
SHA256: 77e59753d93012de50e8355308368d0a61d0b0d05abdd5f2dd017838304b4bd8
Final evidence: /tmp/brine-overlap-mounts.HB5vvX/evidence.json
SHA256: 0f1247a00bf18d0ec476191e71e53fd58d3ddf2e68856b7f871819b9a790cbc1
Normal adapter SHA256: c7e864e330bd1b1ec5cc20dbab8480bec8ca9e095e4175882a6877c5f5341b59
Recoverable originals: before-container_restored_test.go and retired-block.txt
in the audit directory.

The last full coverage checkpoint remains 1,966/2,466 (79.724249797%);
no full live-suite or coverage rerun is claimed. Other shared legacy mocks,
the 25 supplied-status cases and the broader discovery audit remain. Continue
grouped resource/security/container-spec migration with per-leaf evidence.
Goal active; changes uncommitted and unpushed on core.


### 2026-09-15 — grouped security policy and requests-only resources

Retired legacy mock-backed JB-container-022/024/025 after validating their
original assertion lines. The existing three Brine cases remain; no scenario
or net definition is added. Privileged and unprivileged cases share one policy
check over the pod read back from the real Kubernetes API. Both retain unset
RunAsNonRoot and RuntimeDefault seccomp, plus their original container-level
pointer/value requirements. Requests-only now explicitly requires no resource
ceilings, exact original 256m/512Mi requests and API-assigned Burstable QoS.

Kubernetes serialization erases nil versus empty resource maps. The original
nil-map clause therefore moves to TestRequestsOnlyResourceLimitsRemainNil in
container_resources_test.go, calling the production helper directly without
any fake client, database or supplied API state. This is a split replacement,
not a claim that Brine can observe an in-memory representation through the API.
Three legacy cases removed and one pure Go case added: combined count -2.

Eleven isolated production-source overlays yielded 14 exact original-Go
failure pairs before the old source was removed:

- RunAsNonRoot=true, missing seccomp and Unconfined seccomp each fail both
  original security cases and both new Brine cases. All three faults pass
  the old Brine cases, demonstrating the gap closed by the shared policy.
- Missing and wrong-valued AllowPrivilegeEscalation/Privileged pointers fail
  the corresponding original Go and new Brine case, not unrelated setup.
- A CPU ceiling still leaves this pod Burstable: old Brine passes, original
  nil-limits Go and new explicit no-ceiling Brine fail. The pure test also fails.
- An empty limits map passes both Brine versions, but fails original and pure
  Go nil-map checks. This verifies the reason for retaining the pure clause.
- CPU and memory requests increased independently by one fail their exact
  original quantity assertions and the new Brine quantity checks.

All controls pass. Production container.go remained byte-identical throughout;
the mutations existed only under /tmp overlays. Legacy originals and removed
block are recoverable in the audit directory. Other restored Go bodies and
all unrelated feature cases and catalog definitions are unchanged.

Final validation:

- Root Ginkgo: 78 -> 75 leaves; all 75 passed in 23.148860418s.
- The pure nil-map Go test passes independently (no database suite selected).
- Full local Brine: 482/482 in 141.253s; 87 live cases excluded.
- Three native suites passed in 39.651905983s. Default/live vet passed.
- Inventory unchanged: 569 = 482 local + 87 live; 1,046 definitions.
- All 482 local drains completed; normal adapter matches the tested control.
- No live/shared-cluster resource was used or changed. No production edit.

Pre-retirement proof: /tmp/brine-security-policy.7kB73l/pairing.json
SHA256: 817b45c9abc4b71b46489c4dd3fe9e5b5f8cb14faadd2fb72303ae31e27a4bad
Final evidence: /tmp/brine-security-policy.7kB73l/evidence.json
SHA256: e3586d3774d7ff455c2ba40715cbdcc2fe8163bc047e470f1df9eea8314c401a
Normal adapter SHA256: 6b201ccdc1a754099715163b37acc515573339845c53f19f79b0ef26d2e96f8a

The last full coverage checkpoint remains 1,966/2,466 (79.724249797%);
no full live-suite or fresh coverage run is claimed. The 25 supplied-status
cases, wider discovery fixtures and other legacy mock-backed cases remain.
Next local group: configured pull-secret/service-account and registry-secret
cases currently check membership but omit the original exact-list length.
Consolidate their credential assertions with per-leaf mutation evidence.
Goal active; changes uncommitted and unpushed on core.


### 2026-09-15 — complete pull-secret lists and service accounts

Retired JB-container-026/028 after per-leaf mutation validation. The existing
configured-secret, registry-secret and duplicate-secret Brine cases now share
one exact-list sentence using the same comma-separated names as configuration.
The comparison sorts copies, so order is irrelevant but missing, extra and
duplicate names fail. It never mutates the stored pod or expected fixture.
The separate service-account and no-credentials checks remain unchanged.

No scenario was added. Two legacy mock-backed Its, one definition and two
redundant feature assertions were removed. All other restored Go bodies and
unrelated feature cases/vocabulary remain unchanged.

Before retirement, five production fault shapes yielded seven failures at
the exact original Go Expect lines:

- Extra secret: both original length assertions fail. All old Brine cases
  pass, while the new configured, registry and duplicate-list cases fail.
- Wrong configured names: both original membership assertions and all three
  nonempty credential cases fail.
- Wrong registry name: only the registry membership assertion/case fails.
- Omitted registry addition: only the original registry length assertion
  and corresponding Brine case fail.
- Empty service-account name: only the configured account assertion fails.

Two additional checks protect consolidation fidelity: removing registry
deduplication fails the same duplicate-secret Brine case in both versions;
reversing the output list passes original Go and all old/new Brine cases.
The no-credentials case passes all controls and these bounded fault shapes.
All Brine mutation failures occur at the relevant assertion, not during setup.

Final validation:

- Root Ginkgo: 75 -> 73 leaves, all passed in 22.862006557s.
- Existing pure nil-map Go regression retained and passed independently.
- Local Brine: 482/482 in 140.221s; 87 live cases excluded.
- Three native suites passed in 39.981289533s. Default/live vet passed.
- Inventory: 569 = 482 local + 87 live; definitions 1,046 -> 1,045.
- Every local recorder drain completed. Normal adapter matches the control.
- Production container.go is byte-identical; all faults used /tmp overlays.
- No shared-cluster operation or fresh full-suite coverage measurement.

Pre-retirement proof: /tmp/brine-credentials.ohGaBJ/pairing.json
SHA256: bfb96c3599867c872d67440771e1218ae66a4f68a8ab62ae0e5576b759ec252d
Final evidence: /tmp/brine-credentials.ohGaBJ/evidence.json
SHA256: 0290647890f79f8ba9fa00591ba3cedb524b09aafc2a25419feb078a40432f53
Normal adapter SHA256: 1cdbe73039c1d1d1423779a9f498cd602cd7be86e78991a79f95349fac708133
Recoverable originals: before-container_restored_test.go and retired-block.txt.

Coverage remains the last full checkpoint: 1,966/2,466 (79.724249797%).
The goal remains active: 25 supplied-status cases, broader discovery fixtures
and other legacy mocks still require work. Next grouped candidate: the six
restored sidecar cases. Current Brine checks omit ordered container identity,
exact mount equality, environment/ports, pull policy and several sidecar
resource/command clauses; do not retire those on the existing checks alone.
Changes uncommitted and unpushed on core.


### 2026-09-15 — six sidecar contracts, with shared field and identity checks

Retired JB-container-065/066/067/068/071/072 after 46 original-Go assertion
failures across 26 isolated production-source overlays. The existing one- and
multiple-sidecar cases are reused; four cases add the previously missing full
configuration, artifact-input, exec-pod and image-prefix contracts. Six legacy
cases removed and four Brine cases added: combined case count -2.

An ordered name/image table replaces image-only membership checks. Mount
comparison now requires full equality, including permissions, volume identity
and order, then validates every mount against pod volumes. Sidecar settings
share one lookup for environment, ports, command/arguments, resources and
working directory. Explicit assertions retain environment/port struct fields,
security pointers/values, pull policy, working directory and resource strings.

Initial controls found a genuine fixture mismatch: the shared pod-spec draft
does not configure the worker executor, so its pod runs /bin/sh directly. The
exec-sidecar case now explicitly supplies the already-created real SPDY
transport, matching the original exec-mode premise. Other mapping cases retain
the original no-exec compatibility construction. No successful command execution
is claimed here; this group observes the real API-stored pod and Run result.
The common Run action now rejects nil process results instead of discarding
them. The exact original pause argv is asserted independently of production.

Mutation evidence covers missing/reordered/renamed containers, changed images,
env values, port numbers/protocols, security flags, pull policy, read-only/extra
mounts, seccomp, commands, arguments, working directories, four resource fields,
three transport prefixes and a plain image reference, nil process and pause
argv. All original failures are bound to exact Expect source lines.

- Reordering passes all four old Brine controls but fails all six new rosters.
- Read-only and extra-mount faults pass old Brine but fail all three new exact
  mount comparisons. The actual field changes are present in diagnostics.
- Old Brine also misses the sidecar pull policy and pod seccomp faults.
- All three prefixed image forms and the original plain reference are checked
  together, with exact original sidecar names and ordering.
- A nil process fails the original exec case and the new Run result guard at
  When, after real pod creation; it is not treated as an unrelated setup error.
- Existing inherited/explicit working-directory cases remain mutation-sensitive.

After the matrix, the pre-existing working-directory setter was consolidated
onto the same sidecar lookup used by the new settings. Eight controls pass on
the final adapter; rerunning the working-directory fault preserves all three
failure observations exactly (scenario, step and error). The final full local
run also uses this post-consolidation adapter. The matrix and final-control
binaries/evidence remain distinct rather than claiming identical source hashes.

Final validation:

- Root Ginkgo: 73 -> 67 leaves, all passed in 22.388042994s.
- Existing pure nil-map Go regression retained and passed independently.
- Local Brine: 486/486 in 141.188s; 87 live cases excluded.
- Three native suites passed in 42.396633214s. Default/live vet passed.
- Inventory: 573 = 486 local + 87 live; definitions 1,045 -> 1,056.
  Added vocabulary exposes missing field/configuration/exec contracts; shared
  lookups avoid repeated selection logic. No new mocks or supplied statuses.
- All local drains completed. Normal adapter matches the final focused control.
- Production container.go is unchanged. No shared-cluster operation.

Pre-retirement proof: /tmp/brine-sidecars.b0IYzH/pairing.json
SHA256: f098384fe3c88a5f11c618577439cc8b1765d9496f0c4cc57228bda11e0e40a4
Lookup consolidation proof: /tmp/brine-sidecars.b0IYzH/cleanup-evidence.json
SHA256: 6e6b71a676c511b371f292ce3edf7f2d09a4f3611ef424c48415179a9215ebf5
Final evidence: /tmp/brine-sidecars.b0IYzH/evidence.json
SHA256: 17968eb1ab3fc47088fafaa9c17d606a25f61accd51cc12ec842b5afe2e82140
Normal adapter SHA256: 29190f59e9988fb2d5c232e619879622a12cf9db36b6b2d6338d14d7e657676a
Recoverable originals: before-container_restored_test.go and retired-block.txt.
Initial direct-mode mismatch: initial-direct-mode-control.log.

Coverage remains the last full checkpoint: 1,966/2,466 (79.724249797%).
No fresh full live-suite or coverage run is claimed. The goal is still active:
25 supplied-status cases, broader discovery fixtures and remaining legacy mocks
require work. Next local candidate: the returned input/output/cache mount
contracts, which still retain mock-backed pre-Run checks. Preserve concrete
volume identity, exact output paths and executor availability before retirement.
Changes remain uncommitted and unpushed on core.


### 2026-09-15 — returned volumes share one lifecycle and exact path table

Retired JB-container-034/035/038 after eight failures at the exact original
Go assertion lines. Fourteen individually selected production faults exercise
missing/nil input and output volumes, typed-nil caches, input/cache executor
availability, nonempty output handles, duplicate paths, distinct handles,
early/missing pod binding, trailing-slash overlap and relative caches.

The existing returned-volume scenario now contains the complete pre-Run
contract and the subsequent binding check. Its exact path table is reused by
both overlap rows and the relative-cache scenario. Three legacy Go cases and
two redundant Brine lifecycle scenarios are removed: combined case count -5.
Four old assertion forms become one table check; one named-output refinement
preserves a previously missing fixture input capability. Net definitions -2.

The initial metadata-omission fault exposed a real fixture gap: the old draft
assigned output-0/output-1 names rather than the original result/metadata keys.
The replacement now declares those original names explicitly. The same fault,
unchanged, fails the original Go output assertion and the new Brine assertion.
The initial miss is retained in initial-anonymous-output-miss.log. Anonymous
outputs still work, and named/anonymous collisions fail instead of overwriting.

Mutation adapters are built once with an environment-selected branch per fault,
not recompiled for every invocation. Normal and inactive-overlay controls pass.
The production worker.go and container.go remain byte-identical. The old Brine
checks miss input/cache executor loss, metadata-name omission and duplicate paths;
the new check catches them. Existing distinct-handle, binding, overlap and
relative-cache failures remain discriminated. These are constructor/binding
contracts, not claims that API-only fixtures actually executed a task.

Final validation:

- Root Ginkgo: 67 -> 64 leaves, all passed in 22.053555387s.
- The pure nil-resource-map regression passed separately.
- Local Brine: 484/484 passed in 140.971s, with 484 complete recorder drains.
- All 87 live cases excluded; no shared-cluster operation.
- Three native suites passed in 39.555448562s. Default/live vet passed.
- Inventory: 571 = 484 local + 87 live. Definitions: 1,056 -> 1,054.
- Existing catalog metadata is unchanged except the intentional NamedOutputs
  field addition to 55 draft input/output schemas; each expansion was checked.
- The final normal adapter matches the mutation-matrix normal control.
- The retained output-streaming test still consumes filterMountsByPaths;
  its helper remains, with only its stale consumer comment corrected.

Pre-retirement proof: /tmp/brine-returned-volumes.AFMEq1/pairing.json
SHA256: 22acc601d7811e939a2fe0c0698fbe4a8226e3d6570cea889a7a49ef0183ed3b
Final evidence: /tmp/brine-returned-volumes.AFMEq1/evidence.json
SHA256: 29bb7350dea6f00ffcf81ce56daa57bf0f77591a5ada01c528ef80c84ffa65ed
Normal adapter SHA256: 6ba9ffc0d955bf69dd14dbdcb6c172938f581ff60e7374ec4eaa166664cfc2fd
Recoverable originals: before-container_restored_test.go and retired-block.txt.

Coverage remains the last full checkpoint: 1,966/2,466 (79.724249797%).
No fresh full live-suite or coverage measurement is claimed. The goal remains
active: 25 supplied-status cases, broader discovery fixtures and other legacy
mocks remain. The next local group is the four restored scratch/cache-policy
cases: preserve absence of scratch init containers, all-volume hostPath refusal,
the literal job-7-compile cache-key prefix and all-mount empty subPath checks.
The misleading legacy artifact-precedence title is not evidence of a configured
artifact backend; inspect the fixture, not its title.
Changes remain uncommitted and unpushed on core.


### 2026-09-15 — scratch/cache policy on shared whole-volume checks

Retired JB-container-011/015/016/018 after eight failures at the exact original
Go assertion lines across ten independently selected production-source faults.
Four existing Brine scenarios are strengthened; none is added. Four legacy Go
cases are removed. The shared exact ephemeral-mount check now rejects SubPath
and SubPathExpr on every main-container mount. The one-off scenario uses that
same complete table, preserving refusal of hostPath on every pod volume rather
than checking only the named cache.

The scratch scenario uses a reusable init-container count assertion. This
retains the original absence check; neither the original assertion nor this
replacement claims to observe database cache entries. The standalone hostPath
scenario explicitly selects hostpath and retains the literal
/var/concourse/cache/job-7-compile- prefix, including directory creation policy.
The original misleading artifact-precedence title did not configure an artifact
backend; the replacement keeps the actual standalone construction.

The emptydir scenario now explicitly selects that store and retains original
job 42/build-step metadata with no runtime cache identity. A reusable refinement
makes this absence explicit, independently of metadata; the fixture no longer
has to derive an optional runtime identity merely because a job number exists.
Default draft behavior and existing run-job identity construction are unchanged.

- Scratch init injection, unexpected workspace hostPath, and workspace/cache
  subpaths pass the old Brine checks but fail the strengthened checks.
- Cache hostPath substitution, omitted hostPath and changed job/step key prefixes
  continue to fail the existing contract as well as the original Go assertions.
- HostPathDirectory instead of DirectoryOrCreate preserves the older Brine-only
  guard. SubPathExpr is additionally rejected even though the legacy Go check
  asserted only SubPath.
- Normal and inactive-overlay controls pass. Mutation selection is build-once,
  one BRINE_CACHE_MUTATION branch per invocation. Production container.go is
  unchanged; all injected faults live under /tmp.

Final validation:

- Root Ginkgo: 64 -> 60 leaves, all passed in 21.98375547s.
- The pure nil-resource-map regression passed separately.
- Local Brine: 484/484 in 140.501s, with 484 complete drains.
- Three native suites and default/live vet passed.
- Inventory unchanged: 571 = 484 local + 87 live. All live cases excluded.
- Definitions: 1,054 -> 1056. Two reusable capabilities added.
- Existing catalog metadata changes only by the intentional optional-identity
  boolean in 57 draft input/output schemas; every expansion was checked.
- Normal adapter matches the mutation-matrix normal control.

Pre-retirement proof: /tmp/brine-cache-policy.wvrnQ9/pairing.json
SHA256: 2a493cec042fbb347a67bb0432d7f01e99fb8951c74717073ee51bebc27580e7
Final evidence: /tmp/brine-cache-policy.wvrnQ9/evidence.json
SHA256: c1c962bd59ddbb25234d799be9c0af2997f65feba4e4afaf58f6b8c13989720a
Normal adapter SHA256: 87f4d29926bcb916aacd8814582b0283127fd98551f55571ff60c568462f65ad
Recoverable original cases: before-container_restored_test.go and retired-block.txt.

Coverage remains the last full checkpoint: 1,966/2,466 (79.724249797%).
No fresh full live-suite or coverage measurement, or shared-cluster operation.
The goal remains active: 25 supplied-status cases, wider discovery fixtures and
remaining legacy mocks still require work. Next local candidate is the retained
direct-mode pod construction contract: command/args boundaries, complete EnvVar
members, pod identity, singleton main container and secure defaults must be
observed together, not inferred from unrelated fixtures. The exec-mode contract
also retains spy call-count/argv clauses that need real transport observations.
Changes remain uncommitted and unpushed on core.


### 2026-09-15 — one direct-mode contract using shared container checks

Retired JB-container-000 after 17 failures at the exact original Go assertion
lines across 22 independently selected production-source faults. The direct
command and standalone seccomp scenarios become one scenario preserving the
literal run-test-handle pod, singleton main/busybox roster, separate command
and argument arrays, /workdir, both complete FOO/BAZ environment members,
Never restart policy, unprivileged security defaults and process identity.

The main and sidecars now share environment, command/args and working-directory
checks. This is a wording generalization, not a weaker predicate: full EnvVar
membership and separate exact command/args slices remain unchanged. The existing
sidecar environment, command, args and all three working-directory observations
still fail their isolated faults. The original direct Process.ID guard is also
preserved by an empty-ID fault even though JB-container-000 only required a
nonnull process.

A boundary-shift fault preserves flattened argv but moves -c into Command.
It passes the old Brine command assertion and fails both the original Go
Command assertion and the new split comparison. Pod-name, extra-container,
image, environment, restart and privilege faults also expose gaps in the old
two direct-mode cases. Pod/container security nils, escalation nil/true, seccomp
nil/type, working directory, nil process and missing process ID are checked
independently. All replacement failures are assertion failures, not setup errors.

An initial control rejected the attempted structural sharing: the SDK's typed
handlers require matching concrete Go state, not merely matching fields in a
schema. Direct construction now returns the existing PodCreated state, retaining
its process there. The last-used createTask wrapper is also removed after a compile-only check
identified it as unused (initial-unused-helper.log), along with the now-unused
default worker it alone referenced (initial-unused-worker.log). The separate
RunExtraDirectRun type and three narrow checks
are gone. Other pod construction captures its returned process too. The existing
StepRan integration name check remains byte-identical; its state is genuinely
different. A PodCreated name check is added without changing that integration
contract. Initial diagnostic: initial-state-mismatch.log.

Final validation:

- Root Ginkgo: 60 -> 59 leaves, all passed in 21.822416781s.
- Pure nil-resource-map regression passed separately.
- Local Brine: 483/483 in 140.644s; 483 complete recorder drains.
- Three native suites and default/live vet passed.
- Inventory: 570 = 483 local + 87 live. All 87 live cases excluded.
- Definitions: 1,056 -> 1054; three wording generalizations, three narrow
  checks removed and one typed pod-name check added. Net -2.
- The process field is intentionally added to 46 existing PodCreated
  input/output schemas. Direct action/identity schemas lose unused Command and
  Clientset fields. Other catalog metadata is unchanged.
- Shared sidecar predicates are source-identical except helper/wording/diagnostics.
- Production container.go is unchanged. Normal and inactive-overlay controls
  pass; one BRINE_DIRECT_MUTATION branch is selected per invocation.
- Normal adapter matches the mutation-matrix normal control.

Pre-retirement proof: /tmp/brine-direct-pod.8ggD7y/pairing.json
SHA256: 2cc7a65e5da28595790d5491bddf5a62ea0d096ca1d38341eb59c4b32be51213
Final evidence: /tmp/brine-direct-pod.8ggD7y/evidence.json
SHA256: c9f8fdf7ab5a9f26dfda655dc9f0f1ba31fe249ced9f616dc7f57bf9bc7747ab
Normal adapter SHA256: ef2a5c08766224a7ed00a779fad5017df1941ea9a3f763205886fb746fbd2cd0
Recoverable originals: before-container_restored_test.go and retired-block.txt.

Coverage remains the last full checkpoint: 1,966/2,466 (79.724249797%).
No fresh full live-suite or coverage run, and no shared-cluster operation.
The goal remains active: 25 supplied-status cases, broader discovery fixtures
and legacy mocks remain. Next grouped candidate: retained exec/TTY/hijack
contracts, whose spy call-count/argv and TTY clauses must be replaced by real
transport observations, not inferred from command success.
Changes remain uncommitted and unpushed on core.


## 2026-09-15: real looked-up task hijack options

Retired JB-container-044/045/046 and their shared fake-client/executor fixture
after 18 exact original-Go mutation failure pairs. One three-row live outline
preserves the original /bin/bash -l, /bin/bash with TTY, and /bin/sh -c 'echo hi'
commands, all with nil stdin. These are supervised looked-up task containers;
the existing open-stdin resource terminal cases remain distinct.

The upload observer now shares its passive HTTP transport with hijacks. It
forwards requests, responses and errors unchanged. Each hijack checks one real
POST /exec upgrade, the expected pod/container, TTY and stdin options, the
original supervisor wrapper/command, successful Wait and the same sole pod UID.
No pod status or command result is supplied. The official Debian image is
digest-pinned, and a real /bin/bash premise is checked before each scenario.

Twelve independently selected production faults cover TTY true/false,
duplicate requests, login arguments, wrapper shell/option/length, HUP handling,
exit status, nil process, Wait error and pod target. The proof has 19 live
fault cases, 18 matching original-Go failures, and three sets of three passing
controls. Mutations live only in temporary overlays; production process.go
and container.go are unchanged by this batch.

Diagnostics are preserved: the initial missing worker step failed concrete
state matching before namespace creation; the first image lacked /bin/bash,
so it was replaced without changing the original command. The original -ec
wrapper-option fault exited 143 before reaching the argv assertion and is
excluded from that pair. The refined -xc fault reaches the intended assertion.
Both overlay source/binary versions are retained: only wrapper-option uses the
refined version, with fresh inactive Go/live controls. Other fault evidence
belongs to the original version.

Final validation:

- Root Ginkgo: 59 -> 56 leaves, all passed in 21.400s.
- Pure nil-resource-map regression, three native suites and both vet modes passed.
- Local Brine: 483/483 in 141.274s; 483 complete recorder drains.
- Live preservation: 15/15 in 204.104s: interception 8, resource terminal 2,
  volume I/O 5; every recorder drain completed.
- Inventory: 573 = 483 local + 90 live; definitions 1054 -> 1057.
  Three Go cases become three outline rows: no net case increase.
- Existing interception rows, volume request predicates and catalog metadata
  are preserved apart from the three new definitions.
- Fresh read-only checks confirm all 47 namespaces from this batch's controls,
  diagnostics, faults and regressions are absent. The same theborg node UID
  remains Ready with 12 allocatable CPUs; no node changes were made.
- Normal adapter SHA256:
  db6e4409ee853f95e4b731b8f086c30ce655f84d5c98ab1484fa60147e387152

Pre-retirement proof: /tmp/brine-hijack-options.01zWm3/pairing.json
SHA256: 6869ee3e0d676e99146503105d423038d2f195e8b326ab6016a8a31fc45c0add
Final evidence: /tmp/brine-hijack-options.01zWm3/evidence.json
SHA256: ba801c7be21e57a9778d01bf2a38354971c4554f34b2004f8ad670408f58c326
Cleanup: /tmp/brine-hijack-options.01zWm3/cleanup.json
Recoverable originals: before-container_restored_test.go and retired-block.txt.

Coverage remains the historical full checkpoint, 1966/2466 = 79.724249797%;
this batch is not a new full-suite coverage measurement. The full goal remains
active: 25 supplied-status cases, broader discovery fixtures and legacy mocks
remain. The next cohesive candidate is fresh task exec/input/output transport:
its exact request, returned-volume and pod-retention assertions are still
mock-backed and are not retired on this hijack evidence. Work remains
uncommitted and unpushed on core.


## 2026-09-15: fresh-task nil-stdin contract without another scenario

Retired JB-container-030 after 11 exact original-Go mutation failure pairs.
The existing successful task-completion scenario now checks one actual
supervised exec request with the independently quoted command, nil stdin and
nil TTY. The placeholder check preserves the literal pause Command array and
empty Args, not merely separation from the task command. runTask explicitly
rejects a nil process before waiting. Existing output, exit status, pod identity,
completion persistence and current/restarted-web recovery checks remain.

The task and hijack assertions share one passive supervised-exec predicate.
Its checks are extracted from the already validated hijack observer; request
forwarding, upgrade status, routing, command shape, TTY and stdin checks remain.
The upload predicate and transport are unchanged. Only the production worker's
executor is observed; premise and cleanup execution retain the original executor.
No status or result is supplied, and no new scenario is added.

Independent faults cover equivalent pause-script and Command/Args boundary
changes, duplicate exec requests, the quoted command, wrapper shell/option/length,
HUP handling, exit status, nil process and Wait error. All fail the original
Go assertion and the intended replacement check. Normal and inactive-mutation
Go/live controls pass. The mutation binaries are compiled once and selected
by environment; Ginkgo runs the precompiled test binaries serially.

Final validation:

- Root Ginkgo: 56 -> 55 leaves, all passed in 21.477s.
- Pure nil-resource-map regression, three native suites and both vet modes passed.
- Local Brine: 483/483 in 140.127s, with 483 complete recorder drains.
- Live preservation: 25/25 in 387.872s across task commands/recovery,
  interception and volume I/O, with complete recorder drains.
- Inventory remains 573 = 483 local + 90 live. No Brine case was added;
  removing one Go leaf reduces the combined case count by one.
- Definitions: 1057 -> 1058. Existing catalog metadata is unchanged;
  the observer is private task state, not another exported schema.
- Source checks preserve the shared hijack predicate, volume upload predicate
  and transport. Production process.go and container.go are unchanged.
- All 38 owned namespaces from controls, faults and regressions are absent.
  The same theborg node UID remains Ready with 12 allocatable CPUs.
- Normal adapter SHA256:
  4c4d22d63a1b02e323efcdaa8399b4a1c0a4da8316446d4c9ef138124c5c3bef

Pre-retirement proof: /tmp/brine-fresh-exec.pk3tmu/pairing.json
SHA256: 8ac89afce4cb8531fd9da5208ebfbf0e734fe72a36048e4d3c8226c4e1ec3c48
Final evidence: /tmp/brine-fresh-exec.pk3tmu/evidence.json
SHA256: 8c57b99e7797db458357909c54f745eb73469a1e55baa7d739d4cf91991fe1db
Cleanup: /tmp/brine-fresh-exec.pk3tmu/cleanup.json
Recoverable originals: before-container_restored_test.go and retired-block.txt.

Intermittent approval-service capacity errors prevented commands from starting
between these runs; no live run was restarted on an observation timeout.
The evidence checker was corrected for a one-line comment-induced scenario
location shift, while preserving step-count, name and all other checks.

Coverage remains the historical full checkpoint, 1966/2466 = 79.724249797%;
there is no new full-suite coverage measurement in this batch. The goal remains
active: supplied-status scenarios, broader discovery fixtures and other legacy
mocks remain. Fresh-task input streaming and output extraction retain their
distinct Go contracts and are the next cohesive transport group.
Changes remain uncommitted and unpushed on core.


## 2026-09-15: returned output extraction and pod retention

Retired JB-container-041/042 after 15 exact original-Go assertion failures across
14 independent production faults. One new live no-daemon output scenario
replaces the pair. It keeps the actual mounts returned by FindOrCreateContainer,
requires one output at the declared path and a concrete *Volume bound to the
expected pod with an executor, reads its raw StreamOut archive, and checks the
last actual exec request plus the sole original Running pod UID.

An independent real exec reads the actual task output as an archive and verifies
its exact file/content map. The returned volume must stream those exact bytes,
including archive framing. Neither read repairs the task output, reconstructs
the volume, supplies pod status or simulates command execution. The normal
control observed a 2,560-byte archive. Daemon-backed publication/handoff cases
remain distinct; they do not establish this compatibility Volume API contract.

The existing task setup now retains returned mounts and accepts an explicit
output/directory. Recovery callers ignore the additional return value without
changing behavior. The upload and supervised-exec predicates and transport are
unchanged; one download-request predicate is added. Two definitions serve the
new scenario. The unused mount filter and Run-and-read wrapper are removed.
The separate restoredPod helper remains: integration tests still consume it.
The first final attempt stopped at compilation because removal of the mount
filter left its runtime import unused; initial-unused-import.log preserves that
diagnostic. No live regression started in that failed attempt.

Faults cover missing/duplicate returned mounts, pod binding, executor reporting,
appended/discarded stream bytes, byte-preserving tar path/options, exec pod
target, read error, exit status, Wait error, pod deletion and an extra pod.
All pair with the original assertion they challenge. The byte-preserving
routing faults reach the download assertion, not an unrelated extraction error.
Normal and inactive-mutation Go/live controls pass.

Broad post-retirement checkpoint:

- Root Ginkgo: 55 -> 53 leaves, all passed in 21.312s.
- Pure nil-resource-map regression passed separately.
- Local Brine: 483/483 in 140.077s, with complete recorder drains.
- Live preservation: 26/26 in 399.490s across task commands/recovery,
  interception and volume I/O; all recorder drains completed.
- Three native suites and default/live vet passed.
- Inventory: 574 = 483 local + 91 live. One new Brine case replaces two Go
  leaves, reducing the combined count by one.
- Definitions: 1058 -> 1060. Existing catalog metadata is unchanged.
- Production worker.go, volume.go, container.go and process.go are unchanged.

A final focused refinement makes the expected pod name explicit in the feature.
The preceding assertion used production's GeneratePodName for its expectation:
a scoped wrong-name fault passed that version. The identical fault now fails the
returned-volume binding check against the declared output-extract-handle.
The refined normal output case passes in 21.421s; native suites and both vet
modes were rerun and pass. This changes only the output assertion's expectation,
parameter mapping and feature text. The broad run above predates this refinement;
its evidence is retained separately rather than represented as a rerun of the
latest binary. The naming fault is additional discrimination evidence, not
another claimed original-Go assertion pair.

Pre-retirement proof: /tmp/brine-task-output.CTNaeu/pairing.json
SHA256: dc0eb5415647d5a51008a513e7473e537e4b4d7b4420a3a8985a9279b985756b
Broad checkpoint: /tmp/brine-task-output.CTNaeu/evidence.json
SHA256: 4300aa9cc25e6afc383395467c5c9b27670b8553f6f50bbf909f2574ea620d83
Final refinement: /tmp/brine-task-output.CTNaeu/refinement.json
SHA256: 1fe02f1b5cc021d639a7f7d392b8e6b370e521b2985f0dfca2fc8f55ef7f4114
Final normal adapter SHA256:
a923741b99054e2e22c576b6b7a1f49b89b40bf933381f280934abe8b331fb31
Original tests/helpers: before-container_restored_test.go,
before-jetbridge_suite_test.go and the retired-*.txt snapshots in that directory.

Coverage remains the historical full checkpoint, 1966/2466 = 79.724249797%.
This is not a fresh full-suite coverage measurement. The goal remains active:
input-streaming, other legacy mocks, 25 supplied-status scenarios and broader
discovery fixtures remain. Changes are uncommitted and unpushed on core.


## 2026-09-15 — real database creation refusal

Retired JB-container-056 and its three unused database transition wrappers.
The existing worker.feature case retains its real PostgreSQL CHECK constraint
and direct failed-state query, and now also requires the original Go error
context, "mark container as created". No scenario or definition was added.

Three production-overlay mutations fail the exact original Go leaf and both
fresh/stale Brine creation-refusal cases: missing context, skipped Failed
transition, and swallowed error. The old fresh Brine case passes the context
fault, demonstrating the gap closed by this change. Normal and inactive-overlay
controls pass; production worker.go and the normal adapter are unchanged.

Post-retirement checks: all 52 remaining root JetBridge Ginkgo specs pass
(21.172s), and the affected worker/container-run features pass 38/38 cases
(10.138s + 6.755s), with complete recorder drains. The root roster is exactly
its prior 53 minus the retired leaf. The first regression attempt stopped at
compilation on a now-unused atc import; that import was removed before the
passing run. Inventory remains 574 Brine cases. No full-suite or coverage rerun
is claimed; the last full measured production coverage remains 79.724249797%.

Evidence: /tmp/brine-created-transition.CFOAv4/evidence.json
SHA256: bf8095222efd687f69de2b39b85aea2d49ded5d6cb0919170be49ddf03834228
Pairing: /tmp/brine-created-transition.CFOAv4/pairing.json
SHA256: af22a71887876ab9bd6958bdd7653713b50d7a35f8a8e5b7a7c95494bd3631a8
Original source and removed blocks are saved alongside those reports.

The goal remains active: legacy input-streaming, concurrency, volume and GC
mocks, supplied lifecycle/node status and broader discovery doubles remain.
No live cluster operations, commits or pushes were performed in this group.


## 2026-09-15 — consolidate concurrent container contracts

Retired JB-container-061/062/063 after ten exact-original-Go mutation failures
across eight faults. One new worker.feature scenario submits five independent
containers concurrently through separate workers sharing real PostgreSQL and
Kubernetes API clients. It checks creation/submission errors, non-nil results,
actual DB identities and rows, and all five independently identified API pods.
No kubelet or supplied pod status is used; this proves submission, not execution.

The existing property-readback case keeps its original key/value assertion and
adds 20 writers with 20 readers on the same production container. The shared
start gate joins every goroutine before disposal. All errors and exact values
are checked; editing a returned snapshot must not change the container. A
separate alias fault reaches and fails that new snapshot assertion, with a
passing inactive control. This is not a race-detector result or a claim that
all possible schedules were explored.

The eight paired faults cover dropped/empty properties, overlapping creation
refusal, nil containers, creation errors, pod-name collisions, omitted pod
creation and submission errors. Baseline and inactive controls pass. The first
build failed because the assertion called Handle on runtime.Container; it was
corrected to DBContainer().Handle() before any paired runs or retirement.
Temporary approval-service capacity errors delayed work but did not change
its scope. Mutation source and original tests remain in the audit directory.

Post-retirement validation: 49/49 root JetBridge specs (exactly 52 minus these
three); worker.feature 28/28 in 9.352s; container-lifecycle.feature 3/3 in 5.122s;
all three native suites in 37.586s; default/live vet and diff checks pass.
Recorder drains completed, including the deliberate failing runs. The other
Go test bodies and production worker.go/container.go are unchanged. Inventory
is 575 = 484 local + 91 live; definitions 1060 -> 1064. One new Brine scenario
replaces three Go tests, reducing combined cases by two. No live operations,
full-suite run or fresh coverage measurement is claimed.

Evidence: /tmp/brine-concurrent-containers.9KynD7/evidence.json
SHA256: 2a1d5b80b298dd2a06ea3fa484f73eb18cbb8107cea66799881c2d8564947d04
Paired mutations: /tmp/brine-concurrent-containers.9KynD7/pairing.json
SHA256: ae4f96f6481055811ee873b5378304ee0c7079ce4d220dc03e9792a29c2616e9

The goal remains active. Legacy input-streaming, volume and GC mocks, supplied
lifecycle/node status and broader discovery doubles remain. Coverage remains
the historical full checkpoint, 79.724249797%. Work is uncommitted on core.


## 2026-09-15 — preserve raw and gzip pipe errors

Retired JB-volume-014. The existing absent-pod scenario uses the production
SPDY executor against a real API refusal. Its read action now opens and fully
drains both raw and gzip streams, recording opening, reading and closing
separately. Both opens must succeed; each reader must report the original
exec-stream and not-found diagnostics. The write row and its shared failure
assertions remain. There is no simulated executor error or supplied pod status.

Four exact original-Go mutation pairs cover swallowed errors, raw-only loss,
rewritten errors and eager opening failure. The previous gzip-only Brine case
survives raw-only loss; the replacement fails. A gzip-only fault separately
fails the replacement while the original raw Go leaf passes, preserving the
old Brine contract too. Normal and inactive controls pass.

Post-retirement checks: 48/48 JetBridge Ginkgo specs, exactly the previous 49
minus this leaf; 14/14 local volume-feature cases in 23.812s; all three native
suites in 37.135s; default/live vet and diff checks pass. All recorder drains
completed. Other Go test bodies and production volume.go are unchanged.
Inventory remains 575 = 484 local + 91 live; definitions 1064 -> 1065. No
scenario was added. No live-cluster, full-suite or coverage rerun is claimed.

Evidence: /tmp/brine-volume-read-error.zbLgMg/evidence.json
SHA256: e433d59551d410cdcd0f9608a2182df2f1b0593cd9dd9b59a9beec579cb84e0d
Pairing: /tmp/brine-volume-read-error.zbLgMg/pairing.json
SHA256: 87525bcc3b8285a473e08c50038eacabc525b53013b7ce42e4f888a066e33e84

The remaining volume Go cases still require exact file-selection, byte and
metadata coverage; extracted file contents alone are not equivalent evidence.
Legacy input-streaming, GC mocks, supplied lifecycle/node status and broader
discovery doubles also remain. The goal is active; work remains uncommitted
on core. Last full measured production coverage remains 79.724249797%.


## 2026-09-15 — exact live file selection

Retired JB-volume-013. The existing live round-trip scenario still uploads and
reads hello.txt. It also uploads pipeline.yml and requires a raw single-file
archive containing only pipeline.yml with its independently declared contents.
The actual retained Volume performs this read. Its observed request must be
one POST/HTTP-101 exec with the exact mount root, file selector, tar argv and
stdout options. No command or response is supplied by the observer.

The download predicate now shares a member-aware implementation; the existing
returned-task-output check still selects "." through its wrapper. VolumeRead
retains its source only for follow-up observations. Five exact Go/live mutation
pairs cover "./pipeline.yml", mount "/.", "-cf", whole-volume selection and
opening failure. The first three still return correct file contents and are
rejected specifically by the request assertion. Normal/inactive controls pass.

Post-retirement checks: 47/47 JetBridge specs, exactly the prior 48 minus this
leaf; 6/6 live preservation cases in 80.108s (all five volume-I/O cases and the
returned-task-output case); 14/14 local volume cases in 23.395s; three native
suites in 35.719s; default/live vet and diff checks pass. Recorder drains are
complete. All 13 owned live namespaces have matching UID cleanup records and
were independently confirmed absent. The authorized node retained UID
91a864d5-c1e2-4dc4-8696-4e800e53c328, Ready status and 12 allocatable CPUs.

Inventory remains 575 = 484 local + 91 live, with 1065 -> 1066 definitions.
No scenario was added. Other Go test bodies and production volume.go are
unchanged. No full-suite or coverage rerun is claimed.

Evidence: /tmp/brine-volume-file-selector.xQNmet/evidence.json
SHA256: 84ef9fd18591952c739657e76db02e83ce16176c9c6524da141dc5347b840d66
Paired faults: /tmp/brine-volume-file-selector.xQNmet/pairing.json
SHA256: f21bb46ce0622d285c33e6dee9fd0ff4c2ea38c901d14bd6d4d1ae54cf3873ff

The goal remains active. Exact root-stream byte/metadata and volume-to-volume
contracts remain mock-backed, alongside legacy input-streaming, GC and broader
discovery/status doubles. Last full measured production coverage remains
79.724249797%. Changes remain uncommitted and unpushed on core.


## 2026-09-15 — real exported volume execution metadata

All five existing live volume cases now verify transfer purpose and declared
mount through the production OTLP exporter and official collector. The
scenario-scoped tracing resource is reused; no PodExecutor, exporter, receiver
or command is replaced. The passive HTTP observer supplies actual transfer
counts and destinations, while mount expectations come from fixture inputs.
Missing/mislabelled spans cannot disappear through purpose-based filtering:
every observed transfer needs a matching exported metadata entry. The separate
hostname premise is excluded, and any transfer mislabelled as that premise
would leave the required count short.

The collector reader now exposes span-level attributes using the same parsed
attribute type as events. Its native test checks those values through the real
exporter and collector, as well as existing event/identity/disposal checks.

Four exact original-Go/live mutation pairs cover missing upload/download
purpose and wrong upload/download mount metadata. All fail at the expected
metadata assertions while preceding file-content checks pass. Normal and
inactive controls pass. Original Go tests are retained: this closes their
metadata gap, not the still-unproven exact input/output byte boundary.

Preservation: all 5 live volume cases pass in 67.055s, all 14 local volume
cases pass in 23.057s, three native suites pass in 37.187s, and default/live
vet and diff checks pass. Recorder drains completed. All 11 owned live
namespaces have matching UID cleanup records and were independently confirmed
absent. Production volume.go and volume_restored_test.go are unchanged.
Inventory stays 575 = 484 local + 91 live; definitions 1066 -> 1067. No new
scenario, Go-test retirement, full-suite run or fresh coverage claim is made.

Evidence: /tmp/brine-volume-metadata.Yy1JKv/evidence.json
SHA256: 60a0d16021b719aae25f74153d4ba23faaa459f97f0ca012d5eabb1e6290eded
Paired faults: /tmp/brine-volume-metadata.Yy1JKv/pairing.json
SHA256: 320daf872f1cdbfd23b6d773ce7b9b737503519d06b29e5064bf17b02ba20d26

For the next byte-fidelity check, local upstream source establishes that
SpdyRoundTripper.NewConnection uses its private connection, not response.Body;
wrapping that response body would not observe upgraded data. The public
spdystream frame reader can decode stream IDs, headers and DATA frames, but a
real connection observation point still needs validation. No wire observer
has been implemented or assumed correct.

The goal remains active. Exact stream bytes and volume-to-volume contracts,
legacy input-streaming, GC and broader discovery/status doubles remain. Last
full measured coverage is still 79.724249797%. Work is uncommitted on core.

## 2026-09-15 — real volume byte fidelity; two root-stream tests retired

The five existing live volume scenarios now compare caller bytes with actual
SPDY DATA payloads, including tar padding, instead of relying only on extracted
files. Uploads compare against independently constructed caller archives;
raw/gzip reads compare against bytes read and independently decoded by the
caller; handoffs compare source stdout against destination stdin. Each
operation also requires the exact independently declared pod/container/argv
and number of real upgrades. The existing production-OTLP metadata check
requires all observed transfers to have been checked.

The observer is a loopback, mutually authenticated TLS forwarding route to the
verified real Kubernetes endpoint, limited to registered fixture pods in the
owned namespace. It supplies no executor, command output or Kubernetes response.
Captures exclude HTTP credentials, are bounded to 1 MiB per direction, and
require artifact DATA FIN. A closing control-frame fragment is accepted only
after that FIN. Pure parser tests reject incomplete DATA, missing FIN, duplicate
or missing stream declarations, and data after FIN. Connection closure prevents
late capture writes; disposal closes and joins the owned server and handlers.

The first volume uses NewVolume; the additional handoff volume uses
NewDeferredVolume plus SetPodName. Thus root streaming reaches the original
direct-constructor path while the handoff still exercises deferred binding.
The first roundtrip includes both raw and gzip root uploads; ordinary reads and
handoffs exercise both encodings without adding scenarios or vocabulary.

Four byte-padding production faults fail at the exact original Go byte
assertions (lines 125, 142, and 205 in the archived source) and at the new
real-wire comparisons. Normal and inactive controls pass. The two root-byte
faults were replayed after switching the first fixture to direct construction.
Earlier real request and exported-metadata pairs remain indexed in the preceding
entries; the byte comparison closes the missing assertion class.

Only the two merged root StreamIn/StreamOut Its were removed, retiring rows
JB-volume-004/005/007/009/010/011. Their old source remains recoverable in
before-original-volume-tests.go under the evidence directory. The two
volume-to-volume Its are unchanged: their explicit call-order assertion is not
replaced by an observer that matches concurrent real transfers by identity.
No deletion credit is claimed for those cases.

Final checks: 45/45 remaining JetBridge specs (exactly the prior 47 minus those
two), 5/5 live volume scenarios in 67.694s, 14/14 local volume scenarios in
23.421s, three native suites including the parser tests in 36.894s, and both
default/live vet. The 29 owned live namespaces from the prototype and integrated
runs have matching UID cleanup records and were independently confirmed absent.
The authorized node retained its UID, Ready state and 12 allocatable CPUs.
Production volume.go and the retained handoff test bodies are unchanged.

Inventory remains 575 = 484 local + 91 live, with 1067 definitions. No Brine
scenario or step definition was added. This adds real transport-observation
infrastructure; it is not a claim of a net source-line reduction.

Evidence: /tmp/brine-volume-wire.H1iwSm/evidence.json
SHA256: 12095ea049fcc151f660951254ee17e82335d7ffdf31c82a5ff848ba169efdc7

Launch diagnostics are retained separately: one invocation omitted the shell
interpreter for a non-executable environment script; another selected the
local-only manifest by passing a repository feature path. Both were corrected.
An initial integrated control exposed EOF at a closing control-header field
boundary; its regression test and subsequent passing controls are retained.
These failures are not counted as passing validation.

The goal remains active. Remaining work includes the two handoff contracts,
legacy input streaming, GC and discovery doubles, six status-writing sites
across five Brine fixture files, final consolidation review, and final
full-suite/coverage/CI validation. Last full measured production coverage
remains 79.724249797%; no fresh full-suite or coverage run is claimed here.
Work remains uncommitted and unpushed on core.

## 2026-09-15 — final direct-volume handoff contracts migrated

The remaining two Its in volume_restored_test.go are retired, and that file is
removed. They are replaced by direct/deferred rows of the existing real-volume
handoff scenario. Binding style is explicit data in the two existing setup
definitions, not a new family of steps or a hidden constructor choice. Both
rows use real pods, the production executor, raw/gzip transfers, exact
independently declared routes/options/counts, wire-byte equality, and real
exported execution metadata.

This corrects the preceding entry's ordering concern. In the ordinary
fakeExecExecutor path, io.ReadAll(stdin) happens BEFORE appending execCalls;
source calls append before writing their stdout. Consequently the two-element
slice is forced into source/destination order by draining the pipe, not by
executor-entry or HTTP-upgrade order. Its execFuncIO path has different
recording timing. No production request-order contract existed here to
preserve. The replacement identifies each actual transfer by its source or
destination binding and compares the bytes flowing through the real pipe.
AGENTS.md records this verified trap while the shared mock still exists.

Seven production-overlay faults fail the exact original Go assertion and the
corresponding Brine row: wrong direct source/destination binding, wrong deferred
source/destination binding, one extra source exec, and padding corruption on
either side of the handoff. Binding faults are rejected by the owned route
boundary; the extra exec fails with three observed transfers instead of two;
padding fails byte equality. Normal and inactive controls pass. Production
volume.go and the shared mock implementation were not changed.

Checks after removal: all 43 remaining Ginkgo specs pass, exactly the prior 45
minus these two; three native Brine suites pass in 41.000s; all 14 local volume
cases pass in 25.151s; default/live vet and diff checks pass. The final fixture
source passed all 6 live volume cases in 80.727s before the test-file-only
retirement. Every recorder drain completed. All 14 owned namespaces have
matching UID cleanup records and were independently confirmed absent. The
authorized node retained its UID, Ready state and 12 allocatable CPUs.

Inventory is 576 = 484 local + 92 live, with 1067 definitions. The one additional
expanded case is the second binding row; no distinct scenario story or step
definition was added. The old test source remains recoverable at
/tmp/brine-volume-handoff.JXFfCl/before-volume_restored_test.go.

Evidence: /tmp/brine-volume-handoff.JXFfCl/evidence.json
SHA256: d51c560f5f2b3e7ecfe78e34fab803d7b75dddee7210efc5478c41ea0262803a
Paired faults: /tmp/brine-volume-handoff.JXFfCl/pairing.json
SHA256: 87cb6ba708c97bad5a02bff7fd592a2da5ee21a9441b935e827bd6001814ae68

The remaining Ginkgo count is NOT a mock inventory. Twenty specs are in
supervisor_script_test.go (4), supervisor_test.go (15), and the resource
cancellation script group (1); the other 23 are pause replacement (14),
integration (3), input streaming (1), and restored runtime routing (5).
Additional native Go contracts, including three daemon peer-fallback tests,
are outside that Ginkgo count. Inspection confirms those daemon tests still
use fabricated HTTP responses, rewritten destinations and a fake Kubernetes
client; the Brine OCI registry, by contrast, runs an actual registry and
Distribution authentication over TLS despite using httptest for its listener.

The next migration work is the remaining lifecycle/integration/routing and
native daemon/GC doubles, including the existing six status-writing sites in
five Brine fixture files. Final consolidation and full-suite/coverage/CI
validation remain. Last full measured production coverage is still
79.724249797%; no fresh full-suite or coverage claim is made. The full goal
remains active. Work remains uncommitted and unpushed on core.

## 2026-09-15 — supervised-task TTY contracts migrated

The two existing terminal rows now exercise both real execution paths:
a resource process with open stdin observes its actual remote PTY, and a
supervised task with nil stdin must send exactly one exec request with the
declared TTY flag. The task uses the original non-nil 80x24 TTY specification
for the positive row and nil for the negative row. Its command runs through
the production supervisor; redirected command output is deliberately not used
as evidence of remote TTY mode. The existing passive exec observer checks the
task's request, exact pod/container route, nil stdin and supervised command.

This closes the earlier different-path gap rather than relying on resource or
hijack coverage. Three production-overlay faults apply only when p.supervised():
force TTY off, force TTY on, and execute twice. All three fail the exact original
Go TTY/count assertion and the strengthened Brine Then. All three still pass
the archived resource-only Brine scenario, proving why that earlier coverage
was insufficient. Normal and inactive controls pass both terminal rows; the
resource and task really execute under each requested mode.

Only JB-behavioral_runtime_spec-007/008 were retired from
behavioral_runtime_spec_restored_test.go. The two sidecar-log entries and
direct-command entry are unchanged. Production process.go is unchanged.
The original test and resource-only fixture are preserved in the evidence
directory, alongside the mutation overlay and original/new runner outputs.

Verification: 41/41 remaining Ginkgo specs pass (exactly 43 minus the two TTY
entries); three native Brine suites pass in 35.780s; default/live vet, vocabulary
checks, inventory and diff checks pass. The two live rows pass in 40.411s
(normal) and 41.204s (inactive mutant). Every recorder drain completed. All
10 owned namespaces have matching UID cleanup records and were independently
confirmed absent. The authorized node retained its UID, Ready state and
12 allocatable CPUs.

Inventory stays 576 = 484 local + 92 live and 1067 definitions: no added scenario,
example row or step definition. One shared probe and the existing request
observer replace the old mock-only TTY assertions.

Evidence: /tmp/brine-task-tty.cSIGDH/evidence.json
SHA256: 90d85961d81f6c8022b536d91e9a44a216c087282aa65e1d421f3e27c184afb8
Paired faults: /tmp/brine-task-tty.cSIGDH/pairing.json
SHA256: a649786f4435506aefa0ca975434fe04653de38029a5a4449a65669ed8f7d5dd

The next adjacent gap is direct-mode sidecar log routing: the existing real
sidecar-log scenario uses execProcess, while the retained Go cases use
Process without an executor. Direct command embedding, integration/lifecycle,
native daemon/GC doubles and the existing synthetic-status fixtures also
remain, followed by final consolidation and full-suite/coverage/CI validation.
The 41 remaining Ginkgo specs include pure shell/quoting tests and are not a
count of remaining mocks. No fresh full-suite or coverage run is claimed;
last full measured production coverage remains 79.724249797%.

The active goal remains incomplete. Changes are uncommitted and unpushed on core.

## 2026-09-15 — direct sidecar log contracts migrated

The existing sidecar-log outline now covers exec/direct mode and dedicated/fallback
writers in four rows, sharing the same three definitions and real Kubernetes fixture.
Every row independently reads the helper's actual kubelet log and observes its real
terminated status, pod UID and assigned node. A separate passive client observer
requires the runtime's successful helper Follow request; the source probe uses the
unobserved client and cannot satisfy that assertion. Exact main/dedicated/prefixed
output checks retain blank lines and an unterminated tail. The direct compatibility
formatter's existing final newline is explicit; production exec formatting is unchanged.

Two overlay faults suppress direct Process logging for the selected writer mode.
Each fails the exact original any-GetLogs assertion and the replacement Brine Then.
Both exec rows pass under each direct-only fault, establishing the earlier path gap.
The old assertions did not identify sidecars or prove routing; these matched faults
therefore suppress all direct logging, not just helper logging. No stronger historical
claim is made. Normal and inactive controls pass all four rows (59.825s and 62.958s).

Only JB-behavioral_runtime_spec-009/010 and their unused synthetic-status helper were
removed. The PE-02 body and production process.go remain byte-identical. The exact
root Ginkgo inventory falls from 41 to 39, with all 39 passing in 20.716s. Three native
Brine suites pass in 42.036s; default/live vet, vocabulary, catalog, inventory and diff
checks pass. All 14 owned namespaces have matching UID cleanup records and were
independently confirmed absent; the authorized node remains Ready with its original
UID and 12 allocatable CPUs. No cluster-wide changes were made.

Inventory is 578 = 484 local + 94 live, with 1067 definitions.
The two additional expanded rows add an execution mode to the existing story, not
new scenario stories or step definitions. Original source and mutation/runner results
are recoverable in /tmp/brine-direct-sidecars.aAJqqF.

Evidence: /tmp/brine-direct-sidecars.aAJqqF/evidence.json
SHA256: b83057450049f5393288ab2f64fab23943c9ffe00c34a8c262ac2ec1ed013fe0
Paired faults: /tmp/brine-direct-sidecars.aAJqqF/pairing.json
SHA256: 2a7898ae0015218b096fecc6ca58f535a2c8de4b600b5f4cab4e434397b2716e

Remaining work includes direct command embedding, lifecycle/integration and native
daemon/GC doubles, synthetic-status fixtures, final consolidation and full-suite,
coverage and CI validation. The remaining Ginkgo count includes 20 shell/quoting
specs and is not a count of mocks. Last full measured coverage remains 79.724249797%;
this is targeted verification, not a new full-suite/coverage claim. The full goal
remains active. Changes are uncommitted and unpushed on core.

## 2026-09-15 — final restored runtime contract migrated

The direct pod-construction scenario is now an outline with its original shell row
and the exact /opt/resource/in, /tmp/build/get row from JB-behavioral_runtime_spec-031.
Both reuse the same command/argument, ordered roster, working-directory, environment,
security, restart-policy and attachment assertions. The shared When carries Type=task,
requires the direct Process and snapshots ContainersCreated around that same Run.
This uses the real API server and database, without a fake executor or supplied status.
It proves construction, as the original did; it does not claim kubelet execution.

Three faults, restricted to direct task /opt/resource/in construction, independently
change the command, change its argument, or omit the creation counter. Each fails
the exact original Go assertion and the replacement assertion; the original shell
row still passes. Normal and inactive controls pass both rows. An initial fixture
probe omitted ContainerSpec.Type; review corrected that mismatch and the entire
focused matrix was rerun with task-specific faults before retirement. Initial
outputs are kept separately and are not the final proof.

That was the last entry in behavioral_runtime_spec_restored_test.go, so the file
is removed. Its source is recoverable in the evidence directory. Production
container.go remains byte-identical. The exact Ginkgo inventory falls from 39 to 38; all remaining specs pass (20.176158385s). Three native Brine suites pass
(37.091846444s); default/live vet and vocabulary checks pass. The entire affected
container-run.feature passes 12/12 cases in 6.139s, with complete recorder drains.

Inventory is 579 = 485 local + 94 live, with 1067 definitions: one added
expanded row, no added scenario story or step definition. No live-cluster operations
were needed for this construction-only contract.

Evidence: /tmp/brine-direct-command.GldtoX/evidence.json
SHA256: d16f7f1b10189d9ebfcbf1393978798d5e9f6a239f07de0a55089c7126c8dcd4
Paired faults: /tmp/brine-direct-command.GldtoX/pairing.json
SHA256: 0b61ffa9ebf094f5100c5a0f9ed99ef29e3e885550cf6f7700b87e112eab7c71

Remaining work includes workflow/integration and lifecycle doubles, three native
daemon peer-fallback tests, six supplied-status sites in five Brine fixture files,
final consolidation, and full-suite/coverage/CI validation. Twenty remaining Ginkgo
specs are shell/quoting checks; the total is not a mock count. Last full measured
coverage remains 79.724249797%; no fresh full-suite or coverage run is claimed.
The full goal remains active, with changes uncommitted and unpushed on core.

## 2026-09-15 — basic workflow absorbed into live completion coverage

JB-integration-000 is retired into the existing completion/recovery outline. Its
second row uses ubuntu:22.04, task-abc123 and the exact echo hello world && exit 0
command; the original BusyBox/opaque-handle row remains. The shared fixture now
accepts its declared image and omits ProcessSpec.ID, requiring the production
default to return the persisted handle. It checks the first main container and
declared image. Existing assertions preserve the pause command, k8s-worker-1
label, one exact supervised exec without stdin/TTY, zero exit and stored status.
Both rows keep the current-web and restarted-web completion/recovery checks.

Eight Ubuntu-only faults independently break ID, image, pause command, label,
supervised command, exec count, returned exit or stored completion. Each fails
the exact original Go assertion and replacement assertion. A ninth fault prepends
a decoy container: it fails the original first-container image assertion and the
new explicit order check. The BusyBox row passes with all nine faults enabled.
Normal/inactive controls pass both rows; final-source inactive controls pass both
rows in 34.577s. No production code was changed.

The shared task feature plus completed-task interception passed 15/15 live cases
in 273.622s. That run started before the additional order-only guard;
the final-source completion rows, ordering fault and all-faults BusyBox control
were checked afterward. All 37 remaining Ginkgo specs pass (exactly 38 minus
this workflow); the other two integration bodies are byte-identical. Final-source
native Brine suites (3, 38.535569737s), default/live vet and vocabulary checks
pass. All 32 owned namespaces have matching UID cleanup records and were
independently confirmed absent. The authorized node remains Ready with its original
UID and 12 allocatable CPUs.

Inventory is 580 = 485 local + 95 live, with 1067 definitions. One expanded
row was added to the existing story; no new step definition was added. The removed
test source is recoverable at /tmp/brine-basic-workflow.4Ajdp3/before-integration_restored_test.go.

Evidence: /tmp/brine-basic-workflow.4Ajdp3/evidence.json
SHA256: a9b923c3e590f60c6d165cb64192d9ccbe9eb7fd09804046039782e8efcc24a9
Eight-field pairing: /tmp/brine-basic-workflow.4Ajdp3/pairing.json
SHA256: 02387d90265ca4a0f14913a463af348aa56fce01461dfd5af24d6f4016c7c360
Ordering pairing: /tmp/brine-basic-workflow.4Ajdp3/first-pairing.json
SHA256: 011d5c7430927dc7877f8333951c824a5c96be793f65fe329446da1d3427d361

The put-input and sidecar workflow cases remain, along with lifecycle and native
daemon/GC doubles, supplied-status fixtures, final consolidation and full-suite,
coverage and CI validation. The pipeline still has a 30-minute Brine task timeout;
the last full CLI run alone took 28m44.848s before subsequent additions, so final
CI timing and runner-image provenance need verification. Last full measured
coverage remains 79.724249797%; this checkpoint is not a new full-suite/coverage
claim. The full goal remains active. Changes are uncommitted and unpushed on core.

## 2026-09-15 — real mounted npm/PostgreSQL workflow

JB-integration-012 is retired. The replacement reuses the live task runner with
one new application-premise definition and one sidecar-workflow scenario. It runs
the original node:18 / postgres:15 pair, task-sidecar handle and supervised npm test
command in the original application directory. Actual Volume.StreamIn calls stage
package.json, a Node test and namespace-specific input data in the kubelet-mounted
application volume. PostgreSQL answers SELECT 1 before npm reads the application
input and connects to the database. Setup uses a separate real SPDY executor and
cannot satisfy the runtime's exactly-one-exec assertion.

The original sidecar roster, image, both env vars, TCP port and complete ordered
VolumeMount equality are asserted before setup can obscure a bad spec. Every
container mount must resolve to a pod Volume. Main and PostgreSQL each have 256Mi
memory limits within the existing 512Mi namespace quota; PGDATA uses the existing
shared emptyDir, not hostPath or a new persistent volume. No supplied status, fake
artifact, fake executor or fabricated resource response was introduced. Explicit
application preparation is not automatic artifact-staging coverage: production
streamInputs remains a no-op and that separate legacy contract is still retained.

Ten Node/sidecar-specific faults independently break sidecar presence, image,
password, database, TCP port, mount equality, command, exec count, target container
or exit result. All fail the exact original Go assertion and corresponding Brine
assertion. The ordinary completion/recovery and returned-output controls both
pass with all faults enabled. Normal, inactive and final real workflow runs pass;
the final run took 31.229s. All recorder drains completed.

The put-input body is unchanged. The exact root Ginkgo inventory falls from 37 to 36,
with every remaining spec passing. Three native Brine suites pass in 37.364515168s;
default/live vet, vocabulary, catalog, inventory and diff checks pass. All 15 owned
namespaces have matching UID cleanup records and were independently confirmed
absent. The approved node retains its UID, Ready state and 12 allocatable CPUs.

Inventory is 581 = 485 local + 96 live, with 1068 definitions. This is one
new real workflow and one premise definition, replacing the corresponding Go
workflow; ordinary task execution, recovery, output and transport assertions
remain shared. Preserved original: /tmp/brine-npm-postgres.aikaVc/before-integration_restored_test.go.

Evidence: /tmp/brine-npm-postgres.aikaVc/evidence.json
SHA256: 8ca3676577bbb034155d08aafadb07116affe9755e539078de0ab43248458e44
Paired faults: /tmp/brine-npm-postgres.aikaVc/pairing.json
SHA256: b17271b9a7446b09587cd9d8ff5f2964fd0cd74b025e6c86372c20c5ffa34c6e

Remaining work includes the put-input workflow, the no-op input contract, pause
replacement/lifecycle doubles, native daemon peer-fallback tests and supplied-status
Brine fixtures, then consolidation and full-suite/coverage/CI validation. The
remaining Ginkgo count includes 20 shell/quoting specs and is not a mock count.
Last full measured coverage remains 79.724249797%; no new full-suite/coverage claim
is made. The complete goal stays active. Work is uncommitted and unpushed on core.

## 2026-09-15 — mounted-input no-op contract

JB-container-040 is retired into the existing mounted-application story. Its
BusyBox/input-vol-1/echo-done fixture and the npm/PostgreSQL workflow share one
outline and one parameterized premise. Real Volume uploads prepare the input;
only production runtime execs count toward the exactly-one supervised command
assertion. This does not claim automatic init-container staging coverage.

Three isolated production faults (extra exec inside streamInputs, changed
command, wrong exit result) fail both the exact original Go assertions and the
corresponding Brine assertions. Both real cases pass normally (47.013s); the
inactive no-op case and PostgreSQL all-faults control pass, with complete drains.
All 7 owned namespaces were independently confirmed absent. Production
process.go and the remaining put-test body are unchanged.

The last container-restored test file and unused restoredTask helper are removed;
restoredPod and fakeArtifact moved unchanged beside their remaining put consumer.
The original is recoverable at /tmp/brine-input-noop.Bg6ny2/before-container_restored_test.go.
Root Ginkgo: 36 -> 35, all passing. Three native Brine suites, default/live vet,
vocabulary, catalog, inventory and diff checks pass. Inventory: 582 cases
(485 local + 97 live), 1068 definitions: one additional row, no additional
story or definition. Evidence: /tmp/brine-input-noop.Bg6ny2/evidence.json
SHA256: 10116085fcf08b227c22b79094d7b758413eb7ca1cd0a4712eefca57699a7734
Paired failures: /tmp/brine-input-noop.Bg6ny2/pairing.json
SHA256: 6fa454472970c6481401c30a2e909593f28c415ca53c2a624d42c8596413f5cb

The put workflow, pause-replacement and native daemon doubles, supplied-status
Brine fixtures, final consolidation and full-suite/coverage/CI validation remain.
The remaining Ginkgo count includes 20 shell/quoting cases, not 35 mock cases.
No new full-suite or coverage claim is made. Goal active; uncommitted on core.

## 2026-09-15 — clean pause-pod recovery on both entry paths

Three core-added pause_pod_replacement_test.go cases are retired: clean exit
before waitForRunning, clean exit on the first exec dial, and refusal after two
failed dials spend the replacement budget. The old 'across both paths' title
used two dial failures, not a startup failure followed by a dial failure.
One new outline in live/container-lifecycle.feature and two definitions share
real worker/pod setup, TERM of the owned pause PID 1, kubelet Succeeded/exit 0,
transparent pre-dial timing control, and unmodified Kubernetes upgrade responses.
Successful pod creations, runtime exec attempts, returned status/output, exact
command/options and replacement UID are observed; signal execs are excluded.

Five production-overlay faults are detected by both the exact old test and
replacement: startup refusal, dial refusal, unlimited replacement, wrong exit,
and duplicate exec. Normal three-row run: 59.424s; final-assertion-order inactive
run: 61.154s. Existing check-pod reuse passes with all faults enabled. All drains
complete, and all 13 owned namespaces are independently absent. No production
source, node configuration, image, deployment or remote branch changed.

Root Ginkgo falls 35 -> 32, all passing; all retained It bodies are unchanged.
Three native Brine suites, default/live vet, vocabulary, catalog and diff checks
pass. Normal adapter rebuilt. Inventory: 585 (485 local + 100 live), 1070 definitions.
Original tests: /tmp/brine-pause-recovery.Q2vhRM/before-pause_pod_replacement_test.go
Evidence: /tmp/brine-pause-recovery.Q2vhRM/evidence.json
SHA256: 47570a2b4e63971c17cf75c0e1b13cdaa4079e35d684a2986593f8d310b74064
Pairing: /tmp/brine-pause-recovery.Q2vhRM/pairing.json
SHA256: ca96d0c8ff4417318f26d2b09bc772949ad5a410bec53066245bc6cb10dbca53

The eleven failure-specific/streaming pause cases, put workflow, native daemon
doubles and supplied-state fixtures remain, alongside final consolidation and
full-suite/coverage/CI validation. The other twenty Ginkgo cases exercise shell
or quoting behavior. This is not a fresh full-suite/coverage result. Goal active;
work remains uncommitted and unpushed on core.

## 2026-09-15 — OOM recovery shares the pause outline

Three further core-added cases are retired: OOM before the startup wait, OOM
on the first exec dial, and the separate OOM-diagnostics case. Two rows extend
the existing outline; no new story or definition. Both successful rows require
the diagnostic header and OOMKilled reason, so the dial row absorbs that extra
legacy case. All retained legacy It bodies are unchanged.

An explicit pre-existing pod reuses liveOOMPod without its keep-running helper.
Its real PID 1 allocation is armed only after the existing helper verifies the
64Mi cgroup limit. Kubernetes must report one main container, Failed, OOMKilled
and exit 137. Only the replacement is runtime-created in this OOM premise;
the successful-create counter includes both actual API creations. No status,
exec response, node pressure, clock or OOM result is supplied. Shared OOM code
and production process.go are unchanged; the normal adapter is rebuilt.

Four matched original-Go/Brine faults cover startup refusal, dial refusal,
missing diagnostics and a corrupted OOM reason. Normal and inactive OOM rows
pass (48.797s / 49.241s); all three TERM rows pass with every OOM fault enabled.
All drains complete and all 11 owned namespaces are independently absent.
Root Ginkgo: 32 -> 29, all passing. Three native Brine suites, default/live
vet, vocabulary, catalog, inventory and diff checks pass. Inventory: 587 cases
(485 local + 102 live), 1070 definitions. Three Go cases become two Brine rows.

Original tests: /tmp/brine-pause-oom.tYNege/before-pause_pod_replacement_test.go
Evidence: /tmp/brine-pause-oom.tYNege/evidence.json
SHA256: 5afd28826309c322e7538cc6ce5bb0d164a4789d5c7a8d8b6e8d62efa57f5a4e
Pairing: /tmp/brine-pause-oom.tYNege/pairing.json
SHA256: 8de5e6c3ca354bdd776f05faa9fae4b93788b6957c58285ff458302b23ba1f7b

Eight pause failure/streaming cases, the put workflow, native daemon doubles
and supplied-state fixtures remain. The other twenty Ginkgo cases exercise
shell/quoting behavior. Final consolidation and full-suite/coverage/CI checks
are still required; this is not a fresh full-suite or coverage measurement.
Goal active; changes uncommitted and unpushed on core.

## 2026-09-15 — eviction recovery and structured replacement logging

Three core-added legacy cases are retired: eviction before the startup wait,
eviction on the first exec dial, and replacement info logging. Two rows extend
the same pause outline; no new story or definition. These cases assert recovery,
creation/exec counts and Failed/Evicted log fields, not their fixture's memory-
pressure message. The separate node-pressure diagnostic cases are unchanged.

The existing bounded 24Mi-write/16Mi-emptyDir eviction implementation is shared.
Its original pod literal and complete observation body are unchanged; only the
recovery caller gates the writer until needed. Real writer output, pod UID,
Failed/Evicted status and a matching kubelet event establish the premise. The
existing compatibility case also passes after extraction. Replacement logs use
the production JSON logger/writer sink, with message, pod, phase and reason
checked. No node pressure, supplied status or synthetic API/exec response.

Five matched original-Go/Brine faults cover startup/dial refusal, absent log,
wrong phase and wrong reason. Normal recovery pair: 108.909s; compatibility:
55.215s. Inactive recovery and all five TERM/OOM controls pass; their longer
wall times include actual kubelet eviction/namespace cleanup. Every drain is
complete and all 15 owned namespaces are independently absent. Node identity,
Ready state and 12 allocatable CPUs are unchanged.

Root Ginkgo: 29 -> 26, all passing; retained It bodies are unchanged. The
eviction fixture remains for the separate replacement-dies-again case. Three
native Brine suites, default/live vet, vocabulary, catalog and diff checks pass;
normal adapter rebuilt. Inventory: 589 cases (485 local + 104 live),
1070 definitions. Three old cases become two rows in the shared outline.

Original tests: /tmp/brine-pause-eviction.2rYV94/before-pause_pod_replacement_test.go
Evidence: /tmp/brine-pause-eviction.2rYV94/evidence.json
SHA256: f9eee06410aeea1b73895745d954cd0b126830836170d52fc149b83059b42bbb
Pairing: /tmp/brine-pause-eviction.2rYV94/pairing.json
SHA256: 2aba8ac78d5155ccab1dc59176d96c828c1f7c1cee363fcb8de9d1d4253153d2

Five pause failure/streaming cases, the put workflow, native daemon doubles,
supplied-state fixtures, final consolidation and full-suite/coverage/CI checks
remain. The other twenty Ginkgo cases exercise shell/quoting behavior. This is
not a fresh full-suite/coverage measurement. Goal active; uncommitted on core.

## 2026-09-15 — second pause death before startup

One additional row in the shared live/container-lifecycle.feature outline replaces
the legacy second-death case and removes its bornDead status reactor and unused
eviction fixture. A real bounded-volume eviction stops the initial pod; actual
TERM stops the replacement. A transparent transport barrier delays the second
CREATE response until kubelet reports its terminal state, without changing API
bytes or supplying status. Assertions preserve two creations, zero runtime execs
and the original startup-error substring. Production process.go is unchanged.

Both original and Brine detect budget bypass and generic-error mutations; the
new case and three existing TERM controls pass with the appropriate faults off.
All 25 remaining root Ginkgo cases, three Brine Go suites, vet/default+live,
normal adapter build, catalog and diff checks pass. All 6 owned namespaces are absent.
Inventory: 590 cases (485 local + 105 live), 1070 definitions.
Four pause cases, the put case, native doubles and injected statuses remain;
this is not full-suite/coverage revalidation or completion of the goal.
Last full measured coverage remains 79.724249797%. Work is uncommitted on core.

Evidence: /tmp/brine-pause-second-F8iKDy/evidence.json
SHA256: b144c745ce5034453830963f9121ae1fbe572683d5e4f69a924874a4be286581
Pairing and recoverable original source are in the same audit directory.

## 2026-09-15 — consolidate remaining pause policy mocks

Retired all four remaining cases and pause_pod_replacement_test.go. One pure
policy table covers preemption metadata, live/spent guards, and failed init
under both Failed and defensive Succeeded inputs. Real byte-stream tests pin
the live flag. Literal inputs never enter an API tracker or supplied response.
Production changes only extract the existing refusal checks, preserving their
order, snapshot, error text and I/O sequence; a reverse-extraction comparison
against the saved source verifies no other production change.

Existing Brine startup/dial recovery cases retain creation/exec counts and
success/error outcomes. Existing init and interrupted-exec cases now count
real requests too, requiring a pod read so zero counts cannot pass unwired.
Preemption metadata is pure policy coverage, not physical scheduler preemption.
The impossible upgrade-after-output error remains a policy input; live cases
cut actual sockets and observe their actual errors instead.

Three grouped faults fail the original and pure tests: preemption refusal,
ignored init failure, and unsafe streaming retry. The latter combines ignoring
the live guard with retrying genuine connection errors; it is not evidence
that a real transport emits an upgrade error after output. Init and streaming
faults also fail the intended live Then assertions; streaming observes 2/2
creations/execs instead of 1/1. All 13 live controls and three final normal-
adapter cases pass. Root Ginkgo: 21 (20 shell/quoting + one mock-backed put).
Pure/native checks, three Brine Go suites, vet and build pass. All 18 owned
namespaces are absent. Inventory unchanged: 590 (485 local + 105 live),
1070 definitions. Native doubles, injected statuses, put migration and full
final validation remain open; no new full coverage claim (last measured 79.724249797%).
Changes remain uncommitted on core.

Evidence: /tmp/brine-pause-policy-EjF5so/evidence.json
SHA256: d4e970c706a3c2d177601ebb1f1082d798d48b440961cf22cdf36adc08f9f5ec
Pairing, mutation logs and recoverable original source are in the same directory.

## 2026-09-15 — real S3 put retires the last mock-backed Ginkgo workflow

JB-integration-011 now runs in live/s3-resource.feature. The official S3 image
and a real MinIO server are pinned by digest. Two real deferred volumes receive
explicit Volume.StreamIn uploads; the resource uploads the binary archive, and
an independent authenticated S3 client downloads matching bytes through a
loopback-only Kubernetes port-forward. The release-notes input is independently
read too. No host storage/port, user cloud credential, supplied pod status or
fabricated executor/HTTP response. This is not automatic staging or a producer
Get; the removed test never ran either despite its title.

The two mount paths and exact count, zero exit and byte-for-byte stdout contract
are preserved. The real pinned response includes metadata and a trailing newline,
rather than the old fake's minimal JSON. The object path remains
releases/v1.0.0/app.tar.gz; params.file now points into the actual mounted input.
The [upstream resource contract](https://github.com/concourse/s3-resource) and
its checked-out out command guided the fixture, with runtime readback as proof.

Five matched original/live faults are caught at Then: missing mount, extra mount,
wrong mount path, extra stdout newline, and exit 7. Normal and inactive S3 runs
pass, as does the different-handle control with all faults enabled. The old
integration_restored_test.go, fakeArtifact, restoredPod and unused fakeExecExecutor
are removed. Its stale AGENTS entry is removed; live consumers retain their DB
and no-op delegate helpers. Production process.go/container.go are unchanged.

Two fixture discoveries are resolved, not hidden: client-go port forwarding can
die after an early refused connection, so kubelet HTTP readiness precedes it;
admission otherwise adds a token mount, so the resource uses an owned token-free
service account. The original exact-two-mount assertion was not loosened. Both
failed prototypes and all later namespaces are independently confirmed absent.

Post-removal: 20 default Ginkgo cases (shell/quoting), pure/native checks,
three Brine Go suites, vet/default+live, normal adapter and root live-tag compile
pass. Inventory: 591 (485 local + 106 live), 1072 definitions. All 10 owned
namespaces are absent. Normal S3 run: 22.588s wall; no full-suite or new coverage
claim. Historical coverage remains 79.724249797%; native doubles, injected
statuses and final full-goal validation remain open. Uncommitted on core.

Evidence: /tmp/brine-put-s3-rUGdAa/evidence.json
SHA256: f04aac66929ac7dcf3fd51c71b8720eba09613dfcffceae34913d3f17a73923e
Pairing, logs, upstream checkout and recoverable original source share that directory.
