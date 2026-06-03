# CGX: k8s-e2e CI reliability

## Context (2026-05-30)
Spun off from `resource_config_scope_fk_leak_fix_20260530`, where we discovered
the k8s-e2e pipeline tests stale code: `Dockerfile.kind-runner` bakes the source
(`COPY . .` → /src), the image is referenced by a mutable tag (v35), and the
worker serves that tag from cache — so build-kind-runner's fresh push is ignored.
Only a tag bump (v33→v34→v35) forced fresh code. Also: the integration job errored
~3/5 runs from OOM (exit 137 SIGKILL) in the KinD-in-DinD task pods.

## Approach
Decouple volatile source from the stable toolchain image: test jobs build from a
fresh `repo` git resource (`cd repo`) instead of baked `/src`. Add `attempts: 2`
to the heavy tasks for transient-OOM auto-retry. Minimal, pipeline-yaml-only,
avoids the insecure-registry uncertainty of digest-pinning.

### anti-pattern (already logged in the FK track)
- Mutable image tags for CI rootfs + baked source = silently testing stale code.

### key references
- deploy/k8s-e2e-pipeline.yml (resources + 3 jobs)
- deploy/Dockerfile.kind-runner (COPY . . bakes source)
- repo resource config (from live jetbridge pipeline): uri
  https://github.com/tdmtrader/jetbridge.git, branch jetbridge
