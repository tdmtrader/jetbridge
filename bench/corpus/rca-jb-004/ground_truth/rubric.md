# Rubric — rca-jb-004

Primary grading is **judge** against this checklist. `case.yaml#grading` also
records a real mechanical transition (two specs that are red at pre_state and
green with the reference change); use it to corroborate the *change* half of the
output, never as the whole score — a diff can go green while the diagnosis is
wrong, and this case is specifically built so that one such shape exists (see
D-2 below).

Score the diagnosis and the change separately, then combine. The diagnosis is
worth more.

## How to read the agent's output before scoring

- **Where the diagnosis lives.** The task asks for `RCA.md` at the root of the
  materialized repository. Grade the diagnosis wherever the agent actually
  delivered it — a diagnosis of equal quality in the response body rather than
  in the file loses nothing on Part A; note the missed channel separately as a
  process observation.
- **Credit reasoning, not quotation.** The pre_state tree legitimately contains
  the forge track
  `route_artifact_reads_through_daemonset_remove_exec_backed_artifact_io_20260418`
  — real work that predates the incident, deliberately left exposed because an
  engineer at T had it. It dates the *activating* change and it names the
  read-routing work, so an agent can partially shortcut A-2/A-4 by quoting it.
  Score the causal chain the agent argues from the evidence and the code, not
  the ability to cite a dated directory. A claim lifted from an in-tree document
  with no mechanism attached is worth roughly half of one argued from
  `worker.go` / the locator / the observed pattern. This applies equally to any
  fact the agent could have had from an operator's ambient knowledge rather than
  from this investigation.
- **Mechanical results are corroboration.** `case.yaml#grading` restores the
  humans' specs over a *copy* of the agent's tree. A green overlay supports the
  change half only; a red or non-compiling overlay against a solution that
  satisfies B-1 + B-2 is "overlay incompatible" and is yours to resolve — the
  humans' specs encode the humans' solution shape, and B-2 explicitly accepts
  others. The overlay never moves the Part A score in either direction.

---

## Part A — the diagnosis (60%)

### A-1. Names the type confusion (load-bearing, 25%)

Must say, in substance: **an artifact-daemon pod IP is being stored in a field
that holds a Kubernetes Node object name, and is then read back and used as a
Node name.** Both halves are required — "the IP is wrong" without identifying
the *field* it is wrong in, or identifying the field without saying the value is
a pod IP, is half credit.

Full credit requires naming the write site: `Worker.FindDaemonResourceCache` in
`atc/worker/jetbridge/worker.go`, specifically the
`artifactLocator.Record(cacheKey, daemonIP, cacheKey)` call.

Zero for answers that stop at `NodeIPResolver.Resolve` / `daemonURL` — that is
where the error is *printed*, not where the wrong value is *created*. The task
explicitly asks for the latter.

### A-2. Traces the read-back seam (15%)

Must connect the write to the read: the wrapped artifact (`ArtifactFromVolume`
→ `StorageBackend.WrapVolumeForLookup` → `artifactLocator.LocateNode(key)` →
`DaemonSetVolume.sourceNode` → `daemonURL` → `NodeIPResolver.Resolve`). Not all
five names are required; the chain from "the locator entry" to "the Nodes API
call" must be explicit and correct. Bonus, not required: noting that the
correct address was already available and that `NewDaemonSetVolumeFromIP` (the
`sourceIP` branch) exists precisely to skip the resolver.

### A-3. Explains the observed pattern (10%)

Must account for at least: (a) why only cache-**hit** builds fail, and (b) why
wiping the daemon's artifact directory buys exactly one good build. Credit for
also explaining (c) why restarting `web` does nothing — the correct reason is
that the poison is (re)written by the same build that reads it, so there is no
stale state a restart could clear; an answer that says "the locator is in-memory
so a restart clears it and it stays fixed" contradicts the evidence and is wrong.
(d) the daemon-restart observation (the address in the error follows the live
pod IP, which is the tell that it is a pod IP, not a stale record) is a bonus.

### A-4. Provenance (10%)

- Credit for naming `864eba169f` (2026-04-01, hostPath-symlink cache
  registration) as the change that introduced the bad write — this is the human
  answer of record.
- **Higher** credit for additionally identifying `dc5c936532` (2026-04-18,
  route artifact reads through the DaemonSet) as the change that made the
  latent poison reachable, and being explicit that the two are different. The
  operator's "it started on the 18th" is only explicable this way. An answer
  that gets both, correctly distinguished, exceeds the human baseline and should
  be scored accordingly.
- **Penalise** blaming `dc5c936532` alone, and especially recommending reverting
  it: it is a correct change that fixed a separate production failure.
- No credit lost for not finding an exact SHA if the agent correctly describes
  *which change* (by content: "when `FindResourceCache` started returning a
  daemon pod IP instead of a node name") — SHA archaeology is a nice-to-have,
  the causal story is the point. **Dating the routing work is not by itself the
  finding:** the pre_state tree contains the 20260418 forge track and the
  evidence timeline already says "18 Apr — deployed the route-artifact-reads
  work", so naming that date is nearly free. What earns the higher band is the
  causal claim it supports — that the routing change did not introduce the bad
  value, it made an existing bad value reachable as a hard failure — argued from
  the read path. **Check which materialization was used before scoring this
  item:** under the preferred ancestors-only fetch the agent has
  `git log`/`git blame`/`git log -S` and both SHAs are findable, so expect them;
  under the `git archive` fallback there is no history at all and only the
  content-level description is possible.

---

## Part B — the change (40%)

### B-1. Removes the bad write (load-bearing, 15%)

The `Record` call on the probe-hit path must be **deleted**, not repaired. The
justification must be present in some form: a probe learns only a pod IP, so
there is no correct value to write — the entry cannot be made right, only not
written. An agent that keeps the write and "fixes" it (e.g. by looking up a node
name from the pod, adding a second field, or storing a tagged union) has not
resolved the confusion at the point where it is created and takes at most half
of B-1; note if it is otherwise sound, since a wider `ArtifactLocation` redesign
was considered and explicitly rejected by the humans as out of scope.

### B-2. Handles the resulting locator miss (15%)

Deleting the write alone is **not sufficient** — verified: at pre_state the
downstream-read spec still fails, with `DaemonSetVolume.StreamOut: no source
node known (key=rc-42)`. A complete fix must make the read path work when the
locator has no entry for a resource-cache key.

The reference approach: in `WrapVolumeForLookup`, on a locator miss for an
`rc-*`-shaped key, re-probe the live daemons and return a volume bound directly
to the daemon pod IP (`NewDaemonSetVolumeFromIP`), bypassing `NodeIPResolver`.

Acceptable variation — any of these earn full B-2 if the behaviour holds:

- re-probing inside `WrapVolumeForLookup` (what the humans did),
- returning the already-IP-pinned volume from the cache-hit path in a way that
  survives the `ArtifactFromVolume` wrap (e.g. having the wrap decline to
  re-derive a source for a volume that already knows its daemon IP),
- carrying the daemon IP through a distinct, correctly-named channel rather than
  the `NodeName` field.

Required in every variation: **non-`rc-*` lookups are unchanged** (locator
first, `NodeIPResolver` on `StreamOut`), and a locator miss with no daemon hit
still degrades to today's error rather than panicking.

### B-3. Regression test (10%)

A test in `atc/worker/jetbridge` that exercises **cache hit → downstream read**,
not merely the unit that raises the error. Asserting only that
`NodeIPResolver.Resolve` rejects IP-shaped input does not pin the property that
broke and is at most partial credit — that resolver guard is defence in depth,
not the regression.

Full credit if the test would fail at pre_state for the right reason (the
`nodes "<IP>" not found` error surfacing from a downstream `StreamOut`).

**Grade this from the agent's own tree, before any grading overlay is applied.**
The withheld `worker_test.go` restore lands on the same path an agent would
naturally use, so it can erase exactly the artifact being scored;
`case.yaml#grading.overlay_protocol` requires the mechanical leg to run on a
copy. The agent may put its test in any file in `atc/worker/jetbridge` and may
structure it however it likes — the humans' spec is an existence proof, not a
target to match. If the agent's tree contains no new or changed test at all,
B-3 is zero regardless of what the overlay reports.

---

## Bonus (not required, do not penalise absence)

- **Sibling audit** (task item 4): enumerating the other production callers of
  `ArtifactLocator.Record` with a verdict for each. There are exactly two
  besides the bug — `RecordOutputs` (node name from `pod.Spec.NodeName`) and
  `RegisterResourceCache` (transitively from the same) — and both are correct.
  An audit that reaches that conclusion matches what the humans produced.
- **Defence in depth**: rejecting IP-shaped input in `NodeIPResolver.Resolve`
  with a typed sentinel. The humans did this. It is a genuine improvement, but
  it must be *in addition to* B-1+B-2, never instead of.
- Flagging that the pre-existing `worker_test.go` "stale entry for a dead node"
  spec calls an IP a "dead node IP" — the same conflation, in the test suite.

---

## Disqualifiers

- **D-1.** Recommending an RBAC change (granting/widening `nodes/get`). Ruled
  out by the task and irrelevant: the 404 is genuine, not a permissions error.
- **D-2. The translation layer.** Detecting an IP-shaped `sourceNode` on the
  read side and using it as `sourceIP`. This makes the downstream-read spec
  green while leaving a pod IP stored under `NodeName`. It ratifies the type
  confusion rather than removing it, and it leaves a per-pod address in a store
  that is never invalidated when the daemon pod is replaced — which the operator
  observed happening. **This is the designed trap of the case.** The withheld
  spec "writes nothing to the ArtifactLocator for the cache key" is the
  discriminator and is red for this shape. Score it as symptom relief with a
  wrong root-cause resolution: it can still earn Part A credit if the diagnosis
  itself is right, but B-1 is zero.
- **D-3.** Blaming the daemon, the symlink registration, the TTL sweeper, the
  pod CIDR, or ArgoCD. All ruled out by the evidence.
- **D-4.** Removing or bypassing the `sourceNode`/`NodeIPResolver` path
  wholesale. It is correct and in use for producer-recorded artifacts, the
  reaper, and scheduling affinity.
- **D-5.** Reverting `dc5c936532`.

## Reference

- `ground_truth/answer.md` — the full narrative, with pre_state line numbers.
- `ground_truth/reference.diff` — the merged human production change across
  `febae10233`, `638c2a8b00`, `b19a0fb2bc`, `04eade3c88`, `fc36f66737`
  (5 files, +63/-9). An existence proof of a correct solution, not a target to
  match.
- `ground_truth/test.diff` and `ground_truth/withheld_tests/` — the specs the
  humans wrote, used for the mechanical corroboration.
