# Implementation Plan: k8s-e2e CI reliability

## Phase 1: Decouple source from the toolchain image + auto-retry

- [x] Add a `repo` git resource to `deploy/k8s-e2e-pipeline.yml` (line 15;
      uri https://github.com/tdmtrader/jetbridge.git, branch jetbridge).
- [x] `build-kind-runner`: `get: repo` (line 28) so the source version is an input.
- [x] `k8s-integration-tests`: `get: repo` `passed: [build-kind-runner]`
      (lines 150-151); `inputs: [repo]` (line 159); `cd repo` (line 169);
      `attempts: 2` (line 155).
- [x] `k8s-behavioral-tests`: `get: repo` `passed: [k8s-integration-tests]`
      (lines 243-244); `inputs: [repo]` (line 252); `cd repo` (line 262);
      `attempts: 2` (line 248).
- [x] `fly set-pipeline` applied + validated: integration #181 built from `cd repo`
      (path `/tmp/build/.../repo/...`), not baked `/src`, with no tag bump.

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

## Phase 3: Follow-up hardening (DEFERRED — out of scope per spec)

Explicitly out of scope for this track (spec "Out of Scope": full immutable-tag/
digest pinning is future hardening; OOM tuning needs cluster-sizing data). The
day-to-day staleness is already solved by the Phase 1 source-decoupling. These
remain as documented future work — split to a dedicated hardening track if/when
the toolchain image or cluster capacity becomes the bottleneck.

- [~] Toolchain image immutability: pin the kind-runner rootfs by digest (a
      registry-image resource get used as the task `image:`) so even Dockerfile
      changes can't serve stale; resolves the insecure-registry caveat first.
      NOTE: needs Dockerfile.kind-runner rework — its build-time validation steps
      (`go build ./cmd/concourse`, `go test -c ./topgun/...`) still consume the
      baked `/src`; dropping `COPY . .` requires removing those first.
- [~] OOM: right-size task-pod memory / reduce nested concurrency once cluster
      capacity is known. (`attempts: 2` already absorbs transient retries; this is
      for the early-startup OOM-kills that `attempts` cannot recover.)
- [~] Consider dropping the `COPY . .` source bake from Dockerfile.kind-runner
      entirely (image becomes pure toolchain) — coupled with the digest-pin item.
