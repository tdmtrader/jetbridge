---
name: mcp-integration
description: This skill should be used when the user asks to "add MCP server", "integrate MCP", "configure MCP", "connect external service", "set up Model Context Protocol", or wants to connect tools like Google Sheets, Substack, QuickBooks, or other services via MCP. Provides guidance for configuring MCP servers in Conductor projects.
---

# MCP Integration for Conductor Projects

## Overview

Model Context Protocol (MCP) enables Conductor projects to connect with external services and APIs by providing structured tool access. Use MCP integration to connect services like Google Sheets, Substack, QuickBooks, CRMs, calendars, and other tools to your project.

**Key capabilities:**
- Connect to external services (databases, APIs, file systems)
- Provide 10+ related tools from a single service
- Handle OAuth and complex authentication flows
- Configure per-project or globally

## MCP Server Configuration

### Project-Level Configuration (.mcp.json)

Create or edit `.mcp.json` at the project root:

```json
{
  "mcpServers": {
    "google-sheets": {
      "command": "npx",
      "args": ["-y", "@anthropic/mcp-google-sheets"],
      "env": {
        "GOOGLE_APPLICATION_CREDENTIALS": "${GOOGLE_CREDENTIALS_PATH}"
      }
    }
  }
}
```

### Global Configuration

For services used across all projects, add to the global MCP config:
- Claude: `~/.claude/mcp.json`
- Gemini: `~/.gemini/settings.json` (mcpServers section)

## MCP Server Types

### stdio (Local Process)

Execute local MCP servers as child processes. Best for local tools and custom servers.

```json
{
  "filesystem": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-filesystem", "/allowed/path"],
    "env": {
      "LOG_LEVEL": "debug"
    }
  }
}
```

**Use cases:** File system access, local databases, NPM-packaged MCP servers

### SSE (Server-Sent Events)

Connect to hosted MCP servers with OAuth support. Best for cloud services.

```json
{
  "asana": {
    "type": "sse",
    "url": "https://mcp.asana.com/sse"
  }
}
```

**Use cases:** Official hosted MCP servers (Asana, GitHub, etc.), cloud services, OAuth-based authentication

### HTTP (REST API)

Connect to RESTful MCP servers with token authentication.

```json
{
  "api-service": {
    "type": "http",
    "url": "https://api.example.com/mcp",
    "headers": {
      "Authorization": "Bearer ${API_TOKEN}"
    }
  }
}
```

### WebSocket (Real-time)

Connect to WebSocket MCP servers for real-time bidirectional communication.

```json
{
  "realtime-service": {
    "type": "ws",
    "url": "wss://mcp.example.com/ws"
  }
}
```

## Environment Variable Expansion

All MCP configurations support environment variable substitution:

```json
{
  "env": {
    "API_KEY": "${MY_API_KEY}",
    "DATABASE_URL": "${DB_URL}"
  }
}
```

Document all required environment variables in `conductor/tech-stack.md`.

## Popular MCP Servers

| Service | Package | Type | Auth |
|---------|---------|------|------|
| Google Sheets | @anthropic/mcp-google-sheets | stdio | OAuth/Service Account |
| Google Calendar | @anthropic/mcp-google-calendar | stdio | OAuth |
| Google Drive | @anthropic/mcp-google-drive | stdio | OAuth |
| GitHub | @modelcontextprotocol/server-github | stdio | Token |
| Slack | @anthropic/mcp-slack | stdio | Token |
| PostgreSQL | @modelcontextprotocol/server-postgres | stdio | Connection String |
| Filesystem | @modelcontextprotocol/server-filesystem | stdio | Path |
| Brave Search | @anthropic/mcp-brave-search | stdio | API Key |
| Substack | substack-mcp | stdio | Token |

## Integration Workflow

To add an MCP server to a Conductor project:

1. **Identify the service** the user wants to connect
2. **Find the MCP server package** (check npm, GitHub, or the MCP registry)
3. **Determine the server type** (stdio for npm packages, SSE for hosted services)
4. **Configure in .mcp.json** at the project root
5. **Set up authentication** (environment variables, OAuth, or tokens)
6. **Document in tech-stack.md** — note the service, config, and required env vars
7. **Test the connection** — use `/mcp` to verify the server appears and tools work
8. **Update skills** that should use the new MCP tools

## Authentication Patterns

### OAuth (SSE/HTTP)

OAuth handled automatically by the AI CLI:

```json
{
  "type": "sse",
  "url": "https://mcp.example.com/sse"
}
```

User authenticates in browser on first use.

### Token-Based (Environment Variables)

```json
{
  "command": "npx",
  "args": ["-y", "@some/mcp-server"],
  "env": {
    "API_TOKEN": "${MY_SERVICE_TOKEN}"
  }
}
```

### Service Account (Google Services)

```json
{
  "env": {
    "GOOGLE_APPLICATION_CREDENTIALS": "${HOME}/.config/gcloud/application_default_credentials.json"
  }
}
```

## Security Best Practices

- Use HTTPS/WSS for all remote connections
- Store tokens in environment variables, never hardcode
- Document required env vars in `conductor/tech-stack.md`
- Use the most restrictive tool permissions possible

## Conductor Integration

After configuring an MCP server:

1. **Update tech-stack.md** with the service connection details
2. **Reference in skills** — skills can check tech-stack.md to know which MCPs are available
3. **Use in Conductor workflows** — skills like `/business:finances` can use MCP tools for data access
4. **Track configuration** — MCP changes are tracked in git alongside the project

## Quick Reference

| Type | Transport | Best For | Auth |
|------|-----------|----------|------|
| stdio | Process | Local tools, npm packages | Env vars |
| SSE | HTTP | Hosted services, cloud APIs | OAuth |
| HTTP | REST | API backends, token auth | Tokens |
| ws | WebSocket | Real-time, streaming | Tokens |

## Troubleshooting

**Server not connecting:** Check URL, verify package installed, review auth config
**Tools not available:** Run `/mcp` to check status, restart CLI after config changes
**Auth failing:** Clear cached tokens, re-authenticate, check env vars are set

## External Resources

- **Official MCP Docs**: https://modelcontextprotocol.io/
- **MCP SDK**: @modelcontextprotocol/sdk
- **Server Registry**: https://github.com/modelcontextprotocol/servers
