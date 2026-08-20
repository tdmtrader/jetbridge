# Hangar as a Core Durable Artifact Foundation

**Status:** Approved architectural direction; intentionally high-level
**Date:** 2026-08-19
**Scope:** Establish the boundary for a shared durable blob and filesystem-tree
service. This is a starting point for a subsequent design and implementation
plan, not a complete API or migration specification.

## Purpose

Hangar should be the common durable artifact substrate for JetBridge. It stores
bytes in durable storage and makes an exact object or filesystem tree available
to a container. That capability is useful to ordinary CI, an agentic platform,
and future consumers; it is not intrinsically agentic.

The agentic platform will build stricter artifact records over Hangar, but it
should not own or duplicate the storage daemon, cloud backends, transfer path,
or container-materialization machinery.

## Decision

Hangar belongs in core. There should be one core data-plane contract and
implementation with multiple consumer policies, rather than separate cache and
agent storage stacks. A deployment may isolate consumers into different Hangar
instances, credentials, or namespaces without changing that shared contract.

```text
  CI resource caches       Agent snapshots/evidence       Future consumers
  fail-open adapter        strict domain adapter          domain adapter
            \                    |                         /
             +-------------------+------------------------+
                                 |
                     Core Hangar data plane
            durable objects, manifests, transfer, mounts
                                 |
                  filesystem / GCS / S3-compatible store
```

The current durable resource-cache tier and v3's `agent/hangar` represent two
policies over much of the same infrastructure:

- a derivable CI cache may treat an unavailable durable copy as a miss and
  recreate it;
- an authoritative agent artifact must use an exact immutable reference and
  fail when the referenced content cannot be retrieved and verified.

Those policies must remain different. The daemon, backend implementations, and
container integration should be reusable wherever their capabilities satisfy
the consumer's required profile.

## Boundary

### Hangar owns

Hangar is responsible for generic storage and container integration:

- streaming durable reads and writes across supported backends;
- stable object identities and the ability to read one exact immutable write;
- size and logical-content digest verification, atomic publication, and
  explicit conflict, corruption, absence, and infrastructure failures;
- opaque scopes that separate consumers without teaching Hangar what those
  consumers mean;
- immutable filesystem-tree references with safe relative paths and exact
  content;
- safely materializing a referenced object or tree for a container;
- generic, conditional physical-retention operations on which consumers may
  build their own expiry, lease, pin, or reachability policy;
- backend authentication, transport security, authorization enforcement,
  non-leaking observability, and backend conformance.

Content-addressed immutable blobs should be the dependable common primitive.
The digest identifies logical content rather than its compressed, encrypted,
or backend-specific stored representation. Consumers that need mutable names,
such as caches, may use aliases that point to immutable objects. Alias updates
must be versioned or compare-and-swap so they cannot weaken exact references or
silently lose a concurrent update.

### Consumers own

Consumers decide what the bytes mean and how storage failures affect their
operation. Hangar must not understand:

- whether content is derivable or must fail closed;
- agent snapshots, reviews, typed outputs, provenance, lineage, or sealing;
- workflows, tickets, runs, attempts, budgets, or model providers;
- why an object is retained, when a domain record becomes a garbage-collection
  root, or who may publish it;
- tenant and product authorization policy, including which opaque storage
  capabilities a caller receives;
- product-specific schemas or metadata vocabularies.

An agentic record may contain a Hangar object or manifest reference, but core
must not import the agent package or persist agent-domain identifiers in the
Hangar contract. This prohibition applies to production code and tests.

## Consumer semantics

Hangar reports reality accurately. It distinguishes an ordinary miss from a
storage failure, corruption, or immutable-write conflict. Fail-open and
fail-closed behavior belong in adapters above that contract:

- the CI cache adapter may log and count a Hangar failure, then behave as if the
  durable copy were absent so the producing step can run again;
- the agentic adapter must propagate a missing, corrupt, or unverifiable exact
  reference and must not substitute a different version;
- other consumers may choose their own policy without changing the daemon.

This preserves the existing resource-cache behavior while avoiding a storage
API whose only safe consumer is a cache. In particular, fail-open is not a
property of the underlying backend: it is a deliberate decision by a consumer
whose data is known to be reproducible.

## Object and tree model

The next design should settle the concrete API, but it should preserve four
concepts:

1. **Immutable object reference.** Identifies exact logical bytes and carries
   enough information to verify them independently of compression or
   encryption. A backend generation may participate in an exact read, but
   backend versioning alone is not a substitute for a portable content digest.
2. **Tree or manifest reference.** Identifies an immutable filesystem view with
   normalized relative paths and exact object references. Publishing the
   manifest is the commit point: containers must never observe a partial tree.
3. **Mutable alias.** Optionally maps a stable logical name to an immutable
   object or manifest through a versioned, conditional update. This supports
   cache replacement without changing exact references held by other
   consumers.
4. **Lifecycle claim.** An opaque claim prevents or permits physical
   reclamation. The domain determines reachability and authorization; Hangar
   only enforces the claim without interpreting its purpose.

Read-only materialization should be the foundation. A materializer stages and
verifies a complete filesystem outside the destination before exposing it to a
container. The design does not prescribe whether this uses an init container,
a node-local copy, a volume driver, or another mechanism.

A later runtime adapter may seed a writable workspace and publish its result as
a new immutable revision. Hangar must never mutate the source revision, but
selecting and authorizing workspace capture is outside this foundation.

## Relationship to the current implementation

The existing `cmd/artifact-daemon/durable.Store` and its filesystem, GCS, and
S3-compatible backends are useful starting material, not automatically the
strict Hangar contract. They distinguish misses from backend errors, support
enumeration, and expose backend version attributes. They also currently allow
overwrites and unconditional reads and deletes, and some backends expose no
version. The surrounding durable resource-cache tier intentionally converts
failures into cache misses.

The implementation should evolve in place through compatible layers:

- retain existing cache routes and their fail-open behavior;
- add the strict object/tree operations needed by non-derivable consumers;
- reuse the daemon, backends, node-local transfer path, and container runtime
  integration only where they pass the required strict conformance profile;
- move or split packages only where required to expose a core-owned contract;
- keep deployments with Hangar disabled behaving as they do today.

V3's `agent/hangar` is a source of proven invariants—digest validation,
immutable conflicts, generation-pinned reads, and verified materialization—not
an implementation to transplant wholesale. Agent-specific kinds, metadata,
snapshot tables, workflow associations, and checkpoint machinery stay out of
core.

The current durable-artifact documentation remains authoritative for the
resource-cache consumer's present behavior. Its statement that the cache tier
is not v3 Hangar describes today's API and failure policy; it does not define
the desired long-term service boundary.

## Safety and lifecycle invariants

- A successful publish never exposes a partial durable object or tree.
- An exact reference never resolves to different bytes.
- Retrieval verifies the promised identity before content is accepted.
- Materialization rejects path traversal and unsafe archive or symlink forms.
- A materializer stages and verifies the complete destination before making it
  visible to a container; it does not promise atomic replacement of an
  arbitrary live mount.
- Absence, corruption, conflict, and infrastructure failure remain distinct at
  the Hangar boundary, even if a consumer later collapses them to a cache miss.
- Reclamation is conditional and cannot delete content protected by an active
  claim or race a concurrent publication or claim update.
- Physical publication and a consumer's database transaction are separate
  commit points. An integration must not make a domain record usable before
  publication succeeds, and it must reclaim objects orphaned when the database
  transaction does not commit.
- Authorization failures, object names, and metrics must not expose content or
  usage across opaque consumer scopes.

## Validation direction

The future implementation should demonstrate the boundary through behavior,
not merely interfaces:

- every backend advertised for strict Hangar use passes the same exact-read,
  integrity, conflict, conditional-delete, enumeration, and interrupted-write
  conformance scenarios;
- a real daemon can store a tree, survive loss of the producing node, and
  materialize the same tree into a task container;
- the cache adapter remains fail-open under backend outage or corruption;
- a strict adapter fails closed for the same conditions and never accepts a
  different generation or digest;
- concurrent publish, materialize, alias update, lease, and reclaim operations
  cannot expose partial or wrong content;
- Kubernetes behavioral coverage asserts that every generated volume mount has
  a matching pod volume. Cluster tests run in CI, since this repository's K3s
  tier is not viable locally on macOS.

## Non-goals

This foundation does not define or rebuild the agentic workflow platform. It
does not specify snapshot schemas, tickets, workflow compilation, human review,
experiments, publishing, checkpoint recovery, or provider-native pull-request
integration.

It is also not intended to become a general distributed POSIX filesystem. Its
contract is durable immutable objects and filesystem views for task execution,
with narrowly defined alias and lifecycle operations. Package names, HTTP
routes, wire formats, database schema, rollout phases, and performance targets
belong in the follow-up design and implementation plan.

Writable mounts, workspace selection, and automatic capture from a task are
also deferred. A later runtime adapter may build them over verified publication
without expanding Hangar into a general container filesystem or granting task
pods direct object-store credentials.

## Questions for the next design

The implementing agent should resolve these before changing the public API:

1. Is a tree stored as one canonical archive, file-level blobs plus a manifest,
   or a hybrid selected by size and access pattern?
2. Which identity is authoritative across all backends, and how are backend
   generations used to prevent read and delete races?
3. What is the smallest alias API that serves resource caches without making
   mutable names the primary storage model?
4. Should reclamation be driven primarily by explicit leases and pins,
   reachability, TTL, or a deliberately small combination of them?
5. Where is tenant authorization enforced between the ATC, task pod, daemon,
   and bucket, and what capability grants access to one object or tree?
6. Which current artifact-daemon transfer and volume paths can be reused for
   exact materialization, and which cache-specific assumptions must be split
   behind adapters?

The first vertical slice should be small: publish one immutable tree through a
strictly conforming durable backend, materialize it exactly into an ordinary
task, and prove both cache fail-open and strict fail-closed consumers through
the core Hangar contract.
