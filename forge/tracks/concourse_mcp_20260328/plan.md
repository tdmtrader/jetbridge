# Implementation Plan: Concourse MCP

## Phase 1: Streamable HTTP server on ATC

Set up the `mcp-go` server, mount it on the ATC router with auth, migrate the existing 11 tools, and remove the old stdio binary.

- [ ] Task 1.1: Add `mark3labs/mcp-go` dependency to `go.mod`
- [ ] Task 1.2: Create `atc/api/mcpserver/` package with `NewServer()` that returns a configured `mcp-go` server and its Streamable HTTP handler
- [ ] Task 1.3: Register MCP route in `atc/routes.go` and mount handler in `atc/api/handler.go` behind the authenticated auth wrapper
- [ ] Task 1.4: Wire MCP server startup in `atc/atccmd/command.go` — inject the team/pipeline/build accessors needed by tool handlers
- [ ] Task 1.5: Migrate existing 11 tools (list_pipelines, get_pipeline, set_pipeline, list_jobs, list_builds, get_build, get_build_log, trigger_job, abort_build, pause_pipeline, unpause_pipeline) from `cmd/concourse-mcp/mcpserver/tools.go` to new package, adapting to `mcp-go` tool registration API
- [ ] Task 1.6: Delete `cmd/concourse-mcp/` directory entirely
- [ ] Task 1.7: Unit tests for server initialization, tool registration, and auth rejection
- [ ] Task 1.8: Phase 1 verification — MCP endpoint responds to `initialize` and `tools/list` via Streamable HTTP; existing 11 tools callable

---

## Phase 2: New read tools

Add resource, build plan, team, and discovery tools.

- [ ] Task 2.1: `list_resources` — list resources in a pipeline with type, last check status, and pinned version (if any)
- [ ] Task 2.2: `list_resource_versions` — list versions for a resource with metadata and enabled/pinned status
- [ ] Task 2.3: `get_build_plan` — return the step tree for a build (task names, get/put steps, status per step) so agents can see which step failed
- [ ] Task 2.4: `list_teams` — list all teams the authenticated user has access to
- [ ] Task 2.5: `get_info` — return Concourse version, worker count, cluster name (connectivity/health check)
- [ ] Task 2.6: `fly_capabilities` — return structured reference of fly operations NOT covered by MCP tools, with brief descriptions and example fly commands, organized by category (debugging: hijack, containers; admin: workers, volumes, users, caches; pipeline management: ordering, archiving, renaming)
- [ ] Task 2.7: Unit tests for all new read tools
- [ ] Task 2.8: Phase 2 verification — all read tools return correct data against a running Concourse

---

## Phase 3: New write tools

Add resource check, pin/unpin, expose/hide, and destroy pipeline tools.

- [ ] Task 3.1: `check_resource` — trigger a resource check, return the check status
- [ ] Task 3.2: `pin_resource_version` — pin a resource to a specific version by version ID
- [ ] Task 3.3: `unpin_resource` — unpin a pinned resource
- [ ] Task 3.4: `expose_pipeline` — make a pipeline publicly visible
- [ ] Task 3.5: `hide_pipeline` — make a pipeline private
- [ ] Task 3.6: `destroy_pipeline` — delete a pipeline (require pipeline name confirmation in args as safety check)
- [ ] Task 3.7: Unit tests for all write tools
- [ ] Task 3.8: Phase 3 verification — write tools work correctly against a running Concourse

---

## Phase 4: Agent auth convenience

Make it easy for agents to get and use tokens without interactive `fly login`.

- [ ] Task 4.1: Add `fly api-token` command that prints the current target's bearer token to stdout, suitable for piping into agent MCP configuration
- [ ] Task 4.2: Include connection instructions in MCP `initialize` response server info (URL pattern, auth header format, `fly api-token` hint)
- [ ] Task 4.3: Unit tests for `fly api-token` command
- [ ] Task 4.4: Phase 4 verification — end-to-end: `fly login` → `fly api-token` → configure Claude Code MCP → call tool successfully

---
