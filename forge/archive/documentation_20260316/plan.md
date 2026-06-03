# Implementation Plan: JetBridge Documentation Update

## Phase 1: JETBRIDGE.md — Complete Configuration & Feature Reference

- [x] e65a5c6d2 Task: Add missing kubernetes flags to configuration reference (`--kubernetes-base-resource-type`, `--kubernetes-artifact-store-gcs-fuse`, `--kubernetes-image-registry-prefix`, `--kubernetes-image-registry-secret`, `--kubernetes-service-account`)
- [x] e65a5c6d2 Task: Add OTel tracing section (all `--tracing-*` flags: OTLP, Jaeger, Honeycomb, Stackdriver, sampling)
- [x] e65a5c6d2 Task: Add OTel metrics section (all `--otel-metrics-*` flags)
- [x] e65a5c6d2 Task: Add GC tuning section (`--gc-interval`, `--gc-one-off-grace-period`, `--gc-missing-grace-period`, `--gc-hijack-grace-period`, `--gc-failed-grace-period`, `--gc-check-recycle-period`)
- [x] e65a5c6d2 Task: Document `skip_download` feature on get steps with example YAML
- [x] e65a5c6d2 Task: Document configurable base resource types (`--kubernetes-base-resource-type name=image` format) with examples
- [x] e65a5c6d2 Task: Document GCS Fuse artifact store configuration and annotations
- [x] e65a5c6d2 Task: Document health endpoint (`GET /api/v1/health`) response schema and K8s probe usage
- [x] e65a5c6d2 Task: Update sidecar docs to include `image_artifact` references and file-based sidecar config
- [x] e65a5c6d2 Task: Document direct image references for resource types (`image_ref` field in TypeImage)
- [x] e65a5c6d2 Task: Phase 1 verification — cross-reference all flags in atc/atccmd/command.go against documented flags

---

## Phase 2: deploy/chart/README.md — Complete Helm Values Reference

- [x] e65a5c6d2 Task: Add kubernetes.serviceAccount, kubernetes.imageRegistryPrefix, kubernetes.imageRegistrySecret, kubernetes.baseResourceTypes sections
- [x] e65a5c6d2 Task: Add GCS Fuse configuration section (artifactStorePvc.gcsFuse.*)
- [x] e65a5c6d2 Task: Add tracing section (tracing.otlpAddress, tracing.serviceName, etc.)
- [x] e65a5c6d2 Task: Add monitoring section (serviceMonitor.*, alertingRules.*)
- [x] e65a5c6d2 Task: Add network policy section (networkPolicy.*)
- [x] e65a5c6d2 Task: Add PDB section (pdb.*)
- [x] e65a5c6d2 Task: Add ingress section (ingress.*)
- [x] e65a5c6d2 Task: Add pod security context documentation
- [x] e65a5c6d2 Task: Phase 2 verification — cross-reference all values in values.yaml against documented values

---

## Phase 3: README.md — Feature Summary Updates

- [x] e65a5c6d2 Task: Update "What's different" and "Added" sections to mention skip_download, configurable base resource types, health endpoint, OTel, GCS Fuse, direct image refs
- [x] e65a5c6d2 Task: Phase 3 verification — ensure README accurately reflects current feature set

---
