# Implementation Plan: Multi-Provider Agent CLI Support (Vertex AI + Cursor)

## Phase 1: Provider Abstraction in `ci-agent/llm/`

- [ ] Task: Write tests for `NewClient` factory function and `CursorClient`
  - Unit tests in `ci-agent/llm/client_test.go`
  - Test that `NewClient("claude", "")` returns a `ClaudeClient`
  - Test that `NewClient("cursor", "")` returns a `CursorClient`
  - Test that `NewClient("", "")` defaults to `ClaudeClient`
  - Test that unknown provider returns an error
- [ ] Task: Implement `CursorClient` and `NewClient` factory
  - Create `ci-agent/llm/cursor.go` with `CursorClient` struct
  - Invoke `cursor -p <prompt> --output-format json [--model <model>]`
  - Add `ParseCursorEnvelope` for Cursor's JSON output format
  - Add `NewClient(provider, cli string) (Client, error)` factory in `ci-agent/llm/client.go`
- [ ] Task: Make `TracingClient` provider-aware
  - Add `System string` field to `CallOpts` (set by factory or main)
  - `TracingClient` reads `opts.System` instead of hardcoded `"anthropic"`
  - Default to `"anthropic"` for backward compatibility
- [ ] Task: Wire factory into `ci-agent/cmd/ci-agent/main.go`
  - Read `AGENT_PROVIDER` env var (default: `"claude"`)
  - Replace `llm.NewClaudeClient(agentCLI)` with `llm.NewClient(provider, agentCLI)`
  - Pass provider system name through to tracing
- [ ] Task: Phase 1 Manual Verification
  - Run `cd ci-agent && go test ./llm/...` — all tests pass
  - Run `cd ci-agent && go vet ./...` — no warnings
  - Verify `AGENT_PROVIDER=claude ci-agent --phase ...` works as before

---

## Phase 2: Vertex AI Environment Configuration

- [ ] Task: Add Vertex AI env vars to pipeline task definitions
  - Update all 6 `ci/tasks/ci-agent-*.yml` files with new params:
    - `AGENT_PROVIDER: claude` (default)
    - `CLAUDE_CODE_USE_VERTEX: ""` (opt-in)
    - `CLOUD_ML_REGION: ""` (e.g., `us-central1`)
    - `ANTHROPIC_VERTEX_PROJECT_ID: ""` (GCP project)
  - Update task scripts to conditionally install CLI based on `AGENT_PROVIDER`
- [ ] Task: Update `deploy/agent-pipeline.yml` for provider params
  - Add pipeline-level params for `AGENT_PROVIDER` and Vertex AI vars
  - Propagate params to all agent task steps
- [ ] Task: Document Vertex AI + Workload Identity setup
  - Add a section to an existing doc (or inline in pipeline YAML comments)
  - Cover: GKE Workload Identity binding, required IAM roles, env var config
- [ ] Task: Phase 2 Manual Verification
  - Deploy with `CLAUDE_CODE_USE_VERTEX=1` on GKE with Workload Identity
  - Confirm Claude Code CLI authenticates via ADC (no `ANTHROPIC_API_KEY`)
  - Run a review phase end-to-end via Vertex AI

---

## Phase 3: Cursor Review Adapter

- [ ] Task: Write tests for Cursor review adapter
  - Create `ci-agent/adapter/cursor/cursor_test.go`
  - Test prompt construction and finding parsing
  - Test error handling for malformed output
- [ ] Task: Implement Cursor review adapter
  - Create `ci-agent/adapter/cursor/cursor.go` implementing `adapter.Adapter`
  - Mirror the Claude adapter's prompt construction pattern
  - Parse Cursor's JSON output into `[]runner.AgentFinding`
- [ ] Task: Wire adapter selection based on `AGENT_PROVIDER`
  - Update any code that directly instantiates `claude.Adapter` to use a factory
  - Select adapter based on the same `AGENT_PROVIDER` env var
- [ ] Task: Phase 3 Manual Verification
  - Run review phase with `AGENT_PROVIDER=cursor` against a test repo
  - Confirm findings are parsed and output matches expected schema
  - Verify OTel traces show `gen_ai.system: cursor`

---
