# Spec: Helm Chart Parity

## Overview

The JetBridge Helm chart (`deploy/chart/`) is missing several standard Kubernetes service and deployment configuration options that the upstream [concourse/concourse-chart](https://github.com/concourse/concourse-chart) provided. These gaps prevent common production deployment patterns: static LoadBalancer IPs, cloud provider annotations (e.g. AWS ALB cert ARNs), native TLS termination at the web container, and mounting TLS certificate secrets into the pod.

The Concourse `web` binary already supports `--tls-bind-port`, `--tls-cert`, `--tls-key`, and `--enable-lets-encrypt` — the chart simply needs to wire these through.

## Requirements

1. **Service annotations and loadBalancerIP** — Add `service.annotations`, `service.loadBalancerIP`, and `service.loadBalancerSourceRanges` to values.yaml and the service template, matching upstream concourse-chart conventions.
2. **Optional HTTPS container port** — Add `web.tls.enabled`, `web.tls.bindPort` (default 443) to values.yaml. When enabled, add an `https` containerPort to the web deployment and pass `--tls-bind-port`, `--tls-cert`, `--tls-key` flags to the web command.
3. **TLS secret volume mount** — When `web.tls.enabled` is true, mount a Kubernetes Secret (configurable name via `web.tls.secretName`) containing `tls.crt` and `tls.key` into the web pod at a configurable path.
4. **Extra volumes and volume mounts** — Add `web.extraVolumes` and `web.extraVolumeMounts` to values.yaml and the web deployment template. This covers custom CA bundles, credential files, and other operator-specific mounts.
5. **Service HTTPS port** — When `web.tls.enabled`, add an `https` port to the service pointing at the TLS container port.
6. **Health probe port awareness** — When TLS is enabled and HTTP is disabled (bind-port 0), probes should use the HTTPS port and scheme.
7. **Values.yaml documentation** — All new fields documented with comments matching the existing style.

## Acceptance Criteria

- `helm template` with default values produces identical output to before (no regressions).
- `helm template` with `web.tls.enabled=true` produces a deployment with HTTPS port, TLS secret mount, and correct CLI flags.
- `helm template` with `service.annotations` and `service.loadBalancerIP` set produces a service with those fields.
- `helm template` with `web.extraVolumes`/`web.extraVolumeMounts` produces a deployment with those mounts.
- Values schema is portable: users migrating from the upstream concourse-chart can map their existing `web.tls.*` and `service.*` values with minimal translation.

## Out of Scope

- Worker StatefulSet (removed — JetBridge has no workers)
- TSA service (removed — JetBridge registers workers via direct DB)
- Prometheus metrics service (separate concern)
- Let's Encrypt integration (the `web` binary supports it via `--enable-lets-encrypt`, but wiring it through the chart is a separate track — users can use `web.extraArgs` in the meantime)
- Multiple separate services (upstream had separate API/worker-gateway/prometheus services — JetBridge only needs the single web service)
