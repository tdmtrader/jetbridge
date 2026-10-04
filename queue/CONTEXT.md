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

**Admission**: a change is admitted by pushing its commit to
`<admission.prefix><id>` (default `refs/queue/admit/`). The driver drains
those at the start of each step under its own queue lease, and deletes each pushed
ref only once its entry is saved. A refused one is not queued; it is
announced as `refused` and the latest 20 refusals are kept for status.

**Batch**: the first N queued entries that can be tested, in order, tested together
as one candidate. An ejected entry is never in a batch, and an entry that
builds on an ejected one is itself ejected, untested, naming it as the cause.

**Attempt**: one test of a candidate by the Runner; through the JetBridge
runner it is a build. In code it is `core.Run`, the `run` resource version key
and `refs/mq/runs/`. In these docs it is never called a run: in `CONTEXT-MAP.md`
a run is a pipeline run in core and a build is never a run.

**Queue lease**: the Store's one lease, held by the driver, with a growing
token (the fence). Always qualified, as lease is in `CONTEXT-MAP.md`.

**Verdict**: the runner's one result for a candidate: pass, fail, or none (a test job that errored records errored, which is none at once).

**Decision**: what the core does with a batch after a verdict: land, eject,
split, retry, pause or recompose. No verdict retries, then pauses; it never
ejects.

**Ports**: Store (loads and saves the queue, refusing a stale write, and
hands out its one queue lease with a growing token),
Composer (merges a batch onto main into a candidate, or names the one
conflicting entry), Runner (starts an attempt on a candidate, then reports one
verdict), Lander (fast-forwards main, refusing if main moved, and says whether main already holds a candidate), Notifier
(announces each decision; its failure never changes one).

**Driver**: the loop that carries out a strategy's decisions through the
ports, one bounded step at a time, saving before each attempt and after each
settlement. It decides nothing itself, and nothing at all without the queue lease;
the Store refuses a save under an older queue lease, and the Lander refuses any land
asked before its latest fence, even one by the same holder.

**Bisect**: how blame is proven in a red batch of several entries: split it
into halves, try the first half first, and repeat on each red half. Only an
entry that fails on its own is ejected; blame is never guessed.

**Flake**: a red batch whose halves both pass. Its entries land and a flake
record (kind `flaky`, one per flaky batch, naming all its entries) is saved with
the outcome that lands them; nobody is ejected.

**Failed landing**: a land error after a green test, except main having moved,
which composes the batch again. It settles nothing and is tried again. The count
in a row is kept in the queue state; at `lander.max_failures`, `queue health`
raises the alarm (an ALARM line, exit 3) until a land clears it. The queue never
pauses or ejects for it.

**Settle record**: one saved line of history in the queue state for each land,
eject, pause, refusal or flake: id, commit, kind, time, admission time, reason,
cause, attempt and batch. The latest 1000 are kept, across restarts. Status is
read from them and the queue.

**Control ref**: a ref under `admission.control_prefix` (default
`refs/queue/control/`) through which an operator asks the running queue for
something. `queue resume` writes `<prefix>resume-<PauseSeq>`, naming the pause it ends;
the driver clears that pause at its next step and deletes the ref. A ref for
another pause is deleted and changes nothing.

**Stack**: an entry that builds on other queued entries (their heads are
ancestors of its head). The admission source derives which from git; the core
never asks git. A stacked entry is always in a batch with the entries it builds
on, ancestors first.

**Strategy**: `batch.strategy`. It decides which attempts start (Plan) and how
each verdict settles entries (Record: land, eject or pause, each with a
reason, and any flakes, which are surfaced, never hidden). It reads a snapshot of the queue and never writes the queue or lands; the caller
applies its settlements. `serial`, the only one so far and the default, tests
one batch at a time and bisects a red batch with Bisect.

**Adaptive batch size**: `batch.adaptive: {start, min, grow_after}`, off by
default (the batch is always `batch.max`). When on, batches start at `start`,
halve (floored at `min`) after a red batch, a confirmed red, flaky ones
included, and double (capped at `batch.max`) after `grow_after` green landed
batches in a row; no verdict changes nothing. Needs 1 <= min <= start <= max
and grow_after >= 1. The size and streak are not persisted: the driver builds
a fresh strategy on every reload, so the size resets to `start`.

**Main**: the core does not model main. The caller composes each attempt on
current main, which already holds every sub-batch landed so far.
