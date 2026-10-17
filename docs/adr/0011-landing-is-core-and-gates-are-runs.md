---
status: accepted
date: 2026-10-06
---

# Landing is core's, gates are Runs, and core persists no repository credential

A landing queue lands submitted changes on a repository's trunk once every
gate its owner declared has passed on a candidate. The queue is a core
concept: its state is core tables, its engine is a core component, and the
only thing it does with the outside world is admit pipeline Runs. Every gate
is a Run of a template the queue names; so are compose and land. An
agent-review gate is just one such template, reaching core the way ADR-0004
requires. The alternative, a merge-queue process beside the platform with
state in git refs, its own lease and fence, a force-pushed candidate branch
and a raw-HTTP client, was built once (tdmtrader/jetbridge#7) and duplicated
what Runs already guarantee while leaking the queue's credential to hook
scripts.

Core never runs git and never persists a repository credential. The land
template's step receives the credential through the pipeline's credential
manager, the way the release job does, and is the only thing that pushes;
the trunk moves only by fast-forward from a base the step proves is still the
trunk's head. The candidate is a tree the compose Run publishes as a result,
addressed by its tree ref, never a branch: gates test exactly the tree that
lands, and a rebuilt commit from that tree is the one the land step pushes.

## Consequences

- The run admission port gains a queue principal, constructed only by
  `atc/landing`; the importer allowlist pins that.
- Every write that follows a Run's outcome happens in one transaction keyed
  by that Run, so a web restart replays nothing.
- A queue with no gate is a complete queue: compose, then land.
- What must be green is the queue owner's choice, declared per queue; the
  platform ships no default gate.
