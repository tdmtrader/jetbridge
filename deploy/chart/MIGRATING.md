# Chart 0.1.3: standard runtime and MCP

The artifact daemon, daemon mTLS, signed artifact resolution, and authenticated
MCP are standard capabilities. Their deployment configuration is explicit; empty
prerequisites cause rendering to fail instead of disabling a capability.

Hangar strict inputs, the output plane, activation, and the dependent public Run
creation gate are unchanged. This upgrade does not require GCS unless the
installation separately enables Hangar. It does not change global resource
sharing, rerun policy, credential providers, auditing, or optional infrastructure.

## Values migration

| Previous setting | Change |
| --- | --- |
| `artifactDaemon.enabled` | Remove it, including `true`. The daemon always renders. |
| `artifactDaemon.tls.enabled` | Remove it. mTLS always renders. |
| `artifactDaemon.tls.existingSecret` | Preserve the existing reference and set `artifactDaemon.tls.source: existingSecret`. |
| Implicit certificate generation | Select `artifactDaemon.tls.source: generated` explicitly and leave `existingSecret` empty. Live Helm only; offline/GitOps rendering cannot preserve generated material. |
| `artifactDaemon.resolveCapability.existingSecret` | Now required. Preserve an existing key; otherwise provision a Secret with `resolve.key`, exactly 32 random bytes. |
| `--enable-mcp` and `--mcp-client-config` in `web.extraArgs` | Remove them. Copy the existing registered clients into `mcp.clients`; the chart renders and mounts their ConfigMap. |
| `--mcp-disable-operation` | Move operation IDs into `mcp.disabledOperations`. Restrictions are preserved. |
| Environment equivalents of the above chart-owned arguments | Remove them and use the corresponding explicit values. Conflicting overrides fail rendering. |

Keep `postgresql.enabled`, ingress/native TLS choices, monitoring resources,
network policies, PDB, service-account ownership, and other infrastructure
selections explicit. No component is selected from the presence of an endpoint.

MCP requires at least one registered public client. Preserve its exact ID, name,
and redirect URIs; do not substitute a wildcard. Registration does not grant
permissions. OAuth consent, team permissions and operation restrictions still
apply. Use an HTTPS external origin (HTTP is supported only on loopback); MCP
cannot be hosted under a URL subpath. Keep existing database encryption and
session-signing keys. Registering clients does not configure either key for you.

If an old extra ConfigMap mount also supplies a custom CA, retain that mount and
its `SSL_CERT_FILE` setting. Only the MCP registration JSON moves to the new
chart-managed mount at `/etc/concourse/mcp-clients`. Client registration changes
update a pod-template checksum so web reloads the file on rollout.

## Current home cluster

Apply these edits to the existing Argo values; this is a patch guide, not a
replacement values file. Keep the separately managed PostgreSQL deployment,
ingress, encryption, signing keys, registry, and resource settings.

```yaml
artifactDaemon:
  tls:
    source: existingSecret
    existingSecret: concourse-artifact-daemon-tls-pinned
  resolveCapability:
    existingSecret: concourse-artifact-daemon-resolve-capability
mcp:
  clients:
    # Copy all current entries from concourse-mcp-clients/clients.json verbatim.
    - client_id: jetbridge-reference
      client_name: JetBridge reference client
      redirect_uris:
        - http://127.0.0.1:8964/callback
  disabledOperations: [] # Preserve any currently configured restrictions.
```

Remove the old `enabled` fields and extra MCP arguments as described above.
The client entry is illustrative: preserve the deployed name and complete list.
The existing `/etc/concourse/mcp` mount contains certificate trust material and
must remain while `SSL_CERT_FILE` references it. No new output-plane bucket,
cloud identity, activation epoch or Run key is needed for this upgrade.

Argo currently follows `core` for the chart while pinning the application image
separately. Coordinate the chart revision and values update before publishing:
old values are deliberately rejected. The home-infra checkout can lag the live
Argo values, so reconcile against the deployed Application before editing it.

## Upgrading from JetBridge 0.3.1

Upgrade the application image and chart together: 0.3.1 has no authenticated MCP
endpoint. Preserve the explicit database mode and existing credentials. Supply
MCP registrations, daemon certificate ownership and the resolve-key Secret.
If the old deployment used plaintext daemon traffic or unsigned resolution,
coordinate web and daemon replacement during a quiet build window: mixed old
and new processes cannot necessarily communicate during rollout.

Test the intervening database migrations against a restored copy before release,
and retain a recoverable backup. Existing migration machinery applies the schema
updates; this chart cleanup adds no migration. Preserve database encryption if
already configured. If introducing encryption, use the documented coordinated
key rollout; do not rotate or generate a new key implicitly during this change.

## Validation and rollout

1. Render the new chart with the complete migrated values and resolve every
   prerequisite error. Bare defaults intentionally cannot invent clients or
   external Secrets. Do not use `--reuse-values` without removing obsolete keys.
2. Verify referenced Secrets exist with the required keys, and certificates
   have the daemon service's expected SANs. Rendering cannot verify their contents.
3. Test the chart and matching application image together. Keep optional
   integrations and all Hangar settings at their existing explicit selections.
4. Roll out through the deployment's existing release process; verify existing
   pipelines and cross-node artifact transfer, MCP login/consent, allowed
   operations and operation restrictions. Keep generated certificate mode out
   of Argo/GitOps deployments.
