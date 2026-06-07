# Conductor Growth Experience (CGX)

**Track:** `native_resolver_insecure_ca_certs_20260607`
**Purpose:** Log observations during implementation for continuous improvement analysis.

---

## Origin

- [2026-06-07] Created when closing `native_registry_image_check_resolution_20260324`.
  That track moved all `registry-image` checks to native OCI resolution, but the
  resolver (`atc/imageresolver/resolver.go`) silently dropped the `insecure` and
  `ca_certs` source options the legacy check-pod path supported — a regression for
  private/self-signed/insecure registries. This track restores parity.

## Frustrations & Friction

---

## Patterns Observed

---

## Missing Capabilities

---

## Insights & Suggestions

---
