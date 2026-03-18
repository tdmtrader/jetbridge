# Implementation Plan: JetBridge Documentation Update

## Phase 1: JETBRIDGE.md — Complete Configuration & Feature Reference

- [ ] Task: Add missing kubernetes flags to configuration reference (`--kubernetes-base-resource-type`, `--kubernetes-artifact-store-gcs-fuse`, `--kubernetes-image-registry-prefix`, `--kubernetes-image-registry-secret`, `--kubernetes-service-account`)
- [ ] Task: Add OTel tracing section (all `--tracing-*` flags: OTLP, Jaeger, Honeycomb, Stackdriver, sampling)
- [ ] Task: Add OTel metrics section (all `--otel-metrics-*` flags)
- [ ] Task: Add GC tuning section (`--gc-interval`, `--gc-one-off-grace-period`, `--gc-missing-grace-period`, `--gc-hijack-grace-period`, `--gc-failed-grace-period`, `--gc-check-recycle-period`)
- [ ] Task: Document `skip_download` feature on get steps with example YAML
- [ ] Task: Document configurable base resource types (`--kubernetes-base-resource-type name=image` format) with examples
- [ ] Task: Document GCS Fuse artifact store configuration and annotations
- [ ] Task: Document health endpoint (`GET /api/v1/health`) response schema and K8s probe usage
- [ ] Task: Update sidecar docs to include `image_artifact` references and file-based sidecar config
- [ ] Task: Document direct image references for resource types (`image_ref` field in TypeImage)
- [ ] Task: Phase 1 verification — cross-reference all flags in atc/atccmd/command.go against documented flags

---

## Phase 2: deploy/chart/README.md — Complete Helm Values Reference

- [ ] Task: Add kubernetes.serviceAccount, kubernetes.imageRegistryPrefix, kubernetes.imageRegistrySecret, kubernetes.baseResourceTypes sections
- [ ] Task: Add GCS Fuse configuration section (artifactStorePvc.gcsFuse.*)
- [ ] Task: Add tracing section (tracing.otlpAddress, tracing.serviceName, etc.)
- [ ] Task: Add monitoring section (serviceMonitor.*, alertingRules.*)
- [ ] Task: Add network policy section (networkPolicy.*)
- [ ] Task: Add PDB section (pdb.*)
- [ ] Task: Add ingress section (ingress.*)
- [ ] Task: Add pod security context documentation
- [ ] Task: Phase 2 verification — cross-reference all values in values.yaml against documented values

---

## Phase 3: README.md — Feature Summary Updates

- [ ] Task: Update "What's different" and "Added" sections to mention skip_download, configurable base resource types, health endpoint, OTel, GCS Fuse, direct image refs
- [ ] Task: Phase 3 verification — ensure README accurately reflects current feature set

---
