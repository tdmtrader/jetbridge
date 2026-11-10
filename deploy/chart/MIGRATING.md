# Chart 0.2.0: standard runtime and MCP

The artifact daemon, daemon mTLS, signed artifact resolution, and authenticated
MCP are standard capabilities. Their deployment configuration is explicit; empty
prerequisites cause rendering to fail instead of disabling a capability.

Hangar has two namespaces, cache and output, and one tree path: every Run
input is an input publication in the output namespace, read back by a managed
read. The separate input plane and its values are gone (see the values
migration below), and the resource-cache object-store tier is no longer
configurable from the chart. Hangar's output plane has no activation step any
more: the activation walk Job, the inventory
and reclaimer Deployments and the activation database role are gone, the web
runs reclaim and the orphan sweep itself, and whether the plane is in service
is `hangarOutput.webEnabled` alone (see the values migration below). This upgrade
does not require GCS unless the installation separately enables the output
plane.
It does not change credential providers or optional infrastructure.

## Values migration

| Previous setting | Change |
| --- | --- |
| `artifactDaemon.resolveCapability.ttl` and `--kubernetes-artifact-daemon-resolve-capability-ttl` | Remove the value. Expiry is derived from effective pod scheduling and startup timeouts, the 31m fetch retry budget and one 5m margin (56m at defaults; 101m with 60m scheduling and 5m startup). The chart stops rendering the flag, and web no longer parses it: a web given it exits on the unknown flag, so a Deployment still carrying it (one rendered by a chart before 0.2.0, or patched with `kubectl set image`) must lose it before the image moves. Keep the existing resolve key. The nonce and token format are unchanged, so pending pods and mixed web/daemon versions need no token cutover. |
| `POST /resolve` | Removed; use `POST /resolve-batch` with `{"items":[{"key":"...","dest":"...","capability":"..."}]}` and read `results[0]`. Product fetch init containers already use the batch route. |
| `artifactDaemon.enabled` | Remove it, including `true`. The daemon always renders. |
| `artifactDaemon.tls.enabled` | Remove it. mTLS always renders. |
| `artifactDaemon.durable` | Remove it. The closed schema rejects it as an unknown key. The daemon renders no `--durable-*` flag or credential mount, and the `ArtifactDaemonDurableStoreErrors` alert is gone. If you used the tier: its reclaim walk stops with it, so expire the objects already in the bucket with a lifecycle rule on the prefix (or delete the bucket), and an S3 credentials Secret the chart referenced is now unused. The chart offers no way to keep the tier. |
| Standalone daemon `--durable-*`, `--cache-bucket`, web `--kubernetes-artifact-daemon-cache-bucket` / `--kubernetes-artifact-daemon-warm-timeout`, and `hangar-store --cache-namespace` | Removed. Resource caches remain node-local and may be fetched from peers; a miss reruns the get. Remove these arguments before upgrading. Objects left in an old cache store are no longer read or reclaimed; retire that store separately. |
| `hangarOutput.cacheBucket` | Removed. Delete this value; Hangar is the only long-term storage plane. |
| `artifactDaemon.preemption` | Remove it. The closed schema rejects it as an unknown key. The GCP spot preemption watcher (`--preemption-watch`, `--preemption-budget`) is gone; on SIGTERM the daemon drains in-flight mirror jobs before it exits. |
| `artifactDaemon.hangar.store`, `.bucket`, `.endpoint`, `.prefix`, `.enabled`, `.webEnabled`, `.scratchPath`, `.scratchSizeLimit`, `.maxContentBytes`, `.maxEntries`, `hangarOutput.strictInputBucket` | Remove all eleven. The closed schema rejects each as an unknown key. The separate input plane is gone: every Run input is an input publication in the output namespace (`hangarOutput.bucket`), read back by a managed read the output plane serves, so there is no input store, bucket, scratch, limit, enable switch, web switch or ready label (`concourse.dev/hangar-v1` is set by nothing; a pod with a tree input requires the execution-control and output ready labels). `artifactDaemon.hangar.capabilityTTL` stays; the one Hangar key signs read, execution control and output capture warrants and is now the artifact daemon key (see [One artifact daemon key](#one-artifact-daemon-key-2026-11-06)). The one scratch and the one set of tree limits are `artifactDaemon.outputScratch.{path,sizeLimit,maxContentBytes,maxEntries,concurrency}`; the daemon is now handed `--output-max-content-bytes` and `--output-max-entries` from them, and bounds captures, input publications and managed reads alike. The daemon's `--hangar-enabled`, `--hangar-store`, `--hangar-bucket`, `--hangar-endpoint`, `--hangar-prefix`, `--hangar-store-id`, `--hangar-token-file`, `--hangar-ca-cert`, `--hangar-scratch-dir`, `--hangar-max-content-bytes`, `--hangar-max-entries` and `--strict-input-bucket`, and the web's `--kubernetes-hangar-enabled` and `--kubernetes-hangar-input-bucket`, are gone; a binary given one exits on the unknown flag. `hangar-store` takes no `--input-namespace`, and its credentials Secret (`hangarStorage.disk.credentials.existingSecret`) must lose its `input` key: the store refuses a `server.json` naming an unrecognized role. Trees in the old input namespace or bucket are orphaned: nothing reads or expires them, so delete them or expire the prefix with a lifecycle rule. The `ArtifactDaemonRefusingForLoad` alert is gone with the refusal reason it watched; `ArtifactDaemonRefusingSignedRequests` replaces it. |
| `hangarOutput.activation` (`target`, `serviceAccount`, `job.*`) | Remove it. The closed schema rejects it as an unknown key. There is no activation walk Job and no activation epoch table: the web writes its `hangar_enabled` row at startup from `hangarOutput.webEnabled`, and that is whether the output plane is in service. To drain, set `hangarOutput.webEnabled=false`, wait for `fly hangar-status` to report zero residue, then turn the daemon's output plane off. |
| `hangarOutput.inventory`, `hangarOutput.reclaimer` | Remove them. The closed schema rejects each as an unknown key. Their Deployments and service accounts are gone: the web runs reclaim (`hangarOutput.reclaim.interval`, `.deleteTimeout`, `.batch`; defaults 1m, 2m, 10) and the orphan sweep (`hangarOutput.orphanSweep.interval`, default 1h). On GCS, move the inventory's list and the reclaimer's get and delete grants on the output bucket to the web's cloud principal (`serviceAccount.annotations`), which must not be the artifact daemon's. On the disk store the web mounts the store's `inventory` and `reclaimer` tokens, and the store's NetworkPolicy admits web. |
| `hangarOutput.database`, `hangarBootstrap.database` | Remove them. The closed schema rejects each as an unknown key. The activation database role and its bootstrap Job are gone; only the web touches the output plane's tables, as web's own database user. Drop the role and its Secret once nothing runs as them. |
| Upgrading past database migration `1789793154` with the output plane in service | Drain the output plane first (`hangarOutput.webEnabled=false`, wait for `fly hangar-status` to report drained). The output scope derives from the tenant and the store (H(tenant, store)), and new objects are published under it. Objects published under the earlier epoch-derived scope are not readable: nothing reads or reclaims them, and they carry no store marker, so the orphan sweep counts them as foreign and never deletes them. Delete them, or expire the prefix with a bucket lifecycle rule. |
| `hangarOutput.activationEpoch`, `hangarOutput.executionControl.keySecret`, `.keyID`, `.publicKeys`, `hangarBootstrap.referencedKeys` | Remove them. The closed schema rejects each as an unknown key. The node daemon is inside the trusted computing base and is reached over mTLS, so its acknowledgement is its answer on that channel and nothing it says is signed: there is no node control key, no key id, no verification ring (the `-hangar-output-control-keys` ConfigMap and the bootstrap's `-hangar-rings-*` Secret are gone) and no control-key generation. The daemon is handed no `--control-key-id`, `--control-key-file` or `--activation-epoch`; its output plane is mounted by `--execution-control`, and it verifies every warrant with the one Hangar key (`--hangar-key`, now from `secrets.artifactDaemonKeySecret`; see [One artifact daemon key](#one-artifact-daemon-key-2026-11-06)). The web is handed no `--kubernetes-hangar-output-activation-epoch` or `--kubernetes-hangar-output-control-keys`. A Run result binds as an input while its Run succeeded and its claim is live, whatever key was in use when it was published. Delete the control-key Secrets once nothing mounts them. |
| `hangarOutput.capabilityKeySecret`, `hangarOutput.materializationKeySecret`, `hangarOutput.materializationKeyID` | Remove them. The closed schema rejects each as an unknown key. One Hangar key now signs every warrant -- read, execution control and output capture -- and every artifact daemon verifies against it; it is now the artifact daemon key, `secrets.artifactDaemonKeySecret` (see [One artifact daemon key](#one-artifact-daemon-key-2026-11-06)). The daemon is handed `--hangar-key=<path>` (renamed from `--hangar-warrant-key`) and `--execution-control` in place of `--capability-key`; `--capability-key`, `--capability-ttl`, `--materialization-key-file`, `--materialization-key-id` and `--hangar-warrant-ttl` are gone, and a daemon given one exits on the unknown flag. The web is handed `--kubernetes-hangar-key=<path>` (renamed from `--kubernetes-hangar-warrant-key`) with `--kubernetes-hangar-warrant-ttl`; `--kubernetes-hangar-output-warrant-key` and `--kubernetes-hangar-output-materialization-key` are gone. Both now mount it at `/etc/concourse/artifact-daemon-key/artifact-daemon.key` (volume `artifact-daemon-key`). Warrants signed under the old keys stop verifying at the switch: drain the output plane first (`hangarOutput.webEnabled=false`, wait for `fly hangar-status` to report no residue). Delete the two old Secrets once nothing mounts them. `web.runInputSigningKeySecret` stays web-only and separate from the artifact daemon key Secret. |
| Upgrading past database migration `1789793155` | Roll the web with `web.strategy: {type: Recreate}`. The migration renames the Run cancellation queue's capture kind (`handoff_classify` becomes `capture_cancel_or_settle` and two dead kinds go); an old web overlapping the new one would misread the kind. |
| Alerts | `HangarOutputInventorySweepStalled`, `HangarOutputInventoryDebtGrowing` and `HangarOutputOperationLeaseUnheld` are gone with their metrics. New: `HangarOutputCapturesStuckPublishing`, `HangarOutputCapturesUnreleased`, `HangarOutputOrphanSweepStalled`, `HangarOutputOrphanSweepFailing` and `HangarOutputDrainResidue`. |
| `artifactDaemon.tls.existingSecret` | Preserve the existing reference and set `artifactDaemon.tls.source: existingSecret`. |
| Implicit certificate generation | Select `artifactDaemon.tls.source: generated` explicitly and leave `existingSecret` empty. Live Helm only; offline/GitOps rendering cannot preserve generated material. |
| `artifactDaemon.resolveCapability.existingSecret` | Replaced by the required `secrets.artifactDaemonKeySecret`; see [One artifact daemon key](#one-artifact-daemon-key-2026-11-06) for the cutover. |
| `--enable-mcp` and `--mcp-client-config` in `web.extraArgs` (removed) | Remove them. Copy the existing registered clients into `mcp.clients`; the chart renders and mounts their ConfigMap. |
| `--mcp-disable-operation` | Move operation IDs into `mcp.disabledOperations`. Restrictions are preserved. |
| Environment equivalents of the above chart-owned arguments | Remove them and use the corresponding explicit values. Conflicting overrides fail rendering. |
| `rbac.brineLive`, `rbac.brineLiveServiceAccount` | Remove them. The closed schema rejects each as an unknown key. The chart no longer renders the `<fullname>-brine-live` ClusterRole, its ClusterRoleBinding or the named ServiceAccount, and an upgrade prunes them. If you run the brine live tier, declare its ServiceAccount, ClusterRole and ClusterRoleBinding outside the chart, beside the cluster's other test identities (JetBridge's own cluster names all three `jetbridge-brine-live`). Copy the rules from the template's last version, `git show dba4b49803:deploy/chart/templates/brine-live-rbac.yaml`, and write the binding subject's namespace out, since nothing fills it in any more. Map the brine job and `main/one-off` to that ServiceAccount with `kubernetes.stepPodGrants`. Create it before upgrading, so no brine build runs without it. |
| `secrets.create` | Remove it, including `false`. The closed schema rejects it as an unknown key. `secrets.signingKeySecret` is now required; without it the render fails naming it. Create the Secret once: `concourse generate-key -t rsa -f session_signing_key`, or without the concourse binary `openssl genrsa -out session_signing_key 4096` (web reads a PKCS#1 or PKCS#8 PEM), then `kubectl create secret generic <name> --from-file=session_signing_key`, and set `secrets.signingKeySecret: <name>`. A release that ran with `create: true` keeps an orphaned `<fullname>-keys` Secret after the upgrade, because it carried `helm.sh/resource-policy: keep`; nothing mounts it, so delete it by hand. That release's sessions reset once, because the old key lived in each pod's emptyDir and is gone with it. A release already on `create: false` with a named Secret renders unchanged apart from dropping the value. |
| `otelMetrics` and `--otel-metrics-*` | Remove them. ATC OTel metrics instruments and both OTLP/GCP export paths are retired. Use `metrics.enabled` for Prometheus; `tracing.*` still exports traces. OTel-only measurements are retired, not replaced by equivalent Prometheus series. |
| `web.extraArgs` | Remove it. The closed schema rejects it as an unknown key. Move each flag to its typed value: `--cookie-secure` needs nothing: web marks its cookies Secure whenever `web.externalUrl` is `https://` (behind a TLS-terminating proxy with an `http://` URL, set `CONCOURSE_COOKIE_SECURE=true` in `web.env` for now); `--kubernetes-pod-scheduling-timeout` is `kubernetes.podSchedulingTimeout`; `--default-task-cpu-request` is `kubernetes.defaultTaskCPURequest` (millicores); `--kubernetes-preferred-step-node` is `kubernetes.preferredStepNode` (`key=value`); the OTLP flags are `tracing.otlpAddress` and `tracing.serviceName` for traces. Retained flags with no typed value can use their `CONCOURSE_*` variables through `web.env`. |
| `kubernetes.imageRegistryPrefix` | Remove it. The closed schema rejects it as an unknown key. The web flag it rendered, `--kubernetes-image-registry-prefix`, was never read: a resource type's image is a registry-image reference the web resolves and pins as written. `kubernetes.imageRegistrySecret` stays. |
| `web.env` entries for removed upstream web flags (`CONCOURSE_ENABLE_GLOBAL_RESOURCES`, `CONCOURSE_ENABLE_RERUN_WHEN_WORKER_DISAPPEARS`, `CONCOURSE_TELEMETRY_OPT_IN`, `CONCOURSE_ENABLE_LETS_ENCRYPT`, `CONCOURSE_ENABLE_*_AUDITING`, `CONCOURSE_SYSLOG_*`, `CONCOURSE_OPA_URL`, `CONCOURSE_CONCURRENT_REQUEST_LIMIT`, `CONCOURSE_STREAMING_ARTIFACTS_COMPRESSION`, `CONCOURSE_TRACING_{JAEGER,HONEYCOMB,STACKDRIVER}_*`) | Remove them at leisure. The web reads an environment variable only through a declared flag, so one naming a removed flag is inert, not an error; global resources are now unconditional and build rerun on a lost worker is off. The flags themselves fail command-line parsing, and the chart has no value that renders them. |
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
secrets:
  # Created from the old resolve.key bytes; see "One artifact daemon key".
  artifactDaemonKeySecret: concourse-artifact-daemon-key
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
MCP registrations, daemon certificate ownership and the artifact daemon key Secret.
If the old deployment used plaintext daemon traffic or unsigned resolution,
coordinate web and daemon replacement during a quiet build window: mixed old
and new processes cannot necessarily communicate during rollout.

Test the intervening database migrations against a restored copy before release,
and retain a recoverable backup. Existing migration machinery applies the schema
updates; this chart cleanup adds no migration. Preserve database encryption if
already configured. If introducing encryption, use the documented coordinated
key rollout; do not rotate or generate a new key implicitly during this change.

## Migration baseline (2026-10-30)

The 300 upstream migrations up to `1765921815` are now one baseline,
`1765921815_baseline.up.sql`, that runs only on an empty database. A JetBridge
0.3.1 database is past it and needs nothing. A database below it (Concourse
v7.x or earlier) is refused, unchanged, with a message naming JetBridge 0.3.1:
deploy JetBridge 0.3.1 (or Concourse v8.0.x) against it first. The chart's
migration step still runs `concourse migrate --migrate-to-latest-version`;
`--current-db-version`, `--supported-db-version` and `--migrate-db-to-version`
are removed, so drop them from any hand-written job.

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

## C7: Run contract simplification

Remove `web.pipelineRunActivationEpoch`, `web.runResults.readConcurrency` and
`web.runResults.scratchSizeLimit`. The closed schema rejects each as an
unknown key. Run admission has no hold; install or pause templates to control
creation. Credentials and input uploads still require Hangar in service.
Result reads are bounded to two concurrent requests with a fixed 1Gi disk
`emptyDir` at `/var/concourse/run-result-scratch`. Private spool preparation
and crashed-process cleanup remain. The removed binary flags are
`--pipeline-run-activation-epoch`, `--pipeline-run-reclaim-batch`,
`--run-result-scratch-dir` and `--run-result-read-concurrency`. Reclamation
processes at most 20 Runs per pass and still retains detached builds and logs.

Migration `1789793159_simplify_run_contract` requires an empty
`pipeline_run_invocations` table. Stop all web nodes and check
`SELECT count(*) FROM pipeline_run_invocations;` before upgrading. If nonzero,
back up the database and deliberately discard retained invocation evidence
with `TRUNCATE TABLE pipeline_run_invocations;`, then retry the migration.
This forfeits replay and credential-owner evidence for those invocations:
do not resubmit their old keys expecting the same Run or resume credential
handoff for them. Existing headers, payloads, inputs, results and build logs
remain. Reconcile uncertain submissions and finish credential handoffs before
this step. Composition retries likewise must not repeat old calls after the
truncate. Do not truncate any other table or disable its immutability guards.
Every migration from the baseline `1765921815` on is forward-only, including
on empty installs. Restore the pre-upgrade database backup, matching image and
encryption key for recovery; starting an old image does not reliably reject
a newer schema. Follow the [restore procedure](../../docs/migration/DATABASE-MIGRATION-RUNBOOK.md#rollback-procedure).

Release server and bundled clients together: Run responses no longer expose
`activation_epoch` or `run_contract_version`; requests/responses no longer
carry `caused_by_run` or `correlation`; terminal results no longer carry
`version`. The shared Run client and MCP observations use the same new shape.

## C6: local authentication and Kubernetes credentials (2026-10-09, reverted in 0.4.1)

0.4.0 removed the external auth connectors, the Vault, CredHub, SSM, Secrets
Manager and Conjur credential managers, and the DogStatsD, InfluxDB and New
Relic metric emitters. 0.4.1 restores the connectors and the credential
managers, configured through `web.env` as before. The three metric emitters
stay removed: use Prometheus (`metrics.enabled`), OpenTelemetry metrics
(`otelMetrics`) or traces over OTLP or to Google Cloud Trace.

## The encryption key (2026-10-30, rotation restored in 0.4.1)

The chart names one source for the database encryption key,
`secrets.encryptionKeySecret`: a reference to a Secret holding the key under
`encryption-key`, rendered as `CONCOURSE_ENCRYPTION_KEY` on the web container
and on the `migrate-db` init container. A deployment that already sets
`CONCOURSE_ENCRYPTION_KEY` through `web.env` keeps rendering unchanged: the
chart accepts that entry in place of the value and copies it onto
`migrate-db`. Setting both fails the render.

0.4.0 also removed key rotation and the keyless mode from the binary; 0.4.1
restores both. To rotate, stop the webs, run
`concourse migrate --encryption-key NEW --old-encryption-key OLD` against the
database once, then point the Secret at the new key and start the webs. A
database encrypted under a key that is lost cannot be recovered: the backup
and its Secret version travel together
([runbook](../../docs/migration/DATABASE-MIGRATION-RUNBOOK.md#rollback-procedure)).

## Typed values and web.env (2026-10-31, web.env kept in 0.4.1)

The settings deployments commonly passed through `web.env` have typed values.
An entry naming a variable whose typed value is also set fails the render
naming the value. 0.4.0 removed `web.env` itself; 0.4.1 brings it back as the
place for everything the chart deliberately does not type: auth connectors,
credential managers and other binary flags. A values file written for 0.4.0
renders unchanged.

| `web.env` entry | Typed value |
| --- | --- |
| `CONCOURSE_ENCRYPTION_KEY` from a Secret | `secrets.encryptionKeySecret: <Secret name>`; the key must be under `encryption-key` (copy it to that key first if it is elsewhere). Rendered on the web and on `migrate-db`, as before. |
| `SSL_CERT_FILE` | `web.caCertFile: <path>`. Keep the `web.extraVolumes` / `web.extraVolumeMounts` entry that mounts the bundle; the path must lie under its mountPath. |
| `CONCOURSE_AUTH_DURATION` | `web.authDuration` |
| `CONCOURSE_RESOURCE_CHECKING_INTERVAL` | `web.checks.interval` |
| `CONCOURSE_RESOURCE_TYPE_CHECKING_INTERVAL` | `web.checks.resourceTypeInterval` |
| `CONCOURSE_RESOURCE_WITH_WEBHOOK_CHECKING_INTERVAL` | `web.checks.webhookInterval` |
| `CONCOURSE_GLOBAL_RESOURCE_CHECK_TIMEOUT` | `web.checks.timeout` |
| `CONCOURSE_GC_ONE_OFF_GRACE_PERIOD` | `web.gc.oneOffGracePeriod` |
| `CONCOURSE_GC_MISSING_GRACE_PERIOD` | `web.gc.missingGracePeriod` |
| `CONCOURSE_SECRET_RETRY_ATTEMPTS` | `web.secretRetry.attempts` (an integer) |
| `CONCOURSE_SECRET_RETRY_INTERVAL` | `web.secretRetry.interval` |
| `CONCOURSE_COOKIE_SECURE` | Nothing: web marks its cookies Secure whenever `web.externalUrl` is `https://`, which it is wherever users reach the UI over HTTPS, a TLS-terminating proxy included. |
| `CONCOURSE_EMIT_TO_LOGS` | Drop it: the Lager emitter and its `--emit-to-logs` flag are removed, and no flag reads the variable. The chart offers Prometheus (`metrics.enabled`) and OpenTelemetry (`otelMetrics.*`). |
| Any name no `concourse web` flag reads (`CONCOURSE_GC_INTERVAL`, `CONCOURSE_BUILD_TRACKER_INTERVAL`, `CONCOURSE_DB_NOTIFICATION_BUS_QUEUE_SIZE`, ...) | Drop it; it never did anything. |
| Any other `CONCOURSE_*` name | Keep it in `web.env`. Auth connectors (`CONCOURSE_OIDC_*`, `CONCOURSE_GITHUB_*`, `CONCOURSE_MAIN_TEAM_*`, ...), credential managers (`CONCOURSE_VAULT_*`, `CONCOURSE_AWS_SSM_*`, ...) and `CONCOURSE_TRACING_STACKDRIVER_PROJECTID` are configured this way. |

Each typed value is empty by default and then renders nothing, so the binary
keeps its default; durations are Go durations, checked by the schema.

## Security hardening (2026-11-06)

A values file renders unchanged, and nothing here needs an edit to upgrade.
Three changes can still reach a deployment that depends on what they
remove.

- **The artifact helper image is pinned.** `kubernetes.artifactHelperImage`,
  and the binary's own default for `--kubernetes-artifact-helper-image`, are
  `alpine:3.24.4@sha256:93b41ef40b1677fce8fcc64ff4af09daddfacf5040afde618bc46c1e2d1cc7d4`
  instead of `alpine:latest`. A node pulls it once. A cluster that mirrors or
  pre-caches images, or cannot reach Docker Hub, must mirror that exact image
  (by its digest) before upgrading, or set `kubernetes.artifactHelperImage` to
  its own copy; otherwise every step pod with an input waits on its init
  container's image pull. A deployment that already sets the value keeps it.
- **The web ServiceAccount is narrower.** Its pod-manager Role no longer grants
  `apps/deployments` or `apps/daemonsets`, which the web never used, and its
  ClusterRole grants `secrets: get` only when the Kubernetes credential manager
  is on (`kubernetes.credentialManager.enabled`, or `CONCOURSE_KUBERNETES_IN_CLUSTER`
  or `CONCOURSE_KUBERNETES_CONFIG_PATH` in `web.env`). Anything else that runs
  as the web ServiceAccount loses them too: a `kubernetes.stepPodGrants` entry
  or `kubernetes.serviceAccount` naming it, or a job that borrows its token.
  Give such a workload its own ServiceAccount and RBAC before upgrading. The
  chart does not refuse a grant naming the web ServiceAccount.
- **The web's credentials are in a Secret.** The inline database password
  (`postgresql.password`, when `postgresql.existingSecret` is unset) and
  `web.localUsers` are rendered into `<fullname>-web-credentials` and reach
  `migrate-db` and the web as `CONCOURSE_POSTGRES_PASSWORD` and
  `CONCOURSE_ADD_LOCAL_USER`, not as `--postgres-password` and
  `--add-local-user` args. Each holds what the old arg delivered, after YAML's
  reading of the unquoted arg (a ` #` comment and trailing spaces dropped) and
  the kubelet's `$$` -> `$`. A value holding `$(` stays an arg, rendered as
  before, because the kubelet expanded `$(VAR)` in args and never expands a
  Secret's value; so does every local user when one entry does, and either
  value when a `web.env` entry names its variable. A Secret named
  `<fullname>-web-credentials` that the chart does not own must be renamed
  before upgrading. The bundled PostgreSQL pod still reads its password from
  its own env.

## One artifact daemon key (2026-11-06)

One Secret, `secrets.artifactDaemonKeySecret`, holds the artifact daemon key:
exactly 32 random bytes under `artifact-daemon.key`. The web signs every
resolve capability and every Hangar warrant with it and every daemon verifies
them (the two MACs are domain-separated). It is required on every install.
It replaces `artifactDaemon.resolveCapability.existingSecret`,
`artifactDaemon.hangar.keySecret`, `artifactDaemon.hangar.allowGeneratedKey`
and the `hangar.key` entry of the daemon TLS Secret. The closed schema rejects
each removed value as an unknown key; the chart renders no `hangar.key`,
mounts `/etc/concourse/artifact-daemon-key/artifact-daemon.key` in web and
the daemon, and hands it to `--resolve-capability-key` and
`--kubernetes-artifact-daemon-resolve-capability-key` always, and to
`--hangar-key` and `--kubernetes-hangar-key` with
`hangarOutput.executionControl.enabled`. The Hangar bootstrap no longer
creates a warrant key Secret; it still creates the disk store's Secrets and
the Run input signing key, which must be a different Secret.
Resolve has no unsigned mode: the daemon does not start without
`--resolve-capability-key`, and the web does not start without its own.

On an existing cluster, in this order:

1. Create the one Secret from the current resolve key's bytes, so resolve
   capabilities already minted into pending step pods keep verifying:

   ```sh
   kubectl -n <ns> get secret <resolveCapability.existingSecret> \
     -o jsonpath='{.data.resolve\.key}' | base64 -d > artifact-daemon.key
   test "$(wc -c < artifact-daemon.key)" -eq 32
   kubectl -n <ns> create secret generic <new name> --from-file=artifact-daemon.key
   rm artifact-daemon.key
   ```

   Anything outside the chart that reads the old Secret by name must be
   given the new one before step 4: an RBAC Role's `resourceNames` for a
   test or tooling ServiceAccount, for example (JetBridge's live tier reads
   the key from the Secret the daemon mounts, and is refused until its Role
   names the new Secret).

2. If the output plane is on (`hangarOutput.executionControl.enabled`), drain
   it: set `hangarOutput.webEnabled: false`, remove `web.runInputSigningKeySecret`
   and `web.runCredentialWorkerImages` (the chart refuses the first while the
   plane is out of service, and the second without the first; note both for
   step 5), and sync. Then wait until nothing holding an old-key
   warrant is in flight: `fly -t <target> hangar-status` shows
   `pending captures`, `publishing captures` and `unreleased captures` all 0,
   and no reader claim (an in-flight managed read) is open. Do not wait for
   `out of service, drained`: `open claims` also counts consumers' claims on
   retained Run results, which a landing queue keeps open indefinitely and which
   hold no warrant (a result binds whatever key published it).
   `hangar-status` does not separate the two kinds; readers' claims are the
   ones with an expiry, so read them from the database:

   ```sql
   SELECT count(*) FROM hangar_claims
    WHERE released_at IS NULL AND expires_at > now();
   ```

   or, without database access, wait `hangarOutput.operationTimeout` plus five
   minutes after the three capture counts reach 0: no new read is admitted
   while the plane is out of service, and a reader's claim lapses by then
   ([docs/hangar.md](../../docs/hangar.md#in-service-and-the-drain)). Every
   warrant the web signed with the old Hangar key stops verifying in step 4.
   Drain on the OLD chart: a GitOps tool that renders the chart from a moving
   revision cannot render these values once that revision carries this chart,
   so drain before it does, or pin the revision while you drain. No Run is
   admitted while the plane is drained, so a landing queue on the cluster
   lands nothing until step 5: land what must reach the trunk first.
3. Switch values: set `secrets.artifactDaemonKeySecret: <new name>` and remove
   `artifactDaemon.resolveCapability` and the whole `artifactDaemon.hangar`
   mapping (`keySecret`, `allowGeneratedKey` and the `hangar:` key itself);
   keep `hangar:` only to set `capabilityTTL` under it. Keep
   `webEnabled: false` and the two web values removed.
4. Sync, then make sure the web Deployment and the artifact daemon DaemonSet
   have both rolled: both read their keys at startup. The pod templates change,
   so a sync rolls them; otherwise `kubectl -n <ns> rollout restart` each, and
   wait for `kubectl -n <ns> rollout status` on both.
5. Re-enable: set `hangarOutput.webEnabled: true`, restore
   `web.runInputSigningKeySecret` and `web.runCredentialWorkerImages` as they
   were, and sync.
6. Delete the old resolve Secret and the old Hangar key Secret (the bootstrap
   never deletes one), and drop `hangar.key` from an operator-managed daemon
   TLS Secret, once nothing mounts them.

If the order is wrong: a key made from fresh bytes instead of `resolve.key`
fails the input fetch (403) of every step pod created before the restart,
for as long as its capability lives (56m at defaults). Skipping the drain
fails in-flight managed reads and capture control with refused warrants:
restarted daemons refuse old-key warrants and daemons not yet restarted refuse
the new web's. Restarting only one side leaves the same split until the other
restarts, and re-enabling before both have rolled admits work into it.
Deleting an old Secret before the rollout finishes leaves a restarted old pod
unable to mount it. Rendering the new chart before the values change fails,
naming the removed key, and the cluster stays on the old release.

## The privilege mode (2026-11-09)

A new value, `kubernetes.stepPrivilege` (`any | granted`, default `any`),
chooses which step pods may run a privileged container or init container.
Nothing needs an edit to upgrade. `any`
is the behaviour before this value: a step pod is privileged when its task's
`privileged:` or, for a get, put or check, its resource type's says so,
whatever `kubernetes.stepPodGrants` declare, and an `any` render is byte for
byte the render of the chart before it. A grant that already sets
`privileged: true` changes nothing until you choose `granted`.

In both modes, and under every grant, the web refuses a step pod that adds a
Linux capability or joins the host's network, PID or IPC namespace, naming
what it found: a grant's `privileged: true` admits a privileged container
only. No step the runtime builds asks for either, so this changes nothing a
deployment runs today.

Under `granted` the web refuses, when it creates the pod, every privileged step
outside a grant that declares `privileged: true`. The step fails with an error
naming its owner (`<team>/<pipeline>/<job>` or `main/one-off`) and the grant it
lacks, and no Pod or Job is made. A build that can hold no grant (an instanced
pipeline's job, another team's one-off, a Run, or a build the web cannot find)
fails the same way, and a failed build lookup fails any step. A check is
owned by the resource type whose image it runs: a privileged check is admitted
only under a grant whose owner is `<team>/<pipeline>/resource-type/<type>`,
with `privileged: true` and no `serviceAccount` (a type built on a privileged
type needs the privileged type's grant; the type must be a lowercase
identifier). A check of a base type, of an instanced or Run pipeline, or in a
one-off build is refused. A grant binds a team and a pipeline *name*, as a job
grant does: whoever may set the pipeline of that name, including re-creating
it after it is destroyed while the grant remains, gets the grant. A task file can name a granted type as its `image_resource.type` with any source, so grant privileged types only in pipelines whose task configs are trusted. In a job build, a task image of a privileged custom type needs both: the job's grant for its image get and the resource-type grant for its image check. An unknown mode
stops the web at startup. To opt in:

1. Run `hack/step-pod-audit pods` against the cluster (it is read-only). Each
   privileged step pod it reports beyond a grant is an owner that needs one; a
   privileged check pod needs a resource-type grant for the privileged type it
   runs, which the pipeline's `resource_types` show.
2. Add a `kubernetes.stepPodGrants` entry with `privileged: true` for each such
   owner, leaving `serviceAccount` out to keep the default step pod identity's
   account, and sync. Under `any` this changes nothing a step runs.
3. Set `kubernetes.stepPrivilege: granted` and sync; the web logs
   `step-privilege-mode` with the mode and the privileged grants at startup.
   A privileged step of an owner no grant can name (an instanced pipeline's
   job or check, another team's one-off, a Run, a check of a base type) must
   lose its `privileged:` first. `hack/step-pod-audit pods` then holds each
   privileged check pod to the grant its `concourse.dev/step-pod-grant`
   annotation names.

`granted` with no privileged grant is legal: nothing runs privileged. A pod
already running when the mode changes, and a step Job a restarted web resumes,
are not re-checked in this release; let running builds finish before relying
on the gate. To back out, set `any` (or remove the value) and sync.
