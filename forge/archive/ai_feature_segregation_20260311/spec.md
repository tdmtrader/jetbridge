# Spec: AI Feature Segregation

**Track ID:** `ai_feature_segregation_20260311`
**Type:** refactor

## Overview

The JetBridge platform should be a great foundation for running agents, but agentic workflow code should be a clean layer on top — not mixed into core ATC packages. Today, agent-related code lives inside `atc/agent/` and `atc/api/agentfeedback/`, making it look like core ATC infrastructure. As the agent layer grows, this will increasingly pollute the core package tree and slow iteration.

This track moves all agent-related code out of `atc/` into a top-level `agent/` directory, creating a clear architectural boundary. The `ci-agent/` module is already properly isolated and requires no changes.

## Requirements

1. Move `atc/agent/schema/` to `agent/schema/` (top-level).
2. Move `atc/api/agentfeedback/` to `agent/api/feedback/` (top-level).
3. Update the import in `atc/api/handler.go` to point to the new `agent/api/feedback/` location.
4. Agent route constants (`SubmitAgentFeedback`, etc.) remain in `atc/routes.go` — they are just string constants used by the router and are not agent "logic."
5. All existing tests pass after the move with updated import paths.
6. The `agent/` package tree must have **zero imports of `atc/`** — keeping the dependency arrow one-way (atc → agent, never agent → atc).

## Technical Approach

- File moves with import path updates (no logic changes).
- `agent/schema/` already has zero `atc/` imports — it only uses stdlib.
- `agent/api/feedback/` already has zero `atc/` imports — the handler is self-contained with a `MemoryStore`.
- The only core ATC files that change are `atc/api/handler.go` (import path) and potentially test files that reference the old paths.

## Acceptance Criteria

- [ ] No agent-related packages exist under `atc/agent/` or `atc/api/agentfeedback/`.
- [ ] `agent/schema/` contains the output schema with all tests passing.
- [ ] `agent/api/feedback/` contains the feedback handler with all tests passing.
- [ ] `atc/api/handler.go` imports from `agent/api/feedback/` instead of `atc/api/agentfeedback/`.
- [ ] `agent/` has zero imports of any `atc/` package (verified by grep).
- [ ] `go build ./cmd/concourse` succeeds.
- [ ] `go vet ./agent/...` and `go vet ./atc/...` pass.

## Out of Scope

- Moving `ci-agent/` — already a separate Go module, properly isolated.
- Making `agent/` a separate Go module — not needed yet; can be done later if iteration speed demands it.
- Runtime feature flags or build tags for agent features.
- Changing agent route constants in `atc/routes.go` — they're just strings.
- Any logic changes — this is purely a file reorganization.
