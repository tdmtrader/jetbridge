# Renewable login and scoped MCP access

JetBridge keeps ordinary fly/web login and MCP consent separate. MCP grants act
within the user's current team roles and policy rules. A scope restricts those
permissions; it never adds a role.

## Existing fly and browser sessions

New browser-based `fly login` uses a public `fly-browser` client with an
S256-PKCE authorization-code flow and a loopback callback. Password login keeps
its existing `fly` client. Fly persists the original client and refresh
credential, renews at the issuer, and coordinates refresh/login/logout/config
updates through a bounded cross-process lock and atomic writes.

An existing access-only fly target works until expiry, then needs one fresh
`fly login` to obtain a renewable grant. Old password targets that already have
a refresh token retain the unambiguous `fly` client mapping. Cookie-only browser
refresh stays separate from the CLI flow.

| Setting | Default | Meaning |
| --- | --- | --- |
| `--auth-duration` | 24h | Existing fly/web access-token lifetime; unchanged |
| `--auth-refresh-idle-timeout` | 720h | Renewable login expires after 30 days without renewal |
| `--auth-refresh-absolute-timeout` | 2160h | Another interactive login is required after 90 days |
| `--auth-refresh-reuse-interval` | 5s | Brief recovery window for a retried Dex refresh |

A transient network/server failure preserves saved credentials and reports that
renewal can be retried. A rejected/expired grant requires login. A successful
exchange followed by failed persistence reports the failure; the five-second
Dex reuse window is bounded recovery, not a guarantee against arbitrary crashes.

Logout clears the local session and revokes its renewable issuer grant when the
server is reachable. It reports unconfirmed remote revocation rather than
claiming success. Already-issued fly/web JWTs remain valid until their normal
expiry. Dex permits one upstream renewable grant per subject, connector and
client: another login using the same fly client can supersede the previous one.
MCP's separate broker handles this limitation with a shared upstream identity
session and independent downstream consents.

## Enable MCP

MCP is off by default. Add `--enable-mcp --mcp-client-config=/path/mcp-clients.json`
to the web command's existing configuration. Use an HTTPS `--external-url`
(HTTP is permitted on loopback for local development). This initial surface is
mounted at the origin root, not beneath a reverse-proxy path prefix.

Example public-client configuration:

```json
[
  {
    "client_id": "jetbridge-reference",
    "client_name": "JetBridge reference client",
    "redirect_uris": ["http://127.0.0.1:8964/callback"]
  }
]
```

Redirects match exactly, including the port and path. Native public clients have
no secret. The server's dedicated upstream `jetbridge-mcp` Dex client is distinct
from these registrations and must not be configured through `--add-client`.

The database migration adds `mcp_oauth_state`. Opaque access/refresh credentials
are indexed by hash; sensitive broker state uses JetBridge's configured database
encryption strategy and participates in key rotation. Configure an
`--encryption-key` in production: without one, that strategy stores plaintext.
Replicas must share the database, encryption configuration, external URL and
client registrations. Expired records are collected hourly. Disabling MCP leaves
existing fly/web login available; removing the MCP migration destroys its grants.

## Choose what a connection may do

The browser consent page displays the client, callback, signed-in account and
requested categories. Only `read` is selected by default; users may choose a
subset of what the client requested.

| Scope | Category |
| --- | --- |
| `read` | Read pipelines, builds and output |
| `pipelines:write` | Set/manage pipeline configuration and create parameterized runs |
| `builds:write` | Trigger/stop configured jobs and operate resource checks |
| `hijack` | Execute interactive commands inside build containers |
| `admin` | Administrative operations the account already permits |

Categories are independent. `admin` is not a wildcard, and pipeline writes do
not imply hijack or read. An operation must have an explicit classification;
unclassified operations are denied. A parameterized run that can interpolate
configuration requires pipeline-write authority, even though it creates a build.

**Only `pipeline_status(team, pipeline)` is exposed initially.** The remaining
categories describe permissions for future tools; granting them does not add
those tools. Per-team consent selection is deferred: grants apply to the user's
current accessible teams and public data. The existing API evaluates local
team/custom-role/policy restrictions per call, subject to its existing team cache.
External groups are updated from Dex when MCP tokens renew.

Manage and revoke individual connections at `/mcp/oauth/grants`. MCP access
tokens last 15 minutes, with the same default 30-day inactivity/90-day absolute
grant limits. Refresh rotates atomically and cannot broaden consent. Reusing a
consumed refresh credential revokes that grant. MCP revocation blocks subsequent
access and renewal immediately, including already-issued MCP access tokens;
other consents remain usable. A new login creates a new consent.

## Protocol and client profile

| Component | Supported/tested profile |
| --- | --- |
| MCP server/transport | Official Go SDK v1.6.1; MCP 2025-11-25 Streamable HTTP |
| Identity provider | Embedded `github.com/concourse/dex` v1.12.0 |
| Client registration | Pre-registered public clients; exact redirect matching |
| Reference client | `cmd/jb-mcp-client`, using the same official SDK |
| Transport | Stateless POST, JSON responses; no standalone SSE or session affinity |
| Initial tool | `pipeline_status(team, pipeline)`; bounded identity/paused/archived/public fields |

Requests without an MCP grant receive a 401 challenge pointing to protected
resource metadata. That metadata identifies the OAuth issuer. Both client and
broker authorization-code exchanges use S256 PKCE; the broker also verifies the
upstream ID token's signature, issuer, audience, expiry and nonce. MCP resource
requests accept only their dedicated opaque access tokens, never fly/web JWTs or
cookies. A recognized tool call lacking consent receives a 403 scope challenge;
the same tool is omitted from discovery.

For an external URL of `https://ci.example.com`, the endpoints are:

- Resource: `https://ci.example.com/api/v1/mcp`
- Issuer: `https://ci.example.com/mcp/oauth`
- Resource metadata: `https://ci.example.com/.well-known/oauth-protected-resource/api/v1/mcp`
- Authorization metadata: `https://ci.example.com/.well-known/oauth-authorization-server/mcp/oauth`

This is a deliberately declared compatibility profile, not a claim that every
MCP desktop client works. DCR, client-ID metadata documents, the newer protocol
lifecycle, machine credentials, write tools and arbitrary API passthrough are
outside this first implementation. Pre-registration is one of the supported
registration approaches in the [MCP authorization specification](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization).
Transport behavior follows the [Streamable HTTP specification](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports).

## Connect with the reference client

Build the runnable client:

```sh
go build -o /tmp/jb-mcp-client ./cmd/jb-mcp-client
```

With the example registration above, log in once and choose the scopes on the
consent page:

```sh
/tmp/jb-mcp-client connect --endpoint https://ci.example.com/api/v1/mcp
/tmp/jb-mcp-client list --endpoint https://ci.example.com/api/v1/mcp
/tmp/jb-mcp-client status --endpoint https://ci.example.com/api/v1/mcp --team main --pipeline example
```

The default client ID is `jetbridge-reference`; the default callback is
`http://127.0.0.1:8964/callback`. Use `--client-id` and `--redirect-url` to match
another registration. To request more categories, pass a quoted list such as
`--scope "read pipelines:write hijack"`; the user still chooses the subset.

The client stores credentials with owner-only permissions in the platform's
user configuration directory. `--credentials /path/grant.json` overrides that
location. Later commands renew automatically when needed, including after a
fresh client process starts. Cross-process locking prevents two commands from
replaying the same rotated refresh token. The client sends credentials only to
the configured MCP resource and does not follow authenticated redirects.

Revoke the connection and remove its local credentials:

```sh
/tmp/jb-mcp-client logout --endpoint https://ci.example.com/api/v1/mcp
```

If remote revocation cannot be confirmed, local credentials are still removed
and the command reports the distinction. The browser connections page can revoke
the remaining grant. This reference client proves the declared JetBridge profile;
it is not a general OAuth implementation for arbitrary MCP servers.

## Behavioral verification

Authentication scenarios live in the nested Brine module and use real PostgreSQL,
Dex, Sky, fly processes and the official MCP SDK. The auth steps do not use
Kubernetes, but the shared Brine harness eagerly starts its envtest API server;
the normal envtest assets are still required. Rebuild the adapter after changing
Go source:

```sh
cd atc/worker/jetbridge/brine
go build -o .build/brine-adapter-jetbridge ./cmd/brine-adapter-jetbridge
brine check features/authentication.feature
brine check features/mcp-auth.feature
brine run features/authentication.feature --mode sync
brine run features/mcp-auth.feature --mode sync
```

Focused OAuth race tests, migration/encryption tests and existing fly/Elm/API
authorization regressions supplement the real-boundary Brine scenarios. Review
artifacts and sanitized run evidence are linked from the three auth tracks in
Loupe.
