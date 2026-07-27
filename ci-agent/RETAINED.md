# What remains of ci-agent, and why

This module once held the v1 standalone phase runner (review/fix/plan/implement/qa
pipelines). That system was fully replaced by the v3 agentic platform — workflow
functions over immutable snapshots (see `docs/agentic/README.md`) — and was
deleted on 2026-07-26 along with its delivery satellites and the main module's
`agent/devmcp` client.

Only the **dev-mcp server** is deliberately retained:

- `devmcp/` — the MCP server implementing repo-scoped build/test/lint tools
- `cmd/dev-mcp/` — its binary
- `dev-mcp.yml` (repo root) — its per-repo component/command inventory
- `deploy/Dockerfile.mcp-dev-concourse` — its container image

It is the only in-repo implementation of a build/test MCP capability. Whether v3
ships such a capability (as a digest-pinned custom capability sidecar declared by
a workflow — the reserved `dev`/`platform`/`gateway` role names are retired, but
descriptive names like `dev-mcp` are valid) is an open product decision that gets
its own design thread. Do not delete this without that decision; do not wire it
into workflows without the digest-pinning and capability declaration the v3
compiler requires.

Run its tests with `make test-dev-mcp`.
