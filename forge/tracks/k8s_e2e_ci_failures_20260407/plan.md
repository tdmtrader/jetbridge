# Implementation Plan: K8s E2E CI Failures

## Phase 1: Diagnose Behavioral Test Pod Termination

- [ ] Task: Check pod resource limits — inspect Helm values and worker pod spec for memory/ephemeral-storage limits on k8s-cicd worker
- [ ] Task: Check kubelet events — look for OOMKilled or Evicted events on the k8s-cicd node for recent behavioral test pods
- [ ] Task: Reduce Docker build context — the `docker build` sends ~315MB (full repo). Add `.dockerignore` or switch to `COPY --from` with only the two binaries
- [ ] Task: Fix the root cause — apply resource increase, context reduction, or restructure the build step based on diagnosis

## Phase 2: Fix Integration Test Check Exec Race

- [ ] Task: Analyze the exec path — trace how `check-resource` output is read in `topgun/exec.go:81` via `Wait()` and how the K8s runtime handles exec on completed pods
- [ ] Task: Fix the race — ensure check output can be read even when the container has already completed (e.g., fall back to pod logs, or buffer output before pod cleanup)
- [ ] Task: Verify fix — re-run the parallel gets test locally or trigger CI build

## Phase 3: Cleanup

- [ ] Task: Update `FAILURES.md` — run the full behavioral suite (or collect latest CI results) and document actual current failures vs. the stale Feb 15 list
- [ ] Task: Trigger CI builds and confirm both jobs are green
