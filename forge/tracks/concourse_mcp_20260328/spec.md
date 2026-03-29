# Spec: Concourse MCP

**Track ID:** `concourse_mcp_20260328`
**Type:** feature

## Overview

Embed a Streamable HTTP MCP server in the Concourse ATC web node, giving AI agents (Claude Code, Cursor, etc.) first-class access to Concourse operations without requiring the fly CLI or any local binary. Replaces the existing hand-rolled stdio MCP server (`cmd/concourse-mcp/`) with a production-grade implementation using `mark3labs/mcp-go`, mounted alongside the existing API at `/api/v1/mcp`.

## Motivation

In agent-first workflows, developers primarily interact with Concourse through AI agents rather than fly directly. Today this requires a pre-existing `fly login` session and a separate stdio subprocess binary. Hosting MCP on the web node means agents connect directly via HTTP with a bearer token — no fly installation, no local binary, no login dance.

## Requirements

1. Streamable HTTP MCP endpoint at `/api/v1/mcp` on the ATC web server, using `mark3labs/mcp-go`
2. Reuse existing ATC auth (Bearer token via `Authorization` header)
3. ~21 tools covering the primary agent workflow surface:
   - **Pipelines** (7): list, get config, set, pause, unpause, destroy, expose/hide
   - **Jobs** (2): list jobs with status, trigger build
   - **Builds** (4): get status, get logs, get plan, abort
   - **Resources** (5): list resources, list versions, check resource, pin version, unpin version
   - **Discovery** (2): get server info, fly capabilities reference
   - **Teams** (1): list teams
4. `fly api-token` command to generate a bearer token for agent configuration
5. Delete the old `cmd/concourse-mcp/` stdio binary (fully replaced by HTTP endpoint)

## Acceptance Criteria

- [ ] `claude mcp add --transport http concourse https://host/api/v1/mcp --header "Authorization: Bearer <token>"` works end-to-end
- [ ] All ~21 tools callable and returning correct results
- [ ] Existing ATC auth enforced (unauthorized requests rejected)
- [ ] `fly_capabilities` tool returns structured description of fly operations not covered by MCP
- [ ] Unit tests for all tool handlers
- [ ] Old `cmd/concourse-mcp/` directory removed

## Out of Scope

- Hijack/exec (WebSocket streaming — use fly directly)
- Build event streaming (return full logs; no live SSE stream per build)
- Worker/container/volume management
- User management, RBAC configuration
- Pipeline ordering, renaming, archiving
- Clear task caches
- MCP Resources/Prompts capabilities (tools only for now)
