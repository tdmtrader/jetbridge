# Chart 0.1.3: standard runtime and MCP

The artifact daemon, daemon mTLS, signed artifact resolution, and authenticated
MCP are standard capabilities. Their deployment configuration is explicit; empty
prerequisites cause rendering to fail instead of disabling a capability.

Hangar strict inputs, the output plane and the dependent public Run creation
gate are unchanged, except that Hangar names its own store and the durable
resource-cache tier is no longer configurable from the chart. Hangar's
activation is not: with execution control on, the chart walks the activation
epoch to `hangarOutput.activation.target` on every sync instead of running one
activation step per values change, so the target must be set to the plane's
live state before upgrading (see the values migration below). This upgrade
does not require GCS unless the installation separately enables Hangar.
It does not change global resource sharing, rerun policy, credential providers,
auditing, or optional infrastructure.

## Values migration

| Previous setting | Change |
| --- | --- |
| `artifactDaemon.enabled` | Remove it, including `true`. The daemon always renders. |
| `artifactDaemon.tls.enabled` | Remove it. mTLS always renders. |
| `artifactDaemon.durable` | Remove it. The render fails with "artifactDaemon.durable has been removed; the durable tier is not configurable from the chart". The daemon renders no `--durable-*` flag or credential mount, and the `ArtifactDaemonDurableStoreErrors` alert is gone. If you used the tier: its reclaim walk stops with it, so expire the objects already in the bucket with a lifecycle rule on the prefix (or delete the bucket), and an S3 credentials Secret the chart referenced is now unused. The chart offers no way to keep the tier. |
| `artifactDaemon.hangar.enabled` without `hangar.store` | Set `artifactDaemon.hangar.store` (`gcs` or `disk`) and `hangar.bucket`, plus `hangar.prefix` and `hangar.endpoint`. To keep existing trees reachable, set them to the durable tier's old bucket, prefix and endpoint. Hangar no longer inherits the durable tier's store, bucket, endpoint or prefix; without a store the render fails naming `artifactDaemon.hangar.store`. Hangar's store timeout is now the daemon's fixed 5m default; a `durable.timeout` no longer reaches it. |
| `hangarOutput.activation.job.mode`, `hangarOutput.activation.job.facet` | Remove them. The render fails with "hangarOutput.activation.job.mode has been removed; set hangarOutput.activation.target". With `hangarOutput.executionControl.enabled`, the chart renders the activation walk as a PostSync hook (and a Helm post-install/post-upgrade hook) that runs on every sync and drains every facet above its target, and `hangarOutput.activation.target` is required, with no default. Before upgrading, set it to the plane's live state, as `SELECT base_state, output_state FROM hangar_output_activation_epochs WHERE epoch_id = <activationEpoch>` reports it: `output` if both facets are `enabled`, `base` if only base is, `off` otherwise. A lower target would drain a live plane. Quote `"off"`; unquoted, YAML reads it as false. `job.finalize` stays and passes `--finalize`. |
| `hangarOutput.database.existingSecret` with execution control | Now required whenever `hangarOutput.executionControl.enabled` is on, not only with capture: the walk Job runs on every sync as the activation database role. |
| Plain `helm install` or `helm upgrade` with execution control | The walk waits up to 10m (its readiness timeout) for the output DaemonSet before each attestation, and Helm waits for hook Jobs only within `--timeout`, 5m by default. Pass a `--timeout` above 10m, and `--wait`, so web has applied the migrations before the database and walk hooks run. Argo is unaffected. |
| `artifactDaemon.tls.existingSecret` | Preserve the existing reference and set `artifactDaemon.tls.source: existingSecret`. |
| Implicit certificate generation | Select `artifactDaemon.tls.source: generated` explicitly and leave `existingSecret` empty. Live Helm only; offline/GitOps rendering cannot preserve generated material. |
| `artifactDaemon.resolveCapability.existingSecret` | Now required. Preserve an existing key; otherwise provision a Secret with `resolve.key`, exactly 32 random bytes. |
| `--enable-mcp` and `--mcp-client-config` in `web.extraArgs` | Remove them. Copy the existing registered clients into `mcp.clients`; the chart renders and mounts their ConfigMap. |
| `--mcp-disable-operation` | Move operation IDs into `mcp.disabledOperations`. Restrictions are preserved. |
| Environment equivalents of the above chart-owned arguments | Remove them and use the corresponding explicit values. Conflicting overrides fail rendering. |
| `rbac.brineLive`, `rbac.brineLiveServiceAccount` | Remove them. The render fails with "rbac.brineLive has been removed; the brine live tier's identity is not part of the chart" (and the same for `rbac.brineLiveServiceAccount`). The chart no longer renders the `<fullname>-brine-live` ClusterRole, its ClusterRoleBinding or the named ServiceAccount, and an upgrade prunes them. If you run the brine live tier, declare its ServiceAccount, ClusterRole and ClusterRoleBinding outside the chart, beside the cluster's other test identities, with the rules the old `templates/brine-live-rbac.yaml` granted, and map the brine job and `main/one-off` to that ServiceAccount with `kubernetes.stepPodGrants`. Create it before upgrading, so no brine build runs without it. |
| `secrets.create` | Remove it, including `false`. The render fails with "secrets.create has been removed; name a Secret holding session_signing_key in secrets.signingKeySecret, which every web pod mounts". `secrets.signingKeySecret` is now required; without it the render fails naming it. Create the Secret once: `concourse generate-key -t rsa -f session_signing_key`, or without the concourse binary `openssl genrsa -out session_signing_key 4096` (web reads a PKCS#1 or PKCS#8 PEM), then `kubectl create secret generic <name> --from-file=session_signing_key`, and set `secrets.signingKeySecret: <name>`. A release that ran with `create: true` keeps an orphaned `<fullname>-keys` Secret after the upgrade, because it carried `helm.sh/resource-policy: keep`; nothing mounts it, so delete it by hand. That release's sessions reset once, because the old key lived in each pod's emptyDir and is gone with it. A release already on `create: false` with a named Secret renders unchanged apart from dropping the value. |

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
   external Secrets. Do not use `--reuse-values`: it carries the previous
   chart's defaults, so a key the new chart removed (for example
   `artifactDaemon.durable`) fails the render even if you never set it. Use
   complete values files, or `--reset-then-reuse-values` (Helm 3.14 or later).
2. Verify referenced Secrets exist with the required keys, and certificates
   have the daemon service's expected SANs. Rendering cannot verify their contents.
3. Test the chart and matching application image together. Keep optional
   integrations and all Hangar settings at their existing explicit selections.
4. Roll out through the deployment's existing release process; verify existing
   pipelines and cross-node artifact transfer, MCP login/consent, allowed
   operations and operation restrictions. Keep generated certificate mode out
   of Argo/GitOps deployments.
