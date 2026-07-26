# Answer — rca-jb-005 (WITHHELD)

## One sentence

`transcript.ndjson` is written by the **implement** step's runner, but the
ingestion block that reads it was added only to the **harvest** step
(`atc/exec/harvest_step.go`), whose flight volume never contains that file — so
`StreamFile("transcript.ndjson")` on the harvest flight finds nothing, no row is
ever inserted, and the feature fails completely and silently.

## The producer / consumer mismatch

**Producer.** `agent/runner/runner.go` — the `agent-runner` entrypoint, which is
what the **agent step** (`atc:` plan step, named `implement` in the `develop`
workflow) executes. It invokes `claude --output-format stream-json --verbose`,
captures stdout, and writes it to `flight/transcript.ndjson` in *its own* flight
directory (`runner.go` ~:325, ~:391, and `writeTranscript` ~:620–648). Nothing
in `agent/harvest/` writes a transcript; the harvest runner's flight recorder
contains `results.json`, `events.ndjson` and `review.json` only.

**Consumer, as shipped.** `atc/exec/harvest_step.go` `ingestFlightRecorder`
(pre-state ~:722–753): inside `if flightArtifact != nil`, guarded by
`if step.transcriptStore != nil`, it calls
`step.streamer.StreamFile(ingestCtx, flightArtifact, "transcript.ndjson")` on the
**harvest** step's flight artifact. That artifact is produced by
`harvest-runner`, which never writes the file.

**Consumer, as needed.** `atc/exec/agent_step.go` `ingestFlightRecorder`
(pre-state :684, flight block :751–839) already reads `results.json` and
`events.ndjson` off the *agent* step's own flight volume. That is the only place
in the server that ever holds a handle to the artifact the transcript actually
lives in. It had no transcript ingestion at all — `AgentStep` had no
`transcriptStore` field and no option to set one.

## Why it is completely silent

- Ingestion is deliberately best-effort. A `StreamFile` failure is logged at
  `logger.Error("failed-to-stream-flight-file", …)` into the **web's lager log**
  (not the build event stream), and execution continues; a zero-length read is
  skipped by the `else if len(raw) > 0` guard without any log at all. Either way
  no row is written, the metrics/ledger half of the same function is unaffected,
  and the step returns success.
- The failure is therefore invisible in the build log, the build status, the step
  summary and the UI. The only externally observable symptom is the empty table
  and the 404 from the read route.
- The read route's `found == false` → 404 branch is *correct behaviour for an
  absent row*, so the CLI's "no transcript available for run" is not an error
  report — it is the system faithfully reporting nothing was stored.

## What was NOT wrong

Everything except the ingestion location:

- migration `1773106093` / the `agent_run_transcripts` table — correct;
- `atc/db/agent_run_transcript_factory.go` (`Upsert` with
  `ON CONFLICT (build_id, plan_id)`, `Get`) — correct. Note its `Get` already
  documents the intended shape: *"prefers the implement/agent step over the
  terminal harvest step"* — i.e. the design always expected the implement step to
  be the row's author;
- the read route `atc/api/transcriptserver/` — correct;
- `go-concourse` client method and `fly agent runs transcript` — correct;
- the runner-side capture and 512KiB tail-bounding — correct, and already
  deployed; **no runner-image rebuild is required**;
- `atc/atccmd/command.go`'s construction of the transcript factory and
  `engine.WithAgentTranscriptStore(...)` — correct; the store existed and was
  reaching the step factory. Only `AgentStep()` never asked for it.

Note the one pre-state artifact that encodes the wrong assumption and should be
treated as a distractor, not evidence: the migration's own header comment says
*"harvest ingestion persists it here"*.

## The fix that was applied (commit `711974f49c`, +59/-0, two files)

1. `atc/exec/agent_step.go`
   - new field `transcriptStore HarvestTranscriptStore` on `AgentStep`;
   - new option `WithAgentStepTranscriptStore(ts HarvestTranscriptStore)`
     (modelled on `WithAgentMetricsStore`);
   - inside `ingestFlightRecorder`'s `if flightArtifact != nil` block, after the
     `events.ndjson` handling, a nil-guarded copy of the harvest ingestion:
     `StreamFile("transcript.ndjson")` → `io.ReadAll(io.LimitReader(rc, 8<<20))`
     → `boundTranscriptTail(raw)` → `db.AgentRunTranscript{BuildID, PlanID,
     StepName, NDJSON, ByteLen, Truncated, TicketID (when > 0)}` →
     `transcriptStore.Upsert`, with every error logged and swallowed.
   - It reuses the `HarvestTranscriptStore` interface and `boundTranscriptTail`
     helper already defined in the same `exec` package — no new interface, no new
     helper, no duplication of the bounding logic.
2. `atc/engine/step_factory.go` — `AgentStep()` appends
   `exec.WithAgentStepTranscriptStore(factory.agentTranscriptStore)` when that
   field is non-nil, alongside the existing metrics/budget/verifier options. The
   field and its `WithAgentTranscriptStore` factory option already existed (they
   were feeding `harvestOpts`); no `atc/atccmd/command.go` change was needed.

The change is **purely additive**: the harvest step's transcript ingestion was
deliberately left in place. It is a harmless no-op on the harvest flight, and
removing it was not required for correctness. An agent that also removes it, with
an argument, is not wrong — an agent that removes it *instead of* adding the
agent-step ingestion has not fixed anything.

## Tests (landed separately at `9b0049e7ed`, +112 in `atc/exec/agent_step_test.go`)

A `Context("with a transcript store wired (ticket #43)")` on the AgentStep flight
ingestion specs, asserting:

- the row is upserted **from the agent step's own flight**, keyed
  `(BuildID, PlanID, StepName, *TicketID)`, with `NDJSON`/`ByteLen`/`Truncated`
  matching the streamed bytes;
- an over-512KiB transcript is tail-bounded with the
  `{"type":"transcript_truncated",…}` marker prepended and the final `result`
  line surviving;
- an `Upsert` error never fails the step;
- with **no** store wired, `transcript.ndjson` is not even streamed (the
  nil-guard is complete).

## The meta-lesson recorded at the time

From `ci/dogfood/FINDINGS.md` (added one minute after the fix): *"The review
lenses missed it because each half tested in isolation; nothing exercised
implement-flight → transcript-row end-to-end."* Both halves had unit tests that
passed — the runner's write test and the harvest step's ingestion test — and the
seam between them was never crossed by any test or by any live check before
merge.
