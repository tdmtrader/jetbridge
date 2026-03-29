# Implementation Plan: Fix k8s-e2e Pipeline Kind-Runner Build and Test Execution

## Phase 1: Diagnose and fix kind-runner image build

Debug the Docker build process to understand why source files are missing or incomplete.

- [~] Reproduce the build locally: clone repo, create the Dockerfile (without YAML heredoc indentation), build the image, verify `/src/topgun/` contents
- [ ] Fix the Dockerfile heredoc indentation issue in the `build-kind-runner` pipeline job — strip leading whitespace or use a different approach
- [ ] Verify `go test -c ./topgun/k8s/integration/` compiles inside the rebuilt kind-runner image
- [ ] Verify `ginkgo --dry-run ./topgun/k8s_behavioral/` finds the test suite inside the image
- [ ] Update the pipeline and trigger `build-kind-runner` to rebuild the image

---

## Phase 2: Fix integration tests

- [ ] Trigger `k8s-integration-tests` and verify it creates a KinD cluster successfully
- [ ] Verify Concourse deploys via Helm and the topgun integration suite runs
- [ ] Fix any test failures related to the DaemonSet artifact backend (tests may need artifact daemon config)
- [ ] Phase 2 verification: `k8s-integration-tests` passes

---

## Phase 3: Fix behavioral tests

- [ ] Trigger `k8s-behavioral-tests` and verify ginkgo finds and starts the suite
- [ ] Monitor the 2-3 hour test run for failures beyond the known ~3 flaky specs
- [ ] Fix any systematic failures (artifact passing, resource caching, pod lifecycle)
- [ ] Phase 3 verification: behavioral tests complete with ≤3 flaky failures

---
