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

- [x] Triggered build-kind-runner → integration on the restructured pipeline.
- [x] CONFIRMED builds from the `repo` get: integration #181 compiled
      concourse/artifact-daemon/fly/integration.test from `cd repo` (path
      `/tmp/build/.../repo/...`), NOT baked `/src`, with no tag bump. Staleness
      fixed at the root.
- [x] CONFIRMED `attempts: 2` retries: build #181 ran twice; the flaky
      "cross-step input" read-after-reap spec failed attempt 1, PASSED attempt 2.
      My #1 daemon-security port-forward fix also passed. Caveat: `attempts`
      does NOT recover early worker-OOM-kills (build #180 errored at DinD startup
      in 35s with no retry) — those need cluster resource sizing (Phase 3).

## Phase 2 result / newly-exposed real bug (separate track)

Running fresh code surfaced a CONSISTENT failure (both attempts) in the
route-artifact/resilience feature:
`Artifact Read After Producer Pod Reap: loads a file-based task config even after
the producing get pod has been reaped` (artifact_read_after_reap_test.go:124) —
exit 2 / "exec stream: pods not found": the file-config fetch after the producer
get-pod is reaped still routes through the reaped pod instead of the DaemonSet.
NOT OOM (dumpDiagnosticsOnFailure showed normal scheduling events). This is a real
regression in the route-artifact track, masked until now by stale CI. → own track.

## Phase 3: Follow-up hardening (optional / future)

- [ ] Toolchain image immutability: pin the kind-runner rootfs by digest (a
      registry-image resource get used as the task `image:`) so even Dockerfile
      changes can't serve stale; resolves the insecure-registry caveat first.
- [ ] OOM: right-size task-pod memory / reduce nested concurrency once cluster
      capacity is known.
- [ ] Consider dropping the `COPY . .` source bake from Dockerfile.kind-runner
      entirely (image becomes pure toolchain) once Phase 1 is proven.
