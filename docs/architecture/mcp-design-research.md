# JetBridge MCP design research

Research date: September 12, 2026. Repository baseline: `b41dfe01c1`, with existing working-tree changes. This is a design assessment from source inspection and primary documentation; it does not verify the deployed server or installed fly binary. No runtime behavior was changed or tested.

Companion: [current surface audit](mcp-surface-audit.md).

## Recommended direction

Make each JetBridge product operation available through the API, fly and MCP, with the UI choosing its own presentation. Define the operation once, including its input/output contract and authorization requirements. Adapters should share execution and permission checks. Coverage means equivalent capabilities, permissions and outcomes; it need not mean identical command names, tool names, or one HTTP request per workflow.

Use a complete, typed MCP tool catalog with progressive client discovery. Give humans renewable OAuth sessions with short-lived access tokens. Fix fly's existing refresh defects as the first part of that work.

## What explains fly's repeated login

| Finding | Evidence | Consequence |
| --- | --- | --- |
| Browser login never obtains a refresh token for fly | `fly/commands/login.go:128`, `:157`, `:190` — the browser path returns only token type/value | A successful browser login cannot activate fly's refreshing token source |
| Password login requests and stores a refresh token | `fly/commands/login.go:165` requests `offline_access`; `:157` persists the result | Refresh support exists, but the transport contract is broken |
| Client and server disagree on the refresh request | `fly/rc/token_refresh.go:77` posts a form; `skymarshal/skyserver/skyserver.go:212` and `skymarshal/token/middleware.go:124` read only a cookie | The CLI request receives 401 before exchange |
| Client and server also disagree on the response | `fly/rc/token_refresh.go:109` expects access/refresh tokens; `skymarshal/skyserver/skyserver.go:263` returns only a CSRF token and sets browser cookies | Merely accepting the form would not fix refresh |
| Refresh uses the wrong OAuth client for fly password grants | `fly/commands/login.go:168` uses client `fly`; `atc/atccmd/command.go:2256` supplies the web OAuth client to the refresh handler | Fixing the wire format alone would still leave client-bound refresh grants mismatched |
| Refresh failures and persistence failures are suppressed | `fly/rc/token_refresh.go:56` and `:69` | Users see downstream authorization failures instead of a useful refresh diagnosis |
| Logout clears local/browser credentials | `fly/commands/logout.go:44`, `skymarshal/skyserver/skyserver.go:269` | This flow does not revoke an already issued server-side grant |

The default `--auth-duration` is 24 hours (`skymarshal/skycmd/flags.go:47`). The web refresh cookie has a 30-day expiry (`skymarshal/skyserver/skyserver.go:180`); cookie retention does not establish the authorization server's refresh-grant lifetime.

The pinned provider is `github.com/concourse/dex v1.12.0` (`go.mod:26`). Its locally installed source supports authorization code, PKCE, refresh, device, token exchange and client-credentials plumbing. JetBridge's `skymarshal/dexserver/dexserver.go:121` does not supply `RefreshTokenPolicy`. In the pinned source, `server/server.go:314` retains that pointer and `server/refreshhandlers.go:116` dereferences it through a method without a nil guard (`server/rotation.go:230`). This is an additional source-level refresh defect to reproduce and fix. The same provider enforces refresh-client matching (`server/refreshhandlers.go:97`). Module source was inspected under `/Users/tdmtrader/go/pkg/mod/github.com/concourse/dex@v1.12.0`; none of these findings establishes which binary is deployed.

## Authentication design

For humans, use browser authorization-code login with PKCE, then automatic refresh. Register fly and MCP clients separately while using the same identity provider and team authorization. Store refresh credentials securely, serialize concurrent refreshes, persist rotated credentials atomically, and report refresh failures clearly. Add server-side grant revocation and explicit inactive/absolute session lifetimes. A proposed starting policy is 15–60-minute access tokens and several-week renewable sessions; the exact durations are product policy, not an MCP requirement. Public-client refresh credentials require rotation or another supported protection under OAuth guidance; current MCP specifically requires public-client rotation. [MCP security requirements](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/security-considerations), [OAuth security BCP](https://www.rfc-editor.org/rfc/rfc9700.html#section-4.14)

Use HTTPS Streamable HTTP for hosted MCP. Publish protected-resource metadata and authorization-server discovery; support the registration mechanisms the chosen clients implement. The current published revision is **2026-07-28**. It favors Client ID Metadata Documents or pre-registration and deprecates Dynamic Client Registration, retaining it for compatibility. OAuth 2.1 and Client ID Metadata Documents are still IETF drafts referenced by that published MCP revision. Select and test protocol versions against actual clients rather than assuming all support the newest revision. [Versioning](https://modelcontextprotocol.io/docs/2026-07-28/learn/versioning), [authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization), [discovery](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/authorization-server-discovery), [client registration](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/client-registration)

Validate issuer, expiry, intended resource audience and granted permissions on every request. Current JetBridge deliberately uses Dex ID tokens as API bearer tokens (`fly/commands/login.go:181`), and its verifier checks configured client audiences (`atc/atccmd/command.go:2290`); this is not a ready-made MCP resource-access-token contract. Reuse identity and authorization logic, but define a conforming access-token boundary. Do not forward a token issued for MCP to a different protected API. Use shared authorized services in process, or a proper downstream grant/token exchange. [MCP audience and token-passthrough requirements](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/security-considerations)

For unattended work, provide separately scoped service identities and client credentials, preferably with private-key authentication. MCP defines this as an optional extension, so verify client support. Human session longevity and machine identity should be separate features. [Client-credentials extension](https://modelcontextprotocol.io/extensions/auth/oauth-client-credentials)

Dex supports renewable sessions, but refresh availability depends on the connector; for example, its SAML connector does not support `offline_access`. JetBridge must expose and test the pinned provider's policy/configuration rather than assume the latest upstream settings are already wired. [Dex token guidance](https://dexidp.io/docs/configuration/tokens/), [Dex scopes](https://dexidp.io/docs/configuration/custom-scopes-claims-clients/)

## Discovery and tool shape

| Approach | Benefit | Tradeoff and recommendation |
| --- | --- | --- |
| Standard `tools/list` with typed tools | Portable schemas, validation and operation-level metadata | Keep this as the canonical surface |
| Client-side search and deferred loading | Model sees only relevant definitions while the full catalog remains available | Preferred experience; official MCP client guidance recommends it for large catalogs |
| Server-side search plus JSON dispatcher | Small initial surface even in clients without deferred loading | Optional fallback; loses per-operation schemas/annotations at the native tool boundary, so the dispatcher must restore validation, permissions and auditing |
| Search plus sandboxed code execution | Compact access to very large APIs and multi-step composition | More infrastructure and more complex approval/audit behavior; evaluate later if measured workloads justify it |

These approaches separate catalog size from model-context size. Native `tools/list` enumerates tools; host search selects which definitions enter model context. `server/discover` is server capability/version discovery, not semantic tool search. The current specification allows authorization-dependent tool lists, but forbids changing the list as a side effect of previous connection requests. [Client best practices](https://modelcontextprotocol.io/docs/2026-07-28/develop/clients/client-best-practices), [tools specification](https://modelcontextprotocol.io/specification/2026-07-28/server/tools), [server discovery](https://modelcontextprotocol.io/specification/2026-07-28/server/discover)

Cloudflare demonstrates a working search-and-execute design at very large scale. Its suitability for JetBridge is an engineering choice, not a requirement or universal best practice. The validation and approval tradeoffs above are our design assessment. [Cloudflare's implementation](https://blog.cloudflare.com/code-mode-mcp/)

Use parameters for variation within the same operation, and separate tools when intent, authorization or consequences differ. Examples below are proposed contracts, not existing interfaces:

| Proposed tool | Why this boundary helps |
| --- | --- |
| `builds_list(team, pipeline, job, status, limit, cursor)` | Filters belong in one bounded query |
| `pipeline_set_paused(team, pipeline, paused)` | One state-setting operation; combine only if authorization remains equivalent |
| `build_trigger(...)`, `build_abort(...)`, `pipeline_delete(...)` | Distinct actions retain clear schemas, permissions and side-effect metadata |
| `build_summary(build_id, include_failed_steps)` | A useful aggregate read can reduce repeated calls without replacing underlying operations |

Avoid enormous `manage(action, ...)` schemas with many conditionally required arguments. Return structured data, stable identifiers, pagination, bounded logs and actionable errors. Build triggering should return a build/run handle immediately; status, logs and cancellation should work through explicit identifiers across API, CLI and MCP. File uploads and interactive execution need explicit transfer/session contracts, not server assumptions about the client's local filesystem. These are JetBridge design recommendations informed by primary tool-design guidance. [Anthropic tool-design guidance](https://www.anthropic.com/engineering/writing-tools-for-agents)

Read-only, destructive and idempotency annotations improve client behavior but cannot enforce access controls. Recheck authorization on execution even if discovery filtered the tool list. [MCP annotations](https://blog.modelcontextprotocol.io/posts/2026-03-16-tool-annotations/)

## Make parity enforceable

Maintain one operation registry with stable IDs, input/output schemas, permission checks, side effects and adapter mappings. Distinguish product capabilities from local presentation and transport helpers: `fly targets` is local configuration; executing a task is a product workflow involving upload, scheduling and logs. Put UI availability in the registry as optional presentation coverage.

Derive inventories and documentation from this registry where possible. Require every product operation to map to API, CLI and MCP execution. Test authorization equivalence, including cross-team denials, and representative end-to-end workflows. Coverage checks must fail on an empty inventory and must not depend on a fixed operation count, per repository conventions.

Prioritize: (1) repair renewable login and revocation; (2) inventory operations and preserve the existing API authorization boundary; (3) add a typed MCP adapter and complete missing CLI/API mappings; (4) evaluate discovery with failure diagnosis, job reruns, pipeline updates and access administration. Measure successful task completion, wrong-tool calls, schema errors, latency and context use before deciding whether a search-and-execute fallback is useful.
