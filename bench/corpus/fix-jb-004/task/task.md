# Agent steps land as errored with a truncated event tally

**Type:** bug
**Component:** agent-step / flight-recorder ingestion
**Reported by:** platform on-call

## Symptom

A handful of agent steps are being recorded as failures even though the agent
itself finished normally.

On an affected build the step shows up errored, and the ingested
`agent_run_metrics` row looks self-contradictory:

- `status` is `error`, with summary `event stream ended without step.end`
  (on runs where `results.json` was also read, the summary is the agent's own
  *success* text while the status is still `error`)
- `event_counts` is short — it accounts for only the first part of the run
- `cost_usd`, token counters and `turns` are zero or far below what the run
  actually spent, so the step contributes nothing (or nearly nothing) to the
  ticket ledger
- `wall_time_seconds` is unset

## What the flight recorder actually contains

Pulling `events.ndjson` out of the step's flight artifact and reading it by
hand shows the stream is **complete and well formed**: the `cost.record`
lines and the terminating `step.end` line are all there at the end of the
file, exactly as the agent wrote them.

Comparing the file against the ingested `event_counts`, the tally stops
partway through the file. Every event from that point to the end of the
stream is missing from the row — including the ones the ledger and the
step's own pass/fail verdict depend on.

So this is not a crashed agent: the events exist on disk and are being
dropped on the way in.

## Correlation

The steps this happens to are the long, chatty ones — runs where the agent
shelled out to something with very verbose output (a full test suite, a
wide grep) and that output ended up captured in the transcript. Short runs
ingest fine. We have not found a case where a step with a quiet transcript
hit this.

## Expected behavior

Ingestion of a flight-recorder event stream must account for every event the
agent actually wrote. In particular a step whose stream ends with a valid
`step.end` must not be reported as `status=error`, and the `cost.record`
events in that stream must all reach the usage/cost totals — the ledger is
the billing record for the ticket and silently under-counting it is worse
than erroring loudly.

## Constraints

- Do not add a new third-party dependency to make this work.
- Whatever you change must stay memory-bounded. The flight recorder is
  written inside the agent pod and is self-reported: a runaway (or
  prompt-injected) agent can emit an arbitrary amount of output, and reading
  the stream must not become a way to exhaust the web node's memory.
- Do not change how a genuinely truncated or malformed stream is reported —
  a run that really did die before `step.end` must still come out as
  `status=error`.
- Cover the fix with a test.
