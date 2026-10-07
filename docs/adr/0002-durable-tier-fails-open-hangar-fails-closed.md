---
status: accepted
date: 2026-08-13
---

# The durable tier fails open; Hangar fails closed

Two long-term storage tiers exist on purpose, with opposite failure
semantics. The artifact daemon's durable tier holds resource caches, which
are re-fetchable from their source, so every miss, timeout or corrupt object
means "not here" and the build proceeds by fetching again. Hangar holds
exact immutable trees addressed by a complete reference, which nothing else
can reproduce, so absence, corruption, conflict or an infrastructure failure
fails the strict input rather than substituting anything. Merging them into
one tier would have forced one failure policy on both kinds of content, and
either choice is wrong for the other kind.

## Consequences

- Hangar is opt-in and bucket-backed with its own in-service state (one row
  since [ADR-0009](0009-one-node-daemon-one-capture-row.md); activation
  epochs before it); the durable tier is a kill-switchable cache with a
  bucket lifetime set by retention class.
- Hangar never substitutes a newer generation or different content for a
  tree ref. The durable tier keys on content and may be empty at any time.
- The two tiers share no code path for reads. Amended 2026-10-06 (one storage
  interface): both now sit on `hangar/objectstore` with the `hangar/gcs` and
  `hangar/disk` backends, so the separation is a NAMESPACE, not an import ban
  or a key depth. The cache is its own bucket or disk namespace with its own
  client instance; the daemon and web refuse to start with it equal to the
  input or output one. The tier may import only the object interface and its
  two backends — never `hangar/output` — and nothing under `hangar/` imports
  the tier. The daemon holds a delete only over the cache namespace.
  Reaffirmed by ADR-0009: the cache has its own namespace and its own
  store instance.
