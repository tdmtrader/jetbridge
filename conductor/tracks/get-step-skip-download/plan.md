# Plan: Get Step `skip_download`

## Phase 1: Data Model — Add `skip_download` Field

- [x] Write tests for `skip_download` field parsing and planner pass-through
  - Test: YAML `skip_download: true` on get step parses into `GetStep.SkipDownload`
  - Test: planner copies `SkipDownload` from `GetStep` to `GetPlan`
  - Test: plan JSON round-trip preserves `skip_download: true`
  - Test: omitted `skip_download` defaults to false (existing behavior)

- [x] Implement `skip_download` field on `GetStep` and `GetPlan`
  - Add `SkipDownload bool` to `GetStep` in `atc/steps.go`
  - Add `SkipDownload bool` to `GetPlan` in `atc/plan.go`
  - Copy field in `VisitGet` in `atc/builds/planner.go`

## Phase 2: Get Step Execution — Honor `skip_download`

- [x] Write tests for `skip_download` execution behavior
  - Test: `skip_download: true` resolves version but does not create container
  - Test: `skip_download: true` registers nil artifact + image ref URL
  - Test: `skip_download: true` updates resource version metadata
  - Test: `skip_download: false` (default) preserves existing full-download behavior

- [x] Update `get_step.go` to honor `SkipDownload`
  - When `step.plan.SkipDownload` is true, take the existing skip path
    (resolve version, create resource cache, register image ref, no container)
  - Keep the existing implicit registry-image auto-skip for backwards compat
  - `skip_download` works on any runtime, not just K8s

## Phase 3: Config Validation

- [x] Write tests for `skip_download` validation
  - Test: `skip_download: true` on `registry-image` type passes validation
  - Test: `skip_download: true` on a type with `produces: registry-image` passes
  - Test: `skip_download: true` on a non-image resource type produces error
  - Test: `skip_download: false` (or omitted) on any type passes validation

- [x] Implement `skip_download` validation in `VisitGet` f94e30ef8
  - In `atc/step_validator.go`, validate that `skip_download: true` is only
    set on resources whose type is `registry-image` or has `produces: registry-image`
  - Requires looking up the resource's type in the config

## Phase 4: Live Verification

- [ ] Deploy and verify with real pipeline
  - Pipeline with `get: my-image, skip_download: true` + `task: build, image: my-image`
  - Verify task runs with the resolved image version
  - Verify checks still run and version tracking works
  - Verify `fly get-pipeline` round-trips correctly
