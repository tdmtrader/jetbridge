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
