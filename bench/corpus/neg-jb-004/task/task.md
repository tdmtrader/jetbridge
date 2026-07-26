# Ticket #41 — L-1 · Recording-incomplete status tier

**Budget:** `$6` · **Workflow:** `develop`

---

## Why

The deployed agent-runner step image is ~28 versions behind the web. Everything
the flight recorder writes (`flight/results.json`, `events.ndjson`,
`review.json`) postdates it, so **every** harvest ingests an empty flight volume
and the ingestion degrades the metrics row to `status=error` /
`"flight recorder output missing"`. The runs ledger is a wall of fake orange:
runs that really executed and really delivered are recorded as errors because
their *recording* is missing, not because anything failed. A missing recording
needs its own tier so it stops reading as a failed run.

## Files

`agent/schema/metrics.go`, `atc/exec/harvest_step.go`, `atc/exec/agent_step.go`,
`atc/db/agent_run_metrics_factory.go` (scan path only if needed),
`fly/commands/internal/agentrunshelpers` (or wherever `fly agent runs` colors
outcomes), matching tests.

## Behavior

When flight ingestion finds NO flight artifact / zero flight files (the "never
wrote" case, as distinct from a read/stream error mid-ingest), the metrics row
records `status: "incomplete"` with summary `"no flight output (runner image
predates flight recorder?)"`.

`DeriveOutcome`: `incomplete` + succeeded build → `"delivered-unrecorded"`
(amber tier, never red); `incomplete` + failed/errored build → keep the build
verdict.

**Task 0 must verify no DB CHECK constraint restricts the status column (grep
migrations for `agent_run_metrics`); if one exists, STOP and report (migration
slots are coordinated).**

Wire consumers already degrade gracefully on unknown outcome tokens
(`3dadfb55aa` fallback chain) — extend fly's known-vocabulary map.

Contract guard: `Status` doc comment says `ok | failed | error`; update it and
the schema tests.
