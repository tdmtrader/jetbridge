# Durable artifact storage

A long-term home for resource caches, behind the artifact daemon.

## What it is

The artifact daemon keeps everything on a node's hostPath and sweeps it at a
TTL (default 2h). That is right for step outputs and wrong for resource caches,
and the difference is in their keys:

- A **step output** is addressed by a per-build container handle. Nobody will
  ever ask for that key again, so storing it durably costs a bucket and buys
  nothing.
- A **resource cache** is addressed by `rc-<id>`, derived from the resource
  type, version and params (`atc/worker/jetbridge/resource_cache_key.go:12`).
  The same key recurs on every build that wants the same thing — including on a
  node that has never seen it.

So only resource caches are promoted. The node-local copy stays a cache with a
TTL; the durable copy is what outlives the sweeper, the node, and the cluster.

It is **off by default**. With `artifactDaemon.durable.store` unset the daemon
behaves exactly as it did before.

## What it is not

This is not v3's `agent/hangar`, and the difference is deliberate.

`agent/hangar` held LLM workspace bytes, which have no upstream and genuinely
cannot be recreated. So it was fail-**closed**: content-addressed with a
mandatory digest over a canonical tar, an exact three-key metadata vocabulary
enforced on read, and a daemon that turned any non-miss error into HTTP 502.

Every artifact here is re-derivable by re-running the step that produced it.
Applying that strictness to derivable bytes converts a free cache miss into a
broken build. So this tier is fail-**open** in every path, name-keyed rather
than digest-keyed, and S3-compatible rather than GCS-only.

**Fail-open is the property to preserve through any future change.** A
durable-store miss, timeout, expired credential or corrupt object must all
degrade to "not here". `DurableTier`'s methods return `bool`, not `error`, so
there is no error for a caller to accidentally propagate.

## How it fits

The daemon's read path is a ladder (`cmd/artifact-daemon/server.go`,
`resolveOne`): registry → local filesystem → peer node → miss. The durable tier
is consulted only on the resource-cache handlers, and only after the local
lookup fails:

| Path | Behaviour |
|---|---|
| `POST /register` | If the key is `rc-<n>`, tar and upload, detached from the response. |
| `HEAD /resource-caches/{key}` | Local miss → ask the store → `200` with `X-Artifact-Source: durable`. |
| `GET /resource-caches/{key}` | Local miss → restore into `<storage>/steps/<key>`, register the alias, serve normally. |

Restoring under `steps/` is deliberate: the existing sweeper reclaims that tree
by mtime, so a warmed copy cannot grow without bound. It also makes the restored
cache visible to `resolveOne`'s step 2 and to peers. This is the trap that task
caches on hostPath already fell into — nothing reclaims them and node disk grows
monotonically — and the reason not to invent a new unswept directory.

## Layout

```
cmd/artifact-daemon/durable/     the store itself; no daemon imports
  durable.go                     Store interface, key validation, size limit
  fs.go                          filesystem backend
  s3.go                          S3-compatible backend
  spool.go                       body spooling for the S3 signer
cmd/artifact-daemon/
  durable_tier.go                policy: timeouts, fail-open, upload collapsing
  durable_config.go              flags → backend
```

The store package lives under `cmd/artifact-daemon/` because it belongs to the
daemon, not the ATC. Nothing in `atc/` imports it, so a deployment with the tier
off pays nothing for it.

## Interface

```go
type Store interface {
    Has(ctx context.Context, key string) (bool, error)
    Get(ctx context.Context, key string) (io.ReadCloser, bool, error)
    Put(ctx context.Context, key string, body io.Reader) error
    Delete(ctx context.Context, key string) error
}
```

A miss is reported through the `bool`, never as an error. An error means the
store itself failed. That distinction is what lets the tier above it fail open
without swallowing real faults silently.

Keys are validated against `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$`. The fs backend
joins the key onto a root directory, so `../` would escape it; S3 turns a slash
into a prefix, which would hide the object from `Delete`.

## Backends

**s3** — S3-compatible, so it also covers MinIO, Ceph, R2 and Backblaze. That
matters more than native support for any one cloud: a self-hosted JetBridge is
likelier to have a MinIO than an AWS account. `aws-sdk-go-v2` was already a
direct dependency (`atc/creds/ssm`, `atc/creds/secretsmanager`), so this added
one service package rather than a vendor tree.

Bodies are spooled to a temp file before upload. The SDK needs a seekable body
to sign and to retry, and holding a multi-gigabyte cache in memory on every node
of a DaemonSet is how a cluster OOMs.

**filesystem** — the whole store for a single-node install on an NFS or RWX
mount, and the backend every test in the package runs against. Writes go through
a temp file and a rename, so a crashed or over-limit upload never leaves a short
file that a later `Get` would serve as whole.

> A `hostPath` for `durable.path` is **not** durable — it is a second local
> copy. Point it at shared storage or use s3.

## Configuration

```yaml
artifactDaemon:
  durable:
    store: ""                    # "" | s3 | filesystem
    bucket: ""                   # s3
    prefix: ""                   # namespaces one bucket across clusters
    endpoint: ""                 # set for MinIO; empty for AWS
    region: "us-east-1"
    path: ""                     # filesystem
    timeout: "5m"
    maxBytes: 5368709120         # 0 disables
    existingSecret: ""           # AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
```

Credentials arrive as environment from `existingSecret`, never as flags — a flag
lands in the process table and in `kubectl describe pod`. On a managed cluster,
leave `existingSecret` empty and use IRSA or Workload Identity; nothing then
holds a long-lived key.

An incomplete config fails at `helm template`, not at runtime: a daemon that
starts, reports healthy and quietly caches nothing is a much worse failure.

## Failure modes

Every row degrades. None fails a build.

| Failure | Behaviour |
|---|---|
| Bucket unreachable / credentials expired | Logged, counted as `error`; `HEAD` and `GET` answer 404 and the build re-runs the get step. |
| Object absent | Ordinary miss. |
| Operation exceeds `timeout` | Context deadline; treated as a miss. |
| Upload fails | Logged; `POST /register` still returns 201, because the ATC is waiting on it and the build's next step depends on it. |
| Body exceeds `maxBytes` | `ErrTooLarge`, and nothing is stored. A truncated object would restore as a short-but-valid tar and fail a build far from the cause. |
| Restore fails part-way | Extracted through a temp sibling; nothing appears at the destination. |
| Two nodes restore the same key at once | `rename` onto a populated directory is treated as success — both copies are equally valid. |
| Concurrent uploads of one key | Collapsed to a single transfer by an in-flight set. |
| Daemon restarts mid-upload | The object is simply absent; the next request re-uploads. |

## Observability

`artifact_daemon_durable_operations_total{op,outcome}` — a counter, per node.

`op` is `has|restore|store|delete`; `outcome` is `hit|miss|ok|error|raced`.
Series are pre-initialised so an alert can fire from the first failure.

> **Aggregation.** This is a per-node counter of *operations*, so `sum()` across
> nodes is correct. That is not true of a store-*size* gauge on a DaemonSet:
> every node would report the same bucket total and `sum()` would multiply it by
> node count. Use `max by (…)` for any gauge added later. This has already
> caused one silent OOM in this project.

## Testing

25 subtests in `cmd/artifact-daemon/durable`, run against both backends through
one table — a store that only works on disk is not the feature.

The S3 backend is tested against a real `httptest` server speaking the real wire
protocol to the real SDK, matching `atc/creds/ssm`. No mocks, no cloud account.

`cmd/artifact-daemon/durable_tier_test.go` drives the real `Server` handlers,
including a two-node case (a producer uploads; a consumer with empty storage
restores and serves) and a `brokenStore` that fails every operation, asserting
404 rather than 502.

Every property is mutation-verified: making a miss an error, uploading
non-resource-caches, returning 502 on a broken store, and dropping the chart's
`int64` coercion each fail the suite.

**Cannot be tested locally:** real S3/GCS credentials, IRSA and Workload
Identity, and behaviour against a bucket under lifecycle policy.

## Rollout

The tier is off by default, so upgrading changes nothing until
`artifactDaemon.durable.store` is set.

To enable: set the values, roll the DaemonSet, watch
`artifact_daemon_durable_operations_total{outcome="error"}`. A cold node's first
`GET` of a known cache should log `durable.restore` and report `restored`.

**Kill switch:** set `store: ""` and roll. The daemon reverts to node-local plus
peers immediately; nothing else depends on the tier, and the objects in the
bucket are inert.

## Open questions

1. **Reclaim.** Nothing deletes from the bucket today. `DurableTier.Delete`
   exists and is tested but has no caller. The options are a bucket lifecycle
   policy (cheap, dumb, no code), or wiring it to core's existing
   `atc/gc/resource_cache_collector.go`, which already knows when a resource
   cache becomes unreferenced — accurate, but it couples the ATC to the daemon's
   store. **Start with a lifecycle policy**; revisit if cost matters.
2. **Should step outputs ever be promoted?** They are excluded because their
   keys are per-build. If a use case appears (say, retaining a release artifact),
   it needs a stable key, which is a different feature.
3. **GCS.** `cloud.google.com/go/storage` is not currently a dependency, and
   S3-interop covers GCS. Add a native backend only if interop proves
   insufficient.
