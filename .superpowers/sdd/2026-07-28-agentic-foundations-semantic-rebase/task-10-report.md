# Task 10 report — immutable bounded Hangar storage

## Status

**DONE_WITH_CONCERNS** — the Task 10 implementation and focused unit
verification are complete. The required full emulator target could not run in
this environment because no Docker daemon or external emulator endpoint was
available. This boundary is cataloged as `DEFERRED-004`.

## Files and behavior

- `agent/hangar/types.go` defines the provider-neutral `Store` contract,
  immutable references, attributes, and bounded error vocabulary.
- `agent/hangar/keys.go` accepts only the supported artifact kinds and
  canonical lowercase `sha256:<64 hex>` digests, mapping them to fixed
  `hangar/v1/.../*.tar.zst` keys.
- `agent/hangar/gcs.go` implements GCS conditional create and
  generation-pinned reads/deletes. It zstd-compresses bounded input while
  hashing it, writes exact representation metadata, verifies every stored read
  to scratch before exposing it, rejects malformed/corrupt/truncated/oversized
  data, and closes a blocked caller-owned read-closer only on cancellation.
- `agent/hangar/hangarfakes` provides a mutex-protected Store fake for callers.
- `agent/hangar/gcs_integration_test.go` drives the production GCS client
  against fake-gcs-server and covers immutable/idempotent writes, concurrent
  writers, conflict/corruption, truncated zstd, and recovery after deletion of
  all node-local scratch state.
- `Makefile` adds `test-hangar-integration`, using Docker by default or
  `CONCOURSE_HANGAR_TEST_GCS_ENDPOINT` for the in-cluster-compatible emulator.

## RED evidence

1. Before `types.go` and `keys.go`: `go test ./agent/hangar -run
   'Test(KindValidate|DigestValidate|Key|NewObjectRef)$' -count=1` failed with
   `no non-test Go files`.
2. After adding the GCS contract test and dependency declarations but before
   `gcs.go`: `go test -mod=mod ./agent/hangar -run
   'TestGCSStoreEnsureWritesVerifiedZstdObject$' -count=1` failed with missing
   `GCSConfig`, `GCSStore`, and GCS object adapter symbols.
3. Before the fake implementation: `go test ./agent/hangar/hangarfakes -run
   '^TestFakeStoreRecordsConcurrentCallsAndReturnsCopies$' -count=1` failed
   with `no non-test Go files`.

Each red failure was followed by the smallest corresponding implementation and
a green rerun. The initial non-escalated Go runs could not read the host Go
build cache; the evidence commands were rerun with approved host-cache access.

## Exact verification

- Passed: `go test ./agent/hangar -count=1`
- Passed: `go test ./agent/hangar/hangarfakes -count=1`
- Passed: `go test -tags=integration ./agent/hangar -run
  '^TestFakeGCSContainerRegistersCleanupBeforeReturningStartError$' -count=1
  -v`
- Passed: `git diff --check` before the implementation commit.
- Not runnable here: `make test-hangar-integration`. It was attempted once in
  the sandbox and once with approved host access; both ended at `ERROR: a
  running Docker daemon is required`. No external emulator endpoint was set.

## Self-review

Review round 1 focused on canonical identity, create-only semantics,
generation-pinned verification, metadata exactness, compressed and
uncompressed bounds, cancellation behavior, and the no-cache recovery path.
No correctness, security, corruption, or acceptance blocker was found. The
GCS SDK and existing `klauspost/compress` zstd dependency are the only storage
dependencies introduced.

## Deferred observations

- `DEFERRED-004`: execute the full emulator scenario where Docker is running
  or the in-cluster emulator endpoint is supplied. No code workaround was
  added for the absent external service.

## Commits

- `29e5215b13404b301f8dcdc1e98ae78c80c3341a`
