# Implementation Plan: CI Agent OTel GenAI Tracing

## Phase 1: Rich LLM Response Parsing

Parse the full Claude CLI JSON envelope to extract usage metadata (tokens, cost, model, duration) instead of discarding it.

- [x] Task 1.1: Add `CallResult` type to `llm/` with usage fields (input/output/cache tokens, cost, model, duration, finish reason) and update `Client` interface to return it pending
- [x] Task 1.2: Parse Claude CLI JSON envelope in `ClaudeClient.Call()` — extract `result`, `usage`, `cost_usd`, `model`, `duration_ms` fields pending
- [x] Task 1.3: Update `phaserunner` to use `CallResult` and store usage metadata in step results pending
- [x] Task 1.4: Update `plan/adapter/claude` and `adapter/claude` to use `CallResult` metadata pending
- [x] Task 1.5: Phase 1 Manual Verification pending

## Phase 2: OTel SDK Integration & Tracer Setup

Add OTel dependencies and create a tracer package for ci-agent with env-var-based initialization.

- [x] Task 2.1: Add OTel SDK, OTLP exporter, and semconv dependencies to `ci-agent/go.mod` pending
- [x] Task 2.2: Create `ci-agent/tracing/` package with `Init()` (reads `OTEL_EXPORTER_OTLP_ENDPOINT`), `Shutdown()`, and noop behavior when unconfigured pending
- [x] Task 2.3: Wire tracer `Init()`/`Shutdown()` into all `cmd/` entry points pending
- [x] Task 2.4: Phase 2 Manual Verification pending

## Phase 3: GenAI Span Instrumentation

Instrument all LLM call sites with OTel spans using GenAI semantic conventions.

- [x] Task 3.1: Create `llm/tracing.go` with `TracingClient` wrapper that creates GenAI spans around `Client.Call()` — sets `gen_ai.system`, `gen_ai.request.model`, token usage, cost, duration attributes pending
- [x] Task 3.2: Instrument `phaserunner.Run()` with parent phase span and child step spans pending
- [x] Task 3.3: Instrument plan and review adapters with span creation and context propagation pending
- [x] Task 3.4: Unit tests for `TracingClient` span attributes using OTel test exporter pending
- [x] Task 3.5: Phase 3 Manual Verification pending

---
