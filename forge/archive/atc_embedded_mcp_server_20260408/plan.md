# Plan: ATC-Embedded MCP Server

## Phase 1: MCP Protocol & Transport Layer

### 1.1 Create `atc/api/mcpserver/` package with protocol types
- [x] Write tests for MCP JSON-RPC protocol types (request/response marshaling, error codes)
- [x] Implement protocol types — port `protocol.go` from `cmd/concourse-mcp/mcpserver/`, adapted for HTTP transport

### 1.2 Implement Streamable HTTP transport
- [x] Write tests for HTTP transport handler (POST `/api/v1/mcp` dispatches JSON-RPC, SSE streaming for responses)
- [x] Implement HTTP transport — Server implements http.Handler, JSON-RPC request parsing from POST body

### 1.3 Implement server dispatch logic
- [x] Write tests for server dispatch (initialize, tools/list, tools/call, ping, unknown method)
- [x] Implement server struct and dispatch — clean HTTP handler, no stdin/stdout

### 1.4 Register routes and wire into ATC
- [x] Add MCP route constants and route entries in `atc/routes.go`
- [x] Add MCP server creation and handler registration in `atc/api/handler.go`
- [x] Add MCP routes to auth cases in `atc/wrappa/api_auth_wrappa.go`

## Phase 2: Port Existing Tools (11 tools)

### 2.1 Port pipeline tools
- [x] Write tests for `list_pipelines`, `get_pipeline`, `set_pipeline`, `pause_pipeline`, `unpause_pipeline`
- [x] Implement pipeline tools using internal DB interfaces (db.TeamFactory, db.Pipeline)

### 2.2 Port job tools
- [x] Write tests for `list_jobs`
- [x] Implement `list_jobs` tool

### 2.3 Port build tools
- [x] Write tests for `list_builds`, `get_build`, `get_build_log`, `trigger_job`, `abort_build`
- [x] Implement build tools using `db.BuildFactory` and build event streaming

## Phase 3: New Tools (7 tools)

### 3.1 Resource tools
- [x] Write tests for `list_resources`, `list_resource_versions`, `check_resource`
- [x] Implement resource tools using `db.Pipeline` resource interfaces

### 3.2 Job detail tool
- [x] Write tests for `get_job` (inputs, outputs from pipeline config)
- [x] Implement `get_job` tool

### 3.3 Utility tools
- [x] Write tests for `list_teams`, `get_build_plan`, `get_info`
- [x] Implement `list_teams` (via `db.TeamFactory`), `get_build_plan` (via `db.Build`), `get_info` (version, URL)

## Phase 4: Integration & Verification

### 4.1 Auth integration
- [x] MCP routes added to authenticated case in `atc/wrappa/api_auth_wrappa.go`

### 4.2 Manual verification
- [x] Configure Claude Code or Cursor to connect to local ATC MCP endpoint and verify tools work end-to-end
