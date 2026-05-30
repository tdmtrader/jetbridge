# Implementation Plan: Remove implicit registry-image skip download

## Phase 1: Remove implicit skip and fetch_artifact

- [x] Write tests: invert `fetch_artifact` tests to `skip_download` tests in `atc/exec/get_step_test.go` 3845751501
  - Existing `fetch_artifact` context (line 756) → replace with tests verifying registry-image gets perform the physical download by default
  - Existing `skip_download` context (line 856) → verify skip behavior is preserved for explicit `skip_download: true`
  - Add test: registry-image get WITHOUT `skip_download` creates a container and fetches
  - Add test: registry-image get WITH `skip_download: true` skips download, registers image ref, registers nil volume
- [x] Implement: remove `isRegistryImage` and `fetch_artifact` from `atc/exec/get_step.go` 3845751501
  - Remove `isRegistryImage` variable (line 188)
  - Remove `fetchArtifact` variable and check (line 187)
  - Simplify condition to `if step.plan.SkipDownload {` (line 189)
  - Remove `delete(params, "fetch_artifact")` (line 244)
- [x] Phase 1 Manual Verification: run `ginkgo ./atc/exec/` and confirm all tests pass 3845751501

---

## Phase 2: Set SkipDownload on image get plans

- [x] Write tests: add planner test for image get plans having `SkipDownload: true` in `atc/builds/planner_test.go` 9936620c86
  - Verify that `FetchImagePlan` sets `SkipDownload: true` on the image get plan when the image type is `registry-image`
- [x] Implement: set `SkipDownload: true` in `FetchImagePlan` in `atc/config.go` (line ~366) 9936620c86
  - Only set when `image.Type == "registry-image"` to avoid breaking non-registry custom types
- [x] Phase 2 Manual Verification: run `ginkgo ./atc/builds/` and confirm planner tests pass 9936620c86

---

## Phase 3: Clean up references and integration tests

- [x] Update integration test `topgun/k8s/integration/skip_image_get_test.go` — remove `fetch_artifact` references, update to use `skip_download` semantics b9f31156d1
- [x] Search for and remove any remaining `fetch_artifact` references in production code and tests b9f31156d1
- [x] Run full unit test suite: `make test-unit` b9f31156d1
- [x] Phase 3 Manual Verification: verify no `fetch_artifact` references remain in production code (`grep -r fetch_artifact --include='*.go'` returns only test/doc files if any) b9f31156d1

---
