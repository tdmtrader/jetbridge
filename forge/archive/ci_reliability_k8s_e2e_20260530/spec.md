# Spec: k8s-e2e CI reliability — kill stale-image testing + OOM flakiness

**Track ID:** `ci_reliability_k8s_e2e_20260530`
**Type:** refactor (CI infrastructure)
**Origin:** surfaced while resolving `resource_config_scope_fk_leak_fix_20260530`.

## Problem

While root-causing a "flaky" FK behavioral spec, we found the k8s-e2e pipeline
had been **testing stale code for months**, masking the FK fix's effectiveness and
hiding at least two other failures. Two distinct reliability defects:

### 1. Stale-image testing (the big one)
`deploy/Dockerfile.kind-runner` bakes the **entire source tree** (`COPY . .` →
`/src`) into the kind-runner image. The integration/behavioral tasks then
`cd /src && go build …`. The image is referenced by a **mutable tag**
(`rootfs_uri: docker:///registry.home/concourse-kind-runner:v35`), and the
Concourse worker serves that tag **from cache** — so `build-kind-runner`'s fresh
push to the same tag is ignored, and the tasks compile **stale `/src`**.

Evidence: pushing fresh code + rebuilding the image did NOT change what CI ran
(instrumentation absent); only bumping the tag (v33→v34→v35) forced a fresh pull.
That tag-bump is a band-aid that must be repeated on every change.

### 2. Integration/behavioral OOM flakiness
The integration job errored in ~3 of 5 runs (setup or mid-run), with `fly`/task
SIGKILLs (exit 137) — OOM/resource pressure in the heavy KinD-in-DinD task pods.
Runs passed only on manual retry.

## Fix

### Decouple volatile source from the stable toolchain image
The kind-runner image should be a **stable toolchain** (go, docker, kubectl, helm,
ginkgo, go-mod cache) that changes rarely. The **source under test** should come
**fresh from a `repo` git resource** in each test job (`cd repo`), not from baked
`/src`. Then:
- A code change is tested fresh by CI **without bumping the image tag** (the
  common case), because the baked `/src` is no longer used.
- The toolchain image's mutable-tag caching becomes harmless — it only needs a
  tag bump when `Dockerfile.kind-runner` itself changes (rare).

### Auto-retry transient OOM
Add `attempts: 2` to the privileged integration/behavioral tasks so transient
OOM/resource-pressure errors retry automatically (matches the observed
"errors once, passes on retry" behavior).

## Acceptance Criteria

- [ ] integration + behavioral tasks build from the `repo` git resource
      (`cd repo`), not baked `/src`.
- [ ] A source change is reflected in a CI run **without** bumping the
      kind-runner image tag (validated by a marker change or by confirming
      instrumentation/log output reflects HEAD).
- [ ] integration + behavioral tasks have `attempts: 2`.
- [ ] The chain (build-kind-runner → integration → behavioral) passes on the
      restructured pipeline.

## Out of Scope

- Full immutable-tag/digest pinning of the toolchain image (the toolchain rarely
  changes; the source decoupling solves the day-to-day staleness). Noted as a
  future hardening.
- The live `cicd` deploy's `registry.home/jetbridge:latest` daemon-image
  staleness (separate deploy concern).
- Node-capacity / pod-memory tuning for the OOM beyond `attempts` (needs cluster
  sizing data).
