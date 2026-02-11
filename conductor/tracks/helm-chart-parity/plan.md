# Plan: Helm Chart Parity

## Phase 1: Service template enhancements

- `[x]` Add `service.annotations`, `service.loadBalancerIP`, `service.loadBalancerSourceRanges`, `service.labels` to `values.yaml`
- `[x]` Update `templates/service.yaml` — render annotations, loadBalancerIP, loadBalancerSourceRanges, extra labels when set
- `[x]` Verify `helm template` with defaults produces unchanged output

## Phase 2: TLS support in web deployment

- `[x]` Add `web.tls.enabled`, `web.tls.bindPort`, `web.tls.secretName`, `web.tls.mountPath`, `web.tls.certFilename`, `web.tls.keyFilename` to `values.yaml`
- `[x]` Update `templates/web-deployment.yaml` — when `web.tls.enabled`:
  - Add `https` containerPort
  - Add `--tls-bind-port`, `--tls-cert`, `--tls-key` to args
  - Mount TLS secret volume at `web.tls.mountPath`
- `[x]` Update `templates/service.yaml` — when `web.tls.enabled`, add `https` service port mapping to `web.tls.bindPort`
- `[x]` Update liveness/readiness probes — probes are fully user-configurable via `web.livenessProbe`/`web.readinessProbe` values; users override port/scheme when using TLS-only

## Phase 3: Extra volumes and volume mounts

- `[x]` Add `web.extraVolumes` and `web.extraVolumeMounts` to `values.yaml`
- `[x]` Update `templates/web-deployment.yaml` — render `extraVolumes` into pod volumes and `extraVolumeMounts` into container volumeMounts

## Phase 4: Validation

- `[x]` Run `helm template` with defaults — confirm no diff vs current output
- `[x]` Run `helm template` with TLS enabled — confirm HTTPS port, secret mount, CLI flags present
- `[x]` Run `helm template` with service annotations/loadBalancerIP — confirm service rendered correctly
- `[x]` Run `helm template` with extraVolumes/extraVolumeMounts — confirm volumes rendered
- `[x]` Update `deploy/chart/README.md` with new configuration options

## Phase 5: Commit and push

- `[x]` Commit all changes to `jetbridge` branch — `dbf279a65`
- `[x]` Push to `jetbridge` remote
