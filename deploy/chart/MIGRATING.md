# Chart 0.1.3: standard runtime and MCP

The artifact daemon, daemon mTLS, signed artifact resolution, and authenticated
MCP are standard capabilities. Their deployment configuration is explicit; empty
prerequisites cause rendering to fail instead of disabling a capability.

Hangar strict inputs, the output plane and the dependent public Run creation
gate are unchanged, except that Hangar names its own store and the durable
resource-cache tier is no longer configurable from the chart. Hangar's output
plane has no activation step any more: the activation walk Job, the inventory
and reclaimer Deployments and the activation database role are gone, the web
runs reclaim and the orphan sweep itself, and whether the plane is in service
is `hangarOutput.webEnabled` alone (see the values migration below). This upgrade
does not require GCS unless the installation separately enables Hangar.
It does not change global resource sharing, rerun policy, credential providers,
auditing, or optional infrastructure.

## Values migration

| Previous setting | Change |
| --- | --- |
| `artifactDaemon.enabled` | Remove it, including `true`. The daemon always renders. |
| `artifactDaemon.tls.enabled` | Remove it. mTLS always renders. |
| `artifactDaemon.durable` | Remove it. The render fails with "artifactDaemon.durable has been removed; the durable tier is not configurable from the chart". The daemon renders no `--durable-*` flag or credential mount, and the `ArtifactDaemonDurableStoreErrors` alert is gone. If you used the tier: its reclaim walk stops with it, so expire the objects already in the bucket with a lifecycle rule on the prefix (or delete the bucket), and an S3 credentials Secret the chart referenced is now unused. The chart offers no way to keep the tier. |
| Daemon flags set outside the chart: `--durable-store=s3\|filesystem`, `--durable-path`, `--durable-prefix`, `--durable-s3-region`, `--preemption-watch`, `--preemption-budget` | Removed from `artifact-daemon`; a daemon given one exits on the unknown flag. The cache tier is now `--durable-store=gcs\|disk` against its own bucket or disk namespace (see docs/durable-artifact-storage.md), and the daemon refuses a cache bucket equal to the strict-input one. Objects written under an old `--durable-prefix` (or to an S3 or filesystem store) are orphaned: nothing reads or expires them, so delete them, or expire the prefix with a bucket lifecycle rule. |
| `artifactDaemon.preemption` | Remove it. The render fails with "artifactDaemon.preemption has been removed". The GCP spot preemption watcher (`--preemption-watch`, `--preemption-budget`) is gone; on SIGTERM the daemon drains in-flight mirror jobs before it exits. |
| `artifactDaemon.hangar.enabled` without `hangar.store` | Set `artifactDaemon.hangar.store` (`gcs` or `disk`) and `hangar.bucket`, plus `hangar.prefix` and `hangar.endpoint`. To keep existing trees reachable, set them to the durable tier's old bucket, prefix and endpoint. Hangar no longer inherits the durable tier's store, bucket, endpoint or prefix; without a store the render fails naming `artifactDaemon.hangar.store`. Hangar's store timeout is now the daemon's fixed 5m default; a `durable.timeout` no longer reaches it. |
| `hangarOutput.activation` (`target`, `serviceAccount`, `job.*`) | Remove it. The render fails with "hangarOutput.activation has been removed". There is no activation walk Job and no activation epoch table: the web writes its `hangar_enabled` row at startup from `hangarOutput.webEnabled`, and that is whether the output plane is in service. To drain, set `hangarOutput.webEnabled=false`, wait for `fly hangar-status` to report zero residue, then turn the daemon's output plane off. |
| `hangarOutput.inventory`, `hangarOutput.reclaimer` | Remove them. The render fails naming each. Their Deployments and service accounts are gone: the web runs reclaim (`hangarOutput.reclaim.interval`, `.deleteTimeout`, `.batch`; defaults 1m, 2m, 10) and the orphan sweep (`hangarOutput.orphanSweep.interval`, default 1h). On GCS, move the inventory's list and the reclaimer's get and delete grants on the output bucket to the web's cloud principal (`serviceAccount.annotations`), which must not be the artifact daemon's. On the disk store the web mounts the store's `inventory` and `reclaimer` tokens, and the store's NetworkPolicy admits web. |
| `hangarOutput.database`, `hangarBootstrap.database` | Remove them. The render fails naming each. The activation database role and its bootstrap Job are gone; only the web touches the output plane's tables, as web's own database user. Drop the role and its Secret once nothing runs as them. |
| Upgrading past database migration `1789793154` with the output plane in service | Drain the output plane first (`hangarOutput.webEnabled=false`, wait for `fly hangar-status` to report drained). The output scope derives from the tenant and the store (H(tenant, store)), and new objects are published under it. Objects published under the earlier epoch-derived scope are not readable: nothing reads or reclaims them, and they carry no store marker, so the orphan sweep counts them as foreign and never deletes them. Delete them, or expire the prefix with a bucket lifecycle rule. |
| `hangarOutput.activationEpoch`, `hangarOutput.executionControl.keySecret`, `.keyID`, `.publicKeys`, `hangarBootstrap.referencedKeys` | Remove them. The render fails naming each. The node daemon is inside the trusted computing base and is reached over mTLS, so its acknowledgement is its answer on that channel and nothing it says is signed: there is no node control key, no key id, no verification ring (the `-hangar-output-control-keys` ConfigMap and the bootstrap's `-hangar-rings-*` Secret are gone) and no control-key generation. The daemon is handed no `--control-key-id`, `--control-key-file` or `--activation-epoch`; its output plane is mounted by `--capability-key`, which `hangarOutput.capabilityKeySecret` provides and the base facet requires. The web is handed no `--kubernetes-hangar-output-activation-epoch` or `--kubernetes-hangar-output-control-keys`. A Run result binds as an input while its Run succeeded and its claim is live, whatever key was in use when it was published. Delete the control-key Secrets once nothing mounts them. `web.pipelineRunActivationEpoch`, the Run contract's own epoch, is unrelated and stays. |
| Upgrading past database migration `1789793155` | Roll the web with `web.strategy: {type: Recreate}`. The migration renames the Run cancellation queue's capture kind (`handoff_classify` becomes `capture_cancel_or_settle` and two dead kinds go); an old web overlapping the new one would misread the kind. |
| Alerts | `HangarOutputInventorySweepStalled`, `HangarOutputInventoryDebtGrowing` and `HangarOutputOperationLeaseUnheld` are gone with their metrics. New: `HangarOutputCapturesStuckPublishing`, `HangarOutputCapturesUnreleased`, `HangarOutputOrphanSweepStalled`, `HangarOutputOrphanSweepFailing` and `HangarOutputDrainResidue`. |
| `artifactDaemon.tls.existingSecret` | Preserve the existing reference and set `artifactDaemon.tls.source: existingSecret`. |
| Implicit certificate generation | Select `artifactDaemon.tls.source: generated` explicitly and leave `existingSecret` empty. Live Helm only; offline/GitOps rendering cannot preserve generated material. |
| `artifactDaemon.resolveCapability.existingSecret` | Now required. Preserve an existing key; otherwise provision a Secret with `resolve.key`, exactly 32 random bytes. |
| `--enable-mcp` and `--mcp-client-config` in `web.extraArgs` (removed) | Remove them. Copy the existing registered clients into `mcp.clients`; the chart renders and mounts their ConfigMap. |
| `--mcp-disable-operation` | Move operation IDs into `mcp.disabledOperations`. Restrictions are preserved. |
| Environment equivalents of the above chart-owned arguments | Remove them and use the corresponding explicit values. Conflicting overrides fail rendering. |
| `rbac.brineLive`, `rbac.brineLiveServiceAccount` | Remove them. The render fails with "rbac.brineLive has been removed; the brine live tier's identity is not part of the chart" (and the same for `rbac.brineLiveServiceAccount`). The chart no longer renders the `<fullname>-brine-live` ClusterRole, its ClusterRoleBinding or the named ServiceAccount, and an upgrade prunes them. If you run the brine live tier, declare its ServiceAccount, ClusterRole and ClusterRoleBinding outside the chart, beside the cluster's other test identities (JetBridge's own cluster names all three `jetbridge-brine-live`). Copy the rules from the template's last version, `git show dba4b49803:deploy/chart/templates/brine-live-rbac.yaml`, and write the binding subject's namespace out, since nothing fills it in any more. Map the brine job and `main/one-off` to that ServiceAccount with `kubernetes.stepPodGrants`. Create it before upgrading, so no brine build runs without it. |
| `secrets.create` | Remove it, including `false`. The render fails with "secrets.create has been removed; name a Secret holding session_signing_key in secrets.signingKeySecret, which every web pod mounts". `secrets.signingKeySecret` is now required; without it the render fails naming it. Create the Secret once: `concourse generate-key -t rsa -f session_signing_key`, or without the concourse binary `openssl genrsa -out session_signing_key 4096` (web reads a PKCS#1 or PKCS#8 PEM), then `kubectl create secret generic <name> --from-file=session_signing_key`, and set `secrets.signingKeySecret: <name>`. A release that ran with `create: true` keeps an orphaned `<fullname>-keys` Secret after the upgrade, because it carried `helm.sh/resource-policy: keep`; nothing mounts it, so delete it by hand. That release's sessions reset once, because the old key lived in each pod's emptyDir and is gone with it. A release already on `create: false` with a named Secret renders unchanged apart from dropping the value. |
| `web.extraArgs` | Remove it. The render fails with "web.extraArgs has been removed; pass-through flags are gone". Move each flag to its typed value: `--cookie-secure` needs nothing: web marks its cookies Secure whenever `web.externalUrl` is `https://` (behind a TLS-terminating proxy with an `http://` URL, set `CONCOURSE_COOKIE_SECURE=true` in `web.env` for now); `--kubernetes-pod-scheduling-timeout` is `kubernetes.podSchedulingTimeout`; `--default-task-cpu-request` is `kubernetes.defaultTaskCPURequest` (millicores); `--kubernetes-preferred-step-node` is `kubernetes.preferredStepNode` (`key=value`); the OTLP flags are `tracing.otlpAddress`, `tracing.serviceName` and `otelMetrics.otlpAddress`. A flag with no typed value (OIDC and other auth providers, until they get theirs) goes through `web.env` as its `CONCOURSE_*` variable for now. |
| `image.repository`, `web.externalUrl`, `web.localUsers`, `web.mainTeamLocalUser` | Now required. The chart's `concourse-local` image, `http://localhost:8080` URL and `test:test` main-team admin defaults are gone, and a render that leaves any of them out fails naming it. Set the image repository you deploy, the URL users reach the UI at, the local users (`user:password`, comma-separated) and the main-team admin. A release that relied on the old defaults sets them explicitly to keep its behaviour. |

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
