# CGX: K8s E2E CI Failures

**Track:** `k8s_e2e_ci_failures_20260407`

## Session Log

- 2026-04-07: Created track after investigating CI. Integration build #109 has 1 flaky test (check exec race). Behavioral builds #37-#40 all die at ~50s during docker build (pod killed mid-containerd-shim).
