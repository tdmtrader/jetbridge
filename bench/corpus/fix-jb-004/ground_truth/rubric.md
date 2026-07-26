# Rubric — fix-jb-004

Behavioral checklist. Score intent, not diff similarity: the reference change
is 15 lines, but several shapes are equally correct (see `answer.md` §
"Accepted alternatives"). Do not reward matching the literal `5 << 20`
constant.

## Judge guidance (read before scoring)

**The mechanical gate is necessary, not sufficient.** The withheld test only
proves that a stream with an over-64 KiB line is read to completion (must 2).
It cannot see musts 1, 3, 4, 5 or 6 — an unbounded buffer
(`scanner.Buffer(..., math.MaxInt)`) passes it while failing must 3. Always
score the checklist against the agent's diff and its written reasoning, not
against the test exit code alone.

**Credit causal reasoning from evidence, not quotation or pattern-copying.**
The pre_state tree legitimately contains material that shortens the remedy
once the read path is found: `atc/exec/agent_cost_observer.go` implements a
5 MiB bounded line buffer for the analogous stdout-envelope problem (and
`ingestFlightRecorder` uses `io.LimitReader(rc, 5<<20)` for `results.json`),
and `docs/superpowers/plans/agentic-platform/07-agent-step.md` mirrors the
ingestion loop. None of these is withheld — they are the repository under
test, and reusing a local precedent is good engineering that should item 8
explicitly rewards. What must be earned is the **causal chain**: an answer
that reproduces the bounded-buffer pattern without stating why the stream
stopped (must 1) is a pattern match, not a diagnosis, and scores no better
than a partial. Conversely an agent that arrives at a different bounded shape
with a correct causal account scores full marks.

**Fix location is deliberately open.** `task.md` never says which file or
module must change, so a behaviorally-correct fix may land outside
`agent/schema` — e.g. the caller in `atc/exec/agent_step.go` reading the
NDJSON itself with its own bounded reader, or replacing `EventReader`
wholesale. Such an answer can satisfy every must while **failing the
mechanical gate**, which compiles against `schema.NewEventReader(io.Reader)`
and would also fail to compile if the agent changed that signature (allowed
by should 7). A mechanical failure whose cause is "the fix is real but sits
elsewhere / changed the signature" is **not** a case failure: re-score the
change by hand against musts 1–6, and record the deviation. Only a fix that
leaves an oversized line aborting the stream fails must 2.

## Must (each fails the case if missed)

1. **Diagnosis names the read path, not the symptom.** The agent identifies
   that the NDJSON event *reader* aborts the stream on a single line above a
   fixed size limit, and that ingestion's break-on-any-reader-error loop turns
   that abort into "all later events discarded". Naming only "ingestion stops
   early" without locating why is a partial diagnosis.
2. **A stream containing a line well past 64 KiB is read to completion.**
   Every event after the oversized line — specifically `cost.record` and
   `step.end` — is returned to the caller. This is the mechanical gate
   (`ground_truth/withheld_tests/event_reader_bigline_test.go`).
3. **Memory stays bounded.** The change imposes some explicit finite cap on
   how much of a single line is buffered (or otherwise bounds allocation).
   Reading an unbounded line into memory — e.g. `io.ReadAll` on the stream,
   or `scanner.Buffer(..., math.MaxInt)` — fails this item even if the test
   passes.
4. **No new third-party dependency.** `agent/schema` must remain a standalone
   module requiring nothing beyond stdlib (plus its existing ginkgo/gomega
   test deps); it is consumed by `ci-agent` and must not gain a runtime
   require, and must never depend on the main `concourse` module.
5. **Genuine truncation still reports as an error.** A stream that really does
   end without `step.end` must still produce `status=error` in
   `ingestFlightRecorder`. Deleting or weakening the
   "no `step.end` ⇒ error" rule to make the symptom disappear is a fail.
6. **A regression test exists in the agent's own change.** Some added test
   exercises an over-64 KiB line end-to-end through the reader. (The withheld
   grading test is applied independently; this item scores whether the agent
   wrote one too.) Score this from the agent's **submitted diff**, never from
   the post-overlay working tree — the fallback grading form overwrites
   `agent/schema/event_io_test.go` and would erase the very test this item
   asks for.

## Should (quality signal, not pass/fail)

7. **Public API preserved.** `NewEventReader(io.Reader) *EventReader` and
   `(*EventReader).Read() (*Event, error)` keep their signatures — 3 call
   sites exist at this cut (`atc/exec/agent_step.go:698`, plus
   `agent/runner/runner_test.go`). A signature change that forces callers to
   be edited is a heavier answer than the situation warrants.
8. **The cap is justified, not magic.** The chosen bound is explained (named
   constant and/or comment), ideally tied to the existing 5 MiB
   `io.LimitReader` used for `results.json` a few lines above in
   `ingestFlightRecorder` — the local precedent.
9. **Scope discipline.** Two files changed (reader + its test). Refactoring
   the ingestion loop, the event schema, or the metrics row is scope creep
   for this work item.

## Bonus (above ground truth)

10. **Notices the residual hole.** The 5 MiB cap only moves the cliff: a line
    above *any* cap still aborts the stream, because the reader has no way to
    skip a too-long line and resync. Flagging that (or implementing
    skip-and-resync) is strictly better than the reference change — it is
    what the humans did four days later in
    `f83ca7a1909a9dd43d637a40ad3e568c39b6dca4`.
11. **Notices the error-shape gap.** `EventReader.Read` returns
    `scanner.Err()` bare, so ingestion cannot tell "clean end of stream" from
    "reader gave up". Proposing a distinguishable error (so ingestion can log
    the drop instead of silently keeping partial counts) is a real
    improvement; the sibling commit `b899579fc5` fixed the analogous
    silent-swallow one commit earlier.
