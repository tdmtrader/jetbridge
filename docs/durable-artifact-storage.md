# Durable artifact storage

A long-term home for resource caches, behind the artifact daemon.

## What it is

The artifact daemon keeps everything on a node's hostPath and sweeps it at a
TTL (default 2h). That is right for step outputs and wrong for resource caches,
and the difference is in their keys:

- A **step output** is addressed by a per-build container handle. Nobody will
  ever ask for that key again, so storing it durably costs a bucket and buys
  nothing.
- A **resource cache** identifies content — a resource type at a version with
  given params — so the same thing is wanted again on every build, including on
  a node that has never seen it.

So only resource caches are eligible, and the ATC is what decides that — it
names a cache with `DurableKey()` and says nothing about anything else. The
node-local copy stays a cache with a TTL; the durable copy is what outlives the
sweeper, the node, and the cluster.

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

## Status: complete

**Store, content key, ATC↔daemon wiring, retention and reclaim are all landed
and tested. Nothing has run against a real bucket yet.**

`db.ResourceCache.DurableKey()` returns `rc-<sha256>` over the cache's identity,
persisted as `resource_caches.durable_key`
(`atc/db/migration/migrations/1773105504_add_resource_cache_durable_key.up.sql`)
and computed by `durableCacheKey` in `atc/db/resource_cache.go`. The rest of this
section records why the obvious key was wrong, because the reasoning is not
recoverable from the code alone.

### Do not wire this to `rc-<id>`

`rc-<id>` is `fmt.Sprintf("rc-%d", cacheID)` over `resource_caches.id`
(`atc/worker/jetbridge/resource_cache_key.go:12`), and that column is a
surrogate: `CREATE SEQUENCE resource_caches_id_seq`
(`atc/db/migration/migrations/1510262030_initial_schema.up.sql:437`). Rows are
hard-deleted by `CleanUpInvalidCaches` (`atc/db/resource_cache_lifecycle.go:77`,
`sq.Delete("resource_caches")`) whenever a cache falls out of every in-use set,
and the next build re-inserts the same (config, version, params) tuple under a
**new** id.

Two consequences, both of which only appear once the copy is permanent:

1. **Orphans with no reclaim path.** Every GC cycle strands the object under the
   old id. The daemon has no way to learn which ids the ATC deleted, so nothing
   can ever remove them.
2. **Wrong bytes after a database restore.** Restore Postgres from a snapshot
   and the sequence rewinds while the bucket keeps every object ever written.
   Id 42 is re-minted for a different tuple, and the store answers with the old
   tuple's content. A get step then returns something that is not the version it
   asked for, and nothing downstream can detect it.

Today `rc-<id>` is safe **because** the local copy expires at a 2h TTL — the
blast radius is bounded by the sweeper. A permanent copy removes exactly that
bound, which is why this is a defect of the durable tier and not of the existing
daemon.

A per-row UUID — v7 included — fixes only the second consequence. It is still
minted per row, so it still changes on delete-and-recreate: the durable copy
becomes unfindable at exactly the moment it would be useful, and the result is a
store that is safe and never hits.

### The key

`durableCacheKey` hashes what makes two caches interchangeable: the parent
resource cache's own key, the resource type name, the source hash, the version
digest and the params hash. It is computed in `FindOrCreateResourceCache`, which
is the only place all of those are in scope, and stored as a column because
`FindResourceCacheByID` has neither source nor params and could not recompute it.

Rows predating the column hold `NULL`, which reads as "not eligible for durable
storage". The migration cannot backfill them — the source lives in
`resource_configs` and the custom type chain has to be walked in Go — so the next
`FindOrCreateResourceCache` for that tuple fills it in.

The parent must contribute **its own key**, recursively. Do not reach for
`ResourceCache.BaseResourceType()` (`atc/db/resource_cache.go`): it flattens the
custom-type chain to its base, so two different versions of a custom type with
identical source and params collide — the same wrong-bytes bug by a shorter
route. `resource_cache_durable_key_test.go` pins this.

### Eligibility is the ATC's call, not the daemon's

The daemon takes the key as an opaque string. It does not parse it, and it does
not decide what deserves to be kept — it cannot, because "is this re-derivable"
and "will anything ask for this again" are questions about the artifact's
meaning.

An earlier cut had the daemon match `^rc-\d+$` to decide eligibility, which put
the ATC's naming scheme in a second binary with no compiler holding the halves
together and made a key-format change a lockstep redeploy of every node. It is
gone. The ATC supplies a durable name for the artifacts it wants kept and stays
silent about the rest; that silence is the entire protocol.

### Two phases, two questions

The find path asks two different questions and must keep them on separate
channels.

| Path | Question | Answer |
|---|---|---|
| `HEAD /resource-caches/{key}` | *Do you have these bytes on disk right now?* | Local registry only. **Never** the durable store. |
| `POST /durable/restore` | *Can you get them?* | Pulls from the store and makes its own answer true before returning. |
| `POST /register` `{"durable":true}` | — | Tars and uploads, detached from the response. |

**Why HEAD must never consult the store.** Every daemon sees the same bucket. If
HEAD answered from it, all of them would report 200 for anything ever stored;
`ProbeResourceCache` races to the first responder, so the winner would be
arbitrary and the node affinity the probe exists to provide would be gone — a
cache resident on node A served by node B pulling it back out of object storage.
Worse, a durable 200 would tell the ATC to skip the get step while no node holds
the bytes, making bucket availability a hard build dependency in a design whose
whole premise is that it is not.

So the ATC probes locally, and only on a miss — and only when it has a content
key, and only when a daemon advertised `X-Durable-Tier` — asks one daemon to
warm. Candidates are ranked by rendezvous hash of the **node name**, so
concurrent builds converge on one node rather than each pulling a private copy,
and a rolling update (which replaces every pod IP at once) does not reshuffle
every key's owner.

A failed warm suppresses further attempts for that key for 60s. This is not an
optimisation: a get step's own `timeout:` does not bound the warm, and
`attemptGet` re-enters every `GetResourceLockInterval` (5s) while waiting for the
resource lock, so without suppression a degraded bucket would cost a full warm
timeout every five seconds indefinitely.

**Rolling upgrade.** Capability rides the HEAD response, so an old daemon (which
sets no header) receives exactly zero requests to a route it does not have. An
old ATC sends no `durable` field, and `encoding/json` drops it — nothing is
promoted. Neither direction needs a lockstep deploy.

**The restore key travels in the request body, not the path.** As a path segment
it would have to be un-escaped before being joined onto the storage root, where
`%2e%2e%2f%2e%2e%2f` decodes to `../../` and escapes it entirely.

### Retention

Objects are named `<class>/<identity>` — today only `resource-caches/rc-<sha>`.
The daemon walks the store on an interval, deleting objects in a configured
class that are older than its retention period, and reporting what remains.

Policy is `--durable-retention CLASS=DURATION`, repeatable, surfaced in the chart
as `artifactDaemon.durable.retention`. **A class with no entry is kept forever**,
and an unset policy reclaims nothing at all. Silence has to mean keep, because
the alternative is that a typo in a class name empties a bucket.

#### Why JetBridge reclaims rather than a bucket lifecycle rule

A lifecycle rule is the obvious answer and was the first design. Two things
argued it down:

- **Nothing can check the rule is right.** The period lives as a string an
  operator types into a cloud console, and it has to match a prefix this code
  composes — including the store's own `--durable-prefix`, which is easy to
  forget. A rule with the wrong prefix matches nothing, deletes nothing and
  reports no error. The store simply grows.
- **It does not exist for `store: filesystem`.** A shared NFS or RWX volume has
  no lifecycle mechanism, so that backend had no reclaim path at all.

Note what this argument is *not*. The rejected design earlier in this document
is reference-based deletion — driving `Delete` from
`atc/gc/resource_cache_collector.go`, which fires when a cache becomes
unreferenced, which is exactly when the durable copy becomes valuable. That
remains wrong. Age-based reclaim run by JetBridge has none of that problem, and
conflating the two arguments is how this design initially landed in the wrong
place.

A bucket rule remains a perfectly good backstop for when JetBridge is not
running. The two do not conflict: both only ever delete what is already past its
age.

#### Properties worth preserving

- **Everything uncertain keeps.** No timestamp, no class prefix, an unconfigured
  class, or a flat key that merely spells a class name — all kept.
  `RetentionPolicy.expired` is the only thing between a configuration mistake and
  an emptied store, and every branch in it answers "keep".
- **A zero timestamp must never expire.** It reads as 1970, so a naive age check
  would find every object ancient and delete the store on its first pass. This is
  why `Attributes.Updated` exists and why all three backends populate it.
- **Age is since last WRITE, not last read.** Object stores do not track reads,
  so there is no LRU. `promoteToDurable` deliberately has no `Has()`
  short-circuit, so producing a cache again rewrites it and resets its age — the
  policy therefore expires caches that have stopped being *produced*.
- **No leader election.** Deleting an absent key is not an error by the `Store`
  contract, so several daemons reclaiming at once is correct. The jitter and the
  interval exist to make it cheap, not correct.
- **Deletes are capped per pass** and the cap is logged when it bites. A reclaim
  that silently stops early reads exactly like one that finished.
- **One walk, two jobs.** Enumeration is the expensive part, so residency
  measurement shares it. The gauges therefore describe the store as the pass
  leaves it.

`DurableClassResourceCache` (`atc/worker/jetbridge/resource_cache_key.go`) and
the class documented in `deploy/chart/values.yaml` must agree, or a retention
entry names a class nothing produces and is inert.
`TestDocumentedPrefixMatchesTheCodesRetentionClass` holds them together.

### The two key namespaces

| Name | Shape | Why |
|---|---|---|
| local alias | `rc-<sha>` | Becomes a direct child of `steps/`, the only thing the sweeper reclaims. A nested one would never be swept. |
| durable key | `resource-caches/rc-<sha>` | Names an object in a bucket; the prefix is what a lifecycle rule acts on. |

Both travel explicitly on the wire (`key` and `durable_key`) and the daemon
derives neither from the other. `durable_key`'s presence is the whole
eligibility protocol — absent means "do not keep it", which is what a cache
predating the `durable_key` column sends.

### Where restores land

`<storage>/steps/<key>`. This is deliberate: the existing sweeper reclaims that tree
by mtime, so a warmed copy cannot grow without bound. It also makes the restored
cache visible to `resolveOne`'s step 2 and to peers. Task caches on hostPath are
the cautionary case — nothing reclaims them and node disk grows monotonically —
and the reason not to invent a new unswept directory.

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
type Attributes struct {
    Key     string
    Size    int64
    Version string // GCS generation, S3 versionId; empty on a filesystem
}

type Store interface {
    Stat(ctx context.Context, key string) (Attributes, bool, error)
    Get(ctx context.Context, key string) (io.ReadCloser, bool, error)
    Put(ctx context.Context, key string, body io.Reader) error
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, fn func(Attributes) error) error
}
```

A miss is reported through the `bool`, never as an error. An error means the
store itself failed. That distinction is what lets the tier above it fail open
without swallowing real faults silently.

`Stat` rather than a bare `Has` because a caller that needs the size or wants
to pin one particular write should not have to download the body to get it.
`Version` is the backend's own name for a write — a GCS generation, an S3
versionId — and is empty where the backend has no such concept rather than
invented.

`List` is there for reclaim. Storage is the only authority on what storage
holds: a database can be restored, rebuilt or diverge, so anything reconciling
a bucket against one has to enumerate the bucket. A store without `List` can
only delete what something already remembered, which is exactly the set that
does not leak.

Keys are validated against `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$`. The fs backend
joins the key onto a root directory, so `../` would escape it; S3 turns a slash
into a prefix, which would hide the object from `Delete`.

## Backends

**gcs** — native Cloud Storage, and the default choice on GCP.

Cloud Storage does speak S3 through its XML API, but interop signs with SigV4,
which needs an HMAC key: a long-lived secret tied to a service account that
somebody has to mount and rotate. The native client uses Application Default
Credentials, which on GKE is Workload Identity — no key exists to leak. That is
the whole reason this backend exists rather than pointing the S3 one at
`storage.googleapis.com`. There is deliberately no way to pass a credential to
`NewGCS`.

No spooling: Cloud Storage's writer is a plain `io.WriteCloser` that chunks as
it goes. A truncated body cancels the context rather than closing the writer,
because `Close` finalises whatever was written and would publish a short object.

**s3** — S3-compatible, so it covers AWS, MinIO, Ceph, R2 and Backblaze. It
earns its place even with GCS as the primary: theborg is not GCP, and MinIO is
a better on-prem object store than a filesystem over NFS. `aws-sdk-go-v2` was
already a direct dependency (`atc/creds/ssm`, `atc/creds/secretsmanager`).

Bodies are spooled to a temp file before upload. The SDK needs a seekable body
to sign and to retry, and holding a multi-gigabyte cache in memory on every node
of a DaemonSet is how a cluster OOMs. `MaxAttempts` is configuration rather than
a property of the backend: for the daemon a miss is free so 2 is right, but a
caller whose bytes have no upstream should ask for more.

**filesystem** — the whole store for a single-node install on an NFS or RWX
mount, and the backend every test in the package runs against. Writes go through
a temp file and a rename, so a crashed or over-limit upload never leaves a short
file that a later `Get` would serve as whole.

> A `hostPath` for `durable.path` is **not** durable — it is a second local
> copy. Point it at shared storage, or use `gcs`/`s3`.
>
> A **GCS Fuse** mount is also not a safe target for the `filesystem` backend:
> both `FS.Put` and `DurableTier.Restore` get their atomicity from `os.Rename`,
> and Fuse implements rename as copy-then-delete. Use the `gcs` backend, which
> talks to the API directly.

## Configuration

```yaml
artifactDaemon:
  durable:
    store: ""                    # "" | gcs | s3 | filesystem
    bucket: ""                   # gcs, s3
    prefix: ""                   # namespaces one bucket across clusters/consumers
    endpoint: ""                 # set for MinIO; empty for GCP and AWS
    region: "us-east-1"          # s3 only
    path: ""                     # filesystem
    timeout: "5m"
    maxBytes: 5368709120         # 0 disables
    existingSecret: ""           # AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
```

Credentials arrive as environment from `existingSecret`, never as flags — a flag
lands in the process table and in `kubectl describe pod`. On a managed cluster,
leave `existingSecret` empty and use IRSA or Workload Identity; nothing then
holds a long-lived key. With `store: gcs` on GKE it is not needed at all.

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

### Daemon — per-node counters

`artifact_daemon_durable_operations_total{op,outcome}`, where `op` is
`has|restore|store|delete|list` and `outcome` is `hit|miss|ok|error|raced`.
Series are pre-initialised so an alert can fire from the first failure.

`artifact_daemon_durable_reclaimed_objects_total` and `…_bytes_total` — what this
daemon's retention sweep deleted.

All of these are this node's own work, so `sum()` across nodes is correct.

### Daemon — shared-store gauges

`artifact_daemon_durable_store_objects`, `…_store_bytes`, and
`…_store_oldest_object_age_seconds`.

> **Aggregation.** These describe the SHARED store, not the node. Every daemon
> reports the same number, so `sum()` across a DaemonSet multiplies the store by
> node count. Use `max by (…)`. This has already caused one silent OOM in this
> project, which is why the warning is repeated in every `Help` string.

Oldest-object age is the signal that retention is working: it should plateau near
the configured period, and rise without bound if a class is producing objects
that no policy covers.

A failed enumeration leaves the previous gauge values standing rather than
zeroing them — a zero is indistinguishable from an empty store, and "the bucket
went to zero" is the worst false alert these could produce.

### ATC — is the tier earning its egress?

Four counters partition every resource-cache lookup that reaches a daemon:

| Metric | Meaning |
|---|---|
| `resource cache local hits` | a node already had it — the fast path |
| `durable warm hits` | pulled from the store — the tier paying off |
| `durable warm misses` | asked the store, nothing there or it failed |
| `durable warm suppressed` | skipped; a recent warm for this key failed |

They sum to the number of lookups, so any ratio taken from them is meaningful.
Suppressed rising is the bucket-unhealthy signal: the negative cache is absorbing
a retry loop that would otherwise cost a warm timeout every few seconds per
waiting get step.

## Testing

All three backends run the same conformance table — a store that only works on
disk is not the feature. Each of GCS, S3 and filesystem passes the same ten
behaviours.

The cloud backends are tested against real `httptest` servers speaking the real
wire protocols to the real SDKs, matching `atc/creds/ssm`. No mocks, no cloud
account.

One thing the GCS fake taught: the Go client uploads over the JSON API but
fetches object bodies over the **XML** API at `/<bucket>/<object>`. A fake
serving only the JSON routes accepted every write and missed every read.

`cmd/artifact-daemon/durable_tier_test.go` covers the tier: a store/restore
round trip through a real `Server`'s tar writer, a real unavailable filesystem
state, a nil tier, and upload collapsing at a real S3 protocol boundary.

Mutation-verified: making a miss an error, dropping the chart's `int64`
coercion, and removing the S3 retry cap each fail the suite.

**Cannot be tested locally:** real S3/GCS credentials, IRSA and Workload
Identity, and behaviour against a bucket under lifecycle policy.

## Rollout

Nothing to roll out yet: the store has no caller. The flags and chart values
exist and are validated, so the configuration surface can be reviewed now, but
setting `artifactDaemon.durable.store` currently only constructs the backend.

**Kill switch:** set `store: ""` and roll. The daemon reverts to node-local plus
peers immediately; nothing else depends on the tier, and the objects in the
bucket are inert.

## Open questions

1. **True LRU.** Retention is by age since last write; object stores expose no
   last-access time, and JetBridge does not track reads either. Keeping what is
   hot rather than what is recent would mean touching an object on every warm,
   which costs a write per cache hit. Not obviously worth it — revisit if
   measurements show useful caches expiring.
2. **Should step outputs ever be promoted?** They are excluded because their
   keys are per-build. If a use case appears (say, retaining a release artifact),
   it needs a stable key, which is a different feature.
3. **Warm concurrency across nodes.** Rendezvous hashing makes builds on
   *different* nodes agree on one warm owner, and the daemon collapses concurrent
   restores of one key. Nothing collapses two ATC processes racing before either
   has registered the alias — both would restore, and one loses the rename. That
   costs a duplicate download, never a wrong answer, so it is left alone until
   measured.
