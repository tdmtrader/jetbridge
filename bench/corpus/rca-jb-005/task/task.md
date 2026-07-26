# Ticket #43 shipped end-to-end and `agent_run_transcripts` is still empty

**Filed:** 2026-07-20, after the transcript-observability stack merged to `jetbridge`
**Priority:** blocking — the whole point of the feature was to make empty/aborted
agent runs debuggable, and right now there is nothing to look at.

## Symptom

Every piece of the agent tool-call transcript feature is merged and deployed:

- the runner captures `claude --output-format stream-json --verbose` stdout into
  the flight recorder as `transcript.ndjson`;
- migration `1773106093` created `agent_run_transcripts`;
- there is a read route
  `GET /api/v1/agent/tickets/:ticket_id/runs/:build_id/transcript`;
- there is a CLI: `fly agent runs transcript --ticket <id> --build <id>`;
- the store is constructed in `atc/atccmd/command.go` and handed to the step
  factory at boot.

And **`agent_run_transcripts` has zero rows.** Not "wrong rows", not "truncated
rows" — the table has never received an insert, across every dispatched run since
the stack went live. The builds themselves are green, the steps report normal
summaries, the cost/metrics rows land as they always have, and nothing about the
failure surfaces in the build log or in the web UI. `fly agent runs transcript`
just 404s.

See [`evidence/observations.md`](evidence/observations.md) for the deployment
state, the queries that were run, and what came back.

## What is expected

For any dispatched ticket run, after the run finishes there should be one
`agent_run_transcripts` row carrying the turn-by-turn transcript the runner
captured, and `fly agent runs transcript --ticket <id> --build <id>` should print
it.

## What is being asked

1. **Name the root cause.** Say precisely why zero rows are being written, and
   cite the code path that establishes it. "Something in ingestion is broken" is
   not an answer; pin it down to the specific files and call sites involved. Say
   which of the five pieces above are *correct* as well as which is wrong, and
   for each piece you clear, name the evidence or the code that rules it out —
   an unsupported "that part is fine" is not a negative result.
2. **Apply the fix**, as small as it can be while actually being effective
   end-to-end.
3. **Say how the fix is confirmed** — what a re-run should produce that this run
   did not, and what test would have caught this before it shipped.

## Constraints

- Do **not** change the `agent_run_transcripts` schema or its
  `(build_id, plan_id)` primary key, and do not add a migration. The table shape
  is agreed and there is already a read path depending on it.
- Do **not** change the read route, the `go-concourse` client method, or the
  `fly` subcommand.
- Transcript persistence is **best-effort observability**: a missing, unreadable
  or un-storable transcript must never fail a step, must never change a build's
  status, and must never interfere with the existing metrics/ledger ingestion in
  the same code path. Preserve that property.
- Keep the existing 512KiB tail-bounding behaviour and its truncation marker;
  do not re-derive or re-tune it.
- A change to the `agent-runner` container image is expensive here: it has to be
  rebuilt and rolled out through the image pipeline before it can be tested at
  all, and that pipeline has been unreliable this week. If your fix requires a
  runner-image rebuild, say so explicitly and justify the cost.
- Do not disable, delete or "clean up" existing ingestion code as part of the
  fix unless you argue that removing it is required for correctness.

## Deliverable

A short written diagnosis — root cause, the decisive evidence, the mechanism,
what was *not* wrong — filed as `DIAGNOSIS.md` at the root of the repository you
are given, plus the change itself, with test coverage that fails before the
change and passes after it.
