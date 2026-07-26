# Ground truth — fix-jb-004

WITHHELD. Never exposed to the agent under test.

## Root cause

`agent/schema/event_reader.go` — `NewEventReader` wrapped the stream in a
plain `bufio.NewScanner(r)` and never called `scanner.Buffer(...)`, so the
scanner ran with `bufio.MaxScanTokenSize` (64 KiB) as its maximum token size.

An NDJSON event larger than 64 KiB (e.g. a `tool.call` carrying captured
command output) makes `Scan()` return false with `bufio.ErrTooLong`. Two
downstream properties turn that into the reported symptom:

1. `EventReader.Read` surfaces `er.scanner.Err()` as a plain error — the
   caller cannot distinguish it from EOF-ish end-of-stream.
2. `atc/exec/agent_step.go` `ingestFlightRecorder` breaks its ingestion loop
   on *any* reader error (`if err != nil { break // io.EOF or malformed tail
   — keep partial counts }`).

So one oversized line mid-stream terminates ingestion at that point. Every
later `cost.record` is dropped (usage/cost/turns under-counted, ledger row
understated) and `step.end` is never seen, which trips the
"stream missing step.end is defined as error" rule in the same function —
forcing `status=error` and the `event stream ended without step.end`
summary even though a valid `step.end` sat in the file.

## The change that was made

Commit `6e113b067b10bc6b426108fffac8297dd75e6151`
("fix(agent-step): read flight NDJSON lines past the 64KiB scanner limit
[review finding]"), 2026-07-12, parent
`b899579fc50a9e578483a61a98435d49486c68ae`. Two files, 41 insertions,
1 deletion:

- `agent/schema/event_reader.go`: added `const maxEventLine = 5 << 20` and
  `scanner.Buffer(make([]byte, 0, 64*1024), maxEventLine)` in
  `NewEventReader` — start small, grow to a 5 MiB cap. Public API unchanged.
- `agent/schema/event_io_test.go`: added the spec
  `It("reads a line larger than the default 64KiB scanner limit")`, feeding a
  200 KiB `tool.call` line followed by an `agent.end` and asserting both
  events are returned.

## Accepted alternatives

The graded property is behavioral, not the literal 5 MiB constant. Any of
these is a correct answer provided it is memory-bounded:

- a different explicit cap (1 MiB, 10 MiB, ...) via `scanner.Buffer`
- replacing `bufio.Scanner` with `bufio.Reader.ReadString('\n')` /
  `json.Decoder` and imposing an explicit line/size limit
- keeping the cap but *skipping and resyncing* past a line that exceeds it
  instead of aborting the stream

That last shape is in fact where the code went next: commit
`f83ca7a1909a9dd43d637a40ad3e568c39b6dca4` (2026-07-16) reworked the reader
to skip-and-resync above the 5 MiB cap. That is **out of scope** for this
case — it is four days past the cut and answers a different question (what
to do about a line larger than *any* cap). An agent that produces the
skip-and-resync behavior at this cut should be scored as fully correct, not
penalized.

## Wrong answers

- Removing the "no `step.end` ⇒ error" rule in `ingestFlightRecorder`. That
  hides the symptom and breaks genuine crash detection (the task's third
  constraint).
- Making `ingestFlightRecorder` continue past reader errors without fixing
  the reader. Verified empirically on go1.25.6: once `ErrTooLong` fires, the
  next `Scan()` yields one garbage 64 KiB *prefix* of the oversized line
  (which then fails JSON parse) and every `Scan()` after that returns false
  with the error still set. The remainder of the stream is unreachable, so
  the later `cost.record`/`step.end` events are still lost.
- Reading the whole artifact into memory unbounded (violates the
  memory-bounded constraint; note `results.json` ingestion right above
  already uses `io.LimitReader(rc, 5<<20)`, which is the local precedent for
  the 5 MiB figure).
- Adding a third-party NDJSON/streaming library. `agent/schema` is a
  standalone Go module whose only requires are ginkgo/gomega for tests; it
  is consumed by `ci-agent` and must not gain runtime dependencies.
