# What the Go suites still cover, and how that was measured

Written 2026-08-29. Baselines at the time: brine 367/367, `go test` 225,
ginkgo 19/19.

## The protocol, and the correction it needed

A Go suite may be deleted when breaking production reddens BOTH it and brine.
If only the Go suite reddens, brine has a hole and the Go suite stays.

**Applied at file level that bar is weaker than it sounds.** Running a whole
file under `-run` and asking "did it go red" pairs *some* test reddening with
*some* brine scenario reddening, and those can be about different behaviours.
Measured: mutating `ArtifactLocator.Locate` to report found for unknown keys
reddened `artifact_locator_test.go`, and the brine failure it paired with was
"Every input is fetched by one init container in one request" — unrelated.

Per-TEST attribution is the real bar:

    daemon_client_test.go              9 of 16 tests ever reddened
    storage_daemonset_durable_test.go 10 of 10 (after one added mutation)

Deleting `daemon_client_test.go` on the file-level result would have dropped
seven tests nothing covers.

## Results

67 mutations across 10 suites. 53 both-red, 13 reddened only the Go suite.

Of those 13, one was not a coverage gap at all: `daemonset_integration_test.go`
asserts `preferred[0].Weight == 100`, and production emits exactly one
preferred term, so the weight has no scheduling consequence — Kubernetes uses
weights only to rank BETWEEN terms. The Go test pins a constant; brine is
right not to care.

Of the remaining 12, eleven are now closed and verified: the mutation that
exposed each one reddens the new scenario. The behaviours were structural
rather than value-shaped, which is why they were missed:

  - init container ORDER: cleanup-stale after fetch-inputs deletes the inputs
    it just fetched
  - hard node affinity demanding a label no daemon sets, so nothing schedules
  - step volumes no longer creating their hostPath directory
  - task caches filed in the daemon's steps/ tree, colliding with step outputs
  - cleanup-stale mounting the artifact hostPath read-only
  - an input overlapping an output losing its bare-output-name filing
  - a relative scratch path resolved against / instead of the working directory
  - a producer daemon REFUSING a connection, as distinct from a node that left
  - LocateNode reporting found for a key it does not hold
  - a reused CHECK container being handed a cleanup container
  - task caches defaulting to hostPath with a backend configured

## The one gap that stays open, and why

`NodeIPResolver` must refuse an IP-shaped node name WITHOUT asking the Nodes
API. The behaviourally meaningful half is closed — a node planted under the
name `10.0.0.5` makes the API's answer distinguishable from the resolver's, so
a resolver that consulted it returns an address where production returns the
`ErrNodeNameIsIP` sentinel.

The residue is not closeable through an outcome. A mutation that issues the
doomed call and then returns the correct sentinel anyway is identical by every
value that leaves `Resolve`: same error, same absence of an address, same cache
state. Only a call record separates them, and asserting on a call record is the
recording-double pattern `steps/daemon.go`'s header rejects. It stays a Go unit
test, and `node_ip_resolver_test.go` stays with it.

## Unmutatable by construction

`DaemonClient.TriggerMirror` returns nil on 202, non-202, transport failure and
a request that could not be built. No mutation can redden it in either suite,
so its five tests can never earn both-red evidence either way. See the NOT
WHOLE note in features/artifact-daemon.feature.

## Where each suite stands

| suite | status |
|---|---|
| storage_daemonset_durable_test.go | DELETED — 10 of 10 evidenced |
| artifact_locator_test.go | gap closed; ready for per-test attribution |
| volume_daemonset_test.go | gap closed; ready for per-test attribution |
| behavioral_permutations_test.go | 5 gaps closed; ready for attribution |
| daemonset_integration_test.go | 4 closed, 1 was not a gap; ready |
| daemon_client_test.go | 9 of 16 evidenced; TriggerMirror unmutatable — KEEP |
| node_ip_resolver_test.go | KEEP — residue not expressible as an outcome |
| errors_test.go, process_interruption_test.go | KEEP — pure functions |
| storage_daemonset_test.go, behavioral_volume_test.go, daemon_tls_test.go | mixed; 47 tests are unit tests by nature |
