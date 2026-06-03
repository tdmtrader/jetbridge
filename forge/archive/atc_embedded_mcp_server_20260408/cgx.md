# CGX: ATC-Embedded MCP Server

## Session Log

### 2026-04-08 — Track Created
- Researched existing `cmd/concourse-mcp/` implementation (11 tools, stdio transport, clean interfaces)
- Researched ATC API wiring: rata routes, handler factories, SSE pattern from buildserver
- Identified 7 new tools to add based on ATC API surface analysis
- User decision: keep existing auth flow (will extend token TTL separately)
