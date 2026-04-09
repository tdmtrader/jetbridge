# Spec: ATC-Embedded MCP Server

## Overview

Embed an MCP (Model Context Protocol) server directly in the Concourse ATC, replacing the standalone `concourse-mcp` CLI binary. This eliminates the need to install, run, and version-manage a separate binary — any authenticated Concourse user gets MCP access at their ATC URL.

## Why

The existing `concourse-mcp` is a standalone CLI that wraps the `go-concourse` HTTP client, which itself calls the ATC API. This means: install a binary, configure a fly target, keep versions in sync, deal with token expiry requiring `fly login` re-auth. Embedding MCP in the ATC removes all of that — clients just point at the Concourse URL with a bearer token.

## Requirements

1. **SSE transport** — Implement MCP over HTTP using the standard Streamable HTTP transport (`/api/v1/mcp` endpoint) per the MCP spec.
2. **Port all 11 existing tools** — `list_pipelines`, `get_pipeline`, `set_pipeline`, `pause_pipeline`, `unpause_pipeline`, `list_jobs`, `list_builds`, `get_build`, `get_build_log`, `trigger_job`, `abort_build`.
3. **Add 7 new tools** — `list_resources`, `list_resource_versions`, `check_resource`, `get_job`, `list_teams`, `get_build_plan`, `get_info`.
4. **Use existing auth** — Bearer token auth via the standard ATC auth middleware. No new auth mechanism.
5. **Internal API access** — Tools call internal DB/service interfaces directly (not the HTTP API), since they live inside the ATC process.
6. **Team scoping** — Tools that operate on team-scoped resources use the authenticated user's team context, with optional team override parameter (matching existing MCP behavior).

## Technical Approach

- New package: `atc/api/mcpserver/` containing the server, protocol types, tool handlers, and interfaces.
- Routes registered in `atc/routes.go` using rata (matching existing pattern).
- Handler wired in `atc/api/handler.go` via `NewHandler()`.
- Auth handled by existing `APIAuthWrappa` middleware — add MCP route names to the authenticated route cases.
- SSE streaming follows the existing `buildserver/eventhandler.go` pattern using `go-sse/sse`.
- Tools receive `db.TeamFactory`, `db.BuildFactory`, etc. directly — no HTTP client round-trips.

## Acceptance Criteria

1. An MCP client (e.g., Claude Code, Cursor) can connect to `https://<concourse-url>/api/v1/mcp` with a bearer token and use all 18 tools.
2. `tools/list` returns all 18 tools with correct JSON schemas.
3. Each tool works correctly when called via `tools/call` over the HTTP transport.
4. Unauthenticated requests receive 401.
5. Team-scoped operations respect the authenticated user's permissions.
6. Existing ATC endpoints are unaffected.
7. Unit tests cover the server dispatch, each tool handler, and auth integration.

## Out of Scope

- Deprecating or removing the standalone `concourse-mcp` CLI (can happen in a follow-up).
- New auth mechanisms (long-lived API tokens, refresh tokens, etc.).
- MCP resources or prompts capabilities (tools only, matching current implementation).
- Web UI integration for MCP.
