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

**Bisect**: how blame is proven in a red batch of several entries: split it
into halves, try the first half first, and repeat on each red half. Only an
entry that fails on its own is ejected; blame is never guessed.

**Flake**: a red batch whose halves both pass. Its entries land and the flake
is recorded against that batch; nobody is ejected.

**Suspect**: an entry that the failed tests and its changed files point at as
the likely cause of a red batch. Only the top suspect is used, once per batch.

**Hint**: a suspect named before blame is proven. It is never enough to eject:
the suspect runs alone first and is ejected only if it fails alone (a hit);
the rest then run as one batch.

**Miss**: a hint whose suspect passes alone. Nothing settles, the hint is
dropped and the red batch is bisected as usual.
