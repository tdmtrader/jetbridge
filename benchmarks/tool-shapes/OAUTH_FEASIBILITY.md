# Real OAuth validation boundary

Followup: the [real Codex OAuth probe](../../hack/mcp-oauth-probe/README.md) now
verifies enrollment, grouped reads, persisted refresh rotation, revocation and
new-consent listing. This note records the earlier feasibility assessment; the
scope-diagnosis benchmark itself remains a synthetic stdio experiment.

The scope-diagnosis benchmark uses synthetic stdio tools. The earlier HTTP schema
probe used a synthetic bearer token. Neither is evidence of Codex OAuth enrollment.

The existing Brine reference-client scenario already performs real discovery,
PKCE browser authorization, Dex fixture login, explicit MCP consent and an exact
fresh numeric-loopback callback. It drives `internal/mcpclient.Client.Login`
through a cookie jar and the fixture's `owner` account. The same scenario checks
restart, automatic refresh, rotated credential persistence and logout/revocation.
See `atc/worker/jetbridge/brine/steps/mcp_reference.go` and `mcp_auth.go`.
This fixture flow can run automatically without asking the owner to sign in.

The installed Codex CLI 0.153.1 locally reports these relevant capabilities:

```text
codex mcp add --help
  --oauth-client-id <CLIENT_ID>
  --oauth-resource <RESOURCE>
  --oauth-client-registration <AUTO|CIMD|DCR>
codex mcp login --help
  -c, --config <key=value>
  --scopes <SCOPE,SCOPE>
```

The next actual Codex login should use its own fixture client ID and register the
exact redirect URI emitted by that client. Drive its authorization URL through
the existing fixture login and consent forms, then exercise production listing
and calls through the resulting connection. Keep this registration and test grant
separate from any existing deployment credentials. Callback shape, local test-grant
storage, and refresh behavior need observation in that actual login; CLI help
alone does not establish compatibility.

Pre-registration is an available client path, so no DCR, client-ID metadata
document, or MCP protocol change is inherently required for this test. Initial
authentication and explicit consent are required; fixture accounts allow those
steps to be automated. Testing a real deployment identity may instead require its
owner's browser login and consent. No such owner step was requested or performed
as part of this bounded benchmark.
