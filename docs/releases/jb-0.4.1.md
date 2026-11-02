# JetBridge 0.4.1

JetBridge 0.4.1 follows jb-0.4.0 (afc933f94e). It restores capabilities that 0.4.0 removed because the home cluster did not use them, even though other deployments do. The aim of 0.4.0's cleanup was the chart's option sprawl, not what the binary can do. A deployment that skipped 0.4.0 should upgrade from 0.3.1 straight to 0.4.1: it keeps its `var_sources` and idtoken signing keys, which 0.4.0's migration would have dropped.

## Restored

- **Authentication connectors.** GitHub, GitLab, LDAP, SAML, OIDC, OAuth, Cloud Foundry, Bitbucket Cloud and Microsoft are back on `concourse web` and `fly set-team`. A team file that names one is accepted again, alone or mixed with local users. The chart configures them through `web.env`, e.g. `CONCOURSE_OIDC_*`, `CONCOURSE_MAIN_TEAM_OIDC_GROUP`, with secrets via `valueFrom`.
- **Credential managers.** Vault, CredHub, AWS SSM, AWS Secrets Manager and Conjur are back on the web and in `fly validate-pipeline`, configured through `web.env`.
- **Pipeline `var_sources` and `idtoken`.** Pipelines may declare `var_sources` again, including `idtoken` for OIDC federation to cloud providers. The web publishes `/.well-known/openid-configuration` and its JWKS again, and `--oidc-issuer-url` and the `--signing-key-*` flags return.
- **Images from any resource type.** A task's `image_resource` and a resource type's image may again name any type, a custom `registry-image` included. The step falls back to running that type's check and get whenever the native registry resolver cannot read the source: ECR keys, `ca_certs`, an insecure registry, or a non-registry type. `check --shallow` returns with it, and a `docker-image` image (a built-in base type) works again instead of failing with 0.4.0's "not registry-image" sentence.
- **Encryption key rotation.** `concourse migrate --encryption-key NEW --old-encryption-key OLD` re-encrypts the database again, and `--old-encryption-key` alone decrypts it. The web starts without a key again, but the chart still requires one.
- **`web.env`.** The pass-through is back as the place for whatever the chart does not type. A 0.4.0 values file renders unchanged. An entry naming a variable whose typed value is set still fails the render.
- **OpenTelemetry metrics.** The `--otel-metrics-*` flags and the chart's `otelMetrics.otlpAddress`, `otlpHeaders` and `otlpUseTLS` export the web's OTel metric instruments over OTLP gRPC again. Instrument names are unchanged from 0.3.1.
- **Google Cloud Trace.** `--tracing-stackdriver-projectid` (`CONCOURSE_TRACING_STACKDRIVER_PROJECTID`) exports traces to Cloud Trace again. If OTLP is configured as well, OTLP wins.

## Fixes

- The key-rotation passes of `concourse migrate --old-encryption-key` read a column's rows before writing any back. As in 0.3.1 they updated rows while their cursor was still open, which waits forever on a pool of one connection. Encrypting leftover plaintext keeps 0.4.0's batched form, and a keyed web reads a row without a nonce as plaintext rather than erroring.

## New

- **`kubernetes.baseResourceTypes`** (chart) maps a resource type name to an image, rendering one `--kubernetes-base-resource-type=name=image` per entry. It overrides a built-in type, such as your own `registry-image`, or adds one that pipelines name without declaring it, such as `git-with-ado`.

## Still removed

Telemetry is consolidated on Prometheus, OpenTelemetry and Google Cloud:

- the DogStatsD, InfluxDB and New Relic metric emitters, and the Lager emitter;
- the Jaeger and Honeycomb trace exporters (both accept OTLP).

The other 0.4.0 removals stand: Let's Encrypt, the audit log, the syslog drainer, OPA, `fly workers`/`fly volumes`, `cc.xml`, `prototypes:`, the docker-compose dev stack, and the rest listed in the [0.4.0 notes](jb-0.4.0.md#upstream-features-removed).

## Database

- Migration `1789793165` no longer drops anything. On 0.4.0 it dropped `signing_keys`, `pipelines.var_sources` and `pipelines.nonce`.
- New migration `1789793166` re-creates them, empty, if they are missing.
- A database already on 0.4.0 gets the table and columns back without data. A 0.3.1 database upgraded straight to 0.4.1 keeps its data.

## Chart (0.2.0 → 0.2.1)

- `web.env` is back.
- `otelMetrics.*` is back.
- `kubernetes.baseResourceTypes` is new.

## Operator notes

- **Do not run 0.4.0 on a deployment that uses an external auth connector, a vendor credential manager, `var_sources`, a custom image type, or key rotation.** Go from 0.3.1 straight to 0.4.1. The other 0.4.0 operator notes still apply, including removing `run:` steps and `prototypes:` before upgrading.
- A deployment that ran 0.4.0 and set `var_sources` before it must set them again. 0.4.0 refused them at `set-pipeline` and dropped the stored column.
- 0.4.0's idtoken signing keys were dropped, so any cloud-provider trust in the old keys has to pick up the newly published JWKS. The keys rotate in on first start.
