# Spec: CI Agent OTel GenAI Tracing

**Track ID:** `ci_agent_otel_genai_tracing_20260319`
**Type:** feature

## Overview

Add OpenTelemetry instrumentation to the ci-agent module with GenAI semantic conventions. Every LLM call (Claude CLI invocation) will emit OTel spans with standardized attributes for model, token usage, latency, and cost. This enables observability into AI-driven CI operations — understanding cost, performance, and failure patterns across phases.

## Context

The ci-agent currently has **zero OTel infrastructure**. It's a separate Go module (`ci-agent/go.mod`) with only Ginkgo/Gomega as dependencies. The main Concourse project has OTel tracing (`tracing/` package) but ci-agent doesn't import it.

LLM calls happen in three places:
1. `llm.ClaudeClient.Call()` — used by `phaserunner` for multi-step phase execution
2. `plan/adapter/claude.Adapter.invoke()` — used for spec/plan generation
3. `adapter/claude.Adapter.Review()` — used for code review

All three invoke the Claude CLI (`claude -p <prompt> --output-format json`). The CLI returns a JSON envelope with `usage` (token counts), `cost_usd`, `duration_ms`, `model`, and `result` fields. Currently, only the `result` field is extracted — all metadata is discarded.

## Requirements

1. **OTel SDK integration**: Add `go.opentelemetry.io/otel` SDK + OTLP exporter to `ci-agent/go.mod`
2. **GenAI span attributes**: Each LLM call emits a span with OTel GenAI semantic conventions:
   - `gen_ai.system` = "anthropic"
   - `gen_ai.request.model` (from opts or response)
   - `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`
   - `gen_ai.usage.cache_read_input_tokens` (if available)
   - `gen_ai.response.finish_reason`
   - Custom: `gen_ai.cost_usd`, `gen_ai.duration_api_ms`
3. **Span hierarchy**: Phase run → Step spans → LLM call spans (nested)
4. **Rich LLM response**: `Client.Call()` returns metadata (usage, cost, model, duration) alongside the result, so callers can use it
5. **Tracer initialization**: Configured via env vars (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`), with noop tracer when unconfigured
6. **Backward compatible**: When tracing is not configured, zero overhead — noop spans throughout

## Acceptance Criteria

- [x] `ci-agent/go.mod` includes OTel SDK and OTLP exporter dependencies
- [ ] `llm.Client.Call()` returns `CallResult` with usage metadata parsed from Claude CLI JSON envelope
- [ ] Each LLM invocation creates an OTel span with GenAI attributes
- [ ] `phaserunner.Run()` creates a parent span for the phase, with child spans per step
- [ ] Plan and review adapters propagate trace context and emit spans
- [ ] Tracer initializes from `OTEL_EXPORTER_OTLP_ENDPOINT` env var; noop when absent
- [ ] All existing tests pass (no regressions)
- [ ] New unit tests cover: response parsing, span creation, attribute population

## Out of Scope

- Tracing the main Concourse web/worker components (already has tracing)
- Prompt/response content capture in spans (privacy concern — only metadata)
- Metrics (counters, histograms) — traces only for this track
- Streaming/SSE support — ci-agent uses batch CLI calls only
