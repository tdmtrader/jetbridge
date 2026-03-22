---
name: mcp-integration
description: This skill should be used when the user asks to "add MCP server", "integrate MCP", "configure MCP", "use .mcp.json", "set up Model Context Protocol", "connect external service", or wants to connect tools like Google Sheets, Substack, QuickBooks, or other services via MCP. Provides comprehensive guidance for integrating Model Context Protocol servers into Forge projects for external tool and service integration.
---

# MCP Integration for Forge Projects

## Overview

Model Context Protocol (MCP) enables Forge projects to integrate with external services and APIs by providing structured tool access. Use MCP integration to expose external service capabilities as tools within your project.

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
    "database-tools": {
      "command": "./servers/db-server",
      "args": ["--config", "./config.json"],
      "env": {
        "DB_URL": "${DB_URL}"
      }
    }
  }
}
```

**Benefits:**
- Clear separation of concerns
- Easier to maintain
- Better for multiple servers

### Global Configuration

For services used across all projects, add to the global MCP config:
- Claude: `~/.claude/mcp.json`
- Gemini: `~/.gemini/settings.json` (mcpServers section)

## MCP Server Types

### stdio (Local Process)

Execute local MCP servers as child processes. Best for local tools and custom servers.

**Configuration:**
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

**Use cases:**
- File system access
- Local database connections
- Custom MCP servers
- NPM-packaged MCP servers

**Process management:**
- The AI CLI spawns and manages the process
- Communicates via stdin/stdout
- Terminates when the CLI exits

### SSE (Server-Sent Events)

Connect to hosted MCP servers with OAuth support. Best for cloud services.

**Configuration:**
```json
{
  "asana": {
    "type": "sse",
    "url": "https://mcp.asana.com/sse"
  }
}
```

**Use cases:**
- Official hosted MCP servers (Asana, GitHub, etc.)
- Cloud services with MCP endpoints
- OAuth-based authentication
- No local installation needed

**Authentication:**
- OAuth flows handled automatically
- User prompted on first use
- Tokens managed by the AI CLI

### HTTP (REST API)

Connect to RESTful MCP servers with token authentication.

**Configuration:**
```json
{
  "api-service": {
    "type": "http",
    "url": "https://api.example.com/mcp",
    "headers": {
      "Authorization": "Bearer ${API_TOKEN}",
      "X-Custom-Header": "value"
    }
  }
}
```

**Use cases:**
- REST API-based MCP servers
- Token-based authentication
- Custom API backends
- Stateless interactions

### WebSocket (Real-time)

Connect to WebSocket MCP servers for real-time bidirectional communication.

**Configuration:**
```json
{
  "realtime-service": {
    "type": "ws",
    "url": "wss://mcp.example.com/ws",
    "headers": {
      "Authorization": "Bearer ${TOKEN}"
    }
  }
}
```

**Use cases:**
- Real-time data streaming
- Persistent connections
- Push notifications from server
- Low-latency requirements

## Environment Variable Expansion

All MCP configurations support environment variable substitution:

**User environment variables** - From user's shell:
```json
{
  "env": {
    "API_KEY": "${MY_API_KEY}",
    "DATABASE_URL": "${DB_URL}"
  }
}
```

**Best practice:** Document all required environment variables in `forge/tech-stack.md`.

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

To add an MCP server to a Forge project:

1. **Identify the service** the user wants to connect
2. **Find the MCP server package** (check npm, GitHub, or the MCP registry)
3. **Determine the server type** (stdio for npm packages, SSE for hosted services)
4. **Configure in project .mcp.json** at the project root
5. **Set up authentication** (environment variables, OAuth, or tokens)
6. **Document in forge/tech-stack.md** - note the service, config, and required env vars
7. **Test the connection** - use `/mcp` to verify the server appears and tools work
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

User authenticates in browser on first use. No additional configuration needed.

### Token-Based (Headers)

Static or environment variable tokens:

```json
{
  "type": "http",
  "url": "https://api.example.com",
  "headers": {
    "Authorization": "Bearer ${API_TOKEN}"
  }
}
```

Document required environment variables in `forge/tech-stack.md`.

### Environment Variables (stdio)

Pass configuration to MCP server:

```json
{
  "command": "python",
  "args": ["-m", "my_mcp_server"],
  "env": {
    "DATABASE_URL": "${DB_URL}",
    "API_KEY": "${API_KEY}",
    "LOG_LEVEL": "info"
  }
}
```

## Security Best Practices

### Use HTTPS/WSS

Always use secure connections:

```json
"url": "https://mcp.example.com/sse"
```

### Token Management

**DO:**
- Use environment variables for tokens
- Document required env vars in `forge/tech-stack.md`
- Let OAuth flow handle authentication when available

**DON'T:**
- Hardcode tokens in configuration
- Commit tokens to git
- Share tokens in documentation

## Error Handling

### Connection Failures

Handle MCP server unavailability:
- Provide fallback behavior
- Inform user of connection issues
- Check server URL and configuration

### Tool Call Errors

Handle failed MCP operations:
- Validate inputs before calling MCP tools
- Provide clear error messages
- Check rate limiting and quotas

### Configuration Errors

Validate MCP configuration:
- Test server connectivity during development
- Validate JSON syntax
- Check required environment variables

## Performance Considerations

### Lazy Loading

MCP servers connect on-demand:
- Not all servers connect at startup
- First tool use triggers connection
- Connection pooling managed automatically

### Batching

Batch similar requests when possible:

```
# Good: Single query with filters
tasks = search_tasks(project="X", assignee="me", limit=50)

# Avoid: Many individual queries
for id in task_ids:
    task = get_task(id)
```

## Forge Integration

After configuring an MCP server:

1. **Update forge/tech-stack.md** with the service connection details
2. **Reference in skills** - skills can check forge/tech-stack.md to know which MCPs are available
3. **Use in Forge workflows** - skills can use MCP tools for data access
4. **Track configuration** - MCP changes are tracked in git alongside the project

## Quick Reference

### MCP Server Types

| Type | Transport | Best For | Auth |
|------|-----------|----------|------|
| stdio | Process | Local tools, custom servers | Env vars |
| SSE | HTTP | Hosted services, cloud APIs | OAuth |
| HTTP | REST | API backends, token auth | Tokens |
| ws | WebSocket | Real-time, streaming | Tokens |

### Configuration Checklist

- [ ] Server type specified (stdio/SSE/HTTP/ws)
- [ ] Type-specific fields complete (command or url)
- [ ] Authentication configured
- [ ] Environment variables documented in forge/tech-stack.md
- [ ] HTTPS/WSS used (not HTTP/WS)

### Best Practices

**DO:**
- Document required environment variables
- Use secure connections (HTTPS/WSS)
- Test MCP integration before deploying
- Handle connection and tool errors gracefully

**DON'T:**
- Hardcode absolute paths
- Commit credentials to git
- Use HTTP instead of HTTPS
- Skip error handling
- Forget to document setup

## Troubleshooting

**Server not connecting:**
- Check URL is correct
- Verify server is running (stdio)
- Check network connectivity
- Review authentication configuration

**Tools not available:**
- Verify server connected successfully
- Check tool names match exactly
- Run `/mcp` to see available tools
- Restart CLI after config changes

**Authentication failing:**
- Clear cached auth tokens
- Re-authenticate
- Check token scopes and permissions
- Verify environment variables set

## Additional Resources

### Reference Files

For detailed information, consult:

- **`references/server-types.md`** - Deep dive on each server type
- **`references/authentication.md`** - Authentication patterns and OAuth
- **`references/tool-usage.md`** - Using MCP tools in skills

### Example Configurations

Working examples in `examples/`:

- **`stdio-server.json`** - Local stdio MCP server
- **`sse-server.json`** - Hosted SSE server with OAuth
- **`http-server.json`** - REST API with token auth

### External Resources

- **Official MCP Docs**: https://modelcontextprotocol.io/
- **MCP SDK**: @modelcontextprotocol/sdk
- **Server Registry**: https://github.com/modelcontextprotocol/servers
