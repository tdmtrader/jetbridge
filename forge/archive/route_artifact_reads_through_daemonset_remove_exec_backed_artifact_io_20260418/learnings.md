# Learnings

### 2026-04-18 [missing-capability]

[2026-04-18] Local verification of `topgun/k8s/integration` specs is blocked on Colima-based macOS dev machines — testcontainers-go cannot start K3s inside Colima (namespace errors). Phase 1 Red verification has to run on a Linux/Docker Desktop host or in CI. A shorter developer loop here would save multi-minute iteration time. Candidate: a `make test-k8s-integration-ci` that pushes the test to a remote runner, or a local stub using envtest/fake clientset for fast iteration on the Ginkgo spec itself (not the end-to-end behavior). Tracked so we don't silently assume "tests pass locally" for this tier.
