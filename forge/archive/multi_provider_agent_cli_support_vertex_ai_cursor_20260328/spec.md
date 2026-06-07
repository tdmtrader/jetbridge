# Spec: Multi-Provider Agent CLI Support (Vertex AI + Cursor)

**Track ID:** `multi_provider_agent_cli_support_vertex_ai_cursor_20260328`
**Type:** feature

## Overview

The ci-agent pipeline is currently hardcoded to Claude Code CLI with Anthropic API key auth. This creates vendor lock-in and prevents using cheaper or more capable models as they become available. This track adds:

1. **Vertex AI support for Claude Code CLI** — Authenticate via GKE Workload Identity instead of API keys, billing usage to a GCP account.
2. **Cursor CLI as an alternative provider** — Run all ci-agent phases (review, fix, plan, implement, qa) using Cursor's headless agent mode instead of Claude Code.

## Requirements

1. A new `AGENT_PROVIDER` env var selects between `claude` (default) and `cursor`.
2. The `llm.Client` interface gains a factory function that dispatches to the correct provider based on `AGENT_PROVIDER`.
3. A `CursorClient` implements `llm.Client`, invoking Cursor CLI in headless mode with JSON output.
4. The `TracingClient` sets `gen_ai.system` dynamically based on the active provider (not hardcoded to `"anthropic"`).
5. Claude Code CLI can authenticate via Vertex AI when `CLAUDE_CODE_USE_VERTEX=1` is set, using GKE Workload Identity (ADC) — no API key required.
6. All `ci/tasks/ci-agent-*.yml` task definitions accept `AGENT_PROVIDER` and Vertex AI env vars (`CLAUDE_CODE_USE_VERTEX`, `CLOUD_ML_REGION`, `ANTHROPIC_VERTEX_PROJECT_ID`).
7. Task scripts conditionally install the correct CLI binary based on `AGENT_PROVIDER`.
8. The `ci-agent/adapter/` package gains a Cursor review adapter alongside the existing Claude adapter.
9. `CallResult` parsing handles differences between Claude and Cursor JSON output formats.

## Technical Approach

### Provider Abstraction (`ci-agent/llm/`)

The existing `Client` interface is already provider-agnostic:

```go
type Client interface {
    Call(ctx context.Context, prompt string, opts CallOpts) (CallResult, error)
}
```

Add a `CursorClient` and a `NewClient(provider, cli string) Client` factory. The `CallOpts` struct gains a `Provider` field for tracing. The `TracingClient` reads this to set `gen_ai.system`.

### Cursor CLI Invocation

Cursor's headless mode mirrors Claude's invocation pattern:
```
cursor -p <prompt> --output-format json [--model <model>]
```

The `CursorClient` follows the same `exec.CommandContext` pattern as `ClaudeClient`. Output parsing may need a separate `ParseCursorEnvelope` if the JSON schema differs.

### Vertex AI (Claude Code)

No Go code changes — purely env var config. Claude Code CLI natively supports:
- `CLAUDE_CODE_USE_VERTEX=1` — Route requests through Vertex AI
- `CLOUD_ML_REGION` — GCP region (e.g., `us-central1`)
- `ANTHROPIC_VERTEX_PROJECT_ID` — GCP project for billing

On GKE with Workload Identity, ADC provides credentials automatically.

### Pipeline Tasks

Each `ci/tasks/ci-agent-*.yml` gains params for provider selection. The task script conditionally installs the right CLI:
```bash
case "$AGENT_PROVIDER" in
  cursor) curl -fsSL https://cursor.com/install | bash ;;
  *)      npm install -g @anthropic-ai/claude-code ;;
esac
```

## Acceptance Criteria

- [ ] `AGENT_PROVIDER=claude` (default) works identically to current behavior
- [ ] `AGENT_PROVIDER=cursor` runs all 5 phases successfully using Cursor CLI
- [ ] `CLAUDE_CODE_USE_VERTEX=1` with Workload Identity authenticates without an API key
- [ ] OTel traces show correct `gen_ai.system` for each provider
- [ ] All existing ci-agent tests pass without modification
- [ ] New unit tests cover `CursorClient` and the factory function

## Out of Scope

- OpenAI, Gemini, or other LLM providers (future work)
- Changes to the Concourse ATC or runtime — this is ci-agent only
- Cursor IDE integration (only the headless CLI)
- Cost comparison or model benchmarking
