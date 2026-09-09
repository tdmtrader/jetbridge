# Execution and artifact consolidation

**Completed in the workspace on 2026-09-08.** See
[the final completion audit](COMPLETION-AUDIT.md) for the requirement-by-requirement
decision, complete case accounting, final fault replays and residual limits.
The numbered passes below are historical records; their former “work remaining”
statements describe those checkpoints, not the final status.

Starting commit: `74aaa83d7e7781d69aeeca8701116ff1a0056285`.

The fixed scope is the seven feature files and supporting/legacy files listed
in `cmd/brine-census/main.go`. The seventh feature, `container-pod`, contributes
the exact mount contract. New shared test helpers count wherever they are put;
moving code outside these paths earns no reduction. The census itself is
measurement tooling, not a replacement for test code.

## Goal and acceptance criteria

Consolidate brine task-execution and artifact-delivery tests into a smaller,
reusable suite exercising production's `execProcess` path, preserve demonstrated
behavioral coverage, and raise brine-driven production statement coverage to
**at least 40%**.

The coverage requirement was added on 2026-09-08. Unless the user specifies a
broader denominator, it means Go statement coverage of the production package
`github.com/concourse/concourse/atc/worker/jetbridge`, measured by running the full
brine CLI suite against a coverage-instrumented adapter. It does not mean 40% of
the repository, feature cases, or branches. Do not combine Go unit-test coverage
with brine coverage or count adapter/fixture code toward the target.

Record a reproducible coverage baseline and final result, the exact measurement
commands and package denominator, and retain the resulting coverage profile.
The final brine suite must pass and its unrounded covered-statement ratio must
be at least 0.40. The first valid measurement after adding this requirement is
**1,754 / 2,241 statements (78.268630%)**, with all 576 cases passing. Re-run the
gate after subsequent changes. Preserve existing assertions and mutation checks;
do not shrink the denominator to reach the threshold.

This is additional to, not a replacement for, the existing acceptance criteria:
one shared ordinary executor, fewer distinct executor implementations and scoped
step definitions, at least 10% scoped code-line reduction, mutation-backed
disposition decisions, passing Go/vocabulary/full-brine gates, resolution of
runtime regressions over 20%, and a before/after report. Compare runtime using
uninstrumented builds so coverage overhead does not distort that gate.

## Baseline

Run from `atc/worker/jetbridge/brine`:

```sh
go run ./cmd/brine-census
rg -n '^func .*ExecInPod' steps
go build -o .build/brine-adapter-jetbridge ./cmd/brine-adapter-jetbridge
go test ./steps -count=1
export PATH="/home/dev/brine-private/target/debug:/usr/lib/postgresql/15/bin:$PATH"
TIMEFORMAT='wall_seconds=%R'
time brine run --mode sync --format brief
```

The absolute PATH entries describe this validation host only. CI supplies its
tools in the runner image. Use the same environment for the final comparison.

| Measurement | Before | Final |
|---|---:|---:|
| Expanded cases in scope | 174 | 184 |
| Scoped Go test leaves (It/Entry/native) | 87 | 83 |
| Distinct registered definitions used in scope | 298 | 288 |
| Registered definitions in whole brine suite | 1,099 | 1,088 |
| `ExecInPod` implementations in whole brine suite | 10 | 2 |
| Combined scoped lines, including comments/blanks | 18,278 | 16,662 |
| Combined scoped non-comment, nonblank lines | 13,425 | 12,079 |
| Nested module Go tests | pass, 33.057s | pass, 7.098s |
| Full brine CLI verdict and wall time | 568 passed; 147.795s | 578 passed; 169.203s |
| Brine-only production statement coverage | not measured at starting commit | 1,762 / 2,241 (78.625614%) |

The reduction criterion uses **non-comment, nonblank lines**, counted with Go's
token scanner for Go and excluding blank/comment lines in features. It requires
at most **12,082** such lines (at least 10% reduction). Raw line totals are
reported for context; deleting historical commentary earns no credit. New
helpers and their tests are included. Do not compress statements onto one line
to improve this measure.

The earlier text-search estimate of approximately 520 step definitions counted
direct `brine.Define*` calls only. The actual registry has 1,099 definitions:
helper-created definitions must also be counted. This census uses the registry
and parses expanded scenarios, rather than inferring counts from source text.

## Coverage and deletion discipline

The existing `DISPOSITION-jetbridge.md` rows remain authoritative. This pass
does not delete a restored test merely because a neighboring scenario passes.
Each affected GAP/REFUTED row will name either new mutation-backed coverage or
the retained Go/cluster test that still protects it. Final evidence and the
before/after comparison belong here.

## Completion

The numerical gates pass: 10.026071% counted-line reduction, fewer scoped
definitions and executor implementations, 78.625614% Brine-only production
coverage, and a full-suite runtime increase below 20%.

The final audit accounts for all 174 baseline Brine cases and all 87 scoped Go
test leaves, checks the 81 GAP/REFUTED retention decisions against current
source, revalidates deletion evidence, and replays distinguishing faults against
the final source. All required work is complete. Focused Go mocks, explicit
legacy compatibility checks and real-cluster limits remain deliberately
documented in [COMPLETION-AUDIT.md](COMPLETION-AUDIT.md); no broader mock-removal
or cluster-fidelity claim is made.

## First pass: shared execution (2026-09-08)

Six implementations (`localShellAdapter`, `closingShellAdapter`,
`localResourceAdapter`, `localExecAdapter`, `containerAwareAdapter`, and
`ttyAwareShellAdapter`) now use `localExecutor`. Ordinary command execution,
exit conversion, process-group cancellation/cleanup, stream handling, and
terminal allocation have one implementation. The remaining four adapters are
explicit lifecycle/failure fixtures: `execStub`, `reapedPodExecutor`,
`severingExecutor`, and `severingExec`. Their suitability and remaining fallback
scenarios still need the production-path review.

The minimal edits in `worker.go` only replace two references to the removed
adapter. No line reduction is claimed for that file. No legacy tests or brine
cases have been deleted and no disposition verdict has changed in this pass.

Validation:

- The three vocabulary guards passed after consolidation.
- The full brine CLI run passed **568/568**, wall **145.944s**, versus baseline
  147.795s on the same host (about 1.3% faster). Logs are in
  `/tmp/brine-consolidation-baseline.log` and
  `/tmp/brine-consolidation-shared-executor.log` on this validation host.
- The newly added CI command, `go test -count=1 -timeout 5m ./...`, passed;
  steps took **7.269s**. It includes executor contracts for separated streams,
  exit code, PTY allocation/input, and cancellation of descendants, as well as
  the existing vocabulary and daemon fixtures. The PTY-input addition after
  the full CLI run was verified by this command; existing scenarios do not
  supply terminal stdin.
- There are now **5** `ExecInPod` implementations. Selected step definitions
  remain **298**, with **174** expanded cases. The current census reports
  **18,155 total lines / 13,407 non-comment, nonblank lines**, including the
  shared executor and its tests. This is only an 18-code-line reduction;
  **the 10% target has not been met**.

Next: establish production-returned artifact handoff and execution routing;
replace repeated container/volume setup with that shared vocabulary; verify
the affected disposition rows with mutations before removing any coverage.

## Second pass: returned artifact handoff (2026-09-08)

The three-row outline in `volume-streaming.feature` uses a real PostgreSQL
worker, production-returned containers and volumes, and the real node artifact
daemon. Both commands run through `execProcess` with nil stdin (supervised
task execution). The producer writes a named file plus a manifest, with an
out-of-output-directory decoy. The consumer compares the entire tar member
map, including contents: extra files and changed paths are failures.

The worker wraps the returned output with `ArtifactFromVolume`; the producer
pod is deleted before reading. The consumer's returned input volume receives
raw, gzip or s2 data via `StreamIn`. The shared executor resolves extraction
paths against the actual pod's declared hostPath volumes and rejects absent
namespaces, pods and containers. The existing pod-admissibility check now also
rejects duplicate mount paths within each container, including init containers.

This is deliberately NOT a claim that local processes execute kubelet mount
namespaces or automatic init delivery. The explicit `StreamIn` call tests the
returned volume API; production automatic input delivery is handled by init
containers, not by `execProcess.streamInputs`. Real-daemon fetch scenarios and
cluster tests remain necessary for that separate contract. Command arguments
point into host directories resolved from the emitted pod. No production
source or production shell template was changed.

### Challenged faults

Each mutation below was applied alone through a Go build overlay. The
production worktree stayed unchanged. Baseline volume feature: **23 passed,
0 failed**; mutation runs executed the entire feature, not a filtered subset.

| Mutation | Brine result | Retained Go evidence / limit |
|---|---|---|
| Append a duplicate of each input mount in `Container.buildVolumeMounts` | Container-pod: 40 passed, 4 failed, explicitly naming duplicate paths | `TestBuildVolumeMounts_EmptyDirMode_AllEmptyDir` fails: expected 3 mounts, got 4 |
| Pass nil instead of `w.executor` in `Worker.newVolumeForMount` | Volume-streaming: 20 passed, 3 failed; returned input reports no executor | The single focused input-volume `It("returns Volumes with an executor wired up for StreamIn/StreamOut")` fails its HasExecutor assertion |
| Pass `"missing-container"` instead of `mainContainerName` at that constructor call | 20 passed, 3 failed; missing container in consumer pod | New handoff evidence only; no old-test equivalence claimed |
| Return the raw volume from `ArtifactFromVolume` even with a backend | 20 passed, 3 failed; the async failed producer stream cannot be extracted as a tar | New handoff evidence only; no old-test equivalence claimed |

The duplicate mutation is exactly the narrower fault that previously defeated
the migration pairing in `JB-behavioral_permutations-017`. Catching it closes
that *fault-detection gap*, not every clause of the row. The named Go test is
retained for exact cardinality and emptyDir assertions; unique mount paths
alone would not reject an extra mount at a different path. No HOLDS verdict or
test deletion is claimed.

`JB-container-034` retains its named input-volume Go test despite the both-red
executor result, pending a full per-test equivalence challenge.
`JB-container-038` retains its cache-volume HasExecutor test: the new handoff
does not exercise cache volumes. Constructor-only and deferred-binding tests
in `volume_restored_test.go` remain; returned-container coverage must not be
mistaken for coverage of every direct `NewVolume` constructor argument.

### Reproduction

On this host, overlays, mutated copies and CLI logs are in
`/tmp/brine-handoff-mutations.4DJPp4/`. The four overlay names are
`duplicate-mount.json`, `disconnected-executor.json`,
`wrong-container.json`, and `pod-coupled-artifact.json`.
Each JSON maps one absolute production filename to its temporary mutated copy:

```json
{"Replace":{"/absolute/repo/atc/worker/jetbridge/worker.go":"/temporary/mutated-worker.go"}}
```

Exact edits to reproduce in those temporary copies:

- Duplicate mount: immediately after
  `inputMountPaths[filepath.Clean(input.DestinationPath)] = true`, add
  `mounts = append(mounts, mounts[len(mounts)-1])`.
- Disconnected executor: in `newVolumeForMount`, replace the third argument
  `w.executor` to `NewDeferredVolume` with `nil`.
- Wrong container: at the same call, replace `mainContainerName` with
  `"missing-container"`.
- Pod-coupled artifact: in `ArtifactFromVolume`, replace the guard
  `w.storageBackend == nil` with `w.storageBackend == nil || vol != nil`.

From the nested module, for each overlay:

```sh
go build -overlay=/absolute/mutation.json -o .build/brine-adapter-jetbridge ./cmd/brine-adapter-jetbridge
brine run features/volume-streaming.feature --mode sync --format brief
# Use features/container-pod.feature for the duplicate-mount mutation.
```

Paired Go commands, from the repository root:

```sh
go test -overlay=/absolute/duplicate-mount.json ./atc/worker/jetbridge -run '^TestBuildVolumeMounts_EmptyDirMode_AllEmptyDir$' -count=1
GOFLAGS=-overlay=/absolute/disconnected-executor.json ginkgo --focus='returns Volumes with an executor wired up' ./atc/worker/jetbridge
```

The first command runs only the named, non-database standard Go test. The
database-backed Ginkgo case uses the repository-appropriate runner. Restore
the unmutated adapter with `go build` (without `-overlay`) before normal runs.
Do not interpret an expected mutation exit code of 1 as infrastructure failure;
inspect the named failing assertions above.

The CLI's tagged run on this installed version reports excluded scenarios as
zero-step failures. It was useful for an initial check but is not a passing
suite verdict; the complete-feature results above supersede it.

Current census after this pass: **177 selected cases, 301 used definitions,
1,101 total definitions, 18,374 total lines / 13,604 code lines**. The new
contract increases coverage and size. The reduction target (12,082 code lines,
fewer than 298 used definitions) is still unmet. No old cases were removed.
Final normal-build validation for this pass:

- `go test -count=1 -timeout 5m ./...` passed, steps **7.633s**, including
  all three vocabulary guards.
- Full brine CLI: **571 passed, 0 failed**, wall **165.631s**, versus baseline
  **147.795s** (**12.1% slower**, within the 20% limit). Log:
  `/tmp/brine-consolidation-handoff-full.log`.
- Only explanatory prose/comments changed after the tested build; no
  assertions, scenarios, command code or production files changed.
- `git diff --check` passed.
- The unmutated Go controls also passed: the named emptyDir mount-count test
  (**0.017s**) and the focused input-volume HasExecutor Ginkgo case (**1 of 89
  specs passed**, suite command **33.280s**). This rules out a pre-existing red
  test as the explanation for the two paired mutation failures.

## Third pass: shared fixtures and fewer sentences (2026-09-08)

Thirty restored task fixtures share their owner/metadata/team/image setup in
`restoredTask`. All call sites retain their original worker, context, handle,
remaining spec fields, delegate, result and error handling. In particular,
concurrent callers and the intentionally failing database transition still
receive production errors; the helper does not assert success for them.
Twenty-three pod lookups now share `restoredPod`, including the API error and
one-pod assertions. No `It`, `By`, or PORT-ADAPT annotation was removed or
renamed. All per-behavior assertions remain, with pod-count checks strengthened
at sites that previously indexed the first pod without checking cardinality.

The vocabulary changes remove four definitions without removing any case:

- Two identical `ClosingRun.Log` substring getters now have one sentence:
  `the finished step's build log contains {string}`. Output and diagnostic
  scenarios retain exactly the same expected substrings.
- Volume identity is one check carrying the original handle, worker, two
  non-nil database-row checks, daemon-row handle and daemon-row worker
  assertions, with the original field-specific failures. No field check was
  replaced by a type check or a generic success condition.
- Mounted storage kind is one keyed comparison rather than two definitions
  with duplicate lookup/error handling. Missing mounts still fail at lookup;
  wrong sources fail the comparison; an invalid ambiguous source also fails.
  The cache outline now varies storage data (`node-local` / `ephemeral`)
  rather than fragments of a step sentence. Both outline rows remain.

The three vocabulary guards pass with **297** used definitions (baseline
**298**), **1,097** whole-registry definitions, and the same **177** selected
cases as the handoff pass. Current code count: **13,442**, down **162** from
that pass, but still **17 above the original baseline**. The 10% reduction
criterion is not satisfied. Formatting and comment changes receive no credit.

### Retention decisions for the fixture refactor

All restored tests stay. The following row IDs refer to their unchanged,
fully named tests in `DISPOSITION-jetbridge.md` and
`container_restored_test.go`. These are explicit reasons to retain them,
not renewed deletion or HOLDS claims:

| Rows (`JB-container-` prefix) | Retained responsibility |
|---|---|
| 000 | Direct-pod image, command, args, environment and security fields; executing a command locally is not equivalent to checking all emitted fields |
| 002, 004, 005, 008, 009, 010 | Exact pod volume/mount cardinality, paths and backing sources; returned runtime mounts and pod mounts are built separately |
| 006, 007 | Overlap's exact count and reuse of the input's named volume, not merely presence at the shared path |
| 015, 016, 018 | One-off/cache-mode selection and exact cache source/path assertions; a generic storage classification does not cover every cache field |
| 022 | Exact request quantities and absent limits, not only a resulting QoS label |
| 024, 025 | Exact security-context fields and nil-versus-explicit settings |
| 026, 028 | Exact credential and service-account material in the emitted pod |
| 030 | Nil-stdin pause-pod construction and its execution seam |
| 034, 038 | Returned input/cache executor wiring; cache-specific coverage is still distinct from the new handoff |
| 035 | Every returned output's path, non-nil volume and nonempty handle |
| 040 | Absence of exec-based automatic input streaming; explicit handoff StreamIn is a different operation |
| 041, 042 | Legacy direct output-volume StreamOut and pause-pod survival; daemon-backed ArtifactFromVolume is intentionally a different read route |
| 044, 045, 046 | Existing-pod routing and both TTY forwarding branches; local PTY behavior alone does not establish exact hijack destination |
| 052 | FailedContainers movement on pod-creation failure, with its original reset/read timing |
| 055 | Attach refusal without a completion status so the engine can retry through Run |
| 056 | Failed database state after Created() fails, not just an error string |
| 061, 062, 063 | Concurrent properties, independent container creation and concurrent pod creation; serial scenarios do not establish these guarantees |
| 065, 066, 067, 068, 071, 072 | Exact sidecar multiplicity, field mappings, shared mounts, no extra helper sidecar, pause-mode composition and image-prefix handling |

The INERT scratch-cache case (011) and the core-added hijack cancellation
case also remain untouched. Keeping a historical compatibility assertion is
not a claim that production uses the no-executor fallback; ordinary brine
execution/fallback routing still needs its separate review.

### Mutation and normal-build controls

Temporary inputs and logs: `/tmp/brine-consolidation-mutations.1KfKWB/`.
Identity mutations use a temporary feature containing only the unchanged
named identity case and a local manifest pointing to the repository adapter.
Its normal CLI control passed **1/1**. The complete normal container-pod
feature passed **44/44**.

| Separate production overlay | Observed failure |
|---|---|
| `Volume.Source`: return empty string in the dbVolume branch | Identity 0 passed / 1 failed: expected worker k8s-worker-1, got empty |
| `Volume.DBVolume`: return nil | Identity 0 passed / 1 failed: the deferred volume lost its database row |
| `Container.stepVolume`: remove the check-container exclusion | Container-pod 43 passed / 1 failed: check workspace expected ephemeral, got node-local |
| `Container.stepVolume`: make the backend branch unreachable | Container-pod 40 passed / 4 failed, including the keyed comparison: task workspace expected node-local, got ephemeral |

The complete root Jetbridge suite passed through `ginkgo
./atc/worker/jetbridge`: **89/89 specs**, **27.269s** spec time,
**60.011s** command time (including standard Go tests). Log:
`/tmp/brine-consolidation-restored-go.log`.
After extracting the fixture, the earlier disconnected-executor overlay
still failed the focused input-volume Go test at its HasExecutor assertion:
`restored-disconnected-executor.log`, **1 failed / 88 skipped**.
Thus sharing defaults did not hide that previously measured fault.

Reproduction uses the same overlay/build/CLI commands as the second pass,
with `wrong-source.json`, `missing-row.json`, `persistent-check.json` and
`ephemeral-task.json` in the third-pass directory. Build without an overlay
after mutation runs; the mutation command's EXIT trap does this automatically.

The nested module was inspected after a safety-review rejection of
`go test ./...`: it has one test-bearing package, `steps`, and two command
packages with no tests. Its five Go test files contain no PostgreSQL runner
or Ginkgo suite. The rejected command therefore was not the prohibited
concurrent database-suite run; after this evidence was supplied, the same
scoped command was approved. Database-backed parent tests continue to use
Ginkgo. No runner safety restriction was bypassed.

Final third-pass verification:

- `go test -count=1 -timeout 5m ./...` from the nested module: passed,
  **7.085s** for steps, including all three vocabulary guards.
- `go vet ./...` from the nested module: passed.
- Full normal brine CLI: **571 passed / 0 failed**, wall **164.524s**,
  **11.3%** above the original baseline, within the 20% limit.
  Log: `/tmp/brine-consolidation-vocabulary-full.log`.
- `git diff --check`: passed.

Remaining reduction to the original target: **1,360 code lines**. Fewer step
definitions is now proven, but this does not satisfy the separate code-size,
production-routing, cancellation/error-contract and complete-disposition
requirements. The goal remains active.

## Fourth pass: cancellation, transfer failures and coverage (2026-09-08)

The cancellation outline now keeps the original get-before-start case and adds
get/task cancellation before startup and while a real child is running. The
task has nil stdin, selecting production's supervisor and `execProcess`. The
running rows wait for a child PID before cancelling, assert a cancellation
error and a stopped child, and inspect the specific pod through the Kubernetes
API. The shared executor now preserves context cancellation instead of turning
a killed command into an ordinary exit status. Its Go test checks the same
error and descendant cleanup. Task and integration setup share the existing
pod-ready helper.

The handoff outline retains all three exact-file raw/gzip/s2 cases and adds
two faults: an offline producing daemon and a refused consumer write. The
refusal assertion names both the input-streaming stage and the original error;
a later consumer failure cannot stand in for transfer-error propagation.
There is no new executor implementation. The local executor's existing fault
option supplies the refused write; the daemon-offline case stops the real
fixture daemon. These rows do not claim automatic init-container delivery.

### Targeted mutation evidence

Production source was not edited. Overlay files and logs are retained in
`/tmp/brine-contract-mutations.akTLpt/`. Each build used `go build
-overlay=<directory>/<name>.json -o .build/brine-adapter-jetbridge
./cmd/brine-adapter-jetbridge`, then `brine run <feature> --no-engine --mode sync
--format brief`. The shell EXIT trap rebuilt the normal adapter.

| Overlay | CLI feature | Observed failures |
|---|---|---|
| `no-task-cleanup`: disable `execProcess.Wait`'s deferred pod deletion | step-closing | 22 pass / 2 fail: task rows before startup and while running retain their pod |
| `resource-pod-cleanup`: remove the supervised-only deletion guard | step-closing | 22 pass / 2 fail: both resource rows lose their hijackable pod |
| `swallowed-input-error`: return nil after the StreamIn executor fails | volume-streaming | 23 pass / 2 fail: handoff write-refusal row and the existing writer-error scenario |

Controls before mutation: step-closing **24/24**, volume-streaming **25/25**.
The full instrumented normal suite subsequently passed **576/576**. All nested
Go tests (including vocabulary and shared executor contracts) passed in
**7.766s**; `go vet ./...` passed as well.

Final root-package control: `ginkgo ./atc/worker/jetbridge` passed **89/89**
specs in **27.314s** (whole command **59.534s**, including standard Go tests).
Log: `/tmp/brine-consolidation-contracts-go.log`. No database-backed suites
were launched concurrently through plain `go test`.

### Explicit retention decisions

No legacy test was deleted or historical disposition relabeled on these
results. In particular:

| Disposition / retained test | Distinction still protected |
|---|---|
| `JB-kept-000`, container restored hijack cancellation | A looked-up container belongs to another step; cancelling hijack must not delete it. The new outline creates its own containers. |
| `JB-kept-001`, integration restored build cancellation | Exact zero-grace DeleteOptions for a running supervised task. Fake pod absence and a locally killed child do not prove kubelet grace behavior. |
| `JB-kept-002`, process restored supervised teardown | The same zero-grace guarantee before startup; the new before-start row observes deletion but not delete options. |
| `JB-volume-000`, restored Volume Handle | The independently supplied absolute database handle, not only consistency between related getters. |
| `JB-volume-002`, `003`, restored DBVolume tests | Object identity and the daemon volume's team/type fields; the consolidated brine identity check retains its prior handle/worker/non-nil assertions only. |
| `JB-volume-004`–`007`, restored StreamIn tests | Exact command/destination, stdin delivery, non-root extraction and ExecAttrs. Returned handoff adds path/destination evidence but does not claim every exact field or replace these tests. |
| `JB-volume-009`–`011`, `013`, `014`, restored StreamOut tests | Direct pod-volume routing, ExecAttrs, streaming reader and error behavior. Reading ArtifactFromVolume through a daemon is deliberately a different route. |
| `JB-volume-015`–`017`, restored StubVolume tests | Stub I/O refusal and HasExecutor=false; a real producer/consumer handoff never constructs that state. |
| `JB-volume-019`, restored volume uniqueness | Independently supplied database handles and persisted artifact lookup; a single producer output does not compare two independent volume identities. |
| `JB-volume-020`, `021`, restored volume-to-volume streaming | Exact direct source/destination exec routing and late SetPodName binding. The handoff reads the source via a daemon, so it cannot replace these direct-source assertions. |

### Reproducible 40% gate

Run `sh scripts/coverage` from this directory, with brine/PostgreSQL tools on
PATH as above. CI now invokes the same script after nested-module Go tests.
The script runs the full CLI suite, writes a fresh raw coverage directory and
text profile, rejects missing or foreign-package data, and compares integer
statement totals against 40% without rounding. It restores an uninstrumented
adapter on exit. This script is measurement tooling, not replacement scenario
support; it earns no code-reduction credit.

Go's main-package instrumentation installs the exit hook, so both the adapter
main and production package are instrumented. `go tool covdata` filters the
report to **only** `github.com/concourse/concourse/atc/worker/jetbridge`.
Go unit tests, fixture code, other packages and catalog-only runs do not supply
coverage to this measurement. `--no-engine` ensures this run's GOCOVERDIR reaches
the adapter instead of relying on a persistent engine's startup environment.

The first valid coverage baseline after the new criterion was added is
**1,754 / 2,241 = 78.268630%**, with **576 passed / 0 failed**. Evidence:
`/tmp/brine-coverage.VVjUaN/coverage.out`, raw counters in `brine-only/`, and
`brine-only.log`. The earlier empty-data attempt is not counted. Coverage was
not measured at the original starting commit, so no before/after increase is
claimed. Instrumented timing is not used for the 20% runtime gate.

The actual CI script was then validated end to end on Linux amd64 with Go
1.25.12 and brine 0.1.0: **576/576**, the same **1,754 / 2,241** coverage ratio,
and a successful threshold gate. Its retained evidence directory is
`/tmp/brine-coverage.prvYgn/`; `coverage.out` has SHA-256
`fe4b7d42cf8cfdbeb1b7831586e62b91ef5cf8448c3646edebc292d72c40b25b`.
The script's exact awk checker was also challenged separately: empty data
and foreign-package data exited 2, 399/1,000 statements exited 1, and exactly
400/1,000 exited 0. Rounded or empty results cannot satisfy this gate.

The subsequent uninstrumented full CLI run passed **576/576** in **174.814s**
wall time, **18.3%** above baseline and within the 20% limit (177.354s).
Log: `/tmp/brine-consolidation-contracts-full.log`; census:
`/tmp/brine-consolidation-contracts-census.json`. This is close to the limit;
later changes still need a full normal-build comparison. Production supervisor
poll/drain sleeps remain intact; no production behavior was changed to speed
up the new handoff rows.

Residual coverage limitations include zero coverage for the real SPDY
executor, dynamic SetTTY paths, and several task/resource-cache initialization
methods. Local PTY execution, fake pod state and the statement percentage are
not substitutes for those Go/real-cluster contracts. Inspect details with
`go tool cover -func=/tmp/brine-coverage.prvYgn/coverage.out`.

The original size requirement is still unmet: **13,476** scoped code lines
versus the **12,082** target, leaving **1,394** lines to remove through genuine
consolidation. Production-routing review also remains: the beginning of
pod-lifecycle still describes direct-Process success/deletion as ordinary
behavior, and container-run's seccomp case uses no-executor setup. Those must
be migrated or explicitly justified as compatibility coverage; passing the
coverage percentage does not resolve that fidelity gap.

## Fifth pass: shared action plumbing (2026-09-08)

`Transform` and `TransformUsing` share capture validation for fallible brine
actions. They still construct ordinary typed brine maps and preserve resource
declarations. Each operation remains explicit in its handler, including its
error returns. This is parameter plumbing, not an implementation of the
behavior under test or a new executor double.

The mechanical conversion covered **79 handlers** in 13 scoped support files,
removing **80 repeated capture guards** and single-use parameter aliases.
It only selected leading GetString/GetInt reads with simple missing-capture
guards and an unused Recorder. Handlers passing Params onward, using a Recorder,
or requiring more complex parsing remain on the native API. Parameter indices,
patterns, operation calls, state types and behavioral assertions were retained.
The temporary AST patch generator is `/tmp/brine-consolidate-actions.go`.

All declared captures are validated before the operation runs, including
integer overflow; an invalid number cannot cause an operation with zero first
and only fail afterward. Both map variants are tested through actual brine
pipeline dispatch, with a real scoped resource value, a chained state change,
and deliberately failing operations. Separate controls check error identity,
undeclared reads, missing parameters, overflow, empty strings and negative
numbers. `Refine` reuses the same Args error formatter without changing its
state-refinement behavior. All helper code and tests count in the census.

The retained permutation tests also use `strings.Contains` instead of two
handwritten substring helpers. Repository-wide reference checks found their
only consumers in that restored test file. No test or assertion was removed.
The assertions still compare host paths against the original fragments and
still reject duplicate output paths in the mixed-overlap case.

### Mutation and control evidence

Artifacts and logs are in `/tmp/brine-action-mutations.KfrdTX/`. Test-helper
mutations used `go test -overlay=<name>.json ./steps -run '^TestAction' -count=1`.
All four were rejected by the specific contract intended to catch them:

| Helper mutation | Failing control |
|---|---|
| Skip pre-operation argument validation | Missing and overflowing inputs reach the operation, which the test rejects |
| Swallow the operation error | Both resource-backed and plain-action pipeline rows lose the original error; the error-identity check also fails |
| Drop the returned state | The chained pipeline result and valid negative-number result are wrong |
| Drop resource declarations | All pipeline rows reject the missing `base` declaration |

After rebuilding the CLI adapter against each production overlay, previous
distinguishing faults are still detected through the converted action handlers:

| Production fault | Observed CLI result |
|---|---|
| Duplicate input mount | container-pod: 40 pass / 4 fail |
| Wrong consumer exec container | volume-streaming: 22 pass / 3 fail, all successful handoff encodings |
| Swallow StreamIn executor error | volume-streaming: 23 pass / 2 fail, including the handoff refusal row |
| Omit cancelled-task pod deletion | step-closing: 22 pass / 2 fail, both task cancellation rows |
| Delete cancelled resource pods too | step-closing: 22 pass / 2 fail, both resource cancellation rows |

Logs use `production-<mutation>.log`; overlays are those recorded in the second
and fourth passes. A final EXIT trap restores the normal adapter. No production
source was edited. A separate `wrong-storage-path.json` overlay changes the
hostPath `steps` segment to `misrouted`; all three affected retained native
tests fail their original path-fragment assertions:
`TestBuildVolumeMounts_MultipleInputsNoOutputs`, `MixedOverlap`, and
`AllOverlapping`. Their normal standalone control passed in 0.050s.

Those three tests (`JB-behavioral_permutations-000`, `002`, `003`) remain
**RETAIN Go**: they still assert exact volume/mount cardinality and concrete
input/output path assignments beyond the generic mount-admissibility check.
All earlier concrete retention decisions remain in force. Changing parameter
plumbing does not establish new per-test deletion equivalence, and no
historical GAP/REFUTED disposition was relabeled.

Final controls:

- Nested `go test -count=1 -timeout 5m ./...`: pass, steps **8.092s**;
  includes action, refinement, executor and all three vocabulary guards.
- Nested `go vet ./...`: pass.
- Root `ginkgo ./atc/worker/jetbridge`: **89/89**, spec time **27.214s**,
  whole command **59.600s**; `/tmp/brine-consolidation-actions-go.log`.
- `sh scripts/coverage`: **576/576**, **1,754 / 2,241 = 78.268630%**.
  Evidence: `/tmp/brine-coverage.Xlbbzf/`; profile SHA-256
  `7fbd366010a6893f733d85a96b75b45e8aa3b8099540bb0fb5d64df246ec31dd`.
- Full uninstrumented CLI: **576/576**, wall **176.293s**, **19.3%** above
  baseline, within 177.354s. Logs/census: `/tmp/brine-consolidation-actions-full.log`
  and `/tmp/brine-consolidation-actions-census.json`. The remaining runtime
  margin is small, so later changes still need a fresh full comparison.
- `git diff --check`: pass.

The repository's fixed-5434 explanation was found stale: current
`postgresrunner.PickPort` seeds its scan with the PID and probes socket/TCP
availability. The running checks were observed on distinct ports (6170 and
5634). As AGENTS.md requests, its obsolete explanation was removed while its
Ginkgo runner requirement was retained. No runtime code or runner policy was
changed, and documentation changes earn no size-reduction credit.

The test's embedded Gherkin is formatted as readable multiline source and its
full line cost is included; the final action-only control passed in 0.022s.
This pass removes **201** scoped code lines including all new helper/test
costs. Current code size is **13,275**, versus **13,425** originally: only
**150 lines** net reduction from the starting commit. The 10% criterion still
requires **1,193 more lines** of genuine consolidation. The fallback-routing
review and remaining disposition inventory also remain open. The goal is
not complete.

## Sixth pass: independent task execution (2026-09-08)

A two-invocation probe demonstrated that `runTask` could replay a prior suite's
supervisor output without running the new command. Its `fresh` argument was
unused. The production supervisor derives state from process ID and command;
local fake pods share the host's `/tmp`, so fixed handles and static commands
collided across independent scenarios/runs. Workspace-expanded commands already
had unique command hashes, which is why the restart cases did not reveal this.

Evidence is retained in `/tmp/brine-task-isolation.kL14bK/`. Both probe features
use the existing task-command vocabulary, the same handle `isolation-kL14bK`,
and the same command `printenv BRINE_EXECUTION_NONCE`. Run each separately with
`brine run <feature> --no-engine --mode sync --format brief`, setting the nonce
to `first-kL14bK` and `second-kL14bK`, respectively. Each expects its own nonce
in the build log and exit zero. Before the fix, the first passed and the second
failed with `expected ... second-kL14bK, got first-kL14bK`. After the fix both
passed. The temporary manifest points to this workspace's rebuilt adapter.

`runTask` now prefixes the process ID with the scenario workspace basename.
This isolates independent scenarios while retaining the same ID for re-exec
within a scenario. The unused argument is removed. No command rewriting or
production supervisor changes were needed. The task-exit assertion also rejects
an unexpected Wait error instead of accepting its zero-valued status.

Validation:

- Nested `go test -count=1 -timeout 5m ./...`: pass, steps **7.153s**;
  includes all three vocabulary guards. `go vet ./...` and `git diff --check`
  also pass.
- Both independent nonce invocations: **1/1** each, approximately 3s each.
- `task-command.feature`: **8/8**, including resumed-command deduplication,
  different-command execution, quoting, and nonzero exit propagation.
- Full uninstrumented CLI: **576/576**, **182.797s**, versus 147.795s baseline
  (**+23.68%**). The runtime gate is now **unmet** by 5.443s. Compared with the
  prior full log, static task executions increased from about 2s to about 3s;
  the independent nonce probe establishes that the old shorter path replayed
  prior state. Do not restore replay or change the baseline to obtain a pass.
  Real consolidation/runtime work remains necessary.
- Census: **18,054 raw / 13,276 code lines**, **182 scoped cases**, **296 scoped
  definitions**, **1,096 whole-suite definitions**. This fixture fix adds one
  code line net; the 10% target still requires **1,194 lines** of reduction.
  No cases, assertions, or restored tests were deleted and no disposition
  verdicts changed.

The fresh `sh scripts/coverage` gate passed **576/576**, covering **1,754 / 2,241
production statements (78.268630%)**, above the required 40%. Its retained
profile is `/tmp/brine-coverage.88OKRm/coverage.out`; its CLI log is adjacent.
The script restored the normal uninstrumented adapter on exit. Go unit-test
coverage was not combined with this measurement.

Further execution-isolation review remains necessary: `closingRunStep` also uses fixed
IDs and static commands, unlike the integration command step that already
includes a scenario workspace in its command. This pass does not claim to fix
that separate fixture. Unique task IDs prevent reuse but do not reclaim the
supervisor's host `/tmp` directories; cleanup must be scoped safely, never a
blanket deletion. All prior fallback-routing and disposition work remains open.

## Seventh pass: shared assertion controls and pod setup (2026-09-08)

This pass changes only two Go test files and this report. The production
package, brine step implementations, feature cases and CLI adapter are unchanged.

### Assertion controls

The old tests mostly called private comparison functions directly, using public
definitions only to compile captures. A mutation dropping the registered check
handler therefore passed the old controls. The replacement table runs the public
definitions through a real brine registry and pipeline, then validates the
scenario verdict, exit code and emitted error. Undefined, unsatisfied, skipped
and missing-verdict results cannot count as successful controls.

The eleven original comparison/condition/overflow tests are represented by named
subtests in `TestCheckContracts`; the zero-expectation case has its own row.
`TestDetailReachesEveryCombinator` is consolidated into the corresponding keyed
and collection rows, which require nonempty state-derived diagnostic context.
The existing Go error-identity checks, missing-parameter, long-value/display,
failure-detail and refinement controls remain. No production-behavior feature
or restored Go spec was deleted.

Before replacement, both the old file and the candidate passed normal controls.
Temporary overlays in `/tmp/brine-check-consolidation.6OtkBg/` then ran each set
against the same faults. The old controls caught **12/13** and the candidate
caught **13/13**, with no build failures counted as mutation detection.

| Distinguishing mutation | Original control / replacement evidence |
|---|---|
| Disarm string equality | `CheckStringPassesAndFails` / same-named matrix row; error-identity and missing-parameter controls also fail |
| Disarm substring comparison | `CheckContainsPassesAndFails` / same-named row; long-value control also fails |
| Disarm numeric equality | `CheckIntPassesAndFails`, unusable-number and detail tests / matching rows, including explicit expected-zero row |
| Disarm keyed string equality | `CheckStringForRoutesTheKeyAndComparesTheLast` / same-named row |
| Disarm keyed substring comparison | `CheckContainsForPassesAndFails` / same-named row |
| Disarm keyed numeric equality | `CheckIntForRoutesTheKeyAndComparesTheLast` / same-named row |
| Disarm collection count | `CheckCountPassesAndFails` / same-named row, retaining mismatch and empty collection |
| Disarm membership | `CheckMemberPassesAndFails` and `CheckNotMemberIsTheInverse` / both named rows, retaining exact-element and polarity checks |
| Disarm condition check | `CheckThatPassesAndFails` / same-named row plus retained Go error identity |
| Route expected value as lookup key | Key-routing test / same-named keyed-string row |
| Drop failure detail | Original detail tests / keyed, collection and scalar rows plus retained detail test |
| Abbreviate before comparing | Retained long-value control fails in both suites; its needle is in the elided middle |
| Drop registered definition handler | **Old controls pass incorrectly; replacement rejects it** across every matrix row |

Commands use `go test -overlay=<mutation>-<original|candidate>.json ./steps
-run 'TestCheck|TestParamAt|TestFailureDetail|TestLongValues|TestDetail' -count=1`.
The extra condition-check mutation uses `-run TestCheck`. Logs are adjacent
to each overlay. The original and candidate test files are retained there.

### Restored pod setup

`restoredRunPod` replaces **20** identical successful Run-and-read-pod blocks.
Every command remains explicit at its call site. Run errors, pod-list errors and
exactly-one-pod checks are still asserted; the helper neither waits nor simulates
command execution. All original `It` and `By` descriptions and all emitted-pod
assertions remain. Failure, concurrency and process-lifecycle cases do not use it.

Affected `JB-container-` rows are **002, 004–011, 015, 016, 018, 024–026,
065–068 and 072**. Their concrete RETAIN Go reasons in the third-pass inventory
remain in force: exact mount counts, paths/backing sources, overlap reuse,
cache selection, nil-versus-explicit security fields, credential material and
sidecar field mappings are not replaced by generic execution success. Row 011
also remains a legacy scratch/cache fixture; factoring setup does not establish
new evidence for its historical INERT disposition. No disposition is promoted
to HOLDS or deletion eligibility.

Controls:

- Nested module tests and all three vocabulary guards: pass, **7.103s**;
  `go vet ./...` and `git diff --check`: pass.
- Root `ginkgo ./atc/worker/jetbridge`: **89/89** before and after; after spec
  time **27.252s**, command **60.049s**. Logs: `root-go.log` and
  `root-go-after.log` in the evidence directory.
- Wrong production seccomp profile: both selected security-context specs fail
  their original assertions (**0/2 pass**).
- Duplicate production input mount: **3/4 selected Ginkgo specs fail**, covering
  input cardinality, overlap and non-overlap; eight native mount tests fail too.
  Logs: `root-wrong-seccomp.log`, `root-duplicate-mount.log`. These are temporary
  production overlays, not repository edits.

The assertion consolidation saves **71 code lines** and the pod setup saves
**91**, including the new driver/helper: **162** total this pass. Current scope
is **13,114 code / 17,838 raw lines**, **182 cases**, **296 used definitions**,
**1,096 whole-registry definitions** and **5 executor implementations**.
The net reduction from 13,425 is **311 lines (2.32%)**; **1,032 more lines**
must be consolidated to reach 12,082.

No new CLI/coverage result is claimed for this test-only pass. The latest
unchanged-runtime evidence remains **576/576**, **78.268630%** brine-only
production coverage and **182.797s** wall time from the sixth pass.
The runtime gate, remaining fallback/isolation review and disposition inventory
remain open. The goal is not complete.

## Eighth pass: one task fixture and shared recovery contracts (2026-09-08)

`TaskCluster`, `TaskOutcome` and `runTask` now serve both task-command and
closing-step contracts. The duplicate closing cluster, result type, constructor
and execution loop are gone. The shared result includes the returned container,
properties and pod snapshot needed by restart checks. Ordinary execution still
uses `localExecutor` and production `execProcess`, with nil stdin selecting the
real supervisor. Status fault injection changes only the fake kubelet status.

The closing cases now use the common busybox, test-namespace and /workdir
fixture rather than their separate ubuntu/ci-namespace defaults. Those incidental
fixture values were not their brine assertions. The retained Go integration
test still checks its exact image, pause argv, namespace and default process-ID
rule; this is not a claim that a local shell tests kubelet image execution.

Three redundant definitions disappear: the closing-specific Given, Run sentence
and exit-status check. Diagnostic logs remain a distinct check requiring an
unsuccessful Wait; the ordinary task-output/exit checks still reject errors.

### Isolation evidence

The closing fixture had the same host-supervisor replay bug as the former task
fixture. Two independent runs used the same handle and `printenv
BRINE_EXECUTION_NONCE`, expecting first-qyUrE6 and second-qyUrE6 respectively.
Before the change the first passed and the second failed after replaying the
first nonce. Both pass on the shared, scenario-scoped task fixture. Re-execution
within a scenario retains its process ID and supervisor state.

Evidence is in `/tmp/brine-task-unification.qyUrE6/`: `before-first.log`,
`before-second.log`, `after-first.log`, `after-second.log`, and their feature
files. Original sources are saved as `before-steps-*.go` and
`before-features-*.feature`. The unified fixture passed all **24 closing** and
**8 task-command** cases before any case deletion. The CLI takes one PATH;
the rejected two-path invocation was a usage error, not a test result.

### Case consolidation and per-case fault detection

Seven overlapping cases become three contracts. The paired mutation runs use
the shared fixture on both sides, isolating case consolidation from fixture
normalization. Before and candidate controls passed **7/7** and **3/3**;
their focused case times were **25.173s** and **13.098s** respectively.

| Original responsibility | Replacement in task-command.feature | Distinguishing production fault(s), rejected before and after |
|---|---|---|
| Task output; closing zero-exit property and worker ownership | Initial successful-task case checks the log, exit, property and worker label | Discard stdout; write property 1 instead of 0; wrong worker label |
| Task nonzero result; closing persisted status recovered by a new web | Failing-task case checks exit 3 both before and after rebuilding the container and attaching | Wrong returned exit; annotate exitCode+1 |
| Finished-command resume; closing refusal/reuse after loss of completion annotation | Restart case re-execs with the annotation present, then recovers without it; checks one command execution on both routes and exactly one pod | Unstable supervisor state key; missing Attach refusal; duplicate pod |
| Quoting outline's plain-spaces row | Initial task runs the identical echo hello world command and checks identical output/exit | Remove shell quoting; discard stdout |

All **nine** mutations compile and cause the intended assertions to fail in
both paired suites. Each mutation directory contains its production source
overlay, isolated adapter, before/after feature and logs. Run commands use
`go build -overlay=<variant>/overlay.json -o <variant>/adapter
./cmd/brine-adapter-jetbridge`, then `brine run <variant>/<before|after>.feature
--no-engine --mode sync --format brief`. No production source was edited.

The three closing cases and the spaces outline row were removed only after
these comparisons. Operator, quoted-string and path syntax remain outline
rows, and the different-command-on-the-same-container case remains. The restart
contract retains both annotation-present and missing-annotation execution;
combining them into only one route would not preserve the original checks.

The historical cancellation commentary was corrected: supervised task aborts
delete their pods; resource and looked-up hijack sessions have different
ownership rules. `JB-integration-000` and `JB-container-055` remain Go tests,
with explicit updated retention reasons in DISPOSITION-jetbridge.md. In
particular, the latter's never-run pod with unset phase is not the Running
pod exercised by the merged restart scenario. No restored Go spec was deleted.

### Final controls and remaining work

- Nested `go test -count=1 -timeout 5m ./...`: pass, **7.528s**, including all
  three vocabulary guards; `go vet ./...` and `git diff --check`: pass.
- Root Ginkgo: **89/89**, **27.278s** spec / **59.795s** command; `root-go.log`.
- Full uninstrumented CLI, with no concurrent test jobs: **572/572**,
  **174.721s**, **+18.22%** from 147.795s. This is below the 177.354s runtime
  limit; the previous 182.797s regression is resolved for the current tree.
  Evidence: `full.log`.
- Fresh brine-only coverage: **572/572**, **1,754/2,241 = 78.268630%**.
  Profile: `/tmp/brine-coverage.ulzpbz/coverage.out`, SHA-256
  `5dd138b44c0be4b341be6f719f78d5669a2dedbdd0cfd5c9390885f2280175f4`.
  The coverage script restored the normal adapter; Go-test coverage is excluded.
- Cancellation mutations were replayed after sharing pod-status setup:
  no-task-cleanup fails both task rows, resource-pod-cleanup fails both get
  rows. Each full closing-feature replay is **19 pass / 2 fail**. Their
  isolated adapters did not replace the coverage or normal adapter.

This pass removes **103 counted code lines**. Current scope is **13,011 code /
17,687 raw lines**, **178 cases**, **293 used definitions**, **1,093 registered
definitions overall**, and **5 executor implementations**. The net reduction
from baseline is **414 lines (3.08%)**; **929 more lines** must be consolidated
to reach 12,082. Census: `census-final.json` in the evidence directory.

Remaining work includes no-executor fallback scenarios and remaining no-op
executor uses, the per-row disposition inventory, and the size target.
Scenario isolation now covers closing tasks too, but supervisor directories
in the host's /tmp are not yet reclaimed by the workspace disposer. Cleanup
must be scoped safely; a broad sweep is not an acceptable substitute.
The goal remains incomplete.

## Ninth pass: remove success stubs and redundant fault adapters (2026-09-08)

All eight callers of `execStub` now use `localExecutor`, and the no-op
implementation is deleted. Construction-only and pre-start-failure tests still
assert the same pod/volume or timeout contracts; they do not pretend that a
command ran. The tracing fixture now runs `/bin/cat` against its existing stdin
instead of handing `{}` to `/bin/sh` and having the stub report success.
All existing span and exit-status assertions remain.

The two error-only adapters, `reapedPodExecutor` and `severingExec`, were also
deleted. Their call sites select the existing shared executor's `failure`
option with the same errors. The named faults remain visible: an artifact read
cannot reach a reaped producer, and a broken exec transport must not publish a
half-written artifact. Identical five-case artifact controls pass against the
saved pre-change adapter and the candidate (**5/5** each).

There are now **two** `ExecInPod` implementations in the entire brine module
(down from five before this pass and ten at baseline):

- `localExecutor`: ordinary commands, streams, exit codes, PTY, cancellation,
  and explicitly configured unavailable-transport errors.
- `severingExecutor`: changes the fake Kubernetes pod state during exec
  (OOM or deletion), then reports a broken connection so production must fetch
  and diagnose that changed state. It executes no ordinary command; there is
  no separate command-running logic to delegate.

This is not a claim that all mocks or fallback paths are gone. The Kubernetes
client is still fake, and the remaining fallback-only lifecycle cases still
need review. No restored Go spec was deleted. The additional scenario/definition
consolidation below was applied only after its paired mutation checks. No production code or shell template was changed.

### Paired evidence

Evidence directory: `/tmp/brine-executor-review.gB40K4/`. The saved
`before-adapter`, six `before-*.go` fixture snapshots, identical control
features, production overlay sources/JSON, build logs, and run logs are retained.
Normal six-case controls pass **6/6** before and after (3.318s / 6.330s);
the additional time is the startup-metric case actually running its supervised
command rather than accepting the stub's success.

| Production mutation | Old control | Shared-executor control |
|---|---|---|
| Rename `pod.initialized` event | 1 failure | same 1 failure |
| Rename `image.pulled` event | 1 failure | same 1 failure |
| Record zero startup duration | 2 failures | same 2 failures |
| Omit generic startup-timeout diagnostics | 1 failure | same 1 failure |
| Forward `/bin/false` instead of the requested raw command | all 6 pass | 3 exit-status checks fail |
| Omit `pod scheduling timeout` but keep `Unschedulable` | all 6 pass | all 6 pass; retained Go test fails |

The zero-duration mutation initially failed to compile because `startTime`
became unused. That build failure is not detection evidence. The corrected,
compilable mutation multiplies the elapsed milliseconds by zero; both controls
then fail at the intended duration assertions.

The last row demonstrates why `JB-process-023` remains **RETAIN Go**. The
focused restored scheduling test passes normally (**1/1**, 4.108s spec time)
and fails under that mutation at its exact error-wording assertion
(`process_restored_test.go:122`, 4.183s). Both brine versions only assert the
scheduler's cause. The disposition row now records this concrete difference;
the no-op removal does not close the historical REFUTED pairing.

Before the timing merge, nested `go test ./...` passes (7.889s),
`go vet ./...` passes, and all three vocabulary guards pass explicitly.
The affected feature controls pass:
observability 9/9, pod-lifecycle 23/23, container-lifecycle 7/7,
container-run 19/19, volume-streaming 25/25, and failure-priority 18/18.

### Share the supervised startup and command contracts

Simply replacing the startup-metric stub with real execution added about three
seconds: the full suite passed **572/572** in **178.268s**, just beyond the
177.354s limit. Brine-only coverage still passed at **1,754/2,241 (78.268630%)**,
profile `/tmp/brine-coverage.5daIBc/coverage.out`. Neither a successful stub nor
cached supervisor output was reintroduced to recover the benchmark.

Instead, the first task-command case now also asserts the existing **>= 1ms**
startup metric. The shared task runner resets the destructive gauge before
Wait, lets the fake kubelet announce Running after 25ms, and captures the gauge
with the command result. Both old and merged timing contracts use a supervised
task with **nil stdin**; this is not replaced by the separate raw-command
observability test. Task output, exit status, container property and worker
label assertions all remain. A staging error cancels the waiting context and
is joined/reported; the fixture-failure control returns the original staging
error in **32ms** instead of hanging.

Before removing the standalone pod-lifecycle timing scenario, identical
controls passed: the original **two cases in 6.076s**, and the combined
**one case in 3.048s**. Five production mutations fail both the original and
replacement contracts at their intended assertions:

- zero startup duration;
- record duration only when stdin is non-nil (distinguishes raw from supervised);
- wrong persisted success property;
- discard command output;
- wrong worker label.

Each old run has **one expected failure / one pass**; each merged run has
**one expected failure**. The `timing-*/{before,after}.log`, overlays,
candidate sources and pre-merge adapter are in the evidence directory. A
candidate-only source-splicing syntax error was fixed before controls; no
compile failure is counted as detection.

The separate worker Given and timing When are removed. The threshold check
moves to the shared TaskOutcome without changing its comparison or error
guard; moving it earns no line credit. A temporary UUID addition to the
generic lifecycle fixture is no longer needed and is not retained: the
merged timing command uses the task runner's existing workspace-scoped ID.

After the merge, nested `go test ./...` passes (**8.245s**), `go vet ./...`
passes, and the census has no undefined steps. Current counted scope is
**12,972 code / 17,620 raw lines**, **177 scoped cases**, **291 scoped used
definitions**, **1,091 whole-registry definitions**, and **2 executor
implementations**. This pass saves **39 counted code lines**; total reduction
is **453 (3.37%)**, with **890 more** needed to reach 12,082. The error-adapter
removals in `worker.go` and `pod_failure.go` are outside the frozen line census
and earn no line-reduction credit.

### Final validation after the timing merge

- Full normal CLI: **571/571**, **174.275s wall** (`full-final.log`),
  **+17.92%** from baseline and below the **177.354s** runtime limit.
  This benchmark ran without concurrent test or compilation jobs.
- Fresh `sh scripts/coverage`: **571/571**, **1,754 / 2,241 production
  statements (78.268630%)**, above 40%. Profile:
  `/tmp/brine-coverage.FJwQIH/coverage.out`; SHA-256:
  `be5fa687612f9414ccdb0fb675c8744f9dcf191ffc1d3ece9d99f2af68600e64`.
  The coverage script restored the normal adapter after measurement.
- All three vocabulary guards pass (0.478s; `vocabulary-final.log`):
  every definition is used, every scenario step resolves, and no step matches
  two definitions.
- `ginkgo ./atc/worker/jetbridge`: **89/89**, **27.247s** spec time,
  **59.666s** command time (`root-go-final.log`). No restored spec was removed.
- `git diff --check`: pass.

Residual work is not hidden by those green gates. Nineteen of the 22 parsed
pod-lifecycle cases still enter through the generic no-executor worker:
17 scenario declarations plus the additional row in each of its two outlines.
The registry task driver and described-container driver retain that executor
choice. Ordinary execution must move onto the production exec path, while
genuinely distinct fallback checks need explicit retention decisions and
mutation-backed replacements before removal. The **890-line** size gap,
remaining per-row disposition inventory, and safely scoped supervisor-state
cleanup also remain. The goal is not complete.

## Tenth pass: shared restored fixtures and stream contracts (2026-09-08)

This pass changes only restored Go tests and disposition/report documentation.
It does not change production code, brine features, steps, adapters or shell
templates. The shared ordinary brine executor remains unchanged.

### Runtime fixtures: five cases retained

`behavioral_runtime_spec_restored_test.go` now creates its worker, fake client
and delegate once per case through a shared fixture, shares exact pod lookup
and completion handling, and uses two tables for TTY and log-request variants.
All five original leaf case names remain. The image is still exactly
`busybox`; TTY cases still have nil stdin and use the executor, while the
sidecar and command-embedding cases deliberately retain the direct fallback.
Existing assertion values, explicit pod handles, command/args and metric reset
are preserved. The file drops from 297 to 148 counted code lines.

Evidence: `/tmp/brine-restored-runtime.5svis3/` contains the old/candidate test
sources, production overlays and before/after logs. Both controls passed all
five selected cases. Each of seven mutations fails the same original leaf
case(s) before and after:

| Production mutation | Cases failing in both versions |
|---|---|
| Force TTY false | TTY requested |
| Force TTY true | TTY nil |
| Remove all direct log streaming | Both sidecar writer paths |
| Return exec exit status one | Both TTY variants |
| Return direct exit status one | Both sidecar writer paths |
| Replace direct command | Command embedding |
| Remove created-container increment | Command embedding |

A deliberately narrower eighth mutation removes only sidecar log requests.
**Both versions remain green.** The original SC-07 assertions observe any log
request, not its sidecar name, destination writer or prefix. Consolidation
preserves that limited contract; it does not repair or close this gap. Rows
JB-behavioral_runtime_spec-007/008/009/010/031 now name the retained Go test and
its precise reason. No runtime case was deleted.

### Volume streams: six repeated operations become two contracts

Three root-path StreamIn cases called the same method with equivalent opaque
readers, then inspected separate fields of the same exec call. Their merged
case performs one call and preserves exact namespace, pod, container, tar
argv, purpose, mount-path metadata, non-nil stdin and byte identity. Three
root-path StreamOut cases similarly become one call, retaining exact routing,
argv, metadata, successful ReadAll and exact output bytes. Both retain the
single-call assertion and the output reader is closed.

The resulting Go cases are:

- `Volume StreamIn streams exact input bytes to the intended tar destination with stream-in metadata`
- `Volume StreamOut streams exact output bytes from the intended tar destination with stream-out metadata`

The subdirectory upload, file-path download and pipe-error tests remain
separate and unchanged. This is Go retention, not a claim that artifact
round-trips prove arbitrary constructor routing, exact byte streams or
ExecAttrs. In particular, exec-backed StreamOut is a compatibility surface
distinct from production's daemon-backed artifact reads.

Before changing the repository file, original and candidate overlays passed
**9/9** and **5/5** selected stream cases. An initial anchored focus selected
zero cases and an initial candidate did not compile; neither counts as
evidence. The corrected comparison used nonempty selection:

```sh
GOFLAGS=-overlay=/tmp/brine-volume-consolidation.iVOyIr/before.json \
  ginkgo --no-color --fail-on-empty --focus='Volume Stream(In|Out) ' \
  ./atc/worker/jetbridge -- '-test.run=^TestJetbridge$'
```

Use `after.json` for the candidate; each named mutation has a
`<mutation>-before.json` and `<mutation>-after.json`. Passing the native
`-test.run` filter here omits unrelated native Go tests but still executes the
Ginkgo suite. The full package gate below does not use this filter.

Eight paired production mutations then failed the corresponding original
assertion and merged contract: wrong namespace, pod, container, tar
destination, purpose, mount attribute, empty stdin, and discarded stdout.
The wrong-destination mutation also continued to fail the unchanged
subdirectory and file-path cases. Logs and overlay sources are retained in
`/tmp/brine-volume-consolidation.iVOyIr/`; production files were never edited.
Rows JB-volume-004/005/007 and 009/010/011 each name their merged Go retention
and distinguishing mutation. Rows 006/013 explicitly retain their separate
path contracts. The file drops from 309 to 282 counted code lines; four
redundant cases disappear, not four behaviors.

### Validation and current totals

- Full `ginkgo --no-color ./atc/worker/jetbridge`, including native Go tests:
  **85/85**, **26.951s** spec time, **59.781s** command time
  (`root-go-final.log`).
- Nested `go test ./... -count=1`: pass, steps **7.712s**
  (`nested-go-final.log`); `go vet ./...`: pass (`vet-final.log`).
- All three explicit vocabulary guards pass (`vocabulary-final.log`).
- Fixed-scope census: **17,403 raw / 12,796 counted code lines**,
  **177** expanded feature cases, **291** used definitions and **1,091**
  whole-suite definitions (`census-final.json`).
- `git diff --check`: pass.

This pass removes **176** counted lines (149 runtime setup, 27 repeated stream
setup). The total reduction from 13,425 is **629 (4.69%)**. Another **714**
counted lines must be removed to reach the required 12,082; the goal remains
incomplete. No relocation or comment removal earns credit.

No fresh CLI runtime or coverage result is claimed for these Go-test-only
changes. The unchanged brine suite's latest measured result remains
**571/571**, **174.275s**, and **1,754/2,241 production statements (78.268630%)**,
above the required 40%. Remaining ordinary fallback migration, complete
per-row inventory, supervisor-state cleanup, and final all-gate verification
are still required.

## Eleventh pass: distinguish production and compatibility diagnostics (2026-09-08)

Nine diagnostic/metric declarations now use an execution-mode outline: ten
expanded `production` rows install the existing shared `localExecutor`, and
ten `direct compatibility` rows retain the former no-executor behavior.
The sidecar-name outline has two fixture values in each mode. No old scenario
or assertion was removed. This deliberately covers both callers of the shared
diagnostic functions, not two names for the same fixture.

The production rows exercise `execProcess.Wait -> waitForRunning` when the
pod cannot start: image-pull failures, sidecar startup failure, eviction, OOM
history, node pressure/spot labels, cordoning, a missing node, and the
image-pull counter. They fail before any command can execute. This is genuine
production startup/failure coverage, not a claim of successful command
execution or a real Kubernetes transport.

The compatibility rows remain because `Process.pollUntilDone` has separate
diagnostic calls. A failure in that caller is not detected by checking only
`execProcess`, and vice versa. Rows JB-process-011 through 019, 038 and 042
now name their exact outline, retained legacy reason and mutation evidence.
The pre-existing Go disposition status is not reclassified or used to justify
another deletion.

### One process result, with an observable path check

The image-pull metric step now returns `ProcessOutcome`, reusing `report`
and the ordinary step-failure assertion. The separate `MetricsObserved`
type and `the metered step fails saying` definition are removed. The counter
is still reset before Wait and read once afterward; its exact float64
comparison is unchanged.

Every mode row also checks the returned failure's execution-path marker:
production wraps startup failures with `waiting for pod running:`, while
the direct route does not. The check requires a failure and runs after the
original assertions. It observes the returned error, not an executor's
recorded calls. Omitting the executor now fails the regular suite instead of
silently turning the production rows back into compatibility tests.

### Controls and distinguishing mutations

Evidence is retained in `/tmp/brine-lifecycle-exec.Mz1Azh/`.
Original selected controls passed **10/10**; the expanded controls passed
**20/20**. The full final lifecycle feature passed **32/32**.

| Mutation | Original 10 cases failing | Final 20 cases failing |
|---|---:|---:|
| Remove shared pod diagnostics | 5 | 10, both modes |
| Remove shared node diagnostics | 3 | 6, both modes |
| Remove image-pull increments | 1 | 2, both modes |
| Hide the fail-fast reason | 5 | 10, both modes |
| Mute only exec startup diagnostics | 0 | 8, production only |
| Mute only direct polling diagnostics | 8 | 8, compatibility only |
| Omit executor in the new production fixture | not applicable | 10, production only |

The six production mutations were compared before/after the mode expansion,
then rerun after the metric-result consolidation. The metric case still fails
its exact counter assertion for `no-image-metric`, and its existing
ImagePullBackOff assertion for `no-failure-reason`. The seventh, fixture
mutation is `force-fallback`; all ten failures occur at the new path check
and all ten compatibility rows pass.

Each mutation directory contains its source, `overlay.json`, scoped
`.brine` manifest, features and logs. `before-overlay.json` additionally
maps the step file to the retained `pre-metric-steps.go`, for reproducing
the original comparison. `before.log` and `after.log` are the original
and mode-expanded results; `final.log` is the final shared-result version.
Build from the nested brine module, then run inside the mutation directory:

```sh
go build -overlay=<mutation-dir>/overlay.json -o <mutation-dir>/adapter ./cmd/brine-adapter-jetbridge
brine run final.feature --mode sync --format brief --no-engine
```

An initial final-rerun invocation referenced a feature outside its manifest
and was rejected before execution. Only the corrected, manifest-scoped
assertion failures are counted. Production files were not edited.

### Fresh gates and remaining work

- Nested `go test ./... -count=1`: pass, **8.009s** for steps;
  `go vet ./...`: pass.
- All three explicit vocabulary guards: pass, **0.491s**.
- Full normal CLI: **581/581**, **175.277s wall**, **+18.59%** from
  147.795s, below the **177.354s** limit. No competing test or compilation
  jobs ran during the benchmark (`full-final.log`).
- Fresh `sh scripts/coverage`: **581/581**, **1,762/2,241 statements
  (78.625614%)**, up eight covered production statements and above 40%.
  Profile: `/tmp/brine-coverage.HAldPh/coverage.out`, SHA-256
  `b9b7b9d4046ccf046431c51329e2f7c76ae63feb08e6688ace2bd818d576a889`.
  Go-test and fixture coverage are excluded; the normal adapter was restored.
- Fixed-scope census: **187** expanded cases, **292** used definitions,
  **1,092** whole-suite definitions, **17,472 raw / 12,851 counted lines**.
  Executor implementations remain **2**.
- `git diff --check`: pass. Root production and restored Go test sources
  are unchanged from the tenth pass's **85/85** package gate; no fresh root
  Go result is claimed. No production shell template changed.

This pass adds **55** counted lines for new path coverage and guard assertions;
it does not earn a size reduction. The overall reduction is now **574 (4.28%)**,
with **769 more** counted lines to remove to reach 12,082.

Of the 32 lifecycle cases, 13 use an executor (ten new production cases, the
startup deadline, and two named severed-exec faults). Ten direct diagnostic
cases now have explicit compatibility reasons. The other nine still use the
generic no-executor worker: success, nonzero exit, cancellation, two transient
read rows, persistent read failure, two live-sidecar completion cases and
late sidecar failure. Their production counterparts and retention decisions,
the remaining scoped families, supervisor-state cleanup, full per-row audit,
and final size reduction remain unfinished. The goal is still active.

## Twelfth pass: share successful container setup (2026-09-08)

`container_restored_test.go` now has one local `createTask` helper for
successful task creation using that fixture's worker, context and delegate.
Fifteen repeated setup blocks use it. It calls the existing `restoredTask`,
asserts the same success condition with `GinkgoHelper`, and returns the
same container. It does not run a command or simulate an executor.

The six sidecar specs now sit under the existing `Container` fixture instead
of repeating its worker/client/config/delegate setup. They still get fresh
objects for every case. Their original leaf names and assertions remain;
their full names gain the enclosing `Container` prefix. The separate
concurrency fixture is untouched. Alternate-worker calls and intentional
container-creation error handling remain explicit. The failed-Run metric
test shares only its successful creation preamble, not the failing Run.

### Equivalence and mutation evidence

Evidence: `/tmp/brine-container-fixture.eLhp8y/`.

- Original and corrected candidate controls each passed **85/85** Ginkgo
  cases, in **26.918s** and **27.115s** spec time. The first temporary
  candidate nested the sidecar block under the wrong outer fixture and did
  not compile; it was corrected before any repository test was changed.
- The read-only `audit.go` comparison found **41 named It callbacks
  unchanged**, **27 literal task handles with every creation argument
  unchanged**, and **15 shared setup calls**. It parses Go ASTs, normalizes
  callback/argument syntax and requires a nonempty audit. It does not treat
  a textual move as proof of equivalent runtime setup.
- Eleven production mutations were run before/after against the **19**
  affected specs. The exact failing leaf-name sets matched for every pair:

| Mutation | Failing leaves in each version |
|---|---:|
| Wrong pod name | 1 |
| Remove main-container mounts | 9 |
| Wrong mount name | 1 |
| Add unexpected init container | 1 |
| Zero CPU request | 1 |
| Clear privileged flag | 1 |
| Allow privilege escalation | 3 |
| Change seccomp profile | 4 |
| Remove sidecars | 6 |
| Leave sidecar image prefixes intact | 1 |
| Remove failed-container increment | 1 |

Each mutation directory contains `container.go`, `before.json`,
`after.json` and matching logs. The overlays also select the saved original
or candidate test file. No production file was edited. Reproduce from the
repository root with:

```sh
GOFLAGS=-overlay=<mutation-dir>/after.json ginkgo --no-color --fail-on-empty \
  --focus='Container Run with (input volumes|output volumes|same-name input and output|non-overlapping inputs and outputs|cache volumes|scratch path volumes|resource limits|security context)|Container Run creates a Pod|Container Run metrics|Run with sidecar containers' \
  ./atc/worker/jetbridge -- '-test.run=^TestJetbridge$'
```

The nineteen affected JB-container rows now name their retained Go contract,
precise observation and mutation. None is claimed closed by a new brine
replacement. Existing limitations are explicit: the input-name test's
conditional loop still passes when no matching mount exists, the scratch
test observes empty init containers rather than DB cache entries, and the
artifact-labeled sidecar fixture does not independently establish artifact
backend configuration. Consolidating setup does not repair those limits.

### Applied result and validation

The repository file is byte-identical to the mutation-tested, formatted
candidate. No test case or assertion was removed.

- Full `ginkgo --no-color ./atc/worker/jetbridge`, including native Go tests:
  **85/85**, **26.949s** spec time, **59.185s** command time
  (`root-go-final.log`).
- Nested `go test ./... -count=1`: pass, **7.372s** for steps;
  `go vet ./...`: pass.
- All three explicit vocabulary guards: pass, **0.480s**.
- `git diff --check`: pass.
- Container test file: **1,868 -> 1,753 raw lines** and
  **1,541 -> 1,424 counted code lines**.
- Fixed scope: **17,357 raw / 12,734 counted code lines**, **187** expanded
  feature cases, **292** used definitions and **1,092** total definitions.

This removes **117** counted lines of repeated setup, net of the new helper.
Moving the sidecar block earns no credit by itself. Total reduction from
13,425 is **691 (5.15%)**; **652 more** counted lines must be removed to reach
12,082.

No brine feature, adapter or production source changed in this pass, so no
fresh CLI or coverage result is claimed. The latest unchanged-suite gates
remain **581/581**, **175.277s wall**, and **1,762/2,241 production statements
(78.625614%)**, above the 40% requirement. Remaining production/fallback
migration and retention decisions, supervisor-state cleanup, per-row audit
and final size reduction are still required. The goal is not complete.

## Thirteenth pass: owned task supervisor state (2026-09-08)

The shared local executor now optionally maps the production supervisor's
first `S=` assignment into an explicitly supplied fixture workspace. It
preserves the production-generated basename (process ID and command hash)
and every remaining script byte. Task-command and running task-cancellation
fixtures supply that workspace; ordinary commands and resource stdin execution
remain unchanged. No production source or shell template was edited.

The task-workspace disposer follows its private allocation path, not the
public working-directory field, and rejects values without an owned allocation.
It removes only that allocation. No host `/tmp` scan or historical state cleanup
was performed. Unit tests allocate their own outside sentinel and independent
teardown directories, including when challenging a deliberately broken disposer.

`TestSupervisorStateIsOwnedByItsWorkspace` executes real shell commands to
check restart state reuse, isolation between workspaces, apostrophe quoting,
an unchanged caller command, absence of writes to the original state directory,
owned disposal and preservation of another workspace and the outside sentinel.
`TestSupervisorStateMappingRejectsUnsafeHeaders` rejects malformed or escaping
state assignments while retaining ordinary shell execution. These tests do not
substitute for the real production supervisor's restart/command-key contracts.

The existing positive task scenario now also requires nonempty supervisor state
inside its workspace (no exact directory-count assertion). The existing running
task-cancellation check verifies the same ownership after checking the child
stopped. No scenario, outline row, legacy test or existing assertion was removed.
The JB-process-039 timing contract and prior cancellation observations remain.

### Mutation evidence

Evidence: `/tmp/brine-supervisor-cleanup.r47Vma/`. Healthy controls passed all
**7/7** task-command and **21/21** step-closing cases. Six fixture mutations
each compiled and failed the named supervisor unit tests:

| Mutation | Observed failure |
|---|---|
| Skip state scoping | Second workspace reused the first workspace's counter |
| No-op disposal | Owned directory survived disposal |
| Dispose through public path | Owned directory survived disposal |
| Accept non-`/tmp` state | Unsafe `/etc/state` assignment was accepted |
| Remove POSIX apostrophe quoting | Real shell command failed |
| Collapse production state basename | Expected keyed state file was absent |

The unsafe-header scripts only assign a variable and print; they never write to
those paths. The wrong-disposal-path mutation targets a directory allocated by
the test itself, not unrelated host data. All six runs use isolated source
overlays and `go test ./steps -run TestSupervisorState -count=1 -v`.

Four isolated adapter builds also failed their selected real-brine cases at the
intended behavioral assertion:

| Mutation | Observed failure |
|---|---|
| Task executor wired to wrong owned subdirectory | Expected workspace supervisor directory absent |
| Cancellation executor wired to wrong owned subdirectory | Running-task cancellation ownership check failed; other three rows passed |
| Production state key omits command hash | Changed command produced one `ran`, expected two |
| Production supervisor always reruns | Web restart produced three `ran`, expected one |

The two production mutations establish that fixture mapping does not hide the
command-key or replay faults. Their source overlays leave repository production
files unchanged. Reproduce each CLI mutation from its own manifest directory
using `brine run test.feature --mode sync --format brief --no-engine`; build its
adapter from the brine module with `go build -overlay=<dir>/overlay.json
-o <dir>/adapter ./cmd/brine-adapter-jetbridge` first. Each directory retains the
overlay, source, feature, manifest and result log. All ten mutation jobs finished.

A read-only post-run check also confirmed that the exact workspace roots named
in the task-wiring and cancel-wiring failures (`/tmp/brine-task4289133553` and
`/tmp/brine-task1645760033`) no longer existed. Thus the real CLI disposed those
owned allocations on assertion failure, not only in the manual unit disposer
test. No cleanup command was used for this observation.

### Current scope and remaining work

Nested `go test ./... -count=1` passes (**7.732s** for steps), `go vet ./...`
passes, and the three explicit vocabulary guards pass (**0.483s**). The census
reports **187** scoped cases, **293** used / **1,093** whole-suite definitions,
and **17,506 raw / 12,871 counted code lines**. New guards and their tests add
**137** counted lines; they receive no consolidation credit. Net reduction is
**554 (4.13%)**, with **789 more** lines required to reach **12,082**.

This is not global supervisor-state ownership: artifact handoff still installs
the shared executor without a supervisor workspace, and integration task
commands still use a workspace nonce without mapping their state into it. Those
owners need a separate lifecycle review. Remaining fallback migration/retention,
per-row disposition review and size reduction are also unfinished.

The fresh uninstrumented full CLI passed **581/581** in **175.040s wall**
(**18.43%** above the 147.795s baseline, below the **177.354s** limit).
`brine-final.log` retains the verdict and timing. It ran after all mutation
and nested-test jobs ended, using a freshly rebuilt normal adapter and the
same engine mode and host as the baseline. `git diff --check` passes; the
executor count remains **2**. The root Go suite is unchanged since the
twelfth pass's **85/85** result; no fresh root-suite result is claimed here.

The fresh `sh scripts/coverage` run also passed **581/581**, covering
**1,762/2,241 production statements (78.625614%)**, above the required **40%**.
Evidence: `/tmp/brine-coverage.APdCvn/coverage.out` and its adjacent CLI log;
profile SHA-256:
`f946739c02ba0e8d3eea3eacb5fe364b557d44ef8559ece9b35c3bbe1c30d851`.
`coverage-final.log` in this pass's evidence directory records the successful
integer-ratio gate. The script completed and restored the normal adapter.
No Go-test or fixture coverage was added to this denominator. All validation
jobs for this pass are terminal. The goal remains active and incomplete.

## Fourteenth pass: explicit legacy lifecycle contracts (2026-09-08)

The nine remaining generic-worker cases in `pod-lifecycle.feature` are now
explicitly named **Legacy compatibility** and select the existing
`direct compatibility` worker. Both old and explicit worker definitions call
`NewCluster(res)` without options; this does not silently change their executor,
team, volume-repository or Kubernetes setup.

These cases retain distinct `Process.Wait` contracts: immediate completion
and cancellation deletion, main-container completion while sidecars keep the
pod Running, late-sidecar failure precedence, and `PodWatcher.Next` initial-sync
retry/error handling. They are not production task/reaper tests. Production
supervised execution remains in task-command and cancellation in step-closing.
The pod-lifecycle file is now classified as **13 exec-path cases** (ten startup
diagnostic cases, one startup deadline, two named severed-exec faults) and
**19 explicitly retained compatibility cases** (ten diagnostic and these nine).
Other scoped families still require their own executor/retention review.

The two running-sidecar declarations share one two-row outline. Handles,
images, names, commands and exit assertions are retained; the nonzero row now
also checks pod deletion. No expanded scenario row or Go test was removed.

### Equivalence and fault evidence

Evidence: `/tmp/brine-compatibility-review.v3s34E/`.

- Original and candidate controls both passed **9/9**.
- `audit.go` uses brine's parser, requires nonempty unique case identities,
  and compares all expanded inputs/actions/assertions. It found **nine**
  unchanged cases after normalizing only the equivalent worker Given, with
  **one added** nonzero-sidecar cleanup assertion.
- Seven production overlays were tested against both versions. The exact
  case-name audit in `mutation-audit.json` confirms all **17** original
  mutation/case failures remain, plus the one newly caught cleanup failure.

| Production mutation | Original failures | Current failures |
|---|---:|---:|
| Wrong main exit code | 7 | 7 |
| Skip legacy completion deletion | 2 | 3 |
| Wrong exit code for a Running pod | 3 | 3 |
| Let late sidecar failure override terminated main | 1 | 1 |
| Skip legacy cancellation deletion | 1 | 1 |
| Refuse transient initial-sync retry | 2 | 2 |
| Lose persistent initial-sync error category | 1 | 1 |

The positive production task additionally passed with legacy completion
deletion disabled. That mutation therefore distinguishes the retained legacy
contract from this production task contract; the latter does not establish
equivalence for deleting the former. Every mutant compiled and failed an
actual assertion, not a build, empty selection or timeout. No production
source was edited. Reproduce builds with `go build -overlay=<dir>/overlay.json
-o <dir>/adapter ./cmd/brine-adapter-jetbridge` from the brine module, then run
`brine run test.feature --mode sync --format brief --no-engine` from each
mutation's `before/` and `after/` manifest directories.

JB-process-000/001/002/035/036/037/040/041/043 now carry per-row retention
reasons, limits and mutation references. In particular, the two-error row does
not prove a counter reset after success, and the persistent-error assertion
does not establish exactly three attempts. No historical Go deletion is
retroactively justified by this feature consolidation.

### Scope and validation

Nested `go test ./... -count=1` passes (**8.390s** for steps), vet passes, and
all three vocabulary guards pass (**0.536s**). Production process/watch sources
are unchanged and `git diff --check` passes.

All fixed-scope totals remain unchanged: **187** expanded cases, **293** used
definitions, **1,093** whole-suite definitions, **17,506 raw / 12,871 counted
lines**, and two executor implementations. Sharing the sidecar declaration
and adding explicit explanatory text earn **no net line-reduction credit**.
The **789-line** reduction gap remains. Other families' fallback reviews,
remaining state ownership, per-row audit and final consolidation are unfinished.

The fresh normal full suite passed **581/581** in **176.259s wall**
(**19.26%** above baseline, within the **177.354s** limit), with no competing
test/build jobs. Evidence: `brine-final.log`. The unchanged root Go suite's
latest result remains the twelfth pass's **85/85**; no new root result is claimed.

Fresh brine-only coverage also passed **581/581**, covering **1,762/2,241
production statements (78.625614%)**, above the **40%** requirement. Retained
profile: `/tmp/brine-coverage.B7C59r/coverage.out`; SHA-256:
`7ee37bfdf7408775bfbf1822a5081c9b0508612b5a385fa8dc17a63c72601af8`.
The adjacent CLI log and this pass's `coverage-final.log` retain the full gate.
The script restored the normal adapter, all validation jobs are terminal,
and the goal remains active. No Go tests were deleted and no production source
or shell template changed in this pass.

## Fifteenth pass: shared resource-envelope assertions (2026-09-08)

The CPU/memory limits, CPU/memory requests and local-disk envelope checks now
declare their two expected fields and share parameter extraction, main-container
lookup, resource-list selection, quantity parsing, presence checking and numeric
comparison. The field declarations keep the resource name, limit/request choice
and blank allowance explicit. CPU/memory preserve their existing optional blank
expectations; disk values remain mandatory. The three registered sentences and
their scenarios are unchanged.

This is an assertion-plumbing consolidation, not a replacement of production
resource calculation by a fixture model. It reads the pod produced by the worker.
Failure messages now use consistent resource/kind labels; verdict equivalence,
not byte-identical fixture diagnostics, is the preserved contract.

### Before/after evidence

Evidence: `/tmp/brine-resource-checks.6cJsRU/`.

- Both original and candidate resource-feature controls passed **4/4**.
- `audit.go` runs the actual registered checks through brine's public pipeline
  over the same finite matrix: three checks, present/absent main container,
  independent limit/request maps, missing fields, zero/negative/distinct
  quantities, equivalent units, blank strings and invalid quantity strings.
  All **60,000** verdicts match exactly (**861** accepted, **59,139** rejected).
  The audit requires nonempty passing and failing observations; the shared
  checker itself always receives two explicit field declarations.
- The before/after `predicate.json` files are byte-identical, SHA-256
  `3225809c15b66d08b78b3e4c2e328feb83d37c399548583dfb7f1a4387d6ec57`.
- `ast-audit-final.log` confirms **29** non-target definitions and **7**
  unrelated helper functions are AST-identical. The initial attempt to pass
  Go source paths directly to `go run` treated them as compilation units;
  compiling the audit executable and passing the paths as arguments corrected
  that harness invocation. It was not a product/test failure.
- Twelve production overlays independently remove or alter each of CPU,
  memory and ephemeral-storage limits and requests. Every mutant compiled,
  and every original/current pair failed exactly the same cases: **20**
  mutation/case failures on each side, verified in `mutation-audit.json`.

| Production field | Cases failing when missing, each version | Cases failing when wrong, each version |
|---|---:|---:|
| CPU limit | 2 | 2 |
| Memory limit | 2 | 2 |
| Disk limit | 1 | 1 |
| CPU request | 2 | 2 |
| Memory request | 2 | 2 |
| Disk request | 1 | 1 |

Build a mutant from the brine module with `go build
-overlay=<mutation-dir>/<before-or-after>.json
-o <mutation-dir>/<before-or-after>/adapter ./cmd/brine-adapter-jetbridge`;
then run `brine run test.feature --mode sync --format brief --no-engine`
from that version's manifest directory. Source overlays, manifests, exact
features and logs are retained. The predicate audit is equivalence measurement,
not an added replacement suite or relocated test code.

JB-container-019/021/022/023 record the changed assertion backing and evidence.
The requests-only Go test for **nil limits** remains explicitly retained:
matching request quantities and a Burstable label do not establish that separate
observation. No REFUTED row is claimed closed by this helper extraction, and no
scenario, outline row or restored Go test is deleted.

### Applied result

The repository source is byte-identical to the formatted mutation-tested
candidate. All twelve paired mutation jobs are terminal.

- Nested `go test ./... -count=1`: pass, **8.104s** for steps.
- `go vet ./...`: pass.
- All three explicit vocabulary guards: pass, **0.520s**.
- `git diff --check`: pass; production `container.go` is unchanged.
- Resource-check file: **703 -> 660 raw**, **551 -> 511 counted** lines.
- Fixed scope: **17,463 raw / 12,831 counted** lines, **187** expanded cases,
  **293** used definitions and **1,093** total definitions. Executor count is
  unchanged at two.

This removes **40 counted code lines**, net of the new field type and shared
helpers. Comment edits earn no credit. Total reduction is **594 (4.42%)**;
**749 more** counted lines must be removed to reach **12,082**. The goal remains
incomplete, including other families' execution-path/retention review, remaining
state ownership and final disposition audit.

The fresh normal full CLI passed **581/581** in **175.631s wall**
(**18.83%** above the 147.795s baseline, below the **177.354s** limit), with
all other mutation/test/build jobs already finished. Evidence: `brine-final.log`.
The root Go suite has not changed since the twelfth pass's **85/85** result;
no fresh root-suite result is claimed for this brine-assertion-only change.

Fresh `sh scripts/coverage` also passed **581/581**, covering **1,762/2,241
production statements (78.625614%)**, above the required **40%**. Profile:
`/tmp/brine-coverage.hVqAvk/coverage.out`; SHA-256:
`e01523ffd1cab3353f484784d44736933cf959050bc641ee146bf3cd2a7d929c`.
The adjacent CLI log and this pass's `coverage-final.log` retain the gate.
The script completed and restored the normal adapter. All verification jobs
are terminal, `git diff --check` passes, and the goal remains active.

## Sixteenth pass: shared sidecar mount assertions (2026-09-08)

The three named sidecar Go tests in
`behavioral_permutations_restored_test.go` now share `assertSidecarMounts`.
Their configurations, metadata, handles, container specs and seven explicit
path expectations are unchanged. The cache fixture still asserts the name
`helper`. The scratch-only fixture gains main/sidecar mount-count equality.
No test declaration, scenario or outline row is removed.

Evidence is retained in `/tmp/brine-sidecar-mounts.WFbuZG/`: before/after source
and Go overlays, clean controls, `audit.go`, `audit.log`, production mutation
overlays and paired logs, and `mutation-audit.json`. The AST audit checks all
three fixture setup prefixes and seven expected paths, preserves the original
name assertion, and finds 24 unrelated functions AST-identical. The applied
source matches the formatted candidate. Removing the scratch fixture's expected
path fails the helper's nonempty-expectations guard (`empty-expectations.log`).

Ten isolated mutations of production `buildSidecarContainers` preserve all
19 original failing test/mutation combinations and add one:

| Production fault | Before failing tests | After failing tests |
|---|---:|---:|
| Omit sidecar | 3 | 3 |
| Duplicate sidecar | 3 | 3 |
| Wrong sidecar name | 1 | 1 |
| Omit all sidecar mounts | 3 | 3 |
| Drop workspace mount | 2 | 3 |
| Wrong cache path | 2 | 2 |
| Wrong scratch path | 2 | 2 |
| Wrong input path | 1 | 1 |
| Wrong output path | 1 | 1 |
| Wrong workspace path | 1 | 1 |

The added failure is the scratch-only fixture detecting the dropped workspace.
All mutation runs compiled and failed at the named tests; none are empty-test
or compile-error results. All jobs are terminal. The three REFUTED disposition
rows (`JB-behavioral_permutations-009`, `-010`, `-014`) retain their named Go
tests and now describe this evidence. Mount count and required paths do not
prove full VolumeMount field identity. Nil TaskCacheIdentity still selects
emptyDir caches even in the DaemonSet-configured fixtures; this pass does not
claim hostPath cache coverage or close the previous brine-equivalence gaps.

Validation:

- Full root suite via `ginkgo --no-color ./atc/worker/jetbridge`: **85/85**,
  **26.925s** specs / **59.144s** command, including the native Go tests.
- Nested `go test ./... -count=1`: pass, **7.277s** for steps.
- `go vet ./...`: pass.
- Three explicit vocabulary guards: pass, **0.482s**.
- Fixed-scope census: **17,421 raw / 12,801 counted** lines, **187** expanded
  cases, **293** used definitions, **1,093** total definitions; two executors.

The net saving is **30 counted code lines**, including the shared helper.
Total reduction is **624 (4.65%)**; **719 more** counted lines must be removed
to reach **12,082**. Raw lines fall by 42 but comment/blank savings earn no credit.

This pass changes only restored Go tests and documentation, not brine features,
steps, adapter or production code. No fresh CLI/coverage result is claimed:
the latest unchanged gates remain the fifteenth pass's **581/581**, **175.631s**,
and **1,762/2,241 = 78.625614%** brine-only production statement coverage.
The 40% requirement is satisfied by that recorded result; the overall goal
remains incomplete. Remaining work includes the line-reduction target,
execution-path/retention review, fixture state ownership and disposition audit.

## Seventeenth pass: owned artifact-handoff supervisor state (2026-09-08)

The returned-volume handoff now declares the existing scenario-scoped
`task-workspace` resource. Its producer and consumer use separate subdirectories
under that workspace for supervisor state, through the same `localExecutor`.
`runHandoffTask` requires nonempty state in the expected task's directory after
a successful wait. Producer state therefore cannot make the consumer ownership
check pass. Scenario disposal removes both trees on success and failure.
Production supervisor templates and execution behavior are unchanged.

The five raw/gzip/s2/success/transfer-failure examples and all their artifact
expectations are unchanged. No restored Go test, feature case, step definition
or disposition row is deleted or claimed replaced. The initial structural
comparison found only two small outline opportunities; no feature rewrite is
included in this pass.

Evidence: `/tmp/brine-outline-consolidation.tgyjiA/`. Isolated overlays trace
workspace allocations without altering their creation or disposal. The clean
control passes **5/5**, and a separate filesystem checker verifies five unique
allocations no longer exist after the adapter exits. A wrong producer root
fails **5/5** at the producer-state check; a wrong consumer root fails precisely
the **three** examples that execute the consumer, while both transfer-failure
examples still pass. All five workspaces are disposed in each faulty run too.
The initial isolated control failed during daemon build because its cwd was
outside the repository; `setup-failure.log` retains that setup-only result.
The tested wrappers subsequently set the adapter's cwd to the brine module;
that initial failure is not counted as mutation evidence.

Nested Go tests pass (**7.856s**), vet passes, and all three explicit vocabulary
guards pass (**0.472s**). The census remains **187** expanded scoped cases,
**293** used / **1,093** total definitions and two executor implementations.
This ownership fix adds **13 counted lines**: **17,434 raw / 12,814 counted**
in total. The reduction is now **611 (4.55%)**, with **732** counted lines still
to remove to reach **12,082**. This is cleanup progress, not line-reduction
credit. Remaining ownership gaps include integration-task supervisor state and
other temporary fixture allocations; this pass does not claim global cleanup.

The disabled-disposer mutation passes all five artifact examples but leaves
five distinct workspaces containing actual producer supervisor state. The
independent filesystem check detects all five leaks and reclaims only those
test-factory allocations (`no-disposal/cleanup-audit.log`). This distinguishes
working artifact delivery from working fixture cleanup. The retained
`TestSupervisorStateIsOwnedByItsWorkspace` also tests the shared disposer; the
isolated observer verifies its wiring through real CLI scenario teardown.

The fresh normal full CLI passes **581/581** in **174.854s wall**, **18.31%**
above the 147.795s baseline and below the **177.354s** limit. All mutation,
Go-test and build jobs were terminal before that benchmark began.
Evidence: `brine-final.log`. Fresh `sh scripts/coverage` also passes **581/581**,
covering **1,762 / 2,241 production statements (78.625614%)**, above **40%**.
Profile: `/tmp/brine-coverage.0y0nuV/coverage.out`; SHA-256:
`1a7272b4b0d189a51ad7ac5fce892757d88e9dbc2764b72a03d498492f79ffdc`.
`coverage-final.log` retains the gate output. The script completed and restored
the normal adapter; Go unit-test and fixture coverage are excluded. The applied
handoff source is byte-identical to the mutation-tested control source,
`git diff --check` passes, and all verification jobs are terminal. The root Go
tests are unchanged since the sixteenth pass's **85/85**; no fresh root-suite
result is claimed for this brine-fixture-only change. The goal remains active.

## Eighteenth pass: one integration workspace and production exec by default (2026-09-08)

`IntegrationCluster` now carries the scenario's existing `TaskWorkspace`, and
`newIntegrationCluster` always installs `localExecutor` with that supervisor
root. Resource scripts use `resource-image/` under the same workspace and keep
the same supervisor root when their executor is installed. The unused
`ResourceRoot` field and separate unowned `MkdirTemp` allocation are gone.
The command step no longer adds a workspace comment to the user's command:
the owned supervisor root itself prevents cross-scenario replay. A completed
task must leave supervisor state in that root.

One redundant definition, `the worker execs commands in pods`, and its three
feature uses are removed. Integration container runs now take the production
exec path, including the seven pod-name/label/environment cases that previously
created direct-mode pods. No scenario or outline row is removed. The expanded
feature audit preserves all **39** case names, tags, parameters and assertions,
normalizing only those three setup steps. **67** unrelated definitions are
AST-identical. These checks establish preservation of the feature contract,
not equivalence between the old fallback path and production exec.

The restarted-ATC volume-lookup fixture intentionally constructs a fresh worker
without an executor. It only calls `LookupVolume`, never `Run` or `Wait`, so it
is not fallback execution coverage. Its fresh volume-repository and persisted
state reconstruction assertions remain unchanged.

The command step also propagates `StepRan.Err` instead of allowing a following
put to replace and discard it. Ordinary nonzero exit statuses remain results;
this change catches execution errors, not arbitrary nonzero exits.

Evidence: `/tmp/brine-integration-workspace.Wpi8d7/`, including before/after
sources, `audit.go`/`audit.log`, isolated overlays and wrappers, `run-variants.sh`,
all eight CLI logs/status files, and `validate-runs.go`/`validation.log`.
The overlays log allocated paths but do not change allocation/disposal behavior.
Production files in the repository are unchanged.

| Isolated experiment | Before | After |
|---|---|---|
| Clean integration feature | 39/39 pass | 39/39 pass |
| Production `createPausePod` returns an error | 10 fail | Same 10 plus 7 fail |
| Shared executor refuses only `echo built` | 39/39 pass (error discarded) | Task-to-put case fails at its command step |
| Default executor writes supervisor state in wrong owned subdirectory | n/a | Artifact-consumer task case fails |
| Resource-image executor writes state in wrong owned subdirectory | n/a | Task-to-put case fails |

The seven added pause-pod witnesses are generated pod names, handle fallback,
rich labels, absent labels, truncated pipeline labels, secret env references,
and literal env values. The production mutation demonstrates entry into the
pause-pod path; it is not presented as a per-test equivalence proof for deleting
any legacy test. The two ownership faults are distinct: reinstalling the
resource-image executor cannot silently lose supervisor ownership.

The filesystem audit observes **39 distinct workspaces disposed per run**, on
success and failure. Clean controls install seven resource images and execute
two supervisors. Previously those nine allocations were outside the workspace;
after consolidation all are beneath it and disappear with it. The three legacy
comparison runs left 24 test-owned allocations in total; the verifier reclaimed
only their exact traced paths. No broad `/tmp` sweep was used. A quoting error
in the first generated verifier prevented compilation and was corrected before
it ran; that tooling failure is not counted as SUT or mutation evidence.

The three REFUTED restored integration rows (`JB-integration-000`, `-011`,
`-012`) retain named Go tests for exact pause/image/forwarding details, the
two-input put/JSON contract, and full sidecar mount/env/port behavior. Their
retention notes now distinguish those contracts from this pass's gains. No
restored Go test or historical deletion verdict is changed.

Final nested Go tests pass (**8.024s**), vet passes, and the three explicit
vocabulary guards pass (**0.478s**). Census: **187** expanded scoped cases,
**292** used definitions, **1,092** total definitions, two executors,
**17,418 raw / 12,810 counted** lines. The net saving is **four counted lines**;
the larger raw-line saving includes commentary and earns no extra credit.
Total reduction is **615 (4.58%)**, leaving **728** counted lines to reach
**12,082**. The integration fixture's separate supervisor/resource-image
ownership gap is closed; other families' temporary allocations, remaining
fallback/retention decisions and the final disposition audit remain open.

The fresh normal full CLI passes **581/581** in **175.935s wall**, **19.04%**
above the 147.795s baseline and below the **177.354s** limit. All control,
mutation and Go verification jobs were terminal before the benchmark began.
Evidence: `brine-final.log`. Fresh `sh scripts/coverage` also passes **581/581**,
covering **1,762 / 2,241 production statements (78.625614%)**, above **40%**.
Profile: `/tmp/brine-coverage.z3n6ed/coverage.out`; SHA-256:
`88913f79807bca2578b86a41c3fb9b58fdb070cf427197f41c4ab5fe62e58f82`.
`coverage-final.log` retains the gate output. The script completed and restored
the normal adapter. Go unit-test and fixture coverage are excluded.

The applied integration source matches `after.go`, the source used to prepare
the validated overlays; `git diff --check` passes. All eight isolated runs,
their verifier, Go/vocabulary checks, full CLI and coverage jobs are terminal.
Production code and shell templates are unchanged. The root Go tests have not
changed since the sixteenth pass's **85/85** result; no fresh root-suite result
is claimed for this brine-only change. The goal remains active.

## Nineteenth pass: reuse shared cluster setup and state (2026-09-08)

Integration setup now uses the existing `NewCluster` options for namespace,
volume-repository wiring, executor and deadlines. `IntegrationCluster` embeds
`Cluster` instead of repeating its worker/database/client/context fields. The
shared cluster exposes the exact configuration used to construct its worker,
so the restarted-ATC lookup keeps the original configuration. The integration
team is still explicitly named `main`, not the different name `WithTeam`
would generate; its ID is copied into the shared team's ID field.

`TaskCluster` similarly embeds `ClusterReady`, and its constructor uses the
existing `Ready()` projection instead of copying the same four fields. Its
scenario-owned workspace is preserved separately. No feature, step pattern,
scenario, assertion or restored Go test is removed or rewritten.

Evidence: `/tmp/brine-shared-cluster.1gQcFi/`. Four-file before/after snapshots,
constructor probes, an overlay generator using `apply_patch`, isolated CLI
logs/status files, source audit and result/cleanup verifier are retained there.
The control feature contains the unchanged 39 integration cases and the
unchanged first task-command case, selected through brine's parser.

The constructor probes compare the actual returned namespace, worker and team,
full configuration, parent context, workspace ownership and task-state field
projection. They also verify coherence of the newly exposed shared TeamID.
Both controls pass **40/40**, with matching observations from **29 integration
constructors** and **one task constructor**. These are fixture-equivalence
probes, not new production behavior assertions or grounds for deleting Go tests.

| Isolated fixture fault | Before | After |
|---|---|---|
| Omit volume-repository wiring | 10 failures | Same 10 failures |
| Executor refuses commands | 1 failure | Same failure |
| Wrong requested namespace | n/a | 29 constructor-probe failures |
| Wrong team name | n/a | Same 29 probe failures |
| Discard the stored configuration | n/a | Same 29 probe failures |
| Change the startup deadline | n/a | Same 29 probe failures |
| Lose task context in the state projection | n/a | Task-constructor probe fails |

Every run completes all forty cases and disposes forty unique workspaces.
The result verifier compares named failing cases, not just aggregate counts.
All eleven previously observed wiring-fault failures are preserved. The
configuration faults distinguish preserved fixture settings from merely green
neighboring cases; production files are not changed by these experiments.

The source audit finds **70 integration definitions / 18 unrelated functions**
and **eight task definitions / seven unrelated functions** AST-identical. The
shared fixture is unchanged except for exposing its existing `cfg` value; the
domain file is unchanged except for the TaskCluster embedding. An initial
utility attempt to print an unsupported AST field node was corrected before
the final audit; that tooling error is not counted as mutation evidence.

Worker persistence and creation of the `main` team retain their externally
visible order. Worker/resolver construction now precedes team creation; source
inspection confirms those constructors only allocate the objects used here,
without pod creation or network requests. Repository and executor setters
assign independent fields. The retained integration rows (`JB-integration-000`,
`-011`, `-012`) keep their eighteenth-pass Go-retention reasons; this setup
consolidation does not close their distinct behavioral equivalence gaps.

Nested Go tests pass (**7.303s**), vet passes, and all three explicit vocabulary
guards pass (**0.491s**). Census: **17,390 raw / 12,787 counted** lines,
**187** expanded scoped cases, **292** used / **1,092** total definitions,
two executors. The net saving is **23 counted lines**, including the two new
shared-configuration fields. Total reduction is **638 (4.75%)**, leaving
**705** counted lines to reach **12,082**. Comment/blank-line savings earn no
extra credit. The full CLI passes **581/581 in 176.841s**, below the 177.354s
limit with only 0.513s to spare. The fresh coverage gate passes **581/581** and
**1,762 / 2,241 production statements (78.625614%)**, excluding unit tests and
fixtures. Profile: `/tmp/brine-coverage.k3Ow2O/coverage.out`; SHA-256:
`fac151f1ea713e81ff6b4255e3e4e6e0a6f5972456a53d06a95d36b7f1cca589`.
Both jobs are terminal; the coverage script restored the normal adapter.
Root Go tests remain unchanged since the sixteenth-pass 85/85 result.
The goal remains active.

## Twentieth pass: one returned-output and artifact-handoff contract (2026-09-08)

The artifact-handoff outline now absorbs container-run's standalone
`A step's output can be read back out of the volume afterwards` case.
After the producer finishes, its returned output volume is read through
`Volume.StreamOut(".", gzip)` and checked for exact files. These include the
old case's literal `output.txt` / `hello-from-the-step`, the existing parameterized
file and `manifest.txt`. The neighboring decoy must remain absent. The pod is
then deleted and the existing daemon-backed artifact read, returned-input
stream, consuming task and exact-file comparison continue unchanged.

The direct read is explicitly a **compatibility** checkpoint, not production's
artifact-read route. Both old and new fixtures use the same unconditional
`Worker.newVolumeForMount` deferred-volume constructor; the shared fixture also
validates the actual pod and resolves its mount to the host directory. Unlike
the removed fixture, it owns supervisor state within the task workspace and
does not clear host-global supervisor directories before execution.

The five existing outline rows retain their names, tags, parameters, encodings
(raw/gzip/s2), two named transfer faults and expected outcomes. They now check
the additional file as well as their existing file and manifest. The removed
case contributes no separate scheduling or storage-backend contract: it used
a local shell and an absolute host path, without validating a Kubernetes mount.
The exact no-daemon Go contracts remain separately retained below.

Evidence: `/tmp/brine-output-consolidation.pYDZue/`. The apply_patch-based
generator preserves before/after snapshots, twelve isolated CLI directories,
production-only fault overlays, logs and exit statuses. Controls run the
original single case and the consolidated five-row outline separately.

| Production fault, scoped to returned output volumes | Original case | Consolidated outline |
|---|---|---|
| None | 1/1 passes | 5/5 pass |
| Disconnect output executor | Fails | All 5 fail |
| Omit returned output mount | Fails | All 5 fail |
| Tar the wrong output path | Fails | All 5 fail |
| Omit gzip compression of output | Fails | All 5 fail |
| Exec output read into the wrong pod | Passes: destination was ignored | All 5 fail: pod not found |

`verify.go` checks terminal exit status, completed case count, named failures
and the expected failure cause for every run. It proves preservation of the
four original fault detections and the newly distinguishing pod-target fault.
Faults are temporary Go overlays; no production source or shell template is
changed. Successful controls also preserve both existing named transfer-error
outcomes after the new checkpoint.

The source audit proves thirteen unrelated container functions and four
unrelated handoff functions AST-identical, all handoff feature assertions/data
unchanged, and exactly one container scenario removed. It caught an overbroad
temporary feature slice that omitted later unrelated cases; the candidate and
generator were corrected before applying anything to the repository. A second
verifier correction normalized the blank left by deleting that one case.
Neither tooling issue is counted as production-mutation evidence.

Three private definitions, their registration and two private result types
are removed. No Go test is deleted. `JB-container-036` records the replacement
output-executor evidence. `JB-container-041` retains its named Go test for the
independent PodName/HasExecutor/concrete-volume, exact argv and raw stdout
assertions. `JB-container-042` retains its named output-bearing no-daemon
pod-retention test. Existing `JB-volume-009`–`011`, `013`, `014` Go retention
decisions remain: the shared gzip archive check does not assert every exec
attribute, selector, raw pipe byte or late reader-error contract. Historical
GAP/REFUTED labels are not relabeled on this brine-only consolidation.

Nested Go tests pass (**8.028s**), vet passes, and the three explicit vocabulary
guards pass (**0.511s**). Census: **17,229 raw / 12,666 counted** lines,
**186** scoped cases, **289** used / **1,089** total definitions, two executors.
The net saving is **121 counted lines**, including the added shared checkpoint.
Total reduction is **759 (5.65%)**, leaving **584** lines to reach **12,082**.
All deleted vocabulary is absent from active steps/features. Full CLI passes
**580/580 in 170.868s**, versus 176.841s in the previous pass and the 177.354s
limit. The live adapter sources and both feature files exactly match the
verified candidate snapshots. Fresh coverage passes **580/580** with
**1,762 / 2,241 production statements (78.625614%)**, excluding Go unit tests
and fixtures. Profile: `/tmp/brine-coverage.619GJf/coverage.out`; SHA-256:
`163b212da04a40b2adf5c4a91e37e992c524092afab64f1f3af45a02d655db33`.
The coverage job is terminal with exit zero and restored the normal adapter.
All twelve isolated runs, source/result verification, nested Go/vet/vocabulary,
full CLI and coverage gates are terminal. `git diff --check` passes. Production
source and shell templates are unchanged. The root Go sources are unchanged
since the sixteenth-pass 85/85 result. The goal remains active.

Next ownership targets verified in source: `VolumeSet`/`addVolume` and the
persisted-volume identity fixture allocate unowned temporary roots, while the
intercept fixture still calls `clearSupervisorState` on host `/tmp`.
The handoff consolidation removes its old caller, but does not claim those
other fixtures are cleaned up. Ownership work and further consolidation must
preserve the remaining distinct compatibility contracts.

## Twenty-first pass: owned direct-volume fixtures (2026-09-08)

The direct-volume vocabulary now uses the existing scenario-scoped
`task-workspace` resource. `VolumeSet` carries that workspace and `addVolume`
creates each volume's separate directory inside it. The workspace disposer
reclaims all of those directories at scenario end, on success and failure.
The first step explicitly requires the resource and rejects an empty root.

The failed-executor fixture no longer creates a directory: its executor
returns the named error before any filesystem operation. The persisted-volume
identity fixture also stops allocating an unused directory; it retains a
non-nil executor but checks only handles/rows, without running or streaming.
No scenario, step pattern, assertion, production code or Go test is removed.

Evidence: `/tmp/brine-volume-ownership.Vijrts/`. Before/after sources, ten
isolated overlay runs, logs/status files, source audit, root observations and
the independent cleanup verifier are retained. Each run executes the same ten
direct-volume/identity cases selected through brine's parser.

Both controls pass **10/10**. Production mutations preserve the same named
failures before/after: wrong read path **5**, wrong persisted handle **1**, and
swallowed writer error **1**. The source audit finds **28 unrelated definitions**
and **nine unrelated functions** AST-identical; the domain file changes only
by adding the workspace field.

Each old run leaves ten volume roots plus three unused allocations outside
the scenario workspaces. Each new normal/fault run has ten volume roots inside
the owned workspaces, no unused allocations, and no remaining roots after
disposal. A deliberately disabled disposer leaves the cases **10/10 green**
but leaks ten workspaces; the independent observer detects those leaks.
An empty-workspace variant fails all nine volume cases at the resource guard,
creates no escaping volume roots, and still disposes the actual owned roots.
The identity case is unaffected and passes.

The initial verifier assumed an unused scenario resource would not be created.
Actual traces showed brine eagerly creates all scenario resources: ten
workspaces were already allocated and disposed in the old control, but its
volume directories lived elsewhere. The verifier was corrected to check that
observed lifecycle. A generation-anchor ambiguity and documentation-updater
syntax errors were likewise corrected; none is production-fault evidence.

After verifying the terminal runs, the observer reclaimed **62 exact traced
experiment allocations**: 52 roots from the four legacy runs and ten from the
deliberately disabled disposer. It validated their paths/types first. It did
not sweep host /tmp or touch unknown pre-existing directories.

Ten affected historical volume rows now explicitly name their retained Go
tests and concrete distinctions: `JB-volume-000/002/003` (independent handle,
pointer identity, team/type), `014` (late raw reader error), `015/016/017`
(full stub diagnostics and HasExecutor), `019` (two persisted identities), and
`020/021` (exact ordinary/late-bound pod routing). Existing merged StreamIn
and StreamOut retention decisions remain unchanged. No historical
GAP/REFUTED verdict is relabeled.

Nested Go tests pass (**7.138s**), vet passes, and all three vocabulary guards
pass (**0.490s**). Census: **17,229 raw / 12,665 counted** lines, **186** scoped
cases, **289** used / **1,089** total definitions and two executors. This pass
primarily fixes fixture ownership; its net saving is **one counted line**.
Total reduction is **760 (5.66%)**, leaving **583** lines to reach **12,082**.
Full CLI passes **580/580 in 172.310s**, within the 177.354s limit. Both applied
Go sources match the validated candidate snapshots and `git diff --check`
passes. Fresh coverage passes **580/580** and **1,762 / 2,241 production
statements (78.625614%)**, excluding Go unit tests and fixtures. Profile:
`/tmp/brine-coverage.f8tQpz/coverage.out`; SHA-256:
`373df89d9c0cc8ee891f569cae070c94bba3f90756510e0be1fcff5d82d7da00`.
All ten isolated runs, source/ownership verification, nested Go/vet/vocabulary,
full CLI and coverage jobs are terminal; the coverage script restored the
normal adapter. Root Go sources remain unchanged since the sixteenth-pass
85/85 result. The intercept fixture's host-global supervisor cleanup and the
broader consolidation audit remain outstanding. The goal remains active.

Next size-reduction candidates found by source inspection: `closingTar` repeats
the existing `plainTarOfOneFile` implementation, and integration's `labelKeys`
and `keysOf` repeat the same map-key collection. Preserve the sorted output
contract of `sortedFileNames` if consolidating those helpers; despite its name,
the current bool-map `sortedKeys` helper does not sort. These are identified
opportunities, not savings claimed by this pass.

## Twenty-second pass: shared helpers and one successful-task contract (2026-09-08)

`closingServeTar` now uses the existing `plainTarOfOneFile`. Its former
`closingTar` implementation had identical function signatures and AST bodies;
the duplicate and its two now-unused imports are removed. Three string-map
key collectors are replaced by one generic `sortedKeys` shared with the
executor's bool-map diagnostics. Key membership and complete filename lists
are preserved. Previously unordered diagnostic lists are now deterministic;
the filename-error list remains sorted as before. No caller depends on the
old bool helper's nil-versus-empty slice because it joins the keys as text.

Helper evidence: `/tmp/brine-helper-consolidation.IFhP2W/`. Both controls pass:

- **45** exact archive byte/error comparisons, including empty/binary bodies,
  nested/long/unicode names and rejected NUL names.
- **257** nil/empty/subset map comparisons for string and bool values,
  including false-valued entries and missing-container refusal.
- **1,280** registered brine predicate comparisons covering nil pods,
  present/absent labels and mounts, wrong values, and success/error reads.

The probes run through the registered checks and brine pipeline, with expected
verdicts derived independently from the supplied state. A truncated archive
fault fails the same archive test before/after. An omitted-key fault fails
both the key-set test and the consumer-predicate test, rather than passing on
an empty collection. These are fixture faults, not production mutations.
The source audit verifies the remaining non-import declarations, excluding
the consolidated helpers, after only helper-call renaming: 16 closing,
17 integration, 11 volume and one exec-target function.
All existing predicates, cases and data are unchanged in this helper change.

The first candidate build rejected an additional unused `bytes` import; it
was removed before applying the candidate. The patch-size guard later stopped
the large integration file after applying the closing file; guarded small
diff hunks completed the remaining edits without overwriting unrelated work.
Neither tooling issue is counted as fault-detection evidence.

### Successful task scenario consolidation

The standalone `With an exec transport the pod is a placeholder the step runs
inside` case is absorbed by `A task's startup is timed and its output reaches
the build log`. Its two pod checks move intact into the kept case, which also
gains the PE-01 tag. The kept case's original handle, command, status,
properties, worker label, startup timing and owned-state assertions remain.
Its `hello world` containment assertion implies the old `hello` check.
The two legal handles and plain echo arguments do not select distinct runtime
branches; both used the same task fixture, nil stdin and production exec path.
Failed-task and output-bearing pod-retention contracts remain separate.

Task evidence: `/tmp/brine-placeholder-consolidation.drVCBG/`. Eight isolated
CLI runs use temporary production overlays; the original control passes
**2/2** and the merged control **1/1**.

| Production fault | Original cases | Merged case |
|---|---|---|
| Bake the user command into the pause pod | Placeholder case fails; startup case passes | Fails |
| Give the placeholder no command | Placeholder case fails; startup case passes | Fails |
| Delete the pod after successful exec | Both fail | Fails |

The verifier checks exact named failures, exit status, completed counts and
fault-specific diagnostics. Its feature audit proves exactly one scenario
removed and only the two pod checks/tag added to the otherwise unchanged kept
scenario. `JB-container-031` records that replacement. `JB-container-030` keeps
its named Go test for exact nil-stdin pause argv, call count and supervised
command shape; `JB-container-042` keeps its output-bearing no-daemon retention
test. Other integration/volume retention decisions remain unchanged because
the helper consolidation changes no predicates. No Go test or historical
GAP/REFUTED verdict is removed.

Final nested Go tests pass (**7.369s**), vet passes, and all three vocabulary
guards pass (**0.463s**). Census: **17,177 raw / 12,621 counted** lines,
**185** scoped cases, **289** used / **1,089** total definitions, two executors.
The helper consolidation saves **39 counted lines**, the scenario merge
another **5**, for **44** total. Overall reduction is **804 (5.99%)**, leaving
**539** lines to reach **12,082**. New shared implementation lines are counted;
comments and temporary measurement/oracle tooling earn no reduction credit.
Full CLI passes **579/579 in 169.857s**, versus 172.310s in the previous pass
and the 177.354s limit. All four applied Go files and both feature files match
their verified candidate snapshots. Fresh coverage passes **579/579** with
**1,762 / 2,241 production statements (78.625614%)**, excluding Go unit tests
and fixtures. Profile: `/tmp/brine-coverage.5IwvCr/coverage.out`; SHA-256:
`9eabec44e47f63d3f00b2e0b34c32665c76bc784bfe3d2f8939f734b25fe9784`.
All five helper probe runs, eight task CLI runs, both verifiers, nested
Go/vet/vocabulary, full CLI and coverage jobs are terminal. The coverage
script restored the normal adapter and `git diff --check` passes.
Production files and shell templates are unchanged; root Go sources retain
the sixteenth-pass 85/85 result, not a newly claimed root run. The goal
remains active.

Next possible redundancy: the standalone S2-decompression case has a private
S2-writing step used once, while the production returned-volume handoff
already has an S2 row. This is only a candidate: compare their actual streaming
routes and independently challenge decompression/byte delivery before removing
anything. The remaining intercept workspace ownership and disposition audit
also remain open.

## Twenty-third pass: one S2 contract with an independent encoding check (2026-09-08)

The standalone `An artifact compressed with s2 is decompressed on the way in`
case and its private S2-writing step are consolidated into the existing S2 row
of the production returned-volume handoff. The VT-08 tag and explanation move
to that outline. Its five data rows and all other feature assertions remain
unchanged. No Go test is removed.

Both cases call production `Volume.StreamIn` on a `NewDeferredVolume`:
the old fixture constructs it directly, while the handoff obtains it through
`Worker.newVolumeForMount` with an installed executor and a real returned
container mount. Both use root extraction and compare the delivered file's
contents. The kept row additionally checks nested paths and the exact complete
file set through the consuming task. The old gzip readback path is still
exercised by the direct-volume round-trip scenarios and the handoff's
returned-output compatibility checkpoint. Constructor arguments, raw opaque
bytes and execution attributes remain separate retained Go contracts.

The independent old S2 writer revealed a real distinction: if the production
S2 codec reports `raw`, the unguarded handoff stops both encoding and decoding,
and its files still arrive. The original scenario fails because its separately
created S2 bytes are not decompressed. A nil codec would have the same effect,
so the check also requires a non-nil codec for a non-raw request. The handoff
checks that the production codec's encoding equals the encoding explicitly
requested by the feature.
This is an independent expected value, not one derived from the same codec.
No production code or shell template was changed.

Evidence: `/tmp/brine-s2-consolidation.Vj0dj0/`. Temporary Go overlays supply the
before/candidate adapters and production faults without changing production
files in the workspace. Thirteen isolated CLI runs establish:

| Production fault | Original S2 case | Kept handoff |
|---|---|---|
| None | 1/1 passes | 5/5 passes |
| Bypass S2 decompression in StreamIn | Fails | Only S2 row fails |
| Return success without writing S2 input | Fails | Only S2 row fails |
| Extract S2 input under an unintended subdirectory | Fails | Only S2 row fails |
| S2 codec reports the raw encoding | Fails | Only S2 row fails at the new encoding check |
| S2 codec factory returns nil | Fails | Only S2 row fails at the new encoding check |

The unguarded handoff passes **5/5** under the last fault, proving why the new
check is necessary. `verify.go` checks statuses, completed counts, exact failed
scenario names (row 3 for S2), and the decompression/encoding diagnostics.
Its AST audit removes exactly the old S2 definition and unused imports;
the ten other definitions and eleven functions are otherwise identical.
The handoff is AST-identical after excluding just the new encoding guard.
The feature audit checks exact equality after removing the one named case
and transferring its tag. The first candidate compile caught an unused
`bytes` import; that was corrected before candidate runs or workspace edits,
and is not counted as fault-detection evidence.

Reproduction from the nested module, starting with the before snapshots:
`go run <evidence>/prepare.go` and `go run <evidence>/prepare-nil.go` generate
the isolated fixtures; build each variant with
`go build -overlay <variant>/overlay.json -o <variant>/adapter ./cmd/brine-adapter-jetbridge`.
Run `brine run test.feature --mode sync --format brief --no-engine` from each
variant directory with its supplied executable wrapper and save exit status
as `run.status`. `go run <evidence>/verify.go` verifies the resulting logs.
`apply.go` checks current files against the before snapshots before applying
small diff hunks. The snapshots, production overlays and logs are retained.

The disposition audit also records explicit Go retention for
`JB-volume_daemonset-011/-012/-013`: opaque raw peer-body delivery, a real
HEAD probe on a responsive missing peer, and exactly one producer request
with zero peer requests, respectively. These source-inspected assertions are
not equivalent to receiving the right parsed files. Historical verdicts stay
unchanged; the remaining inventory is not declared complete.

Ten remaining permutation rows also gain source-verified named Go retention
notes: `JB-behavioral_permutations-000/-002/-003/-004/-006/-007/-008/-011/-016/-018`.
They preserve hostPath assignments and complete mount sets for distinct
task/Put/Get/check configurations, literal batch-init specifications,
nil-versus-empty API results, and relative-scratch mount cardinality.
The notes name the current Go test and its precise distinction individually.
Existing fifth-pass wrong-storage-path evidence remains linked for the first
three; no new mutation or deletion is claimed for these retained tests.
The fresh root Go run covers the unchanged restored sources.

The remaining two integration and fourteen container rows also now have
individual named retention notes, checked against the current test bodies.
These include exact secret-list cardinality, returned output handles,
executor call counts and TTY flags, the failed DB transition, and shared-state
concurrent construction. Pod-spec-only fallback tests are identified as
construction compatibility coverage, not evidence of ordinary command execution.
The notes distinguish assertions from misleading titles: the Get-to-Put test
creates only a Put, the purported artifact-store override fixture installs no
artifact backend, and the explicit-emptydir test asserts SubPath rather than
positive EmptyDir allocation. No additional behavior is credited to those tests.

A non-empty-guarded text inventory now finds explicit consolidation/retention
notes on all **81 GAP/REFUTED rows** (`disposition-inventory.log`). This is
an inventory result, not proof that every replacement is equivalent. The
final source/evidence audit remains required.

Nested Go tests pass (**7.486s**), vet passes, and the three vocabulary guards
pass (**0.467s**). A fresh full root Ginkgo run, including native Go tests,
passes **85/85**, **27.115s** spec time / **59.664s** command time.
The census reports **184** scoped cases, **288** used / **1,088** total
definitions, two executors, and **17,119 raw / 12,590 counted** lines.
This pass saves **31 counted lines**, including the added encoding guard.
Overall reduction is **835 (6.22%)**, leaving **508** to reach **12,082**.
The final nil-safe candidate passes **578/578** in the full uninstrumented CLI:
**171.898s** wall, below **177.354s**, with other test/build jobs terminal.
An earlier full run/coverage profile covered the first, non-nil-guarded version;
that profile is not substituted for final-source coverage.
Fresh `sh scripts/coverage` validation of the final nil-safe source passes
**578/578**, covering **1,762 / 2,241 production statements (78.625614%)**.
The denominator remains the production jetbridge package only: Go unit tests,
fixtures and adapter statements are excluded. Profile:
`/tmp/brine-coverage.wjvk55/coverage.out`; SHA-256:
`657cd2eda78f65a92468bc8aa80485f38ff470be33b905b129b0f0ffa420d09d`.

All thirteen final control/mutation runs, source/result verifiers, nested
Go/vet/vocabulary, root Go, full CLI and final coverage jobs are terminal.
The coverage script restored the normal adapter. All three edited source/
feature files match their verified candidate snapshots; the two production
mutation targets remain unchanged, and `git diff --check` passes.
This pass records 29 named Go retention decisions without deleting a Go test.
The goal remains active: **508 counted lines** and the final requirement-by-
requirement audit are still outstanding.

The next ownership exception remains the intercept fixture's
`clearSupervisorState` call in `steps/worker.go`. It sweeps a host-global
prefix instead of using the shared TaskWorkspace owner. Any replacement must
preserve `WorkerReady.rebuild`'s executor, locator and daemon-client ordering
and its explicit fault settings. Removing code outside the fixed counting
scope earns no size-reduction credit.

## Twenty-fourth pass: shared successful task construction (2026-09-08)

`container_restored_test.go` now shares successful construction through the
scoped `createTaskOn(worker, handle, spec)` helper. Eleven former direct calls
retain their explicit worker, handle and complete ContainerSpec at the call
site. The existing default-worker `createTask` helper delegates to it.
Context and delegate come from the same enclosing fixture bindings as before.
The shared helper retains the exact success assertion and both production
results, including returned mounts. Failure-path and concurrent calls still
use `restoredTask` directly with their own result/error handling.

This is setup consolidation, not test deletion. All **41 It bodies, names and
order** in the file are preserved. `verify-source.go` independently expands
the new helper calls back into the original assignment and success assertion,
removes the now-shared helper declaration, and compares the whole normalized
Go AST. Only redundant `var err error` declarations are normalized away.
It also verifies the lexical object bindings of `ctx` and `delegate`, so a
shadowed variable cannot silently change which context/delegate gets passed.
All other predicates, configuration values, worker selection and error-path/
concurrency code remain unchanged.

Evidence: `/tmp/brine-task-fixture.OYUw1D/`. The two valid controls run the same
**38 Container specs** successfully. Three temporary production overlays
challenge the constructor results and the shared success assertion:

| Production fault | Before | After |
|---|---:|---:|
| Reject creation of the configured-secrets container | 1 named assertion failure | Same 1 |
| Return no volume mounts | 4 named assertion failures | Same 4 |
| Construct returned volumes without executors | 3 named assertion failures | Same 3 |

All eight valid runs complete the same selected spec set. The report verifier
requires identical failing names, expected constructor/executor assertions,
passing suite hooks and the expected command exit status for each mutation.
No panic, timeout, compile failure or empty selection counts as fault detection.
`mutation-verification.log` lists the exact names; `calls.json` records the
eleven explicit call sites and workers.

The first attempted anchored focus selected zero cases and was rejected by
`--fail-on-empty`. It is retained as `after-control.log/json`, not counted as
a control. The valid runs use the unanchored `Container ` focus, because Ginkgo
matches against a string that includes the suite description. A first verifier
attempt also rejected duplicate blank names from BeforeSuite/AfterSuite nodes;
the corrected verifier requires those hooks to pass and compares actual It
reports separately. Both guards stopped before the workspace edit.

Reproduction uses the before snapshot with `prepare.go` and
`prepare-mutations.go`, then runs each generated overlay from the root:

`GOFLAGS=-overlay=<overlay>.json ginkgo --no-color --fail-on-empty --focus='Container ' --json-report=<report>.json ./atc/worker/jetbridge -- '-test.run=^TestJetbridge$'`.

`verify-source.go` and `verify-mutations.go` check the snapshots and reports.
`apply.go` refuses a changed workspace baseline before applying small diff
hunks. The current file matches `after.go` byte-for-byte; production
`worker.go` matches its pre-mutation snapshot. No production or shell-template
change was made. Existing per-row Go retention decisions remain valid because
their named tests and predicates were not removed or weakened.

Fresh final validation:

- Full root Ginkgo suite, including native Go tests: **85/85**,
  **27.052s** specs / **59.305s** command.
- Nested Go tests: pass, **8.135s**; vet: pass; all three vocabulary guards:
  pass, **0.473s**.
- Census: **184** scoped cases, **288** used / **1,088** total definitions,
  **17,050 raw / 12,520 counted** lines. Executor implementations remain **2**.

The changed Go file falls from **1,424 to 1,354 counted lines**, a **70-line**
reduction including the new helper. Overall reduction is now **905 (6.74%)**,
leaving **438** to reach **12,082**. Raw lines fall by 69 because of the added
explanatory comment; that comment earns no credit.

This pass changes only a root `_test.go` file, not the Brine program or
features. Rebuilding the normal adapter before/after yields the identical
SHA-256 `f237dad91fb9bb16eb7139b359ad5c9b1f019086011e86baef08cb52574d732a`;
the non-empty feature manifests also match (`brine-equivalence.log`).
Therefore the twenty-third-pass **578/578**, **171.898s** full CLI result and
**1,762/2,241 (78.625614%)** Brine-only production coverage remain applicable;
no fresh CLI or coverage run is claimed for this Go-test-only pass.

All validation jobs are terminal, the normal adapter is restored,
and `git diff --check` passes. The goal remains active: the size target,
intercept ownership exception and final requirement-by-requirement audit
remain unfinished.

The intercept exception is confirmed in scope: `container-run.feature` calls
`the operator intercepts the container ... and runs "exit 130"`, in addition
to the three callers in `worker.feature`. Its use of `clearSupervisorState`
is therefore not merely an unrelated worker-test cleanup opportunity.

## Twenty-fifth pass: intercept state uses the shared scenario owner (2026-09-08)

The executor-enabling Given and intercept When now declare the existing
`task-workspace` resource. The Given installs `localExecutor` with that
workspace's supervisor root and calls the unchanged `WorkerReady.rebuild`.
The When rejects a missing/empty workspace and requires successful execution
(including a non-zero exit status) to leave supervisor state in that workspace.
Actual execution errors retain their original message rather than being
replaced by the ownership assertion.

The host-global `clearSupervisorState` prefix sweep and its unused imports
are removed. Disposal belongs to the existing TaskWorkspace resource, whose
private `ownedDir` cannot be redirected by changing the public `Dir`.
No scenario, step pattern, Go test or shared executor implementation is added
or deleted. All four existing intercept cases and their data remain unchanged:
successful attachment, missing pod with a decoy, completed pod, and exit 130.

Evidence: `/tmp/brine-intercept-owner.GCUrGI/`. The old fixture was NOT run
against the real host-global prefix. For the comparison, its shared state
namespace and cleanup glob were translated into each variant's private
`legacy/supervisor` directory. The original sweep algorithm and generated
state basename are preserved. TaskWorkspace factories similarly allocate
inside each variant's `owners` directory; trace files record actual allocations
and mapped command paths. A deliberately unowned executor root is confined
to a private `unowned` directory too. No pre-existing host state was scanned
or removed by these comparison runs.

A sentinel with the same handle prefix represents another run's state.
The old sweep deletes it; the owned fixture preserves its exact contents.
For normal owned controls, both executed commands map into their own scenario
allocations and all four allocations are gone after disposal. Error-only
scenarios still dispose their allocation without fabricating command state.

| Check | Original fixture | Owned fixture |
|---|---|---|
| Control | 4/4 pass | 4/4 pass; all owners disposed; foreign sentinel intact |
| Lookup selects raw handle as pod name | All 4 named cases fail | Same 4 fail |
| execProcess swallows non-zero status | Exit-130 case fails | Same case fails |
| Executor loses its workspace root | — | Both executing cases fail the ownership check |
| Resource returns an empty workspace path | — | All 4 fail the explicit root guard |
| Disposer is disabled | — | CLI 4/4 passes, but filesystem observer detects all 4 surviving owners |

The existing `TestSupervisorStateIsOwnedByItsWorkspace` also passes normally
and rejects the disabled disposer with `owned workspace survived disposal`.
Its independent test cleanup reclaims its own allocations even under that
fault. The CLI's intentional no-disposer allocations remain confined to its
negative-evidence directory; they are not unowned host-global state.

Nine canonical CLI runs plus the two Go owner-test runs are checked by
`verify-results.go`. It requires non-empty scenario selection, exact failed
names and exit status, one distinct owner per case, expected mapped-command
counts, path containment, disposal outcomes and sentinel preservation.
`verify-source.go` verifies the two resource-aware definitions and compares
all remaining code after excluding only their ownership guards and the
removed cleanup helper: **34** retained functions/declarations are otherwise
AST-identical, including worker configuration and rebuild ordering, lookup/
Run/Wait handling, and all result predicates.

An exploratory `wrong-pod` overlay cleared metadata as well as changing the
destination; that also changed execution mode. Those two logs remain as
exploration, not the routing evidence above. The final
`wrong-destination` overlay changes only `container.podName` and leaves metadata
and execution mode intact.

Reproduce the isolated fixtures from the before snapshot with `prepare.go`;
build each adapter using its `overlay.json`, then run
`brine run test.feature --mode sync --format brief --no-engine` in its variant
directory with the provided wrapper. `prepare-go.go` creates the disposer-only
Go overlay. The source/result verifiers gate `apply.go`, which refuses any
workspace baseline mismatch before applying small diff hunks.

Nested Go tests pass (**7.393s**), vet passes, and all three vocabulary guards
pass (**0.545s**). The fixed census remains **184** scoped cases, **288** used /
**1,088** total definitions, two executors, and **17,050 raw / 12,520 counted**
lines. `worker.go` is outside the fixed baseline file list; its net deletion
earns **no size-reduction credit**, and no new helper declaration is introduced.
The remaining size target stays **438 counted lines**.
Full CLI validation passes **578/578 in 167.700s**, within the **177.354s**
runtime limit. Fresh final-source coverage passes **578/578** and covers
**1,762/2,241 production statements (78.625614%)**, exceeding the required
**40%**, without Go unit-test or fixture coverage. The retained profile is
`/tmp/brine-coverage.HEajr7/coverage.out`, SHA-256:
`1f727e1d8e2be624d9e62a69d4681791df099603911f0d08e3d9f6d97b1e941e`.
The coverage job completed successfully and restored the normal adapter.
The validated worker source matches the candidate snapshot; `git diff --check`
passes. No fresh root Go result is claimed for this Brine-only change; the
latest root run remains the twenty-fourth pass's **85/85**.
Intercept supervisor state is now scenario-owned. The overall goal remains
incomplete: **438 counted lines** of reduction and the final evidence audit
remain outstanding.

## Twenty-sixth pass: one retained mount table (2026-09-08)

The preceding turn made progress by completing the pending coverage job and
recording its final-source evidence. This pass consolidates four Go mount
contracts in `container_restored_test.go`, without claiming they have become
Brine-equivalent. Input, output, cache and scratch fixture variations now use
one `DescribeTable("ephemeral working-set mounts")` callback. Each entry keeps
its original leaf name, literal handle, command, ContainerSpec and expected
paths. Expected cardinalities are derived from those independent expected
paths, not from the pod under test. Multiline fixture literals stay multiline;
statement packing earns no credit.

The shared callback preserves volume count, mount count, path membership and
the three existing emptyDir checks. It additionally checks emptyDir for the
output case, whose old title claimed that behavior but whose body did not.
The separate scratch no-init case and its setup are unchanged; it still asserts
empty InitContainers, not absence of database cache rows. Overlap, input-prefix,
non-overlap and all other tests remain unchanged.

Evidence: `/tmp/brine-mount-table.gB4M0T/`. `prepare.go` checks each original
predicate against its complete expected shape; `verify-source.go` independently
checks candidate entries and the shared callback, preserves all **41** leaf
test identities, and proves all source outside the replaced nodes unchanged.
`verify-results.go` rejects empty/wrong selections, failed hooks, unexpected
statuses and any mismatch in exact failed test identities.

All twelve valid focused Ginkgo runs complete the same **five** cases:

| Isolated production fault | Before failures | After failures |
|---|---:|---:|
| None | 0 | 0 |
| Remove a returned pod volume | 4 | 4 |
| Add a duplicate main mount | 4 | 4 |
| Change a mount path, preserving counts | 4 | 4 |
| Replace emptyDir sources with hostPath | 3 | 4 |
| Add an unwanted scratch init container | 1 | 1 |

The additional storage failure is precisely the output entry. Mutation overlays
alter production `container.go` only in isolated builds; the workspace production
file is byte-identical to its pre-pass snapshot. Initial invocations used an
unsupported Ginkgo flag and ran no tests; their logs are excluded. Valid runs
use `GOFLAGS=-overlay=<overlay.json>` with `ginkgo --no-color --fail-on-empty`,
the focused leaf-name expression retained in each report, and
`-- '-test.run=^TestJetbridge$'`. `apply.go` reruns both verifiers and refuses
any workspace baseline mismatch before applying the verified candidate.

Final root Ginkgo passes **85/85**, **27.183s** of specs and **59.449s** command
time. Nested Go passes (**7.888s**), vet passes, and all three vocabulary guards
pass (**0.469s**). The census reports **184** scoped cases, **288** used /
**1,088** total definitions and **16,992 raw / 12,478 counted** lines. This pass
saves **42 counted lines** (58 raw), for **947 / 13,425 = 7.054%** total
reduction. **396 counted lines** remain to the fixed **12,082** target.

No Brine source or feature changed. Rebuilding the normal adapter yields the
same SHA-256 before/after:
`c07ad13ef7b36e2500c6b95266ee1cb2a6eb39bd985ac50f099641c5bea73a2c`.
The nonempty retained manifest verifies all **35** feature files unchanged.
Thus the twenty-fifth pass's **578/578**, **167.700s** normal CLI result and
**1,762/2,241 (78.625614%)** Brine-only production coverage remain applicable;
no fresh CLI/coverage run is claimed for this Go-test-only pass. All jobs are
terminal, the applied source matches the verified snapshot, and
`git diff --check` passes. The size criterion and final evidence audit remain
outstanding; the goal stays active.

## Twenty-seventh pass: shared status construction (2026-09-08)

The preceding turn made verified progress by applying the retained mount table.
This pass replaces **20** identical Kubernetes status wrappers with three small
constructors in the already-counted `steps/domain.go`: nine terminated, six
waiting and five running statuses. Callers still supply the complete terminated
or waiting state, so exit codes, reasons and messages remain explicit. Phases,
container ordering, status update timing and exceptional fields such as image,
restart count and previous termination remain unchanged. There is no new
scenario, step definition, mock executor or production edit.

Evidence: `/tmp/brine-status-fixture.MlhAY3/`. `prepare.go` selects only status
literals with exactly Name and State. `verify-source.go` checks each helper's
entire declaration, expands all calls back to their original literals, and
proves whole-file AST equivalence for `process.go`, `process_gaps.go`,
`observability.go`, `container_lifecycle.go` and `domain.go`. All helpers must
have consumers and the scan must be nonempty. This preserves the operations,
assertions and nominal Brine state transitions, not merely the passing verdicts.

`prepare-runs.go` creates isolated before/after adapters and unchanged copies of
the lifecycle and observability features. Each control and each mutation runs
all **41** expanded cases through `brine run --mode sync --format brief
--no-engine`. All ten runs are terminal:

| Isolated production mutation | Before failures | After failures |
|---|---:|---:|
| None | 0 | 0 |
| Force reported main exit codes to zero | 2 | 2 |
| Omit waiting-container diagnostic text | 6 | 6 |
| Classify failed init containers as completed | 1 | 1 |
| Rename the image-pulled event | 1 | 1 |

`verify-results.go` verifies completed counts, exact failed scenario names and
failed step positions, exit statuses and byte-identical feature inputs.
Production mutations live only in Go overlays; the workspace process source
and shell templates are untouched. `apply.go` reruns both verifiers, checks all
five workspace baselines before any edit, and verifies applied snapshot equality.
No Go test or feature case is deleted, and no historical disposition is changed.

Fresh nested Go passes (**8.102s**), vet passes, and the three vocabulary guards
pass (**0.513s**). The fixed census remains **184** cases, **288** used /
**1,088** total definitions and two executors. It now counts **16,960 raw /
12,441 non-comment, nonblank lines**, including the new constructors. This pass
saves **37 counted lines** (32 raw). Total reduction is **984 / 13,425
(7.330%)**; **359 counted lines** remain to the **12,082** target.
Fresh uninstrumented full CLI validation passes **578/578 in 168.465s**, within
the **177.354s** runtime limit. Fresh Brine-only coverage also passes **578/578**
and covers **1,762/2,241 production statements (78.625614%)**, above **40%**,
excluding Go unit tests and fixture code. Retained profile:
`/tmp/brine-coverage.4yujBv/coverage.out`; SHA-256:
`68916399c6142d1fcca30ee6915bc54ab2d71d34c0da118f71c4de3cd6717755`.
The coverage job completed and restored the normal adapter. All five applied
sources match their verified snapshots, production `process.go` is unchanged,
and `git diff --check` passes. All validation jobs are terminal.

The CI comment's stale hard-coded suite counts were removed; its executable
configuration is unchanged by this pass, and this documentation cleanup earns
no reduction credit. No fresh root Go result is claimed for this Brine-only
change: the root source and tests are unchanged since the twenty-sixth pass's
**85/85** result. The goal remains active with **359 counted lines** of required
reduction and the final requirement-by-requirement evidence audit outstanding.

## Twenty-eighth pass: shared artifact-read contract (2026-09-08)

The local-volume and remote-daemon read steps now share
`readArtifactFiles(ctx, runtime.Artifact, path)`. Both already requested gzip,
decoded the same tar members and returned the same `VolumeRead` error/message
state. The helper keeps those operations, both error branches and the deferred
reader close; setup errors remain fatal at each caller. It is deliberately
separate from `worker.go`'s existing `readArtifact`, which observes opaque,
uncompressed bytes rather than parsed gzip files.

Live and refused remote endpoints now share `volumeAtAddress`: the same node
and EndpointSlice setup, configured daemon port, node resolver, production
volume constructor and optional peer discovery. The forgotten-node branch
remains separate and unchanged. Live server shutdown and real refused-port
reservation/closure still belong to their original branches. The one-address
mirror fixture's limitations have not changed.

Evidence is retained in `/tmp/brine-artifact-read.3WEKJa/`:

- `before.go` and `after.go` preserve the exact source snapshots.
  `verify-source.go` expands the actual new helper bodies into their call
  sites and compares every Go token against the original file. It verifies
  all paths, constructor arguments, gzip requests, branches and deferred
  cleanup, not just function names. Unrelated source is token-identical.
- `prepare-runs.go` creates isolated adapter overlays and exact copies of
  `volume-streaming.feature`. All **24** expanded cases are retained, including
  the five returned-volume handoff rows. No feature, step pattern, scenario,
  assertion or restored Go test was deleted or altered.
- Both controls pass **24/24**. Four production-only overlay faults fail the
  same cases at the same step positions before and after:
  **local-gzip: 9**, **remote-gzip: 4**, **member-path: 1**,
  **peer-discovery: 3**. `verify-results.go` validates exact scenario names,
  expected failure counts, complete-suite totals, statuses and feature bytes;
  `verification.log` retains its successful output.
- The local gzip fault distinguishes parsing from accepting opaque bytes.
  The daemon gzip fault also challenges the gzip and S2 handoff rows.
  The wrong-member fault singles out the subdirectory-selector case.
  Disabled peer discovery breaks the mirror-success, exhausted-search and
  producer-offline-handoff cases. Successful-file checks are not substituted
  for the retained raw-reader or peer-request-count contracts.
- `apply.go` gates source equivalence and the current baseline, applies the
  diff with `apply_patch`, and verifies byte equality with the candidate.
  The initial isolated build found a helper-name collision with the raw-byte
  reader; the candidate was renamed before any workspace source was changed.
  That failed build is not counted as validation.
- Final nested-module Go tests pass (**7.339s**), `go vet ./...` passes, and all
  three vocabulary guards pass (**0.579s**). Production `volume.go` and
  `volume_daemonset.go` are byte-identical to their snapshots: faults never
  modified workspace production code. No shell template changed.

The fresh on-disk census reports **184** scoped cases, **288** used definitions,
**1,088** total definitions, **2** executor implementations, **16,947 raw /
12,428 counted lines**. Both helpers are in the fixed counted scope.
This pass saves **13 counted lines** (13 raw), without comment or relocation
credit. Total reduction is **997 / 13,425 (7.426%)**; **346 counted lines**
remain to the **12,082** target. A preliminary census run with a compiler
overlay read the old source through `os.ReadFile` and is not used as the
candidate's size evidence; `census-final.json` was measured after application.

Fresh full normal CLI validation passes **578/578 in 169.417s wall**,
**14.63%** above baseline and within the **177.354s** limit. All comparison,
Go-test and compilation jobs were terminal before the timed run started.
Evidence: `brine-final.log`. Fresh `sh scripts/coverage` also passes **578/578**
and covers **1,762 / 2,241 production statements (78.625614%)**, above the
required **40%**, excluding Go unit-test and fixture coverage. Profile:
`/tmp/brine-coverage.ZLA7Lw/coverage.out`; SHA-256:
`32e53f20ab0cec6a4fa8442b6292dc465a452717400410a936d6c816861dd5f3`.
The coverage job completed and restored the normal adapter to its pre-coverage
SHA-256 `b64826127b486a899a7b7b6043cb4bee269e401a484f46304330a60691a62f07`.
The applied source still matches the verified candidate, and `git diff --check`
passes. All validation jobs are terminal.

No fresh root Go result is claimed for this Brine-only change: root production
and test source remain unchanged since the twenty-sixth pass's **85/85** result.
The goal remains active: **346 counted lines** of required reduction and the
final requirement-by-requirement evidence audit are still outstanding.

## Twenty-ninth pass: validated custom checks (2026-09-08)

Thirty custom predicates now use `Assert[T]`, a check-mode counterpart to
`Transform` that delegates capture validation to the existing `applyAction`.
It introduces no new capture parser or state transition. The caller still owns
the complete predicate and diagnostics: float-counter equality, subset checks,
exact occurrence counts, case-insensitive errors, database identity and trace
event checks are not coerced into generic equality assertions.

All **46** capture reads retain their original index and type. Missing or
overflowing captures are rejected before invoking the predicate; an undeclared
read is rejected afterwards. This also closes the earlier unchecked-integer
holes in multi-counter checks, whose first captures discarded `GetInt`'s
success flag. Valid scenario inputs and all original predicates are unchanged.
Four new public-dispatch regression rows cover custom-predicate failure, integer
overflow in the first and last capture, and an undeclared read. They reuse the
existing check-contract test runner; no existing test case was removed.

Evidence is retained in `/tmp/brine-custom-checks.L1hyoZ/`:

- `changes.json` names every converted pattern, input type and capture.
  `before/` and `after/` contain all twelve edited source snapshots.
  `verify-source.go` independently derives capture replacements from the
  original AST and proves every predicate, diagnostic, pattern, nominal input
  state and unrelated consumer-file token unchanged. Normalizing out only the
  new test declarations and rows reconstructs the previous test AST.
- `prepare-runs.go` resolves affected features through the actual registry,
  verifies every changed pattern is used, and preserves exact copies of all
  **nine** affected feature files, with **211** expanded scenarios.
  Both controls pass **211/211**.
- Four production-only overlay faults preserve their exact failed scenarios
  and failed-step positions before/after: **image-counter: 2**,
  **init-span: 1**, **sidecar-mount: 1**, **image-secrets: 3**.
  Every fault fails at least one converted custom check, rather than merely
  breaking upstream setup. `verify-results.go` checks every completed scenario
  identity, verdict, failed step, changed-pattern involvement and feature byte.
  `verification.log` retains the successful comparison.
- The new check-contract rows are themselves challenged by three wrapper-only
  faults. Across all **16** contract rows, swallowing errors produces exactly
  **4** failures, skipping capture validation **3**, and losing input state
  **1**. The control passes all rows. `verify-helper-faults.go` checks the exact
  identities and package/parent verdicts; `helper-verification.log` retains it.
- `apply.go` requires source equivalence, wrapper negative controls and all
  current baseline snapshots before applying a single `apply_patch`. Every
  applied file is then compared byte-for-byte with its candidate. Final nested
  Go tests pass (**7.448s**), `go vet ./...` passes, and all three vocabulary
  guards pass (**0.473s**). No production source, shell template or feature
  changed. Preparation-only printer/signature and patch-size errors were fixed
  before application and are not counted as behavioral validation.

### Retention-name audit

A Ginkgo **dry run**, with native Go test execution excluded by
`-test.run=^TestJetbridge$`, records the **85** current Ginkgo identities in
`retained-specs.json`. Native test identities are read from the seven scoped
restored Go files' ASTs. This is an inventory, not a fresh root test pass.

The first inventory could not resolve nine rows: three used differently
capitalized retention notes, and six had abbreviated or pre-table test names.
The parser now recognizes the existing note forms, and six disposition rows
explicitly name the current tests. The mount table still matches its
twenty-sixth-pass verified source snapshot. The scheduling-error and SC-07
bodies were inspected directly; the former retains both error phrases and
stderr clues, while the latter still only proves that some GetLogs request
occurred. Its weak sidecar-routing observation is not claimed fixed.

`retention-inventory-after.json` resolves **all 81 GAP/REFUTED rows** to current
test identities and recorded retention notes, with no missing names.
This does **not** by itself complete the final behavioral/deletion-evidence
audit. No historical verdict, retained Go test or replacement claim changes.

The fresh on-disk census reports **184** scoped cases, **288** used definitions,
**1,088** total registered definitions, **2** executor implementations, and
**16,921 raw / 12,369 counted lines**, including the new wrapper and regression
rows. This pass saves **59 counted lines** (26 raw); no comment, blank-line,
relocation or statement-packing credit is claimed. Total reduction is
**1,056 / 13,425 (7.866%)**; **287 counted lines** remain to the **12,082** target.

Fresh full normal CLI validation passes **578/578 in 168.919s wall**,
**14.29%** above baseline and within the **177.354s** limit. All comparison,
Go-test, identity-inventory and compilation jobs were terminal before the
timed run began. Evidence: `brine-final.log`. Fresh `sh scripts/coverage`
also passes **578/578**, covering **1,762 / 2,241 production statements
(78.625614%)**, above the required **40%**, excluding Go unit-test and
fixture coverage. Retained profile: `/tmp/brine-coverage.mmDSKP/coverage.out`;
SHA-256: `1f0cbc27fb83a1203d0e1e4895583558a61bc2fe80d6afd17d2cfbb34b751e64`.
The coverage job completed and restored the normal adapter to its pre-coverage
SHA-256 `b9a07ac185b0190f7ef25ca7fa29d14be8876de4e6a07f771d1c5048ca2f233e`.
All applied source files still match the candidate snapshots, both mutated
production files match their originals, and `git diff --check` passes.
All validation jobs are terminal.

No fresh root Go execution result is claimed: the identity inventory was a
dry run, and root source/tests are unchanged since the twenty-sixth pass's
**85/85** execution result. The goal remains active with **287 counted lines**
of reduction and the final requirement-by-requirement evidence audit remaining.
A read-only AST scan found **25** single-case Context/Describe blocks with
their own BeforeEach hook in the retained Go scope
(`single-case-fixtures.json`). These are candidates, not proven-safe rewrites:
scope, hook order, full test identity and distinguishing faults still need
verification before folding setup into the sole case.

## Thirtieth pass: single-case fixture ownership (2026-09-08)

The preceding goal-update turn confirmed the coverage criterion without
implementation progress. This pass folds **25** single-case Context/Describe
fixtures into their sole It across the retained container, process and volume
tests. Setup is now adjacent to the assertions it serves. The original context
and leaf titles are joined, preserving every full Ginkgo test identity.

All variable declarations, setup statements, assertions, diagnostics and comments
are preserved. **17** setups need no separate local scope; **8** retain a block
for setup-local declarations. There are no intervening JustBeforeEach hooks in
the compiled root test package. The conversion excludes decorators, additional
lifecycle nodes, initialized context declarations, and hook-level return,
defer or cleanup operations. Parent setup and cleanup remain unchanged.
No Go test case was removed; no feature, production code or shell template changed.

Evidence is retained in `/tmp/brine-single-fixtures.Xv1F9w/`:

- `before/`, `after/` and `rows.json` record every changed fixture.
  `verify-source.go` checks each ordered setup/test statement and declaration,
  restores the old AST nodes to prove unrelated code unchanged, and compares
  all comments. `source-verification.log` records success.
- Before/after full root controls pass **85/85**. `verify-results.go` verifies
  every full test identity, including execution of all 25 changed fixtures.
  Four isolated production faults retain identical failed identities and
  assertion diagnostics: missing image secrets (**2**), nonzero cancellation
  grace (**1**), swallowed StreamOut error (**1**) and unwanted scratch-cache
  initialization (**1**). `results-verification.log` records the comparison.
- `apply.go` requires both verifiers, unchanged production snapshots and all
  current test baselines before applying the three files byte-for-byte.
  A final normal root Ginkgo run passes **85/85**, **26.902s** in specs /
  **59.239s** command time (`root-final.log` and `root-final-report.json`).
  Nested Go tests pass (**7.415s**), vet passes, and the three vocabulary guards
  pass (**0.473s**). All validation jobs are terminal.
- `retention-inventory-final.json` resolves all **81 GAP/REFUTED rows** against
  the executed report and current native Go test declarations. This remains
  a name/retention inventory, not a completed behavioral/deletion audit.
  Existing disposition names and claims need no change.

The on-disk census is **184** scoped cases, **288** used / **1,088** registered
definitions, **2** executor implementations, and **16,871 raw / 12,285 counted
lines**. This pass saves **84 counted lines** (50 raw), with no new helpers,
statement packing, relocation or comment-only credit. Total reduction is
**1,140 / 13,425 (8.492%)**; **203 counted lines** remain to the **12,082** target.

The rebuilt Brine adapter is byte-identical to the normal adapter validated in
the twenty-ninth pass: SHA-256
`b9a07ac185b0190f7ef25ca7fa29d14be8876de4e6a07f771d1c5048ca2f233e`
(`adapter-equivalence.sha256`). No new CLI or coverage run is claimed for this
Go-test-only change. The latest recorded full CLI result remains **578/578 in
168.919s**, and Brine-only production coverage **1,762/2,241 (78.625614%)**,
above the required **40%**. `git diff --check` passes. The goal remains active:
the remaining size reduction and final requirement-by-requirement evidence
audit are unfinished.

## Thirty-first pass: remove behavior-free test wrappers (2026-09-08)

The preceding turn made verified implementation progress. This pass first
checked remaining Brine duplication against the actual registry:
`/tmp/brine-scenario-groups.sLW7KM/scan.go` groups expanded scenarios by their
ordered registered step patterns, with a nonempty-scan guard.
`groups.json` shows that the repeated chains are already outlines except for
two volume-streaming families: three artifact-read cases and two overlap cases.
These are candidates for later data-only consolidation, not deleted coverage.
Similar-looking task/check/get spec projections still have intentional storage,
input-order and field differences; they were not flattened together.

Ten retained Go wrappers contain only one It and no variables, hooks,
decorators or other statements. Those wrappers are removed across
`container_restored_test.go`, `process_restored_test.go`,
`volume_restored_test.go`, `integration_restored_test.go` and
`behavioral_runtime_spec_restored_test.go`. Context and leaf titles are joined,
preserving the full test names. Complete test bodies and comments are unchanged.
No test case, Brine scenario, production source or shell template is removed.

Evidence is retained in `/tmp/brine-scenario-groups.sLW7KM/`:

- `before/`, `after/` and `rows.json` identify all ten wrappers.
  `verify-source.go` compares every complete test body and comment, rejects
  wrappers with other behavior, and restores the old nodes to verify all
  unrelated source. `source-verification.log` records the result.
- Full root controls pass **85/85** before and after. The after run takes
  **26.951s** in specs / **59.807s** command time. All ten changed tests execute
  and every full test identity is preserved.
- Five isolated production faults preserve the same failed identities and
  assertion diagnostics: missing directory volume (**1**), wrong direct
  command (**1**), nonzero cancellation grace (**1**), broken volume handle
  (**2**), and missing scheduling-timeout wording (**1**).
  `verify-results.go` checks exact selections, completed verdicts, failure
  nodes, diagnostics and exit statuses; `results-verification.log` records
  success. The scheduling fault covers all matching timeout-message branches;
  its initially over-narrow preparation anchor was corrected before any
  workspace edit and is not claimed as a behavioral test.
- `apply.go` requires both verifiers and unchanged source baselines before
  applying all five files byte-for-byte. Fresh nested Go tests pass
  (**7.937s**), vet passes, and all vocabulary guards pass (**0.469s**).
  `retention-inventory-final.json` resolves all **81 GAP/REFUTED rows** to
  current test names and retention notes. This is still only an inventory,
  not completion of the behavioral/deletion audit.

The fresh census is **184** scoped cases, **288** used / **1,088** registered
definitions, **2** executor implementations, and **16,871 raw / 12,265 counted
lines**. Removing the wrappers saves **20 counted lines**; raw line count is
unchanged because the source retains separator whitespace. No blank-line,
comment-only, relocation or statement-packing credit is claimed.
Total reduction is **1,160 / 13,425 (8.641%)**; **183 counted lines** remain.

The rebuilt adapter remains byte-identical to the twenty-ninth-pass normal
binary (SHA-256
`b9a07ac185b0190f7ef25ca7fa29d14be8876de4e6a07f771d1c5048ca2f233e`,
`adapter-equivalence.sha256`). No new CLI/coverage execution is claimed for
this Go-test-only change: latest recorded results remain **578/578 in
168.919s** and **1,762/2,241 (78.625614%)** Brine-only production coverage,
above the required **40%**. All validation jobs are terminal and
`git diff --check` passes. The goal remains active: size reduction and the
final requirement-by-requirement evidence audit are incomplete.

## Thirty-second pass: one exact-pod lookup (2026-09-08)

The preceding turn made verified implementation progress. This pass reuses
`restoredPod` for **eight** retained container/integration lookups, with an
explicit namespace argument. Existing callers still pass `test-namespace`;
integration callers pass their original `ci-namespace`. The helper retains the
same client/context, List options, error assertion, exact-one cardinality
assertion and first-pod value. Two of those cases also reuse `restoredRunPod`
with their original `/bin/sh -c "echo hello"` command and empty ProcessIO.

Cancellation checks expecting no pods and the concurrent check expecting
several pods are intentionally untouched. Process waits, identity, resources,
image secrets, commands, input mounts and sidecar assertions remain in their
individual tests. No test cases, Brine scenarios or production behavior change.

Evidence is retained in `/tmp/brine-pod-lookup.TP9dL8/`:

- `before/` and `after/` hold both edited files. `rows.json` records the eight
  lookup sites; `cases.json` adds their test identities. `verify-source.go`
  validates every query and replacement against the original statements,
  including namespace, context, client, List options, both error/cardinality
  assertions and the two exact Run specifications. It reconstructs the
  candidate and checks the entire files, preserving unrelated source.
- The original full root control passes **85/85**. A preparation substitution
  accidentally produced an invalid candidate binding; the compiler rejected
  it before any workspace edit. The terminal failed build is preserved in
  `after-compile-failure.log`. After correction, the full candidate control
  passes **85/85**, **26.909s** in specs / **59.708s** command time.
- Five production-only overlay faults fail the same named test with the same
  assertion diagnostic before/after: missing CPU request, missing registry
  secret, wrong main command, missing input mount and missing sidecar mounts.
  `verify-results.go` checks the full controls' identities, execution of all
  eight changed lookup cases, and exact fault selections, verdicts, failure
  nodes, diagnostics and exit statuses. `source-verification.log` and
  `results-verification.log` retain successful results.
- `apply.go` requires the source/result checks and unchanged baselines before
  applying both files byte-for-byte. Fresh nested Go tests pass (**8.134s**),
  vet passes, and all vocabulary guards pass (**0.463s**). The retention-name
  inventory resolves all **81 GAP/REFUTED rows** against current sources and
  the executed candidate report. This remains an inventory, not completion
  of the final behavioral/deletion audit.

The on-disk census is **184** scoped cases, **288** used / **1,088** registered
definitions, **2** executor implementations and **16,838 raw / 12,236 counted
lines**. This pass saves **29 counted lines** (33 raw), without a new helper,
statement packing, relocation or comment-only credit. Total reduction is
**1,189 / 13,425 (8.857%)**; **154 counted lines** remain to the **12,082** target.

The rebuilt Brine adapter remains byte-identical to the twenty-ninth-pass
normal binary, SHA-256
`b9a07ac185b0190f7ef25ca7fa29d14be8876de4e6a07f771d1c5048ca2f233e`
(`adapter-equivalence.sha256`). No new CLI/coverage execution is claimed for
this Go-test-only change; latest recorded results remain **578/578 in
168.919s** and **1,762/2,241 (78.625614%)** Brine-only production coverage,
above the required **40%**. All validation jobs are terminal and
`git diff --check` passes. The goal remains active: the remaining size
reduction and final requirement-by-requirement audit are unfinished.

## Thirty-third pass: shared error snapshots and status updates (2026-09-08)

The pending isolated comparisons were confirmed live and completed before any
candidate was applied. This pass removes one unused private helper,
`runExtraMarkRunning`, delegates `StepRunning.mutatePod` to the existing
`updateTaskPodStatus`, and shares six identical nil-safe error-message snapshots
through `errorMessage`. Each snapshot remains at its original evaluation point.
All eight changed files, including the new helper in `domain.go`, are already
inside the fixed census. No test case, feature, production source or shell
template changes in this pass.

Evidence is retained in `/tmp/brine-unused-scaffold.UJTCUS/`:

- `repository-consumers-before.log` records that the removed private helper had
  only its declaration as a repository reference. `before/` and `after/` retain
  all eight source snapshots. `verify-source.go` expands the six snapshots and
  the delegated updater back to their original operations, validates the new
  helper body, restores the dead declaration, and compares all unrelated Go
  tokens. Its successful result is in `source-verification.log`.
- The unchanged-feature manifest contains **204 expanded scenarios across ten
  features**, selected by registered patterns in the affected source files.
  Both controls pass **204/204**. Three isolated production diagnostic faults
  preserve exact completed identities, failing identities and step positions:
  missing OOM reason (**4 failures**), missing scheduling reason (**1**), and
  missing init-container detail (**1**). `verify-results.go` also checks feature
  bytes, exit statuses and that failures reach affected registered checks;
  `results-verification.log` records all eight verified runs.
- `apply.go` requires both verifiers and unchanged source baselines, including
  production `process.go` and the existing status updater, before applying all
  eight files byte-for-byte. `apply.log` records success. No old test was deleted.
- Fresh nested Go tests pass (**8.178s**), vet passes, and all three vocabulary
  guards pass (**0.560s**). The full root Jetbridge run through Ginkgo passes
  **85/85**, **26.971s** in specs / **59.574s** command time. The fresh root
  report and source inventory resolve all **81 GAP/REFUTED rows** to current
  retained test names and notes. This remains an inventory, not completion of
  the final behavioral/deletion audit.
- A fresh normal full CLI run passes **578/578 in 168.791s**, **14.21%** above
  the original 147.795s baseline and below the 177.354s limit. Root tests and
  builds finished before this timing run; no other test/build job competed.
- Fresh `sh scripts/coverage` passes **578/578** and reports **1,762/2,241
  production statements (78.625614%)**, above the required **40%**. The retained
  profile is `/tmp/brine-coverage.54kKqM/coverage.out`, SHA-256
  `68916399c6142d1fcca30ee6915bc54ab2d71d34c0da118f71c4de3cd6717755`.
  An independent profile sum confirms the same numerator/denominator and zero
  Brine fixture entries; no Go unit-test coverage was combined with it.
  The script restores the normal adapter byte-for-byte, SHA-256
  `95cf27f9272a3b9989a0db4a9fc04db25dd9089d0a3e9454174ea829cb5eb93c`.

The on-disk census is **184** scoped cases, **288** used / **1,088** registered
definitions, **2** executor implementations and **16,804 raw / 12,200 counted
lines**. This pass saves **36 counted lines** (34 raw), including the cost of
the new helper. No comment-only, relocation or statement-packing credit is
claimed. Total reduction is **1,225 / 13,425 (9.125%)**; **118 counted lines**
remain to the **12,082** target. All validation jobs are terminal and
`git diff --check` passes. The goal remains active: the 10% size target and
final requirement-by-requirement evidence audit are not yet complete.

## Thirty-fourth pass: one-shot pod updates and typed captures (2026-09-08)

The preceding goal turn made verified implementation progress. This pass
reuses the existing `updateTaskPodStatus` for **seven** one-shot fixtures and
the existing `Transform`/`Args` path for **three** callbacks with five string
captures. It introduces no helper, deletes no test or scenario, and changes
no production source or shell template. All seven edited files are already
inside the fixed census.

Each pod mutation keeps its context, client, namespace, handle, API options,
callback statements, error-result type and position before Wait. Multi-update
and asynchronous fixtures are intentionally left alone. Setup errors still
wrap their causes: five previously unnamed Get errors now include the pod
handle, and four update errors use the shared “update pod status” wording.
These are fixture setup messages, not production diagnostics or assertions.
Valid capture behavior is preserved; malformed captures use the existing
central validation rather than repeated callback guards.

Evidence is retained in `/tmp/brine-one-shot-status.BQL7MC/`:

- `before/`, `after/`, `status-sites.json` and `captures.json` retain the exact
  candidates. `verify-source.go` independently checks the updater body, all
  seven callback bodies/API arguments/error returns, three converted capture
  callbacks, allowed import removals, and every unrelated source token.
  `source-verification.log` records success.
- Both unchanged-feature controls pass **197/197** across nine feature files.
  Focused before/after production-only overlays preserve exact scenario
  identities, failures, failing step positions and exit statuses: missing OOM
  reason (**4/50 fail**), missing scheduling reason (**1/18**), missing init
  detail (**1/9**), false TTY flag (**1/17**), and duplicate scheduled events
  (**1/9**). Per-run manifests retain the selected unchanged feature bytes.
  `results-verification.log` records all twelve verified runs.
- `apply.go` requires both verifiers and unchanged baselines before applying
  all seven files byte-for-byte. Fresh nested Go tests pass (**8.818s**), vet
  passes, and all three vocabulary guards pass (**0.468s**). The fresh root
  Ginkgo run passes **85/85**, **26.990s** in specs / **59.607s** command time.
- The current source/executed-report inventory again resolves all **81
  GAP/REFUTED rows** to named retained tests and notes. Reviewing those notes
  and their source confirms existing limitations are still explicit, including
  the sidecar test's any-GetLogs observation and the overlap-name test's
  conditional mount scan. Neither limitation is claimed closed by this pass;
  the final substantive retention/deletion audit remains open.
- The fresh normal full CLI passes **578/578 in 169.076s**, **14.40%** above
  the 147.795s baseline, below the **177.354s** limit. All other test/build
  jobs finished before this timing run.
- Fresh `sh scripts/coverage` passes **578/578** with **1,762/2,241 production
  statements (78.625614%)**, above the required **40%**. The profile is
  `/tmp/brine-coverage.Cknv4l/coverage.out`, SHA-256
  `f3b36ba4a29d00bfcf12e199db4c0a4bdeceeff95015b435b40de4fc274b690f`.
  An independent profile sum confirms the same totals and zero fixture
  entries; Go unit-test coverage is not included. The normal adapter is
  restored byte-for-byte, SHA-256
  `75bbde53d387eff2f2d102a05fe86a7c6c83ab445a05c17044f046b193d15f84`.

The fresh census is **184** scoped cases, **288** used / **1,088** registered
definitions, **2** executor implementations and **16,756 raw / 12,153 counted
lines**. This pass saves **47 counted lines** (48 raw), with no comment-only,
relocation or statement-packing credit. Total reduction is **1,272 / 13,425
(9.475%)**; **71 counted lines** remain to the **12,082** target. All validation
jobs are terminal and `git diff --check` passes. The goal remains active:
the 10% reduction and final requirement-by-requirement audit are incomplete.

## Thirty-fifth pass: comparisons through public checks (2026-09-08)

The preceding goal turn made verified implementation progress. This pass
removes **six private comparison wrappers**: contains, integer, keyed string,
keyed contains, keyed integer and count. Their public checks now use the
existing `Assert`/`Args` path directly; positive and negative membership share
one definition-producing helper. The raw `stringCheck` and `thatCheck` handlers
remain for the Go error-identity contract, which Brine's serialized events
cannot express.

Two existing tests now drive the public check definitions through the existing
`runCheck` pipeline helper: failure details and full-value comparison despite
abbreviation. All their assertions remain, including explicit rejection of an
unexpectedly successful empty-detail check where the old test would panic on
a nil error. The 16-row comparison contract table and raw error-identity tests
are unchanged. No tests or scenarios are deleted, no new helper is introduced,
and no production source or shell template changes.

Evidence is retained in `/tmp/brine-public-checks.GiFrd5/`:

- `before/` and `after/` hold both changed files. `verify-source.go` compares
  all seven valid-capture comparison bodies, getter-error returns, predicates
  and diagnostics; checks membership polarity and public signatures; and
  verifies the preserved test conditions, diagnostics, probe values and setup.
  All unrelated source, including raw error-identity tests, remains unchanged.
  `source-verification.log` records success.
- Both focused Go controls pass all **16 contract rows and four standalone
  tests** (21 Go test/subtest verdicts including the table's parent).
  Twelve paired faults preserve exact failed identities and assertion
  diagnostics: six comparison/count predicates, membership, getter errors,
  truncating before comparison, swapped keys, missing details and empty
  parentheses. The first membership mutation failed to compile because it
  removed the sole read of a local variable; those two failed-build logs are
  retained but excluded. The corrected `member-pass-valid` runs fail the two
  actual membership contracts. `verify-unit-results.go` rejects build failures,
  skipped/missing tests and mismatched failure sets. Its successful log covers
  **26 valid runs**, not the discarded builds.
- Both unchanged-feature CLI controls pass **94/94**. Removing production pod
  volumes fails the same **17/44** scenarios, including eleven failures at
  affected checks; omitting the production OOM reason fails the same **4/50**.
  Exact completed/failing identities, step positions, feature bytes and exit
  statuses match. `results-verification.log` records all six CLI comparisons.
- `apply.go` requires all three verifiers and unchanged baselines before
  applying both files byte-for-byte. Fresh nested Go tests pass (**8.681s**),
  vet passes, and all three vocabulary guards pass (**0.450s**). The fresh
  root Ginkgo suite passes **85/85**, **26.983s** in specs / **59.567s** command
  time. The current-source/executed-report inventory resolves all **81
  GAP/REFUTED rows** to retained names and notes; it is not a substitute for
  the final substantive retention/deletion audit.
- The fresh normal full CLI passes **578/578 in 169.964s**, **15.00%** above
  the 147.795s baseline and below **177.354s**. No other test/build job competed
  with this timing run.
- Fresh `sh scripts/coverage` passes **578/578**, covering **1,762/2,241
  production statements (78.625614%)**, above the required **40%**. The profile
  is `/tmp/brine-coverage.BmvO5b/coverage.out`, SHA-256
  `06596b665cd6aae147287cad8926c4153f641d8684a1e5ed8b4a72762bea1938`.
  An independent sum confirms the same totals and zero fixture entries;
  Go unit-test coverage is not included. The normal adapter is restored
  byte-for-byte, SHA-256
  `7a751f47d06d8eb3bea1336c5a0a4389d9e34398b2b05dad5c5a22d89cd4116f`.

The fresh census is **184** scoped cases, **288** used / **1,088** registered
definitions, **2** executor implementations and **16,705 raw / 12,109 counted
lines**. This pass saves **44 counted lines** (51 raw), without comment-only,
relocation or statement-packing credit. Total reduction is **1,316 / 13,425
(9.803%)**; **27 counted lines** remain to the **12,082** target. All validation
jobs are terminal and `git diff --check` passes. The goal remains active:
size reduction and the final requirement-by-requirement audit are incomplete.

## Thirty-sixth pass: exact mount layouts (2026-09-08)

The preceding goal-update turn only confirmed the already recorded 40%
criterion; it made no implementation progress. This pass revalidated the
worktree on `core`, then consolidated repeated assertions in
`behavioral_permutations_restored_test.go`. Production source is unchanged.

Eight native tests now use `assertMountLayout` for their independently supplied
volume count, identical expected mount count, and contiguous path assertions.
The helper checks volumes first, mounts second, then the explicit paths, in the
original order. Five mount-to-volume-to-hostPath chains in MixedOverlap and
AllOverlapping use `assertHostPathMount`. MixedOverlap's later standalone data
path check remains in its original position. RelativeScratchPath still checks
mounts only; it was not changed to fit the helper.

All fourteen original test identities remain. No fixture, expected count,
path, storage suffix, nil check, or unrelated assertion was removed. New
helpers are in the already-counted file. Comment movement earns no credit.

### Equivalence and distinguishing faults

Evidence: `/tmp/brine-mount-layout.7rhlnY/`.

- `prepare.go` builds a guarded candidate from the exact pre-change snapshot.
- `audit.go verify` independently expands all eight layout calls and five
  hostPath calls, checks the helper bodies, and compares the entire resulting
  file's non-comment tokens with the original. All test setup, literal
  expectations, assertion ordering, and unrelated declarations match.
  Evidence: `source-verification.log`.
- `audit.go run` executes all fourteen native tests against both snapshots,
  with a clean control and nine separate production-only `container.go`
  overlays. These are non-database native tests; the Ginkgo suite is not
  selected by this focused Go command.
- `audit.go results` requires complete, unskipped test verdicts, valid command
  exits, identical failed test identities, and identical assertion diagnostics
  apart from source line numbers. Builds and empty runs cannot count as fault
  detection. All **20 runs** are valid; both controls pass **14/14**.
- `apply.go` re-runs the verifiers, checks the worktree against both original
  snapshots, and applies the candidate byte-identically through apply_patch.
  The production worktree remains unchanged.

| Production fault | Failed tests before / after |
|---|---:|
| Missing volume | 8 / 8 |
| Extra volume | 8 / 8 |
| Missing mount | 10 / 10 |
| Extra mount | 9 / 9 |
| Wrong workspace path | 6 / 6 |
| Mount refers to a missing volume name | 2 / 2 |
| Wrong hostPath directory | 3 / 3 |
| hostPath replaced by emptyDir | 3 / 3 |
| Wrong overlapping output subdirectory | 2 / 2 |

All **51 test/fault detections** are preserved. Exact failed identities and
diagnostics are in `mutation-results.log` and the per-variant JSONL logs.
These tests retain their documented exact-layout/storage obligations; the
new shared assertions are not a claim that Brine replaces them.

### Fresh final gates

`gates.sh` records the exact sequential commands. No competing test/build job
ran during the normal timed CLI execution.

- Nested `go test ./... -count=1` passes, steps package **7.098s**; `go vet
  ./...` passes. The three vocabulary guards pass in **0.454s**.
- Root `ginkgo --no-color --json-report=... ./atc/worker/jetbridge` passes
  **85/85** specs, **26.926s** in specs and **59.333637378s** for the command,
  which also runs the native Go tests.
- The fresh normal full CLI passes **578/578 in 169.203s**, **14.484928%**
  above the 147.795s baseline and below the **177.354s** ceiling.
- Fresh `sh scripts/coverage` passes **578/578**, covering **1,762/2,241
  production statements (78.625614%)**, above the required **40%**.
  Profile: `/tmp/brine-coverage.hatWqo/coverage.out`; SHA-256:
  `b6d77d686d000dde2935f98889411bfe88bf12b9226276fddca0df478ed2970b`.
  An independent sum confirms the denominator, numerator and zero fixture
  entries. Go unit-test coverage is not included.
- The normal adapter is restored byte-identically, SHA-256
  `7a751f47d06d8eb3bea1336c5a0a4389d9e34398b2b05dad5c5a22d89cd4116f`.
  `scoped-source.sha256` fingerprints every file in the final fixed census.

The fresh census is **184** scoped cases, **288** used / **1,088** registered
definitions, **2** executor implementations and **16,662 raw / 12,079 counted
lines**. This pass saves **30 counted lines** (43 raw). Total reduction is
**1,346 / 13,425 (10.026071%)**, meeting the **12,082** ceiling without
relocation, comment-only reductions, or statement packing.

### Completion audit progress and remaining work

Current source inspection confirms that `localExecutor` is the sole ordinary
command implementation. The second `ExecInPod`, `severingExecutor`, drains
stdin and injects a named connection/pod failure; it does not run a second
ordinary command implementation. Local process groups, PTY handling, error
translation, owned supervisor state and their Go contract tests were inspected.
Explicit legacy pod-lifecycle rows still name their distinct Process.Wait
compatibility need. Fake Kubernetes and focused protocol-observation Go tests
remain; this is not repository-wide mock removal.

The current returned-volume handoff, five encoding/fault rows, independent
encoding check, exact-file checkpoints, pod mount-admissibility check, and
four real-cancellation rows were inspected. Running cancellation waits for the
actual child's PID before cancelling, then checks child termination and task
pod removal. Historical distinguishing destination/executor/transfer/cleanup
mutations are indexed in the second and fourth passes; current broad completion
still requires reconciling all affected evidence with the final source.

The fresh name inventory resolves all **81 GAP/REFUTED rows** to notes and
current test identities, using the successful root execution report and current
native declarations. This remains an identity inventory, not a behavioral proof.
All retention notes were reviewed. A concrete gap in the wording of
`JB-container-034` was corrected: its retained Go test checks a selected
non-nil concrete input Volume and HasExecutor **before any Run/pod binding**;
the daemon-backed handoff streams after Run. Its test and historical verdict
are unchanged. The fourteen permutation tests now additionally have complete
source-expansion and paired-fault evidence for this pass's helper changes.

All validation jobs are terminal and `git diff --check` passes. The numerical
gates are met, but the goal remains active until the full retained/replacement
source audit and final reconciliation of removed/merged cases are complete.
