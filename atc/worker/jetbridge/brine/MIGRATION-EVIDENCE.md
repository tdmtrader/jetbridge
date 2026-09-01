# Strict spec-count migration status (2026-09-01)

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
| **fully migrated**: paired failure evidence, no prohibited double, source removed | **982** | **14.32%** |
| strict paired evidence, but source still present | 0 | 0.00% |
| **runs in Brine but not the full philosophy**: paired failure evidence, but uses a stub, test sink, injected-fault object, fake, or mock | **112** | **1.63%** |
| of the preceding exception bucket whose source test was removed | 66 | 0.96% |
| total source tests with paired per-test failure evidence | 1,094 | 15.95% |
| former claimed tests with no admissible paired evidence | 965 | 14.07% |
| Brine scenarios | 1,877 | execution count only |

The requested two headline percentages are therefore **14.32% fully migrated**
and **1.63% validated but running outside the full philosophy**. The second is
not another migration percentage: 46 of its 112 source tests still exist. If
"migrated" is restricted to removed source tests in both buckets, the figures
are 14.32% strict and 0.96% philosophy-exception.

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
| `api/auth` resource authorization, real TCP/production handlers/real PostgreSQL | 29 | 29 | 0 | 29 |
| `api/accessor/accessor_test.go` production accessor profiles | 38 | 38 | 0 | 38 |
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
| `go-concourse/concourse/pipelines_test.go` strict real-server client subset | 24 | 24 | 0 | 24 |
| `destroyer_test.go` | 8 | 0 | 8 | 8 |
| `scanner_test.go` | 14 | 0 | 14 | 14 |
| durable-storage, volume-DaemonSet, and behavioral-permutation campaign | 44 | 0 | 44 | 44 |
| daemonset-integration and daemon-client retained cases | 46 | 0 | 46 | 0 |
| **total** | **1,094** | **982** | **112** | **1,048** |

### Completed sink-free revalidation

The scenario-scoped `jetbridge-db` resource previously called
`dbtest.NewBuilder`, which constructed a `lagertest` logger even when the
scenario's own step never used the Builder. The resource now constructs the
same exported Builder factory surface with an ordinary production logger and
`db.NewWorkerCache`. All exact 22 affected manifests were rerun in full from
mutation case 1 against restored historical source leaves and the sink-free
resource plane. The three terminal batches recorded 243 production mutation
cases and 421 individually paired source/Brine failures: batch A, 82 cases
(`192179406`); batch B, 69 cases (`e3029c15e`); and batch C, 92 cases
(`ff59ff22d`). The affected manifests are:

- `accessor-profiles-strict.results.json` (38)
- `api-auth-admin-strict.results.json` (4)
- `api-auth-authentication-strict.results.json` (10)
- `api-auth-authorization-strict.results.json` (4)
- `api-config-strict.results.json` (73)
- `api-resource-auth-build-strict.results.json` (6)
- `api-resource-auth-pipeline-strict.results.json` (9)
- `api-resource-auth-worker-strict.results.json` (14)
- `cc-api-strict.results.json` (14)
- `cli-api-strict.results.json` (12)
- `component-notifications-strict.results.json` (21)
- `container-domain-strict.results.json` (17)
- `db-build-strict.results.json` (27)
- `db-team-strict.results.json` (18)
- `idtoken-generator-strict.results.json` (15)
- `job-domain-strict.results.json` (35)
- `notification-bus-domain-strict.results.json` (11)
- `pipeline-domain-strict.results.json` (24)
- `resource-scope-domain-strict.results.json` (23)
- `users-api-strict.results.json` (12)
- `wall-api-strict.results.json` (14)
- `worker-factory-domain-strict.results.json` (20)

The accessor correction uses a natural mutation of the production user-ID
match branch. Its historical "granted the same role multiple times" source
leaf asserts only that the role is present when both user and group
authorization match; it does not prove uniqueness. The corrected manifest and
evidence claim that actual role-presence behavior, not a stronger deduplication
property.

After the mutation reruns, all 18 affected features were executed against the
clean adapter and passed 421/421 with terminal `run_end` records in
`.brine-runs` (runs `01M1E5MX8D7NR3EX4KVZ0EBPE5` through
`01M1E5S44R2JSA4GYSBBPVC9EH`). This targeted run is the execution check for
the promoted cohorts.

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

The other 1,085 tests in the former numerator have source references and green
scenarios, but no admissible record of the individual old test and its Brine
replacement failing on the same production defect. This includes all recent
client/API/domain cross-layer counts, exact-status claims, and the 39 source
tests named by `pipeline-retention.feature`; that feature explicitly records
that its Ginkgo half was never run.

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

- `brine check`: 1,877/1,877 valid.
- the exact 18 affected features: 421/421 passed with terminal `run_end`
  records, runs `01M1E5MX8D7NR3EX4KVZ0EBPE5` through
  `01M1E5S44R2JSA4GYSBBPVC9EH`.
- the all-feature run executed 1,877 scenarios and finished 1,875/1,877; its
  two failures are in newly prepared, not-yet-validated pipeline coverage.
  This document does not claim a globally green full run.
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
