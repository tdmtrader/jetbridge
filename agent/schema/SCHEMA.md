# Agent Step Output Schema

This document defines the output conventions for Concourse agent steps. Every agent step writes two files into its output directory:

- **`results.json`** — Structured summary of the agent's outcome.
- **`events.ndjson`** — Streaming event log of the agent's activity.

## `results.json`

### Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `schema_version` | string | yes | Schema version. Currently `"1.0"`. |
| `status` | string | yes | Outcome: `"pass"`, `"fail"`, `"error"`, or `"abstain"`. |
| `confidence` | float | yes | Agent's self-reported confidence, 0.0–1.0. |
| `summary` | string | yes | Human-readable summary of the outcome. |
| `artifacts` | array | yes | List of artifact objects (may be empty). |
| `metadata` | object | no | Unstructured key-value pairs for step-specific extensions. |

### Status Values

| Status | Meaning |
|--------|---------|
| `pass` | Agent completed successfully; outcome is positive. |
| `fail` | Agent completed but found issues or did not meet threshold. |
| `error` | Agent encountered an unrecoverable error during execution. |
| `abstain` | Agent declined to produce a judgment (e.g., insufficient context). |

### Artifact Object

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Logical name (e.g., `"review-comments"`). |
| `path` | string | yes | Relative path within the output directory. |
| `media_type` | string | yes | MIME type (e.g., `"application/json"`, `"text/markdown"`). |
| `metadata` | object | no | Optional key-value pairs specific to this artifact. |

### Example

```json
{
  "schema_version": "1.0",
  "status": "pass",
  "confidence": 0.92,
  "summary": "Code review complete. 2 observations, 0 proven issues.",
  "artifacts": [
    {
      "name": "review-comments",
      "path": "artifacts/comments.json",
      "media_type": "application/json"
    },
    {
      "name": "summary",
      "path": "artifacts/summary.md",
      "media_type": "text/markdown"
    }
  ],
  "metadata": {
    "files_reviewed": 12,
    "issues_found": 0
  }
}
```

## `events.ndjson`

A newline-delimited JSON file written incrementally during execution. Each line is a self-contained event object.

### Event Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ts` | string | yes | RFC 3339 timestamp. |
| `event` | string | yes | Dot-namespaced event type. |
| `data` | object | yes | Event-specific payload. |

### Event Types

| Event Type | Description |
|------------|-------------|
| `step.start` | Agent step execution begins. |
| `step.end` | Agent step execution completes. Echoes final status and cost. |
| `cost.record` | A usage/cost ledger entry for the step. |
| `error` | A non-fatal error the agent handled or recovered from. |

### Example

```
{"ts":"2026-02-09T21:30:00Z","event":"step.start","data":{"step_name":"review","build_id":42,"plan_id":"abc"}}
{"ts":"2026-02-09T21:30:15Z","event":"cost.record","data":{"source":"claude-code","provider":"anthropic","model":"claude-sonnet-4-5-20250929","cost_usd":0.42}}
{"ts":"2026-02-09T21:30:18Z","event":"step.end","data":{"step_name":"review","status":"ok","summary":"Code review complete.","wall_time_seconds":18,"cost_usd":0.42,"turns":6}}
```

## Extensibility

### Custom Event Types

The event type namespace is extensible. Step-specific events use the step type as a prefix:

- `review.file_analyzed` — Review step analyzed a specific file.
- `plan.spec_generated` — Planning step produced a spec.
- `fix.patch_applied` — Fix step applied a code patch.

### Custom Metadata

Both `results.json` (top-level `metadata`) and individual artifacts (`metadata`) accept arbitrary key-value pairs. Use these for step-specific data that downstream consumers may need.

## Go Package

The `agent/schema` package provides Go types for these schemas:

- `Results`, `Artifact`, `Status` — types for `results.json`
- `Event`, `EventType` — types for `events.ndjson` lines
- `EventWriter` — append validated events to an `io.Writer`
- `EventReader` — read and validate events from an `io.Reader`
- `Validate()` methods on `Results` and `Event`

The package has **zero Concourse imports** — it depends only on the Go standard library.
