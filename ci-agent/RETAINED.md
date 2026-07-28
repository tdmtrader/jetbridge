# What remains of ci-agent, and why

This module once held the v1 standalone phase runner (review/fix/plan/implement/qa
pipelines). That system was fully replaced by the v3 agentic platform — workflow
functions over immutable snapshots (see `docs/agentic/README.md`) — and was
deleted on 2026-07-26 along with its delivery satellites and the main module's
`agent/devmcp` client.

Only the **dev capability** is deliberately retained. It has two thin
façades over one config-frozen execution core:

- `devmcp/` — the shared repo-scoped build/test/lint core and the untrusted
  interactive MCP adapter
- `cmd/dev-mcp/` — the streamable-HTTP MCP binary
- `cmd/dev-capability/` — deterministic protected-profile validation with
  machine exit codes and complete attempt logs
- `dev-mcp.yml` (repo root) — its per-repo component/command inventory
- `deploy/Dockerfile.mcp-dev-concourse` — its container image

The interactive MCP result is never authoritative. The deterministic CLI is
prepared for a later fresh hermetic validation task; it rejects authority files
and outputs inside the candidate workspace, but task rendering and server-side
attestation remain separate platform responsibilities.

It is the only in-repo implementation of a build/test MCP capability. Do not
wire the interactive façade into workflows without the digest-pinning and
capability declaration the v3 compiler requires.

Run its tests with `make test-dev-mcp`.
