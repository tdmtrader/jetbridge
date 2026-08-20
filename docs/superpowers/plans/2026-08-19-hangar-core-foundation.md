# Hangar Core Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the first core Hangar vertical slice: publish one immutable canonical filesystem tree to a strictly conforming GCS backend, retrieve and materialize that exact tree into an ordinary read-only task input, and preserve the current resource-cache adapter's fail-open behavior.

**Architecture:** Add a top-level, product-neutral `hangar` package for exact tree references, canonical tree admission, strict backend operations, and scoped materialization grants. Compose it beside—not inside—the existing `durable.Store` cache tier in the artifact daemon. The daemon publishes and reads through protected control-plane routes and accepts a short-lived, exact-reference capability on its node-local materialization route. JetBridge task pods request materialization in a fail-closed init container and mount the resulting input read-only. GCS is the only backend advertised for this strict profile in this slice; filesystem and S3 remain cache backends until they pass the same conformance contract.

**Tech Stack:** Go 1.25, `archive/tar`, `os.Root`, SHA-256, HMAC-SHA256, zstd, Google Cloud Storage generation preconditions, `net/http`, Kubernetes Pod APIs, Helm.

**Spec:** `docs/superpowers/specs/2026-08-19-hangar-core-foundation-design.md`

## Global Constraints

- Work only on `codex/hangar-core-foundation` in `.worktrees/hangar-core-foundation`. Do not edit the pipeline agents' worktree or unrelated untracked files in the repository root.
- Follow strict red-green-refactor TDD. Read `superpowers:test-driven-development/writing-good-tests.md` before adding tests. Record the command and expected failing reason before adding production code.
- Do not change `cmd/artifact-daemon/durable.Store`, its overwrite semantics, `DurableTier`'s bool API, or the existing cache routes. Hangar is an additive strict sibling.
- Core code and tests must never import `agent/*`, mention workflow/run/ticket/snapshot/checkpoint domain identifiers, or persist agent-specific metadata. `Scope` is opaque.
- Exact logical identity is `sha256:<64 lowercase hex>` over the deterministic uncompressed canonical tar. GCS generation pins a physical read/delete but never replaces digest verification.
- Strict errors remain distinguishable with `errors.Is`: not found, conflict, corruption, authorization, limit exceeded, and infrastructure. Context cancellation/deadline errors remain discoverable too.
- Publication is create-if-absent. A verified identical existing object is idempotent success; an unverifiable or different object at the same key is a conflict. No strict operation silently overwrites.
- Materialization stages and verifies a complete tree before exposing it, accepts only a daemon-derived destination under `<storage-path>/steps/<handle>/<volume>`, and makes regular files/directories read-only. It never accepts a caller-provided absolute path.
- The task main container and sidecars must not receive the Hangar signing key. A short-lived grant is bound to exact ref, handle, volume, and expiry. Tokens must not be logged or used as metric labels.
- Hangar defaults off. Enabling requires daemon TLS, a persisted capability key of at least 32 bytes, strict GCS configuration, and a distinct `concourse.dev/hangar-v1=ready` node label. Older/disabled daemons must never receive strict inputs.
- Every changed Pod test must assert that every `volumeMount` name has a matching Pod `Volume`. Hangar input/output path overlap is rejected because the first slice is read-only.
- K3s behavioral proof and real BusyBox-in-pod script proof are CI-only on macOS; write the tests/CI contract locally and explicitly report that tier as not run. Do not claim local K3s verification.
- Do not add aliases, leases/claims, writable workspaces, automatic output capture, DB migrations, S3 strict support, filesystem strict support, or agent-domain adapters in this slice.

---

### Task 1: Define the core exact-reference and store contract

**Files:**

- Create: `hangar/errors.go`
- Create: `hangar/ref.go`
- Create: `hangar/store.go`
- Test: `hangar/ref_test.go`
- Test: `hangar/architecture_test.go`

- [ ] Write focused tests first for `Scope`, `Digest`, `TreeRef`, and physical key validation. The accepted scope grammar is 1–63 ASCII bytes, starts with lowercase alphanumeric, and then contains lowercase alphanumeric, `.`, `_`, or `-`; it has no semantic enum. The key is `hangar/v1/scopes/<scope>/trees/sha256/<hex>.tar.zst`, optionally nested beneath a separately validated deployment prefix.
- [ ] Verify RED with `go test ./hangar -run 'Test(Scope|Digest|TreeRef|TreeKey)'`; the failure must be missing package/types, not malformed test setup.
- [ ] Implement these exact public shapes:

```go
type Scope string
type Digest string

type TreeRef struct {
    Scope      Scope  `json:"scope"`
    Digest     Digest `json:"digest"`
    Generation int64  `json:"generation"`
}

type TreeAttributes struct {
    Ref          TreeRef  `json:"ref"`
    StoredBytes  int64    `json:"stored_bytes"`
    LogicalBytes int64    `json:"logical_bytes"`
    CreatedAt    time.Time `json:"created_at"`
}

type Store interface {
    EnsureTree(context.Context, Scope, Digest, io.Reader, int64) (TreeAttributes, bool, error)
    InspectTree(context.Context, Scope, Digest, int64) (TreeAttributes, error)
    OpenTree(context.Context, TreeRef, int64) (io.ReadCloser, TreeAttributes, error)
    DeleteTree(context.Context, TreeRef) error
}
```

  `EnsureTree`'s boolean is `true` only for a newly committed object. Add sentinels `ErrNotFound`, `ErrConflict`, `ErrCorrupt`, `ErrUnauthorized`, `ErrLimitExceeded`, and `ErrInfrastructure`; wrap causes rather than replacing them.
- [ ] Add an architecture guard that scans every non-test and test Go file beneath `hangar/`, asserts it matched at least one file, parses imports, and rejects any import whose path contains `/agent/` or ends in `/agent`. Do not assert an exact file count.
- [ ] Verify GREEN with `go test ./hangar`, then run `go test . -run '^TestAgenticLayerIsImportedOnlyAtItsWiringPoint$'` to exercise the repository-wide boundary.
- [ ] Self-review against the spec, format, and commit as `feat(hangar): define exact tree storage contract`.

### Task 2: Admit and emit safe canonical filesystem trees

**Files:**

- Create: `hangar/tree.go`
- Test: `hangar/tree_test.go`

- [ ] Start from behavioral tests adapted from the proven `v0.2.230:agent/snapshot/archive_test.go`, renamed and reduced to the generic tree contract. Cover deterministic output across input order/mtime/uid/gid/mode variation; empty directories; regular files; contained relative symlinks; implicit parents; entry/content limits; cancellation; cleanup; truncated files; trailing bytes; duplicates; absolute/traversal/backslash/drive-like paths; symlink-parent writes; escaping/absolute symlinks; hard links; devices; FIFOs; sockets; setuid/setgid; PAX/xattr metadata; and host-equivalent collisions. Every guard-style table must assert it contains cases.
- [ ] Verify RED with `go test ./hangar -run 'Test(Canonicalizer|ValidateArchiveLimits|CanonicalArchiveByteLimit)'` and record that the canonicalizer API is missing.
- [ ] Implement a genericized, package-local version of the proven v3 canonicalizer using `os.Root`; do not import the old package. Public API:

```go
const DefaultMaxTreeEntries int64 = 100_000
const DefaultMaxTreeContentBytes int64 = 10 << 30

type TreeLimits struct {
    MaxContentBytes int64
    MaxEntries      int64
}

type Canonicalizer struct {
    MaxEntries      int64
    MaxContentBytes int64
    TempDir         string
}

type CapturedTree struct {
    Root        string
    ArchivePath string
    Digest      Digest
    ByteSize    int64
    FileCount   int64
}

func (Canonicalizer) Capture(context.Context, io.Reader) (*CapturedTree, error)
func (*CapturedTree) Close() error
func ValidateArchiveLimits(context.Context, io.Reader, TreeLimits) error
func CanonicalArchiveByteLimit(maxContentBytes, maxEntries int64) (int64, error)
func ValidateTempDir(string) error
```

  Canonical tar order is bytewise POSIX path order; metadata is fixed; allowed entry types are directory, regular file, and non-escaping relative symlink. Digest the exact canonical tar bytes.
- [ ] Run the focused tests, then the full `go test ./hangar`. Verify no scratch directory or file survives any failure path.
- [ ] Self-review for path-race, symlink-parent, descriptor-identity, overflow, and cleanup invariants; commit as `feat(hangar): canonicalize immutable trees safely`.

### Task 3: Implement the strict GCS profile

**Files:**

- Create: `hangar/gcs.go`
- Test: `hangar/gcs_test.go`
- Optional test-only helper: `hangar/gcs_fake_test.go`

- [ ] Write the backend conformance tests first. Cover invalid configuration; digest mismatch before upload; logical and compressed size limits; `DoesNotExist` conditional create; concurrent identical ensure; same-key unverifiable object conflict; exact metadata vocabulary; generation-pinned reads; generation/metageneration replacement races; missing, truncated, malformed-zstd, wrong digest, wrong declared size, and extra metadata corruption; cancellation; interrupted upload invisibility; scratch cleanup; and generation-conditioned delete. Assert GCS conditional values/query semantics, not only returned bytes.
- [ ] Verify RED with `go test ./hangar -run 'TestGCS'`; the failure must identify the absent constructor/backend.
- [ ] Implement:

```go
type GCSConfig struct {
    Bucket       string
    Prefix       string
    ScratchDir   string
    ZstdLevel    zstd.EncoderLevel
    ReadTimeout  time.Duration
    WriteTimeout time.Duration
}

func NewStorageClient(context.Context, string) (*storage.Client, error)
func NewGCSStore(*storage.Client, GCSConfig) (*GCSStore, error)
```

  `EnsureTree` hashes and zstd-compresses the logical canonical tar to private scratch before upload, sets exactly the logical digest/size/representation metadata, and writes with `DoesNotExist`. On 412 it fully verifies the extant object; identical is `(attrs, false, nil)`, while absence/change/corruption becomes `ErrConflict`. `OpenTree` pins `Generation`, fully downloads/decompresses/hashes/counts into private scratch before returning a reader, and never exposes bytes before verification. `DeleteTree` uses `GenerationMatch`. Wrap backend/transport failures with `ErrInfrastructure` while preserving underlying context errors.
- [ ] Run `go test ./hangar -run 'TestGCS' -count=1`, `go test ./hangar -count=1`, and any opt-in real-GCS integration only when its environment is already configured; do not require network credentials for unit tests.
- [ ] Self-review conditional operations, timeout cancellation, scratch lifetime, metadata exactness, and idempotency; commit as `feat(hangar): add generation-pinned GCS store`.

### Task 4: Add exact, scoped materialization grants and filesystem exposure

**Files:**

- Create: `hangar/grant.go`
- Create: `hangar/materializer.go`
- Test: `hangar/grant_test.go`
- Test: `hangar/materializer_test.go`

- [ ] Write tests first for a versioned HMAC-SHA256 grant bound to `TreeRef`, handle, volume, and expiry. Cover a minimum 32-byte key, tampering of every field, malformed/base64-variant tokens, wrong version, expiry/skew boundary, constant-time MAC comparison behavior through results, and safe error text that never includes the token.
- [ ] Verify RED with `go test ./hangar -run 'Test(MaterializationGrant|GrantSigner)'`.
- [ ] Implement `GrantSigner`/`GrantVerifier` over one shared key. The token is unpadded base64url of canonical JSON plus a 32-byte MAC, contains no secret, and verifies all requested fields rather than returning caller-controlled claims. Use an injected clock in tests and a default grant TTL of 15 minutes in runtime wiring.
- [ ] Write materializer tests first using a fake strict store. Cover exact open and digest recheck, read-only result modes (`0555` directories, `0444` regular files, symlink preserved), destination derivation under `steps/<handle>/<volume>`, invalid segments, absent/non-empty destination, no partial destination after any store/archive/chmod/rename failure, and concurrent duplicate materializations.
- [ ] Verify RED with `go test ./hangar -run 'TestMaterializer'`.
- [ ] Implement:

```go
type Materializer struct {
    Store        Store
    Canonicalizer Canonicalizer
    StoragePath  string
    MaxTreeBytes int64
}

func (m *Materializer) Materialize(context.Context, TreeRef, string, string) error
```

  Derive and validate the destination internally, open the exact generation, capture/verify the canonical tree into a private sibling staging directory, compare the captured digest with the ref again, recursively remove write bits without following symlinks, require an absent or empty final directory, and expose with one same-filesystem rename. Clean all staging state on failure.
- [ ] Run `go test ./hangar -run 'Test(MaterializationGrant|GrantSigner|Materializer)' -count=1` and `go test ./hangar -count=1`.
- [ ] Request a security-focused review of grant parsing, destination confinement, symlink handling, and rename races; fix every load-bearing finding. Commit as `feat(hangar): authorize and stage exact tree mounts`.

### Task 5: Compose Hangar in the artifact daemon without changing cache semantics

**Files:**

- Create: `cmd/artifact-daemon/hangar.go`
- Create: `cmd/artifact-daemon/hangar_handlers.go`
- Create: `cmd/artifact-daemon/hangar_test.go`
- Modify: `cmd/artifact-daemon/server.go`
- Modify: `cmd/artifact-daemon/main.go`
- Modify: `cmd/artifact-daemon/node_labeler.go` only if shared label cleanup needs a collection helper
- Test: `cmd/artifact-daemon/tls_test.go`
- Test: `cmd/artifact-daemon/durable_handlers_test.go`

- [ ] Add failing handler tests for disabled routes; protected publish/open routes; raw-tar publish returning 201 new/200 identical and exact JSON attributes; exact generation open that fully verifies before writing 200; distinct 400/401/404/409/413/422/503 mappings; materialization requiring a valid bearer grant; token-free logs/bodies; safe handle/volume destinations; and a store failure that leaves the target untouched. Include interrupted request and concurrent identical publish cases.
- [ ] Verify RED with `go test ./cmd/artifact-daemon -run 'TestHangar' -count=1`.
- [ ] Add a nil-safe Hangar service on `Server` and these versioned routes:

```text
POST /hangar/v1/scopes/{scope}/trees
GET  /hangar/v1/scopes/{scope}/trees/sha256/{digest}/generations/{generation}
POST /hangar/v1/materializations
```

  Publication/open are passed through the existing `protect` mTLS wrapper. Materialization is exempt from mTLS but independently requires a short-lived `Authorization: Bearer` grant bound to every request field. Decode bodies with explicit limits; never accept an absolute destination. Fully spool/verify open content before sending response headers so corruption maps to 422 rather than a partial 200.
- [ ] Add daemon flags `--hangar-enabled`, `--hangar-scratch-dir`, `--hangar-capability-key`, `--hangar-max-content-bytes`, and `--hangar-max-entries`. Enabling requires full TLS, `--durable-store=gcs`, non-empty bucket, absolute scratch directory, and a valid key file. Reuse the durable bucket/prefix/endpoint/timeout values to build a separate strict `hangar.GCSStore`; do not pass it through `DurableTier`.
- [ ] Build and validate the strict store before adding `concourse.dev/hangar-v1=ready`; remove both daemon-owned labels on graceful shutdown. Hangar-disabled startup and existing label behavior must remain unchanged.
- [ ] Add regression assertions that `/resource-caches/*`, `/durable/restore`, and a corrupt/unavailable durable cache still collapse to a miss exactly as before, while the same strict failures return non-2xx typed Hangar responses.
- [ ] Run `go test ./cmd/artifact-daemon -run 'Test(Hangar|TLS|Durable)' -count=1`, then `go test ./cmd/artifact-daemon ./cmd/artifact-daemon/durable -count=1`.
- [ ] Self-review route authentication, request limits, status mapping, startup validation, label ordering, and cache diffs; commit as `feat(artifact-daemon): serve strict Hangar trees`.

### Task 6: Materialize Hangar refs into ordinary read-only JetBridge tasks

**Files:**

- Modify: `atc/runtime/types.go`
- Modify: `atc/worker/jetbridge/config.go`
- Modify: `atc/worker/jetbridge/storage_daemonset.go`
- Modify: `atc/worker/jetbridge/container.go`
- Modify: `atc/atccmd/command.go`
- Test: `atc/worker/jetbridge/storage_daemonset_test.go`
- Test: `atc/worker/jetbridge/container_test.go`
- Test: `atc/worker/jetbridge/daemonset_integration_test.go`
- Test: `atc/worker/jetbridge/behavioral_volume_test.go`

- [ ] Add `HangarTree *hangar.TreeRef` to `runtime.Input`. Add failing tests that ordinary `Artifact` and `HangarTree` are mutually exclusive; Hangar input/output overlap is rejected; the task and sidecars mount Hangar inputs read-only; ordinary inputs remain writable as today; every mount name resolves to a Pod volume; and strict refs add required node affinity `concourse.dev/hangar-v1 In [ready]` without removing the cache-ready requirement.
- [ ] Verify RED with the smallest regular Go tests that do not start a database; use Ginkgo rather than `go test` for any DB-backed JetBridge suite per `AGENTS.md`.
- [ ] Split `BuildFetchInitContainers` into the existing `fetch-inputs` path for cache artifacts and a subsequent `materialize-hangar-inputs` init for strict refs. The strict init posts a bounded JSON batch to `/hangar/v1/materializations`; each item includes ref, handle, volume, and a freshly minted 15-minute grant. It exits nonzero on every non-2xx/partial error, retries only transport/503 failures with a bounded count, never logs payload/token, and does not mount the capability key.
- [ ] Add config/flags for `--kubernetes-hangar-enabled` and `--kubernetes-hangar-capability-key`. Load and validate the same persisted key at web startup. Refuse a Hangar-enabled runtime without the daemon hostPath/TLS/key. Hangar refs presented while disabled must fail pod construction, never degrade to an empty input.
- [ ] Keep the main task mount read-only, but let the init request the daemon to populate the node-local hostPath. Assert the init and main mounts all resolve to declared volumes in the same tests. Do not add a new pod volume through `StorageBackend`; reuse the already-created input volume.
- [ ] Exercise the generated shell under a real BusyBox pod in CI. Locally, inspect it with `sh -n` only as a supplemental check and record the CI-only gap; do not claim BusyBox proof from the host shell.
- [ ] Run focused unit tests, then `ginkgo -r -p ./atc/worker/jetbridge` if the local database prerequisites are available. Run `go test . -run '^TestAgenticLayerIsImportedOnlyAtItsWiringPoint$'` after the new core import.
- [ ] Self-review fail-closed behavior, token exposure, mount/volume consistency, read-only enforcement, overlap rejection, affinity, and disabled compatibility; commit as `feat(jetbridge): mount exact Hangar tree inputs`.

### Task 7: Add opt-in Helm rollout and operator documentation

**Files:**

- Modify: `deploy/chart/values.yaml`
- Modify: `deploy/chart/templates/artifact-daemon-daemonset.yaml`
- Modify: `deploy/chart/templates/artifact-daemon-tls-secret.yaml`
- Modify: `deploy/chart/templates/web-deployment.yaml`
- Modify: `deploy/chart/README.md`
- Test: `deploy/chart/tests/durable_store_test.go`
- Test: `deploy/chart/tests/flag_drift_test.go`
- Test: `deploy/chart/tests/securitycontext_test.go`
- Create: `deploy/chart/tests/hangar_test.go`
- Create: `docs/hangar.md`
- Modify: `docs/durable-artifact-storage.md`

- [ ] Add failing Helm tests for unchanged defaults; enabled render; required TLS; required `durable.store: gcs`; required bucket; generated or existing Secret key; web/daemon key mounts and flags; distinct readiness label; scratch path under the existing artifact-storage volume; Workload Identity/no credential Secret; flag drift; and unchanged security context/capabilities. The scan-based flag guard must assert it matched flags and must not assert an exact count.
- [ ] Verify RED with `go test ./deploy/chart/tests -run 'TestHangar' -count=1` (or the package's existing Helm test command if different).
- [ ] Add `artifactDaemon.hangar` values with `enabled: false`, `scratchPath`, `maxContentBytes`, `maxEntries`, and `capabilityTTL`. Reuse `artifactDaemon.durable` GCS bucket/prefix/endpoint/timeout rather than declaring a second backend credential block.
- [ ] Persist `hangar.key` in the existing auto-generated artifact-daemon TLS Secret: reuse its prior value through Helm `lookup`; create a cryptographically random 32-byte-or-stronger value only on first install. For `tls.existingSecret`, document and validate the required `hangar.key`. Mount it read-only only into web and artifact-daemon containers, never task containers.
- [ ] Document enablement, rollout order (daemon support/label first, then web emission), downgrade order (stop emission before daemon downgrade), GCS-only strict support, IAM/Workload Identity, limits/scratch sizing, typed failure semantics, read-only mount behavior, and the explicit difference between cache fail-open and strict fail-closed consumers. Correct the old statement that the durable tier settles the future core boundary while retaining its description of current cache semantics.
- [ ] Run `go test ./deploy/chart/tests -count=1`, render default and enabled charts, and inspect that disabled output contains no Hangar flags/volumes/labels.
- [ ] Because security context is intentionally unchanged, request a capability/security-context audit confirming canonicalization/materialization works with root plus only `DAC_OVERRIDE` and needs no added capability. Address any filesystem operation that would require more privilege.
- [ ] Commit as `feat(chart): gate the Hangar GCS foundation`.

### Task 8: End-to-end regression, review, and handoff

**Files:**

- Modify tests/docs above only as findings require
- Create: `docs/superpowers/reports/2026-08-19-hangar-core-foundation-verification.md`

- [ ] Add or finish one daemon-level behavioral test that sends a raw tree, publishes it to a strict fake GCS, deletes any producing node-local source, materializes the exact generation into another step/volume path, and compares tree bytes/types/modes. Inject absent, corrupt, and replacement-generation cases and prove they fail closed without a partial destination.
- [ ] Add a CI/K3s behavioral test contract for the same flow through a generated Pod. It must compare contents and assert every mount resolves to a volume. Mark it with the repository's existing live/K3s mechanism so it is not falsely reported as locally executed.
- [ ] Run formatting and targeted tests, then the broad non-K3s verification appropriate to this repository: `go test ./hangar ./cmd/artifact-daemon/durable ./cmd/artifact-daemon ./deploy/chart/tests -count=1`, `go test . -run '^TestAgenticLayerIsImportedOnlyAtItsWiringPoint$' -count=1`, and `make test-unit` (or the documented Ginkgo equivalent). Capture exact commands, exit codes, and elapsed times in the verification report.
- [ ] Review `git diff 069c8e8634c1f1cb6abcec2edf09f9a6934d2343...HEAD` for unrelated edits, stale docs, secret/token leaks, TODO/TBD placeholders, exact-count guards, cache semantic drift, agent imports, unpinned reads, unconditional deletes, and claims of unsupported S3/filesystem profiles.
- [ ] Dispatch a whole-branch architecture/security/code-quality reviewer on the most capable available model. Fix load-bearing findings once, run a scoped re-review, and record any adjudicated non-blocking residuals in the SDD ledger and verification report.
- [ ] Run `superpowers:verification-before-completion`, then `superpowers:finishing-a-development-branch`. Do not push, merge, or release without a new explicit user instruction; leave the reviewed commits on `codex/hangar-core-foundation` with a concise handoff.
