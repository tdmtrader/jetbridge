# Task 10 report — immutable bounded Hangar storage

## Status

**ACCEPTED** — the Task 10 implementation, focused unit verification, and
required emulator-backed target are complete. Independent blocking-only review
round 1 found no blocking findings. `DEFERRED-004` was resolved by running the
target against a temporary fake-gcs-server deployment on Borg.

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
- Passed against a temporary Borg emulator:
  `CONCOURSE_HANGAR_TEST_GCS_ENDPOINT=http://127.0.0.1:54443/storage/v1/
  make test-hangar-integration`
  - immutable/idempotent write;
  - different-object conflict;
  - concurrent-writer convergence;
  - truncated-zstd rejection;
  - recovery after complete node-local scratch loss.
- Passed: `git diff --check` before the implementation commit.
- Cleanup verified: the temporary namespace
  `codex-hangar-it-019fa137` no longer exists on Borg.

## Implementer self-review

The implementer self-review focused on canonical identity, create-only semantics,
generation-pinned verification, metadata exactness, compressed and
uncompressed bounds, cancellation behavior, and the no-cache recovery path.
No correctness, security, corruption, or acceptance blocker was found. The
GCS SDK and existing `klauspost/compress` zstd dependency are the only storage
dependencies introduced.

## Independent review

- Round 1 reviewer: Terra, read-only review of
  `27e26dd8fae3989932c8a919c10177b67c252312..3e1e2c6cfc8a3c01c02b43e6a7e843fe3873c935`.
- Blocking findings: none.
- Deferred observations: none material.
- Verdict: **ACCEPTED**. No correction or round-2 review was required.

## Deferred observations

- None. `DEFERRED-004` is resolved by the Borg-backed target evidence above.

## Commits

- `29e5215b13404b301f8dcdc1e98ae78c80c3341a`
  (`feat(hangar): add immutable GCS object storage`)
- `705dcf1d375f9209a6a086b64ea9b5a01cb04f33`
  (`docs(hangar): record Task 10 verification`)
