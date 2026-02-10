# Spec: Agent Step Output Schema

**Track ID:** `agent_step_output_schema_20260209`
**Type:** feature

## Overview

Define the canonical output schema for agent steps in Concourse pipelines. This establishes two file conventions that any agent step writes into its output directory:

1. **`results.json`** — A structured summary of the agent's outcome: status, confidence, and artifacts produced.
2. **`events.ndjson`** — A streaming event log (newline-delimited JSON) capturing the agent's activity: skill lifecycle, tool calls, artifact writes, and decision points.

These schemas are **generic** — not specific to the agent review step. The agent review step (and any future agent step types) will conform to these conventions. The DAG consumes `results.json` as a typed output; `events.ndjson` feeds observability and debugging.

## Design Principle: Extension, Not Core Modification

This schema package is designed as a **standalone extension** to Concourse — analogous to a plugin. It:

- Lives in its own package (`atc/agent/schema` or similar) with **zero imports from Concourse internals**.
- Defines only data types, serialization, and validation — no runtime behavior, no step execution, no DAG awareness.
- Has **no integration points** with existing Concourse code in this track. Future tracks will introduce minimal, well-defined integration seams (e.g., an agent step that writes these files, a DAG hook that reads `results.json`).
- Can be developed, tested, and versioned independently of the Concourse core.

The goal is that this package could be extracted into its own module if needed, with no Concourse-specific dependencies.

## Motivation

Agent steps are non-deterministic and opaque without structured output conventions. Traditional Concourse steps produce well-known outputs (version metadata for get/put, exit codes for tasks). Agent steps need an equivalent contract so that:

- Downstream DAG steps can branch on agent outcome (`status`, `confidence`)
- Operators can audit what an agent did (`events.ndjson`)
- Observability tooling can ingest events into OTel traces
- The schema is stable enough to build tooling against, but extensible for future agent types

## Requirements

### R1: `results.json` Schema

The file MUST be written to the agent step's output directory upon completion. Schema:

```jsonc
{
  "schema_version": "1.0",
  "status": "pass | fail | error | abstain",
  "confidence": 0.0-1.0,        // agent's self-reported confidence
  "summary": "string",           // human-readable summary of outcome
  "artifacts": [
    {
      "name": "string",          // logical name (e.g., "review-comments")
      "path": "string",          // relative path within output dir
      "media_type": "string",    // MIME type (e.g., "application/json", "text/markdown")
      "metadata": {}             // optional key-value pairs
    }
  ],
  "metadata": {}                 // optional top-level key-value pairs for step-specific data
}
```

Field rules:
1. `schema_version` — Required. Semver string. Starts at `"1.0"`.
2. `status` — Required. One of: `pass`, `fail`, `error`, `abstain`. `abstain` means the agent declined to produce a judgment (e.g., insufficient context).
3. `confidence` — Required. Float 0.0-1.0. Downstream steps can gate on this (e.g., "only proceed if confidence > 0.8").
4. `summary` — Required. Short human-readable string.
5. `artifacts` — Required (may be empty array). Each entry has required `name`, `path`, `media_type`. Optional `metadata` object.
6. `metadata` — Optional. Unstructured key-value for step-type-specific extensions. The agent review step would put things like `files_reviewed`, `issues_found` here.

### R2: `events.ndjson` Schema

A newline-delimited JSON file written incrementally during agent execution. Each line is a self-contained event object:

```jsonc
{"ts": "RFC3339", "event": "agent.start", "data": {"step": "review", "model": "claude-sonnet-4-5-20250929"}}
{"ts": "RFC3339", "event": "skill.start", "data": {"skill": "code-review", "target": "src/main.go"}}
{"ts": "RFC3339", "event": "tool.call", "data": {"tool": "grep", "args": {"pattern": "TODO", "path": "src/"}, "duration_ms": 42}}
{"ts": "RFC3339", "event": "tool.result", "data": {"tool": "grep", "status": "ok", "lines_matched": 7}}
{"ts": "RFC3339", "event": "artifact.written", "data": {"name": "review-comments", "path": "artifacts/comments.json", "bytes": 2048}}
{"ts": "RFC3339", "event": "skill.end", "data": {"skill": "code-review", "status": "pass", "duration_ms": 15230}}
{"ts": "RFC3339", "event": "agent.end", "data": {"status": "pass", "confidence": 0.92, "duration_ms": 18500}}
```

Event types (initial set):
1. `agent.start` / `agent.end` — Agent lifecycle bookends. `agent.end` echoes the final status/confidence.
2. `skill.start` / `skill.end` — Logical sub-task boundaries within the agent's work.
3. `tool.call` / `tool.result` — Individual tool invocations and their outcomes.
4. `artifact.written` — Emitted each time an artifact file is produced.
5. `decision` — Agent reasoning checkpoints (e.g., "chose to skip file X because...").
6. `error` — Non-fatal errors the agent handled or recovered from.

Field rules:
1. `ts` — Required. RFC 3339 timestamp.
2. `event` — Required. Dot-namespaced event type.
3. `data` — Required. Event-specific payload (object).
4. Events are append-only. No mutation of prior lines.
5. The event type namespace is extensible — step-specific events use the step type as prefix (e.g., `review.file_analyzed`).

### R3: Go Type Definitions

Define Go structs and constants in a **self-contained package** (`atc/agent/schema`) that:
1. Has **zero imports from Concourse packages** — only stdlib and standard JSON handling.
2. Marshal/unmarshal `results.json` to/from Go types.
3. Marshal/unmarshal individual `events.ndjson` lines.
4. Provide constants for status values and event types.
5. Include validation — `Validate()` methods that enforce required fields and value ranges.

### R4: Schema Documentation

A reference document (in-repo, Markdown) that:
1. Defines both schemas with field descriptions.
2. Includes examples for the agent review step use case.
3. Documents extensibility conventions (how to add new event types, how to use `metadata`).

## Acceptance Criteria

- [ ] `results.json` JSON Schema is defined and documented
- [ ] `events.ndjson` event format is defined and documented
- [ ] Go types exist for `Results`, `Artifact`, `Event`, status constants, and event type constants
- [ ] The schema package has **zero imports from Concourse internals** (only stdlib)
- [ ] `Validate()` on `Results` rejects missing required fields and out-of-range confidence
- [ ] `Validate()` on `Event` rejects missing ts/event/data
- [ ] Marshal/unmarshal round-trip tests pass for both schemas
- [ ] NDJSON writer/reader utility handles append and line-by-line parsing
- [ ] Schema reference doc exists with examples
- [ ] Existing pipeline behavior is unaffected (no changes to existing step types)

## Out of Scope

- Agent step runtime execution (separate track)
- DAG integration for branching on `results.json` fields (separate track)
- OTel trace ingestion from `events.ndjson` (separate track — observability)
- Agent review step implementation (consumes these schemas but is a separate track)
- MCP server / tool registry (separate track)
- Any modifications to existing Concourse packages or step types
