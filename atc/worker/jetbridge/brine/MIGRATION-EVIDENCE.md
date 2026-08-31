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
