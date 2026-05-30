# Implementation Plan: k8s-e2e CI reliability

## Phase 1: Decouple source from the toolchain image + auto-retry

- [ ] Add a `repo` git resource to `deploy/k8s-e2e-pipeline.yml`
      (uri https://github.com/tdmtrader/jetbridge.git, branch jetbridge — matches
      the existing jetbridge pipeline + build-kind-runner's clone).
- [ ] `build-kind-runner`: add `get: repo` (trigger) so the source version is an
      input → enables `passed: [build-kind-runner]` consistency downstream.
- [ ] `k8s-integration-tests`: add `get: repo` (passed: [build-kind-runner]);
      add `inputs: [{name: repo}]` to the task; change `cd /src` → `cd repo` and
      drop the `git init` hack (repo is a real checkout); add `attempts: 2`.
- [ ] `k8s-behavioral-tests`: add `get: repo` (passed: [k8s-integration-tests]);
      same task changes (`inputs: [repo]`, `cd repo`, `attempts: 2`).
- [ ] `fly set-pipeline` to validate + apply.

## Phase 2: Validate fresh-source-without-tag-bump

- [ ] Trigger build-kind-runner → integration on the restructured pipeline.
- [ ] Confirm the run builds from the `repo` get (fresh HEAD) — e.g. the
      `Using Concourse image` provenance log / behavioral web-log dump reflect
      current HEAD WITHOUT a tag bump.
- [ ] Confirm `attempts: 2` retries a transient OOM error automatically (or that
      the run is green).

## Phase 3: Follow-up hardening (optional / future)

- [ ] Toolchain image immutability: pin the kind-runner rootfs by digest (a
      registry-image resource get used as the task `image:`) so even Dockerfile
      changes can't serve stale; resolves the insecure-registry caveat first.
- [ ] OOM: right-size task-pod memory / reduce nested concurrency once cluster
      capacity is known.
- [ ] Consider dropping the `COPY . .` source bake from Dockerfile.kind-runner
      entirely (image becomes pure toolchain) once Phase 1 is proven.
