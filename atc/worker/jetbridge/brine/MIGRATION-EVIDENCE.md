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


# CI: how the private dependency is reached (2026-09-04)

The replace line is a module path now, not a directory:

    replace github.com/brine-dev/brine-go =>
      github.com/MarkDucommun/brine-private/runners/implementations/go v0.0.0-20260823044001-8289e541f77b

It used to point at `/Users/tdmtrader/brine-private/...`, so every number above
was reproducible on exactly one laptop. The new form needs
`GOPRIVATE=github.com/MarkDucommun/*` — keeping the request off proxy.golang.org
and the checksum database — plus a git credential for github.com: the osxkeychain
helper locally, a `url.insteadOf` rewrite carrying `((github-token))` in CI.

Not vendoring, and that is a decision rather than an omission: this repository's
origin is public, and core commit e005b84563 already refused to bake someone
else's private source into artefacts this repository ships.

The pseudo-version pins brine-private 8289e541 — the revision
`registry.home/concourse-test-runner:v9` records in `/etc/brine-commit`, having
built the `brine` CLI and engine it carries from it. Adapter and engine cannot
drift apart. v9 also brings Go 1.25.14 (this module's `go` directive is 1.25.6,
so no toolchain download), `GOPRIVATE` preset, and a warm root module cache.

The pipeline's `brine` job runs the CLI, never `adapter run`: the adapter without
`--features` executes zero scenarios and exits 0, and only the CLI's verdict
folds in `budgets.undefined: 0` and unsatisfied wiring. The job is deliberately
manual — no `trigger: true` on its get, no downstream `passed: [brine]` — because
of the single thing no laptop can check: **whether `((github-token))` can read
`MarkDucommun/brine-private`.** Everything else was verified against a fresh
`GOMODCACHE`. Promotion after one green manual run is two lines: add
`trigger: true`, and add `brine` to tag-rc's `passed:` list.


# Rebase onto core for 0.3.2 (2026-09-04/05)

## The rebase

74 commits replayed onto `core`. Two conflicts, both modify/delete, both the
same shape: a file this branch deleted, modified on core by `0d336e062b`
("propagate opaque task cache identity").

| replayed | file | resolution |
|---|---|---|
| `c67193f78f` (30/74) | `atc/worker/jetbridge/container_test.go` | took the deletion |
| `6d2589a43a` (56/74) | `atc/worker/jetbridge/behavioral_permutations_test.go` | took the deletion |

Taking the deletion was right for the fixture edits core made — those keep an
existing assertion alive, and the assertion was already gone. It was NOT right
for the helper `assertAllPodMountsResolve(pod)` and its call site, which are new
coverage core wrote. That is ported (below), not discarded.

Five commits exist only because core moved under the suite:

- `cf555ffa59` — `atc/scheduler.Scheduler.Schedule` returns `ScheduleResult`
  now, not a bare `bool` (`a107ff233a`, `6c0f2f606b`).
- `72753e4377` — `db.Job.UpdateLastScheduled` was removed (`bb0b6ea126`);
  `ConsumeScheduleRequest` is what production calls.
- `81d13b7020` — `0d336e062b` made `ContainerSpec.TaskCacheIdentity` the only
  thing that selects node-local cache storage. Four scenarios asserting where a
  cache lands were silently getting emptyDirs.
- `001cd0a974` — `65e1f31228` folded the daemon's two egress tar producers into
  one, and the survivor emits a `TypeDir` header per directory.
- `27ca45f79c` — the one **expectation change**. `559921ef96` made `lexicalRel`
  the single computation of where a destination goes, and it answers `is not a
  location inside the storage root` for a relative path of `.` exactly as for
  `..`. Three assertions in `daemon-containment.feature` pinned the two older
  strings (`resolves outside the storage root`, `is the storage root itself`).
  The refusals, the 400s, the untouched files and the before-any-copy ordering
  are all still asserted; only the wording moved, and the old string is still
  asserted on the `/register` path in the same feature.

## What the rebase could have invalidated: 463 rows classified

Every deletion this branch made that a rebase could quietly hollow out — 62
gc/lidar rows whose verdict removed a test, and all 401 deleted jetbridge tests
— was classified against four criteria: **(a)** the recorded mutation target
changed on core; **(b)** the named brine scenario or its step file changed;
**(c)** a symbol either artefact reads changed; **(d)** a core commit touched
the same behaviour.

- **149 impacted**, 314 not.
- By criterion (a row can trip several): (a) 26, (b) 125, (c) 92, (d) 16.
- A completeness critic re-read all 463 afterwards and **flipped 30** rows from
  not-impacted to impacted — 25 jetbridge, 5 gc/lidar — every one of them a case
  where the classifier argued a change was inert instead of applying (b) as
  written. All 30 were then measured.

## Re-verification: 149 measured, 85 did not survive

Each impacted row was re-measured from scratch in its own detached worktree:
recorded mutation re-applied (or remapped and the remapping justified), brine
run, the Go test restored from the merge-base and run, then handed to a skeptic
whose job was to break the pairing with one honest mutation.

| outcome | count |
|---|---|
| HOLDS | 64 |
| REFUTED | 63 |
| GAP | 19 |
| INERT | 3 |
| UNMEASURABLE | 0 |

**REFUTED (63)** — a skeptic found an honest mutation that reddens the Go test
while brine stays green:
GL-064; JB-behavioral_permutations-000, -002, -003, -004, -006, -007, -009,
-010, -011, -014, -017, -018; JB-behavioral_runtime_spec-007, -009, -031;
JB-container-000, -002, -004, -005, -006, -007, -008, -009, -010, -015, -016,
-022, -024, -025, -026, -028, -030, -035, -041, -042, -044, -045, -046, -052,
-055, -056, -065, -066, -068; JB-integration-000, -011, -012; JB-process-023;
JB-volume-000, -002, -003, -005, -006, -011, -014, -015, -016, -019, -021;
JB-volume_daemonset-011, -012, -013.

**GAP (19)** — the Go test reddens, brine does not, and brine owes a scenario:
JB-behavioral_permutations-008, -016; JB-behavioral_runtime_spec-008;
JB-container-018, -034, -038, -040, -062, -063, -067, -071, -072;
JB-volume-004, -007, -009, -010, -013, -017, -020.

**INERT (3)** — no mutation reddened the Go test at all; these are recorded as
defects in the tests, not as coverage:
JB-behavioral_runtime_spec-010, JB-container-011, JB-container-061.

**UNMEASURABLE: none.** Every impacted row was measurable one way or the other.

Per-row evidence lives in `DISPOSITION-gc-lidar.md` (the 11 gc/lidar rows) and
`DISPOSITION-jetbridge.md` (the 138 jetbridge rows).

## 85 tests restored

Restoring is always acceptable; deleting on inference is not. Every row that did
not end HOLDS got its test back.

- `atc/lidar/scanner_test.go` — the GL-064 It, plus its helper
  `attachLidarResourceScope` in `lidar_suite_test.go`. Ginkgo specs 21 -> 22.
- `atc/worker/jetbridge/*_restored_test.go` — seven files, one per deleted file
  the tests came from, each headed with the row ids and why they are back.
  Ginkgo specs 19 -> 86 (+67), plain `Test` funcs 190 -> 207 (+17).
- `atc/gc` unchanged at 84 specs: no gc row failed re-verification.

Sibling tests whose evidence held were cut free cleanly in every case, so
nothing was restored as a consequence of subtree restoration. Where the cut left
a helper or a local with no caller it is marked `// PRUNE-ADAPT:`; where core's
own fixture edits from `0d336e062b` ride along they are marked `// PORT-ADAPT:`.

## Coverage ported from core (2026-09-04)

Three commits — `ff25608c18`, `ae548d0cb6`, `27d81692fa` — put into brine what
resolving the two modify/delete conflicts as deletions dropped:

1. `assertAllPodMountsResolve` becomes a reusable step, `every mount in the pod
   names exactly one of its volumes`, over the seven pod-shape scenarios that
   assemble more than one kind of volume. It walks `InitContainers` too.
2. A new scenario for the run-identity cache key, pinning the key **whole**
   (`/var/concourse/cache/run-17-23-build-assets-34a6ec221a61`) so a change to
   the hash CONTENT reddens it, not only a change to the key format.
3. A new scenario for the downgrade: an explicit `CacheStore=hostpath` with no
   `TaskCacheIdentity` still gets an emptyDir.

Five of the six measurements were both-red; one was INERT and is recorded as
INERT. Suite 566 -> 568 scenarios, all green. The honest limit: of the mount
invariant's two halves, only the DUPLICATE half is new — a dangling mount was
already caught by `volumeAt`. Full evidence in `DISPOSITION-jetbridge.md`.

## CI

Unchanged from the section above, *CI: how the private dependency is reached
(2026-09-04)*. That section is the record: module-path replace, `GOPRIVATE`,
the `brine` job manual until `((github-token))` is proven able to read
`MarkDucommun/brine-private`, and the two-line promotion once it is.

## What still owes a scenario

The 19 GAP rows above. They cluster, and the clusters say what is missing:

- **What a stream actually execs, and what it carries.** JB-volume-004, -009,
  -013 pin the `tar` invocation, its container and its path; JB-volume-007, -010
  pin the purpose and mount path in `ExecAttrs`. No scenario asserts either.
- **That a returned `Volume` can stream at all.** JB-container-034, -038,
  JB-volume-017, -020: brine asserts what a pod spec SAYS; it does not assert
  that the `Volume` handed back to the engine has an executor wired up, nor
  that data moves from one to another.
- **Sidecar field mapping and exec-mode sidecars.** JB-container-067, -071, -072
  — the K8s container spec a sidecar becomes, the pause-pod variant, and image
  prefix stripping for sidecars.
- **Concurrency.** JB-container-062, -063: brine has no way to say "two of these
  at once".
- **Negative and default cases.** JB-behavioral_permutations-008 (check
  container volume set), -016 (no init containers without a DaemonSet),
  JB-behavioral_runtime_spec-008 (`TTY=false` when `ProcessSpec.TTY` is nil),
  JB-container-018 (explicit `CacheStore=emptydir`), -040 (input streaming is a
  no-op because init containers do it).

Until those exist, the restored Go tests are the only thing covering them, which
is precisely why they are restored rather than deleted.
