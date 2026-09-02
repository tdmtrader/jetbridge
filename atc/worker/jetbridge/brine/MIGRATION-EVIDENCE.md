# Strict spec-count migration status (2026-09-02)

The former 2,059 / 6,857 (30.03%) claim is withdrawn. It counted a source
reference in a feature header as coverage. That is an inventory of intent, not
evidence that the replacement discriminates the behavior the source test did.

A source leaf test counts as strict Brine coverage only when the first three
conditions are true, and as fully migrated only when all four are true:

1. a named production mutation was run against the individual source test;
2. the same mutation made a named Brine scenario fail on the corresponding
   assertion;
3. the current Brine path uses no stub, recording sink, injected-fault object,
   fake implementation, or mock; and
4. for **fully migrated**, the source test has actually been removed.

A green Brine run proves only that a scenario executes. It does not satisfy
the first two rules. A mutation that makes some test and some unrelated
scenario fail is also not evidence.

## Revised result

| quantity | source leaf tests | percentage of 6,857 |
|---|---:|---:|
| **fully migrated**: paired failure evidence, no prohibited double, source removed | **1,572** | **22.93%** |
| strict paired evidence, but source still present | 0 | 0.00% |
| **runs in Brine but not the full philosophy**: paired failure evidence, but uses a stub, test sink, injected-fault object, fake, or mock | **112** | **1.63%** |
| of the preceding exception bucket whose source test was removed | 66 | 0.96% |
| total source tests with paired per-test failure evidence | 1,684 | 24.56% |
| former claimed tests with no admissible paired evidence | 375 | 5.47% |
| Brine scenarios | 2,476 | execution count only |

The current two headline percentages are therefore **22.93% fully migrated**
and **1.63% validated but running outside the full philosophy**. The second is
not another migration percentage: 46 of its 112 source tests still exist. If
"migrated" is restricted to removed source tests in both buckets, the figures
are 22.93% strict and 0.96% philosophy-exception.

## Admitted evidence ledger

| cohort | paired source/Brine failures | strict | exception | source removed |
|---|---:|---:|---:|---:|
| gc/lidar disposition, excluding scanner and Destroyer | 36 | 36 | 0 | 36 |
| `artifact_locator_test.go` | 5 | 5 | 0 | 5 |
| `job_config_test.go` | 23 | 23 | 0 | 23 |
| `config_diff_test.go` | 11 | 11 | 0 | 11 |
| `config_test.go` | 19 | 19 | 0 | 19 |
| `pipeline_test.go` | 19 | 19 | 0 | 19 |
| `configvalidate/validate_test.go` | 114 | 114 | 0 | 114 |
| `container_limits_test.go` | 15 | 15 | 0 | 15 |
| `task_test.go` | 37 | 37 | 0 | 37 |
| `api/config_test.go` strict real-server subset | 73 | 73 | 0 | 73 |
| `db/team_test.go` strict real-PostgreSQL subset | 18 | 18 | 0 | 18 |
| `db/build_test.go` strict real-PostgreSQL subset | 27 | 27 | 0 | 27 |
| `db/pipeline_test.go` strict real-PostgreSQL subset | 24 | 24 | 0 | 24 |
| `db/job_test.go` strict real-PostgreSQL subset | 35 | 35 | 0 | 35 |
| `db/worker_factory_test.go` real-PostgreSQL production-cache suite | 20 | 20 | 0 | 20 |
| `db/resource_config_scope_test.go` real-PostgreSQL resource-scope domain | 23 | 23 | 0 | 23 |
| `db/container_test.go` strict real-PostgreSQL subset | 17 | 17 | 0 | 17 |
| `db/component_notifications_test.go` | 21 | 21 | 0 | 21 |
| `db/notifications_bus_test.go` strict real-PostgreSQL subset | 11 | 11 | 0 | 11 |
| `event/parser_test.go` | 26 | 26 | 0 | 26 |
| `creds/idtoken/token_generator_test.go` | 15 | 15 | 0 | 15 |
| `JobFactory.JobsToSchedule` | 15 | 15 | 0 | 15 |
| `build_test.go` core value methods | 8 | 8 | 0 | 8 |
| `public_plan_test.go` concrete public serialization | 5 | 5 | 0 | 5 |
| `worker_test.go` version validation | 3 | 3 | 0 | 3 |
| `vars/template_test.go` concrete variable interpolation subset | 34 | 34 | 0 | 34 |
| `sidecar_test.go` production parser/validation/JSON subset | 20 | 20 | 0 | 20 |
| `configwarning_test.go` | 14 | 14 | 0 | 14 |
| `fly/eventstream/render_test.go` real TCP SSE rendering | 37 | 37 | 0 | 37 |
| `api/auth` real TCP/production-handler authorization boundaries | 18 | 18 | 0 | 18 |
| `api/auth` build/pipeline resource authorization, real TCP/production handlers/real PostgreSQL | 15 | 15 | 0 | 15 |
| `api/auth` worker resource authorization, real TCP/production handlers/real PostgreSQL | 14 | 14 | 0 | 14 |
| `api/accessor/accessor_test.go` production accessor profiles | 38 | 38 | 0 | 38 |
| `api/accessor/accessor_test.go` persisted admin-team authorization matrices | 48 | 48 | 0 | 48 |
| `scheduler/algorithm/algorithm_test.go` production input resolution matrix | 77 | 77 | 0 | 77 |
| `exec/run_state_test.go` production RunState value behavior | 25 | 25 | 0 | 25 |
| `exec/task_config_source_test.go` strict production config-source subset | 18 | 18 | 0 | 18 |
| `db/team_test.go` remaining persistence and team-scoped build-query subset | 25 | 25 | 0 | 25 |
| `db/resource_test.go` strict production resource/query subset | 20 | 20 | 0 | 20 |
| `db/build_test.go` remaining strict production persistence subset | 9 | 9 | 0 | 9 |
| `db/job_test.go` remaining strict production query/lifecycle subset | 19 | 19 | 0 | 19 |
| `db/container_repository_test.go` production persistence subset | 24 | 24 | 0 | 24 |
| `db/container_repository_test.go` production orphan-discovery subset | 12 | 12 | 0 | 12 |
| `db/volume_test.go` production volume-core subset | 17 | 17 | 0 | 17 |
| `db/pipeline_test.go` remaining production query subset | 11 | 11 | 0 | 11 |
| `db/check_factory_test.go` production persistence subset | 23 | 23 | 0 | 23 |
| `db/volume_repository_test.go` production persistence subset | 32 | 32 | 0 | 32 |
| `api/versions_test.go` production versions API subset | 18 | 18 | 0 | 18 |
| `api/builds_test.go` production authorization subset | 20 | 20 | 0 | 20 |
| `db/pipeline_lifecycle_test.go` production lifecycle subset | 11 | 11 | 0 | 11 |
| `api/jobs_test.go` production authorization boundary subset | 13 | 13 | 0 | 13 |
| `api/resources_test.go` production resource API subset | 16 | 16 | 0 | 16 |
| `api/pipelines_test.go` production state-transition subset | 8 | 8 | 0 | 8 |
| `api/builds_test.go` additional real-server production subset | 27 | 27 | 0 | 27 |
| `api/containers_test.go` real-server list/get subset | 10 | 10 | 0 | 10 |
| `db/resource_cache_lifecycle_test.go` production lifecycle subset | 8 | 8 | 0 | 8 |
| `api/workers_test.go` real-server production subset | 15 | 15 | 0 | 15 |
| `api/versions_test.go` remaining real-server production subset | 21 | 21 | 0 | 21 |
| `fly/rc/targets_test.go` production filesystem subset | 13 | 13 | 0 | 13 |
| `api/users_test.go` production users serialization/filter subset | 12 | 12 | 0 | 12 |
| `api/cli_test.go` production CLI downloads | 12 | 12 | 0 | 12 |
| `api/cc_test.go` production CC XML over real TCP/PostgreSQL | 14 | 14 | 0 | 14 |
| `api/wall_test.go` production wall API over real TCP/PostgreSQL | 14 | 14 | 0 | 14 |
| `go-concourse/concourse/teams_test.go` strict real-server client subset | 9 | 9 | 0 | 9 |
| `api/teams_test.go` strict real-server team API subset | 7 | 7 | 0 | 7 |
| `db/resource_type_test.go` production resource-type domain | 22 | 22 | 0 | 22 |
| `db/build_factory_test.go` production build-factory policy | 25 | 25 | 0 | 25 |
| `go-concourse/concourse/jobs_test.go` strict real-server client subset | 19 | 19 | 0 | 19 |
| `api/jobs_test.go` strict real-server job API subset | 9 | 9 | 0 | 9 |
| `api/pipelines_test.go` strict real-server pipeline API subset | 5 | 5 | 0 | 5 |
| `go-concourse/concourse/pipelines_test.go` strict real-server client subset | 14 | 14 | 0 | 14 |
| `db/build_in_memory_check_test.go` production in-memory-build domain | 24 | 24 | 0 | 24 |
| `api/jobs_test.go` production ClearTaskCache API subset | 5 | 5 | 0 | 5 |
| `api/jobs_test.go` production manual-build guard subset | 2 | 2 | 0 | 2 |
| `go-concourse/concourse/builds_test.go` strict real-server client subset | 16 | 16 | 0 | 16 |
| `api/builds_test.go` strict real-server build API subset | 9 | 9 | 0 | 9 |
| `api/jobs_test.go` strict real-server exact-build API subset | 4 | 4 | 0 | 4 |
| `destroyer_test.go` | 8 | 0 | 8 | 8 |
| `scanner_test.go` | 14 | 0 | 14 | 14 |
| durable-storage, volume-DaemonSet, and behavioral-permutation campaign | 44 | 0 | 44 | 44 |
| daemonset-integration and daemon-client retained cases | 46 | 0 | 46 | 0 |
| **total** | **1,684** | **1,572** | **112** | **1,638** |

### Completed production-logger revalidation

A dependency-closure review found that scenario-scoped `jetbridge-db` still
called `postgresrunner.OpenConn`, which instantiated
`lagertest.NewTestLogger` even after the earlier Builder correction. Commit
`aef6c0fe7` changed both scenario and clone connections to call production
`db.Open` with an ordinary `lager.NewLogger`; Brine no longer traverses the
Ginkgo connection wrapper or its join validator.

All 33 previously admitted DB-backed manifests were then rerun completely
from mutation case 1 against restored historical source leaves. The three
terminal batches recorded 336 production mutation cases and 562 individually
paired source/Brine failures, with exact dry-run name checks before every
manifest and clean reversal of every temporary source overlay:

- API/auth batch: 12 manifests, 80 cases, 210 leaves;
- database/domain batch: 10 manifests, 163 cases, 211 leaves; and
- recent client/domain batch: 11 manifests, 93 cases, 141 leaves.

The three new build-client manifests were independently rerun after the same
resource correction: 17 cases and 29 exact leaves, followed by survivor runs
of 138/138 client specs and 622/622 API specs and clean Brine runs of 16/16,
9/9, and 4/4. No pre-correction result contributes to the strict total.

An independent final audit found all 36 result files terminal and fresh: 353
mutation cases and 591 exact source leaves in total. It also checked every
manifest/result case ID and count, expected scenario, source-test name, commit
ancestry, and the absence of unclaimed collateral failures. The final
surviving `atc/db` Ginkgo suite passed 750/750 under the isolated Ginkgo
runner.

The accessor correction uses a natural mutation of the production user-ID
match branch. Its historical "granted the same role multiple times" source
leaf asserts only that the role is present when both user and group
authorization match; it does not prove uniqueness. The corrected manifest and
evidence claim that actual role-presence behavior, not a stronger deduplication
property.

Why the exception rows are exceptions:

- the scanner scenarios use the hand-written `imageRegistry` resolver and
  scope-deletion wrappers;
- the Destroyer scenarios construct `lagertest.NewTestLogger`, a test sink;
- the JetBridge daemon/storage scenarios use one or more of a fake Kubernetes
  clientset, `httptest` stand-in daemon, or in-process shell executor.

This classification is deliberately conservative. A row is not promoted to
strict merely because a later refactor made part of its journey real; it needs
scenario-level proof that the paired path no longer reaches the prohibited
component.

## What does not count

The other 674 tests in the former numerator have source references and green
scenarios, but no admissible record of the individual old test and its Brine
replacement failing on the same production defect. The 39 source tests named
by `pipeline-retention.feature` remain an example: that feature explicitly
records that its Ginkgo half was never run.

The starting 300-odd Brine scenarios are not all in that numerator merely
because they were mutation-tested. They were tested for scenario
falsifiability, and the source suites were subjected to deletion batteries,
but the early batteries sometimes paired a red source suite with an unrelated
red Brine scenario. The later 102-mutation JetBridge campaign corrected that
protocol and explicitly records per-test attribution for 95 source tests; those
95 are admitted above. The remainder of the original scenario set has no
equivalent corrected per-source-test result and therefore does not count here.

No migrated suite has been shown to require these crutches. The fake
Kubernetes paths can use envtest or a real cluster, the hand-written resolver
can use a real OCI registry, the test logger can use an ordinary production
logger, and the stand-in daemon can be replaced by the daemon process. Tests
whose only assertion is a call record or a no-op/absence with no observable
outcome are different: they should stay as Go unit tests and are not migration
candidates.

## Execution verification (not equivalence evidence)

- `brine check`: pending refresh for 2,382 scenarios after the latest cohorts.
- `api-versions-strict.feature`: 18/18 passed after nine natural production
  mutations exactly paired all 18 admitted leaves; 73 source leaves remain.
- `api-builds-remaining-strict.feature`: 20/20 passed after two natural
  authorization-status mutations exactly paired all 20 admitted leaves; 66
  source leaves remain.
- `db-volume-repository-strict.feature` and
  `db-volume-repository-handle-strict.feature`: 32/32 passed after 17 natural
  production mutations exactly paired all 32 admitted leaves; nine unproven
  or error-only leaves remain in Go.
- `db-check-factory-strict.feature`: 23/23 passed after 17 natural
  production mutations exactly paired all 23 admitted leaves; 11 channel-
  dependent in-memory leaves remain in Go.
- `db-pipeline-query-strict.feature`: 11/11 passed after eight natural
  production mutations exactly paired all 11 admitted leaves; 23 unproven
  leaves remain in Go.
- `db-volume-core-strict.feature`: 17/17 passed after ten natural production
  mutations exactly paired all 17 admitted leaves; 21 unproven leaves remain
  in Go.
- `db-container-repository-strict.feature` and
  `db-container-orphans-strict.feature`: 36/36 passed after 12 natural
  production mutations exactly paired all 36 admitted leaves; 21 unproven
  leaves remain in Go.
- four remaining `db/job_test.go` strict features: 19/19 passed after eight
  natural production mutations; 38 unproven leaves remain in Go.
- `db-build-remaining-strict.feature`: 9/9 passed after four natural
  production mutations; 59 unproven leaves remain in Go.
- `db-resource-remaining-strict.feature`: 20/20 passed after six natural
  production mutations; 60 unproven leaves remain in Go.
- `db-team-remaining-strict.feature` and `db-team-build-list-strict.feature`:
  32/32 passed after 11 natural production mutations exactly paired 25
  admitted leaves; 90 unproven leaves remain in Go.
- `task-config-source-strict.feature`: 18/18 passed after nine natural
  production mutations; 24 call-record, injected-error, or otherwise
  non-discriminating leaves remain in Go.
- `run-state-strict.feature`: 25/25 passed after 11 natural production
  mutations exactly paired every admitted leaf; four scripted-step leaves
  remain in Go.
- `scheduler-algorithm-strict.feature`: 77/77 passed after two natural
  production accumulator mutations partitioned all 77 source leaves into 64
  resolved and 13 unresolved cases with exact, disjoint failure sets.
- `access-control.feature`: 52/52 passed after two natural boolean-operator
  mutations paired all 48 admitted authorization leaves with exact, disjoint
  scenario failure sets; the four retained display-ID leaves stayed green.
- the exact 18 affected features: 421/421 passed with terminal `run_end`
  records, runs `01M1E5MX8D7NR3EX4KVZ0EBPE5` through
  `01M1E5S44R2JSA4GYSBBPVC9EH`.
- a historical all-feature run, before the current 1,977-scenario catalog,
  executed 1,877 scenarios and finished 1,875/1,877. That run is not evidence
  about the current catalog, and this document does not claim a globally green
  full run.
- adapter build and `go test ./steps`: passed.
- `go test ./go-concourse/concourse`: passed.
- `git diff --check`: passed.

The sections below preserve the earlier line-deletion and mutation-testing
analysis. Their older scenario totals are historical; where an older section
uses a looser definition, this strict status takes precedence.

---

# What the Go suites still cover, and how that was measured

Final state 2026-08-29. brine 375/375, `go test` 184, ginkgo 19/19.

## The protocol, and the two corrections it needed

A Go suite may be deleted when breaking production reddens BOTH it and brine.
If only the Go suite reddens, brine has a hole and the Go suite stays.

**Correction 1: file-level both-red is not test-level evidence.** Running a
whole file under `-run` pairs *some* test reddening with *some* brine scenario
reddening, and those can be about different behaviours. Measured: mutating
`ArtifactLocator.Locate` to report found for unknown keys reddened
`artifact_locator_test.go`, and the brine failure it paired with was about init
container batching. Per-test attribution is the real bar. It is what stopped
`daemon_client_test.go` being deleted with 7 of its 16 tests unevidenced.

**Correction 2: `old=RED brine=GREEN` is a prompt to investigate, not a
verdict.** Two of the twenty-one findings were the Go test pinning a
representation detail, not brine missing a behaviour:

  - `preferred[0].Weight == 100` — production emits exactly one preferred
    scheduling term, and Kubernetes uses weights only to rank BETWEEN terms, so
    the constant can never affect scheduling.
  - `inits != nil` — the caller does `append(initContainers, inits...)`, and
    appending nil is indistinguishable from appending an empty slice.

Treating those as gaps would have meant writing scenarios asserting an affinity
weight nothing reads and a nil-ness that `append` discards — making the suite
worse in the name of coverage.

## Results

102 mutations across 10 suites. 21 findings, of which 19 were real holes in
brine; 20 are now closed and verified, in the sense that the mutation which
exposed each one reddens the scenario written for it.

The holes were consistently STRUCTURAL rather than value-shaped, which is why a
suite of 330 scenarios written by reading the old tests had missed them. You can
translate an assertion about a value by looking at it. You only find an
assertion about WHEN something runs, or WHERE it lands, by breaking it.

  - init container ORDER: cleanup-stale after fetch-inputs deletes the inputs
    it just fetched
  - a failed artifact fetch EXITING 0, so the step runs against inputs it never
    received
  - an empty artifact key no longer failing fast
  - an unresolvable producer node meaning the daemon key is never recorded, so
    downstream falls back to the raw handle and cannot find an artifact that is
    on disk
  - the on-disk layout of step volumes disagreeing with the daemon key that
    names them, in four separate ways
  - hard node affinity demanding a label no daemon sets
  - a producer daemon REFUSING a connection, as distinct from a node that left
  - `LocateNode` reporting found for a key it does not hold

## The boundary: what brine structurally cannot cover

**brine asserts what a pod spec SAYS, not what the pod DOES.** It builds pod
specs and inspects them; it does not execute init containers. Two behaviours
live entirely inside an init container's `sh -c` text:

    ${HOST_IP} -> 127.0.0.1   in the resolve script's daemon URL
    exit 1     -> exit 0      on the empty-artifact-key guard

Nothing on the PodSpec differs. Closing them needs either a substring check —
the defect repaired twice in this effort, where a `/resolve-batch` assertion
survived a rewrite that kept the string — or executing pods. They stay in Go.

The other permanent residues are assertions with no observable outcome at all:

  - `DaemonClient.TriggerMirror` returns nil on 202, non-202, transport failure
    and a request that could not be built. No mutation can redden it either side.
  - `NodeIPResolver` refusing an IP-shaped name WITHOUT asking the API. A mutant
    that asks and then returns the right sentinel is identical by every value
    that leaves `Resolve`; only a call record separates them.
  - PVC-mode negatives (no affinity, no cleanup) and `UploadOutputs` being a
    no-op.

Asserting any of these needs a double that records what it was asked, which is
the recording-double pattern `steps/daemon.go`'s header rejects.

## Where each suite ended up

DELETED, per-test evidence for every test:

| suite | tests | evidence |
|---|---|---|
| storage_daemonset_durable_test.go | 10 | 10 evidenced |
| artifact_locator_test.go | 6 | 5 evidenced, 1 asserted nothing (no -race) |
| volume_daemonset_test.go | 16 | 16 evidenced |
| behavioral_permutations_test.go | 19 | 18 evidenced, 1 inert (nil vs empty) |

KEPT, with the reason:

| suite | why |
|---|---|
| daemonset_integration_test.go | 37 of 41 evidenced; 2 are script semantics, 4 are unmutatable negatives |
| daemon_client_test.go | 9 of 16; TriggerMirror cannot be reddened by construction |
| node_ip_resolver_test.go | the "no request was made" residue has no outcome |
| errors_test.go, process_interruption_test.go, resource_cache_key_test.go, executor_test.go | pure functions and table-driven classification; Gherkin makes these worse |
| storage_daemonset_test.go, behavioral_volume_test.go, daemon_tls_test.go | mixed; the behavioural half migrated, 47 tests are unit tests by nature |
| supervisor_test.go, supervisor_script_test.go | deliberately kept from the earlier migration |
| live_*_test.go | `//go:build live`; need a real cluster |

---

# How much of this repository can move to brine

Measured 2026-08-30 across all 196,400 lines of Go test code, package by
package — not sampled and extrapolated.

**The answer is about 21,000 lines, or 10.7% of the surface.**

| package | verdict | deletable |
|---|---|---|
| atc/db | partial — policies, not the data layer | 5,600 |
| atc/gc + atc/lidar | strong | 4,100 |
| fly/integration | partial | 3,400 |
| atc/exec | partial | 1,950 |
| atc/api | partial | 1,900 |
| atc/engine + atc/scheduler | partial | 1,800 |
| cmd/artifact-daemon | measured, after migrating 61 behaviours | ~1,200 |
| atc/db/migration | weak | 630 |
| atc/creds + vars | weak | 530 |
| go-concourse | **should not move** | 0 |
| atc + atc/configvalidate | **should not move** | 0 |
| testflight + topgun | **should not move** | 0 |

## Why it is not larger, which is the useful part

**The de-faking already happened.** This programme's engine is "replace the
recording double with a working one and assert the round trip". That payoff was
collected in this repository before brine existed — 60,960 lines of fakes
removed down to 24,190. What is left:

  - atc/db: 31,186 lines, all 1,013 specs on real Postgres, and in the whole
    package exactly TWO hand-written doubles and zero counterfeiter fakes.
  - atc/api: one counterfeiter fake in the entire tree; DB-error paths driven
    by closing a real connection.
  - atc/exec: 12 of 30 files on real Postgres, real delegates, real streamer.
  - atc/db/migration: 23 files, 117 specs, ZERO doubles of any kind.

There is no double left to replace, so every migrated line has to be justified
by the sentence alone — and most of these assertions are not sentences.

**Three packages should not move at all.**

  go-concourse observes a REQUEST, not an outcome, in every assertion. It is
  the layer whose job is the wire format. fly/integration asserts the same
  request shapes one layer up against the same ghttp; migrating either would
  write a third copy of a contract already pinned twice.

  atc/configvalidate varies a GRAMMAR, not a scalar. A Scenario Outline over
  malformed pipeline YAML is a worse Go table.

  testflight and topgun cannot run in brine's tier: testflight needs a deployed
  Concourse and topgun needs K3s, which CLAUDE.md prices at 23 minutes to 3
  hours and marks CI-only. That is 24,187 lines out on physics.

**Shared fixtures bound every estimate.** A file only goes when every test in
it is covered. atc/db's db_suite_test.go is imported by all 57 root test files
and its dbtest.Builder is imported by atc/scheduler, atc/lidar and atc/exec —
it can never be deleted from here, so a brine step layer would WRAP Go that
stays rather than replacing it. The daemon showed the same shape from the other
side: 61 behaviours migrated, ~1,200 lines deletable, because 43 of its
remaining tests assert unexported state or request counts that cannot earn
both-red evidence at any price.

## What this means for a 30% target

30% is 58,920 lines. The measured ceiling is ~21,000. Reaching 30% would mean
migrating atc/db's query-shape assertions (pagination cursors, id-range
boundaries), fly's ui.Table rendering with per-cell colours, and
configvalidate's grammar — each of which is a good Go test that becomes a worse
Gherkin one. The programme's own rules forbid all three.

---

# Where the count actually stands (2026-08-31)

Measured, not estimated:

| quantity | lines |
|---|---|
| Go test surface on `core`, excluding brine | 210,753 |
| deleted on this branch so far | 15,412 |
| added back as consolidated helpers | 241 |
| **net moved** | **15,171 (7.2%)** |
| measured ceiling for the whole programme | ~21,000 (10.0%) |

Everything deleted so far is `atc/worker/jetbridge`. Nothing from the gc,
lidar, db-policy, engine, scheduler or exec migrations has been deleted yet —
those features exist and pass, but the source suites still stand, because
deletion needs PER-TEST both-red evidence and that is a separate campaign.

## The 30% target, stated plainly

30% is 63,000 lines. The ceiling is 21,000. The gap is not effort, and it is
not time — it is that the remaining 175,000 lines are mostly assertions that
do not survive translation into a sentence:

  - `atc/db` query-shape tests (pagination cursors, id-range boundaries) pin a
    representation. A Gherkin sentence about a cursor is a worse Go table.
  - `go-concourse` and `fly/integration` assert a REQUEST, not an outcome. The
    contract is already pinned twice; a third copy adds no discrimination.
  - `atc/configvalidate` varies a grammar, not a scalar.
  - `testflight` and `topgun` (24,187 lines) need a deployed Concourse or K3s.

Reaching 30% would mean migrating those anyway. Each would produce scenarios
that read well and discriminate nothing — which is the exact defect this
programme keeps finding and repairing in its own output. Thirteen such defects
were found in the last batch alone, in a suite that was already fully green.

I am recording the ceiling rather than reporting progress toward a number the
work cannot honestly reach.

## What the last audit changed about the protocol

Eight of the thirteen findings were prose claiming coverage the assertions did
not deliver. A green suite cannot detect those. The additions that can:

  1. A mutation that reddens NOTHING gets written down as unpinned, in the file,
     next to the claim it disproves. Three are recorded from the last batch.
  2. Assertion ORDER is part of the assertion. brine stops at the first failing
     step, so a survival check written last is never evaluated when an earlier
     line reddens. One finding was exactly this.
  3. "Reddened by" prose names the ONE line that reddens, and says plainly when
     the others are decorative.

---

# The ceiling was wrong. Corrected: ~15,000, not 21,000

Four assessors re-derived the ceiling for the four largest candidate packages,
blind to the 21,000 figure. An adjudicator was then shown the prior and sent to
the files where they disagreed. The prior lost.

| package | prior | adjudicated | delta |
|---|---|---|---|
| atc/db | 5,600 | **1,784** | −68% |
| atc/api | 1,900 | **776** (583 after strike) | −59% |
| fly/integration | 3,400 | **5,138** | **+51%** |
| atc/exec | 1,950 | **740** (549 floor) | −62% |
| **programme-wide** | **21,000** | **~15,000** | **−29%** |

## The error has a name

The prior counted BEHAVIOURS EXPRESSIBLE IN GHERKIN. The rule counts WHOLE
FILES. Those diverge by 3-4x here, because the good behaviours live in bad
neighbourhoods — a policy worth migrating sits in a file pinned open by one
assertion about a cursor, a DTO or a schema name.

This is the same error class this programme keeps finding in its own scenarios:
counting what READS like coverage instead of what DISCRIMINATES. I made it at
the level of the estimate rather than the assertion.

The refutation is the programme's own data, which I had and did not apply:
`pipeline-retention.feature` migrated **39 atc/db cases** with named mutations
and can delete **exactly one file, 255 lines**. That measured exchange rate was
sitting in this repository while the estimate said 5,600.

## The largest casualty, which no assessor found and the adjudicator did

`atc/db/job_factory_test.go` — the `JobsToSchedule` Describe is ~800 lines of
the best policy material in the package ("a paused job is not scheduled", "a job
in a paused pipeline is not scheduled"), every case with a one-line mutation and
already in brine's vocabulary. It is held by `VisibleJobs` at line 201 asserting
`visibleJobs[0].NextBuild.{ID,Name,JobName,PipelineID,PipelineName,
PipelineInstanceVars,TeamName}` field by field. **800 lines of first-rate
movable policy, blocked by one dashboard DTO.**

## fly/integration is the one place the prior was too LOW

73 files, one CLI verb each, no shared fixture beyond a 275-line suite. Rule 4
barely bites, so it is now the single largest opportunity in the tree. The prior
set its rate by analogy to atc/db; it should have been set by observing that the
package has no shared state to bind files together.

## Deletable lines are not lines saved — and this should be the headline

brine is **44,179 lines (33,333 steps + 10,846 features) for 472 scenarios ≈ 80
lines per scenario.** A 15,000-line deletion bought with 8,000-10,000 lines of
new Gherkin and step vocabulary is a WASH on volume. About 1,425 lines of fly's
thin verb files are line-neutral at best.

So the programme should stop reporting a deletion count and report **net line
delta plus a discrimination argument**. The volume case does not survive
scrutiny at 15,000. The discrimination case does, and it is the real one: the
audits keep finding assertions that cannot fail, in both estates.

## The 30% target

30% is 58,920-63,000 lines. Rule 5 zeroes topgun/k8s and topgun/k8s_behavioral
outright — 20,268 lines, 10.3% of the corpus. Of the 176,132 addressable lines
you would have to delete 36%, when the four packages holding 47% of the corpus
yield 9.1%, and 4.5% once fly/integration is set aside as structurally atypical.

**Unreachable by a factor of four.** Reaching it means repealing rule 4 — the
rule that stopped seven unevidenced tests being deleted with `daemon_client_test.go`.
The target should be withdrawn, not renegotiated.

---

# The gc/lidar deletion campaign, measured

82 Its across 15 files, each measured with a named production mutation run
against BOTH estates. Skeptics then tried to refute every DELETABLE claim.

| outcome | count |
|---|---|
| survived refutation | 58 |
| **refuted by a skeptic** | **24 (29%)** |
| gap — old reddens, brine green | 18 |
| inert — no mutation reddens the Go test | 4 |
| brine strictly stronger | 4 |

**Deleted: 4 files, 403 lines.** 58 survivors bought only 403 lines, because
rule 4 needs EVERY It in a file covered and 11 of 15 files had at least one
that was not. atc/gc yields 403 of 3,150 lines — **12.8%**, against the
adjudicator's "generous 60%". The corrected ceiling of ~15,000 is still
optimistic.

Deleted, each It with a straddling or predicate-isolating mutation:
`artifacts_collector_test.go` (62), `pipeline_collector_test.go` (62),
`volume_collector_test.go` (195), `worker_collector_test.go` (84).

## The skeptics earned their place

24 of 82 claims died under refutation — nearly a third. The protocol without an
adversarial stage would have deleted those tests on evidence that looked
identical to the good evidence.

## Four Go tests that cannot fail

Found by mutation, not by reading. Each stayed green under every mutation tried:

  - `container_collector_test.go` — "succeeds with nothing to collect"
  - `destroyer_test.go` — "FindDestroyingVolumesForGc returns nothing when the
    worker has no destroying volumes"
  - `scanner_test.go` — "reads persisted pipeline state through a separately
    constructed factory": green under all 37 mutations attempted
  - `scanner_test.go` — "does not schedule a check for an already-cancelled
    empty enumeration"

These are defects in the EXISTING suite, unrelated to migration. They are left
in place and recorded, not deleted — an inert test is a finding, not a licence.

## Eighteen gaps, which are the real inventory

Cases where the Go test reddens and brine does not. These are brine's holes,
concentrated in `scanner_test.go` (6), `resource_config_check_session_collector_test.go`
(4), and pairs in check/task_cache/deprecated_scope/access_tokens. Closing them
is the honest next task; deleting around them would be the dishonest one.

---

# The whole-file rule was withdrawn. The ceiling nearly tripled.

"A file only goes when every test in it is covered" was never a safety rule.
The safety property is that no test dies without its own adversarially-verified
both-red evidence; whole-file deletion was just the tidiest way to honour it.
Individual evidenced `It` blocks can go while the file stands, at the same bar.

| | whole-file | per-It |
|---|---|---|
| atc/db | 1,784 | **12,500** |
| atc/api | ~776 | **5,000** |
| atc/exec | 740 | **2,300** |
| atc/scheduler + atc/engine | 0 | **1,750** |
| fly/integration | 5,138 | **~7,000** |
| remainder | ~6,500 | **~9,500** |
| **programme-wide** | **~13,000** | **~38,000 (19.3%)** |

Confirmed in practice before it was estimated: six gc/lidar files the old rule
valued at ZERO yielded 51 evidenced tests and 1,004 lines at the identical bar.

## What the prior was really measuring

`job_test.go` — 2,861 lines — was zeroed by six pagination Its worth **39 lines
of body**. `job_factory_test.go` was zeroed by one DTO projection at :201 while
750 lines of prime scheduling-admission policy sat beside it. `team_test.go` was
zeroed largely by a single 304-line SQL-scanning It at :3022.

## Why 30% is still not reachable, now for a structural reason

A brace-matched census of every It/Specify/Entry body in the migratable tier
(excluding topgun, testflight and brine itself):

  - migratable-tier test lines: **170,567**
  - lines inside an It body at all: **59,743 — 35.0%**, across **5,152 Its**,
    mean **11.6 lines each**

30% of the 196,400-line corpus is 58,920. The entire It-body surface is 59,743.

**The target is the whole thing.** Reaching 30% means deleting essentially every
test body in the repository — the pagination cursors, the SQL row-shape dumps,
the DTO projections, the span names, the concurrency deadlock guards — because
everything outside those 59,743 lines is scaffolding, helpers, imports and suite
files that no migration can claim.

(The adjudicator put this surface at 54,882 and concluded the target was 4,000
lines out of reach. My own census says 59,743, i.e. ~800 lines PAST it. The
correction makes the point sharper, not weaker: 30% is not near the ceiling, it
IS the ceiling, and only if no rule applies at all.)

## The cost inverts the case above ~15,000

Mean movable It: **11.6 lines**. Mean brine scenario: **93.8 lines** all-in
(10,846 feature + 33,333 step lines over 471 scenarios; the ~80 estimate was 15%
low). Outline collapse is the exception — 39 outlines over 471 scenarios.

Pursuing the full 38,000 means writing ~85,000 lines to delete 38,000:
**net +47,000 lines added.** At the corpus's observed collapse density, nearer
+160,000.

**The net-positive programme is ~15,000 lines**, and it is a disciplined subset
chosen for discrimination, not volume: `JobsToSchedule` (750), `ScheduleBuild`
(608), the SavePipeline rename/history family (337), pin precedence (195).

## Two things the rule change buys nothing

~14,000 test lines carry almost no Its: `cmd/artifact-daemon` is 11,737 lines
with **22** It lines, `atc/worker` 10,140 with **149**. Whole-file remains the
only available rule there.

And `atc/api`'s worth-it figure is **zero**: 379 of its 640 movable Its are
3.15-line status assertions, and its 255-row authorization table is reddened
~150 rows at a time by deleting a single wrapper — coverage that discriminates
once.
