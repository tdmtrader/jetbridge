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

It is **off by default**, and it is **not configurable from the Helm chart**:
the chart passes the daemon no `--durable-*` flag, and its old values group is
a removed key (ADR-0008). The tier is set with the daemon's own flags (see
[Configuration](#configuration)). With `--durable-store` unset the daemon
behaves exactly as it did before.

## Relationship to core Hangar

This resource-cache tier is one consumer policy, not the future boundary for
all durable artifacts. Core Hangar now provides a separate opt-in strict path
for exact immutable tree references; see [Hangar exact tree
storage](hangar.md). Its store is GCS or disk.

The two share **one storage interface** and nothing else. This tier is a thin
wrapper over the same `hangar/objectstore.Client` (create-if-absent, stat,
exact open, list) with the same two backends, `hangar/gcs` and `hangar/disk`,
but always against its **own** bucket or disk namespace and its own client
instance. The cache, the strict inputs and the outputs are three namespaces,
and the daemon and web refuse to start when any two are equal (ADR-0002).

Every resource-cache artifact described here is re-derivable by re-running the
step that produced it. Applying Hangar's strictness to these derivable bytes
would convert a free cache miss into a broken build. This tier therefore
remains fail-**open** in every path and name-keyed, while exact Hangar inputs
fail closed on absence, corruption, conflict, authorization, limits, or
infrastructure failure.

**Fail-open is the property to preserve through any future change.** A
durable-store miss, timeout, expired credential or corrupt object must all
degrade to "not here". `DurableTier`'s methods return `bool`, not `error`, so
there is no error for a caller to accidentally propagate.

## Status: complete

**Store, content key, ATC↔daemon wiring, retention and reclaim are all landed
and tested. Nothing has run against a real bucket yet.** The store is a wrapper
over the shared object interface (GCS or disk); see [Backends](#backends).

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

Policy is `--durable-retention CLASS=DURATION`, repeatable. **A class with no
entry is kept forever**,
and an unset policy reclaims nothing at all. Silence has to mean keep, because
the alternative is that a typo in a class name empties a bucket.

#### Why JetBridge reclaims rather than a bucket lifecycle rule

A lifecycle rule is the obvious answer and was the first design. Two things
argued it down:

- **Nothing can check the rule is right.** The period lives as a string an
  operator types into a cloud console, and it has to match a prefix this code
  composes. A rule with the wrong prefix matches nothing, deletes nothing and
  reports no error. The store simply grows.
- **It does not exist for the disk store.** A disk namespace has no lifecycle
  mechanism, so that backend would have no reclaim path at all.

The retention class stays a **key prefix**, not object metadata, because GCS
lifecycle rules match prefixes: a bucket rule written against
`resource-caches/` is the backstop, and it can only be written if the class is
in the name.

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

`DurableClassResourceCache` (`atc/worker/jetbridge/resource_cache_key.go`) is
the class a `--durable-retention` entry must name; an entry naming any other
class names a class nothing produces and is inert.

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
  store.go                       the wrapper over hangar/objectstore.Client;
                                 builds its own gcs or disk clients
cmd/artifact-daemon/
  durable_tier.go                policy: timeouts, fail-open, upload collapsing
  durable_config.go              flags → store, and the namespace check
```

The store package lives under `cmd/artifact-daemon/` because it belongs to the
daemon, not the ATC. Nothing in `atc/` imports it, so a deployment with the tier
off pays nothing for it. It may import `hangar/objectstore`, `hangar/gcs` and
`hangar/disk` and nothing else under `hangar/` — never `hangar/output` — and
nothing under `hangar/` imports it (`architecture_test.go`,
`TestDurableTierAndHangarAreSeparateStores`).

## Interface

```go
type Attributes struct {
    Key     string
    Size    int64
    Updated time.Time // the object's creation time
    Version string    // the object's generation, in decimal
}

type Store interface {
    Stat(ctx context.Context, key string) (Attributes, bool, error)
    Get(ctx context.Context, key string) (io.ReadCloser, Attributes, bool, error)
    Put(ctx context.Context, key string, body io.Reader) error
    Delete(ctx context.Context, key string) error
    DeleteVersion(ctx context.Context, key, version string) error
    List(ctx context.Context, fn func(Attributes) error) error
}
```

A miss is reported through the `bool`, never as an error. An error means the
store itself failed. That distinction is what lets the tier above it fail open
without swallowing real faults silently.

Over the object interface:

- `Stat` is `StatCurrent`; not-found is a miss.
- `Get` is `StatCurrent` then `OpenExact` of that generation, so an object
  expired and recreated in between reads as a miss rather than a mixture; it
  reports the generation it opened.
- `DeleteVersion` is `DeleteExact` of a generation the caller read, never a
  re-stat; a newer object at the key is kept.
- `Put` is create-if-absent. Objects are immutable and keys are content-derived,
  so a key already present — or a lost race to a concurrent writer of the same
  key (`ErrPreconditionFailed`) — is **success**, and the upload is skipped when
  a stat finds the key first.
- `Delete` is `StatCurrent` then `DeleteExact` of that generation; absent, or
  replaced in between, is success.
- `List` pages the namespace by `(key, generation)`.

Because objects are immutable, a truncated, corrupt or hostile object is not
healed by the next producer's upload. A restore that fails because of the
object deletes exactly the generation it read (`DeleteVersion`, never a re-stat
of the current one), so the next producer can put a good copy back.

`List` is there for reclaim. Storage is the only authority on what storage
holds: a database can be restored, rebuilt or diverge, so anything reconciling
a bucket against one has to enumerate the bucket.

Keys are validated against `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,254}$` per segment,
with at most one class-prefix segment. The daemon also joins the local alias of
a restore onto its `steps/` root with the same validator.

## Backends

**gcs** — native Cloud Storage, through `hangar/gcs`. The client uses
Application Default Credentials, which on GKE is Workload Identity — no key
exists to leak. `--durable-endpoint` points it at an emulator (tests and brine)
and is empty in production.

**disk** — a **dedicated** persistent-disk store (`cmd/hangar-store`, its own
PVC and store ID) through `hangar/disk`, as its `cache` role. That instance is
started with `--cache-namespace` and nothing else: it refuses the input and
output namespaces beside the cache, and its credentials file names only a
`cache` credential, which may create, stat, read, list and delete inside the
cache namespace. The cache never shares the strict store's process, index lock
or concurrency slots.

At startup the disk identity probe is bounded to seconds
(`durable.DefaultProbeTimeout`), not the transfer timeout.

The S3-compatible and filesystem backends are gone. The S3 one needed an HMAC
key or IRSA and a spool to disk for the signer; the filesystem one was only
durable on shared storage and was unsafe on GCS Fuse. Neither had a deployment.

### The delete the daemon holds

This tier is the only place on a node that constructs a delete client
(`gcs.NewDeleteClient` / `disk.NewDeleteClient`), and only for the cache
namespace: the cache is fail-open and the daemon expires its own objects by
retention class. It never holds delete over the input or output namespaces —
the startup check refuses a cache namespace equal to either, and the disk store
confines the cache role to its namespace. `hangar/architecture_test.go` fixes
the only callers of the delete constructors: this package and the output
reclaimer.

## Configuration

The Helm chart does not configure this tier. Its `artifactDaemon.durable`
values were removed (ADR-0008), and setting them fails the render. A
deployment that wants the tier runs the daemon with its own flags:

```
--durable-store=""                     # "" | gcs | disk
--durable-bucket=""                    # the cache's own bucket or disk namespace
--durable-endpoint=""                  # gcs: emulator only; disk: the store's HTTPS origin
--durable-store-id=""                  # disk only
--durable-token-file=""                # disk only: the cache-role credential
--durable-ca-cert=""                   # disk only
--durable-timeout=5m
--durable-max-bytes=5368709120         # 0 disables
--durable-maintenance-interval=15m
--durable-retention=CLASS=DURATION     # repeatable
```

`--durable-bucket` must differ from `--hangar-bucket` when strict inputs are
enabled, and from the output bucket; the daemon exits at startup otherwise.
Web takes the same three names (`--kubernetes-artifact-daemon-cache-bucket`,
`--kubernetes-hangar-input-bucket`, `--kubernetes-hangar-output-bucket`) and
refuses the same way.

An incomplete config fails at daemon startup, which exits rather than serving:
a daemon that starts, reports healthy and quietly caches nothing is a much
worse failure. So does a store that answers and refuses — a rejected
credential, or a disk store reporting another identity. A correct config whose
store cannot be reached at startup is different: the tier starts unconnected
(every operation a miss, and the daemon does not advertise the tier to the
ATC), logs `durable-store-unavailable`, and keeps connecting in the background
with backoff up to 5m; the retention pass starts once it connects.

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
| Key already stored | `Put` succeeds without writing; the first object stands. |
| Object fails to restore because of the object (refused/hostile entry, truncated body, malformed tar, store-reported corruption) | 404; exactly the generation that was read is expired, so the next producer recreates it. A local failure (disk full, permissions, descriptors) or a store/network failure while reading expires nothing. |

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

`cmd/artifact-daemon/durable/durable_test.go` runs one conformance table over
three substrates: the in-memory object client (`hangar/gcstest`), the real GCS
adapter against an in-process fake-gcs-server, and a real disk store behind its
authenticated server as the cache role. No mocks, no cloud account.

`cmd/artifact-daemon/durable_tier_test.go` covers the tier: a store/restore
round trip through a real `Server`'s tar writer, a `brokenStore` that fails
every operation, a nil tier, upload collapsing under concurrency, and a hostile
object that is refused and expired. `durable_config_test.go` covers the
namespace check.

`atc/worker/jetbridge/brine/features/daemon-durable.feature` drives the real
daemon binary against its own cache bucket on an in-process GCS emulator,
including the retention pass over objects backdated server-side.

**Cannot be tested locally:** real GCS credentials, Workload Identity, and
behaviour against a bucket under lifecycle policy.

## Rollout

Resource caches use this tier independently of Hangar. Hangar names its own
store and shares no connection values with this tier; follow the daemon-first
rollout in [the Hangar guide](hangar.md).

**Kill switch:** drop `--durable-store` (or set it to `""`) and roll. The daemon reverts to node-local plus
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
