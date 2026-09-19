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

- Hangar is opt-in and bucket-backed with its own activation state; the
  durable tier is a kill-switchable cache with a bucket lifetime set by
  retention class.
- Hangar never substitutes a newer generation or different content for a
  tree ref. The durable tier keys on content and may be empty at any time.
- The two tiers share no code path for reads. A test bans the artifact
  daemon's durable tier from importing Hangar.
