# JetBridge MCP reference client

This CLI uses the official Go MCP SDK and JetBridge's registered public OAuth
client profile. It discovers authentication from the MCP endpoint's challenge,
asks for browser consent with PKCE, and saves a renewable grant between commands.

Register this client in the JSON file supplied to the web server through
`--enable-mcp --mcp-client-config=/path/to/mcp-clients.json`:

```json
[
  {
    "client_id": "jetbridge-reference",
    "client_name": "JetBridge reference client",
    "redirect_uris": ["http://127.0.0.1:8964/callback"]
  }
]
```

Build the client from the repository root:

```sh
go build -o /tmp/jb-mcp-client ./cmd/jb-mcp-client
```

Connect, list tools, and read a pipeline's status:

```sh
/tmp/jb-mcp-client connect --endpoint https://ci.example.com/api/v1/mcp
/tmp/jb-mcp-client list --endpoint https://ci.example.com/api/v1/mcp
/tmp/jb-mcp-client status --endpoint https://ci.example.com/api/v1/mcp --team main --pipeline example
/tmp/jb-mcp-client logout --endpoint https://ci.example.com/api/v1/mcp
```

`connect` requests `read offline_access` by default. To request more categories,
use `--scope 'read pipelines:write builds:write hijack admin offline_access'`.
The consent page lets the user narrow that request. Categories remain independent
and do not grant permissions beyond the user's JetBridge roles. The initial
server exposes only the read-only `pipeline_status` tool.

If port 8964 is occupied, register a different exact callback URI and pass it to
`connect` using `--redirect-url`. Use `--open-browser=false` to open the printed
authorization URL yourself. The client ID is configurable with `--client-id`;
dynamic registration is not part of this profile.

Credentials are stored in a file with mode 0600 under the operating system's user
configuration directory, with a separate file for each endpoint and client ID.
`--credentials /path/to/file.json` overrides that location. Every command reloads
the file. Renewal is serialized across processes and rotated tokens are saved
atomically. A transient authorization-server failure preserves the saved login.

`logout` revokes the MCP grant and removes its local credentials. If the server is
unreachable, it reports that remote revocation could not be confirmed after
removing the local file. The browser's MCP connections page can also revoke grants.

The reusable implementation is in `internal/mcpclient`. `Login` accepts a browser
visitor function, `HTTPClient` provides automatic renewable bearer authentication,
and `Connect` constructs an official SDK streamable HTTP session.
