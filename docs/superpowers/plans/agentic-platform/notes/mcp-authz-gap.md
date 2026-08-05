# MCP authorization gap (for gateway-mcp / wave-3)

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../../specs/2026-07-21-agentic-workflows-as-functions-design.md) is authoritative. This gap was tracked against the ticket-centric gateway-mcp plan; check the current platform-mcp/gateway auth surfaces for whether it was resolved.

Status: **known gap, tracked** — surfaced by the parity review 2026-07-12.

## The gap

The ATC MCP server (`atc/api/mcpserver`) is **authentication-only**: `MCPEndpoint`
is wrapped in `CheckAuthenticationHandler`, and `server.go` dispatch forwards
`r.Context()` to each `ToolHandler` but never calls `accessor.GetAccessor` /
`IsAuthorized`. Every tool takes a caller-supplied `team` argument and acts on it
with **no per-team/role authorization check**. The accessor is also bound to the
`MCPEndpoint` action, not the per-tool action, so `IsAuthorized(team)` wouldn't
carry the tool's intended role requirement without new plumbing.

Consequences (all pre-existing to the parity work except where noted):
- **Write/mutation tools** (`set_pipeline`, `trigger_job`, `abort_build`,
  `pause_pipeline`, `check_resource`, `copy_resource_versions`, …): any
  authenticated user can mutate **any** team's resources by passing its name.
  This is the higher-severity half.
- **Read tools added for parity** (`list_agent_workflows`, `get_agent_workflow`,
  `agent_cost_rollup`, `list_pipeline_runs`, `get_pipeline_run`): any
  authenticated user can read workflow definitions (incl. raw YAML), the cost
  rollup, and any team's pipeline runs. These expose **non-secret operational
  data only** (no credentials/tokens — the sensitive surfaces, principals and
  credentials, are deliberately excluded from MCP). Consistent with the existing
  tool posture.

## Why not fixed piecemeal here

Gating only the 5 new read tools while the mutating tools stay open would be an
inconsistent half-measure (an attacker can still `trigger_job` on any team) and
would duplicate work. A holistic MCP authorization layer is exactly the charter
of **gateway-mcp** (plan 10, wave-3, the MCP sidecar trust boundary).

## Recommended fix (gateway-mcp)

Add an authorization pass to the MCP dispatch: expose
`accessor.GetAccessorFromContext(ctx)` (read the existing `accessorContextKey`),
and in `handleToolsCall` — or per tool — enforce, for the resolved team:
`acc.IsAdmin() || contains(acc.TeamNames(), team)` (member) for team-scoped
tools, and main-team membership for the platform-global reads. Writes should
require the appropriate role on the target team. Do this uniformly across all
tools, not per-tool, to avoid an inconsistent model.
