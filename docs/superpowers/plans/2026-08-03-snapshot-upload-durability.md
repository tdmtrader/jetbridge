# Snapshot upload durability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make large Jetbridge snapshot uploads wait for the daemon's durable
Hangar acknowledgement while retaining caller cancellation, and safely recover
the one known interrupted GCS commit shape without weakening immutable-object
validation.

**Architecture:** ATC gets a dedicated client used only for snapshot `PUT`s;
unlike the existing bidirectional streaming client, it has no
`ResponseHeaderTimeout`, because a daemon correctly withholds response headers
until it has compressed, uploaded, and verified the complete archive. The
request context remains the sole client-side whole-transfer deadline; existing
connection establishment and daemon/Hangar operation limits remain. Hangar will
recognize only an object with *exactly empty* custom metadata as an
interrupted pre-metadata commit, generation-pin and fully verify its bounded
zstd bytes, then conditionally attach the canonical three metadata entries.
All other malformed, partial, wrong, changing, or cancelled objects fail
closed.

**Tech Stack:** Go, `net/http`, Google Cloud Storage Go client, zstd,
artifact-daemon, existing in-memory GCS object fake, Helm operations docs.

## Problem and evidence

The first-user repository snapshot was about 225 MiB. Its ATC-to-daemon `PUT`
hit the current 30-second `ResponseHeaderTimeout` in
`atc/worker/jetbridge/daemon_client.go` before the daemon could finish its
durable Hangar work. The upload request was cancelled even though neither the
caller context nor the configured Hangar write timeout had expired. This is a
transport-layer timeout mismatch, not a content-size validation failure.

That cancelled upload left a real Hangar object at the digest ending in
`d53` with bytes present but no custom metadata. Hangar correctly refuses it
today because the immutable metadata vocabulary is not exact, but the object
cannot be safely reused by a normal create-only write. The repair must be
limited to the exact empty-metadata state and prove the existing,
generation-pinned content before altering metadata. Never delete, overwrite,
or reuse the existing `d53` object while implementing or testing this track.

## Accepted semantics

| Situation | Required outcome |
| --- | --- |
| Snapshot `PUT` takes longer than 30 seconds before response headers, while its request context remains live | Keep waiting; the dedicated upload client has no `ResponseHeaderTimeout` and no whole-transfer `Client.Timeout`. |
| Caller cancels or reaches its own deadline | `PUT` stops through `http.NewRequestWithContext`; no background retry or detached upload continues. |
| Snapshot GET, checkpoint, and ordinary daemon probes | Retain their existing bounded clients and header-timeout behavior. |
| Create-only collision sees exactly `len(customMetadata) == 0` | Return/recognize `hangar.ErrIncompleteCommit`, generation-pin the object, enforce compressed and uncompressed bounds, zstd-decompress, count, hash, and conditionally write exactly the canonical metadata. |
| Repair verification succeeds and conditional metadata patch wins | Re-run normal immutable inspection and return the resulting verified attributes. |
| Another writer repaired the same generation first | Re-inspect; return success only when the ordinary immutable verification now succeeds for the requested digest. |
| Object metadata is partial, nonempty-but-wrong, malformed, unexpected, or has the wrong digest/size/representation | Return `ErrCorrupt`; never repair it. `ErrConflict` is reserved for an unconverged generation/precondition race. |
| Generation or metageneration changes during read or conditional patch | Fail or re-inspect only for normal concurrent repair convergence; never patch an unpinned/newer object. |
| Context cancellation before the conditional metadata update is issued | Return `ctx.Err()` and do not retry, delete, or mutate metadata. |
| Cancellation races with an already-issued conditional metadata update | Return `ctx.Err()`, do not retry, and do not publish a reference when cancellation is observed before the client publication linearization point. The server-side patch may have committed; a later exact `Ensure` may converge only through ordinary immutable inspection. |
| Failed, incomplete, corrupt, or pre-linearization-cancelled upload | Publish no snapshot reference or declared output. Orphan bytes without canonical metadata are not a platform snapshot. Cancellation after the final response/context check does not revoke an already linearized success. |

## Global constraints

- Do not read benchmark case inputs, ground truth, notes, or rubrics.
- Do not delete, overwrite, retry against, or manually reuse the poisoned real
  `d53` Hangar object.
- Preserve the existing create-only immutable namespace and the exact
  three-entry metadata vocabulary.
- `ErrIncompleteCommit` denotes only the exactly empty custom-metadata state;
  it must not become a permissive alias for arbitrary invalid metadata.
- Do not add a local Docker provider. Before any Docker-backed integration
  command, follow the repository's Docker-on-theborg instructions in
  `AGENTS.md`, use theborg's dind setup, and tear it down afterward. The
  testcontainers K8s suites remain CI-only from this Mac.
- Treat fake-GCS memory/capacity exhaustion as deployment capacity evidence
  only when emulator or platform telemetry corroborates OOM, quota, or disk
  exhaustion. A response timeout by itself remains a failure requiring
  transport, operation-deadline, and backend-health diagnosis; neither case is
  a Hangar repair condition or a reason to relax validation.
- Use one implementer and one independent blocking-only reviewer per task;
  follow the semantic-rebase session context's maximum three review rounds.

---

## File map

| File | Responsibility |
| --- | --- |
| `atc/worker/jetbridge/daemon_client.go` | Own distinct probe/stream/upload HTTP transports and expose the upload client without changing non-upload traffic. |
| `atc/worker/jetbridge/snapshot_content_store.go` | Route snapshot `PUT` requests to the no-header-timeout upload client while retaining request-context cancellation. |
| `atc/worker/jetbridge/snapshot_content_store_test.go` | Exercise the real request boundary and protect the client separation/cancellation contract. |
| `agent/hangar/types.go` | Declare the narrowly scoped incomplete-commit sentinel. |
| `agent/hangar/gcs.go` | Detect exactly metadata-less collisions, generation-pin and verify them, conditionally patch metadata, and converge safely. |
| `agent/hangar/gcs_test.go` | Extend the in-memory object client and cover accepted repair, races, malformed states, limits, and cancellation. |
| `agent/hangar/gcs_integration_test.go` | Exercise the same repair through the GCS-compatible adapter when the controlled integration environment is available. |
| `cmd/artifact-daemon/server.go` | Route a snapshot's `ErrIncompleteCommit` inspection state back through `Ensure` using the local canonical archive. |
| `cmd/artifact-daemon/server_test.go` | Prove daemon snapshot adoption repairs only the recognized state and propagates all unsafe states. |
| `docs/operations/hangar.md` | Document `storage.objects.update` as a narrowly required conditional metadata-repair permission and its operational limits. |

### Task 1: Dedicated snapshot upload transport

**Files:**

- Modify: `atc/worker/jetbridge/daemon_client.go: DaemonClient, NewDaemonClient, snapshot client accessors`
- Modify: `atc/worker/jetbridge/snapshot_content_store.go: putEndpointRequest`
- Test: `atc/worker/jetbridge/snapshot_content_store_test.go`

**Consumes:** The existing `DaemonClient` discovery/TLS construction,
`SnapshotContentStore.putEndpointRequest`, and caller-owned `context.Context`.

**Produces:** `snapshotUploadHTTPClient() (*http.Client, error)`, a dedicated
client used exclusively by snapshot `PUT`; it has a cloned TLS-aware transport,
no `Client.Timeout`, and `ResponseHeaderTimeout == 0`. Existing
`snapshotHTTPClient()` remains the bounded client for GET/checkpoint traffic.

- [ ] **Step 1: Write the failing behavior tests**

  Add these real request-boundary tests to
  `atc/worker/jetbridge/snapshot_content_store_test.go`:

  ```go
  func TestDaemonSnapshotUploadTransportWaitsForDurableResponseHeaders(t *testing.T) {
      client := NewDaemonClient(lagertest.NewTestLogger("upload"), fake.NewSimpleClientset(), "cicd", "artifact-daemon", 7780, nil)
      upload, err := client.snapshotUploadHTTPClient()
      require.NoError(t, err)
      require.Zero(t, upload.Timeout)
      transport := upload.Transport.(*http.Transport)
      require.Zero(t, transport.ResponseHeaderTimeout)

      stream, err := client.snapshotHTTPClient()
      require.NoError(t, err)
      require.Positive(t, stream.Transport.(*http.Transport).ResponseHeaderTimeout)
  }

  func TestSnapshotContentStorePutCancelsDedicatedUpload(t *testing.T) {
      started := make(chan struct{})
      client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(r *http.Request) (*http.Response, error) {
          close(started)
          <-r.Context().Done()
          return nil, r.Context().Err()
      }))
      // Configure client.snapshotUploadClient with that transport, create a
      // store, cancel its Put context after <-started, and require
      // context.Canceled. A successful response or a detached request is a bug.
  }

  func TestSnapshotContentStorePutCancellationBeforePublicationWins(t *testing.T) {
      responseBodyRead := make(chan struct{})
      allowResponseEOF := make(chan struct{})
      // Return a 201 response with the durable Hangar header. Its body signals
      // the first Read and then blocks before EOF. Start Put, wait for
      // <-responseBodyRead, cancel the request context, then close
      // allowResponseEOF. Require context.Canceled and a nil location slice.
      // This exercises the boundary after daemon acknowledgement but before
      // the client's final publication check.
  }
  ```

  The production mutation these tests must catch is routing `PUT` through the
  former `streamingClient`, adding a detached/background context to hide a
  timeout, or returning a durable location without a final context check after
  the response is completely drained and closed. Do not assert source text or
  mock a method call; observe transport fields, the actual request context, and
  the returned location slice.

- [ ] **Step 2: Run the new focused tests and confirm RED**

  Run:

  ```bash
  go test ./atc/worker/jetbridge -run 'TestDaemonSnapshotUploadTransportWaitsForDurableResponseHeaders|TestSnapshotContentStorePutCancel' -count=1
  ```

  Expected: the first test cannot find `snapshotUploadHTTPClient`, and/or the
  `PUT` cancellation test proves the old bounded streaming client is selected.
  If a test passes before production changes, make it distinguish the upload
  client from the streaming client rather than accepting equivalent fakes.

- [ ] **Step 3: Implement the minimal client split**

  In `DaemonClient`, add a private `snapshotUploadClient *http.Client`. In
  `NewDaemonClient`, clone the already TLS-configured base transport twice:

  ```go
  streamingTransport := transport.Clone()
  streamingTransport.ResponseHeaderTimeout = 30 * time.Second
  uploadTransport := transport.Clone()
  uploadTransport.ResponseHeaderTimeout = 0

  streamingClient:      &http.Client{Transport: streamingTransport},
  snapshotUploadClient: &http.Client{Transport: uploadTransport},
  ```

  Implement `snapshotUploadHTTPClient` with the same nil and initialization
  checks as `snapshotHTTPClient`. In `putEndpointRequest`, replace the client
  lookup with `store.daemon.snapshotUploadHTTPClient()`. Keep
  `http.NewRequestWithContext(ctx, ...)`, the caller-owned `io.NopCloser(file)`,
  bounded response-body drain, and spool close ownership unchanged. After the
  response body is fully drained and closed, check `ctx.Err()` immediately
  before returning the status/durable header. That check is the publication
  linearization point: cancellation already observable there returns the
  context error and no caller can publish the location; cancellation after it
  does not revoke the acknowledged success. Do not alter `snapshotHTTPClient`,
  checkpoint clients, GET behavior, or configured daemon/Hangar operation
  timeouts.

- [ ] **Step 4: Run focused tests and confirm GREEN**

  Run:

  ```bash
  go test ./atc/worker/jetbridge -run 'TestDaemonSnapshotUploadTransportWaitsForDurableResponseHeaders|TestSnapshotContentStorePutCancel|TestSnapshotContentStore' -count=1
  ```

  Expected: PASS. Confirm both cancellation tests return `context.Canceled`
  with no location, including the acknowledged-response/pre-publication race;
  the upload transport has no response-header or whole-transfer timeout; and
  the existing GET/stream contract remains bounded.

- [ ] **Step 5: Run the task checkpoint and commit**

  Run:

  ```bash
  go test ./atc/worker/jetbridge -count=1
  git diff --check
  ```

  Commit only Task 1 files:

  ```bash
  git add atc/worker/jetbridge/daemon_client.go atc/worker/jetbridge/snapshot_content_store.go atc/worker/jetbridge/snapshot_content_store_test.go
  git commit -m "fix(snapshot): wait for durable upload acknowledgement"
  ```

### Task 2: Repair the exact metadata-less GCS incomplete commit

**Files:**

- Modify: `agent/hangar/types.go: Hangar error sentinels`
- Modify: `agent/hangar/gcs.go: object interface, conflict path, verification, conditional update adapter`
- Modify: `agent/hangar/gcs_test.go: memory object client and repair cases`
- Modify: `agent/hangar/gcs_integration_test.go: GCS-compatible repair case`
- Modify: `cmd/artifact-daemon/server.go: ensureSnapshotInHangar`
- Modify: `cmd/artifact-daemon/server_test.go: durable snapshot routing`
- Modify: `docs/operations/hangar.md: required permissions and recovery guidance`

**Consumes:** Task 1's uninterrupted snapshot `PUT`, the existing canonical
key/digest validation, `GCSStore.Ensure`, `GCSStore.Inspect`, zstd bounded
verification, and daemon local snapshot archive.

**Produces:** `hangar.ErrIncompleteCommit` only for exact empty custom
metadata; a collision repair path that returns ordinary verified
`hangar.Attributes` after an exact conditional metadata patch; daemon routing
that attempts `Ensure` when `Inspect` reports that narrow sentinel.

- [ ] **Step 1: Write the failing Hangar and daemon behavior tests**

  Add table-driven tests using the existing in-memory object client. Seed a
  raw, valid zstd object at the requested digest with `map[string]string{}`.
  The principal success test must call `Ensure` with the same canonical
  content, then assert all of the following observable outcomes:

  ```go
  attrs, err := store.Ensure(ctx, KindSnapshot, digest, bytes.NewReader(content), int64(len(content)))
  require.NoError(t, err)
  require.Equal(t, seeded.Generation, attrs.Ref.Generation)
  require.Equal(t, testMetadata(digest, int64(len(content)), representationZstd), objects.object(key).metadata)
  require.Equal(t, seeded.Metageneration+1, objects.object(key).metaGen)
  ```

  Here the final argument is a maximum, matching the production `Ensure`
  contract. The exact expected length for incomplete-commit comparison is the
  `uncompressedBytes` value that `Ensure` already measures while reading and
  hashing this caller source before its create-only write collides; do not
  reinterpret `maxUncompressedBytes` as a declared length or change the public
  interface merely to add one.

  Add independently named cases for:

  ```go
  "inspect reports incomplete only for exactly empty metadata"
  "partial metadata remains corrupt and is never updated"
  "wrong empty-metadata bytes remain corrupt and are never updated"
  "compressed or uncompressed bounds reject before metadata update"
  "conditional generation and metageneration protect metadata patch"
  "racing successful repair converges through ordinary inspect"
  "context cancellation before patch dispatch does not update metadata"
  "context cancellation while patch is in flight publishes no reference before linearization and does not retry"
  "daemon retries ensure only after incomplete snapshot inspection"
  ```

  Extend the memory fake with an `UpdateMetadata` operation that records the
  supplied `storage.Conditions`, supports a one-shot race hook, and increments
  metageneration only when both generation and metageneration match. This is a
  stateful GCS double, not a mock assertion: tests assert resulting immutable
  content/metadata and the conditional fence. The partial-metadata,
  wrong-bytes, malformed-zstd, digest, and size cases must assert
  `errors.Is(err, ErrCorrupt)` and `!errors.Is(err, ErrConflict)`; only the
  disappearance/generation/metageneration/precondition race cases assert
  `ErrConflict`.

- [ ] **Step 2: Run the focused tests and confirm RED**

  Run:

  ```bash
  go test ./agent/hangar -run 'TestGCSStore.*(Incomplete|MetadataLess|Repair)' -count=1
  go test ./cmd/artifact-daemon -run 'Test.*Snapshot.*Incomplete' -count=1
  ```

  Expected: the empty-metadata collision returns the existing corruption or
  conflict error; there is no `ErrIncompleteCommit`, no conditional metadata
  update operation, and the daemon treats the state as terminal.

- [ ] **Step 3: Add the narrow sentinel and conditional-update boundary**

  In `agent/hangar/types.go`, add:

  ```go
  ErrIncompleteCommit = errors.New("hangar: incomplete commit")
  ```

  In `validateStoredMetadata`, return `ErrIncompleteCommit` only when
  `len(metadata) == 0`; retain `ErrCorrupt` for every nonempty metadata map
  that is not exactly the canonical three-entry vocabulary. Extend
  `objectHandle` with a conditional metadata-update method, for example:

  ```go
  UpdateMetadata(context.Context, map[string]string) (objectAttrs, error)
  ```

  Implement it in `storageObjectHandle` with the handle already fenced by:

  ```go
  storage.Conditions{
      GenerationMatch:     current.Generation,
      MetagenerationMatch: current.Metageneration,
  }
  ```

  and `storage.ObjectAttrsToUpdate{Metadata: canonicalMetadata}`. Map GCS
  precondition failure to an ordinary conflict/convergence path; do not use an
  unconditional `Update`.

- [ ] **Step 4: Implement bounded, generation-pinned repair**

  In the create-conflict path of `GCSStore.Ensure`:

  1. Preserve the caller-source `uncompressedBytes` and verified digest already
     measured by `Ensure`, then read current attributes and attempt normal
     `inspect` first.
  2. Only when `errors.Is(err, ErrIncompleteCommit)` is true, retain the exact
     current generation and metageneration. Any other error follows the
     explicit taxonomy: preserve `ErrCorrupt` for partial/malformed/wrong
     immutable state; use `ErrConflict` only for disappearance, generation or
     metageneration changes, or an unconverged conditional-precondition race.
     Update `inspectAfterCreateConflict` or split this path so it does not wrap
     an `ErrCorrupt` collision as `ErrConflict`.
  3. Read that generation only, reject negative/oversized compressed size using
     `maxCompressedRepresentation(maxUncompressedBytes)`, then zstd-decode
     through `contextReader` to a bounded `io.Discard`/hasher pipeline. Require
     `actualBytes == uncompressedBytes`, where `uncompressedBytes` is the local
     caller-source measurement taken earlier in this same `Ensure` invocation;
     the public `maxUncompressedBytes` argument remains only an upper bound.
     Also require the computed SHA-256 to equal the requested digest; return
     `ErrCorrupt` on either mismatch or malformed zstd.
  4. Before update, recheck that the current attributes still have the same
     generation, metageneration, and exactly empty metadata. Use the conditional
     generation-plus-metageneration patch to write *only*:

     ```go
     map[string]string{
         metadataUncompressedSHA256: string(digest),
         metadataUncompressedBytes:  strconv.FormatInt(actualBytes, 10),
         metadataRepresentation:     representationZstd,
     }
     ```

  5. On a failed conditional patch, re-run normal `inspect` once. It may
     converge only if another writer installed the exact canonical metadata for
     the requested content. Otherwise return `ErrConflict`/`ErrCorrupt`; never
     loop indefinitely, rewrite bytes, or delete the object.
  6. On a successful patch, call normal `inspect` and return its verified,
     generation-pinned attributes. Return `ctx.Err()` whenever cancellation is
     observed. Cancellation before update dispatch guarantees no patch.
     Cancellation after dispatch may race with a committed conditional update;
     never retry or return a reference from that cancelled call, and allow only
     a later exact `Ensure` to converge through ordinary inspection.

  Keep ordinary `Inspect` read-only: it exposes `ErrIncompleteCommit` for the
  daemon to route, rather than silently mutating state during a read. In
  `Server.ensureSnapshotInHangar`, treat only `ErrNotFound` and
  `ErrIncompleteCommit` as reasons to open the local canonical archive and call
  `Ensure`; all other errors remain wrapped and returned without a write.

- [ ] **Step 5: Update operational permissions and recovery documentation**

  In `docs/operations/hangar.md`, replace the generic permissions wording with
  an explicit least-privilege list: bucket/object inspect/list as required by
  deployment, create, read, generation-conditional delete, and
  `storage.objects.update` solely for the exact metadata-less repair. State
  that the repair performs a full bounded, generation-pinned content digest
  verification and a generation-plus-metageneration conditional metadata
  patch; it never patches partial metadata or replaces bytes. State that
  fake-GCS capacity failures require capacity/retention remediation, not this
  repair, and preserve the warning never to reuse the historical `d53` object.

- [ ] **Step 6: Run focused tests and confirm GREEN**

  Run:

  ```bash
  go test ./agent/hangar -run 'TestGCSStore.*(Incomplete|MetadataLess|Repair)|TestNewStorageClient' -count=1
  go test ./cmd/artifact-daemon -run 'Test.*Snapshot.*(Incomplete|Hangar)' -count=1
  ```

  Expected: the exact empty-metadata object repairs once and returns a pinned
  verified reference; malformed state preserves `ErrCorrupt`; unconverged
  identity/precondition races return `ErrConflict`; cancelled cases fail closed
  without an unconditioned update or published reference; and the daemon routes
  only the sentinel through `Ensure`.

- [ ] **Step 7: Run broad checks and the controlled adapter integration test**

  Run:

  ```bash
  go test ./agent/hangar -count=1
  go test ./cmd/artifact-daemon -count=1
  go test ./atc/worker/jetbridge -count=1
  go test ./deploy -count=1
  git diff --check
  ```

  Then, only with a dedicated persistent fake-GCS deployment that has enough
  memory/disk for the test payload, run:

  ```bash
  make test-hangar-integration
  ```

  Record an emulator OOM, storage quota, or disk-exhaustion event as an
  environment capacity failure only when corroborated by emulator/platform
  telemetry. A response timeout alone is a failed test requiring diagnosis of
  transport, operation deadline, and backend health. Do not make the
  integration test target the historical `d53` object, and do not alter Hangar
  error classification to make an undersized emulator pass.

- [ ] **Step 8: Review checkpoint and commit**

  Have one independent reviewer inspect only the Task 2 diff for: exact empty
  metadata classification, generation/metageneration fencing, bounded
  decompression, digest verification, cancellation propagation, daemon routing,
  and least-privilege documentation. Fix only Critical, High, or
  acceptance-blocking findings; record nonblocking hardening in the deferred
  item catalog. Stop after the first passing review or the session context's
  three-round maximum.

  Commit only Task 2 files:

  ```bash
  git add agent/hangar/types.go agent/hangar/gcs.go agent/hangar/gcs_test.go agent/hangar/gcs_integration_test.go cmd/artifact-daemon/server.go cmd/artifact-daemon/server_test.go docs/operations/hangar.md
  git commit -m "fix(hangar): repair verified metadata-less commits"
  ```

## Integration constraints, risks, and exclusions

- The ATC upload transport change is intentionally not a global timeout
  relaxation. Only snapshot `PUT` waits for the daemon's durable response;
  ordinary GET, checkpoint, and probe clients retain their current bounded
  header waits.
- Repair is not a general object salvage API. It handles the only safely
  identifiable interrupted state—no custom metadata at all—and cannot infer
  intent from partial or foreign metadata.
- Incomplete-commit recovery is byte-preserving storage finalization, not a
  semantic node-output repair hook. It does not invoke a model, alter declared
  output bytes, or consume the single bounded schema-repair opportunity.
- No automatic background repair runs. The artifact daemon repairs only when a
  caller presents a local canonical snapshot archive and asks `Ensure`; a
  read-only `Inspect` remains read-only.
- This track persists only an already explicit, authorized platform-snapshot
  request. It does not create or schedule snapshots, alter immutable snapshot
  IDs, bypass team/resource authorization, change exact server-side rerun
  bindings, or require a repository picker.
- A cancelled, failed, incomplete, or corrupt upload returns no durable
  snapshot location/reference to its caller. Physical orphan bytes remain
  outside snapshot authority until ordinary inspection verifies canonical
  metadata and a later authorized request publishes the reference.
- The fake-GCS OOM observed on the large first-user upload is separate from
  transport and object-integrity correctness. Size a dedicated emulator or use
  production-grade storage for integration evidence.
- The transport split is wire- and storage-schema compatible. New ATC with an
  old daemon waits longer but uses the same request; old ATC with a new daemon
  retains the old client timeout but cannot corrupt an object; valid existing
  objects remain readable. No metadata vocabulary changes.
- This track does not change snapshot schema, replication policy, retention,
  node definitions, or Git writeback behavior. It also does not alter model or
  budget resolution: the node author pins the exact model, callers cannot
  replace it, unavailable models fail, and callers may explicitly override the
  execution budget.
- Before claiming completion, verify the focused and broad commands above,
  `git diff --check`, clean ownership of changed files, and the independent
  review result. Do not claim K8s behavioral coverage from this Mac; follow
  the repository's Docker-on-theborg constraints in `AGENTS.md`.

## Self-review

- Spec coverage: Task 1 covers the dedicated no-header-timeout PUT transport
  and request cancellation. Task 2 covers the exact sentinel, pinned bounded
  verification, conditional metadata patch, race convergence, failure cases,
  daemon routing, permissions, tests, and integration constraints.
- Placeholder scan: no implementation step delegates undefined work; each
  behavior has concrete files, commands, and acceptance observations.
- Type consistency: Task 2 defines `ErrIncompleteCommit` and
  `UpdateMetadata` before its repair and daemon consumers; Task 1 defines
  `snapshotUploadHTTPClient` before `putEndpointRequest` uses it.
