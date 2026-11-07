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
lands, and the commits rebuilt to that tree are the ones the land step
pushes. The queue never squashes: each commit of an entry lands as its own
commit, with its own author, date and message.

## Consequences

- The run admission port gains a queue principal, constructed only by
  `atc/landing`; the importer allowlist pins that.
- Every write that follows a Run's outcome happens in one transaction keyed
  by that Run, so a web restart replays nothing.
- A queue with no gate is a complete queue: compose, then land.
- What must be green is the queue owner's choice, declared per queue; the
  platform ships no default gate.

Amended 2026-11-06: the queue keeps every commit. An entry's commits are
those reachable from its submitted commit and not from the trunk, replayed
oldest first, each as its own commit with its own author, date and message
and an `original:` trailer; it used to land as one commit carrying the tip's
facts. There is no squash mode: an author who wants one commit squashes
before submitting. An entry holding a merge commit is refused, and so is one
with no commit off the trunk.
