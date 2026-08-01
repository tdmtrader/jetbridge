# Agent Step Flight Recorder Schema

This document defines the pod-side output contract for Concourse `agent:`
steps and the server-side types built on it. `agent/schema` is a standalone
Go module (`github.com/concourse/concourse/agent/schema`) with **zero
Concourse imports** — only the Go standard library — so both the in-pod
`agent-runner` binary and the main `atc` module can depend on it without
pulling either into the other.

Every agent step writes an implicit `flight` output directory containing:

- **`results.json`** — a single structured summary of the step's outcome,
  written once at the end of the run.
- **`events.ndjson`** — a streaming, append-only newline-delimited JSON log
  written incrementally during execution.
- **`transcript.ndjson`** — the raw claude CLI `stream-json` turn-by-turn
  output, captured verbatim. It has no schema in this package; the server
  stores it as-is (bounded to the last 512 KiB) for the transcript viewer.

`atc/exec`'s agent step ingests `results.json` and `events.ndjson`
synchronously before the build step returns — the build cannot complete, and
artifact-fabric retention cannot reap the flight output, until ingestion is
done (`atc/exec/agent_step.go` `ingestFlightRecorder`). Ingestion is tolerant
by design: any missing, partial, or corrupt input degrades to a recorded
error/incomplete row rather than failing the build.

## `events.ndjson`

One self-contained JSON object per line, written with `EventWriter.Write` and
read with `EventReader.Read`.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ts` | string | yes | RFC 3339 (nanosecond-precision) timestamp. Set automatically by `EventWriter` if omitted. |
| `event` | string | yes | Dot-namespaced event type. |
| `data` | object | yes | Event-specific payload (never null on the wire — `EventWriter`/`EventReader` normalize `{}`). |

`EventReader` skips blank lines and discards (counting via `Skipped()`) any
single line over 5 MiB rather than failing the whole read — an oversized or
foreign-producer line must never poison ingestion of the `cost.record` and
`step.end` events that follow it (contract §5 invites other producers to
append their own event types; unknown types and extra data keys must be
ignored by every consumer, never repurposed by a producer).

### Event types the runner emits

`agent-runner` (`agent/runner`) is currently the only producer, and it emits
these event types, always opening with `step.start` and closing
with `step.end`:

| Event Type | Payload Go type | When |
|------------|------------------|------|
| `step.start` | `StepStartData` | Once, before the claude CLI is invoked. |
| `mcp.ready` | `MCPReadyData` | Once after successful managed output-builder protocol preflight; only server name, protocol version, and tool names. |
| `error` | `map[string]string{"message": ...}` | Zero or more times — a non-fatal condition the runner handled (e.g. an MCP sidecar failed its health wait). |
| `cost.record` | `CostRecordData` | Once, after the claude CLI invocation returns (parsed from its `CLIEnvelope`). |
| `step.end` | `StepEndData` | Once, always — the step contract requires the stream to end with `step.end`; a stream that doesn't is treated as `error` on ingestion regardless of what `results.json` says. |

#### `StepStartData`

| Field | Type | Description |
|-------|------|-------------|
| `step_name` | string | The step's config name. |
| `build_id` | int | Correlation key back to the `agent_run_metrics` row. |
| `plan_id` | string | Correlation key (with `build_id`) back to the `agent_run_metrics` row. |
| `budget_slice_usd` | float64, omitempty | The step's declared budget slice, if any. |

The pod is never told which durable workflow run it belongs to — `(build_id,
plan_id)` is the only identity it carries. The server joins that pair back to
the workflow-run identity it already holds (`step.metadata.WorkflowRunID`),
which is never read from the flight recorder or from step env.

#### `StepEndData`

| Field | Type | Description |
|-------|------|-------------|
| `step_name` | string | The step's config name. |
| `status` | string | `ok` \| `failed` \| `error` — the three-way status, already mapped from `results.json`'s wire vocabulary (see below). |
| `summary` | string | Human-readable outcome summary. |
| `wall_time_seconds` | int | Elapsed wall-clock time. |
| `cost_usd` | float64 | Resolved cost for the run. |
| `turns` | int | Number of claude CLI turns. |

#### `CostRecordData`

Mirrors `budget.LedgerEntry` (shared-contracts §2.7 / §1.4) — this is the
per-step usage/cost ledger entry.

| Field | Type | Description |
|-------|------|-------------|
| `source` | string | Always `"agent_step"` from the runner today. |
| `provider` | string | e.g. `"anthropic"`. |
| `model` | string | Model identifier from the CLI envelope. |
| `input_tokens` | int64 | |
| `output_tokens` | int64 | |
| `cache_read_tokens` | int64 | |
| `cache_creation_tokens` | int64 | |
| `turns` | int | |
| `cost_usd` | float64 | |

## `results.json`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `schema_version` | string | yes | Currently `"1.0"`. |
| `status` | string | yes | `"pass"`, `"fail"`, `"error"`, or `"abstain"` — see below. |
| `confidence` | float64 | yes | Self-reported confidence, 0.0–1.0. |
| `summary` | string | yes | Human-readable summary of the outcome. |
| `artifacts` | array | yes | List of artifact objects (empty array, never `null`, when there are none). |
| `metadata` | object | no | Unstructured key-value pairs for step-specific extensions. |

### Status values and the three-way bridge

`results.json`'s `status` field keeps its original v1.0 wire vocabulary —
`pass` / `fail` / `error` / `abstain` — independent of the run/step status
taxonomy the DB and APIs use everywhere else (`ok` / `failed` / `error` /
`incomplete`, shared-contracts convention: "agent did badly" is not the same
fact as "the platform broke"). `ThreeWayStatus(Status) (status string,
abstained bool)` is the **only** bridge between the two vocabularies:

| `results.json` `status` | Three-way status | `abstained` |
|--------------------------|-------------------|--------------|
| `pass` | `ok` | false |
| `fail` | `failed` | false |
| `error` | `error` | false |
| `abstain` | `failed` | true |
| (unrecognized) | `error` | false |

The fourth three-way value, **`incomplete`**, never appears in
`results.json` — it exists only on the server-side `RunMetrics.Status` field,
assigned during ingestion when *no* flight output could be read at all
(dominant cause: a runner image predating the flight recorder). It marks a
missing **recording**, not a failed step, and `DeriveOutcome` (below) renders
it as the amber `unrecorded` outcome rather than red, as long as the
underlying build didn't itself fail.

In practice `agent-runner` today only ever writes `pass` or `error` to
`results.json` (`fail`/`abstain` are valid wire values a step *may* emit, but
the current runner's own outcome mapping is binary: CLI succeeded and parsed
cleanly → `pass`, anything else → `error`).

### Artifact object

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Logical name (e.g. `"review-comments"`). |
| `path` | string | yes | Relative path within the output directory. |
| `media_type` | string | yes | MIME type (e.g. `"application/json"`, `"text/markdown"`). |
| `metadata` | object | no | Optional key-value pairs specific to this artifact. |

### Example

```json
{
  "schema_version": "1.0",
  "status": "pass",
  "confidence": 1,
  "summary": "Review complete.",
  "artifacts": []
}
```

```
{"ts":"2026-07-20T21:30:00.123456789Z","event":"step.start","data":{"step_name":"review","build_id":42,"plan_id":"abc"}}
{"ts":"2026-07-20T21:30:15.000000000Z","event":"cost.record","data":{"source":"agent_step","provider":"anthropic","model":"claude-sonnet-5","input_tokens":1200,"output_tokens":340,"cache_read_tokens":0,"cache_creation_tokens":0,"turns":6,"cost_usd":0.42}}
{"ts":"2026-07-20T21:30:15.100000000Z","event":"step.end","data":{"step_name":"review","status":"ok","summary":"Review complete.","wall_time_seconds":15,"cost_usd":0.42,"turns":6}}
```

## Server-side types

These types are not written by the pod; they are the shapes ingestion and
downstream consumers (DB, API, web) build from the two files above.

### `RunMetrics`

The row shape of `agent_run_metrics` (shared-contracts §2.4 / §1.8), written
in-process by `ingestFlightRecorder`. Notable fields beyond the obvious
`BuildID`/`PlanID`/`StepName`/`Model`/`Usage`/`Turns`/`WallTimeSeconds`/`CostUSD`:

| Field | Type | Description |
|-------|------|-------------|
| `WorkflowRunID` | `*WorkflowRunID`, omitempty | The durable workflow run this step ran in; nil for an unbound CI invocation. Server-owned — never read from the pod. |
| `FunctionID` | string, omitempty | The workflow function that produced the step; empty for a direct pipeline agent step. |
| `Status` | string | `ok` \| `failed` \| `error` \| `incomplete` — the agent step's own exit status (see the three-way bridge above). |
| `BuildStatus` | string, omitempty | The pipeline build's own status, joined server-side on read; never accepted from the ingesting client. |
| `Outcome` | string, omitempty | The fused display truth — see `DeriveOutcome` below. Also computed on read, never client-supplied. |
| `Results` | `json.RawMessage`, omitempty | The raw `results.json` body, if one was read. |
| `EventCounts` | `map[string]int`, omitempty | Per-event-type counts from `events.ndjson`. |

`DeriveOutcome()` fuses `BuildStatus` and `Status` into one display verdict —
`ok` / `no_output` / `running` / `failed` / `errored` / `aborted` /
`unrecorded` — with "worst truth wins" precedence: a terminally-bad build
always wins; otherwise a step-reported failure is never masked by a green
build; an `incomplete` step renders the amber `unrecorded` (never red) unless
the build is still open (`running`); only then does a `succeeded` build with
no result in hand render `no_output` instead of a masked `ok`. This is *the*
definition of the rule — `web/elm/src/AgentBadge.elm`'s `runOutcome` mirrors
it only as a client-side fallback for servers that predate the `Outcome`
field.

### `WorkflowRunID`

A positive 64-bit `agent_workflow_runs` primary key. Marshals as a **quoted
decimal string** (`"123"`, never a bare JSON number) so it survives a
JavaScript client — byte-for-byte the same wire format as the main module's
`snapshot.WorkflowRunID`. `UnmarshalJSON` tolerates either a quoted string or
a bare number from producers, but `MarshalJSON` always emits quoted. The main
module converts to/from `snapshot.WorkflowRunID` with a free `int64` cast at
the module boundary — this package never imports the main module.

### `CLIEnvelope`

The claude CLI `--output-format json` / `stream-json` final-line result
envelope (`Type`, `Subtype`, `Result`, `Model`, `CostUSD`, `TotalCostUSD`,
`NumTurns`, `IsError`, `Usage`). It is parsed in **two independent places
that must agree on the same bytes**: `agent-runner` turns it into the flight
recorder's `cost.record` event, and `atc/exec`'s web-side cost observer reads
it off the live stdout stream as an anti-tamper cost floor — the flight
recorder is self-reported from inside a pod a prompt-injected agent could
tamper with, so ingestion floors every ledger-relevant counter against the
value the web node itself observed live. Keeping the envelope shape in one
shared place means a CLI field rename can't silently zero one side's cost
reading while the other still parses it. `ResolvedCostUSD()` prefers
`total_cost_usd` (newer CLIs) and falls back to `cost_usd`.

### `WorkflowVersionStats`

One row of the per-workflow, per-version run aggregation over
`agent_run_metrics` — `Runs`, `WorkflowRuns` (the durable execution-identity
count, smaller whenever a run spans more than one build), `SucceededRuns`,
`TotalCostUSD`, `TotalTurns`, plus `WithDerived()`-computed `SuccessRate` /
`AvgCostUSD` / `AvgTurns`.

## The `AGENT_INPUT_*` / `AGENT_OUTPUT_*` env contract

Agent and task containers also receive the authoritative typed-record
identity for their declared snapshot inputs and record outputs as
environment variables (`AGENT_INPUT_<PORT>_SNAPSHOT_TYPE`,
`AGENT_INPUT_<PORT>_SNAPSHOT_DIGEST`, `AGENT_OUTPUT_<PORT>_RECORD_TYPE`,
`AGENT_OUTPUT_<PORT>_RECORD_SCHEMA`). This is a separate, orthogonal contract
from the flight recorder documented above — it governs the sealed
`record.json` a step produces as its *declared* output, not the
`flight/results.json` + `flight/events.ndjson` observability pair every
agent step writes regardless of its declared outputs. `agent/schema` does not
define these env vars or the sealed-record envelope; see
[`docs/agentic/README.md`](../../docs/agentic/README.md) ("Sealed record
outputs" and "Optional output presence") for the full contract.

## Go package

| Type / function | File | Purpose |
|---|---|---|
| `Results`, `Artifact`, `Status` | `results.go`, `status.go` | `results.json` types; `Validate()` methods. |
| `ThreeWayStatus` | `status.go` | Bridges `results.json`'s wire status to the DB/API three-way vocabulary. |
| `Event`, `EventType` | `event.go` | `events.ndjson` line types; `Validate()`. |
| `EventStepStart`, `EventStepEnd`, `EventCostRecord`, `StepStartData`, `StepEndData`, `CostRecordData` | `event_payloads.go` | The concrete event vocabulary and payloads (§5). |
| `EventWriter` | `event_writer.go` | Append validated events to an `io.Writer`. |
| `EventReader` | `event_reader.go` | Read and validate events from an `io.Reader`; oversized-line skip accounting. |
| `RunMetrics`, `Usage`, `WorkflowRunID`, `DeriveOutcome`, run-outcome constants | `metrics.go` | The `agent_run_metrics` row shape and its server-derived display outcome. |
| `CLIEnvelope` | `envelope.go` | The shared claude-CLI result-envelope shape. |
| `WorkflowVersionStats` | `workflow_stats.go` | Per-workflow-version run aggregation. |

Producers may add new event types and extra data keys; consumers must ignore
ones they don't recognize rather than erroring. Both `results.json`
(top-level `metadata`) and individual artifacts (`metadata`) accept arbitrary
key-value pairs for step-specific extensions.
