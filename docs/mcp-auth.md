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

Repeat `--mcp-disable-operation=<id>` to withhold an operation from every
caller regardless of consent. It is a deployment restriction, never an
authority grant: a disabled operation is absent from the catalog, answers
`DISABLED`, and `capabilities_explain` says more consent will not enable it.
The web node refuses to start on an id no operation answers to, aliases
included -- disable `pipeline_get`, not `pipeline_status`.

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

The first expanded surface exposes precise `pipeline`, `build` and `job` tools,
plus metadata-only `capabilities_explain` and the compatibility `pipeline_status`
alias. Each resource schema contains only implemented, enabled branches allowed
by the connection's exact consent and provable account restrictions. Empty groups
are omitted. Listing never grants access to every target: public pipeline/build
metadata does not imply access to private job output.

Resource, run, container and administration operations remain descriptive only;
additional consent cannot install their missing adapters. Per-team consent selection is deferred: grants apply to the user's
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
| Tools | Precise pipeline/build/job groups, capability explanations, and `pipeline_status` |
| Additional client evidence | Codex CLI 0.153.1; negotiated 2025-06-18; see the pinned [probe evidence](../hack/mcp-oauth-probe/README.md) |

Requests without an MCP grant receive a 401 challenge pointing to protected
resource metadata. That metadata identifies the OAuth issuer. Both client and
broker authorization-code exchanges use S256 PKCE; the broker also verifies the
upstream ID token's signature, issuer, audience, expiry and nonce. MCP resource
requests accept only their dedicated opaque access tokens, never fly/web JWTs or
cookies. A recognized tool call lacking consent receives a 403 scope challenge;
that operation branch is pruned from discovery and empty groups are omitted.

For an external URL of `https://ci.example.com`, the endpoints are:

- Resource: `https://ci.example.com/api/v1/mcp`
- Issuer: `https://ci.example.com/mcp/oauth`
- Resource metadata: `https://ci.example.com/.well-known/oauth-protected-resource/api/v1/mcp`
- Authorization metadata: `https://ci.example.com/.well-known/oauth-authorization-server/mcp/oauth`

This is a deliberately declared compatibility profile, not a claim that every
MCP desktop client works. DCR, client-ID metadata documents, the newer protocol
lifecycle, machine credentials and arbitrary API passthrough remain outside this
implementation. No protocol upgrade or new registration mechanism is required.
A Codex connection needs its own pre-registered client ID and exact loopback
callback (including a fixed listener port). Mixed read/write resource groups may
require client approval even when selecting a read operation; OAuth consent does
not override that client policy. Pre-registration is one of the supported
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
brine check features/mcp-operations.feature
brine run features/authentication.feature --mode sync
brine run features/mcp-auth.feature --mode sync
brine run features/mcp-operations.feature --mode sync
```

Focused OAuth race tests, migration/encryption tests and existing fly/Elm/API
authorization regressions supplement the real-boundary Brine scenarios. Review
artifacts and sanitized run evidence are linked from the three auth tracks in
Loupe.

## Precise operations

| Tool | Implemented operations | Independent consent |
| --- | --- | --- |
| `pipeline` | `pipelines_list`, `pipeline_get`, `pipeline_config_get` | `read` |
| `pipeline` | `pipeline_config_set`, `pipeline_pause`, `pipeline_unpause` | `pipelines:write` |
| `build` | `builds_list`, `build_get`, `build_logs_read` | `read` |
| `build` | `build_abort` | `builds:write` |
| `job` | `job_trigger` | `builds:write` |
| `capabilities_explain` | Describe support, absent consent and known account restrictions | Any valid MCP grant |

Each group uses a closed object with `request.oneOf`: every branch fixes
`operation` and precisely defines `arguments`. All pipeline/job targets require
`team`, `pipeline` and `instance_vars`; `{}` selects the non-instanced pipeline.
Build IDs are numeric. No operation invokes another consent category implicitly.

```sh
/tmp/jb-mcp-client call --endpoint https://ci.example.com/api/v1/mcp \
  --tool pipeline --arguments '{"request":{"operation":"pipelines_list","arguments":{"query":"deploy","limit":20}}}'
/tmp/jb-mcp-client call --endpoint https://ci.example.com/api/v1/mcp \
  --tool pipeline --arguments '{"request":{"operation":"pipeline_get","arguments":{"team":"main","pipeline":"deploy","instance_vars":{"branch":"main"}}}}'
/tmp/jb-mcp-client call --endpoint https://ci.example.com/api/v1/mcp \
  --tool capabilities_explain --arguments '{"resource":"pipeline","operation":"pipeline_config_set"}'
```

Successful grouped calls return `{operation,result}` as both structured content
and compact text. Errors use `isError` and bounded text. A well-formed, implemented
call without consent receives HTTP 403 with `insufficient_scope`; malformed,
wrong-resource and unsupported calls do not invite extra consent. The diagnostic
separates `request_new_consent`, `contact_admin`, `contact_operator`, unsupported
and unknown capabilities. Refresh cannot broaden consent. An access-service outage
is a temporary error, not an impossibility or a reason to request privileges.

`pipeline_config_set` takes `config_yaml` (at most 128 KiB UTF-8) and `version`.
Version `"0"` means create only; a positive decimal version means update that exact
existing version. The result contains the committed `version`, `created` and
warnings. `VERSION_CONFLICT` requires reviewing current state; never silently
retry or overwrite. Supplied YAML preserves variable references and is not read
from files, interpolated, or checked against secret stores by MCP.

The shared strict API is
`PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/config/conditional`, with
`X-Concourse-Config-Version` in `0..2147483647`. It retains canonical `SaveConfig`
role, policy and audit authority. A typed HTTP 409 code `config_version_conflict`
distinguishes a precondition failure from other conflicts. The Go client exposes
`SetPipelineConfigConditional`; fly opts in with `--strict-config-write` and uses
its fetched version. Existing `/config`, internal saves and default fly behavior
remain compatible. Older replicas fail without falling back to a legacy save.
A missing/invalid write receipt or lost trigger response is `OUTCOME_UNKNOWN`;
inspect state before retrying. Abort receipts mean the request was accepted.

Lists use live keyset pages, default 20 and maximum 100. Pipeline filtering and
visibility precede ID-ascending pagination; build job/status filters precede
ID-descending pagination within one exact pipeline. Pass `next_cursor` back with
the same identity and filters. Membership/status can change between pages; start
again to see newly inserted newer builds. The corresponding API modes use
`format=page` and `{items,next_cursor}`; omitting it preserves legacy arrays and
rerun-grouped build order. No broad team-build route is added.

Build logs use `BuildEvents?format=json`, under the same private-output checks as
SSE. Defaults: 32 KiB decoded log text, maximum 64 KiB, 128 events, 256 KiB encoded
API page, and a 1.5-second DB deadline. Each stored event and the page's aggregate
payload transfer are bounded at 8 MiB JSON; indivisible metadata is bounded at
64 KiB. MCP's total request/result limit is 1 MiB including both content copies.
UTF-8 log fragments retain event ID, version and origin metadata. Continue using
`next_cursor`; `caught_up` and `finished` describe the current committed snapshot.
Keep the cursor even when finished to detect late writer commits. On
`STREAM_CHANGED`, restart and **replace** accumulated output. Retention,
invalid cursors, ambiguous legacy event IDs, oversized stored events and oversized
metadata return explicit errors. `STORED_EVENT_TOO_LARGE` cannot be fixed by
reducing `max_bytes`. Default SSE is unchanged.

The mocked [tool-shape benchmark](../benchmarks/tool-shapes/README.md) measures
coarse model success and complete turn token usage; it is separate from these
production authorization and mutation tests. Follow-on parity work should bind
additional operations to shared API/domain contracts without building a second
authorization system or a generated CLI/UI framework.
