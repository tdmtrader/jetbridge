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
request context remains the single cancellation authority. Hangar will
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
| Object metadata is partial, nonempty-but-wrong, malformed, unexpected, or has the wrong digest/size/representation | Return `ErrCorrupt`/`ErrConflict` as appropriate; never repair it. |
| Generation or metageneration changes during read or conditional patch | Fail or re-inspect only for normal concurrent repair convergence; never patch an unpinned/newer object. |
| Context cancellation at any read, decode, hash, or patch stage | Return `ctx.Err()` and do not retry, delete, or mutate metadata. |

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
- Treat fake-GCS memory/capacity exhaustion as deployment capacity evidence,
  not as a Hangar repair condition or a reason to relax validation.
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
  ```

  The production mutation these tests must catch is routing `PUT` through the
  former `streamingClient`, or adding a detached/background context to hide a
  timeout. Do not assert source text or mock a method call; observe transport
  fields and the actual request context.

- [ ] **Step 2: Run the new focused tests and confirm RED**

  Run:

  ```bash
  go test ./atc/worker/jetbridge -run 'TestDaemonSnapshotUploadTransportWaitsForDurableResponseHeaders|TestSnapshotContentStorePutCancelsDedicatedUpload' -count=1
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
  checks as `snapshotHTTPClient`. In `putEndpointRequest`, replace only the
  client lookup with `store.daemon.snapshotUploadHTTPClient()`. Keep
  `http.NewRequestWithContext(ctx, ...)`, the caller-owned `io.NopCloser(file)`,
  bounded response-body drain, and spool close ownership unchanged. Do not
  alter `snapshotHTTPClient`, checkpoint clients, GET behavior, or the
  configured daemon/Hangar operation timeouts.

- [ ] **Step 4: Run focused tests and confirm GREEN**

  Run:

  ```bash
  go test ./atc/worker/jetbridge -run 'TestDaemonSnapshotUploadTransportWaitsForDurableResponseHeaders|TestSnapshotContentStorePutCancelsDedicatedUpload|TestSnapshotContentStore' -count=1
  ```

  Expected: PASS. Confirm the cancellation test returns `context.Canceled`,
  the upload transport has no response-header or whole-transfer timeout, and
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

  Add independently named cases for:

  ```go
  "inspect reports incomplete only for exactly empty metadata"
  "partial metadata remains corrupt and is never updated"
  "wrong empty-metadata bytes remain corrupt and are never updated"
  "compressed or uncompressed bounds reject before metadata update"
  "conditional generation and metageneration protect metadata patch"
  "racing successful repair converges through ordinary inspect"
  "context cancellation during pinned verification does not update metadata"
  "daemon retries ensure only after incomplete snapshot inspection"
  ```

  Extend the memory fake with an `UpdateMetadata` operation that records the
  supplied `storage.Conditions`, supports a one-shot race hook, and increments
  metageneration only when both generation and metageneration match. This is a
  stateful GCS double, not a mock assertion: tests assert resulting immutable
  content/metadata and the conditional fence.

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

  1. Read current attributes and attempt normal `inspect` first.
  2. Only when `errors.Is(err, ErrIncompleteCommit)` is true, retain the exact
     current generation and metageneration. Any other error follows the
     existing `ErrConflict`/ordinary error behavior.
  3. Read that generation only, reject negative/oversized compressed size using
     `maxCompressedRepresentation(maxUncompressedBytes)`, then zstd-decode
     through `contextReader` to a bounded `io.Discard`/hasher pipeline.
     Require the actual uncompressed size and SHA-256 to equal the requested
     digest; return `ErrCorrupt` on mismatch or malformed zstd.
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
     generation-pinned attributes. At every stage, immediately return
     `ctx.Err()` when cancellation wins.

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
  verified reference; all malformed/racing/cancelled cases fail closed without
  an unconditioned update; the daemon routes only the sentinel through
  `Ensure`.

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

  Record an emulator OOM, response timeout, or storage quota event as an
  environment capacity failure. Do not make the integration test target the
  historical `d53` object, and do not alter Hangar error classification to
  make an undersized emulator pass.

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
- No automatic background repair runs. The artifact daemon repairs only when a
  caller presents a local canonical snapshot archive and asks `Ensure`; a
  read-only `Inspect` remains read-only.
- The fake-GCS OOM observed on the large first-user upload is separate from
  transport and object-integrity correctness. Size a dedicated emulator or use
  production-grade storage for integration evidence.
- This track does not change snapshot schema, replication policy, retention,
  provider model selection, node definitions, or Git writeback behavior.
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
