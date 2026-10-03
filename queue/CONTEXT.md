# Queue

A merge queue: changes are admitted, batched, tested together on top of main,
then landed or ejected. A pure core with no I/O; git, CI and messaging sit
behind ports and are supplied by adapters. `queue/` is its own Go module: it
imports nothing from `atc/` or any other context, and core never imports it.
The words under "Words that are not in this repo" in `CONTEXT-MAP.md` do not
appear here.

## Language

**Entry**:
One admitted change: an ID, the commit to land, and when it was admitted.
It is queued until settled as landed or ejected. Both are final, and a
settled entry is never admitted again.
_Avoid_: request, item

**Queue**: the entries in admission order, each with its state.

**Batch**: the first N queued entries, in order, tested together as one
candidate. An ejected entry is never in a batch.

**Verdict**: the runner's one result for a candidate: pass, fail, or none.

**Decision**: what the core does with a batch after a verdict: land, eject,
split, retry, pause or recompose. No verdict retries, then pauses; it never
ejects.

**Ports**: Store (loads and saves the queue, refusing a stale write),
Composer (merges a batch onto main into a candidate, or names the one
conflicting entry), Runner (tests a candidate, reports one verdict), Lander
(fast-forwards main, refusing if main moved), Notifier (tells an entry's
owner what was decided).
